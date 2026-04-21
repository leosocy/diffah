package exporter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPair_ResolveUnique(t *testing.T) {
	pairs := []Pair{
		{Name: "a", BaselinePath: "b1.tar", TargetPath: "t1.tar"},
		{Name: "b", BaselinePath: "b2.tar", TargetPath: "t2.tar"},
	}
	require.NoError(t, ValidatePairs(pairs))

	dup := append(pairs, Pair{Name: "a", BaselinePath: "x", TargetPath: "y"})
	err := ValidatePairs(dup)
	require.Error(t, err)
}
