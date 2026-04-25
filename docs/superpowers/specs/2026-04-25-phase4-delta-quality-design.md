# Phase 4 — Delta Quality & Throughput — Design

- **Status:** Draft
- **Date:** 2026-04-25
- **Author:** @leosocy
- **Roadmap:** Reframes Phase 4 of
  `docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md`
  from "scale robustness" (memory-bounded export on commodity hardware)
  to "delta quality & throughput" (smallest possible deltas + parallel
  encode on the abundant-resource build farms where `diffah diff`
  actually runs).
- **Builds on:**
  - Phase 1 intra-layer (PR #5) — reuses zstd `--patch-from` and the
    sidecar `Encoding=patch` schema.
  - Phase 2 I.a content-similarity (PR #6) — `Planner.pickSimilar` is
    extended to return top-K candidates instead of one.
  - Phase 2 registry import (PR #14) and Phase 3 registry export
    (PR #15) — reuses `SystemContext`, the registrytest harness, and
    the per-blob streaming `GetBlob` path. No additional registry
    surface is added.
- **Parallel track:** Phase 4.5 (apply correctness + fault tolerance)
  is a separate spec, brainstormed after this one merges. It can ship
  independently — the two tracks share no files.

## 1. Motivation

After Phase 3, every diffah verb works against any
`go.podman.io/image/v5` transport, archives can be signed and verified,
and the public surface is feature-complete for the v2 happy path. What
remains is producer-side **quality** and **throughput**:

- The encoder uses `zstd -3 --long=27`. Level 3 leaves 10–25% on the
  table on every patch; the 128 MiB window is too small to find
  cross-region matches in multi-GiB layers.
- `Planner.pickSimilar` commits to the single highest-score baseline
  candidate. Score-based ranking is good but not optimal: the
  best-actual patch (smallest emitted bytes) often comes from
  candidate #2 or #3 in the score order. Today's encoder never tries
  them.
- `encodeShipped` is fully sequential — pair-by-pair, blob-by-blob. On
  a build farm with 64 cores idling and 5 service images to diff, this
  is leaving wall-clock on the table.
- Each baseline blob is fetched at least twice today (once for
  fingerprint priming in `ensureBaselineFP`, then again as a patch
  reference inside `PlanShipped` when that baseline is selected). On a
  registry-to-registry diff this doubles the egress bill.

Phase 4 addresses these four points without touching the apply path
and without changing the sidecar schema. The KPI for the merge gate is
**delta size first, wall-clock second, peak resources third** — the
inverse of the production-readiness-roadmap's original Phase 4 framing,
which was written before we had concrete operator profiles.

## 2. Goals (exit criteria)

A Phase 4 PR train is mergeable when **all seven** hold:

1. **Smaller deltas on a fixed corpus.** On the synthesized GB-scale
   benchmark fixture (§7.4), the new exporter produces a delta archive
   no larger than `0.85 × pre-Phase-4 delta size`. The 15 % floor is
   set to prove that the four mechanisms (level, window, top-K,
   fingerprint-cached lazy fetch) are pulling their weight; benchmarks
   on real fixtures should beat this.
2. **Parallel encode is correct.** With `--workers=N` for any
   `1 ≤ N ≤ 64`, the produced delta archive is **byte-identical** to
   the `--workers=1` archive for the same `(--candidates, --zstd-level,
   --zstd-window-log)` tuple. zstd output is deterministic;
   `pickTopK` is deterministic; the archive writer drains the pool in
   sorted-digest order after every worker has returned.
3. **Lazy baseline fetch.** Across an entire `Export()` call —
   regardless of pair count or top-K K — every distinct baseline blob
   is fetched at most once via `GetBlob`. An integration test on the
   registrytest harness asserts the per-blob fetch count.
4. **Registry-to-registry never materializes the whole image.**
   `diffah diff docker://A docker://B out.tar` writes nothing under
   any path other than `out.tar`. No temporary `oci:` layout, no
   `dir:` stage, no archive other than the requested output. The same
   guarantee holds for `bundle`. (Spool of in-progress single layer
   bytes through `${TMPDIR}` for the zstd CLI is allowed, since it is
   already part of Phase 1's `zstdpatch.Encode` and not a new
   regression.)
5. **CLI surface is documented and example-driven.** Every new flag
   (`--workers`, `--candidates`, `--zstd-level`, `--zstd-window-log`)
   has a paragraph of human-readable description plus at least one
   example in `--help` output and in `cmd/help.go` long help.
6. **Wall-clock regression is bounded.** On the same fixture, new
   wall-clock ≤ `1.5 × pre-Phase-4` with **default flags**. The bound
   accepts that `-L 22` is intrinsically slow; `--workers=8` is
   defaulted to absorb most of the cost.
7. **Backward compat is asymmetric but explicit.**
   - **Phase 3 archives → Phase 4 importer:** byte-identical decode.
   - **Phase 4 archives → Phase 4 importer:** correct decode for any
     `(level ∈ 1..22, windowLog ∈ 10..31)`.
   - **Phase 4 archives → Phase 3 importer:** decodes only when the
     producer used `--zstd-window-log ≤ 27`. Frames with larger windows
     are rejected by older importers with a `Frame requires too much
     memory for decoding` error from the zstd CLI / klauspost decoder
     (the existing `--long=27` cap in `internal/zstdpatch/cli.go:87`
     and `WithDecoderMaxWindow(1<<27)` in
     `internal/zstdpatch/fullgo.go:44`). This is a fail-closed
     behavior — never silent corruption — and is documented in the
     CHANGELOG migration notes.
   - **Sidecar schema:** unchanged. `BlobEntry` does not record level
     or window-log; the zstd frame self-describes both, and the
     Phase 4 importer accepts the full range.

### Non-goals (explicit anti-goals)

- **Streaming `blobPool` / `io.Reader`-based fingerprinter.** The
  original Phase 4 spec required `peak_RSS = O(workers × chunk)`. With
  the user's 64C / 256 GiB build-farm profile, in-memory `[]byte`
  per layer is fine. Streaming refactor is rescheduled to Phase 6 or
  later, when a customer with constrained hardware actually asks.
- **Multi-ref concatenation.** Concatenating top-K baseline refs into
  one mega-ref for zstd `--patch-from` could yield additional
  size wins, but adds a sidecar codec value (`zstd-patch-multiref`),
  decoder support, and a backward-compat path. Phase 4 ships top-K
  only and **measures**: if `--candidates=3` wins are concentrated in
  the first candidate, multi-ref is dead weight; if they spread,
  multi-ref earns its own follow-on phase.
- **Resume / checkpoint.** Interrupted exports must restart from
  scratch.
- **Persistent cross-run cache (`~/.cache/diffah/`).** Phase 2 spec
  already deferred this; Phase 4 keeps the deferral. Spool lives in
  `${TMPDIR}` and is removed on `Export()` return.
- **`--registry-workers` separate from `--workers`.** If high
  parallelism turns out to trigger registry rate-limiting in the
  field, we will add a second flag in a later patch. Not in v1.
- **Apply path correctness improvements.** Tracked in Phase 4.5.
- **GPU / SIMD / hardware-accelerated zstd backends.** The current
  shell-out to `zstd ≥ 1.5` is enough.

## 3. Command surface

Every flag below is added to `diff` and `bundle`. `apply` and
`unbundle` are unchanged.

### 3.1 `--workers N`

**Description (`--help` text):**

> Number of layers to fingerprint and encode in parallel. Higher
> values reduce wall-clock time on multi-pair / multi-layer runs but
> increase concurrent registry connections. Default: `8`.
> `--workers=1` reproduces the strict serial encode used in Phase 3
> and earlier.

**Examples:**

```
# Fast multi-image bundle on a beefy build host:
diffah bundle --workers=32 spec.json out.tar

# Conservative single-threaded run for a flaky / rate-limited registry:
diffah diff --workers=1 docker://reg/app:v1 docker://reg/app:v2 out.tar
```

### 3.2 `--candidates K`

**Description:**

> For each shipped target layer, encode patches against the top-K
> most content-similar baseline layers and emit whichever yields the
> smallest patch. Higher K shrinks the delta at the cost of CPU; K=1
> reproduces the Phase 3 single-best-candidate behavior. Default: `3`.

**Examples:**

```
# Fastest run, original Phase 3 behavior:
diffah diff --candidates=1 baseline:v1 target:v2 out.tar

# Aggressive small-delta sweep — recommended only for release builds:
diffah diff --candidates=5 baseline:v1 target:v2 out.tar
```

### 3.3 `--zstd-level N`

**Description:**

> zstd compression level for both intra-layer patches and the
> full-layer fallback. Range 1–22. Higher levels produce smaller
> output at the cost of CPU. Default: `22` ("ultra"). The default is
> tuned for build-farm execution; `--zstd-level=12` is a reasonable
> speed/size compromise; `--zstd-level=3` matches the zstd CLI
> default and is the fastest.

**Examples:**

```
# CI run that must finish under 10 minutes:
diffah diff --zstd-level=12 baseline:v1 target:v2 out.tar

# Match historical Phase 3 output bytes for regression testing:
diffah diff --zstd-level=3 --candidates=1 baseline:v1 target:v2 out.tar
```

### 3.4 `--zstd-window-log N` (or `auto`)

**Description:**

> zstd long-mode window size, expressed as `log2` of bytes. The
> window bounds how far back zstd searches for matching byte
> sequences within ref+target — larger windows find more repetition
> in multi-GiB layers, but consume proportional encoder memory.
> Accepts `auto` or an integer 10–31. `auto` picks per-layer:
> `≤128 MiB → 27` (128 MiB), `≤1 GiB → 30` (1 GiB), `>1 GiB → 31`
> (2 GiB). Encoder memory ≈ `2 × 2^N` bytes per running encode.
> Default: `auto`.

**Examples:**

```
# Force max window for layers known to scatter deltas across wide
# byte ranges (encoder uses ~4 GiB RAM per worker):
diffah diff --zstd-window-log=31 baseline:v1 target:v2 out.tar

# Match Phase 3 behavior (legacy --long=27 fixed window):
diffah diff --zstd-window-log=27 baseline:v1 target:v2 out.tar
```

### 3.5 Determinism guarantee (printed in `cmd/help.go` long help)

> For a fixed `(baseline, target, --candidates, --zstd-level,
> --zstd-window-log)` tuple, the produced delta archive is
> byte-identical regardless of `--workers` value. Worker count
> affects only wall-clock time, not output bytes.

## 4. Code-level shape

### 4.1 New file: `pkg/exporter/workerpool.go`

A bounded worker pool tailored to Phase 4's use:

```go
type workerPool struct {
    sem chan struct{}                      // size = N workers
    eg  *errgroup.Group                    // ctx-cancellation aware
}

func newWorkerPool(ctx context.Context, n int) (*workerPool, context.Context)
func (p *workerPool) Submit(fn func() error)
func (p *workerPool) Wait() error          // blocks; returns first error
```

Used in two places: fingerprinting baseline layers (§4.4) and encoding
shipped target layers (§4.5). Both phases reuse the same pool but
synchronously between phases — fingerprinting must finish for all
pairs before encoding begins, because top-K selection depends on a
fully-populated fingerprint cache.

### 4.2 New file: `pkg/exporter/fpcache.go`

```go
// fpCache memoizes baseline layer fingerprints across pairs in a
// single Export() call. A cache miss fingerprints the layer and
// stores the result; concurrent misses on the same digest collapse to
// one fetch + one fingerprint via singleflight.Group.
type fpCache struct {
    mu      sync.RWMutex
    fps     map[digest.Digest]Fingerprint   // nil entry = "fingerprint failed"
    bytes   map[digest.Digest][]byte        // raw layer bytes, kept for reuse as patch ref
    sf      singleflight.Group
}

func (c *fpCache) GetOrLoad(
    ctx context.Context,
    meta BaselineLayerMeta,
    fetch func(digest.Digest) ([]byte, error),
    fp Fingerprinter,
) (Fingerprint, []byte, error)
```

The `bytes` field is the lazy-fetch enforcement: when a top-K
candidate is selected, the encoder reuses the cached bytes as the
zstd-patch reference, no second `GetBlob` call. A cache entry is held
for the lifetime of the `Export()` call and dropped on return.

Cache key is the layer digest alone — collisions across baseline
images are content-equivalent by construction (same digest → same
bytes), so a hit on a layer fingerprinted for pair A is correct to
reuse for pair B.

### 4.3 `pkg/exporter/intralayer.go` changes

`Planner.pickSimilar` returns one candidate today. Phase 4 adds:

```go
// PickTopK returns up to K candidates ordered by content-similarity
// score (descending), with size-closest as the tie-break. Falls back
// to size-closest top-K when targetFP is nil or no baseline has a
// fingerprint.
func (p *Planner) PickTopK(targetFP Fingerprint, targetSize int64, k int) []BaselineLayerMeta
```

`PlanShipped` is split:

```go
// PlanShippedTopK encodes the target K times — once per top-K
// candidate plus one "full" encode — and returns the smallest result.
// Each ref is loaded via fpCache.GetOrLoad so the same baseline blob
// is never fetched twice within a single PlanShipped call or across
// multiple calls in the same Export().
func (p *Planner) PlanShippedTopK(
    ctx context.Context,
    s diff.BlobRef,
    target []byte,
    k int,
    level int,
    windowLog int,
) (diff.BlobRef, []byte, error)
```

The K trials run **serially within a single worker** (not parallelized
across workers). The reasoning: cross-worker parallelism is already
provided by the worker pool over distinct shipped layers; doing K
encodes for one layer in parallel inside one worker would just
rebudget the same CPU across thinner slices and complicate
cancellation.

### 4.4 `pkg/exporter/encode.go` changes

`encodeShipped` is rewritten in two phases:

**Phase E1 — Fingerprint priming (worker-pool over the union of
baseline layers):**

```
for every distinct baseline layer across all pairs:
  pool.Submit { fpcache.GetOrLoad(...) }
pool.Wait()
```

**Phase E2 — Encode (worker-pool over shipped target layers):**

```
for every (pair, shippedBlob):
  pool.Submit {
    target := streamBlobBytes(...)
    targetFP := fingerprint(target)
    candidates := planner.PickTopK(targetFP, target.size, k)
    entry, payload := planner.PlanShippedTopK(target, candidates, level, windowLog)
    pool.AddIfAbsent(entry, payload)        // mutex-protected
  }
pool.Wait()
```

Pool ordering across calls is preserved by the existing
`pool.sortedDigests()` drain in `writeBundleArchive`.

### 4.5 `internal/zstdpatch` changes (encode + decode)

**Encode side** — `Encode` and `EncodeFull` gain optional parameters:

```go
type EncodeOpts struct {
    Level     int   // 1-22; 0 = use historical default 3
    WindowLog int   // 10-31; 0 = use historical default 27
}

// Encode is a thin wrapper over the existing CLI shell-out;
// EncodeOpts is wired via the -L and --long flags.
func Encode(ctx context.Context, ref, target []byte, opts EncodeOpts) ([]byte, error)
func EncodeFull(target []byte, opts EncodeOpts) ([]byte, error)
```

**Decode side** — the hardcoded window caps must be lifted to match
the producer's max:

- `cli.go:87` `--long=27` → `--long=31` (zstd CLI rejects frames whose
  declared window exceeds this cap; raising to 31 means Phase 4
  importer accepts any frame Phase 4 producer can emit).
- `fullgo.go:44` `zstd.WithDecoderMaxWindow(1<<27)` →
  `zstd.WithDecoderMaxWindow(1<<31)` (klauspost decoder cap).

Both caps are *upper bounds on allowed memory*, not required allocation:
decoding a Phase 3 frame (window=27) with `--long=31` allocates only
the actual frame's window worth of memory. Lifting the cap does not
inflate decode peak memory for legacy archives.

Decode signatures themselves do not change — `Decode(ctx, ref, patch)`
and `DecodeFull(data)` remain byte-identical to today, just with the
larger window cap baked in.

### 4.6 `pkg/exporter` Options additions

```go
type Options struct {
    // ... existing fields ...

    Workers       int // default 8 if 0
    Candidates    int // default 3 if 0
    ZstdLevel     int // default 22 if 0; 1-22 valid
    ZstdWindowLog int // default 0 = auto; otherwise 10-31
}
```

`auto` window-log resolution lives in the encoder (per-layer), not in
Options — the same Options can drive different window sizes for
different-sized layers within one run.

### 4.7 `cmd` changes

`cmd/diff.go` and `cmd/bundle.go` install the four flags via a new
helper `installEncodingFlags(cmd *cobra.Command, opts *encodingOpts)`
in `cmd/encoding_flags.go` (mirrors the pattern of
`installRegistryFlags` and `installSigningFlags`). Validation is at
the cobra level: K ≥ 1, level 1–22, window-log either `auto` or 10–31.

`cmd/help.go` long-help gains a new "Encoding tuning" section that
explains the four flags and prints the determinism guarantee of §3.5.

### 4.8 What stays untouched

- `pkg/exporter/pool.go` — in-memory blob pool unchanged. Phase 6+
  may revisit.
- `pkg/exporter/writer.go` — single-writer archive emission unchanged
  (already deterministic via sorted digests).
- `pkg/importer/*` — Go-level apply path unchanged (no signature or
  flow changes). The decode-side window cap raise (§4.5) is a 1-line
  change inside `internal/zstdpatch` and does not affect the importer
  package itself.
- `pkg/diff/sidecar.go` — sidecar schema and `BlobEntry` unchanged.
- `pkg/signer/*` — signing/verifying unchanged.

## 5. Concurrency, errors, cancellation

- **Worker pool error semantics:** first error returned by any worker
  is propagated to `errgroup.Wait()`; other workers see ctx
  cancellation on their next checkpoint and return promptly. Partial
  pool state is discarded — `writeBundleArchive` is never called when
  encode fails. The output `.tar` does not exist on disk.
- **fpCache concurrent miss:** `singleflight.Group` collapses
  simultaneous misses on the same digest to one underlying fetch. A
  fetch error is returned to all waiters; the cache entry is **not**
  poisoned (next caller retries). This matches the spirit of today's
  `ensureBaselineFP` which logs and continues on per-baseline failures.
- **Per-blob fetch counter:** an instrumented `imageio.SourceFactory`
  in registrytest tracks `GetBlob(digest)` invocations. Goal #3 is
  asserted by snapshot of the counter map.
- **Pool concurrency:** today's `blobPool` is single-threaded by
  construction (called only from `encodeShipped`). Phase 4 adds an
  RWMutex to `addIfAbsent`/`has`/`get`/`countShipped`/`refCount`.
  `sortedDigests()` is read-only after encode returns, no lock needed
  there.

## 6. Backward compatibility

### 6.1 What breaks

Nothing user-visible. CLI is additive. Sidecar unchanged.

### 6.2 What stays byte-identical

- `--workers=1 --candidates=1 --zstd-level=3 --zstd-window-log=27`
  reproduces Phase 3 byte-identical output for the same baseline +
  target. Verified by the regression-fixture test in §7.6.
- Phase 3 archives apply byte-identically through the Phase 4 importer
  (decode-side window cap raise is upward-compatible — the larger cap
  trivially admits frames declaring smaller windows).
- Phase 4 archives produced with `--zstd-window-log ≤ 27` apply
  byte-identically through the Phase 3 importer.

### 6.3 What does NOT round-trip across importer versions

- Phase 4 archives produced with `--zstd-window-log ≥ 28` (i.e., the
  default `auto` mode whenever any layer is >128 MiB, plus any
  explicit override above 27) **cannot** be decoded by Phase 3 or
  earlier importers. The failure mode is fail-closed: zstd
  `Frame requires too much memory for decoding`. This is documented
  in CHANGELOG migration notes; operators who must serve Phase 3
  consumers can set `--zstd-window-log=27` to opt back into the legacy
  cap and pay the resulting size cost.

### 6.4 Forward compatibility

Reserved future codecs (`zstd-patch-multiref`, etc.) are not used in
Phase 4 outputs. A future importer that does not understand a
multi-ref codec sees `Codec=zstd-patch-multiref` and exits 4
(content) — same path as any other unknown codec. No silent
mis-decoding.

## 7. Testing strategy

### 7.1 Unit — `pkg/exporter/fpcache.go`

- Hit/miss correctness for distinct digests
- Concurrent miss collapsing (singleflight): two goroutines miss the
  same digest, only one underlying `fetch` call is observed
- Fetch error does not poison the cache: post-error retry succeeds

### 7.2 Unit — `pkg/exporter/intralayer.go`

- `PickTopK(K=1)` reproduces today's `pickSimilar` output exactly
- `PickTopK(K=N)` returns N candidates sorted by score-desc, size-tie
- `PlanShippedTopK` picks the smallest of K patches plus full;
  ties break by candidate order in PickTopK output (deterministic)
- `--zstd-level=22 --zstd-window-log=auto` produces a smaller patch
  than `--zstd-level=3 --zstd-window-log=27` on a fixture with
  cross-region similarity (regression-style assertion: new ≤ old × 0.85)

### 7.3 Unit — `internal/zstdpatch/cli.go`

- `EncodeOpts{Level: 22, WindowLog: 30}` produces a frame with
  `-L 22 --long=30` arguments (assert via mocked CLI)
- `EncodeOpts{Level: 0, WindowLog: 0}` defaults to `-L 3 --long=27`
- Decode of any combination round-trips byte-identically

### 7.4 Integration — `cmd/diff_quality_integration_test.go` (new)

Synthesized fixture: deterministic-RNG-seeded 256 MiB layer pair with
a known content-similarity ratio (~70 % shared bytes). Asserts:

- `diffah diff --workers=8` produces a delta archive ≤ `0.85 ×`
  the size produced by Phase 3 defaults on the same fixture.
- Wall-clock ≤ `1.5 ×` the Phase 3 baseline.

### 7.5 Integration — registry streaming

- Run `diffah diff docker://… docker://… out.tar` against the
  registrytest harness with multi-pair input. Capture all
  `GetBlob` invocations; assert that each unique digest appears
  exactly once across the entire run.
- Assert no file under `${TMPDIR}` other than zstd CLI scratch
  directories survives `Export()` return.

### 7.6 Integration — determinism across worker counts

- For each of `--workers ∈ {1, 2, 4, 8, 32}`, with all other flags
  fixed, the produced `out.tar` is byte-identical (sha256 match).

### 7.7 Bench — gated by `DIFFAH_BIG_TEST=1`

- 2 GiB synthesized layer × 1 baseline × 1 target.
- KPIs: delta size, wall-clock. Output JSON to `benchmarks/`.
- CI gate: regression of size > +5 % or time > +50 % vs the previous
  commit's recorded value fails the job.

### 7.8 What we explicitly *don't* test in Phase 4

- Behavior on actual public registries (Docker Hub, GHCR). The
  registrytest harness covers the wire protocol; live-registry tests
  are flaky and a Phase 5 addition.
- Memory-bounded operation under hostile conditions (deferred to the
  future streaming refactor).
- Cross-platform zstd CLI version skew: the project already requires
  zstd ≥ 1.5; Phase 4 adds no version requirement.

## 8. Risks and mitigations

| ID | Risk | Mitigation |
|---|---|---|
| R1 | Default `--zstd-level=22` makes diff "feel slow" — operators expect Phase 3 speed | CHANGELOG explicitly calls out the default change and shows `--zstd-level=12` and `--zstd-level=3` overrides. Benchmark numbers published in `docs/performance.md`. |
| R2 | Top-K trial inside a worker amplifies per-blob CPU cost K-fold | Each baseline ref is loaded from `fpCache` (no extra fetches). CPU is the only amplified resource; bounded by `--candidates`. Operator can `--candidates=1` to opt out. |
| R3 | `--zstd-window-log=31` peak memory ≈ 4 GiB per running encode; with `--workers=8` that's 32 GiB | `auto` default caps window at 30 for layers ≤1 GiB; only layers >1 GiB get 31. Operator sets `--zstd-window-log=N` explicitly to override. |
| R4 | High `--workers` on a rate-limited registry triggers 429 / 503 | Default 8 is well below typical registry concurrency limits. Existing Phase 2 retry/backoff (`--retry-times`, `--retry-delay`) is reused — no new flag needed. If a real customer reports breakage, add `--registry-workers` in a follow-on. |
| R5 | `singleflight.Group` masking transient fetch errors as "all failed" when only the leader's attempt failed | The leader's error is returned to all waiters, but the cache entry is not stored — the next call retries from scratch. Tested in §7.1. |
| R6 | Top-K K values too high (K=10+) blow up CPU with marginal delta gains | Document `K=3` as the recommended ceiling in `--help`; no hard cap (operator with 256 GiB / 64 C is trusted). |
| R7 | Determinism breaks if a future zstd version changes its default heuristics for an explicit `(level, --long)` pair | Phase 4 pins behavior on the explicit flag tuple; we do not depend on zstd defaults for anything we write. CI runs zstd ≥ 1.5 from `apt`/`brew`; the project's `.tool-versions` already pins this. |
| R8 | Phase 4 archives with `--zstd-window-log ≥ 28` are not decodable by Phase 3 importers — operators serving older consumers see fail-closed errors | Documented in CHANGELOG migration notes (§6.3). Operators pin `--zstd-window-log=27` to opt back into legacy compatibility at the cost of size on multi-GiB layers. The `auto` default does NOT silently exceed 27 for layers under 128 MiB, so small/medium image fleets are unaffected. |

## 9. PR slicing plan

Phase 4 lands as four sequential PRs, each independently testable and
reviewable. Numbering continues from Phase 3 (PR #15 was the latest):

1. **PR-1: encoder parameter plumbing + decode cap raise.** Add
   `EncodeOpts` to `internal/zstdpatch`. Lift decode-side window cap
   from `1<<27` to `1<<31` in both `cli.go` (the `-d --long=27` argv)
   and `fullgo.go` (`WithDecoderMaxWindow`). Add `--zstd-level` and
   `--zstd-window-log` flags to `diff` / `bundle`. With default flags
   (`--zstd-level=3 --zstd-window-log=27`) producer behavior is
   unchanged; consumer is permissive for any future Phase 4 archive.
2. **PR-2: top-K candidates.** Add `PickTopK` and `PlanShippedTopK`
   to `Planner`. Add `--candidates` flag. Default `--candidates=3`
   ships a behavior change (smaller deltas). Per-blob fetch-count
   integration test (Goal #3) lands here, since this is the first PR
   to materially change baseline read patterns.
3. **PR-3: parallel encode + fpCache.** Add
   `pkg/exporter/workerpool.go` and `pkg/exporter/fpcache.go`. Add
   `--workers` flag with default `8`. Determinism-across-worker-count
   integration test (Goal #2) lands here.
4. **PR-4: aggressive defaults + bench + docs.** Bump
   `--zstd-level` default from `3` to `22` and `--zstd-window-log`
   default from `27` to `auto`. Add
   `cmd/diff_quality_integration_test.go` and the GB-scale
   `DIFFAH_BIG_TEST` bench. Update `CHANGELOG.md`, `README.md`,
   `docs/performance.md`, and `cmd/help.go` long-help. Wire the CI
   bench regression gate.

PR-1 is risk-free (additive flags, no observable change). PR-2 ships
the first delta-size win. PR-3 ships throughput. PR-4 ships the
"build-farm tuned" defaults *after* parallelism is in place to absorb
their CPU cost — bumping `-L 22` before parallelism would saddle
single-worker users with 5–8× wall-clock with no offsetting win.

## 10. Open questions deferred to follow-on

- Does multi-ref concatenation produce additional wins beyond top-K?
  Decided after PR-2 ships and we have measurements.
- Should `--workers` split into `--encode-workers` + `--registry-workers`?
  Decided after a real operator reports rate-limiting.
- Persistent cross-run blob cache (`~/.cache/diffah/`)? Phase 5+,
  needs its own spec (cache invalidation, GC, size cap).
- Streaming `blobPool` / `io.Reader`-based fingerprinter? Phase 6+,
  needs a real customer with constrained hardware to motivate it.
