# diffah compatibility contract

This document defines the stability guarantees diffah makes about exit
codes, the sidecar schema, structured log output, and progress output.

## Exit codes

| Code | Category     | When |
|------|--------------|------|
| 0    | success      | operation completed |
| 1    | internal     | bug, panic, or an unclassified error |
| 2    | user         | bad flag, missing/extra positional arguments, missing transport prefix on image reference, malformed bundle or baseline spec file, wrong baseline supplied (baseline manifest digest does not match the one the delta was built against), invocation of a removed verb (`export`, `import`); registry 401/403 auth failures |
| 3    | environment  | missing zstd, network failure, filesystem permission; registry network / DNS / TLS failures |
| 4    | content      | sidecar schema mismatch, blob digest mismatch (archive corruption), unsupported schema version (e.g. Phase 1 archive), file that is not a diffah delta archive; registry manifest missing (404, NAME_UNKNOWN) or manifest invalid (unsupported schema) |

**Stability:** exit-code mappings for specific errors may be refined (e.g. a
new env-level fallback being added). Exit code 0 for "success" never
changes; we will not migrate a currently-2 error into a 3 without a
major-version bump.

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

## JSON data output (`--format json` / `-o json`)

Every JSON response is a top-level envelope:

```json
{"schema_version": 1, "data": {...}}
```

Error responses use a distinct envelope with the error payload under `error`:

```json
{"schema_version": 1, "error": {"category": "...", "message": "...", "next_action": "..."}}
```

Rules:

- `schema_version` is bumped only on breaking shape changes.
- Within a `schema_version`, adding fields is non-breaking. Removing or
  renaming is breaking.
- `next_action` is optional; when empty, renderers omit the hint line in
  text mode and the `next_action` key may be omitted in JSON mode.

## Transports

The table below lists which transport prefixes are accepted on each positional type.

| Transport          | `*-IMAGE` positionals | BASELINE-SPEC / OUTPUT-SPEC values | `diff` / `bundle` positionals |
|--------------------|-----------------------|-------------------------------------|-------------------------------|
| `docker-archive:`  | yes                   | yes                                 | yes                           |
| `oci-archive:`     | yes                   | yes                                 | yes                           |
| `docker://`        | yes                   | yes                                 | Phase 3                       |
| `oci:`             | yes                   | yes                                 | Phase 3                       |
| `dir:`             | yes                   | yes                                 | Phase 3                       |
| `docker-daemon:`   | reserved              | reserved                            | reserved                      |
| `containers-storage:` | reserved          | reserved                            | reserved                      |
| `ostree:`          | reserved              | reserved                            | reserved                      |
| `sif:`             | reserved              | reserved                            | reserved                      |
| `tarball:`         | reserved              | reserved                            | reserved                      |

"Phase 3" means the transport is parsed and validated but the verb rejects it with a "not yet implemented" error. "Reserved" means the transport string is recognised and returns "reserved but not yet implemented."

Note: `diff` and `bundle` (the export-side verbs) still only accept archive transports (`docker-archive:`, `oci-archive:`) in Phase 2. Registry-source export is Phase 3.

## Registry and transport flags

The following flags are available on `apply` and `unbundle` whenever the TARGET-IMAGE or any OUTPUT-SPEC entry uses a `docker://`, `oci:`, or `dir:` transport.

| Flag                        | Default | Description |
|-----------------------------|---------|-------------|
| `--authfile PATH`           | see below | Path to a containers auth file. |
| `--creds USER[:PASS]`       | —       | Inline credentials. Mutually exclusive with `--username`/`--password` and `--no-creds`. |
| `--username USER`           | —       | Registry username. Must be paired with `--password`. Mutually exclusive with `--creds` and `--no-creds`. |
| `--password PASS`           | —       | Registry password. Must be paired with `--username`. |
| `--no-creds`                | false   | Disable all credential lookup. Mutually exclusive with `--creds`, `--username`, `--password`, `--authfile`, and `--registry-token`. |
| `--registry-token TOKEN`    | —       | Bearer token passed directly in the Authorization header. Mutually exclusive with `--creds`, `--username`/`--password`, and `--no-creds`. |
| `--tls-verify`              | true    | Verify TLS certificates. Set `--tls-verify=false` only in test environments. |
| `--cert-dir PATH`           | —       | Directory of additional CA certificates (PEM). |
| `--retry-times N`           | 3       | Number of retries for transient failures (5xx, 429, connection-refused). |
| `--retry-delay DURATION`    | —       | Fixed retry delay. If omitted, exponential backoff is used, capped at 30 s. |

**Authfile precedence** (highest to lowest):

1. `--authfile PATH` (explicit flag)
2. `$REGISTRY_AUTH_FILE` environment variable
3. `$XDG_RUNTIME_DIR/containers/auth.json`
4. `$HOME/.docker/config.json`

**Mutual-exclusion rules:**

- `--creds` cannot be combined with `--username`, `--password`, `--no-creds`, or `--registry-token`.
- `--username` and `--password` must appear together; neither can be combined with `--creds`, `--no-creds`, or `--registry-token`.
- `--no-creds` cannot be combined with any other credential flag (`--creds`, `--username`, `--password`, `--authfile`, `--registry-token`).
- `--registry-token` cannot be combined with `--creds`, `--username`, `--password`, or `--no-creds`.

Non-retryable errors (auth 401/403, 404, manifest schema errors) fail immediately without consuming retry budget.
