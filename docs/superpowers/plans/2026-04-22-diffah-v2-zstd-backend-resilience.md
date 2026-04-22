# diffah v2 — zstd Backend Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make diffah's zstd-binary dependency narrowly scoped and its failures actionable: `--intra-layer=auto` downgrades silently when zstd is missing, a new `--intra-layer=required` mode fails fast, `EncodeFull` runs pure-Go via klauspost/compress, and the importer pre-checks availability before touching blob files. No archive schema changes.

**Architecture:** One capability probe (`zstdpatch.Available`) backed by `exec.LookPath` + `zstd --version` parsing, cached via `sync.Once`. Exporter resolves mode at startup (hard-fail `required`, warn+downgrade `auto`→`off`). Importer probes only when the sidecar contains at least one `Encoding: patch` entry. `EncodeFull`/`DecodeFull` move to klauspost; `Encode`/`Decode` (patch-from) continue shelling out.

**Tech Stack:** Go 1.25, `github.com/klauspost/compress/zstd` (already a direct dep via `pkg/exporter/fingerprint.go`), `github.com/stretchr/testify/require`, `zstd ≥ 1.5` CLI on `$PATH` (required for patch-from export and for patch-containing imports only).

**Spec reference:** `docs/superpowers/specs/2026-04-20-diffah-v2-intra-layer-backend-resilience-design.md` (revised 2026-04-22).

**Out of scope:** embedded zstd via `go:embed`, CGO bindings, alternative binary-delta codecs, sidecar schema changes, compression-level tuning, `--fail-on-full-shipped` (deferred — see spec §12.4).

---

## File plan

| File | Action | Responsibility |
|---|---|---|
| `internal/zstdpatch/zstdpatch.go` | modify | Package doc + shared `emptyZstdFrame` helper only |
| `internal/zstdpatch/cli.go` | create | `Encode`/`Decode` (patch-from, shells out) |
| `internal/zstdpatch/fullgo.go` | create | `EncodeFull`/`DecodeFull` (pure Go, klauspost) |
| `internal/zstdpatch/available.go` | create | `Available(ctx)` probe + `ErrZstdBinaryMissing` sentinel + test hook |
| `internal/zstdpatch/cli_test.go` | create | Moved from zstdpatch_test.go; CLI-path tests |
| `internal/zstdpatch/fullgo_test.go` | create | klauspost EncodeFull/DecodeFull round-trip, size-parity, no-CLI assertion |
| `internal/zstdpatch/available_test.go` | create | Probe table-driven + real $PATH parity test |
| `internal/zstdpatch/zstdpatch_test.go` | delete | Replaced by cli_test.go + fullgo_test.go |
| `pkg/exporter/resolvemode.go` | create | `resolveMode` + wrapping of `ErrZstdBinaryMissing` from internal/zstdpatch |
| `pkg/exporter/resolvemode_test.go` | create | Five-row table-driven coverage of resolveMode |
| `pkg/exporter/exporter.go` | modify | Add `Probe` + `WarnOut` fields; call `resolveMode` in `buildBundle`; pass effective mode through |
| `pkg/exporter/exporter_test.go` | modify | Add probe-injection integration cases |
| `pkg/importer/importer.go` | modify | `needsZstd` check + probe before `resolveBaselines` + new `DryRunReport` fields |
| `pkg/importer/importer_test.go` | create | Four-combo matrix `{patch?} × {probe?}` on Import and DryRun |
| `pkg/importer/integration_bundle_test.go` | modify | Add reduced-`$PATH` round-trip scenario |
| `cmd/export.go` | modify | Flag validation accepts `required`, rejects unknowns; flag help mentions all three values |
| `cmd/export_integration_test.go` | modify | Cover new flag value + error path |
| `cmd/inspect.go` | modify | Surface `requires zstd` + `zstd available` lines for patch-containing archives |
| `cmd/inspect_test.go` | modify | Assert new output lines appear |
| `README.md` | modify | Rewrite Requirements section to match spec §11 wording |

## Task breakdown

### Task 0: Spike — validate klauspost EncodeFull ratio parity

**Purpose:** Confirm klauspost `SpeedDefault` + `WithWindowSize(1<<27)` produces a byte count within ±5% of CLI `zstd -3 --long=27` on real layer bytes. If it fails, the plan narrows — only `Available` probe + `required` mode get implemented; `EncodeFull` stays on the CLI. Spec §10.1 covers the narrowing path.

**Files:**
- Create: `internal/zstdpatch/spike_test.go` (temporary; deleted in Task 2)

- [ ] **Step 1: Write the spike test**

```go
//go:build spike

package zstdpatch

import (
	"bytes"
	"os/exec"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestSpike_KlauspostEncodeFullRatio(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found; cannot compare")
	}
	sizes := []int{1 << 10, 1 << 20, 1 << 24} // 1KB, 1MB, 16MB — 200MB optional via env
	for _, n := range sizes {
		target := make([]byte, n)
		for i := range target {
			target[i] = byte(i * 1103515245) // deterministic, non-random pattern
		}
		cliBytes, err := EncodeFull(target) // current CLI-backed implementation
		require.NoError(t, err)

		enc, err := zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithWindowSize(1<<27),
		)
		require.NoError(t, err)
		klauspostBytes := enc.EncodeAll(target, nil)
		_ = enc.Close()

		ratio := float64(len(klauspostBytes)) / float64(len(cliBytes))
		t.Logf("size=%d cli=%d klauspost=%d ratio=%.3f", n, len(cliBytes), len(klauspostBytes), ratio)
		require.InDelta(t, 1.0, ratio, 0.05,
			"klauspost EncodeFull must track CLI within ±5%%; got %.3f at size %d", ratio, n)

		dec, err := zstd.NewReader(bytes.NewReader(klauspostBytes),
			zstd.WithDecoderMaxWindow(1<<27),
		)
		require.NoError(t, err)
		roundtrip, err := dec.DecodeAll(klauspostBytes, nil)
		dec.Close()
		require.NoError(t, err)
		require.True(t, bytes.Equal(roundtrip, target), "round-trip mismatch at size %d", n)
	}
}
```

- [ ] **Step 2: Run spike**

Run: `go test -tags spike ./internal/zstdpatch/ -run TestSpike_KlauspostEncodeFullRatio -v`
Expected: PASS with logged `ratio=` values, each within ±5% of 1.0.

- [ ] **Step 3: Decide branch**

If PASS → continue to Task 1.
If FAIL → rerun with `zstd.SpeedBetterCompression`. If that also fails, update the spec (add decision note on narrowed scope), delete this spike, and skip Tasks 2 entirely; Task 3+ proceed with CLI-backed `EncodeFull`.

- [ ] **Step 4: Do NOT commit the spike**

This test stays local until Task 2 replaces it with a permanent parity test. `git status` should show the spike file untracked; do not `git add` it yet.

### Task 1: Refactor zstdpatch.go — split CLI functions into cli.go (no behaviour change)

**Files:**
- Modify: `internal/zstdpatch/zstdpatch.go`
- Create: `internal/zstdpatch/cli.go`
- Create: `internal/zstdpatch/cli_test.go`
- Delete: `internal/zstdpatch/zstdpatch_test.go` (after copying content to cli_test.go)

- [ ] **Step 1: Create `cli.go` with Encode + Decode moved verbatim**

```go
// Package zstdpatch — CLI-backed patch-from encode/decode.
//
// These functions shell out to `zstd ≥ 1.5`. The pure-Go EncodeFull /
// DecodeFull live in fullgo.go and do NOT require the CLI.

package zstdpatch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Encode produces a zstd frame using --patch-from=ref that decodes to target.
// An empty target returns a precomputed empty frame to avoid invoking the CLI
// on a degenerate case that crashes older zstd builds.
func Encode(ref, target []byte) ([]byte, error) {
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
	cmd := exec.Command("zstd",
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
// digest recorded in the sidecar.
func Decode(ref, patch []byte) ([]byte, error) {
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
	cmd := exec.Command("zstd",
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

- [ ] **Step 2: Overwrite `zstdpatch.go` to keep only package doc + shared helper**

```go
// Package zstdpatch implements zstd --patch-from style byte-level deltas
// and plain zstd encode/decode.
//
// Two backends live in this package:
//   - cli.go      — Encode / Decode (shells out to `zstd ≥ 1.5` for --patch-from)
//   - fullgo.go   — EncodeFull / DecodeFull (pure Go, via github.com/klauspost/compress)
//
// Availability of the CLI backend can be queried via Available(ctx)
// (see available.go).
package zstdpatch

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// emptyZstdFrame returns a canonical zstd frame that decodes to zero bytes.
// Generated once via klauspost/compress so it is guaranteed standards-compliant.
//
// Short-circuiting empty payloads avoids a known assertion failure
// (FIO_highbit64: v != 0) in the zstd CLI < 1.5.x when asked to encode
// an empty file.
var emptyZstdFrame = sync.OnceValue(func() []byte {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		panic(fmt.Sprintf("zstdpatch: klauspost NewWriter: %v", err))
	}
	out := enc.EncodeAll(nil, nil)
	_ = enc.Close()
	return out
})
```

(Delete the old `Encode`, `Decode`, `EncodeFull`, `DecodeFull` bodies from `zstdpatch.go`; they now live in `cli.go` — and `EncodeFull`/`DecodeFull` will be rewritten in Task 2.)

- [ ] **Step 3: Temporarily move `EncodeFull` + `DecodeFull` into cli.go so the build stays green between Task 1 and Task 2**

Append to `cli.go`:

```go
// EncodeFull compresses target as a standalone zstd frame (no reference).
// NOTE: reimplemented in fullgo.go under Task 2.
func EncodeFull(target []byte) ([]byte, error) {
	if len(target) == 0 {
		return append([]byte(nil), emptyZstdFrame()...), nil
	}
	dir, err := os.MkdirTemp("", "zstdpatch-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	targetPath := filepath.Join(dir, "target")
	outPath := filepath.Join(dir, "target.zst")

	if err := os.WriteFile(targetPath, target, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write target: %w", err)
	}

	cmd := exec.Command("zstd",
		"-3", "--long=27",
		targetPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("zstdpatch: encode full: %w\n%s", err, out)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: read output: %w", err)
	}
	return data, nil
}

// DecodeFull reads a standalone zstd frame (no reference).
// NOTE: reimplemented in fullgo.go under Task 2.
func DecodeFull(data []byte) ([]byte, error) {
	if bytesEqual(data, emptyZstdFrame()) {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "zstdpatch-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	inPath := filepath.Join(dir, "input.zst")
	outPath := filepath.Join(dir, "output")

	if err := os.WriteFile(inPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write input: %w", err)
	}

	cmd := exec.Command("zstd",
		"-d", "--long=27",
		inPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("zstdpatch: decode full: %w\n%s", err, out)
	}

	result, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: read output: %w", err)
	}
	return result, nil
}
```

- [ ] **Step 4: Rename existing test file to cli_test.go verbatim**

Move every `func Test*` from `zstdpatch_test.go` into `cli_test.go` unchanged. Delete `zstdpatch_test.go`.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/zstdpatch/... -v`
Expected: all pre-existing tests pass (`TestRoundTrip_Empty`, `TestRoundTrip_SmallDelta`, `TestDecode_WrongReference`, `TestEncodeFull_RoundTrip`).

- [ ] **Step 6: Commit**

```bash
git add internal/zstdpatch/
git rm internal/zstdpatch/zstdpatch_test.go
git commit -m "refactor(zstdpatch): split CLI functions into cli.go for two-backend prep"
```

### Task 2: Move EncodeFull / DecodeFull to klauspost in fullgo.go

**Files:**
- Create: `internal/zstdpatch/fullgo.go`
- Create: `internal/zstdpatch/fullgo_test.go`
- Modify: `internal/zstdpatch/cli.go` (remove the temporary `EncodeFull`/`DecodeFull` stubs)
- Modify: `internal/zstdpatch/cli_test.go` (move the `TestEncodeFull_RoundTrip` case into fullgo_test.go and drop the `skipWithoutZstd` guard for that test)

- [ ] **Step 1: Write failing test in `fullgo_test.go`**

```go
package zstdpatch

import (
	"bytes"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEncodeFull_RoundTrip_NoCLI — klauspost EncodeFull must round-trip
// through DecodeFull without needing the zstd binary on $PATH. Runs with
// $PATH explicitly scrubbed to catch any accidental shell-out.
func TestEncodeFull_RoundTrip_NoCLI(t *testing.T) {
	t.Setenv("PATH", "")
	target := bytes.Repeat([]byte("hello, diffah "), 1<<10)

	compressed, err := EncodeFull(target)
	require.NoError(t, err)
	require.Less(t, len(compressed), len(target))

	got, err := DecodeFull(compressed)
	require.NoError(t, err)
	require.True(t, bytes.Equal(got, target))

	// Sanity check: $PATH really was empty — a zstd exec would have failed.
	_, lookErr := exec.LookPath("zstd")
	require.Error(t, lookErr, "PATH must be empty for this test to be meaningful")
}

// TestEncodeFull_SizeParityVsCLI — klauspost bytes-out must stay within ±5%
// of CLI zstd -3 --long=27 across 1KB, 1MB, 16MB (200MB gated via env).
// Skip when CLI is missing — parity is by definition untestable then.
func TestEncodeFull_SizeParityVsCLI(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; parity cannot be measured")
	}
	sizes := []int{1 << 10, 1 << 20, 1 << 24}
	for _, n := range sizes {
		target := make([]byte, n)
		// Deterministic pattern so regressions are reproducible.
		for i := range target {
			target[i] = byte(i * 1103515245)
		}

		klauspostBytes, err := EncodeFull(target)
		require.NoError(t, err)

		dir := t.TempDir()
		inPath := filepath.Join(dir, "target")
		outPath := filepath.Join(dir, "target.zst")
		require.NoError(t, os.WriteFile(inPath, target, 0o600))
		cmd := exec.Command("zstd", "-3", "--long=27", inPath, "-o", outPath, "-f", "-q")
		require.NoError(t, cmd.Run())
		cliBytes, err := os.ReadFile(outPath)
		require.NoError(t, err)

		ratio := float64(len(klauspostBytes)) / float64(len(cliBytes))
		t.Logf("size=%d cli=%d klauspost=%d ratio=%.3f", n, len(cliBytes), len(klauspostBytes), ratio)
		require.InDelta(t, 1.0, ratio, 0.05,
			"klauspost EncodeFull must track CLI ±5%% at size %d (ratio %.3f)", n, ratio)
	}
}

// TestEncodeFull_Empty — empty target returns the canonical empty frame,
// and DecodeFull returns nil (not []byte{}) — matches Decode's empty contract.
func TestEncodeFull_Empty(t *testing.T) {
	compressed, err := EncodeFull(nil)
	require.NoError(t, err)
	got, err := DecodeFull(compressed)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestEncodeFull_RandomBinary — DecodeFull recovers random bytes byte-exactly.
func TestEncodeFull_RandomBinary(t *testing.T) {
	target := make([]byte, 1<<18)
	_, _ = rand.Read(target)

	compressed, err := EncodeFull(target)
	require.NoError(t, err)

	got, err := DecodeFull(compressed)
	require.NoError(t, err)
	require.True(t, bytes.Equal(got, target))
}
```

- [ ] **Step 2: Run tests — expect fail**

Run: `go test ./internal/zstdpatch/ -run 'TestEncodeFull_(RoundTrip_NoCLI|RandomBinary|SizeParityVsCLI|Empty)$' -v`
Expected: `TestEncodeFull_RoundTrip_NoCLI` FAILS (because `EncodeFull` still shells out through temporary CLI stub). Others may pass coincidentally.

- [ ] **Step 3: Implement `fullgo.go`**

```go
// Package zstdpatch — pure-Go plain-zstd encode/decode via klauspost/compress.
//
// These functions do NOT require the zstd binary. They are used by the
// exporter for the size-ceiling comparison in intralayer.go, and kept in
// the API for decoder symmetry (no current production caller decodes
// zstd-full bytes — see spec §1 and §4.4).

package zstdpatch

import (
	"bytes"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// EncodeFull compresses target as a standalone zstd frame with parameters
// matching the CLI's `-3 --long=27` settings so the size-ceiling comparison
// against a patch-from payload stays consistent whether the CLI is on PATH
// or not.
func EncodeFull(target []byte) ([]byte, error) {
	if len(target) == 0 {
		return append([]byte(nil), emptyZstdFrame()...), nil
	}
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithWindowSize(1<<27),
	)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: new encoder: %w", err)
	}
	defer enc.Close()
	return enc.EncodeAll(target, nil), nil
}

// DecodeFull reads a standalone zstd frame. Kept in the API for symmetry;
// no current production call path invokes it (encoding: full = raw layer
// bytes; encoding: patch decodes via Decode, not DecodeFull).
// WithDecoderMaxWindow matches the encoder for defensive parity.
func DecodeFull(data []byte) ([]byte, error) {
	if bytes.Equal(data, emptyZstdFrame()) {
		return nil, nil
	}
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderMaxWindow(1<<27),
	)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: new decoder: %w", err)
	}
	defer dec.Close()
	out, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: decode full: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 4: Remove the temporary `EncodeFull`/`DecodeFull` + `os/exec` + `os` + `path/filepath` imports from cli.go that only those two functions used**

Update `cli.go`'s import list to only the packages still needed by `Encode`/`Decode` (`fmt`, `os`, `os/exec`, `path/filepath`). Delete the two temporary function bodies added in Task 1, Step 3.

- [ ] **Step 5: Remove the redundant `TestEncodeFull_RoundTrip` in cli_test.go**

The klauspost tests in `fullgo_test.go` cover it. Delete the CLI-era version and its `skipWithoutZstd` invocation.

- [ ] **Step 6: Run the full package**

Run: `go test ./internal/zstdpatch/... -v`
Expected: every test passes. `TestEncodeFull_RoundTrip_NoCLI` passes because klauspost needs no CLI; `TestEncodeFull_SizeParityVsCLI` passes if CLI is on PATH, skips otherwise.

- [ ] **Step 7: Delete the spike file from Task 0**

`rm internal/zstdpatch/spike_test.go`. The spike's job is done; parity is now guarded by `TestEncodeFull_SizeParityVsCLI`.

- [ ] **Step 8: Commit**

```bash
git add internal/zstdpatch/
git commit -m "refactor(zstdpatch): move EncodeFull/DecodeFull to klauspost/compress

No more zstd CLI dependency for the size-ceiling comparison in
pkg/exporter/intralayer.go:108. Patch-from Encode/Decode still shell out."
```

### Task 3: Capability probe — `zstdpatch.Available` + `ErrZstdBinaryMissing`

**Files:**
- Create: `internal/zstdpatch/available.go`
- Create: `internal/zstdpatch/available_test.go`

- [ ] **Step 1: Write failing tests**

```go
package zstdpatch

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAvailable_Table(t *testing.T) {
	cases := []struct {
		name        string
		lookup      func(string) (string, error)
		version     func(context.Context, string) (string, error)
		wantOK      bool
		reasonHint  string
	}{
		{
			name:       "missing binary",
			lookup:     func(string) (string, error) { return "", exec.ErrNotFound },
			wantOK:     false,
			reasonHint: "not on $PATH",
		},
		{
			name:   "supported unix banner",
			lookup: func(string) (string, error) { return "/usr/bin/zstd", nil },
			version: func(context.Context, string) (string, error) {
				return "*** zstd command line interface 64-bits v1.5.6 ***\n", nil
			},
			wantOK: true,
		},
		{
			name:   "supported chocolatey banner",
			lookup: func(string) (string, error) { return "C:\\tools\\zstd.exe", nil },
			version: func(context.Context, string) (string, error) {
				return "zstd 1.5.6\n", nil
			},
			wantOK: true,
		},
		{
			name:   "too old",
			lookup: func(string) (string, error) { return "/usr/bin/zstd", nil },
			version: func(context.Context, string) (string, error) {
				return "*** zstd v1.4.4 ***\n", nil
			},
			wantOK:     false,
			reasonHint: "1.4.4",
		},
		{
			name:   "unparseable banner",
			lookup: func(string) (string, error) { return "/usr/bin/zstd", nil },
			version: func(context.Context, string) (string, error) {
				return "this is not a version string\n", nil
			},
			wantOK:     false,
			reasonHint: "parse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := availableForTesting(context.Background(), tc.lookup, tc.version)
			require.Equal(t, tc.wantOK, ok, "reason=%q", reason)
			if tc.reasonHint != "" {
				require.Contains(t, reason, tc.reasonHint)
			}
		})
	}
}

func TestAvailable_RealPath(t *testing.T) {
	resetAvailableForTesting()
	t.Setenv("PATH", "")
	ok, reason := Available(context.Background())
	require.False(t, ok)
	require.Contains(t, reason, "$PATH")
}

func TestErrZstdBinaryMissing_IsSentinel(t *testing.T) {
	wrapped := newErrZstdBinaryMissing("zstd 1.4.4 too old; need ≥1.5")
	require.True(t, errors.Is(wrapped, ErrZstdBinaryMissing))
	require.Contains(t, wrapped.Error(), "1.4.4")
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/zstdpatch/ -run TestAvailable -v`
Expected: FAIL with undefined `availableForTesting`, `Available`, `resetAvailableForTesting`, `newErrZstdBinaryMissing`, `ErrZstdBinaryMissing`.

- [ ] **Step 3: Implement `available.go`**

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
	"sync"
	"time"
)

// ErrZstdBinaryMissing is the sentinel wrapped by every error the probe
// or its consumers return when zstd is unavailable at usable version.
// Callers should use errors.Is for matching.
var ErrZstdBinaryMissing = errors.New("zstd binary required but unavailable")

func newErrZstdBinaryMissing(reason string) error {
	return fmt.Errorf("%w: %s", ErrZstdBinaryMissing, reason)
}

// Available reports whether zstd ≥ 1.5 is usable for patch-from encode/decode.
// Result is cached per-process via sync.Once. Tests use availableForTesting
// (uncached, dependency-injected) instead.
func Available(ctx context.Context) (ok bool, reason string) {
	availableOnce.Do(func() {
		availableOK, availableReason = availableForTesting(ctx, exec.LookPath, runZstdVersion)
	})
	return availableOK, availableReason
}

var (
	availableOnce     sync.Once
	availableOK       bool
	availableReason   string
)

// resetAvailableForTesting clears the sync.Once cache so a test that
// changes $PATH sees fresh state. Only used in tests.
func resetAvailableForTesting() {
	availableOnce = sync.Once{}
	availableOK = false
	availableReason = ""
}

// availableForTesting runs the probe with injected dependencies (no cache).
// Production uses the wrapping cached Available().
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
	major, minor, err := parseZstdVersion(banner)
	if err != nil {
		return false, fmt.Sprintf("zstd --version parse failed: %v", err)
	}
	if major < 1 || (major == 1 && minor < 5) {
		return false, fmt.Sprintf("zstd %d.%d too old; need ≥1.5", major, minor)
	}
	return true, ""
}

func runZstdVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	//nolint:gosec // G204: path is the exec.LookPath result for the literal "zstd"; no user input reaches exec.Command.
	cmd := exec.CommandContext(ctx, path, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("zstd --version timed out")
		}
		return "", err
	}
	return out.String(), nil
}

var zstdVersionRE = regexp.MustCompile(`v?(\d+)\.(\d+)(?:\.\d+)?`)

func parseZstdVersion(banner string) (major, minor int, err error) {
	m := zstdVersionRE.FindStringSubmatch(banner)
	if m == nil {
		return 0, 0, fmt.Errorf("no version number in %q", firstLine(banner))
	}
	major, err = strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, err
	}
	minor, err = strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, err
	}
	return major, minor, nil
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

- [ ] **Step 4: Run tests — expect pass**

Run: `go test ./internal/zstdpatch/... -v`
Expected: all tests pass, including the new `TestAvailable_*` and `TestErrZstdBinaryMissing_IsSentinel`.

- [ ] **Step 5: Commit**

```bash
git add internal/zstdpatch/available.go internal/zstdpatch/available_test.go
git commit -m "feat(zstdpatch): add Available probe and ErrZstdBinaryMissing sentinel

Probe runs exec.LookPath + zstd --version, parses major.minor, requires
>=1.5, caches via sync.Once. Version regex tolerant of Unix and
Windows/Chocolatey banners."
```

### Task 4: Exporter resolveMode + Options plumbing

**Files:**
- Create: `pkg/exporter/resolvemode.go`
- Create: `pkg/exporter/resolvemode_test.go`
- Modify: `pkg/exporter/exporter.go`

- [ ] **Step 1: Write failing tests for `resolveMode`**

```go
package exporter

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/zstdpatch"
)

func TestResolveMode_Table(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		probeOK     bool
		reason      string
		wantEff     string
		wantWarn    string
		wantErrIs   error
	}{
		{"auto+ok", "auto", true, "", "auto", "", nil},
		{"empty+ok_defaults_to_auto", "", true, "", "auto", "", nil},
		{"auto+missing_downgrades", "auto", false, "zstd not on $PATH", "off",
			"diffah: zstd not on $PATH; disabling intra-layer for this run\n", nil},
		{"off_skips_probe_even_when_missing", "off", false, "zstd not on $PATH", "off", "", nil},
		{"required+ok", "required", true, "", "auto", "", nil},
		{"required+missing_hardfails", "required", false, "zstd not on $PATH", "", "",
			zstdpatch.ErrZstdBinaryMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := func(context.Context) (bool, string) { return tc.probeOK, tc.reason }
			var warn bytes.Buffer
			eff, err := resolveMode(context.Background(), tc.input, probe, &warn)
			if tc.wantErrIs != nil {
				require.Error(t, err)
				require.True(t, errors.Is(err, tc.wantErrIs))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantEff, eff)
			require.Equal(t, tc.wantWarn, warn.String())
		})
	}
}

func TestResolveMode_UnknownValueRejected(t *testing.T) {
	probe := func(context.Context) (bool, string) { return true, "" }
	_, err := resolveMode(context.Background(), "aggressive", probe, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--intra-layer")
	require.Contains(t, err.Error(), "aggressive")
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./pkg/exporter/ -run TestResolveMode -v`
Expected: FAIL with undefined `resolveMode`.

- [ ] **Step 3: Implement `resolvemode.go`**

```go
package exporter

import (
	"context"
	"fmt"
	"io"

	"github.com/leosocy/diffah/internal/zstdpatch"
)

// Probe matches the zstdpatch.Available signature so tests can inject
// stubs without needing the real binary on $PATH.
type Probe func(context.Context) (ok bool, reason string)

// resolveMode turns the user's --intra-layer value + a probe result into
// the effective planning mode. See spec §5 for the decision table.
//
//	user mode   probe ok   effective   warning                                          error
//	auto/""     true       auto        —                                                —
//	auto/""     false      off         "diffah: <reason>; disabling intra-layer ..."    —
//	off         —          off         —                                                —
//	required    true       auto        —                                                —
//	required    false      —           —                                                ErrZstdBinaryMissing
//
// Any other user mode produces a flag-validation error.
func resolveMode(
	ctx context.Context, userMode string, probe Probe, warn io.Writer,
) (effective string, err error) {
	if userMode == "" {
		userMode = "auto"
	}
	switch userMode {
	case "auto":
		ok, reason := probe(ctx)
		if ok {
			return "auto", nil
		}
		if warn != nil {
			fmt.Fprintf(warn, "diffah: %s; disabling intra-layer for this run\n", reason)
		}
		return "off", nil
	case "off":
		return "off", nil
	case "required":
		ok, reason := probe(ctx)
		if ok {
			return "auto", nil
		}
		return "", fmt.Errorf("%w: %s", zstdpatch.ErrZstdBinaryMissing, reason)
	default:
		return "", fmt.Errorf(
			"--intra-layer=%q not recognized; valid values: auto, off, required",
			userMode)
	}
}
```

- [ ] **Step 4: Add `Probe` + `WarnOut` fields to `Options` and wire `resolveMode` into `buildBundle`**

Edit `pkg/exporter/exporter.go`:

```go
import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/leosocy/diffah/internal/zstdpatch"
)

type Options struct {
	Pairs       []Pair
	Platform    string
	Compress    string
	OutputPath  string
	ToolVersion string
	IntraLayer  string
	CreatedAt   time.Time
	Progress    io.Writer

	// Probe reports zstd availability. Defaults to zstdpatch.Available
	// when nil. Tests inject stubs.
	Probe Probe
	// WarnOut receives the one-line downgrade warning in auto + !probe.
	// Defaults to os.Stderr when nil.
	WarnOut io.Writer

	fingerprinter Fingerprinter
}

// defaultedProbe returns opts.Probe, falling back to zstdpatch.Available.
func (o *Options) defaultedProbe() Probe {
	if o.Probe != nil {
		return o.Probe
	}
	return func(ctx context.Context) (bool, string) {
		return zstdpatch.Available(ctx)
	}
}

// defaultedWarnOut returns opts.WarnOut, falling back to os.Stderr.
func (o *Options) defaultedWarnOut() io.Writer {
	if o.WarnOut != nil {
		return o.WarnOut
	}
	return os.Stderr
}
```

Then in `buildBundle`, call `resolveMode` **before** any planning work:

```go
func buildBundle(ctx context.Context, opts *Options) (*builtBundle, error) {
	if err := ValidatePairs(opts.Pairs); err != nil {
		return nil, err
	}
	effectiveMode, err := resolveMode(
		ctx, opts.IntraLayer, opts.defaultedProbe(), opts.defaultedWarnOut())
	if err != nil {
		return nil, err
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().UTC()
	}
	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "planning %d pairs...\n", len(opts.Pairs))
	}

	// ... existing planning loop unchanged ...

	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.Progress); err != nil {
		return nil, fmt.Errorf("encode shipped layers: %w", err)
	}
	// ... rest unchanged ...
}
```

(The call to `encodeShipped` changes only from `opts.IntraLayer` to `effectiveMode` — one argument swap.)

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/exporter/ -run TestResolveMode -v`
Expected: PASS — table + unknown-value tests green.

Also run: `go test ./pkg/exporter/... -v`
Expected: all pre-existing tests still pass (no behaviour change for `auto`/`off` when probe succeeds/is absent).

- [ ] **Step 6: Commit**

```bash
git add pkg/exporter/resolvemode.go pkg/exporter/resolvemode_test.go pkg/exporter/exporter.go
git commit -m "feat(exporter): add resolveMode + Probe/WarnOut options

--intra-layer=required is accepted, hard-fails when zstd is missing;
auto downgrades to off with a stderr warning. Probe defaults to
zstdpatch.Available."
```

### Task 5: Integration test — probe-injected Export end-to-end

**Files:**
- Modify: `pkg/exporter/exporter_test.go`

- [ ] **Step 1: Write failing test harness**

Append to `pkg/exporter/exporter_test.go`:

```go
func TestExport_RequiredMode_FailsWhenProbeMissing(t *testing.T) {
	tmp := t.TempDir()
	opts := exporter.Options{
		Pairs:       []exporter.Pair{{Name: "a", BaselinePath: "does-not-matter", TargetPath: "ditto"}},
		Platform:    "linux/amd64",
		IntraLayer:  "required",
		OutputPath:  filepath.Join(tmp, "bundle.tar"),
		ToolVersion: "test",
		Probe:       func(context.Context) (bool, string) { return false, "zstd not on $PATH" },
	}
	err := exporter.Export(context.Background(), opts)
	require.Error(t, err)
	require.True(t, errors.Is(err, zstdpatch.ErrZstdBinaryMissing))
	// No archive written.
	_, statErr := os.Stat(opts.OutputPath)
	require.True(t, os.IsNotExist(statErr))
}

func TestExport_AutoMode_DowngradesSilentlyWhenProbeMissing(t *testing.T) {
	tmp := t.TempDir()
	var warn bytes.Buffer
	opts := exporter.Options{
		Pairs:       []exporter.Pair{{Name: "a", BaselinePath: "fixture-baseline.tar", TargetPath: "fixture-target.tar"}},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  filepath.Join(tmp, "bundle.tar"),
		ToolVersion: "test",
		Probe:       func(context.Context) (bool, string) { return false, "zstd not on $PATH" },
		WarnOut:     &warn,
	}
	// Use the same pair construction the existing export tests use — copy the
	// setup helper rather than re-implement; see exporter_testing.go.
	pair := buildOCIFixturePair(t, tmp) // existing helper (exporter_testing.go)
	opts.Pairs = []exporter.Pair{pair}

	err := exporter.Export(context.Background(), opts)
	require.NoError(t, err)
	require.Contains(t, warn.String(), "disabling intra-layer for this run")
	// Archive exists and every shipped blob is encoding=full.
	requireAllFullEncoding(t, opts.OutputPath) // helper defined below
}

// requireAllFullEncoding asserts the bundle at path has zero patch entries.
func requireAllFullEncoding(t *testing.T, path string) {
	t.Helper()
	raw, err := archive.ReadSidecar(path)
	require.NoError(t, err)
	sc, err := diff.ParseSidecar(raw)
	require.NoError(t, err)
	for d, b := range sc.Blobs {
		require.Equal(t, diff.EncodingFull, b.Encoding,
			"blob %s unexpectedly encoded as %s", d, b.Encoding)
	}
}
```

Add the imports at the top: `bytes`, `context`, `errors`, `os`, `path/filepath`, `testing`; `github.com/leosocy/diffah/internal/archive`, `github.com/leosocy/diffah/internal/zstdpatch`, `github.com/leosocy/diffah/pkg/diff`, `github.com/leosocy/diffah/pkg/exporter`.

- [ ] **Step 2: Confirm `buildOCIFixturePair` exists**

Run: `grep -n buildOCIFixturePair pkg/exporter/exporter_testing.go`
If missing, stop and add a helper that constructs a pair from the existing `testdata/` OCI fixture — model it on the helpers already in `exporter_testing.go`. Otherwise proceed.

- [ ] **Step 3: Run — expect fail**

Run: `go test ./pkg/exporter/ -run 'TestExport_(Required|AutoMode_Downgrades)' -v`
Expected: FAIL because `Options.Probe`/`Options.WarnOut` were only just added; the test may pass on the first run after Task 4 landed, in which case consolidate Steps 3-4.

- [ ] **Step 4: Run — expect pass after Task 4 landed**

Run: `go test ./pkg/exporter/... -v`
Expected: both new tests pass; existing tests still green.

- [ ] **Step 5: Commit**

```bash
git add pkg/exporter/exporter_test.go
git commit -m "test(exporter): cover required-mode hard-fail and auto-mode silent downgrade"
```

### Task 6: Importer — needsZstd probe + DryRunReport fields

**Files:**
- Modify: `pkg/importer/importer.go`
- Create: `pkg/importer/importer_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/importer/importer_test.go`:

```go
package importer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
)

// probeStub returns a fixed (ok, reason) pair and counts invocations.
type probeStub struct {
	ok       bool
	reason   string
	calls    int
}

func (p *probeStub) probe(context.Context) (bool, string) {
	p.calls++
	return p.ok, p.reason
}

// Four-combo matrix: {archive has any patch entries?} × {probe ok?}
// Each case asserts the whole-import outcome.
func TestImport_NeedsZstdMatrix(t *testing.T) {
	t.Run("all-full_missing_probe_no_call", func(t *testing.T) {
		h := newAllFullBundle(t)
		ps := &probeStub{ok: false, reason: "missing"}
		opts := Options{
			DeltaPath:    h.bundlePath,
			Baselines:    map[string]string{h.imageName: h.baselinePath},
			OutputPath:   t.TempDir(),
			OutputFormat: FormatDockerArchive,
			Probe:        ps.probe,
		}
		require.NoError(t, Import(context.Background(), opts))
		require.Zero(t, ps.calls, "probe must NOT fire when archive has no patch entries")
	})
	t.Run("all-full_ok_probe_noop", func(t *testing.T) {
		h := newAllFullBundle(t)
		ps := &probeStub{ok: true}
		opts := Options{
			DeltaPath:    h.bundlePath,
			Baselines:    map[string]string{h.imageName: h.baselinePath},
			OutputPath:   t.TempDir(),
			OutputFormat: FormatDockerArchive,
			Probe:        ps.probe,
		}
		require.NoError(t, Import(context.Background(), opts))
		require.Zero(t, ps.calls)
	})
	t.Run("patch_missing_probe_hardfail_before_blob", func(t *testing.T) {
		h := newMixedBundle(t) // has at least one encoding=patch
		ps := &probeStub{ok: false, reason: "zstd not on $PATH"}
		opts := Options{
			DeltaPath:    h.bundlePath,
			Baselines:    map[string]string{h.imageName: h.baselinePath},
			OutputPath:   t.TempDir(),
			OutputFormat: FormatDockerArchive,
			Probe:        ps.probe,
		}
		err := Import(context.Background(), opts)
		require.Error(t, err)
		require.True(t, errors.Is(err, zstdpatch.ErrZstdBinaryMissing))
		require.Equal(t, 1, ps.calls)
		// No output files written.
		entries, _ := os.ReadDir(opts.OutputPath)
		require.Empty(t, entries, "no output must be produced when probe hard-fails")
	})
	t.Run("patch_ok_round_trip_succeeds", func(t *testing.T) {
		h := newMixedBundle(t)
		ps := &probeStub{ok: true}
		opts := Options{
			DeltaPath:    h.bundlePath,
			Baselines:    map[string]string{h.imageName: h.baselinePath},
			OutputPath:   t.TempDir(),
			OutputFormat: FormatDockerArchive,
			Probe:        ps.probe,
		}
		require.NoError(t, Import(context.Background(), opts))
		require.Equal(t, 1, ps.calls)
	})
}

// TestDryRun_ReportsNeedsZstdAndAvailable covers the DryRunReport extension.
func TestDryRun_ReportsNeedsZstdAndAvailable(t *testing.T) {
	t.Run("all-full_no_requires_zstd", func(t *testing.T) {
		h := newAllFullBundle(t)
		report, err := DryRun(context.Background(), Options{
			DeltaPath: h.bundlePath,
			Probe:     (&probeStub{ok: true}).probe,
		})
		require.NoError(t, err)
		require.False(t, report.RequiresZstd)
		// ZstdAvailable reflects the probe result but only meaningful when RequiresZstd.
	})
	t.Run("patch_and_missing_probe_still_no_error", func(t *testing.T) {
		h := newMixedBundle(t)
		report, err := DryRun(context.Background(), Options{
			DeltaPath: h.bundlePath,
			Probe:     (&probeStub{ok: false, reason: "missing"}).probe,
		})
		require.NoError(t, err) // DryRun must not error on missing probe
		require.True(t, report.RequiresZstd)
		require.False(t, report.ZstdAvailable)
	})
}

// newAllFullBundle builds a bundle with --intra-layer=off.
func newAllFullBundle(t *testing.T) *bundleHarness {
	t.Helper()
	// Reuse the existing harness helper in integration_bundle_test.go but
	// explicitly pass --intra-layer=off. If the helper is private to that
	// test file, extract it to a shared _testing.go file in this step.
	return newBundleHarness(t, []exporter.Pair{fixturePair(t)}) // off-mode by default
}

// newMixedBundle builds a bundle where at least one layer is patch-encoded
// by running export with --intra-layer=auto against the v3/v4 fixture pair
// that's already patch-producing in existing tests.
func newMixedBundle(t *testing.T) *bundleHarness {
	t.Helper()
	tmp := t.TempDir()
	bundlePath := filepath.Join(tmp, "bundle.tar")
	pair := fixturePair(t)
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs:       []exporter.Pair{pair},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  bundlePath,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	// Sanity-check: at least one patch entry exists. If not, the fixture
	// needs updating and this test is invalid — fail loudly rather than pass silently.
	bundle, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer bundle.cleanup()
	var patches int
	for _, b := range bundle.sidecar.Blobs {
		if b.Encoding == diff.EncodingPatch {
			patches++
		}
	}
	require.Greater(t, patches, 0, "mixed bundle harness fixture must produce at least one patch entry")
	return &bundleHarness{
		t:          t,
		ctx:        context.Background(),
		bundlePath: bundlePath,
		sidecar:    bundle.sidecar,
		tmpDir:     tmp,
		// imageName + baselinePath wired from fixturePair
	}
}

// Discard the empty lines after this — `fixturePair`, `bundleHarness`,
// and `extractBundle` come from integration_bundle_test.go.
var _ = bytes.Buffer{}
```

Note: the helpers `fixturePair` + `newBundleHarness` already live in `integration_bundle_test.go`. If they are `func fixturePair(...)` / `func newBundleHarness(...)` at package level, they're directly callable. If this proves awkward, in Step 3 extract both to an `_internal_test.go` shared file. Do NOT copy-paste.

- [ ] **Step 2: Implement the probe + DryRunReport changes in `importer.go`**

```go
// Options additions (insert where the struct is defined):
type Options struct {
	DeltaPath    string
	Baselines    map[string]string
	Strict       bool
	OutputPath   string
	OutputFormat string
	AllowConvert bool
	Progress     io.Writer
	// Probe reports zstd availability. Defaults to zstdpatch.Available
	// when nil. Tests inject stubs.
	Probe func(context.Context) (bool, string)
}

func (o *Options) probeOrDefault() func(context.Context) (bool, string) {
	if o.Probe != nil {
		return o.Probe
	}
	return func(ctx context.Context) (bool, string) {
		return zstdpatch.Available(ctx)
	}
}

// Inside Import, after extractBundle and before validatePositionalBaseline:
if sidecarHasPatch(bundle.sidecar) {
	ok, reason := opts.probeOrDefault()(ctx)
	if !ok {
		return fmt.Errorf("%w: %s", zstdpatch.ErrZstdBinaryMissing, reason)
	}
}

// Helper next to Import:
func sidecarHasPatch(sc *diff.Sidecar) bool {
	for _, b := range sc.Blobs {
		if b.Encoding == diff.EncodingPatch {
			return true
		}
	}
	return false
}

// DryRunReport gains two bool fields (append to the struct definition):
type DryRunReport struct {
	Feature      string
	Version      string
	Tool         string
	ToolVersion  string
	CreatedAt    time.Time
	Platform     string
	Images       []ImageDryRun
	Blobs        BlobStats
	ArchiveBytes int64
	// Whether any BlobEntry has Encoding == patch.
	RequiresZstd bool
	// Result of the probe at DryRun time. Meaningful only when RequiresZstd.
	ZstdAvailable bool
}

// Inside DryRun, right before return DryRunReport{...}:
requiresZstd := sidecarHasPatch(bundle.sidecar)
var zstdAvailable bool
if requiresZstd {
	zstdAvailable, _ = opts.probeOrDefault()(ctx)
}

return DryRunReport{
	// ...existing field assignments...
	RequiresZstd:  requiresZstd,
	ZstdAvailable: zstdAvailable,
}, nil
```

Add the `zstdpatch` import.

- [ ] **Step 3: Run — expect pass**

Run: `go test ./pkg/importer/... -v`
Expected: all new tests + existing tests pass.

- [ ] **Step 4: Commit**

```bash
git add pkg/importer/importer.go pkg/importer/importer_test.go
git commit -m "feat(importer): probe-early-fail when archive has patch entries

Import and DryRun pre-check zstd availability only when the sidecar
contains at least one Encoding: patch entry. DryRunReport gains
RequiresZstd + ZstdAvailable for diffah inspect to surface."
```

### Task 7: CLI — export flag accepts `required`, inspect surfaces probe state

**Files:**
- Modify: `cmd/export.go`
- Modify: `cmd/export_integration_test.go`
- Modify: `cmd/inspect.go`
- Modify: `cmd/inspect_test.go`

- [ ] **Step 1: Update `cmd/export.go` flag help**

Change the flag registration:

```go
f.StringVar(&exportFlags.intraLayer, "intra-layer", "auto",
	"intra-layer diff mode (auto|off|required)")
```

No runtime validation is needed in `runExport`; `resolveMode` (Task 4) already rejects unknown values with a clear error.

- [ ] **Step 2: Add a failing integration test**

Append to `cmd/export_integration_test.go`:

```go
func TestExport_RejectsUnknownIntraLayerValue(t *testing.T) {
	cmd := newExportCommand()
	cmd.SetArgs([]string{
		"--pair", "a=x.tar,y.tar",
		"--intra-layer", "aggressive",
		filepath.Join(t.TempDir(), "out.tar"),
	})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "aggressive")
}
```

- [ ] **Step 3: Run — expect pass**

Run: `go test ./cmd/ -run TestExport_RejectsUnknownIntraLayerValue -v`
Expected: PASS (the error bubbles up from `resolveMode` inside `exporter.Export`).

- [ ] **Step 4: Update `cmd/inspect.go` to surface `RequiresZstd`/`ZstdAvailable`**

Edit `printBundleSidecar` to accept the importer's `DryRunReport` OR extend `runInspect` to call the importer. Spec §8.6 + §6 want the inspect CLI to print:

```
intra-layer patches required: yes|no
zstd available: yes|no
```

Simplest wiring: `runInspect` calls `importer.DryRun` after parsing the sidecar, and `printBundleSidecar` gains two extra lines conditional on a new parameter.

```go
// In cmd/inspect.go:
import (
	"github.com/leosocy/diffah/pkg/importer"
)

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
	report, err := importer.DryRun(cmd.Context(), importer.Options{DeltaPath: args[0]})
	if err != nil {
		return err
	}
	return printBundleSidecar(cmd.OutOrStdout(), args[0], s, report)
}

func printBundleSidecar(w io.Writer, path string, s *diff.Sidecar, report importer.DryRunReport) error {
	// ... existing output ...
	fmt.Fprintf(w, "intra-layer patches required: %s\n", yesNo(report.RequiresZstd))
	fmt.Fprintf(w, "zstd available: %s\n", yesNo(report.ZstdAvailable))
	// ... per-image loop unchanged ...
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
```

- [ ] **Step 5: Update `cmd/inspect_test.go`**

Add two assertions to the existing test (or a new test case if the file follows table-driven style):

```go
func TestInspect_SurfacesProbeState(t *testing.T) {
	// Build an all-full bundle and a mixed bundle (shared helper with importer tests).
	allFull := newAllFullBundleForInspect(t)
	mixed := newMixedBundleForInspect(t)

	out := runInspectInto(t, allFull.bundlePath)
	require.Contains(t, out, "intra-layer patches required: no")
	require.Contains(t, out, "zstd available: no")

	out = runInspectInto(t, mixed.bundlePath)
	require.Contains(t, out, "intra-layer patches required: yes")
	// zstd available depends on CI host; assert only that the line is present.
	require.Contains(t, out, "zstd available:")
}
```

`runInspectInto` wires a buffer into the cobra command; copy the pattern from existing `inspect_test.go`. Bundle helpers are the same as in Task 6; extract to `cmd/testhelpers_test.go` if cross-package reuse is awkward.

- [ ] **Step 6: Run — expect pass**

Run: `go test ./cmd/ -v`
Expected: all CLI tests pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/
git commit -m "feat(cli): accept --intra-layer=required; surface probe state in inspect

Flag help lists auto|off|required. Inspect prints
  intra-layer patches required: yes|no
  zstd available: yes|no
for every bundle archive."
```

### Task 8: Integration — reduced `$PATH` export+import round-trip

**Files:**
- Modify: `pkg/importer/integration_bundle_test.go`

- [ ] **Step 1: Write failing test**

Append to `integration_bundle_test.go`:

```go
// TestIntegration_AutoDowngradesUnderReducedPATH builds an export under a
// $PATH that excludes zstd, asserts the stderr warning fires exactly once,
// and verifies the resulting archive imports byte-exactly.
func TestIntegration_AutoDowngradesUnderReducedPATH(t *testing.T) {
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "") // deliberately scrub

	tmp := t.TempDir()
	bundlePath := filepath.Join(tmp, "bundle.tar")
	var warn bytes.Buffer
	err := exporter.Export(context.Background(), exporter.Options{
		Pairs:       []exporter.Pair{fixturePair(t)},
		Platform:    "linux/amd64",
		IntraLayer:  "auto",
		OutputPath:  bundlePath,
		ToolVersion: "test",
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		WarnOut:     &warn,
	})
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(warn.String(), "disabling intra-layer for this run"),
		"downgrade warning must appear exactly once")

	// Every shipped blob must be encoding=full (no probe → no patch path).
	bundle, err := extractBundle(bundlePath)
	require.NoError(t, err)
	defer bundle.cleanup()
	for d, b := range bundle.sidecar.Blobs {
		require.Equal(t, diff.EncodingFull, b.Encoding, "blob %s encoded as %s", d, b.Encoding)
	}

	// Round-trip import should succeed under the same scrubbed $PATH —
	// the importer's own probe must not fire for an all-full bundle.
	outDir := filepath.Join(tmp, "out")
	require.NoError(t, os.MkdirAll(outDir, 0o755))
	_ = origPath // kept for diagnostics; not restored, t.Setenv restores at end
	err = importer.Import(context.Background(), importer.Options{
		DeltaPath:    bundlePath,
		Baselines:    map[string]string{fixtureImageName(t): fixtureBaselinePath(t)},
		OutputPath:   outDir,
		OutputFormat: importer.FormatDockerArchive,
	})
	require.NoError(t, err)
}
```

Imports to add: `bytes`, `os`, `strings`.

- [ ] **Step 2: Run — expect pass**

Run: `go test ./pkg/importer/ -run TestIntegration_AutoDowngradesUnderReducedPATH -v`
Expected: PASS — export emits exactly one warning, archive is all-full, import round-trips cleanly.

- [ ] **Step 3: Commit**

```bash
git add pkg/importer/integration_bundle_test.go
git commit -m "test(integration): cover auto-mode silent downgrade under scrubbed \$PATH"
```

### Task 9: Docs — README Requirements section

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Locate the Requirements section**

Run: `grep -n -A10 -iE '^##? requirements' README.md`
Expected: the section header and its current body.

- [ ] **Step 2: Replace with spec §11 wording**

Replace the Requirements body with:

```markdown
## Requirements

- Go 1.25+ (build only).
- `zstd ≥ 1.5` on `$PATH` — **recommended for best compression**. Required only when:
  - using `--intra-layer=required` on `diffah export`;
  - importing an archive that was produced with intra-layer patches.

Archives produced with `--intra-layer=off` (or `auto` on a host without
zstd — `auto` downgrades silently and warns on stderr) import anywhere,
including hosts with no `zstd` binary at all.

Run `diffah inspect <archive>` to see whether a given archive requires
`zstd` at import time.
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(readme): narrow zstd requirement to required-mode + patch-bearing imports"
```

---

## Verification checklist (run before declaring done)

Run these from the repo root; all must pass.

- [ ] `go build ./...`
- [ ] `go test ./...` (full suite — respects existing build tags)
- [ ] `go test -count=1 ./internal/zstdpatch/...` (fresh cache, in case sync.Once cached state surprises CI)
- [ ] `golangci-lint run ./...` (same lint config master uses)
- [ ] `go test -run TestIntegration_AutoDowngradesUnderReducedPATH ./pkg/importer/ -v` (confirms `$PATH`-scrubbed path)
- [ ] `go test -run TestEncodeFull_RoundTrip_NoCLI ./internal/zstdpatch/ -v` (confirms klauspost path needs no CLI)
- [ ] `go test -run TestExport_RequiredMode_FailsWhenProbeMissing ./pkg/exporter/ -v` (confirms required-mode hard-fail)
- [ ] `diffah inspect <any-patch-bearing-archive>` prints the two new lines.
- [ ] `diffah export --pair a=b,c --intra-layer aggressive /tmp/x.tar` exits non-zero with a clear "not recognized" message.

## Self-review notes

- **Spec §1 coverage:** footprint revisit → Task 1 + Task 2 move `EncodeFull`/`DecodeFull` off the CLI.
- **Spec §4.3 probe:** Task 3 builds `Available` with the exact algorithm — `LookPath` → `--version` → regex → major/minor ≥ 1.5 → `sync.Once` cache + injectable test hook.
- **Spec §4.4 klauspost config:** `zstd.WithEncoderLevel(zstd.SpeedDefault)` + `WithWindowSize(1<<27)` in Task 2, `WithDecoderMaxWindow(1<<27)` on `DecodeFull`.
- **Spec §4.5 Options:** Task 4 adds `Probe` + `WarnOut`.
- **Spec §5 resolveMode:** Task 4 implements the five-row table verbatim.
- **Spec §5.1 lenient semantics:** no per-layer `ErrZstdEncodeFailure` is added — existing `encode.go:32-38` fallback remains mode-independent. No new code beyond the existing path is needed.
- **Spec §6 import flow:** Task 6 adds `sidecarHasPatch` + probe-before-`resolveBaselines` + `DryRunReport` fields.
- **Spec §7 errors:** `ErrZstdBinaryMissing` defined in Task 3, used by Task 4 (exporter) and Task 6 (importer).
- **Spec §8 tests:** probe tests (Task 3), fullgo parity tests (Task 2), `resolveMode` table (Task 4), Export integration (Task 5), importer matrix (Task 6), inspect output (Task 7), reduced-`$PATH` end-to-end (Task 8).
- **Spec §9 backward compat:** archive format unchanged; existing archives decode via unchanged `Encode`/`Decode` in `cli.go`.
- **Spec §11 rollout:** README updated (Task 9); no migration tooling required.

---

## Execution handoff

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task with review between tasks.
2. **Inline Execution** — I execute tasks in this session with batched checkpoints.

Which approach?
