package diff

import (
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestSidecar_Marshal_MinimalValidBundle(t *testing.T) {
	s := Sidecar{
		Version:     SchemaVersionV1,
		Feature:     FeatureBundle,
		Tool:        "diffah",
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Blobs: map[digest.Digest]BlobEntry{
			"sha256:aa": {
				Size: 5, MediaType: "application/vnd.oci.image.manifest.v1+json",
				Encoding: EncodingFull, ArchiveSize: 5,
			},
		},
		Images: []ImageEntry{{
			Name: "service-a",
			Baseline: BaselineRef{
				ManifestDigest: "sha256:bb",
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
			Target: TargetRef{
				ManifestDigest: "sha256:aa",
				ManifestSize:   5,
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
		}},
	}
	out, err := s.Marshal()
	require.NoError(t, err)
	require.Contains(t, string(out), `"feature": "bundle"`)
	require.Contains(t, string(out), `"name": "service-a"`)
}
