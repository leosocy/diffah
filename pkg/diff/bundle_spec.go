package diff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.podman.io/image/v5/transports/alltransports"
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
		if err := validateTransportRef(p.Baseline); err != nil {
			return nil, &ErrBundleSpecMissingTransport{
				FieldPath: fmt.Sprintf("pairs[%d].baseline", i),
				Value:     p.Baseline,
			}
		}
		p.Baseline = resolveTransportPrefixedPath(base, p.Baseline)

		if err := validateTransportRef(p.Target); err != nil {
			return nil, &ErrBundleSpecMissingTransport{
				FieldPath: fmt.Sprintf("pairs[%d].target", i),
				Value:     p.Target,
			}
		}
		p.Target = resolveTransportPrefixedPath(base, p.Target)
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
	for name, ref := range spec.Baselines {
		if !nameRegex.MatchString(name) {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"name %q does not match %s", name, nameRegex)}
		}
		if err := validateTransportRef(ref); err != nil {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"baselines[%q]: %s", name, err.Error())}
		}
	}
	return &spec, nil
}

// OutputSpec is the parsed form of an OUTPUT-SPEC JSON file used by
// 'diffah unbundle' to map each image name in the bundle to a fully-
// qualified transport-prefixed destination reference.
type OutputSpec struct {
	Outputs map[string]string `json:"outputs"`
}

// ParseOutputSpec reads a JSON file of the form:
//
//	{"outputs": {"<name>": "<transport>:<path-or-url>", ...}}
//
// Every value must carry a transport prefix accepted by
// go.podman.io/image/v5/transports/alltransports. Returns
// *ErrInvalidBundleSpec on any shape/content failure.
func ParseOutputSpec(path string) (*OutputSpec, error) {
	// Early guard: a directory here is almost always a user typo —
	// they passed the pre-Phase-2 OUTPUT-DIR positional by mistake.
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		return nil, &ErrInvalidBundleSpec{
			Path: path,
			Reason: "OUTPUT-SPEC must be a JSON file, not a directory " +
				"(see 'diffah unbundle --help')",
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	// Reject unknown top-level keys so we do not silently swallow typos
	// (e.g. "output" instead of "outputs").
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	if _, ok := probe["outputs"]; !ok {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: `missing required field "outputs"`}
	}

	var spec OutputSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	if len(spec.Outputs) == 0 {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: "outputs must be non-empty"}
	}
	for name, ref := range spec.Outputs {
		if !nameRegex.MatchString(name) {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"outputs[%q] name does not match %s", name, nameRegex)}
		}
		if err := validateTransportRef(ref); err != nil {
			return nil, &ErrInvalidBundleSpec{Path: path, Reason: fmt.Sprintf(
				"outputs[%q]: %s", name, err.Error())}
		}
	}
	return &spec, nil
}

// validateTransportRef checks that ref has a supported transport
// prefix and parses under alltransports. Rejects bare filesystem paths.
func validateTransportRef(ref string) error {
	colon := strings.Index(ref, ":")
	if colon <= 0 {
		return fmt.Errorf("missing transport prefix: %q (expected e.g. docker-archive:%s)", ref, ref)
	}
	if _, err := alltransports.ParseImageName(ref); err != nil {
		return fmt.Errorf("invalid image reference %q: %w", ref, err)
	}
	return nil
}

// resolveTransportPrefixedPath resolves relative filesystem paths inside
// file-backed transports against the spec-file directory. Registry
// transports (docker://) are returned unchanged.
func resolveTransportPrefixedPath(base, ref string) string {
	prefix, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return ref // unreachable after validateTransportRef; belt-and-braces
	}
	switch prefix {
	case "docker-archive", "oci-archive", "oci", "dir":
		pathPart := rest
		tail := ""
		if idx := strings.Index(rest, ":"); idx >= 0 {
			pathPart, tail = rest[:idx], rest[idx:]
		}
		if !filepath.IsAbs(pathPart) {
			pathPart = filepath.Join(base, pathPart)
		}
		return prefix + ":" + pathPart + tail
	default:
		return ref
	}
}
