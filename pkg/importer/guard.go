package importer

import (
	"github.com/leosocy/diffah/pkg/diff"
)

// defaultImageKey is the sentinel name a single-image invocation uses in
// BASELINE-SPEC / OUTPUT-SPEC (and the legacy apply/unbundle entry points).
// It is remapped to the actual sidecar image name by expandDefaultBaseline /
// expandDefaultOutput before resolution.
const defaultImageKey = "default"

func validatePositionalBaseline(sc *diff.Sidecar, baselines map[string]string) error {
	if len(sc.Images) == 1 {
		return nil
	}
	for name := range baselines {
		if name == defaultImageKey || name == "" {
			return &diff.ErrMultiImageNeedsNamedBaselines{N: len(sc.Images)}
		}
	}
	return nil
}
