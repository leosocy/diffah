// Package importer error types for apply-side correctness and resilience.
// These types sit alongside pkg/diff sentinels but are scoped to importer
// concerns (baseline incompleteness, end-to-end invariant). All implement
// errs.Categorized → CategoryContent so cmd.Execute renders them with
// exit code 4.
package importer

import (
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
	return fmt.Sprintf(
		"image %q: shipped patch %s requires patch source %s, but baseline does not contain it",
		e.ImageName, e.ShippedDigest, e.PatchFromDigest,
	)
}

func (e *ErrMissingPatchSource) Category() errs.Category { return errs.CategoryContent }

func (e *ErrMissingPatchSource) NextAction() string {
	return fmt.Sprintf(
		"image %s: re-run 'diffah diff' against this baseline (patch source %s missing) "+
			"or apply against the original baseline that produced this delta",
		e.ImageName, e.PatchFromDigest,
	)
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

func (e *ErrMissingBaselineReuseLayer) NextAction() string {
	return fmt.Sprintf(
		"image %s: baseline must include layer %s which this delta did not ship — "+
			"pin/add it or re-run diff with a wider baseline",
		e.ImageName, e.LayerDigest,
	)
}

// ErrApplyInvariantFailed — Stage 3 end-to-end check rejected the dest's
// reconstructed manifest. Populated by Phase 2; declared here so all
// importer correctness errors live in one file.
type ErrApplyInvariantFailed struct {
	ImageName  string
	Expected   []digest.Digest
	Got        []digest.Digest
	Missing    []digest.Digest
	Unexpected []digest.Digest
	Reason     string
}

func (e *ErrApplyInvariantFailed) Error() string {
	return fmt.Sprintf("image %q reconstructed mismatch (%s): missing %d layer(s), unexpected %d layer(s)",
		e.ImageName, e.Reason, len(e.Missing), len(e.Unexpected))
}

func (e *ErrApplyInvariantFailed) Category() errs.Category { return errs.CategoryContent }

func (e *ErrApplyInvariantFailed) NextAction() string {
	return fmt.Sprintf("image %s: dest may be partially written; manual cleanup recommended",
		e.ImageName)
}

// isBlobNotFound returns true when err signals "the requested blob does not
// exist at the source," regardless of transport. Auth, TLS, network, and
// timeout errors return false — they are not baseline-incompleteness
// signals and must keep their existing classification.
//
// Three real-world signals are matched, mirroring the existing pattern in
// pkg/diff/classify_registry.go::ClassifyRegistryErr:
//   - os.IsNotExist (dir:, oci:, oci-archive: surface *os.PathError ENOENT)
//   - "Unknown blob" substring (docker-archive: returns this verbatim)
//   - "blob unknown" substring (registry transport per OCI distribution
//     spec: errcode.Error{Code: ErrorCodeBlobUnknown} → "blob unknown to registry")
//
// Wired by Tasks 1.3 (servePatch) and 1.4 (GetBlob) in the same PR1 series;
// declared here so the predicate ships and gets unit-tested (Task 1.2)
// before any caller reaches for it.
//
//nolint:unused // intentional: see comment above.
func isBlobNotFound(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
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
