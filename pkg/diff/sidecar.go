package diff

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/opencontainers/go-digest"
)

var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

const SidecarFilename = "diffah.json"

const SchemaVersionV1 = "v1"

const FeatureBundle = "bundle"

type TargetRef struct {
	ManifestDigest digest.Digest `json:"manifest_digest"`
	ManifestSize   int64         `json:"manifest_size"`
	MediaType      string        `json:"media_type"`
}

type BaselineRef struct {
	ManifestDigest digest.Digest `json:"manifest_digest"`
	MediaType      string        `json:"media_type"`
	SourceHint     string        `json:"source_hint,omitempty"`
}

type ImageEntry struct {
	Name     string      `json:"name"`
	Baseline BaselineRef `json:"baseline"`
	Target   TargetRef   `json:"target"`
}

type BlobEntry struct {
	Size            int64         `json:"size"`
	MediaType       string        `json:"media_type"`
	Encoding        Encoding      `json:"encoding"`
	Codec           string        `json:"codec,omitempty"`
	PatchFromDigest digest.Digest `json:"patch_from_digest,omitempty"`
	ArchiveSize     int64         `json:"archive_size"`
}

type Sidecar struct {
	Version     string                      `json:"version"`
	Feature     string                      `json:"feature"`
	Tool        string                      `json:"tool"`
	ToolVersion string                      `json:"tool_version"`
	CreatedAt   time.Time                   `json:"created_at"`
	Platform    string                      `json:"platform"`
	Blobs       map[digest.Digest]BlobEntry `json:"blobs"`
	Images      []ImageEntry                `json:"images"`
}

func (s Sidecar) validate() error {
	if s.Platform == "" {
		return &ErrSidecarSchema{Reason: "platform is required"}
	}
	if s.Blobs == nil {
		return &ErrSidecarSchema{Reason: "blobs is required (may be empty)"}
	}
	if len(s.Images) == 0 {
		return &ErrSidecarSchema{Reason: "images must contain at least one entry"}
	}
	seen := make(map[string]struct{}, len(s.Images))
	for i, img := range s.Images {
		if !nameRegex.MatchString(img.Name) {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].name %q does not match %s", i, img.Name, nameRegex)}
		}
		if _, dup := seen[img.Name]; dup {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].name %q must be unique", i, img.Name)}
		}
		seen[img.Name] = struct{}{}
		if img.Target.ManifestDigest == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].target.manifest_digest is required", i)}
		}
		if _, ok := s.Blobs[img.Target.ManifestDigest]; !ok {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].target.manifest_digest %s must appear in blobs",
				i, img.Target.ManifestDigest)}
		}
		if img.Baseline.ManifestDigest == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].baseline.manifest_digest is required", i)}
		}
	}
	for d, b := range s.Blobs {
		if err := validateBlobEntry(d, b); err != nil {
			return err
		}
	}
	return nil
}

func validateBlobEntry(d digest.Digest, b BlobEntry) error {
	switch b.Encoding {
	case EncodingFull:
		if b.Codec != "" || b.PatchFromDigest != "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=full must not set codec/patch_from_digest", d)}
		}
		if b.ArchiveSize != b.Size {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=full requires archive_size == size (got %d vs %d)",
				d, b.ArchiveSize, b.Size)}
		}
	case EncodingPatch:
		if b.Codec == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=patch requires codec", d)}
		}
		if b.PatchFromDigest == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=patch requires patch_from_digest", d)}
		}
		if b.ArchiveSize <= 0 || b.ArchiveSize >= b.Size {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=patch requires 0 < archive_size < size (got %d vs %d)",
				d, b.ArchiveSize, b.Size)}
		}
	default:
		return &ErrSidecarSchema{Reason: fmt.Sprintf(
			"blobs[%s] encoding=%q is not recognized", d, b.Encoding)}
	}
	return nil
}

// RequiresZstd reports whether this archive contains at least one
// intra-layer patch payload. Importers and inspectors use this to
// decide whether the zstd binary is required at import time.
func (s Sidecar) RequiresZstd() bool {
	for _, b := range s.Blobs {
		if b.Encoding == EncodingPatch {
			return true
		}
	}
	return false
}

func (s Sidecar) Marshal() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sidecar: %w", err)
	}
	return out, nil
}

func ParseSidecar(raw []byte) (*Sidecar, error) {
	var s Sidecar
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, &ErrInvalidBundleFormat{Cause: err}
	}
	switch {
	case s.Feature != FeatureBundle:
		return nil, &ErrPhase1Archive{GotFeature: s.Feature}
	case s.Version != SchemaVersionV1:
		return nil, &ErrUnknownBundleVersion{Got: s.Version}
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
