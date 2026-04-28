// Package importer error types for apply-side correctness and resilience.
// These types sit alongside pkg/diff sentinels but are scoped to importer
// concerns (baseline incompleteness, end-to-end invariant). All implement
// errs.Categorized → CategoryContent so cmd.Execute renders them with
// exit code 4.
package importer

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// ErrMissingPatchSource (B1) — baseline lacks the patch source layer that
// a shipped patch blob requires for zstd --patch-from reconstruction.
type ErrMissingPatchSource struct {
	ImageName       string
	ShippedDigest   digest.Digest
	PatchFromDigest digest.Digest
}

func (e *ErrMissingPatchSource) Error() string {
	// Preflight constructs this without a shipped digest (it tracks only
	// the missing patch_from set); apply-time construction populates both.
	if e.ShippedDigest == "" {
		return fmt.Sprintf(
			"image %q: target requires patch source %s, but baseline does not contain it",
			e.ImageName, e.PatchFromDigest,
		)
	}
	return fmt.Sprintf(
		"image %q: shipped patch %s requires patch source %s, but baseline does not contain it",
		e.ImageName, e.ShippedDigest, e.PatchFromDigest,
	)
}

func (e *ErrMissingPatchSource) Category() errs.Category { return errs.CategoryContent }

func (*ErrMissingPatchSource) NextAction() string {
	return "re-run 'diffah diff' against this baseline, or apply against the original baseline that produced this delta"
}

// ErrMissingBaselineReuseLayer (B2) — baseline lacks a layer that the
// target manifest references but the delta did not ship.
type ErrMissingBaselineReuseLayer struct {
	ImageName   string
	LayerDigest digest.Digest
}

func (e *ErrMissingBaselineReuseLayer) Error() string {
	return fmt.Sprintf(
		"image %q: target requires layer %s from baseline "+
			"(not shipped in delta), but baseline does not contain it",
		e.ImageName, e.LayerDigest,
	)
}

func (e *ErrMissingBaselineReuseLayer) Category() errs.Category { return errs.CategoryContent }

func (*ErrMissingBaselineReuseLayer) NextAction() string {
	return "baseline must include this layer — pin/add it, or re-run diff with a wider baseline"
}

// ErrApplyInvariantFailed — Stage 3 end-to-end check rejected the dest's
// reconstructed manifest. Populated by Phase 2; declared here so all
// importer correctness errors live in one file.
type ErrApplyInvariantFailed struct {
	ImageName  string
	Missing    []digest.Digest
	Unexpected []digest.Digest
	Reason     string
}

func (e *ErrApplyInvariantFailed) Error() string {
	// Size-mismatch and manifest-digest-mismatch paths populate Reason
	// only and leave both slices empty; suppressing the "missing 0 /
	// unexpected 0" suffix keeps the message honest in those cases.
	if len(e.Missing) == 0 && len(e.Unexpected) == 0 {
		return fmt.Sprintf("image %q reconstructed mismatch (%s)",
			e.ImageName, e.Reason)
	}
	return fmt.Sprintf("image %q reconstructed mismatch (%s): missing %d layer(s), unexpected %d layer(s)",
		e.ImageName, e.Reason, len(e.Missing), len(e.Unexpected))
}

func (e *ErrApplyInvariantFailed) Category() errs.Category { return errs.CategoryContent }

func (*ErrApplyInvariantFailed) NextAction() string {
	return "dest may be partially written; manual cleanup recommended"
}

// isBlobNotFound returns true when err signals "the requested blob does not
// exist at the source," regardless of transport. Auth, TLS, network, and
// timeout errors return false — they are not baseline-incompleteness
// signals and must keep their existing classification.
//
// Three real-world signals are matched, mirroring the existing pattern in
// pkg/diff/classify_registry.go::ClassifyRegistryErr:
//   - errors.Is(err, os.ErrNotExist) (dir:, oci:, oci-archive: surface
//     *os.PathError ENOENT, plus wrapped ENOENT via fmt.Errorf("...: %w", ...)).
//     Callers must wrap this around blob-only fetch paths to avoid
//     misclassifying ENOENT on manifest/index files.
//   - "Unknown blob" substring (docker-archive: returns this verbatim)
//   - "blob unknown" substring (registry transport per OCI distribution
//     spec: errcode.Error{Code: ErrorCodeBlobUnknown} → "blob unknown to registry")
//
// Conservative gap: registry 404s with non-JSON response bodies bypass this
// predicate (containers-image wraps them as "error parsing HTTP 404 response
// body: ...") and remain CategoryEnvironment. Adding a fourth needle for
// "http 404" risks false-positives across unrelated 404s, so we keep the
// predicate conservative; pre-flight (Phase 3) is the catch-all for any
// transport whose 404 shape we don't recognize.
//
// Wired by Tasks 1.3 (servePatch) and 1.4 (GetBlob) in the same PR1 series;
// declared here so the predicate ships and gets unit-tested (Task 1.2)
// before any caller reaches for it.
func isBlobNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unknown blob") {
		return true
	}
	if strings.Contains(msg, "blob unknown") {
		return true
	}
	return false
}

var (
	_ errs.Categorized = (*ErrMissingPatchSource)(nil)
	_ errs.Categorized = (*ErrMissingBaselineReuseLayer)(nil)
	_ errs.Categorized = (*ErrApplyInvariantFailed)(nil)
	_ errs.Advised     = (*ErrMissingPatchSource)(nil)
	_ errs.Advised     = (*ErrMissingBaselineReuseLayer)(nil)
	_ errs.Advised     = (*ErrApplyInvariantFailed)(nil)
)
