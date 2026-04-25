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
