//go:build integration

package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// fixtureSize is a tiny helper returning the size of a testdata fixture.
func fixtureSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}

// seedV1V2 pushes the v1/v2 OCI fixtures into the registry under
// fixtures/v1 and fixtures/v2. The optional auth is applied to both
// pushes; pass nil for anonymous registries.
func seedV1V2(t *testing.T, srv *registrytest.Server, root string) {
	t.Helper()
	seedOCIIntoRegistry(t, srv, "fixtures/v1",
		filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)
	seedOCIIntoRegistry(t, srv, "fixtures/v2",
		filepath.Join(root, "testdata/fixtures/v2_oci.tar"), nil)
}

func TestDiffCLI_AnonymousPull(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedV1V2(t, srv, root)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		registryDockerURL(t, srv, "fixtures/v1"),
		registryDockerURL(t, srv, "fixtures/v2"),
		deltaPath,
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)

	info, err := os.Stat(deltaPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))

	// Delta should be smaller than the sum of the two fixture archives —
	// it reuses the baseline's blobs rather than copying them.
	v1 := fixtureSize(t, filepath.Join(root, "testdata/fixtures/v1_oci.tar"))
	v2 := fixtureSize(t, filepath.Join(root, "testdata/fixtures/v2_oci.tar"))
	require.Less(t, info.Size(), v1+v2,
		"delta (%d) should be smaller than v1+v2 (%d)", info.Size(), v1+v2)
}

func TestDiffCLI_BasicAuth(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	seedOCIIntoRegistry(t, srv, "fixtures/v1",
		filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		basicAuth("alice", "s3cret"))
	seedOCIIntoRegistry(t, srv, "fixtures/v2",
		filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		basicAuth("alice", "s3cret"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		registryDockerURL(t, srv, "fixtures/v1"),
		registryDockerURL(t, srv, "fixtures/v2"),
		deltaPath,
		"--creds", "alice:s3cret",
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)

	info, err := os.Stat(deltaPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestDiffCLI_BearerToken(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBearerToken("abc123"))
	seedOCIIntoRegistryWithToken(t, srv, "fixtures/v1",
		filepath.Join(root, "testdata/fixtures/v1_oci.tar"), "abc123")
	seedOCIIntoRegistryWithToken(t, srv, "fixtures/v2",
		filepath.Join(root, "testdata/fixtures/v2_oci.tar"), "abc123")

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		registryDockerURL(t, srv, "fixtures/v1"),
		registryDockerURL(t, srv, "fixtures/v2"),
		deltaPath,
		"--registry-token", "abc123",
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)

	info, err := os.Stat(deltaPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestDiffCLI_TLSWithCertDir(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithTLS())
	seedV1V2(t, srv, root)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		registryDockerURL(t, srv, "fixtures/v1"),
		registryDockerURL(t, srv, "fixtures/v2"),
		deltaPath,
		"--cert-dir", srv.ClientCertDir(),
	)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)

	info, err := os.Stat(deltaPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestDiffCLI_BadCredsFails(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	seedOCIIntoRegistry(t, srv, "fixtures/v1",
		filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		basicAuth("alice", "s3cret"))
	seedOCIIntoRegistry(t, srv, "fixtures/v2",
		filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		basicAuth("alice", "s3cret"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		registryDockerURL(t, srv, "fixtures/v1"),
		registryDockerURL(t, srv, "fixtures/v2"),
		deltaPath,
		"--creds", "alice:wrong",
		"--tls-verify=false",
	)
	require.Equal(t, 2, exit, "expected exit 2 (user/auth) for wrong credentials; stderr=%q", stderr)
	lower := strings.ToLower(stderr)
	authRelated := strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "unauthorized")
	require.True(t, authRelated, "expected auth-related error in stderr; got: %s", stderr)
}

func TestDiffCLI_UnreachableFails(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedV1V2(t, srv, root)
	v1Ref := registryDockerURL(t, srv, "fixtures/v1")
	v2Ref := registryDockerURL(t, srv, "fixtures/v2")
	srv.Close()

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		v1Ref,
		v2Ref,
		deltaPath,
		"--tls-verify=false",
	)
	require.Equal(t, 3, exit, "expected exit 3 (env) for unreachable registry; stderr=%q", stderr)
}

func TestDiffCLI_UnknownTagFails(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedOCIIntoRegistry(t, srv, "fixtures/v1",
		filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)
	// Do not seed fixtures/v2 — the tag is missing.

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		registryDockerURL(t, srv, "fixtures/v1"),
		registryDockerURL(t, srv, "fixtures/v2"),
		deltaPath,
		"--tls-verify=false",
	)
	require.Equal(t, 4, exit, "expected exit 4 (content) for missing tag; stderr=%q", stderr)
	lower := strings.ToLower(stderr)
	contentRelated := strings.Contains(lower, "manifest") ||
		strings.Contains(lower, "not found")
	require.True(t, contentRelated,
		"expected manifest/not-found error in stderr; got: %s", stderr)
}
