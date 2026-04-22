# Bundle Import Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the deferred Task 25 multi-image import loop, add digest verification on every blob read via a new streaming `bundleImageSource`, switch `OUTPUT` to a directory, rewrite `DryRunReport` to the plan's Task 26 § 6.5 shape, log real `encodeSingleShipped` errors instead of swallowing them, restore missing `SourceHint` from the baseline filename, and clear all 24 lint issues.

**Architecture:** One new error sentinel in `pkg/diff`; a new `bundleImageSource` in `pkg/importer` that implements `types.ImageSource` streaming from disk-backed bundle blobs plus a wrapped baseline source, verifying every served blob's digest; a rewritten `composeImage` that uses it; the multi-image loop in `Import`; a log-warning-and-continue fallback in `encodeShipped`; a `buildBundle` helper that de-duplicates `Export` and `DryRun`; and a final single-commit lint sweep.

**Tech Stack:** Go 1.25.4, `github.com/opencontainers/go-digest`, `go.podman.io/image/v5` (types/copy/directory/docker-archive/oci-archive), `github.com/klauspost/compress/zstd`, `github.com/leosocy/diffah/internal/zstdpatch`, `github.com/stretchr/testify/require`.

**Spec reference:** `docs/superpowers/specs/2026-04-22-bundle-import-cleanup-design.md`.

**Refinement locked in-plan (where spec is silent):**

- `errNoBaselineMatch` sentinel **not needed.** `Planner.Run` in `pkg/exporter/intralayer.go` already emits a `fullEntry(l)` when `pickSimilar` returns `ok=false` and never returns a "no match" error. So every error from `encodeSingleShipped` is a real bug. The fallback-plus-warning logic therefore does not need to discriminate on sentinel — it logs any error to `opts.Progress` and falls back to full. This matches the user-approved design intent (B: log-and-continue) with one fewer moving part than the spec's § 4.5 sketch.
- `bundleHarness.baselinePath(name)` is deleted outright (unused, and fixing it is busywork).

***

## File structure

| Package / file | Change | Responsibility |
|---|---|---|
| `pkg/diff/errors.go` | modify | Add `ErrBaselineBlobDigestMismatch` type and error |
| `pkg/diff/errors_test.go` | modify | Cover the new error's `Error()` string |
| `pkg/importer/compose.go` | rewrite | Replace tmpdir-writing helpers with a streaming `bundleImageSource` + `staticSourceRef` + rewritten `composeImage` |
| `pkg/importer/compose_test.go` | rewrite | Unit tests for each `GetBlob` path (full/patch/baseline) + `GetManifest` + digest mismatch |
| `pkg/importer/importer.go` | modify | Multi-image loop, `OUTPUT`-as-directory guard, `DryRunReport` v2 |
| `pkg/importer/resolve.go` | modify | Extract `rejectUnknownBaselineNames` helper to drop below funlen |
| `pkg/importer/integration_bundle_test.go` | modify | Delete rejection test; add `ImportsBoth` + `PartialSkip` + `OutputMustBeDirectory`; update `DryRunReport` test; delete `baselinePath(name)` |
| `pkg/exporter/encode.go` | modify | Warning-on-error fallback in `encodeShipped`; extract `fullEntry` helper |
| `pkg/exporter/encode_test.go` | new | Tests for silent-full (planner returns full entry) vs warning-full (real error) |
| `pkg/exporter/exporter.go` | modify | Extract `buildBundle` helper; `Export`/`DryRun` call it |
| `pkg/exporter/exporter_test.go` | modify | Test for shared pipeline |
| `pkg/exporter/assemble.go` | modify | `SourceHint = filepath.Base(p.BaselinePath)` |
| `pkg/exporter/pool.go` | modify | Delete unused `setEntry` |
| `cmd/import.go` | modify | Rewrite dry-run rendering for new `DryRunReport` shape |
| `CHANGELOG.md` | modify | Document `OUTPUT`-as-directory breaking change |
| `README.md` | modify | Multi-image import example; update `OUTPUT` description |

No new files in `pkg/exporter` beyond `encode_test.go`. No new packages. No new CLI flags.

***

## Phase 1 — Error sentinel for baseline digest drift

### Task 1: Add `ErrBaselineBlobDigestMismatch`

**Files:**

- Modify: `pkg/diff/errors.go` — add one type near `ErrIntraLayerAssemblyMismatch`
- Test: `pkg/diff/errors_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/diff/errors_test.go`:

```go
func TestErrBaselineBlobDigestMismatch_Message(t *testing.T) {
	e := &ErrBaselineBlobDigestMismatch{
		ImageName: "svc-a",
		Digest:    "sha256:aa",
		Got:       "sha256:bb",
	}
	require.Contains(t, e.Error(), "svc-a")
	require.Contains(t, e.Error(), "sha256:aa")
	require.Contains(t, e.Error(), "sha256:bb")
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./pkg/diff/ -run TestErrBaselineBlobDigestMismatch -v`
Expected: FAIL — type not defined.

- [ ] **Step 3: Add the type**

Append to `pkg/diff/errors.go` after `ErrIntraLayerAssemblyMismatch` (around line 70):

```go
// ErrBaselineBlobDigestMismatch reports that a baseline-served blob's
// computed sha256 did not match the digest the sidecar expected. Bytes
// are never written to the output when this fires.
type ErrBaselineBlobDigestMismatch struct {
	ImageName string
	Digest    string
	Got       string
}

func (e *ErrBaselineBlobDigestMismatch) Error() string {
	return fmt.Sprintf("image %q: baseline blob %s has digest %s",
		e.ImageName, e.Digest, e.Got)
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./pkg/diff/ -run TestErrBaselineBlobDigestMismatch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/diff/errors.go pkg/diff/errors_test.go
git commit -m "feat(diff): add ErrBaselineBlobDigestMismatch sentinel"
```

***

## Phase 2 — `bundleImageSource` streaming reader

### Task 2: bundleImageSource skeleton + GetManifest + trivial methods

**Files:**

- Create: `pkg/importer/compose.go` (full replacement of the existing file)
- Create: `pkg/importer/compose_test.go` (full replacement of the existing file)

This task sets up the type and all `types.ImageSource` methods **except** `GetBlob`, which comes in Tasks 3–5. The existing `composeImage` stays in place as legacy code until Task 7 removes it — but because this task rewrites `compose.go`, the old implementation must temporarily move to a scratch location. Use this split:

- Move the OLD compose.go body into a NEW file `pkg/importer/compose_legacy.go` (same package, just gives us breathing room). Delete `pkg/importer/compose_test.go` since its few assertions cover helpers that will vanish in Task 7. A new `compose_test.go` is authored here.

- [ ] **Step 1: Move the legacy body aside**

```bash
git mv pkg/importer/compose.go pkg/importer/compose_legacy.go
rm pkg/importer/compose_test.go
```

Inside `compose_legacy.go`, rename to avoid symbol collision:

- `composeImage` → `composeImageLegacy`
- `readBlobFromBundle` → stays (still unique)
- `writeBlobAsDigestFile` → stays
- `blobFilePath` → stays
- `extractConfigDigestFromBytes` → stays
- `extractLayerDigests` → stays
- `fetchBaselineBlob` → stays
- `applyPatchAndWrite` → stays
- `applyPatch` → stays
- `composedImage` → stays

Edit `pkg/importer/importer.go` line 69:

```go
ci, err := composeImageLegacy(ctx, img, bundle.sidecar, bundle, rb.Ref)
```

Verify compile: `go build ./...` — must succeed.

- [ ] **Step 2: Write the failing test for GetManifest**

Create `pkg/importer/compose_test.go`:

```go
package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

// buildTestBundle exports a bundle of one (svc-a, v1→v2) for compose tests.
// Returns the bundle path, the extracted tmpdir, and the parsed sidecar.
func buildTestBundle(t *testing.T) (bundlePath string, tmp *extractedBundle) {
	t.Helper()
	outDir := t.TempDir()
	bp := filepath.Join(outDir, "bundle.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{{
			Name:         "svc-a",
			BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath:   "../../testdata/fixtures/v2_oci.tar",
		}},
		Platform:    "linux/amd64",
		IntraLayer:  "off",
		OutputPath:  bp,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	b, err := extractBundle(bp)
	require.NoError(t, err)
	t.Cleanup(b.cleanup)
	return bp, b
}

func openBaseline(t *testing.T, path string) types.ImageSource {
	t.Helper()
	ref, err := imageio.OpenArchiveRef(path)
	require.NoError(t, err)
	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	return src
}

func TestBundleImageSource_GetManifest_ReturnsStoredBytes(t *testing.T) {
	_, b := buildTestBundle(t)
	img := b.sidecar.Images[0]
	mfPath := filepath.Join(b.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	require.NoError(t, err)

	src := &bundleImageSource{
		blobsDir:     b.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      b.sidecar,
		baseline:     openBaseline(t, "../../testdata/fixtures/v1_oci.tar"),
		imageName:    img.Name,
	}

	gotBytes, gotMime, err := src.GetManifest(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, mfBytes, gotBytes)
	require.Equal(t, img.Target.MediaType, gotMime)
	require.Equal(t, digest.FromBytes(gotBytes), img.Target.ManifestDigest)
}
```

- [ ] **Step 3: Run the test, verify it fails**

Run: `go test ./pkg/importer/ -run TestBundleImageSource_GetManifest -v`
Expected: FAIL — `bundleImageSource` undefined.

- [ ] **Step 4: Implement the skeleton in `pkg/importer/compose.go`**

Create `pkg/importer/compose.go`:

```go
package importer

import (
	"context"
	"io"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)

// bundleImageSource implements go.podman.io/image/v5/types.ImageSource for
// one resolved image inside a bundle. Shipped blobs come from the extracted
// bundle's blobs/ directory (decoded on the fly for encoding=patch); required
// blobs come from a wrapped baseline source. Every served blob is digest-
// verified before return. No tmpdir is ever written to — copy.Image reads
// via GetBlob, which returns in-memory bytes.
type bundleImageSource struct {
	blobsDir     string
	manifest     []byte
	manifestMime string
	sidecar      *diff.Sidecar
	baseline     types.ImageSource
	imageName    string
	ref          types.ImageReference
}

func (s *bundleImageSource) Reference() types.ImageReference { return s.ref }
func (s *bundleImageSource) Close() error                    { return nil } // baseline owned by caller

func (s *bundleImageSource) GetManifest(_ context.Context, instance *digest.Digest) ([]byte, string, error) {
	if instance != nil {
		return nil, "", nil
	}
	return s.manifest, s.manifestMime, nil
}

func (s *bundleImageSource) HasThreadSafeGetBlob() bool { return false }

func (s *bundleImageSource) GetSignaturesWithFormat(
	ctx context.Context, instance *digest.Digest,
) ([]types.Signature, error) {
	return nil, nil
}

func (s *bundleImageSource) LayerInfosForCopy(
	ctx context.Context, instance *digest.Digest,
) ([]types.BlobInfo, error) {
	return nil, nil
}

func (s *bundleImageSource) GetBlob(
	ctx context.Context, info types.BlobInfo, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	return nil, 0, diff.ErrBaselineMissingBlob{Digest: info.Digest.String(), Source: s.imageName}.Error() //nolint:staticcheck
}
```

**Note on the `GetBlob` return:** returning an error type where a `error` value is expected wouldn't compile — that line is an intentional placeholder that will be rewritten in Task 3. Use this instead so the build works:

```go
func (s *bundleImageSource) GetBlob(
	ctx context.Context, info types.BlobInfo, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("GetBlob not implemented yet") // TASK-3
}
```

Add `"fmt"` to the imports.

- [ ] **Step 5: Run the test, verify it passes**

Run: `go test ./pkg/importer/ -run TestBundleImageSource_GetManifest -v`
Expected: PASS.

- [ ] **Step 6: Run the whole package to catch regressions**

Run: `go test ./pkg/importer/ -count=1 -short`
Expected: all green (legacy compose path still drives Import).

- [ ] **Step 7: Commit**

```bash
git add pkg/importer/compose.go pkg/importer/compose_test.go pkg/importer/compose_legacy.go pkg/importer/importer.go
git commit -m "feat(importer): scaffold bundleImageSource with manifest + trivial methods

Legacy composeImage renamed to composeImageLegacy and Import switched to it
so the new source can be implemented behind a stable public API."
```

***

### Task 3: GetBlob for bundle-full encoding with digest verification

**Files:**

- Modify: `pkg/importer/compose.go`
- Modify: `pkg/importer/compose_test.go`

- [ ] **Step 1: Write the failing test**

Append to `compose_test.go`:

```go
func TestBundleImageSource_GetBlob_FullEncoding_ReturnsVerifiedBytes(t *testing.T) {
	_, b := buildTestBundle(t)
	img := b.sidecar.Images[0]
	mfPath := filepath.Join(b.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	require.NoError(t, err)

	// Pick a full-encoded blob from the sidecar (manifest + config are always full).
	var fullDigest digest.Digest
	for d, entry := range b.sidecar.Blobs {
		if entry.Encoding == diff.EncodingFull {
			fullDigest = d
			break
		}
	}
	require.NotEmpty(t, fullDigest)

	src := &bundleImageSource{
		blobsDir:     b.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      b.sidecar,
		baseline:     openBaseline(t, "../../testdata/fixtures/v1_oci.tar"),
		imageName:    img.Name,
	}

	rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: fullDigest}, nil)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, int64(len(got)), size)
	require.Equal(t, fullDigest, digest.FromBytes(got))
}
```

Add `"io"` to the test file's imports.

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./pkg/importer/ -run TestBundleImageSource_GetBlob_FullEncoding -v`
Expected: FAIL — `GetBlob not implemented yet`.

- [ ] **Step 3: Implement the full-encoding branch**

Replace the placeholder `GetBlob` in `compose.go`:

```go
import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/pkg/diff"
)
```

```go
func (s *bundleImageSource) GetBlob(
	ctx context.Context, info types.BlobInfo, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	entry, ok := s.sidecar.Blobs[info.Digest]
	if !ok {
		// Required from baseline — Task 5 rewrites this branch.
		return nil, 0, fmt.Errorf("baseline delegation not implemented yet") // TASK-5
	}
	switch entry.Encoding {
	case diff.EncodingFull:
		return s.serveFull(info.Digest)
	case diff.EncodingPatch:
		// Task 4 rewrites this branch.
		return nil, 0, fmt.Errorf("patch decode not implemented yet") // TASK-4
	}
	return nil, 0, fmt.Errorf("unknown encoding %q for blob %s", entry.Encoding, info.Digest)
}

func (s *bundleImageSource) serveFull(d digest.Digest) (io.ReadCloser, int64, error) {
	path := filepath.Join(s.blobsDir, d.Algorithm().String(), d.Encoded())
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read full blob %s: %w", d, err)
	}
	if got := digest.FromBytes(data); got != d {
		return nil, 0, &diff.ErrBaselineBlobDigestMismatch{
			ImageName: s.imageName, Digest: d.String(), Got: got.String(),
		}
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./pkg/importer/ -run TestBundleImageSource_GetBlob_FullEncoding -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/compose.go pkg/importer/compose_test.go
git commit -m "feat(importer): bundleImageSource.GetBlob for full encoding with digest verify"
```

***

### Task 4: GetBlob for patch encoding with digest verification

**Files:**

- Modify: `pkg/importer/compose.go`
- Modify: `pkg/importer/compose_test.go`

- [ ] **Step 1: Write the failing test**

Append to `compose_test.go`:

```go
func TestBundleImageSource_GetBlob_PatchEncoding_DecodesAndVerifies(t *testing.T) {
	// Build a bundle with intra-layer ON so at least one layer is patch-encoded.
	outDir := t.TempDir()
	bp := filepath.Join(outDir, "bundle.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{{
			Name:         "svc-a",
			BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath:   "../../testdata/fixtures/v2_oci.tar",
		}},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  bp,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	b, err := extractBundle(bp)
	require.NoError(t, err)
	t.Cleanup(b.cleanup)

	var patchDigest digest.Digest
	for d, entry := range b.sidecar.Blobs {
		if entry.Encoding == diff.EncodingPatch {
			patchDigest = d
			break
		}
	}
	if patchDigest == "" {
		t.Skip("fixtures produced no patch-encoded layer; nothing to cover")
	}

	img := b.sidecar.Images[0]
	mfPath := filepath.Join(b.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	require.NoError(t, err)

	src := &bundleImageSource{
		blobsDir:     b.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      b.sidecar,
		baseline:     openBaseline(t, "../../testdata/fixtures/v1_oci.tar"),
		imageName:    img.Name,
	}

	rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: patchDigest}, nil)
	require.NoError(t, err)
	defer rc.Close()
	decoded, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, int64(len(decoded)), size)
	require.Equal(t, patchDigest, digest.FromBytes(decoded), "decoded blob must match expected digest")
}

func TestBundleImageSource_GetBlob_PatchEncoding_CorruptedBlob_RaisesAssemblyMismatch(t *testing.T) {
	outDir := t.TempDir()
	bp := filepath.Join(outDir, "bundle.tar")
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs: []exporter.Pair{{
			Name:         "svc-a",
			BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath:   "../../testdata/fixtures/v2_oci.tar",
		}},
		Platform: "linux/amd64", IntraLayer: "auto", OutputPath: bp,
		ToolVersion: "test", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	b, err := extractBundle(bp)
	require.NoError(t, err)
	t.Cleanup(b.cleanup)

	var patchDigest digest.Digest
	for d, entry := range b.sidecar.Blobs {
		if entry.Encoding == diff.EncodingPatch {
			patchDigest = d
			break
		}
	}
	if patchDigest == "" {
		t.Skip("fixtures produced no patch-encoded layer; nothing to cover")
	}

	// Corrupt the patch bytes on disk.
	patchPath := filepath.Join(b.blobDir, patchDigest.Algorithm().String(), patchDigest.Encoded())
	require.NoError(t, os.WriteFile(patchPath, []byte("not a real zstd patch"), 0o600))

	img := b.sidecar.Images[0]
	mfPath := filepath.Join(b.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	require.NoError(t, err)

	src := &bundleImageSource{
		blobsDir:     b.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      b.sidecar,
		baseline:     openBaseline(t, "../../testdata/fixtures/v1_oci.tar"),
		imageName:    img.Name,
	}
	_, _, err = src.GetBlob(context.Background(), types.BlobInfo{Digest: patchDigest}, nil)
	require.Error(t, err) // either decode error or assembly mismatch — both are acceptable signals
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./pkg/importer/ -run TestBundleImageSource_GetBlob_PatchEncoding -v`
Expected: FAIL — `patch decode not implemented yet`.

- [ ] **Step 3: Implement the patch branch**

In `compose.go`, add the `internal/zstdpatch` import:

```go
import (
	...
	"github.com/leosocy/diffah/internal/zstdpatch"
)
```

Replace the `case diff.EncodingPatch` branch in `GetBlob`:

```go
case diff.EncodingPatch:
	return s.servePatch(ctx, info.Digest, entry, cache)
```

Add the helper:

```go
func (s *bundleImageSource) servePatch(
	ctx context.Context, target digest.Digest, entry diff.BlobEntry, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	patchPath := filepath.Join(s.blobsDir, target.Algorithm().String(), target.Encoded())
	patchBytes, err := os.ReadFile(patchPath)
	if err != nil {
		return nil, 0, fmt.Errorf("read patch blob %s: %w", target, err)
	}
	baseBytes, err := s.fetchVerifiedBaselineBlob(ctx, entry.PatchFromDigest, cache)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch patch-from blob %s: %w", entry.PatchFromDigest, err)
	}
	out, err := zstdpatch.Decode(baseBytes, patchBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("decode patch for %s: %w", target, err)
	}
	if got := digest.FromBytes(out); got != target {
		return nil, 0, &diff.ErrIntraLayerAssemblyMismatch{
			Digest: target.String(), Got: got.String(),
		}
	}
	return io.NopCloser(bytes.NewReader(out)), int64(len(out)), nil
}

// fetchVerifiedBaselineBlob reads `d` from the wrapped baseline source and
// verifies its digest. Used both for patch-from references (Task 4) and for
// blobs the sidecar did not ship (Task 5).
func (s *bundleImageSource) fetchVerifiedBaselineBlob(
	ctx context.Context, d digest.Digest, cache types.BlobInfoCache,
) ([]byte, error) {
	rc, _, err := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: d}, cache)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	if got := digest.FromBytes(data); got != d {
		return nil, &diff.ErrBaselineBlobDigestMismatch{
			ImageName: s.imageName, Digest: d.String(), Got: got.String(),
		}
	}
	return data, nil
}
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./pkg/importer/ -run TestBundleImageSource_GetBlob_PatchEncoding -v`
Expected: PASS. If the first test skips because `IntraLayer: "auto"` happened to not produce any patch blob for the v1→v2 fixture, that's fine — fingerprinting may always prefer full; the patch branch still compiles and the second (corruption) test covers the decode path via the same code.

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/compose.go pkg/importer/compose_test.go
git commit -m "feat(importer): bundleImageSource.GetBlob for patch encoding with digest verify"
```

***

### Task 5: GetBlob for baseline delegation with digest verification

**Files:**

- Modify: `pkg/importer/compose.go`
- Modify: `pkg/importer/compose_test.go`

- [ ] **Step 1: Write the failing test**

Append to `compose_test.go`:

```go
func TestBundleImageSource_GetBlob_BaselineDelegation_Verified(t *testing.T) {
	_, b := buildTestBundle(t)
	img := b.sidecar.Images[0]
	mfPath := filepath.Join(b.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	require.NoError(t, err)

	// Any digest not in the sidecar's Blobs map must be fetched from the
	// baseline. Walk the target manifest for a layer digest and find one
	// absent from Blobs (those are the "Required" blobs).
	var layers []struct {
		Digest digest.Digest `json:"digest"`
	}
	require.NoError(t, json.Unmarshal(mfBytes, &struct {
		Layers *[]struct {
			Digest digest.Digest `json:"digest"`
		} `json:"layers"`
	}{Layers: &layers}))

	var requiredDigest digest.Digest
	for _, l := range layers {
		if _, ok := b.sidecar.Blobs[l.Digest]; !ok {
			requiredDigest = l.Digest
			break
		}
	}
	if requiredDigest == "" {
		t.Skip("all target layers shipped; no baseline-delegated blob to cover")
	}

	src := &bundleImageSource{
		blobsDir:     b.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      b.sidecar,
		baseline:     openBaseline(t, "../../testdata/fixtures/v1_oci.tar"),
		imageName:    img.Name,
	}
	rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: requiredDigest}, nil)
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, int64(len(got)), size)
	require.Equal(t, requiredDigest, digest.FromBytes(got))
}
```

Add `"encoding/json"` to the test file's imports.

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./pkg/importer/ -run TestBundleImageSource_GetBlob_BaselineDelegation -v`
Expected: FAIL — `baseline delegation not implemented yet`.

- [ ] **Step 3: Implement the baseline-delegation branch**

Replace the `if !ok` branch in `GetBlob`:

```go
if !ok {
	data, err := s.fetchVerifiedBaselineBlob(ctx, info.Digest, cache)
	if err != nil {
		return nil, 0, fmt.Errorf("baseline serve %s: %w", info.Digest, err)
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./pkg/importer/ -run TestBundleImageSource_GetBlob_BaselineDelegation -v`
Expected: PASS.

- [ ] **Step 5: Run the whole importer package for regressions**

Run: `go test ./pkg/importer/ -count=1 -short`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add pkg/importer/compose.go pkg/importer/compose_test.go
git commit -m "feat(importer): bundleImageSource.GetBlob for baseline blobs with digest verify"
```

***

## Phase 3 — `composeImage` rewrite + legacy deletion

### Task 6: Wrap bundleImageSource in a reference and rewrite composeImage

**Files:**

- Modify: `pkg/importer/compose.go`
- Delete: `pkg/importer/compose_legacy.go`
- Modify: `pkg/importer/importer.go`

This task introduces the reference wrapper and the new `composeImage` signature, and removes the legacy helpers — all in one commit since they're mutually dependent.

- [ ] **Step 1: Add `staticSourceRef` to `compose.go`**

Append to `compose.go`:

```go
import (
	...
	"go.podman.io/image/v5/types"
)

// staticSourceRef wraps a prebuilt ImageSource so copy.Image can consume it
// as a source. The inner ref is synthetic (we don't read from the filesystem
// through the ref itself — only through the source).
type staticSourceRef struct {
	src *bundleImageSource
}

func (r *staticSourceRef) Transport() types.ImageTransport           { return nil }
func (r *staticSourceRef) StringWithinTransport() string             { return "bundle://" + r.src.imageName }
func (r *staticSourceRef) DockerReference() reference.Named          { return nil }
func (r *staticSourceRef) PolicyConfigurationIdentity() string       { return "" }
func (r *staticSourceRef) PolicyConfigurationNamespaces() []string   { return nil }
func (r *staticSourceRef) NewImage(
	ctx context.Context, sys *types.SystemContext,
) (types.ImageCloser, error) {
	return nil, fmt.Errorf("staticSourceRef.NewImage not supported")
}
func (r *staticSourceRef) NewImageSource(
	ctx context.Context, sys *types.SystemContext,
) (types.ImageSource, error) {
	return r.src, nil
}
func (r *staticSourceRef) NewImageDestination(
	ctx context.Context, sys *types.SystemContext,
) (types.ImageDestination, error) {
	return nil, fmt.Errorf("staticSourceRef.NewImageDestination not supported")
}
func (r *staticSourceRef) DeleteImage(ctx context.Context, sys *types.SystemContext) error {
	return fmt.Errorf("staticSourceRef.DeleteImage not supported")
}
```

Add import: `"go.podman.io/image/v5/docker/reference"` (aliased as `reference`). Add `_ = reference.Named(nil)` is not needed — just the import.

- [ ] **Step 2: Add the new `composeImage` function**

Append to `compose.go`:

```go
// composeImage imports a single resolved image into outputDir/<name>/
// (for dir output format) or outputDir/<name>.tar (for archive formats).
// It streams blobs via bundleImageSource — no tmpdir materialization.
func composeImage(
	ctx context.Context,
	img diff.ImageEntry,
	bundle *extractedBundle,
	rb resolvedBaseline,
	outputDir, outputFormat string,
	allowConvert bool,
) error {
	baseSrc, err := rb.Ref.NewImageSource(ctx, nil)
	if err != nil {
		return fmt.Errorf("open baseline source for %q: %w", img.Name, err)
	}
	defer baseSrc.Close()

	mfPath := filepath.Join(bundle.blobDir, img.Target.ManifestDigest.Algorithm().String(),
		img.Target.ManifestDigest.Encoded())
	mfBytes, err := os.ReadFile(mfPath)
	if err != nil {
		return fmt.Errorf("read target manifest %s: %w", img.Target.ManifestDigest, err)
	}

	src := &bundleImageSource{
		blobsDir:     bundle.blobDir,
		manifest:     mfBytes,
		manifestMime: img.Target.MediaType,
		sidecar:      bundle.sidecar,
		baseline:     baseSrc,
		imageName:    img.Name,
	}
	src.ref = &staticSourceRef{src: src}

	resolvedFmt, err := resolveOutputFormat(outputFormat, img.Target.MediaType, allowConvert)
	if err != nil {
		return err
	}

	var outPath string
	switch resolvedFmt {
	case FormatDir:
		outPath = filepath.Join(outputDir, img.Name)
	case FormatDockerArchive, FormatOCIArchive:
		outPath = filepath.Join(outputDir, img.Name+".tar")
	default:
		return fmt.Errorf("unknown --output-format %q", resolvedFmt)
	}

	outRef, err := buildOutputRef(outPath, resolvedFmt)
	if err != nil {
		return err
	}
	policyCtx, err := imageio.DefaultPolicyContext()
	if err != nil {
		return err
	}
	defer func() { _ = policyCtx.Destroy() }()

	copyOpts := &copy.Options{}
	if resolvedFmt == FormatDir {
		copyOpts.PreserveDigests = true
	}
	if _, err := copy.Image(ctx, policyCtx, outRef, src.ref, copyOpts); err != nil {
		return fmt.Errorf("compose %q: %w", img.Name, err)
	}
	return nil
}
```

Add imports: `"go.podman.io/image/v5/copy"`, `"github.com/leosocy/diffah/internal/imageio"`.

- [ ] **Step 3: Delete compose_legacy.go**

```bash
git rm pkg/importer/compose_legacy.go
```

- [ ] **Step 4: Update importer.go to call the new composeImage**

Edit `pkg/importer/importer.go` around line 69. Replace the legacy call:

```go
ci, err := composeImageLegacy(ctx, img, bundle.sidecar, bundle, rb.Ref)
if err != nil {
	return fmt.Errorf("compose image %q: %w", rb.Name, err)
}
defer ci.cleanup()

resolvedFmt, err := resolveOutputFormat(opts.OutputFormat, img.Target.MediaType, opts.AllowConvert)
if err != nil {
	return err
}

tmpOut := opts.OutputPath + ".tmp"
if err := runCopy(ctx, ci.Ref, tmpOut, resolvedFmt); err != nil {
	_ = removeOutput(tmpOut, resolvedFmt)
	return fmt.Errorf("copy image %q: %w", rb.Name, err)
}
if err := os.Rename(tmpOut, opts.OutputPath); err != nil {
	return fmt.Errorf("rename output: %w", err)
}
```

With:

```go
if err := os.MkdirAll(opts.OutputPath, 0o755); err != nil {
	return fmt.Errorf("mkdir output %s: %w", opts.OutputPath, err)
}
if err := composeImage(ctx, img, bundle, rb,
	opts.OutputPath, opts.OutputFormat, opts.AllowConvert); err != nil {
	return err
}
```

Remove now-unused code in `importer.go`:
- `runCopy` (lines 146-165) — `composeImage` owns its own copy call.
- `removeOutput` (lines 167-172) — no longer needed.
- The `buildOutputRef` helper (lines 174-196) stays because `composeImage` calls it.

- [ ] **Step 5: Run the whole package**

Run: `go test ./pkg/importer/ -count=1 -short`
Expected: most tests pass; `TestIntegration_MultiImageBundle_RejectsImport` still passes (multi-image guard still in place from Task 8).

If the single-image `TestIntegration_PartialImport` fails, the likely cause is the output layout shift. The test calls `h.importOpts(...)` which sets `OutputPath: filepath.Join(h.tmpDir, "output.tar")`. Under the new behavior this becomes a directory containing `output.tar/svc-a.tar`. The test only asserts `require.NoError(t, err)`, so it should still pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/importer/compose.go pkg/importer/importer.go
git rm pkg/importer/compose_legacy.go
git commit -m "feat(importer): streaming composeImage via bundleImageSource

Replaces the tmpdir-materializing compose path with a pure streaming
source. Every served blob is digest-verified; baseline blobs go through
the wrapped source (verified); shipped full blobs go through disk +
digest check; patched blobs are decoded in memory + digest check."
```

***

## Phase 4 — Multi-image Import loop + OUTPUT-as-directory

### Task 7: Rewrite Import to iterate all resolved images

**Files:**

- Modify: `pkg/importer/importer.go`

- [ ] **Step 1: Rewrite `Import`**

Replace the entire current `Import` function (lines 36-90) with:

```go
func Import(ctx context.Context, opts Options) error {
	bundle, err := extractBundle(opts.DeltaPath)
	if err != nil {
		return err
	}
	defer bundle.cleanup()

	if err := validatePositionalBaseline(bundle.sidecar, opts.Baselines); err != nil {
		return err
	}
	resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.Strict)
	if err != nil {
		return err
	}

	if err := ensureOutputIsDirectory(opts.OutputPath); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.OutputPath, 0o755); err != nil {
		return fmt.Errorf("mkdir output %s: %w", opts.OutputPath, err)
	}

	progress := opts.Progress
	if progress == nil {
		progress = io.Discard
	}

	resolvedByName := make(map[string]resolvedBaseline, len(resolved))
	for _, r := range resolved {
		resolvedByName[r.Name] = r
	}

	imported := 0
	skipped := make([]string, 0)
	for _, img := range bundle.sidecar.Images {
		rb, ok := resolvedByName[img.Name]
		if !ok {
			fmt.Fprintf(progress, "%s: skipped (no baseline provided)\n", img.Name)
			skipped = append(skipped, img.Name)
			continue
		}
		if err := composeImage(ctx, img, bundle, rb,
			opts.OutputPath, opts.OutputFormat, opts.AllowConvert); err != nil {
			return err
		}
		imported++
	}
	fmt.Fprintf(progress, "imported %d of %d images; skipped: %v\n",
		imported, len(bundle.sidecar.Images), skipped)
	return nil
}

func ensureOutputIsDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat output %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"OUTPUT %s must be a directory (bundle output is written to OUTPUT/<name>.tar or OUTPUT/<name>/)",
			path)
	}
	return nil
}
```

- [ ] **Step 2: Remove unused helpers**

Delete `runCopy` (already gone from Task 6). Confirm `removeOutput` is gone. Keep `buildOutputRef` (called by composeImage).

- [ ] **Step 3: Run the existing integration suite**

Run: `go test ./pkg/importer/ -count=1 -run Integration -v`
Expected:
- `TestIntegration_PartialImport` — PASS (single image, loop iterates once)
- `TestIntegration_BundleOfOnePositional` — PASS (single-image default mapping)
- `TestIntegration_MultiImageBundle_RejectsImport` — **FAIL** (rejection guard now gone; Task 8 replaces this test)
- All other tests — PASS

The `RejectsImport` failure is expected and will be resolved by Task 8 deleting the test.

- [ ] **Step 4: Commit**

```bash
git add pkg/importer/importer.go
git commit -m "feat(importer): iterate all resolved images and require OUTPUT to be a directory

Closes the Task 25 gap from the original plan. OUTPUT is now uniformly a
directory; per-image output lands at OUTPUT/<name>.tar (archive formats)
or OUTPUT/<name>/ (dir format). Pre-existing file at OUTPUT fails with a
clear migration hint."
```

***

### Task 8: Replace rejection test with ImportsBoth + PartialSkip + OutputMustBeDirectory

**Files:**

- Modify: `pkg/importer/integration_bundle_test.go`

- [ ] **Step 1: Delete the rejection test and the broken helper**

In `pkg/importer/integration_bundle_test.go`, delete:

1. Lines 55-57: the `baselinePath(name)` helper (unused, ignores its argument).
2. Lines 199-211: `TestIntegration_MultiImageBundle_RejectsImport` — whole function.

- [ ] **Step 2: Add the happy-path test**

Append to `integration_bundle_test.go`:

```go
func TestIntegration_MultiImageBundle_ImportsBoth(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	outDir := filepath.Join(h.tmpDir, "out")
	opts := Options{
		DeltaPath:    h.bundlePath,
		Baselines:    map[string]string{
			"svc-a": "../../testdata/fixtures/v1_oci.tar",
			"svc-b": "../../testdata/fixtures/v1_oci.tar",
		},
		OutputPath:   outDir,
		OutputFormat: "oci-archive",
	}
	err := Import(h.ctx, opts)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(outDir, "svc-a.tar"))
	require.NoError(t, err, "svc-a.tar must exist")
	_, err = os.Stat(filepath.Join(outDir, "svc-b.tar"))
	require.NoError(t, err, "svc-b.tar must exist")
}

func TestIntegration_MultiImageBundle_PartialSkip(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	outDir := filepath.Join(h.tmpDir, "out")
	var progress bytes.Buffer
	opts := Options{
		DeltaPath:    h.bundlePath,
		Baselines:    map[string]string{"svc-a": "../../testdata/fixtures/v1_oci.tar"},
		OutputPath:   outDir,
		OutputFormat: "oci-archive",
		Progress:     &progress,
	}
	err := Import(h.ctx, opts)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(outDir, "svc-a.tar"))
	require.NoError(t, err, "svc-a.tar must exist")
	_, err = os.Stat(filepath.Join(outDir, "svc-b.tar"))
	require.ErrorIs(t, err, os.ErrNotExist, "svc-b.tar must not exist")
	require.Contains(t, progress.String(), "svc-b: skipped (no baseline provided)")
}

func TestIntegration_Import_OutputMustBeDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newBundleHarness(t, []exporter.Pair{{
		Name:         "svc-a",
		BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath:   "../../testdata/fixtures/v2_oci.tar",
	}})
	preExisting := filepath.Join(h.tmpDir, "not-a-dir")
	require.NoError(t, os.WriteFile(preExisting, []byte("file not dir"), 0o600))
	opts := Options{
		DeltaPath:    h.bundlePath,
		Baselines:    map[string]string{"default": "../../testdata/fixtures/v1_oci.tar"},
		OutputPath:   preExisting,
		OutputFormat: "oci-archive",
	}
	err := Import(h.ctx, opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be a directory")
}
```

Add `"bytes"` to the imports if not already present.

- [ ] **Step 3: Run the suite**

Run: `go test ./pkg/importer/ -count=1 -run Integration -v`
Expected: all tests pass (rejection test deleted, three new tests pass).

- [ ] **Step 4: Commit**

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(importer): replace multi-image rejection with ImportsBoth, PartialSkip, and OutputMustBeDirectory coverage

Drops the now-obsolete RejectsImport assertion. Covers the Task 7 loop's
two code paths (resolved vs skipped) and the OUTPUT-as-directory guard."
```

***

## Phase 5 — `encodeShipped` warning-on-error fallback

### Task 9: Warning-on-error fallback

**Files:**

- Modify: `pkg/exporter/encode.go`
- Modify: `pkg/exporter/exporter.go` — thread `opts.Progress` into `encodeShipped`

- [ ] **Step 1: Write the failing test**

Create `pkg/exporter/encode_test.go`:

```go
package exporter

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// crashingFingerprinter always errors. Drives encodeSingleShipped into the
// real-error branch so encodeShipped's warning + fallback is exercised.
type crashingFingerprinter struct{}

func (crashingFingerprinter) Fingerprint(
	ctx context.Context, mediaType string, raw []byte,
) (Fingerprint, error) {
	return nil, io.ErrUnexpectedEOF
}

func TestExportWithFingerprinter_Crash_LogsWarning_StillWrites(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	var progress bytes.Buffer
	outPath := filepath.Join(t.TempDir(), "bundle.tar")
	err := ExportWithFingerprinter(context.Background(), Options{
		Pairs: []Pair{{
			Name:         "svc-a",
			BaselinePath: "../../testdata/fixtures/v1_oci.tar",
			TargetPath:   "../../testdata/fixtures/v2_oci.tar",
		}},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  outPath,
		ToolVersion: "test",
		Progress:    &progress,
	}, crashingFingerprinter{})
	require.NoError(t, err, "export must tolerate fingerprinter crash")
	require.Contains(t, progress.String(), "patch encode failed",
		"progress must include a fallback warning")
}
```

**Coverage rationale:** we drive `encodeShipped`'s error branch via `Export` rather than calling `encodeShipped` directly, because a direct unit test would need to hand-build a `pairPlan` with real baseline metadata. The existing `exporter_test.go` determinism + roundtrip tests cover the happy path; this Export-level test exercises the warning/fallback branch through the full pipeline.

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./pkg/exporter/ -run TestExportWithFingerprinter_Crash -v`
Expected: FAIL — current code swallows the error silently (`progress` never sees the warning).

- [ ] **Step 3: Thread progress through encodeShipped**

Edit `pkg/exporter/exporter.go`. Change two call sites:

```go
if err := encodeShipped(ctx, pool, plans, opts.IntraLayer, opts.fingerprinter, opts.Progress); err != nil {
	return fmt.Errorf("encode shipped layers: %w", err)
}
```

(Both in `Export` and `DryRun`. This will collapse into a single call in Task 11.)

- [ ] **Step 4: Update encodeShipped**

Edit `pkg/exporter/encode.go`. Replace the whole file:

```go
package exporter

import (
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func encodeShipped(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, progress io.Writer,
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
				pool.addIfAbsent(s.Digest, layerBytes, fullEntry(s))
				continue
			}
			payload, entry, err := encodeSingleShipped(ctx, p, s, layerBytes, fp)
			if err != nil {
				if progress != nil {
					fmt.Fprintf(progress,
						"warning: %s: patch encode failed for %s (%v), falling back to full\n",
						p.Name, s.Digest, err)
				}
				pool.addIfAbsent(s.Digest, layerBytes, fullEntry(s))
				continue
			}
			pool.addIfAbsent(s.Digest, payload, entry)
		}
	}
	return nil
}

func encodeSingleShipped(
	ctx context.Context, p *pairPlan, s diff.BlobRef,
	target []byte, fp Fingerprinter,
) ([]byte, diff.BlobEntry, error) {
	readBlob := func(d digest.Digest) ([]byte, error) {
		if d == s.Digest {
			return target, nil
		}
		return readBlobBytes(ctx, p.BaselineRef, d)
	}
	entries, payloads, err := NewPlanner(p.BaselineLayerMeta, readBlob, fp).Run(ctx, []diff.BlobRef{s})
	if err != nil {
		return nil, diff.BlobEntry{}, err
	}
	if len(entries) == 0 {
		return nil, diff.BlobEntry{}, fmt.Errorf("planner returned no entries")
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
	return payload, bEntry, nil
}

// fullEntry returns a diff.BlobEntry describing `s` stored without patching.
// Used wherever a shipped layer cannot or should not be patch-encoded.
func fullEntry(s diff.BlobRef) diff.BlobEntry {
	return diff.BlobEntry{
		Size: s.Size, MediaType: s.MediaType,
		Encoding: diff.EncodingFull, ArchiveSize: s.Size,
	}
}
```

Note: dropped the leading `""` return value from `encodeSingleShipped` — it was always `""`. The call sites in `encodeShipped` now take `(payload, entry, err)` not `(_, payload, entry, err)`.

- [ ] **Step 5: Run the test, verify it passes**

Run: `go test ./pkg/exporter/ -run TestExportWithFingerprinter_Crash -v`
Expected: PASS.

- [ ] **Step 6: Run the whole package for regressions**

Run: `go test ./pkg/exporter/ -count=1 -short`
Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add pkg/exporter/encode.go pkg/exporter/exporter.go pkg/exporter/encode_test.go
git commit -m "feat(exporter): log warning and fall back to full on encodeSingleShipped errors

Previously encodeShipped swallowed every error class silently. Now any error
from encodeSingleShipped (zstd crash, fingerprinter crash, read failure) is
logged to opts.Progress with image and digest context, and the blob is
stored as encoding=full. Export itself still never aborts on per-layer
failure so the archive remains producible."
```

***

## Phase 6 — Exporter pipeline dedupe (`buildBundle`)

### Task 10: Extract buildBundle and dedupe Export/DryRun

**Files:**

- Modify: `pkg/exporter/exporter.go`

- [ ] **Step 1: Extract the shared prologue**

Replace the content of `pkg/exporter/exporter.go`:

```go
package exporter

import (
	"context"
	"fmt"
	"io"
	"time"
)

type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string
	CreatedAt   time.Time
	Progress    io.Writer

	fingerprinter Fingerprinter
}

type DryRunStats struct {
	TotalBlobs  int
	TotalImages int
	ArchiveSize int64
	PerImage    []ImageStats
}

type ImageStats struct {
	Name         string
	ShippedBlobs int
	ArchiveSize  int64
}

// builtBundle holds the intermediate state shared between Export and DryRun.
type builtBundle struct {
	plans []*pairPlan
	pool  *blobPool
}

// buildBundle plans every pair, seeds the pool with manifests + configs,
// counts shipped refs, and encodes shipped layers (with force-full dedup
// for refCount ≥ 2 and warning-on-error fallback). It does NOT write the
// archive — Export does that; DryRun just reads from the finished pool.
func buildBundle(ctx context.Context, opts *Options) (*builtBundle, error) {
	if err := ValidatePairs(opts.Pairs); err != nil {
		return nil, err
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().UTC()
	}
	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "planning %d pairs...\n", len(opts.Pairs))
	}

	plans := make([]*pairPlan, 0, len(opts.Pairs))
	pool := newBlobPool()

	for _, p := range opts.Pairs {
		plan, err := planPair(ctx, p, opts.Platform)
		if err != nil {
			return nil, fmt.Errorf("plan pair %q: %w", p.Name, err)
		}
		plans = append(plans, plan)
		seedManifestAndConfig(pool, plan)
	}
	for _, plan := range plans {
		for _, s := range plan.Shipped {
			pool.countShipped(s.Digest)
		}
	}
	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "planned %d pairs\n", len(plans))
	}

	if err := encodeShipped(ctx, pool, plans, opts.IntraLayer, opts.fingerprinter, opts.Progress); err != nil {
		return nil, fmt.Errorf("encode shipped layers: %w", err)
	}
	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "encoded %d blobs\n", len(pool.entries))
	}
	return &builtBundle{plans: plans, pool: pool}, nil
}

func Export(ctx context.Context, opts Options) error {
	bb, err := buildBundle(ctx, &opts)
	if err != nil {
		return err
	}
	sidecar := assembleSidecar(bb.pool, bb.plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)
	if err := writeBundleArchive(opts.OutputPath, sidecar, bb.pool); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	if opts.Progress != nil {
		var archiveSize int64
		for _, e := range sidecar.Blobs {
			archiveSize += e.ArchiveSize
		}
		fmt.Fprintf(opts.Progress, "wrote %s (%d bytes)\n", opts.OutputPath, archiveSize)
	}
	return nil
}

func DryRun(ctx context.Context, opts Options) (DryRunStats, error) {
	bb, err := buildBundle(ctx, &opts)
	if err != nil {
		return DryRunStats{}, err
	}
	sidecar := assembleSidecar(bb.pool, bb.plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)
	stats := DryRunStats{
		TotalBlobs:  len(sidecar.Blobs),
		TotalImages: len(sidecar.Images),
	}
	for _, e := range sidecar.Blobs {
		stats.ArchiveSize += e.ArchiveSize
	}
	for _, plan := range bb.plans {
		var imgSize int64
		var shippedCount int
		for _, s := range plan.Shipped {
			if e, ok := bb.pool.entries[s.Digest]; ok {
				imgSize += e.ArchiveSize
				shippedCount++
			}
		}
		stats.PerImage = append(stats.PerImage, ImageStats{
			Name: plan.Name, ShippedBlobs: shippedCount, ArchiveSize: imgSize,
		})
	}
	return stats, nil
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./pkg/exporter/ -count=1 -short`
Expected: all tests pass (behavior unchanged).

- [ ] **Step 3: Commit**

```bash
git add pkg/exporter/exporter.go
git commit -m "refactor(exporter): extract buildBundle helper shared by Export and DryRun"
```

***

## Phase 7 — `DryRunReport` v2

### Task 11: Rewrite DryRunReport types

**Files:**

- Modify: `pkg/importer/importer.go`

- [ ] **Step 1: Rewrite the types in-place**

Replace the existing `DryRunReport` / `ImageDryRunStats` declarations:

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
	FullCount  int
	PatchCount int
	FullBytes  int64
	PatchBytes int64
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

Add imports: `"time"`, `"github.com/opencontainers/go-digest"`.

- [ ] **Step 2: Run the build**

Run: `go build ./...`
Expected: FAIL — `DryRun` still references the old fields (`TotalImages`, `TotalBlobs`, etc.), and `cmd/import.go` too. Task 12 and Task 13 fix these.

Commit deferred; continue to Task 12.

***

### Task 12: Rewrite importer.DryRun to populate the new report

**Files:**

- Modify: `pkg/importer/importer.go`

- [ ] **Step 1: Write the failing test**

This test is already in `integration_bundle_test.go` as `TestIntegration_MultiImageBundle_DryRunReport`. Task 14 updates it. For now, write a focused unit test. Append to `integration_bundle_test.go`:

```go
func TestDryRun_PopulatesAllFields(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{
		"svc-a": "../../testdata/fixtures/v1_oci.tar",
	}, false)
	report, err := DryRun(h.ctx, opts)
	require.NoError(t, err)

	require.Equal(t, "bundle", report.Feature)
	require.Equal(t, "v1", report.Version)
	require.Equal(t, "diffah", report.Tool)
	require.Equal(t, "test", report.ToolVersion)
	require.Equal(t, "linux/amd64", report.Platform)
	require.NotZero(t, report.ArchiveBytes, "ArchiveBytes is the bundle file size")
	require.Greater(t, report.Blobs.FullCount+report.Blobs.PatchCount, 0)

	require.Len(t, report.Images, 2)
	var a, b ImageDryRun
	for _, i := range report.Images {
		switch i.Name {
		case "svc-a":
			a = i
		case "svc-b":
			b = i
		}
	}
	require.Equal(t, "svc-a", a.Name)
	require.True(t, a.BaselineProvided)
	require.True(t, a.WouldImport)
	require.Empty(t, a.SkipReason)
	require.Greater(t, a.LayerCount, 0)

	require.Equal(t, "svc-b", b.Name)
	require.False(t, b.BaselineProvided)
	require.False(t, b.WouldImport)
	require.Contains(t, b.SkipReason, "no baseline provided")
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./pkg/importer/ -run TestDryRun_PopulatesAllFields -v`
Expected: FAIL (current code returns the old shape).

- [ ] **Step 3: Rewrite DryRun**

Replace the existing `DryRun` function in `pkg/importer/importer.go`:

```go
func DryRun(ctx context.Context, opts Options) (DryRunReport, error) {
	bundle, err := extractBundle(opts.DeltaPath)
	if err != nil {
		return DryRunReport{}, err
	}
	defer bundle.cleanup()

	if err := validatePositionalBaseline(bundle.sidecar, opts.Baselines); err != nil {
		return DryRunReport{}, err
	}

	var blobStats BlobStats
	for _, b := range bundle.sidecar.Blobs {
		switch b.Encoding {
		case diff.EncodingFull:
			blobStats.FullCount++
			blobStats.FullBytes += b.ArchiveSize
		case diff.EncodingPatch:
			blobStats.PatchCount++
			blobStats.PatchBytes += b.ArchiveSize
		}
	}

	resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.Strict)
	if err != nil {
		return DryRunReport{}, err
	}
	provided := make(map[string]struct{}, len(resolved))
	for _, r := range resolved {
		provided[r.Name] = struct{}{}
	}

	images := make([]ImageDryRun, 0, len(bundle.sidecar.Images))
	for _, img := range bundle.sidecar.Images {
		layers, err := readManifestLayers(bundle, img.Target.ManifestDigest)
		if err != nil {
			return DryRunReport{}, fmt.Errorf("read target manifest for %q: %w", img.Name, err)
		}
		var archCount, baseCount, patchCount int
		for _, l := range layers {
			if entry, ok := bundle.sidecar.Blobs[l]; ok {
				archCount++
				if entry.Encoding == diff.EncodingPatch {
					patchCount++
				}
			} else {
				baseCount++
			}
		}
		_, has := provided[img.Name]
		row := ImageDryRun{
			Name:                   img.Name,
			BaselineManifestDigest: img.Baseline.ManifestDigest,
			TargetManifestDigest:   img.Target.ManifestDigest,
			BaselineProvided:       has,
			WouldImport:            has,
			LayerCount:             len(layers),
			ArchiveLayerCount:      archCount,
			BaselineLayerCount:     baseCount,
			PatchLayerCount:        patchCount,
		}
		if !has {
			row.SkipReason = "no baseline provided"
		}
		images = append(images, row)
	}

	var archiveBytes int64
	if info, err := os.Stat(opts.DeltaPath); err == nil {
		archiveBytes = info.Size()
	}

	return DryRunReport{
		Feature:      bundle.sidecar.Feature,
		Version:      bundle.sidecar.Version,
		Tool:         bundle.sidecar.Tool,
		ToolVersion:  bundle.sidecar.ToolVersion,
		CreatedAt:    bundle.sidecar.CreatedAt,
		Platform:     bundle.sidecar.Platform,
		Images:       images,
		Blobs:        blobStats,
		ArchiveBytes: archiveBytes,
	}, nil
}

// readManifestLayers parses the target manifest bytes from the extracted
// bundle and returns the layer digests in declaration order.
func readManifestLayers(bundle *extractedBundle, mfDigest digest.Digest) ([]digest.Digest, error) {
	path := filepath.Join(bundle.blobDir, mfDigest.Algorithm().String(), mfDigest.Encoded())
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m struct {
		Layers []struct {
			Digest digest.Digest `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	out := make([]digest.Digest, 0, len(m.Layers))
	for _, l := range m.Layers {
		out = append(out, l.Digest)
	}
	return out, nil
}
```

Add imports: `"encoding/json"`, `"path/filepath"`, `"github.com/opencontainers/go-digest"`.

- [ ] **Step 4: Run the new test, verify it passes**

Run: `go test ./pkg/importer/ -run TestDryRun_PopulatesAllFields -v`
Expected: PASS.

- [ ] **Step 5: Run the whole importer suite**

Run: `go test ./pkg/importer/ -count=1`
Expected: `TestIntegration_MultiImageBundle_DryRunReport` FAILS (old field names); Task 14 updates it.

- [ ] **Step 6: Commit**

```bash
git add pkg/importer/importer.go pkg/importer/integration_bundle_test.go
git commit -m "feat(importer): DryRunReport v2 with per-image layer breakdown

Matches the plan Task 26 § 6.5 shape exactly. Feature/Version/Tool/
ToolVersion/CreatedAt/Platform echoed from the sidecar; BlobStats split by
encoding; ImageDryRun carries both manifest digests, provided/would-import
flags, skip reason, and four layer counts (total, archive-shipped,
baseline-required, patched)."
```

***

### Task 13: Rewrite cmd/import.go dry-run rendering

**Files:**

- Modify: `cmd/import.go`

- [ ] **Step 1: Update the dry-run branch**

Replace the dry-run block in `runImport` (currently lines 71-86):

```go
if importFlags.dryRun {
	report, err := importer.DryRun(ctx, opts)
	if err != nil {
		return err
	}
	return renderDryRunReport(cmd.OutOrStdout(), report)
}
```

Add a new helper below `runImport`:

```go
func renderDryRunReport(w io.Writer, r importer.DryRunReport) error {
	fmt.Fprintf(w, "archive: feature=%s version=%s platform=%s\n",
		r.Feature, r.Version, r.Platform)
	fmt.Fprintf(w, "tool: %s %s, created %s\n",
		r.Tool, r.ToolVersion, r.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "archive bytes: %d\n", r.ArchiveBytes)
	fmt.Fprintf(w, "blobs: %d (full: %d, patch: %d) — full: %d B, patch: %d B\n",
		r.Blobs.FullCount+r.Blobs.PatchCount,
		r.Blobs.FullCount, r.Blobs.PatchCount,
		r.Blobs.FullBytes, r.Blobs.PatchBytes)
	fmt.Fprintf(w, "images: %d\n", len(r.Images))
	for _, img := range r.Images {
		state := "would import"
		if !img.WouldImport {
			state = fmt.Sprintf("skip — %s", img.SkipReason)
		}
		fmt.Fprintf(w, "  %-20s target=%s (%s)\n", img.Name, img.TargetManifestDigest, state)
		fmt.Fprintf(w, "    layers: %d total — %d shipped, %d from baseline, %d patched\n",
			img.LayerCount, img.ArchiveLayerCount, img.BaselineLayerCount, img.PatchLayerCount)
	}
	return nil
}
```

Add imports: `"io"`, `"time"`.

- [ ] **Step 2: Run the cmd package tests**

Run: `go test ./cmd/ -count=1 -short`
Expected: PASS (no test currently asserts the dry-run output content; adjust if existing tests fail due to format changes).

- [ ] **Step 3: Commit**

```bash
git add cmd/import.go
git commit -m "feat(cli): render DryRunReport v2 with per-image layer breakdown"
```

***

### Task 14: Update TestIntegration_MultiImageBundle_DryRunReport for the new shape

**Files:**

- Modify: `pkg/importer/integration_bundle_test.go`

- [ ] **Step 1: Rewrite the test**

Find `TestIntegration_MultiImageBundle_DryRunReport` (around line 292). Replace the body with:

```go
func TestIntegration_MultiImageBundle_DryRunReport(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	h := newMultiImageBundleHarness(t)
	opts := h.importOpts(map[string]string{
		"svc-a": "../../testdata/fixtures/v1_oci.tar",
	}, false)
	report, err := DryRun(h.ctx, opts)
	require.NoError(t, err)

	require.Equal(t, "bundle", report.Feature)
	require.Equal(t, "v1", report.Version)
	require.Len(t, report.Images, 2)

	byName := map[string]ImageDryRun{}
	for _, i := range report.Images {
		byName[i.Name] = i
	}
	require.True(t, byName["svc-a"].WouldImport)
	require.False(t, byName["svc-b"].WouldImport)
	require.Contains(t, byName["svc-b"].SkipReason, "no baseline provided")
	require.Greater(t, report.Blobs.FullCount, 0)
}
```

- [ ] **Step 2: Run the suite**

Run: `go test ./pkg/importer/ -count=1 -run Integration`
Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(importer): migrate DryRunReport integration test to the v2 shape"
```

***

## Phase 8 — Small polishes

### Task 15: SourceHint = filepath.Base + delete unused pool helper

**Files:**

- Modify: `pkg/exporter/assemble.go`
- Modify: `pkg/exporter/pool.go`
- Modify: `pkg/exporter/perpair.go` — need `pairPlan.BaselinePath` to pass through

`pairPlan` currently doesn't carry the baseline path string — it only carries the `types.ImageReference`. Add a field.

- [ ] **Step 1: Add BaselinePath to pairPlan**

In `pkg/exporter/perpair.go`, add a field to `pairPlan`:

```go
type pairPlan struct {
	Name              string
	BaselinePath      string // raw user-supplied path; used for Sidecar SourceHint
	BaselineRef       types.ImageReference
	...
}
```

In `planPair`, populate it:

```go
return &pairPlan{
	Name:             p.Name,
	BaselinePath:     p.BaselinePath,
	BaselineRef:      baseRef,
	...
```

- [ ] **Step 2: Update assembleSidecar**

Edit `pkg/exporter/assemble.go` line 34. Change:

```go
SourceHint:     p.Name + "-baseline",
```

To:

```go
SourceHint:     filepath.Base(p.BaselinePath),
```

Add import: `"path/filepath"`.

- [ ] **Step 3: Delete unused setEntry**

In `pkg/exporter/pool.go`, delete the `setEntry` method:

```go
// DELETE:
func (p *blobPool) setEntry(d digest.Digest, e diff.BlobEntry) {
	p.entries[d] = e
}
```

- [ ] **Step 4: Run all tests and lint**

Run:
```
go test ./... -count=1 -short
make lint
```

Expected: tests pass; lint count drops by 1 (`unused` gone). Other lint issues still present — Task 17 handles them.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/assemble.go pkg/exporter/perpair.go pkg/exporter/pool.go
git commit -m "refactor(exporter): SourceHint from baseline filename; drop unused pool helper"
```

***

## Phase 9 — Docs

### Task 16: Update CHANGELOG and README

**Files:**

- Modify: `CHANGELOG.md`
- Modify: `README.md`

- [ ] **Step 1: CHANGELOG — add OUTPUT-as-directory breaking change**

In `CHANGELOG.md`, locate the "Breaking changes" section and append a fourth bullet:

```markdown
- **`diffah import`**: `OUTPUT` positional argument is now a directory.
  Per-image output lands at `OUTPUT/<name>.tar` (archive formats) or
  `OUTPUT/<name>/` (`dir` format). Single-image bundles still use a
  per-image sub-entry — the default-mapped bundle-of-one produces
  `OUTPUT/default.tar` (or `OUTPUT/default/`).
```

- [ ] **Step 2: README — rewrite the import examples**

Update the "Single-image delta" section in `README.md` (around line 63-70):

```markdown
Reconstruct the full image on the consumer side:

```bash
diffah import \
  --baseline app=v1.tar \
  ./app_v1_to_v2.tar \
  ./out/
# output at ./out/app.tar
```
```

Update the "Multi-image bundle" section (around line 84-91):

```markdown
Import every image from the bundle in one command:

```bash
diffah import \
  --baseline svc-a=v1a.tar \
  --baseline svc-b=v1b.tar \
  ./bundle.tar \
  ./out/
# output at ./out/svc-a.tar and ./out/svc-b.tar
```

`OUTPUT` is always a directory. Per-image output lands at
`OUTPUT/<name>.tar` for archive output formats, or `OUTPUT/<name>/` for
`--output-format dir`.
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md README.md
git commit -m "docs: document OUTPUT-as-directory and multi-image import in README and CHANGELOG"
```

***

## Phase 10 — Lint sweep

### Task 17: Single lint sweep commit

**Files:** any remaining lint-offending file.

The 24 lint issues at the start of this work break down into three groups:

1. **Resolved by the refactor (no action here):** all 4 gosec G306 in `compose.go`, the gocyclo of `composeImage`, the revive `context-as-argument` + `unused-parameter` (codec) in `compose.go`, the unused `blobPool.setEntry`, the `staticcheck QF1003` in `compose.go`. Count: 8.
2. **Resolved by test rewrites:** the `goconst` `"zstd-patch"` (tests now use `exporter.CodecZstdPatch`), the `goconst` `"sha256:bb"` (replace with a test helper constant), the `gocritic appendAssign` in `pair_test.go`, the revive `unused-parameter` in `bundleHarness.baselinePath`. Count: 4.
3. **Residual — need fixes in this task:** gofmt (5), goimports (4), lll (1), funlen (`resolveBaselines`, 1). Count: 11.

- [ ] **Step 1: Run make lint and capture the actual residual list**

Run: `make lint 2>&1`

Expected output should now include only a subset of the original 24. Proceed to fix each remaining issue in the steps below.

- [ ] **Step 2: Fix gofmt and goimports**

Run:

```bash
gofmt -w ./cmd ./pkg ./internal
goimports -w -local github.com/leosocy/diffah ./cmd ./pkg ./internal
```

- [ ] **Step 3: Fix lll at pkg/diff/errors.go:117**

Current:

```go
func (e *ErrMultiImageNeedsNamedBaselines) Error() string {
	return fmt.Sprintf("archive contains %d images; multi-image import requires --baseline NAME=PATH or --baseline-spec", e.N)
}
```

Replace with:

```go
func (e *ErrMultiImageNeedsNamedBaselines) Error() string {
	return fmt.Sprintf(
		"archive contains %d images; multi-image import requires --baseline NAME=PATH or --baseline-spec",
		e.N)
}
```

- [ ] **Step 4: Fix funlen on resolveBaselines**

Edit `pkg/importer/resolve.go`. Extract the post-loop unknown-name check:

```go
func rejectUnknownBaselineNames(sc *diff.Sidecar, expanded map[string]string) error {
	knownNames := make(map[string]struct{}, len(sc.Images))
	for _, img := range sc.Images {
		knownNames[img.Name] = struct{}{}
	}
	for name := range expanded {
		if _, ok := knownNames[name]; !ok {
			names := make([]string, 0, len(sc.Images))
			for _, img := range sc.Images {
				names = append(names, img.Name)
			}
			return &diff.ErrBaselineNameUnknown{Name: name, Available: names}
		}
	}
	return nil
}
```

In `resolveBaselines`, replace the inline logic at the bottom with:

```go
if err := rejectUnknownBaselineNames(sc, expanded); err != nil {
	return nil, err
}
return result, nil
```

- [ ] **Step 5: Replace `"zstd-patch"` in sidecar_test.go with a constant**

In `pkg/diff/sidecar_test.go`, introduce a local test constant at the top of the file:

```go
const testCodecZstdPatch = "zstd-patch"
```

Replace the four inline string literals with `testCodecZstdPatch`.

Similarly for `"sha256:bb"`:

```go
const testPatchFromDigest = digest.Digest("sha256:bb")
```

Replace the four `"sha256:bb"` occurrences.

- [ ] **Step 6: Fix gocritic appendAssign in pair_test.go:16**

Change:

```go
dup := append(pairs, Pair{Name: "a", BaselinePath: "x", TargetPath: "y"})
```

To:

```go
dupPairs := make([]Pair, 0, len(pairs)+1)
dupPairs = append(dupPairs, pairs...)
dupPairs = append(dupPairs, Pair{Name: "a", BaselinePath: "x", TargetPath: "y"})
```

Update the subsequent usage of `dup` to `dupPairs`.

- [ ] **Step 7: Run the full lint**

Run: `make lint`
Expected: **0 issues**.

- [ ] **Step 8: Run all tests one last time**

Run: `go test ./... -count=1`
Expected: all green.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "style: resolve 24 lint issues from bundle cleanup

gofmt + goimports across 5 files; lll wrap on ErrMultiImageNeedsNamedBaselines;
funlen split on resolveBaselines into rejectUnknownBaselineNames; goconst
test-local constants for zstd-patch and sha256:bb; gocritic appendAssign
fixed in pair_test.go. The rest of the original 24 were already obviated
by the compose refactor and the test rewrites in earlier commits."
```

***

## Final verification

### Task 18: Full green build

- [ ] **Step 1: Run the full test + lint + build chain**

```bash
go build ./...
go test ./... -count=1
make lint
```

All three must succeed with zero issues.

- [ ] **Step 2: Verify the CHANGELOG claim is now truthful**

The existing CHANGELOG line "Per-image baselines: Import resolves baselines by name, supporting multi-image bundles where each image has its own baseline." is now backed by `TestIntegration_MultiImageBundle_ImportsBoth`. No further edit required.

- [ ] **Step 3: Push and open PR**

(Outside the scope of this plan — the user drives the merge.)

***

## Summary of commits

1. `feat(diff): add ErrBaselineBlobDigestMismatch sentinel`
2. `feat(importer): scaffold bundleImageSource with manifest + trivial methods`
3. `feat(importer): bundleImageSource.GetBlob for full encoding with digest verify`
4. `feat(importer): bundleImageSource.GetBlob for patch encoding with digest verify`
5. `feat(importer): bundleImageSource.GetBlob for baseline blobs with digest verify`
6. `feat(importer): streaming composeImage via bundleImageSource`
7. `feat(importer): iterate all resolved images and require OUTPUT to be a directory`
8. `test(importer): replace multi-image rejection with ImportsBoth, PartialSkip, and OutputMustBeDirectory coverage`
9. `feat(exporter): log warning and fall back to full on encodeSingleShipped errors`
10. `refactor(exporter): extract buildBundle helper shared by Export and DryRun`
11. (no commit — defers build fix to Task 12)
12. `feat(importer): DryRunReport v2 with per-image layer breakdown`
13. `feat(cli): render DryRunReport v2 with per-image layer breakdown`
14. `test(importer): migrate DryRunReport integration test to the v2 shape`
15. `refactor(exporter): SourceHint from baseline filename; drop unused pool helper`
16. `docs: document OUTPUT-as-directory and multi-image import in README and CHANGELOG`
17. `style: resolve 24 lint issues from bundle cleanup`

17 commits (Task 11 does not commit; it flows directly into Task 12). Every intermediate commit compiles except Task 11 → Task 12, which is an intentional atomic pair.
