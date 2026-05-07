//go:build big

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/diff/errs"
)

// TestImport_ScaleApply2GiB locks in the importer's apply-side memory
// ceiling for the spec §13 acceptance gate: a 2-GiB-layer bundle must
// apply with peak RSS ≤ 8 GiB on a normal CI runner. The /usr/bin/time -v
// guard in .github/workflows/scale-bench.yml's "Apply (scale)" step
// measures peak RSS externally; this test exists to make that workflow
// step have something to invoke under -tags=big.
//
// DEFERRED: the in-process fixture-build + import plumbing is intentionally
// not implemented in PR6. The exporter's TestScaleBench_2GiBLayer relies
// on scripts/build_fixtures -scale=<bytes> to produce baseline.tar +
// target.tar, then drives exporter.Export. Mirroring that for the import
// side requires three steps end-to-end (build fixtures → export delta →
// import delta), each with its own per-step RSS contribution to disambiguate
// from the apply-only ceiling we want to gate. That belongs in a follow-up
// PR with proper test-side helpers (runImportInProcess, buildScale2GiBFixture).
//
// Until that lands, the workflow YAML's apply step is a no-op skip — but
// the YAML scaffolding is in place so the follow-up PR only has to swap
// the test body. Tracking note: spec §13 acceptance #9 (Phase-3 fixture
// round-trip, the I6 sibling) also remains open.
//
// Gated by DIFFAH_BIG_TEST=1 (in addition to the `big` build tag) so an
// accidental `go test -tags=big` doesn't surface a confusing "skipped"
// row on a developer's laptop. CI sets the env var explicitly.
func TestImport_ScaleApply2GiB(t *testing.T) {
	if os.Getenv("DIFFAH_BIG_TEST") != "1" {
		t.Skip("set DIFFAH_BIG_TEST=1 to run")
	}
	t.Skip("apply scale-bench infrastructure pending — see PR6 follow-up; " +
		"workflow YAML in place, test body deferred")
}

// TestImport_ScaleBaselineOnlyReuse4GiB is the hardening-PR2 scale gate for
// the admission contract: baseline-only layers must count against
// --memory-budget even though they are absent from sidecar.Blobs.
//
// The full apply RSS measurement is performed by the surrounding CI job using
// /usr/bin/time -v. This in-process guard exercises the fail-fast admission
// path with a synthetic 4 GiB baseline-only layer so the nightly job has a
// concrete PR2 regression test to invoke.
func TestImport_ScaleBaselineOnlyReuse4GiB(t *testing.T) {
	if os.Getenv("DIFFAH_SCALE_BENCH") != "1" {
		t.Skip("set DIFFAH_SCALE_BENCH=1 to run")
	}

	bundlePath, baselineRef := buildBaselineOnlyScaleBundle(t, 4<<30)

	err := Import(context.Background(), Options{
		DeltaPath: bundlePath,
		Baselines: map[string]string{
			"svc-a": baselineRef,
			"svc-b": baselineRef,
		},
		Outputs: map[string]string{
			"svc-a": "oci-archive:" + filepath.Join(t.TempDir(), "svc-a.tar"),
			"svc-b": "oci-archive:" + filepath.Join(t.TempDir(), "svc-b.tar"),
		},
		Workers:      2,
		MemoryBudget: 2 << 30,
	})
	var userErr *errs.UserError
	if !errors.As(err, &userErr) || userErr.Cat != errs.CategoryUser {
		t.Fatalf("expected CategoryUser budget rejection, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "svc-a") || !strings.Contains(err.Error(), "svc-b") {
		t.Fatalf("budget rejection should mention both oversized images, got %q", err.Error())
	}
}

func buildBaselineOnlyScaleBundle(t *testing.T, layerSize int64) (bundlePath, baselineRef string) {
	t.Helper()

	baselineRef = "oci-archive:../../testdata/fixtures/v1_oci.tar"
	ref, err := imageio.ParseReference(baselineRef)
	if err != nil {
		t.Fatalf("parse baseline ref: %v", err)
	}
	src, err := ref.NewImageSource(context.Background(), nil)
	if err != nil {
		t.Fatalf("open baseline source: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	baselineManifest, baselineMime, err := src.GetManifest(context.Background(), nil)
	if err != nil {
		t.Fatalf("read baseline manifest: %v", err)
	}
	layers, _, targetMime, err := parseManifest(baselineManifest, baselineMime)
	if err != nil {
		t.Fatalf("parse baseline manifest: %v", err)
	}
	if len(layers) == 0 {
		t.Fatal("baseline fixture has no layers")
	}

	targetManifest := withFirstLayerSize(t, baselineManifest, layerSize)
	targetDigest := digest.FromBytes(targetManifest)
	baselineDigest := digest.FromBytes(baselineManifest)

	root := t.TempDir()
	blobDir := filepath.Join(root, "blobs", targetDigest.Algorithm().String())
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		t.Fatalf("mkdir blob dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, targetDigest.Encoded()), targetManifest, 0o600); err != nil {
		t.Fatalf("write target manifest: %v", err)
	}

	sc := diff.Sidecar{
		Version:     diff.SchemaVersionV1,
		Feature:     diff.FeatureBundle,
		Tool:        "diffah-test",
		ToolVersion: "test",
		Platform:    "linux/amd64",
		Images: []diff.ImageEntry{
			scaleImageEntry("svc-a", baselineDigest, targetDigest, int64(len(targetManifest)), targetMime),
			scaleImageEntry("svc-b", baselineDigest, targetDigest, int64(len(targetManifest)), targetMime),
		},
		Blobs: map[digest.Digest]diff.BlobEntry{
			targetDigest: {
				Size:        int64(len(targetManifest)),
				MediaType:   targetMime,
				Encoding:    diff.EncodingFull,
				ArchiveSize: int64(len(targetManifest)),
			},
		},
	}
	sidecar, err := sc.Marshal()
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	bundlePath = filepath.Join(t.TempDir(), "baseline-only-scale.tar")
	if err := archive.Pack(root, sidecar, bundlePath, archive.CompressNone); err != nil {
		t.Fatalf("pack bundle: %v", err)
	}
	return bundlePath, baselineRef
}

func scaleImageEntry(
	name string,
	baselineDigest digest.Digest,
	targetDigest digest.Digest,
	targetSize int64,
	mediaType string,
) diff.ImageEntry {
	return diff.ImageEntry{
		Name: name,
		Baseline: diff.BaselineRef{
			ManifestDigest: baselineDigest,
			MediaType:      mediaType,
		},
		Target: diff.TargetRef{
			ManifestDigest: targetDigest,
			ManifestSize:   targetSize,
			MediaType:      mediaType,
		},
	}
}

func withFirstLayerSize(t *testing.T, manifest []byte, size int64) []byte {
	t.Helper()

	var raw map[string]any
	if err := json.Unmarshal(manifest, &raw); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	layers, ok := raw["layers"].([]any)
	if !ok || len(layers) == 0 {
		t.Fatal("manifest layers field missing or empty")
	}
	first, ok := layers[0].(map[string]any)
	if !ok {
		t.Fatal("manifest first layer is not an object")
	}
	first["size"] = size
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal target manifest: %v", err)
	}
	return out
}

func writeScaleManifest(t *testing.T, blobDir string, layerDigest digest.Digest, size int64) digest.Digest {
	t.Helper()

	raw, err := json.Marshal(struct {
		SchemaVersion int `json:"schemaVersion"`
		Layers        []struct {
			Digest digest.Digest `json:"digest"`
			Size   int64         `json:"size"`
		} `json:"layers"`
	}{
		SchemaVersion: 2,
		Layers: []struct {
			Digest digest.Digest `json:"digest"`
			Size   int64         `json:"size"`
		}{{Digest: layerDigest, Size: size}},
	})
	if err != nil {
		t.Fatalf("marshal scale manifest: %v", err)
	}
	d := digest.FromBytes(raw)
	dir := filepath.Join(blobDir, d.Algorithm().String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, d.Encoded()), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return d
}
