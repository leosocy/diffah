package diff

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func validLegacySidecar() LegacySidecar {
	return LegacySidecar{
		Version:     "v1",
		Tool:        "diffah",
		ToolVersion: "v0.1.0",
		CreatedAt:   time.Date(2026, 4, 20, 13, 21, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Target: LegacyTargetRef{
			ManifestDigest: digest.Digest("sha256:aaa"),
			ManifestSize:   1234,
			MediaType:      "application/vnd.docker.distribution.manifest.v2+json",
		},
		Baseline: LegacyBaselineRef{
			ManifestDigest: digest.Digest("sha256:bbb"),
			MediaType:      "application/vnd.docker.distribution.manifest.v2+json",
			SourceHint:     "docker://x/y:v1",
		},
		RequiredFromBaseline: []BlobRef{{Digest: "sha256:ccc", Size: 10, MediaType: "m"}},
		ShippedInDelta: []BlobRef{{
			Digest:      "sha256:eee",
			Size:        20,
			MediaType:   "m",
			Encoding:    EncodingFull,
			ArchiveSize: 20,
		}},
	}
}

func TestLegacySidecar_MarshalUnmarshalRoundTrip(t *testing.T) {
	orig := validLegacySidecar()

	raw, err := orig.Marshal()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"version": "v1"`)

	back, err := ParseLegacySidecar(raw)
	require.NoError(t, err)
	require.Equal(t, orig, *back)
}

func TestParseLegacySidecar_RejectsUnknownVersion(t *testing.T) {
	s := validLegacySidecar()
	s.Version = "v99"
	raw, err := s.Marshal()
	require.NoError(t, err)

	_, err = ParseLegacySidecar(raw)
	var ve *ErrUnsupportedSchemaVersion
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "v99", ve.Got)
}

func TestLegacySidecar_MarshalRejectsMissingPlatform(t *testing.T) {
	s := validLegacySidecar()
	s.Platform = ""

	_, err := s.Marshal()
	var se *ErrSidecarSchema
	require.ErrorAs(t, err, &se)
}

func TestParseLegacySidecar_RejectsMissingRequiredFields(t *testing.T) {
	cases := map[string]func(s *LegacySidecar){
		"no platform":       func(s *LegacySidecar) { s.Platform = "" },
		"no target digest":  func(s *LegacySidecar) { s.Target.ManifestDigest = "" },
		"no required slice": func(s *LegacySidecar) { s.RequiredFromBaseline = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := validLegacySidecar()
			// Skip Marshal validation by encoding directly.
			mutate(&s)
			raw, err := json.Marshal(s)
			require.NoError(t, err)

			_, err = ParseLegacySidecar(raw)
			var se *ErrSidecarSchema
			require.ErrorAs(t, err, &se)
		})
	}
}

func TestParseLegacySidecar_RejectsMalformedJSON(t *testing.T) {
	_, err := ParseLegacySidecar([]byte("not json"))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "decode"))
}

func TestLegacySidecar_MarshalIsPrettyPrinted(t *testing.T) {
	s := validLegacySidecar()
	raw, err := s.Marshal()
	require.NoError(t, err)
	require.True(t, json.Valid(raw))
	require.Contains(t, string(raw), "\n  ")
}

// --- Intra-layer validation tests (Task 2) ---

func validLegacySidecarWithEncoding() LegacySidecar {
	s := validLegacySidecar()
	s.ShippedInDelta = []BlobRef{
		{
			Digest:      "sha256:eee",
			Size:        20,
			MediaType:   "m",
			Encoding:    EncodingFull,
			ArchiveSize: 20,
		},
	}
	return s
}

func TestLegacySidecar_Rejects_ShippedEntry_MissingEncoding(t *testing.T) {
	s := validLegacySidecarWithEncoding()
	s.ShippedInDelta[0].Encoding = ""
	_, err := s.Marshal()
	var ve *ErrSidecarSchema
	require.ErrorAs(t, err, &ve)
	require.Contains(t, err.Error(), "encoding")
}

func TestLegacySidecar_Rejects_PatchEntry_MissingFromDigest(t *testing.T) {
	s := validLegacySidecarWithEncoding()
	s.ShippedInDelta[0] = BlobRef{
		Digest: "sha256:eee", Size: 20, MediaType: "m",
		Encoding:    EncodingPatch,
		Codec:       "zstd-patch",
		ArchiveSize: 5,
		// PatchFromDigest intentionally empty
	}
	_, err := s.Marshal()
	var ve *ErrSidecarSchema
	require.ErrorAs(t, err, &ve)
	require.Contains(t, err.Error(), "patch_from_digest")
}

func TestLegacySidecar_Rejects_PatchEntry_MissingCodec(t *testing.T) {
	s := validLegacySidecarWithEncoding()
	s.ShippedInDelta[0] = BlobRef{
		Digest: "sha256:eee", Size: 20, MediaType: "m",
		Encoding:        EncodingPatch,
		PatchFromDigest: "sha256:ref",
		ArchiveSize:     5,
	}
	_, err := s.Marshal()
	var ve *ErrSidecarSchema
	require.ErrorAs(t, err, &ve)
	require.Contains(t, err.Error(), "codec")
}

func TestLegacySidecar_Rejects_PatchEntry_ArchiveSize_NotLessThanSize(t *testing.T) {
	s := validLegacySidecarWithEncoding()
	s.ShippedInDelta[0] = BlobRef{
		Digest: "sha256:eee", Size: 20, MediaType: "m",
		Encoding:        EncodingPatch,
		Codec:           "zstd-patch",
		PatchFromDigest: "sha256:ref",
		ArchiveSize:     20, // must be < Size for patch entries
	}
	_, err := s.Marshal()
	var ve *ErrSidecarSchema
	require.ErrorAs(t, err, &ve)
	require.Contains(t, err.Error(), "archive_size")
}

func TestLegacySidecar_Rejects_FullEntry_Has_PatchFromDigest(t *testing.T) {
	s := validLegacySidecarWithEncoding()
	s.ShippedInDelta[0].PatchFromDigest = "sha256:ref"
	_, err := s.Marshal()
	var ve *ErrSidecarSchema
	require.ErrorAs(t, err, &ve)
}

func TestLegacySidecar_Rejects_FullEntry_Archive_NotEqualSize(t *testing.T) {
	s := validLegacySidecarWithEncoding()
	s.ShippedInDelta[0].ArchiveSize = 19
	_, err := s.Marshal()
	var ve *ErrSidecarSchema
	require.ErrorAs(t, err, &ve)
}

func TestLegacySidecar_Rejects_RequiredEntry_HasIntraLayerFields(t *testing.T) {
	s := validLegacySidecarWithEncoding()
	s.RequiredFromBaseline[0].Encoding = EncodingFull
	_, err := s.Marshal()
	var ve *ErrSidecarSchema
	require.ErrorAs(t, err, &ve)
	require.Contains(t, err.Error(), "required_from_baseline")
}

func TestLegacySidecar_PatchEntry_MarshalsCorrectly(t *testing.T) {
	s := validLegacySidecarWithEncoding()
	s.ShippedInDelta[0] = BlobRef{
		Digest: "sha256:eee", Size: 1000, MediaType: "m",
		Encoding:        EncodingPatch,
		Codec:           "zstd-patch",
		PatchFromDigest: "sha256:ref",
		ArchiveSize:     123,
	}
	raw, err := s.Marshal()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"encoding": "patch"`)
	require.Contains(t, string(raw), `"codec": "zstd-patch"`)
	require.Contains(t, string(raw), `"patch_from_digest": "sha256:ref"`)
	require.Contains(t, string(raw), `"archive_size": 123`)

	// Required entry has none of the four fields in the JSON output.
	require.NotContains(t, string(raw), `"encoding": ""`)
}
