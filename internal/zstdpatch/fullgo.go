// Package zstdpatch — pure-Go plain-zstd encode/decode via klauspost/compress.
//
// These functions do NOT require the zstd binary. They are used by the
// exporter for the size-ceiling comparison in intralayer.go, and kept in
// the API for decoder symmetry (no current production caller decodes
// zstd-full bytes — see spec §1 and §4.4).
package zstdpatch

import (
	"bytes"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// EncodeFull compresses target as a standalone zstd frame. Zero-valued
// EncodeOpts reproduces the historical -3 --long=27 default.
//
// Deprecated: use EncodeFullStream. Retained for the importer hot path
// and for the size-ceiling comparator in pkg/exporter/intralayer.go,
// which depends on byte-identical EncodeAll output to keep the
// patch-vs-full decision boundary stable. Do not collapse this into a
// thin wrapper around EncodeFullStream — klauspost's one-shot EncodeAll
// and streaming Write+Close can produce different output sizes for the
// same input, which would silently shift the bundle bytes. The importer
// streaming sibling spec migrates the in-tree caller to EncodeFullStream
// directly; remove EncodeFull only after that lands.
func EncodeFull(target []byte, opts EncodeOpts) ([]byte, error) {
	if len(target) == 0 {
		return append([]byte(nil), emptyZstdFrame()...), nil
	}
	level := opts.Level
	if level == 0 {
		level = 3
	}
	windowLog := opts.WindowLog
	if windowLog == 0 {
		windowLog = 27
	}
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstdLevelToKlauspost(level)),
		zstd.WithWindowSize(1<<windowLog),
	)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: new encoder: %w", err)
	}
	defer enc.Close()
	return enc.EncodeAll(target, nil), nil
}

// DecodeFull reads a standalone zstd frame. WithDecoderMaxWindow=1<<31
// admits any Phase 4-emitted frame; smaller windows still allocate
// only their declared size.
func DecodeFull(data []byte) ([]byte, error) {
	if bytes.Equal(data, emptyZstdFrame()) {
		return nil, nil
	}
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderMaxWindow(1<<31),
	)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: new decoder: %w", err)
	}
	defer dec.Close()
	out, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: decode full: %w", err)
	}
	return out, nil
}

// zstdLevelToKlauspost maps the user-facing 1..22 zstd CLI levels onto
// the four named tiers exposed by klauspost/compress. The CLI lets you
// pick any integer; klauspost only exposes Fastest/Default/Better/Best.
// We bin: 1..2 → Fastest, 3..7 → Default, 8..15 → Better, 16..22 → Best.
//
// Level 3 binds to SpeedDefault (not SpeedFastest) so the historical
// CLI argv `-3 --long=27` and the klauspost EncodeFull stay within the
// ±5 % size-parity tolerance enforced by TestEncodeFull_SizeParityVsCLI.
func zstdLevelToKlauspost(level int) zstd.EncoderLevel {
	switch {
	case level <= 2:
		return zstd.SpeedFastest
	case level <= 7:
		return zstd.SpeedDefault
	case level <= 15:
		return zstd.SpeedBetterCompression
	default:
		return zstd.SpeedBestCompression
	}
}
