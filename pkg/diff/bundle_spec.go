package diff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type BundlePairSpec struct {
	Name     string `json:"name"`
	Baseline string `json:"baseline"`
	Target   string `json:"target"`
}

type BundleSpec struct {
	Pairs []BundlePairSpec `json:"pairs"`
}

func ParseBundleSpec(path string) (*BundleSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	var spec BundleSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	if len(spec.Pairs) == 0 {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: "pairs must be non-empty"}
	}
	seen := make(map[string]struct{}, len(spec.Pairs))
	base := filepath.Dir(path)
	for i := range spec.Pairs {
		p := &spec.Pairs[i]
		if !nameRegex.MatchString(p.Name) {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"pairs[%d].name %q does not match %s", i, p.Name, nameRegex)}
		}
		if _, dup := seen[p.Name]; dup {
			return nil, &ErrDuplicateBundleName{Name: p.Name}
		}
		seen[p.Name] = struct{}{}
		if p.Baseline == "" || p.Target == "" {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"pairs[%d] requires baseline and target", i)}
		}
		p.Baseline = resolveSpecPath(base, p.Baseline)
		p.Target = resolveSpecPath(base, p.Target)
	}
	return &spec, nil
}

type BaselineSpec struct {
	Baselines map[string]string `json:"baselines"`
}

func ParseBaselineSpec(path string) (*BaselineSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	var spec BaselineSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	if len(spec.Baselines) == 0 {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: "baselines must be non-empty"}
	}
	base := filepath.Dir(path)
	resolved := make(map[string]string, len(spec.Baselines))
	for name, p := range spec.Baselines {
		if !nameRegex.MatchString(name) {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"name %q does not match %s", name, nameRegex)}
		}
		resolved[name] = resolveSpecPath(base, p)
	}
	spec.Baselines = resolved
	return &spec, nil
}

func resolveSpecPath(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
