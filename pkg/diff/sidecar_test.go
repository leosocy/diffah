package diff

import (
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
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
			Baseline: BaselineRef{ManifestDigest: "sha256:bb", MediaType: "application/vnd.oci.image.manifest.v1+json"},
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
			e.Codec = "zstd-patch"
			s.Blobs["sha256:aa"] = e
		}, "codec"},
		{"full blob with patch_from_digest", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.PatchFromDigest = "sha256:bb"
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
			e.PatchFromDigest = "sha256:bb"
			e.ArchiveSize = 2
			e.Size = 5
			s.Blobs["sha256:aa"] = e
		}, "codec"},
		{"patch blob missing patch_from_digest", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Encoding = EncodingPatch
			e.Codec = "zstd-patch"
			e.ArchiveSize = 2
			e.Size = 5
			s.Blobs["sha256:aa"] = e
		}, "patch_from_digest"},
		{"patch blob archive_size >= size", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Encoding = EncodingPatch
			e.Codec = "zstd-patch"
			e.PatchFromDigest = "sha256:bb"
			e.ArchiveSize = 5
			e.Size = 5
			s.Blobs["sha256:aa"] = e
		}, "archive_size"},
		{"patch blob archive_size <= 0", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Encoding = EncodingPatch
			e.Codec = "zstd-patch"
			e.PatchFromDigest = "sha256:bb"
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
