# diffah v2 — zstd Backend Resilience Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close five post-implementation review findings on the
`spec/v2-zstd-backend-resilience` branch: (1) thread `ctx` into
`zstdpatch.Encode`/`Decode` with `CommandContext`; (2) wrap `ctx.Err()`
on probe timeout; (3) delete the `sync.Once` probe cache and
`ResetProbeCache`; (4) drop dead `Task 17` skips from `cmd/inspect_test.go`
and add a subprocess integration test; (5) stop `cmd/inspect` from
double-reading the sidecar via `importer.DryRun`.

**Architecture:** All changes are internal. No archive format change,
no CLI flag change, no public-API break outside `zstdpatch` (whose
`Encode`/`Decode` signatures gain `ctx`). One new public helper —
`(*diff.Sidecar).RequiresZstd()` — replaces the private `sidecarHasPatch`
in the importer and is reused by `cmd/inspect`.

**Tech Stack:** Go 1.25, `github.com/stretchr/testify/require`, existing
`testdata/fixtures/v1_oci.tar` + `v2_oci.tar` pair. No new dependencies.

**Spec reference:** `docs/superpowers/specs/2026-04-22-diffah-v2-zstd-backend-resilience-cleanup-design.md`.

**Out of scope:** probe type unification across exporter/importer, fixture
wrapper removal, default-if-nil helper consolidation, `requireAllFullEncoding`
helper sharing, `newMixedBundle` skip hoisting, `cli.go` subprocess
performance work.

---

## File plan

| File | Action | Responsibility |
|---|---|---|
| `pkg/diff/sidecar.go` | modify | Add `(Sidecar).RequiresZstd()` method |
| `pkg/diff/sidecar_test.go` | modify | Add `TestSidecar_RequiresZstd` |
| `pkg/importer/importer.go` | modify | Delete `sidecarHasPatch`; use method; rewrite narrating comment |
| `internal/zstdpatch/available.go` | modify | Fix timeout error wrap; delete cache + `ResetProbeCache`; simplify `Available` |
| `internal/zstdpatch/available_test.go` | modify | Add timeout-wrap assertion |
| `pkg/importer/integration_bundle_test.go` | modify | Drop `zstdpatch.ResetProbeCache()` call |
| `internal/zstdpatch/cli.go` | modify | Add `ctx` to `Encode`/`Decode`; use `CommandContext` |
| `internal/zstdpatch/cli_test.go` | modify | Pass `ctx` in existing tests |
| `pkg/exporter/intralayer.go` | modify | Thread `ctx` into `zstdpatch.Encode` call |
| `pkg/importer/compose.go` | modify | Thread `ctx` into `zstdpatch.Decode` call |
| `cmd/inspect.go` | modify | Drop `importer.DryRun` call; inline `RequiresZstd` + `Available`; change `printBundleSidecar` signature |
| `cmd/inspect_test.go` | modify | Delete `buildInspectTestDelta` + `TestInspectCommand_PrintsSidecarFields`; update remaining tests to new signature |
| `cmd/inspect_integration_test.go` | create | Subprocess integration test for `diffah inspect` |
| `pkg/exporter/resolvemode.go` | modify | Add `modeRequired` const |
| `pkg/exporter/exporter_test.go` | modify | Rewrite narrating comment at `TestExport_RequiredMode_FailsWhenProbeMissing` |

## Task breakdown

### Task 1: Promote `sidecarHasPatch` to `(Sidecar).RequiresZstd()`

**Files:**
- Modify: `pkg/diff/sidecar.go`
- Modify: `pkg/diff/sidecar_test.go`
- Modify: `pkg/importer/importer.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/diff/sidecar_test.go`:

```go
func TestSidecar_RequiresZstd(t *testing.T) {
	t.Run("all full returns false", func(t *testing.T) {
		s := diff.Sidecar{
			Blobs: map[digest.Digest]diff.BlobEntry{
				"sha256:a": {Encoding: diff.EncodingFull},
				"sha256:b": {Encoding: diff.EncodingFull},
			},
		}
		require.False(t, s.RequiresZstd())
	})
	t.Run("any patch returns true", func(t *testing.T) {
		s := diff.Sidecar{
			Blobs: map[digest.Digest]diff.BlobEntry{
				"sha256:a": {Encoding: diff.EncodingFull},
				"sha256:b": {Encoding: diff.EncodingPatch},
			},
		}
		require.True(t, s.RequiresZstd())
	})
	t.Run("empty blobs returns false", func(t *testing.T) {
		s := diff.Sidecar{Blobs: map[digest.Digest]diff.BlobEntry{}}
		require.False(t, s.RequiresZstd())
	})
}
```

Ensure `pkg/diff/sidecar_test.go` already imports `github.com/opencontainers/go-digest`; add it if missing.

- [ ] **Step 2: Run — expect fail**

Run: `go test ./pkg/diff/ -run TestSidecar_RequiresZstd -v`
Expected: FAIL with `s.RequiresZstd undefined`.

- [ ] **Step 3: Implement the method**

Append to `pkg/diff/sidecar.go` (below `validateBlobEntry`):

```go
// RequiresZstd reports whether this archive contains at least one
// intra-layer patch payload. Importers and inspectors use this to
// decide whether the zstd binary is required at import time.
func (s Sidecar) RequiresZstd() bool {
	for _, b := range s.Blobs {
		if b.Encoding == EncodingPatch {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./pkg/diff/ -run TestSidecar_RequiresZstd -v`
Expected: PASS (all three sub-cases).

- [ ] **Step 5: Replace importer's private helper with the method**

Edit `pkg/importer/importer.go`:

1. Delete the private helper (currently lines ~47–54):

   ```go
   func sidecarHasPatch(sc *diff.Sidecar) bool {
   	for _, b := range sc.Blobs {
   		if b.Encoding == diff.EncodingPatch {
   			return true
   		}
   	}
   	return false
   }
   ```

2. In `Import` (around line 63), change:

   ```go
   if sidecarHasPatch(bundle.sidecar) {
   ```

   to:

   ```go
   if bundle.sidecar.RequiresZstd() {
   ```

3. In `DryRun` (around line 171), change:

   ```go
   requiresZstd := sidecarHasPatch(bundle.sidecar)
   ```

   to:

   ```go
   requiresZstd := bundle.sidecar.RequiresZstd()
   ```

- [ ] **Step 6: Run all importer + diff tests**

Run: `go test ./pkg/diff/... ./pkg/importer/... -v`
Expected: all pass (no behaviour change; just moved helper).

- [ ] **Step 7: Commit**

```bash
git add pkg/diff/sidecar.go pkg/diff/sidecar_test.go pkg/importer/importer.go
git commit -m "refactor(diff): promote sidecarHasPatch to Sidecar.RequiresZstd method

Exposes the patch-detection predicate as a public method on the
sidecar so cmd/inspect can stop going through importer.DryRun to
compute the same boolean."
```

### Task 2: Wrap `ctx.Err()` on probe timeout

**Files:**
- Modify: `internal/zstdpatch/available.go`
- Modify: `internal/zstdpatch/available_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/zstdpatch/available_test.go`:

```go
func TestRunZstdVersion_TimeoutWrapsCtxErr(t *testing.T) {
	// Simulate a slow binary by pointing at `sleep`.
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not on $PATH")
	}
	// Shorten the timeout by swapping the function out of context's way:
	// runZstdVersion has a 1s timeout; we invoke with a pre-cancelled ctx
	// to force the timeout branch deterministically.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // ensure ctx is already dead

	_, err = runZstdVersion(ctx, sleep) // will attempt `sleep --version`
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded),
		"expected errors.Is(err, context.DeadlineExceeded); got %v", err)
}
```

Add `"time"` to the imports of `available_test.go` if absent.

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/zstdpatch/ -run TestRunZstdVersion_TimeoutWrapsCtxErr -v`
Expected: FAIL — current code returns `fmt.Errorf("zstd --version timed out")` without `%w`, so `errors.Is(err, context.DeadlineExceeded)` returns false.

- [ ] **Step 3: Fix the wrap**

In `internal/zstdpatch/available.go`, locate `runZstdVersion` (around lines 59–73). Replace:

```go
if err := cmd.Run(); err != nil {
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("zstd --version timed out")
	}
	return "", err
}
```

with:

```go
if err := cmd.Run(); err != nil {
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
		return "", fmt.Errorf("zstd --version timed out: %w", ctxErr)
	}
	return "", err
}
```

The file already imports `"errors"`; no import change needed.

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/zstdpatch/ -run TestRunZstdVersion_TimeoutWrapsCtxErr -v`
Expected: PASS.

Then run the whole package: `go test ./internal/zstdpatch/... -v`
Expected: everything green (no regression).

- [ ] **Step 5: Commit**

```bash
git add internal/zstdpatch/available.go internal/zstdpatch/available_test.go
git commit -m "fix(zstdpatch): wrap ctx.Err() on probe timeout so callers can errors.Is

Previously the timeout branch returned a bare string, so
errors.Is(err, context.DeadlineExceeded) returned false even though
the context was the actual cause."
```

### Task 3: Remove the `sync.Once` probe cache and `ResetProbeCache`

**Files:**
- Modify: `internal/zstdpatch/available.go`
- Modify: `pkg/importer/integration_bundle_test.go`

- [ ] **Step 1: Drop the reset call in the integration test**

Edit `pkg/importer/integration_bundle_test.go`. Inside
`TestIntegration_AutoDowngradesUnderReducedPATH` (around line 451), delete
the line:

```go
zstdpatch.ResetProbeCache()
```

Also drop the `"github.com/leosocy/diffah/internal/zstdpatch"` import
from the file if it's no longer referenced. Run `goimports` or let the
Go toolchain flag it.

- [ ] **Step 2: Rewrite `available.go` with the cache removed**

Open `internal/zstdpatch/available.go`. Replace the current file contents
with:

```go
package zstdpatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

var ErrZstdBinaryMissing = errors.New("zstd binary required but unavailable")

func newErrZstdBinaryMissing(reason string) error {
	return fmt.Errorf("%w: %s", ErrZstdBinaryMissing, reason)
}

// Available reports whether zstd >= 1.5 is usable for patch-from
// encode/decode. Each call does a fresh LookPath + `zstd --version`;
// callers invoke Available at most once per top-level operation, so
// process-wide caching isn't worth the concurrency hazard.
func Available(ctx context.Context) (ok bool, reason string) {
	return availableForTesting(ctx, exec.LookPath, runZstdVersion)
}

func availableForTesting(
	ctx context.Context,
	lookup func(string) (string, error),
	version func(context.Context, string) (string, error),
) (ok bool, reason string) {
	path, err := lookup("zstd")
	if err != nil {
		return false, "zstd not on $PATH"
	}
	banner, err := version(ctx, path)
	if err != nil {
		return false, fmt.Sprintf("zstd --version failed: %v", err)
	}
	major, minor, matched, err := parseZstdVersion(banner)
	if err != nil {
		return false, fmt.Sprintf("zstd --version parse failed: %v", err)
	}
	if major < 1 || (major == 1 && minor < 5) {
		return false, fmt.Sprintf("zstd %s too old; need ≥1.5", matched)
	}
	return true, ""
}

func runZstdVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
			return "", fmt.Errorf("zstd --version timed out: %w", ctxErr)
		}
		return "", err
	}
	return out.String(), nil
}

var zstdVersionRE = regexp.MustCompile(`v?(\d+)\.(\d+)(?:\.\d+)?`)

func parseZstdVersion(banner string) (major, minor int, matched string, err error) {
	m := zstdVersionRE.FindStringSubmatch(banner)
	if m == nil {
		return 0, 0, "", fmt.Errorf("no version number in %q", firstLine(banner))
	}
	matched = m[0]
	major, err = strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, "", err
	}
	minor, err = strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, "", err
	}
	return major, minor, matched, nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	return s
}
```

Removed: `availableCtx` struct, `probeCache` var, `sync` import, and
`ResetProbeCache` function. `runZstdVersion` already has the `%w` wrap
from Task 2.

- [ ] **Step 3: Run the package**

Run: `go test ./internal/zstdpatch/... -v`
Expected: all pass. `TestAvailable_Table` still exercises
`availableForTesting`; `TestAvailable_RealPath` still calls it
directly; `TestRunZstdVersion_TimeoutWrapsCtxErr` still passes.

- [ ] **Step 4: Run the importer integration test with a scrubbed $PATH**

Run: `go test ./pkg/importer/ -run TestIntegration_AutoDowngradesUnderReducedPATH -v`
Expected: PASS. The test should now work without `ResetProbeCache`
because there is no cache to reset.

- [ ] **Step 5: Run the whole repo**

Run: `go test ./... -v`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/zstdpatch/available.go pkg/importer/integration_bundle_test.go
git commit -m "refactor(zstdpatch): drop sync.Once probe cache and ResetProbeCache

Available() ran at most once per top-level Export/Import/DryRun, so
the sync.Once saved at most a single LookPath + subprocess exec per
operation — invisible against multi-second operation times. Removing
the cache also removes the exported test-only ResetProbeCache, the
availableCtx struct, and a latent race between ResetProbeCache and
concurrent Available callers."
```

### Task 4: Add `ctx` to `zstdpatch.Encode`/`Decode` with `CommandContext`

**Files:**
- Modify: `internal/zstdpatch/cli.go`
- Modify: `internal/zstdpatch/cli_test.go`
- Modify: `pkg/exporter/intralayer.go`
- Modify: `pkg/importer/compose.go`

- [ ] **Step 1: Rewrite `cli.go`**

Replace `internal/zstdpatch/cli.go` entirely with:

```go
// Package zstdpatch — CLI-backed patch-from encode/decode.
//
// These functions shell out to `zstd ≥ 1.5`. EncodeFull / DecodeFull live
// in fullgo.go and do NOT require the CLI.
package zstdpatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Encode produces a zstd frame using --patch-from=ref that decodes to target.
// An empty target returns a precomputed empty frame to avoid invoking the CLI
// on a degenerate case that crashes older zstd builds. ctx cancellation kills
// the zstd subprocess.
func Encode(ctx context.Context, ref, target []byte) ([]byte, error) {
	if len(target) == 0 {
		return append([]byte(nil), emptyZstdFrame()...), nil
	}
	dir, err := os.MkdirTemp("", "zstdpatch-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	refPath := filepath.Join(dir, "ref")
	targetPath := filepath.Join(dir, "target")
	outPath := filepath.Join(dir, "patch.zst")

	if err := os.WriteFile(refPath, ref, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write ref: %w", err)
	}
	if err := os.WriteFile(targetPath, target, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write target: %w", err)
	}

	//nolint:gosec // G204: every argv path is created by this function via MkdirTemp; no user input reaches exec.Command.
	cmd := exec.CommandContext(ctx, "zstd",
		"-3", "--long=27",
		"--patch-from="+refPath,
		targetPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("zstdpatch: encode: %w\n%s", err, out)
	}

	patch, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: read patch: %w", err)
	}
	return patch, nil
}

// Decode reads a zstd frame produced by Encode and returns the original
// target bytes. ref must be byte-identical to the ref used at encode time.
// Callers are expected to verify the decoded bytes against the content
// digest recorded in the sidecar. ctx cancellation kills the zstd subprocess.
func Decode(ctx context.Context, ref, patch []byte) ([]byte, error) {
	if bytes.Equal(patch, emptyZstdFrame()) {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "zstdpatch-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	refPath := filepath.Join(dir, "ref")
	patchPath := filepath.Join(dir, "patch.zst")
	outPath := filepath.Join(dir, "target")

	if err := os.WriteFile(refPath, ref, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write ref: %w", err)
	}
	if err := os.WriteFile(patchPath, patch, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write patch: %w", err)
	}

	//nolint:gosec // G204: every argv path is mktempd above; no user input.
	cmd := exec.CommandContext(ctx, "zstd",
		"-d", "--long=27",
		"--patch-from="+refPath,
		patchPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("zstdpatch: decode: %w\n%s", err, out)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: read target: %w", err)
	}
	return data, nil
}
```

- [ ] **Step 2: Update `cli_test.go` call sites**

In `internal/zstdpatch/cli_test.go`, every call to `Encode(...)` and
`Decode(...)` needs `context.Background()` as the first argument.
Replace each occurrence:

```go
patch, err := Encode(ref, nil)
```

with:

```go
patch, err := Encode(context.Background(), ref, nil)
```

and similarly for `Decode`. Add `"context"` to the imports.

Concrete edits (line numbers are approximate — match the calls):

- `TestRoundTrip_Empty`: `Encode(ref, nil)` → `Encode(context.Background(), ref, nil)`; `Decode(ref, patch)` → `Decode(context.Background(), ref, patch)`.
- `TestRoundTrip_SmallDelta`: both calls.
- `TestDecode_WrongReference`: both calls (two `Decode` sites around the `err == nil` branch).

- [ ] **Step 3: Update the exporter call site**

Open `pkg/exporter/intralayer.go`. Around line 104, change:

```go
patch, err := zstdpatch.Encode(refBytes, target)
```

to:

```go
patch, err := zstdpatch.Encode(ctx, refBytes, target)
```

`ctx` is already in scope (function signature is
`func (p *Planner) Run(ctx context.Context, shipped []diff.BlobRef) (...)`).

- [ ] **Step 4: Update the importer call site**

Open `pkg/importer/compose.go`. Around line 109, change:

```go
out, err := zstdpatch.Decode(baseBytes, patchBytes)
```

to:

```go
out, err := zstdpatch.Decode(ctx, baseBytes, patchBytes)
```

`ctx` is already in scope (function is
`func (s *bundleImageSource) servePatch(ctx context.Context, ...)`).

- [ ] **Step 5: Add a cancellation test**

Append to `internal/zstdpatch/cli_test.go`:

```go
// TestEncode_CtxCancellation_KillsSubprocess — a cancelled ctx must
// kill the zstd subprocess instead of blocking until it completes.
func TestEncode_CtxCancellation_KillsSubprocess(t *testing.T) {
	skipWithoutZstd(t)
	// 64 MB target — big enough that zstd takes noticeable time.
	ref := make([]byte, 1<<26)
	_, _ = rand.Read(ref)
	target := append([]byte(nil), ref...)
	target[len(target)/2] ^= 0xFF

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling Encode

	_, err := Encode(ctx, ref, target)
	require.Error(t, err, "Encode must surface the cancellation")
}
```

- [ ] **Step 6: Run the package**

Run: `go test ./internal/zstdpatch/... -v`
Expected: all pass, including the new cancellation test.

- [ ] **Step 7: Run downstream packages**

Run: `go test ./pkg/exporter/... ./pkg/importer/... -v`
Expected: all pass. No behavioural change aside from ctx plumbing.

- [ ] **Step 8: Commit**

```bash
git add internal/zstdpatch/cli.go internal/zstdpatch/cli_test.go pkg/exporter/intralayer.go pkg/importer/compose.go
git commit -m "fix(zstdpatch): thread ctx into Encode/Decode via CommandContext

A cancelled ctx from the caller now kills the zstd subprocess instead
of waiting for it to finish. Brings cli.go in line with
runZstdVersion (available.go) which already used CommandContext."
```

### Task 5: Stop `cmd/inspect` from double-reading the sidecar

**Files:**
- Modify: `cmd/inspect.go`
- Modify: `cmd/inspect_test.go`

- [ ] **Step 1: Update `runInspect` to compute the two flags inline**

Edit `cmd/inspect.go`. Replace the current `runInspect` body (around
lines 28–48) with:

```go
func runInspect(cmd *cobra.Command, args []string) error {
	raw, err := archive.ReadSidecar(args[0])
	if err != nil {
		return err
	}
	s, err := diff.ParseSidecar(raw)
	if err != nil {
		var p1 *diff.ErrPhase1Archive
		if errors.As(err, &p1) {
			fmt.Fprintln(cmd.OutOrStdout(), "This archive uses the Phase 1 (single-image) schema.")
			fmt.Fprintln(cmd.OutOrStdout(), "Re-export with the current diffah to use the bundle format.")
			return nil
		}
		return err
	}
	requiresZstd := s.RequiresZstd()
	zstdAvailable, _ := zstdpatch.Available(cmd.Context())
	return printBundleSidecar(cmd.OutOrStdout(), args[0], s, requiresZstd, zstdAvailable)
}
```

Update imports: add `"github.com/leosocy/diffah/internal/zstdpatch"` and
delete `"github.com/leosocy/diffah/pkg/importer"` (it's no longer used).

- [ ] **Step 2: Update `printBundleSidecar` signature**

In the same file, change the function definition:

```go
func printBundleSidecar(w io.Writer, path string, s *diff.Sidecar, requiresZstd, zstdAvailable bool) error {
	bs := collectBundleStats(s)
	// ...existing output lines unchanged...
	fmt.Fprintf(w, "total archive: %d bytes\n", bs.totalArchiveSize)
	fmt.Fprintf(w, "intra-layer patches required: %s\n", yesNo(requiresZstd))
	fmt.Fprintf(w, "zstd available: %s\n", yesNo(zstdAvailable))
	// ...rest unchanged...
}
```

Concretely: replace the two fmt.Fprintf lines that previously referenced
`report.RequiresZstd` / `report.ZstdAvailable` with the new bool
parameters.

- [ ] **Step 3: Update `cmd/inspect_test.go` call sites**

Remove the `importer` import from `cmd/inspect_test.go`.

In `TestPrintBundleSidecar_PerImageStats`, delete:

```go
report := importer.DryRunReport{
	RequiresZstd:  true,
	ZstdAvailable: true,
}
```

and change:

```go
err := printBundleSidecar(&buf, "/tmp/bundle.tar", s, report)
```

to:

```go
err := printBundleSidecar(&buf, "/tmp/bundle.tar", s, true, true)
```

In `TestRunInspect_BundleSidecar_ParsesDirectly`, change:

```go
err = printBundleSidecar(&buf, "/tmp/bundle.tar", parsed, importer.DryRunReport{})
```

to:

```go
err = printBundleSidecar(&buf, "/tmp/bundle.tar", parsed, false, false)
```

- [ ] **Step 4: Run the cmd tests**

Run: `go test ./cmd/ -v`
Expected: all non-skipped tests pass. `TestInspectCommand_PrintsSidecarFields`
is still skipped with the broken Task-17 reference — that's Task 6's
cleanup, not this task's.

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect.go cmd/inspect_test.go
git commit -m "perf(inspect): stop double-reading the sidecar via importer.DryRun

runInspect was parsing the sidecar itself, then calling
importer.DryRun which internally re-extracted and re-parsed the same
archive — just to read two booleans. Compute RequiresZstd from the
already-parsed sidecar and ZstdAvailable by calling zstdpatch.Available
directly. Halves inspect I/O on large bundles."
```

### Task 6: Delete dead `Task 17` skips; add subprocess integration test for inspect

**Files:**
- Modify: `cmd/inspect_test.go`
- Create: `cmd/inspect_integration_test.go`

- [ ] **Step 1: Delete the dead functions**

Open `cmd/inspect_test.go`. Delete the entire `buildInspectTestDelta`
function (roughly lines 16–20) and the entire
`TestInspectCommand_PrintsSidecarFields` function (roughly lines 22–43).
Both carry `t.Skip("rewritten in Task 17")` — Task 17 does not exist in
any current plan. The remaining tests
(`TestPrintBundleSidecar_PerImageStats`, `TestRunInspect_Phase1Archive_PrintsHint`,
`TestRunInspect_BundleSidecar_ParsesDirectly`) already cover the output
format and error paths.

- [ ] **Step 2: Create the subprocess integration test**

Create `cmd/inspect_integration_test.go`:

```go
//go:build integration

package cmd_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInspectCommand_WithFixtures builds a real bundle via `diffah export`
// and runs `diffah inspect` against it, asserting the new surfaces:
// intra-layer patches required, zstd available, and the per-image section.
func TestInspectCommand_WithFixtures(t *testing.T) {
	root := findRepoRoot(t)
	bundlePath := filepath.Join(t.TempDir(), "bundle.tar")

	// Export a small bundle first.
	exportCmd := exec.Command(
		"go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"export",
		"--pair", "app="+filepath.Join(root, "testdata/fixtures/v1_oci.tar")+","+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
		bundlePath,
	)
	exportCmd.Dir = root
	exportOut, err := exportCmd.CombinedOutput()
	require.NoError(t, err, "export output: %s", exportOut)

	// Now inspect it.
	inspectCmd := exec.Command(
		"go", "run", "-tags", "containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper", ".",
		"inspect", bundlePath,
	)
	inspectCmd.Dir = root
	out, err := inspectCmd.CombinedOutput()
	require.NoError(t, err, "inspect output: %s", out)

	s := string(out)
	require.Contains(t, s, "archive: ")
	require.Contains(t, s, "images: 1")
	require.Contains(t, s, "intra-layer patches required:")
	require.Contains(t, s, "zstd available:")
	require.Contains(t, s, "--- image: app ---")
}
```

Note: `findRepoRoot` already exists in `cmd/export_integration_test.go`
within the same `cmd_test` package, so it's reusable.

- [ ] **Step 3: Run the new test with the integration tag**

Run: `go test -tags "integration containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper" ./cmd/ -run TestInspectCommand_WithFixtures -v`
Expected: PASS. The output should contain all the asserted substrings.

- [ ] **Step 4: Run the non-integration cmd suite to confirm deletions compile**

Run: `go test ./cmd/ -v`
Expected: all non-skipped tests pass. There should be no references to
`buildInspectTestDelta` or `TestInspectCommand_PrintsSidecarFields`.

- [ ] **Step 5: Commit**

```bash
git add cmd/inspect_test.go cmd/inspect_integration_test.go
git commit -m "test(inspect): delete dead Task-17 skips; add subprocess integration test

The skipped TestInspectCommand_PrintsSidecarFields referenced a
non-existent Task 17 from an earlier plan. Replace with a proper
subprocess test modelled on TestExportCommand_WithFixtures that
exercises the full inspect CLI path including the new
intra-layer/zstd output lines."
```

### Task 7: Add `modeRequired` const; rewrite narrating comments

**Files:**
- Modify: `pkg/exporter/resolvemode.go`
- Modify: `pkg/importer/importer.go`
- Modify: `pkg/exporter/exporter_test.go`

- [ ] **Step 1: Add the const and use it**

Open `pkg/exporter/resolvemode.go`. Change:

```go
const (
	modeAuto = "auto"
	modeOff  = "off"
)
```

to:

```go
const (
	modeAuto     = "auto"
	modeOff      = "off"
	modeRequired = "required"
)
```

In the switch statement, change:

```go
case "required":
```

to:

```go
case modeRequired:
```

Also update the error message so the string list stays aligned with the
constants (it already reads `"--intra-layer=%q not recognized; valid values: auto, off, required"` — no change needed to the text since it references the user-facing values, not the constants).

- [ ] **Step 2: Rewrite the narrating comment in `importer.go`**

Open `pkg/importer/importer.go`. Around line 174, replace:

```go
zstdAvailable, _ = opts.probeOrDefault()(ctx) // reason is only used in Import's error path; DryRun discards it
```

with:

```go
// DryRun is informational and must not fail on a missing probe —
// callers want to know whether zstd is required, not be blocked by
// its absence.
zstdAvailable, _ = opts.probeOrDefault()(ctx)
```

- [ ] **Step 3: Rewrite the narrating comment in `exporter_test.go`**

Open `pkg/exporter/exporter_test.go`. Find
`TestExport_RequiredMode_FailsWhenProbeMissing`. Replace the comment
block that currently reads:

```go
// Note: resolveMode returns an error before planPair is called, so the
// dummy paths are never touched. This tests the probe failure path only.
```

with:

```go
// Dummy paths are safe here because resolveMode runs before any
// file-touching work in buildBundle. If that ordering ever changes,
// this test will fail loudly on the dummy paths rather than silently
// skip the probe assertion.
```

- [ ] **Step 4: Run full suite**

Run: `go test ./... -v`
Expected: all pass.

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/resolvemode.go pkg/importer/importer.go pkg/exporter/exporter_test.go
git commit -m "chore: add modeRequired const; rewrite WHAT comments as WHY

The two rewritten comments now state the constraint (DryRun must not
fail; ordering invariant keeps the dummy paths safe) instead of
narrating what the code does."
```

---

## Verification checklist (run before declaring done)

Run from the repo root. All must pass.

- [ ] `go build ./...`
- [ ] `go test ./...` (full suite — respects existing build tags)
- [ ] `go test -count=1 ./internal/zstdpatch/...` (fresh cache, confirms cache deletion didn't leave stale state)
- [ ] `go test -tags "integration containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper" ./cmd/ -run 'TestInspectCommand_WithFixtures|TestExportCommand_WithFixtures|TestExportCommand_DryRun|TestExport_RejectsUnknownIntraLayerValue' -v` (both subprocess suites still green)
- [ ] `golangci-lint run ./...` → `0 issues.`
- [ ] `diffah inspect <any-patch-bearing-archive>` still prints the two lines (`intra-layer patches required:`, `zstd available:`).
- [ ] `grep -r "Task 17" cmd/ pkg/ internal/` returns nothing (no new dead references).
- [ ] `grep -r ResetProbeCache .` returns nothing outside the commit history itself.

## Self-review notes

- **Spec §3.1 (available.go)**: Task 2 wraps timeout; Task 3 removes cache, struct, reset function, and rewrites `Available`.
- **Spec §3.2 (cli.go)**: Task 4 adds ctx + CommandContext, updates callers in intralayer.go and compose.go.
- **Spec §3.3 (sidecar.go)**: Task 1 adds `RequiresZstd()` method + test.
- **Spec §3.4 (importer.go)**: Task 1 replaces `sidecarHasPatch` with the method; Task 7 rewrites the narrating comment at line 174.
- **Spec §3.5 (inspect.go)**: Task 5 drops the DryRun call, changes `printBundleSidecar` signature.
- **Spec §3.6 (resolvemode.go)**: Task 7 adds `modeRequired`.
- **Spec §3.7 (exporter_test.go)**: Task 7 rewrites the dummy-paths comment.
- **Spec §4.1 (tests to delete)**: Task 6 deletes the Task-17-skipped functions.
- **Spec §4.2 (tests to add)**: Task 1 adds `TestSidecar_RequiresZstd`; Task 6 adds the subprocess integration test.
- **Spec §4.3 (tests to update)**: Task 5 updates `cmd/inspect_test.go` for the new signature; Task 3 drops the `ResetProbeCache` call in `integration_bundle_test.go`; Task 4 updates `zstdpatch.Encode/Decode` call sites.
- **Spec §4.4 (verification)**: matches the checklist above.
- **Spec §5 (risks)**: `newMixedBundle`'s internal `t.Skip` is explicitly deferred (spec §2).
- **Spec §6 (rollout)**: single branch, single PR. No migration.

## Execution handoff

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task with review between tasks.
2. **Inline Execution** — execute tasks in this session with batched checkpoints.

Which approach?
