//go:build integration

package importer_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/internal/registrytest"
	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

func TestImporter_PullsBaselineAnonymously(t *testing.T) {
	ctx := context.Background()
	srv := registrytest.New(t)
	pushFixtureIntoRegistry(t, ctx, srv, nil, "app/v1", testdataPath(t, "v1_oci.tar"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs: []exporter.Pair{{
			Name:         "default",
			BaselinePath: testdataPath(t, "v1_oci.tar"),
			TargetPath:   testdataPath(t, "v2_oci.tar"),
		}},
		OutputPath:  deltaPath,
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		ToolVersion: "test",
	}))

	outPath := "oci-archive:" + filepath.Join(tmp, "restored.tar")
	err := importer.Import(ctx, importer.Options{
		DeltaPath: deltaPath,
		Baselines: map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:   map[string]string{"default": outPath},
		Strict:    true,
		AllowConvert: true,
		SystemContext: &types.SystemContext{
			DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
		},
	})
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(tmp, "restored.tar"))
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestImporter_PullsBaselineWithBasicAuth(t *testing.T) {
	ctx := context.Background()
	srv := registrytest.New(t, registrytest.WithBasicAuth("alice", "s3cret"))
	pushFixtureIntoRegistry(t, ctx, srv, &types.DockerAuthConfig{
		Username: "alice",
		Password: "s3cret",
	}, "app/v1", testdataPath(t, "v1_oci.tar"))

	tmp := t.TempDir()
	deltaPath := filepath.Join(tmp, "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		Pairs: []exporter.Pair{{
			Name:         "default",
			BaselinePath: testdataPath(t, "v1_oci.tar"),
			TargetPath:   testdataPath(t, "v2_oci.tar"),
		}},
		OutputPath:  deltaPath,
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		ToolVersion: "test",
	}))

	outPath := "oci-archive:" + filepath.Join(tmp, "restored.tar")
	err := importer.Import(ctx, importer.Options{
		DeltaPath: deltaPath,
		Baselines: map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:   map[string]string{"default": outPath},
		Strict:    true,
		AllowConvert: true,
		SystemContext: &types.SystemContext{
			DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
			DockerAuthConfig: &types.DockerAuthConfig{
				Username: "alice",
				Password: "s3cret",
			},
		},
	})
	require.NoError(t, err)
}

// Helpers — keep at file bottom.

func testdataPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..", "testdata", "fixtures", name)
}

// pushFixtureIntoRegistry copies an OCI archive into the test registry using
// the containers/image copy.Image pipeline. auth may be nil for anonymous push.
func pushFixtureIntoRegistry(t *testing.T, ctx context.Context, srv *registrytest.Server, auth *types.DockerAuthConfig, repo, tarPath string) {
	t.Helper()
	srcRef, err := imageio.ParseReference("oci-archive:" + tarPath)
	require.NoError(t, err, "parse source oci-archive reference")

	dst := "docker://" + registryHost(t, srv) + "/" + repo + ":latest"
	dstRef, err := imageio.ParseReference(dst)
	require.NoError(t, err, "parse destination docker reference")

	policyCtx, err := imageio.DefaultPolicyContext()
	require.NoError(t, err)
	defer func() { _ = policyCtx.Destroy() }()

	sysctx := &types.SystemContext{
		DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
		DockerAuthConfig:            auth,
	}
	_, err = copy.Image(ctx, policyCtx, dstRef, srcRef, &copy.Options{
		SourceCtx:      &types.SystemContext{},
		DestinationCtx: sysctx,
		ReportWriter:   os.Stderr,
	})
	require.NoError(t, err, "push fixture to test registry")
}

func registryHost(t *testing.T, srv *registrytest.Server) string {
	t.Helper()
	parsed, err := url.Parse(srv.URL())
	require.NoError(t, err)
	return parsed.Host
}

func registryDockerURL(t *testing.T, srv *registrytest.Server, repo string) string {
	t.Helper()
	return "docker://" + registryHost(t, srv) + "/" + repo + ":latest"
}
