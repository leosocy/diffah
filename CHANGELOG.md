# Changelog

## [Unreleased] — Phase 3: Registry-native export + signing

### Breaking changes

- **`BundleSpec` JSON**: `baseline` / `target` values must now carry a
  transport prefix. Bare-path values (`"baseline": "v1/svc.tar"`) fail
  with a migration hint. One-liner fix:

  ```
  sed -E -i '' 's|(\"baseline\"\|\"target\"): \"([^:\"]*\.tar[a-z]*)\"|\1: \"docker-archive:\2\"|g' bundle.json
  ```

## [Unreleased] — Phase 2: Registry-native import

### Breaking changes

- **`apply`**: positional TARGET-OUT renamed to TARGET-IMAGE; transport
  prefix now required (e.g. `oci-archive:/tmp/out.tar` instead of
  `/tmp/out.tar`). Bare paths error with a "Did you mean:" hint when the
  tail looks like a tar archive.
- **`unbundle`**: positional OUTPUT-DIR renamed to OUTPUT-SPEC. The new
  positional points at a JSON spec file of shape
  `{"outputs": {"<name>": "<transport>:<ref>"}}` symmetric with
  BASELINE-SPEC. Supplying a directory as OUTPUT-SPEC now errors with
  "must be a JSON file" instead of silently writing there.
- **BASELINE-SPEC JSON**: values must carry a transport prefix. Existing
  spec files that use bare filesystem paths fail user/2 on first parse;
  prefix with `docker-archive:` or `oci-archive:` to migrate.
- **Removed flags**: `--image-format` on both `apply` and `unbundle`.
  The transport prefix on the TARGET-IMAGE / OUTPUT-SPEC entry is now
  authoritative for output format selection.

### Additions

- **Transport acceptance**: `apply` and `unbundle` now accept
  `docker://`, `oci:`, and `dir:` on image positionals (in addition to
  `docker-archive:` and `oci-archive:`).
- **Registry & transport flag block** on `apply` and `unbundle`:
  `--authfile`, `--creds`, `--username`/`--password`, `--no-creds`,
  `--registry-token`, `--tls-verify`, `--cert-dir`, `--retry-times`,
  `--retry-delay`. Authfile precedence chain mirrors skopeo's:
  `$REGISTRY_AUTH_FILE` → `$XDG_RUNTIME_DIR/containers/auth.json` →
  `$HOME/.docker/config.json`.
- **Lazy baseline fetch**: when a baseline is a `docker://` reference,
  only the blobs referenced by patch-encoded entries in the delta
  cross the wire. The baseline manifest is always fetched; no other
  layers are.
- **Retry with backoff**: transient 5xx / 429 / connection-refused
  retries up to `--retry-times` with exponential backoff (capped at 30s)
  or `--retry-delay` if supplied. Auth, 404, and manifest-schema errors
  are non-retryable and fail fast.
- **Registry error classification**: auth 401/403 → exit 2 (user);
  network/DNS/TLS → exit 3 (environment); manifest missing/invalid →
  exit 4 (content).

### Internal

- `pkg/importer.Options` fields: `OutputPath` and `OutputFormat` removed;
  `Outputs map[string]string`, `SystemContext *types.SystemContext`,
  `RetryTimes int`, `RetryDelay time.Duration` added.
- `pkg/diff.ParseOutputSpec` added; `ParseBaselineSpec` now requires
  transport-prefixed values.
- New `internal/imageio.BuildSystemContext`; new
  `internal/registrytest` in-process OCI registry harness used by the
  integration test suite.

### Non-goals / deferred

- Registry-source `diff` and `bundle` (export side): Phase 3.
- Signature generation and verification (cosign): Phase 3.
- The `docker-daemon:`, `containers-storage:`, `ostree:`, `sif:`,
  `tarball:` transports remain "reserved but not yet implemented."

## [Unreleased] — CLI redesign (skopeo-inspired)

- **Removed:** `diffah export` and `diffah import`. Old invocations now
  error with a migration hint pointing at the new verbs.
- **Removed:** `--pair NAME=BASELINE,TARGET` and `--baseline NAME=PATH`
  composite flags.
- **Added:** `diffah diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT` — single
  image delta.
- **Added:** `diffah apply DELTA-IN BASELINE-IMAGE TARGET-OUT` — single
  image reconstruction.
- **Added:** `diffah bundle BUNDLE-SPEC DELTA-OUT` — multi-image bundle
  driven by a JSON spec file (positional, not a flag).
- **Added:** `diffah unbundle DELTA-IN BASELINE-SPEC OUTPUT-DIR` —
  multi-image reconstruction driven by a JSON baseline spec.
- **Changed:** image references on `*-IMAGE` positionals now require a
  transport prefix (`docker-archive:` or `oci-archive:`). Bare paths
  error with a "Did you mean" hint.
- **Renamed:** global `--output text|json` → `--format text|json` (short
  `-o`) to eliminate collision with the old subcommand `--output-format`
  and with the positional OUTPUT slot.
- **Renamed:** subcommand `--output-format docker-archive|oci-archive|dir`
  → `--image-format` (scoped to `apply` / `unbundle`).
- **Added:** short flags `-q` (`--quiet`), `-v` (`--verbose`), `-n`
  (`--dry-run`).
- **Added:** Arguments section in `--help` output with per-arg purpose
  and accepted-transport list; error messages include usage line,
  copy-paste-ready example, and a `Run '<cmd> --help'` pointer.

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
- **`diffah import`**: `OUTPUT` positional argument is now a directory.
  Per-image output lands at `OUTPUT/<name>.tar` (archive formats) or
  `OUTPUT/<name>/` (`dir` format). Single-image bundles still use a
  per-image sub-entry — the default-mapped bundle-of-one produces
  `OUTPUT/default.tar` (or `OUTPUT/default/`).

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
