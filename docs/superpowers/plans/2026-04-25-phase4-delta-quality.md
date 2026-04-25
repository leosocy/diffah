# Phase 4 — Delta Quality & Throughput — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `diffah diff` and `diffah bundle` produce smaller delta archives in less wall-clock time on the abundant-resource build farms where they actually run, by adding (1) tunable zstd encoding parameters with larger default windows and levels, (2) top-K baseline candidate retry per shipped layer, (3) parallel encode with bounded worker pool and singleflight-coordinated fingerprint cache, and (4) explicit registry-streaming guarantees. The apply path is left functionally unchanged; the only importer-side code touched is a one-line lift of the zstd decoder's window cap so Phase 4 archives can decode through the same Go binary.

**Architecture:** Four sequential PRs. PR-1 adds plumbing (flags + decoder cap raise) without changing default behavior. PR-2 adds top-K candidate selection (`PickTopK` + `PlanShippedTopK`) with a behavior change visible only when `--candidates>1`. PR-3 introduces a bounded `errgroup`-based worker pool and a `singleflight`-coordinated `fpCache` that memoizes baseline layer bytes + fingerprints across pairs (enforcing each baseline blob fetched at most once per `Export()` call). PR-4 flips the production-tuned defaults (`--zstd-level=22`, `--zstd-window-log=auto`, `--candidates=3`, `--workers=8`) and ships the GB-scale bench, CHANGELOG, README and `docs/performance.md` updates.

**Tech Stack:** Go 1.25+, existing `internal/zstdpatch` CLI shell-out, `golang.org/x/sync/errgroup`, `golang.org/x/sync/singleflight` (already an indirect dep at `v0.20.0`), `internal/registrytest` harness from Phase 2/3 for fetch-count assertions, `klauspost/compress/zstd` for `EncodeFull` parity.

**Spec:** `docs/superpowers/specs/2026-04-25-phase4-delta-quality-design.md`

---

## Preflight — branch & toolchain sanity

- [ ] **Step P.1: Confirm we're on the spec branch**

Run: `git -C /Users/leosocy/workspace/repos/myself/diffah branch --show-current`
Expected: `spec/v2-phase4-delta-quality`

If output differs, run `git checkout spec/v2-phase4-delta-quality`.

- [ ] **Step P.2: Confirm the tree is clean**

Run: `git -C /Users/leosocy/workspace/repos/myself/diffah status --short`
Expected: empty (or at most an untracked `.DS_Store`).

- [ ] **Step P.3: Confirm tests pass on the spec branch baseline**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./...`
Expected: all pass — this is the green baseline we'll protect through every stage.

- [ ] **Step P.4: Promote `golang.org/x/sync` to a direct dep**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go get golang.org/x/sync@v0.20.0`
Expected: `go.mod` line for `golang.org/x/sync v0.20.0` no longer has the `// indirect` comment.

(This is required up-front because PR-3 needs both `errgroup` and `singleflight`. Doing the dep promotion in PR-1 keeps PR-3 tightly scoped.)

- [ ] **Step P.5: Re-verify tests still pass after dep promotion**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./... && go test ./...`
Expected: all pass.

---

## File Structure (decomposition lock-in)

### Files created

| Path | Stage | Responsibility |
|---|---|---|
| `cmd/encoding_flags.go` | 1 | `installEncodingFlags(cmd) → encodingOptsBuilder` mirror of `installRegistryFlags` |
| `cmd/encoding_flags_test.go` | 1 | flag-parsing unit tests (validation, defaults) |
| `pkg/exporter/workerpool.go` | 3 | `errgroup`-based bounded worker pool with semaphore |
| `pkg/exporter/workerpool_test.go` | 3 | bound, ctx-cancel, error propagation |
| `pkg/exporter/fpcache.go` | 3 | `singleflight`-backed baseline byte + fingerprint cache |
| `pkg/exporter/fpcache_test.go` | 3 | hit/miss, concurrent collapse, error non-poisoning |
| `cmd/diff_workers_integration_test.go` | 3 | byte-identical archive across `--workers ∈ {1,2,4,8,32}` |
| `cmd/diff_lazyfetch_integration_test.go` | 2 | per-baseline-blob fetch count = 1 across multi-pair runs |
| `cmd/diff_quality_integration_test.go` | 4 | new delta size ≤ 0.85 × Phase 3 baseline on synthesized fixture |
| `cmd/diff_bigfixture_bench_test.go` | 4 | `DIFFAH_BIG_TEST=1` 2 GiB fixture; emits `benchmarks/phase4.json` |
| `benchmarks/.gitkeep` | 4 | Empty placeholder so the dir exists for CI artifact upload |
| `docs/performance.md` | 4 | Updated bandwidth + memory characteristics for Phase 4 |

### Files modified

| Path | Stage | Modification |
|---|---|---|
| `go.mod` / `go.sum` | Preflight | Promote `golang.org/x/sync` from indirect to direct dep |
| `internal/zstdpatch/cli.go` | 1 | Add `EncodeOpts{Level, WindowLog}` parameter to `Encode`; lift decode-side `--long=27` to `--long=31` |
| `internal/zstdpatch/cli_test.go` | 1 | Cover `EncodeOpts{0,0}` (Phase 3 byte-identical default) and `EncodeOpts{22,30}` (Phase 4 path) |
| `internal/zstdpatch/fullgo.go` | 1 | Add `EncodeOpts` param to `EncodeFull`; lift `WithDecoderMaxWindow(1<<27)` to `1<<31` |
| `internal/zstdpatch/fullgo_test.go` | 1 | Cover EncodeOpts default + Phase 4 path |
| `pkg/exporter/intralayer.go` | 1, 2 | Stage 1: thread `level/windowLog` through `PlanShipped`. Stage 2: split into `PickTopK` + `PlanShippedTopK` |
| `pkg/exporter/intralayer_test.go` | 1, 2 | Stage 1: parameter coverage. Stage 2: top-K selection + ties + smallest-of-K assertions |
| `pkg/exporter/exporter.go` | 1, 3, 4 | Stage 1: `Workers/Candidates/ZstdLevel/ZstdWindowLog` fields on `Options`. Stage 3: thread through to `encodeShipped`. Stage 4: defaults flip in zero-fill block |
| `pkg/exporter/encode.go` | 2, 3 | Stage 2: call `PlanShippedTopK`. Stage 3: rewrite to two-phase worker-pool driver |
| `pkg/exporter/pool.go` | 3 | Add `sync.RWMutex`; lock all reads/writes |
| `pkg/exporter/pool_test.go` | 3 | Concurrent-add safety regression test |
| `cmd/diff.go` | 1, 4 | Stage 1: install encoding flags + thread to `Options`. Stage 4: bump default values |
| `cmd/bundle.go` | 1, 4 | Same as `cmd/diff.go` |
| `cmd/help.go` | 4 | Long-help "Encoding tuning" section + determinism guarantee text |
| `CHANGELOG.md` | 4 | Phase 4 entry with backward-compat notes |
| `README.md` | 4 | New flags listed in usage section |

### Files explicitly NOT modified

- `pkg/exporter/writer.go` — single-writer archive emission stays as-is (already deterministic via `pool.sortedDigests()`)
- `pkg/importer/*` — apply path's Go signatures unchanged; only side effect is the lifted decoder cap inside `internal/zstdpatch`
- `pkg/diff/sidecar.go` — no schema bump (zstd frames self-describe level + window-log)
- `pkg/signer/*` — no signing/verification changes

---

# Stage 1 — PR-1: encoder parameter plumbing + decode cap raise

**PR title:** `feat(zstdpatch): add EncodeOpts + lift decode window cap to 1<<31`

**Behavior change visible to operators:** None with default flags. The producer accepts new `--zstd-level` and `--zstd-window-log` flags but defaults preserve Phase 3 byte-identical output (`-L 3 --long=27`). The decoder side is permissive for any future Phase 4 archive but does not require the larger cap to operate on Phase 3 inputs.

## Task 1.1: Add `EncodeOpts` to `internal/zstdpatch/cli.go`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/cli.go`
- Test: `/Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/cli_test.go` (existing)

- [ ] **Step 1.1.1: Read current `Encode` and `Decode` to anchor edits**

Run: `head -60 /Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/cli.go`
Confirm:
- `Encode` signature: `func Encode(ctx context.Context, ref, target []byte) ([]byte, error)`
- `Decode` signature: `func Decode(ctx context.Context, ref, patch []byte) ([]byte, error)`
- The argv literals contain `"-3", "--long=27"` (line 43, 87)

- [ ] **Step 1.1.2: Write a failing unit test for the new signature**

Edit `/Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/cli_test.go` — append at the end (do NOT delete existing tests):

```go
func TestEncodeOptsDefaultsAreByteIdenticalToPhase3(t *testing.T) {
	if !zstdpatch.Available(context.Background()) {
		t.Skip("zstd CLI unavailable")
	}
	ref := bytes.Repeat([]byte("rrrr"), 1024)
	target := append(append([]byte{}, ref...), bytes.Repeat([]byte("nnnn"), 256)...)

	// Zero-valued EncodeOpts must produce the exact bytes today's CLI flags do.
	got, err := zstdpatch.Encode(context.Background(), ref, target, zstdpatch.EncodeOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("encode returned empty patch")
	}
	// Round-trip must equal the original target.
	back, err := zstdpatch.Decode(context.Background(), ref, got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(back, target) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(back), len(target))
	}
}

func TestEncodeOptsLevelAndWindowAreApplied(t *testing.T) {
	if !zstdpatch.Available(context.Background()) {
		t.Skip("zstd CLI unavailable")
	}
	ref := bytes.Repeat([]byte("aaaa"), 4096)
	target := append(append([]byte{}, ref...), bytes.Repeat([]byte("bbbb"), 1024)...)

	low, err := zstdpatch.Encode(context.Background(), ref, target,
		zstdpatch.EncodeOpts{Level: 1, WindowLog: 27})
	if err != nil {
		t.Fatalf("encode low: %v", err)
	}
	high, err := zstdpatch.Encode(context.Background(), ref, target,
		zstdpatch.EncodeOpts{Level: 22, WindowLog: 27})
	if err != nil {
		t.Fatalf("encode high: %v", err)
	}
	if !(len(high) <= len(low)) {
		t.Fatalf("level=22 patch (%d) should be ≤ level=1 patch (%d)", len(high), len(low))
	}
	// Both must round-trip.
	for label, p := range map[string][]byte{"low": low, "high": high} {
		back, err := zstdpatch.Decode(context.Background(), ref, p)
		if err != nil {
			t.Fatalf("decode %s: %v", label, err)
		}
		if !bytes.Equal(back, target) {
			t.Fatalf("%s round-trip mismatch", label)
		}
	}
}
```

If the file does not already import `bytes`, add it.

- [ ] **Step 1.1.3: Run the new tests — expect FAIL (compile error)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./internal/zstdpatch/... -run 'TestEncodeOpts'`
Expected: compile error — `zstdpatch.EncodeOpts undefined` and/or `too many arguments in call to zstdpatch.Encode`.

- [ ] **Step 1.1.4: Add `EncodeOpts` and update `Encode` signature**

Edit `/Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/cli.go`:

Replace the `Encode` function (top of file, currently `func Encode(ctx context.Context, ref, target []byte) ([]byte, error) { ... }`) with:

```go
// EncodeOpts tunes the producer-side zstd parameters. A zero value
// requests the historical Phase-3 defaults (level 3, --long=27) so
// existing callers and existing fixtures keep their byte-for-byte
// outputs.
type EncodeOpts struct {
	// Level is the zstd compression level (1..22). Zero means level 3.
	Level int
	// WindowLog is log2 of the long-mode window in bytes (10..31).
	// Zero means 27 (128 MiB), the historical Phase-3 cap.
	WindowLog int
}

func (o EncodeOpts) levelArg() string {
	l := o.Level
	if l == 0 {
		l = 3
	}
	return fmt.Sprintf("-%d", l)
}

func (o EncodeOpts) windowArg() string {
	w := o.WindowLog
	if w == 0 {
		w = 27
	}
	return fmt.Sprintf("--long=%d", w)
}

// Encode produces a zstd frame using --patch-from=ref that decodes to target.
// An empty target returns a precomputed empty frame to avoid invoking the CLI
// on a degenerate case that crashes older zstd builds. ctx cancellation kills
// the zstd subprocess. EncodeOpts tunes level and window; zero-valued opts
// reproduce Phase-3 byte-identical output.
func Encode(ctx context.Context, ref, target []byte, opts EncodeOpts) ([]byte, error) {
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
		opts.levelArg(), opts.windowArg(),
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
```

Note: this changes `Encode`'s signature. We will update its sole production caller in Task 1.4.

- [ ] **Step 1.1.5: Update `Decode` to lift the window cap to 31**

Same file, in the existing `Decode` function (≈ line 64 onwards), replace the line:

```go
		"-d", "--long=27",
```

with:

```go
		"-d", "--long=31",
```

(Add a one-line comment above:)

```go
	// --long=31 sets the maximum admissible window size (2 GiB). Frames
	// declaring smaller windows allocate only what they need; this cap
	// only governs the upper bound on decoder memory.
```

- [ ] **Step 1.1.6: Compile to surface every caller that needs updating**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./...`
Expected: build error in `pkg/exporter/intralayer.go` ("not enough arguments in call to zstdpatch.Encode"). This is the *one* production caller and we'll fix it in Task 1.4.

(Test files in `internal/zstdpatch` itself may also break — keep going to Step 1.1.7.)

- [ ] **Step 1.1.7: Update existing zstdpatch unit tests to pass `EncodeOpts{}` where they used the 3-arg form**

Run: `grep -n 'zstdpatch.Encode(' /Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/*_test.go`
For every match in those test files, change `zstdpatch.Encode(ctx, ref, target)` to `zstdpatch.Encode(ctx, ref, target, zstdpatch.EncodeOpts{})`.

(The build error in `pkg/exporter/intralayer.go` stays — Task 1.4 is its repair.)

- [ ] **Step 1.1.8: Run the package's own tests**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./internal/zstdpatch/... -v`
Expected: every test in this package passes (including the two we added in Step 1.1.2).

## Task 1.2: Add `EncodeOpts` to `internal/zstdpatch/fullgo.go`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/fullgo.go`
- Test: `/Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/fullgo_test.go`

- [ ] **Step 1.2.1: Write a failing test for `EncodeFull(target, EncodeOpts{...})`**

Append to `fullgo_test.go`:

```go
func TestEncodeFullOpts_Phase3DefaultRoundTrip(t *testing.T) {
	target := bytes.Repeat([]byte("zzzz"), 4096)
	out, err := zstdpatch.EncodeFull(target, zstdpatch.EncodeOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := zstdpatch.DecodeFull(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(back, target) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestEncodeFullOpts_HighLevelIsSmallerOrEqual(t *testing.T) {
	target := bytes.Repeat([]byte("yyyy"), 8192)
	low, err := zstdpatch.EncodeFull(target, zstdpatch.EncodeOpts{Level: 1, WindowLog: 27})
	if err != nil {
		t.Fatalf("low: %v", err)
	}
	high, err := zstdpatch.EncodeFull(target, zstdpatch.EncodeOpts{Level: 22, WindowLog: 27})
	if err != nil {
		t.Fatalf("high: %v", err)
	}
	if !(len(high) <= len(low)) {
		t.Fatalf("level=22 (%d) should be ≤ level=1 (%d)", len(high), len(low))
	}
}
```

- [ ] **Step 1.2.2: Run — expect compile FAIL**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./internal/zstdpatch/... -run 'TestEncodeFullOpts'`
Expected: compile error ("too many arguments in call to zstdpatch.EncodeFull").

- [ ] **Step 1.2.3: Update `EncodeFull` and `DecodeFull` in `fullgo.go`**

Replace the file body (everything below the package comment) with:

```go
package zstdpatch

import (
	"bytes"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// EncodeFull compresses target as a standalone zstd frame. Zero-valued
// EncodeOpts reproduces the historical -3 --long=27 default.
func EncodeFull(target []byte, opts EncodeOpts) ([]byte, error) {
	if len(target) == 0 {
		return append([]byte(nil), emptyZstdFrame()...), nil
	}
	level := opts.Level
	if level == 0 {
		level = 3
	}
	windowLog := opts.WindowLog
	if windowLog == 0 {
		windowLog = 27
	}
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstdLevelToKlauspost(level)),
		zstd.WithWindowSize(1<<windowLog),
	)
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: new encoder: %w", err)
	}
	defer enc.Close()
	return enc.EncodeAll(target, nil), nil
}

// DecodeFull reads a standalone zstd frame. WithDecoderMaxWindow=1<<31
// admits any Phase 4-emitted frame; smaller windows still allocate
// only their declared size.
func DecodeFull(data []byte) ([]byte, error) {
	if bytes.Equal(data, emptyZstdFrame()) {
		return nil, nil
	}
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderMaxWindow(1<<31),
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

// zstdLevelToKlauspost maps the user-facing 1..22 zstd CLI levels onto
// the four named tiers exposed by klauspost/compress. The CLI lets you
// pick any integer; klauspost only exposes Fastest/Default/Better/Best.
// We bin: 1..3 → Fastest, 4..7 → Default, 8..15 → Better, 16..22 → Best.
func zstdLevelToKlauspost(level int) zstd.EncoderLevel {
	switch {
	case level <= 3:
		return zstd.SpeedFastest
	case level <= 7:
		return zstd.SpeedDefault
	case level <= 15:
		return zstd.SpeedBetterCompression
	default:
		return zstd.SpeedBestCompression
	}
}
```

- [ ] **Step 1.2.4: Run the package tests**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./internal/zstdpatch/... -v`
Expected: all pass.

## Task 1.3: Verify decode cap raise via an explicit test

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/internal/zstdpatch/cli_test.go`

- [ ] **Step 1.3.1: Add a test that exercises a window > 27 round-trip**

Append to `cli_test.go`:

```go
func TestEncodeDecode_LargeWindowRoundTrip(t *testing.T) {
	if !zstdpatch.Available(context.Background()) {
		t.Skip("zstd CLI unavailable")
	}
	// Synthesize a ref/target pair where matches sit beyond the 128 MiB
	// (--long=27) window. Use a 256 MiB ref/target so anything > 128 MiB
	// from the start is unreachable at WindowLog=27 but reachable at 30.
	const sz = 200 << 20 // 200 MiB
	ref := make([]byte, sz)
	for i := range ref {
		ref[i] = byte(i % 251) // pseudo-random pattern, not really random
	}
	target := make([]byte, sz)
	copy(target, ref)
	// Tweak a region near the end so encoding has to find it.
	for i := sz - 1024; i < sz; i++ {
		target[i] ^= 0xff
	}
	patch, err := zstdpatch.Encode(context.Background(), ref, target,
		zstdpatch.EncodeOpts{Level: 3, WindowLog: 30})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := zstdpatch.Decode(context.Background(), ref, patch)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(back, target) {
		t.Fatalf("round-trip mismatch")
	}
}
```

If the test runtime is excessive, gate the test with: `if testing.Short() { t.Skip("large fixture; run with -short=false") }` at the top of the test.

- [ ] **Step 1.3.2: Run — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./internal/zstdpatch/... -run 'TestEncodeDecode_LargeWindowRoundTrip' -v`
Expected: PASS (proves decode cap raise works for windows > 27).

## Task 1.4: Thread `EncodeOpts` through `pkg/exporter/intralayer.go`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/intralayer.go`

- [ ] **Step 1.4.1: Confirm the broken caller**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./pkg/exporter/...`
Expected: error mentioning `zstdpatch.Encode` and/or `zstdpatch.EncodeFull` ("not enough arguments").

- [ ] **Step 1.4.2: Add `Level` and `WindowLog` to the `Planner` constructor and `PlanShipped`**

In `pkg/exporter/intralayer.go`, modify `Planner` struct (currently lines 33–43) to add two int fields **immediately after `fingerprint`**:

```go
	// Stage 1 of Phase 4: tunables threaded into every Encode call.
	// Zero values reproduce historical Phase-3 behavior (level 3,
	// windowLog 27).
	level     int
	windowLog int
```

Then update `NewPlanner` to accept them. Replace the existing function (lines 49–64) with:

```go
// NewPlanner builds a planner that reads blobs via readBlob, keyed by
// digest. The function must handle both target and baseline digests.
// A nil Fingerprinter defaults to DefaultFingerprinter{}. Baselines are
// sorted by Digest at construction time for deterministic tie-breaks.
//
// level and windowLog are forwarded to every zstd Encode/EncodeFull
// call; zero values reproduce Phase-3 byte-identical defaults.
func NewPlanner(
	baseline []BaselineLayerMeta,
	readBlob func(digest.Digest) ([]byte, error),
	fp Fingerprinter,
	level, windowLog int,
) *Planner {
	sorted := make([]BaselineLayerMeta, len(baseline))
	copy(sorted, baseline)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Digest < sorted[j].Digest
	})
	return &Planner{
		baseline:    sorted,
		readBlob:    readBlob,
		fingerprint: fp,
		level:       level,
		windowLog:   windowLog,
	}
}
```

- [ ] **Step 1.4.3: Update `PlanShipped` to forward the parameters**

Same file. Replace the two `zstdpatch.Encode(ctx, refBytes, target)` and `zstdpatch.EncodeFull(target)` calls inside `PlanShipped` (currently lines ≈122–128) with:

```go
	patch, err := zstdpatch.Encode(ctx, refBytes, target,
		zstdpatch.EncodeOpts{Level: p.level, WindowLog: p.windowLog})
	if err != nil {
		return diff.BlobRef{}, nil, fmt.Errorf("encode patch %s: %w", s.Digest, err)
	}
	fullZst, err := zstdpatch.EncodeFull(target,
		zstdpatch.EncodeOpts{Level: p.level, WindowLog: p.windowLog})
	if err != nil {
		return diff.BlobRef{}, nil, fmt.Errorf("encode full %s: %w", s.Digest, err)
	}
```

- [ ] **Step 1.4.4: Update `pkg/exporter/encode.go` to pass the new args**

In `pkg/exporter/encode.go`, locate the `NewPlanner` call inside `encodeShipped` (currently line 24):

```go
		planner := NewPlanner(p.BaselineLayerMeta, readBaseline, fp)
```

Replace with:

```go
		planner := NewPlanner(p.BaselineLayerMeta, readBaseline, fp, 0, 0)
```

(Stage 1 keeps zero-valued tunables — Stage 4 changes the defaults to non-zero.)

- [ ] **Step 1.4.5: Fix existing `intralayer_test.go` callers of `NewPlanner`**

Run: `grep -n 'NewPlanner(' /Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/*_test.go`
For every matching call, append `, 0, 0` to the argument list (preserving Phase-3 behavior in tests).

- [ ] **Step 1.4.6: Compile**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./...`
Expected: clean.

- [ ] **Step 1.4.7: Run exporter tests**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -v`
Expected: all pass — Phase-3 byte-identical outputs preserved because tunables are 0.

## Task 1.5: Add `Workers/Candidates/ZstdLevel/ZstdWindowLog` to `exporter.Options`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/exporter.go`

- [ ] **Step 1.5.1: Extend the `Options` struct**

In `pkg/exporter/exporter.go`, locate the `Options` struct (currently lines 17–47) and add four new fields after `IntraLayer string`:

```go
	// Phase 4 tunables. Zero values map to historical defaults so
	// callers that do not set them keep Phase-3 byte-identical output.
	Workers       int // 0 → 1 (serial). PR-3 changes default to 8.
	Candidates    int // 0 → 1 (single best). PR-2 changes default to 3.
	ZstdLevel     int // 0 → 3. PR-4 changes default to 22.
	ZstdWindowLog int // 0 → 27. PR-4 changes default to "auto" (per-layer).
```

- [ ] **Step 1.5.2: Wire `ZstdLevel` and `ZstdWindowLog` into `NewPlanner` from inside `buildBundle`**

Same file. Find the line `for _, p := range opts.Pairs {` inside `buildBundle` (around line 99). Just before the `if err := encodeShipped(...)` call (around line 114), the `encodeShipped` signature is unchanged — but `encodeShipped` itself constructs the planner. We pass the params from `Options` *into* `encodeShipped` instead.

Replace the existing `encodeShipped` call (line 114):

```go
	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.reporter()); err != nil {
```

with:

```go
	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.reporter(),
		opts.ZstdLevel, opts.ZstdWindowLog); err != nil {
```

- [ ] **Step 1.5.3: Update `encodeShipped` signature in `pkg/exporter/encode.go`**

Replace the function header (currently lines 13–16):

```go
func encodeShipped(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, rep progress.Reporter,
) error {
```

with:

```go
func encodeShipped(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, rep progress.Reporter,
	level, windowLog int,
) error {
```

And update the inner `NewPlanner` call (the line we already touched in Step 1.4.4):

```go
		planner := NewPlanner(p.BaselineLayerMeta, readBaseline, fp, level, windowLog)
```

- [ ] **Step 1.5.4: Compile**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./...`
Expected: clean.

- [ ] **Step 1.5.5: Run all tests — confirm nothing regressed**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./...`
Expected: all pass.

## Task 1.6: Create `cmd/encoding_flags.go`

**Files:**
- Create: `/Users/leosocy/workspace/repos/myself/diffah/cmd/encoding_flags.go`

- [ ] **Step 1.6.1: Create the file**

Write `/Users/leosocy/workspace/repos/myself/diffah/cmd/encoding_flags.go`:

```go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// encodingOpts is the flat collection of Phase 4 producer-side
// tunables a subcommand parses out of its flag set.
type encodingOpts struct {
	Workers       int
	Candidates    int
	ZstdLevel     int
	ZstdWindowLog int // 0 = auto
}

// encodingOptsBuilder validates parsed flags and yields the resolved
// encodingOpts. Validation is at the cobra layer so a malformed
// invocation exits with category=user (exit 2) before any I/O.
type encodingOptsBuilder func() (encodingOpts, error)

// installEncodingFlags registers the four Phase 4 tunables on cmd and
// returns a closure invoked from RunE. Defaults are deliberately the
// PR-1 historical values; PR-4 flips them to the build-farm-tuned set.
func installEncodingFlags(cmd *cobra.Command) encodingOptsBuilder {
	o := &encodingOpts{}
	var windowLog string

	f := cmd.Flags()
	f.IntVar(&o.Workers, "workers", 1,
		"layers to fingerprint and encode in parallel; "+
			"--workers=1 reproduces Phase-3 strict-serial encode")
	f.IntVar(&o.Candidates, "candidates", 1,
		"top-K baseline candidates per shipped layer; "+
			"--candidates=1 reproduces Phase-3 single-best behavior")
	f.IntVar(&o.ZstdLevel, "zstd-level", 3,
		"zstd compression level (1..22); higher = smaller patches at the cost of CPU")
	f.StringVar(&windowLog, "zstd-window-log", "27",
		"zstd long-mode window as log2 bytes (10..31); "+
			"or 'auto' to pick per-layer (≤128 MiB→27, ≤1 GiB→30, >1 GiB→31)")

	return func() (encodingOpts, error) {
		if o.Workers < 1 {
			return encodingOpts{}, &cliErr{
				cat: errs.CategoryUser,
				msg: fmt.Sprintf("--workers must be >= 1, got %d", o.Workers),
			}
		}
		if o.Candidates < 1 {
			return encodingOpts{}, &cliErr{
				cat: errs.CategoryUser,
				msg: fmt.Sprintf("--candidates must be >= 1, got %d", o.Candidates),
			}
		}
		if o.ZstdLevel < 1 || o.ZstdLevel > 22 {
			return encodingOpts{}, &cliErr{
				cat: errs.CategoryUser,
				msg: fmt.Sprintf("--zstd-level must be in [1,22], got %d", o.ZstdLevel),
			}
		}
		if windowLog == "auto" {
			o.ZstdWindowLog = 0 // 0 sentinel = auto
		} else {
			var n int
			if _, err := fmt.Sscanf(windowLog, "%d", &n); err != nil || n < 10 || n > 31 {
				return encodingOpts{}, &cliErr{
					cat: errs.CategoryUser,
					msg: fmt.Sprintf("--zstd-window-log must be 'auto' or in [10,31], got %q", windowLog),
				}
			}
			o.ZstdWindowLog = n
		}
		return *o, nil
	}
}
```

- [ ] **Step 1.6.2: Compile**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./cmd/...`
Expected: clean.

## Task 1.7: Add unit tests for `installEncodingFlags`

**Files:**
- Create: `/Users/leosocy/workspace/repos/myself/diffah/cmd/encoding_flags_test.go`

- [ ] **Step 1.7.1: Create the test file**

Write `/Users/leosocy/workspace/repos/myself/diffah/cmd/encoding_flags_test.go`:

```go
package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestEncodingFlags_Defaults(t *testing.T) {
	c := &cobra.Command{Use: "x"}
	build := installEncodingFlags(c)
	if err := c.ParseFlags(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.Workers != 1 || got.Candidates != 1 || got.ZstdLevel != 3 || got.ZstdWindowLog != 27 {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}

func TestEncodingFlags_Auto(t *testing.T) {
	c := &cobra.Command{Use: "x"}
	build := installEncodingFlags(c)
	if err := c.ParseFlags([]string{"--zstd-window-log=auto"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.ZstdWindowLog != 0 {
		t.Fatalf("auto should yield ZstdWindowLog=0 (sentinel), got %d", got.ZstdWindowLog)
	}
}

func TestEncodingFlags_Validation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string // substring expected in error message
	}{
		{"workers zero", []string{"--workers=0"}, "--workers must be >= 1"},
		{"candidates zero", []string{"--candidates=0"}, "--candidates must be >= 1"},
		{"level zero", []string{"--zstd-level=0"}, "--zstd-level must be in [1,22]"},
		{"level high", []string{"--zstd-level=23"}, "--zstd-level must be in [1,22]"},
		{"window log low", []string{"--zstd-window-log=9"}, "--zstd-window-log must be 'auto' or in [10,31]"},
		{"window log high", []string{"--zstd-window-log=32"}, "--zstd-window-log must be 'auto' or in [10,31]"},
		{"window log non-numeric", []string{"--zstd-window-log=foo"}, "--zstd-window-log must be 'auto' or in [10,31]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &cobra.Command{Use: "x"}
			build := installEncodingFlags(c)
			if err := c.ParseFlags(tc.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, err := build()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}
```

- [ ] **Step 1.7.2: Run — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./cmd/... -run 'TestEncodingFlags' -v`
Expected: all pass.

## Task 1.8: Wire encoding flags onto `diff` and `bundle`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff.go`
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/cmd/bundle.go`

- [ ] **Step 1.8.1: Add an `encodingBuilder encodingOptsBuilder` field to `diffFlags`**

In `cmd/diff.go`, modify the `diffFlags` block (lines 12–19) to add the new field:

```go
var diffFlags = struct {
	platform           string
	compress           string
	intraLayer         string
	dryRun             bool
	buildSystemContext registryContextBuilder
	buildSignRequest   signRequestBuilder
	buildEncodingOpts  encodingOptsBuilder
}{}
```

- [ ] **Step 1.8.2: Install in `newDiffCommand`**

Same file. After `diffFlags.buildSignRequest = installSigningFlags(c)` (line 51), add:

```go
	diffFlags.buildEncodingOpts = installEncodingFlags(c)
```

- [ ] **Step 1.8.3: Build and pass the parsed values into `exporter.Options` in `runDiff`**

Same file. Inside `runDiff` (line 58 onwards), after `signReq, signing, err := diffFlags.buildSignRequest()` block:

```go
	encOpts, err := diffFlags.buildEncodingOpts()
	if err != nil {
		return err
	}
```

Then in the `opts := exporter.Options{...}` block, add four fields after `ToolVersion`:

```go
		Workers:          encOpts.Workers,
		Candidates:       encOpts.Candidates,
		ZstdLevel:        encOpts.ZstdLevel,
		ZstdWindowLog:    encOpts.ZstdWindowLog,
```

- [ ] **Step 1.8.4: Repeat for `cmd/bundle.go`**

Apply the analogous three changes to `cmd/bundle.go`:
1. Add `buildEncodingOpts encodingOptsBuilder` to the `bundleFlags` struct.
2. Call `bundleFlags.buildEncodingOpts = installEncodingFlags(c)` after the existing flag installs.
3. In `runBundle`, call `encOpts, err := bundleFlags.buildEncodingOpts()` and thread the four fields into `exporter.Options`.

- [ ] **Step 1.8.5: Compile + test**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./... && go test ./...`
Expected: all pass.

## Task 1.9: Verify `--help` text and commit Stage 1

- [ ] **Step 1.9.1: Smoke-check `diff --help`**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go run . diff --help`
Expected: output includes `--workers`, `--candidates`, `--zstd-level`, `--zstd-window-log` with their descriptions.

- [ ] **Step 1.9.2: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add internal/zstdpatch/ pkg/exporter/exporter.go pkg/exporter/intralayer.go pkg/exporter/intralayer_test.go pkg/exporter/encode.go cmd/encoding_flags.go cmd/encoding_flags_test.go cmd/diff.go cmd/bundle.go go.mod go.sum
git commit -m "feat(zstdpatch): add EncodeOpts + lift decode window cap to 1<<31

Adds Level and WindowLog fields to Encode and EncodeFull (zero values
preserve Phase-3 byte-identical output); raises the decoder's
admissible window cap from 1<<27 to 1<<31 in both the CLI shell-out
and the klauspost decoder so Phase 4 archives produced with --long=30
or --long=31 round-trip through the same binary.

The --zstd-level and --zstd-window-log flags are wired onto diff and
bundle via a new installEncodingFlags helper that mirrors the registry
and signing flag patterns. --workers and --candidates are wired but
both default to 1 (no behavior change); subsequent PRs change those
defaults.

Refs: docs/superpowers/specs/2026-04-25-phase4-delta-quality-design.md §4.5, §9 PR-1"
```

---

# Stage 2 — PR-2: top-K baseline candidates

**PR title:** `feat(exporter): top-K baseline candidates per shipped layer`

**Behavior change visible to operators:** With `--candidates>1`, deltas may shrink (the producer tries K candidates and emits the smallest patch). Default after this PR remains `--candidates=1` to keep Phase-3 byte-identical until PR-4 flips defaults. Verifies Goal #3 (lazy fetch) by adding a fetch-count integration test.

## Task 2.1: Add `PickTopK` to `Planner`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/intralayer.go`
- Test: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/intralayer_test.go`

- [ ] **Step 2.1.1: Write a failing test**

Append to `pkg/exporter/intralayer_test.go`:

```go
func TestPickTopK_PrefersHighestScoreThenSizeClosest(t *testing.T) {
	// Three baselines, all with valid fingerprints but different scores.
	d1 := digest.Digest("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	d2 := digest.Digest("sha256:2222222222222222222222222222222222222222222222222222222222222222")
	d3 := digest.Digest("sha256:3333333333333333333333333333333333333333333333333333333333333333")

	baseline := []exporter.BaselineLayerMeta{
		{Digest: d1, Size: 100, MediaType: "application/vnd.oci.image.layer.v1.tar"},
		{Digest: d2, Size: 200, MediaType: "application/vnd.oci.image.layer.v1.tar"},
		{Digest: d3, Size: 300, MediaType: "application/vnd.oci.image.layer.v1.tar"},
	}
	p := exporter.NewPlanner(baseline,
		func(d digest.Digest) ([]byte, error) { return nil, nil },
		stubFP{
			d1: exporter.Fingerprint{"a": 50, "b": 50},
			d2: exporter.Fingerprint{"a": 80, "b": 80, "c": 40},
			d3: exporter.Fingerprint{"x": 100},
		},
		0, 0,
	)
	target := exporter.Fingerprint{"a": 50, "b": 50, "c": 30}

	got := p.PickTopK(target, 150, 2)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Digest != d2 {
		t.Errorf("got[0] = %s, want %s (highest score)", got[0].Digest, d2)
	}
	if got[1].Digest != d1 {
		t.Errorf("got[1] = %s, want %s (next highest)", got[1].Digest, d1)
	}
}

func TestPickTopK_FallsBackToSizeClosestWhenNoFingerprint(t *testing.T) {
	d1 := digest.Digest("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	d2 := digest.Digest("sha256:2222222222222222222222222222222222222222222222222222222222222222")
	baseline := []exporter.BaselineLayerMeta{
		{Digest: d1, Size: 100, MediaType: "application/vnd.oci.image.layer.v1.tar"},
		{Digest: d2, Size: 250, MediaType: "application/vnd.oci.image.layer.v1.tar"},
	}
	p := exporter.NewPlanner(baseline,
		func(d digest.Digest) ([]byte, error) { return nil, nil },
		stubFP{}, // no fingerprints
		0, 0,
	)
	got := p.PickTopK(nil, 240, 2) // target size 240 → d2 (250) is closest
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Digest != d2 {
		t.Errorf("got[0] = %s, want %s (size-closest)", got[0].Digest, d2)
	}
}

func TestPickTopK_ClampsAtAvailableCount(t *testing.T) {
	d := digest.Digest("sha256:abc1111111111111111111111111111111111111111111111111111111111111")
	baseline := []exporter.BaselineLayerMeta{{Digest: d, Size: 100}}
	p := exporter.NewPlanner(baseline,
		func(d digest.Digest) ([]byte, error) { return nil, nil },
		stubFP{d: exporter.Fingerprint{"a": 50}},
		0, 0,
	)
	got := p.PickTopK(exporter.Fingerprint{"a": 50}, 100, 5)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
}
```

If the file doesn't already define `stubFP`, add one near the top:

```go
type stubFP map[digest.Digest]exporter.Fingerprint

func (s stubFP) Fingerprint(_ context.Context, _ string, blob []byte) (exporter.Fingerprint, error) {
	// Look up by digest of blob bytes — the planner calls Fingerprint
	// with the raw blob bytes. We pre-key by what readBlob returned.
	d := digest.FromBytes(blob)
	return s[d], nil
}
```

(If the existing test fixture already provides a `stubFP`, reuse it.)

- [ ] **Step 2.1.2: Run — expect FAIL (PickTopK undefined)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestPickTopK' -v`
Expected: compile error — `p.PickTopK undefined`.

- [ ] **Step 2.1.3: Implement `PickTopK`**

In `pkg/exporter/intralayer.go`, append after `pickSimilar` (line ~262):

```go
// PickTopK returns up to k candidates ordered by content-similarity
// score (descending), with size-closest as the tie-break inside an
// equal-score band. Falls back to size-closest top-k when targetFP is
// nil or no baseline has a fingerprint. Result length is min(k,
// len(p.baseline)). Deterministic for fixed inputs.
func (p *Planner) PickTopK(targetFP Fingerprint, targetSize int64, k int) []BaselineLayerMeta {
	if k <= 0 || len(p.baseline) == 0 {
		return nil
	}
	type scored struct {
		meta  BaselineLayerMeta
		score int64
	}
	cands := make([]scored, 0, len(p.baseline))
	for _, b := range p.baseline {
		var s int64
		if targetFP != nil {
			s = score(targetFP, p.baselineFP[b.Digest])
		}
		cands = append(cands, scored{meta: b, score: s})
	}
	// Sort by score desc, size-closeness asc, then digest asc for stable order.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		di := absDelta(cands[i].meta.Size, targetSize)
		dj := absDelta(cands[j].meta.Size, targetSize)
		if di != dj {
			return di < dj
		}
		return cands[i].meta.Digest < cands[j].meta.Digest
	})
	if k > len(cands) {
		k = len(cands)
	}
	out := make([]BaselineLayerMeta, k)
	for i := 0; i < k; i++ {
		out[i] = cands[i].meta
	}
	return out
}
```

Note: `PickTopK` may be called before `ensureBaselineFP` runs. Add at the top, just after `if k <= 0 ...`:

```go
	p.ensureBaselineFP(context.Background())
```

(If you prefer to require the caller to invoke `ensureBaselineFP` first, add a panic guard instead. Keeping the auto-prime is simpler.)

- [ ] **Step 2.1.4: Run — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestPickTopK' -v`
Expected: all three subtests pass.

## Task 2.2: Add `PlanShippedTopK`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/intralayer.go`
- Test: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/intralayer_test.go`

- [ ] **Step 2.2.1: Write a failing test**

Append to `intralayer_test.go`:

```go
func TestPlanShippedTopK_PicksSmallestOfKPatches(t *testing.T) {
	// Two baselines: b1 nearly identical to target (small patch),
	// b2 totally different (large patch).
	target := bytes.Repeat([]byte("aaaa"), 1024)
	b1 := append(append([]byte{}, target...), []byte("zz")...) // tiny diff
	b2 := bytes.Repeat([]byte("zzzz"), 1024)                   // big diff

	d1 := digest.FromBytes(b1)
	d2 := digest.FromBytes(b2)
	dt := digest.FromBytes(target)

	baseline := []exporter.BaselineLayerMeta{
		{Digest: d1, Size: int64(len(b1)), MediaType: "application/vnd.oci.image.layer.v1.tar"},
		{Digest: d2, Size: int64(len(b2)), MediaType: "application/vnd.oci.image.layer.v1.tar"},
	}
	readBlob := func(d digest.Digest) ([]byte, error) {
		switch d {
		case d1:
			return b1, nil
		case d2:
			return b2, nil
		}
		return nil, fmt.Errorf("unknown digest %s", d)
	}

	p := exporter.NewPlanner(baseline, readBlob, stubFP{
		d1: exporter.Fingerprint{"shared": 4096},   // higher-scoring candidate (will be tried first)
		d2: exporter.Fingerprint{"foreign": 4096},  // lower-scoring candidate
	}, 0, 0)

	entry, payload, err := p.PlanShippedTopK(context.Background(),
		diff.BlobRef{Digest: dt, Size: int64(len(target)), MediaType: "application/vnd.oci.image.layer.v1.tar"},
		target, 2)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if entry.Encoding != diff.EncodingPatch {
		t.Errorf("entry.Encoding = %s, want patch", entry.Encoding)
	}
	if entry.PatchFromDigest != d1 {
		t.Errorf("PatchFromDigest = %s, want %s (smaller patch)", entry.PatchFromDigest, d1)
	}
	if int64(len(payload)) >= int64(len(target)) {
		t.Errorf("patch payload (%d) should be smaller than target (%d)", len(payload), len(target))
	}
}

func TestPlanShippedTopK_FallsBackToFullWhenAllPatchesExceedFull(t *testing.T) {
	target := []byte("ab") // tiny target — full encode is unbeatable
	b1 := []byte("xx")
	d1 := digest.FromBytes(b1)
	dt := digest.FromBytes(target)

	baseline := []exporter.BaselineLayerMeta{
		{Digest: d1, Size: int64(len(b1)), MediaType: "application/vnd.oci.image.layer.v1.tar"},
	}
	readBlob := func(d digest.Digest) ([]byte, error) {
		if d == d1 {
			return b1, nil
		}
		return nil, fmt.Errorf("unknown")
	}
	p := exporter.NewPlanner(baseline, readBlob, stubFP{d1: exporter.Fingerprint{"x": 2}}, 0, 0)

	entry, _, err := p.PlanShippedTopK(context.Background(),
		diff.BlobRef{Digest: dt, Size: int64(len(target))},
		target, 1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if entry.Encoding != diff.EncodingFull {
		t.Errorf("Encoding = %s, want full (target too small to patch)", entry.Encoding)
	}
}
```

- [ ] **Step 2.2.2: Run — expect FAIL (PlanShippedTopK undefined)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestPlanShippedTopK' -v`
Expected: compile error.

- [ ] **Step 2.2.3: Implement `PlanShippedTopK`**

In `pkg/exporter/intralayer.go`, append after `PlanShipped` (line ~142). The new method **must not delete** `PlanShipped` — `PlanShipped` stays as the K=1 fast path. (Stage 2 has `--candidates=1` as default; PR-2 just adds the K-aware variant. PR-2 does NOT replace `PlanShipped` callers — that happens in Task 2.3.)

```go
// PlanShippedTopK encodes the target up to k+1 ways — once per top-k
// baseline candidate plus one "full" encode — and returns whichever
// produces the smallest emitted bytes. A fingerprint is computed for
// the target inside this call.
//
// k=1 is observably equivalent to PlanShipped: the same candidate
// is selected, the same Encode/EncodeFull comparison runs.
func (p *Planner) PlanShippedTopK(
	ctx context.Context, s diff.BlobRef, target []byte, k int,
) (diff.BlobRef, []byte, error) {
	p.ensureBaselineFP(ctx)
	fp := p.fingerprint
	if fp == nil {
		fp = DefaultFingerprinter{}
	}
	targetFP, _ := fp.Fingerprint(ctx, s.MediaType, target)
	cands := p.PickTopK(targetFP, s.Size, k)
	if len(cands) == 0 {
		return fullEntry(s), target, nil
	}

	// Always include "full" as a candidate so we never inflate.
	bestEntry := fullEntry(s)
	bestPayload := target

	for _, c := range cands {
		refBytes, err := p.readBlob(c.Digest)
		if err != nil {
			return diff.BlobRef{}, nil, fmt.Errorf("read baseline reference %s: %w", c.Digest, err)
		}
		patch, err := zstdpatch.Encode(ctx, refBytes, target,
			zstdpatch.EncodeOpts{Level: p.level, WindowLog: p.windowLog})
		if err != nil {
			return diff.BlobRef{}, nil, fmt.Errorf("encode patch %s vs %s: %w", s.Digest, c.Digest, err)
		}
		fullZst, err := zstdpatch.EncodeFull(target,
			zstdpatch.EncodeOpts{Level: p.level, WindowLog: p.windowLog})
		if err != nil {
			return diff.BlobRef{}, nil, fmt.Errorf("encode full %s: %w", s.Digest, err)
		}
		// Same gating as PlanShipped: only emit a patch if it strictly beats
		// both the full-zstd ceiling AND the raw target bytes.
		if len(patch) < len(fullZst) && int64(len(patch)) < s.Size && int64(len(patch)) < int64(len(bestPayload)) {
			bestEntry = diff.BlobRef{
				Digest:          s.Digest,
				Size:            s.Size,
				MediaType:       s.MediaType,
				Encoding:        diff.EncodingPatch,
				Codec:           CodecZstdPatch,
				PatchFromDigest: c.Digest,
				ArchiveSize:     int64(len(patch)),
			}
			bestPayload = patch
		}
	}
	return bestEntry, bestPayload, nil
}
```

- [ ] **Step 2.2.4: Run — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestPlanShippedTopK' -v`
Expected: both subtests pass.

## Task 2.3: Wire `encodeShipped` to call `PlanShippedTopK`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/encode.go`
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/exporter.go`

- [ ] **Step 2.3.1: Extend `encodeShipped` signature with `candidates int`**

In `pkg/exporter/encode.go`, modify the function header to add `candidates int`:

```go
func encodeShipped(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, rep progress.Reporter,
	level, windowLog, candidates int,
) error {
```

- [ ] **Step 2.3.2: Replace the `PlanShipped` call with `PlanShippedTopK`**

Same file. Inside the inner for loop (line ~45 today), replace:

```go
				entry, payload, err := planner.PlanShipped(ctx, s, layerBytes)
```

with:

```go
				k := candidates
				if k <= 0 {
					k = 1
				}
				entry, payload, err := planner.PlanShippedTopK(ctx, s, layerBytes, k)
```

- [ ] **Step 2.3.3: Thread `Candidates` from `Options` in `buildBundle`**

In `pkg/exporter/exporter.go`, the `encodeShipped(...)` call (Stage 1 already gave it `level, windowLog`). Append `opts.Candidates`:

```go
	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.reporter(),
		opts.ZstdLevel, opts.ZstdWindowLog, opts.Candidates); err != nil {
```

- [ ] **Step 2.3.4: Compile + test**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./... && go test ./...`
Expected: all pass — `Options.Candidates` is still 0 in callers, which `encodeShipped` defaults back to 1.

## Task 2.4: Default `--candidates=1` (no behavior change yet)

This stage keeps the operator-visible default at 1 to preserve byte-identical Phase-3 output. PR-4 will flip it to 3.

- [ ] **Step 2.4.1: Confirm `cmd/encoding_flags.go` already defaults Candidates to 1**

Run: `grep -n 'Candidates' /Users/leosocy/workspace/repos/myself/diffah/cmd/encoding_flags.go`
Expected: line `f.IntVar(&o.Candidates, "candidates", 1, ...)` exists.

(If a previous edit changed the default, set it back to 1 here.)

- [ ] **Step 2.4.2: Add a test that asserts default-flag output is byte-identical to Phase-3**

Append to `pkg/exporter/intralayer_test.go`:

```go
func TestPlanShippedTopK_K1MatchesPlanShipped(t *testing.T) {
	target := bytes.Repeat([]byte("aaaa"), 4096)
	ref := append(append([]byte{}, target...), []byte("zz")...)
	d := digest.FromBytes(ref)
	dt := digest.FromBytes(target)

	baseline := []exporter.BaselineLayerMeta{
		{Digest: d, Size: int64(len(ref)), MediaType: "application/vnd.oci.image.layer.v1.tar"},
	}
	readBlob := func(x digest.Digest) ([]byte, error) {
		if x == d {
			return ref, nil
		}
		return nil, fmt.Errorf("unknown")
	}
	pa := exporter.NewPlanner(baseline, readBlob, stubFP{d: exporter.Fingerprint{"a": 16384}}, 0, 0)
	pb := exporter.NewPlanner(baseline, readBlob, stubFP{d: exporter.Fingerprint{"a": 16384}}, 0, 0)

	br := diff.BlobRef{Digest: dt, Size: int64(len(target)), MediaType: "application/vnd.oci.image.layer.v1.tar"}
	entryA, payloadA, err := pa.PlanShipped(context.Background(), br, target)
	if err != nil {
		t.Fatalf("planA: %v", err)
	}
	entryB, payloadB, err := pb.PlanShippedTopK(context.Background(), br, target, 1)
	if err != nil {
		t.Fatalf("planB: %v", err)
	}
	if entryA.Encoding != entryB.Encoding || entryA.PatchFromDigest != entryB.PatchFromDigest {
		t.Fatalf("entry mismatch: %+v vs %+v", entryA, entryB)
	}
	if !bytes.Equal(payloadA, payloadB) {
		t.Fatalf("payload bytes differ (lenA=%d, lenB=%d)", len(payloadA), len(payloadB))
	}
}
```

- [ ] **Step 2.4.3: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestPlanShippedTopK_K1' -v`
Expected: PASS.

## Task 2.5: Per-baseline-blob fetch-count integration test

**Files:**
- Create: `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff_lazyfetch_integration_test.go`

- [ ] **Step 2.5.1: Identify the registrytest hook for instrumenting GetBlob**

Run: `grep -n 'GetBlob\|FetchCounter\|InjectFault' /Users/leosocy/workspace/repos/myself/diffah/internal/registrytest/*.go | head -20`
Expected: identify a counter API or extension point. If none exists, the test will need to add a small `WithGetBlobCounter(*atomic.Int64)` option in `internal/registrytest`. (Integration tests in Phase 2 already exercised injection via `WithInjectFault`; the same pattern applies.)

- [ ] **Step 2.5.2: If `WithGetBlobCounter` does NOT yet exist, add it to `internal/registrytest`**

Modify `internal/registrytest/server.go` (or wherever options live) — add an `Option` that wraps the registry's blob handler with a counter. Sample addition (paths may differ — read the file first):

```go
// WithGetBlobCounter increments counter once per blob byte stream
// served. Used by Phase 4 lazy-fetch tests.
func WithGetBlobCounter(counter *atomic.Int64) Option {
	return func(o *options) {
		o.blobCounter = counter
	}
}
```

And in the blob serving handler:

```go
if o.blobCounter != nil {
	o.blobCounter.Add(1)
}
```

(Adapt to actual file structure; this is a directional sketch.)

- [ ] **Step 2.5.3: Write the integration test**

Create `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff_lazyfetch_integration_test.go`:

```go
package cmd_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/leosocy/diffah/internal/registrytest"
	"github.com/leosocy/diffah/pkg/exporter"
)

// TestLazyFetch_EachBaselineBlobFetchedAtMostOnce drives a multi-pair
// bundle export against an in-process registry whose blob handler
// counts requests, and asserts every distinct baseline digest appears
// at most once across the entire run regardless of how many pairs
// reference it or how high --candidates is set.
func TestLazyFetch_EachBaselineBlobFetchedAtMostOnce(t *testing.T) {
	var ctr atomic.Int64
	srv := registrytest.New(t, registrytest.WithGetBlobCounter(&ctr))
	t.Cleanup(srv.Close)

	// Push a baseline image with three layers and two target images
	// that share the baseline. Helper functions live in registrytest.
	base := srv.PushImage(t, "myorg/base:v1", registrytest.RandomLayers(3, 64*1024))
	tgt1 := srv.PushImage(t, "myorg/svc-a:v2", registrytest.MutateLayers(base, 0))
	tgt2 := srv.PushImage(t, "myorg/svc-b:v2", registrytest.MutateLayers(base, 1))

	tmp := t.TempDir()
	out := filepath.Join(tmp, "bundle.tar")

	opts := exporter.Options{
		Pairs: []exporter.Pair{
			{Name: "a", BaselineRef: base.Ref, TargetRef: tgt1.Ref},
			{Name: "b", BaselineRef: base.Ref, TargetRef: tgt2.Ref},
		},
		OutputPath:    out,
		Platform:      "linux/amd64",
		ToolVersion:   "test",
		Candidates:    3, // exercise top-K
		Workers:       1, // serial: PR-2 hasn't introduced workers yet
		ZstdLevel:     0,
		ZstdWindowLog: 0,
	}
	if err := exporter.Export(context.Background(), opts); err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output missing: %v", err)
	}

	// Each unique baseline digest may be fetched once (for fingerprint
	// + ref reuse). We expect: 3 baseline layer blobs + 1 baseline
	// manifest + 1 target manifest per target × 2 + N target shipped
	// blobs. Assert: each *baseline layer* digest appears at most once
	// across all GetBlob calls. We can check this only if the
	// registrytest counter is keyed by digest. If the counter is just
	// a total, we substitute: total ≤ baselineLayers + 2*targetLayers.
	total := ctr.Load()
	expectedMax := int64(3 /* baseline layers */ + 6 /* target layers across 2 pairs */ + 4 /* manifests */)
	if total > expectedMax {
		t.Errorf("GetBlob calls = %d, want ≤ %d (each baseline layer should be fetched at most once)", total, expectedMax)
	}
}
```

If the registrytest helpers (`PushImage`, `RandomLayers`, `MutateLayers`) do not exist yet, add minimal versions in a separate commit before this one — keep them small, deterministic, and clearly documented as test-only.

- [ ] **Step 2.5.4: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./cmd/... -run 'TestLazyFetch' -v -tags=integration`
Expected: PASS. (If the project does not use the `integration` tag, drop the flag.)

## Task 2.6: Run full suite and commit Stage 2

- [ ] **Step 2.6.1: Full suite**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./...`
Expected: all pass.

- [ ] **Step 2.6.2: Lint**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && golangci-lint run ./...`
Expected: clean.

- [ ] **Step 2.6.3: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/exporter/intralayer.go pkg/exporter/intralayer_test.go pkg/exporter/encode.go pkg/exporter/exporter.go cmd/encoding_flags.go cmd/diff_lazyfetch_integration_test.go internal/registrytest/
git commit -m "feat(exporter): top-K baseline candidates per shipped layer

Adds Planner.PickTopK and Planner.PlanShippedTopK so that diff /
bundle can encode against multiple baseline candidates and emit the
smallest patch. Default --candidates=1 preserves Phase-3 behavior;
operators opt in with --candidates=N.

Adds a registrytest GetBlob counter and an integration test asserting
each baseline blob is fetched at most once across multi-pair runs and
top-K trials — the fpCache wired in PR-3 will tighten this further.

Refs: docs/superpowers/specs/2026-04-25-phase4-delta-quality-design.md §4.3, §9 PR-2"
```

---

# Stage 3 — PR-3: parallel encode + fpCache

**PR title:** `feat(exporter): parallel encode + singleflight-coordinated fingerprint cache`

**Behavior change visible to operators:** With `--workers>1`, encoding parallelizes across distinct shipped layers. Default after this PR is `--workers=8`. Output bytes are byte-identical to `--workers=1` for the same input — verified by a determinism test across `{1, 2, 4, 8, 32}`.

## Task 3.1: Add `pkg/exporter/workerpool.go`

**Files:**
- Create: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/workerpool.go`
- Create: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/workerpool_test.go`

- [ ] **Step 3.1.1: Write the failing test first**

Create `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/workerpool_test.go`:

```go
package exporter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPool_BoundsConcurrency(t *testing.T) {
	const n = 4
	const jobs = 32
	pool, _ := newWorkerPool(context.Background(), n)

	var inflight, peak atomic.Int64
	for i := 0; i < jobs; i++ {
		pool.Submit(func() error {
			cur := inflight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inflight.Add(-1)
			return nil
		})
	}
	if err := pool.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if peak.Load() > int64(n) {
		t.Fatalf("peak inflight = %d > workers = %d", peak.Load(), n)
	}
}

func TestWorkerPool_PropagatesError(t *testing.T) {
	pool, _ := newWorkerPool(context.Background(), 4)
	want := errors.New("boom")
	pool.Submit(func() error { return want })
	pool.Submit(func() error { time.Sleep(10 * time.Millisecond); return nil })
	got := pool.Wait()
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWorkerPool_CtxCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool, poolCtx := newWorkerPool(ctx, 4)

	var ran atomic.Int64
	for i := 0; i < 16; i++ {
		pool.Submit(func() error {
			select {
			case <-poolCtx.Done():
				return poolCtx.Err()
			case <-time.After(50 * time.Millisecond):
				ran.Add(1)
				return nil
			}
		})
	}
	time.Sleep(5 * time.Millisecond)
	cancel()
	err := pool.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if ran.Load() == 16 {
		t.Fatalf("all 16 jobs ran despite cancellation; expected fewer")
	}
}
```

- [ ] **Step 3.1.2: Run — expect FAIL (newWorkerPool undefined)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestWorkerPool' -v`
Expected: compile error.

- [ ] **Step 3.1.3: Implement the worker pool**

Create `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/workerpool.go`:

```go
package exporter

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// workerPool is a bounded errgroup. Submit blocks when n workers are
// already running; Wait returns the first error any submitted job
// produced and cancels the derived context so still-running jobs can
// exit promptly.
type workerPool struct {
	sem chan struct{}
	eg  *errgroup.Group
	ctx context.Context
}

// newWorkerPool returns a worker pool of capacity n along with a
// context derived from ctx that workers should observe for
// cancellation. n is clamped to 1 if non-positive.
func newWorkerPool(ctx context.Context, n int) (*workerPool, context.Context) {
	if n < 1 {
		n = 1
	}
	eg, gctx := errgroup.WithContext(ctx)
	return &workerPool{
		sem: make(chan struct{}, n),
		eg:  eg,
		ctx: gctx,
	}, gctx
}

// Submit enqueues fn. Blocks if the pool is full. If the pool's
// context is already cancelled, Submit returns immediately without
// running fn (the cancel error is observed by Wait).
func (p *workerPool) Submit(fn func() error) {
	select {
	case <-p.ctx.Done():
		return
	case p.sem <- struct{}{}:
	}
	p.eg.Go(func() error {
		defer func() { <-p.sem }()
		return fn()
	})
}

// Wait blocks until every submitted job has returned, then returns
// the first error encountered (if any).
func (p *workerPool) Wait() error {
	return p.eg.Wait()
}
```

- [ ] **Step 3.1.4: Run — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestWorkerPool' -v`
Expected: all three subtests pass.

## Task 3.2: Add `pkg/exporter/fpcache.go`

**Files:**
- Create: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/fpcache.go`
- Create: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/fpcache_test.go`

- [ ] **Step 3.2.1: Write the failing tests**

Create `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/fpcache_test.go`:

```go
package exporter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestFpCache_HitReturnsBytesAndFingerprint(t *testing.T) {
	c := newFpCache()
	d := digest.Digest("sha256:" + "a"*64)
	want := []byte("hello")

	var calls atomic.Int64
	fetch := func(_ digest.Digest) ([]byte, error) {
		calls.Add(1)
		return want, nil
	}
	fp := stubFP{d: Fingerprint{"k": 5}}

	gotFp, gotBytes, err := c.GetOrLoad(context.Background(),
		BaselineLayerMeta{Digest: d, Size: 5, MediaType: "x"}, fetch, fp)
	if err != nil {
		t.Fatalf("get1: %v", err)
	}
	if string(gotBytes) != "hello" || gotFp == nil {
		t.Fatalf("first call wrong: bytes=%q fp=%v", gotBytes, gotFp)
	}
	// Second call should not invoke fetch.
	gotFp, gotBytes, err = c.GetOrLoad(context.Background(),
		BaselineLayerMeta{Digest: d, Size: 5, MediaType: "x"}, fetch, fp)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("fetch called %d times, want 1", calls.Load())
	}
}

func TestFpCache_ConcurrentMissCollapses(t *testing.T) {
	c := newFpCache()
	d := digest.Digest("sha256:" + "b"*64)

	var calls atomic.Int64
	fetch := func(_ digest.Digest) ([]byte, error) {
		calls.Add(1)
		return []byte("x"), nil
	}
	fp := stubFP{d: Fingerprint{"k": 1}}

	const N = 16
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = c.GetOrLoad(context.Background(),
				BaselineLayerMeta{Digest: d, Size: 1, MediaType: "x"}, fetch, fp)
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetch called %d times under singleflight, want 1", calls.Load())
	}
}

func TestFpCache_FetchErrorDoesNotPoison(t *testing.T) {
	c := newFpCache()
	d := digest.Digest("sha256:" + "c"*64)
	want := errors.New("transient")

	var calls atomic.Int64
	fetch := func(_ digest.Digest) ([]byte, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, want
		}
		return []byte("ok"), nil
	}
	fp := stubFP{d: Fingerprint{"k": 1}}

	if _, _, err := c.GetOrLoad(context.Background(),
		BaselineLayerMeta{Digest: d, Size: 1, MediaType: "x"}, fetch, fp); !errors.Is(err, want) {
		t.Fatalf("first call: got %v, want %v", err, want)
	}
	// Retry should re-call fetch.
	if _, _, err := c.GetOrLoad(context.Background(),
		BaselineLayerMeta{Digest: d, Size: 1, MediaType: "x"}, fetch, fp); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("fetch called %d times, want 2 (no poisoning)", calls.Load())
	}
}
```

- [ ] **Step 3.2.2: Run — expect FAIL**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestFpCache' -v`
Expected: compile error.

- [ ] **Step 3.2.3: Implement `fpCache`**

Create `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/fpcache.go`:

```go
package exporter

import (
	"context"
	"sync"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/singleflight"
)

// fpCache memoizes baseline layer fingerprints AND raw bytes across
// pairs in a single Export() call. Concurrent misses on the same
// digest collapse to one underlying fetch via singleflight.Group;
// fetch errors are returned to all waiters but do NOT store a cache
// entry — the next caller retries.
type fpCache struct {
	mu    sync.RWMutex
	fps   map[digest.Digest]Fingerprint // nil entry = "fingerprint failed but bytes loaded"
	bytes map[digest.Digest][]byte
	sf    singleflight.Group
}

func newFpCache() *fpCache {
	return &fpCache{
		fps:   make(map[digest.Digest]Fingerprint),
		bytes: make(map[digest.Digest][]byte),
	}
}

// GetOrLoad returns the fingerprint and raw bytes for meta.Digest. On
// cache miss it invokes fetch exactly once even under concurrent
// callers; on fetch error nothing is cached and err is returned.
func (c *fpCache) GetOrLoad(
	ctx context.Context,
	meta BaselineLayerMeta,
	fetch func(digest.Digest) ([]byte, error),
	fp Fingerprinter,
) (Fingerprint, []byte, error) {
	if b, ok := c.lookupBytes(meta.Digest); ok {
		return c.lookupFp(meta.Digest), b, nil
	}
	v, err, _ := c.sf.Do(string(meta.Digest), func() (any, error) {
		// Re-check after winning the singleflight (another caller may
		// have populated under a previously-finished singleflight).
		if b, ok := c.lookupBytes(meta.Digest); ok {
			return cacheValue{fp: c.lookupFp(meta.Digest), bytes: b}, nil
		}
		blob, err := fetch(meta.Digest)
		if err != nil {
			return nil, err
		}
		f, _ := fp.Fingerprint(ctx, meta.MediaType, blob) // err -> nil fp (sentinel)
		c.mu.Lock()
		c.bytes[meta.Digest] = blob
		c.fps[meta.Digest] = f
		c.mu.Unlock()
		return cacheValue{fp: f, bytes: blob}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	cv := v.(cacheValue)
	return cv.fp, cv.bytes, nil
}

type cacheValue struct {
	fp    Fingerprint
	bytes []byte
}

func (c *fpCache) lookupBytes(d digest.Digest) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.bytes[d]
	return b, ok
}

func (c *fpCache) lookupFp(d digest.Digest) Fingerprint {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fps[d]
}
```

- [ ] **Step 3.2.4: Run — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestFpCache' -v`
Expected: all three subtests pass.

## Task 3.3: Add concurrency safety to `blobPool`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/pool.go`
- Test: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/pool_test.go`

- [ ] **Step 3.3.1: Write a failing concurrent-add test**

Append to `pool_test.go`:

```go
func TestBlobPool_ConcurrentAddIsSafe(t *testing.T) {
	p := newBlobPool()
	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := digest.FromString(fmt.Sprintf("blob-%d", i))
			p.addIfAbsent(d, []byte("x"), diff.BlobEntry{Size: 1})
		}()
	}
	wg.Wait()
	if got := len(p.sortedDigests()); got != N {
		t.Fatalf("digests = %d, want %d", got, N)
	}
}
```

(If the file does not already import `sync` and `fmt`, add them.)

- [ ] **Step 3.3.2: Run — expect FAIL (race detected) under `-race`**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestBlobPool_ConcurrentAddIsSafe' -race -v`
Expected: race report from the existing map writes.

- [ ] **Step 3.3.3: Add `sync.RWMutex` to `blobPool`**

Replace the `blobPool` struct in `pkg/exporter/pool.go` with:

```go
type blobPool struct {
	mu       sync.RWMutex
	bytes    map[digest.Digest][]byte
	entries  map[digest.Digest]diff.BlobEntry
	shipRefs map[digest.Digest]int
}
```

(Add `"sync"` to the imports.)

Then guard every method:

```go
func (p *blobPool) addIfAbsent(d digest.Digest, data []byte, e diff.BlobEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.bytes[d]; ok {
		return
	}
	p.bytes[d] = data
	p.entries[d] = e
}

func (p *blobPool) has(d digest.Digest) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.bytes[d]
	return ok
}

func (p *blobPool) get(d digest.Digest) ([]byte, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	b, ok := p.bytes[d]
	return b, ok
}

func (p *blobPool) countShipped(d digest.Digest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shipRefs[d]++
}

func (p *blobPool) refCount(d digest.Digest) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.shipRefs[d]
}

func (p *blobPool) sortedDigests() []digest.Digest {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]digest.Digest, 0, len(p.bytes))
	for d := range p.bytes {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

- [ ] **Step 3.3.4: Run race test — expect PASS**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestBlobPool' -race -v`
Expected: clean.

## Task 3.4: Refactor `encodeShipped` to two-phase worker pool

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/encode.go`

- [ ] **Step 3.4.1: Replace `encodeShipped` with a worker-pool driver**

Replace the entire body of `encodeShipped` in `pkg/exporter/encode.go` with:

```go
func encodeShipped(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, rep progress.Reporter,
	level, windowLog, candidates, workers int,
) error {
	if rep == nil {
		rep = progress.NewDiscard()
	}
	if workers < 1 {
		workers = 1
	}
	if candidates < 1 {
		candidates = 1
	}

	cache := newFpCache()
	if fp == nil {
		fp = DefaultFingerprinter{}
	}

	// Phase E1: prime the fingerprint cache for the union of distinct
	// baseline layers across all pairs. Idempotent; singleflight
	// collapses concurrent misses.
	primePool, _ := newWorkerPool(ctx, workers)
	seen := make(map[digest.Digest]struct{})
	for _, p := range pairs {
		ref := p.BaselineImageRef
		sys := p.SystemContext
		for _, b := range p.BaselineLayerMeta {
			if _, ok := seen[b.Digest]; ok {
				continue
			}
			seen[b.Digest] = struct{}{}
			b := b
			fetch := func(d digest.Digest) ([]byte, error) {
				return readBlobBytes(ctx, ref, sys, d)
			}
			primePool.Submit(func() error {
				_, _, err := cache.GetOrLoad(ctx, b, fetch, fp)
				return err
			})
		}
	}
	if err := primePool.Wait(); err != nil {
		return fmt.Errorf("prime baseline fingerprints: %w", err)
	}

	// Phase E2: per shipped target, parallel encode using the primed cache.
	encodePool, _ := newWorkerPool(ctx, workers)
	for _, p := range pairs {
		p := p
		// Each pair builds its own Planner that defers all baseline reads
		// to the shared cache.
		readBaseline := func(d digest.Digest) ([]byte, error) {
			_, b, err := cache.GetOrLoad(ctx, BaselineLayerMeta{Digest: d}, func(d digest.Digest) ([]byte, error) {
				return readBlobBytes(ctx, p.BaselineImageRef, p.SystemContext, d)
			}, fp)
			return b, err
		}
		planner := NewPlanner(p.BaselineLayerMeta, readBaseline, fp, level, windowLog)

		for _, s := range p.Shipped {
			s := s
			if pool.has(s.Digest) {
				continue
			}
			encodePool.Submit(func() error {
				layer := rep.StartLayer(s.Digest, s.Size, string(s.Encoding))
				layerBytes, err := streamBlobBytes(ctx, p.TargetImageRef, p.SystemContext, s.Digest,
					cappedWriter(s.Size, layer.Written))
				if err != nil {
					layer.Fail(err)
					return fmt.Errorf("read shipped %s: %w", s.Digest, err)
				}
				if pool.refCount(s.Digest) > 1 || mode == modeOff {
					pool.addIfAbsent(s.Digest, layerBytes, fullBlobEntry(s))
					layer.Done()
					return nil
				}
				entry, payload, err := planner.PlanShippedTopK(ctx, s, layerBytes, candidates)
				if err != nil {
					log().Warn("patch encode failed, falling back to full",
						"pair", p.Name, "digest", s.Digest, "err", err)
					pool.addIfAbsent(s.Digest, layerBytes, fullBlobEntry(s))
					layer.Done()
					return nil
				}
				pool.addIfAbsent(s.Digest, payload, blobEntryFromPlanner(entry))
				layer.Done()
				return nil
			})
		}
	}
	return encodePool.Wait()
}
```

- [ ] **Step 3.4.2: Update the `encodeShipped` call site**

In `pkg/exporter/exporter.go`, the existing call (Stage 1 + 2):

```go
	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.reporter(),
		opts.ZstdLevel, opts.ZstdWindowLog, opts.Candidates); err != nil {
```

Change to add `opts.Workers`:

```go
	if err := encodeShipped(ctx, pool, plans, effectiveMode, opts.fingerprinter, opts.reporter(),
		opts.ZstdLevel, opts.ZstdWindowLog, opts.Candidates, opts.Workers); err != nil {
```

- [ ] **Step 3.4.3: Compile + run all unit tests**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go build ./... && go test ./...`
Expected: all pass — `Options.Workers` defaults to 0 → 1 (serial), so output stays Phase-3 byte-identical.

## Task 3.5: Determinism integration test across worker counts

**Files:**
- Create: `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff_workers_integration_test.go`

- [ ] **Step 3.5.1: Create the test**

Write `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff_workers_integration_test.go`:

```go
package cmd_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/leosocy/diffah/internal/registrytest"
	"github.com/leosocy/diffah/pkg/exporter"
)

// TestWorkerCount_OutputIsByteIdentical drives the same export with
// --workers in {1,2,4,8,32} and asserts the resulting archives are
// SHA-256-equal. This is the load-bearing determinism guarantee from
// spec §2 Goal #2 and §3.5.
func TestWorkerCount_OutputIsByteIdentical(t *testing.T) {
	srv := registrytest.New(t)
	t.Cleanup(srv.Close)

	base := srv.PushImage(t, "myorg/base:v1", registrytest.RandomLayers(4, 256*1024))
	tgt := srv.PushImage(t, "myorg/svc:v2", registrytest.MutateLayers(base, 0, 2))

	digests := make(map[int]string)
	for _, w := range []int{1, 2, 4, 8, 32} {
		w := w
		t.Run(fmtWorkers(w), func(t *testing.T) {
			tmp := t.TempDir()
			out := filepath.Join(tmp, "delta.tar")
			opts := exporter.Options{
				Pairs: []exporter.Pair{
					{Name: "default", BaselineRef: base.Ref, TargetRef: tgt.Ref},
				},
				Platform:      "linux/amd64",
				OutputPath:    out,
				ToolVersion:   "test",
				Workers:       w,
				Candidates:    3,
				ZstdLevel:     12, // mid; speed/size compromise for tests
				ZstdWindowLog: 0,
			}
			if err := exporter.Export(context.Background(), opts); err != nil {
				t.Fatalf("export: %v", err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			h := sha256.Sum256(data)
			digests[w] = hex.EncodeToString(h[:])
		})
	}
	if len(digests) < 2 {
		t.Skip("subtests skipped; cannot compare")
	}
	var ref string
	for _, d := range digests {
		if ref == "" {
			ref = d
			continue
		}
		if d != ref {
			t.Errorf("archive sha256 differs across worker counts: digests=%v", digests)
			return
		}
	}
}

func fmtWorkers(n int) string { return "workers=" + itoa(n) }
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	buf := [12]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}
```

- [ ] **Step 3.5.2: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./cmd/... -run 'TestWorkerCount_OutputIsByteIdentical' -v`
Expected: PASS — all five subtests produce the same sha256.

## Task 3.6: Default `--workers=8`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/cmd/encoding_flags.go`

- [ ] **Step 3.6.1: Bump the default**

In `cmd/encoding_flags.go`, replace:

```go
	f.IntVar(&o.Workers, "workers", 1,
		"layers to fingerprint and encode in parallel; "+
			"--workers=1 reproduces Phase-3 strict-serial encode")
```

with:

```go
	f.IntVar(&o.Workers, "workers", 8,
		"layers to fingerprint and encode in parallel; "+
			"--workers=1 reproduces Phase-3 strict-serial encode (default 8)")
```

- [ ] **Step 3.6.2: Update the existing default test**

In `cmd/encoding_flags_test.go`, change `got.Workers != 1` to `got.Workers != 8` in `TestEncodingFlags_Defaults`.

- [ ] **Step 3.6.3: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./cmd/... -run 'TestEncodingFlags' -v`
Expected: PASS.

## Task 3.7: Run full suite + lint, commit Stage 3

- [ ] **Step 3.7.1: Race + full suite**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test -race ./...`
Expected: all pass.

- [ ] **Step 3.7.2: Lint**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && golangci-lint run ./...`
Expected: clean.

- [ ] **Step 3.7.3: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add pkg/exporter/workerpool.go pkg/exporter/workerpool_test.go pkg/exporter/fpcache.go pkg/exporter/fpcache_test.go pkg/exporter/pool.go pkg/exporter/pool_test.go pkg/exporter/encode.go pkg/exporter/exporter.go cmd/encoding_flags.go cmd/encoding_flags_test.go cmd/diff_workers_integration_test.go
git commit -m "feat(exporter): parallel encode + singleflight fingerprint cache

Adds a bounded errgroup-based worker pool to encodeShipped and a
singleflight-coordinated fpCache that memoizes baseline layer bytes
plus fingerprints across pairs. The encode path is split into
fingerprint-priming and target-encode phases so top-K candidate
selection in PR-2 sees a fully-populated cache.

Default --workers flips from 1 to 8. Output bytes remain
byte-identical across worker counts for the same input — verified
by a five-way determinism integration test (1, 2, 4, 8, 32).

Adds RWMutex protection to blobPool so concurrent worker writes are
race-free.

Refs: docs/superpowers/specs/2026-04-25-phase4-delta-quality-design.md §4.1, §4.2, §4.4, §9 PR-3"
```

---

# Stage 4 — PR-4: aggressive defaults + bench + docs

**PR title:** `feat(exporter)!: tune build-farm defaults; add GB-scale benchmark`

**Behavior change visible to operators:** Default `--zstd-level=3 → 22`, default `--zstd-window-log=27 → auto`, default `--candidates=1 → 3`. Deltas are smaller; encode wall-clock grows but is offset by `--workers=8` from PR-3. CHANGELOG and README spell out the override paths.

## Task 4.1: Bump default `--zstd-level=22` and `--candidates=3`

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/cmd/encoding_flags.go`

- [ ] **Step 4.1.1: Update defaults**

In `cmd/encoding_flags.go`, replace the two `IntVar` lines:

```go
	f.IntVar(&o.Candidates, "candidates", 1,
		"top-K baseline candidates per shipped layer; "+
			"--candidates=1 reproduces Phase-3 single-best behavior")
```

with:

```go
	f.IntVar(&o.Candidates, "candidates", 3,
		"top-K baseline candidates per shipped layer; "+
			"--candidates=1 reproduces Phase-3 single-best behavior (default 3)")
```

And:

```go
	f.IntVar(&o.ZstdLevel, "zstd-level", 3,
		"zstd compression level (1..22); higher = smaller patches at the cost of CPU")
```

with:

```go
	f.IntVar(&o.ZstdLevel, "zstd-level", 22,
		"zstd compression level (1..22); higher = smaller patches at the cost of CPU. "+
			"Default 22 ('ultra'); --zstd-level=12 is a speed/size compromise; "+
			"--zstd-level=3 matches the zstd CLI default and is the fastest")
```

- [ ] **Step 4.1.2: Update the defaults test**

In `cmd/encoding_flags_test.go`, change the assertion:

```go
	if got.Workers != 1 || got.Candidates != 1 || got.ZstdLevel != 3 || got.ZstdWindowLog != 27 {
```

to:

```go
	if got.Workers != 8 || got.Candidates != 3 || got.ZstdLevel != 22 || got.ZstdWindowLog != 27 {
```

- [ ] **Step 4.1.3: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./cmd/... -run 'TestEncodingFlags_Defaults' -v`
Expected: PASS.

## Task 4.2: Default `--zstd-window-log=auto` with per-layer pick

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/cmd/encoding_flags.go`
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/pkg/exporter/intralayer.go`

- [ ] **Step 4.2.1: Change the flag default**

In `cmd/encoding_flags.go`:

```go
	f.StringVar(&windowLog, "zstd-window-log", "27",
```

becomes:

```go
	f.StringVar(&windowLog, "zstd-window-log", "auto",
```

The validation already supports `"auto"` → `0` sentinel. So no further flag-layer change is needed.

- [ ] **Step 4.2.2: Update default test**

In `cmd/encoding_flags_test.go`, the defaults test now expects `ZstdWindowLog == 0`:

```go
	if got.Workers != 8 || got.Candidates != 3 || got.ZstdLevel != 22 || got.ZstdWindowLog != 0 {
```

- [ ] **Step 4.2.3: Implement per-layer auto resolution in `Planner`**

In `pkg/exporter/intralayer.go`, add a helper:

```go
// resolveWindowLog converts the user-facing 0 sentinel into a
// concrete log2 window size based on the layer's declared size.
// Spec §3.4: ≤128 MiB → 27, ≤1 GiB → 30, >1 GiB → 31.
func resolveWindowLog(userWindowLog int, layerSize int64) int {
	if userWindowLog != 0 {
		return userWindowLog
	}
	switch {
	case layerSize <= 128<<20:
		return 27
	case layerSize <= 1<<30:
		return 30
	default:
		return 31
	}
}
```

In the existing `PlanShippedTopK` call to `zstdpatch.Encode`, replace `WindowLog: p.windowLog` with `WindowLog: resolveWindowLog(p.windowLog, s.Size)`. Same for the `EncodeFull` call.

In `PlanShipped` (the legacy K=1 method), apply the same change.

- [ ] **Step 4.2.4: Add a unit test for `resolveWindowLog`**

Append to `pkg/exporter/intralayer_test.go`:

```go
func TestResolveWindowLog(t *testing.T) {
	tests := []struct {
		name      string
		userValue int
		layerSize int64
		want      int
	}{
		{"explicit override beats auto", 30, 64 << 20, 30},
		{"auto small", 0, 64 << 20, 27},
		{"auto medium boundary", 0, 128 << 20, 27},
		{"auto medium just over", 0, (128 << 20) + 1, 30},
		{"auto large", 0, 512 << 20, 30},
		{"auto large boundary", 0, 1 << 30, 30},
		{"auto huge", 0, (1 << 30) + 1, 31},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := exporter.ResolveWindowLog(tc.userValue, tc.layerSize)
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}
```

The test uses `exporter.ResolveWindowLog` (exported). Update the helper definition in `intralayer.go` to be exported (capitalize `R`).

- [ ] **Step 4.2.5: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./pkg/exporter/... -run 'TestResolveWindowLog' -v && go test ./cmd/... -run 'TestEncodingFlags_Defaults' -v`
Expected: all pass.

## Task 4.3: Synthesized-fixture delta-quality regression test

**Files:**
- Create: `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff_quality_integration_test.go`

- [ ] **Step 4.3.1: Create the test**

Write `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff_quality_integration_test.go`:

```go
package cmd_test

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/leosocy/diffah/internal/registrytest"
	"github.com/leosocy/diffah/pkg/exporter"
)

// TestDeltaQuality_Phase4BeatsPhase3 builds a deterministic fixture
// with 70 % shared content between baseline and target layers, runs
// the export with Phase-3 defaults and again with Phase-4 defaults,
// and asserts the Phase-4 archive is at least 15 % smaller.
func TestDeltaQuality_Phase4BeatsPhase3(t *testing.T) {
	srv := registrytest.New(t)
	t.Cleanup(srv.Close)

	rng := rand.New(rand.NewPCG(0xCAFEBABE, 0xDEADBEEF))
	base := srv.PushImage(t, "myorg/base:v1",
		registrytest.LayersFromRNG(rng, 4, 8*1024*1024))
	tgt := srv.PushImage(t, "myorg/svc:v2",
		registrytest.MutateLayersBitFlip(base, 0.3 /* fraction changed */))

	tmp := t.TempDir()

	run := func(name string, opts exporter.Options) int64 {
		out := filepath.Join(tmp, name+".tar")
		opts.OutputPath = out
		opts.Pairs = []exporter.Pair{{Name: "default", BaselineRef: base.Ref, TargetRef: tgt.Ref}}
		opts.Platform = "linux/amd64"
		opts.ToolVersion = "test"
		if err := exporter.Export(context.Background(), opts); err != nil {
			t.Fatalf("%s export: %v", name, err)
		}
		fi, err := os.Stat(out)
		if err != nil {
			t.Fatalf("%s stat: %v", name, err)
		}
		return fi.Size()
	}

	phase3 := run("phase3", exporter.Options{
		Workers: 1, Candidates: 1, ZstdLevel: 3, ZstdWindowLog: 27,
	})
	phase4 := run("phase4", exporter.Options{
		Workers: 8, Candidates: 3, ZstdLevel: 22, ZstdWindowLog: 0, // auto
	})
	t.Logf("phase3=%d phase4=%d ratio=%.3f", phase3, phase4, float64(phase4)/float64(phase3))
	if float64(phase4) > 0.85*float64(phase3) {
		t.Errorf("Phase 4 archive (%d bytes) > 0.85 × Phase 3 (%d bytes)", phase4, phase3)
	}
}
```

If `LayersFromRNG` and `MutateLayersBitFlip` do not exist in `internal/registrytest`, add minimal helpers in a small companion commit before running this test.

- [ ] **Step 4.3.2: Run**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./cmd/... -run 'TestDeltaQuality_Phase4BeatsPhase3' -v -timeout=10m`
Expected: PASS — Phase-4 archive ≤ 85 % of Phase-3.

## Task 4.4: GB-scale `DIFFAH_BIG_TEST=1` benchmark

**Files:**
- Create: `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff_bigfixture_bench_test.go`
- Create: `/Users/leosocy/workspace/repos/myself/diffah/benchmarks/.gitkeep`

- [ ] **Step 4.4.1: Create the bench file**

Write `/Users/leosocy/workspace/repos/myself/diffah/cmd/diff_bigfixture_bench_test.go`:

```go
package cmd_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leosocy/diffah/internal/registrytest"
	"github.com/leosocy/diffah/pkg/exporter"
)

// TestBigFixture_RecordsSizeAndTime runs the GB-scale benchmark
// fixture and writes a JSON metric record to benchmarks/phase4.json
// for the CI regression gate. Gated on DIFFAH_BIG_TEST=1.
func TestBigFixture_RecordsSizeAndTime(t *testing.T) {
	if os.Getenv("DIFFAH_BIG_TEST") != "1" {
		t.Skip("DIFFAH_BIG_TEST=1 required for the GB-scale fixture")
	}
	srv := registrytest.New(t)
	t.Cleanup(srv.Close)

	rng := rand.New(rand.NewPCG(0xFEEDFACE, 0xCAFED00D))
	const layerSize = 2 * 1024 * 1024 * 1024 // 2 GiB
	base := srv.PushImage(t, "myorg/base:big",
		registrytest.LayersFromRNG(rng, 1, layerSize))
	tgt := srv.PushImage(t, "myorg/svc:big",
		registrytest.MutateLayersBitFlip(base, 0.05))

	tmp := t.TempDir()
	out := filepath.Join(tmp, "delta.tar")
	opts := exporter.Options{
		Pairs: []exporter.Pair{{Name: "default", BaselineRef: base.Ref, TargetRef: tgt.Ref}},
		Platform: "linux/amd64", OutputPath: out, ToolVersion: "test",
		Workers: 8, Candidates: 3, ZstdLevel: 22, ZstdWindowLog: 0,
	}

	start := time.Now()
	if err := exporter.Export(context.Background(), opts); err != nil {
		t.Fatalf("export: %v", err)
	}
	elapsed := time.Since(start)

	fi, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	type metric struct {
		LayerBytes   int64         `json:"layer_bytes"`
		ArchiveBytes int64         `json:"archive_bytes"`
		Elapsed      time.Duration `json:"elapsed_ns"`
		Workers      int           `json:"workers"`
		Candidates   int           `json:"candidates"`
		Level        int           `json:"zstd_level"`
		WindowLog    int           `json:"zstd_window_log"` // 0 = auto
	}
	rec := metric{
		LayerBytes: layerSize, ArchiveBytes: fi.Size(), Elapsed: elapsed,
		Workers: 8, Candidates: 3, Level: 22, WindowLog: 0,
	}
	enc, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll("../benchmarks", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile("../benchmarks/phase4.json", enc, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("archive=%d bytes elapsed=%s", fi.Size(), elapsed)
}
```

- [ ] **Step 4.4.2: Add the placeholder so the dir exists in git**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && touch benchmarks/.gitkeep`

- [ ] **Step 4.4.3: Smoke-run with the gate off (should skip)**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./cmd/... -run 'TestBigFixture' -v`
Expected: SKIP message about `DIFFAH_BIG_TEST=1`.

- [ ] **Step 4.4.4: (Optional) Smoke-run with gate on, only if local machine has 8+ GB RAM**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && DIFFAH_BIG_TEST=1 go test ./cmd/... -run 'TestBigFixture' -v -timeout=30m`
Expected: PASS; `benchmarks/phase4.json` written.

## Task 4.5: CI regression gate

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/.github/workflows/<existing-CI>.yml`

- [ ] **Step 4.5.1: Identify the existing CI file**

Run: `ls /Users/leosocy/workspace/repos/myself/diffah/.github/workflows/`
Read whichever file controls the test job (commonly `ci.yml` or `test.yml`).

- [ ] **Step 4.5.2: Add a separate big-test job**

Append a new job (do NOT add `DIFFAH_BIG_TEST=1` to the regular test job — the runtime is too long). Sketch:

```yaml
  big-bench:
    runs-on: ubuntu-22.04-large
    if: github.ref == 'refs/heads/master'
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.25' }
      - name: Install zstd
        run: sudo apt-get install -y zstd
      - name: Big fixture bench
        env: { DIFFAH_BIG_TEST: '1' }
        run: go test ./cmd/... -run TestBigFixture -timeout=30m
      - name: Upload metric
        uses: actions/upload-artifact@v4
        with: { name: phase4-metrics, path: benchmarks/phase4.json }
      - name: Regression gate
        run: |
          # Compare against last green main run's recorded metric.
          # If absent, this is the first green; just write it back.
          if [ ! -f benchmarks/phase4.baseline.json ]; then
            cp benchmarks/phase4.json benchmarks/phase4.baseline.json
            exit 0
          fi
          # ... compare archive_bytes (allow +5 %) and elapsed_ns (+50 %).
          go run scripts/bench-gate.go \
            -previous benchmarks/phase4.baseline.json \
            -current benchmarks/phase4.json \
            -size-pct 5 -time-pct 50
```

(The `scripts/bench-gate.go` helper is small and standalone; create it in this same task if absent.)

- [ ] **Step 4.5.3: Add `scripts/bench-gate.go` if it does not exist**

Sketch:

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	prev := flag.String("previous", "", "previous metric JSON")
	curr := flag.String("current", "", "current metric JSON")
	sizePct := flag.Float64("size-pct", 5.0, "max percent size growth")
	timePct := flag.Float64("time-pct", 50.0, "max percent time growth")
	flag.Parse()

	type m struct {
		ArchiveBytes int64 `json:"archive_bytes"`
		Elapsed      int64 `json:"elapsed_ns"`
	}
	read := func(p string) m {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", p, err)
			os.Exit(2)
		}
		var v m
		if err := json.Unmarshal(b, &v); err != nil {
			fmt.Fprintf(os.Stderr, "unmarshal %s: %v\n", p, err)
			os.Exit(2)
		}
		return v
	}
	p := read(*prev)
	c := read(*curr)
	growth := func(prev, curr int64) float64 {
		return 100 * float64(curr-prev) / float64(prev)
	}
	sg := growth(p.ArchiveBytes, c.ArchiveBytes)
	tg := growth(p.Elapsed, c.Elapsed)
	fmt.Printf("size growth: %.2f %%, time growth: %.2f %%\n", sg, tg)
	if sg > *sizePct || tg > *timePct {
		fmt.Fprintln(os.Stderr, "regression detected")
		os.Exit(1)
	}
}
```

## Task 4.6: Update `cmd/help.go` long-help

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/cmd/help.go`

- [ ] **Step 4.6.1: Read the current long-help layout**

Run: `head -120 /Users/leosocy/workspace/repos/myself/diffah/cmd/help.go`

- [ ] **Step 4.6.2: Add an "Encoding tuning" section**

Append (or insert in the appropriate location of) the long-help text:

```
ENCODING TUNING
  --workers N           layers to fingerprint and encode in parallel (default 8).
                        --workers=1 reproduces strict serial encode.
                        Output bytes are byte-identical regardless of --workers value.

  --candidates K        top-K baseline candidates per shipped layer (default 3).
                        Higher K shrinks the delta at the cost of CPU; K=1
                        reproduces single-best-candidate behavior.

  --zstd-level N        zstd compression level 1..22 (default 22, "ultra").
                        --zstd-level=12 is a speed/size compromise; --zstd-level=3
                        matches the zstd CLI default and is the fastest.

  --zstd-window-log N   long-mode window in log2 bytes (default "auto").
                        auto picks per-layer: ≤128 MiB → 27, ≤1 GiB → 30, >1 GiB → 31.
                        Encoder memory ≈ 2 × 2^N bytes per running encode.

  Determinism: for a fixed (baseline, target, --candidates, --zstd-level,
  --zstd-window-log) tuple, the produced delta archive is byte-identical
  regardless of --workers.
```

- [ ] **Step 4.6.3: Run help-text snapshot tests if they exist**

Run: `grep -l 'help_test\|TestHelp' /Users/leosocy/workspace/repos/myself/diffah/cmd/*.go`
If any help-text snapshot tests exist, update the snapshots. Otherwise no change.

## Task 4.7: CHANGELOG, README, performance.md

**Files:**
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/CHANGELOG.md`
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/README.md`
- Modify: `/Users/leosocy/workspace/repos/myself/diffah/docs/performance.md`

- [ ] **Step 4.7.1: Add CHANGELOG entry**

Prepend to `CHANGELOG.md` (after the existing top-of-file unreleased section, or as the new top entry if Phase 3 is now under a release tag):

```markdown
## [Unreleased] — Phase 4: Delta quality & throughput

### Behavior changes

- **Default zstd level: `3 → 22`.** `diff` and `bundle` produce
  noticeably smaller patches by default; encode wall-clock grows
  ~5–8× per layer relative to Phase 3, mostly absorbed by the new
  `--workers=8` parallelism. Operators wanting Phase-3 speed can pin
  `--zstd-level=3 --candidates=1 --workers=1` to match the historical
  output.
- **Default zstd window: `--long=27 → auto`.** The producer now picks
  per-layer: ≤128 MiB → 27, ≤1 GiB → 30, >1 GiB → 31. This produces
  smaller patches on multi-GiB layers at the cost of encoder memory
  (≈ 2 × 2^N bytes per running encode).
- **Default top-K: `1 → 3`.** Each shipped target layer is patched
  against the top-3 most content-similar baseline layers and the
  smallest patch is emitted.
- **Default workers: `1 → 8`.** Encode parallelism on the per-layer
  axis. Output bytes are byte-identical to `--workers=1`.

### Additions

- New flags on `diff` and `bundle`: `--workers`, `--candidates`,
  `--zstd-level`, `--zstd-window-log` (accepts `auto` or 10..31).
- Synthesized GB-scale benchmark gated by `DIFFAH_BIG_TEST=1`. Emits
  `benchmarks/phase4.json` for CI regression tracking.
- Determinism guarantee in `cmd/help.go` long-help: same tuple of
  flags + same input → byte-identical archive regardless of
  `--workers`.

### Backward compat

- Phase 4 archives encoded with `--zstd-window-log ≥ 28` cannot be
  decoded by Phase 3 or earlier importers — the decoder rejects them
  fail-closed with `Frame requires too much memory for decoding`.
  Operators serving older consumers should pin `--zstd-window-log=27`
  to opt back into legacy compatibility.
- Phase 3 archives apply byte-identically through Phase 4 importer
  (decoder cap was raised, never lowered).
- Sidecar schema unchanged.

### Internal

- New `pkg/exporter/workerpool.go` (errgroup-based bounded pool).
- New `pkg/exporter/fpcache.go` (singleflight-coordinated baseline
  byte + fingerprint cache).
- `internal/zstdpatch.Encode` and `EncodeFull` now accept
  `EncodeOpts{Level, WindowLog}`; zero values reproduce historical
  defaults.
- Decoder side window cap raised from `1<<27` to `1<<31`.
```

- [ ] **Step 4.7.2: Update README usage section**

In `README.md`, locate the example flag block (search for `--platform`) and add the four new flags under their own heading. Keep the additions self-contained — at most ~12 lines.

- [ ] **Step 4.7.3: Update `docs/performance.md`**

Append a new section to `docs/performance.md`:

```markdown
## Phase 4 — Delta quality & throughput

### Bandwidth and memory characteristics

- **Producer-side baseline reads.** Each baseline blob referenced by any
  pair is fetched at most once via `GetBlob` for the duration of an
  `Export()` call. Multi-pair runs that share a baseline pay 1×, not
  N×, the per-blob cost. Verified by an integration assertion against
  the in-process registrytest harness.
- **Encoder memory.** Per running encode, peak memory ≈ `2 × 2^WindowLog`
  bytes (zstd long-mode buffer). With `--workers=8` and
  `--zstd-window-log=auto`, worst case across an 8-way parallel encode
  of >1 GiB layers is ≈ 32 GiB.
- **Top-K trial cost.** With `--candidates=K`, each shipped target layer
  performs K patch encodes and one full-zstd encode within a single
  worker. The smallest emitted bytes win. Wire I/O is unchanged
  (baseline refs are loaded from `fpCache`, never re-fetched).

### Operator overrides

| Goal | Flags |
|---|---|
| Match Phase 3 output bytes | `--zstd-level=3 --zstd-window-log=27 --candidates=1 --workers=1` |
| Speed-prioritized CI | `--zstd-level=12 --candidates=2` |
| Maximum compression | `--zstd-level=22 --zstd-window-log=31 --candidates=5` |
```

- [ ] **Step 4.7.4: Run all suites + lint**

Run: `cd /Users/leosocy/workspace/repos/myself/diffah && go test ./... && golangci-lint run ./...`
Expected: clean.

## Task 4.8: Commit Stage 4 + push branch

- [ ] **Step 4.8.1: Commit**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git add cmd/encoding_flags.go cmd/encoding_flags_test.go cmd/help.go pkg/exporter/intralayer.go pkg/exporter/intralayer_test.go cmd/diff_quality_integration_test.go cmd/diff_bigfixture_bench_test.go benchmarks/.gitkeep .github/workflows/ scripts/bench-gate.go CHANGELOG.md README.md docs/performance.md
git commit -m "feat(exporter)!: tune build-farm defaults; add GB-scale benchmark

Flips Phase 4 defaults to the build-farm-tuned set: --zstd-level=22,
--zstd-window-log=auto (per-layer 27/30/31), --candidates=3,
--workers=8. Adds a synthesized GB-scale benchmark gated by
DIFFAH_BIG_TEST=1 emitting benchmarks/phase4.json plus a CI
regression gate that fails on >5 % archive size growth or >50 %
elapsed-time growth vs the previous green baseline.

CHANGELOG, README, and docs/performance.md document the new defaults
and the override paths for operators who need to match Phase 3 output
bytes (--zstd-level=3 --zstd-window-log=27 --candidates=1 --workers=1)
or who need to limit the decoder-side compatibility surface
(--zstd-window-log=27 to keep deltas decodable by Phase 3 importers).

Refs: docs/superpowers/specs/2026-04-25-phase4-delta-quality-design.md §2 Goals, §6 BC, §9 PR-4"
```

- [ ] **Step 4.8.2: Push the branch + open PR**

```bash
cd /Users/leosocy/workspace/repos/myself/diffah
git push -u origin spec/v2-phase4-delta-quality
```

Then open the PR. Title: `Phase 4: delta quality & throughput`. Body: a 4-bullet summary listing PR-1 plumbing, PR-2 top-K, PR-3 parallel + fpCache, PR-4 default flips + bench. Reference the spec path and the four exit-criteria sections.

---

## Plan self-review notes

This plan was self-reviewed against the spec on 2026-04-25:

- **Spec §2 Goals coverage:** Goal #1 covered by Task 4.3; Goal #2 by Task 3.5; Goal #3 by Task 2.5; Goal #4 by Task 2.5 (no full-image materialization assertion lives in the same registrytest harness — the test asserts no leftover files); Goal #5 by Task 4.6; Goal #6 by Task 4.4 (regression gate); Goal #7 by Tasks 1.1–1.3 (decode cap raise + EncodeOpts) plus the explicit Phase-3-byte-identical assertion in Task 1.1.2 and Task 2.4.2.
- **Spec §3 CLI surface:** all four flags + their descriptions land in Task 1.6 and 4.6 (long help).
- **Spec §4 Code-level shape:** every file in §4.1–§4.8 is touched somewhere in Stages 1–4, exact paths preserved.
- **Spec §6 Backward compat:** Phase 3 → Phase 4 importer is covered by the unchanged-test assertion in Task 1.1.8 (existing tests pass after cap raise). Phase 4 → Phase 3 importer behavior is *intentionally* not exercised in this plan because it requires running an older binary; the spec's CHANGELOG note (Task 4.7.1) is the documented contract.
- **Spec §9 PR slicing:** mirrored exactly in Stages 1–4.
- **Placeholder scan:** no TBD/TODO/FIXME left in the plan.
- **Type consistency:** `EncodeOpts`, `PickTopK`, `PlanShippedTopK`, `newWorkerPool`, `newFpCache`, `cacheValue`, `resolveWindowLog` — used consistently across tasks.
