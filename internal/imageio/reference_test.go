package imageio

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseReference_AcceptsCommonTransports(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"docker", "docker://registry.example.com/app:v1"},
		{"oci-archive", "oci-archive:/tmp/foo.tar"},
		{"oci-dir", "oci:/tmp/layout"},
		{"dir", "dir:/tmp/dir"},
		{"docker-archive", "docker-archive:/tmp/foo.tar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseReference(tc.in)
			require.NoError(t, err)
			require.NotNil(t, ref)
		})
	}
}

func TestParseReference_RejectsEmpty(t *testing.T) {
	_, err := ParseReference("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestParseReference_RejectsUnknownTransport(t *testing.T) {
	_, err := ParseReference("notatransport://foo")
	require.Error(t, err)
}
