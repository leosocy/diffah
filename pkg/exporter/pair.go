package exporter

import (
	"github.com/leosocy/diffah/pkg/diff"
)

type Pair struct {
	Name         string
	BaselinePath string
	TargetPath   string
}

func ValidatePairs(pairs []Pair) error {
	if len(pairs) == 0 {
		return &diff.ErrInvalidBundleSpec{Reason: "pairs must be non-empty"}
	}
	seen := make(map[string]struct{}, len(pairs))
	for _, p := range pairs {
		if _, dup := seen[p.Name]; dup {
			return &diff.ErrDuplicateBundleName{Name: p.Name}
		}
		seen[p.Name] = struct{}{}
	}
	return nil
}
