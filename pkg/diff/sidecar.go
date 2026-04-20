package diff

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/opencontainers/go-digest"
)

// SidecarFilename is the canonical file name of the sidecar written at the
// top level of every delta archive.
const SidecarFilename = "diffah.json"

// SchemaVersionV1 is the sidecar schema version this package writes and
// accepts.
const SchemaVersionV1 = "v1"

// ImageRef describes the target image manifest pointer recorded in a
// sidecar.
type ImageRef struct {
	ManifestDigest digest.Digest `json:"manifest_digest"`
	ManifestSize   int64         `json:"manifest_size"`
	MediaType      string        `json:"media_type"`
}

// BaselineRef describes the baseline image manifest pointer recorded in a
// sidecar. SourceHint is informational only.
type BaselineRef struct {
	ManifestDigest digest.Digest `json:"manifest_digest"`
	MediaType      string        `json:"media_type"`
	SourceHint     string        `json:"source_hint,omitempty"`
}

// Sidecar is the diffah.json file written inside every delta archive.
//
// It serves three purposes: schema versioning (Version), fail-fast
// verification (RequiredFromBaseline is probed at import time), and human
// inspection (ShippedInDelta plus size fields let `diffah inspect` report
// savings without scanning the archive).
type Sidecar struct {
	Version              string      `json:"version"`
	Tool                 string      `json:"tool"`
	ToolVersion          string      `json:"tool_version"`
	CreatedAt            time.Time   `json:"created_at"`
	Platform             string      `json:"platform"`
	Target               ImageRef    `json:"target"`
	Baseline             BaselineRef `json:"baseline"`
	RequiredFromBaseline []BlobRef   `json:"required_from_baseline"`
	ShippedInDelta       []BlobRef   `json:"shipped_in_delta"`
}

// Marshal encodes the sidecar with two-space indentation. It validates the
// payload before writing so an invalid Sidecar cannot be persisted.
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

// ParseSidecar decodes sidecar bytes and validates required fields and the
// schema version. The returned *Sidecar is safe to inspect only if err is
// nil.
func ParseSidecar(raw []byte) (*Sidecar, error) {
	var s Sidecar
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode sidecar: %w", err)
	}
	if s.Version != SchemaVersionV1 {
		return nil, &ErrUnsupportedSchemaVersion{Got: s.Version}
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s Sidecar) validate() error {
	switch {
	case s.Platform == "":
		return &ErrSidecarSchema{Reason: "platform is required"}
	case s.Target.ManifestDigest == "":
		return &ErrSidecarSchema{Reason: "target.manifest_digest is required"}
	case s.RequiredFromBaseline == nil:
		return &ErrSidecarSchema{Reason: "required_from_baseline is required (may be empty slice)"}
	case s.ShippedInDelta == nil:
		return &ErrSidecarSchema{Reason: "shipped_in_delta is required (may be empty slice)"}
	}
	for i, b := range s.RequiredFromBaseline {
		if err := validateRequiredEntry(i, b); err != nil {
			return err
		}
	}
	for i, b := range s.ShippedInDelta {
		if err := validateShippedEntry(i, b); err != nil {
			return err
		}
	}
	return nil
}

// validateRequiredEntry: required-from-baseline entries must not carry any
// intra-layer fields. Those fields describe archive-side encoding and
// baseline-fetched blobs have no archive-side bytes.
func validateRequiredEntry(i int, b BlobRef) error {
	switch {
	case b.Encoding != "",
		b.Codec != "",
		b.PatchFromDigest != "",
		b.ArchiveSize != 0:
		return &ErrSidecarSchema{Reason: fmt.Sprintf(
			"required_from_baseline[%d] must not set encoding/codec/patch_from_digest/archive_size",
			i)}
	}
	return nil
}

// validateShippedEntry: every shipped entry must declare an encoding, and
// that encoding's peer fields must be consistent with the declaration.
func validateShippedEntry(i int, b BlobRef) error {
	switch b.Encoding {
	case "":
		return &ErrSidecarSchema{Reason: fmt.Sprintf(
			"shipped_in_delta[%d].encoding is required", i)}
	case EncodingFull:
		if b.Codec != "" || b.PatchFromDigest != "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"shipped_in_delta[%d] encoding=full must not set codec/patch_from_digest",
				i)}
		}
		if b.ArchiveSize != b.Size {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"shipped_in_delta[%d] encoding=full requires archive_size == size (got %d vs %d)",
				i, b.ArchiveSize, b.Size)}
		}
	case EncodingPatch:
		if b.Codec == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"shipped_in_delta[%d] encoding=patch requires codec", i)}
		}
		if b.PatchFromDigest == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"shipped_in_delta[%d] encoding=patch requires patch_from_digest", i)}
		}
		if b.ArchiveSize <= 0 || b.ArchiveSize >= b.Size {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"shipped_in_delta[%d] encoding=patch requires 0 < archive_size < size (got %d vs %d)",
				i, b.ArchiveSize, b.Size)}
		}
	default:
		return &ErrSidecarSchema{Reason: fmt.Sprintf(
			"shipped_in_delta[%d] encoding=%q is not recognized",
			i, b.Encoding)}
	}
	return nil
}
