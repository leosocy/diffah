package importer

import (
	"bytes"
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

// importOpts builds an Options that writes oci-archive outputs to a per-test
// directory. Baseline values must already carry their transport prefix.
func (h *bundleHarness) importOpts(baselines map[string]string, strict bool) Options {
	outDir := filepath.Join(h.tmpDir, "output")
	outputs := make(map[string]string, len(baselines))
	for name := range baselines {
		outputs[name] = "oci-archive:" + filepath.Join(outDir, name+".tar")
	}
	return Options{
		DeltaPath: h.bundlePath,
		Baselines: baselines,
		Outputs:   outputs,
		Strict:    strict,
	}
}

func TestIntegration_PartialImport(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{{
		Name: "svc-a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{"svc-a": "oci-archive:../../testdata/fixtures/v1_oci.tar"}, false)
	err := Import(h.ctx, opts)
	require.NoError(t, err)
}

func TestIntegration_StrictRejectsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{{
		Name: "svc-a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
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
		{Name: "svc-a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
			TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"},
		{Name: "svc-b", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
			TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"},
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
		Name: "svc-a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{"unknown-svc": "oci-archive:../../testdata/fixtures/v1_oci.tar"}, false)
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
		Name: "svc-a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{"svc-a": "oci-archive:../../testdata/fixtures/v2_oci.tar"}, false)
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
		DeltaPath: "../../testdata/fixtures/v1_phase1.tar",
		Baselines: map[string]string{"default": "oci-archive:../../testdata/fixtures/v1_oci.tar"},
		Outputs:   map[string]string{"default": "oci-archive:" + filepath.Join(t.TempDir(), "out.tar")},
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
		Name: "svc-a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
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
		Name: "svc-a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
	}})
	opts := h.importOpts(map[string]string{"default": "oci-archive:../../testdata/fixtures/v1_oci.tar"}, false)
	err := Import(h.ctx, opts)
	require.NoError(t, err)
}

func TestIntegration_MultiImageBundle_UnknownBaselineName(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{
		"svc-a":      "oci-archive:../../testdata/fixtures/v1_oci.tar",
		"wrong-name": "oci-archive:../../testdata/fixtures/v1_oci.tar",
	}, false)
	err := Import(h.ctx, opts)
	require.Error(t, err)
	var unknown *diff.ErrBaselineNameUnknown
	require.ErrorAs(t, err, &unknown, "unknown baseline name must produce ErrBaselineNameUnknown")
	require.Equal(t, "wrong-name", unknown.Name)
	require.Contains(t, unknown.Available, "svc-a")
	require.Contains(t, unknown.Available, "svc-b")
}

func TestIntegration_MultiImageBundle_StrictMissingAll(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{}, true)
	err := Import(h.ctx, opts)
	require.Error(t, err)
	var missing *diff.ErrBaselineMissing
	require.ErrorAs(t, err, &missing, "strict mode with no baselines must produce ErrBaselineMissing")
	require.ElementsMatch(t, []string{"svc-a", "svc-b"}, missing.Names,
		"must list ALL missing image names")
}

func TestIntegration_MultiImageBundle_StrictMissingOne(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{
		"svc-a": "oci-archive:../../testdata/fixtures/v1_oci.tar",
	}, true)
	err := Import(h.ctx, opts)
	require.Error(t, err)
	var missing *diff.ErrBaselineMissing
	require.ErrorAs(t, err, &missing, "strict mode with partial baselines must produce ErrBaselineMissing")
	require.Equal(t, []string{"svc-b"}, missing.Names,
		"must list only the missing image name")
}

func TestIntegration_MultiImageBundle_BaselineMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{
		"svc-a": "oci-archive:../../testdata/fixtures/v2_oci.tar",
		"svc-b": "oci-archive:../../testdata/fixtures/v1_oci.tar",
	}, false)
	err := Import(h.ctx, opts)
	require.Error(t, err)
	var mismatch *diff.ErrBaselineMismatch
	require.ErrorAs(t, err, &mismatch, "wrong baseline must produce ErrBaselineMismatch")
	require.Equal(t, "svc-a", mismatch.Name)
}

func TestIntegration_MultiImageBundle_PositionalBaselineRejected(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{
		"default": "oci-archive:../../testdata/fixtures/v1_oci.tar",
	}, false)
	err := Import(h.ctx, opts)
	require.Error(t, err)
	var multi *diff.ErrMultiImageNeedsNamedBaselines
	require.ErrorAs(t, err, &multi,
		"positional baseline on multi-image bundle must produce ErrMultiImageNeedsNamedBaselines")
}

func TestIntegration_MultiImageBundle_DryRunReport(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{
		"svc-a": "oci-archive:../../testdata/fixtures/v1_oci.tar",
	}, false)
	report, err := DryRun(h.ctx, opts)
	require.NoError(t, err)

	require.Equal(t, "bundle", report.Feature)
	require.Equal(t, "v1", report.Version)
	require.Len(t, report.Images, 2)

	byName := map[string]ImageDryRun{}
	for _, i := range report.Images {
		byName[i.Name] = i
	}
	require.True(t, byName["svc-a"].WouldImport)
	require.False(t, byName["svc-b"].WouldImport)
	require.Contains(t, byName["svc-b"].SkipReason, "no baseline provided")
	require.Greater(t, report.Blobs.FullCount, 0)
}

func TestIntegration_MultiImageBundle_ForceFullDedup(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	for d, e := range h.sidecar.Blobs {
		if e.Encoding == diff.EncodingPatch {
			t.Fatalf("shared target blobs must be forced to full encoding, got patch for %s", d)
		}
	}
	require.GreaterOrEqual(t, len(h.sidecar.Blobs), 2, "must have at least 2 blobs (manifest + config)")
	require.Len(t, h.sidecar.Images, 2)
	require.Equal(t, "svc-a", h.sidecar.Images[0].Name)
	require.Equal(t, "svc-b", h.sidecar.Images[1].Name)
}

func newMultiImageBundleHarness(t *testing.T) *bundleHarness {
	t.Helper()
	return newBundleHarness(t, []exporter.Pair{
		{Name: "svc-a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
			TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"},
		{Name: "svc-b", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
			TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"},
	})
}

func TestIntegration_MultiImageBundle_ImportsBoth(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	outDir := filepath.Join(h.tmpDir, "out")
	opts := Options{
		DeltaPath: h.bundlePath,
		Baselines: map[string]string{
			"svc-a": "oci-archive:../../testdata/fixtures/v1_oci.tar",
			"svc-b": "oci-archive:../../testdata/fixtures/v1_oci.tar",
		},
		Outputs: map[string]string{
			"svc-a": "oci-archive:" + filepath.Join(outDir, "svc-a.tar"),
			"svc-b": "oci-archive:" + filepath.Join(outDir, "svc-b.tar"),
		},
	}
	err := Import(h.ctx, opts)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(outDir, "svc-a.tar"))
	require.NoError(t, err, "svc-a.tar must exist")
	_, err = os.Stat(filepath.Join(outDir, "svc-b.tar"))
	require.NoError(t, err, "svc-b.tar must exist")
}

func TestIntegration_MultiImageBundle_PartialSkip(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	outDir := filepath.Join(h.tmpDir, "out")
	var buf bytes.Buffer
	opts := Options{
		DeltaPath: h.bundlePath,
		Baselines: map[string]string{"svc-a": "oci-archive:../../testdata/fixtures/v1_oci.tar"},
		Outputs:   map[string]string{"svc-a": "oci-archive:" + filepath.Join(outDir, "svc-a.tar")},
		Progress:  &buf,
	}
	err := Import(h.ctx, opts)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(outDir, "svc-a.tar"))
	require.NoError(t, err, "svc-a.tar must exist")
	_, err = os.Stat(filepath.Join(outDir, "svc-b.tar"))
	require.ErrorIs(t, err, os.ErrNotExist, "svc-b.tar must not exist")
}

func TestDryRun_PopulatesAllFields(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{
		"svc-a": "oci-archive:../../testdata/fixtures/v1_oci.tar",
	}, false)
	report, err := DryRun(h.ctx, opts)
	require.NoError(t, err)

	require.Equal(t, "bundle", report.Feature)
	require.Equal(t, "v1", report.Version)
	require.Equal(t, "diffah", report.Tool)
	require.Equal(t, "test", report.ToolVersion)
	require.Equal(t, "linux/amd64", report.Platform)
	require.NotZero(t, report.ArchiveBytes, "ArchiveBytes is the bundle file size")
	require.Greater(t, report.Blobs.FullCount+report.Blobs.PatchCount, 0)

	require.Len(t, report.Images, 2)
	var a, b ImageDryRun
	for _, i := range report.Images {
		switch i.Name {
		case "svc-a":
			a = i
		case "svc-b":
			b = i
		}
	}
	require.Equal(t, "svc-a", a.Name)
	require.True(t, a.BaselineProvided)
	require.True(t, a.WouldImport)
	require.Empty(t, a.SkipReason)
	require.Greater(t, a.LayerCount, 0)

	require.Equal(t, "svc-b", b.Name)
	require.False(t, b.BaselineProvided)
	require.False(t, b.WouldImport)
	require.Contains(t, b.SkipReason, "no baseline provided")
}

func fixtureImageName(_ *testing.T) string {
	return "svc-a"
}

// fixturePair returns the canonical v1_oci → v2_oci pair used by integration
// tests in this package.
func fixturePair(t *testing.T) exporter.Pair {
	t.Helper()
	return exporter.Pair{
		Name:        "svc-a",
		BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef:   "oci-archive:../../testdata/fixtures/v2_oci.tar",
	}
}

func fixtureBaselineArchivePath(_ *testing.T) string {
	return "../../testdata/fixtures/v1_oci.tar"
}

func TestIntegration_AutoDowngradesUnderReducedPATH(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Setenv("PATH", "")

	tmp := t.TempDir()
	bundlePath := filepath.Join(tmp, "bundle.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs:       []exporter.Pair{fixturePair(t)},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  bundlePath,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	b, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer b.cleanup()
	for d, b := range b.sidecar.Blobs {
		require.Equal(t, diff.EncodingFull, b.Encoding, "blob %s encoded as %s", d, b.Encoding)
	}

	outDir := filepath.Join(tmp, "out")
	require.NoError(t, os.MkdirAll(outDir, 0o755))
	err = Import(context.Background(), Options{
		DeltaPath: bundlePath,
		Baselines: map[string]string{fixtureImageName(t): "oci-archive:" + fixtureBaselineArchivePath(t)},
		Outputs:   map[string]string{fixtureImageName(t): "oci-archive:" + filepath.Join(outDir, fixtureImageName(t)+".tar")},
	})
	require.NoError(t, err)
}
