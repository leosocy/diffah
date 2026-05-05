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

---

(End of current amendments. Future PRs append below.)
