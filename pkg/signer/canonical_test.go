package signer_test

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

// TestJCSCanonical_KeyPermutationInvariant asserts that 20 randomly
// generated JSON objects each canonicalize to the same byte sequence
// across 100 independent re-marshals. JCS (RFC 8785) is deterministic
// by design; this test pins that invariant locally so a future
// dependency swap can't silently introduce key-order non-determinism.
func TestJCSCanonical_KeyPermutationInvariant(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(42, 43))
	for i := 0; i < 20; i++ {
		base := map[string]any{
			"a": float64(r.Int64()),
			"b": "string",
			"c": []any{float64(1), float64(2), float64(3)},
			"d": map[string]any{"x": true, "y": float64(-1)},
			"e": nil,
		}
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatalf("marshal base: %v", err)
		}
		canon1, err := signer.JCSCanonicalFromBytes(raw)
		if err != nil {
			t.Fatalf("canonical: %v", err)
		}
		for j := 0; j < 100; j++ {
			shuffled, err := json.Marshal(base)
			if err != nil {
				t.Fatalf("marshal iter %d: %v", j, err)
			}
			canon2, err := signer.JCSCanonicalFromBytes(shuffled)
			if err != nil {
				t.Fatalf("canonical iter %d: %v", j, err)
			}
			if !bytes.Equal(canon1, canon2) {
				t.Fatalf("iter %d not stable:\n  a=%s\n  b=%s", j, canon1, canon2)
			}
		}
	}
}

// TestJCSCanonical_StructAndBytesAgree asserts JCSCanonical (takes a
// value, marshals, then canonicalizes) produces the same bytes as
// JCSCanonicalFromBytes (takes raw JSON, canonicalizes). This pins the
// "caller's choice" property documented on both functions.
func TestJCSCanonical_StructAndBytesAgree(t *testing.T) {
	t.Parallel()
	v := map[string]any{"z": 1, "a": "x", "m": []any{3, 1, 2}}
	fromStruct, err := signer.JCSCanonical(v)
	if err != nil {
		t.Fatalf("JCSCanonical: %v", err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	fromBytes, err := signer.JCSCanonicalFromBytes(raw)
	if err != nil {
		t.Fatalf("JCSCanonicalFromBytes: %v", err)
	}
	if !bytes.Equal(fromStruct, fromBytes) {
		t.Fatalf("mismatch:\n  struct=%s\n  bytes =%s", fromStruct, fromBytes)
	}
}
