//go:build integration

package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// execSmokeScript runs scripts/smoke-recipes/<name> via bash, capturing
// stdout and stderr separately. The script must be executable. extraEnv
// is appended to os.Environ() so callers can pin recipe-specific envvars
// (DIFFAH_BIN, WORK_DIR, SOURCE_REGISTRY, …) without losing PATH / HOME.
func execSmokeScript(t *testing.T, repoRoot, name string, extraEnv []string) (stdout, stderr string, exit int) {
	t.Helper()
	scriptPath := filepath.Join(repoRoot, "scripts", "smoke-recipes", name)
	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(), extraEnv...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return outBuf.String(), errBuf.String(), ee.ExitCode()
		}
		t.Fatalf("run smoke script %s: %v", name, err)
	}
	return outBuf.String(), errBuf.String(), 0
}

// TestRecipeSmoke_CIDeltaRelease drives docs/recipes/ci-delta-release.md
// against a single in-process registry seeded with the v1/v2 OCI fixtures.
// Asserts that the diff command produces a non-empty delta archive and
// that re-applying it against v1 reconstructs a non-empty OCI archive.
func TestRecipeSmoke_CIDeltaRelease(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedV1V2(t, srv, root)

	// Pin REGISTRY_AUTH_FILE to a non-existent path so the host auth.json
	// (if any) doesn't leak into the smoke. --no-creds in the script also
	// disables credential discovery, but pinning is belt-and-braces and
	// matches the doctor probe integration-test pattern.
	t.Setenv("REGISTRY_AUTH_FILE", filepath.Join(t.TempDir(), "absent-auth.json"))

	work := t.TempDir()
	host := registryHost(t, srv)

	stdout, stderr, exit := execSmokeScript(t, root, "ci-delta-release.sh", []string{
		"DIFFAH_BIN=" + bin,
		"WORK_DIR=" + work,
		"SOURCE_REGISTRY=" + host,
	})
	require.Equal(t, 0, exit,
		"smoke failed (exit=%d)\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)

	deltaInfo, err := os.Stat(filepath.Join(work, "delta.tar"))
	require.NoError(t, err)
	require.Greater(t, deltaInfo.Size(), int64(0))

	restoredInfo, err := os.Stat(filepath.Join(work, "restored.tar"))
	require.NoError(t, err)
	require.Greater(t, restoredInfo.Size(), int64(0))
}
