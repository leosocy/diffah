//go:build integration

package cmd_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDIFFAH_CONFIG_MemoryBudget_DrivesSpoolDefault asserts that a config
// file setting memory-budget to an invalid value produces a user-visible
// error, and that an explicit --memory-budget flag overrides the config value.
//
// Observable proof mirrors TestDIFFAH_CONFIG_DrivesDryRunDefaults: the config
// value reaches the CLI flag via ApplyTo, and the encoding-opts builder
// validates it. An invalid value forces a deterministic, observable failure.
func TestDIFFAH_CONFIG_MemoryBudget_DrivesSpoolDefault(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	// "not-a-number" is an invalid memory-budget value; parseMemoryBudget
	// rejects it with a user-visible error, proving ApplyTo wired the
	// config value through to the command's flag set.
	require.NoError(t, os.WriteFile(cfgPath, []byte("memory-budget: not-a-number\n"), 0o644))

	t.Setenv("DIFFAH_CONFIG", cfgPath)

	v1 := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	v2 := filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	out := filepath.Join(tmp, "delta.tar")

	// First invocation: config sets memory-budget=not-a-number → should fail.
	_, stderr, exit := runDiffahBin(t, bin,
		"diff", "--dry-run",
		"oci-archive:"+v1,
		"oci-archive:"+v2,
		out,
	)
	require.Equalf(t, 2, exit,
		"expected exit 2 (user error) when config sets memory-budget=not-a-number; stderr: %s", stderr)
	require.Containsf(t, stderr, "memory-budget",
		"expected 'memory-budget' in error message; stderr: %s", stderr)

	// Second invocation: explicit --memory-budget=8GiB overrides the config.
	// Changed==true for the memory-budget flag, so ApplyTo leaves it alone.
	_, stderr2, exit2 := runDiffahBin(t, bin,
		"diff", "--dry-run", "--memory-budget=8GiB",
		"oci-archive:"+v1,
		"oci-archive:"+v2,
		out,
	)
	require.Equalf(t, 0, exit2,
		"expected zero exit when --memory-budget=8GiB overrides config; stderr: %s", stderr2)
}

// TestDIFFAH_CONFIG_DrivesDryRunDefaults asserts that a config file at
// $DIFFAH_CONFIG sets defaults that affect `diffah diff --dry-run`
// behavior, and that an explicit CLI flag overrides the config value.
//
// Observable proof: config sets workers=0 (below the --workers>=1 limit),
// which causes the encoding-opts builder to fail with a user-visible error.
// A second invocation with --workers=1 (explicit) succeeds, proving that
// CLI flags win over config values.
func TestDIFFAH_CONFIG_DrivesDryRunDefaults(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	// workers=0 is below the --workers>=1 validation limit; it is a valid
	// YAML integer that Load accepts but the encoding-opts builder rejects.
	// This gives us a deterministic, directly observable proof that ApplyTo
	// wired the config value through to the command's flag set.
	require.NoError(t, os.WriteFile(cfgPath, []byte("workers: 0\n"), 0o644))

	t.Setenv("DIFFAH_CONFIG", cfgPath)

	v1 := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	v2 := filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	out := filepath.Join(tmp, "delta.tar")

	// First invocation: config sets workers=0 → should fail with a
	// user-visible error mentioning "workers".
	_, stderr, exit := runDiffahBin(t, bin,
		"diff", "--dry-run",
		"oci-archive:"+v1,
		"oci-archive:"+v2,
		out,
	)
	// workers=0 fails --workers>=1 validation → CategoryUser → exit 2
	require.Equalf(t, 2, exit,
		"expected exit 2 (user error) when config sets workers=0; stderr: %s", stderr)
	require.Containsf(t, stderr, "workers",
		"expected 'workers' in error message; stderr: %s", stderr)

	// Second invocation: explicit --workers=1 overrides the config value.
	// Changed==true for the workers flag, so ApplyTo leaves it alone.
	_, stderr2, exit2 := runDiffahBin(t, bin,
		"diff", "--dry-run", "--workers=1",
		"oci-archive:"+v1,
		"oci-archive:"+v2,
		out,
	)
	require.Equalf(t, 0, exit2,
		"expected zero exit when --workers=1 overrides config workers=0; stderr: %s", stderr2)
}
