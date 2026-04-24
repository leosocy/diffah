package signer

import (
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
)

// JCSCanonical returns RFC 8785 canonical JSON bytes for v. It round-trips
// through json.Marshal and then jcs.Transform so that maps, slices, and
// structs all serialize deterministically regardless of key insertion
// order or whitespace choices made by the marshaler.
//
// Callers that already hold the on-disk JSON bytes (for example, a
// sidecar JSON extracted from a tar entry) should prefer
// JCSCanonicalFromBytes to avoid a pointless Unmarshal → Marshal round.
func JCSCanonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal for jcs: %w", err)
	}
	return JCSCanonicalFromBytes(raw)
}

// JCSCanonicalFromBytes canonicalizes already-serialized JSON bytes
// without re-marshalling. The input must be valid JSON; invalid JSON
// is surfaced as an error from the underlying jcs.Transform.
func JCSCanonicalFromBytes(raw []byte) ([]byte, error) {
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("jcs transform: %w", err)
	}
	return out, nil
}
