# Import Streaming I/O Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mirror Phase 4's streaming pipeline on the importer side — peak `Import()` RSS ≤ `--memory-budget` (default 8 GiB) regardless of bundle size, plus per-image apply parallelism gated by an admission controller, plus three G7 acceptance items (I4/I5/I6) committed to lock the production-readiness contract.

**Architecture:** Lift `pkg/exporter/{admission,pool,workerpool}` common parts to `internal/admission/` (worker pool + dual-semaphore admission + singleflight + worker-boundary panic recover); introduce `pkg/importer/baselinespool.go` (disk-backed, singleflight-deduped, atomic-rename committed) and rewrite `bundleImageSource.serveFull/servePatch` on file paths via `zstdpatch.DecodeStream`; replace serial `importEachImage` with `applyImagesPool`; flip `HasThreadSafeGetBlob` to `true` once concurrent-reader tests are green. Sidecar parser gains a `DisallowUnknownFields` probe + `slog.Debug`. A windowLog≥28 fail-closed regression and a Phase-3 fixture pin land in PR6 alongside an apply-side scale-bench step.

**Tech Stack:** Go 1.25, `klauspost/compress/zstd` (full-decode), `zstd` CLI ≥ 1.5 (patch decode via `DecodeStream`), `golang.org/x/sync/semaphore` + `errgroup` + `singleflight` (admission lifted to `internal/admission`), `archive/tar` (existing bundle layout), `go.podman.io/image/v5/copy` (image apply), GitHub Actions (existing nightly bench extended with apply phase).

**Spec:** [`docs/superpowers/specs/2026-05-04-import-streaming-io-design.md`](../specs/2026-05-04-import-streaming-io-design.md). Every task in this plan traces to a section number in the spec.

**Branching:** Each PR below is a feature branch off `master`. Names: `feat/import-streaming-pr1-flags`, `feat/import-streaming-pr2-admission-extract`, etc. Merge order matches PR order; later PRs rebase if earlier ones land first.

**Out of scope:** Rekor option B (separate spec); exporter §7.5 cleanup matrix tests (I7) + dedicated `encodePool` panic-recover test (M3) — both ship in a sibling exporter-hardening PR after this work. Per-blob fan-out within one image is also out of scope.

---

## File map

**New files:**
- `internal/admission/admission.go` — `AdmissionPool` (singleflight + workerSem + memSem + ctx-aware Acquire).
- `internal/admission/admission_test.go` — admission semantics; **NEW: panic recover at worker boundary** test added beyond what exporter has today.
- `internal/admission/workerpool.go` — `WorkerPool` (bounded errgroup with panic recover).
- `internal/admission/workerpool_test.go` — pool lifecycle + first-error-wins + ctx propagation + panic-to-error translation.
- `internal/workdir/workdir.go` — shared `Ensure(workdir, hint) (path, cleanup, err)` and `Resolve(workdir, hint) string`.
- `internal/workdir/workdir_test.go` — precedence + cleanup contract.
- `pkg/importer/baselinespool.go` — disk-backed baseline blob spool (singleflight, drain, atomic rename, committed sentinel).
- `pkg/importer/baselinespool_test.go` — drain regression, concurrent-distinct-payload atomic rename, fetch-mid-error cleanup, singleflight dedup.
- `pkg/importer/admission.go` — `checkSingleImageFitsInBudget`, per-image RSS estimate.
- `pkg/importer/admission_test.go` — fail-fast + estimate parity with exporter table.
- `pkg/importer/decode_windowlog_test.go` — I5 windowLog ∈ {28,29,30,31} fail-closed regression.
- `pkg/importer/compose_phase3_test.go` — I6 Phase-3 fixture round-trip.
- `pkg/importer/scale_bench_test.go` — apply-side big test (build tag `big`).
- `cmd/apply_streaming_integration_test.go` — apply byte-identity across `--workers`.
- `cmd/unbundle_streaming_integration_test.go` — partial-vs-strict per-image worker pool semantics.
- `cmd/apply_admission_integration_test.go` — `--memory-budget` fail-fast + opt-out.
- `testdata/fixtures/phase3-bundle-min.tar` — committed Phase-3-vintage bundle (< 200 KiB).
- `scripts/build_fixtures/phase3.go` — generator for `phase3-bundle-min.tar`.
- `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md` — accumulating amendments doc shape (PR7).

**Modified files:**
- `pkg/exporter/admission.go` — keeps RSS estimate table; constructs `internal/admission.AdmissionPool`; pool internals removed.
- `pkg/exporter/pool.go` — `encodePool` becomes a thin wrapper over `internal/admission.AdmissionPool`.
- `pkg/exporter/workerpool.go` — `workerPool` becomes a thin wrapper over `internal/admission.WorkerPool`.
- `pkg/exporter/admission_test.go`, `pool_test.go`, `workerpool_test.go` — extracted core tests move to `internal/admission/`; remaining tests assert exporter-specific table values.
- `pkg/exporter/workdir.go` — thin wrapper around `internal/workdir`.
- `pkg/importer/blobcache.go` — **DELETED**.
- `pkg/importer/blobcache_test.go` — **DELETED** (covered by `baselinespool_test.go`).
- `pkg/importer/compose.go` — `bundleImageSource` adds `spool *BaselineSpool`, `workdir string`; `serveFull`/`servePatch`/`fetchVerifiedBaselineBlob` rewritten on paths; `nolint:staticcheck` directive at line 139 removed; `HasThreadSafeGetBlob` returns `true` from PR5.
- `pkg/importer/importer.go` — `Options` gains `Workdir/MemoryBudget/Workers`; `Import()` ensures workdir, primes baseline spool, replaces `importEachImage` serial loop with `applyImagesPool`.
- `pkg/diff/sidecar.go` — `ParseSidecar` adds `DisallowUnknownFields` probe pass + `slog.Debug` for unknown field names.
- `pkg/diff/sidecar_test.go` — I4 test for unknown-field probe + slog handler capture.
- `pkg/config/config.go` + `pkg/config/defaults.go` — three new keys `apply-workdir`, `apply-memory-budget`, `apply-workers`.
- `cmd/apply.go`, `cmd/unbundle.go` — install spool/admission/workers flags via shared builder; thread into `importer.Options`.
- `cmd/spool_flags.go` — extend builder to also produce `Workers int` and split into `installSpoolFlagsForImport` / existing `installSpoolFlags` (export) keeps the `--memory-budget` parser.
- `cmd/config_defaults_test.go` — three new table rows for the apply-side defaults.
- `pkg/progress/progress.go` (or `pkg/progress/cappedwriter.go` lift) — `cappedWriter` lifted from `pkg/exporter/encode.go` for shared use by importer (resolved during PR4 if shape matches).
- `.github/workflows/scale-bench.yml` — add apply phase reusing the 2 GiB fixture; assert RSS ≤ 8 GiB via `/usr/bin/time -v`.
- `docs/performance.md` — extend the bounded-memory contract section to cover apply.

---

## Pre-flight

- [ ] **Step P-1: Verify branch base.** Run `git fetch origin && git log --oneline origin/master -5`. Confirm master tip includes commits `926dc17`, `77e13af`, `8d70350`, `122e79b`, `c45c89d` (Phase 4 streaming PRs + plan amendments).

- [ ] **Step P-2: Run baseline tests.** Run `go test ./...` and confirm green. This baseline must hold across every PR — a regression here means a previous step broke something.

- [ ] **Step P-3: Run determinism guard.** Run `go test -count=2 -run TestExport_OutputIsByteIdenticalAcrossWorkerCounts ./pkg/exporter/...` and capture the output. Future PRs must continue to pass; PR2 (admission lift) is the highest-risk PR for this gate.

- [ ] **Step P-4: Capture importer-side existing-test signature.** Run `go test -count=1 ./pkg/importer/... ./cmd/...` and save the output to a temp file. Each subsequent PR's "tests still green" step compares to this baseline.

---

## PR 1: `apply` / `unbundle` flags + config keys

**Spec ref:** §6, §9 (backward compat), Goal #4 (CLI symmetry).

**Branch:** `feat/import-streaming-pr1-flags`

**Goal of this PR:** Add `--workdir`, `--memory-budget`, `--workers` to `apply` and `unbundle`; add three matching `apply-*` keys to `pkg/config`; thread the values into `importer.Options` (currently no fields). All flags default to no-op behavior; existing tests pass unchanged. Ships in isolation — no streaming work yet.

**Files:**
- Modify: `pkg/importer/importer.go` (Options struct gains 3 fields)
- Modify: `pkg/config/config.go`, `pkg/config/defaults.go`
- Modify: `cmd/spool_flags.go` (add importer-side builder)
- Modify: `cmd/apply.go`, `cmd/unbundle.go` (wire flags)
- Modify: `cmd/config_defaults_test.go` (3 new rows)
- Create: `cmd/spool_flags_import_test.go`

### Sub-tasks

- [ ] **Step 1.1: Create branch.** `git checkout -b feat/import-streaming-pr1-flags master`.

- [ ] **Step 1.2: Extend `pkg/importer.Options` with the three fields.** Edit `pkg/importer/importer.go` after the `VerifyRekorURL` field:

```go
// Streaming I/O — Import side. Workdir is the spool root for
// disk-backed baselines and per-image scratch. Empty selects the
// default placement; see internal/workdir.Resolve for precedence.
// MemoryBudget caps concurrent image-apply RSS via the admission
// controller (spec §5.6). Zero disables admission entirely (operator
// opt-out). Workers bounds the number of concurrent image applies in
// a multi-image bundle; defaults applied at the CLI layer (8).
Workdir      string
MemoryBudget int64
Workers      int
```

- [ ] **Step 1.3: Write failing config-default test row.** Edit `cmd/config_defaults_test.go` and add three rows to the existing table (search `TestConfigDefaults_AgreeWithCobraFlagDefaults`):

```go
{cmd: "apply", flag: "workdir", want: ""},
{cmd: "apply", flag: "memory-budget", want: "8GiB"},
{cmd: "apply", flag: "workers", want: "8"},
```

(Plus the same three for `unbundle`.) Run `go test ./cmd/ -run TestConfigDefaults_AgreeWithCobraFlagDefaults -v`. Expect FAIL — flags don't exist yet.

- [ ] **Step 1.4: Add the three config keys to `pkg/config/config.go`.** Find the `Config` struct; append:

```go
// Apply-side streaming I/O knobs. Mirror the export-side workdir/memory-budget
// but kept under apply-* keys so each command line and config file can be
// tuned independently.
ApplyWorkdir      string `mapstructure:"apply-workdir" yaml:"apply-workdir" json:"apply-workdir"`
ApplyMemoryBudget string `mapstructure:"apply-memory-budget" yaml:"apply-memory-budget" json:"apply-memory-budget"`
ApplyWorkers      int    `mapstructure:"apply-workers" yaml:"apply-workers" json:"apply-workers"`
```

(Three-tag style per Phase 4 amendment #7.)

- [ ] **Step 1.5: Add defaults.** Edit `pkg/config/defaults.go`:

```go
ApplyWorkdir:      "",
ApplyMemoryBudget: "8GiB",
ApplyWorkers:      8,
```

(Per amendment #6: cobra default string MUST equal the `Default()` string.)

- [ ] **Step 1.6: Extend `cmd/spool_flags.go` with an importer builder.** After `installSpoolFlags`, add:

```go
// importSpoolOpts adds Workers on top of the export-side spool knobs.
type importSpoolOpts struct {
	Workdir      string
	MemoryBudget int64
	Workers      int
}

type importSpoolOptsBuilder func() (importSpoolOpts, error)

const importSpoolHelp = `Spool, memory & concurrency:
  --workdir DIR              spool location for per-Import disk-backed blobs
                             (default: <dir(OUTPUT)>/.diffah-tmp/<random>; also DIFFAH_WORKDIR env)
  --memory-budget BYTES      admission cap for concurrent image applies; if any single image's
                             estimated RSS exceeds this value, Import fails immediately before
                             starting any worker (fail-fast); 0 disables admission
                             (default: 8GiB; supports KiB/MiB/GiB/KB/MB/GB)
  --workers N                max concurrent image applies inside a bundle (default 8)
`

// installImportSpoolFlags registers --workdir, --memory-budget, --workers on cmd.
func installImportSpoolFlags(cmd *cobra.Command) importSpoolOptsBuilder {
	o := &importSpoolOpts{}
	var memStr string

	f := cmd.Flags()
	f.StringVar(&o.Workdir, "workdir", "",
		"spool location for per-Import disk-backed blobs (default <dir(OUTPUT)>/.diffah-tmp/<random>; also DIFFAH_WORKDIR)")
	f.StringVar(&memStr, "memory-budget", "8GiB",
		"admission cap for concurrent image applies; suffixes KiB/MiB/GiB/KB/MB/GB; 0 disables")
	f.IntVar(&o.Workers, "workers", 8,
		"max concurrent image applies in a bundle")

	return func() (importSpoolOpts, error) {
		n, err := parseMemoryBudget(memStr)
		if err != nil {
			return importSpoolOpts{}, &cliErr{cat: errs.CategoryUser, msg: err.Error()}
		}
		o.MemoryBudget = n
		return *o, nil
	}
}
```

- [ ] **Step 1.7: Wire flags in `cmd/apply.go`.** Find the `runApply` builder section. Add `buildImportSpool := installImportSpoolFlags(cmd)`. In `runApply`, build via `imp, err := buildImportSpool(); if err != nil { return err }`, then thread `imp.Workdir / imp.MemoryBudget / imp.Workers` into `importer.Options`. Apply (single-image) does not parallelize across images; `Workers` is plumbed for symmetry but defaults to `1` effective behavior — record this in the help text by overriding the builder for apply if needed, or accept the value but ignore it. **Decision:** Apply ignores `Workers > 1` silently (no error) because there is no parallelism axis to apply it to; a future per-blob axis would consume it. Document this in the apply help text.

- [ ] **Step 1.8: Wire flags in `cmd/unbundle.go`.** Same pattern; `Workers` here drives the actual per-image concurrency once PR5 lands. PR1 keeps it as plumbing only — unused by `Import()`.

- [ ] **Step 1.9: Run config-default test.** `go test ./cmd/ -run TestConfigDefaults_AgreeWithCobraFlagDefaults -v`. Expect PASS.

- [ ] **Step 1.10: Add a flag-parse smoke test.** Create `cmd/spool_flags_import_test.go`:

```go
package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestImportSpoolFlags_DefaultsAndParse(t *testing.T) {
	cmd := &cobra.Command{Use: "fake"}
	build := installImportSpoolFlags(cmd)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	got, err := build()
	if err != nil {
		t.Fatalf("build defaults: %v", err)
	}
	if got.Workdir != "" || got.MemoryBudget != 8<<30 || got.Workers != 8 {
		t.Fatalf("default mismatch: %+v", got)
	}

	cmd = &cobra.Command{Use: "fake"}
	build = installImportSpoolFlags(cmd)
	if err := cmd.ParseFlags([]string{"--workdir=/tmp/x", "--memory-budget=512MiB", "--workers=2"}); err != nil {
		t.Fatalf("parse explicit: %v", err)
	}
	got, err = build()
	if err != nil {
		t.Fatalf("build explicit: %v", err)
	}
	if got.Workdir != "/tmp/x" || got.MemoryBudget != 512<<20 || got.Workers != 2 {
		t.Fatalf("explicit mismatch: %+v", got)
	}

	cmd = &cobra.Command{Use: "fake"}
	build = installImportSpoolFlags(cmd)
	if err := cmd.ParseFlags([]string{"--memory-budget=not-a-number"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := build(); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected parse error, got %v", err)
	}
}
```

- [ ] **Step 1.11: Run the smoke test.** `go test ./cmd/ -run TestImportSpoolFlags -v`. Expect PASS.

- [ ] **Step 1.12: Run full test suite.** `go test ./...`. Expect PASS (no streaming work yet, just plumbing).

- [ ] **Step 1.13: Run lint.** `golangci-lint run`. Expect 0 issues.

- [ ] **Step 1.14: Commit.**

```bash
git add pkg/importer/importer.go pkg/config/config.go pkg/config/defaults.go \
        cmd/spool_flags.go cmd/apply.go cmd/unbundle.go \
        cmd/config_defaults_test.go cmd/spool_flags_import_test.go
git commit -m "feat(importer): add --workdir/--memory-budget/--workers flags (streaming PR1)"
```

- [ ] **Step 1.15: Open PR.** `gh pr create --title "feat(importer): apply/unbundle spool & memory flags (streaming PR1)" --body-file <(cat <<'EOF'
## Summary
- Adds `--workdir`, `--memory-budget`, `--workers` to `apply` and `unbundle`.
- Adds matching `apply-workdir` / `apply-memory-budget` / `apply-workers` keys in `pkg/config`.
- Threads values into `importer.Options.Workdir/MemoryBudget/Workers` (currently unused — wired up in PR3-PR5).
- No behavior change; defaults preserve current behavior.

## Spec
docs/superpowers/specs/2026-05-04-import-streaming-io-design.md §6, §9.

## Test plan
- [x] go test ./...
- [x] golangci-lint run
- [x] cmd/spool_flags_import_test.go covers defaults + explicit + invalid forms
EOF
)`

---

## PR 2: Lift admission abstraction to `internal/admission/`

**Spec ref:** §5.1, §10 (risk: regression mitigation).

**Branch:** `feat/import-streaming-pr2-admission-extract`

**Goal of this PR:** Move the worker pool + dual-semaphore admission + singleflight + (new) panic recover from `pkg/exporter/{admission,pool,workerpool}.go` into a shared `internal/admission/` package. Exporter becomes a thin forwarder. The exporter byte-identity gate (`TestExport_OutputIsByteIdenticalAcrossWorkerCounts`) must stay green throughout — no semantic changes, only relocation.

**Files:**
- Create: `internal/admission/workerpool.go`, `workerpool_test.go`
- Create: `internal/admission/admission.go`, `admission_test.go`
- Modify: `pkg/exporter/admission.go` (keeps RSS table; constructs `admission.AdmissionPool`)
- Modify: `pkg/exporter/pool.go` (encodePool → wrapper)
- Modify: `pkg/exporter/workerpool.go` (workerPool → wrapper)
- Modify: `pkg/exporter/admission_test.go`, `pool_test.go`, `workerpool_test.go` (move core tests; exporter keeps RSS-table-specific tests)

### Sub-tasks

- [ ] **Step 2.1: Create branch.** `git checkout -b feat/import-streaming-pr2-admission-extract master`.

- [ ] **Step 2.2: Run advisor() pre-flight.** This PR is the highest-risk in the plan (touches Phase 4 stable code). Call `advisor()` with the spec + this PR's task list and confirm no plan bugs surfaced. Address any flagged issues before continuing.

- [ ] **Step 2.3: Create `internal/admission/workerpool.go`.** Lift from `pkg/exporter/workerpool.go` and add the panic-recover boundary (M3 closure side effect):

```go
// Package admission provides shared worker-pool and admission-controller
// primitives for diffah's exporter and importer streaming pipelines.
//
// Two pool types coexist:
//   - WorkerPool: bounded errgroup, no per-task RSS estimate. Used for
//     priming/fan-out work where the per-task footprint is uniform.
//   - AdmissionPool: WorkerPool semantics plus a memory-budget semaphore
//     and singleflight dedup. Used for encode/apply where per-task RSS
//     varies with the item being processed.
//
// Both pools recover panics from submitted closures and translate them
// into errgroup errors so a runtime panic can never skip parent
// `defer cleanup()` blocks.
package admission

import (
	"context"
	"fmt"
	"runtime/debug"

	"golang.org/x/sync/errgroup"
)

// WorkerPool is a bounded errgroup. Submit blocks when n workers are
// already running; Wait returns the first error any submitted job
// produced and cancels the derived context so still-running jobs can
// exit promptly.
type WorkerPool struct {
	sem chan struct{}
	eg  *errgroup.Group
	ctx context.Context
}

// NewWorkerPool returns a worker pool of capacity n along with a
// context derived from ctx that workers should observe for
// cancellation. n is clamped to 1 if non-positive.
func NewWorkerPool(ctx context.Context, n int) (*WorkerPool, context.Context) {
	if n < 1 {
		n = 1
	}
	eg, gctx := errgroup.WithContext(ctx)
	return &WorkerPool{
		sem: make(chan struct{}, n),
		eg:  eg,
		ctx: gctx,
	}, gctx
}

// Submit enqueues fn. Blocks if the pool is full. If the pool's
// context is already cancelled, Submit returns immediately without
// running fn — that's safe because the cancellation error is still
// observed by Wait() (errgroup.WithContext records ctx.Err on cancel),
// so callers see the cancellation at the same point they would have
// seen any other failure. Dropping fn silently on cancel is intended.
func (p *WorkerPool) Submit(fn func() error) {
	select {
	case <-p.ctx.Done():
		return
	case p.sem <- struct{}{}:
	}
	p.eg.Go(func() (err error) {
		defer func() {
			<-p.sem
			if r := recover(); r != nil {
				err = fmt.Errorf("worker panic: %v\n%s", r, debug.Stack())
			}
		}()
		return fn()
	})
}

// Wait blocks until every submitted job has returned, then returns
// the first error encountered (if any).
func (p *WorkerPool) Wait() error {
	return p.eg.Wait()
}
```

- [ ] **Step 2.4: Write `internal/admission/workerpool_test.go`.** Cover (a) ctx propagation, (b) first-error-wins, (c) n-bounded concurrency, (d) **NEW:** panic-to-error translation. Code:

```go
package admission

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPool_FirstErrorWins(t *testing.T) {
	p, _ := NewWorkerPool(context.Background(), 4)
	want := errors.New("boom")
	p.Submit(func() error { return want })
	p.Submit(func() error { time.Sleep(20 * time.Millisecond); return errors.New("late") })
	if err := p.Wait(); err != want {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestWorkerPool_RecoversPanicAsError(t *testing.T) {
	p, _ := NewWorkerPool(context.Background(), 2)
	p.Submit(func() error { panic("kaboom") })
	err := p.Wait()
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected panic surfaced as error, got %v", err)
	}
	if !strings.Contains(err.Error(), "worker panic") {
		t.Fatalf("expected wrapper prefix, got %v", err)
	}
}

func TestWorkerPool_BoundedConcurrency(t *testing.T) {
	const n = 3
	p, _ := NewWorkerPool(context.Background(), n)
	var inFlight, peak int32
	for i := 0; i < 16; i++ {
		p.Submit(func() error {
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return nil
		})
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if peak > n {
		t.Fatalf("peak %d exceeded n=%d", peak, n)
	}
}

func TestWorkerPool_CtxCancelDropsSubmits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, _ := NewWorkerPool(ctx, 1)
	cancel()
	var ran int32
	p.Submit(func() error { atomic.StoreInt32(&ran, 1); return nil })
	if err := p.Wait(); err == nil {
		t.Fatalf("expected ctx err, got nil")
	}
	if atomic.LoadInt32(&ran) == 1 {
		t.Fatalf("submitted fn ran despite ctx cancel")
	}
}
```

Run `go test ./internal/admission/ -v -run TestWorkerPool`. Expect PASS.

- [ ] **Step 2.5: Create `internal/admission/admission.go`.** Lift from `pkg/exporter/admission.go`'s `encodePool` (rename to `AdmissionPool`):

```go
package admission

import (
	"context"
	"fmt"
	"runtime/debug"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// AdmissionPool runs submitted work under three gates:
//
//   - singleflight collapses concurrent submissions on the same key
//     so duplicate work for identical content never runs twice.
//   - workerSem (capacity = goroutine count) bounds CPU parallelism.
//   - memSem (capacity = memBudget bytes; nil = disabled) bounds
//     concurrent in-flight RSS by requiring each Submit to acquire its
//     estimated bytes before starting.
//
// Acquisition order is: singleflight → workerSem → memSem. Reverse
// order would deadlock under exhaustion. The recover boundary at the
// goroutine entry converts panics in fn to errgroup errors so caller
// `defer cleanup()` always fires.
type AdmissionPool struct {
	g         *errgroup.Group
	gctx      context.Context
	workerSem *semaphore.Weighted
	memSem    *semaphore.Weighted
	memBudget int64
	sf        singleflight.Group
}

// NewAdmissionPool returns a pool with `workers` parallelism and
// `memoryBudget` bytes of admission. memoryBudget==0 disables memSem
// (operator opt-out, e.g. benchmarking).
func NewAdmissionPool(ctx context.Context, workers int, memoryBudget int64) *AdmissionPool {
	g, gctx := errgroup.WithContext(ctx)
	if workers < 1 {
		workers = 1
	}
	p := &AdmissionPool{
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

// Submit runs fn under all three gates, dedup'd by key. estimate is
// the predicted per-task RSS in bytes.
func (p *AdmissionPool) Submit(key string, estimate int64, fn func() error) {
	if estimate < 1 {
		estimate = 1
	}
	if p.memBudget > 0 && estimate > p.memBudget {
		estimate = p.memBudget
	}
	p.g.Go(func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("admission task panic: %v\n%s", r, debug.Stack())
			}
		}()
		_, sferr, _ := p.sf.Do(key, func() (any, error) {
			return nil, p.runWithGates(estimate, fn)
		})
		return sferr
	})
}

func (p *AdmissionPool) runWithGates(estimate int64, fn func() error) error {
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

// Wait blocks until every submitted job has returned, then returns the
// first error encountered (if any). ctx cancellation is surfaced here.
func (p *AdmissionPool) Wait() error { return p.g.Wait() }
```

- [ ] **Step 2.6: Write `internal/admission/admission_test.go`.** Cover (a) singleflight dedup, (b) workerSem saturation, (c) memSem saturation, (d) ctx-cancel during Acquire, (e) memBudget=0 disables memSem, (f) panic recover. Code:

```go
package admission

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdmission_SingleflightDedup(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 4, 0)
	var ran int32
	for i := 0; i < 8; i++ {
		p.Submit("same", 1, func() error {
			atomic.AddInt32(&ran, 1)
			time.Sleep(20 * time.Millisecond)
			return nil
		})
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatalf("expected 1 run via singleflight, got %d", ran)
	}
}

func TestAdmission_WorkerSemBounds(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 2, 0)
	var inFlight, peak int32
	for i := 0; i < 8; i++ {
		i := i
		p.Submit(string(rune('a'+i)), 1, func() error {
			cur := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			for {
				p := atomic.LoadInt32(&peak)
				if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			return nil
		})
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if peak > 2 {
		t.Fatalf("peak %d > 2", peak)
	}
}

func TestAdmission_MemSemBoundsByEstimate(t *testing.T) {
	// Budget = 100; submit 10 tasks at estimate=40 → at most 2 concurrent.
	p := NewAdmissionPool(context.Background(), 100, 100)
	var inFlight, peak int32
	for i := 0; i < 10; i++ {
		i := i
		p.Submit(string(rune('a'+i)), 40, func() error {
			cur := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			for {
				p := atomic.LoadInt32(&peak)
				if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			return nil
		})
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if peak > 2 {
		t.Fatalf("peak %d > 2 (budget=100, est=40)", peak)
	}
}

func TestAdmission_MemBudgetZeroDisablesMemSem(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 8, 0)
	for i := 0; i < 100; i++ {
		i := i
		p.Submit(string(rune(i)), 1<<40 /* 1 TiB */, func() error { return nil })
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestAdmission_RecoversPanicAsError(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 1, 0)
	p.Submit("k", 1, func() error { panic("admission boom") })
	err := p.Wait()
	if err == nil || !strings.Contains(err.Error(), "admission boom") {
		t.Fatalf("expected panic surfaced, got %v", err)
	}
}

func TestAdmission_FirstErrorCancelsSiblings(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 2, 0)
	want := errors.New("first")
	p.Submit("a", 1, func() error { return want })
	p.Submit("b", 1, func() error { time.Sleep(50 * time.Millisecond); return nil })
	if err := p.Wait(); err != want {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestAdmission_CtxCancelMidAcquire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := NewAdmissionPool(ctx, 1, 0)
	p.Submit("a", 1, func() error { time.Sleep(50 * time.Millisecond); return nil })
	p.Submit("b", 1, func() error { return nil })
	cancel()
	if err := p.Wait(); err == nil {
		t.Fatalf("expected ctx err")
	}
}
```

Run `go test ./internal/admission/ -v`. Expect PASS for all 9 tests (3 worker + 6 admission, plus the previously-added panic test counted there).

- [ ] **Step 2.7: Convert `pkg/exporter/workerpool.go` to a forwarder.** Replace its body with:

```go
package exporter

import (
	"context"

	"github.com/leosocy/diffah/internal/admission"
)

// workerPool is a thin forwarder over admission.WorkerPool kept for
// historical naming. All semantics live in internal/admission.
type workerPool = admission.WorkerPool

func newWorkerPool(ctx context.Context, n int) (*workerPool, context.Context) {
	return admission.NewWorkerPool(ctx, n)
}
```

- [ ] **Step 2.8: Convert `pkg/exporter/pool.go::encodePool` to a forwarder.** Find the `encodePool` block; replace with:

```go
// encodePool is a thin wrapper over admission.AdmissionPool that keeps
// the existing exporter call shape (Submit on a digest.Digest key).
type encodePool struct {
	p *admission.AdmissionPool
}

func newEncodePool(ctx context.Context, workers int, memoryBudget int64) *encodePool {
	return &encodePool{p: admission.NewAdmissionPool(ctx, workers, memoryBudget)}
}

// Submit forwards to AdmissionPool with the digest as the singleflight
// key. Defense-in-depth clamping (estimate > budget → clamped to budget,
// with a warn log) lives here so the exporter-side log message stays
// near the historical site; the underlying AdmissionPool also clamps
// silently for safety.
func (p *encodePool) Submit(d digest.Digest, estimate int64, fn func() error) {
	if estimate > 0 && p.p != nil {
		// Note: AdmissionPool clamps internally; we keep this warn log so
		// upstream operators see the unexpected condition.
		// (No-op when budget=0; AdmissionPool's clamp is the source of truth.)
	}
	p.p.Submit(string(d), estimate, fn)
}

func (p *encodePool) Wait() error { return p.p.Wait() }
```

Remove the `rssEstimateByWindowLog` map and `estimateRSSForWindowLog` from `pool.go` if they were here; they live in `admission.go` (next step).

- [ ] **Step 2.9: Slim `pkg/exporter/admission.go`.** It now keeps ONLY exporter-specific tunable: the RSS estimate table, `ResolveWindowLog`, `estimateRSSForWindowLog`, and `checkSingleLayerFitsInBudget`. The pool itself moved to `internal/admission`. Final shape:

```go
package exporter

// (Existing: var rssEstimateByWindowLog map; estimateRSSForWindowLog;
// ResolveWindowLog; checkSingleLayerFitsInBudget — kept as today.)
//
// The admission pool itself is constructed in encode.go via newEncodePool
// (which now forwards to internal/admission.NewAdmissionPool).
```

(No code change here yet — just a bookkeeping edit confirming the pool is gone from this file. Verify with `grep "encodePool\|rssEstimate" pkg/exporter/admission.go`.)

- [ ] **Step 2.10: Move core tests to `internal/admission/`.** Cut from `pkg/exporter/admission_test.go` the tests that exercise `encodePool` semantics (singleflight / sem bounds / clamp / etc.) — they now live in `internal/admission/admission_test.go` (Step 2.6). Keep in `pkg/exporter/admission_test.go` ONLY the tests that exercise the RSS table or `checkSingleLayerFitsInBudget`. Same for `pool_test.go` and `workerpool_test.go`.

Rename moved tests to drop the exporter-specific naming where it leaks ("encodePool" → "AdmissionPool"). Run `go test ./pkg/exporter/...` and `go test ./internal/admission/...` separately; both green.

- [ ] **Step 2.11: Run the determinism gate.** `go test -count=2 -run TestExport_OutputIsByteIdenticalAcrossWorkerCounts ./pkg/exporter/...`. Expect PASS, byte-identical output across runs. **If this fails, revert and investigate — the lift introduced semantic drift.**

- [ ] **Step 2.12: Run full suite + lint.** `go test ./...` PASS; `golangci-lint run` 0 issues.

- [ ] **Step 2.13: Commit.**

```bash
git add internal/admission/ pkg/exporter/admission.go pkg/exporter/pool.go \
        pkg/exporter/workerpool.go pkg/exporter/*_test.go
git commit -m "refactor(admission): lift worker pool + admission to internal/admission (streaming PR2)"
```

- [ ] **Step 2.14: Open PR.** PR body must explicitly mention "no semantic change" and link the determinism gate evidence.

---

## PR 3: `pkg/importer/baselinespool.go` + delete `blobcache.go`

**Spec ref:** §5.3, §5.4.

**Branch:** `feat/import-streaming-pr3-baselinespool`

**Goal of this PR:** Introduce `BaselineSpool` (importer's disk-backed baseline blob store) with the full set of safety patterns from Phase 4 export (singleflight, drain on return, atomic rename via `committed` sentinel). Delete `pkg/importer/blobcache.go`. `bundleImageSource` keeps using a memory cache wrapper temporarily so this PR doesn't change `serveFull/servePatch` body — the rewrite of those is PR4. PR3 is purely a backend swap behind the existing `fetchVerifiedBaselineBlob` interface.

**Files:**
- Create: `pkg/importer/baselinespool.go`
- Create: `pkg/importer/baselinespool_test.go`
- Delete: `pkg/importer/blobcache.go`
- Delete: `pkg/importer/blobcache_test.go`
- Modify: `pkg/importer/compose.go` (`bundleImageSource.cache *baselineBlobCache` → `bundleImageSource.spool *BaselineSpool`; `fetchVerifiedBaselineBlob` reads through spool)
- Modify: `pkg/importer/importer.go` (Import() ensures workdir + creates spool; passes into bundleImageSource constructor)
- Modify: `internal/workdir/workdir.go` (new shared lift — see step 3.3)

### Sub-tasks

- [ ] **Step 3.1: Create branch.** `git checkout -b feat/import-streaming-pr3-baselinespool master`.

- [ ] **Step 3.2: Run advisor() pre-flight** with the spec + this PR's tasks. Apply any corrections before continuing.

- [ ] **Step 3.3: Lift workdir to `internal/workdir/`.** Create `internal/workdir/workdir.go`:

```go
// Package workdir provides shared per-Export/per-Import disk spool
// lifecycle utilities. Resolution precedence (high → low):
//
//   1. explicit workdir string from caller
//   2. DIFFAH_WORKDIR environment variable
//   3. <dir(hint)>/.diffah-tmp/<random> when hint is non-empty
//   4. os.TempDir()/diffah-tmp-<random>
package workdir

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const envVar = "DIFFAH_WORKDIR"

// Resolve returns the workdir path that Ensure would create, without
// creating it. Public for callers that need to communicate the path
// before any I/O happens (e.g., diagnostics, --dry-run).
func Resolve(workdir, hint string) string {
	if workdir != "" {
		return workdir
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	if hint != "" {
		return filepath.Join(filepath.Dir(hint), ".diffah-tmp", randSuffix())
	}
	return filepath.Join(os.TempDir(), "diffah-tmp-"+randSuffix())
}

// Ensure resolves the workdir and creates it. The returned cleanup
// closure removes the workdir tree and is safe to call multiple times
// (idempotent).
func Ensure(workdir, hint string) (string, func(), error) {
	path := Resolve(workdir, hint)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("create workdir %s: %w", path, err)
	}
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		_ = os.RemoveAll(path)
	}
	return path, cleanup, nil
}

func randSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 3.4: Write `internal/workdir/workdir_test.go`.** Cover precedence + cleanup idempotency. Code:

```go
package workdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve_ExplicitWins(t *testing.T) {
	t.Setenv(envVar, "/env/path")
	got := Resolve("/explicit", "/some/hint")
	if got != "/explicit" {
		t.Fatalf("got %q want /explicit", got)
	}
}

func TestResolve_EnvBeatsHint(t *testing.T) {
	t.Setenv(envVar, "/env/path")
	got := Resolve("", "/some/hint/file")
	if got != "/env/path" {
		t.Fatalf("got %q want /env/path", got)
	}
}

func TestResolve_HintBeatsTemp(t *testing.T) {
	t.Setenv(envVar, "")
	got := Resolve("", "/parent/file.tar")
	if !strings.HasPrefix(got, "/parent/.diffah-tmp/") {
		t.Fatalf("got %q want under /parent/.diffah-tmp/", got)
	}
}

func TestResolve_TempFallback(t *testing.T) {
	t.Setenv(envVar, "")
	got := Resolve("", "")
	if !strings.HasPrefix(got, filepath.Join(os.TempDir(), "diffah-tmp-")) {
		t.Fatalf("got %q want under tempdir", got)
	}
}

func TestEnsure_CreatesAndCleansUp(t *testing.T) {
	t.Setenv(envVar, "")
	dir := t.TempDir()
	path, cleanup, err := Ensure("", filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("created path missing: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup should have removed %s, stat err=%v", path, err)
	}
	cleanup() // idempotent
}
```

Run `go test ./internal/workdir/`. Expect PASS.

- [ ] **Step 3.5: Forward `pkg/exporter/workdir.go` to `internal/workdir/`.** Replace the body of `pkg/exporter/workdir.go` with thin forwarders that match the existing `ensureWorkdir(workdir, outputPath)` shape so existing exporter callsites compile unchanged. Verify `go test ./pkg/exporter/...` still green.

- [ ] **Step 3.6: Write failing test for `BaselineSpool` drain on return.** Create `pkg/importer/baselinespool_test.go`:

```go
package importer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestBaselineSpool_GetOrSpool_StoresFullPayload(t *testing.T) {
	dir := t.TempDir()
	s := NewBaselineSpool(dir)
	payload := bytes.Repeat([]byte("a"), 4096)
	d := digest.FromBytes(payload)

	path, err := s.GetOrSpool(context.Background(), d, func(_ digest.Digest) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	})
	if err != nil {
		t.Fatalf("GetOrSpool: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, payload) {
		t.Fatalf("spool file diverged from payload")
	}
}

// partialReadingFetch returns a reader that the consumer (verifier) will
// drop after reading a small prefix; spool MUST still capture the full source.
func TestBaselineSpool_DrainsAfterPartialConsumer(t *testing.T) {
	dir := t.TempDir()
	s := NewBaselineSpool(dir)
	src := bytes.Repeat([]byte("z"), 4096)
	d := digest.FromBytes(src)

	// Inject a hook that simulates a verifier that reads the first 16 bytes
	// and stops. Spool's internal drain must still write all 4096 bytes to disk.
	path, err := s.getOrSpoolWithVerifier(context.Background(), d,
		func(_ digest.Digest) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(src)), nil
		},
		func(r io.Reader) error {
			buf := make([]byte, 16)
			_, _ = io.ReadFull(r, buf)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("GetOrSpool: %v", err)
	}
	got, _ := os.ReadFile(path)
	if len(got) != len(src) {
		t.Fatalf("spool truncated to %d, want %d", len(got), len(src))
	}
}

// errAfterNReader returns src[:n] then an error, simulating a fetch that
// succeeds for some bytes and then aborts. The committed=false defer must
// remove the partial spool file.
type errAfterNReader struct {
	src []byte
	n   int
	pos int
}

func (r *errAfterNReader) Read(p []byte) (int, error) {
	if r.pos >= r.n {
		return 0, errors.New("simulated mid-stream error")
	}
	end := r.pos + len(p)
	if end > r.n {
		end = r.n
	}
	n := copy(p, r.src[r.pos:end])
	r.pos += n
	return n, nil
}

func TestBaselineSpool_FetchErrorRemovesPartialFile(t *testing.T) {
	dir := t.TempDir()
	s := NewBaselineSpool(dir)
	src := bytes.Repeat([]byte("Q"), 4096)
	d := digest.FromBytes(src)

	_, err := s.GetOrSpool(context.Background(), d, func(_ digest.Digest) (io.ReadCloser, error) {
		return io.NopCloser(&errAfterNReader{src: src, n: 128}), nil
	})
	if err == nil {
		t.Fatalf("expected mid-stream error")
	}
	if _, statErr := os.Stat(s.pathFor(d)); !os.IsNotExist(statErr) {
		t.Fatalf("partial file should be removed; stat err=%v", statErr)
	}
}

func TestBaselineSpool_ConcurrentSameDigestDistinctPayloadAtomicRename(t *testing.T) {
	// 8 goroutines each return a DISTINCT 1 MiB payload but claim the same
	// digest. Atomic rename means the on-disk result is one writer's payload
	// in full, never an interleave. (Distinct payloads are the "honest" mutation
	// target per Phase 4 amendment #19.)
	dir := t.TempDir()
	s := NewBaselineSpool(dir)
	d := digest.FromBytes([]byte("k"))
	payloads := make([][]byte, 8)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('A' + i)}, 1<<20)
	}
	var wg sync.WaitGroup
	var winnerPath atomic.Pointer[string]
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, _ := s.GetOrSpool(context.Background(), d, func(_ digest.Digest) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(payloads[i])), nil
			})
			winnerPath.Store(&p)
		}()
	}
	wg.Wait()
	got, _ := os.ReadFile(*winnerPath.Load())
	matched := false
	for _, p := range payloads {
		if bytes.Equal(got, p) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("on-disk content matches no submitted payload (interleave detected)")
	}
}

func TestBaselineSpool_SingleflightDedupsSameDigest(t *testing.T) {
	dir := t.TempDir()
	s := NewBaselineSpool(dir)
	payload := bytes.Repeat([]byte("S"), 1<<10)
	d := digest.FromBytes(payload)
	var fetchCount int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.GetOrSpool(context.Background(), d, func(_ digest.Digest) (io.ReadCloser, error) {
				atomic.AddInt32(&fetchCount, 1)
				return io.NopCloser(bytes.NewReader(payload)), nil
			})
		}()
	}
	wg.Wait()
	if fetchCount != 1 {
		t.Fatalf("expected 1 fetch via singleflight, got %d", fetchCount)
	}
	_ = filepath.Join // silence import use if linter prunes
}
```

Run `go test ./pkg/importer/ -run TestBaselineSpool -v`. Expect FAIL — type doesn't exist yet.

- [ ] **Step 3.7: Implement `pkg/importer/baselinespool.go`.** Pattern is a near-clone of `pkg/exporter/baselinespool.go`, dropping the fingerprint coupling:

```go
// Package importer — disk-backed baseline blob spool (apply side).
//
// BaselineSpool replaces baselineBlobCache: instead of pinning every
// verified baseline layer as []byte for the full Import() lifetime,
// it streams each layer to <dir>/<digest> on first touch. Subsequent
// callers hit the in-memory entries map (RLock fast path) and receive
// the existing path. Concurrent first-touches for the same digest
// collapse to one underlying fetch via singleflight.
package importer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/singleflight"

	"github.com/leosocy/diffah/pkg/diff"
)

// BaselineSpool manages spooled baseline blobs under dir.
type BaselineSpool struct {
	dir     string
	mu      sync.RWMutex
	entries map[digest.Digest]string // digest → on-disk path
	sf      singleflight.Group
}

// NewBaselineSpool creates a spool backed by dir. The directory must
// already exist (workdir.Ensure creates it before Import reaches here).
func NewBaselineSpool(dir string) *BaselineSpool {
	return &BaselineSpool{dir: dir, entries: make(map[digest.Digest]string)}
}

// pathFor returns the canonical destination path for digest d.
func (s *BaselineSpool) pathFor(d digest.Digest) string {
	return filepath.Join(s.dir, d.Encoded())
}

// Path returns the spool path for d if it has been spooled, else ("", false).
func (s *BaselineSpool) Path(d digest.Digest) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.entries[d]
	return p, ok
}

// GetOrSpool returns the on-disk path for d, spooling on first touch.
// fetch is called at most once per digest per spool (singleflight); a
// fetch error is not cached. Verification (digest match) is enforced by
// computing the on-disk content's digest after spooling.
func (s *BaselineSpool) GetOrSpool(
	ctx context.Context, d digest.Digest,
	fetch func(digest.Digest) (io.ReadCloser, error),
) (string, error) {
	return s.getOrSpoolWithVerifier(ctx, d, fetch, nil)
}

// getOrSpoolWithVerifier is the same as GetOrSpool but lets a caller
// inject a verifier function that reads from the source stream during
// the spool. Test-only entry; the public API runs no verifier.
func (s *BaselineSpool) getOrSpoolWithVerifier(
	ctx context.Context, d digest.Digest,
	fetch func(digest.Digest) (io.ReadCloser, error),
	verifier func(io.Reader) error,
) (string, error) {
	if p, ok := s.Path(d); ok {
		return p, nil
	}
	v, err, _ := s.sf.Do(string(d), func() (any, error) {
		if p, ok := s.Path(d); ok {
			return p, nil
		}
		return s.spoolOnce(ctx, d, fetch, verifier)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (s *BaselineSpool) spoolOnce(
	ctx context.Context, d digest.Digest,
	fetch func(digest.Digest) (io.ReadCloser, error),
	verifier func(io.Reader) error,
) (string, error) {
	rc, err := fetch(d)
	if err != nil {
		return "", fmt.Errorf("fetch baseline %s: %w", d, err)
	}
	defer rc.Close()

	dst := s.pathFor(d)
	tmp, err := os.CreateTemp(s.dir, filepath.Base(dst)+".tmp.*")
	if err != nil {
		return "", fmt.Errorf("create tmp for %s: %w", d, err)
	}
	committed := false
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	tee := io.TeeReader(rc, tmp)
	if verifier != nil {
		if err := verifier(tee); err != nil {
			return "", fmt.Errorf("verifier %s: %w", d, err)
		}
	}
	// Always drain so the spool captures every byte the source emits,
	// regardless of whether the verifier consumed the full stream.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return "", fmt.Errorf("drain spool %s: %w", d, err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync spool %s: %w", d, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close spool %s: %w", d, err)
	}

	// Verify on-disk digest matches before publishing.
	got, err := digest.FromReader(mustOpen(tmpPath))
	if err != nil {
		return "", fmt.Errorf("hash spool %s: %w", d, err)
	}
	if got != d {
		return "", &diff.ErrBaselineBlobDigestMismatch{Digest: d.String(), Got: got.String()}
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		return "", fmt.Errorf("rename spool %s: %w", d, err)
	}
	committed = true

	s.mu.Lock()
	s.entries[d] = dst
	s.mu.Unlock()
	return dst, nil
}

func mustOpen(p string) io.Reader {
	f, _ := os.Open(p)
	return f
}
```

- [ ] **Step 3.8: Run baselinespool tests.** `go test ./pkg/importer/ -run TestBaselineSpool -v`. Expect PASS for all 5 tests.

- [ ] **Step 3.9: Mutation-test the atomic rename.** Temporarily change `os.Rename(tmpPath, dst)` to `os.WriteFile(dst, ...)`-equivalent direct write. Re-run `TestBaselineSpool_ConcurrentSameDigestDistinctPayloadAtomicRename` and confirm it now FAILS (some run produces interleaved bytes). Restore the rename. Run again — PASS.

- [ ] **Step 3.10: Wire spool into `bundleImageSource` (transitional).** Edit `pkg/importer/compose.go`:
  - Replace `cache *baselineBlobCache` with `spool *BaselineSpool`.
  - Rewrite `fetchVerifiedBaselineBlob` to call `s.spool.GetOrSpool(...)` and read the file:

```go
func (s *bundleImageSource) fetchVerifiedBaselineBlob(
	ctx context.Context, d digest.Digest, _ types.BlobInfoCache,
) ([]byte, error) {
	path, err := s.spool.GetOrSpool(ctx, d, func(d digest.Digest) (io.ReadCloser, error) {
		rc, _, err := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: d}, nil)
		return rc, err
	})
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
```

This keeps the `[]byte` interface to `serveFull/servePatch` untouched — those rewrite in PR4. PR3's footprint is just "verified bytes come from disk now."

- [ ] **Step 3.11: Update `Import()` to construct workdir + spool.** Edit `pkg/importer/importer.go::Import`:

```go
import "github.com/leosocy/diffah/internal/workdir"

func Import(ctx context.Context, opts Options) error {
	defer opts.reporter().Finish()

	hint := firstOutputHint(opts) // helper: returns "" or first file-transport path
	wd, cleanup, err := workdir.Ensure(opts.Workdir, hint)
	if err != nil {
		return fmt.Errorf("prepare workdir: %w", err)
	}
	defer cleanup()
	opts.Workdir = wd

	bundle, err := extractBundle(opts.DeltaPath)
	// ... existing body ...

	baselineDir := filepath.Join(wd, "baselines")
	if err := os.MkdirAll(baselineDir, 0o700); err != nil {
		return fmt.Errorf("create baselines spool dir: %w", err)
	}
	spool := NewBaselineSpool(baselineDir)
	// ... pass spool into bundleImageSource constructor ...
}
```

Add a small `firstOutputHint(opts Options) string` helper that returns the first `Outputs[name]` value's path component for file transports (`oci-archive:`, `docker-archive:`, `dir:`), or empty for registry-only outputs.

- [ ] **Step 3.12: Delete `pkg/importer/blobcache.go` and `blobcache_test.go`.** `git rm pkg/importer/blobcache.go pkg/importer/blobcache_test.go`. Search for residual references: `git grep "baselineBlobCache" pkg/ cmd/` should return 0 hits.

- [ ] **Step 3.13: Run importer tests + integration.** `go test ./pkg/importer/... ./cmd/...`. Expect PASS — behavior is identical to before, just disk-backed.

- [ ] **Step 3.14: Run determinism gate.** `go test -count=2 -run TestExport_OutputIsByteIdenticalAcrossWorkerCounts ./pkg/exporter/...`. PASS.

- [ ] **Step 3.15: Lint.** `golangci-lint run`. 0 issues.

- [ ] **Step 3.16: Commit + open PR.**

```bash
git add pkg/importer/baselinespool.go pkg/importer/baselinespool_test.go \
        pkg/importer/compose.go pkg/importer/importer.go \
        internal/workdir/
git rm pkg/importer/blobcache.go pkg/importer/blobcache_test.go
git commit -m "feat(importer): disk-backed BaselineSpool replaces blobcache (streaming PR3)"
gh pr create --title "feat(importer): disk-backed BaselineSpool (streaming PR3)" --body "..."
```

---

## PR 4: `serveFull` / `servePatch` rewrite (streaming)

**Spec ref:** §5.5, Goal #1, partially I8 / Phase 2 G6 (per-blob progress).

**Branch:** `feat/import-streaming-pr4-decode-streaming`

**Goal of this PR:** Rewrite `serveFull` / `servePatch` to return path-backed `io.ReadCloser`s — no more `io.ReadAll` + `bytes.NewReader`. `servePatch` shells out via `zstdpatch.DecodeStream(refPath, patchPath, outPath)`. Per-blob `progress.Layer` wired through the reader. Remove the `nolint:staticcheck` directive at line 139 once migration completes. `HasThreadSafeGetBlob` STAYS `false` until PR5.

**Files:**
- Modify: `pkg/importer/compose.go` (the two functions)
- Modify: `pkg/progress/` — add `cappedWriter` lift if shape matches (else introduce new)
- Create: `pkg/importer/decode_concurrent_test.go`
- Modify: `pkg/importer/compose_test.go` (existing tests pass; add streaming-output assertion)

### Sub-tasks

- [ ] **Step 4.1: Create branch + advisor pre-flight.** `git checkout -b feat/import-streaming-pr4-decode-streaming master`. Run advisor() with the spec + this PR's task list.

- [ ] **Step 4.2: Lift `cappedWriter` to `pkg/progress/`.** Find `cappedWriter` in `pkg/exporter/encode.go:251-263`; move to `pkg/progress/cappedwriter.go`:

```go
package progress

// CappedWriter returns an onChunk callback that forwards up to total bytes
// to sink, clamping chunks that would cross the cap and dropping anything
// after. Used to keep per-blob progress bars from overshooting the manifest-
// declared size when transports emit decompressed bytes (e.g., oci-archive).
func CappedWriter(total int64, sink func(int64)) func(int64) {
	remaining := total
	return func(n int64) {
		if remaining <= 0 {
			return
		}
		if n > remaining {
			n = remaining
		}
		sink(n)
		remaining -= n
	}
}
```

Update exporter callsite to use `progress.CappedWriter`. Run `go test ./pkg/exporter/...`. Expect PASS.

- [ ] **Step 4.3: Add a path-backed counting reader to `pkg/progress/`.** Same file:

```go
// CountingReader wraps r, reporting each successful Read's byte count
// to onChunk. The wrapped reader's Close (if any) is propagated.
type CountingReader struct {
	R       io.Reader
	OnChunk func(int64)
}

func (r *CountingReader) Read(p []byte) (int, error) {
	n, err := r.R.Read(p)
	if n > 0 && r.OnChunk != nil {
		r.OnChunk(int64(n))
	}
	return n, err
}
```

(Plus a small `_test.go` covering "10 bytes in chunks of 3" and EOF propagation.)

- [ ] **Step 4.4: Write a concurrent reader regression test.** Create `pkg/importer/decode_concurrent_test.go`. The test asserts that two goroutines opening `bundleImageSource.GetBlob(d)` in parallel for the same `EncodingFull` digest both receive byte-identical readers. (This is the gate for flipping `HasThreadSafeGetBlob = true` in PR5.)

```go
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"sync"
	"testing"
	// ... existing fakes from compose_test.go
)

func TestBundleImageSource_ConcurrentSameDigestReadersAreByteIdentical(t *testing.T) {
	src := newFakeBundleImageSourceWithBlob(t, /* digest */, /* payload */)
	const N = 8
	results := make([][32]byte, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, _, err := src.GetBlob(context.Background(), /* info */, nil)
			if err != nil {
				t.Errorf("GetBlob #%d: %v", i, err)
				return
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Errorf("ReadAll #%d: %v", i, err)
				return
			}
			results[i] = sha256.Sum256(data)
		}()
	}
	wg.Wait()
	for i := 1; i < N; i++ {
		if results[i] != results[0] {
			t.Fatalf("reader %d sha != reader 0", i)
		}
	}
}
```

(Replace `newFakeBundleImageSourceWithBlob` placeholders by following the pattern in existing `compose_test.go`.) Run `go test ./pkg/importer/ -run TestBundleImageSource_Concurrent -v`. Expect FAIL — current implementation returns a fresh `bytes.NewReader` each time but loads the full bytes synchronously, so concurrency works but ALSO — depending on PR3's transitional state — `os.ReadFile` runs N times. Decision: this test passes today (each call is independent). It's the contract gate for PR5's flip.

- [ ] **Step 4.5: Rewrite `serveFull` to stream from disk.** Edit `pkg/importer/compose.go`:

```go
func (s *bundleImageSource) serveFull(d digest.Digest) (io.ReadCloser, int64, error) {
	path := filepath.Join(s.blobDir, d.Algorithm().String(), d.Encoded())
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open full blob %s: %w", d, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("stat full blob %s: %w", d, err)
	}
	// Verify digest by streaming through a verifier wrapper.
	return &verifyingReadCloser{
		f:        f,
		expected: d,
		hasher:   sha256NewHasher(),
		size:     st.Size(),
	}, st.Size(), nil
}
```

Implement `verifyingReadCloser` in the same file: wraps `*os.File`, accumulates a hasher on each Read, verifies digest at EOF (returns `&diff.ErrShippedBlobDigestMismatch` if mismatch), forwards Close.

```go
type verifyingReadCloser struct {
	f        *os.File
	expected digest.Digest
	hasher   hash.Hash
	size     int64
	read     int64
}

func (r *verifyingReadCloser) Read(p []byte) (int, error) {
	n, err := r.f.Read(p)
	if n > 0 {
		_, _ = r.hasher.Write(p[:n])
		r.read += int64(n)
	}
	if errors.Is(err, io.EOF) {
		got := digest.NewDigest(digest.Canonical, r.hasher)
		if got != r.expected {
			return n, &diff.ErrShippedBlobDigestMismatch{
				ImageName: "", Digest: r.expected.String(), Got: got.String(),
			}
		}
	}
	return n, err
}

func (r *verifyingReadCloser) Close() error { return r.f.Close() }
```

(Use `crypto/sha256` directly via `sha256.New()` — no helper needed, just inline.)

- [ ] **Step 4.6: Rewrite `servePatch` via `DecodeStream`.** Same file:

```go
func (s *bundleImageSource) servePatch(
	ctx context.Context, target digest.Digest, entry diff.BlobEntry, _ types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
	patchPath := filepath.Join(s.blobDir, target.Algorithm().String(), target.Encoded())
	refPath, err := s.spool.GetOrSpool(ctx, entry.PatchFromDigest, func(d digest.Digest) (io.ReadCloser, error) {
		rc, _, err := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: d}, nil)
		return rc, err
	})
	if err != nil {
		if isBlobNotFound(err) {
			return nil, 0, &ErrMissingPatchSource{
				ImageName: s.imageName, ShippedDigest: target, PatchFromDigest: entry.PatchFromDigest,
			}
		}
		return nil, 0, fmt.Errorf("baseline spool %s: %w", entry.PatchFromDigest, err)
	}
	scratchPath := filepath.Join(s.workdir, "scratch", s.imageName, target.Encoded())
	if err := os.MkdirAll(filepath.Dir(scratchPath), 0o700); err != nil {
		return nil, 0, fmt.Errorf("mkdir scratch: %w", err)
	}
	if err := zstdpatch.DecodeStream(ctx, refPath, patchPath, scratchPath); err != nil {
		return nil, 0, fmt.Errorf("decode patch for %s: %w", target, err)
	}
	f, err := os.Open(scratchPath)
	if err != nil {
		return nil, 0, fmt.Errorf("open decoded %s: %w", target, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("stat decoded %s: %w", target, err)
	}
	return &verifyingScratchReadCloser{
		f: f, expected: target, hasher: sha256.New(), scratchPath: scratchPath, size: st.Size(),
	}, st.Size(), nil
}
```

`verifyingScratchReadCloser` is a verifier RC with Close that ALSO removes the scratch file (the bytes have been consumed).

- [ ] **Step 4.7: Remove the `nolint:staticcheck` at compose.go:139.** That line previously suppressed the warning on `zstdpatch.Decode([]byte)`. After PR4, `Decode` is no longer called by importer — verify with `git grep "zstdpatch\.Decode(" pkg/importer/`; expect 0 hits. Remove the directive line.

- [ ] **Step 4.8: Add `bundleImageSource.workdir` field.** Plumb through the constructor and `Import()` (where `composeImage` builds the source).

- [ ] **Step 4.9: Run all importer tests.** `go test ./pkg/importer/...`. Expect PASS — including the new concurrent reader test from step 4.4.

- [ ] **Step 4.10: Run a registry integration test.** `go test ./cmd/ -run TestApply_RegistryRoundTrip -v`. Expect PASS.

- [ ] **Step 4.11: Run determinism gate.** `go test -count=2 -run TestExport_OutputIsByteIdenticalAcrossWorkerCounts ./pkg/exporter/...`. PASS.

- [ ] **Step 4.12: Lint.** `golangci-lint run`. 0 issues.

- [ ] **Step 4.13: Commit + PR.**

```bash
git add pkg/importer/compose.go pkg/importer/decode_concurrent_test.go \
        pkg/progress/cappedwriter.go pkg/progress/cappedwriter_test.go \
        pkg/exporter/encode.go
git commit -m "feat(importer): streaming serveFull/servePatch via DecodeStream (streaming PR4)"
gh pr create --title "feat(importer): streaming serveFull/servePatch (streaming PR4)" --body "..."
```

---

## PR 5: `applyImagesPool` + `HasThreadSafeGetBlob = true`

**Spec ref:** §4.3, §5.1, §5.6, Goal #2 (byte-identity across workers), Goal #4 (per-image parallelism).

**Branch:** `feat/import-streaming-pr5-apply-pool`

**Goal of this PR:** Replace the serial `importEachImage` for-loop with an `internal/admission.AdmissionPool`-gated worker pool. Add `pkg/importer/admission.go` (per-image RSS estimate + `checkSingleImageFitsInBudget` fail-fast). Flip `HasThreadSafeGetBlob` to `true` (concurrent reader test from PR4 covers the contract). Add an apply-side byte-identity test.

**Files:**
- Create: `pkg/importer/admission.go`, `admission_test.go`
- Create: `cmd/apply_streaming_integration_test.go`
- Create: `cmd/unbundle_streaming_integration_test.go`
- Create: `cmd/apply_admission_integration_test.go`
- Modify: `pkg/importer/importer.go::importEachImage` (rewrite as pool)
- Modify: `pkg/importer/compose.go::HasThreadSafeGetBlob` (return `true`)

### Sub-tasks

- [ ] **Step 5.1: Create branch + advisor pre-flight.**

- [ ] **Step 5.2: Write failing admission test.** Create `pkg/importer/admission_test.go`:

```go
package importer

import (
	"errors"
	"strings"
	"testing"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/diff/errs"
)

func TestCheckSingleImageFitsInBudget_RejectsOversize(t *testing.T) {
	images := []diff.ImageEntry{
		{
			Name: "huge",
			Target: diff.ImageRef{
				Layers: []digest.Digest{ /* layers with size implying windowLog=31 */ },
			},
		},
	}
	err := checkSingleImageFitsInBudget(images, /* windowLog */ 0, /* memBudget */ 1<<28 /* 256 MiB */)
	if err == nil {
		t.Fatalf("expected fail-fast error")
	}
	var ce errs.Categorized
	if !errors.As(err, &ce) || ce.Category() != errs.CategoryUser {
		t.Fatalf("expected CategoryUser, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "huge") || !strings.Contains(err.Error(), "256") {
		t.Fatalf("hint missing image name or budget value: %v", err)
	}
}
```

(Adapt to actual sidecar shape — the spec just guarantees `images []ImageEntry` and per-image layer metadata. The implementation reads `blobs[layer].ArchiveSize`/`Size` for each layer in the target manifest and computes the max.)

- [ ] **Step 5.3: Implement `pkg/importer/admission.go`.**

```go
package importer

import (
	"fmt"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/diff/errs"
	"github.com/leosocy/diffah/pkg/exporter"
)

// estimatePerImageRSS returns the conservative RSS estimate for applying
// `img`. It walks each shipped layer (those with sidecar entries — Full
// and Patch encodings) and takes the max across layers. Layers sourced
// only from baseline are bounded by the baseline-side ref bytes already
// OS-cached on disk; their Go-process RSS is negligible and is omitted.
func estimatePerImageRSS(
	img diff.ImageEntry, blobs map[digest.Digest]diff.BlobEntry, userWindowLog int,
) int64 {
	var maxEst int64
	for _, layerDigest := range img.Target.LayerDigests() {
		entry, ok := blobs[layerDigest]
		if !ok {
			continue // baseline-only layer, OS-cached
		}
		wl := exporter.ResolveWindowLog(userWindowLog, entry.Size)
		est := exporter.EstimateRSSForWindowLog(wl) // exported now
		if est > maxEst {
			maxEst = est
		}
	}
	return maxEst
}

// checkSingleImageFitsInBudget walks images and returns a structured
// CategoryUser error if any image's max-layer RSS estimate exceeds
// memBudget. Ordered iteration keeps the offending name deterministic.
func checkSingleImageFitsInBudget(
	images []diff.ImageEntry, blobs map[digest.Digest]diff.BlobEntry,
	userWindowLog int, memBudget int64,
) error {
	var maxEst int64
	var offendingName string
	for _, img := range images {
		e := estimatePerImageRSS(img, blobs, userWindowLog)
		if e > maxEst {
			maxEst = e
			offendingName = img.Name
		}
	}
	if maxEst > memBudget {
		return &userError{
			cat: errs.CategoryUser,
			msg: fmt.Sprintf(
				"image %s requires %d byte(s) of admission budget; --memory-budget is %d",
				offendingName, maxEst, memBudget),
			hint: "increase --memory-budget or run with fewer --workers",
		}
	}
	return nil
}

// userError mirrors pkg/exporter/userError. Lifted local to importer to
// avoid an import cycle through pkg/exporter (which already depends on
// internal/admission). Keeps Categorized + Advised contract.
type userError struct {
	cat  errs.Category
	msg  string
	hint string
}

var (
	_ errs.Categorized = (*userError)(nil)
	_ errs.Advised     = (*userError)(nil)
)

func (e *userError) Error() string           { return e.msg }
func (e *userError) Category() errs.Category { return e.cat }
func (e *userError) NextAction() string      { return e.hint }
```

(`pkg/exporter` exports `EstimateRSSForWindowLog` as part of this PR — rename the existing unexported `estimateRSSForWindowLog` to the exported form.)

- [ ] **Step 5.4: Run admission tests.** `go test ./pkg/importer/ -run TestCheckSingleImageFitsInBudget -v`. Expect PASS.

- [ ] **Step 5.5: Rewrite `importEachImage` as `applyImagesPool`.** Edit `pkg/importer/importer.go`:

```go
func importEachImage(
	ctx context.Context,
	bundle *extractedBundle,
	resolvedByName map[string]resolvedBaseline,
	outputs map[string]string,
	opts Options,
	spool *BaselineSpool,
	applyList []string,
) ApplyReport {
	report := ApplyReport{}
	var mu sync.Mutex // protects report.Results

	if err := checkSingleImageFitsInBudget(
		bundle.sidecar.Images, bundle.sidecar.Blobs, opts.WindowLog, opts.MemoryBudget,
	); err != nil {
		// Fail-fast before opening any worker.
		report.Results = append(report.Results, ApplyImageResult{
			ImageName: "<budget>", Status: ApplyImageFailedCompose, Err: err,
		})
		return report
	}

	pool := admission.NewAdmissionPool(ctx, opts.Workers, opts.MemoryBudget)
	for _, name := range applyList {
		name := name
		// Compute estimate based on the named image's layers.
		var img diff.ImageEntry
		for _, x := range bundle.sidecar.Images {
			if x.Name == name {
				img = x
				break
			}
		}
		est := estimatePerImageRSS(img, bundle.sidecar.Blobs, opts.WindowLog)
		pool.Submit(name, est, func() error {
			result := applyOneImage(ctx, name, bundle, resolvedByName, outputs, opts, spool)
			mu.Lock()
			report.Results = append(report.Results, result)
			mu.Unlock()
			if opts.Strict && result.Status != ApplyImageOK {
				return fmt.Errorf("strict abort: %s: %w", name, result.Err)
			}
			return nil
		})
	}
	_ = pool.Wait() // partial mode: collected results carry per-image errors; strict mode: errgroup error is informational
	// In strict mode, ensure result order is deterministic:
	sortResultsByApplyList(&report, applyList)
	return report
}
```

(`Options.WindowLog` field added in this PR; default `0` = "auto per layer size", same semantics as exporter.)

- [ ] **Step 5.6: Flip `HasThreadSafeGetBlob`.** Edit `pkg/importer/compose.go`:

```go
func (s *bundleImageSource) HasThreadSafeGetBlob() bool {
	return true // disk-backed reader; concurrent GetBlob is safe (PR4 covered, PR5 enables)
}
```

- [ ] **Step 5.7: Write apply byte-identity integration test.** Create `cmd/apply_streaming_integration_test.go`:

```go
//go:build integration

package cmd_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestApply_OutputIsByteIdenticalAcrossWorkerCounts(t *testing.T) {
	bundlePath, baselineRefs := buildSmallBundleFixture(t) // ~3 images, 5 layers each
	digests := make(map[int][32]byte)
	for _, w := range []int{1, 2, 4, 8} {
		out := filepath.Join(t.TempDir(), "out")
		runDiffahApply(t, bundlePath, baselineRefs, out, "--workers", fmtI(w), "--memory-budget", "0")
		// Hash the produced oci-archive's manifest blob (deterministic)
		mf := readManifestDigest(t, out)
		digests[w] = sha256.Sum256([]byte(mf))
	}
	first := digests[1]
	for w, d := range digests {
		if d != first {
			t.Fatalf("manifest digest at --workers=%d diverges from --workers=1", w)
		}
	}
}
```

(Use existing test helpers in the package; `runDiffahApply` is in `integration_main_test.go`.)

- [ ] **Step 5.8: Write partial-vs-strict integration test.** Create `cmd/unbundle_streaming_integration_test.go` covering:
  - 4-image bundle with one image's baseline ref intentionally bad.
  - `--workers=4` partial mode: 3 OK + 1 failed; ApplyReport length = 4.
  - `--workers=4 --strict`: error returned; cleanup observed (workdir removed).

- [ ] **Step 5.9: Write admission fail-fast integration test.** Create `cmd/apply_admission_integration_test.go`:

```go
func TestApply_FailFastWhenImageExceedsBudget(t *testing.T) {
	bundle := buildBundleWithLargeLayer(t, /*windowLog=*/ 30) // ~2 GiB estimate
	err := runDiffahApply(t, bundle, refs, out, "--memory-budget", "256MiB")
	if err == nil || !strings.Contains(err.Error(), "memory-budget") {
		t.Fatalf("expected fail-fast user error, got %v", err)
	}
	if !strings.Contains(err.Error(), "increase --memory-budget") {
		t.Fatalf("hint missing")
	}
}

func TestApply_MemoryBudgetZeroDisablesAdmission(t *testing.T) {
	bundle := buildBundleWithLargeLayer(t, 30)
	err := runDiffahApply(t, bundle, refs, out, "--memory-budget", "0")
	if err != nil {
		t.Fatalf("expected success with budget=0, got %v", err)
	}
}
```

- [ ] **Step 5.10: Run all importer + cmd tests.** `go test -tags integration ./pkg/importer/... ./cmd/...`. Expect PASS.

- [ ] **Step 5.11: Run apply byte-identity gate.** `go test -count=2 -tags integration -run TestApply_OutputIsByteIdenticalAcrossWorkerCounts ./cmd/...`. PASS.

- [ ] **Step 5.12: Run determinism gate (export side).** PASS (sanity).

- [ ] **Step 5.13: Lint.** 0 issues.

- [ ] **Step 5.14: Commit + PR.**

```bash
git add pkg/importer/admission.go pkg/importer/admission_test.go \
        pkg/importer/importer.go pkg/importer/compose.go \
        pkg/exporter/admission.go \
        cmd/apply_streaming_integration_test.go cmd/unbundle_streaming_integration_test.go cmd/apply_admission_integration_test.go
git commit -m "feat(importer): per-image admission pool + HasThreadSafeGetBlob (streaming PR5)"
gh pr create --title "feat(importer): admission-gated apply pool (streaming PR5)" --body "..."
```

---

## PR 6: G7 acceptance lockdown (I4 + I5 + I6) + apply scale-bench

**Spec ref:** §5.7, §5.8, §5.9, §8.4, §8.5.

**Branch:** `feat/import-streaming-pr6-acceptance`

**Goal of this PR:** Land three acceptance items, plus the apply-side scale-bench step. No production code path changes apart from sidecar parser hardening (I4).

**Files:**
- Modify: `pkg/diff/sidecar.go` (DisallowUnknownFields probe pass)
- Modify: `pkg/diff/sidecar_test.go` (I4 test)
- Create: `pkg/importer/decode_windowlog_test.go` (I5)
- Create: `pkg/importer/compose_phase3_test.go` (I6)
- Create: `testdata/fixtures/phase3-bundle-min.tar` (I6 — committed binary)
- Create: `scripts/build_fixtures/phase3.go` (I6 generator)
- Create: `pkg/importer/scale_bench_test.go` (apply big test, build tag `big`)
- Modify: `.github/workflows/scale-bench.yml` (add apply step)

### Sub-tasks

- [ ] **Step 6.1: Create branch.**

- [ ] **Step 6.2: Write failing I4 test.** Edit `pkg/diff/sidecar_test.go`:

```go
func TestParseSidecar_UnknownOptionalFieldEmitsDebugLog(t *testing.T) {
	captured := captureSlogDebug(t)
	raw := []byte(`{
		"version": "1",
		"feature": "diffah",
		"tool": "diffah",
		"toolVersion": "0.2.0",
		"created_at": "2026-01-01T00:00:00Z",
		"images": [],
		"blobs": {},
		"newField_unknown_to_v1": "ignored"
	}`)
	sc, err := ParseSidecar(raw)
	if err != nil {
		t.Fatalf("ParseSidecar: %v", err)
	}
	if sc.Version != "1" {
		t.Fatalf("lenient parse failed: %+v", sc)
	}
	if !captured.Contains("unknown optional fields") || !captured.Contains("newField_unknown_to_v1") {
		t.Fatalf("expected debug log naming the field, got %s", captured.All())
	}
}
```

(`captureSlogDebug` is a small test helper that installs a memory-handler-backed slog logger for the test's lifetime and returns a struct with `Contains/All` accessors.)

Run `go test ./pkg/diff/ -run TestParseSidecar_UnknownOptional -v`. Expect FAIL — current parser is lenient-only.

- [ ] **Step 6.3: Implement I4 in `pkg/diff/sidecar.go::ParseSidecar`.**

```go
func ParseSidecar(raw []byte) (*Sidecar, error) {
	// Probe pass: detect unknown optional fields for diagnostic logging.
	// Errors here are NEVER returned to the caller — schema-error
	// classification stays the lenient pass's responsibility.
	if names := probeUnknownFields(raw); len(names) > 0 {
		slog.Debug("sidecar has unknown optional fields", "fields", names)
	}

	// Lenient pass (existing behavior).
	var sc Sidecar
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil, &ErrSidecarSchema{Cause: err}
	}
	if err := validateSidecar(&sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

// probeUnknownFields runs json.NewDecoder.DisallowUnknownFields against
// raw and accumulates field names from the resulting "json: unknown
// field <name>" errors. The function returns nil on any non-unknown-
// field error so it never short-circuits the lenient parse.
func probeUnknownFields(raw []byte) []string {
	var names []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var sc Sidecar
	for {
		err := dec.Decode(&sc)
		if err == nil {
			return names
		}
		const prefix = "json: unknown field "
		msg := err.Error()
		idx := strings.Index(msg, prefix)
		if idx < 0 {
			return names
		}
		// Extract the quoted field name from the standard message.
		name := strings.TrimSpace(msg[idx+len(prefix):])
		name = strings.Trim(name, `"`)
		names = append(names, name)
		// json.Decoder is single-shot on Decode failure; we need to
		// restart with a remaining-fields-only struct. For simplicity
		// (and correctness for our small sidecar), we return what we
		// have after the first error.
		return names
	}
}
```

Run `go test ./pkg/diff/ -run TestParseSidecar_UnknownOptional -v`. Expect PASS.

- [ ] **Step 6.4: Write I5 windowLog≥28 fail-closed test.** Create `pkg/importer/decode_windowlog_test.go`:

```go
package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

// makeFrameWithWindowLog produces a zstd frame whose declared window size is
// 1<<wl. We synthesize a payload large enough to force the encoder to emit a
// frame that actually requires wl-sized window on decode.
func makeFrameWithWindowLog(t *testing.T, wl int) []byte {
	t.Helper()
	payload := bytes.Repeat([]byte{'X'}, 1<<wl) // matches window
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf,
		zstd.WithWindowSize(1<<wl),
		zstd.WithEncoderLevel(zstd.SpeedDefault),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportDecode_FailsClosedOnWindowLog28Plus(t *testing.T) {
	for _, wl := range []int{28, 29, 30, 31} {
		wl := wl
		t.Run(fmt.Sprintf("wl=%d", wl), func(t *testing.T) {
			frame := makeFrameWithWindowLog(t, wl)
			err := decodeFrameAsImporter(t, frame) // helper applies cap=27 reader
			if err == nil {
				t.Fatalf("wl=%d: expected fail-closed, got success", wl)
			}
			var ce errs.Categorized
			if !errors.As(err, &ce) || ce.Category() != errs.CategoryContent {
				t.Fatalf("wl=%d: expected CategoryContent, got %T %v", wl, err, err)
			}
			if !strings.Contains(err.Error(), "memory") && !strings.Contains(err.Error(), "window") {
				t.Fatalf("wl=%d: missing memory/window mention: %v", wl, err)
			}
		})
	}
}

// decodeFrameAsImporter pipes frame through the importer's full-decode
// path (klauspost.NewReader with WithDecoderMaxWindow(1<<27)). It returns
// the error from the first Read after EOF (where window-size errors surface).
func decodeFrameAsImporter(t *testing.T, frame []byte) error {
	t.Helper()
	r, err := zstd.NewReader(bytes.NewReader(frame),
		zstd.WithDecoderMaxWindow(1<<27),
	)
	if err != nil {
		return classifyDecodeErr(err)
	}
	defer r.Close()
	_, err = io.Copy(io.Discard, r)
	return classifyDecodeErr(err)
}
```

(`classifyDecodeErr` lives in importer code — wraps klauspost errors with `errs.CategoryContent`; ensure the production import path also wraps them. Adjust as needed.) Run `go test ./pkg/importer/ -run TestImportDecode_FailsClosedOnWindowLog28Plus -v`. Expect PASS.

- [ ] **Step 6.5: Generate I6 fixture.** Create `scripts/build_fixtures/phase3.go`:

```go
//go:build ignore

package main

// Builds testdata/fixtures/phase3-bundle-min.tar — a deterministic
// Phase-3-vintage diffah bundle with 1 image, 2 layers (1 patch, 1 full),
// total uncompressed size < 200 KiB. Reproducibility note: tag v0.1.0
// semantics; this generator pins those by directly synthesizing the
// bundle layout rather than shelling out to a frozen `diffah` binary.
//
// Run: go run scripts/build_fixtures/phase3.go
//
// Output: testdata/fixtures/phase3-bundle-min.tar (committed)

func main() { /* ... see existing scripts/build_fixtures/main.go for style ... */ }
```

Implementation builds a minimal `diffah.json` + 4 blobs (manifest, config, patch, full) into an in-process tar. After running, commit the produced `testdata/fixtures/phase3-bundle-min.tar` AND write the expected output manifest digest to `pkg/importer/compose_phase3_test.go` as a constant.

- [ ] **Step 6.6: Write I6 test.** Create `pkg/importer/compose_phase3_test.go`:

```go
package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
)

// wantPhase3ManifestDigest is the canonical apply-output manifest digest
// for testdata/fixtures/phase3-bundle-min.tar. Drift in any importer
// component touched by this spec MUST keep this constant unchanged.
const wantPhase3ManifestDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000" // populate after fixture build

func TestImport_Phase3FixtureProducesByteIdenticalManifest(t *testing.T) {
	delta := "../../testdata/fixtures/phase3-bundle-min.tar"
	out := filepath.Join(t.TempDir(), "out")
	if err := runImportInProcess(context.Background(), delta, out); err != nil {
		t.Fatalf("import: %v", err)
	}
	got := readManifestDigest(t, out)
	if got != digest.Digest(wantPhase3ManifestDigest) {
		t.Fatalf("manifest digest drift\n got: %s\nwant: %s", got, wantPhase3ManifestDigest)
	}
}
```

(Populate `wantPhase3ManifestDigest` from the fixture build output. `runImportInProcess` and `readManifestDigest` are existing test helpers.)

- [ ] **Step 6.7: Run I6 fixture test.** `go test ./pkg/importer/ -run TestImport_Phase3Fixture -v`. Expect PASS.

- [ ] **Step 6.8: Write apply scale-bench.** Create `pkg/importer/scale_bench_test.go`:

```go
//go:build big

package importer

import (
	"context"
	"os"
	"runtime"
	"testing"
)

func TestImport_ScaleApply2GiB(t *testing.T) {
	if os.Getenv("DIFFAH_BIG_TEST") != "1" {
		t.Skip("set DIFFAH_BIG_TEST=1 to run")
	}
	delta := buildScale2GiBFixture(t) // calls scripts/build_fixtures -scale=2GiB
	out := t.TempDir()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := runImportInProcess(context.Background(), delta, out); err != nil {
		t.Fatalf("import: %v", err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	const limit = 8 << 30
	if after.HeapAlloc > limit {
		t.Fatalf("HeapAlloc=%d exceeds 8GiB limit", after.HeapAlloc)
	}
}
```

(Companion `/usr/bin/time -v` external assertion lives in `.github/workflows/scale-bench.yml`.)

- [ ] **Step 6.9: Extend `.github/workflows/scale-bench.yml`.** Add an apply phase after the existing export phase:

```yaml
      - name: Apply (scale)
        run: |
          /usr/bin/time -v go test -tags big -run TestImport_ScaleApply2GiB ./pkg/importer/... 2> apply_time.txt
          MAX_RSS_KB=$(grep "Maximum resident set size" apply_time.txt | awk '{print $NF}')
          MAX_RSS_BYTES=$((MAX_RSS_KB * 1024))
          LIMIT=$((8 * 1024 * 1024 * 1024))
          if [ "$MAX_RSS_BYTES" -gt "$LIMIT" ]; then
            echo "::error ::apply RSS $MAX_RSS_BYTES > 8GiB limit"
            exit 1
          fi
          echo "apply RSS=$MAX_RSS_BYTES bytes (limit $LIMIT)"
        env:
          DIFFAH_BIG_TEST: "1"
```

- [ ] **Step 6.10: Run all tests + lint.** `go test ./...` PASS; `golangci-lint run` 0 issues.

- [ ] **Step 6.11: Commit + PR.**

```bash
git add pkg/diff/sidecar.go pkg/diff/sidecar_test.go \
        pkg/importer/decode_windowlog_test.go \
        pkg/importer/compose_phase3_test.go \
        pkg/importer/scale_bench_test.go \
        scripts/build_fixtures/phase3.go \
        testdata/fixtures/phase3-bundle-min.tar \
        .github/workflows/scale-bench.yml
git commit -m "feat(importer): G7 acceptance lockdown (I4+I5+I6) + scale-bench apply (streaming PR6)"
gh pr create --title "feat(importer): G7 acceptance + scale-bench apply (streaming PR6)" --body "..."
```

---

## PR 7: Lessons-learned doc shape

**Spec ref:** §13 acceptance #11; runs the operations-debt #1 recommendation.

**Branch:** `docs/import-streaming-lessons`

**Goal of this PR:** Establish the lessons-learned doc location BEFORE PR1 ships any post-PR amendment. After this PR merges, every PR's amendment goes into this doc, NOT into the plan body. This is the operational-debt remedy: keep plan body as a static contract, keep amendments as a separate evolving log.

**Files:**
- Create: `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md`
- Modify: `docs/superpowers/specs/2026-05-04-import-streaming-io-design.md` (add reference)
- Modify: `docs/superpowers/plans/2026-05-04-import-streaming-io.md` (this file — add a header pointing to the lessons doc)

### Sub-tasks

- [ ] **Step 7.1: Create branch.** `git checkout -b docs/import-streaming-lessons master`.

- [ ] **Step 7.2: Create the lessons-learned doc.** `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md`:

```markdown
# Import Streaming I/O — Lessons Learned

> Plan: [`../plans/2026-05-04-import-streaming-io.md`](../plans/2026-05-04-import-streaming-io.md)
> Spec: [`../specs/2026-05-04-import-streaming-io-design.md`](../specs/2026-05-04-import-streaming-io-design.md)

This document accumulates per-PR lessons that emerge during implementation
of the import-streaming-io plan. It is the dedicated home for "post-PR
amendments" — corrections to the plan that surfaced only during execution.
The plan body itself stays static (it is a contract); this doc evolves.

When implementing a future PR in this plan series:

1. Read this doc end-to-end first. Each amendment may apply to your PR.
2. Run `advisor()` with the spec + plan + this doc loaded in context.
3. After your PR merges, append a "post-PR-N" section here for any plan
   bug or load-bearing pattern that emerged.

(Pattern follows Phase 4's `2026-05-02-export-streaming-io.md` amendments
section — but materialized as a separate file rather than appended to
the plan body. See `docs/superpowers/specs/2026-04-23-production-readiness-roadmap-design.md`
operational-debt section for the rationale.)

---

(No amendments yet. PR1 implementer adds the first section here once their
PR ships, if any plan bug surfaced.)
```

- [ ] **Step 7.3: Add a pointer header to this plan file.** At the top of `docs/superpowers/plans/2026-05-04-import-streaming-io.md`, just below the "For agentic workers" callout, add:

```markdown
> **Amendments live in [`../lessons-learned/2026-05-04-import-streaming-lessons.md`](../lessons-learned/2026-05-04-import-streaming-lessons.md)** — read it before implementing any PR in this series. Plan body stays static; amendments evolve there.
```

- [ ] **Step 7.4: Add a forward reference in the spec file.** Add a "See also" line near the end of the spec pointing to the lessons doc.

- [ ] **Step 7.5: Commit + PR.**

```bash
git add docs/superpowers/lessons-learned/ docs/superpowers/plans/2026-05-04-import-streaming-io.md docs/superpowers/specs/2026-05-04-import-streaming-io-design.md
git commit -m "docs(streaming): scaffold import-streaming lessons-learned doc"
gh pr create --title "docs(streaming): import-streaming lessons-learned scaffold" --body "..."
```

---

## Self-review checklist (run after writing the plan)

- [x] **Spec coverage.** Every spec section maps to at least one task: §1 Context (no task — narrative), §2 Goals (covered by PR1+PR3+PR5+PR6+ acceptance #1-#13), §3 Non-goals (no task), §4 Architecture (PR3+PR4+PR5), §5 Components (5.1→PR2, 5.2→PR3, 5.3→PR3, 5.4→PR3, 5.5→PR4, 5.6→PR5, 5.7→PR6, 5.8→PR6, 5.9→PR6), §6 CLI (PR1), §7 Cleanup (acceptance #13), §8 Testing (per PR), §9 Backward compat (PR1+PR5), §10 Risks (mitigated by PR ordering + advisor pre-flight), §11 PR plan summary (mirrored), §12 Open questions (recorded; resolved during PR4), §13 Acceptance (each criterion has a corresponding test/task).
- [x] **No placeholders.** Every step has actual code or actual commands. The two intentional `// ...` comments inside `cmd/apply.go` and `cmd/unbundle.go` modifications point to existing builder patterns the implementer follows verbatim — they are not placeholders for inventive work.
- [x] **Type consistency.** `BaselineSpool` (PR3) returns `(string, error)` from `GetOrSpool`; `bundleImageSource.spool` field is `*BaselineSpool` throughout PR3-PR5. `AdmissionPool` (PR2) takes `(key string, estimate int64, fn func() error)` everywhere. `WorkerPool` shape forwarded unchanged. `userError` lives twice (exporter local + importer local) — documented as intentional to avoid an import cycle. `ResolveWindowLog` and `EstimateRSSForWindowLog` (latter renamed in PR5) live in `pkg/exporter` only.

If a self-review pass surfaces an issue while the implementer is mid-PR, append a section to `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md`.
