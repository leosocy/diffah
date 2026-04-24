//go:build integration

package cmd_test

import (
	"archive/tar"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/stretchr/testify/require"
)

// mustProduceSignedDelta runs `diffah diff --sign-key <fixture>` on the
// standard v1/v2 OCI fixtures and returns the resulting delta path.
// The archive's .sig neighbor is asserted to exist before return so a
// subsequent tamper/verify test always has a signed baseline to work
// against.
func mustProduceSignedDelta(t *testing.T, root, bin, outDir string) string {
	t.Helper()
	deltaPath := filepath.Join(outDir, "signed.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		"--sign-key", filepath.Join(root, "pkg/signer/testdata/test_ec_p256.key"),
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		deltaPath,
	)
	require.Equal(t, 0, exit, "diff --sign-key failed: %s", stderr)
	_, err := os.Stat(deltaPath + ".sig")
	require.NoError(t, err, "signed delta has no .sig sidecar")
	return deltaPath
}

// mustProduceUnsignedDelta runs `diffah diff` (no --sign-key) on the
// standard v1/v2 OCI fixtures and asserts no .sig sidecar is emitted.
func mustProduceUnsignedDelta(t *testing.T, root, bin, outDir string) string {
	t.Helper()
	deltaPath := filepath.Join(outDir, "unsigned.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		deltaPath,
	)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)
	_, err := os.Stat(deltaPath + ".sig")
	require.True(t, errors.Is(err, os.ErrNotExist),
		"unsigned delta should have no .sig (stat err=%v)", err)
	return deltaPath
}

// writeWrongPubKey generates a throwaway ECDSA P-256 public key and
// writes it (PKIX PEM) to path. Used for the key-mismatch verify test
// — the generated key is deliberately unrelated to the fixture
// private key under pkg/signer/testdata/.
func writeWrongPubKey(t *testing.T, path string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	require.NoError(t, os.WriteFile(path, pemBytes, 0o644))
}

// tamperSidecarByteInArchive rewrites archivePath's diffah.json tar
// entry with one hex-digit flipped inside the body. The rest of the
// tar (blob entries, file mode, entry order) is preserved verbatim.
// The XOR-with-0x01 trick preserves the hex-digit class (0↔1, 2↔3,
// …, e↔f), so the mutated JSON still parses as valid — guaranteeing
// the test exercises the verify hook rather than an early parser
// failure.
func tamperSidecarByteInArchive(t *testing.T, archivePath string) {
	t.Helper()
	raw, err := os.ReadFile(archivePath)
	require.NoError(t, err)

	var out bytes.Buffer
	tr := tar.NewReader(bytes.NewReader(raw))
	tw := tar.NewWriter(&out)
	mutated := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		if hdr.Name == diff.SidecarFilename {
			require.Greater(t, len(body), 0, "sidecar entry is empty")
			idx := findHexDigit(body)
			require.GreaterOrEqual(t, idx, 0, "no hex digit found in sidecar body")
			body[idx] ^= 0x01
			mutated = true
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err = tw.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.True(t, mutated, "archive %s had no %s entry", archivePath, diff.SidecarFilename)
	require.NoError(t, os.WriteFile(archivePath, out.Bytes(), 0o644))
}

// findHexDigit returns the index of the first hex digit in body, or
// -1 if none. Used by tamperSidecarByteInArchive to pick a byte whose
// XOR-with-0x01 neighbor is also a hex digit, keeping the sidecar
// JSON structurally valid after the flip.
func findHexDigit(body []byte) int {
	for i, b := range body {
		if (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') {
			return i
		}
	}
	return -1
}

// tamperSigFileByte flips one byte of the .sig neighbor so the base64
// decode still succeeds (the byte is xor'd inside the file, not at the
// base64 layer) but the decoded signature no longer matches.
func tamperSigFileByte(t *testing.T, sigPath string) {
	t.Helper()
	data, err := os.ReadFile(sigPath)
	require.NoError(t, err)
	require.Greater(t, len(data), 0, ".sig file is empty")
	data[0] ^= 0x01
	require.NoError(t, os.WriteFile(sigPath, data, 0o644))
}

// TestVerifyCLI_Matrix walks every cell of the verify matrix from spec
// §3.3. Each subtest produces a fresh delta archive under a unique
// TempDir so the outer TestMain can clean up after the `go test` run.
func TestVerifyCLI_Matrix(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	correctPub := filepath.Join(root, "pkg/signer/testdata/test_ec_p256.pub")

	applyDelta := func(t *testing.T, deltaPath string, extraArgs ...string) (string, int) {
		t.Helper()
		out := filepath.Join(t.TempDir(), "restored.tar")
		args := []string{
			"apply",
			deltaPath,
			"oci-archive:" + filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"oci-archive:" + out,
		}
		args = append(args, extraArgs...)
		_, stderr, exit := runDiffahBin(t, bin, args...)
		return stderr, exit
	}

	t.Run("signed+correctKey_exit0", func(t *testing.T) {
		tmp := t.TempDir()
		delta := mustProduceSignedDelta(t, root, bin, tmp)
		stderr, exit := applyDelta(t, delta, "--verify", correctPub)
		require.Equal(t, 0, exit, "expected exit 0 with correct key; stderr=%s", stderr)
	})

	t.Run("signed+wrongKey_exit4", func(t *testing.T) {
		tmp := t.TempDir()
		delta := mustProduceSignedDelta(t, root, bin, tmp)
		wrongPub := filepath.Join(tmp, "wrong.pub")
		writeWrongPubKey(t, wrongPub)
		stderr, exit := applyDelta(t, delta, "--verify", wrongPub)
		require.Equal(t, 4, exit, "expected exit 4 with wrong key; stderr=%s", stderr)
		require.Contains(t, strings.ToLower(stderr), "signature",
			"expected signature error in stderr: %s", stderr)
	})

	t.Run("signed+noVerify_exit0", func(t *testing.T) {
		tmp := t.TempDir()
		delta := mustProduceSignedDelta(t, root, bin, tmp)
		stderr, exit := applyDelta(t, delta)
		require.Equal(t, 0, exit, "expected exit 0 with no --verify on signed; stderr=%s", stderr)
	})

	t.Run("unsigned+verify_exit4", func(t *testing.T) {
		tmp := t.TempDir()
		delta := mustProduceUnsignedDelta(t, root, bin, tmp)
		stderr, exit := applyDelta(t, delta, "--verify", correctPub)
		require.Equal(t, 4, exit, "expected exit 4 when --verify on unsigned; stderr=%s", stderr)
		lower := strings.ToLower(stderr)
		require.True(t,
			strings.Contains(lower, "no signature") || strings.Contains(lower, "unsigned"),
			"expected 'no signature' mention in stderr: %s", stderr)
	})

	t.Run("unsigned+noVerify_exit0", func(t *testing.T) {
		tmp := t.TempDir()
		delta := mustProduceUnsignedDelta(t, root, bin, tmp)
		stderr, exit := applyDelta(t, delta)
		require.Equal(t, 0, exit, "expected exit 0 on unsigned with no --verify; stderr=%s", stderr)
	})

	t.Run("signed+tamperedSidecar_exit4", func(t *testing.T) {
		tmp := t.TempDir()
		delta := mustProduceSignedDelta(t, root, bin, tmp)
		tamperSidecarByteInArchive(t, delta)
		stderr, exit := applyDelta(t, delta, "--verify", correctPub)
		require.Equal(t, 4, exit, "expected exit 4 for tampered sidecar; stderr=%s", stderr)
	})

	t.Run("signed+tamperedSigFile_exit4", func(t *testing.T) {
		tmp := t.TempDir()
		delta := mustProduceSignedDelta(t, root, bin, tmp)
		tamperSigFileByte(t, delta+".sig")
		stderr, exit := applyDelta(t, delta, "--verify", correctPub)
		require.Equal(t, 4, exit, "expected exit 4 for tampered .sig; stderr=%s", stderr)
	})

	t.Run("cosignReserved_exit2", func(t *testing.T) {
		tmp := t.TempDir()
		delta := mustProduceSignedDelta(t, root, bin, tmp)
		stderr, exit := applyDelta(t, delta, "--verify", "cosign://kms.example/key")
		require.Equal(t, 2, exit,
			"expected exit 2 for reserved cosign:// URI; stderr=%s", stderr)
		require.Contains(t, strings.ToLower(stderr), "reserved",
			"expected 'reserved' mention in stderr: %s", stderr)
	})
}
