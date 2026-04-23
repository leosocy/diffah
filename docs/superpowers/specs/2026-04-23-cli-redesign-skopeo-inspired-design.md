# diffah CLI Redesign — skopeo-inspired

- **Status:** Draft
- **Date:** 2026-04-23
- **Author:** @leosocy
- **Scope:** Full rewrite of the user-facing `diffah` CLI: command names, argument forms, image reference grammar, error/help UX, flag surface.
- **Non-scope:** No changes to `pkg/diff`, `pkg/exporter`, `pkg/importer`, `pkg/progress`, or the on-disk sidecar schema. This is a CLI-only refactor; all library behavior is preserved.

## 1. Motivation

The current CLI is unusable in practice. Two concrete failure modes from the field:

1. **Silent failure on missing flag.** `diffah import --baseline default=v1.tar --delta d.tar output.tar` in the shipped `dev` binary errored without visible output because the old CLI required `--output` as a flag and `output.tar` was a silently-rejected positional. Users had no way to recover.
2. **Composite-value flags.** `--pair NAME=BASELINE,TARGET` and `--baseline NAME=PATH` pack three pieces of data into a single string. For the common single-image case this forces users to invent a placeholder name (`default=...`). First-time users don't know `default` is a magic string and there's no hint in the help output.

Additionally:
- `export` / `import` collide with Docker vocabulary (`docker export` exports *containers*, `docker import` loads *filesystems*) — different operations.
- No image transport prefix means diffah can only address local tars; adding registry or daemon sources later would break the CLI again.
- Global `--output text|json` (rendering format) collides with positional `OUTPUT` (filesystem path) and `--output-format docker-archive|...` (archive format inside OUTPUT). One word, three meanings.
- Error messages don't guide users to a working command. Unknown flags don't suggest corrections.

The design goal: **make the CLI feel like skopeo** — explicit transports, positional subjects, symmetric verbs, helpful errors.

## 2. Goals

1. Every command has a clear verb and a short positional signature. No composite-value flags on the primary path.
2. Image references use a uniform `transport:path` grammar mirroring skopeo. Supported transports are documented in each command's help.
3. Multi-image bundle work is a separate subcommand (`bundle` / `unbundle`), driven by a spec file — no flag explosion in single-image path.
4. Every error tells the user *what failed*, *how to fix it*, and *where to look next* (with a copy-paste-ready example).
5. Global flag names are unambiguous: no name reused across positional / flag / value layers.
6. Old `export` / `import` are removed; invoking them yields a targeted error pointing at the replacement command.

### Non-goals

- No new transports in this iteration. Only the transports the current implementation already supports (`docker-archive`, `oci-archive`, `oci`, `dir`) are reachable. Future `docker://` and `docker-daemon:` additions are out-of-scope but the grammar reserves space for them.
- No new library capability. This is a CLI-only refactor. If the existing library can't satisfy a transport, the CLI reports an `unsupported transport` error with a pointer to future-phase tracking.
- No deprecation alias period. Pre-`v0.1.0` project; hard rename is documented in CHANGELOG.

## 3. Command surface

### 3.1 Single-image: `diff` / `apply`

```
diffah diff  BASELINE-IMAGE TARGET-IMAGE DELTA-OUT
diffah apply DELTA-IN       BASELINE-IMAGE TARGET-OUT
```

- `diff`: compute a delta archive from `BASELINE-IMAGE` to `TARGET-IMAGE`; write to `DELTA-OUT`.
- `apply`: reconstruct `TARGET-OUT` by applying `DELTA-IN` on top of `BASELINE-IMAGE`.
- `BASELINE-IMAGE` / `TARGET-IMAGE` use the transport grammar (§4). `DELTA-OUT` / `DELTA-IN` / `TARGET-OUT` are plain filesystem paths (the delta archive format is diffah's own; the reconstructed image format is the `--image-format` flag).

### 3.2 Multi-image: `bundle` / `unbundle`

```
diffah bundle   BUNDLE-SPEC  DELTA-OUT
diffah unbundle DELTA-IN     BASELINE-SPEC  OUTPUT-DIR
```

- `bundle`: read `BUNDLE-SPEC` (JSON; same schema as today's `--bundle FILE`); produce a multi-image delta archive at `DELTA-OUT`.
- `unbundle`: apply `DELTA-IN` using `BASELINE-SPEC` (JSON; same schema as today's `--baseline-spec FILE`); per-image reconstructed images land under `OUTPUT-DIR/<name>.tar` (or `<name>/` for `dir` image-format).
- Spec schemas are the existing `pkg/diff.BundleSpec` and `pkg/diff.BaselineSpec`. No wire-format changes.

### 3.3 Utility commands (unchanged verbs, touched help text only)

```
diffah inspect DELTA
diffah doctor
diffah version
```

`inspect` keeps its current positional. `doctor` and `version` keep their current zero-positional form.

### 3.4 Removed verbs

- `diffah export` → error (§6.3).
- `diffah import` → error (§6.3).
- `--pair NAME=BASELINE,TARGET` flag → removed; users move to `diff` (single-image) or `bundle` (spec file for multi-image).
- `--baseline NAME=PATH` flag → removed; same migration.

## 4. Image reference grammar

All `*-IMAGE` positionals use a **transport-prefixed** reference:

```
transport:path
```

### Supported transports (this iteration)

| Transport         | Syntax                     | What it points to                               |
|-------------------|----------------------------|-------------------------------------------------|
| `docker-archive`  | `docker-archive:PATH`      | Tar produced by `docker save`                   |
| `oci-archive`     | `oci-archive:PATH`         | Tar produced by `skopeo copy ... oci-archive:…` |
| `oci`             | `oci:PATH[:TAG]`           | OCI layout directory                            |
| `dir`             | `dir:PATH`                 | `skopeo copy ... dir:…` raw blob directory      |

### Reserved for future phases (CLI rejects with a pointer)

| Transport        | Status                                                |
|------------------|-------------------------------------------------------|
| `docker://`      | Reserved for registry-native transport (Phase 2).     |
| `docker-daemon:` | Reserved for local docker daemon transport (Phase 2). |

If a user supplies a reserved transport, the CLI produces:

```
Error: transport 'docker://' is reserved but not yet implemented.
Supported transports in this version:
  docker-archive:PATH
  oci-archive:PATH
  oci:PATH[:TAG]
  dir:PATH
Tracking: see CHANGELOG / roadmap for registry support timeline.
```

### Strictness

- **A bare path without a transport prefix is a hard error.** The CLI does not sniff file magic. This matches skopeo; it means the user's intent is always explicit.
- The transport list in errors is copy-paste ready.
- `inspect` takes a *delta archive path*, not an image reference — no transport prefix required or accepted.

## 5. Flag surface

### 5.1 Global flags (apply to all subcommands)

| Flag                | Short | Values                      | Meaning                                                              |
|---------------------|-------|-----------------------------|----------------------------------------------------------------------|
| `--format`          | `-o`  | `text` (default) \| `json`  | Rendering format for `inspect`, `dry-run`, `doctor`, error bodies.   |
| `--log-level`       |       | `debug\|info\|warn\|error`  | Unchanged.                                                           |
| `--log-format`      |       | `auto\|text\|json`          | Unchanged.                                                           |
| `--progress`        |       | `auto\|bars\|lines\|off`    | Unchanged.                                                           |
| `--quiet`           | `-q`  | (bool)                      | Unchanged; now also has short form.                                  |
| `--verbose`         | `-v`  | (bool)                      | Unchanged; now also has short form.                                  |

**Rename:** global `--output` → `--format` (short `-o`). Rationale: eliminates collision with positional `OUTPUT` and with subcommand `--image-format`. `-o` matches kubectl convention.

### 5.2 `diff` subcommand flags

| Flag             | Values                                  | Meaning                                                      |
|------------------|-----------------------------------------|--------------------------------------------------------------|
| `--platform`     | e.g. `linux/amd64` (default)            | Target platform for baseline/target manifest selection.      |
| `--compress`     | algorithm string                        | Pass-through to existing exporter.                           |
| `--intra-layer`  | `auto\|off\|required` (default `auto`)  | Pass-through to existing exporter.                           |
| `--dry-run`      | `-n`                                    | Plan without writing the delta.                              |

### 5.3 `apply` subcommand flags

| Flag              | Values                                 | Meaning                                                      |
|-------------------|----------------------------------------|--------------------------------------------------------------|
| `--image-format`  | `docker-archive\|oci-archive\|dir`     | Format of the reconstructed TARGET-OUT (renamed from `--output-format`). |
| `--allow-convert` | (bool)                                 | Same as today.                                               |
| `--dry-run`       | `-n`                                   | Verify baseline reachability only.                           |

### 5.4 `bundle` subcommand flags

Positionals: `BUNDLE-SPEC DELTA-OUT` (see §3.2). Flags:

| Flag             | Values                                  | Meaning                                                      |
|------------------|-----------------------------------------|--------------------------------------------------------------|
| `--platform`     | e.g. `linux/amd64` (default)            | Target platform for baseline/target manifest selection.      |
| `--compress`     | algorithm string                        | Pass-through to existing exporter.                           |
| `--intra-layer`  | `auto\|off\|required` (default `auto`)  | Pass-through to existing exporter.                           |
| `--dry-run`      | `-n`                                    | Plan without writing the bundle.                             |

### 5.5 `unbundle` subcommand flags

Positionals: `DELTA-IN BASELINE-SPEC OUTPUT-DIR` (see §3.2). Flags:

| Flag              | Values                                 | Meaning                                                      |
|-------------------|----------------------------------------|--------------------------------------------------------------|
| `--image-format`  | `docker-archive\|oci-archive\|dir`     | Per-image reconstructed format (renamed from `--output-format`). |
| `--allow-convert` | (bool)                                 | Same as today.                                               |
| `--strict`        | (bool)                                 | Require every baseline referenced by the bundle to be present. |
| `--dry-run`       | `-n`                                   | Verify baseline reachability only.                           |

### 5.6 Flag renames summary

| Before                     | After                    | Scope                                             |
|----------------------------|--------------------------|---------------------------------------------------|
| `--output` (global)        | `--format` / `-o`        | Rendering (text/json).                            |
| `--output-format`          | `--image-format`         | `apply` / `unbundle` only.                        |
| `--pair NAME=BASE,TARGET`  | (removed)                | Migrate to `diff` (single) or `bundle` (spec file). |
| `--baseline NAME=PATH`     | (removed)                | Migrate to `apply` (single) or `unbundle` (spec file). |
| `--baseline-spec FILE`     | positional `BASELINE-SPEC` | `unbundle` now takes spec as the 2nd positional. |
| `--bundle FILE`            | positional `BUNDLE-SPEC`   | `bundle` now takes spec as the 1st positional.   |

## 6. Error and help UX

### 6.1 Help text template (applies to every subcommand)

```
<short description, one line>

Usage:
  diffah <verb> [flags] <POSITIONAL-1> <POSITIONAL-2> [<POSITIONAL-3>]

Arguments:
  POSITIONAL-1   <what it is, what transports/formats are accepted>
  POSITIONAL-2   <same>
  POSITIONAL-3   <same>

Examples:
  # <plain-language scenario description>
  diffah <verb> <concrete args, all transports spelled out>

  # <next scenario>
  diffah <verb> <concrete args>

  # Dry-run
  diffah <verb> --dry-run <concrete args>

Flags:
  ...

Global Flags:
  ...
```

Cobra's default help omits the Arguments section; we add it by setting a custom `SetHelpTemplate` on each command that accepts positionals.

**Per-command example counts:**
- `diff` / `apply`: 3 examples each (common case, cross-format, dry-run).
- `bundle` / `unbundle`: 2 examples each (spec file, strict mode).
- `inspect`: 2 examples (text, json).
- `doctor` / `version`: no examples needed.

### 6.2 Argument count errors

When positional count is wrong:

```
$ diffah diff docker-archive:/tmp/old.tar
Error: 'diff' requires 3 arguments (BASELINE-IMAGE, TARGET-IMAGE, DELTA-OUT), got 1.

Usage:
  diffah diff BASELINE-IMAGE TARGET-IMAGE DELTA-OUT

Example:
  diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar

Run 'diffah diff --help' for more examples.
```

Implementation: replace `cobra.ExactArgs(N)` with a custom `Args` func that emits the above template. Exit code 2 (user error).

### 6.3 Removed-command errors

```
$ diffah export --pair app=v1.tar,v2.tar bundle.tar
Error: unknown command 'export'. This command was removed in the CLI redesign.

Did you mean one of:
  diffah diff   BASELINE-IMAGE TARGET-IMAGE DELTA-OUT   # single-image delta
  diffah bundle BUNDLE-SPEC    DELTA-OUT                 # multi-image bundle via spec file

Run 'diffah --help' for the full command list.
```

```
$ diffah import --baseline default=v1.tar --delta d.tar output.tar
Error: unknown command 'import'. This command was removed in the CLI redesign.

Did you mean one of:
  diffah apply    DELTA-IN BASELINE-IMAGE TARGET-OUT    # single-image apply
  diffah unbundle DELTA-IN BASELINE-SPEC  OUTPUT-DIR    # multi-image unbundle via spec file

Run 'diffah --help' for the full command list.
```

Implementation: intercept `export` / `import` via a pre-exec hook that emits the above and exits with code 2.

### 6.4 Missing transport prefix

```
$ diffah diff /tmp/old.tar /tmp/new.tar delta.tar
Error: missing transport prefix for BASELINE-IMAGE: "/tmp/old.tar"

Image references require a transport prefix. Supported transports:
  docker-archive:PATH     # Docker tar archive (docker save)
  oci-archive:PATH        # OCI tar archive (skopeo copy ... oci-archive:...)
  oci:PATH[:TAG]          # OCI layout directory
  dir:PATH                # Directory with raw blobs

Did you mean:  docker-archive:/tmp/old.tar

Run 'diffah diff --help' for examples.
```

The "Did you mean" suggestion picks `docker-archive:` if the path looks like a file (filename ends with `.tar` / `.tgz` / `.tar.gz`); otherwise the hint lists both `oci:` and `dir:`. Pure lexical heuristic; no filesystem stat.

### 6.5 Unknown flag (cobra built-in, enabled)

```
$ diffah diff --targett docker-archive:/x.tar ...
Error: unknown flag '--targett'.

(cobra's built-in suggester will suggest the closest known flag.)
Run 'diffah diff --help' for available flags.
```

Implementation: enable cobra's `SuggestionsMinimumDistance` (default 2) and ensure a custom `FlagErrorFunc` wraps the message with the final "Run --help" line.

### 6.6 Reserved-but-not-implemented transport

Pattern shown in §4: lists supported transports and points at the roadmap.

### 6.7 JSON mode error rendering

When `-o json` is active, errors render as structured JSON (existing behavior preserved):

```json
{
  "error": {
    "category": "user",
    "code": "missing_transport",
    "message": "missing transport prefix for BASELINE-IMAGE: \"/tmp/old.tar\"",
    "supported_transports": ["docker-archive", "oci-archive", "oci", "dir"],
    "hint": "docker-archive:/tmp/old.tar"
  }
}
```

The existing `pkg/errs` classification taxonomy is reused — this design adds new error codes (`missing_transport`, `reserved_transport`, `unknown_command`, `wrong_arg_count`) but doesn't change the category system.

## 7. Architecture

### 7.1 Package layout

```
cmd/
  root.go          # Global flag wiring (--format, --log-*, --progress, -q, -v)
  diff.go          # New: single-image diff subcommand
  apply.go         # New: single-image apply subcommand
  bundle.go        # New: multi-image bundle subcommand
  unbundle.go      # New: multi-image unbundle subcommand
  inspect.go       # Touched: help text + positional error template
  doctor.go        # Touched: help text only
  version.go       # Unchanged
  transport.go     # New: image reference parser + error helpers
  help.go          # New: shared help template installer
  removed.go       # New: traps for 'export' / 'import' -> redirection errors

  export.go        # DELETED
  import.go        # DELETED
```

### 7.2 Image reference parser (new)

`cmd/transport.go` exposes:

```go
type ImageRef struct {
    Transport string // "docker-archive" | "oci-archive" | "oci" | "dir"
    Path      string // filesystem path (for archive/dir transports)
    Tag       string // only populated for "oci" transport
}

func ParseImageRef(argName, raw string) (ImageRef, error)
```

`argName` is the positional-arg name (e.g. `BASELINE-IMAGE`) used in the error message. The parser is purely lexical — no filesystem stat. Supported-transport list and "Did you mean" hint are embedded in the returned error.

The parser returns a well-typed struct; existing library code (`pkg/exporter`, `pkg/importer`) accepts `ImageRef` or paths via adapters in each subcommand.

### 7.3 Spec-file parsers (reused)

`diff.ParseBundleSpec` and `diff.ParseBaselineSpec` already exist. The subcommands call them on the positional argument.

### 7.4 Help template (new)

`cmd/help.go` installs a custom help template on commands that accept positional arguments. The template adds an `Arguments:` section by reading a `PositionalArgs` annotation set on each command. See §6.1 for the template.

### 7.5 Custom Args validator (new)

Replaces `cobra.ExactArgs(N)` with a helper:

```go
func requireArgs(argNames []string) cobra.PositionalArgs
```

The returned validator, on mismatch, emits the template from §6.2 and returns a sentinel error classified as `user` (exit 2).

### 7.6 Removed-command trap

`cmd/removed.go` registers two "stub" cobra commands named `export` and `import`. Their `RunE` always returns a sentinel error with the §6.3 message. This intercepts *before* users hit the old flag-parsing code path (which no longer exists anyway, since the old flags are deleted).

## 8. Testing

Every change lands with test coverage matching the existing `cmd/` test patterns (`*_test.go`, golden-file integration tests via `testmain.go`).

### 8.1 Unit tests

- `cmd/transport_test.go`: parse tables covering each supported transport, rejected reserved transports, missing prefix, "Did you mean" hint selection (file vs. directory path).
- `cmd/help_test.go`: Arguments-section rendering for a sample command.
- `cmd/removed_test.go`: invoking `export` / `import` returns the redirection message and exit code 2.

### 8.2 Integration tests (`*_integration_test.go`)

- `diff_integration_test.go`: end-to-end `diff` → `inspect` → `apply` round-trip using `docker-archive` tars.
- `bundle_integration_test.go`: end-to-end `bundle` → `unbundle` using spec files.
- `exit_integration_test.go`: extend with cases for new error scenarios (missing transport, wrong arg count, removed command).

### 8.3 JSON schema tests

- `diff_json_test.go` / `apply_json_test.go`: `--format json` error envelopes for each new error code.
- `inspect_json_test.go`: existing snapshot regenerated only if schema genuinely changes (it should not).

### 8.4 Golden-file help output

- `cmd/help_golden_test.go`: snapshot the `--help` output of each subcommand. Stable string makes it a regression guard against accidental help-text drift.

### 8.5 Manual acceptance

Before declaring done, run through the user-failure scenario from §1:

```
$ diffah diff
<expect: §6.2 error with example>

$ diffah diff /tmp/old.tar /tmp/new.tar delta.tar
<expect: §6.4 missing-transport error with "Did you mean" hint>

$ diffah export --pair ...
<expect: §6.3 removed-command redirection>

$ diffah diff docker-archive:/tmp/old.tar docker-archive:/tmp/new.tar delta.tar
$ diffah inspect delta.tar
$ diffah apply delta.tar docker-archive:/tmp/old.tar docker-archive:/tmp/restored.tar
<expect: round-trip succeeds, restored tar digests match original>
```

## 9. Migration notes (CHANGELOG text)

```
### Breaking changes

- Commands `diffah export` and `diffah import` are removed. Use `diffah diff` /
  `diffah apply` for single-image workflows and `diffah bundle` / `diffah unbundle`
  for multi-image bundles.
- `--pair` and `--baseline` composite flags are removed. Multi-image workflows
  now require a JSON spec file passed as a positional argument to `bundle` /
  `unbundle`.
- Image references now require a transport prefix (`docker-archive:`,
  `oci-archive:`, `oci:`, `dir:`). Bare paths error out with a "Did you mean"
  hint.
- Global `--output` renamed to `--format` (`-o`). The old name collided with
  positional output paths and the `--output-format` archive-format flag.
- `--output-format` renamed to `--image-format` (scoped to `apply` / `unbundle`).
```

## 10. Rollout

Single commit-series on a feature branch (`spec/cli-redesign-skopeo-inspired`):

1. Add transport parser + help template helpers (no command changes yet).
2. Add new subcommands (`diff`, `apply`, `bundle`, `unbundle`) alongside old ones.
3. Add removed-command traps; delete old `export.go` / `import.go`; delete now-unused flag globals.
4. Update golden-file tests; update CHANGELOG.
5. Regenerate any JSON schema snapshots that changed.

Each step is independently testable. Step 2 is where the design is visible end-to-end.

---

## 11. Open questions resolved in brainstorming

| Q | Decision |
|---|---|
| Transport prefix enforcement | A1: strict, no sniffing. |
| Multi-image command form | B1: separate `bundle` / `unbundle` subcommands. |
| Old command alias period | C1: hard rename, redirection error. |
| Error message verbosity | D1: multi-line with usage line, example, and `--help` pointer. |
