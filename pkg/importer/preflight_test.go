package importer

import (
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
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
			{Name: "svc-a", Target: diff.TargetRef{
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
