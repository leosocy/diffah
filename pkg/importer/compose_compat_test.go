package importer

import (
	"context"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// fakeDestRef is a minimal types.ImageReference whose only meaningful
// method is Transport().Name() — enforceOutputCompat only consults
// that plus the source manifest, so every other method can panic.
type fakeDestRef struct{ name string }

type fakeTransport struct{ name string }

func (t fakeTransport) Name() string                                        { return t.name }
func (fakeTransport) ParseReference(_ string) (types.ImageReference, error) { return nil, nil }
func (fakeTransport) ValidatePolicyConfigurationScope(_ string) error       { return nil }

func (r fakeDestRef) Transport() types.ImageTransport { return fakeTransport{name: r.name} }
func (fakeDestRef) StringWithinTransport() string   { return "" }
func (fakeDestRef) DockerReference() reference.Named { return nil }
func (fakeDestRef) PolicyConfigurationIdentity() string {
	return ""
}
func (fakeDestRef) PolicyConfigurationNamespaces() []string { return nil }
func (fakeDestRef) NewImage(context.Context, *types.SystemContext) (types.ImageCloser, error) {
	return nil, nil
}
func (fakeDestRef) NewImageSource(context.Context, *types.SystemContext) (types.ImageSource, error) {
	return nil, nil
}
func (fakeDestRef) NewImageDestination(context.Context, *types.SystemContext) (types.ImageDestination, error) {
	return nil, nil
}
func (fakeDestRef) DeleteImage(context.Context, *types.SystemContext) error { return nil }

// fakeMimeSrc is a minimal types.ImageSource that returns the configured
// manifest media type from GetManifest.
type fakeMimeSrc struct{ mime string }

func (s *fakeMimeSrc) Reference() types.ImageReference { return nil }
func (s *fakeMimeSrc) Close() error                    { return nil }
func (s *fakeMimeSrc) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	return []byte("{}"), s.mime, nil
}
func (s *fakeMimeSrc) HasThreadSafeGetBlob() bool { return false }
func (s *fakeMimeSrc) GetBlob(context.Context, types.BlobInfo, types.BlobInfoCache) (io.ReadCloser, int64, error) {
	return nil, 0, nil
}
func (s *fakeMimeSrc) GetSignatures(context.Context, *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (s *fakeMimeSrc) LayerInfosForCopy(context.Context, *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}

func TestEnforceOutputCompat(t *testing.T) {
	cases := []struct {
		name         string
		destTpt      string
		sourceMime   string
		allowConvert bool
		wantErr      bool
	}{
		{"docker-archive + schema2 OK", "docker-archive", mimeDockerSchema2, false, false},
		{"docker-archive + OCI rejected", "docker-archive", mimeOCIManifest, false, true},
		{"docker-archive + OCI allowed with allow-convert", "docker-archive", mimeOCIManifest, true, false},
		{"oci-archive + OCI OK", "oci-archive", mimeOCIManifest, false, false},
		{"oci-archive + schema2 rejected", "oci-archive", mimeDockerSchema2, false, true},
		{"oci layout + OCI OK", "oci", mimeOCIManifest, false, false},
		{"oci layout + schema2 rejected", "oci", mimeDockerSchema2, false, true},
		{"dir always permitted (OCI source)", "dir", mimeOCIManifest, false, false},
		{"dir always permitted (schema2 source)", "dir", mimeDockerSchema2, false, false},
		{"docker:// handled by upstream copy (OCI source)", "docker", mimeOCIManifest, false, false},
		{"empty mime skips the check", "docker-archive", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Bypass the fakeDestRef wiring for the Transport() name
			// by using the exported constructor-style struct inline.
			err := enforceOutputCompat(
				fakeDestRef{name: tc.destTpt},
				&fakeMimeSrc{mime: tc.sourceMime},
				tc.allowConvert,
			)
			if tc.wantErr {
				require.Error(t, err)
				var typed *diff.ErrIncompatibleOutputFormat
				require.ErrorAs(t, err, &typed)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
