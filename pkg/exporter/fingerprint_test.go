package exporter

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestScore_DisjointFingerprints(t *testing.T) {
	a := Fingerprint{digest.FromBytes([]byte("one")): 100}
	b := Fingerprint{digest.FromBytes([]byte("two")): 200}
	require.Zero(t, score(a, b))
}

func TestScore_IdenticalFingerprints(t *testing.T) {
	d := digest.FromBytes([]byte("shared"))
	a := Fingerprint{d: 500}
	require.Equal(t, int64(500), score(a, a))
}

func TestScore_PartialOverlap(t *testing.T) {
	shared := digest.FromBytes([]byte("shared"))
	onlyA := digest.FromBytes([]byte("onlya"))
	onlyB := digest.FromBytes([]byte("onlyb"))
	a := Fingerprint{shared: 100, onlyA: 50}
	b := Fingerprint{shared: 100, onlyB: 200}
	require.Equal(t, int64(100), score(a, b))
}

func TestScore_EmptyTarget(t *testing.T) {
	var empty Fingerprint
	b := Fingerprint{digest.FromBytes([]byte("x")): 100}
	require.Zero(t, score(empty, b))
}

func TestScore_NilCandidate(t *testing.T) {
	a := Fingerprint{digest.FromBytes([]byte("x")): 100}
	require.Zero(t, score(a, nil))
}

func TestScore_NilTarget(t *testing.T) {
	b := Fingerprint{digest.FromBytes([]byte("x")): 100}
	require.Zero(t, score(nil, b))
}
