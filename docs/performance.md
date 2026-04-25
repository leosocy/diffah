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
