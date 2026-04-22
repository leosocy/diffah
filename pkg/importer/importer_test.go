package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

type probeStub struct {
	ok     bool
	reason string
	calls  int
}

func (p *probeStub) probe(ctx context.Context) (bool, string) {
	p.calls++
	return p.ok, p.reason
}

func TestImport_NeedsZstdMatrix(t *testing.T) {
	t.Run("all-full_missing_probe_no_call", func(t *testing.T) {
		h := newAllFullBundle(t)
		ps := &probeStub{ok: false, reason: "missing"}
		opts := Options{
			DeltaPath:    h.bundlePath,
			Baselines:    map[string]string{h.imageName(): h.baselinePath()},
			OutputPath:   t.TempDir(),
			OutputFormat: FormatOCIArchive,
			Probe:        ps.probe,
		}
		require.NoError(t, Import(context.Background(), opts))
		require.Zero(t, ps.calls, "probe must NOT fire when archive has no patch entries")
	})
	t.Run("all-full_ok_probe_noop", func(t *testing.T) {
		h := newAllFullBundle(t)
		ps := &probeStub{ok: true}
		opts := Options{
			DeltaPath:    h.bundlePath,
			Baselines:    map[string]string{h.imageName(): h.baselinePath()},
			OutputPath:   t.TempDir(),
			OutputFormat: FormatOCIArchive,
			Probe:        ps.probe,
		}
		require.NoError(t, Import(context.Background(), opts))
		require.Zero(t, ps.calls)
	})
	t.Run("patch_missing_probe_hardfail_before_blob", func(t *testing.T) {
		h := newMixedBundle(t)
		ps := &probeStub{ok: false, reason: "zstd not on $PATH"}
		opts := Options{
			DeltaPath:    h.bundlePath,
			Baselines:    map[string]string{h.imageName(): h.baselinePath()},
			OutputPath:   t.TempDir(),
			OutputFormat: FormatOCIArchive,
			Probe:        ps.probe,
		}
		err := Import(context.Background(), opts)
		require.Error(t, err)
		require.True(t, errors.Is(err, zstdpatch.ErrZstdBinaryMissing))
		require.Equal(t, 1, ps.calls)
		entries, _ := os.ReadDir(opts.OutputPath)
		require.Empty(t, entries, "no output must be produced when probe hard-fails")
	})
	t.Run("patch_ok_round_trip_succeeds", func(t *testing.T) {
		h := newMixedBundle(t)
		ps := &probeStub{ok: true}
		opts := Options{
			DeltaPath:    h.bundlePath,
			Baselines:    map[string]string{h.imageName(): h.baselinePath()},
			OutputPath:   t.TempDir(),
			OutputFormat: FormatOCIArchive,
			Probe:        ps.probe,
		}
		require.NoError(t, Import(context.Background(), opts))
		require.Equal(t, 1, ps.calls)
	})
}

func TestDryRun_ReportsNeedsZstdAndAvailable(t *testing.T) {
	t.Run("all-full_no_requires_zstd", func(t *testing.T) {
		h := newAllFullBundle(t)
		report, err := DryRun(context.Background(), Options{
			DeltaPath: h.bundlePath,
			Probe:     (&probeStub{ok: true}).probe,
		})
		require.NoError(t, err)
		require.False(t, report.RequiresZstd)
	})
	t.Run("patch_and_missing_probe_still_no_error", func(t *testing.T) {
		h := newMixedBundle(t)
		report, err := DryRun(context.Background(), Options{
			DeltaPath: h.bundlePath,
			Probe:     (&probeStub{ok: false, reason: "missing"}).probe,
		})
		require.NoError(t, err)
		require.True(t, report.RequiresZstd)
		require.False(t, report.ZstdAvailable)
	})
}

func newAllFullBundle(t *testing.T) *bundleHarness {
	t.Helper()
	return newBundleHarness(t, []exporter.Pair{fixturePair(t)})
}

func newMixedBundle(t *testing.T) *bundleHarness {
	t.Helper()
	ctx := context.Background()
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "bundle.tar")
	pair := fixturePair(t)
	err := exporter.Export(ctx, exporter.Options{
		Pairs:       []exporter.Pair{pair},
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

	var patches int
	for _, b := range b.sidecar.Blobs {
		if b.Encoding == diff.EncodingPatch {
			patches++
		}
	}
	if patches == 0 {
		t.Skip("zstd not available on this host; cannot test patch encoding")
	}
	return &bundleHarness{t: t, ctx: ctx, tmpDir: tmpDir, bundlePath: bundlePath, sidecar: b.sidecar}
}

func fixturePair(t *testing.T) exporter.Pair {
	t.Helper()
	return exporter.Pair{
		Name:         "svc-a",
		BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath:   "../../testdata/fixtures/v2_oci.tar",
	}
}

func (h *bundleHarness) imageName() string {
	if len(h.sidecar.Images) == 0 {
		return ""
	}
	return h.sidecar.Images[0].Name
}

func (h *bundleHarness) baselinePath() string {
	if len(h.sidecar.Images) == 0 {
		return ""
	}
	return "../../testdata/fixtures/v1_oci.tar"
}
