package exporter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPair_ResolveUnique(t *testing.T) {
	pairs := []Pair{
		{Name: "a", BaselineRef: "b1.tar", TargetRef: "t1.tar"},
		{Name: "b", BaselineRef: "b2.tar", TargetRef: "t2.tar"},
	}
	require.NoError(t, ValidatePairs(pairs))

	dupPairs := make([]Pair, 0, len(pairs)+1)
	dupPairs = append(dupPairs, pairs...)
	dupPairs = append(dupPairs, Pair{Name: "a", BaselineRef: "x", TargetRef: "y"})
	err := ValidatePairs(dupPairs)
	require.Error(t, err)
}
