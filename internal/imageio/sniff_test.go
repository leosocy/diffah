package imageio

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSniffArchiveFormat(t *testing.T) {
	cases := []struct {
		name, path, want string
	}{
		{"oci v1", "../../testdata/fixtures/v1_oci.tar", FormatOCIArchive},
		{"s2 v1", "../../testdata/fixtures/v1_s2.tar", FormatDockerArchive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SniffArchiveFormat(tc.path)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
