package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// LayerRef is a (digest, size) pair extracted from a manifest's layers list.
type LayerRef struct {
	Digest digest.Digest
	Size   int64
}

// parseManifestLayers parses raw manifest bytes (Docker schema 2 or OCI v1)
// and returns the layer list plus canonical media type. Manifest lists and
// indexes are rejected — callers must select an instance upstream.
func parseManifestLayers(raw []byte, mediaType string) ([]LayerRef, string, error) {
	layers, _, mt, err := parseManifest(raw, mediaType)
	return layers, mt, err
}

// parseManifest parses raw manifest bytes and returns the layer list, the
// config descriptor digest, and the canonical media type. Manifest lists and
// indexes are rejected — callers must select an instance upstream.
func parseManifest(raw []byte, mediaType string) ([]LayerRef, digest.Digest, string, error) {
	canonical := manifest.NormalizedMIMEType(mediaType)
	switch canonical {
	case manifest.DockerV2Schema2MediaType:
		m, err := manifest.Schema2FromManifest(raw)
		if err != nil {
			return nil, "", "", fmt.Errorf("parse docker schema 2 manifest: %w", err)
		}
		out := make([]LayerRef, len(m.LayersDescriptors))
		for i, d := range m.LayersDescriptors {
			out[i] = LayerRef{Digest: d.Digest, Size: d.Size}
		}
		return out, m.ConfigDescriptor.Digest, canonical, nil
	case imgspecv1.MediaTypeImageManifest:
		m, err := manifest.OCI1FromManifest(raw)
		if err != nil {
			return nil, "", "", fmt.Errorf("parse OCI manifest: %w", err)
		}
		out := make([]LayerRef, len(m.Layers))
		for i, d := range m.Layers {
			out[i] = LayerRef{Digest: d.Digest, Size: d.Size}
		}
		return out, m.Config.Digest, canonical, nil
	default:
		return nil, "", "", fmt.Errorf("unsupported manifest media type %q", mediaType)
	}
}

// readSidecarTargetLayers retrieves the target manifest blob from the
// extracted bundle (always EncodingFull) and parses its layer list.
func readSidecarTargetLayers(
	bundle *extractedBundle, img diff.ImageEntry,
) ([]LayerRef, string, error) {
	mfDigest := img.Target.ManifestDigest
	path := filepath.Join(bundle.blobDir, mfDigest.Algorithm().String(), mfDigest.Encoded())
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read target manifest %s for image %q: %w",
			mfDigest, img.Name, err)
	}
	return parseManifestLayers(raw, img.Target.MediaType)
}

// readDestManifestLayers opens the dest, fetches its manifest, and parses it.
// Returns the layer list, dest media type, and the dest manifest digest
// computed by digest.FromBytes on the bytes returned by GetManifest.
func readDestManifestLayers(
	ctx context.Context, destRef types.ImageReference, sysctx *types.SystemContext,
) ([]LayerRef, string, digest.Digest, error) {
	src, err := destRef.NewImageSource(ctx, sysctx)
	if err != nil {
		return nil, "", "", fmt.Errorf("open dest source %q: %w",
			destRef.StringWithinTransport(), err)
	}
	defer src.Close()
	raw, mediaType, err := src.GetManifest(ctx, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("read dest manifest %q: %w",
			destRef.StringWithinTransport(), err)
	}
	layers, mt, err := parseManifestLayers(raw, mediaType)
	if err != nil {
		return nil, "", "", err
	}
	return layers, mt, digest.FromBytes(raw), nil
}
