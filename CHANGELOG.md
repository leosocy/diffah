# Changelog

## [Unreleased] — Phase 5: DX & diagnostics polish

### Additions

- **Config file** (`~/.diffah/config.yaml` or `$DIFFAH_CONFIG`) supplies
  defaults for nine flags: `--platform`, `--intra-layer`, `--authfile`,
  `--retry-times`, `--retry-delay`, `--zstd-level`, `--zstd-window-log`,
  `--workers`, `--candidates`. CLI flags always override config; absent
  config = built-in defaults; malformed config = exit 2 with the offending
  file path and the parse error.
- New helper subcommands:
  - `diffah config show` — print the resolved config (yaml; `--format=json` for JSON).
  - `diffah config init [PATH] [--force]` — write a template.
  - `diffah config validate [PATH]` — validate a single file.

### Behavior changes

- `diff` / `bundle` / `apply` / `unbundle` flag defaults now come from the
  resolved config when no flag is set on the command line. With no config
  file present, behavior is unchanged.

### Backward compatibility

- No change for users who don't create a config file. Existing CI scripts
  with explicit flags keep their explicit values.

## [Unreleased] — Apply correctness & resilience (Track A)

### Behavior changes

- **`--strict` semantic widens.** In addition to the existing "baseline
  spec missing for image-X is an error," `--strict` now also rejects
  baselines that are present but incomplete (missing patch sources or
  missing reuse layers needed by the delta). Without `--strict` (default
  partial mode), affected images are skipped and a final summary lists
  them; the run still exits 0 if at least one image succeeded.

### New invariants

- Every successful `diffah apply` / `unbundle` re-reads the destination
  manifest and proves the layer set matches the sidecar's expectation.
  Failures produce exit 4 with explicit Missing/Unexpected diagnostics.

### Categorized errors

- B1 (`ErrMissingPatchSource`) and B2 (`ErrMissingBaselineReuseLayer`)
  surface from `apply` / `unbundle` with actionable hints. Auth, TLS,
  network, and timeout errors retain their existing classification.

### Performance

- Pre-flight is built into apply; failure modes for incomplete baselines
  are now detected before the first layer body is fetched. Pre-flight
  shares the baseline manifest fetch with apply (≤ 2 GETs per baseline
  across the full pipeline).

## [Unreleased] — Phase 4: Delta quality & throughput

### Behavior changes

- **Default zstd level: `3 → 22`.** `diff` and `bundle` produce
  noticeably smaller patches by default. Level 22 ('ultra') is
  significantly slower per layer than the historical level 3 — the
  cost is largely absorbed by the new `--workers=8` parallelism on
  hosts with multiple cores. Operators wanting Phase-3 output speed
  can pin `--zstd-level=3 --candidates=1 --workers=1 --zstd-window-log=27`
  to reproduce the historical behavior.
- **Default zstd window: `--long=27 → auto`.** The producer now picks
  per-layer: ≤128 MiB → 27, ≤1 GiB → 30, >1 GiB → 31. Smaller patches
  on multi-GiB layers at the cost of encoder memory
  (≈ 2 × 2^N bytes per running encode).
- **Default top-K: `1 → 3`.** Each shipped target layer is patched
  against the top-3 most content-similar baseline layers and the
  smallest patch wins.
- **Default workers: `1 → 8`.** Encode parallelism on the per-layer
  axis. Output bytes are byte-identical to `--workers=1` for a fixed
  flag tuple.

### Additions

- New flags on `diff` and `bundle`: `--workers`, `--candidates`,
  `--zstd-level`, `--zstd-window-log` (accepts `auto` or 10..31).
- Long-help (`diff --help`, `bundle --help`) now documents the
  encoding-tuning flags, the determinism guarantee, and the Phase-3
  override path.

### Backward compat

- Phase 4 archives encoded with `--zstd-window-log ≥ 28` cannot be
  decoded by Phase 3 or earlier importers — the decoder rejects them
  with `Frame requires too much memory for decoding`. Operators
  serving older consumers should pin `--zstd-window-log=27`.
- Phase 3 archives apply byte-identically through Phase 4 importer
  (decoder cap was raised, never lowered).
- Sidecar schema unchanged.

### Bug fixes

- **Importer baseline-blob dedup (Phase 2 Goal 4 regression).** `diffah apply` and `diffah unbundle` now fetch each distinct baseline blob digest at most once per invocation, regardless of how many shipped patches reference it or how many images in a multi-image bundle share it. Previously, a baseline layer used both as `PatchFromDigest` and as a baseline-only layer was fetched twice; in multi-image bundles, blobs shared across pairs were fetched once per pair. Backed by a per-`Import()` `singleflight`-coordinated cache mirroring the export-side `pkg/exporter/fpcache.go`. `bundleImageSource.HasThreadSafeGetBlob` now delegates to the underlying baseline source, unlocking parallel layer copy via `copy.Image` for `docker://` baselines.

### Internal

- New `pkg/exporter/workerpool.go` (errgroup-based bounded pool).
- New `pkg/exporter/fpcache.go` (singleflight-coordinated baseline
  byte + fingerprint cache; each baseline blob fetched at most once
  per `Export()` call).
- New `ResolveWindowLog(userValue, layerSize)` helper threading
  per-layer window choice through `PlanShipped` and `PlanShippedTopK`.
- `internal/zstdpatch.Encode` and `EncodeFull` accept
  `EncodeOpts{Level, WindowLog}`; zero values reproduce historical
  defaults.
- Decoder side window cap raised from `1<<27` to `1<<31`.

### Deferred to a follow-up PR

- GB-scale benchmark gated on `DIFFAH_BIG_TEST=1` and the CI
  regression gate that consumes its output. The synthesized fixture
  needs registrytest helpers (`LayersFromRNG`, `MutateLayersBitFlip`)
  that don't yet exist; landing them under their own scoped PR keeps
  the defaults flip merge unit small.

## [Unreleased] — Phase 3: Registry-native export + signing

### Breaking changes

- **`BundleSpec` JSON**: `baseline` / `target` values must now carry a
  transport prefix. Bare-path values (`"baseline": "v1/svc.tar"`) fail
  with a migration hint. One-liner fix:

  ```
  sed -E -i '' 's|(\"baseline\"\|\"target\"): \"([^:\"]*\.tar[a-z]*)\"|\1: \"docker-archive:\2\"|g' bundle.json
  ```

### Additions

- **Registry sources on `diff` and `bundle`**: both verbs now accept
  `docker://`, `oci:`, `dir:`, and the archive transports on
  BASELINE-IMAGE / TARGET-IMAGE (and in BundleSpec values). The
  Phase 2 registry & transport flag block (`--authfile`, `--creds`,
  `--username`/`--password`, `--no-creds`, `--registry-token`,
  `--tls-verify`, `--cert-dir`, `--retry-times`, `--retry-delay`) is
  installed on both `diff` and `bundle`.
- **Signing on `diff` and `bundle`**: `--sign-key PATH` writes a
  cosign-compatible `.sig` sidecar next to the archive. Supports plain
  PEM and cosign-boxed (scrypt + nacl/secretbox) private keys.
  Passphrase via `--sign-key-password-stdin`.
- **Rekor transparency opt-in**: `--rekor-url URL` is registered on
  both verbs but Rekor upload is not yet implemented in this Phase 3
  slice; passing `--rekor-url` currently errors with a
  "not yet implemented" hint pointing at a follow-on PR.
- **Verification on `apply` and `unbundle`**: `--verify PATH`
  (ECDSA-P256 PEM public key) requires the archive's signature to
  match. Absent `--verify` preserves today's behavior — signed
  archives are processed byte-identically. When `--verify` is supplied
  and the archive is unsigned, exit code is 4 (content error).
- **Rekor proof verification**: `--verify-rekor-url URL` checks the
  Rekor inclusion proof when a `.rekor.json` sidecar is present.
  Missing `.rekor.json` only warns — it does not fail.
- **`docs/performance.md`** documents bandwidth and memory
  characteristics — chiefly that content-similarity matching reads
  (but does not retain) every baseline and target layer.

### Internal

- `pkg/exporter.Options` fields: `SystemContext *types.SystemContext`,
  `RetryTimes int`, `RetryDelay time.Duration`, `SignKeyPath string`,
  `SignKeyPassphrase []byte`, `RekorURL string` added.
- `pkg/exporter.Pair` renames `BaselinePath`/`TargetPath` →
  `BaselineRef`/`TargetRef`; values now carry a transport prefix.
- `planPair` swaps `imageio.OpenArchiveRef` for
  `alltransports.ParseImageName`.
- New `pkg/signer` package built on `github.com/gowebpki/jcs` +
  `golang.org/x/crypto/nacl/secretbox` + `golang.org/x/crypto/scrypt`.
  Public API: `Sign`, `Verify`, `WriteSidecars`, `LoadSidecars`,
  `JCSCanonical`, `JCSCanonicalFromBytes`, `ProbeKey`. Typed errors:
  `ErrKeyEncrypted`, `ErrKeyPassphraseIncorrect`,
  `ErrKeyUnsupportedKDF`, `ErrSignatureInvalid`, `ErrArchiveUnsigned`.
- New `cmd/sign_flags.go` (`installSigningFlags`) and
  `cmd/verify_flags.go` (`installVerifyFlags`).
- New bandwidth integration test in `cmd/bandwidth_integration_test.go`
  gates runaway per-blob fetch regressions.

### Non-goals / deferred

- **Keyless signing** (Fulcio / OIDC / ephemeral certs). `--keyless`
  and `cosign://` URIs are registered but return a
  "reserved but not yet implemented" error.
- **KMS signing** (`cosign://kms-uri` private-key references).
- **Inline-embedded signatures** (`--sign-inline`) — sidecars always
  land adjacent to the archive in Phase 3.
- **Attaching pre-existing x509 certs** (`--sign-cert`).
- **Rekor upload implementation** — the flag is wired but posting to
  Rekor is deferred to a follow-on PR.
- **Per-baseline-blob lazy-fetch on export** — `diff`/`bundle` today
  read every baseline layer for content-similarity fingerprinting
  (each blob hit a small constant number of times). Tightening to
  "exactly once" is Phase 4 scale work.
- The `docker-daemon:`, `containers-storage:`, `ostree:`, `sif:`,
  `tarball:` transports remain "reserved but not yet implemented."

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
