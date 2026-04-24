package importer

import (
	"context"
	"fmt"

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
}

func resolveBaselines(
	ctx context.Context, sc *diff.Sidecar, baselines map[string]string, sysctx *types.SystemContext, strict bool,
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
		ref, err := imageio.ParseReference(raw)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("parse baseline reference %q for %q: %w", raw, img.Name, err)
		}
		src, err := ref.NewImageSource(ctx, sysctx)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("open baseline source for %q: %w",
				img.Name, diff.ClassifyRegistryErr(err, raw))
		}

		manifestBytes, _, err := src.GetManifest(ctx, nil)
		if err != nil {
			_ = src.Close()
			cleanup()
			return nil, fmt.Errorf("read baseline manifest for %q: %w",
				img.Name, diff.ClassifyRegistryErr(err, raw))
		}
		got := digest.FromBytes(manifestBytes)
		if got != img.Baseline.ManifestDigest {
			_ = src.Close()
			cleanup()
			return nil, &diff.ErrBaselineMismatch{
				Name: img.Name, Expected: string(img.Baseline.ManifestDigest), Got: string(got),
			}
		}
		resolved[img.Name] = struct{}{}
		result = append(result, resolvedBaseline{
			Name: img.Name, Ref: ref, Src: src, Manifest: got,
		})
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
// when there is exactly one image and the caller supplied "default" as the key,
// rewrite it to the image's actual name so the per-image lookup in Import works.
func expandDefaultOutput(sc *diff.Sidecar, outputs map[string]string) map[string]string {
	if len(sc.Images) != 1 {
		return outputs
	}
	_, ok := outputs["default"]
	if !ok {
		return outputs
	}
	expanded := make(map[string]string, len(outputs))
	for k, v := range outputs {
		if k == "default" {
			expanded[sc.Images[0].Name] = v
		} else {
			expanded[k] = v
		}
	}
	return expanded
}

func expandDefaultBaseline(sc *diff.Sidecar, baselines map[string]string) map[string]string {
	if len(sc.Images) != 1 {
		return baselines
	}
	_, ok := baselines["default"]
	if !ok {
		return baselines
	}
	expanded := make(map[string]string, len(baselines))
	for k, v := range baselines {
		if k == "default" {
			expanded[sc.Images[0].Name] = v
		} else {
			expanded[k] = v
		}
	}
	return expanded
}
