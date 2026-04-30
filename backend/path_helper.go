package main

import (
	"os"
	"path/filepath"
)

func getAppRoot() string {
	if root := os.Getenv("PROXYGW_HOME"); root != "" {
		return root
	}
	// Safety: if running under 'go test' but PROXYGW_HOME is not set, 
	// panic to prevent overwriting production files.
	for _, arg := range os.Args {
		if arg == "-test.v" || arg == "-test.run" || filepath.Base(os.Args[0]) == "backend.test" {
			panic("FATAL: PROXYGW_HOME must be set when running tests to avoid production data corruption")
		}
	}
	return "/root/proxygw"
}

func getPath(elem ...string) string {
	paths := append([]string{getAppRoot()}, elem...)
	return filepath.Join(paths...)
}
