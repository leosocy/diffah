# diffah Apply Correctness & Resilience — Design

- **Status:** Draft
- **Date:** 2026-04-26
- **Author:** @leosocy
- **Parent:** `docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md` (extends the apply path beyond Phase 2 registry-native import)
- **Scope:** importer-side only — no exporter changes, no sidecar schema bump

## 1. Context

The diffah apply path (`diffah apply` / `diffah unbundle`) reconstructs a target image from `BASELINE-IMAGE + DELTA`. Today it is functionally correct on the happy path, but two operationally important failure modes give users either nothing actionable or no end-to-end guarantee at all.

**Current apply data flow** (`pkg/importer/importer.go::Import`):

```
extractBundle → resolveBaselines → for each image: composeImage
   composeImage:
     copy.Image(dest, src=bundleImageSource)
        bundleImageSource.GetBlob(d):
          d ∈ sidecar.Blobs?  yes → archive (full or zstdpatch.Decode)
                              no  → fetch from baseline (Task 5 reuse layer)
          servePatch needs sidecar.Blobs[d].PatchFromDigest from baseline
```

**Audit findings** (verified against `pkg/importer/compose.go`):

1. **No end-to-end invariant.** After `copy.Image` returns, nothing re-reads the destination to confirm "the layer set written to dest matches what the sidecar expected." Users implicitly trust `copy.Image`'s internal verification.
2. **Missing-baseline-layer errors are uncategorized.** Both `servePatch → fetchVerifiedBaselineBlob(PatchFromDigest)` failure and the baseline-only-reuse-layer fallback failure produce the same opaque `baseline serve <digest>: <underlying err>` string. Users cannot tell whether they need to re-run diff (B1: missing patch source) or supply a wider baseline (B2: missing reuse layer). No `errs.Categorized` — the error falls through to default classification.
3. **Multi-image bundles fail mid-way.** When `image-A` succeeds but `image-B`'s baseline turns out to be incomplete, the failure surfaces partway through `image-B`'s `copy.Image`, after possibly pushing several blobs. There is no upfront scan to identify all incomplete baselines before starting any apply work.
4. **No regression test** covers the user-visible scenarios "baseline contains layers also shipped in the delta" or "baseline is incomplete relative to the delta."

The correctness invariant *itself* is met (no duplicate, no missing layer in dest if the apply succeeds), but the diagnostic layer and the early-failure detection are not what an operationally-mature CLI demands.

## 2. Goals & Non-goals

### Goals

1. **Hard end-to-end invariant** — every successful `apply` proves the destination's layer set is exactly the sidecar's target expectation, with **no flag to disable**. Failures map to `CategoryContent` (exit 4) with explicit Missing/Unexpected diffs.
2. **Categorized failure for incomplete baselines** — distinguish "missing patch source" (B1) from "missing baseline-only reuse layer" (B2). Each gets a stable sentinel error type, a Classify rule, and an actionable hint.
3. **Fail-early via pre-flight** — before any `copy.Image` call, scan baseline manifests against sidecar expectations and decide which images can apply. Multi-image bundles in partial mode skip the unrecoverable images upfront instead of mid-stream.
4. **Backward compatibility** — no schema bump, no new flags. Existing apply test suite stays green; only `TestApplyCommand_RoundTrip` gains an invariant assertion.

### Non-goals

- Track B work (export-side delta size depth — `--candidates=all`, cross-image baseline pool, multi-codec race-and-pick). Out of scope.
- Performance optimizations on apply (dedup fast-path that prefers baseline-stored bytes over archive-stored bytes when both contain the same digest, cross-repo blob mount). Correctness already holds; perf is a follow-up track.
- `diffah dryrun --check-baseline` standalone subcommand. Pre-flight is built into apply; an explicit dryrun mode is a post-Track-A stretch.
- `diffah doctor` enhancements (Phase 5 DX polish).
- Streaming export / GB-scale benchmark (Phase 4 spec gaps tracked separately).
- New CLI flags or escape hatches (`--no-preflight`, `--no-verify-end-to-end`). All checks are mandatory.

## 3. High-level Architecture

apply path becomes four stages. Each new stage is a pure-read step except Stage 2 (existing).

```
┌──────────────────────────────────────────────────────────────────────┐
│ diffah apply / unbundle                                              │
└──────────────────────────────────────────────────────────────────────┘
        │
        ▼
   ┌──────────────────────────────────────────────────────────────────┐
   │ Stage 0 — extractBundle + parseSidecar  (existing)                │
   └──────────────────────────────────────────────────────────────────┘
        │
        ▼
   ┌──────────────────────────────────────────────────────────────────┐
   │ Stage 1 — Pre-flight   (NEW · PR3)                                │
   │   for each image in sidecar.Images:                               │
   │     • locate resolved baseline ImageSource                        │
   │     • GET baseline manifest + config (KB-level)                   │
   │     • compute requiredBaselineDigests                             │
   │     • diff against baseline.layers ∪ {config.digest}              │
   │     • classify per image: OK | B1 | B2 | PreflightError           │
   │     • emit streaming stderr report (partial mode)                 │
   │   decision:                                                       │
   │     strict mode + any non-OK → scan-all-then-abort, exit 4        │
   │     partial mode → keep OK list, mark non-OK as skipped           │
   └──────────────────────────────────────────────────────────────────┘
        │ (preflight pass list)
        ▼
   ┌──────────────────────────────────────────────────────────────────┐
   │ Stage 2 — composeImage  (existing) per OK image                   │
   │   copy.Image(dest, bundleImageSource)                             │
   │   GetBlob errors are now wrapped in B1/B2 sentinel  (PR1)         │
   └──────────────────────────────────────────────────────────────────┘
        │
        ▼
   ┌──────────────────────────────────────────────────────────────────┐
   │ Stage 3 — End-to-end invariant   (NEW · PR2)                      │
   │   for each successfully composed image:                           │
   │     • read dest manifest via NewImageSource                       │
   │     • assert layer digest set == sidecar expected                 │
   │     • assert per-layer size == sidecar.Blobs[d].Size              │
   │     • assert manifest digest == sidecar.Target.ManifestDigest     │
   │       (only when no schema conversion happened)                   │
   │   failure → ErrApplyInvariantFailed (Category=Content)            │
   └──────────────────────────────────────────────────────────────────┘
        │
        ▼
   ┌──────────────────────────────────────────────────────────────────┐
   │ Stage 4 — Final summary                                           │
   │   stderr: applied N/M; per failed image: name + category + hint   │
   │   exit code: 0 iff all images OK; 4 if any failure                │
   └──────────────────────────────────────────────────────────────────┘
```

**Key invariants:**

- Stages 1 and 3 are pure-read (no dest mutation, no rollback needed).
- Stage 2 is the only stage that writes to dest. Successfully pushed images are not rolled back when later images fail (docker:// push is irreversible; dir/oci-archive files are already on disk).
- A single image is atomic at the spec level: it must pass all of Stage 1+2+3 to be marked `applied`. Mid-Stage-2 failure marks the image `failed` even if some blobs were already pushed.
- The pipeline never proceeds past a sidecar schema error: Stage 1 raises it immediately and Stage 4 exits 4 regardless of `--strict`.

## 4. Detailed Design

### 4.1 PR1 — Error Classification

**New file** `pkg/importer/errors.go`:

```go
// ErrMissingPatchSource (B1) — baseline lacks the patch source layer that a
// shipped patch blob requires for zstd --patch-from reconstruction.
type ErrMissingPatchSource struct {
    ImageName       string
    ShippedDigest   digest.Digest
    PatchFromDigest digest.Digest
}

// ErrMissingBaselineReuseLayer (B2) — baseline lacks a layer that the target
// manifest references but the delta did not ship (baseline-only reuse path).
type ErrMissingBaselineReuseLayer struct {
    ImageName   string
    LayerDigest digest.Digest
}

// ErrApplyInvariantFailed — Stage 3 end-to-end check rejected the dest's
// reconstructed manifest. Declared in PR1 for layer consistency, populated
// by PR2's invariant verifier.
type ErrApplyInvariantFailed struct {
    ImageName  string
    Expected   []digest.Digest
    Got        []digest.Digest
    Missing    []digest.Digest // expected ∖ got
    Unexpected []digest.Digest // got ∖ expected
    Reason     string          // free-form: "manifest digest mismatch", "layer count mismatch"
}
```

Each implements `Error()` and `Category() errs.Category` returning `errs.CategoryContent`. The `Category()` method enables existing `errs.Classify` to find them via `errors.As`.

**New Classify hook entries** (`pkg/diff/errs/classify.go`):

| Sentinel | Hint format |
|---|---|
| `ErrMissingPatchSource` | `image %s: re-run 'diffah diff' against this baseline (patch source %s missing) or apply against the original baseline that produced this delta` |
| `ErrMissingBaselineReuseLayer` | `image %s: baseline must include layer %s which this delta did not ship — pin/add it or re-run diff with a wider baseline` |
| `ErrApplyInvariantFailed` | `image %s reconstructed mismatch (%s): missing %d layer(s), unexpected %d layer(s)` |

**Trigger sites** (`pkg/importer/compose.go`):

1. `servePatch::fetchVerifiedBaselineBlob(entry.PatchFromDigest)` — if the underlying error indicates "blob not found," wrap as `ErrMissingPatchSource{ImageName, target, entry.PatchFromDigest}`.
2. `GetBlob` baseline-only reuse branch — if `fetchVerifiedBaselineBlob(d)` fails with "blob not found," wrap as `ErrMissingBaselineReuseLayer{ImageName, d}`.

**"Blob not found" predicate** (helper `pkg/importer/errors.go::isBlobNotFound`):

```go
func isBlobNotFound(err error) bool {
    if errors.Is(err, types.ErrBlobNotFound) { return true }
    var statusErr interface{ StatusCode() int }
    if errors.As(err, &statusErr) && statusErr.StatusCode() == http.StatusNotFound {
        return true
    }
    return false
}
```

The exact set of underlying error types matched is verified by unit tests against fixtures from `containers-image`. If `containers-image` later changes its error surface, the predicate is the single update site.

**Boundary**: only "blob not found" is reclassified. Auth, TLS, network timeout, DNS and other underlying errors keep their existing `CategoryEnvironment` / `CategoryUser` classification — they are not baseline-incompleteness signals.

**Tests (PR1):**

| Test | Verifies |
|---|---|
| `TestErrMissingPatchSource_Format` | `Error()` format and `Category()` value |
| `TestErrMissingReuseLayer_Format` | same for B2 |
| `TestClassify_B1_HintFormat` | hint string contains shipped digest + patch source digest |
| `TestClassify_B2_HintFormat` | hint string contains image name + missing layer digest |
| `TestServePatch_NotFound_WrapsB1` | mock baseline source returning `ErrBlobNotFound` → `ErrMissingPatchSource` propagates with correct fields |
| `TestServePatch_AuthFailure_PreservesEnvironmentCategory` | same mock returning HTTP 401 → no B1 wrapping, original category preserved |
| `TestApplyCLI_MissingPatchSourceB1` (cmd integration) | exit 4, stderr contains B1 hint |
| `TestApplyCLI_MissingReuseLayerB2` (cmd integration) | exit 4, stderr contains B2 hint |

PR1 is independently shippable — without PR2/PR3 it already gives users actionable error messages.

### 4.2 PR2 — End-to-end Invariant

**New file** `pkg/importer/invariant.go`:

```go
// verifyApplyInvariant re-reads the dest manifest after a successful copy.Image
// and proves the layer set matches the sidecar's expectation.
func verifyApplyInvariant(
    ctx context.Context,
    img diff.ImageEntry,
    sidecar *diff.Sidecar,
    destRef types.ImageReference,
    sysctx *types.SystemContext,
) error
```

**Verification rules** (in order of strength):

| Rule | Strength | Failure |
|---|---|---|
| dest manifest layer digest set ⊆⊇ sidecar expected layer set | **mandatory** — layer bytes are content-addressed; never changes through schema conversion | `ErrApplyInvariantFailed` with `Missing` and `Unexpected` populated |
| dest manifest per-layer `size` equals `sidecar.Blobs[d].Size` | **mandatory** | `ErrApplyInvariantFailed{Reason: "layer size mismatch"}` |
| dest manifest digest equals `sidecar.Target.ManifestDigest` | **conditional** — only when dest manifest mediaType equals `sidecar.Target.MediaType` (i.e., no schema conversion happened) | `ErrApplyInvariantFailed{Reason: "manifest digest mismatch"}` |

The manifest-digest check is conditional because `copy.Image` legitimately rewrites manifests when source and dest mediaType differ (e.g., docker schema 2 source → OCI dest). Layer bytes and sizes never change through such conversion.

**Implementation outline:**

```go
func verifyApplyInvariant(...) error {
    expected, expectedMediaType, err := readSidecarTargetLayers(sidecar, img)
    if err != nil { return err }

    actualSet, actualMediaType, actualDigest, err :=
        readDestManifestLayers(ctx, destRef, sysctx)
    if err != nil {
        return fmt.Errorf("invariant: read dest manifest: %w", err)
    }

    if missing, unexpected := layerSetDiff(expected, actualSet); len(missing)+len(unexpected) > 0 {
        return &ErrApplyInvariantFailed{ImageName: img.Name, ...}
    }
    if err := verifyPerLayerSize(expected, actualSet, sidecar.Blobs); err != nil {
        return err
    }
    if expectedMediaType == actualMediaType && actualDigest != img.Target.ManifestDigest {
        return &ErrApplyInvariantFailed{
            ImageName: img.Name, Reason: "manifest digest mismatch", ...
        }
    }
    return nil
}
```

`readSidecarTargetLayers` retrieves `sidecar.Blobs[img.Target.ManifestDigest]` (an `EncodingFull` blob in the archive's blob dir), parses it via `manifest.Schema2FromManifest` or `manifest.OCI1FromManifest` based on `img.Target.MediaType`.

`readDestManifestLayers` calls `destRef.NewImageSource(ctx, sysctx)` then `GetManifest(ctx, nil)`, parsing in the same way. Closes the source on return.

**Integration point** (`pkg/importer/importer.go::importEachImage`, around current `composeImage` call):

```go
if err := composeImage(ctx, img, bundle, rb, destRef, opts); err != nil {
    record(img.Name, "compose", err)
    if opts.Strict { return err }
    continue
}
if err := verifyApplyInvariant(ctx, img, bundle.sidecar, destRef, sysctx); err != nil {
    record(img.Name, "invariant", err)
    if opts.Strict { return err }
    continue
}
```

**Final summary renderer** (used by both PR2 and PR3 for Stage 4 output):

```
diffah: applied 3/4 images
  ✓ svc-a: applied + verified
  ✓ svc-b: applied + verified
  ✗ svc-c: applied with invariant mismatch
      missing 1 layer in dest: sha256:abc...
      unexpected 0 layer
      hint: dest may be partially written; manual cleanup recommended
  - svc-d: skipped (preflight: missing patch source)

note: dest may contain partially-written images from this run.
manual cleanup is required for any image marked failed/mismatch above.
```

**Tests (PR2):**

| Test | Verifies |
|---|---|
| `TestVerifyApplyInvariant_HappyPath_unit` | layer set match passes |
| `TestVerifyApplyInvariant_LayerMissing_unit` | mock dest source returning manifest with one fewer layer → `ErrApplyInvariantFailed` with `Missing` populated |
| `TestVerifyApplyInvariant_LayerUnexpected_unit` | mock with extra layer → `Unexpected` populated |
| `TestVerifyApplyInvariant_SizeMismatch_unit` | mock with same digest set but wrong per-layer size → `Reason="layer size mismatch"` |
| `TestVerifyApplyInvariant_AcrossSchemaConversion_unit` | dest mediaType differs from sidecar; layer set matches → pass; manifest digest check skipped |
| `TestApplyCommand_RoundTrip` (existing, modified) | adds `verifyApplyInvariant` assertion at end |
| `TestApplyCLI_RoundTripWithInvariantPasses` (new integration) | end-to-end via real registrytest |

PR2 depends on PR1 only for the shared sentinel-error file `errors.go`; declaring `ErrApplyInvariantFailed` together with B1/B2 keeps the importer's error surface in one file.

### 4.3 PR3 — Pre-flight

**New file** `pkg/importer/preflight.go`:

```go
type PreflightStatus int
const (
    PreflightOK PreflightStatus = iota
    PreflightMissingPatchSource    // B1
    PreflightMissingReuseLayer     // B2
    PreflightError                 // baseline manifest unreachable / parse failure
    PreflightSchemaError           // sidecar inconsistency
)

type PreflightResult struct {
    ImageName            string
    Status               PreflightStatus
    MissingPatchSources  []digest.Digest
    MissingReuseLayers   []digest.Digest
    Err                  error // populated for PreflightError / PreflightSchemaError
}

// RunPreflight returns per-image results and a flag indicating whether any
// image is non-OK. Caller decides partial vs strict handling.
//
// anyFailure is true iff at least one result has Status != PreflightOK.
// PreflightError counts as a failure for both partial and strict modes;
// in partial mode the affected image is skipped (not aborted). PreflightSchemaError
// is a fatal abort condition returned via the err return value, not via results.
func RunPreflight(
    ctx context.Context,
    bundle *extractedBundle,
    resolved []resolvedBaseline,
    sysctx *types.SystemContext,
    reporter progress.Reporter,
) (results []PreflightResult, anyFailure bool, err error)
```

**Per-image scan algorithm:**

```
for each img in bundle.sidecar.Images:
    1. parse sidecar.Blobs[img.Target.ManifestDigest] as target manifest
       extract: targetLayers = layers[].digest (set)
       on failure → PreflightSchemaError, abort entire RunPreflight
    2. compute:
       reuseLayers = targetLayers ∖ sidecar.Blobs.Keys()       # B2 candidates
       patchSrcs   = { sidecar.Blobs[d].PatchFromDigest
                       for d in targetLayers
                       where sidecar.Blobs[d].Encoding == Patch
                       and sidecar.Blobs[d].PatchFromDigest != "" }
       required    = reuseLayers ∪ patchSrcs
    3. resolvedBaseline.Src.GetManifest(ctx, nil) → baseline manifest
       on failure: PreflightResult{Status: PreflightError, Err: classified}
       continue (next image)
       extract: baselineSet = layers[].digest ∪ {config.digest}
    4. classify:
       missingPatchSrcs   = patchSrcs   ∖ baselineSet
       missingReuseLayers = reuseLayers ∖ baselineSet
       Status =
         OK                  if both sets empty
         MissingPatchSource  if missingPatchSrcs non-empty (regardless of B2 — B1 takes precedence)
         MissingReuseLayer   if only missingReuseLayers non-empty
       record result with both digest slices populated independently of Status
       (Status reflects the dominant failure category for hint selection;
        consumers of PreflightResult inspect both slices for full diagnostics)
    5. reporter.Update(phase="preflight", img.Name, formatResult(result))
       (streaming stderr; partial mode users see results as scan progresses)
```

**Network cost analysis**: pre-flight calls `GetManifest` on the same `resolvedBaseline.Src` instance that Stage 2's `copy.Image` later consumes. Whether that translates to zero or one extra round-trip per baseline depends on the underlying `containers-image` source's caching behavior, which varies by transport. Best case (transport caches manifest after first read): zero extra GETs. Worst case (transport re-fetches): exactly one extra KB-level manifest GET per baseline image — orders of magnitude smaller than the layer-body fetches pre-flight saves by failing early.

`baselineSet` includes the config digest because `copy.Image` will fetch config when reconstructing the dest image; if config is missing, apply will fail just as surely as if a layer were missing.

**Decision logic** (`pkg/importer/importer.go::Import`, before `importEachImage`):

```go
results, anyFailure, err := RunPreflight(ctx, bundle, resolved, sysctx, reporter)
if err != nil { return err } // schema errors are fatal regardless of mode
if opts.Strict && anyFailure {
    return abortWithPreflightSummary(results) // list ALL failures, exit 4
}
applyList := filterOK(results)
if len(applyList) == 0 {
    return abortWithPreflightSummary(results) // exit 4: nothing to apply
}
proceedWith(applyList) // partial mode: apply only OK images
```

**Strict mode invariant** — scan all images first, then abort with the complete list. Don't fail on first non-OK; the user wants every problem visible at once.

**Streaming stderr report** (partial mode):

```
[preflight] scanning 4 images...
[preflight] svc-a: OK
[preflight] svc-b: OK
[preflight] svc-c: missing 1 patch source (sha256:def...) — will skip
[preflight] svc-d: missing 2 baseline-only layers — will skip
[preflight] applying 2/4 images (skipping 2 — see report at end)
```

Output via `progress.Reporter` so JSON mode emits structured per-image events; text mode emits the lines above.

**`--strict` semantic extension**: existing `--strict` (currently: "baseline spec missing for image-X is an error") expands to include "baseline incomplete for image-X is an error." The CLI flag name and help text stay the same; only the semantic coverage widens. Document this in CHANGELOG.

**Tests (PR3):**

| Test | Verifies |
|---|---|
| `TestPreflight_AllOK_unit` | full sidecar + full baseline → all OK |
| `TestPreflight_B1_unit` | sidecar with patch blob, baseline missing patch source → MissingPatchSource |
| `TestPreflight_B2_unit` | sidecar without reuse layer ship, baseline missing reuse layer → MissingReuseLayer |
| `TestPreflight_BothB1AndB2_unit` | both missing → MissingPatchSource with both fields filled |
| `TestPreflight_BaselineUnreachable_unit` | mock baseline GetManifest returning 503 → PreflightError, no abort |
| `TestPreflight_SchemaError_unit` | sidecar.Target.ManifestDigest not in sidecar.Blobs → returns error, aborts scan |
| `TestPreflight_ConfigDigestMissing_unit` | baseline lacks config.digest → MissingReuseLayer with config digest |
| `TestApplyCLI_PartialModeSkipsB2_int` | 4-image bundle, svc-c B2 → exit 0, dest contains svc-a/b/d, summary lists svc-c skipped |
| `TestApplyCLI_StrictModeAbortsAfterFullScan_int` | same bundle + `--strict` → exit 4, dest empty, stderr lists all non-OK images |
| `TestApplyCLI_PreflightManifestFetchBounded_int` | mock registry counts baseline manifest GETs ≤ 2 across pre-flight + apply (proves pre-flight does not regress to per-image multiple round-trips) |
| `TestApplyCLI_PartialModeAllFail_int` | every image B1/B2 → exit 4, dest empty, summary lists all skipped |

PR3 depends on PR1 (preflight result stringer reuses B1/B2 hint format) and PR2 (final summary renderer is shared).

## 5. Testing Strategy

### Per-PR test pyramid

Each PR carries its own units (~70%) and integrations (~20%). The full Track A integration matrix consolidates user scenarios in cmd-level tests:

| ID | User scenario | Test | PR |
|---|---|---|---|
| U1 | baseline contains layers also shipped in delta | `TestApplyCLI_BaselineHasShippedLayer` | PR2 (regression coverage as part of round-trip + invariant) |
| U2 | baseline missing patch source (B1) | `TestApplyCLI_MissingPatchSourceB1` | PR1 |
| U3 | baseline missing reuse layer (B2) | `TestApplyCLI_MissingReuseLayerB2` | PR1 |
| U4 | multi-image bundle, partial mode skips one B1 | `TestUnbundleCLI_PartialModeSkipsB1` | PR3 |
| U5 | same with `--strict`, scan-all-then-abort | `TestUnbundleCLI_StrictAbortsAfterFullScan` | PR3 |
| U6 | baseline manifest 503 in partial mode | `TestApplyCLI_PreflightBaselineUnreachable` | PR3 |
| U7 | round-trip happy path with invariant | existing `TestApplyCommand_RoundTrip` (modified) | PR2 |
| U8 | docker-schema2 → OCI conversion path | `TestApplyCLI_InvariantPassesAcrossSchemaConversion` | PR2 |
| U9 | pre-flight does not regress baseline manifest GET count | `TestApplyCLI_PreflightManifestFetchBounded` | PR3 |
| U10 | injected dest manifest deviation | `TestVerifyApplyInvariant_LayerMissing_unit` | PR2 |

### Fixture strategy

- **No new committed test fixtures.** Existing v1/v3/v4 OCI archives under `testdata/fixtures/` provide all needed real images.
- **Synthetic incomplete baselines** are generated at test setup by stripping a layer from an existing baseline OCI archive (helper: `testutil.MakeIncompleteBaseline(srcArchive, omitLayer)`). Output kept in `t.TempDir()`, not committed.
- B1/B2 boundary fixtures exercised purely through these synthetic mutations.

### Race detector coverage

All new package-level tests in `pkg/importer/` and all new cmd-level integration tests run under `go test -race`. Pre-flight introduces a per-image GetManifest call on `resolvedBaseline.Src`; this must not race against subsequent `composeImage` reads via the same source.

### Test count delta

- PR1: ~6 unit + 2 integration = **8 new tests**
- PR2: ~5 unit + 2 integration + 1 modified existing = **7 new + 1 modified**
- PR3: ~7 unit + 4 integration = **11 new tests**
- Total: **~26 new + 1 modified**

## 6. Backward Compatibility

| Surface | Change |
|---|---|
| Sidecar schema (`Sidecar`, `BlobEntry`, `ImageEntry`) | None |
| Exporter | None |
| CLI flags | None added; `--strict` semantic widens to cover B1/B2 (CHANGELOG entry required) |
| Exit code taxonomy | None (still 0/1/2/3/4) |
| Existing apply tests | All retained green; only `TestApplyCommand_RoundTrip` gains an invariant assertion |
| Old archive × new binary | Compatible — pre-flight and invariant infer required digests from existing sidecar fields |
| New archive × old binary | Compatible — old binary skips new stages, behaves as PR #26 era |

## 7. Edge Cases & Error Handling

### Context cancellation

- Stage 1 / Stage 3 are new code; they explicitly check `ctx.Done()` before each per-image sub-step (`GetManifest`, layer-set diff).
- Stage 2 is `copy.Image`'s responsibility; it already handles cancellation.
- Cancellation in any stage halts the entire pipeline regardless of partial/strict mode (cancellation is an explicit operator signal, not a baseline-incompleteness signal).

### Pre-flight network errors

- `baseline.GetManifest` failures route through existing `diff.ClassifyRegistryErr` → `errs.CategoryEnvironment` for transient errors / `errs.CategoryUser` for auth errors.
- `--retry-times` and `--retry-delay` already configure `containers-image` retry behavior; pre-flight reuses this without adding a separate retry loop.
- Per-image `PreflightError` does not abort other images in partial mode (treated like B1/B2 skip).

### All-images-fail in partial mode

- All images either skip or fail → exit 4 (zero successful applies = failed run).
- Distinct from "0-image bundle" which is rejected at sidecar validation as a schema error.

### Schema errors (sidecar self-inconsistency)

- `img.Target.ManifestDigest` not in `sidecar.Blobs`, parse failure on target manifest, etc. → `PreflightSchemaError` aborts the entire `RunPreflight` and returns an error from `Import`.
- These are not baseline-incompleteness errors; partial mode cannot rescue them.

### Single-image bundles

- Treated identically to multi-image (no specialization). Stage 1, 2, 3, 4 all run; partial mode with N=1 means a non-OK image makes the run fail (no other images to fall back to).

### Dest already partially written

- Successfully composed images are not rolled back when later images fail or invariant rejects.
- Stage 4 summary explicitly notes this:
  > `note: dest may contain partially-written images from this run. manual cleanup is required for any image marked failed/mismatch above.`
- docker:// dest: user manually deletes the tag.
- dir:/oci-archive: dest: user manually deletes the file/directory.

### Escape hatches — none

- No `--no-preflight`, no `--no-verify-end-to-end`. Both checks are mandatory by design (Goal #1).
- Only behavior switch is the existing `--strict` (partial vs strict mode).

### Coexistence with PR #26 baseline blob cache

- Pre-flight reads only manifest + config; never invokes `s.cache`. Cache continues to dedup layer body fetches in Stage 2.
- Invariant reads dest manifest only; cache uninvolved.

### Coexistence with Phase 3 cosign signing

- `verifyApplyInvariant` runs after signature verification (which already uses `Phase("verifying")`). Both share the verifying phase in progress reporter.
- Signature failure (cosign mismatch) and invariant failure (manifest mismatch) are independent; both map to `CategoryContent`.

## 8. Open Questions

None blocking. Two items to decide during implementation:

1. **Final summary line wrapping width** — Stage 4 stderr report should respect terminal width via `progress.Reporter` formatting helpers; specifically what "wrap at 80 cols vs detect TTY width" the reporter uses is an implementation detail.
2. **Preflight progress percentage** — for very large bundles (100+ images), should pre-flight emit a `<n>/<N>` counter? Defer; the per-image streaming output is already sufficient feedback.

## 9. Out of Scope

Listed for clarity; track separately:

- Track B (export-side delta size depth)
- apply-side performance: dedup fast-path that prefers baseline bytes over archive bytes when both contain the same digest, cross-repo blob mount
- `dryrun --check-baseline` standalone subcommand
- `diffah doctor` enhanced checks (Phase 5)
- Streaming export, GB-scale benchmark (Phase 4 spec gap)
- Schema v2 (no schema bump in this track)

## 10. Acceptance Criteria

A Track A release ships when **all** hold:

1. `pkg/importer/errors.go` defines `ErrMissingPatchSource`, `ErrMissingBaselineReuseLayer`, `ErrApplyInvariantFailed`. All three implement `Category() == errs.CategoryContent`. Classify hooks render hints matching §4.1.
2. Failed apply on a B1 baseline produces exit 4 with the B1 hint visible in stderr.
3. Failed apply on a B2 baseline produces exit 4 with the B2 hint visible in stderr.
4. `verifyApplyInvariant` runs after every successful `composeImage`. `TestApplyCommand_RoundTrip` asserts no invariant error. A unit test confirms invariant rejects an injected manifest with a missing layer.
5. `RunPreflight` runs before `importEachImage`. Multi-image bundle with one B1 image in partial mode produces exit 0 and dest contains exactly the OK images. Same bundle with `--strict` produces exit 4 and dest unchanged.
6. mock-registry test confirms apply lifecycle issues exactly one baseline manifest GET per image (pre-flight does not add extra round-trips).
7. CHANGELOG entry documents `--strict` semantic extension.
8. All existing apply tests green under `go test -race`.
