# diffah Export Streaming I/O — Design

- **Status:** Draft
- **Date:** 2026-05-02
- **Author:** @leosocy
- **Implements:** Phase 4 ("Scale robustness") of [`2026-04-23-production-readiness-roadmap-design.md`](2026-04-23-production-readiness-roadmap-design.md).
- **Scope:** Exporter only. The symmetric importer OOM is acknowledged in §9 and tracked as a sibling spec.

## 1. Context

`diffah export` today accumulates encoded blob bytes in memory until the writer drains them into the output `.tar`. Three RAM hot spots compound:

| Hot spot | Code | Memory shape |
|---|---|---|
| Output staging | `pkg/exporter/pool.go` `blobPool.bytes` | `Σ encoded_size` for every shipped blob until `writeBundleArchive` |
| Baseline retention | `pkg/exporter/fpcache.go` `fpCache.bytes` | `Σ baseline_layer_size` for every distinct baseline digest, pinned for the entire `Export()` |
| Per-encode buffers | `pkg/exporter/encode.go` `encodeOneShipped` + `pkg/exporter/intralayer.go` `PlanShippedTopK` | `target_bytes + K × patch_bytes + full_zstd_bytes` per in-flight encode |

A single 4 GiB layer therefore allocates ~4 GiB before encoding starts, ~4 GiB while encoding (target + ref both resident), and the encoded output stays resident until the bundle is written. Multiple GB-scale layers OOM the process well below the production-readiness goal of "4 GB-layer image within 8 GB RAM."

A second, less-visible source of pressure: the `internal/zstdpatch` CLI wrapper writes its `[]byte` inputs to temp files on every call (see `internal/zstdpatch/cli.go` lines 53-67) and reads the patch back into a `[]byte`. Even when the caller already has the bytes on disk, we pay write+read+allocate. The `[]byte`-shaped API is itself part of the problem.

## 2. Goals

A release meets the streaming-I/O bar when **all four** criteria hold:

1. **Memory contract.** Peak RSS ≤ a configurable budget (default 8 GiB). The single biggest layer must fit the budget; aggregate corpus size does not affect peak RSS.
2. **Bit-exact output.** The bundle `.tar` is byte-identical across `--workers` settings (1, 4, 8) for fixed inputs. The existing `determinism_test.go` assertions pass unchanged.
3. **Bandwidth parity.** Each baseline layer is fetched at most once per `Export()` (today's `fpCache` already provides this; the streaming refactor preserves it).
4. **Cleanup.** Successful or failed `Export()` (including `ctrl-C` and panics) leaves no temp files behind.

## 3. Non-goals

- **Importer streaming.** `pkg/importer/blobcache.go` and `bundleImageSource.serveFull/servePatch` mirror the same OOM but are deliberately deferred to a sibling spec — see §9.
- **Replacing the zstd CLI subprocess with a pure-Go patch encoder.** klauspost/compress does not implement zstd `--patch-from`. Out of scope.
- **Resume / checkpoint** for interrupted exports. A single `Export()` either completes or starts over.
- **Multi-host distributed encoding.**
- **Backward-incompatible CLI changes.** Both new flags have sane defaults; existing invocations behave identically.

## 4. Architecture

### 4.1 Pipeline shape

The exporter becomes a **producer → spill spool → ordered drainer** pipeline. Three disk-backed stores live under a single per-`Export()` workdir:

```
<workdir>/
  baselines/<digest>     ← one file per distinct baseline layer (TeeReader-written once)
  targets/<digest>       ← one file per shipped target layer (deleted after last patch encode)
  blobs/<digest>         ← one file per encoded output blob (drained sorted into output.tar)
```

Data flow per pair:

```
plan ──► fetch+spool baselines ──► encode workers ──► <workdir>/blobs/
                  │                       │                   │
                  └─► fingerprint        admission            └─► sorted drain ──► output.tar
                      (TeeReader)        controller               (digest order)
```

Memory upper bound:

```
peak_RSS  ≈  fixed_overhead
            + Σ (admission-controlled in-flight encodes × estimated_per_encode_RSS)
            + Σ (in-flight TeeReader buffers ≈ 64 KiB × distinct baseline streams)
```

Importantly, `peak_RSS` is independent of `Σ baseline_size`, `Σ target_size`, and `Σ output_size` — those live on disk.

### 4.2 Workdir lifecycle

- **Path resolution** (precedence first → last):
  1. `--workdir DIR` flag.
  2. `DIFFAH_WORKDIR` env var.
  3. Default: `<dir(opts.OutputPath)>/.diffah-tmp/<random16hex>`.
- **Creation:** `os.MkdirAll(workdir, 0o700)` on `Export()` entry; subdirs `baselines/`, `targets/`, `blobs/` created lazily on first use.
- **Cleanup:** `defer os.RemoveAll(workdir)` in `Export()` so success, failure, ctx-cancel, and panic all reach it. Worker goroutines additionally `defer` per-encode temp-file cleanup so their losing-candidate spills are gone before the top-level `RemoveAll`.
- **Co-location rationale:** the user already has space on the output filesystem to write the bundle; co-locating the spool there makes "ENOSPC on `os.TempDir()`" impossible by default. Operators with a dedicated fast-temp disk override via `--workdir`.

### 4.3 Memory budget enforcement

Two gates apply to every encode submission:

1. **Worker semaphore** — capacity `min(GOMAXPROCS, 4)` (default), overridden by `--workers N`. Hard cap on goroutines.
2. **Memory admission** — capacity `--memory-budget BYTES` (default `8GiB`). Each encode declares an estimated RSS via the table below; admission blocks until `inflight_estimate + new_estimate ≤ budget`.

```go
// pkg/exporter/admission.go — revisable internal table; not exposed.
var rssEstimateByWindowLog = map[int]int64{
    27: 256 << 20, // 256 MiB
    28: 512 << 20,
    29: 1 << 30,   // 1 GiB
    30: 2 << 30,
    31: 4 << 30,   // empirical zstd-CLI peak with --long=31 at level 22
}
```

The estimate selection uses the layer's `windowLog` (already chosen per layer by `ResolveWindowLog` in `pkg/exporter/intralayer.go:25-37`). It is intentionally conservative; benchmark data may revise the table.

**Single-layer-exceeds-budget guard.** Before opening any spool, `Export()` walks `plans[].Shipped[]`, computes the largest predicted estimate, and fails fast with a structured error if it exceeds the budget. Error category is `user`; next-action hint: `try --memory-budget=<2× the layer size> or --workers 1 with a smaller window`.

Implementation: `golang.org/x/sync/semaphore.NewWeighted(budgetBytes)` + `Acquire(ctx, estimate)`. Release on encode completion regardless of outcome.

`--memory-budget 0` disables admission control (operator opt-out for benchmarking against the worker-only default). Documented but not recommended.

### 4.4 Determinism

Output bit-exactness is preserved because:

- The blob pool is content-addressed; first-write-wins on digest collision.
- Writer iterates `pool.sortedDigests()` and `io.Copy`s each spill file. Sort order is byte-lex on digest.
- Encoder candidate selection is digest-tie-broken (existing `PickTopK` behavior unchanged).
- Admission ordering does not affect output — it only affects when an encode runs, not what it produces.

## 5. Components

### 5.1 `internal/zstdpatch` API extension

Add path-based functions; keep legacy `[]byte` functions as one-line shims marked deprecated:

```go
// EncodeStream produces a zstd patch from refPath against targetPath, written
// to outPath. Returns the encoded size in bytes. ctx cancellation kills the
// zstd subprocess. Bit-equivalent to Encode(refBytes, targetBytes) when the
// inputs are byte-identical.
func EncodeStream(ctx context.Context, refPath, targetPath, outPath string, opts EncodeOpts) (int64, error)

// DecodeStream reverses EncodeStream. Added now (even though importer
// streaming is out of scope) to avoid a second package-surface churn when
// the importer migration spec lands.
func DecodeStream(ctx context.Context, refPath, patchPath, outPath string) (int64, error)

// EncodeFullStream wraps klauspost/compress/zstd.Encoder and writes to w.
// No subprocess, no temp files. Used by the streaming planner to compute
// the full-zstd size ceiling without ever materializing the encoded bytes
// to disk.
func EncodeFullStream(ctx context.Context, targetPath string, w io.Writer, opts EncodeOpts) (int64, error)
```

CLI variants (`EncodeStream`, `DecodeStream`) shell out to `zstd --patch-from=refPath targetPath -o outPath` with no intermediate `os.WriteFile`/`os.ReadFile`. The existing `Encode/Decode/EncodeFull` retained as wrappers:

```go
// Deprecated: use EncodeStream. Retained for the importer hot path until
// the importer streaming spec migrates it.
func Encode(ctx context.Context, ref, target []byte, opts EncodeOpts) ([]byte, error) {
    // write ref, target to temp; call EncodeStream; read patch from temp.
}
```

The shim path costs the same disk I/O as today, so importer behavior is unchanged.

### 5.2 Baseline spool (`pkg/exporter/baselinespool.go`)

Replaces `fpCache.bytes`. Public surface:

```go
type baselineSpool struct {
    dir string
    mu  sync.RWMutex
    entries map[digest.Digest]baselineEntry
    sf  singleflight.Group
}

type baselineEntry struct {
    Path        string       // <workdir>/baselines/<digest>
    Fingerprint Fingerprint  // nil = fingerprint failed; size-only fallback
}

// GetOrSpool ensures the digest is on disk and fingerprinted, exactly once.
// Concurrent callers on the same digest collapse to a single fetch+fingerprint
// pass via singleflight.
func (s *baselineSpool) GetOrSpool(
    ctx context.Context, meta BaselineLayerMeta,
    fetch func(digest.Digest) (io.ReadCloser, error),
    fp Fingerprinter,
) (baselineEntry, error)
```

First-touch flow:
1. Open `<dir>/<digest>` for write.
2. `tee := io.TeeReader(fetchReader, file)`.
3. `fp.FingerprintReader(ctx, mediaType, tee)` — new method on `Fingerprinter` that streams (see §5.5). Drains `tee` to EOF.
4. Close file, store entry, release singleflight.
5. On error mid-stream: close + `os.Remove` the partial file; do not cache; next caller retries (mirrors `fpCache`'s no-cache-on-error contract).

The patch encoder consumes baselines by `entry.Path`, never by `[]byte`.

### 5.3 Encoder pool (`pkg/exporter/encodepool.go`)

Replaces today's `workerpool.go` for the encode phase. Two-gate submission:

```go
type encodePool struct {
    ctx       context.Context
    workers   *errgroup.Group
    workerSem *semaphore.Weighted // capacity = workers count
    memSem    *semaphore.Weighted // capacity = memory budget bytes; nil = disabled
    sf        singleflight.Group  // collisions on same digest
}

func (p *encodePool) Submit(d digest.Digest, estimate int64, fn func() error)
```

Submission semantics:
- `singleflight.Do(string(d))` ensures only one goroutine per digest is admitted; collisions return the winner's result without ever calling `fn`.
- Acquires `workerSem` (1 token) and `memSem` (`estimate` tokens). Both blocking, both ctx-aware.
- `defer` releases both gates.
- Errors propagate through `errgroup`; first error cancels the rest.

`memSem == nil` (when `--memory-budget 0`) skips the memory gate entirely.

### 5.4 Per-encode flow (`encodeOneShipped`)

Rewritten to operate on file paths end-to-end. Note that in the existing planner, the `fullZst` value is purely a *size ceiling* used to gate whether a candidate patch is worth shipping — it is never the surviving payload. `encoding=full` in the sidecar means "ship the original target bytes verbatim" (the layer is already compressed by the upstream registry). Pseudo-flow:

```
1. targetPath := <workdir>/targets/<s.Digest>
   stream s.Digest from p.TargetImageRef into targetPath
   (we also TeeReader through a counter for progress.Layer.Written)

2. targetFP := fp.FingerprintReader(ctx, s.MediaType, openFile(targetPath))   // re-read of on-disk target
   cands := planner.PickTopK(ctx, targetFP, s.Size, K)

3. fullZstSize := EncodeFullStream(targetPath, &countingWriter{}, opts)   // size-only; no disk write

   bestSize  := s.Size                                // raw-target ceiling — always available
   bestKind  := "full"                                // = ship raw target bytes
   bestPath  := targetPath
   bestCand  := nil

   for c in cands:
       candPath := <workdir>/blobs/<s.Digest>.cand-<c.Digest[:8]>
       patchSize := EncodeStream(ctx, baselineSpool[c].Path, targetPath, candPath, opts)
       // Patch must strictly beat all three: full-zstd ceiling, raw target, and running best.
       if patchSize < fullZstSize && patchSize < s.Size && patchSize < bestSize:
           if bestCand != nil { os.Remove(bestPath) }   // discard previous winner
           bestSize = patchSize; bestKind = "patch"; bestCand = c; bestPath = candPath
       else:
           os.Remove(candPath)

4. finalPath := <workdir>/blobs/<s.Digest>
   if bestKind == "patch":
       os.Rename(bestPath, finalPath)            // bestPath was a cand-* spill
       sidecarEntry = patch entry referencing bestCand.Digest
       os.Remove(targetPath)                     // target spool no longer needed
   else: // bestKind == "full"
       os.Rename(targetPath, finalPath)          // target spool itself becomes the output blob
       sidecarEntry = full entry

5. pool.AddEntry(s.Digest, finalPath, sidecarEntry)
```

**Why size-only `EncodeFullStream`:** the full-zstd encoding is a size ceiling only — it is *never* persisted. The klauspost streaming encoder lets us count bytes without writing them. This saves one full-layer disk write per encoded layer.

**Why per-candidate spill (rather than size-only patch encoding):** the CLI `zstd --patch-from` does not have a "size only" mode that suppresses the output file. We could pipe stdout to a counter, but the CLI requires `-o PATH` for `--patch-from`; piping requires `-` and breaks the path-based shape of the new API. Per-candidate spill is the simplest correct approach; disk cost is bounded by `(K+1) × max_layer_size` in the worst case (K candidate spills + 1 target spool), and the running-best optimization deletes losers as we go.

**Why re-fingerprint the target from disk:** today's `PlanShippedTopK` computes `targetFP` from in-memory bytes. With the target already on disk, we re-read it through `FingerprintReader`. Cost: one extra sequential read of the target spool (which is OS-cached because we just wrote it). This avoids holding the target bytes in RAM purely to fingerprint them.

### 5.5 Fingerprinter `Reader`-based variant

Add to the `Fingerprinter` interface:

```go
type Fingerprinter interface {
    Fingerprint(ctx context.Context, mediaType string, blob []byte) (Fingerprint, error)         // existing
    FingerprintReader(ctx context.Context, mediaType string, r io.Reader) (Fingerprint, error)   // NEW
}
```

`DefaultFingerprinter.FingerprintReader` reuses the existing `fingerprintTar(ctx, decompressed)` path; the new entrypoint just constructs the decompressor from `r` instead of `bytes.NewReader(blob)`. The existing `Fingerprint([]byte)` becomes a one-line wrapper.

### 5.6 Writer (`pkg/exporter/writer.go`)

Replace `pool.get(d) → []byte` with `pool.spillPath(d) → string`:

```go
for _, d := range pool.sortedDigests() {
    f, err := os.Open(pool.spillPath(d))
    if err != nil { return fmt.Errorf("open spill %s: %w", d, err) }
    info, err := f.Stat()
    if err != nil { f.Close(); return err }
    hdr := &tar.Header{Name: blobPath(d), Size: info.Size(), Mode: 0o644, Format: tar.FormatPAX}
    if err := tw.WriteHeader(hdr); err != nil { f.Close(); return err }
    if _, err := io.Copy(tw, f); err != nil { f.Close(); return err }
    f.Close()
}
```

Sidecar entry write is unchanged (small, in-memory).

### 5.7 `blobPool` slimming

```go
type blobPool struct {
    mu       sync.RWMutex
    spills   map[digest.Digest]string         // <workdir>/blobs/<digest>
    entries  map[digest.Digest]diff.BlobEntry
    shipRefs map[digest.Digest]int
}
```

`addIfAbsent` becomes `addEntryIfAbsent(d, spillPath, entry)`. Manifests and configs (small, in-memory today via `seedManifestAndConfig`) are written to `<workdir>/blobs/<digest>` once at seed time so the writer drains a single store uniformly.

## 6. CLI surface changes

```
Spool & memory:
      --workdir DIR              spool location
                                 default: <dir(OUTPUT)>/.diffah-tmp/<random>
                                 also: DIFFAH_WORKDIR env (overrides default; --workdir overrides env)
      --memory-budget BYTES      adaptive admission budget for encoder concurrency
                                 default: 8GiB
                                 supports suffixes: KiB MiB GiB KB MB GB; 0 disables admission
```

Existing `--workers N` semantics tighten to "hard cap on encoder goroutines"; default already `min(GOMAXPROCS, 4)`. Both new flags also added to `~/.diffah/config.yaml` (Phase 5.2 config file) under top-level `workdir:` and `memory_budget:`.

Example error from single-layer-exceeds-budget guard:

```
diffah: user: layer sha256:abc12345... is 5.2 GiB; admission requires --memory-budget ≥ 5.2 GiB or --workers 1 with --long ≤ 30
  hint: try --memory-budget=12GiB
```

## 7. Backward compatibility

- **Output bit-exactness.** Existing `determinism_test.go` test cases pass unchanged — same digests, same ordering, same tar entry contents. CI gate.
- **CLI invocations.** No removed flags; new flags have defaults. `diffah export --pair name=base,target out.tar` works identically.
- **Sidecar schema.** Unchanged. No schema version bump.
- **`internal/zstdpatch` legacy API.** `Encode`, `Decode`, `EncodeFull` retained as deprecated `[]byte`-shaped wrappers. Importer compiles unchanged. Deprecation comment points at the importer streaming sibling spec.
- **In-process exporter callers.** `Options` gains `Workdir string` and `MemoryBudget int64`; both zero-valued behave as documented defaults. No existing field changes meaning.

## 8. Testing strategy

### 8.1 Unit

- **`zstdpatch` parity.** `EncodeStream`/`DecodeStream`/`EncodeFullStream` round-trip against the legacy `Encode`/`Decode`/`EncodeFull`, byte-for-byte, on shared fixture inputs. New parity tests added to `internal/zstdpatch/cli_test.go` and `internal/zstdpatch/fullgo_test.go`.
- **Baseline spool.** TeeReader correctness (file content ≡ source bytes), singleflight collapses concurrent first-touches to one fetch (assert via `atomic.Int32` fetch counter), error mid-stream removes partial spool file and does not cache.
- **Admission controller.** Token budgeting (3 encodes × 2 GiB each must serialize under 4 GiB budget), single-layer-exceeds-budget fail-fast, `--workers 1` parity with old serial path, `--memory-budget 0` disables gate.
- **Encoder per-encode flow.** K losing candidates' temp files are deleted; winner is renamed (no copy); raw-target-wins path renames target spool; full-zstd-wins re-encodes; cancel mid-encode kills subprocess and removes partial output.
- **Fingerprinter `Reader` variant.** Returns identical fingerprint to the `[]byte` variant on shared fixtures.

### 8.2 Determinism

`pkg/exporter/determinism_test.go` extended (or new cases added) to assert byte-identical bundle output across:
- `--workers 1` vs `--workers 4` vs `--workers 8`
- `--memory-budget 0` vs `--memory-budget 8GiB`
- Different `--workdir` paths (output content unaffected)

### 8.3 Integration

Existing exporter integration tests (`pkg/exporter/exporter_test.go`, `intralayer_test.go`, `compose_test.go`) pass unchanged. The `bundle.tar` byte-equality assertions are the contract.

### 8.4 GB-scale benchmark

New file: `pkg/exporter/scale_bench_test.go`, build-tagged `//go:build big`, gated by `DIFFAH_BIG_TEST=1`.

**Fixture (patchable, not random):**
- A deterministic 2 GiB OCI layer: tar contents = 1024 files × 2 MiB each, each file body is `sha256(seed || file_index)` repeated to fill 2 MiB. Seeded RNG; reproducible.
- Target = baseline + one extra 4 MiB tar entry appended at the end. Patch encoder should produce a small patch (<10 MiB).
- Why not `dd if=/dev/urandom`: random data does not compress and has zero inter-layer similarity — would not exercise the patch path the spec is supposed to validate.

**RSS measurement:**
- **Linux:** test wrapper re-execs the bench binary as a subprocess under `/usr/bin/time -v`; parses `Maximum resident set size (kbytes):` from stderr. Reported in `benchmarks/scale-export-linux.json`.
- **macOS:** in-process goroutine polls `ps -o rss= -p $$` every 200 ms; records max. Reported in `benchmarks/scale-export-darwin.json`.
- `runtime.MemStats` is *not* used: the zstd subprocess RSS is invisible to the Go runtime.

**CI integration:**
- Nightly job `scale-bench.yml` runs `go test -tags=big -timeout=30m ./pkg/exporter/...` with `DIFFAH_BIG_TEST=1`, on `ubuntu-latest` (16 GiB runner). Asserts:
  - Peak RSS ≤ 8 GiB.
  - Walltime ≤ 110 % of `benchmarks/scale-export-linux.json` baseline.
  - Output tar digest matches the previous run's digest (determinism over time, modulo deliberate fixture changes).

### 8.5 Cleanup contract

Test that `Export()` failure paths leave no files under `<workdir>`:
- Encoder error mid-stream.
- ctx cancellation.
- Worker goroutine panic (recovered, re-raised).
- Disk full on output write.

After each: `ioutil.ReadDir(workdir)` returns empty (or workdir itself is removed).

## 9. Future work (sibling specs)

- **Importer streaming.** `pkg/importer/blobcache.go` `baselineBlobCache.bytes` and `pkg/importer/compose.go` `bundleImageSource.serveFull/servePatch` mirror the same OOM. Same spill-to-disk pattern; uses `DecodeStream` from §5.1. Estimated smaller than this spec because no encoder admission control is needed (decode is single-pass with predictable footprint).
- **Streaming registry push.** Once importer streams, the registry destination push (currently buffers per-layer through `copy.Image`) can stream too. Likely a small upstream `go.podman.io/image/v5` change or a custom destination wrapper.

## 10. Risks

| Risk | Mitigation |
|---|---|
| RSS estimate table understates real zstd peak; budget lets through too many concurrent encodes | Conservative defaults (4 GiB at `--long=31`); benchmark drives revision; budget is operator-overridable |
| Spool ENOSPC on the output filesystem | Default workdir co-located with output (operator already provisioned space for the bundle); structured error category=environment with hint pointing at `--workdir` |
| Per-encode candidate spills create 4× write amplification on disk | Acceptable in exchange for RAM bound; documented in `docs/performance.md` (new); operators with slow local disk can lower `--candidates` |
| Cancellation leaves orphan zstd subprocess | `exec.CommandContext` propagates ctx; subprocess receives SIGKILL on ctx cancel; covered by §8.5 |
| Determinism regression slips through | `determinism_test.go` is the authoritative gate; CI runs at multiple `--workers`; bit-equality required |
| Importer compiles via deprecated shim, then someone touches the shim and breaks the importer | Sibling spec is referenced in the deprecation comment; PR template should require updating both when modifying the shim |

## 11. Open questions

These do not block the spec but should be resolved during implementation:

1. **Workdir placement in tests:** integration tests today write to `t.TempDir()`. The new default would put the spool under `t.TempDir()/.diffah-tmp/...`, which is fine. Confirm no test relies on a clean output dir post-export.
2. **`--memory-budget` units:** spec says SI + IEC suffixes; pick a parser. `github.com/dustin/go-humanize.ParseBytes` is already in `go.sum` transitively — verify before adopting.
3. **RSS estimation table calibration:** the values in §4.3 are educated guesses. First implementation should ship with these as defaults, then the GB-scale benchmark should record actual peak RSS per windowLog and we revise (PR follow-up, not blocking).
4. **`DecodeStream` placement:** added to `internal/zstdpatch` now even though no exporter code calls it, anticipating the importer sibling spec. If the importer spec deviates (e.g., wants a `DecodeReader` instead), we'll adjust then. Cost of adding now: ~30 lines.

## 12. Phased landing

The spec is large but landable in independent PRs. Suggested order:

1. **PR 1 — `zstdpatch` streaming API.** Add `EncodeStream`/`DecodeStream`/`EncodeFullStream`. Legacy API becomes a wrapper. Parity tests. No exporter changes yet. Smallest risk.
2. **PR 2 — Baseline spool.** Replace `fpCache.bytes` with `baselineSpool` writing to a workdir. Add `Fingerprinter.FingerprintReader`. Output bytes unchanged.
3. **PR 3 — Output blob spill + writer streaming.** Replace `blobPool.bytes` with `blobPool.spills`. Update `writer.go` to `io.Copy` from spill files. Output bytes unchanged.
4. **PR 4 — Per-encode streaming flow.** Rewrite `encodeOneShipped` to operate on paths end-to-end. Use `EncodeFullStream` size-only, K candidate spills.
5. **PR 5 — Admission controller + `--memory-budget`.** Add encoder pool with two-gate submission. CLI flag, config file integration.
6. **PR 6 — `--workdir` flag, env, default placement.** Wire workdir resolution end-to-end.
7. **PR 7 — GB-scale benchmark + CI nightly job.** Fixture, RSS measurement, regression gate.

PR 7 is the only one that needs the runner-class change; PRs 1-6 land on the existing CI matrix.

## 13. Acceptance

This spec is satisfied when:

- The 2 GiB-layer fixture from §8.4 exports under 8 GiB RSS on the CI nightly runner.
- `determinism_test.go` passes across `--workers 1/4/8` with output bit-equal.
- `Export()` cleans up its workdir on every termination path (success, error, cancel, panic).
- `internal/zstdpatch.Encode/Decode/EncodeFull` are deprecated but functionally unchanged for importer callers.
- `docs/performance.md` documents the bounded-memory guarantee, the `--memory-budget` flag, and the `--workdir` flag.
