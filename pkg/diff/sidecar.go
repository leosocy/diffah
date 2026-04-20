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
	}
	return nil
}
