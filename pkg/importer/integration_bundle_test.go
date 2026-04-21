package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

type bundleHarness struct {
	t          *testing.T
	ctx        context.Context
	tmpDir     string
	bundlePath string
	sidecar    *diff.Sidecar
}

func newBundleHarness(t *testing.T, pairs []exporter.Pair) *bundleHarness {
	t.Helper()
	ctx := context.Background()
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "bundle.tar")
	err := exporter.Export(ctx, exporter.Options{
		Pairs:       pairs,
		Platform:    "linux/amd64",
		IntraLayer:  "off",
		OutputPath:  bundlePath,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer b.cleanup()
	return &bundleHarness{t: t, ctx: ctx, tmpDir: tmpDir, bundlePath: bundlePath, sidecar: b.sidecar}
}

func (h *bundleHarness) importOpts(baselines map[string]string, strict bool) Options {
	outPath := filepath.Join(h.tmpDir, "output.tar")
	return Options{
		DeltaPath:    h.bundlePath,
		Baselines:    baselines,
		Strict:       strict,
		OutputPath:   outPath,
		OutputFormat: "oci-archive",
	}
}

func (h *bundleHarness) baselinePath(name string) string {
	return "../../testdata/fixtures/v1_oci.tar"
}

func TestIntegration_PartialImport(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{{
		Name: "svc-a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{"svc-a": "../../testdata/fixtures/v1_oci.tar"}, false)
	err := Import(h.ctx, opts)
	require.NoError(t, err)
}

func TestIntegration_StrictRejectsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{{
		Name: "svc-a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{}, true)
	err := Import(h.ctx, opts)
	require.Error(t, err)
	var missing *diff.ErrBaselineMissing
	require.ErrorAs(t, err, &missing)
}

func TestIntegration_ForceFullDedup(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{
		{Name: "svc-a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath: "../../testdata/fixtures/v2_oci.tar"},
		{Name: "svc-b", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath: "../../testdata/fixtures/v2_oci.tar"},
	})
	for d, e := range h.sidecar.Blobs {
		if d == h.sidecar.Images[0].Target.ManifestDigest {
			continue
		}
		if e.Encoding == diff.EncodingPatch {
			t.Fatalf("shared target should force full encoding, got patch for %s", d)
		}
	}
}

func TestIntegration_UnknownBaselineName(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{{
		Name: "svc-a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{"unknown-svc": "../../testdata/fixtures/v1_oci.tar"}, false)
	err := Import(h.ctx, opts)
	require.Error(t, err)
	var unknown *diff.ErrBaselineNameUnknown
	require.ErrorAs(t, err, &unknown, "unknown baseline name must produce ErrBaselineNameUnknown")
	require.Equal(t, "unknown-svc", unknown.Name)
}

func TestIntegration_BaselineMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{{
		Name: "svc-a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{"svc-a": "../../testdata/fixtures/v2_oci.tar"}, false)
	err := Import(h.ctx, opts)
	require.Error(t, err)
	var mismatch *diff.ErrBaselineMismatch
	require.ErrorAs(t, err, &mismatch)
}

func TestIntegration_LegacyArchiveRejected(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	opts := Options{
		DeltaPath:    "../../testdata/fixtures/v1_phase1.tar",
		Baselines:    map[string]string{"default": "../../testdata/fixtures/v1_oci.tar"},
		OutputPath:   filepath.Join(t.TempDir(), "output.tar"),
		OutputFormat: "oci-archive",
	}
	err := Import(context.Background(), opts)
	require.Error(t, err)
	var p1 *diff.ErrPhase1Archive
	require.ErrorAs(t, err, &p1)
}

func TestIntegration_Determinism(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir := t.TempDir()
	pairs := []exporter.Pair{{
		Name: "svc-a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar",
	}}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	path1 := filepath.Join(dir, "bundle1.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs: pairs, Platform: "linux/amd64", IntraLayer: "off",
		OutputPath: path1, ToolVersion: "test", CreatedAt: ts,
	})
	require.NoError(t, err)

	path2 := filepath.Join(dir, "bundle2.tar")
	err = exporter.Export(context.Background(), exporter.Options{
		Pairs: pairs, Platform: "linux/amd64", IntraLayer: "off",
		OutputPath: path2, ToolVersion: "test", CreatedAt: ts,
	})
	require.NoError(t, err)

	b1, err := os.ReadFile(path1)
	require.NoError(t, err)
	b2, err := os.ReadFile(path2)
	require.NoError(t, err)
	require.Equal(t, b1, b2, "two exports with same inputs must be byte-identical")
}

func TestIntegration_BundleOfOnePositional(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{{
		Name: "svc-a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{"default": "../../testdata/fixtures/v1_oci.tar"}, false)
	err := Import(h.ctx, opts)
	require.NoError(t, err)
}
