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

// EncodeFull compresses target as a standalone zstd frame with parameters
// matching the CLI's `-3 --long=27` settings so the size-ceiling comparison
// against a patch-from payload stays consistent whether the CLI is on PATH
// or not.
func EncodeFull(target []byte) ([]byte, error) {
	if len(target) == 0 {
		return append([]byte(nil), emptyZstdFrame()...), nil
	}
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithWindowSize(1<<27),
	)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: new encoder: %w", err)
	}
	defer enc.Close()
	return enc.EncodeAll(target, nil), nil
}

// DecodeFull reads a standalone zstd frame. Kept in the API for symmetry;
// no current production call path invokes it (encoding: full = raw layer
// bytes; encoding: patch decodes via Decode, not DecodeFull).
// WithDecoderMaxWindow matches the encoder for defensive parity.
func DecodeFull(data []byte) ([]byte, error) {
	if bytes.Equal(data, emptyZstdFrame()) {
		return nil, nil
	}
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderMaxWindow(1<<27),
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
