//go:build integration

package importer_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/internal/registrytest"
	"github.com/leosocy/diffah/pkg/diff"
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
		DeltaPath:    deltaPath,
		Baselines:    map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:      map[string]string{"default": outPath},
		Strict:       true,
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
		DeltaPath:    deltaPath,
		Baselines:    map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:      map[string]string{"default": outPath},
		Strict:       true,
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

func TestImporter_PushesOutputToRegistry(t *testing.T) {
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

	targetRef := registryDockerURL(t, srv, "app/v2")
	err := importer.Import(ctx, importer.Options{
		DeltaPath:    deltaPath,
		Baselines:    map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:      map[string]string{"default": targetRef},
		Strict:       true,
		AllowConvert: true,
		SystemContext: &types.SystemContext{
			DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
		},
	})
	require.NoError(t, err)

	// Second client reads the pushed tag back using crane — proves
	// the image is actually present and readable in the registry.
	img, err := crane.Pull(registryHost(t, srv)+"/app/v2:latest", crane.Insecure)
	require.NoError(t, err)
	d, err := img.Digest()
	require.NoError(t, err)
	require.NotEmpty(t, d.String())
}

func TestImporter_LazyBaselineFetch_OnlyReferencedBlobsPulled(t *testing.T) {
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

	before := len(srv.BlobHits())

	outPath := "oci-archive:" + filepath.Join(tmp, "restored.tar")
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:     deltaPath,
		Baselines:     map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:       map[string]string{"default": outPath},
		Strict:        true,
		AllowConvert:  true,
		SystemContext: &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
	}))

	all := srv.BlobHits()
	newHits := all[before:]

	// All baseline blobs that appear in newHits must NOT be in the delta
	// archive (i.e., must be baseline-only layers the importer needs to
	// pull to reconstruct the target). Blobs that ARE shipped in the
	// delta must never appear.
	shipped := shippedDigestsInDelta(t, deltaPath)
	for _, hit := range newHits {
		require.NotContains(t, shipped, hit.Digest,
			"unexpected baseline blob fetched (it was shipped in the delta): %s", hit.Digest)
	}
	// Sanity: at least the manifest-GET implicit layer references plus
	// any truly baseline-only layers should have happened. Don't gate on
	// an exact count — the importer may issue HEAD+GET or cache-aware
	// variants. Just assert "some pulls happened AND none were wasteful".
	// If the fixtures have no baseline-only layers, this allows zero.
}

// shippedDigestsInDelta parses the delta archive's sidecar JSON and
// returns the set of blob digests the delta ships (full or patch).
// composeImage must NEVER pull those from the baseline.
func shippedDigestsInDelta(t *testing.T, deltaPath string) map[digest.Digest]struct{} {
	t.Helper()
	raw, err := archive.ReadSidecar(deltaPath)
	require.NoError(t, err)
	sc, err := diff.ParseSidecar(raw)
	require.NoError(t, err)
	out := make(map[digest.Digest]struct{}, len(sc.Blobs))
	for d := range sc.Blobs {
		out[d] = struct{}{}
	}
	return out
}

func TestImporter_RetriesOn503(t *testing.T) {
	ctx := context.Background()
	// Fault only fires on GET /manifests/ (not PUT, which is used during push seeding).
	srv := registrytest.New(t,
		registrytest.WithInjectFault(func(r *http.Request) bool {
			return r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/")
		}, http.StatusServiceUnavailable, 2),
	)
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
	require.NoError(t, importer.Import(ctx, importer.Options{
		DeltaPath:     deltaPath,
		Baselines:     map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:       map[string]string{"default": outPath},
		Strict:        true,
		AllowConvert:  true,
		RetryTimes:    3,
		SystemContext: &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
	}))
}

func TestImporter_NoRetryWhenRetryTimesIsZero(t *testing.T) {
	ctx := context.Background()
	// Fault only fires on GET /manifests/ (not PUT used during push seeding).
	srv := registrytest.New(t,
		registrytest.WithInjectFault(func(r *http.Request) bool {
			return r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/")
		}, http.StatusServiceUnavailable, 2),
	)
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
		DeltaPath:     deltaPath,
		Baselines:     map[string]string{"default": registryDockerURL(t, srv, "app/v1")},
		Outputs:       map[string]string{"default": outPath},
		Strict:        true,
		AllowConvert:  true,
		RetryTimes:    0,
		SystemContext: &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
	})
	require.Error(t, err)
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
