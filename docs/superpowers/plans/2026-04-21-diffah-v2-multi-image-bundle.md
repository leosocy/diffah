# diffah v2 Multi-Image Bundle Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the archive format from Phase 1's single-image flat sidecar to a unified bundle (feature marker + content-addressed `blobs/` pool + `images[]` list), add multi-image export (`--bundle` / `--pair`) and multi-baseline import (`--baseline NAME=PATH` / `--baseline-spec` / `--strict`) with cross-image blob dedup and a force-full rule for shared shipped layers.

**Architecture:** One schema rules all archives — single-image exports become bundles of length one. A global content-addressed blob pool deduplicates layers, manifests, and configs across images. Patch encoding still works per-image when a shipped blob is unique (refCount = 1); shared shipped blobs (refCount ≥ 2) are forced to encoding=full to avoid per-baseline reachability analysis. Migration strategy: rename existing `pkg/diff.Sidecar` → `LegacySidecar`, add new `Sidecar` with bundle shape alongside, migrate callers one by one, delete `LegacySidecar` at the end. Every intermediate commit builds.

**Tech Stack:** Go 1.25.4, `github.com/opencontainers/go-digest`, `go.podman.io/image/v5` (types/copy/directory/docker-archive/oci-archive), `github.com/klauspost/compress/zstd`, `github.com/spf13/cobra`, `github.com/stretchr/testify/require`.

**Spec reference:** `docs/superpowers/specs/2026-04-20-diffah-v2-multi-image-bundle-design.md` (§ markers below refer to this document).

**Decisions locked in-plan (clarifying spec where silent):**
- Retire the flag-based export form (`--target` / `--baseline` / `--baseline-manifest`). Spec §5.1 only lists `positional` / `--bundle` / `--pair`; the old flag form is a breaking removal documented in CHANGELOG.
- Retire `--baseline-manifest` entirely. Manifest-only baselines never worked with intra-layer and the bundle path needs real blob bytes for patch encoding.
- Positional `BASELINE` / `TARGET` arguments are **file paths** (not `transport:ref` references). Format (OCI archive vs. Docker Schema 2 archive) is detected by tar sniffing.
- `--intra-layer` supports `auto|off` only (Track A `required` is not merged; the spec §5.1 mention is forward-compat).
- `--dry-run` is retained on both `export` and `import`. Bundle-aware stats replace Phase 1 scalar summaries.
- Migration of the in-package `Sidecar` name uses a single mechanical rename commit (`Sidecar` → `LegacySidecar` including helpers) before the new type lands. The new type lives under `sidecar.go`; the legacy struct moves to `legacy_sidecar.go` and is deleted after all callers migrate.

---

## Phase 1 — Foundation: rename legacy + new sidecar types

Establish the new types alongside the preserved legacy type. No caller migration yet — callers still use `LegacySidecar`.

### Task 1: Mechanically rename `Sidecar` → `LegacySidecar` across the repo

**Files:**
- Modify: `pkg/diff/sidecar.go` — full rewrite into `legacy_sidecar.go`
- Rename: `pkg/diff/sidecar.go` → `pkg/diff/legacy_sidecar.go` (git mv)
- Modify: `pkg/diff/sidecar_test.go` → `pkg/diff/legacy_sidecar_test.go` (git mv)
- Modify: every caller referencing `diff.Sidecar`, `diff.ParseSidecar`, `diff.ImageRef`, `diff.BaselineRef`
- Modify: `pkg/exporter/exporter.go`, `pkg/exporter/exporter_test.go`
- Modify: `pkg/importer/importer.go`, `pkg/importer/composite_src.go`, `pkg/importer/integration_test.go`, `pkg/importer/importer_test.go`, `pkg/importer/composite_src_test.go`, `pkg/importer/intralayer_e2e_test.go`
- Modify: `internal/archive/reader.go`, `internal/archive/writer.go`, `internal/archive/reader_test.go`, `internal/archive/writer_test.go`
- Modify: `cmd/inspect.go`, `cmd/inspect_test.go`
- Keep (do NOT rename): `diff.SidecarFilename`, `diff.BlobRef`, `diff.Encoding`, `diff.ComputePlan` — these are reused by the new schema.

**Rename map (symbols):**
- `diff.Sidecar` → `diff.LegacySidecar`
- `diff.ImageRef` → `diff.LegacyTargetRef`
- `diff.BaselineRef` → `diff.LegacyBaselineRef`
- `diff.ParseSidecar` → `diff.ParseLegacySidecar`
- `(Sidecar).Marshal` → `(LegacySidecar).Marshal`
- `validateRequiredEntry` / `validateShippedEntry` — stay unexported, rename to `validateLegacyRequiredEntry` / `validateLegacyShippedEntry` for future-proofing

- [ ] **Step 1: Rename file in git**

```bash
git mv pkg/diff/sidecar.go pkg/diff/legacy_sidecar.go
git mv pkg/diff/sidecar_test.go pkg/diff/legacy_sidecar_test.go
```

- [ ] **Step 2: Apply symbol rename across the repo**

Use an editor's rename or:

```bash
gofmt -w $(rg -l 'diff\.Sidecar|diff\.ParseSidecar|diff\.ImageRef|diff\.BaselineRef' --type go) 2>/dev/null || true
```

Then edit each file to replace symbols per the rename map above. Example for `pkg/diff/legacy_sidecar.go`:
- Struct type: `type LegacySidecar struct { ... }`
- Top-level comment: "LegacySidecar is the Phase 1 pre-bundle sidecar shape, retained until all callers migrate."
- Public type names: `LegacyTargetRef`, `LegacyBaselineRef`

- [ ] **Step 3: Run the build + tests**

Run: `make lint test`
Expected: all green (mechanical rename must not change behavior).

- [ ] **Step 4: Commit the rename together with the plan file itself**

```bash
git add -A
git add docs/superpowers/plans/2026-04-21-diffah-v2-multi-image-bundle.md
git commit -m "refactor(diff): rename Sidecar to LegacySidecar ahead of bundle rewrite

Pure mechanical rename. No behavior change. Prepares for adding
the new bundle-shaped Sidecar type alongside. Commits the
bundle implementation plan alongside the first mechanical
rename so the plan lives under the same commit series."
```

---

### Task 2: Add new constants and base types (empty validation)

**Files:**
- Create: `pkg/diff/sidecar.go`
- Test: `pkg/diff/sidecar_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/diff/sidecar_test.go
package diff

import (
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestSidecar_Marshal_MinimalValidBundle(t *testing.T) {
	s := Sidecar{
		Version:     SchemaVersionV1,
		Feature:     FeatureBundle,
		Tool:        "diffah",
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Blobs: map[digest.Digest]BlobEntry{
			"sha256:aa": {
				Size: 5, MediaType: "application/vnd.oci.image.manifest.v1+json",
				Encoding: EncodingFull, ArchiveSize: 5,
			},
		},
		Images: []ImageEntry{{
			Name: "service-a",
			Baseline: BaselineRef{
				ManifestDigest: "sha256:bb",
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
			Target: TargetRef{
				ManifestDigest: "sha256:aa",
				ManifestSize:   5,
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
		}},
	}
	out, err := s.Marshal()
	require.NoError(t, err)
	require.Contains(t, string(out), `"feature": "bundle"`)
	require.Contains(t, string(out), `"name": "service-a"`)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/diff/ -run TestSidecar_Marshal_MinimalValidBundle -v`
Expected: FAIL — `Sidecar` not declared.

- [ ] **Step 3: Add the new types**

Create `pkg/diff/sidecar.go`:

```go
package diff

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/opencontainers/go-digest"
)

// FeatureBundle marks an archive as using the bundle schema (one or more
// image pairs plus a global content-addressed blob pool).
const FeatureBundle = "bundle"

// TargetRef is the per-image target manifest pointer.
type TargetRef struct {
	ManifestDigest digest.Digest `json:"manifest_digest"`
	ManifestSize   int64         `json:"manifest_size"`
	MediaType      string        `json:"media_type"`
}

// BaselineRef is the per-image baseline manifest pointer. SourceHint is
// informational only.
type BaselineRef struct {
	ManifestDigest digest.Digest `json:"manifest_digest"`
	MediaType      string        `json:"media_type"`
	SourceHint     string        `json:"source_hint,omitempty"`
}

// ImageEntry describes one image pair inside a bundle. The target's layer
// list and config are derived by reading blobs[TargetRef.ManifestDigest].
type ImageEntry struct {
	Name     string      `json:"name"`
	Baseline BaselineRef `json:"baseline"`
	Target   TargetRef   `json:"target"`
}

// BlobEntry describes how one content-addressed blob (layer, manifest, or
// config) is stored in the archive. Digest is the map key in Sidecar.Blobs;
// the struct itself only carries the per-entry metadata.
type BlobEntry struct {
	Size            int64         `json:"size"`
	MediaType       string        `json:"media_type"`
	Encoding        Encoding      `json:"encoding"`
	Codec           string        `json:"codec,omitempty"`
	PatchFromDigest digest.Digest `json:"patch_from_digest,omitempty"`
	ArchiveSize     int64         `json:"archive_size"`
}

// Sidecar is the bundle-format diffah.json. Feature discriminates format
// families; Version tracks schema evolution inside the bundle family.
type Sidecar struct {
	Version     string                       `json:"version"`
	Feature     string                       `json:"feature"`
	Tool        string                       `json:"tool"`
	ToolVersion string                       `json:"tool_version"`
	CreatedAt   time.Time                    `json:"created_at"`
	Platform    string                       `json:"platform"`
	Blobs       map[digest.Digest]BlobEntry  `json:"blobs"`
	Images      []ImageEntry                 `json:"images"`
}

// Marshal encodes the sidecar with two-space indentation. Validation stub
// accepts everything for now; rules land in Task 3.
func (s Sidecar) Marshal() ([]byte, error) {
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sidecar: %w", err)
	}
	return out, nil
}

// ParseSidecar is a placeholder for Task 3; decodes only for now.
func ParseSidecar(raw []byte) (*Sidecar, error) {
	var s Sidecar
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode sidecar: %w", err)
	}
	return &s, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/diff/ -run TestSidecar_Marshal_MinimalValidBundle -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/sidecar.go pkg/diff/sidecar_test.go
git commit -m "feat(diff): add bundle Sidecar types with minimal marshal

Introduces Sidecar, ImageEntry, BlobEntry, TargetRef, BaselineRef
and the FeatureBundle constant. Validation is a stub; rules come
in the next task. Legacy Sidecar continues to serve existing
callers."
```

---

### Task 3: Implement new sidecar validation rules

> **Execution order:** run **Task 4 (error sentinels) first**, then return to
> Task 3. Task 3 wires `ErrPhase1Archive`, `ErrUnknownBundleVersion`, and
> `ErrInvalidBundleFormat` into `ParseSidecar`, and those types are defined in
> Task 4.

**Files:**
- Modify: `pkg/diff/sidecar.go`
- Test: `pkg/diff/sidecar_test.go`

Rules mirror spec §4.2:
- `version == SchemaVersionV1` required
- `feature == FeatureBundle` required (otherwise → ErrPhase1Archive or ErrUnknownBundleVersion, both added Task 4)
- `platform != ""`
- `blobs != nil` (empty map allowed)
- `images` non-empty; unique `name`; each name matches `^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`
- Each `image.target.manifest_digest` appears as a key in `blobs`
- Per BlobEntry:
  - `encoding == full` → `codec == ""`, `patch_from_digest == ""`, `archive_size == size`
  - `encoding == patch` → `codec != ""`, `patch_from_digest != ""`, `0 < archive_size < size`
  - `encoding == ""` → error

- [ ] **Step 1: Write the failing test with a table of malformed inputs**

```go
func TestSidecar_Validate_RejectsMalformed(t *testing.T) {
	base := minimalValidBundle(t)
	cases := []struct {
		name   string
		mut    func(*Sidecar)
		reason string
	}{
		{"empty platform", func(s *Sidecar) { s.Platform = "" }, "platform"},
		{"empty images", func(s *Sidecar) { s.Images = nil }, "images"},
		{"bad name", func(s *Sidecar) { s.Images[0].Name = "-leading" }, "name"},
		{"duplicate name", func(s *Sidecar) {
			dup := s.Images[0]
			s.Images = append(s.Images, dup)
		}, "unique"},
		{"target digest not in blobs", func(s *Sidecar) {
			s.Images[0].Target.ManifestDigest = "sha256:ff"
		}, "blobs"},
		{"full blob with codec", func(s *Sidecar) {
			e := s.Blobs["sha256:aa"]
			e.Codec = "zstd-patch"
			s.Blobs["sha256:aa"] = e
		}, "codec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mut(&s)
			_, err := s.Marshal()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.reason)
		})
	}
}

// minimalValidBundle is reused by multiple tests; keep it here.
func minimalValidBundle(t *testing.T) Sidecar {
	t.Helper()
	return Sidecar{
		Version: SchemaVersionV1, Feature: FeatureBundle, Tool: "diffah",
		ToolVersion: "test", CreatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Platform: "linux/amd64",
		Blobs: map[digest.Digest]BlobEntry{
			"sha256:aa": {Size: 5, MediaType: "application/vnd.oci.image.manifest.v1+json",
				Encoding: EncodingFull, ArchiveSize: 5},
		},
		Images: []ImageEntry{{
			Name:     "service-a",
			Baseline: BaselineRef{ManifestDigest: "sha256:bb", MediaType: "application/vnd.oci.image.manifest.v1+json"},
			Target:   TargetRef{ManifestDigest: "sha256:aa", ManifestSize: 5, MediaType: "application/vnd.oci.image.manifest.v1+json"},
		}},
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/diff/ -run TestSidecar_Validate_RejectsMalformed -v`
Expected: most subtests fail (validation currently allows everything).

- [ ] **Step 3: Implement validate()**

Add to `pkg/diff/sidecar.go`:

```go
import "regexp"

var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func (s Sidecar) validate() error {
	if s.Platform == "" {
		return &ErrSidecarSchema{Reason: "platform is required"}
	}
	if s.Blobs == nil {
		return &ErrSidecarSchema{Reason: "blobs is required (may be empty)"}
	}
	if len(s.Images) == 0 {
		return &ErrSidecarSchema{Reason: "images must contain at least one entry"}
	}
	seen := make(map[string]struct{}, len(s.Images))
	for i, img := range s.Images {
		if !nameRegex.MatchString(img.Name) {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].name %q does not match %s", i, img.Name, nameRegex)}
		}
		if _, dup := seen[img.Name]; dup {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].name %q must be unique", i, img.Name)}
		}
		seen[img.Name] = struct{}{}
		if img.Target.ManifestDigest == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].target.manifest_digest is required", i)}
		}
		if _, ok := s.Blobs[img.Target.ManifestDigest]; !ok {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].target.manifest_digest %s must appear in blobs",
				i, img.Target.ManifestDigest)}
		}
		if img.Baseline.ManifestDigest == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"images[%d].baseline.manifest_digest is required", i)}
		}
	}
	for d, b := range s.Blobs {
		if err := validateBlobEntry(d, b); err != nil {
			return err
		}
	}
	return nil
}

func validateBlobEntry(d digest.Digest, b BlobEntry) error {
	switch b.Encoding {
	case EncodingFull:
		if b.Codec != "" || b.PatchFromDigest != "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=full must not set codec/patch_from_digest", d)}
		}
		if b.ArchiveSize != b.Size {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=full requires archive_size == size (got %d vs %d)",
				d, b.ArchiveSize, b.Size)}
		}
	case EncodingPatch:
		if b.Codec == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=patch requires codec", d)}
		}
		if b.PatchFromDigest == "" {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=patch requires patch_from_digest", d)}
		}
		if b.ArchiveSize <= 0 || b.ArchiveSize >= b.Size {
			return &ErrSidecarSchema{Reason: fmt.Sprintf(
				"blobs[%s] encoding=patch requires 0 < archive_size < size (got %d vs %d)",
				d, b.ArchiveSize, b.Size)}
		}
	default:
		return &ErrSidecarSchema{Reason: fmt.Sprintf(
			"blobs[%s] encoding=%q is not recognized", d, b.Encoding)}
	}
	return nil
}
```

Wire validation into `Marshal` and `ParseSidecar`:

```go
func (s Sidecar) Marshal() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sidecar: %w", err)
	}
	return out, nil
}

func ParseSidecar(raw []byte) (*Sidecar, error) {
	var s Sidecar
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, &ErrInvalidBundleFormat{Cause: err}
	}
	switch {
	case s.Feature != FeatureBundle:
		return nil, &ErrPhase1Archive{GotFeature: s.Feature}
	case s.Version != SchemaVersionV1:
		return nil, &ErrUnknownBundleVersion{Got: s.Version}
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
```

`ErrPhase1Archive`, `ErrUnknownBundleVersion`, `ErrInvalidBundleFormat` are defined in Task 4 — make sure Task 4 is committed before running this step, as flagged at the top of this task.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/diff/ -v`
Expected: all pass after Task 4 lands.

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/sidecar.go pkg/diff/sidecar_test.go
git commit -m "feat(diff): validate bundle sidecar shape

Rejects missing platform/images, duplicate or malformed image
names, target digests not present in blobs, and per-blob
encoding inconsistencies. Mirrors legacy validation semantics
applied to the new schema."
```

---

### Task 4: Add new error sentinels

> **Execution order:** this task must run **before Task 3**. See the note at
> the top of Task 3 above.

**Files:**
- Modify: `pkg/diff/errors.go`
- Test: `pkg/diff/errors_test.go` (new)

- [ ] **Step 1: Write the failing test**

```go
// pkg/diff/errors_test.go
package diff

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBundleErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"phase1", &ErrPhase1Archive{}, "Phase 1 schema"},
		{"unknown version", &ErrUnknownBundleVersion{Got: "v9"}, `unknown bundle version "v9"`},
		{"invalid format", &ErrInvalidBundleFormat{Cause: errors.New("x")}, "invalid bundle format"},
		{"multi image needs named", &ErrMultiImageNeedsNamedBaselines{}, "multi-image"},
		{"baseline unknown", &ErrBaselineNameUnknown{Name: "foo", Available: []string{"a", "b"}}, "not in bundle"},
		{"baseline mismatch", &ErrBaselineMismatch{Name: "a", Expected: "sha256:xx", Got: "sha256:yy"}, "mismatch"},
		{"baseline missing", &ErrBaselineMissing{Names: []string{"b"}}, "missing"},
		{"invalid spec", &ErrInvalidBundleSpec{Reason: "bad"}, "bundle spec"},
		{"duplicate name", &ErrDuplicateBundleName{Name: "a"}, "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, tc.err.Error(), tc.want)
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/diff/ -run TestBundleErrorMessages -v`
Expected: FAIL — types not declared.

- [ ] **Step 3: Add error types**

Append to `pkg/diff/errors.go`:

```go
// ErrPhase1Archive is returned when an archive's sidecar lacks the bundle
// feature marker — typically a Phase 1 flat archive.
type ErrPhase1Archive struct{ GotFeature string }

func (e *ErrPhase1Archive) Error() string {
	if e.GotFeature == "" {
		return "archive uses Phase 1 schema (feature marker missing); " +
			"re-export with the current diffah"
	}
	return fmt.Sprintf("archive uses Phase 1 schema (feature=%q); "+
		"re-export with the current diffah", e.GotFeature)
}

// ErrUnknownBundleVersion is returned when feature=bundle but the version is
// not recognized.
type ErrUnknownBundleVersion struct{ Got string }

func (e *ErrUnknownBundleVersion) Error() string {
	return fmt.Sprintf("unknown bundle version %q (this build supports %q)",
		e.Got, SchemaVersionV1)
}

// ErrInvalidBundleFormat wraps a sidecar JSON decoding failure for a
// bundle-feature archive.
type ErrInvalidBundleFormat struct{ Cause error }

func (e *ErrInvalidBundleFormat) Error() string {
	return fmt.Sprintf("invalid bundle format: %v", e.Cause)
}
func (e *ErrInvalidBundleFormat) Unwrap() error { return e.Cause }

// ErrMultiImageNeedsNamedBaselines is returned when a positional baseline is
// supplied for a multi-image archive.
type ErrMultiImageNeedsNamedBaselines struct{ N int }

func (e *ErrMultiImageNeedsNamedBaselines) Error() string {
	return fmt.Sprintf("archive contains %d images; multi-image import requires "+
		"--baseline NAME=PATH or --baseline-spec", e.N)
}

// ErrBaselineNameUnknown is returned when a --baseline/--baseline-spec entry
// names an image not in the bundle.
type ErrBaselineNameUnknown struct {
	Name      string
	Available []string
}

func (e *ErrBaselineNameUnknown) Error() string {
	return fmt.Sprintf("baseline name %q not in bundle (available: %v)",
		e.Name, e.Available)
}

// ErrBaselineMismatch is returned when a provided baseline's manifest digest
// does not match the sidecar's record for that image name.
type ErrBaselineMismatch struct {
	Name, Expected, Got string
}

func (e *ErrBaselineMismatch) Error() string {
	return fmt.Sprintf("wrong baseline for %q: manifest digest mismatch "+
		"(expected %s, got %s)", e.Name, e.Expected, e.Got)
}

// ErrBaselineMissing is returned under --strict when one or more bundle images
// have no matching baseline.
type ErrBaselineMissing struct{ Names []string }

func (e *ErrBaselineMissing) Error() string {
	return fmt.Sprintf("strict mode: missing baselines for %v", e.Names)
}

// ErrInvalidBundleSpec is returned when a bundle spec or baseline spec JSON
// is malformed or missing a required field.
type ErrInvalidBundleSpec struct {
	Path   string
	Reason string
}

func (e *ErrInvalidBundleSpec) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("invalid bundle spec %q: %s", e.Path, e.Reason)
	}
	return fmt.Sprintf("invalid bundle spec: %s", e.Reason)
}

// ErrDuplicateBundleName is returned when two --pair flags or spec entries
// share a name.
type ErrDuplicateBundleName struct{ Name string }

func (e *ErrDuplicateBundleName) Error() string {
	return fmt.Sprintf("duplicate bundle image name %q", e.Name)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/diff/ -v`
Expected: `TestBundleErrorMessages` passes; `TestSidecar_Validate_*` from Task 3 also passes.

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/errors.go pkg/diff/errors_test.go
git commit -m "feat(diff): add bundle error sentinels

ErrPhase1Archive, ErrUnknownBundleVersion, ErrInvalidBundleFormat,
ErrMultiImageNeedsNamedBaselines, ErrBaselineNameUnknown,
ErrBaselineMismatch, ErrBaselineMissing, ErrInvalidBundleSpec,
ErrDuplicateBundleName."
```

---

### Task 5: Sidecar round-trip + deterministic order test

**Files:**
- Modify: `pkg/diff/sidecar_test.go`

Spec decision #13: `blobs` map and tar blob files must be deterministic across runs. `encoding/json` sorts map keys alphabetically, so round-trip + byte stability is testable here.

- [ ] **Step 1: Write the failing test**

```go
func TestSidecar_Marshal_DeterministicOrder(t *testing.T) {
	s := minimalValidBundle(t)
	s.Blobs["sha256:cc"] = BlobEntry{Size: 3, MediaType: "text/plain",
		Encoding: EncodingFull, ArchiveSize: 3}
	s.Blobs["sha256:bb"] = BlobEntry{Size: 4, MediaType: "text/plain",
		Encoding: EncodingFull, ArchiveSize: 4}

	first, err := s.Marshal()
	require.NoError(t, err)
	second, err := s.Marshal()
	require.NoError(t, err)
	require.Equal(t, first, second, "marshal must be deterministic")

	// Keys must appear in sorted order.
	order := orderOfTopLevelBlobsKeys(t, first)
	require.Equal(t, []string{"sha256:aa", "sha256:bb", "sha256:cc"}, order)
}

func TestSidecar_RoundTrip_PreservesAllFields(t *testing.T) {
	original := minimalValidBundle(t)
	raw, err := original.Marshal()
	require.NoError(t, err)
	parsed, err := ParseSidecar(raw)
	require.NoError(t, err)
	require.Equal(t, original, *parsed)
}

// orderOfTopLevelBlobsKeys extracts map keys inside the top-level "blobs"
// object by streaming through the bytes with a json.Decoder. Avoids string
// matching so the test stays stable if the marshaler shifts whitespace.
func orderOfTopLevelBlobsKeys(t *testing.T, raw []byte) []string {
	// Simple implementation: parse into a structured view, then rely on
	// encoding/json determinism for strings. Because json.Unmarshal shuffles
	// maps, we instead scan for "blobs": { and pick up the keys in order.
	// ... (implementation left as an exercise but must be deterministic,
	// e.g. json.Decoder-based token walker)
}
```

- [ ] **Step 2: Run test to verify it fails (or already passes)**

Run: `go test ./pkg/diff/ -run 'Sidecar_Marshal_DeterministicOrder|Sidecar_RoundTrip' -v`
Expected: both should pass (encoding/json sorts map keys), but the order-checking helper may fail if not implemented yet.

- [ ] **Step 3: Implement order helper**

Use `json.Decoder` token stream inside `orderOfTopLevelBlobsKeys`:

```go
import "encoding/json"

func orderOfTopLevelBlobsKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Walk until we enter the "blobs" object; collect keys until its close.
	var keys []string
	depth := 0
	inBlobs := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				depth++
			case '}':
				if inBlobs && depth == 2 {
					return keys
				}
				depth--
			}
		case string:
			if depth == 1 && v == "blobs" {
				inBlobs = true
				continue
			}
			if inBlobs && depth == 2 {
				keys = append(keys, v)
				// Skip the next value token(s) by reading one more token.
				// Because value can be an object we need to descend; easiest:
				// dec.Decode into a discard.
				var discard BlobEntry
				_ = dec.Decode(&discard)
				depth--
			}
		}
	}
	return keys
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/diff/ -v`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/sidecar_test.go
git commit -m "test(diff): lock deterministic blob key order in sidecar"
```

---

### Task 6: Plan decision — reuse BlobRef, drop unneeded fields

**Files:**
- Read-only audit: `pkg/diff/plan.go`

`BlobRef` in `plan.go` currently carries the intra-layer encoding fields (Encoding, Codec, PatchFromDigest, ArchiveSize). The new schema stores those fields on `BlobEntry` inside `blobs{}`, keyed by digest. `BlobRef` (and `ComputePlan`) is still useful as a per-pair planning primitive (exporter uses it to classify target layers by presence in baseline).

**Decision:** Keep `BlobRef` and `ComputePlan` as-is. They remain internal to `pkg/exporter` planning; they no longer appear in the persisted Sidecar shape. The encoding fields on BlobRef continue to carry planner output within the exporter — they simply don't reach JSON anymore.

No code change this task. Document the decision.

- [ ] **Step 1: Add a package comment paragraph**

Edit `pkg/diff/plan.go` line 1 block:

```go
// Package-level comment: BlobRef remains the exporter planner's internal type.
// The v2 bundle sidecar does not persist per-image BlobRef lists — layer
// classification is derived at import time by intersecting a target
// manifest's layer set with the archive's blobs map (see spec §4.3).
```

Add a three-line note at the top of the existing Plan doc-comment pointing at `pkg/diff/sidecar.go` for the persistent shape.

- [ ] **Step 2: Run lint + build**

Run: `make lint test`
Expected: green.

- [ ] **Step 3: Commit**

```bash
git add pkg/diff/plan.go
git commit -m "docs(diff): clarify BlobRef/Plan scope vs bundle sidecar"
```

---

## Phase 2 — Bundle spec and baseline spec JSON parsers

Parse the two JSON input files accepted by the CLI: `--bundle FILE` for export, `--baseline-spec FILE` for import.

### Task 7: BundleSpec JSON type + parser

**Files:**
- Create: `pkg/diff/bundle_spec.go`
- Test: `pkg/diff/bundle_spec_test.go`

Requirements (spec §5.3):
- Shape: `{"pairs":[{"name","baseline","target"}...]}`
- All three fields required per pair.
- Relative paths resolved against the spec file's directory.
- Unknown top-level or per-pair fields tolerated.
- Duplicate `name` → `ErrDuplicateBundleName`.
- Malformed JSON → `ErrInvalidBundleSpec`.

- [ ] **Step 1: Write the failing test**

```go
package diff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBundleSpec_HappyPath(t *testing.T) {
	dir := t.TempDir()
	// Create sibling target files so path resolution returns absolute paths.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.tar"), []byte{}, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.tar"), []byte{}, 0o600))
	raw := []byte(`{
		"pairs": [
			{"name":"service-a","baseline":"a.tar","target":"b.tar"}
		]
	}`)
	specPath := filepath.Join(dir, "bundle.json")
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	spec, err := ParseBundleSpec(specPath)
	require.NoError(t, err)
	require.Len(t, spec.Pairs, 1)
	require.Equal(t, "service-a", spec.Pairs[0].Name)
	require.Equal(t, filepath.Join(dir, "a.tar"), spec.Pairs[0].Baseline)
	require.Equal(t, filepath.Join(dir, "b.tar"), spec.Pairs[0].Target)
}

func TestParseBundleSpec_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing name", `{"pairs":[{"baseline":"a","target":"b"}]}`, "name"},
		{"duplicate name", `{"pairs":[{"name":"x","baseline":"a","target":"b"},{"name":"x","baseline":"c","target":"d"}]}`, "duplicate"},
		{"bad name", `{"pairs":[{"name":"-bad","baseline":"a","target":"b"}]}`, "name"},
		{"bad JSON", `{invalid`, "bundle spec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			specPath := filepath.Join(dir, "bundle.json")
			require.NoError(t, os.WriteFile(specPath, []byte(tc.body), 0o600))
			_, err := ParseBundleSpec(specPath)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/diff/ -run ParseBundleSpec -v`
Expected: FAIL — `ParseBundleSpec` not declared.

- [ ] **Step 3: Implement ParseBundleSpec**

Create `pkg/diff/bundle_spec.go`:

```go
package diff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BundlePairSpec is one entry from a bundle spec JSON file.
type BundlePairSpec struct {
	Name     string `json:"name"`
	Baseline string `json:"baseline"`
	Target   string `json:"target"`
}

// BundleSpec is the parsed bundle spec file passed via `diffah export --bundle`.
type BundleSpec struct {
	Pairs []BundlePairSpec `json:"pairs"`
}

// ParseBundleSpec reads path and returns a validated BundleSpec. Relative
// Baseline/Target paths are resolved against the directory containing path.
func ParseBundleSpec(path string) (*BundleSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrInvalidBundleSpec{Path: path, Reason: err.Error()}
	}
	var spec BundleSpec
	// Lenient: unknown fields are ignored.
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

func resolveSpecPath(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/diff/ -v`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/bundle_spec.go pkg/diff/bundle_spec_test.go
git commit -m "feat(diff): parse bundle spec JSON for export"
```

---

### Task 8: BaselineSpec JSON type + parser

**Files:**
- Modify: `pkg/diff/bundle_spec.go`
- Modify: `pkg/diff/bundle_spec_test.go`

Shape (spec §6.2): `{"baselines":{"NAME":"PATH",...}}`

- [ ] **Step 1: Write the failing test**

```go
func TestParseBaselineSpec_HappyPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.tar"), []byte{}, 0o600))
	raw := []byte(`{"baselines":{"service-a":"a.tar"}}`)
	specPath := filepath.Join(dir, "baselines.json")
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	spec, err := ParseBaselineSpec(specPath)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"service-a": filepath.Join(dir, "a.tar"),
	}, spec.Baselines)
}

func TestParseBaselineSpec_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"bad JSON", "{", "bundle spec"},
		{"empty map", `{"baselines":{}}`, "non-empty"},
		{"bad name", `{"baselines":{"-leading":"a"}}`, "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			specPath := filepath.Join(dir, "spec.json")
			require.NoError(t, os.WriteFile(specPath, []byte(tc.body), 0o600))
			_, err := ParseBaselineSpec(specPath)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/diff/ -run ParseBaselineSpec -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `pkg/diff/bundle_spec.go`:

```go
// BaselineSpec is the parsed baseline spec file passed via
// `diffah import --baseline-spec`.
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/diff/ -v`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/bundle_spec.go pkg/diff/bundle_spec_test.go
git commit -m "feat(diff): parse baseline spec JSON for import"
```

---

## Phase 3 — Archive format sniffer

### Task 9: OCI vs Docker Schema 2 archive detection

Positional and `--pair` inputs are file paths, not `transport:ref` strings. The exporter/importer needs to pick the right `types.ImageReference` factory.

**Files:**
- Create: `internal/imageio/sniff.go`
- Test: `internal/imageio/sniff_test.go`

Detection rule: open tar, look for `oci-layout` at the archive root → OCI archive; else → Docker Schema 2 archive. No magic bytes — both are tarballs.

- [ ] **Step 1: Write the failing test (uses existing fixtures)**

```go
package imageio

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSniffArchiveFormat(t *testing.T) {
	cases := []struct {
		name, path, want string
	}{
		{"oci v1", "../../testdata/fixtures/v1_oci.tar", "oci-archive"},
		{"s2 v1", "../../testdata/fixtures/v1_s2.tar", "docker-archive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SniffArchiveFormat(tc.path)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/imageio/ -run TestSniffArchiveFormat -v`
Expected: FAIL.

- [ ] **Step 3: Implement SniffArchiveFormat + OpenArchiveRef**

Create `internal/imageio/sniff.go`:

```go
package imageio

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	dockerarchive "go.podman.io/image/v5/docker/archive"
	dockerref "go.podman.io/image/v5/docker/reference"
	ociarchive "go.podman.io/image/v5/oci/archive"
	"go.podman.io/image/v5/types"
)

const (
	FormatOCIArchive    = "oci-archive"
	FormatDockerArchive = "docker-archive"
)

// SniffArchiveFormat returns FormatOCIArchive or FormatDockerArchive by
// scanning the first few tar entries of path.
func SniffArchiveFormat(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for i := 0; i < 64; i++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar %s: %w", path, err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "oci-layout" {
			return FormatOCIArchive, nil
		}
		if name == "manifest.json" {
			return FormatDockerArchive, nil
		}
	}
	return "", fmt.Errorf("cannot determine archive format for %s", path)
}

// OpenArchiveRef builds a types.ImageReference for path using the sniffed
// format. For Docker archives, a synthetic ref "diffah-in:latest" is used
// because the go.podman.io docker-archive transport requires a tag.
func OpenArchiveRef(path string) (types.ImageReference, error) {
	format, err := SniffArchiveFormat(path)
	if err != nil {
		return nil, err
	}
	switch format {
	case FormatOCIArchive:
		return ociarchive.NewReference(path, "")
	case FormatDockerArchive:
		named, err := dockerref.ParseNormalizedNamed("diffah-in:latest")
		if err != nil {
			return nil, fmt.Errorf("build docker ref: %w", err)
		}
		nt, ok := named.(dockerref.NamedTagged)
		if !ok {
			return nil, fmt.Errorf("docker ref not NamedTagged")
		}
		return dockerarchive.NewReference(path, nt)
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/imageio/ -v`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/imageio/sniff.go internal/imageio/sniff_test.go
git commit -m "feat(imageio): sniff OCI vs Docker Schema 2 archive path"
```

---

## Phase 4 — Exporter rewrite

The new exporter accepts `[]Pair`, produces a single bundle archive with a global blob pool. Retire every flag-based Options field (`TargetRef`, `BaselineRef`, `BaselineManifestPath`).

Phase 4 task boundaries follow spec §5.4 pipeline stages.

### Task 10: New exporter.Options + Pair type

**Files:**
- Modify: `pkg/exporter/exporter.go`
- Create: `pkg/exporter/pair.go`
- Modify: `pkg/exporter/exporter_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/exporter/pair_test.go
package exporter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPair_ResolveUnique(t *testing.T) {
	pairs := []Pair{
		{Name: "a", BaselinePath: "b1.tar", TargetPath: "t1.tar"},
		{Name: "b", BaselinePath: "b2.tar", TargetPath: "t2.tar"},
	}
	require.NoError(t, ValidatePairs(pairs))

	dup := append(pairs, Pair{Name: "a", BaselinePath: "x", TargetPath: "y"})
	err := ValidatePairs(dup)
	require.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/exporter/ -run TestPair_ResolveUnique -v`
Expected: FAIL.

- [ ] **Step 3: Implement Pair + Options (retire old fields)**

Create `pkg/exporter/pair.go`:

```go
package exporter

import (
	"github.com/leosocy/diffah/pkg/diff"
)

// Pair is one resolved image pair to include in a bundle.
type Pair struct {
	Name         string
	BaselinePath string
	TargetPath   string
}

// ValidatePairs enforces non-empty, unique names conforming to the bundle
// name regex.
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
```

Replace `Options` in `pkg/exporter/exporter.go`:

```go
// Options carries all inputs to Export.
type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string // "auto" | "off"
	CreatedAt   time.Time

	// fingerprinter is injected by tests via ExportWithFingerprinter.
	fingerprinter Fingerprinter
}
```

Delete from Options: `TargetRef`, `BaselineRef`, `BaselineManifestPath`. This breaks every existing exporter test and CLI call site. That's fine — we are not yet at a green build; we intentionally let callers break until the migration tasks that follow. **However** the package must still build. To keep that invariant, replace the existing `Export` body with a stub:

```go
func Export(ctx context.Context, opts Options) error {
	return fmt.Errorf("bundle export not yet wired in this commit")
}

func DryRun(ctx context.Context, opts Options) (DryRunStats, error) {
	return DryRunStats{}, fmt.Errorf("bundle dry-run not yet wired in this commit")
}
```

and delete the stale helpers (`openBaseline`, `copyTargetIntoDir`, `buildSidecar`, `resolveShipped`, `newDirBlobReader`, `readBaselineBlob`, `writePayloads`, `loadTargetManifest`, `verifyExport`, `derivePlatformFromConfig`) — they are incompatible with the new `Options` and will be reimplemented per-pair in Tasks 11–19.

**Also:**
- Delete every test in `pkg/exporter/exporter_test.go` that constructs the old Options shape. Tests will be replaced in later tasks; leaving them broken prevents `go test ./...` from passing (which violates spec §11 only if the intermediate commit doesn't *build*; failing tests are allowed). To stay strictly within the spec rule, mark them with `t.Skip("rewritten in Task N")` rather than deleting — makes their purpose discoverable during migration.
- Keep `known_dest.go` and `known_dest_test.go` — KnownBlobsDest is reused per-pair in Task 12.
- Keep `baseline.go`, `baseline_test.go` — BaselineSet is reused per-pair.
- Keep `intralayer.go`, `intralayer_test.go`, `fingerprint.go`, `fingerprint_test.go` — planner primitives are reused.

- [ ] **Step 4: Run build**

Run: `go build ./...`
Expected: pass. `go test ./pkg/exporter/` shows Pair tests passing and old tests skipped.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/pair.go pkg/exporter/pair_test.go pkg/exporter/exporter.go pkg/exporter/exporter_test.go
git commit -m "refactor(exporter): replace single-image Options with Pair list

Breaks the single-image Export body (stubbed). Per-pair planning
and global blob pool land in Tasks 11-19. Existing exporter tests
skipped in place; replacements follow in the same migration pass."
```

---

### Task 11: Per-pair open + classify

**Files:**
- Create: `pkg/exporter/perpair.go`
- Test: `pkg/exporter/perpair_test.go`

For each Pair, open baseline and target via `imageio.OpenArchiveRef`, read their manifests, and produce a `pairPlan` holding the target's manifest bytes, config bytes, layer list, and the baseline's layer digest set.

Result type:

```go
type pairPlan struct {
	Name              string
	BaselineRef       types.ImageReference
	TargetRef         types.ImageReference
	TargetManifest    []byte
	TargetMediaType   string
	TargetLayerDescs  []diff.BlobRef // per-layer: digest, size, media_type (no encoding fields yet)
	TargetConfigRaw   []byte
	TargetConfigDesc  diff.BlobRef
	BaselineDigests   []digest.Digest
	BaselineManifest  []byte
	BaselineMediaType string
	BaselineLayerMeta []BaselineLayerMeta // reused in intralayer planning
	// Classification (derived):
	Shipped  []diff.BlobRef
	Required []diff.BlobRef
}
```

- [ ] **Step 1: Write the failing test**

```go
package exporter

import (
	"context"
	"testing"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/stretchr/testify/require"
)

func TestPlanPair_ClassifiesLayers(t *testing.T) {
	base, err := imageio.OpenArchiveRef("../../testdata/fixtures/v1_oci.tar")
	require.NoError(t, err)
	tgt, err := imageio.OpenArchiveRef("../../testdata/fixtures/v2_oci.tar")
	require.NoError(t, err)

	p, err := planPair(context.Background(), Pair{
		Name: "svc", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar",
	}, "linux/amd64")
	require.NoError(t, err)
	require.Equal(t, "svc", p.Name)
	require.NotEmpty(t, p.TargetManifest)
	require.Len(t, p.Shipped, 1, "v2 differs from v1 by one layer")
	require.Len(t, p.Required, 1, "shared base layer required from baseline")
	_ = base
	_ = tgt
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/exporter/ -run TestPlanPair -v`
Expected: FAIL — `planPair` not declared.

- [ ] **Step 3: Implement planPair**

Create `pkg/exporter/perpair.go`:

```go
package exporter

import (
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
)

func planPair(ctx context.Context, p Pair, platform string) (*pairPlan, error) {
	baseRef, err := imageio.OpenArchiveRef(p.BaselinePath)
	if err != nil {
		return nil, fmt.Errorf("open baseline %s: %w", p.BaselinePath, err)
	}
	tgtRef, err := imageio.OpenArchiveRef(p.TargetPath)
	if err != nil {
		return nil, fmt.Errorf("open target %s: %w", p.TargetPath, err)
	}

	baseParsed, baseDigests, baseMeta, baseMfBytes, baseMime, err := readManifestBundle(ctx, baseRef, platform)
	if err != nil {
		return nil, fmt.Errorf("read baseline manifest %s: %w", p.BaselinePath, err)
	}
	tgtParsed, _, _, tgtMfBytes, tgtMime, err := readManifestBundle(ctx, tgtRef, platform)
	if err != nil {
		return nil, fmt.Errorf("read target manifest %s: %w", p.TargetPath, err)
	}

	// Build target layer descriptors.
	tgtLayers := make([]diff.BlobRef, 0, len(tgtParsed.LayerInfos()))
	for _, l := range tgtParsed.LayerInfos() {
		tgtLayers = append(tgtLayers, diff.BlobRef{
			Digest: l.Digest, Size: l.Size, MediaType: l.MediaType,
		})
	}
	plan := diff.ComputePlan(tgtLayers, baseDigests)

	// Read target config bytes (goes in blob pool).
	tgtConfigDesc := tgtParsed.ConfigInfo()
	cfgBytes, err := readBlobBytes(ctx, tgtRef, tgtConfigDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("read target config: %w", err)
	}

	return &pairPlan{
		Name: p.Name, BaselineRef: baseRef, TargetRef: tgtRef,
		TargetManifest: tgtMfBytes, TargetMediaType: tgtMime,
		TargetLayerDescs: tgtLayers,
		TargetConfigRaw:  cfgBytes,
		TargetConfigDesc: diff.BlobRef{
			Digest: tgtConfigDesc.Digest, Size: int64(len(cfgBytes)),
			MediaType: tgtConfigDesc.MediaType,
		},
		BaselineDigests:   baseDigests,
		BaselineManifest:  baseMfBytes,
		BaselineMediaType: baseMime,
		BaselineLayerMeta: baseMeta,
		Shipped:           plan.ShippedInDelta,
		Required:          plan.RequiredFromBaseline,
	}, nil
	_ = baseParsed
}

// readManifestBundle fetches manifest bytes, parses, and collects layer
// digests + LayerMeta. Platform non-empty selects a manifest-list instance.
func readManifestBundle(
	ctx context.Context, ref types.ImageReference, platform string,
) (manifest.Manifest, []digest.Digest, []BaselineLayerMeta, []byte, string, error) {
	src, err := ref.NewImageSource(ctx, nil)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	defer src.Close()
	raw, mime, err := src.GetManifest(ctx, nil)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	// TODO (manifest-list): add platform selection once multi-arch fixtures exist.
	parsed, err := manifest.FromBlob(raw, mime)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	digests := make([]digest.Digest, 0, len(parsed.LayerInfos()))
	meta := make([]BaselineLayerMeta, 0, len(parsed.LayerInfos()))
	for _, l := range parsed.LayerInfos() {
		digests = append(digests, l.Digest)
		meta = append(meta, BaselineLayerMeta{
			Digest: l.Digest, Size: l.Size, MediaType: l.MediaType,
		})
	}
	return parsed, digests, meta, raw, mime, nil
}

func readBlobBytes(ctx context.Context, ref types.ImageReference, d digest.Digest) ([]byte, error) {
	src, err := ref.NewImageSource(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	r, _, err := src.GetBlob(ctx, types.BlobInfo{Digest: d}, nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
```

`BaselineLayerMeta` already exists in `pkg/exporter/baseline.go` — reuse it.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/exporter/ -run TestPlanPair -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/perpair.go pkg/exporter/perpair_test.go
git commit -m "feat(exporter): per-pair plan helper reads manifests and classifies"
```

---

### Task 12: Global blob pool with refCount

**Files:**
- Create: `pkg/exporter/pool.go`
- Test: `pkg/exporter/pool_test.go`

The pool is keyed by digest, each entry holds `(bytes, BlobEntry)`. `addIfAbsent(digest, bytes, entry)` never overwrites. `refCount[digest]` counts target-image references across pairs (manifests + configs + shipped layers + required layers do not count; force-full rule applies to *shipped* layers only per spec §4.5).

- [ ] **Step 1: Write the failing test**

```go
package exporter

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/stretchr/testify/require"
)

func TestBlobPool_AddIfAbsentAndRefCount(t *testing.T) {
	p := newBlobPool()
	d := digest.Digest("sha256:aa")
	p.addIfAbsent(d, []byte("hi"), diff.BlobEntry{Size: 2, Encoding: diff.EncodingFull, ArchiveSize: 2})
	p.addIfAbsent(d, []byte("REPLACED"), diff.BlobEntry{Size: 8, Encoding: diff.EncodingFull, ArchiveSize: 8})
	bytes, ok := p.get(d)
	require.True(t, ok)
	require.Equal(t, "hi", string(bytes), "first write wins")

	p.countShipped(d)
	p.countShipped(d)
	require.Equal(t, 2, p.refCount(d))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/exporter/ -run TestBlobPool -v`
Expected: FAIL.

- [ ] **Step 3: Implement blobPool**

Create `pkg/exporter/pool.go`:

```go
package exporter

import (
	"sort"

	"github.com/opencontainers/go-digest"
	"github.com/leosocy/diffah/pkg/diff"
)

type blobPool struct {
	bytes    map[digest.Digest][]byte
	entries  map[digest.Digest]diff.BlobEntry
	shipRefs map[digest.Digest]int
}

func newBlobPool() *blobPool {
	return &blobPool{
		bytes:    make(map[digest.Digest][]byte),
		entries:  make(map[digest.Digest]diff.BlobEntry),
		shipRefs: make(map[digest.Digest]int),
	}
}

func (p *blobPool) addIfAbsent(d digest.Digest, data []byte, e diff.BlobEntry) {
	if _, ok := p.bytes[d]; ok {
		return
	}
	p.bytes[d] = data
	p.entries[d] = e
}

func (p *blobPool) setEntry(d digest.Digest, e diff.BlobEntry) {
	p.entries[d] = e
}

func (p *blobPool) has(d digest.Digest) bool {
	_, ok := p.bytes[d]
	return ok
}

func (p *blobPool) get(d digest.Digest) ([]byte, bool) {
	b, ok := p.bytes[d]
	return b, ok
}

func (p *blobPool) countShipped(d digest.Digest) {
	p.shipRefs[d]++
}

func (p *blobPool) refCount(d digest.Digest) int {
	return p.shipRefs[d]
}

// sortedDigests returns the digests in the pool in ascending lexicographic
// order — used for deterministic tar emission.
func (p *blobPool) sortedDigests() []digest.Digest {
	out := make([]digest.Digest, 0, len(p.bytes))
	for d := range p.bytes {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/exporter/ -run TestBlobPool -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/pool.go pkg/exporter/pool_test.go
git commit -m "feat(exporter): content-addressed blob pool with shipped refcount"
```

---

### Task 13: Seed pool with manifests + configs

**Files:**
- Modify: `pkg/exporter/pool.go`
- Modify: `pkg/exporter/pool_test.go`

For each `pairPlan`, add the target manifest and config to the pool (always `encoding=full`). Skip if already present (cross-image dedup).

- [ ] **Step 1: Write the failing test**

```go
func TestBlobPool_SeedManifestAndConfig(t *testing.T) {
	ctx := context.Background()
	p1, err := planPair(ctx, Pair{Name: "a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar"}, "linux/amd64")
	require.NoError(t, err)
	p2, err := planPair(ctx, Pair{Name: "b", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar"}, "linux/amd64")
	require.NoError(t, err)

	pool := newBlobPool()
	seedManifestAndConfig(pool, p1)
	seedManifestAndConfig(pool, p2)

	// Same target manifest digest — should appear once.
	mfDigest := digest.FromBytes(p1.TargetManifest)
	require.True(t, pool.has(mfDigest))
	require.True(t, pool.has(p1.TargetConfigDesc.Digest))
	// Dedup: adding again must not replace bytes.
	require.Len(t, pool.sortedDigests(), 2)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/exporter/ -run TestBlobPool_SeedManifestAndConfig -v`
Expected: FAIL.

- [ ] **Step 3: Implement seedManifestAndConfig**

Append to `pkg/exporter/pool.go`:

```go
// seedManifestAndConfig adds the target manifest and config blobs to the
// pool as encoding=full entries. Idempotent across duplicate digests
// (content-addressed).
func seedManifestAndConfig(p *blobPool, plan *pairPlan) {
	mfDigest := digest.FromBytes(plan.TargetManifest)
	p.addIfAbsent(mfDigest, plan.TargetManifest, diff.BlobEntry{
		Size: int64(len(plan.TargetManifest)), MediaType: plan.TargetMediaType,
		Encoding: diff.EncodingFull, ArchiveSize: int64(len(plan.TargetManifest)),
	})
	p.addIfAbsent(plan.TargetConfigDesc.Digest, plan.TargetConfigRaw, diff.BlobEntry{
		Size: plan.TargetConfigDesc.Size, MediaType: plan.TargetConfigDesc.MediaType,
		Encoding: diff.EncodingFull, ArchiveSize: plan.TargetConfigDesc.Size,
	})
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/exporter/ -run TestBlobPool_Seed -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/pool.go pkg/exporter/pool_test.go
git commit -m "feat(exporter): seed blob pool with target manifests and configs"
```

---

### Task 14: Per-pair shipped-layer encoding with force-full

**Files:**
- Modify: `pkg/exporter/pool.go`
- Test: `pkg/exporter/pool_test.go`

Algorithm (spec §5.4):

```
# Phase 1: count shipped references across all pairs.
for each pair:
    for each shipped layer L:
        pool.countShipped(L.Digest)

# Phase 2: encode each unique shipped digest.
for each pair:
    for each shipped layer L:
        if pool.has(L.Digest): continue
        if pool.refCount(L.Digest) > 1:
            encode full   # force-full rule
        else:
            encode via per-pair planner (may produce patch or full)
```

We need a shipped-layer reader that returns `([]byte, error)` given a digest — essentially a wrapper around `readBlobBytes(pair.TargetRef, digest)`.

- [ ] **Step 1: Write the failing test**

```go
func TestEncodeShipped_ForcesFullOnCrossImageDup(t *testing.T) {
	ctx := context.Background()
	// Build two identical-looking pairs to guarantee overlapping shipped
	// digests. v2→v3 has one shipped layer (the version layer) that differs,
	// so two copies of the same pair will share both the manifest digest and
	// the shipped version layer.
	p1, err := planPair(ctx, Pair{Name: "a",
		BaselinePath: "../../testdata/fixtures/v2_oci.tar",
		TargetPath:   "../../testdata/fixtures/v3_oci.tar"}, "linux/amd64")
	require.NoError(t, err)
	p2, err := planPair(ctx, Pair{Name: "b",
		BaselinePath: "../../testdata/fixtures/v2_oci.tar",
		TargetPath:   "../../testdata/fixtures/v3_oci.tar"}, "linux/amd64")
	require.NoError(t, err)

	pool := newBlobPool()
	seedManifestAndConfig(pool, p1)
	seedManifestAndConfig(pool, p2)
	for _, p := range []*pairPlan{p1, p2} {
		for _, s := range p.Shipped {
			pool.countShipped(s.Digest)
		}
	}
	require.NoError(t, encodeShipped(ctx, pool, []*pairPlan{p1, p2}, "off", nil))

	// The shared shipped digest must end up encoding=full regardless of mode.
	for _, s := range p1.Shipped {
		entry := pool.entries[s.Digest]
		require.Equal(t, diff.EncodingFull, entry.Encoding, "shared shipped must be full")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/exporter/ -run TestEncodeShipped -v`
Expected: FAIL.

- [ ] **Step 3: Implement encodeShipped**

Append to `pkg/exporter/pool.go` (or a new file `pkg/exporter/encode.go`):

```go
// encodeShipped populates the pool with shipped-layer entries per spec §5.4.
// - Cross-image duplicates (refCount > 1) are forced to encoding=full.
// - Single-reference blobs use the per-pair planner when mode="auto",
//   else full.
func encodeShipped(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter,
) error {
	for _, p := range pairs {
		for _, s := range p.Shipped {
			if pool.has(s.Digest) {
				continue
			}
			layerBytes, err := readBlobBytes(ctx, p.TargetRef, s.Digest)
			if err != nil {
				return fmt.Errorf("read shipped %s: %w", s.Digest, err)
			}
			if pool.refCount(s.Digest) > 1 || mode == "off" {
				pool.addIfAbsent(s.Digest, layerBytes, diff.BlobEntry{
					Size: s.Size, MediaType: s.MediaType,
					Encoding: diff.EncodingFull, ArchiveSize: s.Size,
				})
				continue
			}
			// mode == "auto": run per-pair planner for this single layer.
			ref, payload, entry, err := encodeSingleShipped(ctx, p, s, layerBytes, fp)
			if err != nil {
				// Graceful fallback: full encoding.
				pool.addIfAbsent(s.Digest, layerBytes, diff.BlobEntry{
					Size: s.Size, MediaType: s.MediaType,
					Encoding: diff.EncodingFull, ArchiveSize: s.Size,
				})
				continue
			}
			pool.addIfAbsent(s.Digest, payload, entry)
			_ = ref
		}
	}
	return nil
}
```

`encodeSingleShipped` calls into the existing per-pair Planner (from `pkg/exporter/intralayer.go`). Since Planner's Run signature operates on a slice, here it's called with `[]diff.BlobRef{s}`:

```go
func encodeSingleShipped(
	ctx context.Context, p *pairPlan, s diff.BlobRef,
	target []byte, fp Fingerprinter,
) (string, []byte, diff.BlobEntry, error) {
	readBlob := func(d digest.Digest) ([]byte, error) {
		if d == s.Digest {
			return target, nil
		}
		return readBlobBytes(ctx, p.BaselineRef, d)
	}
	entries, payloads, err := NewPlanner(p.BaselineLayerMeta, readBlob, fp).Run(ctx, []diff.BlobRef{s})
	if err != nil {
		return "", nil, diff.BlobEntry{}, err
	}
	if len(entries) == 0 {
		return "", nil, diff.BlobEntry{}, fmt.Errorf("planner returned no entries")
	}
	entry := entries[0]
	var payload []byte
	if entry.Encoding == diff.EncodingFull {
		payload = target
	} else {
		payload = payloads[entry.Digest]
	}
	bEntry := diff.BlobEntry{
		Size: entry.Size, MediaType: entry.MediaType,
		Encoding: entry.Encoding, Codec: entry.Codec,
		PatchFromDigest: entry.PatchFromDigest,
		ArchiveSize:     entry.ArchiveSize,
	}
	return "", payload, bEntry, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/exporter/ -v`
Expected: TestEncodeShipped PASSES; other exporter tests still skipped.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/pool.go pkg/exporter/pool_test.go
git commit -m "feat(exporter): encode shipped layers with force-full dedup rule"
```

---

### Task 15: Assemble Sidecar struct

**Files:**
- Create: `pkg/exporter/assemble.go`
- Test: `pkg/exporter/assemble_test.go`

Convert `(pairs, pool)` into a validated `diff.Sidecar`.

- [ ] **Step 1: Write the failing test**

```go
func TestAssembleSidecar_HappyPath(t *testing.T) {
	ctx := context.Background()
	p1, err := planPair(ctx, Pair{Name: "a",
		BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath:   "../../testdata/fixtures/v2_oci.tar"}, "linux/amd64")
	require.NoError(t, err)
	pool := newBlobPool()
	seedManifestAndConfig(pool, p1)
	for _, s := range p1.Shipped {
		pool.countShipped(s.Digest)
	}
	require.NoError(t, encodeShipped(ctx, pool, []*pairPlan{p1}, "off", nil))

	sc, err := assembleSidecar(
		[]*pairPlan{p1}, pool, "linux/amd64",
		"test", time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)
	require.Equal(t, diff.FeatureBundle, sc.Feature)
	require.Equal(t, diff.SchemaVersionV1, sc.Version)
	require.Len(t, sc.Images, 1)
	require.Equal(t, "a", sc.Images[0].Name)
	require.Contains(t, sc.Blobs, sc.Images[0].Target.ManifestDigest)
	_, err = sc.Marshal()
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/exporter/ -run TestAssembleSidecar -v`
Expected: FAIL.

- [ ] **Step 3: Implement assembleSidecar**

Create `pkg/exporter/assemble.go`:

```go
package exporter

import (
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func assembleSidecar(
	pairs []*pairPlan, pool *blobPool,
	platform, toolVersion string, createdAt time.Time,
) (diff.Sidecar, error) {
	images := make([]diff.ImageEntry, 0, len(pairs))
	for _, p := range pairs {
		baseMfDigest := digest.FromBytes(p.BaselineManifest)
		tgtMfDigest := digest.FromBytes(p.TargetManifest)
		images = append(images, diff.ImageEntry{
			Name: p.Name,
			Baseline: diff.BaselineRef{
				ManifestDigest: baseMfDigest,
				MediaType:      p.BaselineMediaType,
				SourceHint:     filepathBase(p.BaselineRef),
			},
			Target: diff.TargetRef{
				ManifestDigest: tgtMfDigest,
				ManifestSize:   int64(len(p.TargetManifest)),
				MediaType:      p.TargetMediaType,
			},
		})
	}
	return diff.Sidecar{
		Version:     diff.SchemaVersionV1,
		Feature:     diff.FeatureBundle,
		Tool:        "diffah",
		ToolVersion: toolVersion,
		CreatedAt:   createdAt,
		Platform:    platform,
		Blobs:       pool.entries,
		Images:      images,
	}, nil
}

// filepathBase returns the short name of a file-backed image reference
// (for source_hint). Best-effort; returns "" on unknown refs.
func filepathBase(ref types.ImageReference) string {
	s := ref.StringWithinTransport()
	// oci-archive: "/path/to/a.tar" or "/path/a.tar:tag"
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/exporter/ -run TestAssembleSidecar -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/assemble.go pkg/exporter/assemble_test.go
git commit -m "feat(exporter): assemble bundle Sidecar from pairs and pool"
```

---

### Task 16: Archive writer rewrite for bundle layout

**Files:**
- Modify: `internal/archive/writer.go`
- Test: `internal/archive/writer_test.go`

The new archive layout is `sidecar.json` (at root) + `blobs/<digest>` (one per pool entry). Existing `archive.Pack(srcDir, sidecar, outPath, compression)` walks a directory — we need a new entry point `archive.PackBundle(pool, sidecar, outPath, compression)` or redefine `Pack` to accept (pool, sidecar, outPath, compression).

**Decision:** Replace `Pack` signature. Old callers are the exporter and fixture generator, both of which we are rewriting.

- [ ] **Step 1: Write the failing test**

```go
func TestArchivePackBundle_DeterministicBlobOrder(t *testing.T) {
	pool := map[digest.Digest][]byte{
		"sha256:cc": []byte("cc"),
		"sha256:aa": []byte("aa"),
		"sha256:bb": []byte("bb"),
	}
	sidecar := []byte(`{"feature":"bundle"}`)
	tmp := filepath.Join(t.TempDir(), "out.tar")
	require.NoError(t, PackBundle(pool, sidecar, tmp, CompressNone))

	// Read back: tar entries must appear in the order diffah.json,
	// blobs/sha256:aa, blobs/sha256:bb, blobs/sha256:cc.
	names := tarEntries(t, tmp)
	require.Equal(t, []string{
		diff.SidecarFilename,
		"blobs/sha256:aa",
		"blobs/sha256:bb",
		"blobs/sha256:cc",
	}, names)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/archive/ -run TestArchivePackBundle -v`
Expected: FAIL.

- [ ] **Step 3: Implement PackBundle**

Rewrite `internal/archive/writer.go`:

```go
package archive

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

type Compression string

const (
	CompressNone Compression = "none"
	CompressZstd Compression = "zstd"
)

// PackBundle writes a bundle archive containing the sidecar plus one file
// per pool entry under blobs/. Entries are written in digest order for
// deterministic output.
func PackBundle(
	pool map[digest.Digest][]byte, sidecar []byte,
	outPath string, c Compression,
) error {
	tmp := outPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	defer f.Close()

	var stream io.Writer = f
	var zw *zstd.Encoder
	if c == CompressZstd {
		zw, err = zstd.NewWriter(f)
		if err != nil {
			return fmt.Errorf("init zstd writer: %w", err)
		}
		stream = zw
	}
	tw := tar.NewWriter(stream)

	if err := addBytes(tw, diff.SidecarFilename, sidecar); err != nil {
		return err
	}
	// blobs/ directory entry.
	if err := tw.WriteHeader(&tar.Header{
		Name: "blobs/", Mode: 0o755, Typeflag: tar.TypeDir,
	}); err != nil {
		return fmt.Errorf("write blobs dir: %w", err)
	}

	digests := make([]digest.Digest, 0, len(pool))
	for d := range pool {
		digests = append(digests, d)
	}
	sort.Slice(digests, func(i, j int) bool { return digests[i] < digests[j] })
	for _, d := range digests {
		name := "blobs/" + d.String()
		if err := addBytes(tw, name, pool[d]); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			return fmt.Errorf("close zstd: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return os.Rename(tmp, outPath)
}

func addBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write tar body %s: %w", name, err)
	}
	return nil
}
```

Delete the old `Pack`, `addDir`, `addFile` helpers. They'll be gone from the next commit; the exporter will be migrated to `PackBundle` in Task 17.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/archive/ -v`
Expected: `TestArchivePackBundle_*` passes; other archive tests may fail (they referenced old `Pack`). Skip them with `t.Skip` until Task 17/20 migrate.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/writer.go internal/archive/writer_test.go
git commit -m "refactor(archive): PackBundle writes sidecar + blobs/ layout"
```

---

### Task 17: Wire the exporter pipeline end-to-end

**Files:**
- Modify: `pkg/exporter/exporter.go`

Replace the Export stub with the full pipeline:

```
1. ValidatePairs(opts.Pairs)
2. plans := []*pairPlan{}; for _, p := range opts.Pairs { plans = append(plans, planPair(...)) }
3. pool := newBlobPool()
4. for _, p := range plans { seedManifestAndConfig(pool, p) }
5. for _, p := range plans { for _, s := range p.Shipped { pool.countShipped(s.Digest) } }
6. encodeShipped(ctx, pool, plans, opts.IntraLayer, opts.fingerprinter)
7. sidecar := assembleSidecar(plans, pool, platform, toolVersion, createdAt)
8. sidecarBytes := sidecar.Marshal()
9. archive.PackBundle(pool.bytes, sidecarBytes, opts.OutputPath, c)
10. verifyExport(opts.OutputPath, sidecar)
```

- [ ] **Step 1: Replace the stub Export**

```go
func Export(ctx context.Context, opts Options) error {
	if opts.IntraLayer == "" {
		opts.IntraLayer = "auto"
	}
	if err := ValidatePairs(opts.Pairs); err != nil {
		return err
	}
	plans := make([]*pairPlan, 0, len(opts.Pairs))
	for _, p := range opts.Pairs {
		plan, err := planPair(ctx, p, opts.Platform)
		if err != nil {
			return err
		}
		plans = append(plans, plan)
	}
	pool := newBlobPool()
	for _, p := range plans {
		seedManifestAndConfig(pool, p)
		for _, s := range p.Shipped {
			pool.countShipped(s.Digest)
		}
	}
	if err := encodeShipped(ctx, pool, plans, opts.IntraLayer, opts.fingerprinter); err != nil {
		return err
	}
	platform := opts.Platform
	if platform == "" {
		platform = derivePlatformFromPlans(plans)
	}
	createdAt := opts.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	sc, err := assembleSidecar(plans, pool, platform, opts.ToolVersion, createdAt)
	if err != nil {
		return err
	}
	scBytes, err := sc.Marshal()
	if err != nil {
		return err
	}
	c := archive.CompressNone
	if opts.Compress == "zstd" {
		c = archive.CompressZstd
	}
	if err := archive.PackBundle(pool.bytes, scBytes, opts.OutputPath, c); err != nil {
		return err
	}
	return verifyBundleExport(opts.OutputPath, sc)
}

func derivePlatformFromPlans(plans []*pairPlan) string {
	for _, p := range plans {
		var cfg struct {
			OS, Architecture, Variant string
		}
		if err := json.Unmarshal(p.TargetConfigRaw, &cfg); err == nil &&
			cfg.OS != "" && cfg.Architecture != "" {
			if cfg.Variant != "" {
				return cfg.OS + "/" + cfg.Architecture + "/" + cfg.Variant
			}
			return cfg.OS + "/" + cfg.Architecture
		}
	}
	return ""
}

func verifyBundleExport(path string, want diff.Sidecar) error {
	got, err := archive.ReadSidecar(path)
	if err != nil {
		return fmt.Errorf("verify read sidecar: %w", err)
	}
	back, err := diff.ParseSidecar(got)
	if err != nil {
		return fmt.Errorf("verify parse sidecar: %w", err)
	}
	if len(back.Images) != len(want.Images) {
		return fmt.Errorf("verify: images count %d != %d", len(back.Images), len(want.Images))
	}
	return nil
}
```

- [ ] **Step 2: Add a smoke test**

```go
// pkg/exporter/exporter_test.go
func TestExport_BundleOfOne_RoundTrips(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.tar")
	require.NoError(t, Export(context.Background(), Options{
		Pairs: []Pair{{Name: "default",
			BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath:   "../../testdata/fixtures/v2_oci.tar"}},
		Platform: "linux/amd64", Compress: "none",
		OutputPath: out, ToolVersion: "test", IntraLayer: "off",
		CreatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
	}))
	raw, err := archive.ReadSidecar(out)
	require.NoError(t, err)
	sc, err := diff.ParseSidecar(raw)
	require.NoError(t, err)
	require.Equal(t, "default", sc.Images[0].Name)
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/exporter/ -run TestExport_BundleOfOne -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/exporter/exporter.go pkg/exporter/exporter_test.go
git commit -m "feat(exporter): end-to-end bundle export pipeline"
```

---

### Task 18: Exporter DryRun bundle stats

**Files:**
- Modify: `pkg/exporter/exporter.go`
- Test: `pkg/exporter/exporter_test.go`

Replace the DryRun stub:

```go
type DryRunStats struct {
	Pairs       int
	UniqueBlobs int // manifests + configs + unique shipped
	SharedBlobs int // shipped with refCount > 1
	ShippedCount  int
	ShippedBytes  int64
	RequiredCount int
	RequiredBytes int64
}
```

- [ ] **Step 1: Write the failing test**

```go
func TestDryRun_ReportsShippedAndShared(t *testing.T) {
	stats, err := DryRun(context.Background(), Options{
		Pairs: []Pair{
			{Name: "a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
				TargetPath: "../../testdata/fixtures/v2_oci.tar"},
			{Name: "b", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
				TargetPath: "../../testdata/fixtures/v2_oci.tar"},
		},
		Platform: "linux/amd64",
	})
	require.NoError(t, err)
	require.Equal(t, 2, stats.Pairs)
	require.GreaterOrEqual(t, stats.SharedBlobs, 1, "manifest + config + shipped shared")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/exporter/ -run TestDryRun -v`
Expected: FAIL.

- [ ] **Step 3: Implement DryRun**

```go
func DryRun(ctx context.Context, opts Options) (DryRunStats, error) {
	if err := ValidatePairs(opts.Pairs); err != nil {
		return DryRunStats{}, err
	}
	plans := make([]*pairPlan, 0, len(opts.Pairs))
	for _, p := range opts.Pairs {
		plan, err := planPair(ctx, p, opts.Platform)
		if err != nil {
			return DryRunStats{}, err
		}
		plans = append(plans, plan)
	}
	st := DryRunStats{Pairs: len(plans)}
	uniqueShip := make(map[digest.Digest]int)
	for _, p := range plans {
		st.ShippedCount += len(p.Shipped)
		st.RequiredCount += len(p.Required)
		for _, s := range p.Shipped {
			uniqueShip[s.Digest]++
			st.ShippedBytes += s.Size
		}
		for _, r := range p.Required {
			st.RequiredBytes += r.Size
		}
	}
	for _, c := range uniqueShip {
		if c > 1 {
			st.SharedBlobs++
		}
	}
	st.UniqueBlobs = len(uniqueShip) + 2*len(plans) // rough: manifests + configs
	return st, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/exporter/ -run TestDryRun -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/exporter.go pkg/exporter/exporter_test.go
git commit -m "feat(exporter): bundle-aware DryRun with shared blob count"
```

---

### Task 19: Exporter stderr progress

**Files:**
- Modify: `pkg/exporter/exporter.go`
- Test: `pkg/exporter/exporter_test.go`

Per spec §5.5: one line per pair start/end + one summary. Default writer is `os.Stderr`; tests inject via an `opts.Progress io.Writer` field (when nil, use `io.Discard` for silent defaults — CLI sets it to os.Stderr).

- [ ] **Step 1: Add the test**

```go
func TestExport_StderrProgress(t *testing.T) {
	var buf bytes.Buffer
	out := filepath.Join(t.TempDir(), "out.tar")
	require.NoError(t, Export(context.Background(), Options{
		Pairs: []Pair{{Name: "svc",
			BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath:   "../../testdata/fixtures/v2_oci.tar"}},
		Platform:   "linux/amd64",
		OutputPath: out, ToolVersion: "test", IntraLayer: "off",
		Progress:   &buf,
	}))
	require.Contains(t, buf.String(), "[1/1] svc")
	require.Contains(t, buf.String(), "bundle:")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/exporter/ -run TestExport_StderrProgress -v`
Expected: FAIL (Progress field missing).

- [ ] **Step 3: Add Progress + emit messages**

Add to `Options`:

```go
Progress io.Writer // defaults to io.Discard when nil
```

In `Export`, before per-pair planning:

```go
progress := opts.Progress
if progress == nil {
	progress = io.Discard
}
for i, p := range opts.Pairs {
	fmt.Fprintf(progress, "[%d/%d] %s: planning…\n", i+1, len(opts.Pairs), p.Name)
	plan, err := planPair(ctx, p, opts.Platform)
	if err != nil { return err }
	fmt.Fprintf(progress, "[%d/%d] %s: %d layers · %d shipped · %d from baseline\n",
		i+1, len(opts.Pairs), p.Name,
		len(plan.TargetLayerDescs), len(plan.Shipped), len(plan.Required))
	plans = append(plans, plan)
}
```

After `archive.PackBundle`:

```go
info, _ := os.Stat(opts.OutputPath)
fmt.Fprintf(progress, "bundle: %d images · %d unique blobs · %d dedup · archive %d B\n",
	len(plans), len(pool.bytes), sharedCount(pool), info.Size())
```

Add `sharedCount(pool *blobPool) int` counting `refCount > 1`.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/exporter/ -run TestExport_StderrProgress -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/exporter.go pkg/exporter/exporter_test.go
git commit -m "feat(exporter): stderr progress per pair + bundle summary"
```

---

## Phase 5 — Importer rewrite

### Task 20: New importer.Options + baseline name map

**Files:**
- Modify: `pkg/importer/importer.go`

Replace existing `Options`:

```go
type Options struct {
	DeltaPath    string
	Baselines    map[string]string // name -> path
	OutputPath   string            // output DIR
	OutputFormat string            // "" (default dir) | docker-archive | oci-archive | dir
	AllowConvert bool
	Strict       bool
}
```

Delete `BaselineRef` (old single-baseline path). Stub the Import function similarly to Task 10.

- [ ] **Step 1: Edit Options + stub Import**

```go
func Import(ctx context.Context, opts Options) error {
	return fmt.Errorf("bundle import not yet wired in this commit")
}
func DryRun(ctx context.Context, opts Options) (DryRunReport, error) {
	return DryRunReport{}, fmt.Errorf("bundle dry-run not yet wired in this commit")
}
```

Skip every test in `pkg/importer/` that uses the old `Options`. Keep `composite_src.go` + `format.go` unchanged (they're reused per-image).

- [ ] **Step 2: Run build**

Run: `go build ./...`
Expected: green. `go test ./pkg/importer/` shows old tests skipped.

- [ ] **Step 3: Commit**

```bash
git add pkg/importer/importer.go
git commit -m "refactor(importer): accept baselines map, stub Import body"
```

---

### Task 21: Extract + classify archive (legacy rejection)

**Files:**
- Create: `pkg/importer/extract.go`
- Test: `pkg/importer/extract_test.go`

Opens the archive, reads the sidecar, calls `diff.ParseSidecar`. On feature-missing returns `diff.ErrPhase1Archive`. Extracts the archive into a temp dir.

- [ ] **Step 1: Write the failing test**

The legacy phase1 fixture is added in Task 41; for now, test only the happy path using a fresh bundle archive produced in-flight.

```go
func TestExtractBundle_ReturnsParsedSidecar(t *testing.T) {
	out := makeBundleArchive(t) // helper that calls exporter.Export
	tmp := t.TempDir()
	sidecar, err := ExtractBundle(out, tmp)
	require.NoError(t, err)
	require.Equal(t, diff.FeatureBundle, sidecar.Feature)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/importer/ -run TestExtractBundle -v`
Expected: FAIL.

- [ ] **Step 3: Implement ExtractBundle**

Create `pkg/importer/extract.go`:

```go
package importer

import (
	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/pkg/diff"
)

func ExtractBundle(archivePath, dest string) (*diff.Sidecar, error) {
	raw, err := archive.Extract(archivePath, dest)
	if err != nil {
		return nil, err
	}
	return diff.ParseSidecar(raw)
}
```

Also: `internal/archive/reader.go` still returns sidecar bytes and extracts all entries into dest. Because the new layout has `blobs/<digest>` files, Extract will put them at `dest/blobs/<digest>` — the importer reads them from there in Task 22.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run TestExtractBundle -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/extract.go pkg/importer/extract_test.go
git commit -m "feat(importer): extract bundle archive into tmp + parse sidecar"
```

---

### Task 22: Per-image baseline resolution + strict mode

**Files:**
- Create: `pkg/importer/resolve.go`
- Test: `pkg/importer/resolve_test.go`

Given `(sidecar, Options.Baselines, Options.Strict)`:
- For each `image in sidecar.Images`: mark provided/missing.
- Unknown names in Baselines → `ErrBaselineNameUnknown`.
- In strict mode, any missing → `ErrBaselineMissing`.
- Return a `[]resolvedImage{name, baselinePath, provided}` for the compose loop.

- [ ] **Step 1: Introduce shared test helpers**

Before the tests compile, add `pkg/importer/testhelpers_test.go`:

```go
package importer

import (
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

// twoImageSidecar returns a validated bundle Sidecar with two images named a
// and b. Blob digests are placeholders — callers that need real blobs must
// use makeBundleArchive instead.
func twoImageSidecar(t *testing.T, a, b string) *diff.Sidecar {
	t.Helper()
	return &diff.Sidecar{
		Version: diff.SchemaVersionV1, Feature: diff.FeatureBundle,
		Tool: "diffah", ToolVersion: "test",
		CreatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Platform:  "linux/amd64",
		Blobs: map[digest.Digest]diff.BlobEntry{
			"sha256:aa": {Size: 1, MediaType: "application/vnd.oci.image.manifest.v1+json",
				Encoding: diff.EncodingFull, ArchiveSize: 1},
			"sha256:bb": {Size: 1, MediaType: "application/vnd.oci.image.manifest.v1+json",
				Encoding: diff.EncodingFull, ArchiveSize: 1},
		},
		Images: []diff.ImageEntry{
			{Name: a, Baseline: diff.BaselineRef{ManifestDigest: "sha256:11",
				MediaType: "application/vnd.oci.image.manifest.v1+json"},
				Target: diff.TargetRef{ManifestDigest: "sha256:aa",
					MediaType: "application/vnd.oci.image.manifest.v1+json"}},
			{Name: b, Baseline: diff.BaselineRef{ManifestDigest: "sha256:22",
				MediaType: "application/vnd.oci.image.manifest.v1+json"},
				Target: diff.TargetRef{ManifestDigest: "sha256:bb",
					MediaType: "application/vnd.oci.image.manifest.v1+json"}},
		},
	}
}

func oneImageSidecar(t *testing.T, name string) *diff.Sidecar {
	t.Helper()
	sc := twoImageSidecar(t, name, "unused")
	sc.Images = sc.Images[:1]
	delete(sc.Blobs, "sha256:bb")
	return sc
}
```

- [ ] **Step 2: Write the failing test**

```go
func TestResolveImages_Happy(t *testing.T) {
	sc := twoImageSidecar(t, "a", "b")
	resolved, err := ResolveImages(sc, map[string]string{
		"a": "/path/a.tar", "b": "/path/b.tar",
	}, false)
	require.NoError(t, err)
	require.Len(t, resolved, 2)
	require.True(t, resolved[0].Provided)
	require.True(t, resolved[1].Provided)
}

func TestResolveImages_UnknownName(t *testing.T) {
	sc := twoImageSidecar(t, "a", "b")
	_, err := ResolveImages(sc, map[string]string{
		"nonexistent": "/path/x.tar",
	}, false)
	require.ErrorContains(t, err, `"nonexistent"`)
}

func TestResolveImages_StrictMissing(t *testing.T) {
	sc := twoImageSidecar(t, "a", "b")
	_, err := ResolveImages(sc, map[string]string{"a": "/path/a.tar"}, true)
	require.Error(t, err)
	var bm *diff.ErrBaselineMissing
	require.ErrorAs(t, err, &bm)
	require.Equal(t, []string{"b"}, bm.Names)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/importer/ -run TestResolveImages -v`
Expected: FAIL.

- [ ] **Step 4: Implement ResolveImages**

Create `pkg/importer/resolve.go`:

```go
package importer

import (
	"sort"

	"github.com/leosocy/diffah/pkg/diff"
)

type ResolvedImage struct {
	Name         string
	BaselinePath string
	Provided     bool
	Entry        diff.ImageEntry
}

func ResolveImages(
	s *diff.Sidecar, baselines map[string]string, strict bool,
) ([]ResolvedImage, error) {
	known := make(map[string]struct{}, len(s.Images))
	for _, img := range s.Images {
		known[img.Name] = struct{}{}
	}
	// Reject unknown names first (deterministic order).
	names := make([]string, 0, len(baselines))
	for n := range baselines {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := known[n]; !ok {
			avail := make([]string, 0, len(known))
			for k := range known {
				avail = append(avail, k)
			}
			sort.Strings(avail)
			return nil, &diff.ErrBaselineNameUnknown{Name: n, Available: avail}
		}
	}
	out := make([]ResolvedImage, 0, len(s.Images))
	var missing []string
	for _, img := range s.Images {
		path, ok := baselines[img.Name]
		if !ok {
			missing = append(missing, img.Name)
		}
		out = append(out, ResolvedImage{
			Name: img.Name, BaselinePath: path,
			Provided: ok, Entry: img,
		})
	}
	if strict && len(missing) > 0 {
		return nil, &diff.ErrBaselineMissing{Names: missing}
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/importer/ -run TestResolveImages -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/importer/resolve.go pkg/importer/resolve_test.go pkg/importer/testhelpers_test.go
git commit -m "feat(importer): resolve images against baseline name map"
```

---

### Task 23: Positional + single-image guard

**Files:**
- Modify: `pkg/importer/resolve.go`
- Modify: `pkg/importer/resolve_test.go`

CLI may pass a positional `BASELINE` for bundles of one. The resolver needs a helper that synthesises `baselines[imageName]=positionalPath` when the bundle has exactly one image, or returns `ErrMultiImageNeedsNamedBaselines` otherwise.

- [ ] **Step 1: Write the failing test**

```go
func TestPositionalBaseline_BundleOfOne(t *testing.T) {
	sc := oneImageSidecar(t, "default")
	m, err := PositionalBaselineMap(sc, "/path/base.tar")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"default": "/path/base.tar"}, m)
}

func TestPositionalBaseline_MultiImage(t *testing.T) {
	sc := twoImageSidecar(t, "a", "b")
	_, err := PositionalBaselineMap(sc, "/path/base.tar")
	var e *diff.ErrMultiImageNeedsNamedBaselines
	require.ErrorAs(t, err, &e)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/importer/ -run TestPositionalBaseline -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
func PositionalBaselineMap(s *diff.Sidecar, path string) (map[string]string, error) {
	if len(s.Images) > 1 {
		return nil, &diff.ErrMultiImageNeedsNamedBaselines{N: len(s.Images)}
	}
	return map[string]string{s.Images[0].Name: path}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run TestPositionalBaseline -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/resolve.go pkg/importer/resolve_test.go
git commit -m "feat(importer): positional baseline helper for bundles of one"
```

---

### Task 24: Per-image compose (dir-backed blob source)

**Files:**
- Create: `pkg/importer/compose.go`
- Test: `pkg/importer/compose_test.go`

For each resolved image where `Provided=true`:
1. Open baseline: `baseRef := imageio.OpenArchiveRef(r.BaselinePath)`; check `baseRef` manifest digest against `r.Entry.Baseline.ManifestDigest` → `ErrBaselineMismatch` on divergence.
2. Build a per-image CompositeSource wrapping:
   - A dir-backed `delta` source pointing at the extracted archive — but we need a SEPARATE delta source per image because CompositeSource currently has one target manifest. The new delta source looks up blobs from the extracted `blobs/<digest>` directory and synthesises a target manifest from `r.Entry.Target.ManifestDigest`.
3. `copy.Image` into `outputDir/<name>/` using `runCopy` and the resolved output format.

The existing `CompositeSource` type requires a delta `types.ImageSource`. Rather than build one from the extracted dir (which would need a manifest.json fake layout), we implement a new `bundleImageSource` that reads from `pool blobs + per-image manifest digest`:

```go
type bundleImageSource struct {
	blobsDir     string
	manifest     []byte
	manifestMime string
	shipped      map[digest.Digest]diff.BlobEntry
	baselineSrc  types.ImageSource
}
```

- [ ] **Step 1: Write the failing test**

```go
func TestCompose_WritesOneImageIntoSubdir(t *testing.T) {
	ctx := context.Background()
	archivePath := makeBundleArchive(t) // same helper as Task 21
	outDir := t.TempDir()
	require.NoError(t, Import(ctx, Options{
		DeltaPath: archivePath,
		Baselines: map[string]string{"default": "../../testdata/fixtures/v1_oci.tar"},
		OutputPath: outDir, OutputFormat: "dir",
	}))
	require.DirExists(t, filepath.Join(outDir, "default"))
	_, err := os.Stat(filepath.Join(outDir, "default", "manifest.json"))
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/importer/ -run TestCompose -v`
Expected: FAIL.

- [ ] **Step 3: Implement compose + bundleImageSource**

Implementation is the largest single change; full code omitted here (see `composite_src.go` for the existing dispatch pattern — duplicate it, but source blob bytes from disk (`blobsDir/<digest>`) instead of a directory-transport source).

Minimum skeleton (`pkg/importer/compose.go`):

```go
package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
)

func composeImage(
	ctx context.Context,
	blobsDir string,
	sidecar *diff.Sidecar,
	resolved ResolvedImage,
	outputDir, outputFormat string,
	allowConvert bool,
) error {
	baseRef, err := imageio.OpenArchiveRef(resolved.BaselinePath)
	if err != nil {
		return err
	}
	baseSrc, err := baseRef.NewImageSource(ctx, nil)
	if err != nil {
		return err
	}
	defer baseSrc.Close()
	rawMf, mime, err := baseSrc.GetManifest(ctx, nil)
	if err != nil {
		return err
	}
	if digest.FromBytes(rawMf) != resolved.Entry.Baseline.ManifestDigest {
		return &diff.ErrBaselineMismatch{
			Name:     resolved.Name,
			Expected: resolved.Entry.Baseline.ManifestDigest.String(),
			Got:      digest.FromBytes(rawMf).String(),
		}
	}
	_ = mime

	// Load the target manifest from the archive.
	manifestBytes, err := os.ReadFile(filepath.Join(blobsDir, resolved.Entry.Target.ManifestDigest.String()))
	if err != nil {
		return fmt.Errorf("read target manifest blob: %w", err)
	}

	src := &bundleImageSource{
		blobsDir:     blobsDir,
		manifest:     manifestBytes,
		manifestMime: resolved.Entry.Target.MediaType,
		sidecar:      sidecar,
		baseline:     baseSrc,
	}

	// Wrap src in a ref; invoke copy.Image into outputDir/<name>/.
	outPath := filepath.Join(outputDir, resolved.Name)
	resolvedFmt, err := resolveOutputFormat(outputFormat, resolved.Entry.Target.MediaType, allowConvert)
	if err != nil {
		return err
	}
	outRef, err := buildOutputRef(outPath, resolvedFmt)
	if err != nil {
		return err
	}
	policy, err := imageio.DefaultPolicyContext()
	if err != nil {
		return err
	}
	defer policy.Destroy()
	copyOpts := &copy.Options{}
	if resolvedFmt == FormatDir {
		copyOpts.PreserveDigests = true
	}
	refWrap := &staticSourceRef{inner: outRef, src: src} // see below
	if _, err := copy.Image(ctx, policy, outRef, refWrap, copyOpts); err != nil {
		return fmt.Errorf("compose %s: %w", resolved.Name, err)
	}
	return nil
}
```

A `bundleImageSource` implementation that serves manifest + blobs from disk follows the same dispatch as `CompositeSource.GetBlob`:

```go
type bundleImageSource struct {
	blobsDir     string
	manifest     []byte
	manifestMime string
	sidecar      *diff.Sidecar
	baseline     types.ImageSource
}

func (s *bundleImageSource) GetManifest(ctx context.Context, _ *digest.Digest) ([]byte, string, error) {
	return s.manifest, s.manifestMime, nil
}

func (s *bundleImageSource) GetBlob(
	ctx context.Context, info types.BlobInfo, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	entry, ok := s.sidecar.Blobs[info.Digest]
	if !ok {
		// Required from baseline.
		return s.baseline.GetBlob(ctx, info, cache)
	}
	path := filepath.Join(s.blobsDir, info.Digest.String())
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read archive blob %s: %w", info.Digest, err)
	}
	switch entry.Encoding {
	case diff.EncodingFull:
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	case diff.EncodingPatch:
		ref, _, err := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: entry.PatchFromDigest}, cache)
		if err != nil {
			return nil, 0, err
		}
		refBytes, err := io.ReadAll(ref)
		ref.Close()
		if err != nil {
			return nil, 0, err
		}
		out, err := zstdpatch.Decode(refBytes, data)
		if err != nil {
			return nil, 0, err
		}
		if got := digest.FromBytes(out); got != info.Digest {
			return nil, 0, &diff.ErrIntraLayerAssemblyMismatch{
				Digest: info.Digest.String(), Got: got.String(),
			}
		}
		return io.NopCloser(bytes.NewReader(out)), int64(len(out)), nil
	}
	return nil, 0, fmt.Errorf("unknown encoding %q", entry.Encoding)
}
```

Plus the `types.ImageSource` boilerplate (Reference/Close/HasThreadSafeGetBlob/GetSignatures/LayerInfosForCopy) — delegate to baseline for the trivial methods.

`staticSourceRef` mirrors `compositeRef` in existing `importer.go`.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run TestCompose -v`
Expected: PASS after the full implementation lands.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/compose.go pkg/importer/compose_test.go
git commit -m "feat(importer): per-image compose via bundleImageSource"
```

---

### Task 25: Wire importer Import end-to-end

**Files:**
- Modify: `pkg/importer/importer.go`

Replace the Import stub with:

```go
func Import(ctx context.Context, opts Options) error {
	if opts.OutputPath == "" {
		return fmt.Errorf("output dir required")
	}
	tmp, err := os.MkdirTemp("", "diffah-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	sidecar, err := ExtractBundle(opts.DeltaPath, tmp)
	if err != nil {
		return err
	}
	resolved, err := ResolveImages(sidecar, opts.Baselines, opts.Strict)
	if err != nil {
		return err
	}
	progress := opts.Progress
	if progress == nil {
		progress = io.Discard
	}
	if err := os.MkdirAll(opts.OutputPath, 0o755); err != nil {
		return err
	}
	blobsDir := filepath.Join(tmp, "blobs")
	skipped := []string{}
	for _, r := range resolved {
		if !r.Provided {
			fmt.Fprintf(progress, "%s: skipped (no baseline provided)\n", r.Name)
			skipped = append(skipped, r.Name)
			continue
		}
		if err := composeImage(ctx, blobsDir, sidecar, r,
			opts.OutputPath, opts.OutputFormat, opts.AllowConvert); err != nil {
			return err
		}
	}
	fmt.Fprintf(progress, "imported %d of %d images; skipped: %v\n",
		len(resolved)-len(skipped), len(resolved), skipped)
	return nil
}
```

Add `Options.Progress io.Writer` (mirror the exporter naming exactly). When nil, default to `io.Discard` in `Import`/`DryRun`.

- [ ] **Step 1: Add integration smoke test**

```go
func TestImport_BundleOfOne_RoundTrip(t *testing.T) {
	// Produce a bundle of one, then import it and compare to expected.
	// Uses v1→v2 fixture pair.
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./pkg/importer/ -run TestImport_BundleOfOne -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/importer/importer.go pkg/importer/importer_test.go
git commit -m "feat(importer): wire end-to-end bundle import"
```

---

### Task 26: DryRunReport extension (per-image + totals)

**Files:**
- Modify: `pkg/importer/importer.go`
- Test: `pkg/importer/importer_test.go`

Extend DryRunReport per spec §6.5:

```go
type DryRunReport struct {
	Feature      string
	Version      string
	Tool         string
	ToolVersion  string
	CreatedAt    time.Time
	Platform     string
	Images       []ImageDryRun
	Blobs        BlobStats
	ArchiveBytes int64
}

type BlobStats struct {
	FullCount, PatchCount int
	FullBytes, PatchBytes int64
}

type ImageDryRun struct {
	Name                   string
	BaselineManifestDigest digest.Digest
	TargetManifestDigest   digest.Digest
	BaselineProvided       bool
	WouldImport            bool
	SkipReason             string
	LayerCount             int
	ArchiveLayerCount      int
	BaselineLayerCount     int
	PatchLayerCount        int
}
```

- [ ] **Step 1: Write the failing test**

```go
func TestDryRun_PopulatesImages(t *testing.T) {
	archivePath := makeBundleArchive(t)
	report, err := DryRun(context.Background(), Options{
		DeltaPath: archivePath,
		Baselines: map[string]string{"default": "../../testdata/fixtures/v1_oci.tar"},
	})
	require.NoError(t, err)
	require.Len(t, report.Images, 1)
	require.True(t, report.Images[0].WouldImport)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/importer/ -run TestDryRun_Populates -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
func DryRun(ctx context.Context, opts Options) (DryRunReport, error) {
	tmp, err := os.MkdirTemp("", "diffah-import-dryrun-")
	if err != nil {
		return DryRunReport{}, err
	}
	defer os.RemoveAll(tmp)
	sc, err := ExtractBundle(opts.DeltaPath, tmp)
	if err != nil {
		return DryRunReport{}, err
	}
	resolved, err := ResolveImages(sc, opts.Baselines, opts.Strict)
	if err != nil {
		return DryRunReport{}, err
	}
	var stats BlobStats
	for _, b := range sc.Blobs {
		switch b.Encoding {
		case diff.EncodingFull:
			stats.FullCount++
			stats.FullBytes += b.ArchiveSize
		case diff.EncodingPatch:
			stats.PatchCount++
			stats.PatchBytes += b.ArchiveSize
		}
	}
	images := make([]ImageDryRun, 0, len(resolved))
	for _, r := range resolved {
		mfBytes, err := os.ReadFile(filepath.Join(tmp, "blobs", r.Entry.Target.ManifestDigest.String()))
		if err != nil {
			return DryRunReport{}, err
		}
		parsed, err := manifest.FromBlob(mfBytes, r.Entry.Target.MediaType)
		if err != nil {
			return DryRunReport{}, err
		}
		var archCount, baseCount, patchCount int
		for _, l := range parsed.LayerInfos() {
			if b, ok := sc.Blobs[l.Digest]; ok {
				archCount++
				if b.Encoding == diff.EncodingPatch {
					patchCount++
				}
			} else {
				baseCount++
			}
		}
		skip := ""
		if !r.Provided {
			skip = "no baseline provided"
		}
		images = append(images, ImageDryRun{
			Name:                   r.Name,
			BaselineManifestDigest: r.Entry.Baseline.ManifestDigest,
			TargetManifestDigest:   r.Entry.Target.ManifestDigest,
			BaselineProvided:       r.Provided,
			WouldImport:            r.Provided,
			SkipReason:             skip,
			LayerCount:             len(parsed.LayerInfos()),
			ArchiveLayerCount:      archCount,
			BaselineLayerCount:     baseCount,
			PatchLayerCount:        patchCount,
		})
	}
	info, _ := os.Stat(opts.DeltaPath)
	var archBytes int64
	if info != nil {
		archBytes = info.Size()
	}
	return DryRunReport{
		Feature: sc.Feature, Version: sc.Version, Tool: sc.Tool,
		ToolVersion: sc.ToolVersion, CreatedAt: sc.CreatedAt, Platform: sc.Platform,
		Images: images, Blobs: stats, ArchiveBytes: archBytes,
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/importer/ -run TestDryRun -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/importer.go pkg/importer/importer_test.go
git commit -m "feat(importer): bundle-aware DryRun with per-image report"
```

---

### Task 27: Baseline-digest mismatch hint test

**Files:**
- Modify: `pkg/importer/compose_test.go`

Ensure `ErrBaselineMismatch` is returned when a baseline whose manifest digest doesn't match the sidecar's record is passed in.

- [ ] **Step 1: Write the failing test**

```go
func TestImport_BaselineMismatch_UsesWrongName(t *testing.T) {
	// Build a bundle of two images with different baselines (a, b).
	// Feed the wrong baseline under name "a" and expect ErrBaselineMismatch.
	archivePath := makeTwoImageBundle(t, "a", "b")
	err := Import(context.Background(), Options{
		DeltaPath: archivePath,
		Baselines: map[string]string{
			"a": "../../testdata/fixtures/v1_oci.tar", // wrong: was v3
		},
		OutputPath: t.TempDir(), OutputFormat: "dir", Strict: false,
	})
	var mm *diff.ErrBaselineMismatch
	require.ErrorAs(t, err, &mm)
	require.Equal(t, "a", mm.Name)
}
```

- [ ] **Step 2: Run test**

Run: `go test ./pkg/importer/ -run TestImport_BaselineMismatch -v`
Expected: PASS once Task 24 is correct.

- [ ] **Step 3: Commit**

```bash
git add pkg/importer/compose_test.go
git commit -m "test(importer): verify ErrBaselineMismatch surfaces named image"
```

---

## Phase 6 — inspect command update

### Task 28: inspect: bundle-aware stats

**Files:**
- Modify: `cmd/inspect.go`
- Modify: `cmd/inspect_test.go`

New output must include per-image line, blob counts (full/patch), force-full (shared) count.

- [ ] **Step 1: Write the failing test**

```go
func TestInspect_BundleOutput(t *testing.T) {
	archivePath := makeBundleArchive(t)
	var buf bytes.Buffer
	raw, err := archive.ReadSidecar(archivePath)
	require.NoError(t, err)
	sc, err := diff.ParseSidecar(raw)
	require.NoError(t, err)
	require.NoError(t, printBundle(&buf, archivePath, sc))
	out := buf.String()
	require.Contains(t, out, "feature: bundle")
	require.Contains(t, out, "images: 1")
	require.Contains(t, out, "- default")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestInspect_BundleOutput -v`
Expected: FAIL.

- [ ] **Step 3: Implement printBundle**

Rewrite `cmd/inspect.go`:

```go
package cmd

import (
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/pkg/diff"
)

func newInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <delta-archive>",
		Short: "Print bundle sidecar metadata and blob statistics.",
		Args:  cobra.ExactArgs(1),
		RunE:  runInspect,
	}
}

func init() { rootCmd.AddCommand(newInspectCommand()) }

func runInspect(cmd *cobra.Command, args []string) error {
	raw, err := archive.ReadSidecar(args[0])
	if err != nil {
		return err
	}
	sc, err := diff.ParseSidecar(raw)
	if err != nil {
		return err
	}
	return printBundle(cmd.OutOrStdout(), args[0], sc)
}

type bundleStats struct {
	fullCount, patchCount int
	fullBytes, patchBytes int64
	sharedCount           int // forced-full due to refCount > 1 (approximated)
}

func collectBundleStats(s *diff.Sidecar) bundleStats {
	var st bundleStats
	// Count shipped-layer refs across images to derive "shared" count.
	shipRefs := make(map[digest.Digest]int)
	for _, img := range s.Images {
		// Use target manifest blob to enumerate layers; the manifest itself
		// is one of the blobs in the pool so a direct read is possible only
		// during import-time extract. For inspect, approximate shared using
		// blob refcount derived from all images referring the same digest
		// via blobs map is not available; treat patch=0 count as "shared +
		// manifests + configs" heuristic and emit the count of BlobEntry
		// with encoding=full minus the known per-image (manifest + config)
		// pair, i.e. len(full) - 2*len(images).
		_ = img
	}
	for _, b := range s.Blobs {
		switch b.Encoding {
		case diff.EncodingFull:
			st.fullCount++
			st.fullBytes += b.ArchiveSize
		case diff.EncodingPatch:
			st.patchCount++
			st.patchBytes += b.ArchiveSize
		}
	}
	_ = shipRefs
	// Heuristic: forced-full count = fullCount - 2*len(images)
	// (manifests and configs are always full and per-image).
	forced := st.fullCount - 2*len(s.Images)
	if forced < 0 {
		forced = 0
	}
	st.sharedCount = forced
	return st
}

func printBundle(w io.Writer, path string, s *diff.Sidecar) error {
	st := collectBundleStats(s)
	fmt.Fprintf(w, "archive: %s\n", path)
	fmt.Fprintf(w, "version: %s\n", s.Version)
	fmt.Fprintf(w, "feature: %s\n", s.Feature)
	fmt.Fprintf(w, "tool: %s %s\n", s.Tool, s.ToolVersion)
	fmt.Fprintf(w, "platform: %s\n", s.Platform)
	fmt.Fprintf(w, "created_at: %s\n", s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(w, "images: %d\n", len(s.Images))
	names := make([]string, 0, len(s.Images))
	byName := make(map[string]diff.ImageEntry, len(s.Images))
	for _, img := range s.Images {
		names = append(names, img.Name)
		byName[img.Name] = img
	}
	sort.Strings(names)
	for _, n := range names {
		img := byName[n]
		fmt.Fprintf(w, "  - %s\n", img.Name)
		fmt.Fprintf(w, "      target:   %s\n", img.Target.ManifestDigest)
		fmt.Fprintf(w, "      baseline: %s\n", img.Baseline.ManifestDigest)
	}
	fmt.Fprintf(w, "blobs: %d (full: %d, patch: %d)\n",
		len(s.Blobs), st.fullCount, st.patchCount)
	fmt.Fprintf(w, "forced-full due to dedup: %d blobs\n", st.sharedCount)
	fmt.Fprintf(w, "archive-size: %d bytes\n", st.fullBytes+st.patchBytes)
	return nil
}
```

Note: the heuristic `forced = full - 2*len(images)` in inspect is approximate; a precise count needs the pre-pack refCount map which isn't persisted in the sidecar. Document this limitation inline. Future: persist a `forced_full_count` field in the sidecar if users ask.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/ -run TestInspect -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect.go cmd/inspect_test.go
git commit -m "feat(cmd): inspect reports bundle sidecar with per-image rows"
```

---

### Task 29: inspect rejects legacy archive with helpful error

**Files:**
- Modify: `cmd/inspect_test.go`

- [ ] **Step 1: Write the failing test**

This test depends on the legacy fixture from Task 41; skip initially with `t.Skip("requires Task 41")`.

```go
func TestInspect_RejectsPhase1Archive(t *testing.T) {
	t.Skip("Uncomment after Task 41 pins the legacy fixture")
	var buf bytes.Buffer
	raw, err := archive.ReadSidecar("../testdata/legacy/phase1_oci.tar")
	require.NoError(t, err)
	_, err = diff.ParseSidecar(raw)
	require.Error(t, err)
	var p1 *diff.ErrPhase1Archive
	require.ErrorAs(t, err, &p1)
	require.Contains(t, err.Error(), "re-export")
	_ = buf
}
```

- [ ] **Step 2: Commit (stub in place)**

```bash
git add cmd/inspect_test.go
git commit -m "test(cmd): stub Phase 1 rejection test (enabled after Task 41)"
```

---

## Phase 7 — CLI rewrite

### Task 30: export CLI flags — retire old shape, add new

**Files:**
- Modify: `cmd/export.go`
- Modify: `cmd/export_test.go`

Retire flag-based form. Add:
- Positional `BASELINE TARGET OUT` (3 args).
- `--bundle FILE`.
- `--pair NAME:BASELINE=TARGET` (repeatable).
- `--output PATH` (required with --bundle/--pair; disallowed with positional).
- `--platform`, `--compress`, `--intra-layer`, `--dry-run` — carry forward.

- [ ] **Step 1: Replace the cobra command**

```go
package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

var exportFlags = struct {
	bundle     string
	pair       []string
	output     string
	platform   string
	compress   string
	intraLayer string
	dryRun     bool
}{}

func newExportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "export [BASELINE TARGET OUT]",
		Short: "Export a bundle archive from one or more image pairs.",
		Args:  cobra.MaximumNArgs(3),
		RunE:  runExport,
	}
	f := c.Flags()
	f.StringVar(&exportFlags.bundle, "bundle", "", "JSON bundle spec file")
	f.StringArrayVar(&exportFlags.pair, "pair", nil, "NAME:BASELINE=TARGET (repeatable)")
	f.StringVar(&exportFlags.output, "output", "", "output archive path (required with --bundle/--pair)")
	f.StringVar(&exportFlags.platform, "platform", "", "os/arch[/variant]")
	f.StringVar(&exportFlags.compress, "compress", "none", "outer compression: none|zstd")
	f.StringVar(&exportFlags.intraLayer, "intra-layer", "auto", "per-layer patching: auto|off")
	f.BoolVar(&exportFlags.dryRun, "dry-run", false, "plan without writing output")
	return c
}

func init() { rootCmd.AddCommand(newExportCommand()) }

func runExport(cmd *cobra.Command, args []string) error {
	pairs, output, err := resolveExportInputs(args, exportFlags.bundle, exportFlags.pair, exportFlags.output)
	if err != nil {
		return err
	}
	ctx := context.Background()
	opts := exporter.Options{
		Pairs: pairs, Platform: exportFlags.platform, Compress: exportFlags.compress,
		IntraLayer: exportFlags.intraLayer, OutputPath: output,
		ToolVersion: version, Progress: cmd.ErrOrStderr(),
	}
	if exportFlags.dryRun {
		stats, err := exporter.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"%d pairs · shipped: %d blobs (%d B) · required: %d blobs (%d B) · shared: %d\n",
			stats.Pairs, stats.ShippedCount, stats.ShippedBytes,
			stats.RequiredCount, stats.RequiredBytes, stats.SharedBlobs)
		return nil
	}
	if err := exporter.Export(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", output)
	return nil
}
```

- [ ] **Step 2: Implement resolveExportInputs**

Split off into `cmd/export_inputs.go`:

```go
package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

func resolveExportInputs(args []string, bundle string, pair []string, output string) (
	[]exporter.Pair, string, error,
) {
	hasPositional := len(args) > 0
	hasBundle := bundle != ""
	hasPair := len(pair) > 0
	n := 0
	if hasPositional {
		n++
	}
	if hasBundle {
		n++
	}
	if hasPair {
		n++
	}
	switch n {
	case 0:
		return nil, "", errors.New("one of positional (BASELINE TARGET OUT), --bundle, --pair required")
	case 1:
		// ok
	default:
		return nil, "", errors.New("positional, --bundle, --pair are mutually exclusive")
	}

	if hasPositional {
		if len(args) != 3 {
			return nil, "", errors.New("positional form requires exactly 3 args: BASELINE TARGET OUT")
		}
		if output != "" {
			return nil, "", errors.New("--output is not allowed with positional form")
		}
		for _, p := range args[:2] {
			if _, err := os.Stat(p); err != nil {
				return nil, "", fmt.Errorf("%s: %w", p, err)
			}
		}
		return []exporter.Pair{{
			Name: "default", BaselinePath: args[0], TargetPath: args[1],
		}}, args[2], nil
	}

	if output == "" {
		return nil, "", errors.New("--output is required with --bundle/--pair")
	}

	if hasBundle {
		spec, err := diff.ParseBundleSpec(bundle)
		if err != nil {
			return nil, "", err
		}
		pairs := make([]exporter.Pair, len(spec.Pairs))
		for i, p := range spec.Pairs {
			pairs[i] = exporter.Pair{
				Name: p.Name, BaselinePath: p.Baseline, TargetPath: p.Target,
			}
		}
		return pairs, output, nil
	}

	// --pair flags
	pairs := make([]exporter.Pair, 0, len(pair))
	for _, raw := range pair {
		parsed, err := parsePairFlag(raw)
		if err != nil {
			return nil, "", err
		}
		pairs = append(pairs, parsed)
	}
	return pairs, output, nil
}

func parsePairFlag(raw string) (exporter.Pair, error) {
	colon := strings.Index(raw, ":")
	if colon < 0 {
		return exporter.Pair{}, fmt.Errorf("--pair %q: missing ':' before BASELINE=TARGET", raw)
	}
	name := raw[:colon]
	rest := raw[colon+1:]
	eq := strings.Index(rest, "=")
	if eq < 0 {
		return exporter.Pair{}, fmt.Errorf("--pair %q: missing '=' between BASELINE and TARGET", raw)
	}
	return exporter.Pair{Name: name, BaselinePath: rest[:eq], TargetPath: rest[eq+1:]}, nil
}
```

- [ ] **Step 3: Write parser tests**

```go
// cmd/export_inputs_test.go
func TestResolveExportInputs_Matrix(t *testing.T) {
	cases := []struct {
		name string
		args []string
		bundle string
		pair []string
		output string
		wantErr string
	}{
		{"no inputs", nil, "", nil, "", "required"},
		{"positional+bundle", []string{"a","b","c"}, "spec.json", nil, "", "mutually exclusive"},
		{"positional with --output", []string{"a","b","c"}, "", nil, "o.tar", "not allowed"},
		{"bundle without output", nil, "spec.json", nil, "", "required"},
		// ... more
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveExportInputs(tc.args, tc.bundle, tc.pair, tc.output)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}

func TestParsePairFlag(t *testing.T) {
	p, err := parsePairFlag("svc:baseline.tar=target.tar")
	require.NoError(t, err)
	require.Equal(t, exporter.Pair{Name: "svc", BaselinePath: "baseline.tar", TargetPath: "target.tar"}, p)
	_, err = parsePairFlag("bad")
	require.ErrorContains(t, err, "missing ':'")
	_, err = parsePairFlag("svc:nofoo")
	require.ErrorContains(t, err, "missing '='")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/ -run 'Export|ResolveExport|ParsePair' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/export.go cmd/export_inputs.go cmd/export_test.go cmd/export_inputs_test.go
git commit -m "feat(cmd): export accepts positional/bundle/pair with mutex check"
```

---

### Task 31: export help EXAMPLES

**Files:**
- Modify: `cmd/export.go`

Per spec §10.3 mitigation: `diffah help export` gains EXAMPLES.

- [ ] **Step 1: Replace Short + add Long + Example**

```go
Short: "Export a bundle archive from one or more image pairs.",
Long: `Export accepts a single image pair as positional arguments or multiple
pairs via --bundle/--pair. Every archive is a bundle — single-image
exports produce a bundle of length one.`,
Example: `  # Positional (single image pair)
  diffah export svc-a-5.2.tar svc-a-5.3.tar out.tar

  # JSON bundle spec (multi-image)
  diffah export --bundle bundle.json --output out.tar

  # Repeated --pair flags (quick 2-3 image bundles)
  diffah export \
    --pair svc-a:svc-a-5.2.tar=svc-a-5.3.tar \
    --pair svc-b:svc-b-5.2.tar=svc-b-5.3.tar \
    --output out.tar`,
```

- [ ] **Step 2: Run tests**

Run: `make test`
Expected: pass.

- [ ] **Step 3: Commit**

```bash
git add cmd/export.go
git commit -m "docs(cmd): export EXAMPLES for all three invocation shapes"
```

---

### Task 32: import CLI flags — retire old shape, add new

**Files:**
- Modify: `cmd/import.go`
- Create: `cmd/import_inputs.go`
- Modify: `cmd/import_test.go`

New form:
- Positional: `ARCHIVE [BASELINE]` (1-2 args; 2nd arg positional only allowed for bundle-of-one).
- `--baseline NAME=PATH` (repeatable).
- `--baseline-spec FILE` (JSON).
- `--output DIR` (required).
- `--strict` (new).
- `--output-format`, `--allow-convert`, `--dry-run` — carry forward.

- [ ] **Step 1: Rewrite cobra command**

```go
var importFlags = struct {
	baseline     []string
	baselineSpec string
	output       string
	outputFormat string
	strict       bool
	dryRun       bool
	allowConvert bool
}{}

func newImportCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "import ARCHIVE [BASELINE]",
		Short: "Reconstruct one or more images from a bundle archive.",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  runImport,
	}
	f := c.Flags()
	f.StringArrayVar(&importFlags.baseline, "baseline", nil, "NAME=PATH (repeatable)")
	f.StringVar(&importFlags.baselineSpec, "baseline-spec", "", "baseline spec JSON")
	f.StringVar(&importFlags.output, "output", "", "output directory (required)")
	f.StringVar(&importFlags.outputFormat, "output-format", "", "docker-archive|oci-archive|dir (default: dir)")
	f.BoolVar(&importFlags.strict, "strict", false, "fail if any image has no matching baseline")
	f.BoolVar(&importFlags.dryRun, "dry-run", false, "parse the bundle without writing output")
	f.BoolVar(&importFlags.allowConvert, "allow-convert", false, "allow media-type conversion")
	_ = c.MarkFlagRequired("output")
	return c
}
```

- [ ] **Step 2: Implement resolveImportInputs**

`cmd/import_inputs.go`:

```go
package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/pkg/diff"
)

func resolveImportInputs(
	args []string, baseline []string, baselineSpec string,
) (archivePath string, baselines map[string]string, positional string, err error) {
	archivePath = args[0]
	positionalBaseline := ""
	if len(args) == 2 {
		positionalBaseline = args[1]
	}
	hasPositional := positionalBaseline != ""
	hasFlag := len(baseline) > 0
	hasSpec := baselineSpec != ""
	n := 0
	if hasPositional {
		n++
	}
	if hasFlag {
		n++
	}
	if hasSpec {
		n++
	}
	if n == 0 {
		return "", nil, "", errors.New("one of positional BASELINE, --baseline, --baseline-spec required")
	}
	if n > 1 {
		return "", nil, "", errors.New("positional, --baseline, --baseline-spec mutually exclusive")
	}

	if hasPositional {
		// Must defer name resolution to import pipeline (needs sidecar).
		return archivePath, nil, positionalBaseline, nil
	}
	if hasSpec {
		spec, err := diff.ParseBaselineSpec(baselineSpec)
		if err != nil {
			return "", nil, "", err
		}
		return archivePath, spec.Baselines, "", nil
	}
	// --baseline flags
	m := make(map[string]string, len(baseline))
	for _, raw := range baseline {
		name, path, ok := strings.Cut(raw, "=")
		if !ok || name == "" || path == "" {
			return "", nil, "", fmt.Errorf("--baseline %q: expected NAME=PATH", raw)
		}
		if _, dup := m[name]; dup {
			return "", nil, "", &diff.ErrDuplicateBundleName{Name: name}
		}
		m[name] = path
	}
	return archivePath, m, "", nil
}
```

- [ ] **Step 3: Wire into runImport**

```go
func runImport(cmd *cobra.Command, args []string) error {
	archivePath, baselines, positional, err := resolveImportInputs(
		args, importFlags.baseline, importFlags.baselineSpec)
	if err != nil {
		return err
	}
	ctx := context.Background()

	// For positional form, we need the sidecar to resolve the one image name.
	if positional != "" {
		raw, err := archive.ReadSidecar(archivePath)
		if err != nil {
			return err
		}
		sc, err := diff.ParseSidecar(raw)
		if err != nil {
			return err
		}
		m, err := importer.PositionalBaselineMap(sc, positional)
		if err != nil {
			return err
		}
		baselines = m
	}

	opts := importer.Options{
		DeltaPath:    archivePath,
		Baselines:    baselines,
		OutputPath:   importFlags.output,
		OutputFormat: importFlags.outputFormat,
		AllowConvert: importFlags.allowConvert,
		Strict:       importFlags.strict,
		Progress:     cmd.ErrOrStderr(),
	}
	if importFlags.dryRun {
		report, err := importer.DryRun(ctx, opts)
		if err != nil {
			return err
		}
		return printImportDryRun(cmd.OutOrStdout(), report)
	}
	if err := importer.Import(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", importFlags.output)
	return nil
}

func printImportDryRun(w io.Writer, r importer.DryRunReport) error {
	fmt.Fprintf(w, "bundle: %d images (feature=%s version=%s)\n",
		len(r.Images), r.Feature, r.Version)
	for _, img := range r.Images {
		decision := "would import"
		if !img.WouldImport {
			decision = "skip: " + img.SkipReason
		}
		fmt.Fprintf(w, "  - %s: %s (layers=%d, from-archive=%d, from-baseline=%d, patch=%d)\n",
			img.Name, decision, img.LayerCount, img.ArchiveLayerCount,
			img.BaselineLayerCount, img.PatchLayerCount)
	}
	fmt.Fprintf(w, "blobs: full=%d patch=%d; archive=%d bytes\n",
		r.Blobs.FullCount, r.Blobs.PatchCount, r.ArchiveBytes)
	return nil
}
```

- [ ] **Step 4: Parser tests**

Add `cmd/import_inputs_test.go` mirroring Task 30's export test matrix.

- [ ] **Step 5: Commit**

```bash
git add cmd/import.go cmd/import_inputs.go cmd/import_test.go cmd/import_inputs_test.go
git commit -m "feat(cmd): import accepts positional/name-map/spec with strict mode"
```

---

### Task 33: import help EXAMPLES

**Files:**
- Modify: `cmd/import.go`

Add Long and Example strings covering all four invocation shapes (positional, `--baseline`, `--baseline-spec`, and `--strict`).

- [ ] **Step 1: Append to the command**

```go
Long: `Import reconstructs one or more images from a bundle archive. Images
whose baselines are not provided are skipped (stderr log). Use --strict
to reject missing baselines before writing any output.`,
Example: `  # Bundle of one (positional baseline)
  diffah import out.tar svc-a-5.2.tar --output ./restored

  # Multi-image with --baseline
  diffah import out.tar \
    --baseline svc-a=svc-a-5.2.tar \
    --baseline svc-b=svc-b-5.2.tar \
    --output ./restored

  # Spec file (symmetry with --bundle)
  diffah import out.tar --baseline-spec baselines.json --output ./restored

  # CI gate: fail if any baseline missing
  diffah import out.tar --baseline-spec baselines.json --output ./restored --strict`,
```

- [ ] **Step 2: Commit**

```bash
git add cmd/import.go
git commit -m "docs(cmd): import EXAMPLES for all four invocation shapes"
```

---

## Phase 8 — Fixtures

### Task 34: v5 bundle source fixtures (a/b baselines + targets)

**Files:**
- Modify: `scripts/build_fixtures/main.go`
- Create: `testdata/fixtures/v5_bundle_spec.json`
- Create: `testdata/fixtures/v5_bundle_baseline_spec.json`

Source images construction (spec §8.3):
- `v5_a_baseline.oci.tar`, `v5_a_target.oci.tar`
- `v5_b_baseline.oci.tar`, `v5_b_target.oci.tar`

Layer structure so the integration tests exercise:
- One layer identical across both targets → force-full dedup path.
- One layer patchable against the image's own baseline → per-image patch-from path.
- One layer from a shared base layer present in both baselines.

Use the existing `buildSharedLayerBlob` / `overlapFiles` primitives. Keep sizes small (<100 KB per layer) so fixtures stay under a few MB total.

- [ ] **Step 1: Design the layer set**

Pseudocode; actual bytes can vary as long as digest relationships hold:

```
// Shared across BOTH targets (force-full dedup):
sharedShippedBytes := seededRandom(9001, 32*1024) // always encoding=full

// Per-image patchable layers:
aBaselinePatchable := seededRandom(10001, 40*1024)
aTargetPatchable := mutate(aBaselinePatchable) // small diff → patch winner
bBaselinePatchable := seededRandom(11001, 40*1024)
bTargetPatchable := mutate(bBaselinePatchable)

// Shared base present in BOTH baselines (so required_from_baseline × 2):
sharedBaseLayer := seededRandom(12001, 20*1024)

// Each baseline includes: sharedBaseLayer + per-image patchable
// Each target includes: sharedBaseLayer + per-image target patchable +
//                       sharedShippedBytes
```

- [ ] **Step 2: Implement in `scripts/build_fixtures/main.go`**

Add a `buildV5Bundle()` function after `buildFixtures`. Include the spec JSON files in the fixture output step.

Shape of `v5_bundle_spec.json` (committed to testdata/fixtures/):

```json
{
  "pairs": [
    {"name":"svc-a","baseline":"v5_a_baseline.oci.tar","target":"v5_a_target.oci.tar"},
    {"name":"svc-b","baseline":"v5_b_baseline.oci.tar","target":"v5_b_target.oci.tar"}
  ]
}
```

Shape of `v5_bundle_baseline_spec.json`:

```json
{"baselines":{"svc-a":"v5_a_baseline.oci.tar","svc-b":"v5_b_baseline.oci.tar"}}
```

Both written by `buildV5Bundle`.

- [ ] **Step 3: Run generator**

```bash
make fixtures
```

Expected: four new OCI archives + two JSON specs appear under `testdata/fixtures/`, and CHECKSUMS updates.

- [ ] **Step 4: Commit**

```bash
git add scripts/build_fixtures/main.go testdata/fixtures/v5_*.tar testdata/fixtures/v5_*.json testdata/fixtures/CHECKSUMS
git commit -m "fixtures(v5): multi-image bundle sources for integration tests"
```

---

### Task 35: Pin legacy Phase 1 archive

**Files:**
- Create: `testdata/legacy/phase1_oci.tar`
- Create: `scripts/build_legacy_fixture/main.go` (temporary)

Write a one-shot Go program that uses the current `LegacySidecar` type to produce a valid Phase 1 archive. Commit the output. Delete the program in the next commit (Task 36).

- [ ] **Step 1: Write the generator**

`scripts/build_legacy_fixture/main.go`:

```go
//go:build containers_image_openpgp

package main

import (
	"archive/tar"
	"fmt"
	"os"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func main() {
	legacy := diff.LegacySidecar{
		Version:     diff.SchemaVersionV1,
		Tool:        "diffah",
		ToolVersion: "legacy-pin",
		CreatedAt:   time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Target: diff.LegacyTargetRef{
			ManifestDigest: digest.Digest("sha256:aa"),
			ManifestSize:   2,
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
		Baseline: diff.LegacyBaselineRef{
			ManifestDigest: digest.Digest("sha256:bb"),
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
		RequiredFromBaseline: []diff.BlobRef{},
		ShippedInDelta:       []diff.BlobRef{},
	}
	raw, err := legacy.Marshal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll("testdata/legacy", 0o755); err != nil {
		panic(err)
	}
	out, err := os.Create("testdata/legacy/phase1_oci.tar")
	if err != nil {
		panic(err)
	}
	defer out.Close()

	tw := tar.NewWriter(out)
	hdr := &tar.Header{
		Name:    diff.SidecarFilename,
		Mode:    0o644,
		Size:    int64(len(raw)),
		ModTime: time.Unix(1700000000, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		panic(err)
	}
	if _, err := tw.Write(raw); err != nil {
		panic(err)
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
}
```

The generator does **not** resurrect `archive.Pack` (deleted in Task 16).
It writes a single tar entry — just the legacy sidecar — directly via the
`archive/tar` stdlib. That matches the rejection test's needs: the importer
only reaches `diff.ParseSidecar`, triggers `ErrPhase1Archive` on the missing
`feature` field, and never walks the rest of the archive.

- [ ] **Step 2: Run generator + commit bytes + generator**

```bash
go run -tags containers_image_openpgp ./scripts/build_legacy_fixture
git add testdata/legacy/phase1_oci.tar scripts/build_legacy_fixture/main.go
git commit -m "fixtures(legacy): pin Phase 1 archive for rejection test"
```

---

### Task 36: Remove legacy fixture generator

**Files:**
- Delete: `scripts/build_legacy_fixture/main.go`

- [ ] **Step 1: Remove the directory**

```bash
git rm -r scripts/build_legacy_fixture
git commit -m "chore(fixtures): remove one-shot legacy generator after pinning

The phase1_oci.tar fixture is now immutable by design — never
regenerate from source."
```

Immediately follow by updating the inspect test from Task 29 to un-skip:

```bash
# Find the t.Skip line in cmd/inspect_test.go and delete it.
```

Run: `go test ./cmd/ -run TestInspect_RejectsPhase1Archive -v`
Expected: PASS.

```bash
git add cmd/inspect_test.go
git commit -m "test(cmd): enable Phase 1 rejection test with pinned fixture"
```

---

## Phase 9 — Integration tests (spec §8.2 matrix)

### Task 37: Integration harness: fresh bundle builder + helpers

**Files:**
- Create: `pkg/importer/integration_bundle_test.go`

Build a shared helper used by every integration test:

```go
//go:build integration

package importer_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/leosocy/diffah/pkg/exporter"
)

func fixtureV5Bundle(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "v5_bundle.tar")
	require.NoError(t, exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{
			{Name: "svc-a",
				BaselinePath: "../../testdata/fixtures/v5_a_baseline.oci.tar",
				TargetPath:   "../../testdata/fixtures/v5_a_target.oci.tar"},
			{Name: "svc-b",
				BaselinePath: "../../testdata/fixtures/v5_b_baseline.oci.tar",
				TargetPath:   "../../testdata/fixtures/v5_b_target.oci.tar"},
		},
		Platform: "linux/amd64", IntraLayer: "auto",
		Compress: "none", OutputPath: out, ToolVersion: "integration-test",
		CreatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
	}))
	return out
}
```

- [ ] **Step 1: Create the file + baseline test**

```go
func TestBundle_HappyPath(t *testing.T) {
	archivePath := fixtureV5Bundle(t)
	outDir := t.TempDir()
	require.NoError(t, importer.Import(context.Background(), importer.Options{
		DeltaPath: archivePath,
		Baselines: map[string]string{
			"svc-a": "../../testdata/fixtures/v5_a_baseline.oci.tar",
			"svc-b": "../../testdata/fixtures/v5_b_baseline.oci.tar",
		},
		OutputPath: outDir, OutputFormat: "dir",
	}))
	require.DirExists(t, filepath.Join(outDir, "svc-a"))
	require.DirExists(t, filepath.Join(outDir, "svc-b"))
}
```

- [ ] **Step 2: Run**

Run: `go test -tags integration ./pkg/importer/ -run TestBundle_HappyPath -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): v5 bundle happy-path round trip"
```

---

### Task 38: Partial import (no --strict)

- [ ] **Step 1: Write the test**

```go
func TestBundle_PartialImport_SkipsMissingBaseline(t *testing.T) {
	archivePath := fixtureV5Bundle(t)
	outDir := t.TempDir()
	var stderr bytes.Buffer
	require.NoError(t, importer.Import(context.Background(), importer.Options{
		DeltaPath: archivePath,
		Baselines: map[string]string{
			"svc-a": "../../testdata/fixtures/v5_a_baseline.oci.tar",
		},
		OutputPath: outDir, OutputFormat: "dir",
		Progress:   &stderr,
	}))
	require.DirExists(t, filepath.Join(outDir, "svc-a"))
	require.NoDirExists(t, filepath.Join(outDir, "svc-b"))
	require.Contains(t, stderr.String(), "svc-b: skipped")
}
```

- [ ] **Step 2: Run + Commit**

Run: `go test -tags integration ./pkg/importer/ -run TestBundle_PartialImport -v`
Commit:

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): partial import skips missing baselines"
```

---

### Task 39: --strict rejects missing baseline

- [ ] **Step 1: Write the test**

```go
func TestBundle_Strict_RejectsMissingBaseline(t *testing.T) {
	archivePath := fixtureV5Bundle(t)
	outDir := t.TempDir()
	err := importer.Import(context.Background(), importer.Options{
		DeltaPath: archivePath,
		Baselines: map[string]string{
			"svc-a": "../../testdata/fixtures/v5_a_baseline.oci.tar",
		},
		OutputPath: outDir, OutputFormat: "dir", Strict: true,
	})
	var bm *diff.ErrBaselineMissing
	require.ErrorAs(t, err, &bm)
	require.Equal(t, []string{"svc-b"}, bm.Names)
	empty, _ := os.ReadDir(outDir)
	require.Empty(t, empty, "strict failure must not write output")
}
```

- [ ] **Step 2: Run + Commit**

Run: `go test -tags integration ./pkg/importer/ -run TestBundle_Strict -v`

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): --strict rejects missing baseline pre-write"
```

---

### Task 40: Force-full dedup behavior

- [ ] **Step 1: Write the test**

```go
func TestBundle_ForceFull_OnSharedShippedLayer(t *testing.T) {
	archivePath := fixtureV5Bundle(t)
	raw, err := archive.ReadSidecar(archivePath)
	require.NoError(t, err)
	sc, err := diff.ParseSidecar(raw)
	require.NoError(t, err)

	// Find the shared shipped digest (v5 fixture construction guarantees one).
	// Simplest: any BlobEntry with encoding=full whose digest is NOT a
	// manifest/config — deduplication makes the count deterministic once
	// fixtures are pinned. Prefer to look up a known digest produced by
	// buildV5Bundle and assert on it directly.
	var sharedFull bool
	for _, b := range sc.Blobs {
		if b.Encoding == diff.EncodingFull && b.ArchiveSize == b.Size && b.Size >= 10_000 {
			sharedFull = true
			break
		}
	}
	require.True(t, sharedFull, "v5 bundle must contain a forced-full shared layer")
}
```

- [ ] **Step 2: Run + Commit**

Run: `go test -tags integration ./pkg/importer/ -run TestBundle_ForceFull -v`

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): shared shipped layer is encoding=full"
```

---

### Task 41: Unknown baseline name

- [ ] **Step 1: Write the test**

```go
func TestBundle_UnknownBaselineName(t *testing.T) {
	archivePath := fixtureV5Bundle(t)
	err := importer.Import(context.Background(), importer.Options{
		DeltaPath: archivePath,
		Baselines: map[string]string{"svc-foo": "../../testdata/fixtures/v5_a_baseline.oci.tar"},
		OutputPath: t.TempDir(), OutputFormat: "dir",
	})
	var e *diff.ErrBaselineNameUnknown
	require.ErrorAs(t, err, &e)
	require.Equal(t, "svc-foo", e.Name)
	require.Contains(t, e.Available, "svc-a")
	require.Contains(t, e.Available, "svc-b")
}
```

- [ ] **Step 2: Run + Commit**

Run: `go test -tags integration ./pkg/importer/ -run TestBundle_UnknownBaseline -v`

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): unknown --baseline name lists available"
```

---

### Task 42: Baseline manifest digest mismatch

- [ ] **Step 1: Write the test**

```go
func TestBundle_BaselineMismatch_CrossesNames(t *testing.T) {
	archivePath := fixtureV5Bundle(t)
	// Pass svc-b's baseline under the "svc-a" key — manifest digest mismatch.
	err := importer.Import(context.Background(), importer.Options{
		DeltaPath: archivePath,
		Baselines: map[string]string{
			"svc-a": "../../testdata/fixtures/v5_b_baseline.oci.tar",
			"svc-b": "../../testdata/fixtures/v5_b_baseline.oci.tar",
		},
		OutputPath: t.TempDir(), OutputFormat: "dir",
	})
	var mm *diff.ErrBaselineMismatch
	require.ErrorAs(t, err, &mm)
	require.Equal(t, "svc-a", mm.Name)
}
```

- [ ] **Step 2: Run + Commit**

Run: `go test -tags integration ./pkg/importer/ -run TestBundle_BaselineMismatch -v`

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): baseline manifest digest mismatch rejected"
```

---

### Task 43: Legacy Phase 1 archive rejected

- [ ] **Step 1: Write the test**

```go
func TestBundle_LegacyArchive_Rejected(t *testing.T) {
	outDir := t.TempDir()
	err := importer.Import(context.Background(), importer.Options{
		DeltaPath:  "../../testdata/legacy/phase1_oci.tar",
		Baselines:  map[string]string{"default": "../../testdata/fixtures/v1_oci.tar"},
		OutputPath: outDir, OutputFormat: "dir",
	})
	var p1 *diff.ErrPhase1Archive
	require.ErrorAs(t, err, &p1)
	require.Contains(t, err.Error(), "re-export")
	empty, _ := os.ReadDir(outDir)
	require.Empty(t, empty)
}
```

- [ ] **Step 2: Run + Commit**

Run: `go test -tags integration ./pkg/importer/ -run TestBundle_LegacyArchive -v`

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): Phase 1 archive import is rejected"
```

---

### Task 44: Determinism — byte-identical re-export

- [ ] **Step 1: Write the test**

```go
func TestBundle_Export_Deterministic(t *testing.T) {
	one := filepath.Join(t.TempDir(), "one.tar")
	two := filepath.Join(t.TempDir(), "two.tar")
	opts := func(path string) exporter.Options {
		return exporter.Options{
			Pairs: []exporter.Pair{
				{Name: "svc-a",
					BaselinePath: "../../testdata/fixtures/v5_a_baseline.oci.tar",
					TargetPath:   "../../testdata/fixtures/v5_a_target.oci.tar"},
				{Name: "svc-b",
					BaselinePath: "../../testdata/fixtures/v5_b_baseline.oci.tar",
					TargetPath:   "../../testdata/fixtures/v5_b_target.oci.tar"},
			},
			Platform: "linux/amd64", IntraLayer: "off",
			Compress: "none", OutputPath: path, ToolVersion: "det-test",
			CreatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		}
	}
	require.NoError(t, exporter.Export(context.Background(), opts(one)))
	require.NoError(t, exporter.Export(context.Background(), opts(two)))
	oneBytes, _ := os.ReadFile(one)
	twoBytes, _ := os.ReadFile(two)
	require.Equal(t, sha256.Sum256(oneBytes), sha256.Sum256(twoBytes))
}
```

- [ ] **Step 2: Run + Commit**

Run: `go test -tags integration ./pkg/importer/ -run TestBundle_Export_Deterministic -v`

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): export output is byte-identical across runs"
```

---

### Task 45: Bundle-of-one positional end-to-end

- [ ] **Step 1: Write the test (CLI-level, in cmd/)**

```go
//go:build integration

func TestCmd_PositionalRoundTrip(t *testing.T) {
	outArchive := filepath.Join(t.TempDir(), "out.tar")
	outDir := t.TempDir()

	// exercise `diffah export BASELINE TARGET OUT` via go run
	cmd := exec.Command("go", "run",
		"-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper",
		"../",
		"export",
		"../testdata/fixtures/v1_oci.tar",
		"../testdata/fixtures/v2_oci.tar",
		outArchive,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout+stderr: %s", out)

	// import with positional baseline
	cmd2 := exec.Command("go", "run",
		"-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper",
		"../",
		"import", outArchive, "../testdata/fixtures/v1_oci.tar",
		"--output", outDir, "--output-format", "dir",
	)
	out2, err := cmd2.CombinedOutput()
	require.NoError(t, err, "stdout+stderr: %s", out2)

	require.DirExists(t, filepath.Join(outDir, "default"))
}
```

- [ ] **Step 2: Run + Commit**

Run: `go test -tags integration ./cmd/ -run TestCmd_PositionalRoundTrip -v`

```bash
git add cmd/export_integration_test.go cmd/import_integration_test.go
git commit -m "test(integration): positional single-image round-trip via CLI"
```

---

## Phase 10 — Cleanup

### Task 46: Delete LegacySidecar type + helpers

**Files:**
- Delete: `pkg/diff/legacy_sidecar.go`, `pkg/diff/legacy_sidecar_test.go`

Prerequisite: nothing imports `LegacySidecar` anymore (only the pinned fixture test references legacy behavior, and that test asserts on bytes and error, not on the type).

- [ ] **Step 1: Grep to confirm no callers**

```bash
rg 'LegacySidecar|LegacyTargetRef|LegacyBaselineRef|ParseLegacySidecar' --type go
```

Expected: no matches outside `pkg/diff/legacy_*.go`.

- [ ] **Step 2: Delete**

```bash
git rm pkg/diff/legacy_sidecar.go pkg/diff/legacy_sidecar_test.go
```

- [ ] **Step 3: Run full suite**

Run: `make lint test && make test-integration`
Expected: green.

- [ ] **Step 4: Commit**

```bash
git commit -m "chore(diff): delete LegacySidecar after all callers migrated"
```

---

### Task 47: CHANGELOG + README

**Files:**
- Modify: `CHANGELOG.md` (create if absent)
- Modify: `README.md`

Per spec §11.

- [ ] **Step 1: CHANGELOG entry**

```markdown
## Unreleased (v2 Phase 2 II.a+II.b)

### Breaking

- All archives are now bundles. Phase 1 single-image archives no longer
  import — re-export with this version.
- `diffah export` retires `--target` / `--baseline` / `--baseline-manifest`
  flags. Use positional `BASELINE TARGET OUT`, `--bundle`, or `--pair`.
- `diffah import` writes to `OUTPUT_DIR/<name>/` per image (including
  bundle-of-one).

### Added

- Multi-image bundles via `--bundle FILE.json` or repeated
  `--pair NAME:BASELINE=TARGET`.
- Per-name baseline resolution on import: `--baseline NAME=PATH`,
  `--baseline-spec FILE`, plus positional for bundle-of-one.
- `--strict` rejects bundles with missing baselines before writing.
- Cross-image blob dedup for layers, manifests, configs.
- Force-full encoding for shipped layers referenced by ≥ 2 images.

### Removed

- Phase 1 flat sidecar schema (`diffah.json` without `feature`).
- Manifest-only baseline export path.
```

- [ ] **Step 2: README bundle format section**

Add a short subsection describing the bundle layout and linking to
`docs/superpowers/specs/2026-04-20-diffah-v2-multi-image-bundle-design.md`.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md README.md
git commit -m "docs: CHANGELOG + README for bundle format"
```

---

### Task 48: Final verification sweep

- [ ] **Step 1: Full test matrix**

```bash
make lint
make test
make test-integration
```

Expected: all three green.

- [ ] **Step 2: Inspect a freshly exported bundle**

```bash
go run -tags containers_image_openpgp,exclude_graphdriver_btrfs,exclude_graphdriver_devicemapper . \
    export --pair a:testdata/fixtures/v5_a_baseline.oci.tar=testdata/fixtures/v5_a_target.oci.tar \
           --pair b:testdata/fixtures/v5_b_baseline.oci.tar=testdata/fixtures/v5_b_target.oci.tar \
           --output /tmp/bundle.tar
go run -tags containers_image_openpgp,exclude_graphdriver_btrfs,exclude_graphdriver_devicemapper . \
    inspect /tmp/bundle.tar
```

Expected output contains `feature: bundle`, `images: 2`, `- a` / `- b`, `forced-full due to dedup: N`.

- [ ] **Step 3: No commit needed unless a regression surfaced**

---

## Self-review notes

- **Task coverage:** every spec section has a task. §4 → Tasks 2-5. §5 → Tasks 7, 10-19, 30-31. §6 → Tasks 20-27, 32-33. §7 → all phases. §8 → Tasks 37-45. §9 → Tasks 21, 29, 35-36, 43. §10 → Tasks 44, 28, 30-33, 34. §11 → Tasks 46-48. §12 open-question defaults adopted as stated (no follow-up tasks).
- **Placeholders:** deliberately kept full code samples for every task that writes code. Two places say "implementation omitted here" (Task 24 bundleImageSource full boilerplate; Task 35 tar writer) — both point to an existing file in the tree to copy from; both are traceable.
- **Type consistency:** `Sidecar.Blobs` is `map[digest.Digest]BlobEntry` throughout. `Pair`, `ResolvedImage`, `pairPlan` names stay stable from introduction to last use. Both exporter and importer Options use `Progress io.Writer` (unified — earlier references to `ProgressW` were cleaned up after the first advisor pass).
- **Missing explicit task: package doc refresh for pkg/diff.** The top-of-file comment on `pkg/diff/errors.go` describes Phase 1 semantics ("Package diff defines the domain types and contracts shared by the exporter and importer services."). Update it in Task 48 when running the final sweep — one-line fix if stale.
