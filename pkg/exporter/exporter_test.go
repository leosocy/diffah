package exporter_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
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

func TestOptions_AcceptsSystemContext(t *testing.T) {
	sys := &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue}
	opts := exporter.Options{
		Pairs:         []exporter.Pair{{Name: "a", BaselineRef: "b", TargetRef: "t"}},
		SystemContext: sys,
		RetryTimes:    3,
	}
	if opts.SystemContext == nil {
		t.Fatal("SystemContext should be retained")
	}
}

func TestPair_EmptyPairsRejected(t *testing.T) {
	err := exporter.ValidatePairs(nil)
	require.Error(t, err)
}

func TestPair_DuplicateNameRejected(t *testing.T) {
	pairs := []exporter.Pair{
		{Name: "a", BaselineRef: "b1.tar", TargetRef: "t1.tar"},
		{Name: "a", BaselineRef: "b2.tar", TargetRef: "t2.tar"},
	}
	err := exporter.ValidatePairs(pairs)
	require.Error(t, err)
}

func TestExport_RequiredMode_FailsWhenProbeMissing(t *testing.T) {
	tmp := t.TempDir()
	// Dummy paths are safe here because resolveMode runs before any
	// file-touching work in buildBundle. If that ordering ever changes,
	// this test will fail loudly on the dummy paths rather than silently
	// skip the probe assertion.
	opts := exporter.Options{
		Pairs:       []exporter.Pair{{Name: "a", BaselineRef: "does-not-matter", TargetRef: "ditto"}},
		Platform:    "linux/amd64",
		IntraLayer:  "required",
		OutputPath:  filepath.Join(tmp, "bundle.tar"),
		ToolVersion: "test",
		Probe:       func(context.Context) (bool, string) { return false, "zstd not on $PATH" },
	}
	err := exporter.Export(context.Background(), opts)
	require.Error(t, err)
	require.True(t, errors.Is(err, zstdpatch.ErrZstdBinaryMissing))
	_, statErr := os.Stat(opts.OutputPath)
	require.True(t, os.IsNotExist(statErr))
}

func TestExport_AutoMode_DowngradesSilentlyWhenProbeMissing(t *testing.T) {
	tmp := t.TempDir()
	opts := exporter.Options{
		Pairs:       []exporter.Pair{{Name: "a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar", TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"}},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  filepath.Join(tmp, "bundle.tar"),
		ToolVersion: "test",
		Probe:       func(context.Context) (bool, string) { return false, "zstd not on $PATH" },
	}
	err := exporter.Export(context.Background(), opts)
	require.NoError(t, err)
	requireAllFullEncoding(t, opts.OutputPath)
}

func requireAllFullEncoding(t *testing.T, path string) {
	t.Helper()
	raw, err := archive.ReadSidecar(path)
	require.NoError(t, err)
	sc, err := diff.ParseSidecar(raw)
	require.NoError(t, err)
	for d, b := range sc.Blobs {
		require.Equal(t, diff.EncodingFull, b.Encoding,
			"blob %s unexpectedly encoded as %s", d, b.Encoding)
	}
}
