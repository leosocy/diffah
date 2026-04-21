# Changelog

## [Unreleased] — Multi-image bundle support

### Breaking changes

- **`diffah export`**: `--target`, `--baseline`, `--output` flags removed.
  Replaced by `--pair NAME=BASELINE,TARGET` (repeatable) and a positional
  output argument. Use `--bundle FILE` to specify pairs via JSON.
- **`diffah import`**: `--delta`, `--baseline`, `--output` flags removed.
  Replaced by `--baseline NAME=PATH` (repeatable), `--baseline-spec FILE`,
  `--strict`, and positional `DELTA OUTPUT` arguments.
- **Sidecar schema**: The sidecar JSON (`diffah.json`) now uses the bundle
  format with `feature: "bundle"`, a `blobs` map, and an `images` array.
  Phase 1 archives are detected and rejected with a helpful hint.

### New features

- **Multi-image bundles**: Export multiple image deltas into a single
  deduplicated archive. A content-addressed blob pool stores each unique
  blob once, reducing archive size when images share layers.
- **Cross-image dedup**: Shared shipped blobs (referenced by 2+ images) are
  forced to `encoding=full` to avoid per-baseline reachability analysis.
- **Per-image baselines**: Import resolves baselines by name, supporting
  multi-image bundles where each image has its own baseline.
- **Strict mode**: `--strict` flag on import requires all baselines to be
  provided; missing baselines are an error instead of a skip.
- **Bundle spec files**: `--bundle bundle.json` (export) and
  `--baseline-spec baselines.json` (import) for complex multi-image setups.
- **Per-image inspect**: `diffah inspect` shows per-image target/baseline
  manifest digests and source hints for bundle archives.
- **Deterministic output**: Two exports with identical inputs produce
  byte-identical archives (sorted blob order, deterministic timestamps).
- **Progress output**: `diffah export` prints progress to stderr when
  `--progress` is set.

### Internal

- `pkg/diff.Sidecar` replaces `LegacySidecar` (deleted).
- `pkg/exporter` rewritten with `blobPool`, `pairPlan`, `encodeShipped`,
  `assembleSidecar`, `writeBundleArchive`.
- `pkg/importer` rewritten with `extractBundle`, `resolveBaselines`,
  `validatePositionalBaseline`, `composeImage`.
- `CompositeSource` removed — replaced by directory-backed compose.
- `imageio.OpenArchiveRef` sniffs OCI vs Docker schema 2 archives.
