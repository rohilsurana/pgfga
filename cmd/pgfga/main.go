package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rohilsurana/pgfga"
	"github.com/rohilsurana/pgfga/parser"
	"github.com/rohilsurana/pgfga/transform"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "validate":
		err = cmdValidate()
	case "new":
		err = cmdNew()
	case "finalize":
		err = cmdFinalize()
	case "migrate":
		err = cmdMigrate()
	default:
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `pgfga - pure-Postgres fine-grained authorization

Usage:
  pgfga validate              Validate all .fga schemas
  pgfga new                   Create a new WIP schema from the latest version
  pgfga finalize              Promote WIP schema to a versioned schema
  pgfga migrate [--dsn=...] [--schema=...]
                              Migrate the database to the latest schema version

Environment:
  PGFGA_DSN          Postgres connection string (for migrate)
  PGFGA_SCHEMA       Postgres schema for pgfga objects (default: public)
  PGFGA_LOCAL=true   Enable local mode (includes WIP schemas in migrate)
`)
}

// Schema directory helpers

func getAllSchemaDirs() ([]string, error) {
	entries, err := os.ReadDir("schemas")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.Contains(e.Name(), "README") {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs, nil
}

type schemaVersion struct {
	isWIP   bool
	dirName string
	number  int64
}

func getLatestVersion(filterWIP bool) (*schemaVersion, error) {
	dirs, err := getAllSchemaDirs()
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, nil
	}

	hasWIP := false
	for _, d := range dirs {
		if d == "wip" {
			hasWIP = true
			break
		}
	}
	if hasWIP && !filterWIP {
		return &schemaVersion{isWIP: true, dirName: "wip"}, nil
	}

	var versioned []string
	for _, d := range dirs {
		if d != "wip" {
			versioned = append(versioned, d)
		}
	}
	if len(versioned) == 0 {
		return nil, nil
	}

	latest := versioned[len(versioned)-1]
	var num int64
	fmt.Sscanf(strings.TrimPrefix(latest, "v"), "%d", &num)
	return &schemaVersion{dirName: latest, number: num}, nil
}

func schemaFilePath(dirName string) string {
	return filepath.Join("schemas", dirName, "schema.fga")
}

// Commands

func cmdValidate() error {
	dirs, err := getAllSchemaDirs()
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return fmt.Errorf("no schemas found")
	}

	allValid := true
	for _, dir := range dirs {
		path := schemaFilePath(dir)
		if err := parser.ValidateFile(path); err != nil {
			fmt.Fprintf(os.Stderr, "INVALID %s: %v\n", path, err)
			allValid = false
		} else {
			fmt.Printf("OK %s\n", path)
		}
	}
	if !allValid {
		return fmt.Errorf("some schemas are invalid")
	}
	fmt.Println("all schemas are valid")
	return nil
}

func cmdNew() error {
	latest, err := getLatestVersion(false)
	if err != nil {
		return err
	}
	if latest == nil {
		return fmt.Errorf("no existing schemas found")
	}
	if latest.isWIP {
		return fmt.Errorf("WIP schema already exists, finalize it first")
	}

	srcDir := filepath.Join("schemas", latest.dirName)
	dstDir := filepath.Join("schemas", "wip")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dstDir, e.Name()), data, 0o644); err != nil {
			return err
		}
	}

	fmt.Printf("created WIP schema from %s\n", latest.dirName)
	return nil
}

func cmdFinalize() error {
	latest, err := getLatestVersion(false)
	if err != nil {
		return err
	}
	if latest == nil {
		return fmt.Errorf("no schemas found")
	}
	if !latest.isWIP {
		return fmt.Errorf("no WIP schema to finalize")
	}

	current, err := getLatestVersion(true)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("no finalized schema found")
	}

	newVersion := fmt.Sprintf("v%03d", current.number+1)
	srcDir := filepath.Join("schemas", "wip")
	dstDir := filepath.Join("schemas", newVersion)

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dstDir, e.Name()), data, 0o644); err != nil {
			return err
		}
	}

	if err := os.RemoveAll(srcDir); err != nil {
		return err
	}

	fmt.Printf("finalized schema as %s\n", newVersion)
	return nil
}

func cmdMigrate() error {
	dsn := os.Getenv("PGFGA_DSN")
	pgSchema := os.Getenv("PGFGA_SCHEMA")
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--dsn=") {
			dsn = strings.TrimPrefix(arg, "--dsn=")
		}
		if strings.HasPrefix(arg, "--schema=") {
			pgSchema = strings.TrimPrefix(arg, "--schema=")
		}
	}
	if dsn == "" {
		return fmt.Errorf("PGFGA_DSN or --dsn required")
	}

	var opts []pgfga.Option
	if pgSchema != "" {
		opts = append(opts, pgfga.WithSchema(pgSchema))
	}

	isLocal := os.Getenv("PGFGA_LOCAL") == "true"
	ctx := context.Background()

	client, err := pgfga.Connect(dsn, opts...)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Migrate(ctx); err != nil {
		return err
	}

	if isLocal {
		return localMigrate(ctx, client)
	}
	return nonLocalMigrate(ctx, client)
}

func localMigrate(ctx context.Context, client *pgfga.Client) error {
	latest, err := getLatestVersion(false)
	if err != nil {
		return err
	}
	if latest == nil {
		return fmt.Errorf("no schema versions found")
	}

	var version int64
	var dirName string

	if latest.isWIP {
		current, err := getLatestVersion(true)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("no finalized schema found")
		}
		version = current.number + 1
		dirName = "wip"
	} else {
		version = latest.number
		dirName = latest.dirName
	}

	path := schemaFilePath(dirName)
	if err := client.LoadModelFile(ctx, version, path); err != nil {
		return err
	}

	fmt.Printf("migrated schema version %d (%s)\n", version, dirName)
	return nil
}

func nonLocalMigrate(ctx context.Context, client *pgfga.Client) error {
	latest, err := getLatestVersion(true)
	if err != nil {
		return err
	}
	if latest == nil {
		return fmt.Errorf("no finalized schema found")
	}

	dbVersion, err := client.GetLatestSchemaVersion(ctx)
	if err != nil {
		return err
	}
	if dbVersion >= latest.number {
		fmt.Printf("database already at version %d, no migration needed\n", dbVersion)
		return nil
	}

	path := schemaFilePath(latest.dirName)
	model, err := parser.ParseFile(path)
	if err != nil {
		return err
	}
	rows, err := transform.GenerateAuthzModel(latest.number, model)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("no model rows generated")
	}

	if err := client.LoadModelFile(ctx, latest.number, path); err != nil {
		return err
	}

	fmt.Printf("migrated schema version %d\n", latest.number)
	return nil
}
