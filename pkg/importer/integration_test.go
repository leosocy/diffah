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
	"github.com/leosocy/diffah/pkg/diff"
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
		allowConvert    bool
		wantConflict    bool
	}{
		{"oci→docker-archive rejected", "v2_oci.tar", "v1_oci.tar", "oci-archive", "docker-archive", false, true},
		{"oci→docker-archive with allow", "v2_oci.tar", "v1_oci.tar", "oci-archive", "docker-archive", true, false},
		{"oci→oci-archive match", "v2_oci.tar", "v1_oci.tar", "oci-archive", "oci-archive", false, false},
		{"oci→auto", "v2_oci.tar", "v1_oci.tar", "oci-archive", "", false, false},
		{"oci→dir", "v2_oci.tar", "v1_oci.tar", "oci-archive", "dir", false, false},
		{"schema2→docker-archive match", "v2_s2.tar", "v1_s2.tar", "docker-archive", "docker-archive", false, false},
		{"schema2→auto", "v2_s2.tar", "v1_s2.tar", "docker-archive", "", false, false},
		{"schema2→oci-archive rejected", "v2_s2.tar", "v1_s2.tar", "docker-archive", "oci-archive", false, true},
		{"schema2→oci-archive with allow", "v2_s2.tar", "v1_s2.tar", "docker-archive", "oci-archive", true, false},
		{"schema2→dir", "v2_s2.tar", "v1_s2.tar", "docker-archive", "dir", false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := repoRoot(t)

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
				AllowConvert: tc.allowConvert,
			})
			if tc.wantConflict {
				var conflict *diff.ErrIncompatibleOutputFormat
				require.ErrorAs(t, err, &conflict)
				_, statErr := os.Stat(out)
				require.True(t, os.IsNotExist(statErr))
				return
			}
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
