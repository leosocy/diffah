package diff

import "github.com/opencontainers/go-digest"

// BlobRef is the canonical description of a layer or config blob referenced
// from a manifest.
type BlobRef struct {
	Digest    digest.Digest `json:"digest"`
	Size      int64         `json:"size"`
	MediaType string        `json:"media_type"`
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
