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
larger than the CLI's, failing the ≤ 1.5× acceptance criterion.

After Phase 1 landed, a closer read of the exporter reveals the runtime
footprint is narrower than it appeared:

- `pkg/exporter/exporter.go:257` `resolveShipped` short-circuits when
  `IntraLayer == "off"`: returns `fullEntry` for every shipped blob and
  **no payload replacement** (`nil` map). The raw extracted target blobs
  on disk become the archive contents untouched.
- `pkg/diff/sidecar.go:138` validates that `encoding: "full"` always has
  `archive_size == size` — confirming that `encoding: "full"` stores
  *raw* layer bytes exactly as they appear in the target image.
- `pkg/exporter/intralayer.go:74` calls `zstdpatch.EncodeFull` only to
  compare its byte-count against the patch; the bytes themselves are
  thrown away. The stored "full" payload is the raw layer blob.
- `pkg/importer/composite_src.go:73-74` dispatches `EncodingFull` to
  `delta.GetBlob` — a passthrough copy. `zstdpatch.DecodeFull` is never
  invoked on the import side.

The real Phase 1 footprint of the `zstd` binary therefore turns out to be:

- **Required:** export with `--intra-layer=auto` (patch-from calls **and**
  the size-ceiling `EncodeFull` call), import of archives that contain
  any `encoding: "patch"` blob.
- **Not required:** export with `--intra-layer=off`, import of any
  archive whose shipped blobs are all `encoding: "full"`.

The second bullet was already true before this design, but the tool
nowhere advertises it and nowhere fails cleanly when users land in the
first case without `zstd` installed.

Two operational problems remain:

1. **Opaque failure mode in `auto`.** Any host without `zstd ≥ 1.5`
   (Ubuntu 20.04 LTS ships 1.4.4; many container base images strip
   compression CLIs) trying an `auto` export hits an unhelpful
   `exec: "zstd": executable file not found` deep inside the planner
   after some blobs have already been written.
2. **No way to guarantee patch quality in CI.** Operators who rely on
   the patch-from savings cannot distinguish "ran in auto mode and got
   lucky" from "zstd was present and patches actually encoded" — the
   tool silently degrades.

This design hardens the backend so that:

- `--intra-layer=auto` probes for `zstd ≥ 1.5` once at start-up and
  either proceeds with patching or silently downgrades to full-only
  encoding for the whole run.
- A new `--intra-layer=required` mode **refuses to proceed** when
  `zstd` is unavailable, giving CI pipelines a fail-fast contract.
- `zstdpatch.EncodeFull` (used only for the size-ceiling comparison)
  is reimplemented on `klauspost/compress`, removing the `zstd` binary
  dependency for the `auto` size comparison itself — the only non-patch
  call to the CLI that the exporter currently makes.
- Import-side probe only fires when the archive genuinely needs
  patch-from decoding, and returns an actionable error message in that
  case (instead of the current late, opaque failure).

The feature is pure hardening: no new sidecar fields, no new payload bytes,
no schema bump. All existing archives continue to decode.

## 2. Scope and non-goals

**In scope:**

- New `zstdpatch.Available(ctx) (ok bool, reason string)` capability probe
  that `exec.LookPath`s the binary, invokes `zstd --version`, parses
  major.minor, requires ≥ 1.5, and caches via `sync.Once`.
- `internal/zstdpatch.EncodeFull` reimplemented on
  `github.com/klauspost/compress/zstd` (pure Go, already a direct dep).
  This is the only non-patch call the exporter currently makes — used
  for the size-ceiling comparison in `pkg/exporter/intralayer.go:74`
  against the patch. `Encode` and `Decode` continue to shell out to
  `zstd --patch-from`. `DecodeFull` stays in the API for symmetry but
  is not exercised by any production call path (no archive stores
  zstd-full bytes; `encoding: "full"` = raw layer bytes).
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
| 1 | `EncodeFull` moves to klauspost; `DecodeFull` moves symmetrically but is dead code on the Phase 1 production call path. | klauspost handles plain zstd correctly; the Phase 1 decision record rejected it only for patch-from. `EncodeFull` is the size-ceiling comparator (`pkg/exporter/intralayer.go:74`), the only non-patch zstd call the exporter makes — moving it off `os/exec` lets `--intra-layer=auto` exports run without `zstd` when all layers end up as `encoding: "full"`. |
| 2 | Patch-from path stays on `os/exec zstd`. | No pure-Go candidate currently matches CLI ratio at 200 MB+ layers (see §10.1). Re-evaluate when state of the art changes. |
| 3 | `--intra-layer` gains `required` mode (not a boolean flag). | Separates "best-effort" from "quality SLA"; CI pipelines that must not silently regress need the explicit contract. |
| 4 | `auto` default preserved; `auto` + no zstd = silent downgrade with stderr warning. | Matches Phase 1 expectation that `auto` "does the best thing available" without user intervention. |
| 5 | Capability probe lives in `internal/zstdpatch`, not in exporter/importer. | Single source of truth; the probe's semantics (what counts as "available") are a codec-package concern. |
| 6 | Probe result cached via `sync.Once`. | Probe is observable (runs `exec`) but pure; caching avoids per-blob overhead and makes concurrent use safe. |
| 7 | Import-side probe only fires when sidecar has at least one `Encoding: patch` BlobRef. | Full-only archives import everywhere — this is the user-facing portability promise. |
| 8 | Minimum version pinned at zstd 1.5. | `--patch-from` shipped in zstd 1.5.0; earlier versions cannot encode or decode patches regardless of klauspost. |
| 9 | klauspost decoder configured defensively with `WithDecoderMaxWindow(1<<27)`, even though no production call path currently invokes `DecodeFull`. | Keeps the API symmetric with the encoder configuration. If a future feature ever routes zstd-full bytes through a decoder, the config is already correct. Zero cost today. |
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

Decoder (defensive / future-proof; no current production caller):
- `zstd.WithDecoderMaxWindow(1 << 27)` — configured to parity with the
  encoder so any future feature routing zstd-full bytes through the
  decoder finds the window cap already correct. Phase 1's
  `encoding: "full"` path stores raw layer bytes, never zstd-full
  bytes, so existing archives impose no compatibility burden here.

A single encoder is constructed per `Export` invocation and reused
across `EncodeFull` calls (one per shipped layer in auto mode). The
decoder is only instantiated lazily if ever called.

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
         // No payload replacement — raw extracted blob on disk becomes
         // the archive content. Matches Phase 1 resolveShipped behaviour
         // (pkg/exporter/exporter.go:258-262).
         payload  := nil    // keeps raw extracted blob
         encoding := EncodingFull   // "full"
      else: // "auto"
         patchBytes := zstdpatch.Encode(baseline, layerBytes)
         fullSize   := len(zstdpatch.EncodeFull(layerBytes))   // size ceiling only
         if len(patchBytes) < fullSize && int64(len(patchBytes)) < layer.Size:
            payload         := patchBytes
            encoding        := EncodingPatch  // "patch"
            patchFromDigest := baseline.Digest
         else:
            payload  := nil    // keeps raw extracted blob
            encoding := EncodingFull  // "full"
      write blob + BlobRef with encoding (and patch_from_digest if patch)
 4. write sidecar (schema unchanged)
```

**Key property preserved from Phase 1:** `encoding: "full"` always means
"raw layer bytes as extracted from the target image" — `archive_size ==
size`. `EncodeFull` is a size comparator, never a persisted payload.
Existing Phase 1 archives on disk therefore remain fully compatible
with this design.

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
      "full"  → delta.GetBlob passthrough                            // raw bytes, no zstd needed
      "patch" → zstdpatch.Decode(baselineBytes(patchFromDigest), payload)  // os/exec
      (required_from_baseline) → passthrough from baseline source
 5. digest-verify every patched layer (unchanged from Phase 1)
```

**Portability invariant (pre-existing, now made explicit and enforced).**
If the sidecar contains zero `Encoding: patch` BlobRefs, the import path
performs zero `exec.LookPath` calls and zero external-process launches.
This was already true in Phase 1 because `encoding: "full"` means raw
bytes and never required `zstd`; this design surfaces the property as a
testable assertion and guarantees the step 3 probe is skipped when it is
not needed.

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

### 8.2 klauspost full-zstd size-comparator parity

`fullgo_test.go`: klauspost `EncodeFull` must produce byte counts that
track CLI `zstd -3 --long=27` within ±5 % across three size tiers
(1 KB, 1 MB, 200 MB), with no `zstd` on `$PATH` (tests assert
`exec.LookPath` is never called for the klauspost path). Matters for
the planner's `len(patchBytes) < fullSize` comparison in `auto` mode;
a drifting ratio shifts per-layer patch-vs-full decisions.

A byte-exact EncodeFull → DecodeFull round-trip test is kept even
though the production call path does not invoke `DecodeFull`, to keep
the symmetric API honest and catch future regressions if a feature
ever starts routing zstd-full bytes through the decoder. The 200 MB
case uses a fixed-seed `math/rand` source to keep CI deterministic.

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

- **Archive format.** Unchanged. `encoding: "full"` remains raw layer
  bytes (`archive_size == size`); `encoding: "patch"` remains
  zstd-patch-from bytes. Existing Phase 1 archives on disk import
  unmodified because no schema field or payload semantic changes. The
  `DecodeFull` + `WithDecoderMaxWindow(1<<27)` configuration is
  defensive / future-proof only — no current production call path
  decodes zstd-full bytes (§4.4).
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

**Severity:** medium. Reason: `EncodeFull` is the size ceiling in the
`auto` planner (`pkg/exporter/intralayer.go:74`). If klauspost's bytes
drift ≥ 5 % from CLI `-3 --long=27`, per-layer patch-vs-full decisions
shift — layers that were emitted as patch under CLI may come out as
raw-full under klauspost (or vice versa), and the archive's advertised
savings drift. The payload bytes on disk are unchanged (raw bytes for
full, patch bytes for patch); only the classification can shift.

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
