//go:build integration

package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/registrytest"
)

// writeBundleSpec marshals spec as JSON and returns its on-disk path.
func writeBundleSpec(t *testing.T, tmp string, pairs []map[string]string) string {
	t.Helper()
	spec := map[string]any{"pairs": pairs}
	raw, err := json.MarshalIndent(spec, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(tmp, "bundle.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

// dockerPairs returns a single-pair BundleSpec pairs slice that points
// at the seeded fixtures/v1 and fixtures/v2 repos in srv.
func dockerPairs(t *testing.T, srv *registrytest.Server) []map[string]string {
	t.Helper()
	return []map[string]string{{
		"name":     "svc-a",
		"baseline": registryDockerURL(t, srv, "fixtures/v1"),
		"target":   registryDockerURL(t, srv, "fixtures/v2"),
	}}
}

func TestBundleCLI_AnonymousPull(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedV1V2(t, srv, root)

	tmp := t.TempDir()
	specPath := writeBundleSpec(t, tmp, dockerPairs(t, srv))
	outPath := filepath.Join(tmp, "bundle.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"bundle", specPath, outPath,
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "bundle failed: %s", stderr)

	info, err := os.Stat(outPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestBundleCLI_BasicAuth(t *testing.T) {
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
	specPath := writeBundleSpec(t, tmp, dockerPairs(t, srv))
	outPath := filepath.Join(tmp, "bundle.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"bundle", specPath, outPath,
		"--creds", "alice:s3cret",
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "bundle failed: %s", stderr)

	info, err := os.Stat(outPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestBundleCLI_BearerToken(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBearerToken("abc123"))
	seedOCIIntoRegistryWithToken(t, srv, "fixtures/v1",
		filepath.Join(root, "testdata/fixtures/v1_oci.tar"), "abc123")
	seedOCIIntoRegistryWithToken(t, srv, "fixtures/v2",
		filepath.Join(root, "testdata/fixtures/v2_oci.tar"), "abc123")

	tmp := t.TempDir()
	specPath := writeBundleSpec(t, tmp, dockerPairs(t, srv))
	outPath := filepath.Join(tmp, "bundle.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"bundle", specPath, outPath,
		"--registry-token", "abc123",
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "bundle failed: %s", stderr)

	info, err := os.Stat(outPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestBundleCLI_TLSWithCertDir(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithTLS())
	seedV1V2(t, srv, root)

	tmp := t.TempDir()
	specPath := writeBundleSpec(t, tmp, dockerPairs(t, srv))
	outPath := filepath.Join(tmp, "bundle.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"bundle", specPath, outPath,
		"--cert-dir", srv.ClientCertDir(),
	)
	require.Equal(t, 0, exit, "bundle failed: %s", stderr)

	info, err := os.Stat(outPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestBundleCLI_BadCredsFails(t *testing.T) {
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
	specPath := writeBundleSpec(t, tmp, dockerPairs(t, srv))
	outPath := filepath.Join(tmp, "bundle.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"bundle", specPath, outPath,
		"--creds", "alice:wrong",
		"--tls-verify=false",
	)
	require.Equal(t, 2, exit, "expected exit 2 (user/auth) for wrong credentials; stderr=%q", stderr)
	lower := strings.ToLower(stderr)
	authRelated := strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "unauthorized")
	require.True(t, authRelated, "expected auth-related error in stderr; got: %s", stderr)
}

func TestBundleCLI_UnreachableFails(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedV1V2(t, srv, root)
	v1Ref := registryDockerURL(t, srv, "fixtures/v1")
	v2Ref := registryDockerURL(t, srv, "fixtures/v2")
	srv.Close()

	tmp := t.TempDir()
	specPath := writeBundleSpec(t, tmp, []map[string]string{{
		"name":     "svc-a",
		"baseline": v1Ref,
		"target":   v2Ref,
	}})
	outPath := filepath.Join(tmp, "bundle.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"bundle", specPath, outPath,
		"--tls-verify=false",
	)
	require.Equal(t, 3, exit, "expected exit 3 (env) for unreachable registry; stderr=%q", stderr)
}

func TestBundleCLI_UnknownTagFails(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedOCIIntoRegistry(t, srv, "fixtures/v1",
		filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)
	// fixtures/v2 is not seeded — the target tag is missing.

	tmp := t.TempDir()
	specPath := writeBundleSpec(t, tmp, dockerPairs(t, srv))
	outPath := filepath.Join(tmp, "bundle.tar")

	_, stderr, exit := runDiffahBin(t, bin,
		"bundle", specPath, outPath,
		"--tls-verify=false",
	)
	require.Equal(t, 4, exit, "expected exit 4 (content) for missing tag; stderr=%q", stderr)
	lower := strings.ToLower(stderr)
	contentRelated := strings.Contains(lower, "manifest") ||
		strings.Contains(lower, "not found")
	require.True(t, contentRelated,
		"expected manifest/not-found error in stderr; got: %s", stderr)
}
