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

// TestRecipeSmoke_AirgapDelivery drives docs/recipes/airgap-delivery.md
// entirely against on-disk OCI archive fixtures — no registry. Exercises
// producer diff, sneakernet cp, and customer apply, then asserts the
// reconstructed archive is non-empty.
func TestRecipeSmoke_AirgapDelivery(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	work := t.TempDir()

	stdout, stderr, exit := execSmokeScript(t, root, "airgap-delivery.sh", []string{
		"DIFFAH_BIN=" + bin,
		"WORK_DIR=" + work,
		"BASELINE_OCI_TAR=" + filepath.Join(root, "testdata", "fixtures", "v1_oci.tar"),
		"TARGET_OCI_TAR=" + filepath.Join(root, "testdata", "fixtures", "v2_oci.tar"),
	})
	require.Equal(t, 0, exit,
		"smoke failed (exit=%d)\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)

	deltaInfo, err := os.Stat(filepath.Join(work, "delta.tar"))
	require.NoError(t, err)
	require.Greater(t, deltaInfo.Size(), int64(0))

	restoredInfo, err := os.Stat(filepath.Join(work, "customer", "restored.tar"))
	require.NoError(t, err)
	require.Greater(t, restoredInfo.Size(), int64(0))
}

// TestRecipeSmoke_OfflineVerify drives docs/recipes/offline-verify.md
// using the project's static EC P256 test key pair. Exercises the
// happy-path sign + verify, then re-runs apply against a tampered
// archive and asserts a non-zero exit.
func TestRecipeSmoke_OfflineVerify(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	work := t.TempDir()

	stdout, stderr, exit := execSmokeScript(t, root, "offline-verify.sh", []string{
		"DIFFAH_BIN=" + bin,
		"WORK_DIR=" + work,
		"BASELINE_OCI_TAR=" + filepath.Join(root, "testdata", "fixtures", "v1_oci.tar"),
		"TARGET_OCI_TAR=" + filepath.Join(root, "testdata", "fixtures", "v2_oci.tar"),
		"SIGN_KEY_PEM=" + filepath.Join(root, "pkg", "signer", "testdata", "test_ec_p256.key"),
		"VERIFY_KEY_PEM=" + filepath.Join(root, "pkg", "signer", "testdata", "test_ec_p256.pub"),
	})
	require.Equal(t, 0, exit,
		"smoke failed (exit=%d)\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)

	restoredInfo, err := os.Stat(filepath.Join(work, "restored.tar"))
	require.NoError(t, err)
	require.Greater(t, restoredInfo.Size(), int64(0))

	// The tampered apply must NOT have produced an output archive.
	_, err = os.Stat(filepath.Join(work, "tampered-restored.tar"))
	require.True(t, os.IsNotExist(err) || sizeOrZero(t, filepath.Join(work, "tampered-restored.tar")) == 0,
		"tampered apply should not have produced a non-empty restored archive")
}

// sizeOrZero returns the file size at path, or 0 if the file is absent.
func sizeOrZero(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
