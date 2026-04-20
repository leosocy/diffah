//go:build integration

package importer_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

func TestImport_Matrix(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name            string
		targetFixture   string
		baselineFixture string
		sourceTransport string // "oci-archive" or "docker-archive"
		outputFormat    string
	}{
		{"oci→docker-archive", "v2_oci.tar", "v1_oci.tar", "oci-archive", "docker-archive"},
		{"oci→oci-archive", "v2_oci.tar", "v1_oci.tar", "oci-archive", "oci-archive"},
		{"oci→dir", "v2_oci.tar", "v1_oci.tar", "oci-archive", "dir"},
		{"schema2→docker-archive", "v2_s2.tar", "v1_s2.tar", "docker-archive", "docker-archive"},
		{"schema2→oci-archive", "v2_s2.tar", "v1_s2.tar", "docker-archive", "oci-archive"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := repoRoot(t)

			// Build delta using the matching transport for both target and baseline.
			var delta string
			if tc.sourceTransport == "oci-archive" {
				delta = buildDelta(t, tc.targetFixture, tc.baselineFixture)
			} else {
				delta = buildDeltaS2(t, tc.targetFixture, tc.baselineFixture)
			}

			baselineRefStr := tc.sourceTransport + ":" + filepath.Join(root, "testdata/fixtures", tc.baselineFixture)
			baselineRef, err := imageio.ParseReference(baselineRefStr)
			require.NoError(t, err)

			out := filepath.Join(t.TempDir(), fmt.Sprintf("out-%s", tc.outputFormat))
			err = importer.Import(ctx, importer.Options{
				DeltaPath:    delta,
				BaselineRef:  baselineRef,
				OutputFormat: tc.outputFormat,
				OutputPath:   out,
			})
			require.NoError(t, err)

			info, err := os.Stat(out)
			require.NoError(t, err)
			if tc.outputFormat == "dir" {
				require.True(t, info.IsDir())
			} else {
				require.Greater(t, info.Size(), int64(0))
			}
		})
	}
}

// buildDeltaS2 is a schema-2 variant of buildDelta used by the matrix test.
// It uses docker-archive: for both target and baseline.
func buildDeltaS2(t *testing.T, targetTar, baselineTar string) string {
	t.Helper()
	ctx := context.Background()
	root := repoRoot(t)

	target, err := imageio.ParseReference(
		"docker-archive:" + filepath.Join(root, "testdata/fixtures", targetTar))
	require.NoError(t, err)
	baseline, err := imageio.ParseReference(
		"docker-archive:" + filepath.Join(root, "testdata/fixtures", baselineTar))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "delta.tar")
	require.NoError(t, exporter.Export(ctx, exporter.Options{
		TargetRef: target, BaselineRef: baseline, OutputPath: out, ToolVersion: "test",
	}))
	return out
}
