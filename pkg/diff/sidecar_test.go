package diff

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

const (
	testCodecZstdPatch  = "zstd-patch"
	testPatchFromDigest = digest.Digest("sha256:bb")
)

func minimalValidBundle(t *testing.T) Sidecar {
	t.Helper()
	return Sidecar{
		Version: SchemaVersionV1, Feature: FeatureBundle, Tool: "diffah",
		ToolVersion: "test", CreatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Platform: "linux/amd64",
		Blobs: map[digest.Digest]BlobEntry{
			"sha256:aa": {Size: 5, MediaType: "application/vnd.oci.image.manifest.v1+json",
				Encoding: EncodingFull, ArchiveSize: 5},
		},
		Images: []ImageEntry{{
			Name:     "service-a",
			Baseline: BaselineRef{ManifestDigest: testPatchFromDigest, MediaType: "application/vnd.oci.image.manifest.v1+json"},
			Target:   TargetRef{ManifestDigest: "sha256:aa", ManifestSize: 5, MediaType: "application/vnd.oci.image.manifest.v1+json"},
		}},
	}
}

func TestSidecar_Marshal_MinimalValidBundle(t *testing.T) {
	s := minimalValidBundle(t)
	out, err := s.Marshal()
	require.NoError(t, err)
	require.Contains(t, string(out), `"feature": "bundle"`)
	require.Contains(t, string(out), `"name": "service-a"`)
}

func TestSidecar_Validate_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*Sidecar)
		reason string
	}{
		{"empty platform", func(s *Sidecar) { s.Platform = "" }, "platform"},
		{"nil blobs", func(s *Sidecar) { s.Blobs = nil }, "blobs"},
		{"empty images", func(s *Sidecar) { s.Images = nil }, "images"},
		{"bad name", func(s *Sidecar) { s.Images[0].Name = "-leading" }, "name"},
		{"duplicate name", func(s *Sidecar) {
			dup := s.Images[0]
			s.Images = append(s.Images, dup)
		}, "unique"},
		{"target digest not in blobs", func(s *Sidecar) {
			s.Images[0].Target.ManifestDigest = "sha256:ff"
		}, "blobs"},
		{"full blob with codec", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Codec = testCodecZstdPatch
			s.Blobs["sha256:aa"] = e
		}, "codec"},
		{"full blob with patch_from_digest", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.PatchFromDigest = testPatchFromDigest
			s.Blobs["sha256:aa"] = e
		}, "codec"},
		{"full blob archive_size != size", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.ArchiveSize = 99
			s.Blobs["sha256:aa"] = e
		}, "archive_size"},
		{"patch blob missing codec", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Encoding = EncodingPatch
			e.PatchFromDigest = testPatchFromDigest
			e.ArchiveSize = 2
			e.Size = 5
			s.Blobs["sha256:aa"] = e
		}, "codec"},
		{"patch blob missing patch_from_digest", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Encoding = EncodingPatch
			e.Codec = testCodecZstdPatch
			e.ArchiveSize = 2
			e.Size = 5
			s.Blobs["sha256:aa"] = e
		}, "patch_from_digest"},
		{"patch blob archive_size >= size", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Encoding = EncodingPatch
			e.Codec = testCodecZstdPatch
			e.PatchFromDigest = testPatchFromDigest
			e.ArchiveSize = 5
			e.Size = 5
			s.Blobs["sha256:aa"] = e
		}, "archive_size"},
		{"patch blob archive_size <= 0", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Encoding = EncodingPatch
			e.Codec = testCodecZstdPatch
			e.PatchFromDigest = testPatchFromDigest
			e.ArchiveSize = 0
			e.Size = 5
			s.Blobs["sha256:aa"] = e
		}, "archive_size"},
		{"empty encoding", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Encoding = ""
			s.Blobs["sha256:aa"] = e
		}, "not recognized"},
		{"missing target manifest_digest", func(s *Sidecar) {
			s.Images[0].Target.ManifestDigest = ""
		}, "manifest_digest"},
		{"missing baseline manifest_digest", func(s *Sidecar) {
			s.Images[0].Baseline.ManifestDigest = ""
		}, "manifest_digest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := minimalValidBundle(t)
			tc.mut(&s)
			_, err := s.Marshal()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.reason)
		})
	}
}

func TestParseSidecar_RejectsPhase1(t *testing.T) {
	_, err := ParseSidecar([]byte(`{"version":"v1","tool":"diffah"}`))
	var p1 *ErrPhase1Archive
	require.ErrorAs(t, err, &p1)
}

func TestParseSidecar_RejectsUnknownVersion(t *testing.T) {
	_, err := ParseSidecar([]byte(`{"version":"v9","feature":"bundle","tool":"diffah","platform":"linux/amd64","blobs":{},"images":[]}`))
	var uv *ErrUnknownBundleVersion
	require.ErrorAs(t, err, &uv)
}

func TestParseSidecar_RejectsInvalidJSON(t *testing.T) {
	_, err := ParseSidecar([]byte(`{bad`))
	var ibf *ErrInvalidBundleFormat
	require.ErrorAs(t, err, &ibf)
}

func TestSidecar_Marshal_DeterministicOrder(t *testing.T) {
	s := minimalValidBundle(t)
	s.Blobs["sha256:cc"] = BlobEntry{Size: 3, MediaType: "text/plain",
		Encoding: EncodingFull, ArchiveSize: 3}
	s.Blobs[testPatchFromDigest] = BlobEntry{Size: 4, MediaType: "text/plain",
		Encoding: EncodingFull, ArchiveSize: 4}

	first, err := s.Marshal()
	require.NoError(t, err)
	second, err := s.Marshal()
	require.NoError(t, err)
	require.Equal(t, first, second, "marshal must be deterministic")

	order := orderOfTopLevelBlobsKeys(t, first)
	require.Equal(t, []string{"sha256:aa", "sha256:bb", "sha256:cc"}, order)
}

func TestSidecar_RoundTrip_PreservesAllFields(t *testing.T) {
	original := minimalValidBundle(t)
	raw, err := original.Marshal()
	require.NoError(t, err)
	parsed, err := ParseSidecar(raw)
	require.NoError(t, err)
	require.Equal(t, original, *parsed)
}

func TestSidecar_RequiresZstd(t *testing.T) {
	t.Run("all full returns false", func(t *testing.T) {
		s := Sidecar{
			Blobs: map[digest.Digest]BlobEntry{
				"sha256:a": {Encoding: EncodingFull},
				"sha256:b": {Encoding: EncodingFull},
			},
		}
		require.False(t, s.RequiresZstd())
	})
	t.Run("any patch returns true", func(t *testing.T) {
		s := Sidecar{
			Blobs: map[digest.Digest]BlobEntry{
				"sha256:a": {Encoding: EncodingFull},
				"sha256:b": {Encoding: EncodingPatch},
			},
		}
		require.True(t, s.RequiresZstd())
	})
	t.Run("empty blobs returns false", func(t *testing.T) {
		s := Sidecar{Blobs: map[digest.Digest]BlobEntry{}}
		require.False(t, s.RequiresZstd())
	})
}

func orderOfTopLevelBlobsKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	var keys []string
	depth := 0
	inBlobs := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				depth++
			case '}':
				if inBlobs && depth == 2 {
					return keys
				}
				depth--
			}
		case string:
			if depth == 1 && v == "blobs" {
				inBlobs = true
				continue
			}
			if inBlobs && depth == 2 {
				keys = append(keys, v)
				var discard BlobEntry
				_ = dec.Decode(&discard)
			}
		}
	}
	return keys
}
