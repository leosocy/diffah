# Decision: zstd patch-from backend for internal/zstdpatch

**Date:** 2026-04-20
**Status:** Decided

## Context

Diffah v2 Phase 1 needs a zstd patch-from encoder and decoder for
`internal/zstdpatch`. Two candidates:

1. `github.com/klauspost/compress/zstd` with raw dictionaries — pure Go.
2. `os/exec` against the `zstd` CLI (≥ 1.5) — external runtime dependency.

Spike ran against two layer pairs from a representative service image
upgrade (`baseline` → `target`).

## Measurements

### Pair 1: layer 0, ~123 MB (similar content, minor updates)

| Metric           | klauspost (DictRaw) | CLI (`--patch-from`) |
|------------------|--------------------:|---------------------:|
| Patch size       |       14,524,028 B  |        2,893,509 B   |
| Ratio vs CLI     |              5.02×  |              1.00×   |
| Full zstd        |       37,640,163 B  |                  —   |
| Patch / full     |             38.6%   |               7.7%   |
| Encode time      |              2.7 s  |                  —   |
| Round-trip       |       byte-exact ✓  |                  ✓   |

### Pair 2: layer 5, ~213 MB (same size, application layer)

| Metric           | klauspost (DictRaw) | CLI (`--patch-from`) |
|------------------|--------------------:|---------------------:|
| Patch size       |       91,385,952 B  |          118,458 B   |
| Ratio vs CLI     |            771.0×   |              1.00×   |
| Full zstd        |       91,496,274 B  |                  —   |
| Patch / full     |             99.9%   |              0.13%   |
| Encode time      |              9.0 s  |                  —   |
| Round-trip       |       byte-exact ✓  |                  ✓   |

### Analysis

klauspost's `WithEncoderDictRaw` processes the reference as a standard zstd
dictionary (seeding the hash tables) but does NOT replicate the CLI's
`--patch-from` semantics, which treats the entire reference file as initial
window history. Two key failures:

1. **Window truncation**: For data > window-log size (128 MB at `--long=27`),
   the dictionary is silently truncated, producing near-zero benefit (Pair 2).
2. **Matching quality**: Even when the dictionary fits (Pair 1 at 123 MB),
   the raw-dictionary hash seeding produces patches 5× larger than the CLI's
   dedicated patch-from mode. This fails the acceptance criterion of ≤ 1.5×.

Peak heap was 1.1–2.0 GB, also exceeding the 500 MB target.

## Decision

**os/exec** — the klauspost raw dictionary API does not implement patch-from
semantics. Patch sizes are 5–771× larger than the CLI, failing the ≤ 1.5×
acceptance criterion. The CLI (`zstd ≥ 1.5 --patch-from`) produces correct,
compact patches at a fraction of the memory cost.

## Consequences

- `internal/zstdpatch` shells out to `zstd --patch-from` via `exec.Cmd`.
- The public interface remains `Encode(ref, target) → patch`,
  `EncodeFull(target) → zstd`, `Decode(ref, patch) → target`.
- README gains a runtime-dependency note: `zstd ≥ 1.5` must be on `$PATH`.
- Unit tests skip with `t.Skip()` if `zstd` is not available.
- No CGO dependency; the diffah binary remains a static Go binary.
- klauspost/compress remains a direct dep (used elsewhere) but is not used
  for intra-layer patching.
