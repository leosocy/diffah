package diff

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func validSidecar() Sidecar {
	return Sidecar{
		Version:     "v1",
		Tool:        "diffah",
		ToolVersion: "v0.1.0",
		CreatedAt:   time.Date(2026, 4, 20, 13, 21, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Target: ImageRef{
			ManifestDigest: digest.Digest("sha256:aaa"),
			ManifestSize:   1234,
			MediaType:      "application/vnd.docker.distribution.manifest.v2+json",
		},
		Baseline: BaselineRef{
			ManifestDigest: digest.Digest("sha256:bbb"),
			MediaType:      "application/vnd.docker.distribution.manifest.v2+json",
			SourceHint:     "docker://x/y:v1",
		},
		RequiredFromBaseline: []BlobRef{{Digest: "sha256:ccc", Size: 10, MediaType: "m"}},
		ShippedInDelta:       []BlobRef{{Digest: "sha256:eee", Size: 20, MediaType: "m"}},
	}
}

func TestSidecar_MarshalUnmarshalRoundTrip(t *testing.T) {
	orig := validSidecar()

	raw, err := orig.Marshal()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"version": "v1"`)

	back, err := ParseSidecar(raw)
	require.NoError(t, err)
	require.Equal(t, orig, *back)
}

func TestParseSidecar_RejectsUnknownVersion(t *testing.T) {
	s := validSidecar()
	s.Version = "v99"
	raw, err := s.Marshal()
	require.NoError(t, err)

	_, err = ParseSidecar(raw)
	var ve *ErrUnsupportedSchemaVersion
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "v99", ve.Got)
}

func TestSidecar_MarshalRejectsMissingPlatform(t *testing.T) {
	s := validSidecar()
	s.Platform = ""

	_, err := s.Marshal()
	var se *ErrSidecarSchema
	require.ErrorAs(t, err, &se)
}

func TestParseSidecar_RejectsMissingRequiredFields(t *testing.T) {
	cases := map[string]func(s *Sidecar){
		"no platform":       func(s *Sidecar) { s.Platform = "" },
		"no target digest":  func(s *Sidecar) { s.Target.ManifestDigest = "" },
		"no required slice": func(s *Sidecar) { s.RequiredFromBaseline = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := validSidecar()
			// Skip Marshal validation by encoding directly.
			mutate(&s)
			raw, err := json.Marshal(s)
			require.NoError(t, err)

			_, err = ParseSidecar(raw)
			var se *ErrSidecarSchema
			require.ErrorAs(t, err, &se)
		})
	}
}

func TestParseSidecar_RejectsMalformedJSON(t *testing.T) {
	_, err := ParseSidecar([]byte("not json"))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "decode"))
}

func TestSidecar_MarshalIsPrettyPrinted(t *testing.T) {
	s := validSidecar()
	raw, err := s.Marshal()
	require.NoError(t, err)
	require.True(t, json.Valid(raw))
	require.Contains(t, string(raw), "\n  ")
}
