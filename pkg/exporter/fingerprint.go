// Package exporter — content-similarity helpers.
//
// This file defines the byte-weighted fingerprint used by Planner to
// pick the most-content-similar baseline layer for each shipped layer.
// See docs/superpowers/specs/2026-04-20-diffah-v2-content-similarity-matching-design.md.
package exporter

import "github.com/opencontainers/go-digest"

// Fingerprint of a decompressed tar layer: for each distinct regular-file
// content digest, the size of one instance. Directories, symlinks, hard
// links, and special files are skipped — they contribute no real bytes
// to zstd's patch-from window.
type Fingerprint map[digest.Digest]int64

// score returns the total byte-weighted intersection between target and
// candidate fingerprints. Nil inputs return 0.
func score(target, candidate Fingerprint) int64 {
	if target == nil || candidate == nil {
		return 0
	}
	var bytes int64
	for d, size := range target {
		if _, ok := candidate[d]; ok {
			bytes += size
		}
	}
	return bytes
}
