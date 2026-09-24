package main

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	runningUnderGoTestOnce sync.Once
	runningUnderGoTest     bool
)

// isRunningUnderGoTest is evaluated once: os.Args never changes after start,
// and getPath is called from hot loops.
func isRunningUnderGoTest() bool {
	runningUnderGoTestOnce.Do(func() {
		if filepath.Base(os.Args[0]) == "backend.test" {
			runningUnderGoTest = true
			return
		}
		for _, arg := range os.Args {
			if arg == "-test.v" || arg == "-test.run" {
				runningUnderGoTest = true
				return
			}
		}
	})
	return runningUnderGoTest
}

// getAppRoot returns the install root. PROXYGW_HOME is read on every call on
// purpose: tests point it at a temp dir with t.Setenv.
func getAppRoot() string {
	if root := os.Getenv("PROXYGW_HOME"); root != "" {
		return root
	}
	// Safety: if running under 'go test' but PROXYGW_HOME is not set,
	// panic to prevent overwriting production files.
	if isRunningUnderGoTest() {
		panic("FATAL: PROXYGW_HOME must be set when running tests to avoid production data corruption")
	}
	return "/root/proxygw"
}

func getPath(elem ...string) string {
	paths := append([]string{getAppRoot()}, elem...)
	return filepath.Join(paths...)
}
