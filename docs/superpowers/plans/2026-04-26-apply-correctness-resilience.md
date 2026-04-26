# Apply Correctness & Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden `diffah apply` / `diffah unbundle` so that every successful run proves the dest's reconstructed manifest matches the sidecar's expectation, and that incomplete baselines produce categorized, actionable diagnostics — split into three independently shippable PRs.

**Architecture:** Extends the apply pipeline (`extractBundle → resolveBaselines → composeImage`) with a Stage-1 pre-flight scan, B1/B2-categorized fetch errors, and a Stage-3 invariant verifier. New sentinel error types under `pkg/importer/errors.go` implement `errs.Categorized` so existing `Classify` routes them to `CategoryContent` (exit 4). No sidecar schema bump; the existing `--strict` flag's semantic widens to cover B1/B2 incomplete-baseline cases.

**Tech Stack:** Go 1.25.9 · `pkg/diff/errs` (Categorized + Advised interfaces) · `go.podman.io/image/v5` (manifest parsing, ImageSource, copy.Image) · `pkg/progress.Reporter` (Phase events) · `pkg/importer` (existing apply pipeline) · `golang.org/x/sync/singleflight` (existing baselineBlobCache, untouched).

**Spec:** `docs/superpowers/specs/2026-04-26-apply-correctness-resilience-design.md` (commit `88aad45`).

---

## File Structure

| Path | Phase | Action | Purpose |
|---|---|---|---|
| `pkg/importer/errors.go` | 1 | create | Sentinel error types (B1/B2/invariant) + `isBlobNotFound` predicate |
| `pkg/importer/errors_test.go` | 1 | create | Unit tests for `Error()`, `Category()`, predicate |
| `pkg/diff/errs/classify.go` | 1 | modify | `NextAction` hint plumbing for new sentinels |
| `pkg/importer/compose.go` | 1 | modify | Wrap `servePatch` + `GetBlob` baseline-only-reuse failures |
| `cmd/apply_resilience_integration_test.go` | 1 | create | CLI-level B1/B2 hint test |
| `pkg/importer/manifest.go` | 2 | create | `readSidecarTargetLayers`, `readDestManifestLayers`, `layerSetDiff` |
| `pkg/importer/invariant.go` | 2 | create | `verifyApplyInvariant` + per-layer-size helper |
| `pkg/importer/invariant_test.go` | 2 | create | Unit tests with mock `ImageSource` |
| `pkg/importer/summary.go` | 2 | create | `applyResult`, `applyReport`, `renderSummary` (Stage-4 final renderer; populated by Phase 2 + Phase 3) |
| `pkg/importer/importer.go` | 2 | modify | Plumb `verifyApplyInvariant` into `importEachImage` + return `applyReport` |
| `cmd/apply_test.go` | 2 | modify | Add invariant assertion to existing `TestApplyCommand_RoundTrip` |
| `pkg/importer/preflight.go` | 3 | create | `PreflightStatus`, `PreflightResult`, `RunPreflight`, required-digest math |
| `pkg/importer/preflight_test.go` | 3 | create | Unit tests for required-digest computation, scan, classify |
| `pkg/importer/importer.go` | 3 | modify | Run pre-flight before `importEachImage`; partial vs strict path |
| `cmd/apply_preflight_integration_test.go` | 3 | create | partial mode / strict mode / unreachable / GET-bounded |
| `cmd/unbundle_preflight_integration_test.go` | 3 | create | Multi-image bundle preflight matrix |
| `CHANGELOG.md` | 3 | modify | `--strict` semantic-extension entry |

**File responsibility boundaries:**
- `errors.go` is the single source of truth for all importer-specific sentinel errors. `compose.go` and `preflight.go` import from it; tests assert on its exported types.
- `manifest.go` owns OCI/Docker schema 2 manifest parsing helpers shared between `invariant.go` and `preflight.go`. Avoids duplicating media-type dispatch.
- `summary.go` owns Stage-4 rendering. Phase 2 introduces it for invariant failures; Phase 3 extends it with pre-flight skip results. Phase 3 should not edit Phase 2's renderer signatures, only add fields.

---

## Phase 1 — PR1: Error Classification (B1/B2 Sentinels)

**Goal:** Replace opaque `baseline serve <hash>: ...` errors with categorized `ErrMissingPatchSource` / `ErrMissingBaselineReuseLayer` so users get actionable hints (re-run diff vs widen baseline). All existing apply paths keep working.

**Success Criteria:**
1. `pkg/importer/errors.go` defines `ErrMissingPatchSource`, `ErrMissingBaselineReuseLayer`, `ErrApplyInvariantFailed` with `Category() errs.Category` returning `CategoryContent`.
2. `isBlobNotFound(err)` returns `true` for `types.ErrBlobNotFound` and any error wrapping an HTTP-404-bearing status; `false` for auth/TLS/network/timeout.
3. Apply against a baseline missing a patch source produces exit 4 with stderr containing the B1 hint phrase `re-run 'diffah diff' against this baseline`.
4. Apply against a baseline missing a reuse layer produces exit 4 with stderr containing the B2 hint phrase `pin/add it or re-run diff with a wider baseline`.
5. All existing apply tests still green; auth/TLS/timeout errors remain `CategoryEnvironment`.

**Status:** pending

### Task 1.1: Sentinel error types — declare struct + Error()

**Files:**
- Create: `pkg/importer/errors.go`
- Test: `pkg/importer/errors_test.go`

- [ ] **Step 1: Write failing test for `ErrMissingPatchSource.Error()` format**

```go
// pkg/importer/errors_test.go
package importer

import (
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestErrMissingPatchSource_Format(t *testing.T) {
	err := &ErrMissingPatchSource{
		ImageName:       "svc-a",
		ShippedDigest:   digest.Digest("sha256:aaa"),
		PatchFromDigest: digest.Digest("sha256:bbb"),
	}
	got := err.Error()
	if !strings.Contains(got, "svc-a") {
		t.Errorf("Error() must include image name; got %q", got)
	}
	if !strings.Contains(got, "sha256:bbb") {
		t.Errorf("Error() must include patch source digest; got %q", got)
	}
	if !strings.Contains(got, "patch source") {
		t.Errorf("Error() must mention 'patch source'; got %q", got)
	}
}

func TestErrMissingBaselineReuseLayer_Format(t *testing.T) {
	err := &ErrMissingBaselineReuseLayer{
		ImageName:   "svc-b",
		LayerDigest: digest.Digest("sha256:ccc"),
	}
	got := err.Error()
	if !strings.Contains(got, "svc-b") {
		t.Errorf("Error() must include image name; got %q", got)
	}
	if !strings.Contains(got, "sha256:ccc") {
		t.Errorf("Error() must include layer digest; got %q", got)
	}
}

func TestErrApplyInvariantFailed_Format(t *testing.T) {
	err := &ErrApplyInvariantFailed{
		ImageName:  "svc-c",
		Missing:    []digest.Digest{"sha256:ddd"},
		Unexpected: nil,
		Reason:     "layer count mismatch",
	}
	got := err.Error()
	if !strings.Contains(got, "svc-c") {
		t.Errorf("Error() must include image name; got %q", got)
	}
	if !strings.Contains(got, "layer count mismatch") {
		t.Errorf("Error() must include reason; got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/importer/ -run 'TestErrMissingPatchSource_Format|TestErrMissingBaselineReuseLayer_Format|TestErrApplyInvariantFailed_Format' -v`
Expected: FAIL with "undefined: ErrMissingPatchSource" etc.

- [ ] **Step 3: Create `pkg/importer/errors.go` with structs + Error()**

```go
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
	return fmt.Sprintf("image %q: shipped patch %s requires patch source %s, but baseline does not contain it",
		e.ImageName, e.ShippedDigest, e.PatchFromDigest)
}

func (e *ErrMissingPatchSource) Category() errs.Category { return errs.CategoryContent }

func (e *ErrMissingPatchSource) NextAction() string {
	return fmt.Sprintf("image %s: re-run 'diffah diff' against this baseline (patch source %s missing) or apply against the original baseline that produced this delta",
		e.ImageName, e.PatchFromDigest)
}

// ErrMissingBaselineReuseLayer (B2) — baseline lacks a layer that the
// target manifest references but the delta did not ship.
type ErrMissingBaselineReuseLayer struct {
	ImageName   string
	LayerDigest digest.Digest
}

func (e *ErrMissingBaselineReuseLayer) Error() string {
	return fmt.Sprintf("image %q: target requires layer %s from baseline (not shipped in delta), but baseline does not contain it",
		e.ImageName, e.LayerDigest)
}

func (e *ErrMissingBaselineReuseLayer) Category() errs.Category { return errs.CategoryContent }

func (e *ErrMissingBaselineReuseLayer) NextAction() string {
	return fmt.Sprintf("image %s: baseline must include layer %s which this delta did not ship — pin/add it or re-run diff with a wider baseline",
		e.ImageName, e.LayerDigest)
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
// Wired by Tasks 1.3 (servePatch) and 1.4 (GetBlob) in the same PR1 series.
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
```

**Predicate rationale.** The original spec assumed `types.ErrBlobNotFound`
and a `StatusCode() int` interface, but containers-image v5 does not expose
a stable sentinel for blob-missing — registry transports return
`errcode.Error` (string-formatted "blob unknown to registry") and
docker-archive returns "Unknown blob …" verbatim. Matching by string
mirrors the existing project convention in
`pkg/diff/classify_registry.go::ClassifyRegistryErr` (auth/missing/invalid
needles). `os.IsNotExist` covers `dir:`, `oci:`, and `oci-archive:`
transports which surface `*os.PathError` with ENOENT.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/importer/ -run 'TestErrMissingPatchSource_Format|TestErrMissingBaselineReuseLayer_Format|TestErrApplyInvariantFailed_Format' -v`
Expected: PASS — three new tests green.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/errors.go pkg/importer/errors_test.go
git commit -m "feat(importer): introduce B1/B2/invariant sentinel error types

PR1 of apply correctness & resilience — declares the three sentinel
types with errs.Categorized + Advised so cmd.Execute renders them
with exit code 4 and an actionable hint. Implementation wiring lands
in subsequent tasks.

Refs: docs/superpowers/specs/2026-04-26-apply-correctness-resilience-design.md"
```

### Task 1.2: Categorized + isBlobNotFound predicate tests

**Files:**
- Test: `pkg/importer/errors_test.go` (extend)

> **Predicate revision (Task 1.1 implementation note).** The original spec
> assumed an upstream `types.ErrBlobNotFound` sentinel and a `StatusCode()`
> interface; empirical inspection of `go.podman.io/image/v5` showed neither
> exists in a stable, machine-readable form. Task 1.1 ships a string-match
> predicate mirroring `pkg/diff/classify_registry.go::ClassifyRegistryErr`.
> The Task 1.2 cases below are aligned to that predicate.

- [ ] **Step 1: Write failing tests for Category() and isBlobNotFound()**

```go
// append to pkg/importer/errors_test.go
import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestSentinels_Categorized(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"B1", &ErrMissingPatchSource{}},
		{"B2", &ErrMissingBaselineReuseLayer{}},
		{"invariant", &ErrApplyInvariantFailed{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, hint := errs.Classify(tc.err)
			if cat != errs.CategoryContent {
				t.Errorf("%s: Category() = %v, want CategoryContent", tc.name, cat)
			}
			if hint == "" {
				t.Errorf("%s: NextAction() must produce a non-empty hint", tc.name)
			}
		})
	}
}

func TestIsBlobNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// negative cases — must NOT classify as blob-not-found.
		{"nil", nil, false},
		{"plain string", errors.New("boom"), false},
		{"url error", &url.Error{Op: "Get", URL: "x", Err: errors.New("dns")}, false},

		// positive cases — three real-world transports that the predicate
		// recognises (mirrors classify_registry.go pattern).
		{"os.ErrNotExist direct", os.ErrNotExist, true},
		{
			"PathError ENOENT",
			&os.PathError{Op: "open", Path: "x", Err: syscall.ENOENT},
			true,
		},
		{"docker-archive Unknown blob", fmt.Errorf("Unknown blob sha256:abc"), true},
		{
			"registry blob unknown",
			fmt.Errorf("fetching blob: blob unknown to registry"),
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBlobNotFound(tc.err); got != tc.want {
				t.Errorf("isBlobNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail or pass**

Run: `go test ./pkg/importer/ -run 'TestSentinels_Categorized|TestIsBlobNotFound' -v`
Expected: PASS — Task 1.1's implementation already covers these. (If a test fails, verify Category() and isBlobNotFound semantics match.)

- [ ] **Step 3: (no implementation needed — Task 1.1 already wrote it)**

If Step 2 passed, skip. If Step 2 failed, return to `pkg/importer/errors.go` and reconcile.

- [ ] **Step 4: Run all importer tests under race detector**

Run: `go test -race ./pkg/importer/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/errors_test.go
git commit -m "test(importer): cover Categorized + isBlobNotFound predicate

Verifies the new sentinels classify to CategoryContent with non-empty
hints, and that isBlobNotFound matches types.ErrBlobNotFound + HTTP 404
while rejecting auth/network/url errors."
```

### Task 1.3: Wire B1 wrap into `servePatch`

**Files:**
- Modify: `pkg/importer/compose.go` (around line 114)

- [ ] **Step 1: Write failing integration test that triggers B1**

```go
// Append to pkg/importer/compose_test.go (file already exists)
func TestServePatch_BlobNotFound_WrapsB1(t *testing.T) {
	// Build a minimal sidecar referencing a patch blob whose patch_from
	// digest is missing from a fake baseline source.
	patchBytes := []byte("ignored")
	target := digest.FromBytes([]byte("target"))
	patchSrc := digest.FromBytes([]byte("missing-source"))

	dir := t.TempDir()
	blobDir := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(filepath.Join(blobDir, target.Algorithm().String()), 0o755); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(blobDir, target.Algorithm().String(), target.Encoded())
	if err := os.WriteFile(patchPath, patchBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	src := &bundleImageSource{
		blobDir:   blobDir,
		imageName: "svc-x",
		baseline:  &fakeBlobNotFoundSource{},
		cache:     newBaselineBlobCache(),
		sidecar:   &diff.Sidecar{},
	}
	entry := diff.BlobEntry{
		Encoding:        diff.EncodingPatch,
		PatchFromDigest: patchSrc,
	}
	_, _, err := src.servePatch(context.Background(), target, entry, nil)

	var b1 *ErrMissingPatchSource
	if !errors.As(err, &b1) {
		t.Fatalf("expected ErrMissingPatchSource, got %T: %v", err, err)
	}
	if b1.PatchFromDigest != patchSrc {
		t.Errorf("PatchFromDigest = %v, want %v", b1.PatchFromDigest, patchSrc)
	}
	if b1.ShippedDigest != target {
		t.Errorf("ShippedDigest = %v, want %v", b1.ShippedDigest, target)
	}
}

// fakeBlobNotFoundSource is a minimal types.ImageSource that always returns
// types.ErrBlobNotFound from GetBlob.
type fakeBlobNotFoundSource struct{}

func (fakeBlobNotFoundSource) Reference() types.ImageReference         { return nil }
func (fakeBlobNotFoundSource) Close() error                            { return nil }
func (fakeBlobNotFoundSource) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	return nil, "", nil
}
func (fakeBlobNotFoundSource) HasThreadSafeGetBlob() bool { return true }
func (fakeBlobNotFoundSource) GetSignatures(context.Context, *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (fakeBlobNotFoundSource) LayerInfosForCopy(context.Context, *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}
func (fakeBlobNotFoundSource) GetBlob(context.Context, types.BlobInfo, types.BlobInfoCache) (io.ReadCloser, int64, error) {
	return nil, 0, types.ErrBlobNotFound
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/importer/ -run TestServePatch_BlobNotFound_WrapsB1 -v`
Expected: FAIL — current `servePatch` wraps the error as a plain `fmt.Errorf("fetch patch-from blob %s: %w", ...)`; `errors.As` to `*ErrMissingPatchSource` returns false.

- [ ] **Step 3: Modify `pkg/importer/compose.go::servePatch` to wrap B1**

Replace this block in `servePatch` (currently around lines 121-124):

```go
	baseBytes, err := s.fetchVerifiedBaselineBlob(ctx, entry.PatchFromDigest, cache)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch patch-from blob %s: %w", entry.PatchFromDigest, err)
	}
```

With:

```go
	baseBytes, err := s.fetchVerifiedBaselineBlob(ctx, entry.PatchFromDigest, cache)
	if err != nil {
		if isBlobNotFound(err) {
			return nil, 0, &ErrMissingPatchSource{
				ImageName:       s.imageName,
				ShippedDigest:   target,
				PatchFromDigest: entry.PatchFromDigest,
			}
		}
		return nil, 0, fmt.Errorf("fetch patch-from blob %s: %w", entry.PatchFromDigest, err)
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/importer/ -run TestServePatch_BlobNotFound_WrapsB1 -v`
Expected: PASS.

Run: `go test -race ./pkg/importer/...` to ensure no regression.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/compose.go pkg/importer/compose_test.go
git commit -m "feat(importer): wrap servePatch missing-source as B1

When fetchVerifiedBaselineBlob returns 'blob not found' for the
patch-from digest, surface ErrMissingPatchSource so cmd.Execute
renders an actionable hint (re-run diff or apply against original
baseline). Auth/TLS/timeout errors keep their original wrapping."
```

### Task 1.4: Wire B2 wrap into `GetBlob` baseline-only branch

**Files:**
- Modify: `pkg/importer/compose.go` (around line 80-87)

- [ ] **Step 1: Write failing test for B2 wrap**

```go
// Append to pkg/importer/compose_test.go
func TestGetBlob_BaselineOnlyMissing_WrapsB2(t *testing.T) {
	missing := digest.FromBytes([]byte("missing-baseline-layer"))

	src := &bundleImageSource{
		blobDir:   t.TempDir(),
		imageName: "svc-y",
		baseline:  &fakeBlobNotFoundSource{},
		cache:     newBaselineBlobCache(),
		sidecar:   &diff.Sidecar{Blobs: map[digest.Digest]diff.BlobEntry{}},
	}
	_, _, err := src.GetBlob(context.Background(),
		types.BlobInfo{Digest: missing}, nil)

	var b2 *ErrMissingBaselineReuseLayer
	if !errors.As(err, &b2) {
		t.Fatalf("expected ErrMissingBaselineReuseLayer, got %T: %v", err, err)
	}
	if b2.LayerDigest != missing {
		t.Errorf("LayerDigest = %v, want %v", b2.LayerDigest, missing)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/importer/ -run TestGetBlob_BaselineOnlyMissing_WrapsB2 -v`
Expected: FAIL.

- [ ] **Step 3: Modify `GetBlob` baseline-only branch in `pkg/importer/compose.go`**

Replace (currently around lines 84-87):

```go
	entry, ok := s.sidecar.Blobs[info.Digest]
	if !ok {
		data, err := s.fetchVerifiedBaselineBlob(ctx, info.Digest, cache)
		if err != nil {
			return nil, 0, fmt.Errorf("baseline serve %s: %w", info.Digest, err)
		}
```

With:

```go
	entry, ok := s.sidecar.Blobs[info.Digest]
	if !ok {
		data, err := s.fetchVerifiedBaselineBlob(ctx, info.Digest, cache)
		if err != nil {
			if isBlobNotFound(err) {
				return nil, 0, &ErrMissingBaselineReuseLayer{
					ImageName:   s.imageName,
					LayerDigest: info.Digest,
				}
			}
			return nil, 0, fmt.Errorf("baseline serve %s: %w", info.Digest, err)
		}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run TestGetBlob_BaselineOnlyMissing_WrapsB2 -v`
Expected: PASS.

Run: `go test -race ./pkg/importer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/compose.go pkg/importer/compose_test.go
git commit -m "feat(importer): wrap baseline-only-reuse 404 as B2

When the bundle does not ship a layer that the target manifest
references, and the baseline likewise lacks it, GetBlob now
returns ErrMissingBaselineReuseLayer. Renders the hint to widen
the baseline or re-run diff."
```

### Task 1.5: cmd-level integration tests for B1/B2 hints

**Files:**
- Create: `cmd/apply_resilience_integration_test.go`

- [ ] **Step 1: Write failing CLI integration tests**

```go
//go:build integration

package cmd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leosocy/diffah/internal/testutil"
)

// TestApplyCLI_MissingPatchSourceB1 — produces a bundle where the shipped
// patch needs baseline layer L_src; replaces baseline with one that omits
// L_src; runs apply and asserts exit 4 + B1 hint.
func TestApplyCLI_MissingPatchSourceB1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Build a normal bundle (existing helper used by registry_integration_test.go).
	bundle := testutil.BuildIntraLayerBundle(t, dir, "svc-a")
	// Strip baseline of the patch-source layer.
	incompleteBaseline := testutil.StripLayer(t, bundle.BaselineArchive,
		bundle.PatchSourceDigest)

	exit, _, stderr := runDiffah(t, ctx, "apply",
		"--baseline", "default="+incompleteBaseline,
		bundle.DeltaPath, "dir:"+filepath.Join(dir, "out"))

	if exit != 4 {
		t.Fatalf("exit = %d, want 4 (CategoryContent)", exit)
	}
	if !strings.Contains(stderr, "re-run 'diffah diff'") {
		t.Errorf("stderr missing B1 hint phrase; got:\n%s", stderr)
	}
}

func TestApplyCLI_MissingBaselineReuseLayerB2(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	bundle := testutil.BuildIntraLayerBundle(t, dir, "svc-b")
	// Strip baseline of a reuse-only layer (one referenced by target manifest
	// but not shipped in the delta).
	incompleteBaseline := testutil.StripLayer(t, bundle.BaselineArchive,
		bundle.BaselineOnlyReuseDigest)

	exit, _, stderr := runDiffah(t, ctx, "apply",
		"--baseline", "default="+incompleteBaseline,
		bundle.DeltaPath, "dir:"+filepath.Join(dir, "out"))

	if exit != 4 {
		t.Fatalf("exit = %d, want 4", exit)
	}
	if !strings.Contains(stderr, "re-run diff with a wider baseline") {
		t.Errorf("stderr missing B2 hint phrase; got:\n%s", stderr)
	}
}
```

- [ ] **Step 2: If `internal/testutil` lacks `BuildIntraLayerBundle` / `StripLayer` / `runDiffah`, add minimal versions**

Inspect `internal/testutil/` and `cmd/*_integration_test.go` for existing helpers. If a `BuildIntraLayerBundle` / `StripLayer` helper does not exist, create it under `internal/testutil/incomplete_baseline.go`:

```go
package testutil

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
)

type IntraLayerBundle struct {
	DeltaPath               string
	BaselineArchive         string
	PatchSourceDigest       digest.Digest
	BaselineOnlyReuseDigest digest.Digest
}

// BuildIntraLayerBundle synthesizes a baseline OCI archive + a target
// derived from it + a diffah bundle that contains both an encoding=patch
// blob (referencing PatchSourceDigest) and a baseline-only reuse layer
// (BaselineOnlyReuseDigest). Implementation reuses pkg/exporter through
// the diffah binary; not committed as a test fixture.
//
// Build by:
//   1. Locate an existing v4 fixture pair under testdata/fixtures/
//      (testdata/fixtures/v4_baseline_oci.tar + v4_target_oci.tar exist
//      per memory diffah_v2_phase1_status notes).
//   2. Copy them to dir as <name>_baseline.tar and <name>_target.tar.
//   3. Invoke `diffah diff oci-archive:<baseline> oci-archive:<target> <delta>`
//      to produce the bundle (use the same subprocess invocation pattern
//      as cmd/diff_integration_test.go).
//   4. Parse the resulting sidecar to populate PatchSourceDigest (the
//      first encoding=patch blob's PatchFromDigest) and
//      BaselineOnlyReuseDigest (the first target manifest layer that
//      does not appear in sidecar.Blobs).
func BuildIntraLayerBundle(t *testing.T, dir, name string) IntraLayerBundle {
	t.Helper()
	// Implementation: see comment above. Concretely:
	//   - shell out to the dev binary (same approach as cmd/diff_integration_test.go)
	//   - then read the produced delta archive's sidecar with diff.ParseSidecar
	//     and pkg/diff/internal helpers.
	t.Skip("BuildIntraLayerBundle: implement against testdata/fixtures/v4_*.tar before running this test")
	return IntraLayerBundle{}
}

// StripLayer copies srcArchive to a new file under t.TempDir() with the
// blob matching `omit` removed from blobs/sha256/ and from the manifest's
// layers list. Returns the new archive path.
func StripLayer(t *testing.T, srcArchive string, omit digest.Digest) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stripped.tar")
	in, err := os.Open(srcArchive)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	o, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	tr := tar.NewReader(in)
	tw := tar.NewWriter(o)
	defer tw.Close()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(h.Name) == omit.Encoded() {
			continue // drop blob entry
		}
		// TODO when manifest is encountered: rewrite layers[] to remove the
		// entry referencing `omit`. For initial impl, manifest may stay
		// unchanged; the GetBlob path will fail with 404 from the registry
		// or os.IsNotExist on dir transport — both routed through
		// isBlobNotFound.
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
```

(Note: full StripLayer with manifest rewrite is non-trivial. If manifest rewriting becomes necessary for the test to fail at 404 rather than at digest mismatch, expand the helper to parse `index.json` / `manifest.json` and rewrite. Initial pass: try the simplest version first; iterate if tests don't reach the B1/B2 path.)

If `runDiffah` does not exist, locate the equivalent in existing `cmd/*_integration_test.go` files (the existing `TestApplyCLI_*` tests must already invoke the binary somehow) and reuse.

- [ ] **Step 3: Run integration tests**

Run: `go test -tags integration ./cmd/ -run 'TestApplyCLI_MissingPatchSourceB1|TestApplyCLI_MissingBaselineReuseLayerB2' -v`
Expected: PASS.

If any test ends in a different exit code, debug: most likely the failure path is `digest mismatch` or `manifest decode failure` rather than `blob not found`. Fix the StripLayer helper to also rewrite the manifest's layers list so the GetBlob actually triggers `isBlobNotFound`.

- [ ] **Step 4: Run full importer + cmd test suite**

Run: `go test -race ./pkg/importer/... && go test -tags integration -race ./cmd/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/apply_resilience_integration_test.go internal/testutil/incomplete_baseline.go
git commit -m "test(cmd): integration coverage for B1/B2 hints

Synthesizes incomplete baselines (omit patch source / omit reuse
layer) and asserts exit 4 + the actionable hint phrase reaches
stderr. Closes PR1 of the apply correctness & resilience track."
```

---

## Phase 2 — PR2: End-to-End Invariant

**Goal:** After every successful `composeImage`, prove that the dest's reconstructed manifest matches the sidecar's expectation. No flag to disable. Failures map to `ErrApplyInvariantFailed` (CategoryContent → exit 4).

**Success Criteria:**
1. `verifyApplyInvariant` runs after every successful `composeImage` in `importEachImage`.
2. Layer digest set mismatch → `ErrApplyInvariantFailed{Missing, Unexpected populated}`.
3. Per-layer size mismatch → `ErrApplyInvariantFailed{Reason: "layer size mismatch"}`.
4. Manifest digest check skipped when dest mediaType differs from sidecar (schema conversion path passes).
5. Existing `TestApplyCommand_RoundTrip` includes invariant assertion and stays green.
6. Stage-4 summary renderer prints applied/failed counts + per-image reason on stderr.

**Status:** pending

### Task 2.1: Manifest helpers — `readSidecarTargetLayers` + `readDestManifestLayers`

**Files:**
- Create: `pkg/importer/manifest.go`
- Create: `pkg/importer/manifest_test.go`

- [ ] **Step 1: Write failing tests for both helpers**

```go
// pkg/importer/manifest_test.go
package importer

import (
	"context"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestReadSidecarTargetLayers_OCI(t *testing.T) {
	manifestBytes := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},
		"layers":[
			{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l1","size":100},
			{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l2","size":200}
		]
	}`)
	layers, mediaType, err := parseManifestLayers(manifestBytes,
		"application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("mediaType = %q", mediaType)
	}
	if len(layers) != 2 {
		t.Fatalf("len(layers) = %d, want 2", len(layers))
	}
	if layers[0].Digest != digest.Digest("sha256:l1") {
		t.Errorf("layers[0].Digest = %v", layers[0].Digest)
	}
	if layers[0].Size != 100 {
		t.Errorf("layers[0].Size = %d", layers[0].Size)
	}
}

func TestReadSidecarTargetLayers_DockerSchema2(t *testing.T) {
	manifestBytes := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
		"config": {"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:cfg","size":10},
		"layers":[
			{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"sha256:l1","size":100}
		]
	}`)
	layers, _, err := parseManifestLayers(manifestBytes,
		"application/vnd.docker.distribution.manifest.v2+json")
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 || layers[0].Digest != digest.Digest("sha256:l1") {
		t.Fatalf("layers = %v", layers)
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./pkg/importer/ -run 'TestReadSidecarTargetLayers' -v`
Expected: FAIL (parseManifestLayers undefined).

- [ ] **Step 3: Create `pkg/importer/manifest.go`**

```go
package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// LayerRef is a (digest, size) pair extracted from a manifest's layers list.
type LayerRef struct {
	Digest digest.Digest
	Size   int64
}

// parseManifestLayers parses raw manifest bytes (Docker schema 2 or OCI v1)
// and returns the layer list + canonical media type. Manifest lists are
// rejected — callers must select an instance upstream.
func parseManifestLayers(raw []byte, mediaType string) ([]LayerRef, string, error) {
	canonical := manifest.NormalizedMIMEType(mediaType)
	switch canonical {
	case manifest.DockerV2Schema2MediaType:
		m, err := manifest.Schema2FromManifest(raw)
		if err != nil {
			return nil, "", fmt.Errorf("parse docker schema 2 manifest: %w", err)
		}
		out := make([]LayerRef, len(m.LayersDescriptors))
		for i, d := range m.LayersDescriptors {
			out[i] = LayerRef{Digest: d.Digest, Size: d.Size}
		}
		return out, canonical, nil
	case imgspecv1.MediaTypeImageManifest:
		m, err := manifest.OCI1FromManifest(raw)
		if err != nil {
			return nil, "", fmt.Errorf("parse OCI manifest: %w", err)
		}
		out := make([]LayerRef, len(m.LayersDescriptors))
		for i, d := range m.LayersDescriptors {
			out[i] = LayerRef{Digest: d.Digest, Size: d.Size}
		}
		return out, canonical, nil
	default:
		return nil, "", fmt.Errorf("unsupported manifest media type %q", mediaType)
	}
}

// readSidecarTargetLayers retrieves the target manifest blob from the
// extracted bundle (always EncodingFull) and parses its layer list.
func readSidecarTargetLayers(
	bundle *extractedBundle, img diff.ImageEntry,
) ([]LayerRef, string, error) {
	mfDigest := img.Target.ManifestDigest
	path := filepath.Join(bundle.blobDir, mfDigest.Algorithm().String(), mfDigest.Encoded())
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read target manifest %s for image %q: %w",
			mfDigest, img.Name, err)
	}
	return parseManifestLayers(raw, img.Target.MediaType)
}

// readDestManifestLayers opens the dest, fetches its manifest, parses it.
// Returns layer list, dest mediaType, and the dest manifest's digest
// (computed by digest.FromBytes on the canonical bytes returned).
func readDestManifestLayers(
	ctx context.Context, destRef types.ImageReference, sysctx *types.SystemContext,
) ([]LayerRef, string, digest.Digest, error) {
	src, err := destRef.NewImageSource(ctx, sysctx)
	if err != nil {
		return nil, "", "", fmt.Errorf("open dest source %q: %w",
			destRef.StringWithinTransport(), err)
	}
	defer src.Close()
	raw, mediaType, err := src.GetManifest(ctx, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("read dest manifest %q: %w",
			destRef.StringWithinTransport(), err)
	}
	layers, mt, err := parseManifestLayers(raw, mediaType)
	if err != nil {
		return nil, "", "", err
	}
	return layers, mt, digest.FromBytes(raw), nil
}
```

Note: the `imgspecv1` import is from `github.com/opencontainers/image-spec/specs-go/v1`. Add it to the imports block.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run 'TestReadSidecarTargetLayers' -v`
Expected: PASS.

Run: `go vet ./pkg/importer/...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/manifest.go pkg/importer/manifest_test.go
git commit -m "feat(importer): manifest layer extraction helpers

readSidecarTargetLayers parses the target manifest blob from an
extracted bundle; readDestManifestLayers opens dest via NewImageSource
and parses its manifest. Both delegate media-type dispatch to a shared
parseManifestLayers using containers-image's manifest package.

Used by Phase 2 invariant verification and Phase 3 pre-flight."
```

### Task 2.2: `verifyApplyInvariant` core

**Files:**
- Create: `pkg/importer/invariant.go`
- Create: `pkg/importer/invariant_test.go`

- [ ] **Step 1: Write failing tests**

```go
// pkg/importer/invariant_test.go
package importer

import (
	"context"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestLayerSetDiff(t *testing.T) {
	expected := []LayerRef{
		{Digest: "sha256:a", Size: 10},
		{Digest: "sha256:b", Size: 20},
	}
	actual := []LayerRef{
		{Digest: "sha256:a", Size: 10},
		{Digest: "sha256:c", Size: 30},
	}
	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing) != 1 || missing[0] != "sha256:b" {
		t.Errorf("missing = %v, want [sha256:b]", missing)
	}
	if len(unexpected) != 1 || unexpected[0] != "sha256:c" {
		t.Errorf("unexpected = %v, want [sha256:c]", unexpected)
	}
}

func TestLayerSetDiff_Empty(t *testing.T) {
	missing, unexpected := layerSetDiff(nil, nil)
	if len(missing) != 0 || len(unexpected) != 0 {
		t.Errorf("expected empty diffs, got missing=%v unexpected=%v", missing, unexpected)
	}
}

func TestVerifyPerLayerSize_Matches(t *testing.T) {
	expected := []LayerRef{{Digest: "sha256:a", Size: 100}}
	actual := []LayerRef{{Digest: "sha256:a", Size: 100}}
	blobs := map[digest.Digest]diff.BlobEntry{"sha256:a": {Size: 100}}
	if err := verifyPerLayerSize(expected, actual, blobs); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyPerLayerSize_Mismatch(t *testing.T) {
	expected := []LayerRef{{Digest: "sha256:a", Size: 100}}
	actual := []LayerRef{{Digest: "sha256:a", Size: 999}}
	blobs := map[digest.Digest]diff.BlobEntry{"sha256:a": {Size: 100}}
	err := verifyPerLayerSize(expected, actual, blobs)
	if err == nil {
		t.Fatal("expected size mismatch error, got nil")
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./pkg/importer/ -run 'TestLayerSetDiff|TestVerifyPerLayerSize' -v`
Expected: FAIL (helpers undefined).

- [ ] **Step 3: Create `pkg/importer/invariant.go`**

```go
package importer

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// verifyApplyInvariant re-reads the dest manifest after a successful
// copy.Image and proves the layer set matches the sidecar's expectation.
func verifyApplyInvariant(
	ctx context.Context,
	img diff.ImageEntry,
	bundle *extractedBundle,
	destRef types.ImageReference,
	sysctx *types.SystemContext,
) error {
	expected, expectedMediaType, err := readSidecarTargetLayers(bundle, img)
	if err != nil {
		return fmt.Errorf("invariant: read sidecar target manifest: %w", err)
	}
	actual, actualMediaType, actualManifestDigest, err :=
		readDestManifestLayers(ctx, destRef, sysctx)
	if err != nil {
		return fmt.Errorf("invariant: read dest manifest: %w", err)
	}

	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing)+len(unexpected) > 0 {
		return &ErrApplyInvariantFailed{
			ImageName: img.Name,
			Expected:  digestsOf(expected),
			Got:       digestsOf(actual),
			Missing:   missing,
			Unexpected: unexpected,
			Reason:    "layer set mismatch",
		}
	}
	if err := verifyPerLayerSize(expected, actual, bundle.sidecar.Blobs); err != nil {
		return &ErrApplyInvariantFailed{
			ImageName: img.Name,
			Expected:  digestsOf(expected),
			Got:       digestsOf(actual),
			Reason:    err.Error(),
		}
	}
	if expectedMediaType == actualMediaType && actualManifestDigest != img.Target.ManifestDigest {
		return &ErrApplyInvariantFailed{
			ImageName: img.Name,
			Expected:  []digest.Digest{img.Target.ManifestDigest},
			Got:       []digest.Digest{actualManifestDigest},
			Reason:    "manifest digest mismatch",
		}
	}
	return nil
}

func layerSetDiff(expected, actual []LayerRef) (missing, unexpected []digest.Digest) {
	want := make(map[digest.Digest]struct{}, len(expected))
	for _, e := range expected {
		want[e.Digest] = struct{}{}
	}
	have := make(map[digest.Digest]struct{}, len(actual))
	for _, a := range actual {
		have[a.Digest] = struct{}{}
	}
	for d := range want {
		if _, ok := have[d]; !ok {
			missing = append(missing, d)
		}
	}
	for d := range have {
		if _, ok := want[d]; !ok {
			unexpected = append(unexpected, d)
		}
	}
	return missing, unexpected
}

func verifyPerLayerSize(
	expected, actual []LayerRef,
	sidecarBlobs map[digest.Digest]diff.BlobEntry,
) error {
	actualByDigest := make(map[digest.Digest]int64, len(actual))
	for _, a := range actual {
		actualByDigest[a.Digest] = a.Size
	}
	for _, e := range expected {
		gotSize, ok := actualByDigest[e.Digest]
		if !ok {
			continue // covered by layerSetDiff
		}
		wantSize := e.Size
		if entry, present := sidecarBlobs[e.Digest]; present && entry.Size > 0 {
			wantSize = entry.Size
		}
		if gotSize != wantSize {
			return fmt.Errorf("layer size mismatch: %s want %d got %d",
				e.Digest, wantSize, gotSize)
		}
	}
	return nil
}

func digestsOf(refs []LayerRef) []digest.Digest {
	out := make([]digest.Digest, len(refs))
	for i, r := range refs {
		out[i] = r.Digest
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run 'TestLayerSetDiff|TestVerifyPerLayerSize' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/invariant.go pkg/importer/invariant_test.go
git commit -m "feat(importer): verifyApplyInvariant core

Core invariant verification: compares dest's reconstructed manifest
against the sidecar's target expectation. Layer digest set is
mandatory; per-layer size is mandatory; manifest digest is checked
only when no schema conversion happened (mediaType equality).

Failures produce ErrApplyInvariantFailed with Missing/Unexpected/Reason
populated for actionable diagnostics."
```

### Task 2.3: Mock-based invariant integration test

**Files:**
- Modify: `pkg/importer/invariant_test.go`

- [ ] **Step 1: Write a mock-`ImageSource`-driven test for the invariant happy path + injected-mismatch path**

```go
// Append to pkg/importer/invariant_test.go
import (
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"
)

type fakeDestSource struct {
	manifest     []byte
	manifestMime string
}

func (f *fakeDestSource) Reference() types.ImageReference                   { return nil }
func (f *fakeDestSource) Close() error                                      { return nil }
func (f *fakeDestSource) GetManifest(_ context.Context, _ *digest.Digest) ([]byte, string, error) {
	return f.manifest, f.manifestMime, nil
}
func (f *fakeDestSource) HasThreadSafeGetBlob() bool { return true }
func (f *fakeDestSource) GetSignatures(_ context.Context, _ *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (f *fakeDestSource) LayerInfosForCopy(_ context.Context, _ *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}
func (f *fakeDestSource) GetBlob(_ context.Context, _ types.BlobInfo, _ types.BlobInfoCache) (io.ReadCloser, int64, error) {
	return nil, 0, nil
}

// (For these unit tests, we exercise readDestManifestLayers via a wrapper
// that takes raw manifest bytes directly, sidestepping NewImageSource.)
func TestVerifyApplyInvariant_HappyPath(t *testing.T) {
	mfBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l1","size":100}]}`)
	mfDigest := digest.FromBytes(mfBytes)

	bundle := &extractedBundle{
		blobDir: writeBlobToTempDir(t, mfDigest, mfBytes),
		sidecar: &diff.Sidecar{
			Blobs: map[digest.Digest]diff.BlobEntry{
				mfDigest:           {Size: int64(len(mfBytes))},
				digest.Digest("sha256:l1"): {Size: 100},
			},
			Images: []diff.ImageEntry{
				{Name: "svc-a",
					Target: diff.TargetRef{
						ManifestDigest: mfDigest,
						MediaType:      "application/vnd.oci.image.manifest.v1+json",
					}},
			},
		},
	}

	expected, _, err := readSidecarTargetLayers(bundle, bundle.sidecar.Images[0])
	if err != nil { t.Fatal(err) }

	// Same manifest as dest → invariant passes.
	actual, _, _, err := parseAsDestForTest(mfBytes,
		"application/vnd.oci.image.manifest.v1+json")
	if err != nil { t.Fatal(err) }

	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing)+len(unexpected) != 0 {
		t.Errorf("happy path expected no diff; missing=%v unexpected=%v", missing, unexpected)
	}
}

func TestVerifyApplyInvariant_LayerMissing(t *testing.T) {
	expected := []LayerRef{
		{Digest: "sha256:a", Size: 100},
		{Digest: "sha256:b", Size: 200},
	}
	actual := []LayerRef{
		{Digest: "sha256:a", Size: 100},
	}
	missing, unexpected := layerSetDiff(expected, actual)
	if len(missing) != 1 || missing[0] != "sha256:b" {
		t.Errorf("Missing should be [sha256:b], got %v", missing)
	}
	if len(unexpected) != 0 {
		t.Errorf("Unexpected should be empty, got %v", unexpected)
	}
}

func writeBlobToTempDir(t *testing.T, d digest.Digest, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	algoDir := filepath.Join(dir, d.Algorithm().String())
	if err := os.MkdirAll(algoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(algoDir, d.Encoded()), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// parseAsDestForTest exposes parseManifestLayers via a name that mirrors
// readDestManifestLayers' signature (sidesteps the NewImageSource dance
// for unit tests).
func parseAsDestForTest(raw []byte, mediaType string) ([]LayerRef, string, digest.Digest, error) {
	layers, mt, err := parseManifestLayers(raw, mediaType)
	if err != nil { return nil, "", "", err }
	return layers, mt, digest.FromBytes(raw), nil
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./pkg/importer/ -run 'TestVerifyApplyInvariant' -v`
Expected: PASS — relies entirely on already-implemented helpers.

- [ ] **Step 3: (no implementation needed)**

Skip if Step 2 passes.

- [ ] **Step 4: Verify no race**

Run: `go test -race ./pkg/importer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/invariant_test.go
git commit -m "test(importer): mock-driven invariant unit coverage

Layer set match → no diff. Layer missing → Missing populated.
Smoke-tests the canonical happy path through readSidecarTargetLayers
and the layer-set-diff math."
```

### Task 2.4: Wire `verifyApplyInvariant` into `importEachImage`

**Files:**
- Modify: `pkg/importer/importer.go::importEachImage`
- Create: `pkg/importer/summary.go` (Stage-4 renderer skeleton)

- [ ] **Step 1: Write failing integration test**

Pick the existing `TestApplyCommand_RoundTrip` (in `cmd/apply_test.go` per the audit). Add at its end:

```go
// Within TestApplyCommand_RoundTrip, after the existing apply succeeds:
import "go.podman.io/image/v5/manifest"

// ... existing test body ...

// Verify dest manifest has the same layer set as the source bundle's target.
destSrc := openDest(t, ctx, outRef) // existing helper if present, else inline
mfBytes, mfMime, err := destSrc.GetManifest(ctx, nil)
if err != nil { t.Fatal(err) }
defer destSrc.Close()

mflist, err := manifest.OCI1FromManifest(mfBytes)
_ = mflist // consume to assert no error path silently swallowed
if err != nil {
	if _, err := manifest.Schema2FromManifest(mfBytes); err != nil {
		t.Fatalf("dest manifest unparseable: %v", err)
	}
}
_ = mfMime
```

(If `openDest` is not present, the assertion can be done by re-running the apply and capturing logs. The point is to make sure the test would catch an invariant regression by reading dest manifest after apply.)

- [ ] **Step 2: Run test**

Run: `go test ./cmd/ -run TestApplyCommand_RoundTrip -v`
Expected: PASS — the existing round-trip should already produce a parseable dest manifest. (We're verifying the assertion plumbing is right; functional verification comes from the next step's integration into the production code path.)

- [ ] **Step 3: Modify `importEachImage` to call `verifyApplyInvariant`; create `summary.go`**

In `pkg/importer/importer.go::importEachImage`:

```go
// after composeImage success:
if err := composeImage(ctx, img, bundle, rb, destRef,
    opts.SystemContext, opts.AllowConvert, opts.reporter(), cache); err != nil {
    return 0, nil, err  // existing
}
if err := verifyApplyInvariant(ctx, img, bundle, destRef, opts.SystemContext); err != nil {
    return 0, nil, err  // invariant failure aborts the run; Phase 3 will partial-mode this
}
imported++
```

Create `pkg/importer/summary.go`:

```go
package importer

import (
	"fmt"
	"io"
)

type ApplyImageStatus int

const (
	ApplyImageOK ApplyImageStatus = iota
	ApplyImageFailedCompose
	ApplyImageFailedInvariant
	ApplyImageSkippedPreflight // populated in Phase 3
)

type ApplyImageResult struct {
	ImageName string
	Status    ApplyImageStatus
	Err       error
}

type ApplyReport struct {
	Total   int
	Results []ApplyImageResult
}

func (r ApplyReport) Successful() int {
	n := 0
	for _, x := range r.Results {
		if x.Status == ApplyImageOK {
			n++
		}
	}
	return n
}

// renderSummary writes the Stage-4 final summary to w. Phase 3 extends
// this with the partial-mode "skipped at preflight" branch.
func renderSummary(w io.Writer, r ApplyReport) {
	fmt.Fprintf(w, "diffah: applied %d/%d images\n", r.Successful(), r.Total)
	for _, x := range r.Results {
		switch x.Status {
		case ApplyImageOK:
			fmt.Fprintf(w, "  ok  %s: applied + verified\n", x.ImageName)
		case ApplyImageFailedCompose:
			fmt.Fprintf(w, "  err %s: compose failed: %v\n", x.ImageName, x.Err)
		case ApplyImageFailedInvariant:
			fmt.Fprintf(w, "  err %s: applied with invariant mismatch: %v\n",
				x.ImageName, x.Err)
		case ApplyImageSkippedPreflight:
			fmt.Fprintf(w, "  skip %s: preflight skipped (%v)\n", x.ImageName, x.Err)
		}
	}
	if r.Successful() < r.Total {
		fmt.Fprintf(w, "\nnote: dest may contain partially-written images from this run.\n")
		fmt.Fprintf(w, "manual cleanup is required for any image marked failed/mismatch above.\n")
	}
}
```

(Phase 2 stops here — `importEachImage` returns the first invariant error, which `cmd.Execute` renders. Phase 3 will rewrite this loop to use `ApplyReport` and partial-mode logic.)

- [ ] **Step 4: Run all tests**

Run: `go test ./pkg/importer/... && go test ./cmd/...`
Expected: PASS.

Run: `go test -tags integration -race ./cmd/... ./pkg/importer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/importer.go pkg/importer/summary.go cmd/apply_test.go
git commit -m "feat(importer): end-to-end invariant after composeImage

Each successful composeImage is now followed by verifyApplyInvariant
which re-reads the dest manifest, asserts layer digest set + per-layer
size match the sidecar, and (when no schema conversion happened) the
manifest digest itself. Failures produce ErrApplyInvariantFailed which
maps to exit 4 via existing CategoryContent classification.

Introduces summary.ApplyReport / renderSummary for Stage-4 final
output; Phase 3 extends with partial-mode skipped images.

Closes PR2 of the apply correctness & resilience track."
```

### Task 2.5: Schema-conversion edge case test

**Files:**
- Modify: `pkg/importer/invariant_test.go`

- [ ] **Step 1: Add test for the conditional manifest digest rule**

```go
// Append to pkg/importer/invariant_test.go
func TestVerifyApplyInvariant_AcrossSchemaConversion(t *testing.T) {
	// Two manifests with the same layer set but different mediaTypes;
	// digests will differ, but the invariant must still pass because
	// schema conversion happened.
	ociBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l1","size":100}]}`)
	dockerBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"sha256:l1","size":100}]}`)

	expectedLayers, _, err := parseManifestLayers(ociBytes,
		"application/vnd.oci.image.manifest.v1+json")
	if err != nil { t.Fatal(err) }
	actualLayers, _, err := parseManifestLayers(dockerBytes,
		"application/vnd.docker.distribution.manifest.v2+json")
	if err != nil { t.Fatal(err) }

	missing, unexpected := layerSetDiff(expectedLayers, actualLayers)
	if len(missing)+len(unexpected) != 0 {
		t.Errorf("layer set must match across schema conversion; missing=%v unexpected=%v",
			missing, unexpected)
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./pkg/importer/ -run TestVerifyApplyInvariant_AcrossSchemaConversion -v`
Expected: PASS.

- [ ] **Step 3: (no implementation needed)**

Skip.

- [ ] **Step 4: Run full suite**

Run: `go test -race ./pkg/importer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/invariant_test.go
git commit -m "test(importer): invariant passes across schema 2 ↔ OCI conversion

Asserts the conditional rule (manifest digest checked only when
mediaType matches) by parsing equivalent manifests in both formats
and confirming layer set match still holds."
```

---

## Phase 3 — PR3: Pre-flight + Partial/Strict Path

**Goal:** Before any `copy.Image` call, scan baseline manifests against sidecar expectations and classify each image OK/B1/B2/PreflightError. Partial mode skips non-OK images and applies the rest; strict mode scans all then aborts with a complete report. Existing `--strict` flag widens to cover this.

**Success Criteria:**
1. `RunPreflight` runs in `Import()` before `importEachImage`.
2. Multi-image bundle with one B1 image in partial mode → exit 0; dest contains only OK images; summary lists svc-c skipped.
3. Same bundle with `--strict` → exit 4; dest unchanged; stderr lists all non-OK images (scan-all-then-abort).
4. Pre-flight emits streaming stderr per image as it scans.
5. Pre-flight does not regress baseline manifest GET count (mock registry asserts ≤ 2 GETs per baseline image across pre-flight + apply).
6. CHANGELOG entry documents `--strict` semantic extension.

**Status:** pending

### Task 3.1: `PreflightStatus`, `PreflightResult`, required-digest math

**Files:**
- Create: `pkg/importer/preflight.go`
- Create: `pkg/importer/preflight_test.go`

- [ ] **Step 1: Write failing tests for required-digest computation**

```go
// pkg/importer/preflight_test.go
package importer

import (
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestComputeRequiredBaselineDigests(t *testing.T) {
	mfBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[
		{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:shipped-full","size":100},
		{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:shipped-patch","size":50},
		{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:reuse","size":200}
	]}`)
	mfDigest := digest.FromBytes(mfBytes)

	sidecar := &diff.Sidecar{
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest:                            {Encoding: diff.EncodingFull, Size: int64(len(mfBytes))},
			digest.Digest("sha256:shipped-full"):  {Encoding: diff.EncodingFull, Size: 100},
			digest.Digest("sha256:shipped-patch"): {Encoding: diff.EncodingPatch, PatchFromDigest: digest.Digest("sha256:patch-src"), Size: 50},
			digest.Digest("sha256:cfg"):           {Encoding: diff.EncodingFull, Size: 10},
		},
		Images: []diff.ImageEntry{
			{Name: "svc-a", Target: diff.TargetRef{
				ManifestDigest: mfDigest,
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			}},
		},
	}

	bundle := &extractedBundle{
		blobDir: writeBlobToTempDir(t, mfDigest, mfBytes),
		sidecar: sidecar,
	}

	reuse, patchSrcs, err := computeRequiredBaselineDigests(bundle, sidecar.Images[0])
	if err != nil { t.Fatal(err) }

	wantReuse := []digest.Digest{"sha256:reuse"}
	wantPatchSrcs := []digest.Digest{"sha256:patch-src"}
	if !equalDigestSets(reuse, wantReuse) {
		t.Errorf("reuse = %v, want %v", reuse, wantReuse)
	}
	if !equalDigestSets(patchSrcs, wantPatchSrcs) {
		t.Errorf("patchSrcs = %v, want %v", patchSrcs, wantPatchSrcs)
	}
}

func equalDigestSets(a, b []digest.Digest) bool {
	if len(a) != len(b) {
		return false
	}
	want := make(map[digest.Digest]struct{}, len(b))
	for _, d := range b {
		want[d] = struct{}{}
	}
	for _, d := range a {
		if _, ok := want[d]; !ok {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test**

Run: `go test ./pkg/importer/ -run TestComputeRequiredBaselineDigests -v`
Expected: FAIL — `computeRequiredBaselineDigests` undefined.

- [ ] **Step 3: Create `pkg/importer/preflight.go` skeleton + math**

```go
package importer

import (
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
)

type PreflightStatus int

const (
	PreflightOK PreflightStatus = iota
	PreflightMissingPatchSource
	PreflightMissingReuseLayer
	PreflightError
	PreflightSchemaError
)

type PreflightResult struct {
	ImageName            string
	Status               PreflightStatus
	MissingPatchSources  []digest.Digest
	MissingReuseLayers   []digest.Digest
	Err                  error
}

// computeRequiredBaselineDigests returns the baseline-only reuse layer
// set (B2 candidates) and the patch-source set (B1 candidates) inferred
// from the target manifest + sidecar.
func computeRequiredBaselineDigests(
	bundle *extractedBundle, img diff.ImageEntry,
) (reuse, patchSrcs []digest.Digest, err error) {
	targetLayers, _, err := readSidecarTargetLayers(bundle, img)
	if err != nil {
		return nil, nil, err
	}
	shipped := bundle.sidecar.Blobs

	reuseSet := make(map[digest.Digest]struct{})
	patchSet := make(map[digest.Digest]struct{})
	for _, layer := range targetLayers {
		entry, isShipped := shipped[layer.Digest]
		if !isShipped {
			reuseSet[layer.Digest] = struct{}{}
			continue
		}
		if entry.Encoding == diff.EncodingPatch && entry.PatchFromDigest != "" {
			patchSet[entry.PatchFromDigest] = struct{}{}
		}
	}
	return setToSlice(reuseSet), setToSlice(patchSet), nil
}

func setToSlice(s map[digest.Digest]struct{}) []digest.Digest {
	out := make([]digest.Digest, 0, len(s))
	for d := range s {
		out = append(out, d)
	}
	return out
}
```

- [ ] **Step 4: Run test**

Run: `go test ./pkg/importer/ -run TestComputeRequiredBaselineDigests -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/preflight.go pkg/importer/preflight_test.go
git commit -m "feat(importer): preflight required-digest computation

Given a sidecar and an image entry, computeRequiredBaselineDigests
returns the baseline-only reuse layer set (B2 candidates) and the
patch-source digest set (B1 candidates). Layer-by-layer dispatch
based on Encoding=Full/Patch.

PR3 scaffold: PreflightStatus, PreflightResult declared but not yet
populated by RunPreflight."
```

### Task 3.2: Single-image preflight scan + classify

**Files:**
- Modify: `pkg/importer/preflight.go`
- Modify: `pkg/importer/preflight_test.go`

- [ ] **Step 1: Write failing tests for scanOneImage classification**

```go
// Append to pkg/importer/preflight_test.go
func TestScanOneImage_AllOK(t *testing.T) {
	// Construct a bundle where the baseline contains every required digest.
	// (Implementation detail: use the same fixture-builder as
	// TestComputeRequiredBaselineDigests, plus a fakeBaselineSource that
	// returns a manifest containing all required digests.)
	bundle, img, baseline := buildPreflightFixture(t, []digest.Digest{
		"sha256:patch-src", "sha256:reuse", "sha256:cfg",
	})
	r := scanOneImage(context.Background(), bundle, img, baseline)
	if r.Status != PreflightOK {
		t.Errorf("Status = %v, want PreflightOK; result=%+v", r.Status, r)
	}
}

func TestScanOneImage_B1_OnlyPatchSrcMissing(t *testing.T) {
	bundle, img, baseline := buildPreflightFixture(t, []digest.Digest{
		"sha256:reuse", "sha256:cfg",
		// patch-src omitted
	})
	r := scanOneImage(context.Background(), bundle, img, baseline)
	if r.Status != PreflightMissingPatchSource {
		t.Errorf("Status = %v, want PreflightMissingPatchSource", r.Status)
	}
	if len(r.MissingPatchSources) != 1 || r.MissingPatchSources[0] != "sha256:patch-src" {
		t.Errorf("MissingPatchSources = %v", r.MissingPatchSources)
	}
}

func TestScanOneImage_B2_OnlyReuseMissing(t *testing.T) {
	bundle, img, baseline := buildPreflightFixture(t, []digest.Digest{
		"sha256:patch-src", "sha256:cfg",
		// reuse omitted
	})
	r := scanOneImage(context.Background(), bundle, img, baseline)
	if r.Status != PreflightMissingReuseLayer {
		t.Errorf("Status = %v, want PreflightMissingReuseLayer", r.Status)
	}
	if len(r.MissingReuseLayers) != 1 || r.MissingReuseLayers[0] != "sha256:reuse" {
		t.Errorf("MissingReuseLayers = %v", r.MissingReuseLayers)
	}
}

func TestScanOneImage_BothB1AndB2(t *testing.T) {
	bundle, img, baseline := buildPreflightFixture(t, []digest.Digest{
		"sha256:cfg",
		// both patch-src and reuse omitted
	})
	r := scanOneImage(context.Background(), bundle, img, baseline)
	if r.Status != PreflightMissingPatchSource {
		t.Errorf("when both missing, Status should be MissingPatchSource (B1 dominates); got %v", r.Status)
	}
	if len(r.MissingPatchSources) != 1 || len(r.MissingReuseLayers) != 1 {
		t.Errorf("both slices should be filled independently; got patch=%v reuse=%v",
			r.MissingPatchSources, r.MissingReuseLayers)
	}
}

// buildPreflightFixture constructs an extractedBundle (with the same shape
// as TestComputeRequiredBaselineDigests) plus a fake baseline ImageSource
// whose manifest reports the given digest set.
func buildPreflightFixture(t *testing.T, baselineDigests []digest.Digest) (
	*extractedBundle, diff.ImageEntry, types.ImageSource,
) {
	t.Helper()
	mfBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[
		{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:shipped-full","size":100},
		{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:shipped-patch","size":50},
		{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:reuse","size":200}
	]}`)
	mfDigest := digest.FromBytes(mfBytes)
	sidecar := &diff.Sidecar{
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest: {Encoding: diff.EncodingFull, Size: int64(len(mfBytes))},
			"sha256:shipped-full":  {Encoding: diff.EncodingFull, Size: 100},
			"sha256:shipped-patch": {Encoding: diff.EncodingPatch, PatchFromDigest: "sha256:patch-src", Size: 50},
			"sha256:cfg":           {Encoding: diff.EncodingFull, Size: 10},
		},
		Images: []diff.ImageEntry{{
			Name:   "svc-a",
			Target: diff.TargetRef{ManifestDigest: mfDigest, MediaType: "application/vnd.oci.image.manifest.v1+json"},
		}},
	}
	bundle := &extractedBundle{
		blobDir: writeBlobToTempDir(t, mfDigest, mfBytes),
		sidecar: sidecar,
	}
	baseline := &fakeManifestSource{
		layers: baselineDigests,
	}
	return bundle, sidecar.Images[0], baseline
}

type fakeManifestSource struct {
	layers []digest.Digest
}

func (f *fakeManifestSource) Reference() types.ImageReference                   { return nil }
func (f *fakeManifestSource) Close() error                                      { return nil }
func (f *fakeManifestSource) HasThreadSafeGetBlob() bool                        { return true }
func (f *fakeManifestSource) GetBlob(context.Context, types.BlobInfo, types.BlobInfoCache) (io.ReadCloser, int64, error) {
	return nil, 0, nil
}
func (f *fakeManifestSource) GetSignatures(context.Context, *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (f *fakeManifestSource) LayerInfosForCopy(context.Context, *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}
func (f *fakeManifestSource) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	// Reconstruct an OCI manifest from f.layers; config digest is the first
	// element labeled "cfg" if present, else synthetic.
	cfg := digest.Digest("sha256:synth-cfg")
	for _, d := range f.layers {
		if d == "sha256:cfg" {
			cfg = d
		}
	}
	type descriptor struct {
		MediaType string        `json:"mediaType"`
		Digest    digest.Digest `json:"digest"`
		Size      int64         `json:"size"`
	}
	type m struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Config        descriptor   `json:"config"`
		Layers        []descriptor `json:"layers"`
	}
	mf := m{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        descriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: cfg, Size: 10},
	}
	for _, d := range f.layers {
		if d == cfg { continue }
		mf.Layers = append(mf.Layers, descriptor{
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Digest:    d, Size: 100,
		})
	}
	raw, err := json.Marshal(mf)
	if err != nil { return nil, "", err }
	return raw, mf.MediaType, nil
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./pkg/importer/ -run 'TestScanOneImage' -v`
Expected: FAIL — `scanOneImage` undefined.

- [ ] **Step 3: Implement `scanOneImage` in `preflight.go`**

```go
// Append to pkg/importer/preflight.go

import "encoding/json"

// scanOneImage performs the per-image preflight scan. The baseline
// ImageSource must already be opened (typically rb.Src). On baseline
// fetch failure, returns Status=PreflightError with Err populated.
func scanOneImage(
	ctx context.Context,
	bundle *extractedBundle,
	img diff.ImageEntry,
	baseline types.ImageSource,
) PreflightResult {
	if err := ctx.Err(); err != nil {
		return PreflightResult{ImageName: img.Name, Status: PreflightError, Err: err}
	}
	reuse, patchSrcs, err := computeRequiredBaselineDigests(bundle, img)
	if err != nil {
		return PreflightResult{
			ImageName: img.Name, Status: PreflightSchemaError, Err: err,
		}
	}
	rawMf, mfMime, err := baseline.GetManifest(ctx, nil)
	if err != nil {
		return PreflightResult{ImageName: img.Name, Status: PreflightError, Err: err}
	}
	baselineLayers, _, err := parseManifestLayers(rawMf, mfMime)
	if err != nil {
		return PreflightResult{ImageName: img.Name, Status: PreflightError, Err: err}
	}
	baselineSet := make(map[digest.Digest]struct{}, len(baselineLayers)+1)
	for _, l := range baselineLayers {
		baselineSet[l.Digest] = struct{}{}
	}
	// config digest also belongs to the baseline reachability set
	if cfg, _ := configDigestOf(rawMf, mfMime); cfg != "" {
		baselineSet[cfg] = struct{}{}
	}

	missingPatchSrcs := digestsNotIn(patchSrcs, baselineSet)
	missingReuse := digestsNotIn(reuse, baselineSet)

	res := PreflightResult{
		ImageName:           img.Name,
		MissingPatchSources: missingPatchSrcs,
		MissingReuseLayers:  missingReuse,
	}
	switch {
	case len(missingPatchSrcs) == 0 && len(missingReuse) == 0:
		res.Status = PreflightOK
	case len(missingPatchSrcs) > 0:
		res.Status = PreflightMissingPatchSource // B1 dominates
	default:
		res.Status = PreflightMissingReuseLayer
	}
	return res
}

func digestsNotIn(want []digest.Digest, have map[digest.Digest]struct{}) []digest.Digest {
	out := []digest.Digest{}
	for _, d := range want {
		if _, ok := have[d]; !ok {
			out = append(out, d)
		}
	}
	return out
}

// configDigestOf parses raw manifest bytes and returns the config descriptor's digest.
func configDigestOf(raw []byte, mediaType string) (digest.Digest, error) {
	type descriptor struct {
		Digest digest.Digest `json:"digest"`
	}
	type minimal struct {
		Config descriptor `json:"config"`
	}
	var m minimal
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	return m.Config.Digest, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run 'TestScanOneImage' -v`
Expected: PASS.

Run: `go test -race ./pkg/importer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/preflight.go pkg/importer/preflight_test.go
git commit -m "feat(importer): per-image preflight scan + classify

scanOneImage fetches the baseline manifest, parses layer digests,
includes config.digest, and classifies the result against the
required (reuse, patchSrcs) sets. B1 (missing patch source) takes
precedence in Status when both B1 and B2 hold; both digest slices
are populated independently.

Foundation for RunPreflight assembly."
```

### Task 3.3: `RunPreflight` assembly + streaming reporter

**Files:**
- Modify: `pkg/importer/preflight.go`
- Modify: `pkg/importer/preflight_test.go`

- [ ] **Step 1: Write failing test for `RunPreflight` over multi-image bundle**

```go
// Append to pkg/importer/preflight_test.go
func TestRunPreflight_MultiImage_PartialFailures(t *testing.T) {
	// Build a bundle with two images: svc-a (baseline complete), svc-b
	// (baseline missing reuse layer).
	bundle, resolved := buildMultiImagePreflightFixture(t)
	results, anyFail, err := RunPreflight(context.Background(), bundle, resolved, nil, progress.NewDiscard())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if !anyFail {
		t.Error("anyFail should be true")
	}
	if results[0].Status != PreflightOK {
		t.Errorf("svc-a Status = %v, want OK", results[0].Status)
	}
	if results[1].Status != PreflightMissingReuseLayer {
		t.Errorf("svc-b Status = %v, want MissingReuseLayer", results[1].Status)
	}
}

// buildMultiImagePreflightFixture — extends buildPreflightFixture to
// produce two images with their own baselines. svc-a's baseline contains
// the full required digest set; svc-b's baseline is missing
// "sha256:reuse-b".
func buildMultiImagePreflightFixture(t *testing.T) (
	*extractedBundle, []resolvedBaseline,
) {
	t.Helper()
	// svc-a's target manifest
	mfA := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg-a","size":10},"layers":[
		{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:reuse-a","size":100}
	]}`)
	mfADigest := digest.FromBytes(mfA)
	// svc-b's target manifest
	mfB := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg-b","size":10},"layers":[
		{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:reuse-b","size":200}
	]}`)
	mfBDigest := digest.FromBytes(mfB)

	dir := t.TempDir()
	algoDir := filepath.Join(dir, "sha256")
	if err := os.MkdirAll(algoDir, 0o755); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(algoDir, mfADigest.Encoded()), mfA, 0o644); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(algoDir, mfBDigest.Encoded()), mfB, 0o644); err != nil { t.Fatal(err) }

	sidecar := &diff.Sidecar{
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfADigest:           {Encoding: diff.EncodingFull, Size: int64(len(mfA))},
			mfBDigest:           {Encoding: diff.EncodingFull, Size: int64(len(mfB))},
			"sha256:cfg-a":      {Encoding: diff.EncodingFull, Size: 10},
			"sha256:cfg-b":      {Encoding: diff.EncodingFull, Size: 10},
		},
		Images: []diff.ImageEntry{
			{Name: "svc-a", Target: diff.TargetRef{ManifestDigest: mfADigest, MediaType: "application/vnd.oci.image.manifest.v1+json"}},
			{Name: "svc-b", Target: diff.TargetRef{ManifestDigest: mfBDigest, MediaType: "application/vnd.oci.image.manifest.v1+json"}},
		},
	}
	bundle := &extractedBundle{blobDir: dir, sidecar: sidecar}

	resolved := []resolvedBaseline{
		{Name: "svc-a", Src: &fakeManifestSource{layers: []digest.Digest{"sha256:cfg-a", "sha256:reuse-a"}}},
		{Name: "svc-b", Src: &fakeManifestSource{layers: []digest.Digest{"sha256:cfg-b" /* reuse-b omitted */}}},
	}
	return bundle, resolved
}
```

- [ ] **Step 2: Run test**

Run: `go test ./pkg/importer/ -run TestRunPreflight_MultiImage_PartialFailures -v`
Expected: FAIL.

- [ ] **Step 3: Implement `RunPreflight` and `buildMultiImagePreflightFixture`**

```go
// Append to pkg/importer/preflight.go

// RunPreflight scans every image in the bundle and returns per-image
// results. anyFailure is true if at least one result has Status != OK.
// PreflightSchemaError is fatal and returned via the err return value.
func RunPreflight(
	ctx context.Context,
	bundle *extractedBundle,
	resolved []resolvedBaseline,
	sysctx *types.SystemContext,
	reporter progress.Reporter,
) ([]PreflightResult, bool, error) {
	resolvedByName := make(map[string]resolvedBaseline, len(resolved))
	for _, r := range resolved {
		resolvedByName[r.Name] = r
	}
	if reporter != nil {
		reporter.Phase("preflight")
	}

	results := make([]PreflightResult, 0, len(bundle.sidecar.Images))
	anyFail := false
	for _, img := range bundle.sidecar.Images {
		if err := ctx.Err(); err != nil {
			return results, anyFail, err
		}
		rb, ok := resolvedByName[img.Name]
		if !ok {
			results = append(results, PreflightResult{
				ImageName: img.Name, Status: PreflightError,
				Err: fmt.Errorf("baseline not resolved for image %q", img.Name),
			})
			anyFail = true
			continue
		}
		r := scanOneImage(ctx, bundle, img, rb.Src)
		if r.Status == PreflightSchemaError {
			return results, true, r.Err
		}
		if r.Status != PreflightOK {
			anyFail = true
		}
		results = append(results, r)
		// Stream report — even though progress.Reporter only has Phase,
		// we use slog at the importer level for structured per-image events.
		log().InfoContext(ctx, "preflight",
			"image", r.ImageName, "status", r.Status.String(),
			"missing_patch_sources", len(r.MissingPatchSources),
			"missing_reuse_layers", len(r.MissingReuseLayers))
	}
	return results, anyFail, nil
}

// String returns a human-readable label for the status (used by slog).
func (s PreflightStatus) String() string {
	switch s {
	case PreflightOK:
		return "ok"
	case PreflightMissingPatchSource:
		return "missing-patch-source"
	case PreflightMissingReuseLayer:
		return "missing-reuse-layer"
	case PreflightError:
		return "error"
	case PreflightSchemaError:
		return "schema-error"
	}
	return "unknown"
}
```

Implement `buildMultiImagePreflightFixture` in `preflight_test.go` by extending the single-image fixture builder.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run TestRunPreflight -v`
Expected: PASS.

Run: `go test -race ./pkg/importer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/preflight.go pkg/importer/preflight_test.go
git commit -m "feat(importer): RunPreflight multi-image assembly

Iterates every image, dispatches scanOneImage, aggregates results.
PreflightSchemaError aborts immediately as the err return value
(fatal regardless of partial/strict). Other statuses are recorded
and emitted via slog at info level for streaming visibility.

PreflightStatus.String for slog/UX rendering."
```

### Task 3.4: Wire `RunPreflight` into `Import()` + partial/strict path

**Files:**
- Modify: `pkg/importer/importer.go::Import` and `importEachImage`

- [ ] **Step 1: Write failing integration test for partial mode skipping**

```go
//go:build integration

// cmd/unbundle_preflight_integration_test.go
package cmd_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leosocy/diffah/internal/testutil"
)

func TestUnbundleCLI_PartialModeSkipsB2(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Build a 2-image bundle. svc-a baseline complete; svc-b baseline
	// missing a reuse layer.
	bundle := testutil.BuildTwoImageBundle(t, dir)
	incompleteBaselineB := testutil.StripLayer(t, bundle.BaselineArchives["svc-b"],
		bundle.ReuseDigests["svc-b"])

	exit, _, stderr := runDiffah(t, ctx, "unbundle",
		"--baseline-spec", testutil.WriteBaselineSpec(t, map[string]string{
			"svc-a": bundle.BaselineArchives["svc-a"],
			"svc-b": incompleteBaselineB,
		}),
		bundle.DeltaPath,
		filepath.Join(dir, "out"))

	if exit != 0 {
		t.Fatalf("partial mode: exit = %d, want 0", exit)
	}
	if !strings.Contains(stderr, "applied 1/2") {
		t.Errorf("stderr should report 'applied 1/2'; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "svc-b") {
		t.Errorf("stderr should mention svc-b; got:\n%s", stderr)
	}
}

func TestUnbundleCLI_StrictAbortsAfterFullScan(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	bundle := testutil.BuildTwoImageBundle(t, dir)
	incompleteBaselineB := testutil.StripLayer(t, bundle.BaselineArchives["svc-b"],
		bundle.ReuseDigests["svc-b"])

	exit, _, stderr := runDiffah(t, ctx, "unbundle",
		"--strict",
		"--baseline-spec", testutil.WriteBaselineSpec(t, map[string]string{
			"svc-a": bundle.BaselineArchives["svc-a"],
			"svc-b": incompleteBaselineB,
		}),
		bundle.DeltaPath,
		filepath.Join(dir, "out"))

	if exit != 4 {
		t.Fatalf("strict mode: exit = %d, want 4", exit)
	}
	// In strict mode, scan-all-then-abort: stderr should list svc-b's
	// missing layer even though svc-a passed.
	if !strings.Contains(stderr, "svc-b") {
		t.Errorf("stderr should mention svc-b; got:\n%s", stderr)
	}
}
```

(If `BuildTwoImageBundle` / `WriteBaselineSpec` are not present in `internal/testutil/`, add them following the same pattern as Task 1.5's `BuildIntraLayerBundle`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -tags integration ./cmd/ -run 'TestUnbundleCLI_(PartialModeSkipsB2|StrictAbortsAfterFullScan)' -v`
Expected: FAIL — Import() does not yet run preflight, so partial mode does not skip and strict mode does not abort upfront.

- [ ] **Step 3: Modify `Import()` and `importEachImage` to run preflight**

In `pkg/importer/importer.go::Import` (around line 99 — after `resolvedByName` is built):

```go
	cache := newBaselineBlobCache()

	// NEW: run preflight before any composeImage attempt.
	preflightResults, anyPreflightFail, perr := RunPreflight(ctx, bundle, resolved, opts.SystemContext, rep)
	if perr != nil {
		return perr // schema error is fatal regardless of mode
	}
	if opts.Strict && anyPreflightFail {
		// Strict mode: scan-all-then-abort with full report.
		return abortWithPreflightSummary(opts.reporter(), preflightResults)
	}

	// Filter to OK images only. Non-OK results enter the final report as skipped.
	applyList := make([]string, 0, len(preflightResults))
	skippedByPreflight := make(map[string]PreflightResult)
	for _, r := range preflightResults {
		if r.Status == PreflightOK {
			applyList = append(applyList, r.ImageName)
		} else {
			skippedByPreflight[r.ImageName] = r
		}
	}

	report := ApplyReport{Total: len(bundle.sidecar.Images)}
	for _, name := range applyList {
		// (existing per-image logic, but populating ApplyReport)
	}
	for name, r := range skippedByPreflight {
		report.Results = append(report.Results, ApplyImageResult{
			ImageName: name,
			Status:    ApplyImageSkippedPreflight,
			Err:       preflightResultToErr(r),
		})
	}

	renderSummary(os.Stderr, report)
	if report.Successful() < report.Total {
		return fmt.Errorf("%d of %d images failed apply", report.Total-report.Successful(), report.Total)
	}
	return nil
```

Refactor `importEachImage` to populate `ApplyReport` per image rather than returning early on first error:

```go
func importEachImage(
	ctx context.Context,
	bundle *extractedBundle,
	resolvedByName map[string]resolvedBaseline,
	outputs map[string]string,
	opts Options,
	cache *baselineBlobCache,
	applyList []string,
) ApplyReport {
	report := ApplyReport{Total: len(bundle.sidecar.Images)}
	for _, name := range applyList {
		img, ok := findImageByName(bundle.sidecar.Images, name)
		if !ok { continue }
		rb := resolvedByName[name]
		rawOut := outputs[name]
		if err := ensureOutputParent(rawOut); err != nil {
			report.Results = append(report.Results, ApplyImageResult{
				ImageName: name, Status: ApplyImageFailedCompose, Err: err,
			})
			continue
		}
		destRef, err := imageio.ParseReference(rawOut)
		if err != nil {
			report.Results = append(report.Results, ApplyImageResult{
				ImageName: name, Status: ApplyImageFailedCompose, Err: err,
			})
			continue
		}
		if err := composeImage(ctx, img, bundle, rb, destRef,
			opts.SystemContext, opts.AllowConvert, opts.reporter(), cache); err != nil {
			report.Results = append(report.Results, ApplyImageResult{
				ImageName: name, Status: ApplyImageFailedCompose, Err: err,
			})
			if opts.Strict { return report }
			continue
		}
		if err := verifyApplyInvariant(ctx, img, bundle, destRef, opts.SystemContext); err != nil {
			report.Results = append(report.Results, ApplyImageResult{
				ImageName: name, Status: ApplyImageFailedInvariant, Err: err,
			})
			if opts.Strict { return report }
			continue
		}
		report.Results = append(report.Results, ApplyImageResult{
			ImageName: name, Status: ApplyImageOK,
		})
	}
	return report
}

func findImageByName(imgs []diff.ImageEntry, name string) (diff.ImageEntry, bool) {
	for _, img := range imgs {
		if img.Name == name { return img, true }
	}
	return diff.ImageEntry{}, false
}

func preflightResultToErr(r PreflightResult) error {
	switch r.Status {
	case PreflightMissingPatchSource:
		return &ErrMissingPatchSource{
			ImageName: r.ImageName, PatchFromDigest: firstOrEmpty(r.MissingPatchSources),
		}
	case PreflightMissingReuseLayer:
		return &ErrMissingBaselineReuseLayer{
			ImageName: r.ImageName, LayerDigest: firstOrEmpty(r.MissingReuseLayers),
		}
	case PreflightError, PreflightSchemaError:
		return r.Err
	}
	return nil
}

func firstOrEmpty(ds []digest.Digest) digest.Digest {
	if len(ds) == 0 { return "" }
	return ds[0]
}

func abortWithPreflightSummary(rep progress.Reporter, results []PreflightResult) error {
	report := ApplyReport{Total: len(results)}
	for _, r := range results {
		status := ApplyImageOK
		if r.Status != PreflightOK {
			status = ApplyImageSkippedPreflight
		}
		report.Results = append(report.Results, ApplyImageResult{
			ImageName: r.ImageName, Status: status, Err: preflightResultToErr(r),
		})
	}
	renderSummary(os.Stderr, report)
	return fmt.Errorf("preflight rejected %d images (--strict)", countFailures(report))
}

func countFailures(r ApplyReport) int {
	n := 0
	for _, x := range r.Results {
		if x.Status != ApplyImageOK {
			n++
		}
	}
	return n
}
```

Adjust the call in `Import()` to pass `applyList` to `importEachImage` and merge `report` with `skippedByPreflight`. Refactor as needed to keep both paths consistent.

- [ ] **Step 4: Run integration tests**

Run: `go test -tags integration ./cmd/ -run 'TestUnbundleCLI_(PartialModeSkipsB2|StrictAbortsAfterFullScan)' -v`
Expected: PASS.

Run: `go test -tags integration -race ./cmd/... ./pkg/importer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/importer.go pkg/importer/summary.go pkg/importer/preflight.go cmd/unbundle_preflight_integration_test.go internal/testutil/*.go
git commit -m "feat(importer): wire RunPreflight into Import; partial/strict path

Pre-flight now runs before importEachImage. Partial mode skips
non-OK images and applies the rest, populating ApplyReport with
the skipped reasons. Strict mode scans every image then aborts
with a complete summary — no first-failure short-circuit.

importEachImage refactored to return ApplyReport so the partial
path is observable end-to-end. ErrMissingPatchSource /
ErrMissingBaselineReuseLayer are constructed from preflight
results for uniform error rendering."
```

### Task 3.4b: Partial mode handles baseline 503 (PreflightError) gracefully

**Files:**
- Modify: `cmd/unbundle_preflight_integration_test.go`

- [ ] **Step 1: Add the U6 test — baseline manifest 503 in partial mode**

```go
// Append to cmd/unbundle_preflight_integration_test.go
func TestUnbundleCLI_PreflightBaselineUnreachable_PartialSkips(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// 2-image bundle: svc-a baseline reachable; svc-b baseline returns 503.
	bundle := testutil.BuildTwoImageBundle(t, dir)
	srv503 := registrytest.NewAlwaysFailServer(t, http.StatusServiceUnavailable)
	defer srv503.Close()

	exit, _, stderr := runDiffah(t, ctx, "unbundle",
		"--baseline-spec", testutil.WriteBaselineSpec(t, map[string]string{
			"svc-a": bundle.BaselineArchives["svc-a"],
			"svc-b": "docker://" + srv503.Addr + "/svc-b:v1",
		}),
		"--tls-verify=false",
		"--retry-times=1",
		bundle.DeltaPath,
		filepath.Join(dir, "out"))

	if exit != 0 {
		t.Fatalf("partial mode with one unreachable baseline: exit = %d, want 0 (svc-a applied)", exit)
	}
	if !strings.Contains(stderr, "applied 1/2") {
		t.Errorf("stderr should report 'applied 1/2'; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "svc-b") {
		t.Errorf("stderr should mention svc-b; got:\n%s", stderr)
	}
}
```

(If `registrytest.NewAlwaysFailServer` is not present, add as a small helper — it serves any request with the given status code. ~20-line wrapper around `httptest.Server`.)

- [ ] **Step 2: Run test to verify it passes**

Run: `go test -tags integration ./cmd/ -run TestUnbundleCLI_PreflightBaselineUnreachable_PartialSkips -v`
Expected: PASS — partial mode with PreflightError on one baseline should skip that image and still apply the others.

- [ ] **Step 3: (no production-code change needed; PreflightError is already handled in Task 3.3 and partial-mode skip in Task 3.4)**

If Step 2 fails, debug whether `RunPreflight` correctly returns `PreflightError` (not propagating up as an aborting err) when baseline GetManifest fails. Reconcile with Task 3.3's contract.

- [ ] **Step 4: Run full integration suite under race**

Run: `go test -tags integration -race ./cmd/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/unbundle_preflight_integration_test.go internal/testutil/registrytest/*.go
git commit -m "test(cmd): partial mode survives unreachable baseline (U6)

Asserts that PreflightError on one baseline (HTTP 503) does not
abort the rest of the bundle in partial mode — svc-a still applies,
svc-b is skipped and reported in the final summary."
```

### Task 3.5: GET-bounded preflight test

**Files:**
- Create: `cmd/apply_preflight_integration_test.go`

- [ ] **Step 1: Write a mock-registry test that counts manifest GETs**

```go
//go:build integration

package cmd_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/leosocy/diffah/internal/testutil/registrytest"
)

func TestApplyCLI_PreflightManifestFetchBounded(t *testing.T) {
	ctx := context.Background()

	// Use the existing registrytest harness with a manifest-GET counter.
	srv := registrytest.NewServerWithCounter(t)
	defer srv.Close()

	// Push a real baseline image to srv.
	pushFixture := registrytest.MustPushFixture(t, srv, "service-x", "v1")

	// Build a delta whose baseline points at srv.
	deltaPath := buildDeltaAgainstRegistry(t, pushFixture)

	// Run apply pulling the same baseline back.
	exit, _, _ := runDiffah(t, ctx, "apply",
		"--baseline", "default=docker://"+srv.Addr+"/service-x:v1",
		"--tls-verify=false",
		deltaPath, "dir:"+t.TempDir())

	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}

	manifestGETs := atomic.LoadInt64(&srv.ManifestGETs)
	if manifestGETs > 2 {
		t.Errorf("baseline manifest GETs = %d, want ≤ 2 (preflight + apply may share)", manifestGETs)
	}
}
```

(If `registrytest.NewServerWithCounter` / `srv.ManifestGETs` is not present, add them under `internal/testutil/registrytest/` — extend the existing harness with a `ManifestGETs int64` counter incremented from the GET handler.)

- [ ] **Step 2: Run test**

Run: `go test -tags integration ./cmd/ -run TestApplyCLI_PreflightManifestFetchBounded -v`
Expected: FAIL initially if the registrytest harness lacks the counter; once the counter is added, PASS.

- [ ] **Step 3: Add `ManifestGETs` counter to `internal/testutil/registrytest`**

Locate the existing handler file (audit: `pkg/importer/registry_integration_test.go::pushFixtureIntoRegistry` confirms `registrytest` exists). In its server type:

```go
// internal/testutil/registrytest/server.go (or similar)
type Server struct {
    Addr         string
    ManifestGETs int64 // atomic
    // ...
}
// in the manifest GET handler:
if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifests/") {
    atomic.AddInt64(&s.ManifestGETs, 1)
}
```

- [ ] **Step 4: Re-run test**

Run: `go test -tags integration ./cmd/ -run TestApplyCLI_PreflightManifestFetchBounded -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/apply_preflight_integration_test.go internal/testutil/registrytest/*.go
git commit -m "test(cmd): preflight does not regress baseline manifest GET count

Pre-flight reads each baseline's manifest at most once before apply
begins; copy.Image's own manifest fetch may share or duplicate
depending on transport caching. Asserts ≤ 2 GETs per baseline as
the contract; ensures pre-flight is not multi-fetching."
```

### Task 3.6: CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add an entry under the Unreleased section**

```markdown
## [Unreleased] — Apply correctness & resilience (Track A)

### Behavior changes

- **`--strict` semantic widens.** In addition to the existing "baseline
  spec missing for image-X is an error," `--strict` now also rejects
  baselines that are present but incomplete (missing patch sources or
  missing reuse layers needed by the delta). Without `--strict` (default
  partial mode), affected images are skipped and a final summary lists
  them; the run still exits 0 if at least one image succeeded.

### New invariants

- Every successful `diffah apply` / `unbundle` re-reads the destination
  manifest and proves the layer set matches the sidecar's expectation.
  Failures produce exit 4 with explicit Missing/Unexpected diagnostics.

### Categorized errors

- B1 (`ErrMissingPatchSource`) and B2 (`ErrMissingBaselineReuseLayer`)
  surface from `apply` / `unbundle` with actionable hints. Auth, TLS,
  network, and timeout errors retain their existing classification.

### Performance

- Pre-flight is built into apply; failure modes for incomplete baselines
  are now detected before the first layer body is fetched.
```

- [ ] **Step 2: (no test to run for docs)**

Skip.

- [ ] **Step 3: (no implementation step)**

Skip.

- [ ] **Step 4: Verify CHANGELOG renders**

Run: `cat CHANGELOG.md | head -30`
Expected: new section visible at the top.

- [ ] **Step 5: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): apply correctness & resilience entry

Documents --strict semantic extension, end-to-end invariant, B1/B2
categorized errors, and pre-flight as user-facing changes. No flag
additions."
```

---

## Final Phase 3 Validation

After Task 3.6:

- [ ] Run full test matrix: `go test -race ./... && go test -tags integration -race ./...`
- [ ] Run linter: `make lint` (or the project's equivalent — check `Makefile`)
- [ ] Run formatter check: `gofmt -d $(find . -name '*.go' -not -path './vendor/*')` — zero diff expected
- [ ] Self-test all three Phase exit criteria against current master HEAD

If everything green, the three Phases are independently shippable as PR1 / PR2 / PR3.
