# diffah compatibility contract

This document defines the stability guarantees diffah makes about exit
codes, the sidecar schema, structured log output, and progress output.

## Exit codes

| Code | Category     | When |
|------|--------------|------|
| 0    | success      | operation completed |
| 1    | internal     | bug, panic, or an unclassified error |
| 2    | user         | bad flag, missing input, conflicting options, malformed `--baseline` or `--pair`, wrong baseline supplied |
| 3    | environment  | missing zstd, network failure, filesystem permission, registry auth |
| 4    | content      | sidecar schema mismatch, digest mismatch, archive corruption, unsupported schema version |

**Stability:** exit-code mappings for specific errors may be refined (e.g. a
new env-level fallback being added). Exit code 0 for "success" never
changes; we will not migrate a currently-2 error into a 3 without a
major-version bump.

**CLI edge case:** `cmd.Execute` remaps unclassified `CategoryInternal`
errors to `CategoryUser` (exit 2) with the hint "run 'diffah --help' for
usage", because an unclassified error from the CLI edge is almost always a
user-input problem rather than a diffah bug. Exit code 1 is reserved for
genuine internal bugs but is not currently produced by the CLI path;
programmatic callers using `errs.Classify` directly may observe it.

## Sidecar schema version

Sidecar field `version` (currently `v1`) is authoritative. Import-side
negotiation rule: a reader that does not know `sidecar.version` must exit
4 with a message of the form

> `unknown bundle version "vN" (this build supports "v1") — upgrade diffah to import`

Rules:

- **Forward-compat on read.** Unknown *optional* fields are ignored with
  a `debug`-level log line. Unknown *required* fields yield a content
  error (exit 4).
- **Deprecation cycle.** Removing a CLI flag or a sidecar field requires
  one full minor-version cycle as *deprecated* (warning on every use)
  before the removal lands.
- **What bumps the version.** Sidecar schema changes bump `version`.
  CLI-only changes (new flags, new commands) do not.

## Structured log output (slog)

The JSON log format emits records with the following reserved fields:

- `time`, `level`, `msg` — stdlib slog standards.
- `component` — one of `exporter`, `importer`, `diff`, `archive`,
  `imageio`, `oci`, `zstdpatch`. New components may be added.

Key-name stability:

- Adding new keys is non-breaking.
- Renaming or removing keys is breaking — requires one minor-version
  cycle with both keys emitted and a deprecation warning.
- Machine consumers should match on `msg` + `component` + stable keys
  (digest, size, phase, etc.).

## Progress output

Progress output (bars on TTY, lines on non-TTY) is for **humans, not
machines**. It has no stability guarantee. Machine consumers must use
`--log-format=json` + structured slog events instead.

## JSON data output (`--output json`)

Every JSON response is a top-level envelope:

```json
{"schema_version": 1, "data": {...}}
```

Error responses use the same envelope with the error payload under `data`:

```json
{"schema_version": 1, "data": {"category": "...", "message": "...", "next_action": "..."}}
```

Rules:

- `schema_version` is bumped only on breaking shape changes.
- Within a `schema_version`, adding fields is non-breaking. Removing or
  renaming is breaking.
- `next_action` is optional; when empty, renderers omit the hint line in
  text mode and the `next_action` key may be omitted in JSON mode.
