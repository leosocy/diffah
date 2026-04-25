package exporter

import (
	"github.com/leosocy/diffah/pkg/diff"
)

type Pair struct {
	Name string
	// BaselineRef / TargetRef carry transport-prefixed references that
	// planPair feeds directly to alltransports.ParseImageName. Supported
	// forms include "docker-archive:/tmp/old.tar",
	// "oci-archive:/tmp/new.tar", "oci:/tmp/layout", "dir:/tmp/dir",
	// and "docker://host/repo:tag". Bare paths are rejected; cmd/
	// callers are responsible for normalizing their inputs.
	BaselineRef string
	TargetRef   string
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
