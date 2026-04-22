# diffah v2 — zstd Backend Resilience: Post-Merge Cleanup

**Date:** 2026-04-22
**Status:** Design approved, pending implementation plan
**Scope owner:** diffah core (leosocy)
**Phase:** v2 Phase 2 — follow-up to the zstd-backend-resilience track
**Depends on:** `spec/v2-zstd-backend-resilience` branch (HEAD `a32c4cf`), implementing plan `docs/superpowers/plans/2026-04-22-diffah-v2-zstd-backend-resilience.md`.
**Supersedes scope:** none. Closes five review findings surfaced after the plan was fully implemented.

## 1. Purpose and motivation

The zstd-backend-resilience branch delivered the full plan (Tasks 0–9):
`Available` probe, `--intra-layer=required` mode, klauspost-backed
`EncodeFull`/`DecodeFull`, importer pre-check, inspect surfacing,
README narrowing. Build and lint are clean.

A post-implementation review surfaced five concrete issues that the
plan's self-review missed. Each warrants a fix, but none of them —
individually or combined — change the archive format, the public CLI
contract, or the spec-level behaviour in
`2026-04-20-diffah-v2-intra-layer-backend-resilience-design.md`. They
are *internal* correctness, encapsulation, and duplication fixes.

This design closes them as one cleanup increment. Every decision below
is locked — no new open questions.

Findings addressed:

1. **Missing context cancellation** — `internal/zstdpatch/cli.go:40,84`
   uses `exec.Command` (not `CommandContext`), so a cancelled ctx can't
   kill a hung zstd subprocess. Inconsistent with `runZstdVersion` in
   `available.go:62`, which correctly uses `CommandContext`.
2. **Error chain lost on timeout** — `available.go:68` returns
   `fmt.Errorf("zstd --version timed out")` without wrapping
   `ctx.Err()`. Callers can't `errors.Is(err, context.DeadlineExceeded)`.
3. **`ResetProbeCache` exported from production package** —
   `available.go:103-105`. Removed in `7ae865c` as "race-prone exported
   mutable state," re-added in `a32c4cf` because the integration test
   needs it. The underlying `sync.Once` cache itself is premature
   optimisation: `Available()` is called exactly once per
   Export/Import/DryRun, and the ~5 ms it saves is negligible against
   multi-second operation times.
4. **Broken `"Task 17"` skip** — `cmd/inspect_test.go:17,23` carries
   `t.Skip("rewritten in Task 17")` from a previous unrelated plan.
   `TestInspectCommand_PrintsSidecarFields` is dead; the new output
   lines have no end-to-end CLI coverage.
5. **inspect double-reads the sidecar** — `cmd/inspect.go:28-48` calls
   `ReadSidecar`+`ParseSidecar`, then `importer.DryRun`, which
   internally re-reads and re-parses the archive via `extractBundle`.
   The only reason `DryRun` is called at all is to obtain
   `RequiresZstd` and `ZstdAvailable` — both trivially derivable from
   the sidecar already in hand.

## 2. Scope and non-goals

**In scope:**

- Remove the `sync.Once`-based probe cache and `ResetProbeCache`.
  `Available(ctx)` probes on every call.
- Add ctx cancellation to the CLI-backed `Encode`/`Decode` (new signatures).
- Thread wrapped `ctx.Err()` through the probe-timeout error.
- Promote the private `sidecarHasPatch` helper to a public method
  `(*diff.Sidecar).RequiresZstd() bool`.
- Strip `importer.DryRun` from `cmd/inspect`. Compute the two booleans
  inline from the already-parsed sidecar.
- Delete the Task-17-skipped dead tests; replace with a subprocess
  integration test modelled on `cmd/export_integration_test.go`.
- Add `const modeRequired` to `pkg/exporter/resolvemode.go`.
- Rewrite two WHAT-narrating comments to explain WHY.

**Non-goals (deferred to a later cleanup pass):**

- Probe-type unification between exporter and importer packages.
- `fixtureImageName` / `fixtureBaselinePath` wrapper removal.
- Consolidating the three "default-if-nil" helpers
  (`defaultedProbe`, `defaultedWarnOut`, `probeOrDefault`).
- Sharing a `requireAllFullEncoding` helper across test packages.
- Hoisting `newMixedBundle`'s internal `t.Skip` to the caller. Current
  behaviour is accurate — the helper literally can't produce patches
  without zstd, CI has zstd, and the skip is benign.
- Optimising `cli.go`'s `MkdirTemp + WriteFile + exec + ReadFile`
  encode path. Out of scope per the original plan §13.

**Out of scope entirely:**

- Sidecar schema changes.
- CLI flag additions or renames.
- klauspost-vs-CLI parity threshold changes.
- CGO/embedded-zstd alternatives.

## 3. File-level changes

### 3.1 `internal/zstdpatch/available.go`

Simplify substantially.

- **Delete** `availableCtx` struct (finding #6 vanishes with #3).
- **Delete** `probeCache` package var.
- **Delete** `ResetProbeCache()`.
- **Rewrite** `Available(ctx)` as:

  ```go
  func Available(ctx context.Context) (ok bool, reason string) {
      return availableForTesting(ctx, exec.LookPath, runZstdVersion)
  }
  ```

- **Fix** `runZstdVersion`'s timeout branch:

  ```go
  if err := cmd.Run(); err != nil {
      if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
          return "", fmt.Errorf("zstd --version timed out: %w", ctxErr)
      }
      return "", err
  }
  ```

Net: file drops ~15 lines. `newErrZstdBinaryMissing`,
`availableForTesting`, `runZstdVersion`, `parseZstdVersion`, and
`firstLine` are unchanged.

Production cost: each `Available(ctx)` call now does one `LookPath` plus
one `zstd --version` subprocess (~5 ms). Callers invoke it at most once
per top-level Export/Import/DryRun; the aggregate cost is invisible
against the rest of the operation.

### 3.2 `internal/zstdpatch/cli.go`

Both `Encode` and `Decode` gain a `ctx` first parameter and use
`exec.CommandContext`.

```go
func Encode(ctx context.Context, ref, target []byte) ([]byte, error)
func Decode(ctx context.Context, ref, patch []byte) ([]byte, error)
```

Internals change from:

```go
cmd := exec.Command("zstd", ...)
```

to:

```go
cmd := exec.CommandContext(ctx, "zstd", ...)
```

Callers updated:

- `pkg/exporter/intralayer.go:104` (inside `(*Planner).Run` — already
  has `ctx`; thread it through to the `zstdpatch.Encode` call).
- `pkg/importer/compose.go:109` (inside `(*bundleImageSource).servePatch`
  — already has `ctx`; thread it through to the `zstdpatch.Decode`
  call).

`EncodeFull` / `DecodeFull` in `fullgo.go` stay ctx-less — they run
pure-Go with no subprocess or I/O to cancel.

The empty-target short-circuit (`if len(target) == 0 ...`) keeps its
behaviour; no ctx-awareness needed there.

### 3.3 `pkg/diff/sidecar.go`

Add one method on `*Sidecar`:

```go
// RequiresZstd reports whether this archive contains at least one
// intra-layer patch payload. Callers use this to decide whether the
// zstd binary is required at import time.
func (s *Sidecar) RequiresZstd() bool {
    for _, b := range s.Blobs {
        if b.Encoding == EncodingPatch {
            return true
        }
    }
    return false
}
```

One unit test in `pkg/diff/sidecar_test.go`: mixed + all-full fixtures.

### 3.4 `pkg/importer/importer.go`

- **Delete** the private `sidecarHasPatch` helper.
- Replace both call sites (in `Import` and in `DryRun`) with
  `bundle.sidecar.RequiresZstd()`.
- **Keep** `DryRunReport.{RequiresZstd, ZstdAvailable}` as-is —
  they're part of the public importer API and still useful for
  external consumers.
- **Fix** the narrating comment at `:174`. Replace
  `// reason is only used in Import's error path; DryRun discards it`
  with:

  ```go
  // DryRun is informational and must not fail on a missing probe —
  // callers want to know whether zstd is required, not be blocked by
  // its absence.
  ```

### 3.5 `cmd/inspect.go`

- **Delete** the `github.com/leosocy/diffah/pkg/importer` import.
- **Delete** the `importer.DryRun(...)` call.
- Inside `runInspect`, right after a successful `ParseSidecar`:

  ```go
  requiresZstd := s.RequiresZstd()
  zstdAvailable, _ := zstdpatch.Available(cmd.Context())
  return printBundleSidecar(cmd.OutOrStdout(), args[0], s, requiresZstd, zstdAvailable)
  ```

- Change `printBundleSidecar` signature:

  ```go
  func printBundleSidecar(w io.Writer, path string, s *diff.Sidecar,
      requiresZstd, zstdAvailable bool) error
  ```

- Output lines remain identical (`intra-layer patches required: yes|no`
  etc.) — only the plumbing changes.

Net effect: inspect reads the archive exactly once.

### 3.6 `pkg/exporter/resolvemode.go`

Add `const modeRequired = "required"` alongside `modeAuto` and
`modeOff`; use it in the switch case.

### 3.7 `pkg/exporter/exporter_test.go`

Rewrite the narrating comment at line 70-71. Replace

```
// Note: resolveMode returns an error before planPair is called, so the
// dummy paths are never touched. This tests the probe failure path only.
```

with

```
// Dummy paths are safe here because resolveMode runs before any
// file-touching work in buildBundle. If that ordering ever changes,
// this test will fail loudly on the dummy paths rather than silently
// skip the probe assertion.
```

## 4. Testing

### 4.1 Tests to delete

- `cmd/inspect_test.go`: `buildInspectTestDelta` (helper, line 16-20)
  and `TestInspectCommand_PrintsSidecarFields` (line 22-43). Both carry
  the dead `t.Skip("rewritten in Task 17")`. The output-formatting
  logic is already covered by `TestPrintBundleSidecar_PerImageStats`
  and `TestRunInspect_BundleSidecar_ParsesDirectly`.

### 4.2 Tests to add

- **`cmd/inspect_integration_test.go`** (new file): subprocess-driven
  end-to-end test modelled on `cmd/export_integration_test.go`. It:

  1. Builds a real bundle via `exporter.Export` against the existing
     `testdata/fixtures` fixtures.
  2. Runs `go run . inspect <bundle>` via `exec.Command` with the repo
     root as CWD.
  3. Asserts the stdout contains:
     - `archive: `
     - `images: 1`
     - `intra-layer patches required:`
     - `zstd available:`
     - `--- image: ` section header.

- **`pkg/diff/sidecar_test.go`**: one new test
  `TestSidecar_RequiresZstd` with two sub-cases (all-full ⇒ false,
  mixed ⇒ true). Reuses existing `diff.BlobEntry` fixtures.

### 4.3 Tests to update (signature only)

- `cmd/inspect_test.go`:
  - `TestPrintBundleSidecar_PerImageStats` — pass `true, true` instead
    of constructing `importer.DryRunReport`.
  - `TestRunInspect_BundleSidecar_ParsesDirectly` — pass `false, false`
    instead of `importer.DryRunReport{}`.
  - Drop the `importer` import.

- `pkg/importer/importer_test.go`: no functional changes.
  `TestImport_NeedsZstdMatrix` and `TestDryRun_ReportsNeedsZstdAndAvailable`
  still exercise `bundle.sidecar.RequiresZstd()` via `Import`/`DryRun`.

- `pkg/importer/integration_bundle_test.go`:
  `TestIntegration_AutoDowngradesUnderReducedPATH` — delete the
  `zstdpatch.ResetProbeCache()` line (the function is gone). The
  `t.Setenv("PATH", "")` now takes effect on every `Available()` call
  since there is no cache.

- `pkg/exporter/intralayer.go:104` call site of `zstdpatch.Encode`: adds `ctx`.
- `pkg/importer/compose.go:109` call site of `zstdpatch.Decode`: adds `ctx`.

### 4.4 Verification

- `go build ./...` clean.
- `go test ./...` clean (unit + integration).
- `golangci-lint run ./...` clean.
- Manual: `diffah inspect <patch-bearing-archive>` prints the two
  expected lines; timing shows roughly half the previous wall-clock on
  a large archive (no more double-parse).

## 5. Risks and mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| `Available` re-running per call adds visible perf cost | Low | Only ~5 ms per Export/Import/DryRun — measured, negligible |
| Integration test flakes when host lacks zstd | Low | `newMixedBundle` still skips gracefully; the new inspect E2E test uses `exporter.Export` which already handles host-zstd absence via auto-downgrade |
| Context cancellation mid-Encode leaves a temp dir behind | Low | `defer os.RemoveAll(dir)` is already in place; `CommandContext` only kills the subprocess |
| Someone relies on `ResetProbeCache` externally | None | `internal/` package — not an importable API |
| Test subprocess `go run .` is slow | Known | Already the pattern for `cmd/export_integration_test.go`; `testing.Short()` guard keeps `-short` runs fast |

## 6. Rollout

Single cleanup branch off `spec/v2-zstd-backend-resilience`:
`spec/v2-zstd-backend-resilience-cleanup`. One PR, one merge. No
migration, no CHANGELOG entry beyond a single bullet
("internal: removed redundant probe cache; inspect no longer double-reads
sidecar; added inspect subprocess integration test").

---

**End of design.**
