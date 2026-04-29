# Phase 5 — DX & Diagnostics Polish (Combined Design)

> Three independent deliverables in the production-readiness roadmap's
> Phase 5: a YAML config file (P5.2), `diffah doctor` expansion (P5.1),
> and richer `diffah inspect` output (P5.3). One design covers all three;
> three plans / PRs ship them in sequence.

## 1. Context

Phase 5 of the production-readiness roadmap (`docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md` §4 Phase 5) targets developer experience and diagnostics polish. Phases 1–4 are merged: structured logging, registry-native I/O, signing, scale-robust encoding, and apply-side correctness and resilience are all on master. The DX gaps users hit today:

- **Repeated flag noise.** CI invocations re-pass `--platform`, `--intra-layer`, `--authfile`, `--zstd-level`, `--workers`, `--candidates`, etc. on every `diff` / `bundle` / `apply` / `unbundle`. No project-level or user-level defaults exist.
- **Diagnostics gaps.** `diffah doctor` only checks zstd. Operators who hit a "delta failed to apply" can't ask the tool what's wrong with their environment without running a full apply.
- **Bundle introspection is shallow.** `diffah inspect` reports per-bundle stats (counts, sizes, intra-layer required) but no per-layer breakdown, no waste signals, no top-N summary. Hard to answer "did this delta actually buy us anything?" without tooling.

This spec combines the fixes for all three gaps. They are **independent subsystems** sharing only one weak coupling (doctor's `config` check exercises the parser shipped by P5.2). Each ships as its own PR.

## 2. Goals

- **G1.** Per-user / per-project config file (`~/.diffah/config.yaml` or `$DIFFAH_CONFIG`) supplies defaults for nine widely-repeated flags. CLI flags always override config.
- **G2.** Hard-fail on malformed config. CI must surface configuration bugs immediately, not silently fall back to built-in defaults.
- **G3.** `diffah doctor` runs five checks: zstd (existing), tmpdir, authfile, network (opt-in via `--probe`), and config. Three-level status (`ok` / `warn` / `fail`) maps onto exit codes per `errs.Category` taxonomy.
- **G4.** `diffah inspect` enriches its default output with per-layer encoding / size / ratio table, patch-oversized waste detection, top-10 savings list, and a 5-bucket size histogram. Backward-compatible: new content is additive; existing first-line shape and JSON keys are preserved.

## 3. Non-goals

- **No env-var-per-field.** `$DIFFAH_CONFIG` (path override) is the only env var. Per-field env vars (e.g., `$DIFFAH_PLATFORM`) are explicitly out of scope; viper's `AutomaticEnv` is not enabled. May be revisited in a future phase if demand surfaces.
- **No registry-cred probing in doctor.** The authfile check parses the file but does not actually authenticate against any registry. Cred liveness is `--probe`'s job (and only when explicitly opted in).
- **No baseline rescan in inspect.** Waste detection is sidecar-only. The "shipped full but should have been a patch against this baseline" class of waste requires a baseline image source, which is `diff --dry-run`'s territory.
- **No flag-driven inspect gating.** No `--detailed` / `--layers` / `--waste` toggles in v1; the enriched view is always-on. Backward compat is preserved via additive output.
- **No multi-source layered config.** No project-local `./diffah.yaml` merged over user-global; one file per invocation. May be added later under explicit demand.

## 4. Architecture

```
                   ┌─────────────────────────────────┐
                   │      cmd/  (cobra commands)     │
                   ├─────────────────────────────────┤
                   │ diff / bundle / apply / unbundle│
                   │   ↑   reads defaults from       │
                   │   │   pkg/config.ApplyTo(flags) │
                   │   │                             │
                   │ doctor   ←  pkg/config.Validate │
                   │ inspect  ←  pkg/importer.       │
                   │              InspectImageDetail │
                   │ config show / init / validate   │
                   └─────────────────────────────────┘
                                     │
                                     ▼
                   ┌─────────────────────────────────┐
                   │       pkg/config/               │
                   ├─────────────────────────────────┤
                   │  Config struct (9 fields)       │
                   │  Load(path string) (*Config,    │
                   │       error)                    │
                   │  Validate(path string) error    │
                   │  defaults                       │
                   │  ApplyTo(flagSet, *Config)      │
                   │                                 │
                   │  backed by spf13/viper for      │
                   │  YAML/TOML/JSON auto-detection  │
                   └─────────────────────────────────┘
```

Three new packages / modules:

- **`pkg/config/`** — schema + loader + cobra integration helper.
- **`cmd/doctor_checks.go`** — four new `Check` implementations alongside existing `zstdCheck`.
- **`pkg/importer/inspect_data.go`** — pure function deriving `InspectImageDetail` from sidecar + target-manifest bytes. `cmd/inspect_render.go` renders text / JSON.

## 5. P5.2 Config file

### 5.1 Schema (v1, 9 fields)

```yaml
# ~/.diffah/config.yaml
platform: linux/amd64        # diff, bundle
intra-layer: auto             # diff, bundle  (auto|off|required)
authfile: /path/to/auth.json  # diff, bundle, apply, unbundle
retry-times: 3                # apply, unbundle
retry-delay: 2s               # apply, unbundle  (Go duration string)
zstd-level: 22                # diff, bundle  (1..22)
zstd-window-log: auto         # diff, bundle  (auto | 10..31)
workers: 8                    # diff, bundle
candidates: 3                 # diff, bundle
```

A field present in the config but irrelevant to the running command is silently ignored (e.g., `retry-times` on `diff`). A field absent from the config falls back to its built-in default.

### 5.2 Lookup chain

```
Path resolution (first match wins):
  1. $DIFFAH_CONFIG  (env, must be absolute path)
  2. ~/.diffah/config.yaml
  3. (no file → built-in defaults)

Value resolution per field (most → least specific):
  1. CLI flag (only when explicitly set on the command line)
  2. config file value (if present)
  3. built-in default
```

`viper.IsSet(flagName)` distinguishes "user passed `--platform`" from "Cobra populated the flag's default."

### 5.3 Parse-error behavior

- **File does not exist** → silently use built-in defaults. Not an error.
- **File exists but malformed** (bad YAML, unknown field, type mismatch) → **hard fail**, write to stderr `config: <path>: <viper error including line:col>`, exit code 2 (`errs.CategoryUser`).
- Unknown fields trigger an error listing the valid field names.

### 5.4 Helper subcommands

```
diffah config show              # print resolved config (yaml default; --output json)
diffah config init [PATH]       # write a template at PATH (default: ~/.diffah/config.yaml)
                                #   — refuses to overwrite an existing file unless --force
diffah config validate [PATH]   # validate a single file path; exits 0 / 2
                                #   — shares Validate() with doctor's `config` check
```

`config show` resolves the same precedence chain as a real run; the output reflects what `diff` / `apply` / etc. would actually use.

### 5.5 Cobra integration

Each command's flag-installer (`installRegistryFlags`, `installEncodingFlags`, etc.) is followed by a single `config.ApplyTo(cmd.Flags(), cfg)` call in `PersistentPreRunE`. `ApplyTo` walks the flag set, looks up each known field name, and — only when the flag has not been explicitly set by the user — overwrites its `DefValue` from the config struct. This relies on cobra's `Flag.Changed` accessor, not viper's per-flag binding, to keep the integration narrow and easy to reason about.

The viper instance is scoped to the program lifetime (one `viper.Viper` per process); we do not call `AutomaticEnv()`.

### 5.6 Package layout

```
pkg/config/
  config.go         # Config struct + per-field tags
  load.go           # Load(path string) (*Config, error)
  validate.go       # Validate(path string) error
  defaults.go       # Built-in default values (single source of truth)
  apply.go          # ApplyTo(flags *pflag.FlagSet, cfg *Config)
  config_test.go
  load_test.go
  validate_test.go
  apply_test.go

cmd/
  config.go              # `diffah config` parent + subcommand registration
  config_show.go
  config_init.go
  config_validate.go
  config_test.go         # cmd-level integration (text + json output)
```

### 5.7 Error categorization

All `pkg/config` errors implement `errs.Categorized → CategoryUser` (exit 2). `cmd.Execute` already maps that to exit 2 via the existing `errs.Categorized` interface check.

## 6. P5.1 Doctor expansion

### 6.1 Check set

| Name | Status conditions |
|---|---|
| `zstd` (existing) | **OK**: binary found + version ≥ 1.5. **Fail**: binary missing or version too old. |
| `tmpdir` | **OK**: `$TMPDIR` (or `os.TempDir()` if unset) accepts a 1 KiB write-then-delete probe. **Fail**: any I/O error, ENOSPC, EACCES, etc. |
| `authfile` | **OK**: lookup chain resolves to a file that exists, is readable, parses as JSON, and contains an `auths` map. **Warn**: no file found (anonymous pulls only — still works). **Fail**: file exists but JSON is malformed or `auths` is missing. |
| `network` | **Skip** (status `ok`, detail `"--probe not supplied; check skipped"`) when `--probe` is absent. **OK**: `GetManifest` against the supplied ref succeeds. **Fail**: error from `GetManifest` classified through `diff.ClassifyRegistryErr` (DNS / TCP timeout / TLS / 401 / 5xx / etc.). |
| `config` | **OK**: file does not exist (defaults are used) OR file exists and `pkg/config.Validate` returns nil. **Fail**: file exists and `Validate` returns an error. |

### 6.2 authfile lookup chain

Walked in order; first existing file wins. Status reports the resolved path:

```
1. $REGISTRY_AUTH_FILE
2. $XDG_RUNTIME_DIR/containers/auth.json
3. $HOME/.docker/config.json
```

Detail strings:
- OK: `"resolved: /path/to/auth.json (3 registries configured)"`
- Warn: `"no authfile found in lookup chain; anonymous pulls only"`
- Fail: `"resolved: /path/to/auth.json — JSON parse error: <details>"`

### 6.3 `--probe` semantics

```
diffah doctor --probe docker://registry.example.com/foo:latest
```

Implementation:

```go
ref, err := imageio.ParseReference(probe)              // existing parser
src, err := ref.NewImageSource(ctx, sysctx)            // honors --tls-verify
defer src.Close()
_, _, err = src.GetManifest(ctx, nil)                  // single round-trip
```

`err` (if any) is wrapped via `diff.ClassifyRegistryErr` so Detail strings stay consistent with the rest of the importer's error vocabulary.

### 6.4 Exit-code mapping

```go
func runDoctor(...) error {
    results := runChecks(...)
    renderResults(results)
    for _, r := range results {
        if r.Status == statusFail {
            return errEnvironmentCheckFailed       // CategoryEnvironment → exit 3
        }
    }
    return nil                                     // exit 0
}
```

`statusWarn` does not change the exit code (advisory). The existing `--output json` mode preserves the same exit codes.

### 6.5 File layout

```
cmd/doctor.go                       # existing — extends defaultChecks() to return 5
cmd/doctor_checks.go                # new — 4 new Check implementations
cmd/doctor_test.go                  # existing — adds unit cases per check
cmd/doctor_integration_test.go      # new — registry-backed --probe path against in-process registrytest
```

The `Check` interface (`Name()`, `Run(ctx) CheckResult`) does not change. Three-level status is already encoded in `statusOK / statusWarn / statusFail` constants.

## 7. P5.3 Inspect enrichment

### 7.1 New text sections

Per-image, appended after the existing image header:

```
Image: <name>
  Manifest: <digest>
  ...existing per-image lines...

  Layers (target manifest order):
    [F]  sha256:xxx... 12.4 MiB target / 12.4 MiB archive  (1.00× — full)
    [P]  sha256:yyy...  8.0 MiB target /  0.5 MiB archive  (0.06× — patch from sha256:zzz...)
    [B]  sha256:www...  5.1 MiB target /     0 B archive  (— baseline-only)

  Waste:
    none                                           # or list, see §7.3

  Top savings (5/10):                              # min(N, layer-count)
    1. sha256:xxx... saved 7.5 MiB (94 %)
    2. ...

  Layer-size histogram (target bytes):
    < 1 MiB        │██░░░░░░░░░░  2
    1–10 MiB       │██████░░░░░░  6
    10–100 MiB     │████░░░░░░░░  3
    100 MiB–1 GiB  │░░░░░░░░░░░░  0
    ≥ 1 GiB        │░░░░░░░░░░░░  0
```

### 7.2 Per-layer fields (sidecar-derivable)

| Field | Computation |
|---|---|
| Tag | `[F]` Full / `[P]` Patch / `[B]` baseline-only-reuse |
| `target_size` | `LayerRef.Size` from target manifest (already parsed by `pkg/importer/manifest.go`) |
| `archive_size` | `BlobEntry.ArchiveSize` from sidecar (0 for baseline-only) |
| `ratio` | `archive_size / target_size`; baseline-only → not displayed |
| `saved` | `target_size - archive_size`; baseline-only → `target_size` |
| `patch_from` | `BlobEntry.PatchFromDigest` for Patch encoding; empty otherwise |

### 7.3 Waste detection (sidecar-only)

One category in v1: `patch_oversized` — any blob with `Encoding == EncodingPatch && ArchiveSize >= LayerRef.Size`. Output:

```
Waste:
  patch-oversized  sha256:yyy... archive 12 MiB ≥ target 8 MiB
                   (patch is bigger than full; force --intra-layer=off for this layer)
```

Zero waste → `none`.

### 7.4 Top-N savings

Fixed `N = 10`. Sorted by `saved_bytes` desc; ties broken by digest lexicographic. Layers with `saved_bytes == 0` (full encoding where archive == target, the no-savings case) are omitted from the list. Display: rank, digest, saved bytes, saved percent.

### 7.5 Histogram

Five fixed log-scale buckets keyed on `target_size`. Bucket boundaries are half-open intervals `[low, high)`:

| Bucket label (display) | Interval (bytes) |
|---|---|
| `< 1 MiB` | `[0, 1 MiB)` |
| `1–10 MiB` | `[1 MiB, 10 MiB)` |
| `10–100 MiB` | `[10 MiB, 100 MiB)` |
| `100 MiB–1 GiB` | `[100 MiB, 1 GiB)` |
| `≥ 1 GiB` | `[1 GiB, ∞)` |

Each `target_size` lands in exactly one bucket. Bar width: max 12 characters; scale = `floor(12 × count / max(counts))` over the buckets of the same image. Uses U+2588 (`█`) for filled, U+2591 (`░`) for empty.

### 7.6 `--output json` schema

Existing fields are preserved. Per image, four new keys are added:

```json
{
  "name": "...",
  "manifest_digest": "sha256:...",
  "layer_count": 3,
  "archive_layer_count": 2,
  "layers": [
    {
      "digest": "sha256:xxx",
      "encoding": "full",
      "target_size": 13000000,
      "archive_size": 13000000,
      "ratio": 1.0,
      "saved_bytes": 0,
      "patch_from": ""
    },
    {
      "digest": "sha256:yyy",
      "encoding": "patch",
      "target_size": 8000000,
      "archive_size": 500000,
      "ratio": 0.0625,
      "saved_bytes": 7500000,
      "patch_from": "sha256:zzz..."
    },
    {
      "digest": "sha256:www",
      "encoding": "baseline_only",
      "target_size": 5000000,
      "archive_size": 0,
      "saved_bytes": 5000000
    }
  ],
  "waste": [
    {
      "kind": "patch_oversized",
      "digest": "sha256:yyy",
      "archive_size": 12000000,
      "target_size": 8000000
    }
  ],
  "top_savings": [
    {
      "digest": "sha256:xxx",
      "saved_bytes": 7500000,
      "saved_ratio": 0.94
    }
  ],
  "size_histogram": {
    "buckets": ["<1MiB", "1-10MiB", "10-100MiB", "100MiB-1GiB", ">=1GiB"],
    "counts":  [2, 6, 3, 0, 0]
  }
}
```

Old consumers ignore unknown keys; no field is removed or retyped.

### 7.7 Package layout

```
pkg/importer/inspect_data.go        # new: pure function (sidecar, image-entry, manifest bytes) → InspectImageDetail
pkg/importer/inspect_data_test.go   # new: exhaustive table-driven coverage of layer/waste/top/histogram derivation

cmd/inspect.go                      # existing — extends per-image rendering; preserves prefix lines
cmd/inspect_render.go               # new: renderLayerTable / renderWaste / renderTopN / renderHistogram + JSON builder
cmd/inspect_test.go                 # existing — adds cases against synthesized + fixture-derived sidecars
```

`InspectImageDetail` is the single struct that both text and JSON renderers consume. The text renderer formats it; the JSON renderer marshals it directly.

## 8. Testing strategy

### 8.1 Per-PR unit coverage

- **P5.2.** `pkg/config/{load,validate,apply}_test.go` cover: missing file (defaults), valid YAML, malformed YAML (line-numbered error), unknown field, type mismatch, duration parsing, `intra-layer` / `zstd-window-log` enum-style strings. `cmd/config_*_test.go` exercises `show` / `init` / `validate` end-to-end with `t.TempDir`.
- **P5.1.** `cmd/doctor_test.go` table-drives each new check against fixture inputs (synthetic authfile JSON, controlled `$TMPDIR`, in-memory config). `cmd/doctor_integration_test.go` exercises `--probe` against `internal/registrytest`'s in-process registry.
- **P5.3.** `pkg/importer/inspect_data_test.go` table-drives derivation of layer/waste/top/histogram from synthesized sidecars (no I/O). `cmd/inspect_test.go` asserts text + JSON shapes.

### 8.2 Integration coverage

- `cmd/config_integration_test.go` (build tag `integration`): writes `~/.diffah/config.yaml` to `t.TempDir`, sets `$DIFFAH_CONFIG`, runs `diffah diff --dry-run` against the v1/v2 fixture pair, asserts the dry-run output reflects config-supplied defaults (e.g., `--workers=4` from config, `--zstd-level=12` from config).
- `cmd/doctor_integration_test.go`: drives doctor against various authfile / `$TMPDIR` states + `--probe` against `internal/registrytest` (success, 401, network-down).

### 8.3 No-regression checks

- `cmd/inspect_test.go` keeps an existing assertion that the first three lines of output are the legacy `feature` / `version` / `tool` block — proves backward compat for grep-based scripts.
- JSON shape tests assert the existing `feature` / `version` / `images[*].layer_count` / etc. fields are still present and unchanged.

## 9. Backward compatibility

- **Config (P5.2).** Brand new file; absent configs are not an error. No existing CLI behavior changes when no config file is present.
- **Doctor (P5.1).** Existing `zstd` check semantics are preserved; new checks are additive. Exit code goes from "always 0 on success" to "0 unless any check fails"; this is a deliberate strengthening (spec §2 G3) and CHANGELOG-noted.
- **Inspect (P5.3).** Text output preserves all existing prefix lines; new sections are appended **per-image** at the bottom of each image block. JSON adds keys; old keys preserved. Old grep / jq scripts continue to work.

## 10. PR strategy

Three PRs in this order:

1. **PR-1: P5.2 config foundation.** `pkg/config/` package + `cmd/config show / init / validate` + cobra integration into `diff` / `bundle` / `apply` / `unbundle`. Hard-fail behavior for malformed configs. Title: `feat(config): YAML config file (Phase 5.2)`.
2. **PR-2: P5.1 doctor expansion.** Four new checks + exit-code mapping + integration test against `registrytest`. Hard-dependent on PR-1 — uses `pkg/config.Validate` for the `config` check. Must merge after PR-1. Title: `feat(doctor): authfile / tmpdir / network / config checks (Phase 5.1)`.
3. **PR-3: P5.3 inspect enrichment.** `pkg/importer/inspect_data.go` + cmd renderers + JSON-shape additions. Independent of PR-1 / PR-2. Title: `feat(inspect): per-layer table, waste, top-N, histogram (Phase 5.3)`.

CHANGELOG entries land in their respective PRs under a single `[Unreleased] — Phase 5: DX & diagnostics polish` section.

## 11. Out of scope (explicit anti-goals)

- Per-field environment variables (covered by §3 Non-goals).
- Layered config (project-local merged over user-global).
- `diffah doctor` running registry-cred liveness checks beyond `--probe`.
- Inspect waste detection that requires a baseline image source.
- Inspect flag gating (`--detailed`, `--layers`, `--waste`, `--histogram`).
- Homebrew tap publishing (P5.4 — separate repository work).
- Multi-arch Docker image publishing verification (P5.5 — separate, mostly already configured in `.goreleaser.yaml`).
- Recipes cookbook (P5.6 — pure documentation work, separate PR).

## 12. Open questions

None at design time. All decisions settled in brainstorming session 2026-04-29.
