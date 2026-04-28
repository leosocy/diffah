package importer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
)

const (
	testPreflightSvcA = "svc-a"
	testPreflightSvcB = "svc-b"
)

func TestComputeRequiredBaselineDigests(t *testing.T) {
	mfBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:shipped-full","size":100},{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:shipped-patch","size":50},{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:reuse","size":200}]}`)
	mfDigest := digest.FromBytes(mfBytes)

	sidecar := &diff.Sidecar{
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest:                              {Encoding: diff.EncodingFull, Size: int64(len(mfBytes))},
			digest.Digest("sha256:shipped-full"):  {Encoding: diff.EncodingFull, Size: 100},
			digest.Digest("sha256:shipped-patch"): {Encoding: diff.EncodingPatch, PatchFromDigest: digest.Digest("sha256:patch-src"), Size: 50},
			digest.Digest("sha256:cfg"):           {Encoding: diff.EncodingFull, Size: 10},
		},
		Images: []diff.ImageEntry{
			{Name: testPreflightSvcA, Target: diff.TargetRef{
				ManifestDigest: mfDigest,
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			}},
		},
	}

	bundle := &extractedBundle{
		blobDir: writeBlobToTempDir(t, mfDigest, mfBytes),
		sidecar: sidecar,
	}

	reuse, patchSrcs, err := computeRequiredBaselineDigests(bundle, sidecar.Images[0])
	if err != nil {
		t.Fatal(err)
	}

	wantReuse := []digest.Digest{"sha256:reuse"}
	wantPatchSrcs := []digest.Digest{"sha256:patch-src"}
	if !equalDigestSets(reuse, wantReuse) {
		t.Errorf("reuse = %v, want %v", reuse, wantReuse)
	}
	if !equalDigestSets(patchSrcs, wantPatchSrcs) {
		t.Errorf("patchSrcs = %v, want %v", patchSrcs, wantPatchSrcs)
	}
}

func equalDigestSets(a, b []digest.Digest) bool {
	if len(a) != len(b) {
		return false
	}
	want := make(map[digest.Digest]struct{}, len(b))
	for _, d := range b {
		want[d] = struct{}{}
	}
	for _, d := range a {
		if _, ok := want[d]; !ok {
			return false
		}
	}
	return true
}

func TestScanOneImage_AllOK(t *testing.T) {
	bundle, img, rb := buildPreflightFixture(t, []digest.Digest{
		"sha256:patch-src", "sha256:reuse", "sha256:cfg",
	})
	r := scanOneImage(context.Background(), bundle, img, rb)
	if r.Status != PreflightOK {
		t.Errorf("Status = %v, want PreflightOK; result=%+v", r.Status, r)
	}
}

func TestScanOneImage_B1_OnlyPatchSrcMissing(t *testing.T) {
	bundle, img, rb := buildPreflightFixture(t, []digest.Digest{
		"sha256:reuse", "sha256:cfg",
	})
	r := scanOneImage(context.Background(), bundle, img, rb)
	if r.Status != PreflightMissingPatchSource {
		t.Errorf("Status = %v, want PreflightMissingPatchSource", r.Status)
	}
	if len(r.MissingPatchSources) != 1 || r.MissingPatchSources[0] != "sha256:patch-src" {
		t.Errorf("MissingPatchSources = %v", r.MissingPatchSources)
	}
}

func TestScanOneImage_B2_OnlyReuseMissing(t *testing.T) {
	bundle, img, rb := buildPreflightFixture(t, []digest.Digest{
		"sha256:patch-src", "sha256:cfg",
	})
	r := scanOneImage(context.Background(), bundle, img, rb)
	if r.Status != PreflightMissingReuseLayer {
		t.Errorf("Status = %v, want PreflightMissingReuseLayer", r.Status)
	}
	if len(r.MissingReuseLayers) != 1 || r.MissingReuseLayers[0] != "sha256:reuse" {
		t.Errorf("MissingReuseLayers = %v", r.MissingReuseLayers)
	}
}

func TestScanOneImage_BothB1AndB2(t *testing.T) {
	bundle, img, rb := buildPreflightFixture(t, []digest.Digest{
		"sha256:cfg",
	})
	r := scanOneImage(context.Background(), bundle, img, rb)
	if r.Status != PreflightMissingPatchSource {
		t.Errorf("when both missing, Status should be MissingPatchSource (B1 dominates); got %v", r.Status)
	}
	if len(r.MissingPatchSources) != 1 || len(r.MissingReuseLayers) != 1 {
		t.Errorf("both slices should be filled independently; got patch=%v reuse=%v",
			r.MissingPatchSources, r.MissingReuseLayers)
	}
}

// buildPreflightFixture constructs an extractedBundle (with the same shape as
// TestComputeRequiredBaselineDigests) plus a resolvedBaseline whose manifest
// bytes report baselineDigests as the layer / config digests.
func buildPreflightFixture(t *testing.T, baselineDigests []digest.Digest) (
	*extractedBundle, diff.ImageEntry, resolvedBaseline,
) {
	t.Helper()
	mfBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:shipped-full","size":100},{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:shipped-patch","size":50},{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:reuse","size":200}]}`)
	mfDigest := digest.FromBytes(mfBytes)
	sidecar := &diff.Sidecar{
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest:               {Encoding: diff.EncodingFull, Size: int64(len(mfBytes))},
			"sha256:shipped-full":  {Encoding: diff.EncodingFull, Size: 100},
			"sha256:shipped-patch": {Encoding: diff.EncodingPatch, PatchFromDigest: "sha256:patch-src", Size: 50},
			"sha256:cfg":           {Encoding: diff.EncodingFull, Size: 10},
		},
		Images: []diff.ImageEntry{{
			Name:   testPreflightSvcA,
			Target: diff.TargetRef{ManifestDigest: mfDigest, MediaType: "application/vnd.oci.image.manifest.v1+json"},
		}},
	}
	bundle := &extractedBundle{
		blobDir: writeBlobToTempDir(t, mfDigest, mfBytes),
		sidecar: sidecar,
	}
	rb := resolvedBaseline{
		Name:          testPreflightSvcA,
		ManifestBytes: synthBaselineManifest(t, baselineDigests, "sha256:cfg"),
		ManifestMime:  "application/vnd.oci.image.manifest.v1+json",
	}
	return bundle, sidecar.Images[0], rb
}

// synthBaselineManifest builds a synthetic OCI manifest whose config digest is
// configDigest (or a generated placeholder if configDigest is absent from
// digests) and whose layers list every other digest in digests. Used to make
// preflight think the baseline contains exactly the listed digest set.
func synthBaselineManifest(t *testing.T, digests []digest.Digest, configDigest digest.Digest) []byte {
	t.Helper()
	cfg := configDigest
	hasCfg := false
	for _, d := range digests {
		if d == cfg {
			hasCfg = true
			break
		}
	}
	if !hasCfg {
		cfg = "sha256:synth-cfg"
	}
	type descriptor struct {
		MediaType string        `json:"mediaType"`
		Digest    digest.Digest `json:"digest"`
		Size      int64         `json:"size"`
	}
	type m struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Config        descriptor   `json:"config"`
		Layers        []descriptor `json:"layers"`
	}
	mf := m{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        descriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: cfg, Size: 10},
	}
	for _, d := range digests {
		if d == cfg {
			continue
		}
		mf.Layers = append(mf.Layers, descriptor{
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Digest:    d, Size: 100,
		})
	}
	raw, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRunPreflight_MultiImage_PartialFailures(t *testing.T) {
	bundle, resolved := buildMultiImagePreflightFixture(t)
	results, anyFail, err := RunPreflight(context.Background(), bundle, resolved, progress.NewDiscard())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if !anyFail {
		t.Error("anyFail should be true")
	}
	if results[0].Status != PreflightOK {
		t.Errorf("svc-a Status = %v, want OK", results[0].Status)
	}
	if results[1].Status != PreflightMissingReuseLayer {
		t.Errorf("svc-b Status = %v, want MissingReuseLayer", results[1].Status)
	}
}

// buildMultiImagePreflightFixture extends buildPreflightFixture to produce
// two images, each with its own target manifest blob and synthetic baseline
// manifest. svc-a's baseline contains the full required digest set; svc-b's
// baseline is missing "sha256:reuse-b".
func buildMultiImagePreflightFixture(t *testing.T) (*extractedBundle, []resolvedBaseline) {
	t.Helper()
	mfA := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg-a","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:reuse-a","size":100}]}`)
	mfADigest := digest.FromBytes(mfA)
	mfB := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg-b","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:reuse-b","size":200}]}`)
	mfBDigest := digest.FromBytes(mfB)

	dir := t.TempDir()
	algoDir := filepath.Join(dir, "sha256")
	if err := os.MkdirAll(algoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(algoDir, mfADigest.Encoded()), mfA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(algoDir, mfBDigest.Encoded()), mfB, 0o644); err != nil {
		t.Fatal(err)
	}

	sidecar := &diff.Sidecar{
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfADigest:      {Encoding: diff.EncodingFull, Size: int64(len(mfA))},
			mfBDigest:      {Encoding: diff.EncodingFull, Size: int64(len(mfB))},
			"sha256:cfg-a": {Encoding: diff.EncodingFull, Size: 10},
			"sha256:cfg-b": {Encoding: diff.EncodingFull, Size: 10},
		},
		Images: []diff.ImageEntry{
			{Name: testPreflightSvcA, Target: diff.TargetRef{ManifestDigest: mfADigest, MediaType: "application/vnd.oci.image.manifest.v1+json"}},
			{Name: testPreflightSvcB, Target: diff.TargetRef{ManifestDigest: mfBDigest, MediaType: "application/vnd.oci.image.manifest.v1+json"}},
		},
	}
	bundle := &extractedBundle{blobDir: dir, sidecar: sidecar}

	mime := "application/vnd.oci.image.manifest.v1+json"
	resolved := []resolvedBaseline{
		{
			Name:          testPreflightSvcA,
			ManifestBytes: synthBaselineManifest(t, []digest.Digest{"sha256:cfg-a", "sha256:reuse-a"}, "sha256:cfg-a"),
			ManifestMime:  mime,
		},
		{
			Name:          testPreflightSvcB,
			ManifestBytes: synthBaselineManifest(t, []digest.Digest{"sha256:cfg-b"}, "sha256:cfg-b"),
			ManifestMime:  mime,
		},
	}
	return bundle, resolved
}

func TestPreflightStatusString(t *testing.T) {
	cases := []struct {
		s    PreflightStatus
		want string
	}{
		{PreflightOK, "ok"},
		{PreflightMissingPatchSource, "missing-patch-source"},
		{PreflightMissingReuseLayer, "missing-reuse-layer"},
		{PreflightError, "error"},
		{PreflightSchemaError, "schema-error"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("PreflightStatus(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestPreflightResultToErr_B1RoundTrip(t *testing.T) {
	r := PreflightResult{
		ImageName:           testPreflightSvcA,
		Status:              PreflightMissingPatchSource,
		MissingPatchSources: []digest.Digest{"sha256:patch-src", "sha256:other"},
	}
	err := preflightResultToErr(r)
	var pe *ErrMissingPatchSource
	if !errors.As(err, &pe) {
		t.Fatalf("got %T, want *ErrMissingPatchSource", err)
	}
	if pe.ImageName != testPreflightSvcA {
		t.Errorf("ImageName = %q, want svc-a", pe.ImageName)
	}
	if pe.PatchFromDigest != "sha256:patch-src" {
		t.Errorf("PatchFromDigest = %q, want sha256:patch-src", pe.PatchFromDigest)
	}
}

func TestPreflightResultToErr_B2RoundTrip(t *testing.T) {
	r := PreflightResult{
		ImageName:          testPreflightSvcB,
		Status:             PreflightMissingReuseLayer,
		MissingReuseLayers: []digest.Digest{"sha256:reuse"},
	}
	err := preflightResultToErr(r)
	var be *ErrMissingBaselineReuseLayer
	if !errors.As(err, &be) {
		t.Fatalf("got %T, want *ErrMissingBaselineReuseLayer", err)
	}
	if be.ImageName != testPreflightSvcB || be.LayerDigest != "sha256:reuse" {
		t.Errorf("got %+v, want svc-b/sha256:reuse", be)
	}
}

func TestPreflightResultToErr_OKReturnsNil(t *testing.T) {
	if got := preflightResultToErr(PreflightResult{Status: PreflightOK}); got != nil {
		t.Errorf("OK should map to nil error, got %v", got)
	}
}
