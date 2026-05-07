package importer

import (
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/pkg/blobinfocache/none"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// runBaselinePreflight checks each apply candidate against that image's own
// baseline source for every baseline-only layer. This closes the cross-image
// masking hole where the shared digest-keyed spool could let image A's
// baseline satisfy image B's missing layer.
func runBaselinePreflight(
	ctx context.Context,
	applyList []string,
	bundle *extractedBundle,
	resolvedByName map[string]resolvedBaseline,
) ([]string, map[string]PreflightResult) {
	filtered := make([]string, 0, len(applyList))
	skipped := make(map[string]PreflightResult)

	for _, name := range applyList {
		if err := ctx.Err(); err != nil {
			skipped[name] = baselinePreflightSkip(name, "", err)
			continue
		}
		img, ok := findImageByName(bundle.sidecar.Images, name)
		if !ok {
			skipped[name] = baselinePreflightSkip(name, "", fmt.Errorf("image %q not found in sidecar", name))
			continue
		}
		rb, ok := resolvedByName[name]
		if !ok {
			skipped[name] = baselinePreflightSkip(name, "", fmt.Errorf("baseline not resolved for image %q", name))
			continue
		}
		missing, err := firstUnavailableBaselineOnlyLayer(ctx, bundle, img, rb.Src)
		if missing == "" && err == nil {
			filtered = append(filtered, name)
			continue
		}
		skipped[name] = baselinePreflightSkip(name, missing, err)
	}
	return filtered, skipped
}

func firstUnavailableBaselineOnlyLayer(
	ctx context.Context,
	bundle *extractedBundle,
	img diff.ImageEntry,
	src types.ImageSource,
) (digest.Digest, error) {
	layers, _, err := readSidecarTargetLayers(bundle, img)
	if err != nil {
		return "", err
	}
	for _, layer := range layers {
		if _, shipped := bundle.sidecar.Blobs[layer.Digest]; shipped {
			continue
		}
		if err := checkBaselineBlobAvailable(ctx, src, layer.Digest); err != nil {
			if isBlobNotFound(err) {
				return layer.Digest, nil
			}
			return layer.Digest, err
		}
	}
	return "", nil
}

func checkBaselineBlobAvailable(
	ctx context.Context, src types.ImageSource, d digest.Digest,
) error {
	rc, _, err := src.GetBlob(ctx, types.BlobInfo{Digest: d}, none.NoCache)
	if err != nil {
		return err
	}
	defer rc.Close()

	var one [1]byte
	if _, err := rc.Read(one[:]); err != nil && err != io.EOF {
		return err
	}
	return nil
}

func baselinePreflightSkip(name string, d digest.Digest, err error) PreflightResult {
	return PreflightResult{
		ImageName:   name,
		Status:      PreflightBaselineMissing,
		LayerDigest: d,
		Err:         err,
	}
}
