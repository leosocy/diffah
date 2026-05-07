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

## Post-PR-2 amendments

**A1. `errgroup.WithContext` does NOT auto-record `ctx.Err` on cancel.**
The plan's `WorkerPool.Submit` snippet says "errgroup.WithContext records
ctx.Err on cancel, so callers see the cancellation at the same point they
would have seen any other failure" — that is FACTUALLY WRONG. Verified
against `golang.org/x/sync/errgroup`: `Group.err` is set ONLY when a
`Go(f)` callback returns non-nil. A Submit-after-cancel that drops the
fn silently makes `Wait()` return `nil` instead of the cancellation error.
PR2's implementer correctly added an explicit `p.eg.Go(func() error {
return err })` post-cancel branch (compose.go in PR3+ also relies on this
shape). Pre-select `ctx.Err()` check then makes deterministic which
branch wins under select non-determinism.
**How to apply:** Any future lift of WorkerPool that drops the
`p.eg.Go(func() error { return err })` post-cancel branch will silently
break `TestWorkerPool_CtxCancelDropsSubmits`. Keep it.

**A2. Drop the budget-clamp `Warn` log in admission internals.**
Adding it would force `internal/admission` to depend on the exporter's
logger, defeating the importer-reuse goal. The user-facing budget overflow
is gated by `pkg/exporter.checkSingleLayerFitsInBudget` (and now
`pkg/importer.checkSingleImageFitsInBudget`) BEFORE any Submit, so the
clamp branch in `AdmissionPool.Submit` is unreachable in production.
**How to apply:** Future contributors adding observability to the clamp
should plumb a callback (`OnClamp func(estimate, budget int64)`) through
`NewAdmissionPool` rather than introducing a logger import.

## Post-PR-3 amendments

**A3. The plan's mutation test (Step 3.9) was conceptually unsound but
the test as written DOES validate atomic rename.**
Step 3.9 says "swap `os.Rename` for a non-atomic write and confirm
`TestBaselineSpool_ConcurrentSameDigestDistinctPayloadAtomicRename`
fails." The first review pass concluded the test wouldn't fail because
singleflight gates concurrent writers. WRONG: the test deliberately
calls `s.spoolOnce` directly, bypassing both the fast-path lookup AND
singleflight, so 8 concurrent writers genuinely race on dst. Mutating
`spoolOnce` to write directly to dst causes interleaved bytes that fail
the byte-equality assertion.
**How to apply:** Don't trust the implementer's caveats blindly — read
the test and trace what the test setup actually exercises before
deciding the test "doesn't really test what it claims."

**A4. Re-wrap `*diff.ErrBaselineBlobDigestMismatch` at the call site to
repopulate ImageName.** The spool is per-Import (image-agnostic), but
the operator-facing error has historically named the offending image.
PR3's first cut dropped the ImageName field. Fix lives in
`pkg/importer/compose.go::fetchVerifiedBaselineBlob` (and `servePatch`
when the spool fetch closure surfaces a mismatch through the patch
path).
**How to apply:** Any future caller of `BaselineSpool.GetOrSpool` that
exposes errors to operators must re-wrap. The spool surface is
intentionally narrow.

## Post-PR-4 amendments

**A5. `progress.CountingReader` and `_ progress.Reporter` were initially
shipped without an in-tree caller — amendment-13 violation.**
Code review caught both. CountingReader was dropped from PR4 and is
slated for PR5 alongside its first wiring; the dead Reporter parameter
was removed from `composeImage`. Lesson: amendment-13 ("don't ship 'for
next PR' code") applies on the import side too. Lifted helpers without
callers are speculative API surface; introduce them in the PR that uses
them.

**A6. `BlobInfoCache` MUST be forwarded into the spool fetch closure;
nil panics in the docker registry transport.** The plan's draft passed
nil; the implementer correctly forwarded the upstream cache (mirroring
`fetchVerifiedBaselineBlob`'s pre-existing pattern).
**How to apply:** Any new place that calls `s.baseline.GetBlob(ctx, info,
nil)` must verify against an integration test that exercises the
docker:// transport. nil-cache panics surface only at runtime under
specific transports.

**A7. `verifyingReadCloser.mismatchErr` needs a defensive `default`
case.** A future `readerKind` addition that forgets to wire a sentinel
must not silently downgrade a digest mismatch to "no error". Added in
PR4 review fixes.
**How to apply:** Whenever you switch on a small int-typed enum that
can grow, write the default branch.

## Post-PR-5 amendments

**A8. `MaxParallelDownloads = 1` is required when flipping
`HasThreadSafeGetBlob` to `true`.** copy.Image consults the dest's
`HasThreadSafePutBlob` — for `dir:`, `oci:`, `docker://`, this is true,
making `concurrentBlobCopiesSemaphore = NewWeighted(MaxParallelDownloads)`
default to 6. Without forcing 1, peak RSS becomes
`min(6, layers) × max-per-layer` instead of `max-per-layer`, silently
violating the `--memory-budget` operator contract for non-oci-archive
outputs. Image-level parallelism is provided by the AdmissionPool;
intra-image fan-out is redundant.
**How to apply:** When importer-side options grow per-blob fan-out
(future PR), the RSS estimator must account for that fan-out OR the
fan-out must respect the same `MemoryBudget` semaphore.

**A9. PR-5 strict mode does NOT cancel in-flight applies — only
queued ones.** errgroup cancels gctx, which dequeues unstarted Submits;
in-flight closures hold the outer `ctx` and run to completion. This
mirrors the old serial loop's "in-flight applies aren't killed
mid-stream" contract. The apply report can therefore have one failure
row plus zero or more sibling success rows under strict mode at
workers > 1. Documented in `importEachImage`'s doc comment.
**How to apply:** If a future PR wants strict to cancel in-flight,
thread `gctx` into `applyOneImage` and check periodically. The current
behavior is intentional, not a bug.

**A10. Two pre-existing unbundle preflight tests are pinned at
`--workers=1`** because they relied on PR3's serial-loop ordering for
cross-image baseline-spool dedup test setup. The cleanest follow-up is
per-image baseline-completeness preflight, which would let those tests
work at any worker count. Tracked here so PR6/PR7 reviewers don't lose
the breadcrumb.
**How to apply:** When implementing per-image baseline preflight
(future), revisit `cmd/unbundle_preflight_integration_test.go`'s
`--workers=1` pins and remove them.

## Post-PR-6 amendments

**A11. I5 windowLog fail-closed test does NOT exercise the production
decode path.** PR6 ships `pkg/importer/decode_windowlog_test.go` with a
`zstd.WithDecoderMaxWindow(1<<27)` cap, but the production decode path
in `internal/zstdpatch/fullgo.go::DecodeFull` uses `1<<31` and the patch
path (`stream.go`) shells out to `zstd --long=31`. **Neither caps at
1<<27 today.** The test locks in DEFENSIVE behavior for a hypothetical
future caller that calls `zstd.NewReader` without an explicit cap
override; it does NOT prove that today's importer rejects windowLog≥28
frames.
**How to apply:** Spec §13 acceptance #8 should be considered OPEN until
either (a) the production caps are tightened to align with the test, or
(b) the spec is amended to scope the I5 contract to "defensive
in-process klauspost cap." A follow-up PR is needed; the I4 / scale-
bench / I6 follow-ups should be bundled if they cluster naturally.

**A12. I5 windowLog range scoped to {28, 29}, not {28-31}.**
klauspost/compress's encoder hard-caps `WithWindowSize` at `1<<29`
(`MaxWindowSize` constant). Frames declaring wl=30 or wl=31 cannot be
synthesized in-process via the klauspost encoder. The test exercises
both wl=28 (2× cap) and wl=29 (4× cap), which is sufficient to prove
the rejection path; wl ∈ {30, 31} verification would require shelling
out to the zstd CLI binary or hand-crafting a frame header — neither is
worth the complexity.
**How to apply:** Don't write tests that drive klauspost beyond
`MaxWindowSize`. If a future regression requires verifying wl=30/31
specifically, use the zstd CLI with a temp-file fixture.

**A13. Apply scale-bench step in nightly CI is a no-op until follow-up
lands.** PR6 wires `.github/workflows/scale-bench.yml` with an "Apply
(scale)" step, but `TestImport_ScaleApply2GiB` `t.Skip`s its body until
the apply scale-bench fixture infrastructure (fixture builder
`buildScale2GiBFixture` + in-process `runImportInProcess` helper) lands
in a follow-up. The workflow log will read "PASS: apply peak RSS X KiB
<= 8 GiB ceiling" but that proof is meaningless — the skipped test
allocates negligible RSS, so the guard trivially passes.
**How to apply:** Spec §13 acceptance #10 is OPEN. Follow-up PR must
land both the fixture builder and the in-process import helper. The
step now carries a deferred-body comment so on-call review of the
nightly log isn't misled. Until that follow-up ships, treat the green
nightly as silence on apply RSS, not a guarantee.

**A14. I6 (Phase-3 fixture round-trip) deferred entirely.** PR6 did not
ship `testdata/fixtures/phase3-bundle-min.tar`, the generator script,
or `pkg/importer/compose_phase3_test.go`. The Phase-3-vintage bundle
generator requires synthesizing a v0.1.0-shape diffah bundle directly
(no shell-out to a frozen binary), which means encoding precise
historical bundle layout knowledge that the team has not validated.
**How to apply:** Spec §13 acceptance #9 is OPEN. Before implementing,
check `git log --all -- testdata/fixtures/` and `git log v0.1.0..` to
locate the historical sidecar shape. The lookup may turn up that v0.1.0
shipped without all the schema fields current `diff.Sidecar` requires —
the generator may need to mark legacy fields with `omitempty` or the
test may need a tolerant parse path.

## Post-merge retrospective amendments

**A15. Apply-side config keys consciously merged with diff-side,
against the plan.**
Plan PR1 (`docs/superpowers/plans/2026-05-04-import-streaming-io.md`
lines 56, 84, 129-130, 265-266) and spec §4.4 + §13 acceptance #6
(`docs/superpowers/specs/2026-05-04-import-streaming-io-design.md`
lines 35, 223-224, 362) called for separate `apply-workdir` /
`apply-memory-budget` / `apply-workers` keys in `pkg/config.Config`.
The PR1 implementer collapsed them: apply / unbundle reuse `workdir`
/ `memory-budget` from the diff-side and the `--workers=8` default
is hard-coded inside `installImportSpoolFlags`. Decision rationale
lives in the comment at `cmd/config_defaults_test.go:37-40`:
per-command differentiation is via CLI flags, not config keys;
sharing keeps the operator's mental model "one workdir, one budget"
rather than "did I set both, which wins?"
**How to apply:** Treat the plan/spec text on this point as
superseded by the implementation; do not "fix" the divergence by
re-introducing the split keys. Future PRs that add command-specific
spool knobs should weigh this same trade-off; default to one shared
key unless there is a concrete operator-facing reason to split.
Per-invocation asymmetry is still reachable via CLI flags (`apply
--memory-budget=4GiB` overrides the shared config value for that one
invocation).

**A16. Baseline-only reuse still buffers entire blob in RAM via
`os.ReadFile`.**
`pkg/importer/compose.go::fetchVerifiedBaselineBlob` (lines 307-321)
reads the spooled file with `os.ReadFile` and returns `[]byte`; the
helper's own TODO at lines 298-301 acknowledges this as a known
follow-up but the gap never made it into this lessons doc, so the
post-merge retrospective surfaced it as if it were new. The
per-image admission estimator at `pkg/importer/admission.go:55-60`
skips baseline-only layers entirely (`continue` in the loop), so
the worst case is `Workers × max-baseline-only-blob-size` of
unaccounted heap: 8 workers each touching a 4 GiB baseline-only
layer can allocate ~32 GiB while the budget gate reports near-zero
estimate. The headline `--memory-budget` UX is therefore false in
any bundle that exercises baseline reuse on multi-GiB layers — the
disk-spool acceptance the plan claimed is necessary but not
sufficient for the budget contract.
**How to apply:** Follow-up PR must (a) change
`fetchVerifiedBaselineBlob` return type from `([]byte, error)` to
`(io.ReadCloser, int64, error)` backed by `os.Open(path)` + `Stat`,
propagating through `bundleImageSource.GetBlob` so callers stream
rather than slurp; (b) extend `estimatePerImageRSS` to include
baseline-only layer sizes from the target manifest — those bytes
still hit the OS page cache and contribute to apply-time RSS even
though no zstd decoder window is live; (c) add an apply-side
scale-bench fixture that exercises a baseline-only-reuse layer
≥ 4 GiB so the existing nightly bench actually proves the
baseline-reuse path respects the budget, not just the patch path.
Spec §13 acceptance must be augmented with this scenario before
this gap can be considered closed.

---

(End of current amendments. Future PRs append below.)
