package importer

import (
	"github.com/leosocy/diffah/pkg/diff"
)

func validatePositionalBaseline(sc *diff.Sidecar, baselines map[string]string) error {
	if len(sc.Images) == 1 {
		return nil
	}
	for name := range baselines {
		if name == "default" || name == "" {
			return &diff.ErrMultiImageNeedsNamedBaselines{N: len(sc.Images)}
		}
	}
	return nil
}
