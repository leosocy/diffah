package diff

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/opencontainers/go-digest"
)

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

func (s Sidecar) Marshal() ([]byte, error) {
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sidecar: %w", err)
	}
	return out, nil
}

func ParseSidecar(raw []byte) (*Sidecar, error) {
	var s Sidecar
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode sidecar: %w", err)
	}
	return &s, nil
}
