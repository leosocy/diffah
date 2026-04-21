package exporter_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/exporter"
)

func TestExport_OCIFixture_HappyPath(t *testing.T) {
	t.Skip("rewritten in Task 17")
	_ = exporter.Options{}
}

func TestExport_S2Fixture_HappyPath(t *testing.T) {
	t.Skip("rewritten in Task 17")
	_ = exporter.Options{}
}

func TestExport_NoBaselineReturnsError(t *testing.T) {
	t.Skip("rewritten in Task 17")
	_ = exporter.Options{}
}

func TestExport_ManifestOnlyBaseline(t *testing.T) {
	t.Skip("rewritten in Task 17")
	_ = exporter.Options{}
}

func TestExport_DryRun_DoesNotWriteOutput(t *testing.T) {
	t.Skip("rewritten in Task 17")
	_ = exporter.Options{}
}

func TestExport_DryRun_ManifestOnlyBaseline(t *testing.T) {
	t.Skip("rewritten in Task 17")
	_ = exporter.Options{}
}

func TestExport_DeterministicArchive(t *testing.T) {
	t.Skip("rewritten in Task 17")
	_ = exporter.Options{}
}

func TestPair_EmptyPairsRejected(t *testing.T) {
	err := exporter.ValidatePairs(nil)
	require.Error(t, err)
}

func TestPair_DuplicateNameRejected(t *testing.T) {
	pairs := []exporter.Pair{
		{Name: "a", BaselinePath: "b1.tar", TargetPath: "t1.tar"},
		{Name: "a", BaselinePath: "b2.tar", TargetPath: "t2.tar"},
	}
	err := exporter.ValidatePairs(pairs)
	require.Error(t, err)
}
