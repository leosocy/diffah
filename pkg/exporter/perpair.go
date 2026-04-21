package exporter

import (
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
)

type pairPlan struct {
	Name              string
	BaselineRef       types.ImageReference
	TargetRef         types.ImageReference
	TargetManifest    []byte
	TargetMediaType   string
	TargetLayerDescs  []diff.BlobRef
	TargetConfigRaw   []byte
	TargetConfigDesc  diff.BlobRef
	BaselineDigests   []digest.Digest
	BaselineManifest  []byte
	BaselineMediaType string
	BaselineLayerMeta []BaselineLayerMeta
	Shipped  []diff.BlobRef
	Required []diff.BlobRef
}

func planPair(ctx context.Context, p Pair, platform string) (*pairPlan, error) {
	baseRef, err := imageio.OpenArchiveRef(p.BaselinePath)
	if err != nil {
		return nil, fmt.Errorf("open baseline %s: %w", p.BaselinePath, err)
	}
	tgtRef, err := imageio.OpenArchiveRef(p.TargetPath)
	if err != nil {
		return nil, fmt.Errorf("open target %s: %w", p.TargetPath, err)
	}

	_, baseDigests, baseMeta, baseMfBytes, baseMime, err := readManifestBundle(ctx, baseRef, platform)
	if err != nil {
		return nil, fmt.Errorf("read baseline manifest %s: %w", p.BaselinePath, err)
	}
	tgtParsed, _, _, tgtMfBytes, tgtMime, err := readManifestBundle(ctx, tgtRef, platform)
	if err != nil {
		return nil, fmt.Errorf("read target manifest %s: %w", p.TargetPath, err)
	}

	tgtLayers := make([]diff.BlobRef, 0, len(tgtParsed.LayerInfos()))
	for _, l := range tgtParsed.LayerInfos() {
		tgtLayers = append(tgtLayers, diff.BlobRef{
			Digest: l.Digest, Size: l.Size, MediaType: l.MediaType,
		})
	}
	plan := diff.ComputePlan(tgtLayers, baseDigests)

	tgtConfigDesc := tgtParsed.ConfigInfo()
	cfgBytes, err := readBlobBytes(ctx, tgtRef, tgtConfigDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("read target config: %w", err)
	}

	return &pairPlan{
		Name: p.Name, BaselineRef: baseRef, TargetRef: tgtRef,
		TargetManifest: tgtMfBytes, TargetMediaType: tgtMime,
		TargetLayerDescs: tgtLayers,
		TargetConfigRaw:  cfgBytes,
		TargetConfigDesc: diff.BlobRef{
			Digest: tgtConfigDesc.Digest, Size: int64(len(cfgBytes)),
			MediaType: tgtConfigDesc.MediaType,
		},
		BaselineDigests:   baseDigests,
		BaselineManifest:  baseMfBytes,
		BaselineMediaType: baseMime,
		BaselineLayerMeta: baseMeta,
		Shipped:           plan.ShippedInDelta,
		Required:          plan.RequiredFromBaseline,
	}, nil
}

func readManifestBundle(
	ctx context.Context, ref types.ImageReference, platform string,
) (manifest.Manifest, []digest.Digest, []BaselineLayerMeta, []byte, string, error) {
	src, err := ref.NewImageSource(ctx, nil)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	defer src.Close()
	raw, mime, err := src.GetManifest(ctx, nil)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	if manifest.MIMETypeIsMultiImage(mime) {
		chosen, err := selectPlatform(ctx, src, raw, mime, platform, ref.StringWithinTransport())
		if err != nil {
			return nil, nil, nil, nil, "", err
		}
		raw, mime = chosen.raw, chosen.mime
	}
	parsed, err := manifest.FromBlob(raw, mime)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	digests := make([]digest.Digest, 0, len(parsed.LayerInfos()))
	meta := make([]BaselineLayerMeta, 0, len(parsed.LayerInfos()))
	for _, l := range parsed.LayerInfos() {
		digests = append(digests, l.Digest)
		meta = append(meta, BaselineLayerMeta{
			Digest: l.Digest, Size: l.Size, MediaType: l.MediaType,
		})
	}
	return parsed, digests, meta, raw, mime, nil
}

func readBlobBytes(ctx context.Context, ref types.ImageReference, d digest.Digest) ([]byte, error) {
	src, err := ref.NewImageSource(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	r, _, err := src.GetBlob(ctx, types.BlobInfo{Digest: d}, nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
