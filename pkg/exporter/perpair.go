package exporter

import (
	"bytes"
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
	Name             string
	BaselineRef      string
	BaselineImageRef types.ImageReference
	TargetImageRef   types.ImageReference
	// SystemContext carries the transport/registry knobs — auth files,
	// TLS verification, mirror rewriting, etc. — from opts.SystemContext.
	// It is forwarded to every NewImageSource call in encodeShipped so
	// each ref's transport receives the same credentials planPair used.
	// Nil is acceptable for archive-only paths.
	SystemContext     *types.SystemContext
	TargetManifest    []byte
	TargetMediaType   string
	TargetLayerDescs  []diff.BlobRef
	TargetConfigRaw   []byte
	TargetConfigDesc  diff.BlobRef
	BaselineDigests   []digest.Digest
	BaselineManifest  []byte
	BaselineMediaType string
	BaselineLayerMeta []BaselineLayerMeta
	Shipped           []diff.BlobRef
	Required          []diff.BlobRef
}

func planPair(ctx context.Context, p Pair, opts *Options) (*pairPlan, error) {
	baseRef, err := imageio.OpenArchiveRef(p.BaselineRef)
	if err != nil {
		return nil, fmt.Errorf("open baseline %s: %w", p.BaselineRef, err)
	}
	tgtRef, err := imageio.OpenArchiveRef(p.TargetRef)
	if err != nil {
		return nil, fmt.Errorf("open target %s: %w", p.TargetRef, err)
	}

	_, baseDigests, baseMeta, baseMfBytes, baseMime, err := readManifestBundle(
		ctx, baseRef, opts.SystemContext, opts.Platform)
	if err != nil {
		return nil, fmt.Errorf("read baseline manifest %s: %w", p.BaselineRef, err)
	}
	tgtParsed, _, _, tgtMfBytes, tgtMime, err := readManifestBundle(
		ctx, tgtRef, opts.SystemContext, opts.Platform)
	if err != nil {
		return nil, fmt.Errorf("read target manifest %s: %w", p.TargetRef, err)
	}

	tgtLayers := make([]diff.BlobRef, 0, len(tgtParsed.LayerInfos()))
	for _, l := range tgtParsed.LayerInfos() {
		tgtLayers = append(tgtLayers, diff.BlobRef{
			Digest: l.Digest, Size: l.Size, MediaType: l.MediaType,
		})
	}
	plan := diff.ComputePlan(tgtLayers, baseDigests)

	tgtConfigDesc := tgtParsed.ConfigInfo()
	cfgBytes, err := readBlobBytes(ctx, tgtRef, opts.SystemContext, tgtConfigDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("read target config: %w", err)
	}

	return &pairPlan{
		Name: p.Name, BaselineRef: p.BaselineRef, BaselineImageRef: baseRef, TargetImageRef: tgtRef,
		SystemContext:  opts.SystemContext,
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
	ctx context.Context, ref types.ImageReference, sys *types.SystemContext, platform string,
) (manifest.Manifest, []digest.Digest, []BaselineLayerMeta, []byte, string, error) {
	src, err := ref.NewImageSource(ctx, sys)
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

func readBlobBytes(
	ctx context.Context, ref types.ImageReference, sys *types.SystemContext, d digest.Digest,
) ([]byte, error) {
	return streamBlobBytes(ctx, ref, sys, d, nil)
}

// streamBlobBytes reads a blob and, if onChunk is non-nil, reports each chunk's
// byte count as it arrives. The returned slice holds the full blob bytes.
// Encoders wire onChunk to progress.Layer.Written so the bar animates during
// the read instead of jumping 0→100 % at Done().
func streamBlobBytes(
	ctx context.Context, ref types.ImageReference, sys *types.SystemContext,
	d digest.Digest, onChunk func(int64),
) ([]byte, error) {
	src, err := ref.NewImageSource(ctx, sys)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	r, _, err := src.GetBlob(ctx, types.BlobInfo{Digest: d}, nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readAllReportingChunks(r, onChunk)
}

// readAllReportingChunks drains r into a byte slice and, for every non-zero
// chunk read, invokes onChunk(n). If onChunk is nil this collapses to
// io.ReadAll. Split out so chunking behavior is unit-testable without a live
// ImageSource.
func readAllReportingChunks(r io.Reader, onChunk func(int64)) ([]byte, error) {
	if onChunk == nil {
		return io.ReadAll(r)
	}
	var buf bytes.Buffer
	chunk := make([]byte, 64*1024)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			onChunk(int64(n))
		}
		if err == io.EOF {
			return buf.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}
