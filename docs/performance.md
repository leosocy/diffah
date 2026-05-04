# diffah — performance characteristics

## Bandwidth

`diffah diff` and `diffah bundle` must read every byte of every baseline
and target layer to fingerprint their tar entries for content-similarity
matching. For an N-GB baseline set paired with an M-GB target set, expect
approximately `N + M` GB of registry egress per run when the source is a
`docker://` reference.

**Baseline and target layers are not retained.** Bytes stream through an
in-memory tar reader and are discarded as soon as the fingerprint is
computed. Peak RSS stays within `O(workers × max_layer_chunk)` — not
`O(sum of layer sizes)`.

Today's implementation issues a small constant number of round-trips
per baseline blob — today we observe ~3-4 hits per digest (HEAD +
retry-path HEAD + GET, driven by containers-image's stream path). The
integration test `TestDiffCLI_BandwidthBaselineBlobsAreFetchedBounded`
in `cmd/` gates this at a loose upper bound (`10`) to catch runaway
regressions (e.g., accidentally opening a fresh `ImageSource` per
layer). Tightening it toward the "exactly one GET per layer" target is
tracked as a Phase-4 caching refactor in the production-readiness
roadmap.

## Memory

The exporter does not accumulate blob bytes. Plan → encode → write is
pipelined; encoded blobs are flushed to the output archive as they are
produced. Peak RSS on a typical multi-layer OCI fixture stays in the tens
of MB range.

A future phase adds a GB-scale synthesized benchmark gated behind
`DIFFAH_BIG_TEST=1` with an explicit memory-growth regression gate. The
shape of that gate is documented in
`docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md`
§Phase 4.

## Registry-target push

`apply` / `unbundle` write each reconstructed layer once to the target
registry. For images where the delta-selected shipped blobs are small,
push bandwidth approximates the delta size, not the reconstructed image
size.

## When to avoid `diff` over a registry source

If both baselines are already on disk as OCI or Docker archives, the
`docker-archive:` / `oci-archive:` sources are strictly cheaper — no
HTTP round-trip, no auth negotiation. Registry sources are for
workflows where copying the baseline locally first is itself expensive
(e.g., air-gapped producer pipelines running behind a caching proxy).

## Phase 4 — Delta quality & throughput

### Bandwidth and memory characteristics

- **Producer-side baseline reads.** Each baseline blob referenced by any
  pair is loaded at most once into `fpCache` for the duration of an
  `Export()` call. Multi-pair runs that share a baseline pay 1×, not
  N×, the per-blob cost. Singleflight collapses concurrent misses on
  the same digest, so a worker pool of any size still issues a single
  fetch per distinct baseline digest.
- **Encoder memory.** Per running encode, peak memory ≈ `2 × 2^WindowLog`
  bytes (the zstd long-mode buffer). With `--workers=8` and
  `--zstd-window-log=auto`, worst case across an 8-way parallel encode
  of >1 GiB layers is ≈ 32 GiB. Build-farm-class hosts are the target;
  set `--zstd-window-log=27` (≈ 2 GiB worst case at 8 workers) for
  laptop-class environments.
- **Top-K trial cost.** With `--candidates=K`, each shipped target layer
  performs K patch encodes and one full-zstd encode within a single
  worker. The smallest emitted bytes win. Wire I/O is unchanged
  (baseline refs are loaded from `fpCache`, never re-fetched).

### Determinism

For a fixed `(baseline, target, --candidates, --zstd-level,
--zstd-window-log)` tuple, the produced delta archive is byte-identical
regardless of `--workers`. Pinned by the unit test
`TestExport_OutputIsByteIdenticalAcrossWorkerCounts` in `pkg/exporter`,
which drives the same export across workers ∈ {1, 2, 4, 8, 16} and
SHA-256-equates the resulting archives.

### Operator overrides

| Goal | Flags |
|---|---|
| Match Phase-3 output bytes | `--zstd-level=3 --zstd-window-log=27 --candidates=1 --workers=1` |
| Speed-prioritized CI | `--zstd-level=12 --candidates=2` |
| Maximum compression | `--zstd-level=22 --zstd-window-log=31 --candidates=5` |
| Phase-3 importer compatibility | `--zstd-window-log=27` (other Phase-4 flags ok) |

---

## Phase 4 — Streaming I/O: bounded-memory contract

The Phase 4 streaming pipeline replaces in-memory blob accumulation with a
producer→spool→ordered-drainer design. Peak RSS is bounded regardless of layer
size, capped by `--memory-budget` (default `8GiB`).

### Streaming knobs

| Knob | Default | Effect |
|---|---|---|
| `--memory-budget BYTES` | `8GiB` | Admission cap for concurrent encoder RSS. Encodes are admitted only when the sum of in-flight estimated RSS plus the new encode's estimate fits within this budget. |
| `--workers N` | `min(GOMAXPROCS, 4)` | Hard cap on encoder goroutines. Both the worker-count gate and the memory-budget gate apply independently. |
| `--workdir DIR` | `<dir(OUTPUT)>/.diffah-tmp/<random>` | Spool root for disk-backed baseline / target / output blob spills. Also settable via `DIFFAH_WORKDIR`. |

### RSS estimate table

The estimated RSS per encode is derived from the chosen `windowLog`
(itself a function of layer size, via `--zstd-window-log=auto`):

| `windowLog` | Layer size threshold | RSS estimate |
|---|---|---|
| 27 | ≤ 128 MiB | 256 MiB |
| 28 | ≤ 256 MiB | 512 MiB |
| 29 | ≤ 512 MiB | 1 GiB |
| 30 | ≤ 1 GiB | 2 GiB |
| 31 | > 1 GiB | 4 GiB |

These estimates are intentionally conservative. The nightly CI benchmark
validates the 8 GiB ceiling on a real 2 GiB-layer fixture.

### Disk budget

Each in-flight encode uses up to `(K+1) × max_layer_size` of spool space
(K candidate spills + 1 target spool). Under typical settings
(`--workers 4`, `--candidates 3`, GB-scale layers) the spool can peak at
around `4 × 4 × 1 GiB ≈ 16 GiB`. Operators with limited spool space should:

- Set `--workdir` to a filesystem with more free space.
- Lower `--workers` and/or `--candidates`.

### Single-layer-exceeds-budget fail-fast

If any single shipped layer's estimated RSS exceeds `--memory-budget`,
`Export()` fails immediately with a structured error before opening any spool.
The error message includes the offending layer digest and suggests either
raising `--memory-budget` to at least twice the layer size or lowering
`--zstd-window-log`.

### Disabling admission (benchmarking / debugging)

Pass `--memory-budget=0` to disable the admission controller entirely. Encode
goroutines are then limited only by `--workers`. Use this to benchmark raw
throughput without admission serialisation.

### Nightly benchmark — spec §13 acceptance gate

A deterministic 2 GiB-layer fixture is built by
`scripts/build_fixtures -scale=2147483648 -out=<dir>` and consumed by
`TestScaleBench_2GiBLayer` in `pkg/exporter/scale_bench_test.go`
(build tag `big`, env `DIFFAH_BIG_TEST=1`).

The nightly GitHub Actions workflow (`.github/workflows/scale-bench.yml`):
1. Builds and runs the test wrapped in `/usr/bin/time -v`.
2. Parses peak RSS from the `/usr/bin/time` output.
3. Fails the job if peak RSS exceeds **8 GiB** (8 388 608 KiB).

A walltime regression gate will be added in a follow-up PR once real nightly
baselines are established from actual runs; no placeholder is committed now.
