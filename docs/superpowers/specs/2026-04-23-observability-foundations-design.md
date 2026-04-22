# diffah Phase 1 — Observability & Operability Foundations

- **Status:** Draft
- **Date:** 2026-04-23
- **Author:** @leosocy
- **Parent:** `docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md` (Phase 1 of the five-phase roadmap)
- **Scope:** structured logging, structured errors with exit-code taxonomy, machine-readable JSON output, TTY-aware per-layer progress bars, and the compatibility-policy doc that glues them together.

## 1. Motivation

Today diffah uses `fmt.Fprintf(os.Stderr, ...)` for all user-facing output: progress lines, warnings, errors. This is fine for a feature-development loop but insufficient for production:

- **No structure:** CI log parsers can't extract categories, phases, or per-layer events from free-form text.
- **No error classification:** `cmd.Execute` returns a bare `error` and always exits `1`. Operators can't tell user-input bugs apart from environment problems apart from bundle corruption.
- **No per-layer feedback:** multi-GB bundles export/import silently until the final "wrote foo.tar" line. Users doubt whether the tool is alive.
- **No install hints:** an error like `zstd binary missing` says so but doesn't tell the operator *what* to install.
- **No compatibility promise:** there's no documented contract for exit codes, sidecar schema evolution, or log/progress output — downstream automation has no stable surface.

Phase 1 lays observability and operability foundations that every subsequent phase (registry-native import, signing, scale, DX polish) can build on without re-solving these concerns.

## 2. Goals

1. **Every user-visible operation produces structured logs** via `log/slog`.
2. **Errors carry category + next-action hints** and map to a stable 0/1/2/3/4 exit-code taxonomy.
3. **`inspect` and `dry-run` support `--output json`** with a versioned schema; errors also render as JSON under that flag.
4. **Progress is Docker-style per-layer** on a TTY; gracefully degrades to line summaries on non-TTY and respects `NO_COLOR` / `CI`.
5. **A `docs/compat.md`** documents the exit-code contract, sidecar-schema evolution policy, and log/progress output stability guarantees.
6. **A `diffah doctor` scaffold** exists (just the command + the existing zstd probe); richer checks ship in Phase 5.

### Non-goals

- No new user-facing feature; every existing command behaves unchanged under default flags.
- No change to sidecar schema (version stays `v1`; Phase 1 documents the *policy*, not a version bump).
- No registry transports (Phase 2).
- No signing (Phase 3).
- No performance / scale work (Phase 4). Streaming, bounded memory, and parallelism are explicitly out.
- No new test fixtures. Reuse existing v1/v3/v4 archives and synthetic in-memory ones.

## 3. High-level architecture

Three concerns, three separate channels sharing one stream of domain events:

```
                ┌─────────────────────────────────────────────┐
                │   Domain events emitted by pkg/exporter,    │
                │   pkg/importer, pkg/diff, internal/*        │
                │   (PlanStarted, LayerEncoded, BlobWritten,  │
                │    ProbeResult, Warning, OperationFailed)   │
                └──────────┬──────────────────────────────────┘
                           │
       ┌───────────────────┼────────────────────┐
       ▼                   ▼                    ▼
   slog handler     progress.Reporter       return error ──► cmd.Execute
   (text|json      (mpb bars | line          (Categorized        │
    to stderr)      summaries |                + Advised)        │
                    discard)                                     ▼
                                                            exit code + hint
```

Key design choice: **progress is not an slog handler.** Mixing pretty multi-bar output with structured log records into a single handler corrupts both (mpb uses escape sequences; slog handlers are expected to emit line-structured records). Domain code emits to both channels independently; they are peer consumers of the same events.

## 4. Components

### 4.1 `pkg/diff/errs` — new package

Defines the category taxonomy, the extraction interfaces, and the classification helper:

```go
// pkg/diff/errs/category.go
package errs

type Category int

const (
    CategoryInternal    Category = iota // exit 1 — unknown/unclassified/panics
    CategoryUser                        // exit 2 — bad flags, missing inputs, misuse
    CategoryEnvironment                 // exit 3 — missing tools, network, auth, FS
    CategoryContent                     // exit 4 — schema/digest/corruption
)

func (c Category) ExitCode() int     { /* 1..4 */ }
func (c Category) String() string    { /* "internal"|"user"|"environment"|"content" */ }

// Categorized is implemented by error types that want an explicit category.
type Categorized interface{ Category() Category }

// Advised is implemented by error types that carry an install/next-action hint.
type Advised interface{ NextAction() string }

// Classify walks the error chain (errors.As) to extract the category and
// next-action hint. An error with no Categorized in its chain defaults to
// CategoryInternal. A default category+hint for env errors is consulted
// via CategoryOf(errors.Is-matched sentinels) as a fallback — this lets
// network/FS/OS errors (`net.OpError`, `fs.PathError`, `context.DeadlineExceeded`)
// classify as environment without requiring wrapper types everywhere.
func Classify(err error) (Category, string) { ... }
```

**Why the interface-first shape (approach C from brainstorm):** the codebase already has ~20 structured error types in `pkg/diff/errors.go`. A wrapper struct would force every return site to re-wrap; an interface lets each type opt in with a one-line method. For chain-wrapped errors (`fmt.Errorf("...: %w", cause)`), `errors.As` traverses naturally — the innermost classified error wins.

**Default classification fallbacks** (when no type in the chain implements `Categorized`):

| Sentinel match (`errors.Is`) | Category | Default hint |
|---|---|---|
| `context.Canceled`, `context.DeadlineExceeded` | Environment | "operation was cancelled or timed out" |
| `zstdpatch.ErrZstdBinaryMissing` | Environment | `"install zstd 1.5+ (brew install zstd / apt install zstd)"` |
| `zstdpatch.ErrZstdEncodeFailure` | Environment | `"zstd encode failed; re-run with --log-level=debug for details"` |
| `*net.OpError`, `*url.Error` | Environment | `"network error talking to registry; check connectivity and --authfile"` |
| `*fs.PathError`, `os.ErrPermission`, `os.ErrNotExist` | Environment | `"filesystem error: <path>"` |
| (any other) | Internal | `""` |

Every domain-specific type from `pkg/diff/errors.go` opts into an explicit category — fallbacks are a safety net for untyped wrapped errors (standard library, `podman.io/image`).

### 4.2 `pkg/diff/errors.go` — category methods on existing types

Each existing error type gains a one-liner:

```go
func (*ErrManifestListUnselected)       Category() errs.Category { return errs.CategoryUser }
func (*ErrManifestListUnselected)       NextAction() string      { return "pass --platform os/arch[/variant]" }

func (*ErrSidecarSchema)                Category() errs.Category { return errs.CategoryContent }
func (*ErrSidecarSchema)                NextAction() string      { return "archive may be corrupt or from an unsupported version" }

func (*ErrBaselineMissingBlob)          Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMissingBlob)          NextAction() string      { return "verify the --baseline value matches the baseline the delta was built against" }

func (*ErrIncompatibleOutputFormat)     Category() errs.Category { return errs.CategoryUser }
func (*ErrIncompatibleOutputFormat)     NextAction() string      { return "pass --allow-convert to accept digest drift, or pick a compatible --output-format" }

func (*ErrSourceManifestUnreadable)     Category() errs.Category { /* delegate via Unwrap chain; defaults Environment */ }

func (*ErrDigestMismatch)               Category() errs.Category { return errs.CategoryContent }
func (*ErrIntraLayerAssemblyMismatch)   Category() errs.Category { return errs.CategoryContent }
func (*ErrBaselineBlobDigestMismatch)   Category() errs.Category { return errs.CategoryContent }
func (*ErrShippedBlobDigestMismatch)    Category() errs.Category { return errs.CategoryContent }
func (*ErrPhase1Archive)                Category() errs.Category { return errs.CategoryContent }
func (*ErrUnknownBundleVersion)         Category() errs.Category { return errs.CategoryContent }
func (*ErrInvalidBundleFormat)          Category() errs.Category { return errs.CategoryContent }

func (*ErrBaselineMissingPatchRef)      Category() errs.Category { return errs.CategoryUser }
func (*ErrIntraLayerUnsupported)        Category() errs.Category { return errs.CategoryUser }
func (*ErrMultiImageNeedsNamedBaselines) Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineNameUnknown)          Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMismatch)             Category() errs.Category { return errs.CategoryUser }
func (*ErrBaselineMissing)              Category() errs.Category { return errs.CategoryUser }
func (*ErrInvalidBundleSpec)            Category() errs.Category { return errs.CategoryUser }
func (*ErrDuplicateBundleName)          Category() errs.Category { return errs.CategoryUser }
```

`NextAction()` added on those where a concrete hint helps (manifest list, baseline mismatch, output format conflict, bundle-spec syntax). Methods are tested via `TestCategory_ExhaustiveMapping` in `pkg/diff/errors_test.go` — a table-driven test that guarantees every type has a non-internal category.

### 4.3 `cmd/root.go` — `Execute` becomes the classification edge

```go
// cmd/root.go
func Execute(stderr io.Writer) int {
    err := rootCmd.Execute()
    if err == nil {
        return 0
    }
    cat, hint := errs.Classify(err)
    renderError(stderr, cat, err, hint, errorFormat /* "text"|"json" */)
    return cat.ExitCode()
}
```

`main.go` becomes `os.Exit(cmd.Execute(os.Stderr))`. (Current `main.go` just calls `cmd.Execute` and returns `err`; the diff is minimal.)

Text rendering:
```
diffah: environment: zstd not found on $PATH
  hint: install zstd 1.5+ (brew install zstd / apt install zstd)
```

JSON rendering (under `--output json`):
```json
{"schema_version":1,"error":{"category":"environment","message":"zstd not found on $PATH","next_action":"install zstd 1.5+ (brew install zstd / apt install zstd)"}}
```

### 4.4 `cmd/logger.go` — new file, slog bootstrap

```go
// cmd/logger.go
package cmd

import (
    "io"
    "log/slog"
    "os"
    "github.com/mattn/go-isatty"
)

func installLogger(stderr io.Writer, level, format string) *slog.Logger {
    opts := &slog.HandlerOptions{ Level: parseLevel(level) }
    h := pickHandler(stderr, format, opts)
    logger := slog.New(h)
    slog.SetDefault(logger)
    return logger
}

func pickHandler(w io.Writer, format string, opts *slog.HandlerOptions) slog.Handler {
    switch format {
    case "json":
        return slog.NewJSONHandler(w, opts)
    case "text":
        return slog.NewTextHandler(w, opts)
    case "", "auto":
        if isTTY(w) && os.Getenv("CI") != "true" {
            return slog.NewTextHandler(w, opts)
        }
        return slog.NewJSONHandler(w, opts)
    }
    // unknown: fall back to JSON as the safe default for machine consumers
    return slog.NewJSONHandler(w, opts)
}
```

The root command installs the logger in `PersistentPreRunE`. Flags:

- `--log-level=debug|info|warn|error` (default `info`; env `DIFFAH_LOG_LEVEL`)
- `--log-format=auto|text|json` (default `auto`; env `DIFFAH_LOG_FORMAT`)
- `--quiet` — shortcut; disables progress AND sets log level to `warn`
- `--verbose` — shortcut; sets log level to `debug`

### 4.5 slog wiring across packages

Each package (`pkg/exporter`, `pkg/importer`, `pkg/diff`, `internal/imageio`, `internal/archive`, `internal/oci`, `internal/zstdpatch`) gets a package-scoped logger:

```go
// pkg/exporter/exporter.go
var logger = slog.Default().With("component", "exporter")
```

Events to emit (examples, not exhaustive — full list enumerated in migration PR #3):

- `logger.Debug("plan pair", "name", p.Name, "baseline", p.BaselinePath, "target", p.TargetPath)`
- `logger.Info("exported bundle", "path", out, "images", nImages, "blobs", nBlobs, "archive_bytes", sz)`
- `logger.Warn("intra-layer auto: zstd unavailable, silently downgrading", "reason", reason)`
- `logger.Error("encode blob failed", "digest", d, "mode", mode, "err", err)`

**Existing `fmt.Fprintf(os.Stderr, ...)` calls are migrated wholesale** to slog equivalents. The `Progress io.Writer` field (see 4.6) takes over pretty-printed output.

### 4.6 `pkg/progress` — new package

```go
// pkg/progress/reporter.go
package progress

import "github.com/opencontainers/go-digest"

type Reporter interface {
    Phase(name string)                                                     // "planning", "encoding", "writing", "extracting", "composing"
    StartLayer(d digest.Digest, totalBytes int64, encoding string) Layer    // encoding = "full" | "patch"
    Finish()
}

type Layer interface {
    Written(n int64)
    Done()
    Fail(err error)
}

func NewDiscard() Reporter                                               // no-op, tests + --quiet
func NewLine(w io.Writer) Reporter                                       // phase/layer summary lines
func NewBars(w io.Writer) Reporter                                       // mpb multi-bar (TTY only)
func NewAuto(w io.Writer) Reporter                                       // picks NewBars/NewLine based on isatty + NO_COLOR + CI
```

`NewBars` uses `vbauerster/mpb/v8` (new dependency — justification: skopeo/containerd/buildah ecosystem standard, per brainstorm). One `mpb.Progress` per exporter/importer run; one bar per layer; decorators for `CountersKibiByte`, `EwmaETA`, `OnComplete` with a checkmark. Rendering is paused via `mpb.Progress.Pause`/`Resume` around synchronous slog flushes to avoid interleaved escape sequences on TTY.

`exporter.Options.Progress` changes type from `io.Writer` to `progress.Reporter`. A deprecation alias is kept for one minor release cycle:

```go
// Deprecated: use ProgressReporter. Will be removed in v0.4.
Progress io.Writer
// ProgressReporter supersedes Progress. Defaults to progress.NewDiscard().
ProgressReporter progress.Reporter
```

When both are set, `ProgressReporter` wins. When only `Progress` is set, it's wrapped via `progress.FromWriter(w)` (a `lineReporter` bound to the writer).

### 4.7 `--output json` on inspect and dry-run

Top-level persistent flag `--output=text|json` (default `text`). Bound on `rootCmd`.

**`diffah inspect`** current text output is reformatted as:
```json
{
  "schema_version": 1,
  "data": {
    "archive": "./bundle.tar",
    "version": "v1",
    "feature": "bundle",
    "tool": "diffah",
    "tool_version": "0.3.0",
    "platform": "linux/amd64",
    "requires_zstd": true,
    "images": [ { "name": "svc-a", "target": { "manifest_digest": "sha256:..." }, "baseline": { "manifest_digest": "sha256:..." }, "baseline_source": "svc-a-baseline" } ],
    "blobs": { "total": 5, "full_count": 4, "patch_count": 1, "full_bytes": 123, "patch_bytes": 456 },
    "patch_savings": { "bytes": 87654, "ratio": 0.415 },
    "total_archive_bytes": 123456
  }
}
```

**`diffah export --dry-run`** returns the existing `DryRunStats` as JSON under `data`.

**`diffah import --dry-run`** returns the existing `DryRunReport` as JSON under `data` (includes `requires_zstd` and `zstd_available`).

Snapshot tests in `cmd/testdata/schemas/`:
- `inspect.snap.json` — a canonical fixture archive's full JSON output
- `export-dryrun.snap.json`
- `import-dryrun.snap.json`
- `error.snap.json` — each category rendered as JSON

Volatile fields (`created_at`, `tool_version`) are normalized to fixed strings before comparison.

### 4.8 `docs/compat.md` — new file

Covers:
- **Exit-code taxonomy** — table of codes + category meanings + examples of errors that produce each.
- **Sidecar schema evolution** — versioning rules: `v1` current; `vN+1` readers must reject `vN` they don't know with a clear message; adding optional fields is non-breaking; removing/renaming/re-typing fields bumps the version; deprecation requires one minor-version cycle warning.
- **Log output stability** — `slog` keys are part of the contract; keys may be added non-breakingly; removing or renaming keys requires one cycle of deprecation. JSON log format is stable per `schema_version` at the top-level record.
- **Progress output stability** — progress is for humans, not machines; no stability guarantee for text/bar output. Machine consumers must use `--log-format=json` + structured slog events instead.

### 4.9 `diffah doctor` scaffold

Minimal Phase 1 surface — one check only (zstd). The scaffold's value in Phase 1 is proving the command wiring + `--output json` integration work end-to-end; the check matrix grows in Phase 5.

```
$ diffah doctor
zstd ........................................... ok (1.5.5 via /opt/homebrew/bin/zstd)
```

Under `--output json`:
```json
{"schema_version":1,"data":{"checks":[{"name":"zstd","status":"ok","detail":"1.5.5 via /opt/homebrew/bin/zstd"}]}}
```

On failure:
```
zstd ........................................... missing
  hint: install zstd 1.5+ (brew install zstd / apt install zstd)
```

Exits with category=Environment (code 3) when any check fails. Phase 5 adds network reachability, authfile parse, writable output dir, config-file parseable.

## 5. Migration plan — 10 PRs in dependency order

Each PR is independently merge-able; later PRs depend only on the artefacts of earlier ones landing on master.

| # | PR | Dependency |
|---|---|---|
| 1 | `pkg/diff/errs` package + `Category`/`Categorized`/`Advised` + `Classify` helper + unit tests | — |
| 2 | Add `Category()`/`NextAction()` on every `pkg/diff/errors.go` type; wire exit codes into `cmd.Execute`; add fallback classification for `zstdpatch` + stdlib sentinels; integration test asserting each error type emits the expected exit code in a real subprocess run | PR 1 |
| 3 | `cmd/logger.go` slog bootstrap; `--log-level`/`--log-format`/`--quiet`/`--verbose` flags; `PersistentPreRunE` installs default; replace `fmt.Fprintf` in `cmd/*` main flow | — |
| 4 | Thread `slog.Default().With("component", ...)` into `pkg/exporter`, `pkg/importer`, `pkg/diff`, `internal/archive`, `internal/imageio`, `internal/oci`, `internal/zstdpatch`; replace existing `fmt.Fprintf` with appropriate slog calls | PR 3 |
| 5 | `pkg/progress` package (interfaces + `discardReporter` + `lineReporter` + `FromWriter` adapter); `exporter.Options.ProgressReporter` added (deprecation alias retained) | — |
| 6 | `mpbReporter` + new `vbauerster/mpb/v8` dep; `--progress=auto|bars|lines|off` flag; `NewAuto` selector; pause/resume around slog flushes | PR 5 |
| 7 | `--output json` top-level flag; JSON renderer for `inspect`; JSON error formatter; snapshot tests for `inspect.snap.json` + `error.snap.json` | PR 2, PR 3 |
| 8 | `--output json` on `export --dry-run` + `import --dry-run`; snapshot tests | PR 7 |
| 9 | `docs/compat.md` — exit-code table, schema-version policy, slog key stability, progress stability disclaimer | PR 2, PR 4 |
| 10 | `diffah doctor` command scaffold with one check (zstd probe); `--output json` support via PR 7 infrastructure. Extensibility plumbing (a `Check` interface, a registry, a renderer) lands here so Phase 5 just adds more checks. | PR 7 |

**Ordering rationale:**
- PR 1 + 2 land the error taxonomy first; every subsequent PR's tests can assert on the right exit codes.
- PR 3 + 4 land slog before progress, so the exporter/importer can log domain events that progress later renders.
- PR 5 + 6 split progress into "interface + simple implementations" then "mpb renderer", so the dep on `mpb` lands cleanly in one PR.
- PR 7 is the first user-visible surface change (`--output json`); 8 extends it; 9 documents it.
- PR 10 ships the `doctor` stub after all infrastructure is present.

## 6. Testing strategy

### 6.1 Unit tests

- `pkg/diff/errs` — `Classify` table-test: every existing `pkg/diff/ErrX` type → its expected `Category`. `Classify(fmt.Errorf("wrap: %w", &ErrDigestMismatch{...}))` returns `CategoryContent`. Stdlib fallback tests for `context.DeadlineExceeded`, `*net.OpError`, `*fs.PathError`.
- `pkg/diff/errors_test.go` — `TestCategory_ExhaustiveMapping` asserts every type in `pkg/diff/errors.go` implements `Categorized` (walked via reflection over the `diff` package exported symbols; fails the build when a new error type is added without a category).
- `cmd/logger_test.go` — tests `pickHandler` with combinations of `format`, `isatty`, `CI` env.
- `pkg/progress/*_test.go` — `discardReporter` no-op test; `lineReporter` output shape test; `mpbReporter` TTY-detection test (synthetic non-TTY writer drops to line).

### 6.2 JSON snapshot tests

`cmd/testdata/schemas/` holds golden files (idiomatic Go `testdata/` location, not vendored as a Go package):
- `inspect.snap.json`
- `export-dryrun.snap.json`
- `import-dryrun.snap.json`
- `error-user.snap.json`, `error-env.snap.json`, `error-content.snap.json`, `error-internal.snap.json`

Test harness normalizes `created_at` → `"<T>"`, `tool_version` → `"<V>"` before comparing bytes. A `DIFFAH_UPDATE_SNAPSHOTS=1` env var regenerates snapshots rather than comparing.

### 6.3 Integration tests

`cmd/*_integration_test.go` (build tag `integration`):
- `TestExitCode_UserError_BadPair` — invokes `diffah export --pair malformed ./out.tar`; asserts exit code 2 + stderr JSON error with `category=user`.
- `TestExitCode_EnvError_MissingZstd` — invokes `diffah export --intra-layer=required --pair app=v1.tar,v2.tar ./out.tar` with `$PATH` scrubbed; asserts exit code 3 + `category=environment`.
- `TestExitCode_ContentError_UnknownSchema` — invokes `diffah import` against a forged sidecar with `version: "v999"`; asserts exit code 4.

### 6.4 What is NOT tested

- Visual correctness of `mpb` bars — we assert on non-TTY `lineReporter` output only. TTY rendering is manually validated during development.
- Terminal-specific escape-sequence encoding — trust `mpb`.
- Exact byte stability of `slog`'s JSON output across Go versions — we pin Go 1.25.4 in `.tool-versions`.

## 7. Risk register

| Risk | Mitigation |
|---|---|
| `mpb` + `slog` both write to stderr on TTY → interleaved escape sequences corrupt bars | `mpbReporter` wraps the slog handler with a `Pause`/`Resume` bracket. Default: on TTY, bar rendering pauses around every `slog.Info/Warn/Error` call and flushes bars to current positions. Non-TTY → `lineReporter` → no conflict. |
| Deprecating `Options.Progress io.Writer` breaks external Go consumers | Keep the field with a `Deprecated:` godoc comment; `buildBundle` wraps it via `progress.FromWriter`. Remove in v0.4 after `docs/compat.md` announces. |
| JSON snapshot drift from timestamp/version fields | Normalize volatile fields in the test setup before byte-comparing. |
| `errors.As` doesn't find a `Categorized` under deep wrapping | All top-level returns from `pkg/diff`, `pkg/exporter`, `pkg/importer` must either (a) return a typed error directly or (b) wrap with `fmt.Errorf("...: %w", err)` — `errors.As` walks `%w` chains. Reviewer checklist: any return that hides the cause via string concatenation is a bug. |
| Fallback classification for `*net.OpError` is too broad — swallows bugs that should be internal | Fallbacks are only consulted when **no** type in the chain implements `Categorized`. Every diffah-typed error goes through its explicit method first. |
| `vbauerster/mpb/v8` is a new dependency | Justification: skopeo uses it (`go.podman.io/image/v5` ecosystem standard); ~2k stars; actively maintained; no transitive deps beyond `mattn/go-runewidth` (single-module). Added to `go.mod` explicitly in PR 6. |
| `log/slog` requires Go 1.21+; we want to avoid accidentally pinning higher | `.tool-versions` already pins 1.25.4; `go.mod` gets `go 1.21` only (so old Go versions fail fast, but CI proves 1.25.4 works). |

## 8. Open questions (resolved inline later if still open at spec time)

1. **`--output json` vs `--format json`?** Current prevailing convention in Go CLIs varies. Skopeo uses `--format` (Go template). kubectl uses `-o json`. Docker uses `--format`. *Resolution:* `--output json` for our case because `--format` in skopeo semantics is a Go template (which we don't want to support), and `--output` in diffah is not currently taken (export's `--output` was removed in the bundle schema migration — positional arg now).
2. **Persistent vs per-command `--output json`?** *Resolution:* persistent on root; takes effect on whichever commands render structured output. Commands that never emit data (like `version`) ignore it.
3. **Exit code for partial-import-skipped (non-`--strict`) case?** Today, when `--strict` is off and some baselines are missing, import exits `0` and logs which images were skipped. *Resolution:* preserve today's behavior (exit `0` when at least one image imports); slog records each skip at `warn` level; JSON output lists skipped images. Exit code reflects the *operation*, not the content.
4. **`--quiet` precedence over `--log-level`?** *Resolution:* `--quiet` wins. It's a stronger operator intent signal. Document it.

## 9. Out-of-scope for Phase 1 (intentional)

- Any change to the bundle schema or sidecar layout.
- `diffah doctor` checks beyond zstd (Phase 5).
- Metrics (OpenTelemetry, Prometheus exporters) — deferred indefinitely unless a real operator asks.
- Log rotation / file sinks — `slog` goes to stderr; redirection is the operator's responsibility.
- Localization of error messages.
- Streaming / parallel / bounded-memory (Phase 4).
- Registry transports (Phase 2).
- Signing (Phase 3).

## 10. Acceptance criteria

Phase 1 is done when:

1. `go test ./...` + `-tags integration` pass.
2. Running `diffah export ...` on a TTY with `--progress=auto` shows per-layer mpb bars; on `| cat` it shows line summaries.
3. `diffah inspect ./bundle.tar --output json | jq -e '.schema_version == 1 and .data.images | length > 0'` succeeds.
4. Forcing each of the four error categories yields exit codes 1/2/3/4 respectively, with a clear next-action hint.
5. `docs/compat.md` exists and is linked from `README.md`.
6. `diffah doctor` runs and reports `ok` / `missing` for zstd.
7. `pkg/progress`, `pkg/diff/errs`, `cmd/logger.go` are present; existing `Options.Progress io.Writer` is marked `Deprecated:` but still works.

When all seven criteria above hold, Phase 1 is ready to merge. Phase 2 (registry-native import) can brainstorm in parallel once PR 4 (slog threaded through pkg/*) lands — it only needs the structured-error + slog contracts to be stable, not the whole Phase 1.
