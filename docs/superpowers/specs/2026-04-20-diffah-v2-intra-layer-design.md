# diffah v2 Phase 1 — Intra-Layer Binary Diff

**Date:** 2026-04-20
**Status:** Design approved, pending implementation plan
**Scope owner:** diffah core (leosocy)

## 1. Purpose and motivation

diffah v1 ships a layer-level delta: layers whose digest already exists in the
consumer's baseline are marked `required_from_baseline` and omitted from the
archive; everything else is shipped in full. Empirical benchmarks against
production images demonstrate this works well for patch-level version bumps
(45–82% savings vs `docker save | zstd`) but collapses to 0% whenever the
base image is rebuilt from source — the exact scenario driving quarterly CVE
refreshes for air-gapped customers.

Rebuild-driven rebases do not change the byte content of each layer
meaningfully: POC measurements on three production services
(`service-A`, `service-B`, `service-C`) show
every base layer has >99% byte overlap with its predecessor despite every
digest changing. A byte-level delta of the affected layers against
size-closest baseline layers, compressed with `zstd --patch-from`, reduces
the rebase delivery to 3.4–53.7% of the full `docker save | zstd` size:

| Service pair | v1 delta (zstd) | POC intra-layer | Saved |
|---|---|---|---|
| service-B 5.2→5.3 | 572 MB | **19 MB** | **−96.6%** |
| service-C 5.2→5.3 | 348 MB | **187 MB** | **−46.2%** |
| service-A 5.2→5.3 | 626 MB | **342 MB** | **−45.3%** |

Phase 1 extends the exporter and importer to compute and apply intra-layer
patches, keeping the archive format, CLI shape, and v1 behaviour on the
non-rebase case unchanged.

## 2. Scope and non-goals

**In scope (Phase 1):**

- `internal/zstdpatch` package wrapping `github.com/klauspost/compress/zstd`
  for patch-from style byte-level encode/decode.
- Exporter extension computing a per-layer patch for each
  `ShippedInDelta` entry, choosing `min(patch, full)` per layer, and
  emitting the chosen form into the archive.
- Importer extension assembling patched layers on the fly through the
  existing `CompositeSource` with a post-assembly digest verification lock.
- Sidecar schema evolution on the existing `version: "v1"` string
  (pre-release, no backward-compat obligation).
- New CLI switch `--intra-layer=auto|off` on `export` (default `auto`).
- `inspect` output augmentation showing full-vs-patch split and patch
  ratios.
- `DryRun` extension probing `patch_from_digest` in addition to
  `required_from_baseline`.

**Explicitly out of scope (later phases / separate tracks):**

- Cross-image batching (shared base layer deduplication across multiple
  service deliveries) — Phase 2.
- Content-similarity-based layer matching (e.g. tar entry digest set
  intersection) — Phase 3.
- Alternative patch codecs (`bsdiff`, `xdelta3`) — deferred pending Phase 1
  production data.
- Parallel patch computation — sequential processing is sufficient at the
  current data scale (≤ 30 s wall time per typical image).
- Direct push (`--output docker://...`), cosign verification, progress
  reporting, multi-arch target manifest lists — tracked separately.

## 3. Decision log

These decisions were resolved during design brainstorming and are load-bearing
for the architecture below:

| # | Decision | Reason |
|---|---|---|
| 1 | Sidecar keeps `version: "v1"`; schema evolves in place. No version dispatch. | Tool not yet public — no legacy archives to preserve. Single code path is simpler. |
| 2 | Archive can mix `encoding=full` and `encoding=patch` per layer. | POC data shows even best-case pairs have 1–2 layers where patching exceeds full — automatic fall-back is required. |
| 3 | `--intra-layer=auto` is the default. `--intra-layer=off` is an opt-out. | Savings are consistent; hiding the feature behind a flag would bury the primary value. |
| 4 | Single importer code path; no per-archive mode switch. | Follows from #1. |
| 5 | Always compute patch, take `min(patch_bytes, full_zstd_bytes)` per layer. | Compute cost is dwarfed by savings; thresholds introduce magic numbers. |
| 6 | Extend `shipped_in_delta[]` entries with `encoding`/`codec`/`patch_from_digest`/`archive_size`. Patch file names equal the target digest; sidecar is the authoritative discriminator. | One canonical list with a discriminated union — mirrors v1 structure users already understand. |
| 7 | `klauspost/compress/zstd` is the patch codec, gated by a compatibility spike (Phase 1 Task 0). Fallback: `os/exec` against the `zstd` CLI. | Preserves the pure-Go, static-binary property operators rely on. |

## 4. Architecture

### 4.1 Component overview

```
                          ┌──────────────────────┐
Export path:              │ IntraLayerPlanner    │  (new, pkg/exporter)
                          └─────────┬────────────┘
pkg/diff.ComputePlan ─┐             │
                     ─┼─> Shipped ─>│ size-match + zstd patch + min()
pkg/diff.Sidecar    <─┘             │
                                    ▼
                          ┌──────────────────────┐
                          │ Archive (v1 layout)  │  (unchanged on disk)
                          └─────────┬────────────┘
                                    ▼
                          ┌──────────────────────┐
Import path:              │ CompositeSource      │  (extended, pkg/importer)
                          └─────────┬────────────┘
pkg/diff.ParseSidecar ───>          │ GetBlob: full → read;
                                    │           patch → zstdpatch.Decode + verify
                                    ▼
                          ┌──────────────────────┐
                          │ copy.Image (unchanged)│
                          └──────────────────────┘
```

**New packages**

- `internal/zstdpatch/`: thin wrapper over klauspost zstd providing
  `Encode(ref, target) → patch`, `EncodeFull(target) → zstdBytes`,
  `Decode(ref, patch) → target`.

**Touched packages**

- `pkg/diff`: `BlobRef` gains four fields; `Sidecar.validate` gains three
  rules; new error types.
- `pkg/exporter`: new `intralayer.go` containing `IntraLayerPlanner`; the
  existing exporter orchestration invokes it when `opts.IntraLayer==auto`.
- `pkg/importer`: `composite_src.go` `GetBlob` gains a patch-assemble
  branch; `DryRun` probes the patch-ref union.
- `cmd/export.go`: one new `--intra-layer` flag.
- `cmd/inspect.go`: additional output lines.

### 4.2 Data flow on the happy path

**Export (`service-B 5.2 → 5.3`, auto, baseline=docker-daemon):**

1. Target and baseline manifests parsed; `ComputePlan` partitions into
   `RequiredFromBaseline=[]` (zero-overlap rebase) and
   `ShippedInDelta=[all 21 new layers]`.
2. `IntraLayerPlanner` iterates `ShippedInDelta`. For each layer L it scans
   baseline layers, picks the size-closest (ties broken by first-seen), reads
   both blobs, computes `zstdpatch.Encode(ref, L)` and
   `zstdpatch.EncodeFull(L)`, emits whichever is smaller with the appropriate
   `encoding`.
3. Sidecar is marshalled (`version: "v1"`). `RequiredFromBaseline` is an
   empty slice `[]` (post-PR #4 guarantee).
4. Archive is packed as today, optionally outer-zstd'd.

**Import (consumer has 5.2 baseline, runs `diffah import --delta ... --baseline docker-daemon:...5.2`):**

1. Sidecar parsed, compat check (existing) runs.
2. Baseline source opened; fail-fast probe extended: union of
   `required_from_baseline[].digest` and
   `{e.patch_from_digest : e in shipped_in_delta, e.encoding==patch}` must
   all be reachable in baseline. Missing ones → `ErrBaselineMissingBlob` or
   `ErrBaselineMissingPatchRef`.
3. `copy.Image` runs against `CompositeSource`. For each layer digest it
   requests:
   - `encoding=full` → archive file bytes returned as-is (v1 path).
   - `encoding=patch` → fetch `ref = baselineSrc.GetBlob(patch_from_digest)`,
     call `zstdpatch.Decode(ref, archive_bytes)`, verify
     `sha256(result) == digest`, return `result`. Mismatch →
     `ErrIntraLayerAssemblyMismatch`, fail-fast.
4. `verifyImport` runs (existing). Output file is renamed atomically.

### 4.3 Key invariants

| Invariant | Enforced where |
|---|---|
| Target blob digest = sha256 of what `CompositeSource.GetBlob` returns | `composite_src.go` after patch decode; `verifyImport` for dir output |
| `patch_from_digest` refers to a blob present in the consumer's baseline | `openCompositeSource` probe; `DryRun` probe |
| `encoding=full ⇒ archive_size == size` and no codec / patch_from_digest | `Sidecar.validate` |
| `encoding=patch ⇒ archive_size < size`, codec non-empty, patch_from_digest non-empty | `Sidecar.validate` |
| Mixed archives (full + patch in same archive) are first-class | Validator accepts, Importer dispatches per entry |
| zstd encoder version drift does not break consumers | Decoder reads zstd frame headers autonomously; we never mandate a specific encoder build |

## 5. Sidecar schema (v1, evolved)

### 5.1 Types

```go
// Encoding discriminates how a shipped blob is stored in the archive.
type Encoding string

const (
    EncodingFull  Encoding = "full"
    EncodingPatch Encoding = "patch"
)

// BlobRef describes one layer or config blob.
//
// The Encoding/Codec/PatchFromDigest/ArchiveSize fields apply to
// ShippedInDelta entries only. RequiredFromBaseline entries omit them
// entirely — those layers are fetched from baseline as-is, so an
// archive-level encoding concept does not apply.
type BlobRef struct {
    Digest    digest.Digest `json:"digest"`
    Size      int64         `json:"size"`
    MediaType string        `json:"media_type"`

    Encoding        Encoding      `json:"encoding,omitempty"`           // set on ShippedInDelta; zero value on RequiredFromBaseline
    Codec           string        `json:"codec,omitempty"`              // e.g. "zstd-patch"; set only when Encoding=patch
    PatchFromDigest digest.Digest `json:"patch_from_digest,omitempty"`  // set only when Encoding=patch
    ArchiveSize     int64         `json:"archive_size,omitempty"`       // bytes of the file stored under Digest in the archive; zero on RequiredFromBaseline
}
```

The `Sidecar` top-level struct is unchanged. Only `ShippedInDelta`
entries carry the intra-layer fields; `RequiredFromBaseline` entries
omit them entirely. To avoid a JSON shape split across two collections,
marshal/unmarshal still uses a single `BlobRef` type —
required-from-baseline entries simply leave the intra-layer fields at
their zero values (elided by `omitempty`).

### 5.2 Validation rules

`Sidecar.validate()` existing v1 rules keep firing. Additional rules
applied **only to `ShippedInDelta` entries** (not to
`RequiredFromBaseline`, whose entries omit all four fields):

1. `Encoding` must equal `"full"` or `"patch"`; empty is invalid.
2. If `Encoding == "patch"`: `Codec != ""`, `PatchFromDigest != ""`,
   `0 < ArchiveSize < Size`.
3. If `Encoding == "full"`: `Codec == ""`, `PatchFromDigest == ""`,
   `ArchiveSize == Size`.

`RequiredFromBaseline` entries are validated with the v1 rules only
(`Digest != ""`, `Size >= 0`, `MediaType != ""`). Presence of any of the
four intra-layer fields on a `RequiredFromBaseline` entry is a schema
violation.

### 5.3 Concrete example

```json
{
  "version": "v1",
  "tool": "diffah",
  "tool_version": "v0.2.0",
  "created_at": "2026-04-20T08:00:00Z",
  "platform": "linux/arm64",
  "target":   { "manifest_digest": "sha256:50b7...", "manifest_size": 3210, "media_type": "application/vnd.docker.distribution.manifest.v2+json" },
  "baseline": { "manifest_digest": "sha256:8bba...", "media_type": "application/vnd.docker.distribution.manifest.v2+json", "source_hint": "registry.example.com/service-A:abcd1234..." },
  "required_from_baseline": [],
  "shipped_in_delta": [
    {
      "digest": "sha256:d431af9f...",
      "size": 143697920,
      "media_type": "application/vnd.docker.image.rootfs.diff.tar.gzip",
      "encoding": "patch",
      "codec": "zstd-patch",
      "patch_from_digest": "sha256:dc105447...",
      "archive_size": 6483456
    },
    {
      "digest": "sha256:5165893471db...",
      "size": 1269803520,
      "media_type": "application/vnd.docker.image.rootfs.diff.tar.gzip",
      "encoding": "full",
      "archive_size": 1269803520
    }
  ]
}
```

## 6. Export pipeline

### 6.1 IntraLayerPlanner responsibilities

```go
// Planner computes per-layer encoding decisions for ShippedInDelta.
type Planner struct {
    baselineSrc types.ImageSource
    baseline    []BaselineLayerMeta  // {digest, size, mediaType}
    readBlob    func(types.ImageSource, digest.Digest) ([]byte, error)
}

// Run returns a []BlobRef ready to drop into Sidecar.ShippedInDelta. It
// also produces an on-disk layout map indicating which bytes to persist
// under each target digest: the raw blob for encoding=full, or the patch
// bytes for encoding=patch.
func (p *Planner) Run(ctx context.Context, shipped []diff.BlobRef) (
    entries []diff.BlobRef,
    payloads map[digest.Digest][]byte,
    err error,
)
```

Algorithm per shipped layer `L`:

1. `best = baseline[argmin over j of |baseline[j].size - L.size|]`.
   Ties: first-seen index wins (deterministic ordering).
2. `targetBytes = readBlob(target_source, L.digest)`.
3. `refBytes    = readBlob(baseline_source, best.digest)`.
4. `patchBytes  = zstdpatch.Encode(refBytes, targetBytes)`.
5. `fullZstBytes = zstdpatch.EncodeFull(targetBytes)` — used only for
   the `min()` comparison; not persisted.
6. If `len(patchBytes) < len(fullZstBytes)`: emit `encoding=patch` with
   `archive_size=len(patchBytes)`, `patch_from_digest=best.digest`,
   `codec="zstd-patch"`. Persist `patchBytes`.
7. Else: emit `encoding=full` with `archive_size=L.size`. Persist
   `targetBytes` verbatim.

`fullZstBytes` exists purely to make the compare fair: the alternative to
shipping a patch is shipping the blob, and if the archive is outer-zstd'd
the blob will be compressed once at the archive level. Comparing `patch`
to `full_zstd` instead of to raw `Size` avoids biasing the decision toward
patches for already-incompressible content.

### 6.2 Exporter orchestration

```go
// Export (pseudo)
plan := diff.ComputePlan(target.Layers, baseline.LayerDigests)

var shipped []diff.BlobRef
var payloads map[digest.Digest][]byte
// opts.BaselineRef is non-nil when the caller provided --baseline.
// opts.BaselineManifestPath is non-empty when the caller provided
// --baseline-manifest (these two flags are mutually exclusive at the CLI layer).
switch opts.IntraLayer {
case "off":
    shipped, payloads = fullOnlyEntries(plan.ShippedInDelta, targetSrc)
case "auto":
    if opts.BaselineManifestPath != "" {
        return &diff.ErrIntraLayerUnsupported{Reason: "--baseline-manifest has no blob bytes"}
    }
    shipped, payloads, err = planner.Run(ctx, plan.ShippedInDelta)
}
sidecar := diff.Sidecar{
    Version:              "v1",
    /* standard fields */
    RequiredFromBaseline: plan.RequiredFromBaseline,
    ShippedInDelta:       shipped,
}
writeArchive(sidecar, payloads) // existing path
```

### 6.3 `--baseline-manifest` interaction

With `--baseline-manifest`, the exporter only has manifest bytes — no
baseline blob bytes — so patches cannot be computed. Behaviour:

- `--intra-layer=auto` **+** `--baseline-manifest` → `ErrIntraLayerUnsupported`.
  User must opt into `--intra-layer=off` explicitly.
- `--intra-layer=off` **+** `--baseline-manifest` → works today (v1 behaviour).

Rationale: silent fallback creates a false sense of savings. Explicit
`off` is one extra flag, cost negligible, surprise eliminated.

## 7. Import pipeline

### 7.1 CompositeSource.GetBlob

```go
// GetBlob(d digest.Digest) (pseudo)
switch s.classify(d) {
case inRequiredFromBaseline:
    return s.baselineSrc.GetBlob(d)

case shippedFull:
    return s.deltaDir.Read(d)

case shippedPatch:
    entry := s.sidecarByDigest[d]
    ref, err := s.baselineSrc.GetBlob(entry.PatchFromDigest)
    if err != nil { return nil, err }
    patch, err := s.deltaDir.Read(d)
    if err != nil { return nil, err }
    assembled, err := zstdpatch.Decode(ref, patch)
    if err != nil { return nil, fmt.Errorf("decode patch %s: %w", d, err) }
    if got := digest.FromBytes(assembled); got != d {
        return nil, &diff.ErrIntraLayerAssemblyMismatch{Digest: d.String(), Got: got.String()}
    }
    return assembled, nil
}
```

### 7.2 Baseline probe extension

Current `openCompositeSource` probe: every entry in `RequiredFromBaseline`
must exist as a layer of the baseline manifest.

Intra-layer extension: additionally, for every `shipped_in_delta` entry
with `Encoding=patch`, its `patch_from_digest` must exist as a baseline
layer too. Probe union is computed up-front so the error surfaces before
any blob byte is read.

Missing `patch_from_digest` → `ErrBaselineMissingPatchRef`. Structurally
the same as `ErrBaselineMissingBlob` but distinguishes "I need this layer
to *be* the output" (required-from-baseline variant) from "I need this
layer to *decode* the output patch" (patch-ref variant), which matters
for operator troubleshooting.

### 7.3 DryRun

`DryRunReport` adds:

```go
type DryRunReport struct {
    /* existing fields */
    RequiredPatchRefs  int       // number of distinct patch_from_digests referenced
    MissingPatchRefs   []string  // distinct; empty ⇒ all reachable
}
```

`AllReachable` becomes the conjunction of both missing sets being empty.

## 8. Error taxonomy

New domain errors (`pkg/diff/errors.go`):

```go
// ErrIntraLayerAssemblyMismatch reports that a patched layer's computed
// sha256 did not match the manifest-declared digest. This indicates either
// a corrupt baseline blob (hash drift), a decoder bug, or in-flight
// corruption of the archive. Import must fail fast with no partial output.
type ErrIntraLayerAssemblyMismatch struct{ Digest, Got string }

// ErrBaselineMissingPatchRef is the patch-specific sibling of
// ErrBaselineMissingBlob. It's raised when a shipped layer with
// encoding=patch names a patch_from_digest that is absent from the
// provided baseline.
type ErrBaselineMissingPatchRef struct{ Digest, Source string }

// ErrIntraLayerUnsupported is raised on the exporter side when the
// current options make intra-layer mode impossible (e.g. baseline is a
// manifest-only reference with no blob bytes).
type ErrIntraLayerUnsupported struct{ Reason string }
```

All pre-existing errors stay. The CLI-level stderr surface (`diffah:
<msg>`, PR #2) handles these the same way as the rest.

## 9. CLI changes

### 9.1 `diffah export`

```
--intra-layer=auto|off     (default: auto)
```

- `auto`: compute per-layer patches; choose `min(patch, full)`.
- `off`: every shipped entry is `encoding=full`. Equivalent to v1
  behaviour byte-for-byte in the archive (though the sidecar still carries
  the `encoding` field — that is the schema, not the behaviour).

### 9.2 `diffah import`

No new flags. `--dry-run` now probes patch references automatically.

### 9.3 `diffah inspect`

Augmented output:

```
archive: ./delta.tar
version: v1
platform: linux/arm64
target manifest digest: sha256:...
baseline manifest digest: sha256:...
shipped: 18 blobs (2.30 GB raw)
  ├─ full:  7 blobs (0.60 GB archive)
  └─ patch: 11 blobs (0.18 GB archive, avg ratio 18.9%)
required: 0 blobs (0 B)
total archive: 0.78 GB
saved 66.9% vs full image
```

Where "avg ratio" is the arithmetic mean of
`entry.ArchiveSize / entry.Size` across patch entries, and "saved X% vs
full image" is `1 - total_archive / (shipped_raw + required_raw)`.

## 10. Testing strategy

### 10.1 Unit tests (`make test`, no external dependencies)

- `TestZstdpatch_RoundTrip` — encode/decode recovers bytes (0 B, 1 MB,
  100 MB tiers).
- `TestZstdpatch_WrongReference` — decoding with the wrong reference
  returns an error, not silently-different bytes.
- `TestIntraLayerPlanner_PrefersFullWhenPatchLarger` — synthetic target
  with no byte overlap to reference → `encoding=full`.
- `TestIntraLayerPlanner_PicksSizeClosestMatch` — three baseline layers,
  size-closest wins.
- `TestSidecar_v1_RejectsMissingEncoding` — absent `encoding` →
  `ErrSidecarSchema`.
- `TestSidecar_v1_RejectsPatchMissingFromDigest`.
- `TestSidecar_v1_FullMustNotHavePatchFields`.
- `TestCompositeSource_AppliesPatch`.
- `TestCompositeSource_AssemblyMismatchErrors` — flip one byte in a
  patch, decode, assert `ErrIntraLayerAssemblyMismatch`.
- `TestImport_DryRun_DetectsMissingPatchRef`.

### 10.2 Integration tests (`make test-integration`)

- `TestIntraLayer_EndToEnd_OCIFixture` — new fixture pair where some
  layers differ only by a byte; exporter-auto + importer → reconstructed
  manifest digest equals sidecar target digest (byte-exact).
- `TestIntraLayer_EndToEnd_Schema2Fixture` — same for docker schema 2.
- `TestIntraLayer_MixedEncoding_Matrix` — one archive containing both
  encoding flavours round-trips cleanly.
- `TestExport_ManifestBaseline_WithIntraLayerAuto_Errors` — surfaces
  `ErrIntraLayerUnsupported`.

### 10.3 Regression

All pre-existing exporter/importer/inspect tests must continue to pass
without modification. Tests that exercised `--intra-layer=off`-equivalent
behaviour (i.e. the v1 default) remain green because `off` is a valid
choice with byte-for-byte v1 output.

### 10.4 Fixture additions

Extend `scripts/build_fixtures` to produce a third fixture `v3_{oci,s2}`
where each layer differs from `v2_` only by a controlled few bytes (e.g.
one file's content bumped). This is the fixture that validates patches
produce near-zero bytes and is cheap to check into `testdata/fixtures/`.

## 11. Risks and spike gates

### 11.1 Phase 1 Task 0 — klauspost/zstd compatibility spike (0.5–1 day)

**Goal**: confirm that `github.com/klauspost/compress/zstd` can produce
patch bytes, using a large reference blob as dictionary, at a compression
ratio competitive with the reference CLI.

**Benchmark inputs**: service-A 5.2→5.3 layer 0 (136 MB reference, 137 MB
target). POC established 6.3 MB patch via the CLI at ratio 17.7%.

**Acceptance criteria**:

- klauspost produces a decode-compatible patch.
- `klauspost.patch_bytes ≤ 1.5 × cli.patch_bytes` on the benchmark inputs.
- Decode round-trips to byte-exact target bytes.
- Peak memory for 150 MB reference under 500 MB (window-log 27).

**If the spike fails**: `internal/zstdpatch` is re-implemented with
`os/exec` against the `zstd` CLI (same interface). README notes a
runtime dependency on `zstd ≥ 1.5` on both sides. No other design element
changes.

### 11.2 Other risks

| Risk | Mitigation |
|---|---|
| Large (>1 GB) layer encode time or memory | Window-log 27 (128 MB window) caps memory; streamed I/O in klauspost; fail fast if layer > 2 GB (a limit of zstd `--patch-from` CLI — worth reflecting in Go API). |
| Size-closest match is wrong for an app layer that grew substantially (e.g. service-A layer 12: 1048 MB → matched to 747 MB) | Decision #5 (`min(patch, full)`) ensures degradation to full. Worst case: zero savings on that layer. |
| Encoder version drift (different klauspost versions emit different bytes) | Decoder consumes the zstd frame headers; the bytes need not be bit-identical across encoder versions. Only the *decoded* bytes matter. |
| Consumer's baseline digest present but with divergent bytes (shouldn't happen under content-addressable guarantees) | Post-assembly sha256 check catches it. `ErrIntraLayerAssemblyMismatch` identifies the offending digest. |

## 12. Phase 1 deliverable definition

Phase 1 is done when:

1. `internal/zstdpatch` exists with unit test coverage on the three tiers.
2. `pkg/exporter/intralayer.go` implements the planner; `pkg/exporter`
   consumes it when `opts.IntraLayer == auto`.
3. `pkg/importer/composite_src.go` dispatches on encoding; digest verify
   runs every patch path; `DryRun` probes patch refs.
4. `pkg/diff/sidecar.go` validator enforces the three new rules; three new
   domain errors exist.
5. `cmd/export.go` exposes `--intra-layer`; `cmd/inspect.go` prints the
   full/patch split.
6. All unit tests and integration tests listed in §10 pass.
7. Re-running the three 5.2→5.3 production pair benchmarks reproduces at
   least 80% of the POC savings (allowing for encoder ratio gap).
8. Linter clean; golangci-lint 0 issues.
