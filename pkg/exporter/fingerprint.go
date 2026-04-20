// Package exporter — content-similarity helpers.
//
// See docs/superpowers/specs/2026-04-20-diffah-v2-content-similarity-matching-design.md.
package exporter

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
)

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
	var total int64
	for d, size := range target {
		if _, ok := candidate[d]; ok {
			total += size
		}
	}
	return total
}

// Fingerprinter hashes a compressed layer blob into a Fingerprint.
// Media type picks the decompressor; unknown / malformed input yields
// an error wrapping ErrFingerprintFailed.
type Fingerprinter interface {
	Fingerprint(ctx context.Context, mediaType string, blob []byte) (Fingerprint, error)
}

// ErrFingerprintFailed is the sentinel wrapped by every error returned
// from DefaultFingerprinter.Fingerprint. Planner branches use
// errors.Is(err, ErrFingerprintFailed) to fall back to size-closest.
var ErrFingerprintFailed = errors.New("fingerprint failed")

// DefaultFingerprinter handles plain tar (initially). gzip and zstd
// compressors are added in later tasks.
type DefaultFingerprinter struct{}

// Fingerprint implements Fingerprinter. This first revision handles only
// plain tar; it always opens the blob as a tar reader. Subsequent tasks
// extend to gzip+tar and zstd+tar layers.
func (DefaultFingerprinter) Fingerprint(
	ctx context.Context, mediaType string, blob []byte,
) (Fingerprint, error) {
	return fingerprintTar(ctx, bytes.NewReader(blob))
}

// fingerprintTar streams through a tar reader, hashing every regular-file
// entry's content and deduplicating by digest.
func fingerprintTar(ctx context.Context, r io.Reader) (Fingerprint, error) {
	tr := tar.NewReader(r)
	fp := make(Fingerprint)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrFingerprintFailed, err)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: tar header: %w", ErrFingerprintFailed, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		h := sha256.New()
		if _, err := io.Copy(h, tr); err != nil {
			return nil, fmt.Errorf("%w: tar body: %w", ErrFingerprintFailed, err)
		}
		d := digest.NewDigest(digest.SHA256, h)
		if _, exists := fp[d]; !exists {
			fp[d] = hdr.Size
		}
	}
	return fp, nil
}
