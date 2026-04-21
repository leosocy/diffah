package diff

import "github.com/opencontainers/go-digest"

// Encoding discriminates how a shipped blob is stored in the archive.
// The zero value ("") is invalid for ShippedInDelta entries and must not
// be present on RequiredFromBaseline entries.
type Encoding string

const (
	// EncodingFull: the archive file stored under Digest contains the
	// target blob bytes verbatim.
	EncodingFull Encoding = "full"
	// EncodingPatch: the archive file contains a codec-specific patch that
	// reconstructs the target when applied to PatchFromDigest.
	EncodingPatch Encoding = "patch"
)

// BlobRef is the canonical description of a layer or config blob referenced
// from a manifest.
//
// The Encoding/Codec/PatchFromDigest/ArchiveSize fields apply to
// ShippedInDelta entries only. RequiredFromBaseline entries omit them
// entirely — those layers are fetched from baseline as-is, so an
// archive-level encoding concept does not apply.
type BlobRef struct {
	Digest    digest.Digest `json:"digest"`
	Size      int64         `json:"size"`
	MediaType string        `json:"media_type"`

	Encoding        Encoding      `json:"encoding,omitempty"`
	Codec           string        `json:"codec,omitempty"`
	PatchFromDigest digest.Digest `json:"patch_from_digest,omitempty"`
	ArchiveSize     int64         `json:"archive_size,omitempty"`
}

// Plan records the outcome of ComputePlan: which target layers must be
// resolved from baseline at import time, and which must be shipped inside
// the delta archive.
type Plan struct {
	ShippedInDelta       []BlobRef
	RequiredFromBaseline []BlobRef
}

// ComputePlan partitions target into RequiredFromBaseline and
// ShippedInDelta according to which digests already exist in baseline.
//
// Order within each partition follows the target's original order so that
// manifest layer ordering can be preserved downstream.
func ComputePlan(target []BlobRef, baseline []digest.Digest) Plan {
	known := make(map[digest.Digest]struct{}, len(baseline))
	for _, d := range baseline {
		known[d] = struct{}{}
	}

	// Pre-allocate empty (non-nil) slices so an all-one-sided partition
	// still produces a marshal-able sidecar — sidecar.validate rejects nil
	// slices because JSON decoding can't tell them apart from omitted
	// fields.
	plan := Plan{
		ShippedInDelta:       []BlobRef{},
		RequiredFromBaseline: []BlobRef{},
	}
	for _, b := range target {
		if _, ok := known[b.Digest]; ok {
			plan.RequiredFromBaseline = append(plan.RequiredFromBaseline, b)
		} else {
			plan.ShippedInDelta = append(plan.ShippedInDelta, b)
		}
	}
	return plan
}
