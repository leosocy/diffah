package importer

import (
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestLayerSetDiff(t *testing.T) {
	expected := []LayerRef{
		{Digest: "sha256:a", Size: 10},
		{Digest: "sha256:b", Size: 20},
	}
	actual := []LayerRef{
		{Digest: "sha256:a", Size: 10},
		{Digest: "sha256:c", Size: 30},
	}
	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing) != 1 || missing[0] != "sha256:b" {
		t.Errorf("missing = %v, want [sha256:b]", missing)
	}
	if len(unexpected) != 1 || unexpected[0] != "sha256:c" {
		t.Errorf("unexpected = %v, want [sha256:c]", unexpected)
	}
}

func TestLayerSetDiff_Empty(t *testing.T) {
	missing, unexpected := layerSetDiff(nil, nil)
	if len(missing) != 0 || len(unexpected) != 0 {
		t.Errorf("expected empty diffs, got missing=%v unexpected=%v", missing, unexpected)
	}
}

func TestVerifyPerLayerSize_Matches(t *testing.T) {
	expected := []LayerRef{{Digest: "sha256:a", Size: 100}}
	actual := []LayerRef{{Digest: "sha256:a", Size: 100}}
	blobs := map[digest.Digest]diff.BlobEntry{"sha256:a": {Size: 100}}
	if err := verifyPerLayerSize(expected, actual, blobs); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyPerLayerSize_Mismatch(t *testing.T) {
	expected := []LayerRef{{Digest: "sha256:a", Size: 100}}
	actual := []LayerRef{{Digest: "sha256:a", Size: 999}}
	blobs := map[digest.Digest]diff.BlobEntry{"sha256:a": {Size: 100}}
	err := verifyPerLayerSize(expected, actual, blobs)
	if err == nil {
		t.Fatal("expected size mismatch error, got nil")
	}
}
