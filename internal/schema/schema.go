package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Version struct {
	IsWIP   bool
	DirName string
	Number  int64
}

func GetAllSchemaDirs(baseDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(baseDir, "schemas"))
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
	sort.Strings(dirs)
	return dirs, nil
}

func GetLatestVersion(baseDir string, filterWIP bool) (*Version, error) {
	dirs, err := GetAllSchemaDirs(baseDir)
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
		return &Version{IsWIP: true, DirName: "wip"}, nil
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
	num, err := strconv.ParseInt(strings.TrimPrefix(latest, "v"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse version %s: %w", latest, err)
	}

	return &Version{DirName: latest, Number: num}, nil
}

func SchemaFilePath(baseDir, dirName string) string {
	return filepath.Join(baseDir, "schemas", dirName, "schema.fga")
}
