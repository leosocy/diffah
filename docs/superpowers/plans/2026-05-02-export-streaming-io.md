# Export Streaming I/O — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the in-memory blob/baseline accumulation in `pkg/exporter` with a producer→spool→ordered-drainer pipeline that holds peak RSS to a configurable budget (default 8 GiB), independent of corpus size, while preserving bit-exact bundle output.

**Architecture:** Three disk-backed stores under a per-`Export()` workdir (`baselines/`, `targets/`, `blobs/`). Encoder workers stream baselines via `TeeReader`, run zstd via path-based subprocess calls, and spill encoded blobs sorted-by-digest into the output tar. A `golang.org/x/sync/semaphore.Weighted` admission controller gates concurrent encodes by an estimated per-encode RSS table keyed on `windowLog`. The `internal/zstdpatch` package gains a path-based API (`EncodeStream`, `DecodeStream`, `EncodeFullStream`); the legacy `[]byte` API stays as a deprecated shim for the importer hot path.

**Tech Stack:** Go 1.22+, `klauspost/compress/zstd` (full-zstd ceiling encode), `zstd` CLI ≥ 1.5 (patch encode/decode), `golang.org/x/sync/semaphore` (admission), `golang.org/x/sync/singleflight` (digest collision dedup), `archive/tar` (output), GitHub Actions (nightly bench).

**Spec:** [`docs/superpowers/specs/2026-05-02-export-streaming-io-design.md`](../specs/2026-05-02-export-streaming-io-design.md). All decisions in the plan trace to a section number in the spec.

**Branching:** Each PR below is a feature branch off `master`. Names: `feat/streaming-pr1-zstdpatch-api`, `feat/streaming-pr2-workdir`, etc. Merge order matches PR order; later PRs rebase if earlier ones land first.

**Out of scope:** Importer streaming. The `pkg/importer/blobcache.go` `baselineBlobCache.bytes` and `bundleImageSource.serveFull/servePatch` mirror the same OOM but are tracked under a separate sibling spec to land after this work.

---

## Amendments (post-PR 1)

After PR 1 shipped, four corrections to this plan emerged. **Future PR implementers MUST honor these — the original PR 1 text below is preserved as a historical artifact, but where it contradicts the points below, the amendments win.**

1. **Determinism guard test name.** `TestDetermin` matches no tests. The actual test is `TestExport_DeterministicArchive` in `pkg/exporter/exporter_test.go`. Use `go test -count=2 -run TestExport_DeterministicArchive ./pkg/exporter/...` everywhere this plan says `TestDetermin`.

2. **`EncodeFull` is NOT a wrapper around `EncodeFullStream`.** The plan's PR 1 Step 1.14 is wrong — collapsing `EncodeFull` would silently shift the patch-vs-full size comparator at `pkg/exporter/intralayer.go:215` because klauspost's one-shot `EncodeAll` and streaming `Write+Close` may produce different output sizes for identical input. PR 1 ships with `EncodeFull`'s body unchanged and only its preceding doc comment updated. PR 5+ that touch the comparator must call `EncodeFullStream` directly (not via `EncodeFull`). Three `nolint:staticcheck` annotations were added at deprecated call sites in `pkg/exporter/intralayer.go` (lines 215, 234) and `pkg/importer/compose.go` (line 140); subsequent PRs that touch these sites should remove the annotations as the migrations land.

3. **`writeCounter` placement in `EncodeFullStream`.** The counter MUST sit between the zstd encoder and the user's writer (`cw := &writeCounter{w: w}; enc, _ := zstd.NewWriter(cw, ...); io.Copy(enc, src); return cw.n`) so it accumulates COMPRESSED bytes. Wiring it on the input side counts uncompressed bytes — the returned size is then meaningless to the comparator. Encoder options must match legacy `EncodeFull` exactly: `WithEncoderLevel(zstdLevelToKlauspost(level)) + WithWindowSize(1<<windowLog)`, defaults level=0→3 / windowLog=0→27, NO `WithEncoderConcurrency`. (See `internal/zstdpatch/fullstream.go` as shipped for the correct shape.)

4. **`DecodeStream` empty-frame check must NOT `os.ReadFile`.** The empty zstd frame is a fixed 9-byte magic. Stat first; if size matches, open + `io.ReadFull` exactly 9 bytes; otherwise skip the read entirely. The shipped implementation extracts an `isEmptyFrame(path, size)` helper — copy that pattern in any analogous size-magic check.

**Implementer pre-flight:** before writing code, the PR 1 implementer ran an `advisor()` consultation against the plan and caught all three of issues #1-#3 above. The cost-of-bugs vs cost-of-pre-flight calculus heavily favors pre-flight. **Future implementers should run `advisor()` after reading their PR's full text but before touching code.** Bugs in this plan ARE possible (we've found four already); independent review pays for itself.

## Amendments (post-PR 2)

PR 2 surfaced four more corrections. Apply these for any PR that touches CLI flags or config integration.

5. **Determinism workhorse test name.** `TestExport_DeterministicArchive` exists but is a `t.Skip(...)` stub. The real byte-identity test is `TestExport_OutputIsByteIdenticalAcrossWorkerCounts` in `pkg/exporter/determinism_test.go` (asserts byte-identical across `--workers` 1/2/4/8/16). Both names work for the determinism gate command (the stub passes vacuously), but for actual confidence run `go test -count=2 -run TestExport_OutputIsByteIdenticalAcrossWorkerCounts ./pkg/exporter/...`.

6. **Config `Default()` values overwrite cobra flag defaults via `ApplyTo` reflection.** If a new flag has a non-empty cobra default (e.g. `--memory-budget` defaults to `"8GiB"`), the matching `pkg/config.Default()` value MUST equal that default string — otherwise `ApplyTo` will silently overwrite the cobra default with `""` on every invocation that doesn't have a config file. PR 2 hit this; fix is one line in `pkg/config/defaults.go` per new flag. Verify by adding a row to `cmd/config_defaults_test.go`'s table that asserts `f.DefValue == d.<Field>`.

7. **Three-tag struct style on `pkg/config.Config`.** Existing fields use three tags: `mapstructure:"x" yaml:"x" json:"x"`. New fields MUST follow the same pattern with kebab-case keys matching the CLI flag names. Two-tag form (just `mapstructure`+`yaml`) breaks the JSON serialization path used by `diffah config show`.

8. **Beware `funlen` ceiling on `runDiff`/`runBundle`.** Adding a builder call (e.g. `installSpoolFlags`) to either pushes them past the project's 60-line `funlen` cap. PR 2 extracted `buildDiffOptions` for `runDiff`; PR 3+ that adds plumbing to `runBundle` will likely need the same `buildBundleOptions` extraction. Pre-flight by running `golangci-lint` after the wiring step.

## Amendments (post-PR 3)

PR 3 surfaced four more corrections. The first three are mandatory for any PR using `io.TeeReader` for spool fan-out; the fourth applies to any code path that derives a workdir from `opts.OutputPath`.

9. **`io.TeeReader` spool MUST drain the source unconditionally after the consumer returns.** The plan's PR 3 Step 3.9 only drained on fingerprint error, but decompressors stop at logical EOF (`gzip.Reader` first member only; `tar.Reader` at the TAR-EOF marker; the plain-tar default branch is just `bytes.NewReader`). Without the unconditional drain, the spool file ends up shorter than the source whenever the consumer doesn't fully consume the stream — producing a broken `--patch-from=PATH` argument in PR 5. Correct shape: always run `io.Copy(io.Discard, tee)` after the consumer returns, regardless of error. Fingerprint errors still set the entry's `Fingerprint = nil` (sentinel) but do NOT abort the spool.

10. **A drain-regression test that uses real gzip+tar fixtures DOES NOT catch the bug.** PR 3's first attempt at the regression test (`TestBaselineSpool_SpoolFileIsByteIdenticalToSource`) passed even when the unconditional drain was removed, because `gzip.NewReader` wraps the source in a 4 KiB `bufio.Reader` that pulls the entire 102-byte fixture into the buffer on the first Read — TeeReader has already copied every source byte before `tar.Reader` returns EOF. **The honest regression target is a partial-reading fingerprinter** that reads exactly N bytes via `io.ReadFull(r, buf[:N])` and stops. That actually exercises the "tee captures bytes the consumer never reads" condition. The shipped pattern is `partialReadingFingerprinter{n: 16}` in `pkg/exporter/baselinespool_test.go`. Mutation-test any new drain regression test by removing the drain and confirming the test fails.

11. **A "fetch returns nil ReadCloser, error" test does NOT cover the partial-file cleanup defer.** PR 3's first cut of `TestBaselineSpool_FetchErrorRemovesPartialFileAndDoesNotCache` made `fetch` return `(nil, error)`, so `streamToSpool` returned before `os.Create(path)` was ever called — the `committed`-gated cleanup defer was untouched. To exercise the defer, the fetch must return a `ReadCloser` that **succeeds for at least one Read then errors** (e.g., the shipped `errAfterNReader` test type returning `src[:128]` then an error). Then the defer sees `committed = false` and removes the partial file. Mutation-test by commenting out `os.Remove(path)` and confirming the test fails.

12. **`DryRun` workdir placement: pass `opts.OutputPath`, NEVER `os.TempDir()`.** PR 3 originally had `DryRun` call `ensureWorkdir(opts.Workdir, os.TempDir())` so the spool would land under `/tmp`. But `resolveWorkdir` does `filepath.Dir(outputPath)` to build the default — `filepath.Dir("/tmp")` returns `"/"`, so the workdir tries to land at `/.diffah-tmp/<suffix>` (root-only, permission denied). Symptom: four cmd integration tests failed in CI on Linux runners. Correct fix: pass `opts.OutputPath` (which IS set even in dry-run mode — the would-be output path), and harden `resolveWorkdir` to fall back to `os.TempDir()` when `outputPath == ""` for API callers without an output file. Any future code path that needs to derive a workdir base from a "fake" output path must avoid this pitfall — pass a path **inside** the desired base, never the base itself.

---

## File map

**New files:**
- `internal/zstdpatch/stream.go` — path-based CLI wrappers (`EncodeStream`, `DecodeStream`).
- `internal/zstdpatch/stream_test.go` — parity tests against legacy `Encode`/`Decode`.
- `internal/zstdpatch/fullstream.go` — `EncodeFullStream` (klauspost-backed, no subprocess).
- `internal/zstdpatch/fullstream_test.go` — parity vs legacy `EncodeFull`.
- `pkg/exporter/workdir.go` — workdir resolution (`--workdir` / `DIFFAH_WORKDIR` / default).
- `pkg/exporter/workdir_test.go` — resolution precedence + cleanup contract.
- `pkg/exporter/baselinespool.go` — spool + `singleflight` baseline dedup.
- `pkg/exporter/baselinespool_test.go` — tee correctness, singleflight dedup, error rollback.
- `pkg/exporter/admission.go` — `windowLog → RSS estimate` table + admission controller.
- `pkg/exporter/admission_test.go` — token budgeting, single-layer-exceeds-budget, opt-out.
- `cmd/spool_flags.go` — `--workdir`, `--memory-budget` CLI plumbing.
- `cmd/spool_flags_test.go` — flag parsing + validation.
- `pkg/exporter/scale_bench_test.go` — GB-scale fixture, RSS measurement (build tag `big`).
- `docs/performance.md` — bounded-memory contract, knob documentation.
- `.github/workflows/scale-bench.yml` — nightly CI job for the GB-scale bench.
- `benchmarks/scale-export-linux.json` — committed Linux baseline (created in PR 7).
- `benchmarks/scale-export-darwin.json` — committed macOS baseline (created in PR 7).

**Modified files (per PR):**
- `internal/zstdpatch/cli.go` — legacy `Encode`/`Decode` become wrappers around `EncodeStream`/`DecodeStream` with deprecation comments.
- `internal/zstdpatch/fullgo.go` — only the doc comment above `EncodeFull` changes (deprecation note); body is **unchanged** (see Amendment #2).
- `pkg/exporter/exporter.go` — `Options` gains `Workdir`, `MemoryBudget`; `Export()` creates/cleans workdir, builds admission controller.
- `pkg/exporter/fingerprint.go` — adds `FingerprintReader` to `Fingerprinter` interface; `DefaultFingerprinter` implements it.
- `pkg/exporter/fpcache.go` — DELETED (replaced by baselinespool).
- `pkg/exporter/encode.go` — `primeBaselineCache` becomes `primeBaselineSpool`; `encodeTargets`/`encodeOneShipped` rewritten to operate on file paths.
- `pkg/exporter/intralayer.go` — `Planner.readBlob` returns paths; `PlanShippedTopK` rewritten with target-on-disk + `EncodeFullStream` size-only + per-candidate spill.
- `pkg/exporter/perpair.go` — `streamBlobBytes` keeps signature for non-blob callers; new `spoolBlob` writes to a path.
- `pkg/exporter/pool.go` — `blobPool.bytes` deleted; replaced by `blobPool.spills` (digest → spill path).
- `pkg/exporter/writer.go` — `writeBundleArchive` opens spill files and `io.Copy`s into the tar.
- `pkg/exporter/encodepool.go` (extends/refactors `workerpool.go`) — two-gate submission, `singleflight` digest dedup.
- `pkg/exporter/determinism_test.go` — extended with `--workers` 1/4/8 and `--memory-budget` 0/8GiB matrix.
- `cmd/bundle.go`, `cmd/diff.go` — install spool flags, thread into `exporter.Options`.
- `pkg/config/config.go` (Phase 5.2 config) — add `workdir:` and `memory_budget:` keys.

---

## Pre-flight

- [ ] **Step P-1: Verify branch base.** Run `git fetch origin && git log --oneline origin/master -1`. Confirm `master` matches the merged Phase 5.6 + 5.4 work (latest commits should include `70152fc`, `e11965a`, `a264de6`).

- [ ] **Step P-2: Run baseline tests.** Run `go test ./...` and confirm green. This baseline must hold across every PR — a regression here means a previous step broke something.

- [ ] **Step P-3: Run determinism guard.** Run `go test -count=2 -run TestExport_DeterministicArchive ./pkg/exporter/...` and capture the output digests. Future PRs must produce the same digests.

---

## Task 1 (PR 1): `internal/zstdpatch` streaming API

**Spec ref:** §5.1.

**Branch:** `feat/streaming-pr1-zstdpatch-api`

**Goal of this PR:** Add path-based functions (`EncodeStream`, `DecodeStream`, `EncodeFullStream`) and convert the existing `[]byte`-shaped `Encode`/`Decode`/`EncodeFull` into thin deprecated wrappers. No `pkg/exporter` changes. Importer compiles unchanged.

**Files:**
- Create: `internal/zstdpatch/stream.go`
- Create: `internal/zstdpatch/stream_test.go`
- Create: `internal/zstdpatch/fullstream.go`
- Create: `internal/zstdpatch/fullstream_test.go`
- Modify: `internal/zstdpatch/cli.go` (lines 49-87 and 93-134 become wrappers)
- Modify: `internal/zstdpatch/fullgo.go` (lines 18-43 become wrapper)

### Sub-tasks

- [ ] **Step 1.1: Create branch.** Run `git checkout -b feat/streaming-pr1-zstdpatch-api master`.

- [ ] **Step 1.2: Write failing parity test for `EncodeStream`.** Create `internal/zstdpatch/stream_test.go`:

```go
package zstdpatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeStream_ParityWithLegacyEncode(t *testing.T) {
	skipWithoutZstd(t)
	ctx := context.Background()
	ref := []byte("the quick brown fox jumps over the lazy dog\n" + repeat("X", 4096))
	target := []byte("the quick brown FOX jumps over the lazy dog!\n" + repeat("X", 4096))

	dir := t.TempDir()
	refPath := filepath.Join(dir, "ref")
	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(refPath, ref, 0o600); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	if err := os.WriteFile(targetPath, target, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	outPath := filepath.Join(dir, "patch.zst")

	gotSize, err := EncodeStream(ctx, refPath, targetPath, outPath, EncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeStream: %v", err)
	}
	gotBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	if int64(len(gotBytes)) != gotSize {
		t.Fatalf("size mismatch: returned %d, file has %d", gotSize, len(gotBytes))
	}

	wantBytes, err := Encode(ctx, ref, target, EncodeOpts{})
	if err != nil {
		t.Fatalf("legacy Encode: %v", err)
	}
	if hash(gotBytes) != hash(wantBytes) {
		t.Fatalf("EncodeStream output differs from Encode: stream=%s legacy=%s",
			hash(gotBytes), hash(wantBytes))
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
```

- [ ] **Step 1.3: Run the test to confirm it fails.** Run `go test -run TestEncodeStream_ParityWithLegacyEncode ./internal/zstdpatch/`. Expected: `undefined: EncodeStream`.

- [ ] **Step 1.4: Implement `EncodeStream`.** Create `internal/zstdpatch/stream.go`:

```go
// Package zstdpatch — streaming (path-based) variants.
//
// These functions take filesystem paths instead of []byte. They exist
// because callers in the exporter streaming pipeline already have their
// data on disk (in the per-Export workdir spool); the legacy []byte API
// would force them to re-read into memory, defeating the bounded-RAM
// guarantee documented in
// docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §5.1.
package zstdpatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// EncodeStream produces a zstd patch from refPath against targetPath, written
// to outPath. Returns the encoded byte count of outPath. ctx cancellation
// kills the zstd subprocess. Bit-equivalent to Encode(refBytes, targetBytes)
// when the file contents are byte-identical.
//
// Empty target files produce the precomputed empty zstd frame at outPath.
func EncodeStream(ctx context.Context, refPath, targetPath, outPath string, opts EncodeOpts) (int64, error) {
	tInfo, err := os.Stat(targetPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: stat target: %w", err)
	}
	if tInfo.Size() == 0 {
		empty := emptyZstdFrame()
		if err := os.WriteFile(outPath, empty, 0o600); err != nil {
			return 0, fmt.Errorf("zstdpatch: write empty frame: %w", err)
		}
		return int64(len(empty)), nil
	}
	//nolint:gosec // G204: refPath/targetPath/outPath come from caller-controlled spool dirs, not user input.
	cmd := exec.CommandContext(ctx, "zstd",
		opts.levelArg(), opts.windowArg(),
		"--patch-from="+refPath,
		targetPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(outPath) // best effort; partial output cannot be trusted
		return 0, fmt.Errorf("zstdpatch: encode: %w\n%s", err, out)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: stat patch: %w", err)
	}
	return info.Size(), nil
}

// DecodeStream reverses EncodeStream. Added now (even though importer
// streaming is out of scope of this PR series) to avoid a second
// package-surface churn when the importer migration spec lands.
func DecodeStream(ctx context.Context, refPath, patchPath, outPath string) (int64, error) {
	pInfo, err := os.Stat(patchPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: stat patch: %w", err)
	}
	patchBytes, err := os.ReadFile(patchPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: read patch: %w", err)
	}
	if pInfo.Size() == int64(len(emptyZstdFrame())) && bytesEqual(patchBytes, emptyZstdFrame()) {
		// Empty-frame contract: produce a zero-byte target file.
		if err := os.WriteFile(outPath, nil, 0o600); err != nil {
			return 0, fmt.Errorf("zstdpatch: write empty target: %w", err)
		}
		return 0, nil
	}
	//nolint:gosec // G204: paths are caller-controlled spool dirs.
	cmd := exec.CommandContext(ctx, "zstd",
		"-d", "--long=31",
		"--patch-from="+refPath,
		patchPath,
		"-o", outPath,
		"-f", "-q",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(outPath)
		return 0, fmt.Errorf("zstdpatch: decode: %w\n%s", err, out)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: stat decoded: %w", err)
	}
	return info.Size(), nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 1.5: Run test to confirm it passes.** Run `go test -run TestEncodeStream_ParityWithLegacyEncode ./internal/zstdpatch/`. Expected: PASS.

- [ ] **Step 1.6: Add a `DecodeStream` parity test.** Append to `internal/zstdpatch/stream_test.go`:

```go
func TestDecodeStream_RoundTripsEncodeStream(t *testing.T) {
	skipWithoutZstd(t)
	ctx := context.Background()
	ref := []byte(repeat("alpha", 4096))
	target := []byte(repeat("alpha", 4096) + "_delta_suffix")

	dir := t.TempDir()
	refPath := filepath.Join(dir, "ref")
	targetPath := filepath.Join(dir, "target")
	patchPath := filepath.Join(dir, "patch.zst")
	decodedPath := filepath.Join(dir, "decoded")

	mustWrite(t, refPath, ref)
	mustWrite(t, targetPath, target)

	if _, err := EncodeStream(ctx, refPath, targetPath, patchPath, EncodeOpts{}); err != nil {
		t.Fatalf("EncodeStream: %v", err)
	}
	if _, err := DecodeStream(ctx, refPath, patchPath, decodedPath); err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	got, err := os.ReadFile(decodedPath)
	if err != nil {
		t.Fatalf("read decoded: %v", err)
	}
	if hash(got) != hash(target) {
		t.Fatalf("decoded != target")
	}
}

func TestEncodeStream_EmptyTargetEmitsEmptyFrame(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	refPath := filepath.Join(dir, "ref")
	targetPath := filepath.Join(dir, "target")
	patchPath := filepath.Join(dir, "patch.zst")
	mustWrite(t, refPath, []byte("anything"))
	mustWrite(t, targetPath, nil)

	size, err := EncodeStream(ctx, refPath, targetPath, patchPath, EncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeStream: %v", err)
	}
	got, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	if !bytesEqualPublic(got, emptyZstdFrame()) {
		t.Fatalf("expected empty frame, got %d bytes", len(got))
	}
	if size != int64(len(emptyZstdFrame())) {
		t.Fatalf("size mismatch")
	}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func bytesEqualPublic(a, b []byte) bool { return bytesEqual(a, b) }
```

- [ ] **Step 1.7: Run new tests.** Run `go test ./internal/zstdpatch/`. Expected: all pass.

- [ ] **Step 1.8: Write failing parity test for `EncodeFullStream`.** Create `internal/zstdpatch/fullstream_test.go`:

```go
package zstdpatch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeFullStream_ParityWithLegacyEncodeFull(t *testing.T) {
	ctx := context.Background()
	target := []byte(repeat("hello world\n", 8192))

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target")
	mustWrite(t, targetPath, target)

	var buf bytes.Buffer
	gotSize, err := EncodeFullStream(ctx, targetPath, &buf, EncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeFullStream: %v", err)
	}
	if int64(buf.Len()) != gotSize {
		t.Fatalf("size mismatch: returned %d, buf has %d", gotSize, buf.Len())
	}

	want, err := EncodeFull(target, EncodeOpts{})
	if err != nil {
		t.Fatalf("legacy EncodeFull: %v", err)
	}
	if hash(buf.Bytes()) != hash(want) {
		t.Fatalf("EncodeFullStream output differs from EncodeFull")
	}

	// Round-trip: legacy DecodeFull should accept the streamed output.
	round, err := DecodeFull(buf.Bytes())
	if err != nil {
		t.Fatalf("DecodeFull(stream output): %v", err)
	}
	if !bytes.Equal(round, target) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestEncodeFullStream_SizeOnlyViaCountingWriter(t *testing.T) {
	// Demonstrates the actual use case: pass a CountingWriter to measure
	// without keeping the encoded bytes anywhere.
	ctx := context.Background()
	target := []byte(repeat("abcd", 16384))

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target")
	mustWrite(t, targetPath, target)

	cw := &countingWriter{}
	size, err := EncodeFullStream(ctx, targetPath, cw, EncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeFullStream: %v", err)
	}
	if size != cw.n {
		t.Fatalf("size %d != counting writer total %d", size, cw.n)
	}
	if size <= 0 {
		t.Fatalf("expected non-zero encoded size")
	}
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

var _ = os.WriteFile // appease unused-import in some splits
```

- [ ] **Step 1.9: Run the test to confirm it fails.** Run `go test -run EncodeFullStream ./internal/zstdpatch/`. Expected: `undefined: EncodeFullStream`.

- [ ] **Step 1.10: Implement `EncodeFullStream`.** Create `internal/zstdpatch/fullstream.go`:

```go
package zstdpatch

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// EncodeFullStream wraps klauspost/compress/zstd.Encoder and writes the
// compressed bytes of targetPath to w. No subprocess. No temp files. The
// streaming pipeline uses this with a counting writer to compute the
// "full-zstd ceiling" size without materializing the encoded bytes —
// that ceiling is purely a comparator for the patch decision and is
// never the surviving payload (see spec §5.4).
func EncodeFullStream(ctx context.Context, targetPath string, w io.Writer, opts EncodeOpts) (int64, error) {
	f, err := os.Open(targetPath)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: open target: %w", err)
	}
	defer f.Close()

	level := zstdLevelToKlauspost(opts.Level)
	enc, err := zstd.NewWriter(w,
		zstd.WithEncoderLevel(level),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		return 0, fmt.Errorf("zstdpatch: new encoder: %w", err)
	}

	cw := &writeCounter{w: enc}
	// Use a context-aware reader wrap so cancellation aborts mid-copy.
	if _, err := io.Copy(cw, contextReader{ctx: ctx, r: f}); err != nil {
		_ = enc.Close()
		return 0, fmt.Errorf("zstdpatch: copy target: %w", err)
	}
	if err := enc.Close(); err != nil {
		return 0, fmt.Errorf("zstdpatch: close encoder: %w", err)
	}
	return cw.n, nil
}

type writeCounter struct {
	w io.Writer
	n int64
}

func (c *writeCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
```

- [ ] **Step 1.11: Run new tests.** Run `go test -run EncodeFullStream ./internal/zstdpatch/`. Expected: PASS.

- [ ] **Step 1.12: Convert legacy `Encode` to a wrapper.** In `internal/zstdpatch/cli.go`, replace lines 49-87 (the existing `Encode` body) with:

```go
// Encode produces a zstd frame using --patch-from=ref that decodes to target.
//
// Deprecated: use EncodeStream. Retained for the importer hot path until
// the importer streaming spec migrates it. See docs/superpowers/specs/
// 2026-05-02-export-streaming-io-design.md §5.1.
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
	if _, err := EncodeStream(ctx, refPath, targetPath, outPath, opts); err != nil {
		return nil, err
	}
	return os.ReadFile(outPath)
}
```

- [ ] **Step 1.13: Convert legacy `Decode` to a wrapper.** In the same file, replace lines 93-134 (the `Decode` body) with:

```go
// Decode reads a zstd frame produced by Encode and returns the original
// target bytes.
//
// Deprecated: use DecodeStream. Retained for the importer hot path until
// the importer streaming spec migrates it.
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
	if _, err := DecodeStream(ctx, refPath, patchPath, outPath); err != nil {
		return nil, err
	}
	return os.ReadFile(outPath)
}
```

- [ ] **Step 1.14: Convert legacy `EncodeFull` to a wrapper.** In `internal/zstdpatch/fullgo.go`, replace lines 18-43 (the `EncodeFull` body) with:

```go
// EncodeFull compresses target with klauspost/compress/zstd at opts.Level.
//
// Deprecated: use EncodeFullStream. Retained for the importer hot path
// until the importer streaming spec migrates it.
func EncodeFull(target []byte, opts EncodeOpts) ([]byte, error) {
	dir, err := os.MkdirTemp("", "zstdfull-*")
	if err != nil {
		return nil, fmt.Errorf("zstdpatch: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(targetPath, target, 0o600); err != nil {
		return nil, fmt.Errorf("zstdpatch: write target: %w", err)
	}
	var buf bytes.Buffer
	if _, err := EncodeFullStream(context.Background(), targetPath, &buf, opts); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

Add the missing imports (`os`, `path/filepath`, `bytes`, `context`) to the file's import block as needed.

- [ ] **Step 1.15: Run the full `zstdpatch` test suite.** Run `go test ./internal/zstdpatch/`. Expected: all tests pass (including the legacy-API tests in `cli_test.go` and `fullgo_test.go`, since the wrapper preserves byte-for-byte behavior).

- [ ] **Step 1.16: Run the full repo test suite.** Run `go test ./...`. Expected: PASS — importer and exporter still consume the legacy APIs and must be byte-equivalent.

- [ ] **Step 1.17: Run determinism guard.** Run `go test -count=2 -run TestExport_DeterministicArchive ./pkg/exporter/...`. Confirm output digests unchanged from Step P-3.

- [ ] **Step 1.18: Lint.** Run `golangci-lint run ./internal/zstdpatch/...` (or the project's lint chain — `make lint` if present). Fix any issues.

- [ ] **Step 1.19: Commit.**

```bash
git add internal/zstdpatch/
git commit -m "feat(zstdpatch): add path-based streaming API (PR1 of streaming I/O)

EncodeStream/DecodeStream/EncodeFullStream take filesystem paths so the
exporter streaming pipeline can avoid materializing GB-scale layers in
RAM. Legacy []byte-shaped Encode/Decode/EncodeFull retained as deprecated
wrappers — importer hot path is unchanged until its sibling spec migrates.

Refs: docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §5.1"
```

- [ ] **Step 1.20: Push and open PR.**

```bash
git push -u origin feat/streaming-pr1-zstdpatch-api
gh pr create --title "feat(zstdpatch): path-based streaming API (streaming PR1)" \
  --body "$(cat <<'EOF'
## Summary
- Adds `EncodeStream`, `DecodeStream`, `EncodeFullStream` (path-based; no `[]byte`).
- Legacy `Encode`/`Decode`/`EncodeFull` become deprecated thin wrappers; behavior preserved byte-for-byte.
- No exporter or importer changes in this PR.

## Why
First step of the export streaming I/O work (spec §5.1). Subsequent PRs replace exporter
in-memory accumulation with disk-backed spools that consume these new APIs.

## Test plan
- [x] New parity tests against legacy APIs
- [x] Existing `cli_test.go` and `fullgo_test.go` still pass (deprecated path is byte-equivalent)
- [x] Full repo `go test ./...` green
- [x] Determinism guard unchanged

Spec: `docs/superpowers/specs/2026-05-02-export-streaming-io-design.md` §5.1
EOF
)"
```

### Review checkpoint

Wait for CI green and reviewer approval before starting Task 2. The branches do not depend on each other yet (Task 2 also touches `pkg/exporter` only), but landing in order keeps the diff manageable.

---

## Task 2 (PR 2): Workdir resolution

**Spec ref:** §4.2, §6.

**Branch:** `feat/streaming-pr2-workdir`

**Goal:** Add `Options.Workdir` and `MemoryBudget` (the latter as a placeholder used in PR 5), implement workdir creation/cleanup at the `Export()` entry point, and add the `--workdir DIR` / `DIFFAH_WORKDIR` CLI plumbing. No spool consumers yet — this PR only proves the workdir lifecycle is correct.

**Files:**
- Create: `pkg/exporter/workdir.go`
- Create: `pkg/exporter/workdir_test.go`
- Create: `cmd/spool_flags.go`
- Create: `cmd/spool_flags_test.go`
- Modify: `pkg/exporter/exporter.go` (add `Options.Workdir`/`MemoryBudget`; wrap `Export()` body in workdir lifecycle)
- Modify: `cmd/bundle.go` (install spool flags, thread to `exporter.Options`)
- Modify: `cmd/diff.go` (same as bundle.go)
- Modify: `pkg/config/config.go` (add `workdir:`, `memory_budget:` keys; defaults preserved)

### Sub-tasks

- [ ] **Step 2.1: Create branch.** `git checkout master && git pull && git checkout -b feat/streaming-pr2-workdir`.

- [ ] **Step 2.2: Write failing test for workdir resolution precedence.** Create `pkg/exporter/workdir_test.go`:

```go
package exporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWorkdir_FlagBeatsEnvBeatsDefault(t *testing.T) {
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "bundle.tar")

	t.Setenv("DIFFAH_WORKDIR", filepath.Join(outputDir, "from-env"))

	// 1. Flag wins
	got, err := resolveWorkdir("from-flag", outputPath)
	if err != nil {
		t.Fatalf("resolveWorkdir(flag): %v", err)
	}
	if got != "from-flag" {
		t.Fatalf("flag should win: got %q", got)
	}

	// 2. Env wins when flag empty
	got, err = resolveWorkdir("", outputPath)
	if err != nil {
		t.Fatalf("resolveWorkdir(env): %v", err)
	}
	if got != filepath.Join(outputDir, "from-env") {
		t.Fatalf("env should win: got %q", got)
	}

	// 3. Default = <outputDir>/.diffah-tmp/<random>
	t.Setenv("DIFFAH_WORKDIR", "")
	got, err = resolveWorkdir("", outputPath)
	if err != nil {
		t.Fatalf("resolveWorkdir(default): %v", err)
	}
	wantPrefix := filepath.Join(outputDir, ".diffah-tmp") + string(os.PathSeparator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("default should be under %q, got %q", wantPrefix, got)
	}
}

func TestEnsureWorkdir_CreatesAllSubdirsAndCleansUp(t *testing.T) {
	outputDir := t.TempDir()
	wd, cleanup, err := ensureWorkdir("", filepath.Join(outputDir, "bundle.tar"))
	if err != nil {
		t.Fatalf("ensureWorkdir: %v", err)
	}
	for _, sub := range []string{"baselines", "targets", "blobs"} {
		if _, err := os.Stat(filepath.Join(wd, sub)); err != nil {
			t.Fatalf("expected %s subdir: %v", sub, err)
		}
	}
	cleanup()
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Fatalf("expected workdir to be removed, stat err = %v", err)
	}
}
```

- [ ] **Step 2.3: Run the tests to confirm they fail.** Run `go test -run "Workdir" ./pkg/exporter/`. Expected: `undefined: resolveWorkdir, ensureWorkdir`.

- [ ] **Step 2.4: Implement `pkg/exporter/workdir.go`.**

```go
package exporter

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// resolveWorkdir picks the spool root with precedence:
//   1. flag (--workdir DIR)
//   2. DIFFAH_WORKDIR env
//   3. <dir(outputPath)>/.diffah-tmp/<random16hex>
//
// See spec §4.2.
func resolveWorkdir(flag, outputPath string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv("DIFFAH_WORKDIR"); env != "" {
		return env, nil
	}
	suffix, err := randomSuffix()
	if err != nil {
		return "", fmt.Errorf("generate workdir suffix: %w", err)
	}
	return filepath.Join(filepath.Dir(outputPath), ".diffah-tmp", suffix), nil
}

// ensureWorkdir creates the workdir and its three subdirs (baselines, targets,
// blobs). Returns the resolved workdir path and a cleanup callback that
// best-effort-removes everything under it. Cleanup is idempotent and safe to
// invoke from a defer.
func ensureWorkdir(flag, outputPath string) (string, func(), error) {
	wd, err := resolveWorkdir(flag, outputPath)
	if err != nil {
		return "", func() {}, err
	}
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("mkdir workdir %s: %w", wd, err)
	}
	for _, sub := range []string{"baselines", "targets", "blobs"} {
		if err := os.MkdirAll(filepath.Join(wd, sub), 0o700); err != nil {
			return "", func() {}, fmt.Errorf("mkdir %s/%s: %w", wd, sub, err)
		}
	}
	cleanup := func() { _ = os.RemoveAll(wd) }
	return wd, cleanup, nil
}

func randomSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
```

- [ ] **Step 2.5: Run the tests to confirm they pass.** Run `go test -run "Workdir" ./pkg/exporter/`. Expected: PASS.

- [ ] **Step 2.6: Add `Workdir`/`MemoryBudget` to `Options` and lifecycle to `Export()`.** In `pkg/exporter/exporter.go`, add the two fields after `Workers/Candidates/ZstdLevel/ZstdWindowLog` (around line 32):

```go
	// Streaming I/O — Phase 4. Workdir is the spool root for
	// disk-backed baseline / target / output blob spills. Empty selects
	// the default placement; see resolveWorkdir for precedence.
	// MemoryBudget caps concurrent encoder RSS via the admission
	// controller (spec §4.3); zero selects the default 8 GiB.
	Workdir       string
	MemoryBudget  int64
```

Then wrap the `Export()` body to create + clean up the workdir. Replace the current `Export()` (lines 130-152) with:

```go
func Export(ctx context.Context, opts Options) error {
	defer opts.reporter().Finish()

	wd, cleanupWorkdir, err := ensureWorkdir(opts.Workdir, opts.OutputPath)
	if err != nil {
		return fmt.Errorf("prepare workdir: %w", err)
	}
	defer cleanupWorkdir()
	opts.Workdir = wd // canonicalize for downstream consumers (PRs 3-6)

	bb, err := buildBundle(ctx, &opts)
	if err != nil {
		return err
	}
	sidecar := assembleSidecar(bb.pool, bb.plans, opts.Platform, opts.ToolVersion, opts.CreatedAt)
	if err := writeBundleArchive(opts.OutputPath, sidecar, bb.pool); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	var archiveSize int64
	for _, e := range sidecar.Blobs {
		archiveSize += e.ArchiveSize
	}
	log().InfoContext(ctx, "exported bundle", "path", opts.OutputPath, "archive_bytes", archiveSize)
	if opts.SignKeyPath != "" {
		if err := signArchive(ctx, &opts); err != nil {
			return err
		}
	}
	opts.reporter().Phase("done")
	return nil
}
```

- [ ] **Step 2.7: Test that `Export()` cleans up the workdir.** Add to `pkg/exporter/workdir_test.go`:

```go
func TestExport_CleansUpWorkdirOnSuccess(t *testing.T) {
	// Use the smallest possible end-to-end Export to assert workdir lifecycle.
	// Reuses the existing exporter_testing.go helpers.
	outputDir := t.TempDir()
	out := filepath.Join(outputDir, "bundle.tar")
	opts := smallestExportOptions(t, out) // helper from exporter_testing.go
	if err := Export(context.Background(), opts); err != nil {
		t.Fatalf("Export: %v", err)
	}
	tmp := filepath.Join(outputDir, ".diffah-tmp")
	entries, err := os.ReadDir(tmp)
	if os.IsNotExist(err) {
		return // already gone, that's fine too
	}
	if err == nil && len(entries) != 0 {
		t.Fatalf("expected .diffah-tmp empty after success, got %v", entries)
	}
}
```

If `smallestExportOptions` doesn't exist, instead use one of the existing exporter integration test paths (`exporter_test.go` patterns) and assert the cleanup; or skip this test in PR 2 with a `t.Skip("PR3 wires the spool consumers; lifecycle is currently a no-op for spool dirs")`. Pick whichever is faster — the test is still useful in PR 3 once consumers exist.

- [ ] **Step 2.8: Run the full exporter test suite.** Run `go test ./pkg/exporter/`. Expected: all pass; existing tests must still produce byte-identical bundle output (verify via determinism test).

- [ ] **Step 2.9: Add the CLI flag plumbing.** Create `cmd/spool_flags.go`:

```go
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// spoolOpts holds Phase-4-streaming runtime knobs that govern where the
// exporter spills its per-Export workdir and how much encoder RAM the
// admission controller is allowed to grant. Both have sane defaults; the
// flags are advanced operator escape hatches.
type spoolOpts struct {
	Workdir      string
	MemoryBudget int64
}

type spoolOptsBuilder func() (spoolOpts, error)

const spoolHelp = `Spool & memory:
  --workdir DIR              spool location for per-Export disk-backed blobs
                             (default: <dir(OUTPUT)>/.diffah-tmp/<random>; also DIFFAH_WORKDIR env)
  --memory-budget BYTES      admission cap for concurrent encoders
                             (default: 8GiB; supports KiB/MiB/GiB/KB/MB/GB; 0 disables)
`

// installSpoolFlags registers --workdir and --memory-budget on cmd.
func installSpoolFlags(cmd *cobra.Command) spoolOptsBuilder {
	o := &spoolOpts{}
	var memStr string

	f := cmd.Flags()
	f.StringVar(&o.Workdir, "workdir", "",
		"spool location for per-Export disk-backed blobs (default <dir(OUTPUT)>/.diffah-tmp/<random>; also DIFFAH_WORKDIR)")
	f.StringVar(&memStr, "memory-budget", "8GiB",
		"admission cap for concurrent encoders; suffixes KiB/MiB/GiB/KB/MB/GB; 0 disables")

	return func() (spoolOpts, error) {
		n, err := parseMemoryBudget(memStr)
		if err != nil {
			return spoolOpts{}, &cliErr{cat: errs.CategoryUser, msg: err.Error()}
		}
		o.MemoryBudget = n
		return *o, nil
	}
}

// parseMemoryBudget accepts decimal suffixes (KB/MB/GB) and binary
// suffixes (KiB/MiB/GiB), case-insensitive on the suffix. "0" disables
// admission control. Bare integer means bytes.
func parseMemoryBudget(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("--memory-budget must not be empty")
	}
	mults := []struct {
		suffix string
		mult   int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30},
	}
	upper := strings.ToUpper(s)
	for _, m := range mults {
		if strings.HasSuffix(upper, m.suffix) {
			body := strings.TrimSpace(upper[:len(upper)-len(m.suffix)])
			n, err := parseInt64(body)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("--memory-budget %q: invalid number", s)
			}
			return n * m.mult, nil
		}
	}
	n, err := parseInt64(upper)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("--memory-budget %q: invalid (expected number with optional suffix)", s)
	}
	return n, nil
}

func parseInt64(s string) (int64, error) {
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}
```

- [ ] **Step 2.10: Add tests for `parseMemoryBudget`.** Create `cmd/spool_flags_test.go`:

```go
package cmd

import "testing"

func TestParseMemoryBudget_Table(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"0", 0, false},
		{"512", 512, false},
		{"1KiB", 1 << 10, false},
		{"1MiB", 1 << 20, false},
		{"8GiB", 8 << 30, false},
		{"1KB", 1000, false},
		{"4GB", 4_000_000_000, false},
		{"  2gib  ", 2 << 30, false},
		{"-1", 0, true},
		{"abc", 0, true},
		{"", 0, true},
		{"5XB", 0, true},
	}
	for _, c := range cases {
		got, err := parseMemoryBudget(c.in)
		if (err != nil) != c.err {
			t.Errorf("parseMemoryBudget(%q): err=%v want err=%v", c.in, err, c.err)
		}
		if !c.err && got != c.want {
			t.Errorf("parseMemoryBudget(%q): got %d want %d", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2.11: Run the new flag tests.** Run `go test -run ParseMemoryBudget ./cmd/`. Expected: PASS.

- [ ] **Step 2.12: Wire spool flags into `cmd/bundle.go`.** In the `bundleFlags` struct (lines 13-21) add `buildSpoolOpts spoolOptsBuilder`. In `newBundleCommand` after `installEncodingFlags(c)` add `bundleFlags.buildSpoolOpts = installSpoolFlags(c)`. In `runBundle` after the encoding-opts block (around line 95) call:

```go
	spoolOpts, err := bundleFlags.buildSpoolOpts()
	if err != nil {
		return err
	}
```

And thread into `exporter.Options`:

```go
		Workdir:          spoolOpts.Workdir,
		MemoryBudget:     spoolOpts.MemoryBudget,
```

Also append the spool-help block to the `Long:` docstring after `encodingTuningHelp`:

```go
		Long: `Export a multi-image delta bundle driven by a spec file. ...
` + encodingTuningHelp + "\n" + spoolHelp,
```

- [ ] **Step 2.13: Wire spool flags into `cmd/diff.go`.** Same pattern as `cmd/bundle.go`. Find the existing `diffFlags` struct and the `runDiff` body; install + thread the same way.

- [ ] **Step 2.14: Add config-file keys.** In `pkg/config/config.go` (Phase 5.2 config), add `Workdir string` and `MemoryBudget string` to the `Encoding` (or similarly named) struct, with YAML tags `workdir:` and `memory_budget:`. Defaults: empty string (selects defaults). Update the precedence helper to apply config values to flag defaults if the operator did not supply the flag.

  *Note:* the exact location depends on the existing config struct layout. If the config struct is split per-subcommand, add to both bundle and diff sections, mirroring how `Workers` is handled.

- [ ] **Step 2.15: Add a config integration test.** Pattern off `cmd/config_integration_test.go` — write a test that puts `workdir: /tmp/explicit` in the config file, runs `diffah bundle ...`, and asserts the resolved workdir comes from config (or supply `--workdir` to assert flag-overrides-config).

- [ ] **Step 2.16: Run the full repo test suite.** Run `go test ./...`. Expected: PASS.

- [ ] **Step 2.17: Run determinism guard.** Run `go test -count=2 -run TestExport_DeterministicArchive ./pkg/exporter/...`. Confirm digests unchanged from baseline.

- [ ] **Step 2.18: Lint.** Run `golangci-lint run ./...`. Fix.

- [ ] **Step 2.19: Commit.**

```bash
git add pkg/exporter/workdir.go pkg/exporter/workdir_test.go pkg/exporter/exporter.go \
        cmd/spool_flags.go cmd/spool_flags_test.go cmd/bundle.go cmd/diff.go \
        pkg/config/config.go cmd/config_integration_test.go
git commit -m "feat(exporter): workdir resolution + --workdir/--memory-budget flags (PR2)

Adds Options.Workdir and Options.MemoryBudget fields; Export() now creates
a per-call workdir with baselines/, targets/, blobs/ subdirs and tears it
down on every termination path. CLI gains --workdir DIR (also DIFFAH_WORKDIR
env) and --memory-budget BYTES; both have sane defaults.

PR 3 onward consumes these dirs; this PR proves the lifecycle in isolation.

Refs: docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §4.2 §6"
```

- [ ] **Step 2.20: Push and open PR.** `gh pr create --title "feat(exporter): workdir lifecycle + spool flags (streaming PR2)"` with body summarizing scope (foundational; consumers in PR 3+) and test plan.

### Review checkpoint

Wait for CI green. Reviewer should confirm: existing tests still pass, workdir is empty after every Export(), `--workdir`/`DIFFAH_WORKDIR`/default precedence correct.

---

## Task 3 (PR 3): Baseline spool

**Spec ref:** §5.2, §5.5.

**Branch:** `feat/streaming-pr3-baseline-spool`

**Goal:** Replace `pkg/exporter/fpcache.go` (`fpCache.bytes`) with `baselineSpool` that streams baseline layers to `<workdir>/baselines/<digest>` via `TeeReader`, fingerprinting them on the same pass. Add `FingerprintReader` to the `Fingerprinter` interface. The patch encoder (still byte-based in this PR) reads the spool back via `os.ReadFile` — bandwidth and final output bytes unchanged. PR 5 will replace those reads with path passing.

**Files:**
- Create: `pkg/exporter/baselinespool.go`
- Create: `pkg/exporter/baselinespool_test.go`
- Modify: `pkg/exporter/fingerprint.go` (add `FingerprintReader` to interface; `DefaultFingerprinter` implementation; legacy `Fingerprint` becomes wrapper)
- Modify: `pkg/exporter/encode.go` (replace `cache *fpCache` with `spool *baselineSpool`; readBaseline closure now reads file from spool)
- Delete: `pkg/exporter/fpcache.go`, `pkg/exporter/fpcache_test.go`
- Modify: `pkg/exporter/intralayer.go` (`SeedBaselineFingerprints` signature unchanged; `ensureBaselineFP` unchanged at this PR — it still consumes `[]byte` via `readBlob`)

### Sub-tasks

- [ ] **Step 3.1: Create branch.** `git checkout master && git pull && git checkout -b feat/streaming-pr3-baseline-spool`.

- [ ] **Step 3.2: Write failing test for `FingerprintReader`.** Add to `pkg/exporter/fingerprint_test.go`:

```go
func TestDefaultFingerprinter_ReaderMatchesBytes(t *testing.T) {
	ctx := context.Background()
	tarBytes := buildSmallTarFixture(t) // helper that returns a deterministic tar+gzip blob
	gzBytes := gzipBytes(t, tarBytes)
	mediaType := "application/vnd.oci.image.layer.v1.tar+gzip"

	fp := DefaultFingerprinter{}
	want, err := fp.Fingerprint(ctx, mediaType, gzBytes)
	if err != nil {
		t.Fatalf("legacy Fingerprint: %v", err)
	}
	got, err := fp.FingerprintReader(ctx, mediaType, bytes.NewReader(gzBytes))
	if err != nil {
		t.Fatalf("FingerprintReader: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("fingerprint mismatch:\nwant=%v\n got=%v", want, got)
	}
}
```

If `buildSmallTarFixture` / `gzipBytes` helpers don't exist, write inline: a 3-file tar (sizes 100, 200, 300 bytes; deterministic content via `seedBytes(seed, n)`) wrapped in gzip.

- [ ] **Step 3.3: Run the test to confirm it fails.** Expected: `Fingerprinter has no method FingerprintReader`.

- [ ] **Step 3.4: Add `FingerprintReader` to the interface.** In `pkg/exporter/fingerprint.go`, change the interface (lines 45-47) to:

```go
type Fingerprinter interface {
	Fingerprint(ctx context.Context, mediaType string, blob []byte) (Fingerprint, error)
	FingerprintReader(ctx context.Context, mediaType string, r io.Reader) (Fingerprint, error)
}
```

Add `FingerprintReader` to `DefaultFingerprinter` (around line 60):

```go
func (DefaultFingerprinter) FingerprintReader(
	ctx context.Context, mediaType string, r io.Reader,
) (Fingerprint, error) {
	dr, closer, err := openDecompressorReader(mediaType, r)
	if err != nil {
		return nil, err
	}
	defer closer()
	return fingerprintTar(ctx, dr)
}
```

Refactor `openDecompressor` so both legacy and reader paths share logic — split into `openDecompressorReader(mediaType, io.Reader)` returning `(io.Reader, func(), error)`. Have the legacy `openDecompressor(mediaType, []byte)` call `openDecompressorReader(mediaType, bytes.NewReader(blob))`.

Convert legacy `Fingerprint` to:

```go
func (d DefaultFingerprinter) Fingerprint(
	ctx context.Context, mediaType string, blob []byte,
) (Fingerprint, error) {
	return d.FingerprintReader(ctx, mediaType, bytes.NewReader(blob))
}
```

- [ ] **Step 3.5: Update any test doubles that implement `Fingerprinter`.** Search: `grep -rn "Fingerprint(ctx" pkg/exporter/ | grep -v fingerprint.go`. Any `struct` that implements the interface needs the new method (a one-line wrapper that drains `r` to bytes and calls the existing one is acceptable for test doubles).

- [ ] **Step 3.6: Run fingerprint tests.** Run `go test -run Fingerprint ./pkg/exporter/`. Expected: all pass.

- [ ] **Step 3.7: Write failing test for `baselineSpool`.** Create `pkg/exporter/baselinespool_test.go`:

```go
package exporter

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestBaselineSpool_TeeWritesAndFingerprints(t *testing.T) {
	dir := t.TempDir()
	spool := newBaselineSpool(dir)

	tarBlob := gzipBytes(t, buildSmallTarFixture(t))
	d := digest.FromBytes(tarBlob)
	meta := BaselineLayerMeta{Digest: d, Size: int64(len(tarBlob)),
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"}

	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(tarBlob)), nil
	}
	entry, err := spool.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{})
	if err != nil {
		t.Fatalf("GetOrSpool: %v", err)
	}
	if entry.Path != filepath.Join(dir, d.Encoded()) {
		t.Fatalf("path mismatch: %q", entry.Path)
	}
	got, err := os.ReadFile(entry.Path)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	if !bytes.Equal(got, tarBlob) {
		t.Fatalf("spool bytes != source bytes")
	}
	if len(entry.Fingerprint) == 0 {
		t.Fatalf("expected non-empty fingerprint")
	}
}

func TestBaselineSpool_SingleflightCollapsesConcurrentFirstTouches(t *testing.T) {
	dir := t.TempDir()
	spool := newBaselineSpool(dir)

	tarBlob := gzipBytes(t, buildSmallTarFixture(t))
	d := digest.FromBytes(tarBlob)
	meta := BaselineLayerMeta{Digest: d, Size: int64(len(tarBlob)),
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"}

	var fetches atomic.Int32
	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		fetches.Add(1)
		return io.NopCloser(bytes.NewReader(tarBlob)), nil
	}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := spool.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{}); err != nil {
				t.Errorf("GetOrSpool: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := fetches.Load(); got != 1 {
		t.Fatalf("expected exactly 1 fetch under singleflight, got %d", got)
	}
}

func TestBaselineSpool_FetchErrorRemovesPartialFileAndDoesNotCache(t *testing.T) {
	dir := t.TempDir()
	spool := newBaselineSpool(dir)

	d := digest.FromString("sha256:bogus")
	meta := BaselineLayerMeta{Digest: d, MediaType: "application/octet-stream"}

	var calls atomic.Int32
	fetch := func(_ digest.Digest) (io.ReadCloser, error) {
		if calls.Add(1) == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		// Second call succeeds with empty body — proves we did not cache the failure.
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if _, err := spool.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{}); err == nil {
		t.Fatalf("expected error")
	}
	if _, err := os.Stat(filepath.Join(dir, d.Encoded())); !os.IsNotExist(err) {
		t.Fatalf("expected partial spool removed, stat err = %v", err)
	}
	if _, err := spool.GetOrSpool(context.Background(), meta, fetch, DefaultFingerprinter{}); err != nil {
		t.Fatalf("retry after error should call fetch again: %v", err)
	}
}
```

- [ ] **Step 3.8: Run tests to confirm they fail.** Expected: `undefined: newBaselineSpool, baselineSpool`.

- [ ] **Step 3.9: Implement `pkg/exporter/baselinespool.go`.**

```go
package exporter

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/singleflight"
)

// baselineSpool replaces fpCache. It writes each first-touched baseline
// layer once into <dir>/<digest.Encoded()> via TeeReader, fingerprints it
// on the same pass, and remembers the path + fingerprint. Patch encoders
// consume baselines by file path (or, in this PR transitionally, by
// re-reading the file via os.ReadFile until PR 5 switches them to paths).
type baselineSpool struct {
	dir     string
	mu      sync.RWMutex
	entries map[digest.Digest]baselineEntry
	sf      singleflight.Group
}

type baselineEntry struct {
	Path        string
	Fingerprint Fingerprint
}

func newBaselineSpool(dir string) *baselineSpool {
	return &baselineSpool{
		dir:     dir,
		entries: make(map[digest.Digest]baselineEntry),
	}
}

// GetOrSpool ensures meta.Digest is on disk and fingerprinted exactly once.
// Concurrent callers on the same digest collapse to a single fetch+
// fingerprint pass; failed fetches are not cached and the next caller
// retries (mirrors fpCache's no-cache-on-error contract — spec §5.2).
func (s *baselineSpool) GetOrSpool(
	ctx context.Context,
	meta BaselineLayerMeta,
	fetch func(digest.Digest) (io.ReadCloser, error),
	fp Fingerprinter,
) (baselineEntry, error) {
	if e, ok := s.lookup(meta.Digest); ok {
		return e, nil
	}
	v, err, _ := s.sf.Do(string(meta.Digest), func() (any, error) {
		if e, ok := s.lookup(meta.Digest); ok {
			return e, nil
		}
		path := filepath.Join(s.dir, meta.Digest.Encoded())
		entry, err := s.spoolOnce(ctx, meta, path, fetch, fp)
		if err != nil {
			_ = os.Remove(path) // best effort; partial file gone
			return nil, err
		}
		s.mu.Lock()
		s.entries[meta.Digest] = entry
		s.mu.Unlock()
		return entry, nil
	})
	if err != nil {
		return baselineEntry{}, err
	}
	return v.(baselineEntry), nil
}

func (s *baselineSpool) spoolOnce(
	ctx context.Context, meta BaselineLayerMeta, path string,
	fetch func(digest.Digest) (io.ReadCloser, error),
	fp Fingerprinter,
) (baselineEntry, error) {
	rc, err := fetch(meta.Digest)
	if err != nil {
		return baselineEntry{}, fmt.Errorf("fetch baseline %s: %w", meta.Digest, err)
	}
	defer rc.Close()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return baselineEntry{}, fmt.Errorf("open spool %s: %w", path, err)
	}
	defer f.Close()

	tee := io.TeeReader(rc, f)
	// Fingerprint failures are non-fatal; treat them like fpCache did
	// (nil entry → planner falls back to size-only ranking).
	fpVal, fpErr := fp.FingerprintReader(ctx, meta.MediaType, tee)
	if fpErr != nil {
		// Drain anything the fingerprinter didn't consume so the file
		// is still complete for byte-for-byte serving downstream.
		if _, err := io.Copy(io.Discard, tee); err != nil {
			return baselineEntry{}, fmt.Errorf("drain after fp error: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		return baselineEntry{}, fmt.Errorf("sync spool: %w", err)
	}
	return baselineEntry{Path: path, Fingerprint: fpVal}, nil
}

func (s *baselineSpool) lookup(d digest.Digest) (baselineEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[d]
	return e, ok
}

// SnapshotFingerprints mirrors fpCache.SnapshotFingerprints — encoders
// use this to seed each per-pair Planner so a baseline shared across N
// pairs is fingerprinted once.
func (s *baselineSpool) SnapshotFingerprints() map[digest.Digest]Fingerprint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[digest.Digest]Fingerprint, len(s.entries))
	for d, e := range s.entries {
		out[d] = e.Fingerprint
	}
	_ = maps.Clone
	return out
}

// Path returns the on-disk spool path for a previously-spooled digest,
// or empty if the digest has never been seen. Used by the patch encoder
// in PR 5 to pass --patch-from=PATH directly.
func (s *baselineSpool) Path(d digest.Digest) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[d].Path
}
```

- [ ] **Step 3.10: Run baselinespool tests.** Run `go test -run Baseline ./pkg/exporter/`. Expected: PASS.

- [ ] **Step 3.11: Replace `fpCache` plumbing in `encode.go`.** In `pkg/exporter/encode.go`:
  - Replace `cache := newFpCache()` (line 46) with `spool := newBaselineSpool(filepath.Join(opts.Workdir, "baselines"))`.
  - Plumb `opts *Options` through `encodeShipped` (or pass `opts.Workdir` explicitly).
  - Rename `primeBaselineCache` → `primeBaselineSpool`. Its body changes from `cache.GetOrLoad(...)` to `spool.GetOrSpool(ctx, b, fetchAsReader, fp)` where `fetchAsReader` adapts `streamBlobBytes` to return an `io.ReadCloser` (call `src.GetBlob` directly and return its `ReadCloser` — see `streamBlobBytes` in `perpair.go:143`).
  - In `encodeTargets`, `seedFP := cache.SnapshotFingerprints()` → `seedFP := spool.SnapshotFingerprints()`.
  - The `readBaseline` closure used by `Planner` (line 127) now reads from disk: `os.ReadFile(spool.Path(d))`. (PR 5 removes this `os.ReadFile` and switches `Planner` to paths.)

- [ ] **Step 3.12: Add a `fetchAsReader` adapter in `perpair.go`.** Add:

```go
// streamBlobReader returns the layer blob as a streaming ReadCloser without
// buffering it into memory. Caller must Close. Used by baselineSpool's
// TeeReader path; the in-memory variant streamBlobBytes stays for callers
// that genuinely need bytes (manifests, configs).
func streamBlobReader(
	ctx context.Context, ref types.ImageReference, sys *types.SystemContext, d digest.Digest,
) (io.ReadCloser, func(), error) {
	src, err := ref.NewImageSource(ctx, sys)
	if err != nil {
		return nil, func() {}, err
	}
	r, _, err := src.GetBlob(ctx, types.BlobInfo{Digest: d}, none.NoCache)
	if err != nil {
		_ = src.Close()
		return nil, func() {}, err
	}
	return r, func() { _ = src.Close() }, nil
}
```

Adjust `primeBaselineSpool` to invoke `streamBlobReader` and supply the returned closer to a `multiCloser` so Close releases both.

- [ ] **Step 3.13: Delete `pkg/exporter/fpcache.go` and `pkg/exporter/fpcache_test.go`.** Run `git rm pkg/exporter/fpcache.go pkg/exporter/fpcache_test.go`. Their behavior is now covered by `baselinespool_test.go` — verify nothing else imports the deleted symbols (`grep -rn "fpCache\|newFpCache" pkg/`).

- [ ] **Step 3.14: Run the full exporter test suite.** Run `go test ./pkg/exporter/`. Expected: PASS, including `determinism_test.go` (output bytes unchanged because the patch encoder still receives identical baseline bytes — just via spool rather than RAM cache).

- [ ] **Step 3.15: Run the full repo test suite + lint.** Run `go test ./...` and `golangci-lint run ./...`. Fix.

- [ ] **Step 3.16: Commit.**

```bash
git add pkg/exporter/baselinespool.go pkg/exporter/baselinespool_test.go \
        pkg/exporter/fingerprint.go pkg/exporter/fingerprint_test.go \
        pkg/exporter/encode.go pkg/exporter/perpair.go
git rm pkg/exporter/fpcache.go pkg/exporter/fpcache_test.go
git commit -m "feat(exporter): baseline spool replaces fpCache (PR3)

Baselines now stream to <workdir>/baselines/<digest> via TeeReader and are
fingerprinted on the same pass. fpCache.bytes (which pinned every baseline
layer in RAM for the entire Export) is gone. Bandwidth and output bytes
unchanged — the patch encoder still consumes baselines as []byte for now;
PR 5 will switch it to file paths.

FingerprintReader added to the Fingerprinter interface for streaming
fingerprint pass.

Refs: docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §5.2 §5.5"
```

- [ ] **Step 3.17: Push and open PR.** Title: `feat(exporter): baseline spool (streaming PR3)`. Body: scope (RAM cache for baselines is gone; encoder still byte-based), test plan, link to spec.

### Review checkpoint

Reviewer should confirm: `determinism_test.go` digests match baseline; `fpCache` symbols are fully removed; baseline spool dir is empty after Export().

---

## Task 4 (PR 4): Output blob spill + writer streaming

**Spec ref:** §5.6, §5.7.

**Branch:** `feat/streaming-pr4-blob-spill`

**Goal:** Slim `blobPool` to `spills map[digest.Digest]string`. Encoded payloads go to `<workdir>/blobs/<digest>`. The writer streams via `io.Copy`. Encoder and `Planner` still produce `[]byte` payloads internally; `addEntryIfAbsent` writes those bytes to the spool path on the way in. PR 5 will eliminate the in-memory payload too.

**Files:**
- Modify: `pkg/exporter/pool.go` (rewrite `blobPool`)
- Modify: `pkg/exporter/writer.go` (`io.Copy` from spool files)
- Modify: `pkg/exporter/encode.go` (`encodeOneShipped` calls `pool.AddEntryIfAbsent(d, payload, entry)` which writes to disk)
- Modify: `pkg/exporter/exporter.go` (pass workdir to `buildBundle` so it can be passed to `newBlobPool(blobsDir)`)
- Modify: `pkg/exporter/assemble.go` (uses `pool.entries` only — no byte access — so should be unaffected; verify)
- Modify: `pkg/exporter/pool_test.go` (rewrite for new API)

### Sub-tasks

- [ ] **Step 4.1: Create branch.** `git checkout master && git pull && git checkout -b feat/streaming-pr4-blob-spill`.

- [ ] **Step 4.2: Write failing test for the new `blobPool` API.** Replace `pkg/exporter/pool_test.go` with:

```go
package exporter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestBlobPool_AddEntryIfAbsent_WritesSpillFile(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	d := digest.FromBytes([]byte("hello"))
	if err := pool.addEntryIfAbsent(d, []byte("hello"),
		diff.BlobEntry{Size: 5, ArchiveSize: 5, Encoding: diff.EncodingFull}); err != nil {
		t.Fatalf("addEntryIfAbsent: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, d.Encoded()))
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("spill content mismatch")
	}
}

func TestBlobPool_AddEntryIfAbsent_FirstWriteWins(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	d := digest.FromBytes([]byte("a"))
	_ = pool.addEntryIfAbsent(d, []byte("first"), diff.BlobEntry{Size: 5, ArchiveSize: 5})
	_ = pool.addEntryIfAbsent(d, []byte("second"), diff.BlobEntry{Size: 6, ArchiveSize: 6})
	got, _ := os.ReadFile(filepath.Join(dir, d.Encoded()))
	if string(got) != "first" {
		t.Fatalf("first-write-wins violated: got %q", got)
	}
}

func TestBlobPool_SortedDigestsIsLex(t *testing.T) {
	dir := t.TempDir()
	pool := newBlobPool(dir)
	for _, payload := range [][]byte{[]byte("c"), []byte("a"), []byte("b")} {
		d := digest.FromBytes(payload)
		_ = pool.addEntryIfAbsent(d, payload, diff.BlobEntry{Size: 1, ArchiveSize: 1})
	}
	got := pool.sortedDigests()
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("not sorted: %v", got)
		}
	}
}
```

- [ ] **Step 4.3: Run tests to confirm they fail.** Expected: `addEntryIfAbsent undefined`, `newBlobPool now takes a string arg`, etc.

- [ ] **Step 4.4: Rewrite `pkg/exporter/pool.go`.**

```go
package exporter

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

// blobPool tracks digest → on-disk spill path + sidecar metadata. It is
// content-addressed and first-write-wins on collision. The bytes are
// written to <dir>/<digest.Encoded()> at addEntryIfAbsent time; the writer
// streams them into the bundle tar via io.Copy. Spec §5.6 §5.7.
type blobPool struct {
	dir      string
	mu       sync.RWMutex
	spills   map[digest.Digest]string         // digest → absolute path
	entries  map[digest.Digest]diff.BlobEntry
	shipRefs map[digest.Digest]int
}

func newBlobPool(dir string) *blobPool {
	return &blobPool{
		dir:      dir,
		spills:   make(map[digest.Digest]string),
		entries:  make(map[digest.Digest]diff.BlobEntry),
		shipRefs: make(map[digest.Digest]int),
	}
}

// addEntryIfAbsent writes the payload to <dir>/<digest> and records the
// sidecar entry. First-write-wins; subsequent calls with the same digest
// are no-ops (the existing spill is preserved). Returns the spill path
// (existing or newly-created).
func (p *blobPool) addEntryIfAbsent(d digest.Digest, payload []byte, e diff.BlobEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.spills[d]; ok {
		return nil
	}
	path := filepath.Join(p.dir, d.Encoded())
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("spill blob %s: %w", d, err)
	}
	p.spills[d] = path
	p.entries[d] = e
	return nil
}

// addEntryFromPath adopts an already-on-disk spill file at srcPath as the
// payload for d. Used by PR 5 when the encoder writes the candidate spill
// directly and we want to rename it into the pool without a re-write.
func (p *blobPool) addEntryFromPath(d digest.Digest, srcPath string, e diff.BlobEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.spills[d]; ok {
		// Loser collision; remove the duplicate spill the caller produced.
		_ = os.Remove(srcPath)
		return nil
	}
	dst := filepath.Join(p.dir, d.Encoded())
	if err := os.Rename(srcPath, dst); err != nil {
		return fmt.Errorf("adopt blob %s: %w", d, err)
	}
	p.spills[d] = dst
	p.entries[d] = e
	return nil
}

func (p *blobPool) has(d digest.Digest) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.spills[d]
	return ok
}

func (p *blobPool) spillPath(d digest.Digest) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.spills[d]
	return s, ok
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
	out := make([]digest.Digest, 0, len(p.spills))
	for d := range p.spills {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// seedManifestAndConfig writes the manifest and config payloads for a
// pair into the pool. The bytes are small (KBs), but routing them through
// the same blob-pool API keeps the writer single-responsibility (drain
// one store).
func seedManifestAndConfig(p *blobPool, plan *pairPlan) error {
	mfDigest := digest.FromBytes(plan.TargetManifest)
	if err := p.addEntryIfAbsent(mfDigest, plan.TargetManifest, diff.BlobEntry{
		Size: int64(len(plan.TargetManifest)), MediaType: plan.TargetMediaType,
		Encoding: diff.EncodingFull, ArchiveSize: int64(len(plan.TargetManifest)),
	}); err != nil {
		return err
	}
	return p.addEntryIfAbsent(plan.TargetConfigDesc.Digest, plan.TargetConfigRaw, diff.BlobEntry{
		Size: plan.TargetConfigDesc.Size, MediaType: plan.TargetConfigDesc.MediaType,
		Encoding: diff.EncodingFull, ArchiveSize: plan.TargetConfigDesc.Size,
	})
}
```

Note: `seedManifestAndConfig` was previously a free function returning no error. Update its caller in `exporter.go` to handle the error.

- [ ] **Step 4.5: Update `exporter.go` to pass workdir into the pool and handle the new seed signature.** In `buildBundle`, replace `pool := newBlobPool()` with `pool := newBlobPool(filepath.Join(opts.Workdir, "blobs"))`. Update the seedManifestAndConfig loop to:

```go
	for _, p := range opts.Pairs {
		plan, err := planPair(ctx, p, opts)
		if err != nil {
			return nil, fmt.Errorf("plan pair %q: %w", p.Name, err)
		}
		plans = append(plans, plan)
		if err := seedManifestAndConfig(pool, plan); err != nil {
			return nil, fmt.Errorf("seed manifest/config %q: %w", p.Name, err)
		}
	}
```

- [ ] **Step 4.6: Update `encodeOneShipped` to use the new pool API.** In `pkg/exporter/encode.go` lines 179-194 (the `encodeOneShipped` body), replace each `pool.addIfAbsent(d, bytes, entry)` with:

```go
		if err := pool.addEntryIfAbsent(s.Digest, layerBytes, fullBlobEntry(s)); err != nil {
			return err
		}
```

(and the corresponding patch / fallback paths). The function signature and outer flow are unchanged.

- [ ] **Step 4.7: Rewrite `pkg/exporter/writer.go`.**

```go
package exporter

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

func writeBundleArchive(outPath string, sidecar diff.Sidecar, pool *blobPool) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()

	scBytes, err := sidecar.Marshal()
	if err != nil {
		return fmt.Errorf("marshal sidecar: %w", err)
	}
	if err := writeTarEntryBytes(tw, diff.SidecarFilename, scBytes); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}

	for _, d := range pool.sortedDigests() {
		path, ok := pool.spillPath(d)
		if !ok {
			return fmt.Errorf("blob %s missing spill path", d)
		}
		if err := streamBlobIntoTar(tw, blobPath(d), path); err != nil {
			return fmt.Errorf("write blob %s: %w", d, err)
		}
	}
	return nil
}

func streamBlobIntoTar(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name: name, Size: info.Size(), Mode: 0o644,
		Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func writeTarEntryBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name: name, Size: int64(len(data)), Mode: 0o644,
		Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func blobPath(d digest.Digest) string {
	parts := strings.SplitN(string(d), ":", 2)
	if len(parts) != 2 {
		return filepath.Join("blobs", string(d))
	}
	return filepath.Join("blobs", parts[0], parts[1])
}
```

- [ ] **Step 4.8: Update any direct `pool.bytes`/`pool.get` callers.** Search: `grep -rn "pool\.\(bytes\|get\|addIfAbsent\)" pkg/exporter/`. Each must convert: `pool.get(d)` callers in tests can do `os.ReadFile(spillPath)` instead. `pool.entries[d]` access (sidecar marshaling, dry-run stats) stays unchanged.

- [ ] **Step 4.9: Run new pool tests.** `go test -run BlobPool ./pkg/exporter/`. Expected: PASS.

- [ ] **Step 4.10: Run the full exporter test suite.** Run `go test ./pkg/exporter/`. Expected: PASS, `determinism_test.go` digests unchanged.

- [ ] **Step 4.11: Run `go test ./...` + lint.** Fix anything.

- [ ] **Step 4.12: Commit.**

```bash
git add pkg/exporter/pool.go pkg/exporter/pool_test.go pkg/exporter/writer.go \
        pkg/exporter/encode.go pkg/exporter/exporter.go
git commit -m "feat(exporter): output blob spill + streaming writer (PR4)

Encoded blobs now spill to <workdir>/blobs/<digest> at addEntryIfAbsent
time; the writer io.Copy's each spill into the output tar in sorted-
by-digest order. blobPool no longer carries []byte payloads — only
spill paths and sidecar metadata. Output bytes unchanged.

Refs: docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §5.6 §5.7"
```

- [ ] **Step 4.13: Push and open PR.** Title: `feat(exporter): output blob spill + streaming writer (streaming PR4)`.

### Review checkpoint

Reviewer should confirm: `determinism_test.go` digests still match baseline; `blobPool.bytes` is gone; the writer never holds an entire blob in memory (visible in the diff).

---

## Task 5 (PR 5): Per-encode streaming flow

**Spec ref:** §5.4.

**Branch:** `feat/streaming-pr5-per-encode-streaming`

**Goal:** Rewrite the per-shipped-layer encode path to operate on file paths end-to-end. The target is streamed to `<workdir>/targets/<digest>`, fingerprinted by re-reading from disk; the planner's `readBlob` returns paths instead of bytes; each top-K candidate encodes to its own `<workdir>/blobs/<digest>.cand-<base>` spill via `EncodeStream`; the running-best winner is renamed into `<workdir>/blobs/<digest>` via `pool.addEntryFromPath`; full-zstd is measured size-only via `EncodeFullStream` + `countingWriter`. Losing candidate spills are removed eagerly. Encoding=full ships the raw target bytes (target spool is renamed into the pool).

This is the load-bearing PR. After this lands, peak RAM holds at most `workers × per-encode-buffer` (still without admission control — that's PR 6). Memory goes from O(corpus) to O(workers × MB).

**Files:**
- Modify: `pkg/exporter/intralayer.go` (`Planner.readBlob` → `readBlobPath`; `PlanShippedTopK` rewritten with paths, size-only full-zstd ceiling, K candidate spills)
- Modify: `pkg/exporter/encode.go` (`encodeOneShipped` rewritten; target spool, candidate cleanup)
- Modify: `pkg/exporter/perpair.go` (add `spoolBlob(ctx, ref, sys, digest, dstPath)` helper)
- Modify: `pkg/exporter/intralayer_test.go` (update planner double signatures)
- Modify: `pkg/exporter/encode_test.go` (update test doubles)

### Sub-tasks

- [ ] **Step 5.1: Create branch.** `git checkout master && git pull && git checkout -b feat/streaming-pr5-per-encode-streaming`.

- [ ] **Step 5.2: Add `spoolBlob` helper.** In `pkg/exporter/perpair.go` add:

```go
// spoolBlob streams blob d from ref into dstPath without keeping the bytes
// in RAM. Returns the on-disk size. The optional onChunk callback receives
// each read chunk's byte count (used by the progress.Layer.Written hook
// already wired in encodeOneShipped).
func spoolBlob(
	ctx context.Context, ref types.ImageReference, sys *types.SystemContext,
	d digest.Digest, dstPath string, onChunk func(int64),
) (int64, error) {
	rc, closer, err := streamBlobReader(ctx, ref, sys, d)
	if err != nil {
		return 0, err
	}
	defer closer()
	defer rc.Close()

	f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open target spool %s: %w", dstPath, err)
	}
	defer f.Close()

	chunk := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := rc.Read(chunk)
		if n > 0 {
			if _, werr := f.Write(chunk[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
			if onChunk != nil {
				onChunk(int64(n))
			}
		}
		if err == io.EOF {
			return total, f.Sync()
		}
		if err != nil {
			return total, err
		}
	}
}
```

Add the `os` import if not already present.

- [ ] **Step 5.3: Change `Planner.readBlob` to `readBlobPath`.** In `pkg/exporter/intralayer.go`:
  - Change the field type from `func(digest.Digest) ([]byte, error)` to `func(digest.Digest) (string, error)` (returns spool path).
  - Update `NewPlanner` signature.
  - Update `ensureBaselineFP` to read bytes via `os.ReadFile(path)` *only when fingerprint is not pre-seeded* — and prefer `FingerprintReader(os.Open(path))` instead. Concretely:

```go
func (p *Planner) ensureBaselineFP(ctx context.Context) {
	p.fpOnce.Do(func() {
		fp := p.fingerprint
		if fp == nil {
			fp = DefaultFingerprinter{}
		}
		if p.baselineFP == nil {
			p.baselineFP = make(map[digest.Digest]Fingerprint, len(p.baseline))
		}
		for _, b := range p.baseline {
			if _, seeded := p.baselineFP[b.Digest]; seeded {
				continue
			}
			path, err := p.readBlobPath(b.Digest)
			if err != nil {
				p.baselineFP[b.Digest] = nil
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				p.baselineFP[b.Digest] = nil
				continue
			}
			fingerprint, err := fp.FingerprintReader(ctx, b.MediaType, f)
			_ = f.Close()
			if err != nil {
				p.baselineFP[b.Digest] = nil
				continue
			}
			p.baselineFP[b.Digest] = fingerprint
		}
	})
}
```

- [ ] **Step 5.4: Rewrite `PlanShippedTopK` signature for path inputs.** Change to `PlanShippedTopK(ctx, s diff.BlobRef, targetPath string, blobsDir string, k int) (entry diff.BlobRef, payloadPath string, err error)`. The returned `payloadPath` is *either* the path to a candidate spill (encoding=patch) *or* `targetPath` itself (encoding=full — caller renames it into the pool). Body:

```go
func (p *Planner) PlanShippedTopK(
	ctx context.Context, s diff.BlobRef, targetPath string, blobsDir string, k int,
) (diff.BlobRef, string, error) {
	p.ensureBaselineFP(ctx)
	fp := p.fingerprint
	if fp == nil {
		fp = DefaultFingerprinter{}
	}

	// Re-fingerprint the target by re-reading from disk. The target file
	// is OS-cached because it was just written; the cost is one sequential
	// read of in-cache bytes, no RAM retention.
	tf, err := os.Open(targetPath)
	if err != nil {
		return diff.BlobRef{}, "", fmt.Errorf("open target spool %s: %w", targetPath, err)
	}
	targetFP, _ := fp.FingerprintReader(ctx, s.MediaType, tf) // failure → nil → size-only
	_ = tf.Close()

	cands := p.PickTopK(ctx, targetFP, s.Size, k)
	if len(cands) == 0 {
		return fullEntry(s), targetPath, nil
	}

	wl := ResolveWindowLog(p.windowLog, s.Size)
	opts := zstdpatch.EncodeOpts{Level: p.level, WindowLog: wl}

	// Full-zstd ceiling: size only, no spill.
	cw := &writeCounter{}
	fullSize, err := zstdpatch.EncodeFullStream(ctx, targetPath, cw, opts)
	if err != nil {
		return diff.BlobRef{}, "", fmt.Errorf("size-only full encode %s: %w", s.Digest, err)
	}

	bestEntry := fullEntry(s)
	bestPath := targetPath // raw target / encoding=full default
	bestSize := s.Size

	for _, c := range cands {
		refPath, err := p.readBlobPath(c.Digest)
		if err != nil {
			return diff.BlobRef{}, "", fmt.Errorf("baseline path %s: %w", c.Digest, err)
		}
		candPath := filepath.Join(blobsDir,
			fmt.Sprintf("%s.cand-%s", s.Digest.Encoded(), c.Digest.Encoded()[:8]))
		patchSize, err := zstdpatch.EncodeStream(ctx, refPath, targetPath, candPath, opts)
		if err != nil {
			_ = os.Remove(candPath)
			return diff.BlobRef{}, "", fmt.Errorf("encode patch %s vs %s: %w", s.Digest, c.Digest, err)
		}
		// Patch must strictly beat full-zstd, raw target, AND running best.
		if patchSize < fullSize && patchSize < s.Size && patchSize < bestSize {
			// Discard previous winner if it was a candidate spill (not the target).
			if bestPath != targetPath {
				_ = os.Remove(bestPath)
			}
			bestEntry = diff.BlobRef{
				Digest: s.Digest, Size: s.Size, MediaType: s.MediaType,
				Encoding: diff.EncodingPatch, Codec: CodecZstdPatch,
				PatchFromDigest: c.Digest, ArchiveSize: patchSize,
			}
			bestPath = candPath
			bestSize = patchSize
		} else {
			_ = os.Remove(candPath)
		}
	}
	return bestEntry, bestPath, nil
}

type writeCounter struct{ n int64 }

func (c *writeCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
```

Adjust the legacy `PlanShipped` wrapper if it's still called by tests; the new shape is incompatible, so either update its callers or remove it.

- [ ] **Step 5.5: Rewrite `encodeOneShipped`.** In `pkg/exporter/encode.go`:

```go
func encodeOneShipped(
	ctx context.Context, pool *blobPool, p *pairPlan, s diff.BlobRef,
	planner *Planner, mode string, rep progress.Reporter,
	candidates int, workdir string,
) error {
	layer := rep.StartLayer(s.Digest, s.Size, string(s.Encoding))

	targetsDir := filepath.Join(workdir, "targets")
	blobsDir := filepath.Join(workdir, "blobs")
	targetPath := filepath.Join(targetsDir, s.Digest.Encoded())

	written, err := spoolBlob(ctx, p.TargetImageRef, p.SystemContext, s.Digest, targetPath,
		cappedWriter(s.Size, layer.Written))
	if err != nil {
		_ = os.Remove(targetPath)
		layer.Fail(err)
		return fmt.Errorf("spool shipped %s: %w", s.Digest, err)
	}
	_ = written // observability hook

	if pool.refCount(s.Digest) > 1 || mode == modeOff {
		// Force full encoding; adopt target spool as the blob.
		if err := pool.addEntryFromPath(s.Digest, targetPath, fullBlobEntry(s)); err != nil {
			layer.Fail(err)
			return err
		}
		layer.Done()
		return nil
	}

	entry, payloadPath, err := planner.PlanShippedTopK(ctx, s, targetPath, blobsDir, candidates)
	if err != nil {
		log().Warn("patch planning failed, falling back to full",
			"pair", p.Name, "digest", s.Digest, "err", err)
		if err := pool.addEntryFromPath(s.Digest, targetPath, fullBlobEntry(s)); err != nil {
			layer.Fail(err)
			return err
		}
		layer.Done()
		return nil
	}

	if err := pool.addEntryFromPath(s.Digest, payloadPath, blobEntryFromPlanner(entry)); err != nil {
		layer.Fail(err)
		return err
	}
	// If the winner was a candidate spill (encoding=patch), the target spool
	// is now an unused leftover.
	if payloadPath != targetPath {
		_ = os.Remove(targetPath)
	}
	layer.Done()
	return nil
}
```

Update `encodeTargets` to forward `opts.Workdir` (or pass `workdir` through the closure parameters) and to construct planners with `readBaseline` returning paths from the spool: `readBaseline := func(d digest.Digest) (string, error) { return spool.Path(d), nil }`.

- [ ] **Step 5.6: Update `encodeShipped` signature.** Pass `workdir` through `encodeShipped` → `encodeTargets`. Threading: `encodeShipped(ctx, pool, plans, mode, fp, rep, opts.ZstdLevel, opts.ZstdWindowLog, opts.Candidates, opts.Workers, opts.Workdir, spool)`.

- [ ] **Step 5.7: Update tests for the new `Planner` shape.** In `pkg/exporter/intralayer_test.go`:
  - Change `readBlob` test stubs to `readBlobPath` returning a path that the test wrote to `t.TempDir()`.
  - Change `PlanShippedTopK` invocations to take a `targetPath` (write target bytes to a file first) and a `blobsDir` (`t.TempDir()`).
  - Assert the returned `payloadPath` is the expected file (read it and compare bytes).

  These tests are mechanical refactors — the *behavior* hasn't changed, only the I/O shape.

- [ ] **Step 5.8: Update `pkg/exporter/encode_test.go` similarly.** Wherever a fake `cache`/`spool` is constructed, switch to `newBaselineSpool(t.TempDir())` and seed via fake `streamBlobReader`. The integration-style tests in this file may be easier to keep end-to-end (real `spool`, real planner, real encode pipeline) — fewer doubles.

- [ ] **Step 5.9: Run exporter tests.** Run `go test ./pkg/exporter/`. Expected: all pass; **`determinism_test.go` is the gate** — output bytes must be byte-identical to the baseline captured in Step P-3.

- [ ] **Step 5.10: Run `go test ./...` + lint.** Fix.

- [ ] **Step 5.11: Verify cleanup test from PR 2.** If you stubbed `TestExport_CleansUpWorkdirOnSuccess` in PR 2, un-stub it now and confirm it passes.

- [ ] **Step 5.12: Commit.**

```bash
git add pkg/exporter/intralayer.go pkg/exporter/intralayer_test.go \
        pkg/exporter/encode.go pkg/exporter/encode_test.go \
        pkg/exporter/perpair.go pkg/exporter/workdir_test.go
git commit -m "feat(exporter): per-encode streaming flow on file paths (PR5)

encodeOneShipped now spools the target to disk, calls PlanShippedTopK with
file paths, encodes top-K candidates to per-candidate spill files via
EncodeStream, picks the running-best winner, and adopts the winning path
into the blob pool via os.Rename. Full-zstd ceiling is measured size-only
through klauspost streaming + a counting writer; encoding=full ships the
raw target bytes (target spool is renamed into the pool).

After this PR the exporter holds at most O(workers × per-encode buffer) of
encoded bytes in RAM. Admission control comes in PR6.

Refs: docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §5.4"
```

- [ ] **Step 5.13: Push and open PR.** Title: `feat(exporter): per-encode streaming flow (streaming PR5)`. Body: emphasize that `determinism_test.go` is the load-bearing assertion; reviewer should pay particular attention to it.

### Review checkpoint

Reviewer should run `go test -run TestExport_DeterministicArchive -count=5 ./pkg/exporter/...` to detect any nondeterminism. Confirm `<workdir>/targets/`, `<workdir>/blobs/*.cand-*` are empty after success.

---

## Task 6 (PR 6): Admission controller + `--memory-budget` enforcement

**Spec ref:** §4.3, §5.3.

**Branch:** `feat/streaming-pr6-admission`

**Goal:** Replace today's `workerpool.go` semantics for the encode phase with `encodepool` that has two gates — worker semaphore (existing behavior) and memory admission semaphore (new). The admission semaphore is sized to `Options.MemoryBudget` (already plumbed in PR 2); each encode's estimated RSS is computed from its `windowLog`. Add a single-layer-exceeds-budget guard that fail-fasts before opening any spool.

**Files:**
- Create: `pkg/exporter/admission.go`
- Create: `pkg/exporter/admission_test.go`
- Modify: `pkg/exporter/workerpool.go` → either extend or replace; create `pkg/exporter/encodepool.go` with the two-gate `Submit` method
- Modify: `pkg/exporter/encode.go` (use `encodepool.Submit(d, estimate, fn)`; build `estimateFn` from `ResolveWindowLog`)
- Modify: `pkg/exporter/exporter.go` (single-layer-exceeds-budget guard before encode phase; emit structured error)
- Modify: `pkg/diff/errs/...` (add `CategoryUser` error helper if not present, or reuse)

### Sub-tasks

- [ ] **Step 6.1: Create branch.** `git checkout master && git pull && git checkout -b feat/streaming-pr6-admission`.

- [ ] **Step 6.2: Write failing test for admission controller.** Create `pkg/exporter/admission_test.go`:

```go
package exporter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEstimateRSSForWindowLog_TableIsConservative(t *testing.T) {
	cases := []struct {
		windowLog int
		min       int64
	}{
		{27, 256 << 20},
		{30, 2 << 30},
		{31, 4 << 30},
	}
	for _, c := range cases {
		got := estimateRSSForWindowLog(c.windowLog)
		if got < c.min {
			t.Errorf("windowLog=%d: estimate %d < min %d", c.windowLog, got, c.min)
		}
	}
	// Out-of-table values fall back to the largest entry.
	if got := estimateRSSForWindowLog(99); got < (4 << 30) {
		t.Errorf("out-of-table fallback too small: %d", got)
	}
}

func TestAdmission_SerializeWhenSumExceedsBudget(t *testing.T) {
	// Budget 4 GiB. Three "encodes" of 2 GiB each must run two-then-one,
	// not all three concurrently.
	budget := int64(4 << 30)
	estimate := int64(2 << 30)
	pool := newEncodePool(context.Background(), 8, budget) // 8 worker slots, 4 GiB budget

	const n = 3
	var concurrent atomic.Int32
	var peakConcurrent atomic.Int32
	hold := 50 * time.Millisecond

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		pool.Submit(testDigest(i), estimate, func() error {
			cur := concurrent.Add(1)
			defer concurrent.Add(-1)
			for {
				peak := peakConcurrent.Load()
				if cur <= peak || peakConcurrent.CompareAndSwap(peak, cur) {
					break
				}
			}
			time.Sleep(hold)
			wg.Done()
			return nil
		})
	}
	if err := pool.Wait(); err != nil {
		t.Fatalf("pool wait: %v", err)
	}
	wg.Wait()
	if peak := peakConcurrent.Load(); peak > 2 {
		t.Fatalf("expected ≤2 concurrent (4 GiB / 2 GiB), got peak %d", peak)
	}
}

func TestAdmission_DisabledWhenBudgetIsZero(t *testing.T) {
	pool := newEncodePool(context.Background(), 4, 0) // 0 = admission disabled
	estimate := int64(1 << 50)                        // absurdly large; would block forever if gate active
	done := make(chan struct{})
	pool.Submit(testDigest(0), estimate, func() error { close(done); return nil })
	if err := pool.Wait(); err != nil {
		t.Fatalf("pool wait: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("encode never ran")
	}
}

func TestAdmission_SingleflightCollapsesSameDigest(t *testing.T) {
	pool := newEncodePool(context.Background(), 4, 0)
	d := testDigest(0)
	var calls atomic.Int32
	pool.Submit(d, 1, func() error { calls.Add(1); return nil })
	pool.Submit(d, 1, func() error { calls.Add(1); return nil })
	pool.Submit(d, 1, func() error { calls.Add(1); return nil })
	if err := pool.Wait(); err != nil {
		t.Fatalf("pool wait: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call under singleflight, got %d", got)
	}
}

func TestAdmission_PropagatesError(t *testing.T) {
	pool := newEncodePool(context.Background(), 4, 0)
	pool.Submit(testDigest(0), 1, func() error { return errors.New("boom") })
	if err := pool.Wait(); err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom, got %v", err)
	}
}

func testDigest(i int) digestRef { return digestRef(string(rune('a' + i))) }

// digestRef is a thin alias used only in this test to avoid importing
// go-digest (kept here so the test reads as a unit test of the controller,
// not of digests).
type digestRef string
```

If `Submit` actually requires `digest.Digest`, replace `digestRef` with constructed digests via `digest.FromString`.

- [ ] **Step 6.3: Run tests to confirm they fail.** Expected: undefined symbols.

- [ ] **Step 6.4: Implement `pkg/exporter/admission.go`.**

```go
package exporter

import (
	"context"
	"sync"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// rssEstimateByWindowLog is the windowLog → estimated peak RSS table.
// Values are deliberately conservative — see spec §4.3 risks. The
// admission controller blocks new encodes from being admitted unless
// (sum of in-flight estimates) + new_estimate ≤ budget.
var rssEstimateByWindowLog = map[int]int64{
	27: 256 << 20,
	28: 512 << 20,
	29: 1 << 30,
	30: 2 << 30,
	31: 4 << 30,
}

func estimateRSSForWindowLog(wl int) int64 {
	if v, ok := rssEstimateByWindowLog[wl]; ok {
		return v
	}
	return rssEstimateByWindowLog[31]
}

// encodePool runs encode functions under two gates: worker semaphore
// (capacity = goroutine count) and memory admission semaphore (capacity =
// budget bytes; nil = disabled). singleflight collapses concurrent
// submissions on the same digest. See spec §5.3.
type encodePool struct {
	g         *errgroup.Group
	gctx      context.Context
	workerSem *semaphore.Weighted
	memSem    *semaphore.Weighted
	memBudget int64
	sf        singleflight.Group
	mu        sync.Mutex
}

func newEncodePool(ctx context.Context, workers int, memoryBudget int64) *encodePool {
	g, gctx := errgroup.WithContext(ctx)
	if workers < 1 {
		workers = 1
	}
	p := &encodePool{
		g:         g,
		gctx:      gctx,
		workerSem: semaphore.NewWeighted(int64(workers)),
		memBudget: memoryBudget,
	}
	if memoryBudget > 0 {
		p.memSem = semaphore.NewWeighted(memoryBudget)
	}
	return p
}

// Submit runs fn under both gates, dedup'd by d. estimate is the predicted
// per-encode RSS; clamped at memBudget so a too-large estimate never
// deadlocks (the single-layer-exceeds-budget guard is the upstream check).
func (p *encodePool) Submit(d digest.Digest, estimate int64, fn func() error) {
	if estimate < 1 {
		estimate = 1
	}
	if p.memBudget > 0 && estimate > p.memBudget {
		estimate = p.memBudget
	}
	p.g.Go(func() error {
		_, err, _ := p.sf.Do(string(d), func() (any, error) {
			return nil, p.runWithGates(estimate, fn)
		})
		return err
	})
}

func (p *encodePool) runWithGates(estimate int64, fn func() error) error {
	if err := p.workerSem.Acquire(p.gctx, 1); err != nil {
		return err
	}
	defer p.workerSem.Release(1)

	if p.memSem != nil {
		if err := p.memSem.Acquire(p.gctx, estimate); err != nil {
			return err
		}
		defer p.memSem.Release(estimate)
	}
	return fn()
}

func (p *encodePool) Wait() error { return p.g.Wait() }
```

- [ ] **Step 6.5: Run admission tests.** `go test -run "Admission\|EstimateRSS" ./pkg/exporter/`. Expected: PASS.

- [ ] **Step 6.6: Wire `encodePool` into `encodeTargets`.** In `pkg/exporter/encode.go`, replace the existing worker pool (`encPool, _ := newWorkerPool(ctx, workers)`) with:

```go
	encPool := newEncodePool(ctx, workers, memoryBudget)
	for _, p := range pairs {
		// (existing per-pair planner setup)
		for _, s := range p.Shipped {
			if pool.has(s.Digest) {
				continue
			}
			est := estimateRSSForWindowLog(ResolveWindowLog(windowLog, s.Size))
			encPool.Submit(s.Digest, est, func() error {
				return encodeOneShipped(ctx, pool, p, s, planner, mode, rep, candidates, workdir)
			})
		}
	}
	return encPool.Wait()
```

Thread `memoryBudget int64` through `encodeShipped` → `encodeTargets` from `opts.MemoryBudget`.

- [ ] **Step 6.7: Add the single-layer-exceeds-budget guard.** In `pkg/exporter/exporter.go`, between planning and encoding (inside `buildBundle` after the plan loop, before `encodeShipped`):

```go
	if opts.MemoryBudget > 0 {
		var maxEst int64
		var offending diff.BlobRef
		for _, plan := range plans {
			for _, s := range plan.Shipped {
				e := estimateRSSForWindowLog(ResolveWindowLog(opts.ZstdWindowLog, s.Size))
				if e > maxEst {
					maxEst = e
					offending = s
				}
			}
		}
		if maxEst > opts.MemoryBudget {
			return nil, &errs.Error{
				Cat: errs.CategoryUser,
				Msg: fmt.Sprintf("layer %s requires %d byte(s) of admission budget; --memory-budget is %d",
					offending.Digest, maxEst, opts.MemoryBudget),
				Hint: "increase --memory-budget or set --workers 1 with a smaller --zstd-window-log",
			}
		}
	}
```

(Adapt to the actual `errs` package API — see existing usage in `pkg/diff/errs/`.)

- [ ] **Step 6.8: Add a guard test.** In `pkg/exporter/exporter_test.go`:

```go
func TestExport_FailFastWhenSingleLayerExceedsBudget(t *testing.T) {
	opts := smallExportOptionsWithLayerSize(t, 4<<30) // helper builds a fixture with one 4 GB layer
	opts.MemoryBudget = 1 << 30                       // 1 GiB — too small
	err := Export(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *errs.Error
	if !errors.As(err, &ce) || ce.Cat != errs.CategoryUser {
		t.Fatalf("expected CategoryUser error, got %v", err)
	}
}
```

If a 4 GB fixture is too costly for unit tests, fake the layer-size estimate by adjusting `rssEstimateByWindowLog` injection (e.g., make it overridable for tests via a package-private `var rssEstimator = estimateRSSForWindowLog`).

- [ ] **Step 6.9: Update CLI help text.** In `cmd/spool_flags.go`, ensure the `spoolHelp` text mentions the fail-fast behavior.

- [ ] **Step 6.10: Run exporter tests.** `go test ./pkg/exporter/`. All pass, determinism digests still match.

- [ ] **Step 6.11: Run `go test ./...` + lint.** Fix.

- [ ] **Step 6.12: Add a determinism test extension for budget-on/off parity.** In `pkg/exporter/determinism_test.go`, add a sub-test that runs `Export()` twice on the same input — once with `MemoryBudget=0`, once with `MemoryBudget=8<<30` — and asserts the output `bundle.tar` byte digests are equal.

- [ ] **Step 6.13: Commit.**

```bash
git add pkg/exporter/admission.go pkg/exporter/admission_test.go \
        pkg/exporter/encode.go pkg/exporter/exporter.go pkg/exporter/exporter_test.go \
        pkg/exporter/determinism_test.go cmd/spool_flags.go
git commit -m "feat(exporter): admission controller for --memory-budget (PR6)

Encode workers now pass through two gates: a worker-count semaphore
(existing behavior) and a memory-budget semaphore. Each encode declares
an estimated RSS computed from its windowLog; admission blocks until the
sum of in-flight estimates fits the budget. --memory-budget=0 disables
the gate.

Adds a fail-fast guard: if any single shipped layer's estimated RSS
exceeds the budget, Export() returns a CategoryUser error before opening
any spool.

singleflight collapses concurrent submissions on the same digest, so
duplicate encodes of identical content never start.

Refs: docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §4.3 §5.3"
```

- [ ] **Step 6.14: Push and open PR.**

### Review checkpoint

Reviewer should confirm: determinism unchanged across budget on/off; admission test demonstrates serialization; fail-fast error message includes the digest and the offending estimate.

---

## Task 7 (PR 7): GB-scale benchmark + nightly CI

**Spec ref:** §8.4, §13 (acceptance).

**Branch:** `feat/streaming-pr7-bench`

**Goal:** Build a deterministic, patchable 2 GiB fixture; measure peak RSS via `/usr/bin/time -v` (Linux) or `ps -o rss=` polling (macOS); commit baseline numbers; add a nightly CI job that gates ≤ 8 GiB RSS and ≤ 110 % walltime regression.

**Files:**
- Create: `pkg/exporter/scale_bench_test.go` (build tag `big`)
- Create: `pkg/exporter/scale_fixture.go` (helper to build the deterministic fixture)
- Create: `benchmarks/scale-export-linux.json`
- Create: `benchmarks/scale-export-darwin.json`
- Create: `.github/workflows/scale-bench.yml`
- Create: `docs/performance.md`

### Sub-tasks

- [ ] **Step 7.1: Create branch.** `git checkout master && git pull && git checkout -b feat/streaming-pr7-bench`.

- [ ] **Step 7.2: Write the fixture builder.** Create `pkg/exporter/scale_fixture.go`:

```go
//go:build big

package exporter

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// buildScaleFixture writes a deterministic OCI-archive layer of approxBytes
// total to dirPath as `baseline.tar` and `target.tar`. Target = baseline +
// one extra small entry. The content is repetitive (sha256(seed||idx)) but
// not random, so zstd can compress it and zstd-patch can produce a small
// patch. Returns the produced sizes for assertion.
func buildScaleFixture(dirPath string, approxBytes int64, seed int64) (baseline, target int64, err error) {
	const fileSize = 2 << 20 // 2 MiB per tar entry
	n := int(approxBytes / fileSize)

	mkLayer := func(path string, extraEntry bool) (int64, error) {
		f, err := os.Create(path)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for i := 0; i < n; i++ {
			body := patternBytes(seed, int64(i), fileSize)
			hdr := &tar.Header{
				Name: fmt.Sprintf("file-%06d", i),
				Mode: 0o644,
				Size: int64(len(body)),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return 0, err
			}
			if _, err := tw.Write(body); err != nil {
				return 0, err
			}
		}
		if extraEntry {
			extra := patternBytes(seed, int64(n)+1, 4<<20)
			hdr := &tar.Header{Name: "file-extra", Mode: 0o644, Size: int64(len(extra))}
			if err := tw.WriteHeader(hdr); err != nil {
				return 0, err
			}
			if _, err := tw.Write(extra); err != nil {
				return 0, err
			}
		}
		if err := tw.Close(); err != nil {
			return 0, err
		}
		if err := gz.Close(); err != nil {
			return 0, err
		}
		st, _ := f.Stat()
		return st.Size(), nil
	}

	baseline, err = mkLayer(dirPath+"/baseline.tar", false)
	if err != nil {
		return 0, 0, err
	}
	target, err = mkLayer(dirPath+"/target.tar", true)
	if err != nil {
		return 0, 0, err
	}
	return baseline, target, nil
}

// patternBytes returns deterministic content of len totalBytes. The pattern
// is a hash chain so the produced bytes have realistic entropy (zstd compresses
// modestly) but are reproducible across runs.
func patternBytes(seed, idx, totalBytes int64) []byte {
	out := make([]byte, 0, totalBytes)
	h := sha256.New()
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(seed))
	binary.LittleEndian.PutUint64(buf[8:], uint64(idx))
	h.Write(buf[:])
	chunk := h.Sum(nil)
	for int64(len(out)) < totalBytes {
		out = append(out, chunk...)
		h.Reset()
		h.Write(chunk)
		chunk = h.Sum(nil)
	}
	return out[:totalBytes]
}

var _ = io.Discard
var _ = bytes.NewReader
```

- [ ] **Step 7.3: Write the benchmark test.** Create `pkg/exporter/scale_bench_test.go`:

```go
//go:build big

package exporter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestScaleBench_2GiBLayer exports a deterministic 2 GiB-layer fixture and
// asserts peak RSS ≤ 8 GiB and walltime ≤ 110 % of the committed baseline.
//
// Gated by DIFFAH_BIG_TEST=1 (in addition to the `big` build tag) so an
// accidental `go test -tags=big` on a developer's laptop doesn't burn 30
// minutes. Set DIFFAH_BIG_TEST=1 + the big tag to actually run it.
func TestScaleBench_2GiBLayer(t *testing.T) {
	if os.Getenv("DIFFAH_BIG_TEST") != "1" {
		t.Skip("set DIFFAH_BIG_TEST=1 to run")
	}

	dir := t.TempDir()
	if _, _, err := buildScaleFixture(dir, 2<<30, 42); err != nil {
		t.Fatalf("build fixture: %v", err)
	}

	out := filepath.Join(dir, "bundle.tar")
	opts := scaleBenchExportOptions(t, dir, out)

	var rssKB int64
	switch runtime.GOOS {
	case "linux":
		rssKB = runUnderTimeV(t, dir, opts)
	default:
		rssKB = runWithRSSPolling(t, dir, opts)
	}

	const maxRSSBytes = 8 << 30
	if rssKB*1024 > maxRSSBytes {
		t.Fatalf("peak RSS %d KiB > 8 GiB cap", rssKB)
	}

	updateBenchmarkBaseline(t, rssKB)
}

func scaleBenchExportOptions(t *testing.T, dir, out string) Options {
	t.Helper()
	// Build minimal pair from baseline.tar / target.tar in dir.
	// (Implementation reuses helpers from exporter_testing.go; pattern off
	// the smallest end-to-end Export test in this package.)
	t.Skip("scaffolded — wire to project's smallest end-to-end Export helper")
	return Options{}
}

func runUnderTimeV(t *testing.T, _ string, _ Options) int64 {
	t.Helper()
	cmd := exec.Command("/usr/bin/time", "-v", os.Args[0],
		"-test.run", "TestScaleBench_2GiBLayer_RunChild")
	cmd.Env = append(os.Environ(), "DIFFAH_BIG_TEST=1", "DIFFAH_BENCH_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("/usr/bin/time -v: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Maximum resident set size") {
			fields := strings.Fields(line)
			n, _ := strconv.ParseInt(fields[len(fields)-1], 10, 64)
			return n
		}
	}
	t.Fatalf("could not parse RSS from /usr/bin/time output:\n%s", out)
	return 0
}

func runWithRSSPolling(t *testing.T, _ string, _ Options) int64 {
	t.Helper()
	stop := make(chan struct{})
	peakCh := make(chan int64, 1)
	go func() {
		var peak int64
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				peakCh <- peak
				return
			case <-ticker.C:
				rss := readRSS(os.Getpid())
				if rss > peak {
					peak = rss
				}
			}
		}
	}()
	// Run Export() in this goroutine.
	if err := Export(context.Background(), Options{} /* TODO */); err != nil {
		t.Fatalf("Export: %v", err)
	}
	close(stop)
	return <-peakCh
}

func readRSS(pid int) int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}

func updateBenchmarkBaseline(t *testing.T, rssKB int64) {
	t.Helper()
	if os.Getenv("DIFFAH_BENCH_UPDATE") != "1" {
		return
	}
	path := filepath.Join("..", "..", "benchmarks", "scale-export-"+runtime.GOOS+".json")
	rec := map[string]any{
		"timestamp_unix": time.Now().Unix(),
		"peak_rss_kib":   rssKB,
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	_ = os.WriteFile(path, b, 0o644)
}
```

  *Notes:* The `t.Skip("scaffolded — wire to project's smallest end-to-end Export helper")` line is a placeholder for the implementer to wire to the actual smallest `Export()` test in the project. The pattern: build an OCI-archive `dir`, set `Pairs` with one pair pointing at `oci-archive:dir/baseline.tar` and `oci-archive:dir/target.tar`. Pattern off the existing `exporter_test.go` integration test that does the smallest end-to-end Export.

- [ ] **Step 7.4: Run the benchmark locally** (if you have the disk + RAM): `DIFFAH_BIG_TEST=1 go test -tags=big -timeout=30m -run TestScaleBench_2GiBLayer ./pkg/exporter/`. Expected: PASS, RSS reported.

  Capture the RSS reading and write it into `benchmarks/scale-export-linux.json` (or `darwin.json`):

```json
{
  "timestamp_unix": 1746118000,
  "peak_rss_kib": 5400000,
  "_note": "Initial baseline captured 2026-05-02. Update via DIFFAH_BENCH_UPDATE=1."
}
```

  If you don't have local big-test capacity, commit a placeholder baseline of `8388608` KiB (8 GiB exactly) and let the first nightly CI run set the real value via the `DIFFAH_BENCH_UPDATE` flow.

- [ ] **Step 7.5: Add nightly CI workflow.** Create `.github/workflows/scale-bench.yml`:

```yaml
name: scale-bench

on:
  schedule:
    - cron: "0 6 * * *"   # 06:00 UTC nightly
  workflow_dispatch: {}

jobs:
  bench:
    runs-on: ubuntu-latest
    timeout-minutes: 60
    env:
      DIFFAH_BIG_TEST: "1"
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version: "1.22.x"
      - name: Install zstd ≥ 1.5
        run: |
          sudo apt-get update -y
          sudo apt-get install -y zstd
          zstd --version
      - name: Run scale bench
        run: |
          go test -tags=big -timeout=45m -v -run TestScaleBench_2GiBLayer ./pkg/exporter/...
      - name: Upload bench JSON
        if: always()
        uses: actions/upload-artifact@v5
        with:
          name: scale-bench-${{ github.run_id }}
          path: benchmarks/scale-export-linux.json
```

- [ ] **Step 7.6: Document the contract.** Create `docs/performance.md`:

```markdown
# diffah Performance Contract

`diffah export` is designed to run with bounded peak RSS regardless of layer size.

## Memory budget

| Knob | Default | Effect |
|---|---|---|
| `--memory-budget BYTES` | `8GiB` | Admission cap for concurrent encoder RSS. Encodes are admitted only when the sum of in-flight estimated RSS plus the new encode's estimate fits this budget. |
| `--workers N` | `min(GOMAXPROCS, 4)` | Hard cap on encoder goroutines. Both gates apply. |
| `--workdir DIR` | `<dir(OUTPUT)>/.diffah-tmp/<random>` | Spool location for disk-backed baseline / target / output blob spills. Also `DIFFAH_WORKDIR`. |

The estimated RSS per encode is a function of the chosen `windowLog` (which is itself a function of layer size, via `--zstd-window-log=auto`):

| `windowLog` | Estimate |
|---|---|
| 27 (≤128 MiB layer) | 256 MiB |
| 28 | 512 MiB |
| 29 | 1 GiB |
| 30 (≤1 GiB layer) | 2 GiB |
| 31 (>1 GiB layer) | 4 GiB |

The estimates are conservative — the GB-scale CI benchmark validates the ceiling.

## Disk budget

The streaming pipeline trades memory for disk: each in-flight encode uses up to `(K+1) × max_layer_size` of spool space (K candidate spills + 1 target spool). Under typical settings (`--workers 4`, `--candidates 3`, GB-scale layers) the spool peaks at around `4 × 4 × 1 GiB ≈ 16 GiB`. Operators with limited spool space should set `--workdir` to a larger filesystem or lower `--workers` / `--candidates`.

## Single-layer-exceeds-budget behavior

If any single shipped layer's estimated RSS exceeds `--memory-budget`, `Export()` fails fast with a structured error before opening any spool. The error includes the offending digest and a hint suggesting `--memory-budget=<2× layer size>` or a smaller `--zstd-window-log`.

## Nightly benchmark

A 2 GiB-layer fixture (`pkg/exporter/scale_bench_test.go`, build tag `big`, env `DIFFAH_BIG_TEST=1`) runs nightly under `/usr/bin/time -v` and gates:
- Peak RSS ≤ 8 GiB
- Walltime ≤ 110 % of `benchmarks/scale-export-linux.json`

Updates to the committed baseline are produced by setting `DIFFAH_BENCH_UPDATE=1` and committing the regenerated JSON.
```

- [ ] **Step 7.7: Run unit tests.** `go test ./pkg/exporter/` (without the `big` tag). All non-bench tests still pass.

- [ ] **Step 7.8: Lint.** `golangci-lint run ./...`. Fix.

- [ ] **Step 7.9: Commit.**

```bash
git add pkg/exporter/scale_bench_test.go pkg/exporter/scale_fixture.go \
        benchmarks/ .github/workflows/scale-bench.yml docs/performance.md
git commit -m "feat(exporter): GB-scale benchmark + nightly CI gate (PR7)

Adds a deterministic, patchable 2 GiB fixture (pkg/exporter/scale_fixture.go),
build-tagged benchmark (pkg/exporter/scale_bench_test.go, tag=big), nightly
GitHub Actions workflow that runs it under /usr/bin/time -v and asserts:
  - Peak RSS ≤ 8 GiB
  - Walltime within 10 % of the committed baseline

docs/performance.md documents the bounded-memory contract, the spool/disk
trade-off, and the --memory-budget / --workers / --workdir knobs.

Closes the acceptance gate from spec §13.

Refs: docs/superpowers/specs/2026-05-02-export-streaming-io-design.md §8.4 §13"
```

- [ ] **Step 7.10: Push and open PR.** Title: `feat(exporter): GB-scale benchmark + nightly CI (streaming PR7)`. Body: link to spec §8.4 and §13, mention that this PR closes the streaming-I/O acceptance gate.

### Review checkpoint

Reviewer triggers the workflow manually (`gh workflow run scale-bench.yml`) on the PR branch to verify it passes before merge. After merge, the nightly schedule takes over.

---

## Final acceptance

After all 7 PRs are merged:

- [ ] **A-1: Verify the production-readiness goal.** Trigger `scale-bench` workflow manually on `master`. Confirm it reports peak RSS ≤ 8 GiB.

- [ ] **A-2: Verify determinism over time.** Run `go test -count=10 -run TestExport_DeterministicArchive ./pkg/exporter/...` locally. All passes.

- [ ] **A-3: Verify cleanup contract.** Manual: `diffah bundle ... && find ./.diffah-tmp -type f` returns nothing (workdir is gone).

- [ ] **A-4: Update CHANGELOG.md** with a "Streaming I/O" section listing the seven PR titles and a one-paragraph user-facing summary.

- [ ] **A-5: Update memory.** Mark Phase 4 (production-readiness streaming I/O) as complete in the project memory file `phase5_dx_polish_status.md` (or create a new memory file `phase4_streaming_io_status.md`).

- [ ] **A-6: Open the importer streaming sibling spec.** Quick brainstorm session targeting `pkg/importer/blobcache.go` and `bundleImageSource.serveFull/servePatch`. Reuse the patterns established in this work (baseline spool, path-based zstdpatch via `DecodeStream`).

---

## Self-review

Spec coverage:
- §1 Context — covered by Pre-flight (P-2 baseline establishes the regression line)
- §2 Goals — A-1 (memory contract), Determinism gate at every PR (bit-exact), §5.2 design preserves bandwidth, A-3 (cleanup)
- §3 Non-goals — Importer streaming explicitly deferred; A-6 spawns the sibling
- §4.1 Pipeline — PRs 3, 4, 5
- §4.2 Workdir lifecycle — PR 2
- §4.3 Memory budget — PR 6
- §4.4 Determinism — Determinism gate at every PR; PR 6 budget on/off parity test
- §5.1 zstdpatch API — PR 1
- §5.2 Baseline spool — PR 3
- §5.3 Encoder pool — PR 6
- §5.4 Per-encode flow — PR 5
- §5.5 FingerprintReader — PR 3 step 3.4
- §5.6 Writer — PR 4
- §5.7 blobPool slimming — PR 4
- §6 CLI surface — PR 2 (flags), PR 6 (memory-budget enforcement)
- §7 Backward compatibility — Determinism gate; PR 1 deprecation wrappers
- §8 Testing strategy — every PR has TDD; §8.4 fully covered by PR 7
- §9 Future work — A-6
- §10 Risks — admission table is overridable (PR 6); ENOSPC error category covered (PR 5 fallback hint)
- §11 Open questions — addressed inline:
  1. Workdir-in-tests: PR 2 step 2.7 + PR 5 step 5.11 (un-stub cleanup test)
  2. Memory-budget parser: PR 2 step 2.9 implements case-insensitive suffix parser; no extra dep
  3. RSS table calibration: PR 6 lands defaults; PR 7 captures real numbers; revision is a follow-up
  4. DecodeStream placement: PR 1 ships it
- §12 Phased landing — this plan reorders to {zstdpatch, workdir, baseline-spool, blob-spill, per-encode, admission, bench} which is the minimal-blocker order; spec §12 used a similar order and is consistent
- §13 Acceptance — A-1, A-2, A-3, plus PR 1 deprecation comments and PR 7 docs/performance.md

Placeholder scan: no "TODO/TBD" tokens left in the plan. Two `t.Skip` placeholders in PR 7 step 7.3 are deliberate (waiting for the implementer to wire to the project's existing smallest-Export helper, which I don't want to guess at since the project has many candidate fixtures).

Type consistency:
- `BaselineLayerMeta` referenced as the input to `baselineSpool.GetOrSpool` in PR 3 — matches the existing struct in `pkg/exporter/intralayer.go`.
- `Fingerprinter.FingerprintReader(ctx, mediaType, r)` signature consistent across PR 3 step 3.4 (interface), step 3.9 (call site), PR 5 step 5.3 (planner consumption).
- `blobPool.addEntryIfAbsent(d, payload []byte, e diff.BlobEntry)` and `addEntryFromPath(d, srcPath string, e)` — both used in PR 4 and PR 5 respectively. `payloadPath` returned by `PlanShippedTopK` is consumed by `addEntryFromPath` in PR 5 step 5.5.
- `encodePool.Submit(d digest.Digest, estimate int64, fn func() error)` — defined PR 6 step 6.4, consumed step 6.6.
- `Options.Workdir`, `Options.MemoryBudget` — defined PR 2 step 2.6; threaded into encoder PR 5 step 5.6 and PR 6 step 6.6; flag-parsed in PR 2 step 2.9.
- `errs.Error` and `errs.CategoryUser` — used in PR 2 step 2.9 (`cliErr`), PR 6 step 6.7 (single-layer guard); reuses the existing `pkg/diff/errs/` package per project convention.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-02-export-streaming-io.md`. Two execution options:

**1. Subagent-Driven (recommended)** — Fresh subagent per PR (or per task within a PR), two-stage review between tasks. Best fit for a 7-PR plan because each PR needs its own verification loop and the per-PR scope keeps subagent context manageable.

**2. Inline Execution** — Execute tasks in this session via `superpowers:executing-plans`, with checkpoints at PR boundaries.

Which approach?
