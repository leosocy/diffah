//go:build integration

package cmd_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/registrytest"
)

// buildDelta runs "diffah diff" on the standard v1/v2 OCI fixture pair and
// writes the delta archive to deltaPath. Fails the test on non-zero exit.
func buildDelta(t *testing.T, bin, root, deltaPath string) {
	t.Helper()
	_, stderr, exit := runDiffahBin(t, bin,
		"diff",
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		deltaPath,
	)
	require.Equal(t, 0, exit, "diff failed: %s", stderr)
}

// basicAuth returns a DockerAuthConfig for user/pass credential seeding.
func basicAuth(user, pass string) *types.DockerAuthConfig {
	return &types.DockerAuthConfig{Username: user, Password: pass}
}

func TestApplyCLI_PushToFreshTag(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		registryDockerURL(t, srv, "app/v2"),
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "apply failed: %s", stderr)

	img, err := crane.Pull(registryHost(t, srv)+"/app/v2:latest", crane.Insecure)
	require.NoError(t, err)
	d, err := img.Digest()
	require.NoError(t, err)
	require.NotEmpty(t, d.String())
}

func TestApplyCLI_AnonymousPullBaseline(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+restoredArchive,
		"--tls-verify=false",
		"--no-creds",
	)
	require.Equal(t, 0, exit, "apply failed: %s", stderr)

	info, err := os.Stat(restoredArchive)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestApplyCLI_BasicAuthViaCreds(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		basicAuth("alice", "s3cret"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+restoredArchive,
		"--creds", "alice:s3cret",
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "apply failed: %s", stderr)

	info, err := os.Stat(restoredArchive)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestApplyCLI_BasicAuthViaAuthfile(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		basicAuth("alice", "s3cret"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	host := registryHost(t, srv)
	encoded := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	authfileContent := map[string]any{
		"auths": map[string]any{
			host: map[string]string{"auth": encoded},
		},
	}
	authfileRaw, err := json.MarshalIndent(authfileContent, "", "  ")
	require.NoError(t, err)
	authfilePath := filepath.Join(tmp, "auth.json")
	require.NoError(t, os.WriteFile(authfilePath, authfileRaw, 0o600))

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+restoredArchive,
		"--authfile", authfilePath,
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "apply failed: %s", stderr)

	info, err := os.Stat(restoredArchive)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestApplyCLI_BearerToken(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBearerToken("abc123"))
	seedOCIIntoRegistryWithToken(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), "abc123")

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+restoredArchive,
		"--registry-token", "abc123",
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "apply failed: %s", stderr)

	info, err := os.Stat(restoredArchive)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestApplyCLI_TLSVerifyDefaultFailsWithoutCertDir(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithTLS())
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+restoredArchive,
		// no --tls-verify=false, no --cert-dir
	)
	require.NotEqual(t, 0, exit, "expected non-zero exit when TLS verification fails")
	lowerStderr := strings.ToLower(stderr)
	certOrVerify := strings.Contains(lowerStderr, "certificate") ||
		strings.Contains(lowerStderr, "verify") ||
		strings.Contains(lowerStderr, "x509")
	require.True(t, certOrVerify, "expected TLS-related error in stderr; got: %s", stderr)
}

func TestApplyCLI_TLSVerifyFalseBypasses(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithTLS())
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+restoredArchive,
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "apply failed: %s", stderr)

	info, err := os.Stat(restoredArchive)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestApplyCLI_Retry503WithRetryTimes3(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t,
		registrytest.WithInjectFault(func(r *http.Request) bool {
			return r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/")
		}, http.StatusServiceUnavailable, 2),
	)
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"), nil)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+restoredArchive,
		"--retry-times", "3",
		"--tls-verify=false",
	)
	require.Equal(t, 0, exit, "apply failed: %s", stderr)

	info, err := os.Stat(restoredArchive)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestApplyCLI_AuthFailureExit2(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	seedOCIIntoRegistry(t, srv, "app/v1", filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
		basicAuth("alice", "s3cret"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "app/v1"),
		"oci-archive:"+restoredArchive,
		"--creds", "alice:wrong",
		"--tls-verify=false",
	)
	require.Equal(t, 2, exit, "expected exit 2 (user/auth) for wrong credentials; stderr=%q", stderr)
	lowerStderr := strings.ToLower(stderr)
	authOrUnauth := strings.Contains(lowerStderr, "authentication") ||
		strings.Contains(lowerStderr, "unauthorized")
	require.True(t, authOrUnauth, "expected auth-related error in stderr; got: %s", stderr)
}

func TestApplyCLI_MissingManifestExit4(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	srv := registrytest.New(t)

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		registryDockerURL(t, srv, "nonexistent/repo"),
		"oci-archive:"+restoredArchive,
		"--tls-verify=false",
	)
	require.Equal(t, 4, exit, "expected exit 4 (content) for missing manifest; stderr=%q", stderr)
	lowerStderr := strings.ToLower(stderr)
	manifestOrNotFound := strings.Contains(lowerStderr, "manifest") ||
		strings.Contains(lowerStderr, "not found")
	require.True(t, manifestOrNotFound, "expected manifest-related error in stderr; got: %s", stderr)
}
