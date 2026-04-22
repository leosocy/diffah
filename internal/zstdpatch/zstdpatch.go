// Package zstdpatch implements zstd --patch-from style byte-level deltas
// and plain zstd encode/decode.
//
// Two backends live in this package:
//   - cli.go      — Encode / Decode (shells out to `zstd ≥ 1.5` for --patch-from)
//   - fullgo.go   — EncodeFull / DecodeFull (pure Go, via github.com/klauspost/compress)
//
// Availability of the CLI backend can be queried via Available(ctx)
// (see available.go).
package zstdpatch

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// emptyZstdFrame returns a canonical zstd frame that decodes to zero bytes.
// Generated once via klauspost/compress so it is guaranteed standards-compliant.
//
// Short-circuiting empty payloads avoids a known assertion failure
// (FIO_highbit64: v != 0) in the zstd CLI < 1.5.x when asked to encode
// an empty file.
var emptyZstdFrame = sync.OnceValue(func() []byte {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		panic(fmt.Sprintf("zstdpatch: klauspost NewWriter: %v", err))
	}
	out := enc.EncodeAll(nil, nil)
	_ = enc.Close()
	return out
})
