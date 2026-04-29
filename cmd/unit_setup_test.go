//go:build !integration

package cmd_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain ensures no real ~/.diffah/config.yaml leaks into the unit test
// suite. Load() returns defaults for a non-existent path, so pointing
// $DIFFAH_CONFIG at an absent file scopes every test to built-in defaults
// regardless of what the developer has in their home directory.
func TestMain(m *testing.M) {
	var noCfgDir string
	if os.Getenv("DIFFAH_CONFIG") == "" {
		dir, _ := os.MkdirTemp("", "diffah-unit-no-config-")
		os.Setenv("DIFFAH_CONFIG", filepath.Join(dir, "absent.yaml"))
		noCfgDir = dir
	}
	code := m.Run()
	if noCfgDir != "" {
		_ = os.RemoveAll(noCfgDir)
	}
	os.Exit(code)
}
