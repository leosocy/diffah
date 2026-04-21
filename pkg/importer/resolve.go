package importer

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"go.podman.io/image/v5/types"
)

type resolvedBaseline struct {
	Name     string
	Ref      types.ImageReference
	Manifest digest.Digest
}

func resolveBaselines(
	ctx context.Context, sc *diff.Sidecar, baselines map[string]string, strict bool,
) ([]resolvedBaseline, error) {
	result := make([]resolvedBaseline, 0, len(sc.Images))
	resolved := make(map[string]struct{}, len(sc.Images))

	expanded := expandDefaultBaseline(sc, baselines)

	for _, img := range sc.Images {
		path, ok := expanded[img.Name]
		if !ok {
			if strict {
				return nil, &diff.ErrBaselineMissing{Names: []string{img.Name}}
			}
			continue
		}
		ref, err := imageio.OpenArchiveRef(path)
		if err != nil {
			return nil, fmt.Errorf("open baseline %s for %q: %w", path, img.Name, err)
		}
		src, err := ref.NewImageSource(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("open baseline source for %q: %w", img.Name, err)
		}
		raw, _, err := src.GetManifest(ctx, nil)
		src.Close()
		if err != nil {
			return nil, fmt.Errorf("read baseline manifest for %q: %w", img.Name, err)
		}
		got := digest.FromBytes(raw)
		if got != img.Baseline.ManifestDigest {
			return nil, &diff.ErrBaselineMismatch{
				Name: img.Name, Expected: string(img.Baseline.ManifestDigest), Got: string(got),
			}
		}
		resolved[img.Name] = struct{}{}
		result = append(result, resolvedBaseline{
			Name: img.Name, Ref: ref, Manifest: got,
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
			return nil, &diff.ErrBaselineMissing{Names: missing}
		}
	}

	return result, nil
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
