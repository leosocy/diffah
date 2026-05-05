package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/diff/errs"
)

// writeFakeManifest synthesizes a minimal OCI-shaped manifest containing
// the given layer digests and writes it to <blobDir>/<algo>/<encoded>,
// returning the manifest's own digest. The shape matches what the real
// extract step puts on disk and is what extractLayerDigests parses.
func writeFakeManifest(t *testing.T, blobDir string, layerDigests []digest.Digest) digest.Digest {
	t.Helper()

	type layer struct {
		Digest digest.Digest `json:"digest"`
	}
	type manifest struct {
		SchemaVersion int     `json:"schemaVersion"`
		Layers        []layer `json:"layers"`
	}
	mf := manifest{SchemaVersion: 2}
	for _, ld := range layerDigests {
		mf.Layers = append(mf.Layers, layer{Digest: ld})
	}
	raw, err := json.Marshal(mf)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	mfDigest := digest.FromBytes(raw)
	dir := filepath.Join(blobDir, mfDigest.Algorithm().String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, mfDigest.Encoded()), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return mfDigest
}

// fakeShippedDigest returns a deterministic synthetic digest derived from
// label so two layers in the same test never collide.
func fakeShippedDigest(label string) digest.Digest {
	return digest.FromBytes([]byte("layer:" + label))
}

func TestEstimatePerImageRSS_TakesMaxAcrossShippedLayers(t *testing.T) {
	tmp := t.TempDir()
	blobDir := filepath.Join(tmp, "blobs")

	smallDigest := fakeShippedDigest("small")
	bigDigest := fakeShippedDigest("big")
	mfDigest := writeFakeManifest(t, blobDir, []digest.Digest{smallDigest, bigDigest})

	img := diff.ImageEntry{
		Name: "img",
		Target: diff.TargetRef{
			ManifestDigest: mfDigest,
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
	}
	blobs := map[digest.Digest]diff.BlobEntry{
		smallDigest: {Size: 16 << 20, Encoding: diff.EncodingFull, ArchiveSize: 16 << 20}, // 16 MiB → wl=27 → 256 MiB
		bigDigest:   {Size: 4 << 30, Encoding: diff.EncodingFull, ArchiveSize: 4 << 30},   // 4 GiB → wl=31 → 4 GiB
	}

	est, err := estimatePerImageRSS(img, blobDir, blobs, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if est != (4 << 30) {
		t.Fatalf("expected estimate to be driven by big layer (4 GiB); got %d bytes", est)
	}
}

func TestEstimatePerImageRSS_SkipsBaselineOnlyLayers(t *testing.T) {
	tmp := t.TempDir()
	blobDir := filepath.Join(tmp, "blobs")

	shippedDigest := fakeShippedDigest("shipped")
	baselineOnlyDigest := fakeShippedDigest("baseline-only")
	mfDigest := writeFakeManifest(t, blobDir, []digest.Digest{shippedDigest, baselineOnlyDigest})

	img := diff.ImageEntry{
		Name: "img",
		Target: diff.TargetRef{
			ManifestDigest: mfDigest,
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
	}
	// Only the shipped layer appears in blobs; baselineOnlyDigest is
	// resolved from the baseline source at apply time and contributes no
	// DecodeStream RSS.
	blobs := map[digest.Digest]diff.BlobEntry{
		shippedDigest: {Size: 16 << 20, Encoding: diff.EncodingFull, ArchiveSize: 16 << 20},
	}

	est, err := estimatePerImageRSS(img, blobDir, blobs, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 16 MiB → wl=27 → 256 MiB. The baseline-only layer (which would
	// otherwise dominate at any size) must NOT contribute.
	if est != (256 << 20) {
		t.Fatalf("expected only-shipped-layer to drive estimate (256 MiB); got %d bytes", est)
	}
}

func TestCheckSingleImageFitsInBudget_RejectsOversize(t *testing.T) {
	tmp := t.TempDir()
	blobDir := filepath.Join(tmp, "blobs")

	bigDigest := fakeShippedDigest("4g")
	mfDigest := writeFakeManifest(t, blobDir, []digest.Digest{bigDigest})

	img := diff.ImageEntry{
		Name: "huge-image",
		Target: diff.TargetRef{
			ManifestDigest: mfDigest,
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
	}
	blobs := map[digest.Digest]diff.BlobEntry{
		bigDigest: {Size: 4 << 30, Encoding: diff.EncodingFull, ArchiveSize: 4 << 30}, // wl=31 → 4 GiB
	}

	const budget int64 = 256 << 20 // 256 MiB
	err := checkSingleImageFitsInBudget([]diff.ImageEntry{img}, blobDir, blobs, 0, budget)
	if err == nil {
		t.Fatalf("expected user-facing error for oversized image, got nil")
	}
	var cat errs.Categorized
	if !errors.As(err, &cat) || cat.Category() != errs.CategoryUser {
		t.Fatalf("expected CategoryUser error, got %T %v", err, err)
	}
	if !contains(err.Error(), "huge-image") {
		t.Fatalf("expected error to mention image name 'huge-image'; got %q", err.Error())
	}
	if !contains(err.Error(), fmt.Sprintf("%d", budget)) {
		t.Fatalf("expected error to mention budget bytes %d; got %q", budget, err.Error())
	}
}

func TestCheckSingleImageFitsInBudget_BudgetZeroOptsOut(t *testing.T) {
	tmp := t.TempDir()
	blobDir := filepath.Join(tmp, "blobs")

	// Synthesize an image whose estimate would be huge (4 GiB) — budget=0
	// must opt out regardless.
	bigDigest := fakeShippedDigest("4g-no-budget")
	mfDigest := writeFakeManifest(t, blobDir, []digest.Digest{bigDigest})

	img := diff.ImageEntry{
		Name: "img",
		Target: diff.TargetRef{
			ManifestDigest: mfDigest,
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
	}
	blobs := map[digest.Digest]diff.BlobEntry{
		bigDigest: {Size: 4 << 30, Encoding: diff.EncodingFull, ArchiveSize: 4 << 30},
	}

	if err := checkSingleImageFitsInBudget([]diff.ImageEntry{img}, blobDir, blobs, 0, 0); err != nil {
		t.Fatalf("expected nil with budget=0; got %v", err)
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
