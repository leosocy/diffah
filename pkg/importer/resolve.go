package importer

import (
	"context"
	"fmt"
	"time"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
)

type resolvedBaseline struct {
	Name     string
	Ref      types.ImageReference
	Src      types.ImageSource
	Manifest digest.Digest
	// ManifestBytes / ManifestMime are populated by openOneBaseline so
	// downstream consumers (preflight) can avoid a second GetManifest
	// round-trip against the baseline source.
	ManifestBytes []byte
	ManifestMime  string
}

func resolveBaselines(
	ctx context.Context,
	sc *diff.Sidecar,
	baselines map[string]string,
	sysctx *types.SystemContext,
	retryTimes int,
	retryDelay time.Duration,
	strict bool,
) ([]resolvedBaseline, error) {
	result := make([]resolvedBaseline, 0, len(sc.Images))
	resolved := make(map[string]struct{}, len(sc.Images))

	expanded := expandDefaultBaseline(sc, baselines)

	// cleanup closes any sources already opened if we return an error partway through.
	cleanup := func() {
		closeResolvedBaselines(result)
	}

	for _, img := range sc.Images {
		raw, ok := expanded[img.Name]
		if !ok {
			continue
		}
		rb, err := openOneBaseline(ctx, img, raw, sysctx, retryTimes, retryDelay)
		if err != nil {
			cleanup()
			return nil, err
		}
		resolved[img.Name] = struct{}{}
		result = append(result, rb)
	}

	if strict {
		var missing []string
		for _, img := range sc.Images {
			if _, ok := resolved[img.Name]; !ok {
				missing = append(missing, img.Name)
			}
		}
		if len(missing) > 0 {
			cleanup()
			return nil, &diff.ErrBaselineMissing{Names: missing}
		}
	}

	if err := rejectUnknownBaselineNames(sc, expanded); err != nil {
		cleanup()
		return nil, err
	}

	return result, nil
}

// openOneBaseline parses raw, opens the ImageSource, verifies its manifest
// digest against the sidecar's expectation, and returns the held-open
// resolvedBaseline. Registry errors are classified before they propagate so
// exit-code categorisation sees the typed error through the %w chain.
func openOneBaseline(
	ctx context.Context,
	img diff.ImageEntry,
	raw string,
	sysctx *types.SystemContext,
	retryTimes int,
	retryDelay time.Duration,
) (resolvedBaseline, error) {
	ref, err := imageio.ParseReference(raw)
	if err != nil {
		return resolvedBaseline{}, fmt.Errorf("parse baseline reference %q for %q: %w", raw, img.Name, err)
	}
	src, err := withRetry(ctx, retryTimes, retryDelay, func(ctx context.Context) (types.ImageSource, error) {
		return ref.NewImageSource(ctx, sysctx)
	})
	if err != nil {
		return resolvedBaseline{}, fmt.Errorf("open baseline source for %q: %w",
			img.Name, diff.ClassifyRegistryErr(err, raw))
	}
	type manifestPayload struct {
		bytes []byte
		mime  string
	}
	mf, err := withRetry(ctx, retryTimes, retryDelay, func(ctx context.Context) (manifestPayload, error) {
		b, mime, e := src.GetManifest(ctx, nil)
		return manifestPayload{bytes: b, mime: mime}, e
	})
	if err != nil {
		_ = src.Close()
		return resolvedBaseline{}, fmt.Errorf("read baseline manifest for %q: %w",
			img.Name, diff.ClassifyRegistryErr(err, raw))
	}
	got := digest.FromBytes(mf.bytes)
	if got != img.Baseline.ManifestDigest {
		_ = src.Close()
		return resolvedBaseline{}, &diff.ErrBaselineMismatch{
			Name: img.Name, Expected: string(img.Baseline.ManifestDigest), Got: string(got),
		}
	}
	return resolvedBaseline{
		Name:          img.Name,
		Ref:           ref,
		Src:           src,
		Manifest:      got,
		ManifestBytes: mf.bytes,
		ManifestMime:  mf.mime,
	}, nil
}

// closeResolvedBaselines closes all held-open ImageSource instances.
// Safe to call with a nil or partially-populated slice.
func closeResolvedBaselines(list []resolvedBaseline) {
	for _, rb := range list {
		if rb.Src != nil {
			_ = rb.Src.Close()
		}
	}
}

func rejectUnknownBaselineNames(sc *diff.Sidecar, expanded map[string]string) error {
	knownNames := make(map[string]struct{}, len(sc.Images))
	for _, img := range sc.Images {
		knownNames[img.Name] = struct{}{}
	}
	for name := range expanded {
		if _, ok := knownNames[name]; !ok {
			names := make([]string, 0, len(sc.Images))
			for _, img := range sc.Images {
				names = append(names, img.Name)
			}
			return &diff.ErrBaselineNameUnknown{Name: name, Available: names}
		}
	}
	return nil
}

// expandDefaultOutput mirrors expandDefaultBaseline for the Outputs map:
// when there is exactly one image and the caller supplied the defaultImageKey
// as the key, rewrite it to the image's actual name so the per-image lookup
// in Import works.
func expandDefaultOutput(sc *diff.Sidecar, outputs map[string]string) map[string]string {
	return rewriteDefaultKey(sc, outputs)
}

func expandDefaultBaseline(sc *diff.Sidecar, baselines map[string]string) map[string]string {
	return rewriteDefaultKey(sc, baselines)
}

func rewriteDefaultKey(sc *diff.Sidecar, m map[string]string) map[string]string {
	if len(sc.Images) != 1 {
		return m
	}
	if _, ok := m[defaultImageKey]; !ok {
		return m
	}
	expanded := make(map[string]string, len(m))
	for k, v := range m {
		if k == defaultImageKey {
			expanded[sc.Images[0].Name] = v
		} else {
			expanded[k] = v
		}
	}
	return expanded
}
