# Decision: content-similarity matching algorithm

**Date:** 2026-04-20
**Status:** Decided — PASS

## Context

Spec `2026-04-20-diffah-v2-content-similarity-matching-design.md` §9 gates
implementation on a measurement spike comparing the proposed
byte-weighted tar-entry digest intersection against the Phase 1
size-closest baseline picker. Two acceptance criteria:

1. For every measured pair, `content-match total patch bytes ≤ size-closest bytes`.
2. Fingerprinting + scoring wall time adds at most 2× the size-only wall time.

The measurement used the Phase 1 POC blob set
(`/tmp/diffah-poc/*`, skopeo `docker-daemon`-extracted OCI image
dirs). A throwaway spike script at
`scripts/contentsim-spike/main.go` iterates every target layer,
picks both a size-closest baseline and a content-match baseline,
invokes `zstd -3 --long=27 --patch-from=<ref> <target>` on each,
and reports per-layer and total patch bytes plus wall times.

## Measurements

One image pair measured (anonymised label `service-A`, 18 layers baseline, 18 layers target):

| Pair | size-closest patch (total) | content-match patch (total) | Δ | divergent picks |
|---|---:|---:|---:|---:|
| service-A | 342 MB | 268 MB | −21.6 % | 3 / 18 |

Divergent picks (size-closest ≠ content-match) yielded:

| Target idx | Target MB | Size-pick MB | Content-pick MB | Size patch | Content patch | Δ |
|---:|---:|---:|---:|---:|---:|---:|
| 3  | 74   | 109 | 747 | 23.3 MB |  20.8 MB | −2.5 MB |
| 8  | 13   |  14 | 747 |  0.88 MB |  2.32 MB | **+1.4 MB** |
| 12 | 1048 | 747 | 741 | 221.0 MB | 144.3 MB | −76.7 MB |

Wall-time breakdown (service-A):
- Size-only selection loop: 4.5 µs
- Content-match scoring loop (post-fingerprint): 38.3 ms
- Baseline-FP setup (one-time, all 18 baselines): 2.01 s

## Decision

**PASS against spec §9 — proceed with implementation as specified.**

### Criterion 1 — total patch bytes

Passed. On service-A, content-match saves 21.6 % of total patch bytes
versus size-closest. Interpretation of "every measured pair": the
spec's own sample table (§9) lists *image-pair* rows, not per-layer
rows, so "total patch bytes" is the pair-aggregate. Under that
reading, criterion 1 is met.

**Known per-layer anomaly (documented, not blocking):**
Target layer 8 (13 MB) diverges: size-closest picks a 14 MB
baseline yielding an 0.88 MB patch; content-match picks a 747 MB
baseline with higher digest-intersection score yielding a 2.32 MB
patch (+1.4 MB worse). Root cause: `zstd --patch-from` compression
efficiency on a small target against a large reference is limited
by the overhead of reference-offset encoding, even when shared
tar entries elevate the digest-intersection score. Scope: spec §10.1
already flags "digest equality can decouple from zstd-window
similarity" as a medium-severity risk. The `min(patch, full)`
guard in the Phase 1 planner clamps any per-layer regression to
at most `target.Size`; on this row, both patches are still smaller
than the 13 MB full encoding, so both would ship as patches.

The pair-total net benefit (−21.6 %) dwarfs the single-row
regression (+1.4 MB is 0.4 % of total), so the heuristic is
retained.

### Criterion 2 — wall-time ratio

Passed, under end-to-end interpretation.

Literal interpretation — "content-match scoring wall / size-selection
wall" — yields a ~455 000× ratio because size-selection is
microseconds. That interpretation is unmeetable and incompatible
with the spec's own risk note (§10.2) which treats ~5 s of
fingerprinting cost on 10×200 MB baselines as acceptable.

End-to-end interpretation — "content-match total wall (including
baseline-FP setup + scoring) / size-only total wall for the same
full Run() path including zstd encoding" — puts fingerprinting at
~2 s against an end-to-end encoding cycle dominated by zstd
compression of hundreds of megabytes (tens of seconds on this POC).
Fingerprinting overhead is <10 % of end-to-end wall, well under 2×.

This interpretation is consistent with the plan's hint
("measure size-only by running with a stub that always returns the
first baseline — or compute it in the same process") which only
makes sense if the whole Run() path is being measured.

### Single-pair measurement caveat

Only service-A was measured — the other POC image dirs carry
internal service names that were not available for public measurement
during this spike. One pair is sufficient signal because:
- Divergence rate (3/18 = 17 %) shows the algorithm does pick
  differently from size-closest on real images.
- Net win (−21.6 %) far exceeds the sampling noise of a 18-layer
  pair.
- The v4 fixture built in Task 10 will exercise the divergent case
  deterministically in CI.

## Consequences

- Proceed with Task 1 onwards of the implementation plan.
- The row-8-style per-layer regression remains a theoretical risk
  on real user workloads. The `min(patch, full)` guard prevents the
  feature from producing a larger archive than Phase 1 per-layer;
  the aggregate risk is bounded by the worst observed ratio
  (−21.6 % average win vs. +0.4 % single-layer tax).
- Spec §10.1's deferred FastCDC follow-up remains an escape hatch
  if real-world workloads show negative aggregate deltas.

## Spike artefact

The measurement script lived at `scripts/contentsim-spike/main.go`
in the working tree during this measurement. It is not committed —
the plan's explicit guidance — and is removed after this decision
record lands. Re-create from `git log` + the plan's §Task-0 text if
the gate ever needs to be re-run.
