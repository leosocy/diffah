//go:build big

package importer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ociarchive "go.podman.io/image/v5/oci/archive"
	"go.podman.io/image/v5/pkg/blobinfocache/none"
	"go.podman.io/image/v5/types"

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
// /usr/bin/time -v. This test runs both the admitted apply path and the
// fail-fast admission path against a real baseline-only layer, not just
// inflated manifest metadata. Set DIFFAH_SCALE_BENCH_BYTES locally to smoke
// the path with a smaller blob; CI uses the 4 GiB default.
func TestImport_ScaleBaselineOnlyReuse4GiB(t *testing.T) {
	if os.Getenv("DIFFAH_SCALE_BENCH") != "1" {
		t.Skip("set DIFFAH_SCALE_BENCH=1 to run")
	}

	layerSize := int64(4 << 30)
	if raw := os.Getenv("DIFFAH_SCALE_BENCH_BYTES"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			t.Fatalf("invalid DIFFAH_SCALE_BENCH_BYTES=%q", raw)
		}
		layerSize = parsed
	}

	bundlePath, baselineRef, perImageSize := buildBaselineOnlyScaleBundle(t, layerSize)

	admitBudget := int64(8 << 30)
	rejectBudget := int64(2 << 30)
	if layerSize != 4<<30 {
		admitBudget = perImageSize * 2
		rejectBudget = perImageSize / 2
		if rejectBudget == 0 {
			rejectBudget = 1
		}
	}

	err := Import(context.Background(), Options{
		DeltaPath: bundlePath,
		Baselines: map[string]string{
			"svc-a": baselineRef,
			"svc-b": baselineRef,
		},
		Outputs: map[string]string{
			"svc-a": "oci-archive:" + filepath.Join(t.TempDir(), "svc-a-admitted.tar"),
			"svc-b": "oci-archive:" + filepath.Join(t.TempDir(), "svc-b-admitted.tar"),
		},
		Workers:      2,
		MemoryBudget: admitBudget,
	})
	if err != nil {
		t.Fatalf("expected admitted baseline-only apply under budget %d, got %v", admitBudget, err)
	}

	err = Import(context.Background(), Options{
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
		MemoryBudget: rejectBudget,
	})
	var userErr *errs.UserError
	if !errors.As(err, &userErr) || userErr.Cat != errs.CategoryUser {
		t.Fatalf("expected CategoryUser budget rejection, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "svc-a") || !strings.Contains(err.Error(), "svc-b") {
		t.Fatalf("budget rejection should mention both oversized images, got %q", err.Error())
	}
}

func buildBaselineOnlyScaleBundle(t *testing.T, layerSize int64) (
	bundlePath, baselineRef string, perImageSize int64,
) {
	t.Helper()

	baselinePath := filepath.Join(t.TempDir(), "baseline.tar")
	writeScaleBaselineArchive(t, baselinePath, layerSize)

	baselineRef = "oci-archive:" + baselinePath
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
	perImageSize = layers[0].Size

	targetManifest := baselineManifest
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
	return bundlePath, baselineRef, perImageSize
}

func writeScaleBaselineArchive(t *testing.T, path string, layerSize int64) {
	t.Helper()

	layerPath := filepath.Join(t.TempDir(), "layer.tar.gz")
	layerInfo, diffID := buildValidScaleLayerFile(t, layerPath, layerSize)

	configJSON := scaleConfigJSON(t, diffID)
	configDigest := digest.FromBytes(configJSON)
	configInfo := types.BlobInfo{
		Digest:    configDigest,
		Size:      int64(len(configJSON)),
		MediaType: ociConfigMediaType,
	}
	manifestBytes := scaleManifestJSON(t, configInfo, layerInfo)

	ref, err := ociarchive.NewReference(path, "diffah-scale-baseline:latest")
	if err != nil {
		t.Fatalf("new oci reference: %v", err)
	}
	dest, err := ref.NewImageDestination(context.Background(), nil)
	if err != nil {
		t.Fatalf("new image destination: %v", err)
	}
	defer dest.Close()

	lf, err := os.Open(layerPath)
	if err != nil {
		t.Fatalf("open scale layer: %v", err)
	}
	defer lf.Close()
	if _, err := dest.PutBlob(
		context.Background(),
		lf,
		layerInfo,
		none.NoCache,
		false,
	); err != nil {
		t.Fatalf("put scale layer: %v", err)
	}
	if _, err := dest.PutBlob(
		context.Background(),
		strings.NewReader(string(configJSON)),
		configInfo,
		none.NoCache,
		true,
	); err != nil {
		t.Fatalf("put scale config: %v", err)
	}
	if err := dest.PutManifest(context.Background(), manifestBytes, nil); err != nil {
		t.Fatalf("put scale manifest: %v", err)
	}
	if err := dest.PutSignatures(context.Background(), nil, nil); err != nil {
		t.Fatalf("put scale signatures: %v", err)
	}
	if err := dest.Commit(context.Background(), nil); err != nil {
		t.Fatalf("commit scale archive: %v", err)
	}
}

func buildValidScaleLayerFile(t *testing.T, path string, approxBytes int64) (types.BlobInfo, digest.Digest) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create scale layer: %v", err)
	}
	defer f.Close()

	compHash := sha256.New()
	compSize := &writeCounter{}
	gz, err := gzip.NewWriterLevel(io.MultiWriter(f, compHash, compSize), gzip.DefaultCompression)
	if err != nil {
		t.Fatalf("create gzip writer: %v", err)
	}
	gz.ModTime = time.Time{}
	gz.OS = 0xFF

	rawHash := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(gz, rawHash))
	const entrySize = 2 << 20
	remaining := approxBytes
	for idx := int64(0); remaining > 0; idx++ {
		size := int64(entrySize)
		if remaining < size {
			size = remaining
		}
		if err := writeScaleTarEntry(tw, idx, size); err != nil {
			t.Fatalf("write scale tar entry %d: %v", idx, err)
		}
		remaining -= size
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close scale tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close scale gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close scale layer: %v", err)
	}

	return types.BlobInfo{
			Digest:    digest.NewDigest(digest.SHA256, compHash),
			Size:      compSize.n,
			MediaType: ociLayerMediaType,
		},
		digest.NewDigest(digest.SHA256, rawHash)
}

func writeScaleTarEntry(tw *tar.Writer, idx, size int64) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:     fmt.Sprintf("file-%06d", idx),
		Mode:     0o644,
		Size:     size,
		ModTime:  time.Unix(1700000000, 0),
		Typeflag: tar.TypeReg,
		Uid:      0,
		Gid:      0,
	}); err != nil {
		return err
	}

	h := sha256.New()
	chunk := h.Sum([]byte(fmt.Sprintf("diffah-scale-%d", idx)))
	written := int64(0)
	for written < size {
		n := int64(len(chunk))
		if written+n > size {
			n = size - written
		}
		if _, err := tw.Write(chunk[:n]); err != nil {
			return err
		}
		written += n
		h.Reset()
		h.Write(chunk)
		chunk = h.Sum(chunk[:0])
	}
	return nil
}

type writeCounter struct {
	n int64
}

func (wc *writeCounter) Write(p []byte) (int, error) {
	wc.n += int64(len(p))
	return len(p), nil
}

func scaleConfigJSON(t *testing.T, layerDigest digest.Digest) []byte {
	t.Helper()

	raw, err := json.Marshal(struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		RootFS       struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
		Config  map[string]any `json:"config"`
		History []any          `json:"history"`
	}{
		Architecture: "amd64",
		OS:           "linux",
		RootFS: struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		}{
			Type:    "layers",
			DiffIDs: []string{layerDigest.String()},
		},
		Config:  map[string]any{},
		History: []any{},
	})
	if err != nil {
		t.Fatalf("marshal scale config: %v", err)
	}
	return raw
}

func scaleManifestJSON(t *testing.T, configInfo, layerInfo types.BlobInfo) []byte {
	t.Helper()

	raw, err := json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        struct {
			MediaType string        `json:"mediaType"`
			Size      int64         `json:"size"`
			Digest    digest.Digest `json:"digest"`
		} `json:"config"`
		Layers []struct {
			MediaType string        `json:"mediaType"`
			Size      int64         `json:"size"`
			Digest    digest.Digest `json:"digest"`
		} `json:"layers"`
	}{
		SchemaVersion: 2,
		MediaType:     ociManifestMT,
		Config: struct {
			MediaType string        `json:"mediaType"`
			Size      int64         `json:"size"`
			Digest    digest.Digest `json:"digest"`
		}{
			MediaType: configInfo.MediaType,
			Size:      configInfo.Size,
			Digest:    configInfo.Digest,
		},
		Layers: []struct {
			MediaType string        `json:"mediaType"`
			Size      int64         `json:"size"`
			Digest    digest.Digest `json:"digest"`
		}{{
			MediaType: layerInfo.MediaType,
			Size:      layerInfo.Size,
			Digest:    layerInfo.Digest,
		}},
	})
	if err != nil {
		t.Fatalf("marshal scale manifest: %v", err)
	}
	return raw
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

const (
	ociLayerMediaType  = "application/vnd.oci.image.layer.v1.tar+gzip"
	ociConfigMediaType = "application/vnd.oci.image.config.v1+json"
	ociManifestMT      = "application/vnd.oci.image.manifest.v1+json"
)
