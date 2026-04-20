# diffah v2 — Intra-Layer Backend Resilience

**Date:** 2026-04-20
**Status:** Design approved, pending implementation plan
**Scope owner:** diffah core (leosocy)
**Supersedes:** nothing; extends Phase 1 (`2026-04-20-diffah-v2-intra-layer-design.md`).

## 1. Purpose and motivation

diffah v2 Phase 1 shipped intra-layer binary patches with `zstd --patch-from`.
The Phase 1 backend decision record
(`docs/superpowers/decisions/2026-04-20-zstdpatch-backend.md`) picked
`os/exec` over `klauspost/compress` because klauspost's raw-dictionary API
does not implement patch-from semantics — measured patches were 5–771×
larger than the CLI's, failing the ≤ 1.5× acceptance criterion. The
consequence: **`internal/zstdpatch` requires `zstd ≥ 1.5` on `$PATH` for
both encode and decode paths, including the non-patch "full zstd" path
that does not need patch-from at all.**

Two operational problems follow:

1. **Deployment fragility.** Any host without `zstd ≥ 1.5` (Ubuntu 20.04
   LTS ships 1.4.4; many container base images strip compression CLIs)
   cannot run diffah at all — not even against archives that contain no
   patches. Users hit an opaque `exec: "zstd": executable file not found`
   late in an export or import, after the archive is half-written.
2. **Import-side portability loss.** An archive produced on a well-provisioned
   build host can fail to import on a minimal air-gapped destination even
   when the specific archive has no `patch_from_digest` refs.

This design hardens the backend so that:

- Full-zstd encode/decode need **no external binary** (klauspost handles it).
- Patch-from encode/decode still use the CLI (no pure-Go candidate currently
  matches its ratio at our layer sizes — see §10.1).
- The user has three explicit modes — `auto`, `off`, `required` — and a
  single capability probe decides what actually runs.
- Full-only archives round-trip on any host, with or without `zstd`
  installed.

The feature is pure hardening: no new sidecar fields, no new payload bytes,
no schema bump. All existing archives continue to decode.

## 2. Scope and non-goals

**In scope:**

- New `zstdpatch.Available(ctx) (ok bool, reason string)` capability probe
  that `exec.LookPath`s the binary, invokes `zstd --version`, parses
  major.minor, requires ≥ 1.5, and caches via `sync.Once`.
- `internal/zstdpatch.EncodeFull` and `DecodeFull` reimplemented on
  `github.com/klauspost/compress/zstd` (pure Go, already a direct dep).
  `Encode` and `Decode` continue to shell out to `zstd --patch-from`.
- `--intra-layer=auto|off|required` on `diffah export` (was `auto|off`).
  `required` is the new mode; default stays `auto`.
- Export-side mode resolution that downgrades `auto` to `off` with a
  stderr warning when the probe fails, and hard-fails `required` before
  any filesystem work.
- Import-side probe that runs only when the sidecar contains at least one
  `Encoding: patch` BlobRef (whose `patch_from_digest` field names the
  reference baseline), returning `ErrZstdBinaryMissing` before any blob
  file is opened otherwise.
- `DryRunReport` gains `RequiresZstd` and `ZstdAvailable` fields.
- `diffah inspect` surfaces both probe results.
- Version-tolerant probe parsing — both Unix and Windows `zstd --version`
  banners handled.

**Explicitly out of scope:**

- Embedded `zstd` binary via `go:embed` (revisit only if ops push back).
- Alternative pure-Go binary-delta codecs (`bsdiff`, `vcdiff`,
  CDC + chunk-zstd). Re-evaluate when a credible candidate matches CLI
  ratio at 200 MB scale.
- CGO bindings for `libzstd`.
- Compression-level tuning or adaptive window-log.
- Sidecar schema changes (`Encoding` remains the sole per-blob
  discriminator; no new fields).
- Cross-image batching, content-similarity layer matching, parallel
  patch computation — tracked separately in the Phase 2+ roadmap.

## 3. Decision log

| # | Decision | Reason |
|---|---|---|
| 1 | Full-zstd path (`EncodeFull` / `DecodeFull`) moves to klauspost. | klauspost handles plain zstd correctly; the Phase 1 decision record rejected it only for patch-from. Removing the external-binary requirement for non-patch paths is the single largest portability win. |
| 2 | Patch-from path stays on `os/exec zstd`. | No pure-Go candidate currently matches CLI ratio at 200 MB+ layers (see §10.1). Re-evaluate when state of the art changes. |
| 3 | `--intra-layer` gains `required` mode (not a boolean flag). | Separates "best-effort" from "quality SLA"; CI pipelines that must not silently regress need the explicit contract. |
| 4 | `auto` default preserved; `auto` + no zstd = silent downgrade with stderr warning. | Matches Phase 1 expectation that `auto` "does the best thing available" without user intervention. |
| 5 | Capability probe lives in `internal/zstdpatch`, not in exporter/importer. | Single source of truth; the probe's semantics (what counts as "available") are a codec-package concern. |
| 6 | Probe result cached via `sync.Once`. | Probe is observable (runs `exec`) but pure; caching avoids per-blob overhead and makes concurrent use safe. |
| 7 | Import-side probe only fires when sidecar has at least one `Encoding: patch` BlobRef. | Full-only archives import everywhere — this is the user-facing portability promise. |
| 8 | Minimum version pinned at zstd 1.5. | `--patch-from` shipped in zstd 1.5.0; earlier versions cannot encode or decode patches regardless of klauspost. |
| 9 | klauspost decoder configured with `WithDecoderMaxWindow(1<<27)`. | Phase 1 archives encoded `--long=27` frames with 128 MB windows; klauspost's default decoder cap would reject them. Backward compatibility for existing archives requires this. |
| 10 | No sidecar schema change. | Existing `Encoding` field already discriminates full vs patch. New probe state is runtime-only, not on-wire. |

## 4. Architecture

### 4.1 Package layout

```
internal/zstdpatch/
  zstdpatch.go        Public API surface + shared helpers
  cli.go              os/exec backend:    Encode, Decode         (patch-from; needs CLI ≥ 1.5)
  fullgo.go           klauspost backend:  EncodeFull, DecodeFull (pure Go)
  available.go        exec.LookPath + version probe + sync.Once cache
  *_test.go           Unit tests per file
```

The package exposes one stable surface — `Encode`, `EncodeFull`, `Decode`,
`DecodeFull`, `Available` — and hides the backend choice internally. No
caller in `pkg/exporter` or `pkg/importer` learns that two backends exist.

### 4.2 Mode trichotomy

| Mode | Probe result | Effective behaviour |
|---|---|---|
| `auto` (default) | ok | Planner computes `min(patch, full)` per layer as today |
| `auto` | fail | Planner short-circuits: every shipped layer → `Encoding: full`; stderr warning |
| `off` | either | Planner always emits `Encoding: full`; probe not required |
| `required` | ok | Same as `auto` + ok |
| `required` | fail | Return `ErrZstdBinaryMissing` before touching the filesystem |

`off` deliberately skips the probe: a user who chose `off` does not need
to own `zstd` and should not be blocked on a system dependency that the
run will not use.

### 4.3 Capability probe

```go
// Available reports whether zstd ≥ 1.5 is usable for patch-from encode/decode.
// Result is cached per-process.
func Available(ctx context.Context) (ok bool, reason string)
```

Algorithm:
1. `exec.LookPath("zstd")` — on miss, return `(false, "zstd not on $PATH")`.
2. Run `zstd --version` with a 1-second context timeout.
3. Parse major/minor from the banner using a tolerant regex
   (`v?(\d+)\.(\d+)(?:\.\d+)?`) that matches Unix and Windows output.
4. If `major < 1` or `(major == 1 && minor < 5)`, return
   `(false, "zstd <version> too old; need ≥1.5")`.
5. On success, return `(true, "")`.

The result is memoised with `sync.Once`. For testing, a package-private
`availableForTesting(lookup, version func() ...)` hook allows injection
without touching `$PATH`.

### 4.4 klauspost full-zstd configuration

Encoder:
- `zstd.WithWindowSize(1 << 27)` — matches Phase 1's `--long=27`.
- `zstd.WithEncoderLevel(zstd.SpeedDefault)` — klauspost's `SpeedDefault`
  is the documented analogue of CLI `-3` (klauspost's levels are bucketed:
  `SpeedFastest` ≈ CLI 1, `SpeedDefault` ≈ CLI 3, `SpeedBetterCompression`
  ≈ CLI 7-8, `SpeedBestCompression` ≈ CLI 11). Spike (§10.1) validates
  the bytes-out stay within ±5 % of CLI `-3` on the reference layer pair.

Decoder:
- `zstd.WithDecoderMaxWindow(1 << 27)` — **required** to decode archives
  produced by Phase 1's `os/exec zstd --long=27`. Without this flag,
  existing archives fail to import silently.

A single encoder and decoder are constructed per `Export` / `Import`
invocation and reused across layers, so klauspost's per-instance setup
cost is paid once, not per blob.

### 4.5 `Options.IntraLayer` extension

`Options.IntraLayer` stays the existing `string` field
(`exporter.go:38`) to avoid a breaking rename. It accepts one additional
value:

| Value | Meaning |
|---|---|
| `""` or `"auto"` | default — try patch-from, downgrade silently if zstd absent |
| `"off"` | never patch; every shipped layer is `Encoding: full` |
| `"required"` | **new** — fail hard if zstd absent |

Flag validation rejects any other string with a helpful message listing
the three valid values.

Two new fields on `Options` enable test injection without real `$PATH`
churn:

```go
type Options struct {
    …existing fields…
    // Probe reports zstd availability. Defaults to zstdpatch.Available
    // when nil. Tests inject a stub.
    Probe    func(context.Context) (ok bool, reason string)
    // WarnOut receives the one-line downgrade warning in auto + !probe.
    // Defaults to os.Stderr when nil.
    WarnOut  io.Writer
}
```

## 5. Export flow

```
Export(ctx, opts)
 1. effectiveMode, warning, err := resolveMode(opts.IntraLayer, opts.Probe(ctx))
     if err ≠ nil → return err (no archive written)
     if warning ≠ "" → fprintln(opts.WarnOut, warning)
 2. planner := IntraLayerPlanner{Mode: effectiveMode, …}
 3. for each target layer in Plan.ShippedInDelta:
      if effectiveMode == "off":
         payload  := zstdpatch.EncodeFull(layerBytes)
         encoding := EncodingFull  // "full"
      else: // "auto"
         patchBytes, fullBytes := computeBoth(layerBytes, baseline)
         if len(patchBytes) < len(fullBytes):
            payload        := patchBytes
            encoding       := EncodingPatch  // "patch"
            patchFromDigest := baseline.Digest
         else:
            payload        := fullBytes
            encoding       := EncodingFull   // "full"
      write blob + BlobRef with encoding (and patch_from_digest if patch)
 4. write sidecar (schema unchanged)
```

`resolveMode` table (inputs → outputs):

| User mode | Probe ok | Effective | Warning | Error |
|---|---|---|---|---|
| `auto` | true | `auto` | — | — |
| `auto` | false | `off` | `diffah: <reason>; disabling intra-layer for this run` | — |
| `off` | — | `off` | — | — |
| `required` | true | `auto` | — | — |
| `required` | false | — | — | `ErrZstdBinaryMissing` |

## 6. Import flow

```
Import(ctx, archivePath)
 1. read sidecar
 2. needsZstd := any BlobRef has Encoding == "patch"
 3. if needsZstd:
      ok, reason := zstdpatch.Available(ctx)
      if !ok → return wrapped ErrZstdBinaryMissing
 4. CompositeSource.GetBlob dispatches per Encoding:
      "full"  → zstdpatch.DecodeFull(payload)                        // klauspost
      "patch" → zstdpatch.Decode(baselineBytes(patchFromDigest), payload)  // os/exec
      (required_from_baseline) → passthrough from baseline source
 5. digest-verify every patched layer (unchanged from Phase 1)
```

**Portability invariant.** If the sidecar contains zero `Encoding: patch`
BlobRefs, the import path performs zero `exec.LookPath` calls and zero
external-process launches. This is a testable property.

`DryRun(ctx, archivePath) (DryRunReport, error)` follows the same logic
but does **not** return `ErrZstdBinaryMissing` when the probe fails on a
patch-containing archive. Instead it fills:

```go
type DryRunReport struct {
    …existing fields…
    RequiresZstd   bool   // any BlobRef has Encoding == "patch"
    ZstdAvailable  bool   // result of zstdpatch.Available(ctx)
}
```

The caller (`diffah inspect`) is responsible for converting "needs zstd
but not available" into user-facing guidance; `DryRun` simply reports.

## 7. Error model

| Error | Kind | Raised by | Trigger |
|---|---|---|---|
| `ErrZstdBinaryMissing` | **new** sentinel | exporter | `--intra-layer=required` + probe fails |
| `ErrZstdBinaryMissing` | same sentinel | importer | archive has patch refs + probe fails |
| `ErrIntraLayerAssemblyMismatch` | existing | importer | post-patch digest mismatch |
| `ErrBaselineMissingPatchRef` | existing | importer | patch ref names absent baseline |
| `ErrIntraLayerUnsupported` | existing | exporter | `auto` + manifest-only baseline |

`ErrZstdBinaryMissing` is wrapped with the probe's `reason` string so
`errors.Is(err, zstdpatch.ErrZstdBinaryMissing)` works and the user sees
a diagnosable message:

```
diffah: zstd binary required but unavailable: zstd 1.4.4 too old; need ≥1.5
  install zstd ≥ 1.5 from your distro or https://github.com/facebook/zstd/releases
```

The install hint is formatted at the CLI layer, not in the library error,
so library callers are not coupled to distribution-specific guidance.

## 8. Testing strategy

### 8.1 Probe unit tests (`internal/zstdpatch/available_test.go`)

Driven by the injectable test hook (no real `$PATH` churn in the common case):

| Case | Injected lookup | Injected version | Expected |
|---|---|---|---|
| Present, new | `/usr/bin/zstd` | `"*** zstd command line interface 64-bits v1.5.6 ***"` | `ok=true` |
| Missing | `ErrNotFound` | — | `ok=false`, reason `"zstd not on $PATH"` |
| Too old | `/usr/bin/zstd` | `"v1.4.4"` | `ok=false`, reason contains `"1.4.4"` and `"≥1.5"` |
| Windows banner | `C:\tools\zstd.exe` | `"zstd command line interface 32-bits v1.5.5"` | `ok=true` |
| Chocolatey banner | `…zstd.exe` | `"zstd 1.5.6"` | `ok=true` |
| Garbage banner | `/usr/bin/zstd` | `"not a real version string"` | `ok=false`, reason mentions parse failure |

One end-to-end integration test manipulates `t.Setenv("PATH", "")` and
asserts the real `Available` returns `ok=false` for parity.

### 8.2 klauspost full-zstd round-trip

`fullgo_test.go`: EncodeFull + DecodeFull byte-exact round-trip on three
size tiers (1 KB, 1 MB, 200 MB), with no `zstd` on `$PATH` (tests assert
`exec.LookPath` is never called). The 200 MB case uses a fixed-seed
`math/rand` source to keep CI deterministic.

**Regression test for decision #9:** a golden fixture archive produced
by Phase 1's `os/exec zstd --long=27 EncodeFull` must decode byte-exact
via klauspost `DecodeFull`. Without `WithDecoderMaxWindow(1<<27)`, this
test fails loudly.

### 8.3 Exporter mode-resolution (`pkg/exporter/exporter_test.go`)

Table-driven over the five rows of §5. Each case:

- Injects a stub `Options.Probe` returning the desired `(ok, reason)`.
- Runs against the v3 fixture pair.
- Asserts:
  - Exit condition (success / `ErrZstdBinaryMissing`).
  - Whether `Options.WarnOut` received a line (and its content).
  - For success cases, the sidecar encoding distribution
    (`all full` / `mixed patch+full`).

### 8.4 Importer matrix (`pkg/importer/importer_test.go`)

Four-combo matrix on `{archive has any Encoding: patch BlobRef?} × {probe ok?}`:

| Archive | Probe | Expected |
|---|---|---|
| all-full | missing | Round-trip succeeds; **zero** `exec.LookPath` calls (asserted via injected `Probe`) |
| all-full | present | Round-trip succeeds |
| patch-refs | missing | `ErrZstdBinaryMissing` **before** any blob file opened (asserted via file-open counter) |
| patch-refs | present | Round-trip succeeds |

`DryRunReport` assertions cover the same grid but never error; only fill
`RequiresZstd` and `ZstdAvailable`.

### 8.5 Integration (`pkg/importer/integration_test.go`)

Existing v3-fixture scenarios stay green. New scenario: a sub-test that
`t.Setenv`s a reduced `$PATH` excluding the real `zstd`, runs export +
import on the v3 pair under that environment, and asserts:

- Export produced an archive with all `Encoding: full`.
- Stderr contained the downgrade warning exactly once.
- Import round-trip was byte-exact.

### 8.6 CLI tests

- `cmd/export_test.go`: flag parsing accepts `required`, rejects `foo`.
- `cmd/inspect_test.go`: new output lines
  `intra-layer patches required: yes|no` and
  `zstd available: yes|no` appear for archives with/without patch refs.

## 9. Backward compatibility

- **Archive format.** Unchanged. Existing Phase 1 archives on disk import
  unmodified. The klauspost `DecodeFull` with `WithDecoderMaxWindow(1<<27)`
  is the load-bearing compatibility detail (§4.4, decision #9).
- **CLI.** `--intra-layer=auto` default preserved. `off` preserved.
  `required` is additive and opt-in.
- **Public API.** `internal/zstdpatch` is internal, so callers are only
  `pkg/exporter` and `pkg/importer`; both update in the same patch.
  `Options.IntraLayer` gains an enum value but existing callers passing
  `auto` or `off` observe no behaviour change.
- **DryRunReport.** Two additive fields; existing consumers reading named
  fields (not structural-equality) are unaffected.

## 10. Risks

### 10.1 klauspost `EncodeFull` ratio drifts from CLI `EncodeFull`

**Severity:** medium. Reason: Phase 1's baseline savings measurements
used CLI full-zstd bytes. If klauspost produces meaningfully larger
full-zstd output (say >10 %), the advertised ratios degrade for users
who run in `off` mode or who hit the silent-downgrade path.

**Mitigation:** Task 0 spike of the implementation plan re-runs the
reference layer pair (`/tmp/diffah-poc/ref.blob`, `target.blob`, 123 MB
and 213 MB) through klauspost `WithEncoderLevel(SpeedDefault)` +
`WithWindowSize(1<<27)` and compares bytes-out to CLI `-3 --long=27`.
Acceptance: within ±5 %. If `SpeedDefault` fails acceptance the spike
retries `SpeedBetterCompression` — the escalation cost is one extra
run. If neither passes, the plan narrows to only adding `required` mode
+ probe and leaves `EncodeFull` / `DecodeFull` on `os/exec` (sacrificing
the portability win on the full path but keeping everything else).

### 10.2 Windows `zstd --version` output variance

**Severity:** low. Reason: Chocolatey, scoop, and self-built `zstd.exe`
emit slightly different banners. A too-strict regex breaks otherwise
valid installations.

**Mitigation:** tolerant regex `v?(\d+)\.(\d+)(?:\.\d+)?` matches anywhere
in the output. Unit tests (§8.1) cover three observed banner styles.

### 10.3 `t.Setenv("PATH", "")` pollution

**Severity:** low. Reason: if not restored, breaks downstream tests.

**Mitigation:** `t.Setenv` restores automatically at test-end; we rely
on this exclusively and never use raw `os.Setenv` in tests.

### 10.4 `sync.Once` defeats per-test probe injection

**Severity:** medium. Reason: cache means a test that sets up an injected
probe after another test has already called `Available()` sees the
earlier result.

**Mitigation:** tests call `zstdpatch.availableForTesting` with explicit
inputs instead of going through the cached `Available()`. Production
callers use `Available()`; tests use the uncached variant. A
package-private `resetAvailableForTesting()` is also provided for tests
that intentionally want to exercise the caching behaviour.

### 10.5 Context cancellation during `zstd --version`

**Severity:** low. Reason: if the probe hangs (hostile `zstd` replacement
on `$PATH`), exporter start-up stalls.

**Mitigation:** `exec.CommandContext` with a 1-second deadline. On
timeout, probe returns `(false, "zstd --version timed out")`.

## 11. Rollout

- Single patch — no feature flag. Behaviour change for users is strictly
  additive: `required` is a new mode; existing modes unchanged save for
  the probe running.
- No migration tooling needed — archive format unchanged.
- README updates: the "Requirements" section is rewritten to say
  *"`zstd ≥ 1.5` is recommended for best compression; required only
  when using `--intra-layer=required` or when importing an archive that
  was produced with intra-layer patches."*

## 12. Open questions

None blocking. Items for the implementation plan to resolve:

1. Exact klauspost level mapping (§4.4). Spike output decides this.
2. Whether to expose `--require-zstd-version=X.Y` for operators who want
   to pin a higher floor than 1.5. Deferred — adds CLI surface area
   without a current user request.
3. Whether `diffah doctor` (a hypothetical health-check subcommand)
   should pre-flight the probe. Deferred — tracked in Phase 2+ roadmap.
