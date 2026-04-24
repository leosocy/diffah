//go:build integration

package cmd_test

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/internal/registrytest"
)

// seedOCIIntoRegistry copies an OCI archive into the test registry using
// the containers/image copy.Image pipeline. auth may be nil for anonymous push.
// TLS verification is unconditionally skipped so that seeding always succeeds
// regardless of the registry's TLS configuration — only the CLI under test
// is subject to TLS policy.
func seedOCIIntoRegistry(t *testing.T, srv *registrytest.Server, repo, tarPath string, auth *types.DockerAuthConfig) {
	t.Helper()
	seedOCIWithSysctx(t, srv, repo, tarPath, &types.SystemContext{
		DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
		DockerAuthConfig:            auth,
	})
}

// seedOCIIntoRegistryWithToken copies an OCI archive into a bearer-token
// protected registry. The token is sent as DockerBearerRegistryToken.
func seedOCIIntoRegistryWithToken(t *testing.T, srv *registrytest.Server, repo, tarPath, token string) {
	t.Helper()
	seedOCIWithSysctx(t, srv, repo, tarPath, &types.SystemContext{
		DockerInsecureSkipTLSVerify:  types.OptionalBoolTrue,
		DockerBearerRegistryToken:    token,
	})
}

// seedOCIWithSysctx is the shared implementation used by both seed helpers.
func seedOCIWithSysctx(t *testing.T, srv *registrytest.Server, repo, tarPath string, sysctx *types.SystemContext) {
	t.Helper()
	ctx := context.Background()

	srcRef, err := imageio.ParseReference("oci-archive:" + tarPath)
	require.NoError(t, err, "parse source oci-archive reference")

	dst := "docker://" + registryHost(t, srv) + "/" + repo + ":latest"
	dstRef, err := imageio.ParseReference(dst)
	require.NoError(t, err, "parse destination docker reference")

	policyCtx, err := imageio.DefaultPolicyContext()
	require.NoError(t, err)
	defer func() { _ = policyCtx.Destroy() }()

	_, err = copy.Image(ctx, policyCtx, dstRef, srcRef, &copy.Options{
		SourceCtx:      &types.SystemContext{},
		DestinationCtx: sysctx,
		ReportWriter:   os.Stderr,
	})
	require.NoError(t, err, "push fixture to test registry")
}

// registryHost returns the host:port of the test registry.
func registryHost(t *testing.T, srv *registrytest.Server) string {
	t.Helper()
	parsed, err := url.Parse(srv.URL())
	require.NoError(t, err)
	return parsed.Host
}

// registryDockerURL returns the docker:// reference for a repo in the test registry.
func registryDockerURL(t *testing.T, srv *registrytest.Server, repo string) string {
	t.Helper()
	return "docker://" + registryHost(t, srv) + "/" + repo + ":latest"
}
