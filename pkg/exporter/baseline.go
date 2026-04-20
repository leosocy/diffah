package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// BaselineSet is the producer-side abstraction over a baseline image. It
// exposes only what the diff algorithm needs: the layer digest sequence and
// the manifest reference recorded in the sidecar.
type BaselineSet interface {
	LayerDigests(ctx context.Context) ([]digest.Digest, error)
	ManifestRef() diff.BaselineRef
}

// ImageBaseline reads the baseline from any containers-image transport.
type ImageBaseline struct {
	ref          types.ImageReference
	sys          *types.SystemContext
	sourceHint   string
	manifestRaw  []byte
	manifestMime string
	parsed       manifest.Manifest
}

// NewImageBaseline opens the reference and loads the manifest once so that
// LayerDigests is cheap. When platform is non-empty and the reference points
// to a manifest list, the matching instance is selected; otherwise a manifest
// list triggers diff.ErrManifestListUnselected.
func NewImageBaseline(ctx context.Context, ref types.ImageReference, sys *types.SystemContext, sourceHint, platform string) (*ImageBaseline, error) {
	src, err := ref.NewImageSource(ctx, sys)
	if err != nil {
		return nil, fmt.Errorf("open baseline source: %w", err)
	}
	defer src.Close()

	raw, mime, err := src.GetManifest(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("read baseline manifest: %w", err)
	}

	if manifest.MIMETypeIsMultiImage(mime) {
		chosen, err := selectPlatform(ctx, src, raw, mime, platform, ref.StringWithinTransport())
		if err != nil {
			return nil, err
		}
		raw, mime = chosen.raw, chosen.mime
	}

	parsed, err := manifest.FromBlob(raw, mime)
	if err != nil {
		return nil, fmt.Errorf("parse baseline manifest: %w", err)
	}
	return &ImageBaseline{
		ref:          ref,
		sys:          sys,
		sourceHint:   sourceHint,
		manifestRaw:  raw,
		manifestMime: mime,
		parsed:       parsed,
	}, nil
}

// LayerDigests returns the layer digests in manifest order.
func (b *ImageBaseline) LayerDigests(_ context.Context) ([]digest.Digest, error) {
	infos := b.parsed.LayerInfos()
	out := make([]digest.Digest, 0, len(infos))
	for _, l := range infos {
		out = append(out, l.Digest)
	}
	return out, nil
}

// ManifestRef returns the diff.BaselineRef recorded in the sidecar.
func (b *ImageBaseline) ManifestRef() diff.BaselineRef {
	return diff.BaselineRef{
		ManifestDigest: digest.FromBytes(b.manifestRaw),
		MediaType:      b.manifestMime,
		SourceHint:     b.sourceHint,
	}
}

// ManifestBaseline reads baseline layer digests from a standalone manifest.json
// file on disk. Used when the original baseline image is no longer accessible.
type ManifestBaseline struct {
	path   string
	raw    []byte
	mime   string
	parsed manifest.Manifest
}

// NewManifestBaseline parses path as a container image manifest. Manifest
// lists are rejected with diff.ErrManifestListUnselected regardless of the
// platform argument; callers must resolve the platform-specific instance
// before calling this constructor. Use NewImageBaseline when the baseline
// image is still accessible via a transport and platform selection is needed.
func NewManifestBaseline(path, _ string) (*ManifestBaseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	mime, err := sniffManifestMIME(raw)
	if err != nil {
		return nil, err
	}
	if manifest.MIMETypeIsMultiImage(mime) {
		return nil, &diff.ErrManifestListUnselected{Ref: path}
	}
	parsed, err := manifest.FromBlob(raw, mime)
	if err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return &ManifestBaseline{path: path, raw: raw, mime: mime, parsed: parsed}, nil
}

// LayerDigests returns the layer digests in manifest order.
func (b *ManifestBaseline) LayerDigests(_ context.Context) ([]digest.Digest, error) {
	infos := b.parsed.LayerInfos()
	out := make([]digest.Digest, 0, len(infos))
	for _, l := range infos {
		out = append(out, l.Digest)
	}
	return out, nil
}

// ManifestRef returns the diff.BaselineRef for the manifest on disk.
func (b *ManifestBaseline) ManifestRef() diff.BaselineRef {
	return diff.BaselineRef{
		ManifestDigest: digest.FromBytes(b.raw),
		MediaType:      b.mime,
		SourceHint:     b.path,
	}
}

func sniffManifestMIME(raw []byte) (string, error) {
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("decode manifest: %w", err)
	}
	if probe.MediaType == "" {
		return "", fmt.Errorf("manifest has empty mediaType")
	}
	return probe.MediaType, nil
}

type chosenManifest struct {
	raw  []byte
	mime string
}

// selectPlatform picks the correct manifest instance from a manifest list
// using the supplied platform string ("os/arch[/variant]").
func selectPlatform(ctx context.Context, src types.ImageSource, raw []byte, mime, platform, refName string) (chosenManifest, error) {
	if platform == "" {
		return chosenManifest{}, &diff.ErrManifestListUnselected{Ref: refName}
	}
	list, err := manifest.ListFromBlob(raw, mime)
	if err != nil {
		return chosenManifest{}, fmt.Errorf("parse manifest list: %w", err)
	}
	sys := &types.SystemContext{}
	if err := applyPlatformToSystemContext(sys, platform); err != nil {
		return chosenManifest{}, err
	}
	instanceDigest, err := list.ChooseInstance(sys)
	if err != nil {
		return chosenManifest{}, fmt.Errorf("choose platform %s: %w", platform, err)
	}
	body, bodyMime, err := src.GetManifest(ctx, &instanceDigest)
	if err != nil {
		return chosenManifest{}, fmt.Errorf("fetch instance manifest %s: %w", instanceDigest, err)
	}
	return chosenManifest{raw: body, mime: bodyMime}, nil
}

// applyPlatformToSystemContext parses "os/arch[/variant]" into SystemContext.
func applyPlatformToSystemContext(sys *types.SystemContext, platform string) error {
	parts := strings.Split(platform, "/")
	switch len(parts) {
	case 2:
		sys.OSChoice = parts[0]
		sys.ArchitectureChoice = parts[1]
	case 3:
		sys.OSChoice = parts[0]
		sys.ArchitectureChoice = parts[1]
		sys.VariantChoice = parts[2]
	default:
		return fmt.Errorf("invalid --platform %q: expected os/arch[/variant]", platform)
	}
	return nil
}
