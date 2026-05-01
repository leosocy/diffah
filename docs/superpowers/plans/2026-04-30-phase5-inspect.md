# Phase 5.3 — Inspect Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich `diffah inspect` output with a per-layer encoding/size/ratio table, patch-oversized waste detection, a top-10 savings list, and a five-bucket size histogram. New content is **additive** — existing first-line shape (`archive:` / `version:` / `feature:` / `tool:` / …) and JSON keys are preserved, so old grep / jq scripts keep working.

**Architecture:** A new pure data layer (`pkg/importer/inspect_data.go`) takes a parsed `*diff.Sidecar`, one `diff.ImageEntry`, and that image's target manifest bytes, and returns an `InspectImageDetail` struct holding the four derived sections (layers, waste, top-savings, histogram). A new archive helper (`internal/archive/ReadSidecarAndManifestBlobs`) reads the sidecar plus a caller-supplied list of blob digests in one tar pass, so inspect stays "no full extract" — only the small target-manifest blobs are pulled. `cmd/inspect.go` orchestrates the read; `cmd/inspect_render.go` formats the four sections for both text and JSON output.

**Tech Stack:** Go 1.25, `github.com/spf13/cobra` (existing), `github.com/stretchr/testify/require`, `pkg/diff` (Sidecar / BlobEntry / Encoding constants), `pkg/importer` (parseManifestLayers — already exists in `manifest.go`), `internal/archive` (Extract / ReadSidecar — new helper added in Task 2), `github.com/opencontainers/go-digest`.

**Spec reference:** `docs/superpowers/specs/2026-04-29-phase5-dx-polish-design.md` §7.

**Brainstorm decisions on top of §7:**

- **D1.** A new `internal/archive.ReadSidecarAndManifestBlobs(path, digests []digest.Digest) ([]byte, map[digest.Digest][]byte, error)` reads the sidecar plus N named blob entries in one tar pass. Inspect calls it with the per-image `Target.ManifestDigest` list. Decompression is the same as `ReadSidecar` (zstd magic sniff). Missing-blob-in-archive is an error.
- **D2.** `pkg/importer.parseManifestLayers` is already package-private in the same package as the new `inspect_data.go`, so it is reused directly without exporting. The new file lives in `pkg/importer/` to share that helper.
- **D3.** `[B]` baseline-only-reuse is detected as: layer digest from target manifest NOT present in `sidecar.Blobs`. This is the only meaning of "baseline_only" — there is no `EncodingBaseline` constant; the sidecar `Blobs` map by definition only contains shipped (Full or Patch) blobs.
- **D4.** Top-N is fixed at `N = 10`. When fewer than 10 layers have non-zero savings, the rendered list is shorter (header reads `Top savings (k/10):` where `k = min(10, qualifying_count)`). Layers with `saved_bytes == 0` (e.g., a Full encoding where archive == target) are excluded from the list entirely.
- **D5.** Histogram bar width: max 12 cells; ceiling rounding so a non-empty bucket always renders ≥ 1 filled cell. Filled cell `█` (U+2588), empty cell `░` (U+2591).
- **D6.** New JSON keys are added **per image entry**, alongside the existing `name` / `target` / `baseline`: `layer_count`, `archive_layer_count`, `layers` (array), `waste` (array), `top_savings` (array), `size_histogram` (object with `buckets` / `counts`). Top-level keys (`archive`, `blobs`, `total_archive_bytes`, …) are unchanged.
- **D7.** When the target manifest is itself a manifest list / index (which the importer already rejects upstream), `parseManifestLayers` returns an error; inspect surfaces it as a per-image warning to stderr and continues to the next image.

**Out of scope** (per spec §3 / §11):

- No flag gating (`--detailed`, `--layers`, `--waste`, `--histogram`). Enriched view is always-on.
- No baseline rescan (waste detection that requires a baseline image source).
- No new waste categories beyond `patch_oversized` in v1.
- No change to the existing top-level (bundle-wide) summary output.

---

## File plan

| File | Action | Responsibility |
|---|---|---|
| `internal/archive/reader.go` | modify | Add `ReadSidecarAndManifestBlobs(path, digests)` helper |
| `internal/archive/reader_test.go` | modify | Unit tests for new helper (single + multiple blobs, missing-blob error) |
| `pkg/importer/inspect_data.go` | create | `InspectImageDetail`, `LayerRow`, `WasteEntry`, `TopSaving`, `SizeHistogram` types + `BuildInspectImageDetail` pure function |
| `pkg/importer/inspect_data_test.go` | create | Table-driven coverage of layer classification, waste detection, top-N selection, histogram bucketing |
| `cmd/inspect_render.go` | create | `renderLayerTable`, `renderWaste`, `renderTopSavings`, `renderHistogram`, `imageDetailToJSON` |
| `cmd/inspect_render_test.go` | create | Golden text fragments + JSON-shape assertions per renderer |
| `cmd/inspect.go` | modify | Read manifest blobs via new archive helper; call `BuildInspectImageDetail` per image; append per-image sections in both text and JSON paths |
| `cmd/inspect_test.go` | modify | Assert per-image sections in text mode + backward-compat for legacy lines |
| `cmd/inspect_json_test.go` | modify | Assert new per-image JSON keys + unchanged old keys |
| `cmd/testdata/schemas/inspect.snap.json` | regenerate | Snapshot updated via `DIFFAH_UPDATE_SNAPSHOTS=1` |
| `CHANGELOG.md` | modify | Phase 5.3 entry under `[Unreleased]` |

---

## Phase 1 — Branch + archive helper

### Task 1: Confirm worktree state

**Files:**
- (none — git only)

- [ ] **Step 1: Confirm working tree on `spec/phase5-inspect`**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah-phase5-inspect
git status --short
git rev-parse --abbrev-ref HEAD
git log --oneline -1
```

Expected: on `spec/phase5-inspect`, the only uncommitted file should be this plan (commit it as task-zero if so), latest commit is `f5143e5 feat(doctor): authfile / tmpdir / network / config checks (Phase 5.1) (#33)`.

- [ ] **Step 2: Verify CI baseline**

```bash
go build ./...
go test ./cmd/ ./pkg/importer/ ./internal/archive/ -count=1 -short
```

Expected: all PASS. Establishes that whatever follows is caused by *our* edits, not pre-existing breakage.

### Task 2: Add `archive.ReadSidecarAndManifestBlobs` helper

**Files:**
- Modify: `internal/archive/reader.go`
- Modify: `internal/archive/reader_test.go`

- [ ] **Step 1: Write the failing test for sidecar + single blob**

Append to `internal/archive/reader_test.go`:

```go
func TestReadSidecarAndManifestBlobs_SingleBlob(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.tar")

	manifestBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`)
	manifestDigest := digest.FromBytes(manifestBytes)

	sc := minimalSidecarForBlobTest(t, manifestDigest, len(manifestBytes))
	scBytes, err := json.Marshal(sc)
	require.NoError(t, err)

	writeBundleTar(t, out, map[string][]byte{
		diff.SidecarFilename: scBytes,
		"blobs/" + manifestDigest.Algorithm().String() + "/" + manifestDigest.Encoded(): manifestBytes,
	})

	gotSidecar, gotBlobs, err := ReadSidecarAndManifestBlobs(out, []digest.Digest{manifestDigest})
	require.NoError(t, err)
	require.JSONEq(t, string(scBytes), string(gotSidecar))
	require.Len(t, gotBlobs, 1)
	require.Equal(t, manifestBytes, gotBlobs[manifestDigest])
}

func minimalSidecarForBlobTest(t *testing.T, mfDigest digest.Digest, mfSize int) *diff.Sidecar {
	t.Helper()
	return &diff.Sidecar{
		Version:     diff.SchemaVersionV1,
		Feature:     diff.FeatureBundle,
		Tool:        "diffah",
		ToolVersion: "v0-test",
		CreatedAt:   time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{
				ManifestDigest: mfDigest,
				ManifestSize:   int64(mfSize),
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
			Baseline: diff.BaselineRef{
				ManifestDigest: digest.Digest("sha256:" + strings.Repeat("b", 64)),
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
		}},
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest: {Size: int64(mfSize), MediaType: "application/vnd.oci.image.manifest.v1+json", Encoding: diff.EncodingFull, ArchiveSize: int64(mfSize)},
		},
	}
}

func writeBundleTar(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	tw := tar.NewWriter(f)
	for name, data := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name, Size: int64(len(data)), Mode: 0o644, Format: tar.FormatPAX,
		}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
}
```

Imports needed in `reader_test.go` (add to existing block; do not duplicate):

```go
import (
	"archive/tar"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/archive/ -run TestReadSidecarAndManifestBlobs_SingleBlob -count=1 -v
```

Expected: FAIL with `undefined: ReadSidecarAndManifestBlobs`.

- [ ] **Step 3: Implement `ReadSidecarAndManifestBlobs`**

Append to `internal/archive/reader.go`:

```go
// ReadSidecarAndManifestBlobs returns the sidecar bytes and the bytes of every
// blob in digests, in a single tar pass. Used by `diffah inspect` to enrich
// per-image output without extracting the full archive. Every digest in the
// argument MUST appear as a `blobs/<algo>/<encoded>` entry; missing-blob is
// an error. The returned map is keyed by digest, never nil.
func ReadSidecarAndManifestBlobs(archivePath string, digests []digest.Digest) ([]byte, map[digest.Digest][]byte, error) {
	want := make(map[string]digest.Digest, len(digests))
	for _, d := range digests {
		want[blobTarPath(d)] = d
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", archivePath, err)
	}
	defer f.Close()

	stream, closer, err := openDecompressed(f)
	if err != nil {
		return nil, nil, err
	}
	if closer != nil {
		defer closer()
	}

	tr := tar.NewReader(stream)
	var sidecar []byte
	got := make(map[digest.Digest][]byte, len(digests))

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Name == diff.SidecarFilename {
			sidecar, err = io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("read sidecar: %w", err)
			}
			continue
		}
		if d, ok := want[hdr.Name]; ok {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("read %s: %w", hdr.Name, err)
			}
			got[d] = data
		}
	}

	if sidecar == nil {
		return nil, nil, &diff.ErrNotADiffahArchive{Path: archivePath}
	}
	for _, d := range digests {
		if _, ok := got[d]; !ok {
			return nil, nil, fmt.Errorf("blob %s not found in archive %s", d, archivePath)
		}
	}
	return sidecar, got, nil
}

// blobTarPath returns the in-archive tar entry name for a blob, matching
// the writer convention in pkg/exporter/writer.go.
func blobTarPath(d digest.Digest) string {
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return filepath.Join("blobs", string(d))
	}
	return filepath.Join("blobs", parts[0], parts[1])
}
```

Ensure `path/filepath`, `strings`, and `github.com/opencontainers/go-digest` are imported in `reader.go`.

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/archive/ -run TestReadSidecarAndManifestBlobs_SingleBlob -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Add multi-blob + missing-blob cases**

Append to `internal/archive/reader_test.go`:

```go
func TestReadSidecarAndManifestBlobs_MultipleBlobs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.tar")

	mf1 := []byte(`{"schemaVersion":2,"layers":[]}`)
	mf2 := []byte(`{"schemaVersion":2,"layers":[{"digest":"sha256:abc","size":7}]}`)
	d1 := digest.FromBytes(mf1)
	d2 := digest.FromBytes(mf2)

	sc := minimalSidecarForBlobTest(t, d1, len(mf1))
	sc.Images = append(sc.Images, diff.ImageEntry{
		Name: "svc-2",
		Target: diff.TargetRef{
			ManifestDigest: d2, ManifestSize: int64(len(mf2)),
			MediaType: "application/vnd.oci.image.manifest.v1+json",
		},
		Baseline: diff.BaselineRef{
			ManifestDigest: digest.Digest("sha256:" + strings.Repeat("c", 64)),
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
	})
	sc.Blobs[d2] = diff.BlobEntry{
		Size: int64(len(mf2)), MediaType: "application/vnd.oci.image.manifest.v1+json",
		Encoding: diff.EncodingFull, ArchiveSize: int64(len(mf2)),
	}
	scBytes, err := json.Marshal(sc)
	require.NoError(t, err)

	writeBundleTar(t, out, map[string][]byte{
		diff.SidecarFilename: scBytes,
		"blobs/" + d1.Algorithm().String() + "/" + d1.Encoded(): mf1,
		"blobs/" + d2.Algorithm().String() + "/" + d2.Encoded(): mf2,
	})

	_, blobs, err := ReadSidecarAndManifestBlobs(out, []digest.Digest{d1, d2})
	require.NoError(t, err)
	require.Equal(t, mf1, blobs[d1])
	require.Equal(t, mf2, blobs[d2])
}

func TestReadSidecarAndManifestBlobs_MissingBlobReturnsError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.tar")

	mf := []byte(`{"schemaVersion":2,"layers":[]}`)
	d := digest.FromBytes(mf)
	sc := minimalSidecarForBlobTest(t, d, len(mf))
	scBytes, err := json.Marshal(sc)
	require.NoError(t, err)

	writeBundleTar(t, out, map[string][]byte{diff.SidecarFilename: scBytes})

	_, _, err = ReadSidecarAndManifestBlobs(out, []digest.Digest{d})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in archive")
}
```

- [ ] **Step 6: Run all archive tests**

```bash
go test ./internal/archive/ -count=1 -v
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/archive/reader.go internal/archive/reader_test.go
git commit -m "feat(archive): ReadSidecarAndManifestBlobs reads sidecar + named blobs in one pass"
```

---

## Phase 2 — Pure data layer (`pkg/importer/inspect_data.go`)

### Task 3: Define detail types

**Files:**
- Create: `pkg/importer/inspect_data.go`

- [ ] **Step 1: Create the file**

```go
package importer

import "github.com/opencontainers/go-digest"

// LayerRowKind classifies a target-manifest layer by how it was shipped.
type LayerRowKind string

const (
	LayerKindFull         LayerRowKind = "full"
	LayerKindPatch        LayerRowKind = "patch"
	LayerKindBaselineOnly LayerRowKind = "baseline_only"
)

// LayerRow describes one row of the per-image layer table.
type LayerRow struct {
	Digest      digest.Digest
	Kind        LayerRowKind
	TargetSize  int64
	ArchiveSize int64
	PatchFrom   digest.Digest
}

func (r LayerRow) SavedBytes() int64 {
	if r.Kind == LayerKindBaselineOnly {
		return r.TargetSize
	}
	return r.TargetSize - r.ArchiveSize
}

func (r LayerRow) SavedRatio() float64 {
	if r.TargetSize == 0 {
		return 0
	}
	return float64(r.SavedBytes()) / float64(r.TargetSize)
}

func (r LayerRow) Ratio() float64 {
	if r.Kind == LayerKindBaselineOnly || r.TargetSize == 0 {
		return 0
	}
	return float64(r.ArchiveSize) / float64(r.TargetSize)
}

type WasteKind string

const WasteKindPatchOversized WasteKind = "patch_oversized"

type WasteEntry struct {
	Kind        WasteKind
	Digest      digest.Digest
	ArchiveSize int64
	TargetSize  int64
}

type TopSaving struct {
	Digest     digest.Digest
	SavedBytes int64
	SavedRatio float64
}

type SizeHistogram struct {
	Buckets []string
	Counts  []int
}

type InspectImageDetail struct {
	Name              string
	ManifestDigest    digest.Digest
	LayerCount        int
	ArchiveLayerCount int
	Layers            []LayerRow
	Waste             []WasteEntry
	TopSavings        []TopSaving
	Histogram         SizeHistogram
}
```

- [ ] **Step 2: Verify the file compiles**

```bash
go build ./pkg/importer/
```

- [ ] **Step 3: Commit**

```bash
git add pkg/importer/inspect_data.go
git commit -m "feat(importer): inspect detail types (LayerRow, WasteEntry, TopSaving, SizeHistogram)"
```

### Task 4: Implement layer-row builder (F / P / B classification)

**Files:**
- Modify: `pkg/importer/inspect_data.go`
- Create: `pkg/importer/inspect_data_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/importer/inspect_data_test.go`:

```go
package importer

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func d(s string) digest.Digest { return digest.Digest("sha256:" + s) }

func TestBuildLayerRows_ClassifiesFullPatchAndBaselineOnly(t *testing.T) {
	manifestLayers := []LayerRef{
		{Digest: d("aaa"), Size: 100},
		{Digest: d("bbb"), Size: 200},
		{Digest: d("ccc"), Size: 300},
	}
	blobs := map[digest.Digest]diff.BlobEntry{
		d("aaa"): {Size: 100, Encoding: diff.EncodingFull, ArchiveSize: 100},
		d("bbb"): {Size: 200, Encoding: diff.EncodingPatch, Codec: "zstd-patch", PatchFromDigest: d("ref1"), ArchiveSize: 50},
		// ccc absent → baseline-only
	}

	rows := buildLayerRows(manifestLayers, blobs)
	require.Len(t, rows, 3)

	require.Equal(t, LayerKindFull, rows[0].Kind)
	require.EqualValues(t, 100, rows[0].TargetSize)
	require.EqualValues(t, 100, rows[0].ArchiveSize)
	require.EqualValues(t, 0, rows[0].SavedBytes())

	require.Equal(t, LayerKindPatch, rows[1].Kind)
	require.EqualValues(t, 200, rows[1].TargetSize)
	require.EqualValues(t, 50, rows[1].ArchiveSize)
	require.Equal(t, d("ref1"), rows[1].PatchFrom)
	require.EqualValues(t, 150, rows[1].SavedBytes())

	require.Equal(t, LayerKindBaselineOnly, rows[2].Kind)
	require.EqualValues(t, 300, rows[2].TargetSize)
	require.EqualValues(t, 0, rows[2].ArchiveSize)
	require.EqualValues(t, 300, rows[2].SavedBytes())
}
```

- [ ] **Step 2: Verify it fails**

```bash
go test ./pkg/importer/ -run TestBuildLayerRows_ -count=1 -v
```

- [ ] **Step 3: Implement `buildLayerRows`**

Add `"github.com/leosocy/diffah/pkg/diff"` to `pkg/importer/inspect_data.go` import block, then append:

```go
func buildLayerRows(manifestLayers []LayerRef, blobs map[digest.Digest]diff.BlobEntry) []LayerRow {
	rows := make([]LayerRow, 0, len(manifestLayers))
	for _, l := range manifestLayers {
		b, ok := blobs[l.Digest]
		if !ok {
			rows = append(rows, LayerRow{Digest: l.Digest, Kind: LayerKindBaselineOnly, TargetSize: l.Size})
			continue
		}
		row := LayerRow{Digest: l.Digest, TargetSize: l.Size, ArchiveSize: b.ArchiveSize}
		switch b.Encoding {
		case diff.EncodingFull:
			row.Kind = LayerKindFull
		case diff.EncodingPatch:
			row.Kind = LayerKindPatch
			row.PatchFrom = b.PatchFromDigest
		default:
			row.Kind = LayerKindFull
		}
		rows = append(rows, row)
	}
	return rows
}
```

- [ ] **Step 4: Verify it passes**

```bash
go test ./pkg/importer/ -run TestBuildLayerRows_ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/inspect_data.go pkg/importer/inspect_data_test.go
git commit -m "feat(importer): buildLayerRows classifies layers as full/patch/baseline-only"
```

### Task 5: Implement waste detection

**Files:**
- Modify: `pkg/importer/inspect_data.go`
- Modify: `pkg/importer/inspect_data_test.go`

- [ ] **Step 1: Write failing tests**

Append to `pkg/importer/inspect_data_test.go`:

```go
func TestDetectWaste_PatchOversizedFlagsArchiveAtOrAboveTarget(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("ok"), Kind: LayerKindPatch, TargetSize: 1000, ArchiveSize: 100},
		{Digest: d("eq"), Kind: LayerKindPatch, TargetSize: 500, ArchiveSize: 500},
		{Digest: d("over"), Kind: LayerKindPatch, TargetSize: 500, ArchiveSize: 600},
		{Digest: d("full"), Kind: LayerKindFull, TargetSize: 500, ArchiveSize: 500},
		{Digest: d("base"), Kind: LayerKindBaselineOnly, TargetSize: 500, ArchiveSize: 0},
	}
	w := detectWaste(rows)
	require.Len(t, w, 2)
	require.Equal(t, WasteKindPatchOversized, w[0].Kind)
	require.Equal(t, d("eq"), w[0].Digest)
	require.Equal(t, d("over"), w[1].Digest)
}

func TestDetectWaste_NoneWhenAllPatchesProfitable(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("a"), Kind: LayerKindPatch, TargetSize: 1000, ArchiveSize: 100},
		{Digest: d("b"), Kind: LayerKindPatch, TargetSize: 2000, ArchiveSize: 200},
	}
	require.Empty(t, detectWaste(rows))
}
```

- [ ] **Step 2: Verify they fail**

```bash
go test ./pkg/importer/ -run TestDetectWaste_ -count=1 -v
```

- [ ] **Step 3: Implement `detectWaste`**

Append to `pkg/importer/inspect_data.go`:

```go
func detectWaste(rows []LayerRow) []WasteEntry {
	var out []WasteEntry
	for _, r := range rows {
		if r.Kind == LayerKindPatch && r.ArchiveSize >= r.TargetSize {
			out = append(out, WasteEntry{
				Kind:        WasteKindPatchOversized,
				Digest:      r.Digest,
				ArchiveSize: r.ArchiveSize,
				TargetSize:  r.TargetSize,
			})
		}
	}
	return out
}
```

- [ ] **Step 4: Verify they pass**

```bash
go test ./pkg/importer/ -run TestDetectWaste_ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/inspect_data.go pkg/importer/inspect_data_test.go
git commit -m "feat(importer): detectWaste flags patch_oversized layers"
```

### Task 6: Implement top-N savings

**Files:**
- Modify: `pkg/importer/inspect_data.go`
- Modify: `pkg/importer/inspect_data_test.go`

- [ ] **Step 1: Write failing tests**

Append to `pkg/importer/inspect_data_test.go`:

```go
func TestComputeTopSavings_SortsBySavedBytesDesc(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("small"), Kind: LayerKindPatch, TargetSize: 100, ArchiveSize: 50},
		{Digest: d("big"), Kind: LayerKindPatch, TargetSize: 1000, ArchiveSize: 100},
		{Digest: d("mid"), Kind: LayerKindPatch, TargetSize: 500, ArchiveSize: 100},
	}
	top := computeTopSavings(rows, 10)
	require.Len(t, top, 3)
	require.Equal(t, d("big"), top[0].Digest)
	require.EqualValues(t, 900, top[0].SavedBytes)
	require.InDelta(t, 0.9, top[0].SavedRatio, 0.001)
	require.Equal(t, d("mid"), top[1].Digest)
	require.Equal(t, d("small"), top[2].Digest)
}

func TestComputeTopSavings_OmitsZeroSavingRowsAndCapsAtN(t *testing.T) {
	var rows []LayerRow
	for i := 0; i < 15; i++ {
		rows = append(rows, LayerRow{
			Digest: d(string(rune('a' + i))), Kind: LayerKindPatch,
			TargetSize: 100, ArchiveSize: int64(100 - i),
		})
	}
	top := computeTopSavings(rows, 10)
	require.Len(t, top, 10)
	require.EqualValues(t, 14, top[0].SavedBytes)
	require.EqualValues(t, 5, top[9].SavedBytes)
}

func TestComputeTopSavings_TieBreakerByDigestLexicographic(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("zzz"), Kind: LayerKindFull, TargetSize: 100, ArchiveSize: 60},
		{Digest: d("aaa"), Kind: LayerKindFull, TargetSize: 100, ArchiveSize: 60},
		{Digest: d("mmm"), Kind: LayerKindFull, TargetSize: 100, ArchiveSize: 60},
	}
	top := computeTopSavings(rows, 10)
	require.Equal(t, d("aaa"), top[0].Digest)
	require.Equal(t, d("mmm"), top[1].Digest)
	require.Equal(t, d("zzz"), top[2].Digest)
}

func TestComputeTopSavings_BaselineOnlyContributesFullTargetSize(t *testing.T) {
	rows := []LayerRow{
		{Digest: d("base"), Kind: LayerKindBaselineOnly, TargetSize: 1000},
		{Digest: d("patch"), Kind: LayerKindPatch, TargetSize: 1000, ArchiveSize: 200},
	}
	top := computeTopSavings(rows, 10)
	require.Equal(t, d("base"), top[0].Digest)
	require.EqualValues(t, 1000, top[0].SavedBytes)
}
```

- [ ] **Step 2: Verify they fail**

```bash
go test ./pkg/importer/ -run TestComputeTopSavings_ -count=1 -v
```

- [ ] **Step 3: Implement `computeTopSavings`**

Add `"sort"` to `pkg/importer/inspect_data.go` import block, then append:

```go
func computeTopSavings(rows []LayerRow, n int) []TopSaving {
	saved := make([]TopSaving, 0, len(rows))
	for _, r := range rows {
		s := r.SavedBytes()
		if s <= 0 {
			continue
		}
		saved = append(saved, TopSaving{Digest: r.Digest, SavedBytes: s, SavedRatio: r.SavedRatio()})
	}
	sort.SliceStable(saved, func(i, j int) bool {
		if saved[i].SavedBytes != saved[j].SavedBytes {
			return saved[i].SavedBytes > saved[j].SavedBytes
		}
		return saved[i].Digest < saved[j].Digest
	})
	if len(saved) > n {
		saved = saved[:n]
	}
	return saved
}
```

- [ ] **Step 4: Verify they pass**

```bash
go test ./pkg/importer/ -run TestComputeTopSavings_ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/inspect_data.go pkg/importer/inspect_data_test.go
git commit -m "feat(importer): computeTopSavings ranks layers by saved bytes"
```

### Task 7: Implement histogram

**Files:**
- Modify: `pkg/importer/inspect_data.go`
- Modify: `pkg/importer/inspect_data_test.go`

- [ ] **Step 1: Write failing tests**

Append to `pkg/importer/inspect_data_test.go`:

```go
func TestComputeHistogram_BucketBoundariesAreHalfOpen(t *testing.T) {
	const (
		MiB = 1 << 20
		GiB = 1 << 30
	)
	rows := []LayerRow{
		{Digest: d("a"), TargetSize: 0},
		{Digest: d("b"), TargetSize: MiB - 1},
		{Digest: d("c"), TargetSize: MiB},
		{Digest: d("d"), TargetSize: 10 * MiB},
		{Digest: d("e"), TargetSize: 100*MiB - 1},
		{Digest: d("f"), TargetSize: 100 * MiB},
		{Digest: d("g"), TargetSize: GiB},
	}
	h := computeHistogram(rows)
	require.Equal(t, []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"}, h.Buckets)
	require.Equal(t, []int{2, 1, 2, 1, 1}, h.Counts)
}

func TestComputeHistogram_EmptyInputProducesAllZero(t *testing.T) {
	h := computeHistogram(nil)
	require.Equal(t, []int{0, 0, 0, 0, 0}, h.Counts)
}
```

- [ ] **Step 2: Verify they fail**

```bash
go test ./pkg/importer/ -run TestComputeHistogram_ -count=1 -v
```

- [ ] **Step 3: Implement `computeHistogram`**

Append to `pkg/importer/inspect_data.go`:

```go
const (
	histMiB = 1 << 20
	histGiB = 1 << 30
)

var histogramBucketLabels = []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"}

func histogramBucketIndex(size int64) int {
	switch {
	case size < histMiB:
		return 0
	case size < 10*histMiB:
		return 1
	case size < 100*histMiB:
		return 2
	case size < histGiB:
		return 3
	default:
		return 4
	}
}

func computeHistogram(rows []LayerRow) SizeHistogram {
	counts := make([]int, len(histogramBucketLabels))
	for _, r := range rows {
		counts[histogramBucketIndex(r.TargetSize)]++
	}
	labels := make([]string, len(histogramBucketLabels))
	copy(labels, histogramBucketLabels)
	return SizeHistogram{Buckets: labels, Counts: counts}
}
```

- [ ] **Step 4: Verify they pass**

```bash
go test ./pkg/importer/ -run TestComputeHistogram_ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/inspect_data.go pkg/importer/inspect_data_test.go
git commit -m "feat(importer): computeHistogram buckets layer sizes into 5 log-scale ranges"
```

### Task 8: Wire `BuildInspectImageDetail` orchestrator

**Files:**
- Modify: `pkg/importer/inspect_data.go`
- Modify: `pkg/importer/inspect_data_test.go`

- [ ] **Step 1: Write failing tests**

Add `"encoding/json"`, `"strings"`, `"time"` to existing import block of `pkg/importer/inspect_data_test.go`. Then append:

```go
const ociMediaType = "application/vnd.oci.image.manifest.v1+json"

func fakeOCIManifest(t *testing.T, layers []LayerRef) []byte {
	t.Helper()
	type layer struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	}
	body := struct {
		SchemaVersion int     `json:"schemaVersion"`
		MediaType     string  `json:"mediaType"`
		Config        layer   `json:"config"`
		Layers        []layer `json:"layers"`
	}{
		SchemaVersion: 2,
		MediaType:     ociMediaType,
		Config:        layer{MediaType: "application/vnd.oci.image.config.v1+json", Digest: "sha256:" + strings.Repeat("c", 64), Size: 10},
	}
	for _, l := range layers {
		body.Layers = append(body.Layers, layer{
			MediaType: "application/vnd.oci.image.layer.v1.tar",
			Digest:    string(l.Digest),
			Size:      l.Size,
		})
	}
	out, err := json.Marshal(body)
	require.NoError(t, err)
	return out
}

func TestBuildInspectImageDetail_EndToEnd(t *testing.T) {
	manifestLayers := []LayerRef{
		{Digest: d(strings.Repeat("a", 64)), Size: 12 * (1 << 20)},
		{Digest: d(strings.Repeat("b", 64)), Size: 8 * (1 << 20)},
		{Digest: d(strings.Repeat("c", 64)), Size: 5 * (1 << 20)},
	}
	mfBytes := fakeOCIManifest(t, manifestLayers)
	mfDigest := digest.FromBytes(mfBytes)

	sc := &diff.Sidecar{
		Version: diff.SchemaVersionV1, Feature: diff.FeatureBundle, Tool: "diffah",
		ToolVersion: "test", CreatedAt: time.Now(), Platform: "linux/amd64",
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{ManifestDigest: mfDigest, ManifestSize: int64(len(mfBytes)), MediaType: ociMediaType},
			Baseline: diff.BaselineRef{ManifestDigest: digest.Digest("sha256:" + strings.Repeat("0", 64)), MediaType: ociMediaType},
		}},
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest:                   {Size: int64(len(mfBytes)), MediaType: ociMediaType, Encoding: diff.EncodingFull, ArchiveSize: int64(len(mfBytes))},
			d(strings.Repeat("a", 64)): {Size: 12 * (1 << 20), Encoding: diff.EncodingFull, ArchiveSize: 12 * (1 << 20)},
			d(strings.Repeat("b", 64)): {Size: 8 * (1 << 20), Encoding: diff.EncodingPatch, Codec: "zstd-patch", PatchFromDigest: d(strings.Repeat("9", 64)), ArchiveSize: 500 * 1024},
		},
	}

	detail, err := BuildInspectImageDetail(sc, sc.Images[0], mfBytes)
	require.NoError(t, err)
	require.Equal(t, "svc", detail.Name)
	require.Equal(t, mfDigest, detail.ManifestDigest)
	require.Equal(t, 3, detail.LayerCount)
	require.Equal(t, 2, detail.ArchiveLayerCount)
	require.Len(t, detail.Layers, 3)
	require.Equal(t, LayerKindFull, detail.Layers[0].Kind)
	require.Equal(t, LayerKindPatch, detail.Layers[1].Kind)
	require.Equal(t, LayerKindBaselineOnly, detail.Layers[2].Kind)
	require.Empty(t, detail.Waste)
	require.NotEmpty(t, detail.TopSavings)
	require.Equal(t, []int{0, 1, 2, 0, 0}, detail.Histogram.Counts)
}

func TestBuildInspectImageDetail_RejectsManifestList(t *testing.T) {
	sc := &diff.Sidecar{
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{
				ManifestDigest: d("00"), MediaType: "application/vnd.oci.image.index.v1+json",
			},
		}},
	}
	_, err := BuildInspectImageDetail(sc, sc.Images[0], []byte(`{"schemaVersion":2,"manifests":[]}`))
	require.Error(t, err)
}
```

- [ ] **Step 2: Verify they fail**

```bash
go test ./pkg/importer/ -run TestBuildInspectImageDetail_ -count=1 -v
```

- [ ] **Step 3: Implement the orchestrator**

Add `"fmt"` to `pkg/importer/inspect_data.go` import block, then append:

```go
const inspectTopN = 10

// BuildInspectImageDetail derives the per-image detail block. Pure: no I/O.
func BuildInspectImageDetail(sc *diff.Sidecar, img diff.ImageEntry, manifestBytes []byte) (InspectImageDetail, error) {
	manifestLayers, _, err := parseManifestLayers(manifestBytes, img.Target.MediaType)
	if err != nil {
		return InspectImageDetail{}, fmt.Errorf("inspect detail for %q: %w", img.Name, err)
	}
	rows := buildLayerRows(manifestLayers, sc.Blobs)

	archiveCount := 0
	for _, r := range rows {
		if r.Kind != LayerKindBaselineOnly {
			archiveCount++
		}
	}

	return InspectImageDetail{
		Name:              img.Name,
		ManifestDigest:    img.Target.ManifestDigest,
		LayerCount:        len(rows),
		ArchiveLayerCount: archiveCount,
		Layers:            rows,
		Waste:             detectWaste(rows),
		TopSavings:        computeTopSavings(rows, inspectTopN),
		Histogram:         computeHistogram(rows),
	}, nil
}
```

- [ ] **Step 4: Verify all importer tests pass**

```bash
go test ./pkg/importer/ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add pkg/importer/inspect_data.go pkg/importer/inspect_data_test.go
git commit -m "feat(importer): BuildInspectImageDetail composes per-image enrichment"
```

---

## Phase 3 — Renderers (`cmd/inspect_render.go`)

### Task 9: Per-layer table renderer

**Files:**
- Create: `cmd/inspect_render.go`
- Create: `cmd/inspect_render_test.go`

- [ ] **Step 1: Write failing test**

Create `cmd/inspect_render_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/importer"
)

func dig(s string) digest.Digest { return digest.Digest("sha256:" + s) }

func TestRenderLayerTable_FullPatchBaseline(t *testing.T) {
	detail := importer.InspectImageDetail{
		Layers: []importer.LayerRow{
			{Digest: dig(strings.Repeat("a", 64)), Kind: importer.LayerKindFull, TargetSize: 13_000_000, ArchiveSize: 13_000_000},
			{Digest: dig(strings.Repeat("b", 64)), Kind: importer.LayerKindPatch, TargetSize: 8_000_000, ArchiveSize: 500_000, PatchFrom: dig(strings.Repeat("z", 64))},
			{Digest: dig(strings.Repeat("c", 64)), Kind: importer.LayerKindBaselineOnly, TargetSize: 5_000_000},
		},
	}

	var buf bytes.Buffer
	renderLayerTable(&buf, detail)
	out := buf.String()

	require.Contains(t, out, "Layers (target manifest order):")
	require.Contains(t, out, "[F]")
	require.Contains(t, out, "[P]")
	require.Contains(t, out, "[B]")
	require.Contains(t, out, "1.00× — full")
	require.Contains(t, out, "0.06× — patch from sha256:")
	require.Contains(t, out, "— baseline-only")
}
```

- [ ] **Step 2: Verify it fails**

```bash
go test ./cmd/ -run TestRenderLayerTable_ -count=1 -v
```

- [ ] **Step 3: Implement `renderLayerTable`**

Create `cmd/inspect_render.go`:

```go
package cmd

import (
	"fmt"
	"io"

	"github.com/leosocy/diffah/pkg/importer"
)

func renderLayerTable(w io.Writer, d importer.InspectImageDetail) {
	if len(d.Layers) == 0 {
		return
	}
	fmt.Fprintln(w, "  Layers (target manifest order):")
	for _, r := range d.Layers {
		tag := layerTag(r.Kind)
		switch r.Kind {
		case importer.LayerKindFull:
			fmt.Fprintf(w, "    %s  %s %s target / %s archive  (%.2f× — full)\n",
				tag, r.Digest, humanBytes(r.TargetSize), humanBytes(r.ArchiveSize), r.Ratio())
		case importer.LayerKindPatch:
			fmt.Fprintf(w, "    %s  %s %s target / %s archive  (%.2f× — patch from %s)\n",
				tag, r.Digest, humanBytes(r.TargetSize), humanBytes(r.ArchiveSize), r.Ratio(), r.PatchFrom)
		case importer.LayerKindBaselineOnly:
			fmt.Fprintf(w, "    %s  %s %s target /     0 B archive  (— baseline-only)\n",
				tag, r.Digest, humanBytes(r.TargetSize))
		}
	}
}

func layerTag(k importer.LayerRowKind) string {
	switch k {
	case importer.LayerKindFull:
		return "[F]"
	case importer.LayerKindPatch:
		return "[P]"
	case importer.LayerKindBaselineOnly:
		return "[B]"
	}
	return "[?]"
}

func humanBytes(n int64) string {
	const (
		KiB = 1 << 10
		MiB = 1 << 20
		GiB = 1 << 30
	)
	switch {
	case n < KiB:
		return fmt.Sprintf("%d B", n)
	case n < MiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(KiB))
	case n < GiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(MiB))
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(GiB))
	}
}
```

- [ ] **Step 4: Verify it passes**

```bash
go test ./cmd/ -run TestRenderLayerTable_ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect_render.go cmd/inspect_render_test.go
git commit -m "feat(inspect): renderLayerTable formats per-layer F/P/B table"
```

### Task 10: Waste renderer

**Files:**
- Modify: `cmd/inspect_render.go`
- Modify: `cmd/inspect_render_test.go`

- [ ] **Step 1: Write failing test**

Append to `cmd/inspect_render_test.go`:

```go
func TestRenderWaste_PatchOversizedShowsHint(t *testing.T) {
	detail := importer.InspectImageDetail{
		Waste: []importer.WasteEntry{
			{Kind: importer.WasteKindPatchOversized, Digest: dig(strings.Repeat("y", 64)), ArchiveSize: 12_000_000, TargetSize: 8_000_000},
		},
	}
	var buf bytes.Buffer
	renderWaste(&buf, detail)
	out := buf.String()

	require.Contains(t, out, "Waste:")
	require.Contains(t, out, "patch-oversized")
	require.Contains(t, out, "archive 11.4 MiB ≥ target 7.6 MiB")
	require.Contains(t, out, "patch is bigger than full")
}

func TestRenderWaste_NoneWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderWaste(&buf, importer.InspectImageDetail{})
	require.Contains(t, buf.String(), "Waste:")
	require.Contains(t, buf.String(), "    none")
}
```

(If the size formatting in your `humanBytes` rounds differently from `11.4 MiB` / `7.6 MiB`, adjust assertions to match what the function produces — read the failure diff to find the exact string.)

- [ ] **Step 2: Verify they fail**

```bash
go test ./cmd/ -run TestRenderWaste_ -count=1 -v
```

- [ ] **Step 3: Implement `renderWaste`**

Append to `cmd/inspect_render.go`:

```go
func renderWaste(w io.Writer, d importer.InspectImageDetail) {
	fmt.Fprintln(w, "  Waste:")
	if len(d.Waste) == 0 {
		fmt.Fprintln(w, "    none")
		return
	}
	for _, ws := range d.Waste {
		switch ws.Kind {
		case importer.WasteKindPatchOversized:
			fmt.Fprintf(w, "    patch-oversized  %s archive %s ≥ target %s\n",
				ws.Digest, humanBytes(ws.ArchiveSize), humanBytes(ws.TargetSize))
			fmt.Fprintln(w, "                   (patch is bigger than full; force --intra-layer=off for this layer)")
		}
	}
}
```

- [ ] **Step 4: Verify they pass**

```bash
go test ./cmd/ -run TestRenderWaste_ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect_render.go cmd/inspect_render_test.go
git commit -m "feat(inspect): renderWaste prints patch_oversized rows or 'none'"
```

### Task 11: Top-N savings renderer

**Files:**
- Modify: `cmd/inspect_render.go`
- Modify: `cmd/inspect_render_test.go`

- [ ] **Step 1: Write failing test**

Append to `cmd/inspect_render_test.go`:

```go
func TestRenderTopSavings_PrintsRankedRows(t *testing.T) {
	detail := importer.InspectImageDetail{
		TopSavings: []importer.TopSaving{
			{Digest: dig(strings.Repeat("x", 64)), SavedBytes: 7_500_000, SavedRatio: 0.94},
			{Digest: dig(strings.Repeat("y", 64)), SavedBytes: 1_500_000, SavedRatio: 0.50},
		},
	}
	var buf bytes.Buffer
	renderTopSavings(&buf, detail)
	out := buf.String()

	require.Contains(t, out, "Top savings (2/10):")
	require.Contains(t, out, "1. sha256:")
	require.Contains(t, out, "saved 7.2 MiB (94 %)")
	require.Contains(t, out, "2. sha256:")
}

func TestRenderTopSavings_OmittedWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderTopSavings(&buf, importer.InspectImageDetail{})
	require.Empty(t, buf.String())
}
```

- [ ] **Step 2: Verify it fails**

```bash
go test ./cmd/ -run TestRenderTopSavings_ -count=1 -v
```

- [ ] **Step 3: Implement `renderTopSavings`**

Append to `cmd/inspect_render.go`:

```go
const inspectTopNDisplay = 10

func renderTopSavings(w io.Writer, d importer.InspectImageDetail) {
	if len(d.TopSavings) == 0 {
		return
	}
	fmt.Fprintf(w, "  Top savings (%d/%d):\n", len(d.TopSavings), inspectTopNDisplay)
	for i, s := range d.TopSavings {
		fmt.Fprintf(w, "    %d. %s saved %s (%d %%)\n",
			i+1, s.Digest, humanBytes(s.SavedBytes), int(s.SavedRatio*100+0.5))
	}
}
```

- [ ] **Step 4: Verify it passes**

```bash
go test ./cmd/ -run TestRenderTopSavings_ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect_render.go cmd/inspect_render_test.go
git commit -m "feat(inspect): renderTopSavings prints ranked top-10 savings"
```

### Task 12: Histogram renderer

**Files:**
- Modify: `cmd/inspect_render.go`
- Modify: `cmd/inspect_render_test.go`

- [ ] **Step 1: Write failing test**

Append to `cmd/inspect_render_test.go`:

```go
func TestRenderHistogram_FilledAndEmptyBars(t *testing.T) {
	detail := importer.InspectImageDetail{
		Histogram: importer.SizeHistogram{
			Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
			Counts:  []int{2, 6, 3, 0, 0},
		},
	}
	var buf bytes.Buffer
	renderHistogram(&buf, detail)
	out := buf.String()

	require.Contains(t, out, "Layer-size histogram (target bytes):")
	require.Contains(t, out, "< 1 MiB")
	require.Contains(t, out, "1–10 MiB")
	require.Contains(t, out, "10–100 MiB")
	require.Contains(t, out, "100 MiB–1 GiB")
	require.Contains(t, out, "≥ 1 GiB")
	require.Contains(t, out, "█")
	require.Contains(t, out, "░")
}

func TestRenderHistogram_AllZeroPrintsAllEmptyBars(t *testing.T) {
	detail := importer.InspectImageDetail{
		Histogram: importer.SizeHistogram{
			Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
			Counts:  []int{0, 0, 0, 0, 0},
		},
	}
	var buf bytes.Buffer
	renderHistogram(&buf, detail)
	require.Contains(t, buf.String(), "░░░░░░░░░░░░")
}
```

- [ ] **Step 2: Verify they fail**

```bash
go test ./cmd/ -run TestRenderHistogram_ -count=1 -v
```

- [ ] **Step 3: Implement `renderHistogram`**

Append to `cmd/inspect_render.go`:

```go
const histogramBarWidth = 12

var histogramDisplayLabels = []string{
	"< 1 MiB",
	"1–10 MiB",
	"10–100 MiB",
	"100 MiB–1 GiB",
	"≥ 1 GiB",
}

func renderHistogram(w io.Writer, d importer.InspectImageDetail) {
	fmt.Fprintln(w, "  Layer-size histogram (target bytes):")
	maxCount := 0
	for _, c := range d.Histogram.Counts {
		if c > maxCount {
			maxCount = c
		}
	}
	for i, count := range d.Histogram.Counts {
		label := histogramDisplayLabels[i]
		filled := 0
		if maxCount > 0 {
			filled = (histogramBarWidth*count + maxCount - 1) / maxCount
			if filled > histogramBarWidth {
				filled = histogramBarWidth
			}
		}
		fmt.Fprintf(w, "    %-14s│%s  %d\n", label, buildBar(filled, histogramBarWidth), count)
	}
}

func buildBar(filled, total int) string {
	out := make([]rune, 0, total)
	for i := 0; i < total; i++ {
		if i < filled {
			out = append(out, '█')
		} else {
			out = append(out, '░')
		}
	}
	return string(out)
}
```

- [ ] **Step 4: Verify they pass**

```bash
go test ./cmd/ -run TestRenderHistogram_ -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect_render.go cmd/inspect_render_test.go
git commit -m "feat(inspect): renderHistogram draws 12-cell log-scale bars"
```

### Task 13: JSON-shape augmentation builder

**Files:**
- Modify: `cmd/inspect_render.go`
- Modify: `cmd/inspect_render_test.go`

- [ ] **Step 1: Write failing test**

Append to `cmd/inspect_render_test.go`:

```go
func TestImageDetailToJSON_Shape(t *testing.T) {
	detail := importer.InspectImageDetail{
		Name:              "svc",
		ManifestDigest:    dig(strings.Repeat("a", 64)),
		LayerCount:        3,
		ArchiveLayerCount: 2,
		Layers: []importer.LayerRow{
			{Digest: dig(strings.Repeat("a", 64)), Kind: importer.LayerKindFull, TargetSize: 1000, ArchiveSize: 1000},
			{Digest: dig(strings.Repeat("b", 64)), Kind: importer.LayerKindPatch, TargetSize: 800, ArchiveSize: 50, PatchFrom: dig(strings.Repeat("z", 64))},
			{Digest: dig(strings.Repeat("c", 64)), Kind: importer.LayerKindBaselineOnly, TargetSize: 500},
		},
		Waste: []importer.WasteEntry{
			{Kind: importer.WasteKindPatchOversized, Digest: dig(strings.Repeat("y", 64)), ArchiveSize: 1200, TargetSize: 800},
		},
		TopSavings: []importer.TopSaving{
			{Digest: dig(strings.Repeat("c", 64)), SavedBytes: 500, SavedRatio: 1.0},
		},
		Histogram: importer.SizeHistogram{
			Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
			Counts:  []int{3, 0, 0, 0, 0},
		},
	}

	got := imageDetailToJSON(detail)

	layers := got["layers"].([]map[string]any)
	require.Len(t, layers, 3)
	require.Equal(t, "full", layers[0]["encoding"])
	require.EqualValues(t, 1000, layers[0]["target_size"])
	require.InDelta(t, 1.0, layers[0]["ratio"], 0.001)
	require.EqualValues(t, 0, layers[0]["saved_bytes"])

	require.Equal(t, "patch", layers[1]["encoding"])
	require.Equal(t, "sha256:"+strings.Repeat("z", 64), layers[1]["patch_from"])

	require.Equal(t, "baseline_only", layers[2]["encoding"])
	require.NotContains(t, layers[2], "ratio")

	waste := got["waste"].([]map[string]any)
	require.Len(t, waste, 1)
	require.Equal(t, "patch_oversized", waste[0]["kind"])

	top := got["top_savings"].([]map[string]any)
	require.Len(t, top, 1)
	require.EqualValues(t, 500, top[0]["saved_bytes"])

	hist := got["size_histogram"].(map[string]any)
	require.Equal(t, []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"}, hist["buckets"])
	require.Equal(t, []int{3, 0, 0, 0, 0}, hist["counts"])

	require.EqualValues(t, 3, got["layer_count"])
	require.EqualValues(t, 2, got["archive_layer_count"])
}
```

- [ ] **Step 2: Verify it fails**

```bash
go test ./cmd/ -run TestImageDetailToJSON_Shape -count=1 -v
```

- [ ] **Step 3: Implement `imageDetailToJSON`**

Append to `cmd/inspect_render.go`:

```go
func imageDetailToJSON(d importer.InspectImageDetail) map[string]any {
	layers := make([]map[string]any, 0, len(d.Layers))
	for _, r := range d.Layers {
		row := map[string]any{
			"digest":       r.Digest.String(),
			"encoding":     string(r.Kind),
			"target_size":  r.TargetSize,
			"archive_size": r.ArchiveSize,
			"saved_bytes":  r.SavedBytes(),
		}
		if r.Kind != importer.LayerKindBaselineOnly {
			row["ratio"] = r.Ratio()
		}
		if r.Kind == importer.LayerKindPatch {
			row["patch_from"] = r.PatchFrom.String()
		} else {
			row["patch_from"] = ""
		}
		layers = append(layers, row)
	}

	waste := make([]map[string]any, 0, len(d.Waste))
	for _, ws := range d.Waste {
		waste = append(waste, map[string]any{
			"kind":         string(ws.Kind),
			"digest":       ws.Digest.String(),
			"archive_size": ws.ArchiveSize,
			"target_size":  ws.TargetSize,
		})
	}

	top := make([]map[string]any, 0, len(d.TopSavings))
	for _, s := range d.TopSavings {
		top = append(top, map[string]any{
			"digest":      s.Digest.String(),
			"saved_bytes": s.SavedBytes,
			"saved_ratio": s.SavedRatio,
		})
	}

	return map[string]any{
		"layer_count":         d.LayerCount,
		"archive_layer_count": d.ArchiveLayerCount,
		"layers":              layers,
		"waste":               waste,
		"top_savings":         top,
		"size_histogram": map[string]any{
			"buckets": d.Histogram.Buckets,
			"counts":  d.Histogram.Counts,
		},
	}
}
```

- [ ] **Step 4: Verify it passes**

```bash
go test ./cmd/ -run TestImageDetailToJSON_Shape -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect_render.go cmd/inspect_render_test.go
git commit -m "feat(inspect): imageDetailToJSON shapes new per-image keys"
```

---

## Phase 4 — Wire into `cmd/inspect.go`

### Task 14: Read manifest blobs and integrate detail rendering

**Files:**
- Modify: `cmd/inspect.go`

- [ ] **Step 1: Replace `runInspect`**

```go
func runInspect(cmd *cobra.Command, args []string) error {
	path := args[0]

	rawSidecar, err := archive.ReadSidecar(path)
	if err != nil {
		return err
	}
	s, err := diff.ParseSidecar(rawSidecar)
	if err != nil {
		var p1 *diff.ErrPhase1Archive
		if errors.As(err, &p1) {
			return reportPhase1Archive(cmd.OutOrStdout(), p1)
		}
		return err
	}

	digests := make([]digest.Digest, 0, len(s.Images))
	for _, img := range s.Images {
		digests = append(digests, img.Target.ManifestDigest)
	}
	_, manifestBlobs, err := archive.ReadSidecarAndManifestBlobs(path, digests)
	if err != nil {
		return fmt.Errorf("read target manifests: %w", err)
	}

	details := make(map[string]importer.InspectImageDetail, len(s.Images))
	for _, img := range s.Images {
		mfBytes := manifestBlobs[img.Target.ManifestDigest]
		detail, derr := importer.BuildInspectImageDetail(s, img, mfBytes)
		if derr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: cannot derive details for image %q: %v\n", img.Name, derr)
			continue
		}
		details[img.Name] = detail
	}

	requiresZstd := s.RequiresZstd()
	zstdAvailable, _ := zstdpatch.Available(cmd.Context())
	if outputFormat == outputJSON {
		return writeJSON(cmd.OutOrStdout(), inspectJSON(path, s, requiresZstd, zstdAvailable, details))
	}
	return printBundleSidecar(cmd.OutOrStdout(), path, s, requiresZstd, zstdAvailable, details)
}
```

Imports: add `github.com/opencontainers/go-digest` and `github.com/leosocy/diffah/pkg/importer`.

- [ ] **Step 2: Update `printBundleSidecar`**

Replace existing function — preserve every legacy line; only append the four new sections inside the per-image loop:

```go
func printBundleSidecar(w io.Writer, path string, s *diff.Sidecar, requiresZstd, zstdAvailable bool, details map[string]importer.InspectImageDetail) error {
	bs := collectBundleStats(s)

	fmt.Fprintf(w, "archive: %s\n", path)
	fmt.Fprintf(w, "version: %s\n", s.Version)
	fmt.Fprintf(w, "feature: %s\n", s.Feature)
	fmt.Fprintf(w, "tool: %s\n", s.Tool)
	fmt.Fprintf(w, "tool_version: %s\n", s.ToolVersion)
	fmt.Fprintf(w, "platform: %s\n", s.Platform)
	fmt.Fprintf(w, "created_at: %s\n", s.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "images: %d\n", len(s.Images))
	fmt.Fprintf(w, "blobs: %d (full: %d, patch: %d)\n", len(s.Blobs), bs.fullCount, bs.patchCount)
	if bs.patchCount > 0 && bs.patchOrigSize > 0 {
		avgRatio := float64(bs.patchArchiveSize) / float64(bs.patchOrigSize) * 100
		fmt.Fprintf(w, "avg patch ratio: %.1f%%\n", avgRatio)
	}
	fmt.Fprintf(w, "total archive: %d bytes\n", bs.totalArchiveSize)
	fmt.Fprintf(w, "intra-layer patches required: %s\n", yesNo(requiresZstd))
	fmt.Fprintf(w, "zstd available: %s\n", yesNo(zstdAvailable))
	if bs.patchCount > 0 {
		savings := bs.patchOrigSize - bs.patchArchiveSize
		savingsPct := float64(savings) / float64(bs.patchOrigSize) * 100
		fmt.Fprintf(w, "patch savings: %d bytes (%.1f%% vs full)\n", savings, savingsPct)
	}

	for _, img := range s.Images {
		fmt.Fprintf(w, "\n--- image: %s ---\n", img.Name)
		fmt.Fprintf(w, "  target manifest digest: %s (%s)\n", img.Target.ManifestDigest, img.Target.MediaType)
		fmt.Fprintf(w, "  baseline manifest digest: %s (%s)\n", img.Baseline.ManifestDigest, img.Baseline.MediaType)
		if img.Baseline.SourceHint != "" {
			fmt.Fprintf(w, "  baseline source: %s\n", img.Baseline.SourceHint)
		}

		if d, ok := details[img.Name]; ok {
			fmt.Fprintln(w)
			renderLayerTable(w, d)
			fmt.Fprintln(w)
			renderWaste(w, d)
			fmt.Fprintln(w)
			renderTopSavings(w, d)
			fmt.Fprintln(w)
			renderHistogram(w, d)
		}
	}
	return nil
}
```

- [ ] **Step 3: Update `inspectJSON`**

```go
func inspectJSON(path string, s *diff.Sidecar, requiresZstd, zstdAvailable bool, details map[string]importer.InspectImageDetail) any {
	bs := collectBundleStats(s)
	images := make([]map[string]any, 0, len(s.Images))
	for _, img := range s.Images {
		entry := map[string]any{
			"name": img.Name,
			"target": map[string]any{
				"manifest_digest": img.Target.ManifestDigest.String(),
				"media_type":      img.Target.MediaType,
			},
			"baseline": map[string]any{
				"manifest_digest": img.Baseline.ManifestDigest.String(),
				"media_type":      img.Baseline.MediaType,
			},
		}
		if img.Baseline.SourceHint != "" {
			entry["baseline"].(map[string]any)["source_hint"] = img.Baseline.SourceHint
		}
		if d, ok := details[img.Name]; ok {
			for k, v := range imageDetailToJSON(d) {
				entry[k] = v
			}
		}
		images = append(images, entry)
	}
	blobs := map[string]any{
		"total":       len(s.Blobs),
		"full_count":  bs.fullCount,
		"patch_count": bs.patchCount,
	}
	if bs.patchCount > 0 {
		blobs["full_bytes"] = bs.totalArchiveSize - bs.patchArchiveSize
		blobs["patch_bytes"] = bs.patchArchiveSize
	}
	result := map[string]any{
		"archive":             path,
		"version":             s.Version,
		"feature":             s.Feature,
		"tool":                s.Tool,
		"tool_version":        s.ToolVersion,
		"platform":            s.Platform,
		"created_at":          s.CreatedAt.Format(time.RFC3339),
		"images":              images,
		"blobs":               blobs,
		"total_archive_bytes": bs.totalArchiveSize,
		"requires_zstd":       requiresZstd,
		"zstd_available":      zstdAvailable,
	}
	if bs.patchCount > 0 && bs.patchOrigSize > 0 {
		savings := bs.patchOrigSize - bs.patchArchiveSize
		savingsPct := float64(savings) / float64(bs.patchOrigSize) * 100
		result["patch_savings"] = map[string]any{
			"bytes": savings,
			"ratio": savingsPct / 100,
		}
	}
	return result
}
```

- [ ] **Step 4: Verify build**

```bash
go build ./...
```

(Test files will fail compilation until Task 15 — that is expected.)

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect.go
git commit -m "feat(inspect): wire per-image detail through text and JSON output"
```

### Task 15: Update existing inspect tests for new signatures

**Files:**
- Modify: `cmd/inspect_test.go`
- Modify: `cmd/inspect_json_test.go`

- [ ] **Step 1: Update calls to `printBundleSidecar`**

In `cmd/inspect_test.go`, update every existing call to pass a new trailing `nil`:

```go
err := printBundleSidecar(&buf, "/tmp/bundle.tar", s, true, true, nil)
```

Apply the same change inside `TestRunInspect_BundleSidecar_ParsesDirectly`.

- [ ] **Step 2: Update `inspectJSON` calls**

In `cmd/inspect_json_test.go`, update every call to pass a trailing `nil`:

```go
result := inspectJSON("/tmp/bundle.tar", s, true, true, nil)
```

Apply to `TestInspectJSON_Structure` and `TestInspectJSON_NoPatchSavings`. The snapshot test is updated separately in Task 16.

- [ ] **Step 3: Verify the legacy suite still passes**

```bash
go test ./cmd/ -run "TestPrintBundleSidecar_PerImageStats|TestRunInspect_BundleSidecar_ParsesDirectly|TestInspectJSON_Structure|TestInspectJSON_NoPatchSavings" -count=1 -v
```

Expected: all PASS — they assert only legacy keys / lines, which still hold with `details == nil`.

- [ ] **Step 4: Commit**

```bash
git add cmd/inspect_test.go cmd/inspect_json_test.go
git commit -m "test(inspect): update legacy callers for new details parameter"
```

### Task 16: Add per-image enrichment assertions and regenerate snapshot

**Files:**
- Modify: `cmd/inspect_test.go`
- Modify: `cmd/inspect_json_test.go`
- Modify: `cmd/testdata/schemas/inspect.snap.json` (regenerated)

- [ ] **Step 1: Add text-mode assertion**

Add `"strings"` and `"github.com/leosocy/diffah/pkg/importer"` to `cmd/inspect_test.go` imports if missing. Append:

```go
func TestPrintBundleSidecar_AppendsPerImageSections(t *testing.T) {
	mfDigest := digest.Digest("sha256:" + strings.Repeat("a", 64))
	s := &diff.Sidecar{
		Version: "v1", Feature: "bundle", Tool: "diffah", ToolVersion: "v0.x",
		CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC), Platform: "linux/amd64",
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{ManifestDigest: mfDigest, ManifestSize: 100, MediaType: "application/vnd.oci.image.manifest.v1+json"},
			Baseline: diff.BaselineRef{ManifestDigest: digest.Digest("sha256:" + strings.Repeat("b", 64)), MediaType: "application/vnd.oci.image.manifest.v1+json"},
		}},
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest: {Size: 100, Encoding: diff.EncodingFull, ArchiveSize: 100},
		},
	}
	details := map[string]importer.InspectImageDetail{
		"svc": {
			Name: "svc", ManifestDigest: mfDigest, LayerCount: 1, ArchiveLayerCount: 1,
			Layers: []importer.LayerRow{
				{Digest: digest.Digest("sha256:" + strings.Repeat("c", 64)), Kind: importer.LayerKindFull, TargetSize: 1000, ArchiveSize: 1000},
			},
			Histogram: importer.SizeHistogram{
				Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
				Counts:  []int{1, 0, 0, 0, 0},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, printBundleSidecar(&buf, "/tmp/bundle.tar", s, false, false, details))
	out := buf.String()

	require.Contains(t, out, "archive: /tmp/bundle.tar")
	require.Contains(t, out, "feature: bundle")
	require.Contains(t, out, "--- image: svc ---")
	require.Contains(t, out, "Layers (target manifest order):")
	require.Contains(t, out, "Waste:")
	require.Contains(t, out, "Layer-size histogram (target bytes):")
}
```

- [ ] **Step 2: Add JSON-shape assertion**

Add `"strings"` and `"github.com/leosocy/diffah/pkg/importer"` to `cmd/inspect_json_test.go` imports if missing. Append:

```go
func TestInspectJSON_PerImageDetailKeysPresent(t *testing.T) {
	mfDigest := digest.Digest("sha256:" + strings.Repeat("a", 64))
	s := &diff.Sidecar{
		Version: "v1", Feature: "bundle", Tool: "diffah", ToolVersion: "v0.x",
		CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC), Platform: "linux/amd64",
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{ManifestDigest: mfDigest, ManifestSize: 100, MediaType: "application/vnd.oci.image.manifest.v1+json"},
			Baseline: diff.BaselineRef{ManifestDigest: digest.Digest("sha256:" + strings.Repeat("b", 64)), MediaType: "application/vnd.oci.image.manifest.v1+json"},
		}},
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest: {Size: 100, Encoding: diff.EncodingFull, ArchiveSize: 100},
		},
	}
	details := map[string]importer.InspectImageDetail{
		"svc": {
			Name: "svc", ManifestDigest: mfDigest, LayerCount: 1, ArchiveLayerCount: 1,
			Layers: []importer.LayerRow{
				{Digest: digest.Digest("sha256:" + strings.Repeat("c", 64)), Kind: importer.LayerKindFull, TargetSize: 1000, ArchiveSize: 1000},
			},
			Histogram: importer.SizeHistogram{
				Buckets: []string{"<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"},
				Counts:  []int{1, 0, 0, 0, 0},
			},
		},
	}
	result := inspectJSON("/tmp/bundle.tar", s, false, false, details)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	var env struct {
		Data struct {
			Images []struct {
				Name              string `json:"name"`
				LayerCount        int    `json:"layer_count"`
				ArchiveLayerCount int    `json:"archive_layer_count"`
				Layers            []struct {
					Digest      string `json:"digest"`
					Encoding    string `json:"encoding"`
					TargetSize  int64  `json:"target_size"`
					ArchiveSize int64  `json:"archive_size"`
				} `json:"layers"`
				Waste         []map[string]any `json:"waste"`
				TopSavings    []map[string]any `json:"top_savings"`
				SizeHistogram struct {
					Buckets []string `json:"buckets"`
					Counts  []int    `json:"counts"`
				} `json:"size_histogram"`
			} `json:"images"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Len(t, env.Data.Images, 1)
	img0 := env.Data.Images[0]
	require.Equal(t, "svc", img0.Name)
	require.Equal(t, 1, img0.LayerCount)
	require.Equal(t, 1, img0.ArchiveLayerCount)
	require.Len(t, img0.Layers, 1)
	require.Equal(t, "full", img0.Layers[0].Encoding)
	require.Empty(t, img0.Waste)
}
```

- [ ] **Step 3: Update `TestInspectJSON_Snapshot` to read manifest blobs and pass details**

Replace `TestInspectJSON_Snapshot` body in `cmd/inspect_json_test.go`:

```go
func TestInspectJSON_Snapshot(t *testing.T) {
	archivePath := filepath.Join("..", "testdata", "fixtures", "v5_bundle.tar")
	if _, err := os.Stat(archivePath); err != nil {
		t.Skipf("fixture missing: %s", archivePath)
	}

	rawSidecar, err := archive.ReadSidecar(archivePath)
	require.NoError(t, err)
	s, err := diff.ParseSidecar(rawSidecar)
	require.NoError(t, err)

	digests := make([]digest.Digest, 0, len(s.Images))
	for _, img := range s.Images {
		digests = append(digests, img.Target.ManifestDigest)
	}
	_, blobs, err := archive.ReadSidecarAndManifestBlobs(archivePath, digests)
	require.NoError(t, err)

	details := make(map[string]importer.InspectImageDetail, len(s.Images))
	for _, img := range s.Images {
		d, derr := importer.BuildInspectImageDetail(s, img, blobs[img.Target.ManifestDigest])
		require.NoError(t, derr)
		details[img.Name] = d
	}

	requiresZstd := s.RequiresZstd()
	zstdAvailable, _ := zstdpatch.Available(t.Context())

	result := inspectJSON(archivePath, s, requiresZstd, zstdAvailable, details)

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, result))

	got := buf.String()

	snap := filepath.Join("testdata", "schemas", "inspect.snap.json")
	want, rerr := os.ReadFile(snap)
	if rerr != nil || os.Getenv("DIFFAH_UPDATE_SNAPSHOTS") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(snap), 0o755))
		require.NoError(t, os.WriteFile(snap, []byte(normalizeJSON(got)), 0o644))
		if rerr != nil {
			t.Fatalf("snapshot was missing; written. Re-run to verify.")
		}
		return
	}

	gotNorm := normalizeJSON(got)
	if string(want) != gotNorm {
		t.Errorf("snapshot mismatch.\nwant:\n%s\ngot:\n%s", want, gotNorm)
	}
}
```

- [ ] **Step 4: Regenerate the snapshot**

```bash
DIFFAH_UPDATE_SNAPSHOTS=1 go test ./cmd/ -run TestInspectJSON_Snapshot -count=1 -v
```

Expected: PASS, snapshot rewritten. Open `cmd/testdata/schemas/inspect.snap.json` and verify the per-image entry now contains `layer_count`, `archive_layer_count`, `layers`, `waste`, `top_savings`, `size_histogram`.

- [ ] **Step 5: Re-run snapshot test for determinism**

```bash
go test ./cmd/ -run TestInspectJSON_Snapshot -count=1 -v
```

Expected: PASS without override.

- [ ] **Step 6: Run full cmd test suite**

```bash
go test ./cmd/ -count=1
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/inspect_test.go cmd/inspect_json_test.go cmd/testdata/schemas/inspect.snap.json
git commit -m "test(inspect): assert per-image detail sections + regenerate snapshot"
```

---

## Phase 5 — CHANGELOG + quality gate

### Task 17: Add Phase 5.3 entry to CHANGELOG

- [ ] **Step 1: Locate `[Unreleased] — Phase 5` block**

```bash
grep -n "Phase 5" CHANGELOG.md | head -10
```

- [ ] **Step 2: Append Phase 5.3 entry under `### Added`**

```markdown
- `diffah inspect` now appends a per-image layer table (full / patch / baseline-only with target / archive sizes and ratio), waste detection (`patch_oversized`), top-10 savings list, and a five-bucket log-scale layer-size histogram. JSON output gains six per-image keys (`layer_count`, `archive_layer_count`, `layers`, `waste`, `top_savings`, `size_histogram`). Existing first-line text shape and existing JSON keys are preserved — old grep / jq scripts still work. (Phase 5.3)
```

- [ ] **Step 3: Verify Markdown still parses**

```bash
head -50 CHANGELOG.md
```

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): add Phase 5.3 inspect enrichment entry"
```

### Task 18: Quality gate

- [ ] **Step 1: Formatter**

```bash
gofmt -l ./...
```

- [ ] **Step 2: Lint**

```bash
golangci-lint run ./...
```

(Pre-existing `internal/imageio/sysctx.go:155` G703 warning carried over from prior PRs is acceptable; do not chase it.)

- [ ] **Step 3: Unit tests**

```bash
go test ./... -count=1
```

- [ ] **Step 4: Build**

```bash
go build ./...
```

- [ ] **Step 5: Integration tests**

```bash
go test -tags integration ./... -count=1 -timeout 5m
```

- [ ] **Step 6: Smoke-test manually**

```bash
go run . inspect testdata/fixtures/v5_bundle.tar
go run . inspect --output json testdata/fixtures/v5_bundle.tar | head -80
```

Verify text shows all four new sections per image and JSON includes the six new per-image keys.

### Task 19: Open PR

- [ ] **Step 1: Push branch**

```bash
git push -u origin spec/phase5-inspect
```

- [ ] **Step 2: Create PR**

```bash
gh pr create \
  --title "feat(inspect): per-layer table, waste, top-N, histogram (Phase 5.3)" \
  --body "$(cat <<'EOF'
## Summary

- Enriches `diffah inspect` with a per-image layer table (Full / Patch / Baseline-only), patch-oversized waste detection, top-10 savings list, and a 5-bucket log-scale layer-size histogram.
- Adds `internal/archive.ReadSidecarAndManifestBlobs` so inspect can read N target manifest blobs in one tar pass without a full archive extract.
- Adds `pkg/importer.BuildInspectImageDetail` as a pure data layer.
- Backward-compatible: existing first-line text shape and existing JSON keys are preserved. New JSON keys are additive per image.

Refs: docs/superpowers/specs/2026-04-29-phase5-dx-polish-design.md §7

## Test plan

- [ ] go test ./...
- [ ] go test -tags integration ./...
- [ ] golangci-lint run ./...
- [ ] Manual smoke: go run . inspect testdata/fixtures/v5_bundle.tar
- [ ] Manual smoke: go run . inspect --output json testdata/fixtures/v5_bundle.tar
- [ ] Snapshot regenerated and checked into PR
EOF
)"
```

- [ ] **Step 3: Watch CI**

```bash
gh pr checks --watch
```

Expected: all 6 checks pass.

---

## Self-review

### Spec coverage

| Spec section | Plan coverage |
|---|---|
| §7.1 New text sections | Tasks 9–12 + Task 14 (composition) + Task 16 (assertion) |
| §7.2 Per-layer fields | Task 4 (`buildLayerRows`) + Task 8 (orchestrator) |
| §7.3 Waste detection | Task 5 + Task 10 |
| §7.4 Top-N savings | Task 6 + Task 11 |
| §7.5 Histogram | Task 7 + Task 12 |
| §7.6 JSON schema | Task 13 + Task 14 + Task 16 |
| §7.7 Package layout | File plan table |
| §8.1 Per-PR unit coverage | Tasks 4–8, 9–13 |
| §8.3 No-regression checks | Task 15 + Task 16 step 4–5 |
| §9 Backward compatibility | Task 14 (legacy lines preserved); Task 13/14 (additive JSON) |
| §10 PR strategy → PR-3 | Tasks 1, 19 |

### Type consistency

- `LayerRowKind` constants `LayerKindFull` / `LayerKindPatch` / `LayerKindBaselineOnly` used identically across Tasks 3, 4, 8, 9, 13, 16.
- `BuildInspectImageDetail` signature `(*diff.Sidecar, diff.ImageEntry, []byte) (InspectImageDetail, error)` matches between Task 8 (definition) and Tasks 14, 16 (callers).
- `ReadSidecarAndManifestBlobs` signature `(string, []digest.Digest) ([]byte, map[digest.Digest][]byte, error)` matches between Task 2 (definition) and Tasks 14, 16 (callers).
- `printBundleSidecar` and `inspectJSON` both gain a final `details map[string]importer.InspectImageDetail`; Tasks 14, 15, 16 all use the same signature.
- `inspectTopN` (Task 8, in `pkg/importer`) and `inspectTopNDisplay` (Task 11, in `cmd`) deliberately have different identifiers because they live in different packages.

---

## Execution handoff

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session via executing-plans, batch with checkpoints.

Which approach?
