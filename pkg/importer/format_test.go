package importer

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestResolveOutputFormat(t *testing.T) {
	const (
		docker = "application/vnd.docker.distribution.manifest.v2+json"
		oci    = "application/vnd.oci.image.manifest.v1+json"
	)
	tests := []struct {
		name         string
		userFmt      string
		sourceMime   string
		allowConvert bool
		want         string
		wantErr      bool
		wantErrAs    any
	}{
		{"auto picks docker-archive for schema2", "", docker, false, FormatDockerArchive, false, nil},
		{"auto picks oci-archive for oci", "", oci, false, FormatOCIArchive, false, nil},
		{"explicit docker-archive matches schema2", FormatDockerArchive, docker, false, FormatDockerArchive, false, nil},
		{"explicit oci-archive matches oci", FormatOCIArchive, oci, false, FormatOCIArchive, false, nil},
		{"dir always allowed for schema2", FormatDir, docker, false, FormatDir, false, nil},
		{"dir always allowed for oci", FormatDir, oci, false, FormatDir, false, nil},
		{
			name:       "schema2 source to oci-archive rejected without allow",
			userFmt:    FormatOCIArchive,
			sourceMime: docker,
			wantErr:    true,
			wantErrAs:  (*diff.ErrIncompatibleOutputFormat)(nil),
		},
		{
			name:       "oci source to docker-archive rejected without allow",
			userFmt:    FormatDockerArchive,
			sourceMime: oci,
			wantErr:    true,
			wantErrAs:  (*diff.ErrIncompatibleOutputFormat)(nil),
		},
		{
			name:         "schema2 to oci-archive allowed with allow flag",
			userFmt:      FormatOCIArchive,
			sourceMime:   docker,
			allowConvert: true,
			want:         FormatOCIArchive,
		},
		{
			name:         "oci to docker-archive allowed with allow flag",
			userFmt:      FormatDockerArchive,
			sourceMime:   oci,
			allowConvert: true,
			want:         FormatDockerArchive,
		},
		{
			name:       "unknown user format rejected",
			userFmt:    "tarball",
			sourceMime: docker,
			wantErr:    true,
		},
		{
			name:       "unknown source mime rejected when auto",
			userFmt:    "",
			sourceMime: "application/vnd.example+json",
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveOutputFormat(tc.userFmt, tc.sourceMime, tc.allowConvert)
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantErrAs != nil {
					require.True(t, errors.As(err, &tc.wantErrAs),
						"expected error type %T, got %v", tc.wantErrAs, err)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
