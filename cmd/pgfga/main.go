package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rohilsurana/pgfga/internal/db"
	"github.com/rohilsurana/pgfga/internal/parser"
	"github.com/rohilsurana/pgfga/internal/schema"
	"github.com/rohilsurana/pgfga/internal/transform"
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
  pgfga migrate [--dsn=...]   Migrate the database to the latest schema version

Environment:
  PGFGA_DSN          Postgres connection string (for migrate)
  PGFGA_LOCAL=true   Enable local mode (includes WIP schemas in migrate)
`)
}

func baseDir() string {
	return "."
}

func cmdValidate() error {
	dirs, err := schema.GetAllSchemaDirs(baseDir())
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return fmt.Errorf("no schemas found")
	}

	allValid := true
	for _, dir := range dirs {
		path := schema.SchemaFilePath(baseDir(), dir)
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
	latest, err := schema.GetLatestVersion(baseDir(), false)
	if err != nil {
		return err
	}
	if latest == nil {
		return fmt.Errorf("no existing schemas found")
	}
	if latest.IsWIP {
		return fmt.Errorf("WIP schema already exists, finalize it first")
	}

	srcDir := filepath.Join(baseDir(), "schemas", latest.DirName)
	dstDir := filepath.Join(baseDir(), "schemas", "wip")

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

	fmt.Printf("created WIP schema from %s\n", latest.DirName)
	return nil
}

func cmdFinalize() error {
	latest, err := schema.GetLatestVersion(baseDir(), false)
	if err != nil {
		return err
	}
	if latest == nil {
		return fmt.Errorf("no schemas found")
	}
	if !latest.IsWIP {
		return fmt.Errorf("no WIP schema to finalize")
	}

	current, err := schema.GetLatestVersion(baseDir(), true)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("no finalized schema found")
	}

	newVersion := fmt.Sprintf("v%03d", current.Number+1)
	srcDir := filepath.Join(baseDir(), "schemas", "wip")
	dstDir := filepath.Join(baseDir(), "schemas", newVersion)

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
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--dsn=") {
			dsn = strings.TrimPrefix(arg, "--dsn=")
		}
	}
	if dsn == "" {
		return fmt.Errorf("PGFGA_DSN or --dsn required")
	}

	isLocal := os.Getenv("PGFGA_LOCAL") == "true"

	ctx := context.Background()
	conn, err := db.Connect(dsn)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := db.EnsureSchema(ctx, conn); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}

	if isLocal {
		return localMigrate(ctx, conn)
	}
	return nonLocalMigrate(ctx, conn)
}

func localMigrate(ctx context.Context, conn *sql.DB) error {
	latest, err := schema.GetLatestVersion(baseDir(), false)
	if err != nil {
		return err
	}
	if latest == nil {
		return fmt.Errorf("no schema versions found")
	}

	var versionToCreate int64
	var dirName string

	if latest.IsWIP {
		current, err := schema.GetLatestVersion(baseDir(), true)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("no finalized schema found")
		}
		versionToCreate = current.Number + 1
		dirName = "wip"
	} else {
		versionToCreate = latest.Number
		dirName = latest.DirName
	}

	rows, err := loadSchemaRows(versionToCreate, dirName)
	if err != nil {
		return err
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := db.DeleteSchemaVersion(ctx, tx, versionToCreate); err != nil {
		return err
	}
	if err := db.InsertAuthzModel(ctx, tx, rows); err != nil {
		return err
	}
	return tx.Commit()
}

func nonLocalMigrate(ctx context.Context, conn *sql.DB) error {
	latest, err := schema.GetLatestVersion(baseDir(), true)
	if err != nil {
		return err
	}
	if latest == nil {
		return fmt.Errorf("no finalized schema found")
	}

	dbVersion, err := db.GetLatestSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}

	if dbVersion >= latest.Number {
		fmt.Printf("database already at version %d, no migration needed\n", dbVersion)
		return nil
	}

	rows, err := loadSchemaRows(latest.Number, latest.DirName)
	if err != nil {
		return err
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := db.InsertAuthzModel(ctx, tx, rows); err != nil {
		return err
	}
	return tx.Commit()
}

func loadSchemaRows(version int64, dirName string) ([]transform.AuthzModelRow, error) {
	path := schema.SchemaFilePath(baseDir(), dirName)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("schema file not found: %s", path)
	}

	model, err := parser.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	rows, err := transform.GenerateAuthzModel(version, model)
	if err != nil {
		return nil, fmt.Errorf("transform %s: %w", path, err)
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("no authz model rows generated from %s", path)
	}

	return rows, nil
}
