# diffah Production-Readiness Roadmap — Design

- **Status:** Draft
- **Date:** 2026-04-23
- **Author:** @leosocy
- **Supersedes/Extends:** the v2 feature track is complete; this document defines the hardening path from "feature-complete v2" to "production-ready."

## 1. Context

All four v2 feature tracks are merged to `master`:

| Track | Delivered | Merge |
|---|---|---|
| Phase 1 — intra-layer binary diff (zstd `--patch-from`) | PR #5 | `792fb4f` |
| Phase 2 I.a — content-similarity layer matching | PR #6 | `8af11b5` |
| Phase 2 II.a+II.b — multi-image bundles | PR #7 | `efa34e1` |
| Phase 2 Track A — zstd backend resilience | PR #8 | `ff6f739` |

What exists today: `diffah export`, `diffah import`, `diffah inspect`, `diffah version`; CI on ubuntu + macOS with race/cover + lint + integration + goreleaser; deterministic archives; `--dry-run` on both directions; `--intra-layer=auto|off|required` with zstd probe.

What is **not** production-ready yet:
- Blob pool accumulates in memory → OOMs on GB-scale layers.
- Logging is `fmt.Fprintf` to stderr — unparseable, no structure, no levels.
- Errors use `fmt.Errorf` strings — no category, no next-action hint for operators.
- Progress output is a single summary line — no per-layer feedback, no ETA.
- Baselines and outputs are local `.tar` files only — no registry transports.
- No signing or signature verification — trust boundary is unenforced.
- No `doctor` / preflight — "does this work here?" requires a full run.
- No config file — CI invocations balloon with repeated flags.
- Sidecar schema has no documented evolution / deprecation policy.

## 2. Goals — the "production-ready" bar

A release is production-ready when **all eight** criteria hold:

1. **Scale.** `diffah export` completes on a 4 GB-layer image within 8 GB RAM. Peak memory scales with concurrency, not with layer size.
2. **Observability.** Every user-visible operation emits:
   - *Machine layer:* structured (`slog`) logs; errors carry `{category, cause, next-action}`; `inspect`, `dry-run`, `doctor` support `--output json`.
   - *Human layer:* TTY-aware, per-layer progress bars (Docker-style) showing bytes/total, ratio, ETA, and current phase. Falls back to line-based summaries on non-TTY stderr; respects `NO_COLOR` and `CI=true`.
3. **Operability.** `diffah doctor` exits non-zero with actionable messages when the environment is broken. Exit codes: `0` success, `1` unknown/internal, `2` user error, `3` environment error, `4` content/schema error.
4. **Trust.** Export can sign deltas with cosign; import can verify when configured (`--verify`). Unsigned deltas still work (backward-compatible).
5. **Integration.** Source and sink accept `docker://`, `docker-archive:`, `oci:`, `oci-archive:`, `dir:` uniformly (read and write), via `go.podman.io/image/v5/transports/alltransports`.
6. **DX.** Install via `brew install leosocy/tap/diffah`, `docker run ghcr.io/leosocy/diffah`, or `go install`. A config file (`~/.diffah/config.yaml` or `$DIFFAH_CONFIG`) removes repeated-flag friction.
7. **Schema safety.** Sidecar schema version is explicitly negotiated; future versions don't silently corrupt older clients. Deprecation policy documented in `docs/compat.md`.
8. **Regression safety.** CI benchmark suite gates on memory + wall-time regressions per commit.

## 3. Non-goals (explicit anti-goals)

- No multi-tenant server mode or HTTP API — diffah stays a CLI.
- No GUI or TUI (beyond interactive progress bars on stderr).
- No runtime plugin system for transports, matchers, or signing backends. Extension via in-tree contribution only.
- No distributed or multi-machine execution.
- Opt-in flags stay opt-in, never default: `--strict`, `--allow-convert`, `--intra-layer=required`, `--verify`.
- No replacement of `go.podman.io/image/v5` — proven transport stack; don't reinvent.

## 4. Proposed roadmap — five phases

Phases are ordered so each one makes the next cheaper to build and safer to debug. Within a phase, items can land incrementally (one PR per item). Downstream phases may begin as soon as their in-prior-phase dependencies have merged — they are not gated on full phase completion. P1 is the only hard prerequisite (its artefacts are threaded into every later phase).

### Phase 1 — Observability & operability foundations

**Deliverables**
- `slog` adopted across `cmd`, `pkg/exporter`, `pkg/importer`, `pkg/diff`, `internal/*`. A single logger is constructed at `cmd.Execute` from `--log-level` and a `DIFFAH_LOG` env var; handlers default to text-on-TTY, JSON when `CI=true` or `--log-format=json`.
- Structured error type `diffah/errors.Error` with `{Category, Cause error, NextAction string}`. Categories map to exit codes (see Goal #3). User-facing error printing renders: `diffah: <category>: <message>\n  hint: <next-action>`.
- `--output json` on `inspect`, `dry-run`, and the new `doctor`. JSON schema committed under `pkg/diff/schemas/` and snapshot-tested.
- **TTY-aware, per-layer progress bars.** Renders Docker-style bars on an interactive stderr:
  ```
  Exporting bundle.tar
  svc-a:v2 Planning... done
  sha256:a1b2c3  Encoding [=====>        ]  45.2MB / 124.3MB  patch 38%  12MB/s  ETA 6s
  sha256:d4e5f6  Full     [=============>]  89.0MB / 89.0MB                             ✓
  sha256:789abc  Queued
  ```
  On non-TTY stderr: emits line-based phase summaries (`[export] plan done (5 pairs)`, `[export] encoded 3/12 blobs`). Respects `NO_COLOR=1` and `CI=true`.
- Exit-code taxonomy wired at `cmd.Execute` edge: pattern-match the returned error against the category set.
- `docs/compat.md` (new) documents schema evolution + exit-code guarantees.

**Scope out**
- No change to sidecar schema.
- No new commands other than `doctor` (which moves to Phase 5 polish if cost balloons; for P1 we deliver only the infrastructure).

**Phase exit criteria**
- `go test ./...` passes with `slog` output captured and asserted.
- `--output json` snapshots stable across runs.
- Progress bars manually verified on TTY + scripted CI run.

### Phase 2 — Registry-native import *(first concrete user-facing deliverable)*

**Deliverables**
- Adopt `go.podman.io/image/v5/transports/alltransports.ParseImageName` for baseline refs and output refs. Supported transports:
  - `docker://host/repo:tag`
  - `docker-archive:path[:name:tag]`
  - `oci:layout-dir[:reference]`
  - `oci-archive:archive.tar[:reference]`
  - `dir:path`
  - Bare paths keep today's sniffing behavior — backward-compatible for all existing invocations.
- Skopeo-parity `SystemContext` flag block (applied to every transport uniformly):

  | Flag | Behavior |
  |---|---|
  | `--authfile PATH` | Default `${XDG_RUNTIME_DIR}/containers/auth.json`, fallback `$HOME/.docker/config.json` |
  | `--creds USER[:PASS]` | One-shot inline credentials |
  | `--username`/`--password` | Split form |
  | `--no-creds` | Force anonymous |
  | `--registry-token TOKEN` | Bearer token |
  | `--tls-verify=<bool>` | Default `true` |
  | `--cert-dir PATH` | mTLS client certs (`*.crt`, `*.cert`, `*.key`) |
  | `--retry-times N` | Retry count (default `3`) |
  | `--retry-delay DURATION` | Fixed delay; omitted → exponential backoff |
- Output as registry reference: `diffah import ... BUNDLE docker://host/name:tag`. For multi-image bundles, the positional `OUTPUT` accepts a `%s` template (filled with image name) or `--output-spec FILE` maps per-image destinations explicitly.
- **Lazy baseline layer fetch.** For each `encoding=patch` shipped blob, only the one referenced baseline layer is fetched from the registry — not the entire baseline image. Manifest is always fetched.
- Progress bars (from P1) render pull/push per-layer.
- Error categories for registry failures: auth → user (exit 2), network/timeout/DNS → env (exit 3), manifest-not-found / schema mismatch → content (exit 4).

**Scope out**
- Signing/verification (Phase 3).
- Export-from-registry (Phase 3 — symmetric extension).
- Mirror lists or pull-through caching.
- `containers-storage:` transport (the Podman local store) — can land opportunistically if a user needs it.

**Phase exit criteria**
- Integration tests pass against an in-process `zot` or `registry:2` container for: anonymous pull, basic-auth pull, bearer-token pull, TLS pull with `--cert-dir`, registry push to a fresh tag, lazy-layer assertion (only referenced layers hit the wire).
- `diffah import --baseline app=docker://... ./bundle.tar docker://...` round-trips against a real public image on a manual test.

### Phase 3 — Registry-native export + signing

**Deliverables**
- `diffah export --pair NAME=docker://reg/app:v1,docker://reg/app:v2 OUT` — symmetric extension. Baseline and target references use the same `alltransports` parsing.
- Export-side bandwidth note: content-similarity matching requires tar-entry fingerprinting on every baseline layer, so every baseline layer must be *read* (not necessarily stored). Implementation streams the layer bytes through the tar reader without materializing them — memory stays bounded, but bandwidth equals the baseline's total layer size. This is fundamental to content-similarity matching and not a regression vs today.
- Pushing the diffah archive itself is unchanged (it's a local `.tar`, not an image — a bundle archive cannot be `docker://`-pushed; signing covers the trust gap instead).
- Signing via cosign (detached signature sidecar files next to the diffah bundle):
  - `--sign-key PATH` (sigstore-format private key) or `--sign-key cosign://KMS-URI`.
  - Three sidecar files colocated with the output, using cosign-standard suffixes (distinct from diffah's "bundle" term): `OUTPUT.sig` (base64 signature), `OUTPUT.cert` (Fulcio cert or empty for key-based signing), `OUTPUT.rekor.json` (Rekor transparency-log bundle — renamed from cosign's default `.bundle` to avoid nomenclature clash with the diffah bundle archive). Optional `--sign-inline` writes them into additional entries inside the `.tar` itself.
  - Signing covers the sidecar JSON digest encoded via [RFC 8785 JSON Canonicalization Scheme (JCS)](https://www.rfc-editor.org/rfc/rfc8785), not the whole tar (layer blobs are already content-addressed via SHA-256). Exact byte sequence fed to cosign: `sha256(jcs(sidecar.json))`.
- Import-side verification:
  - `--verify PATH` (public key) or `--verify cosign://...` (keyless via Rekor/Fulcio).
  - When `--verify` is set and the signature is missing or invalid → exit 4.
  - When `--verify` is absent, behavior unchanged (backward-compat).
- Progress bars include sign/verify phases.

**Scope out**
- Sigstore transparency log queries (Rekor search UI) — we only verify the inclusion proof, we don't search.
- Keyless signing in ephemeral CI environments — supported via `cosign://` but not the default path.

**Phase exit criteria**
- Round-trip: `export --sign-key k.priv` → archive carries sig → `import --verify k.pub` succeeds; `import --verify wrong.pub` exits 4 with category=content.
- Unsigned archive + `import` (no `--verify`) behaves identically to today.

### Phase 4 — Scale robustness

**Deliverables**
- Streaming export path. The `blobPool` in `pkg/exporter/pool.go` today accumulates the full encoded blob in-memory before writing. Replace with a streaming writer: blobs are encoded and flushed to the output `.tar` as soon as they're produced; the pool tracks sidecar metadata only (digest, size, encoding, refs). Memory budget is bounded by `concurrency × layer-buffer-size`, not by total corpus size.
- Parallel encode. A worker pool (bounded by `runtime.GOMAXPROCS(0)` or `--workers`) encodes independent layers concurrently. The archive writer is a single-writer serializer downstream of the pool (deterministic output preserved — writers sort by digest before emission).
- Streaming fingerprinting. `pkg/exporter.fingerprint` currently decompresses the whole layer for tar-entry enumeration. Refactor to stream through a `tar.Reader` over the decompressed layer without retaining the full expanded bytes.
- Document bounded-memory guarantee in `docs/performance.md` (new): `peak_RSS ≈ O(workers × max_layer_chunk)`, not `O(sum_of_all_layer_sizes)`.
- GB-scale CI benchmark. A synthesized fixture (deterministic RNG seeded in test) at 2 GB layer × 1 target × 1 baseline, gated by `DIFFAH_BIG_TEST=1` (skip by default locally; run nightly in CI via an additional job). Benchmark regression gates on `bytes_allocated` and wall-time — thresholds committed alongside the benchmark.

**Scope out**
- Resume / checkpoint for interrupted exports — deferred unless a real user reports an operationally-relevant issue.
- GPU/accelerator-specific optimizations.

**Phase exit criteria**
- 2 GB synthesized-fixture export runs in ≤ 8 GB RSS on CI runner.
- Bench harness runs on `push:master`, publishes results to a `benchmarks/` artifact; regression > 10 % fails the job.

### Phase 5 — DX & diagnostics polish

Each item here is independent — ship opportunistically, any order, any PR size.

**Deliverables (unordered)**
- `diffah doctor` command — preflight checks: zstd presence/version, authfile reachability, network reachability of a user-supplied `docker://` reference, writable output dir, config-file parseable. Structured output with `--output json`.
- Homebrew formula (tap `leosocy/tap`) — separate repo.
- Multi-arch Docker image — `ghcr.io/leosocy/diffah:<version>` and `:latest`. Built in `release.yml`. Image entrypoint is `diffah`.
- Config file at `~/.diffah/config.yaml` or `$DIFFAH_CONFIG`: default platform, default intra-layer mode, default authfile, default retry policy. Per-flag CLI values always override config.
- Richer `inspect`: per-layer ratio (patch/full/baseline-only), waste detection (layers shipped full that would've been smaller as patches or vice-versa), text histogram of layer sizes, top-N biggest-saving layers. All under `--output json` too.
- Cookbook `docs/recipes/*.md`: air-gapped customer delivery, nightly registry-to-registry mirror, CI-driven delta release, offline verification.

**Phase exit criteria**
- Each item lands independently with its own tests.
- `README.md` references the cookbook from a new "Recipes" section.

## 5. Detailed design — Phase 2 (the first phase we will spec → plan → execute)

Phase 2 is the first concrete user-facing deliverable after P1, and its surface is large enough to warrant a detailed outline in this roadmap so that the subsequent brainstorming session can proceed efficiently.

### 5.1 CLI surface changes

New flag block (added to `diffah import`; same set added to `diffah export` in Phase 3):

```
Registry & transport:
      --authfile PATH            path to authentication file
      --creds USER[:PASS]        inline credentials
      --username string          registry username
      --password string          registry password
      --no-creds                 access the registry anonymously
      --registry-token TOKEN     bearer token for registry access
      --tls-verify               require HTTPS + verify certificates (default true)
      --cert-dir PATH            client certificates directory
      --retry-times N            retry count (default 3)
      --retry-delay DURATION     fixed inter-retry delay (default: exponential)
```

CLI shape examples:

```bash
# Registry → registry, single image
diffah import \
  --baseline app=docker://harbor.example.com/app:v1 \
  --authfile ~/.config/containers/auth.json \
  ./bundle.tar \
  docker://harbor.example.com/app:v2

# Local OCI layout baseline → registry push
diffah import \
  --baseline app=oci:/srv/cache/app-v1 \
  ./bundle.tar \
  docker://harbor.example.com/app:v2

# Multi-image bundle with template destination
diffah import \
  --baseline svc-a=docker://harbor.example.com/svc-a:v1 \
  --baseline svc-b=docker://harbor.example.com/svc-b:v1 \
  ./bundle.tar \
  'docker://harbor.example.com/%s:v2'

# Multi-image with explicit per-image destinations
diffah import \
  --baseline-spec baselines.json \
  --output-spec outputs.json \
  ./bundle.tar
```

### 5.2 Code-level shape

- New `internal/imageio/ref.go` (or extend existing `reference.go`): `ParseRef(s string) (types.ImageReference, error)` — prefers `alltransports.ParseImageName` when a transport prefix is present; bare paths fall through to the existing OCI/Docker archive sniffer.
- `pkg/importer.Options` gains `SystemContext *types.SystemContext` plus `OutputTemplate string` (the `docker://reg/%s:v2` form) or `OutputSpec map[string]string`.
- `pkg/importer.resolveBaselines` switches from `map[string]string → openLocalArchive` to `map[string]types.ImageReference → openRef`. `openRef` handles lazy blob access via `types.ImageSource`.
- `pkg/importer.composeImage` fetches only the baseline layers it needs (one lookup per `encoding=patch` shipped blob plus the manifest + config). Uses `ImageSource.GetBlob(ctx, blobInfo, blobCache)` for layer streaming.
- `cmd/import.go` parses the new flag block into a `*types.SystemContext` via the `go.podman.io/image/v5/pkg/cli/pflags` helper (or hand-wired equivalent if upstream doesn't expose one cleanly).
- `buildOutputRef` (currently in `pkg/importer/importer.go`) is replaced by `alltransports.ParseImageName`-backed resolution + the `%s` template expansion for multi-image.

### 5.3 Backward compatibility

- Existing `diffah import --baseline app=./v1.tar bundle.tar ./out/` remains byte-identical. Bare paths still sniff. `OUTPUT` as a directory still writes `./out/app.tar`.
- `--output-format=<oci-archive|docker-archive|dir>` keeps its current meaning for path-style outputs. When `OUTPUT` carries an explicit transport prefix (`docker://`, `oci:`, etc.), `--output-format` is ignored (the transport is authoritative); an explicit `--output-format` combined with a transport-prefixed `OUTPUT` emits a warning.
- Sidecar schema unchanged (no schema version bump in Phase 2 — only the on-wire transport surface changes).
- `--dry-run` behavior unchanged; the `--output json` form gains a `resolved_transport` field (what transport each baseline/output was resolved to).

### 5.4 Phase 2 testing strategy

- Unit: `ParseRef` exhaustively tested on all five transports + bare-path fallback.
- Integration (gated by `integration` tag): spin up an in-process `zot` (preferred — pure Go, no container runtime needed) or fall back to a `registry:2` container via testcontainers. Cover: anonymous pull, basic-auth pull, bearer-token pull, TLS pull with `--cert-dir`, push to a fresh tag, push to an existing tag (overwrite), lazy-layer assertion (intercept HTTP and count layer-blob GETs — must equal the count of `encoding=patch` shipped blobs + 1 manifest + 1 config).
- Error taxonomy: each failure path returns a `diffah/errors.Error` with the expected category.

### 5.5 Risks

| Risk | Mitigation |
|---|---|
| `go.podman.io/image/v5/pkg/cli` API churn between versions | Pin dependency version in this phase's spec; vendor flag helpers if upstream removes them |
| Auth-file path precedence surprises on macOS (no `XDG_RUNTIME_DIR`) | Document fallback chain in both `--help` and `docs/` |
| Registry rate limiting on Docker Hub anonymous pulls | Phase 2 exits with category=env and a next-action hint pointing to `--authfile`; rate-limit retries covered by `--retry-delay` exponential backoff |
| Multi-image `%s` template collision with legitimate `%` in image names | Forbid `%` in image names at export time (already implicit via bundle-spec validation — verify); document explicitly |
| Lazy-layer fetch doesn't work against old Docker Registry v1 endpoints | Out of scope; v2 protocol is universal as of 2020+ |

## 6. Cross-cutting concerns

### 6.1 Testing strategy per phase

- **P1:** unit tests on `slog` adapter; snapshot tests on `--output json` schema; TTY-detection tests for progress bars (synthetic non-TTY writers); structured-error round-trip tests.
- **P2:** integration tests against an in-process `zot` or `registry:2`; fixture-based auth tests (success/failure/token/mTLS); streaming-blob tests verifying only referenced baseline layers are fetched.
- **P3:** cosign keypair fixtures (offline, committed); verify good-key accept + wrong-key reject + unsigned reject when `--verify` is set; round-trip test: export-signed → import-verified.
- **P4:** synthesized GB-scale fixture gated by `DIFFAH_BIG_TEST=1` (not committed, generated deterministically per test); bounded-memory assertion via `runtime.MemStats` delta checks; benchmark regression gates in CI.
- **P5:** smoke tests for every cookbook recipe in `docs/recipes/*.md` (simple shell-based assertions).

### 6.2 Schema evolution policy

Documented in `docs/compat.md` (deliverable of Phase 1):

- Sidecar `version` field is authoritative. Current value: `v1`.
- **Import-side negotiation:** `sidecar.version` greater than the maximum version the current binary knows → exit 4 (content/schema error) with a message of the form `this archive was produced by diffah ≥ vX.Y; you have vZ.Z — upgrade diffah to import`.
- **Forward-compat on read:** unknown *optional* fields are ignored with a debug-level log line. Unknown *required* fields cause a schema error.
- **Deprecation:** removing a CLI flag or a sidecar field requires one full minor-version cycle as deprecated (warning on every use), then removal in the next minor.
- Version bumps are driven by *sidecar schema* changes, not by CLI feature additions. CLI-only changes don't bump the schema version.

### 6.3 Phase independence rules

- Phases ship in order, but items within a phase land incrementally (one PR per item).
- A downstream phase may begin as soon as its dependencies in prior phases are merged — it is not gated on full phase completion.
- P1 is the only hard prerequisite: `slog`, structured errors, and progress-bar infrastructure are threaded into every later phase.

## 7. Open questions

These don't block the roadmap but should be resolved when each relevant phase is brainstormed in detail:

1. **P1:** should the error printer support `--error-format=json` as a top-level flag, or is `--output json` on specific commands sufficient? *(Current leaning: top-level flag for all commands, since errors happen everywhere.)*
2. **P2:** when `--output-spec` and positional `OUTPUT` are both provided, precedence order? *(Current leaning: `--output-spec` wins; positional becomes an error.)*
3. **P3:** should we support sigstore's "keyless" flow (Fulcio ephemeral certs) in CI environments, or require explicit keys? *(Current leaning: explicit keys by default; keyless behind `--keyless` opt-in.)*
4. **P4:** worker-pool default concurrency — `GOMAXPROCS` or something smaller (e.g., `min(GOMAXPROCS, 4)`) to avoid starving a shared CI host? *(Current leaning: `min(GOMAXPROCS, 4)` default, `--workers N` override.)*
5. **P5:** `diffah doctor` — is this a standalone P5 item, or does P1 already ship a minimal version? *(Current leaning: P1 ships the scaffolding + the zstd probe check; P5 adds network + authfile + writability checks.)*

## 8. Next steps

1. Review + approve this roadmap.
2. Brainstorm **Phase 1** in detail via `superpowers:brainstorming` → write its spec → plan → execute.
3. Brainstorm Phase 2 once P1 lands (or in parallel once P1 `slog`/errors infrastructure is merged).
4. Re-evaluate priorities at each phase boundary — if a real-world deployment exposes a ceiling earlier than expected, promote the relevant phase.
