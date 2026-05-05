# diffah Import Streaming I/O — Design

- **Status:** Draft
- **Date:** 2026-05-04
- **Author:** @leosocy
- **Implements:** Closes the symmetric OOM gap acknowledged in [`2026-05-02-export-streaming-io-design.md`](2026-05-02-export-streaming-io-design.md) §9, plus three G7 acceptance items (I4 / I5 / I6) from the production-readiness roadmap.
- **Scope:** Importer (`apply` / `unbundle`) and the shared admission abstraction lifted from `pkg/exporter`. Exporter cleanup matrix (I7) and `encodePool` panic recover (M3) are tracked as a separate exporter-hardening PR.

## 1. Context

`diffah apply` / `unbundle` today accumulates baseline blob bytes and patch payloads in memory. Three RAM hot spots compound:

| Hot spot | Code | Memory shape |
|---|---|---|
| Baseline retention | `pkg/importer/blobcache.go` `baselineBlobCache.bytes` | `Σ baseline_layer_size` for every distinct verified baseline digest, pinned for the entire `Import()` |
| Per-blob serving | `pkg/importer/compose.go::bundleImageSource.serveFull/servePatch` | Full `[]byte` materialization of each shipped blob via `io.ReadAll` then `bytes.NewReader` |
| Patch decode | `pkg/importer/compose.go:140` calling `zstdpatch.Decode([]byte, []byte)` | `ref_bytes + patch_bytes + decoded_target_bytes` simultaneously resident |

A 4 GiB layer therefore needs at least `~4 GiB` (baseline cache) + `~4 GiB` (patch decode `ref_bytes`) + decoded output to be applied. The exporter Phase-4 contract ("4 GB-layer image within 8 GB RAM") is broken on the consumer side: a bundle that exporter happily produces under 8 GiB cannot be applied by `diffah apply` under the same budget.

A second compounding factor: `importEachImage` is a serial for-loop over `applyList` (`pkg/importer/importer.go:192-210`); even if memory were not the constraint, multi-image bundles run one image at a time. Apply throughput is single-threaded across images.

Two structural debts also accumulate:

- `pkg/exporter/{admission,pool,workerpool}.go` contains a worker pool + dual-semaphore admission controller + singleflight that is generic but lives inside the exporter package. Importer cannot reuse it without violating dependency direction.
- `pkg/importer/compose.go:139` carries a `//nolint:staticcheck` directive that pins the deprecated `zstdpatch.Decode([]byte, []byte)` API in production; that nolint will not retire until the importer migrates to `DecodeStream`.

## 2. Goals

A release meets the import-streaming-I/O bar when **all five** criteria hold:

1. **Memory contract.** Peak RSS during `Import()` ≤ a configurable budget (default 8 GiB). The single biggest applied image must fit the budget; aggregate bundle size does not affect peak RSS.
2. **Bit-exact apply output.** The composed image manifest digest is byte-identical across `--workers` settings (1, 2, 4, 8) for a fixed bundle + baselines. (Existing exporter `determinism_test.go` byte-identity gate stays green throughout.)
3. **Bandwidth parity.** Each baseline blob is fetched from its source ref at most once per `Import()`. (`baselineBlobCache.GetOrLoad` already provides this via singleflight; the streaming refactor preserves the contract while moving bytes from RAM to disk.)
4. **CLI symmetry.** `apply` / `unbundle` accept `--workdir`, `--memory-budget`, `--workers` with the same semantics as `diff` / `bundle`. `pkg/config.Default()` exposes matching `apply-*` keys.
5. **Cleanup.** Successful or failed `Import()` — including `ctrl-C`, partial-mode failures, and worker-goroutine panics — leaves no temp files behind under the apply workdir.

Plus three G7 acceptance items from the roadmap:

- **I4.** `pkg/diff/sidecar.ParseSidecar` honors the `docs/compat.md:31-32` contract: unknown optional fields are detected and recorded via `slog.Debug` (not silently dropped).
- **I5.** A regression test synthesizes zstd frames with `windowLog ∈ {28, 29, 30, 31}` and asserts the importer rejects them with `errs.CategoryContent` (Phase 3 importer was capped at `windowLog = 27`; the importer must remain fail-closed when a future producer exceeds the cap).
- **I6.** A committed `<200 KiB` Phase-3-vintage bundle fixture round-trips through the current importer with byte-identical output, pinning the spec §6.2 cross-version contract.

## 3. Non-goals

- **Rekor transparency-log verification (option B).** `pkg/signer/verifier.go::verifyRekorBundle` stays a stub; tracked separately. The pre-grooming PR before this spec adds explicit "reserved" errors on `--verify-rekor-url`.
- **Exporter §7.5 cleanup matrix (I7) and `encodePool` panic recover (M3).** The shared admission abstraction (§5.1) DOES introduce panic recover at the worker layer — exporter inherits this for free — but the dedicated cleanup-matrix tests (ctx-cancel / panic / disk-full on `Export()`) are out of scope here. They land in a separate exporter-hardening PR after this spec ships.
- **Per-blob fan-out within a single image.** `copy.Image` already runs limited internal blob concurrency; layering an explicit per-blob worker pool inside one image apply complicates RSS bounding without clear throughput value. Out of scope.
- **Sidecar v2 schema bump.** Admission decisions are entirely importer-internal; no new sidecar fields.
- **Importing into containerd / runtime daemons.** Output transports stay what `alltransports` accepts.
- **Backward-incompatible CLI changes.** All new flags have sane defaults; existing `apply` / `unbundle` invocations behave identically (sequential apply, default 8 GiB budget, in-process default workdir).

## 4. Architecture

### 4.1 Pipeline shape

`Import` becomes a **prime baselines (parallel) → apply images (admission-gated, parallel)** pipeline. A single per-`Import()` workdir holds three on-disk stores plus the existing extracted bundle:

```
<workdir>/
  bundle/                ← extracted bundle (existing extractedBundle layout)
  baselines/<digest>     ← one file per distinct verified baseline layer
  scratch/<image>/       ← per-image scratch (decoded target, patch I/O)
```

Data flow:

```
delta.tar
  ├─ extractBundle               → <workdir>/bundle/
  ├─ verifySignature (optional)
  ├─ resolveBaselines             (existing, unchanged)
  ├─ preflight                    (existing, unchanged)
  ├─ primeBaselineSpool           (parallel, internal/admission.WorkerPool, singleflight)
  │     for each baseline digest referenced by patch entries:
  │       fetch via existing GetBlob → TeeReader → digest verifier + spool to disk
  └─ applyImagesPool              (internal/admission.AdmissionPool)
       for each image in applyList (concurrent, gated by --workers + --memory-budget):
         applyOneImage
           composeImage
             for each layer:
               EncodingFull   → archive blob → klauspost decoder → dest
               EncodingPatch  → DecodeStream(refPath, patchPath, scratchPath) → dest
                                 (refPath comes from baselines/; patchPath from bundle/blobs/)
             push composite to dest
```

### 4.2 Workdir lifecycle

- **Resolution precedence** (`pkg/diff/workdir.go`, lifted shared from `pkg/exporter/workdir.go`): explicit `--workdir` flag → `DIFFAH_WORKDIR` env → colocated with output (sibling of the first output ref's parent dir for `apply`; sibling of the bundle path for `unbundle`) → `os.TempDir()` fallback for callers without an output path (e.g., dry-run, library callers).
- A unique per-`Import()` suffix (`.diffah-tmp/<random>`) is appended; concurrent invocations on the same machine are isolated.
- Cleanup on every exit path — see §7.

### 4.3 Concurrency model

**Single-axis: per-image.** The bundle apply loop (`importEachImage`) becomes an `internal/admission.AdmissionPool`. Within a single image, `composeImage` keeps its current per-layer behavior (whatever `copy.Image` does internally, plus the new disk-streamed `serveFull/servePatch`).

**Strict mode interaction.** `Strict` mode preserves "abort on first error" semantics — admission pool's `errgroup` cancellation propagates to siblings; siblings already running finish their current step and surface a partial report. Partial mode runs all images regardless and aggregates errors.

**HasThreadSafeGetBlob.** Currently `bundleImageSource.HasThreadSafeGetBlob` delegates to the baseline source. After this work, the bundle-side reader is fully disk-backed and inherently thread-safe; the method returns `true` unconditionally. (Lift gate: PR5 in §11 — PR4 lands the disk-backed reader, PR5 flips the flag once concurrent-reader tests are green.)

## 5. Components

### 5.1 `internal/admission/` (new shared package)

Lifted from `pkg/exporter/{admission,pool,workerpool}.go`. Three primary types:

- `WorkerPool` — thin `errgroup` wrapper with ctx propagation. Submitted work runs on bounded worker count. Adds a recover-and-translate-to-error layer at the goroutine boundary (this is what closes M3 transparently for the exporter side).
- `AdmissionPool` — singleflight + `workerSem` (worker count) + `memSem` (memory budget). `Submit(key, estimate, fn)` first dedups on key, then acquires both semaphores in fixed order (workerSem first, then memSem), runs fn, releases both on return.
- `Semaphore` — alias for `golang.org/x/sync/semaphore.Weighted`; helper for ctx-aware Acquire that returns `ctx.Err()` on cancel.

Critical invariants (carried verbatim from Phase 4 amendments — these are the contract):

1. `singleflight` runs **outside** the semaphores: dedup before resource acquisition, not after. (Phase 4 amendment #11 shape.)
2. Acquisition order is `workerSem → memSem`, fixed; reverse order would deadlock under exhaustion.
3. The recover boundary lives in the `Pool.Submit` worker closure — any panic in user-supplied `fn` is converted to a `error` returned via the errgroup. `defer cleanup()` at higher levels still fires.
4. Admission with `memBudget == 0` disables `memSem` entirely (operator opt-out for benchmarks); `workerSem` still applies.

`pkg/exporter/admission.go` retains only the exporter-specific RSS estimate table (`estimateRSSForWindowLog`, `ResolveWindowLog`, `checkSingleLayerFitsInBudget`) and constructs an `admission.AdmissionPool` internally. `pkg/exporter/pool.go::workerPool` and `encodePool` keep their existing exterior API and forward to the shared package.

`pkg/importer/admission.go` (new) constructs an `admission.AdmissionPool` with importer-specific RSS estimation (§5.6) and an importer-specific `checkSingleImageFitsInBudget` helper.

### 5.2 `internal/workdir/` (new shared)

Lifted from `pkg/exporter/workdir.go`. Two exported entry points:

- `Ensure(workdir string, hint string) (path string, cleanup func(), err error)` — `hint` is a path "near where the output goes" used to derive the default base; for export it's the output file path, for import it's the first output ref or bundle path.
- `Resolve(workdir string, hint string) string` — pure-resolution variant, no side effects.

Both honor the precedence order in §4.2. `pkg/exporter/workdir.go` becomes a thin wrapper that forwards.

`internal/workdir/` is preferred over `pkg/diff/workdir.go` because workdir is an I/O helper; `pkg/diff/` is a pure domain package and acquiring side-effecting code there breaks the layer's character.

### 5.3 `pkg/importer/baselinespool.go` (new)

Disk-backed baseline blob spool, mirror of `pkg/exporter/baselinespool.go`. Differences and shared invariants:

| Trait | Exporter spool | Importer spool |
|---|---|---|
| Priming | `primeBaselineSpool` upfront across all pairs | `primeBaselineSpool` upfront across all images-in-applyList |
| Consumer | Fingerprinter + `Planner.PlanShippedTopK` | `bundleImageSource.servePatch` (path-based) |
| Singleflight | Yes — dedups concurrent fetches of same digest | Yes — same shape |
| Drain on return | Yes (Phase 4 amendment #9) | Yes — identical pattern |
| Atomic rename | `os.CreateTemp + Sync + Close + Rename` (amendment #18) | Identical |
| `committed` sentinel | Yes (amendment #11) | Yes |
| Failure mode | Soft (logs Warn, planner falls back to size-only) | Soft for prime; hard at consume time (apply fails for that image only — `errs.CategoryContent` if blob is required and missing) |

API shape:

```go
type BaselineSpool struct{ /* singleflight, dir, ... */ }

func NewBaselineSpool(dir string) *BaselineSpool

func (s *BaselineSpool) GetOrSpool(
  ctx context.Context,
  d digest.Digest,
  fetch func(digest.Digest) (io.ReadCloser, error),
) (path string, err error)

func (s *BaselineSpool) Path(d digest.Digest) (string, bool)
```

### 5.4 `pkg/importer/blobcache.go` — DELETED

`baselineBlobCache` is removed. Every `s.cache.GetOrLoad(...)` callsite migrates to `s.spool.GetOrSpool(...) → path`. The `[]byte`-shaped cache and its tests are replaced by `BaselineSpool` + `baselinespool_test.go`. The deletion is a single PR (PR3 in §11).

### 5.5 `bundleImageSource.serveFull` / `servePatch` rewrite

**`serveFull`**: instead of `io.ReadAll(blobReader)` + `bytes.NewReader(allBytes)`, the source returns a path-backed `io.ReadCloser` that streams from `<workdir>/bundle/blobs/<digest>` directly. The reader wraps an optional `progress.Layer` for per-blob byte counting (closes I8 / Phase 2 G6).

**`servePatch`**: instead of materializing `refBytes` and `patchBytes`, the source resolves both to paths (`baselineSpool.Path(refDigest)` and the bundle blob path) and shells out via `zstdpatch.DecodeStream(refPath, patchPath, outPath)`. The output path lives under `<workdir>/scratch/<image>/<digest>`. The function returns an `io.ReadCloser` over `outPath`; closing the reader removes the scratch file.

Both functions honor `HasThreadSafeGetBlob = true` after PR5. The `nolint:staticcheck` at `compose.go:139` is removed once the migration completes (PR4).

### 5.6 RSS estimation for apply

Per-image RSS estimate is the `max` over the image's layers; per-layer estimate is keyed on the layer's `windowLog`:

| Layer encoding | RSS estimate | Rationale |
|---|---|---|
| `EncodingFull` | `2 × (1 << windowLog)` | klauspost decoder window + buffer (Go-side process RAM) |
| `EncodingPatch` | `2 × (1 << windowLog) + small overhead` | zstd CLI subprocess: decoder window + ref OS-cached + small framing buffer; ref bytes are OS-cached file pages, not Go heap |

This matches the exporter's `estimateRSSForWindowLog` table verbatim — encoders need more memory than decoders for the same `windowLog`, so the exporter table is a conservative upper bound for apply. Reusing it costs some throughput (we admit fewer concurrent applies than the budget would allow at perfect estimates) but ensures we never OOM. Tuning is future work.

`checkSingleImageFitsInBudget(images, windowLog, memBudget)` runs before `applyImagesPool` opens; it walks every image's layers, computes the max per-layer estimate per image, and returns a structured `errs.CategoryUser` error if any single image exceeds the budget.

### 5.7 Sidecar parser hardening (I4)

`pkg/diff/sidecar.go::ParseSidecar` runs two passes:

1. **Probe pass.** `decoder := json.NewDecoder(bytes.NewReader(raw)); decoder.DisallowUnknownFields(); _ = decoder.Decode(&strict)` — failure surfaces unknown field names via the standard `json: unknown field "<name>"` error. The probe error is captured into a `[]string` (parsed via the standard error format) and emitted as `slog.Debug("sidecar has unknown optional fields", "fields", names)`.
2. **Lenient pass.** Standard `json.Unmarshal(raw, &sidecar)` runs as today; unknown fields are silently dropped. Existing behavior preserved.

Schema-error categorization (`errs.CategoryContent`) is unchanged; the probe pass never returns its error to the caller.

### 5.8 windowLog ≥ 28 fail-closed test (I5)

A new test file `pkg/importer/decode_windowlog_test.go` synthesizes zstd frames with `WithWindowSize(1 << N)` for `N ∈ {28, 29, 30, 31}` (using `klauspost/compress/zstd`'s encoder with a captured payload), wraps them in a minimal one-blob `EncodingFull` archive fixture, and asserts the importer's decode path returns an error categorized as `errs.CategoryContent`. The error message is asserted to mention "Frame requires too much memory" (klauspost) or "Window size larger than" (CLI zstd) so an operator can identify the cause.

### 5.9 Phase-3 fixture pin (I6)

`testdata/fixtures/phase3-bundle-min.tar` is a committed `<200 KiB` bundle generated using v0.1.0 (Phase-3-vintage) `diffah bundle` semantics (single image, two layers, one patch, one full). A new test `pkg/importer/compose_phase3_test.go` applies the fixture in-process to an `oci-archive:` destination and asserts the destination manifest's digest matches a `wantManifestDigest` constant committed alongside the fixture. Drift in any subsequent importer change that touches `composeImage`, `bundleImageSource`, or `zstdpatch.DecodeStream` will fail this test.

## 6. CLI surface changes

Three new persistent flags on `apply` and `unbundle`:

```
Spool & memory:
      --workdir PATH          spool directory; default colocated with output
                              (env: DIFFAH_WORKDIR)
      --memory-budget SIZE    peak RSS budget; default 8GiB; 0 disables admission
                              (env: DIFFAH_MEMORY_BUDGET)
      --workers N             max concurrent image applies in a bundle; default 8
                              (env: DIFFAH_WORKERS)
```

Defaults match exporter for symmetry. Config file gains:

```yaml
apply-workdir: ""
apply-memory-budget: "8GiB"
apply-workers: 8
```

(per-command keys; the global ones stay export-side. `cmd/config_defaults_test.go` adds three table rows asserting `f.DefValue == d.<Field>`.)

## 7. Cleanup contract

| Path | Workdir cleaned? | Mechanism |
|---|---|---|
| Successful `Import()` | yes | top-level `defer cleanup()` |
| Single-image apply error (partial mode) | yes | error captured in `ApplyReport`; loop continues; cleanup at top defer |
| All-image failure (partial: 0 successful) / strict-mode early return | yes | error returned from `Import`; cleanup at top defer |
| `ctx.Done()` mid-apply | yes | `admission.Pool` Acquire returns `ctx.Err()`; sibling workers cancel; cleanup at top defer |
| Worker goroutine panic in `applyOneImage` | yes | `internal/admission` recover converts to errgroup error; cleanup at top defer |
| `disk full` while writing baselineSpool | yes (partial file) | `committed=false → defer os.Remove(tmp)`; image error in report; cleanup at top defer |
| Process kill (SIGKILL) | orphan accepted | aspirational orphan-reclaim helper is out of scope here; documented limitation matches Phase-4 export side |

The exporter's symmetric cleanup matrix (I7) is **not** acceptance-locked here — it lands in a separate exporter-hardening PR after this spec.

## 8. Testing strategy

### 8.1 Unit (lifted)

`internal/admission/*_test.go` — migrated from `pkg/exporter/{admission,pool,workerpool}_test.go`:

- AdmissionPool dedup via singleflight (concurrent same-key submits dedupe to one execution).
- workerSem + memSem dual-gate behavior; saturation; ctx-cancel-during-Acquire returns `ctx.Err()`.
- WorkerPool ctx propagation; first-error-wins; remaining workers cancel.
- **NEW: panic recover at worker boundary** — submit a fn that panics; assert pool returns a non-nil error; assert other in-flight workers are cancelled; assert no goroutine leaks.

### 8.2 Unit (importer)

`pkg/importer/baselinespool_test.go` — mirror of exporter's:

- Drain-on-return regression (partial-reading consumer reading 16 bytes of a larger source).
- Concurrent same-digest writers with **distinct** payloads (atomic rename gate; mutation-test by replacing rename with direct OpenFile and asserting failure).
- Fetch-mid-stream-error cleanup of partial file.
- Singleflight dedup under concurrent `GetOrSpool` calls.

`pkg/importer/admission_test.go`:

- `checkSingleImageFitsInBudget` rejects oversized image with `errs.CategoryUser` and the documented hint string.
- Per-image RSS estimate matches the exporter table for identical `windowLog`.

`pkg/diff/sidecar_test.go` (extended):

- I4: a fixture sidecar with one unknown optional field passes `ParseSidecar` and emits `slog.Debug` with the unknown field name (verified via slog test handler).
- Schema-error path (malformed JSON) still returns `errs.CategoryContent`.

`internal/workdir/workdir_test.go` (lifted):

- Precedence: explicit flag > env > hint-derived > tempdir.
- Cleanup deletes the spool root on close.
- Cleanup is idempotent (double-close is no-op).

### 8.3 Integration (`cmd/`)

- `cmd/apply_streaming_integration_test.go` — apply same bundle under `--workers ∈ {1, 2, 4, 8}`; assert all destinations produce identical image manifest digests (byte-identity gate).
- `cmd/unbundle_streaming_integration_test.go`:
  - 4-image bundle, `--workers=4` partial mode: inject one-image fault (e.g., bad baseline ref); assert other 3 succeed; assert ApplyReport has 3 OK + 1 failed.
  - Same bundle, `--workers=4`, `--strict`: assert fail-fast on the failing image; assert at most one other image started.
- `cmd/apply_admission_integration_test.go`:
  - `--memory-budget=10MiB` with a single-layer image at `windowLog=27` (estimate ~256 MiB) → fail-fast `errs.CategoryUser` error before any work begins; hint text matches `"increase --memory-budget"` substring.
  - `--memory-budget=0` disables admission entirely; no fail-fast; budget violations cannot occur (test passes by not triggering the admission path).

### 8.4 G7 acceptance

- I5: `pkg/importer/decode_windowlog_test.go` — synthesize windowLog=28 frame (others as table cases); assert `errs.CategoryContent` and message substring.
- I6: `pkg/importer/compose_phase3_test.go` — load `testdata/fixtures/phase3-bundle-min.tar`, apply, assert manifest digest equals committed constant.

### 8.5 Big test (build tag `big`)

`pkg/importer/scale_bench_test.go`:

- `scripts/build_fixtures -scale=2GiB` produces a 2 GiB-layer bundle (same fixture exporter side uses).
- Test executes `Import` against an `oci-archive:` destination.
- Asserts peak RSS ≤ 8 GiB via `runtime.MemStats.HeapAlloc` watermark plus an external `/usr/bin/time -v` check in the GHA workflow.
- Skipped by default; gated by `DIFFAH_BIG_TEST=1` or `-tags big`.

`.github/workflows/scale-bench.yml`:

- Adds an `apply` step after the existing `export` step, sharing the workspace and the `2GiB` fixture.
- Asserts `apply` peak RSS ≤ 8 GiB via the same `/usr/bin/time -v` gate.

### 8.6 Exporter regression gate

- `TestExport_OutputIsByteIdenticalAcrossWorkerCounts` (`pkg/exporter/determinism_test.go`) MUST stay green throughout PR2 (admission abstraction lift). Treated as an existing-functionality safety net, not a new test.

## 9. Backward compatibility

- All new flags default to current behavior (workdir colocated with output; budget large enough not to be triggered for typical bundles; workers=8 means parallelism is enabled by default for bundles, but single-image apply is unaffected).
- Sidecar schema is unchanged.
- `pkg/importer.Options` gains `Workdir`, `MemoryBudget`, `Workers` fields; library callers without these set get historical behavior (workers=8 default applied at flag-parse, not in `Import`).
- `bundleImageSource.HasThreadSafeGetBlob` flips from "delegate to baseline" to "true". `copy.Image` may run more concurrent `GetBlob` calls than before. The disk-backed reader is reentrant; this is the intended behavior. (Phase-3 archives apply the same as before — output is byte-identical.)
- `pkg/exporter` external API is unchanged. Internal `workerPool` / `encodePool` types keep their public method shape; only their bodies forward to `internal/admission`.

## 10. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Admission abstraction lift introduces export regression | Medium | High | PR2 (the lift) keeps `TestExport_OutputIsByteIdenticalAcrossWorkerCounts` and exporter unit tests green. The lift only moves code, no semantic change. Review checklist requires an `advisor()` pre-flight before merge. |
| `HasThreadSafeGetBlob = true` triggers more aggressive `copy.Image` internal concurrency, exposing latent races in disk-backed reader | Medium | Medium | PR4 lands the disk-backed reader; PR4's tests assert concurrent `Open(d)` for the same digest produce byte-identical readers. PR4 keeps `HasThreadSafeGetBlob` as `false` (existing delegate); a follow-up PR5 flips it to `true` after the concurrent-reader tests pass. |
| Apply RSS estimate (using exporter table) is too conservative; throughput on smaller layers is bottlenecked | Low | Low (throughput only) | Documented as known-conservative in `docs/performance.md`. Tuning is opt-in future work. |
| Phase-3 fixture (I6) exceeds size budget, bloating repo / PR diff | Low | Medium | Generation script enforces ≤ 2 layers ≤ 64 KiB each; size budget hard-asserted in fixture-build script. Stored as plain `.tar`, not `.tar.zst`. |
| Sidecar `DisallowUnknownFields` probe pass adds parse overhead | Low | Low | Sidecar size is bounded (KB scale); the probe pass is a single second decode of bytes already in RAM. Benchmark only if user-visible. |
| Importer's per-image worker pool starves on bundles with one large + N small images | Low | Low | `applyImagesPool` doesn't size-balance; large image runs with one worker slot, small images fill the others. Acceptable for v1; revisit if observed. |

## 11. Phased landing (PR plan summary)

Detailed plan goes in `docs/superpowers/plans/2026-05-04-import-streaming-io.md` (next step). High-level slicing:

| PR | Branch | Scope | Lift gates |
|---|---|---|---|
| 1 | `feat/import-streaming-pr1-flags` | `apply` / `unbundle` flags + config keys + defaults table | none |
| 2 | `feat/import-streaming-pr2-admission-extract` | Lift `pkg/exporter/{admission,pool,workerpool}` common parts to `internal/admission/`; exporter forwards | exporter byte-identity gate stays green; mutation-test pool panic recover |
| 3 | `feat/import-streaming-pr3-baselinespool` | `pkg/importer/baselinespool.go` + `primeBaselineSpool`; delete `blobcache.go`; migrate callers | importer-side existing tests stay green; new spool tests added |
| 4 | `feat/import-streaming-pr4-decode-streaming` | `serveFull` / `servePatch` rewrite; `DecodeStream` adoption; importer per-blob progress wired; `nolint:staticcheck` removed | `HasThreadSafeGetBlob` stays `false` until PR5 |
| 5 | `feat/import-streaming-pr5-apply-pool` | `importEachImage` → `applyImagesPool`; admission integration; `checkSingleImageFitsInBudget` fail-fast; `HasThreadSafeGetBlob = true` flip | byte-identity gate (apply-side) added |
| 6 | `feat/import-streaming-pr6-acceptance` | I4 sidecar `DisallowUnknownFields` probe + slog.Debug; I5 windowLog≥28 fail-closed; I6 Phase-3 fixture pin; `pkg/importer/scale_bench_test.go`; `scale-bench.yml` apply step | none new |
| 7 | `docs/import-streaming-lessons` | `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md` accumulating per-PR amendments (not folded back into the plan body — runs the operations-debt #1 recommendation as the lessons-doc shape) | none |

Each PR runs the proven Phase-4 discipline: `advisor()` pre-flight → sonnet implementer → sonnet spec-compliance review → opus code-quality review → fixer pass as needed.

## 12. Open questions

1. **`progress.Layer` reuse on importer side — does the existing exporter `cappedWriter` pattern fit, or do we need an importer-specific shape?** `cappedWriter` is exporter-internal. Likely just lift it to `pkg/progress/` as a shared helper. Decided in PR4 implementation.
2. **`zstdpatch.Decode([]byte, []byte)` lifecycle.** After PR4, importer is the last in-tree caller; the deprecated function is then truly dead. Delete in PR4 or retain for one minor cycle as a compatibility shim for hypothetical out-of-tree consumers? Lean: delete (we don't owe out-of-tree compatibility for an unreleased internal helper).

## 13. Acceptance

The spec is satisfied when **all of the following** are demonstrable on master:

1. `go test -tags big -run TestImport_ScaleApply ./pkg/importer/...` (or equivalent) completes within 8 GiB RSS on a 2 GiB-layer fixture.
2. `go test -count=2 -run TestApply_ByteIdenticalAcrossWorkerCounts ./cmd/...` (apply-side byte-identity gate) passes deterministically.
3. `pkg/importer/blobcache.go` no longer exists; `git grep "baselineBlobCache"` returns 0 hits.
4. `pkg/importer/compose.go` no longer carries the `nolint:staticcheck` directive at line 139.
5. `internal/admission/` exists with at least one importer-side and one exporter-side caller; both call sites use the same `Pool.Submit(key, estimate, fn)` signature.
6. `apply --workdir` / `apply --memory-budget` / `apply --workers` plus `unbundle` equivalents are documented in `--help`; `cmd/config_defaults_test.go` table covers them.
7. I4: `pkg/diff/sidecar.go::ParseSidecar` honors `DisallowUnknownFields` probe and emits `slog.Debug` with unknown field names — covered by `pkg/diff/sidecar_test.go`.
8. I5: `pkg/importer/decode_windowlog_test.go` synthesizes windowLog ∈ {28,29,30,31} frames and asserts each rejected with `errs.CategoryContent`.
9. I6: `testdata/fixtures/phase3-bundle-min.tar` is committed and `pkg/importer/compose_phase3_test.go` passes against it.
10. `.github/workflows/scale-bench.yml` includes an apply phase asserting RSS ≤ 8 GiB via `/usr/bin/time -v`; nightly run on master green for at least one full nightly cycle before this spec is closed.
11. Pre-PR-7 amendments doc exists at `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md`; future implementers find amendments in the dedicated doc, not in the plan body.
12. **Bandwidth parity (Goal 3).** A `cmd/apply_registry_integration_test.go` (or sibling) test asserts that for an N-image bundle sharing K distinct baseline digests across patch entries, the registry sees exactly K `GetBlob` calls (not K × patch-count). Tightens the existing loose `≤6` slack assertions to "exactly K" (closes a gap that was already on the queue from `phase234_review_findings_2026-04-25.md`).
13. **Cleanup contract (Goal 5).** Integration tests cover three of the §7 cleanup paths and assert `os.Stat(workdir) == NotExist`: (a) successful `Import()`, (b) ctx-cancel mid-apply, (c) injected worker-goroutine panic. The remaining paths (partial mode, all-fail, disk-full simulation) are covered by unit tests on the relevant components.

When all 13 hold, set this spec's `Status:` field to `Done`.
