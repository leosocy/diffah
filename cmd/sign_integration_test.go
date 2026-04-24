//go:build integration

package cmd_test

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runDiffahBinWithStdin mirrors runDiffahBin but wires stdin. Only the
// encrypted-key passphrase test needs this; runDiffahBin in the shared
// harness leaves Stdin unset (causing /dev/null on exec.Run).
func runDiffahBinWithStdin(t *testing.T, bin string, stdin []byte, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return outBuf.String(), errBuf.String(), ee.ExitCode()
		}
		t.Fatalf("run diffah: %v", err)
	}
	return outBuf.String(), errBuf.String(), 0
}

// signFixtureArgs returns the 3 positional args for `diffah diff` on
// the standard v1/v2 OCI fixtures, followed by the delta output path.
func signFixtureArgs(root, delta string) []string {
	return []string{
		"diff",
		"oci-archive:" + filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:" + filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		delta,
	}
}

// TestSignCLI_HappyPath_ArchiveProducesSig asserts that passing
// --sign-key PATH on `diff` yields a base64-decodable .sig sidecar next
// to the archive and the command exits 0.
func TestSignCLI_HappyPath_ArchiveProducesSig(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")

	args := append(signFixtureArgs(root, delta),
		"--sign-key", filepath.Join(root, "pkg/signer/testdata/test_ec_p256.key"),
	)
	_, stderr, exit := runDiffahBin(t, bin, args...)
	require.Equal(t, 0, exit, "diff --sign-key failed: %s", stderr)

	info, err := os.Stat(delta)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))

	sigRaw, err := os.ReadFile(delta + ".sig")
	require.NoError(t, err, "signature sidecar missing")
	trimmed := strings.TrimRight(string(sigRaw), "\n ")
	_, err = base64.StdEncoding.DecodeString(trimmed)
	require.NoError(t, err, "sidecar is not base64: %q", sigRaw)
}

// TestSignCLI_NoSignFlag_NoSidecar asserts the archive is written
// without a .sig file when --sign-key is omitted. Confirms the sign
// hook is strictly opt-in.
func TestSignCLI_NoSignFlag_NoSidecar(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin, signFixtureArgs(root, delta)...)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)

	_, err := os.Stat(delta + ".sig")
	require.True(t, os.IsNotExist(err), ".sig should not exist when --sign-key is unset (err=%v)", err)
}

// TestSignCLI_RekorDeferred asserts --rekor-url surfaces a
// "not implemented" error until the Rekor upload path lands. Any
// non-zero exit is acceptable; the message must mention rekor.
func TestSignCLI_RekorDeferred(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")

	args := append(signFixtureArgs(root, delta),
		"--sign-key", filepath.Join(root, "pkg/signer/testdata/test_ec_p256.key"),
		"--rekor-url", "https://rekor.example.com",
	)
	_, stderr, exit := runDiffahBin(t, bin, args...)
	require.NotEqual(t, 0, exit, "expected non-zero exit when --rekor-url is supplied (rekor upload deferred)")
	require.Contains(t, strings.ToLower(stderr), "rekor", "expected rekor mention in stderr: %s", stderr)
}

// TestSignCLI_DryRun_BadKey asserts a missing / unreadable key file is
// caught during dry-run and routed to CategoryUser (exit 2).
func TestSignCLI_DryRun_BadKey(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")

	args := append(signFixtureArgs(root, delta),
		"--dry-run",
		"--sign-key", "/does/not/exist/key.pem",
	)
	_, stderr, exit := runDiffahBin(t, bin, args...)
	require.Equal(t, 2, exit, "expected exit 2 for bad key path; stderr=%s", stderr)
	lower := strings.ToLower(stderr)
	require.True(t,
		strings.Contains(lower, "read key file") || strings.Contains(lower, "not found") ||
			strings.Contains(lower, "no such file"),
		"expected key-read error in stderr: %s", stderr)
}

// TestSignCLI_DryRun_GoodKey asserts a well-formed key passes the probe
// and dry-run completes without writing a .sig file (since the archive
// itself is never written in dry-run).
func TestSignCLI_DryRun_GoodKey(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")

	args := append(signFixtureArgs(root, delta),
		"--dry-run",
		"--sign-key", filepath.Join(root, "pkg/signer/testdata/test_ec_p256.key"),
	)
	_, stderr, exit := runDiffahBin(t, bin, args...)
	require.Equal(t, 0, exit, "dry-run should succeed with a good key; stderr=%s", stderr)

	_, err := os.Stat(delta + ".sig")
	require.True(t, os.IsNotExist(err), ".sig should not be written during dry-run (err=%v)", err)
	_, err = os.Stat(delta)
	require.True(t, os.IsNotExist(err), "archive should not be written during dry-run (err=%v)", err)
}

// TestSignCLI_CosignReservedSyntax asserts the reserved cosign:// URI
// scheme errors with a forward-looking "not yet implemented" message,
// keeping the flag surface stable for future KMS support.
func TestSignCLI_CosignReservedSyntax(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")

	args := append(signFixtureArgs(root, delta),
		"--sign-key", "cosign://kms.example/key",
	)
	_, stderr, exit := runDiffahBin(t, bin, args...)
	require.Equal(t, 2, exit, "expected exit 2 for reserved cosign:// URI; stderr=%s", stderr)
	require.Contains(t, strings.ToLower(stderr), "reserved",
		"expected reserved/not-implemented mention in stderr: %s", stderr)
}

// TestSignCLI_EncryptedKeyPassphraseStdin asserts the end-to-end path
// for cosign-boxed keys: passphrase arrives on stdin, the key decrypts,
// and a .sig file is produced.
func TestSignCLI_EncryptedKeyPassphraseStdin(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()
	delta := filepath.Join(tmp, "delta.tar")

	passBytes, err := os.ReadFile(filepath.Join(root, "pkg/signer/testdata/test_ec_p256_enc.pass"))
	require.NoError(t, err)
	// readOneLine in cmd/sign_flags.go accepts EOF-without-newline, but
	// a terminating newline is how real shells deliver `echo passphrase
	// | diffah ...`, so it is the more representative path to exercise.
	stdinBytes := append(append([]byte{}, passBytes...), '\n')

	args := append(signFixtureArgs(root, delta),
		"--sign-key", filepath.Join(root, "pkg/signer/testdata/test_ec_p256_enc.key"),
		"--sign-key-password-stdin",
	)
	_, stderr, exit := runDiffahBinWithStdin(t, bin, stdinBytes, args...)
	require.Equal(t, 0, exit, "diff --sign-key encrypted failed: %s", stderr)

	sigRaw, err := os.ReadFile(delta + ".sig")
	require.NoError(t, err, "signature sidecar missing")
	trimmed := strings.TrimRight(string(sigRaw), "\n ")
	_, err = base64.StdEncoding.DecodeString(trimmed)
	require.NoError(t, err, "sidecar is not base64: %q", sigRaw)
}
