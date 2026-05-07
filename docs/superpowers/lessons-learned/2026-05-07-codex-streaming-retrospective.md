# Codex Retrospective — Streaming I/O Stack (2026-05-07)

> Source: `/codex` consult mode, session `019e0103-5895-7141-9897-0fffb8eddcdc`
> Scope: post-merge architecture review of the 14-PR streaming I/O initiative (export PRs #38-49, import PRs #50-62)
> Lessons-learned doc: [`2026-05-04-import-streaming-lessons.md`](./2026-05-04-import-streaming-lessons.md) (A1-A14 already captured by team; Codex was asked to find what is NOT in there)

## Bottom line (Codex's words)

> This is not yet a coherent bounded-memory architecture. Export mostly moved the blob hot path to disk. Import moved patch reconstruction to disk, but left baseline-only reuse as `os.ReadFile`, estimates those layers as `0`, and shares one digest-keyed baseline spool across images with different baseline sources. That is the main production risk.

(Codex could not run `go test` — sandbox is read-only and Go's build workdir creation failed under `/var/folders/...`. All findings are static-analysis based.)

---

## P0 findings

### 1. Import `--memory-budget` is FALSE for baseline-only reuse

`pkg/importer/admission.go:32-60` explicitly skips layers absent from `sidecar.Blobs` as "no DecodeStream RSS cost." But `pkg/importer/compose.go:97-110` serves those baseline-only layers through `fetchVerifiedBaselineBlob`, and `pkg/importer/compose.go:298-321` does `os.ReadFile(path)`.

Worst case: 8 workers each hit a 4 GiB baseline-only layer; estimator admits them as near-zero; process tries to allocate ~32 GiB.

**Smallest fix:** make baseline-only reuse return a path-backed `io.ReadCloser` from the spool, `Stat` for size, no `ReadFile`.

### 2. Shared importer spool can hide per-image baseline corruption / missing blobs

One `BaselineSpool` is created per import at `pkg/importer/importer.go:140-144`, keyed only by digest at `pkg/importer/baselinespool.go:37-41`. The `--workers=1` pins in `cmd/unbundle_preflight_integration_test.go:59-68` and `:122-127` are effectively a bug report: a complete baseline for `svc-a` can prime a digest and mask missing B2 for `svc-b`.

**Smallest fix:** per-image baseline completeness preflight before apply; then remove the `--workers=1` pins. (A10 already promised this follow-up but didn't rank it as a correctness gap.)

### 3. Patch/full verification only happens on EOF; early close skips integrity check

`verifyingReadCloser.Read` verifies only when `Read` returns `io.EOF` at `pkg/importer/compose.go:248-259`. `Close` only closes/removes scratch at `:279-284`. If a consumer closes early (especially patch output from `servePatch`), mismatch is never surfaced.

**Smallest fix:** track EOF/verified state; on `Close`, if not verified, drain-and-verify or return a typed short-read / incomplete-consumption error. Add a test with a corrupt patch reader closed before EOF.

### 4. Sidecar digest path traversal is not fully closed

`internal/archive/reader.go:86-94` has `safeJoin` (good). But later paths are built from sidecar/manifest digests at `pkg/importer/compose.go:124-126`, `:163`, `pkg/importer/importer.go:741-743`, `pkg/importer/manifest.go:67-69`. `pkg/diff/sidecar.go:95-133` validates encoding semantics but never calls `digest.Validate()` on blob keys, manifest digests, or `patch_from_digest`. A malicious digest algorithm like `..` can influence path joins.

**Smallest fix:** validate every digest in `Sidecar.validate`: blob map keys, image target/baseline manifest digests, and patch refs. Reject any digest whose algorithm/encoded form is not canonical.

---

## Architecture drift (export ↔ import)

| Drift | Export | Import | Cost to re-converge |
|---|---|---|---|
| Workdir lifecycle | One workdir + subdirs created up front (`pkg/exporter/exporter.go:196-201`, `pkg/exporter/workdir.go:38-58`); cleanup logs failures | Workdir created at `pkg/importer/importer.go:91-96`, but extraction goes to a SEPARATE `os.MkdirTemp("", "diffah-import-")` in `pkg/importer/extract.go:23-47`, outside `--workdir` | Low |
| BaselineSpool surface | Returns `{Path, Fingerprint}`; fail-soft on fingerprint errors (`pkg/exporter/baselinespool.go:27-36`, `:145-158`) | Returns only `path`; verifies digest; hard-fail on mismatch (`pkg/importer/baselinespool.go:69-80`, `:161-164`) | Low if you document the divergence; medium for a shared interface |
| Atomic publication | Direct `os.Create` to final path (`pkg/exporter/baselinespool.go:127`); singleflight makes it mostly safe | Temp + rename (`pkg/importer/baselinespool.go:127-170`) | Low — make export use temp+rename |
| Admission scope | Estimates over `applyList` | Estimates over ALL sidecar images at `pkg/importer/importer.go:248-250` — in partial mode, a skipped huge image can reject a small valid image | Trivial |
| Error wrapping | `fmt.Errorf` with context | Mutates the matched mismatch error in place at `pkg/importer/compose.go:176-180` and `:315-319` | Low — construct a new `ErrBaselineBlobDigestMismatch` instead of mutating |
| Cleanup logging | Logs cleanup failures (`pkg/exporter/workdir.go:46-50`) | Drops `RemoveAll` errors (`internal/workdir/workdir.go:44-51`); drops extraction cleanup errors (`pkg/importer/extract.go:46-48`) | Low |

---

## Concurrency / silent failure (NOT in lessons doc)

- `internal/admission/workerpool.go:60-68` — post-cancel branch is correct for context cancellation but starts one tiny goroutine per dropped submit. With thousands of queued items this is avoidable noise. Fix: `Submit` returning bool/error, or a `sync.Once`-guarded `RecordCancelOnce`.
- `internal/admission/admission.go:65-83` — starts a goroutine per submitted item BEFORE acquiring worker/memory gates. Memory is bounded for work, not for queued goroutines. Fix: blocking submission on worker gate or bounded queue.
- `internal/archive/reader.go:129-138` — ignores `io.EOF` from `io.CopyN`. A truncated tar entry can be accepted as a shorter file. Fix: treat any short copy as corruption.

---

## What `--memory-budget` does NOT count

| Bucket | File:line |
|---|---|
| Baseline-only `os.ReadFile` | `pkg/importer/compose.go:298-321` |
| Manifest / config `os.ReadFile` | scattered |
| Goroutine stacks for all submitted tasks | `internal/admission/admission.go:65-83` |
| Zstd subprocess aggregate RSS (patch path shells out to `zstd --long=31`) | `internal/zstdpatch/stream.go` |
| OS page cache | n/a — outside Go's view |
| Extracted bundle files | `pkg/importer/extract.go` |
| Scratch output file cache | various |

`MaxParallelDownloads: 1` enforcement is hand-rolled inside `composeImage` at `pkg/importer/compose.go:419-432`. Any future `copy.Image` caller with `bundleImageSource` can bypass it; not a type-level invariant.

---

## CLI / config — documented precedence is wrong

`pkg/config/config.go:8-11` claims **CLI > config > default**. Actual order is **CLI > config > env > default** because `DIFFAH_WORKDIR` is handled inside `internal/workdir.Resolve` at `internal/workdir/workdir.go:23-33`, which runs after config has already overridden the field.

There is no `DIFFAH_MEMORY_BUDGET` env path in `cmd/spool_flags.go:74-95` or config loading — memory budget is CLI/config/default only.

`cmd/config_defaults_test.go:37-46` says apply shares diff's `workdir` / `memory-budget` keys; the import plan called for separate `apply-workdir` / `apply-memory-budget`.

Byte-suffix parsing is shared (`cmd/spool_flags.go:97-128:parseMemoryBudget`) — export/import parse identically. Good.

---

## Test gaps the lessons doc didn't name

- ENOSPC / EACCES injection on spool dirs
- Short reads from registry transports
- Cancellation at every `GetBlob` / `DecodeStream` choke point
- Early-close verification tests for `verifyingReadCloser`
- Digest path-traversal sidecar tests
- Import baseline-only large-layer RSS tests
- The `--workers=1` pinned tests are not harmless — at higher concurrency they can silently miss the intended B2 because the shared spool is digest-keyed across baselines
- `pkg/importer/decode_concurrent_test.go:18-35` only covers `EncodingFull`, but `HasThreadSafeGetBlob` is already `true` at `pkg/importer/compose.go:69-80` — patch concurrency is uncovered

---

## What the lessons doc captured well vs. missed

**Captured well:** errgroup cancellation hazards (A1), blob cache/spool API shape (A6), `MaxParallelDownloads` (A8), open acceptance gates (A11/A13/A14).

**Missed:** baseline-only reuse still buffers in RAM, importer spool identity is too broad for per-image baselines, verification depends on EOF, config/env precedence drifted, import extraction is outside the workdir model, digest validation is insufficient for path construction.

---

## Follow-up debt — ranked by blast radius

### P0 (data loss / silent corruption / memory budget violation)
- Stream baseline-only reuse from spool instead of `os.ReadFile`. Fix `pkg/importer/compose.go:97-110` and `:307-321`.
- Validate all sidecar digests before using them in paths. Fix `pkg/diff/sidecar.go:60-133`.
- Close the cross-image baseline masking hole. Prefer per-image baseline completeness preflight; then remove `--workers=1` pins.
- Make `verifyingReadCloser.Close` fail if EOF verification never happened.

### P1 (test coverage that masks real regressions)
- Move import extraction under `<workdir>/bundle` and unify cleanup/logging.
- Budget-check only `applyList`, not every sidecar image.
- Add ENOSPC / EACCES / short-read / cancel tests around spool and DecodeStream.
- Add patch-path concurrent `GetBlob` test now that `HasThreadSafeGetBlob` is true.
- Treat short `CopyN` in archive extraction as content corruption.

### P2 (ergonomic / observability)
- Make CLI/config/env precedence explicit and consistent. Either implement `DIFFAH_MEMORY_BUDGET` and env-before-config, or remove that claim from help/spec.
- Add cleanup failure logging for importer.
- Add queued-goroutine limits or backpressure to `AdmissionPool.Submit`.

---

## What's clean

> Forcing `MaxParallelDownloads: 1` at the `copy.Image` call site is the right small containment for containers/image fan-out.

— Codex
