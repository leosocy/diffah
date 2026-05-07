# diffah Streaming I/O Hardening — Design

- **Status:** Draft
- **Date:** 2026-05-07
- **Author:** @leosocy
- **Implements:** Closes the four P0-class correctness gaps surfaced in the [post-merge codex retrospective](../lessons-learned/2026-05-07-codex-streaming-retrospective.md) of the streaming I/O initiative ([export](2026-05-02-export-streaming-io-design.md) + [import](2026-05-04-import-streaming-io-design.md)).
- **Scope:** `pkg/diff/sidecar.go`, `pkg/importer/{compose.go, admission.go, importer.go, preflight.go, extract.go}`, `internal/archive/reader.go`, plus their unit + integration test sets. No CLI surface change. No spec change to the streaming I/O contracts already shipped — this is hardening of those contracts, not a new feature.

## 1. Context

The streaming I/O initiative shipped 14 PRs (export PRs #38-49, import PRs #50-62) and closed the bulk of its declared §13 acceptance gates. A post-merge retrospective via OpenAI Codex CLI (medium reasoning effort, full read-only repo access) found four P0-class gaps that the streaming-I/O lessons doc did not capture, plus a smaller set of architectural drift items between export and import sides.

The four P0s, in fix order (smallest blast-radius-fix first):

| ID | Failure mode | Worst case |
|---|---|---|
| P0 #4 | Sidecar digest fields are not validated by `digest.Validate()` before being joined into `<workdir>/blobs/` paths | Crafted sidecar with `algorithm="../../etc/passwd"` escapes blobDir; bundle signature verification does not protect (signature covers the malicious bytes) |
| P0 #1 | `pkg/importer/compose.go::fetchVerifiedBaselineBlob` reads entire spooled blob into RAM via `os.ReadFile`; admission estimator skips baseline-only layers | 8 workers × 4 GiB baseline-only layer = ~32 GiB heap allocation while budget gate reports near-zero estimate; headline `--memory-budget` UX is empirically false |
| P0 #3 | `verifyingReadCloser` runs digest check only on `io.EOF`; early `Close` skips verification | Corrupt patch that triggers a downstream early-close (destination put fails, ctx cancellation) is silently accepted |
| P0 #2 | Per-Import `BaselineSpool` is keyed by digest only; per-image baseline-completeness is not enforced | Multi-image bundle: image A's baseline primes the spool for digest X; image B (whose own baseline lacks X) reads A's spooled bytes and bypasses B2-completeness check |

Plus one concurrency item that pairs naturally with P0 #3 (same PR):

| ID | Failure mode |
|---|---|
| Conc C | `internal/archive/reader.go::writeFile` ignores `io.EOF` from `io.CopyN`; truncated tar entries extract as short blob files; downstream digest verification catches the consequence under the wrong root cause |

These five items plus three small drift cleanups (extraction location, admission scope, error mutation) are the subject of this hardening initiative. Findings the codex retrospective surfaced as Conc A (workerpool post-cancel goroutine noise), Conc B (admission goroutine before gate), drift B/C (BaselineSpool surface unification, atomic publication), and Cfg A (config precedence docstring) are out of scope; their absolute severity is low and they belong in a separate hygiene cycle.

The four P0s are not regressions — three of them existed before the streaming I/O initiative; the streaming work moved code around without resolving them. P0 #4's path-build sites grew during streaming (more digest-driven joins under `<workdir>/`) but the validation gap predates this work.

## 2. Goals

A release closes this hardening initiative when **all** of the following hold:

1. **Path traversal closed.** `pkg/diff.ParseSidecar` rejects every sidecar whose `Sidecar.Blobs` keys, `ImageEntry.Target.ManifestDigest`, `ImageEntry.Baseline.ManifestDigest`, or `BlobEntry.PatchFromDigest` fails `digest.Validate()`. Regression corpus in `pkg/diff/testdata/malicious-sidecars/` covers the five attack variants in §5.1.

2. **Baseline-only reuse streams.** `pkg/importer/compose.go::fetchVerifiedBaselineBlob` returns a path-backed `io.ReadCloser`; `bundleImageSource.GetBlob` no longer wraps a slurped buffer in `bytes.NewReader`. The per-image admission estimator at `pkg/importer/admission.go::estimatePerImageRSS` counts baseline-only layer size contributions. Apply-side regression test asserts `--memory-budget=2GiB` rejects an apply that references a 4 GiB baseline-only layer; `--memory-budget=8GiB` admits it AND peak RSS stays ≤ 1.5× budget.

3. **Verification cannot be skipped.** `verifyingReadCloser.Close` returns a typed `*diff.ErrBlobIncompletelyConsumed` when the reader was closed without ever reaching `io.EOF` AND no other error has short-circuited the path. `internal/archive.Extract` rejects truncated tar entries with a typed error mentioning the entry name.

4. **Per-image baseline completeness is preflighted.** Before `importEachImage` constructs its admission pool, every image's resolved baseline source is checked against its required baseline-only layer digests. Cross-image masking (the §1 P0 #2 scenario) surfaces as `ErrMissingBaselineReuseLayer` for the failing image; the two `--workers=1` pinned tests in `cmd/unbundle_preflight_integration_test.go` are unpinned and pass at the default `--workers=8`.

5. **No regression.** The full unit + integration suite passes (`go test ./...`); the nightly scale-bench (`.github/workflows/scale-bench.yml`) extended with the §5.2 baseline-only-reuse fixture stays under its 8 GiB ceiling.

## 3. Non-goals

- **Redesigning the bounded-memory architecture.** Streaming I/O ships; this is hardening, not v2.
- **Performance tuning.** RSS regression tests gate "stays under budget," not "uses less RAM than before."
- **CLI surface changes.** `--memory-budget`, `--workdir`, `--workers` keep their semantics; help text is updated only where prose was misleading (§6).
- **DIFFAH_MEMORY_BUDGET env path.** Decided as not-doing; env applies only to path-type config (see [lessons doc A15](../lessons-learned/2026-05-04-import-streaming-lessons.md#post-merge-retrospective-amendments)).
- **Re-introducing `apply-workdir` / `apply-memory-budget` / `apply-workers` config keys.** The shared-key design is intentional (lessons doc A15); plan/spec text in the import-streaming docs on the split is superseded.
- **A11/A13/A14 acceptance gates from the import-streaming spec.** Tracked separately; touched only if a hardening PR's scope incidentally closes one of them (it does not).
- **Cross-format / OCI compatibility work.**
- **Drift items below P0 severity** (admission goroutine ordering, workerpool post-cancel noise, BaselineSpool surface unification). Deferred to a separate hygiene cycle.

## 4. Architecture

This initiative is fixes-shaped, not architecture-shaped. The streaming pipeline already exists; this work lands targeted contract changes inside the existing layers without re-drawing them.

| Layer | Touched component | Contract change |
|---|---|---|
| `pkg/diff` (pure domain) | `Sidecar.validate` + `validateBlobEntry` | Rejects sidecars with non-canonical digest fields. |
| `pkg/diff` (pure domain) | `errors.go` | Adds `*ErrBlobIncompletelyConsumed{Kind, Digest, ImageName}`. |
| `internal/archive` (I/O helper) | `writeFile` | Rejects truncated tar entries instead of silently accepting them. |
| `pkg/importer` (apply pipeline) | `compose.go::fetchVerifiedBaselineBlob`, `compose.go::verifyingReadCloser`, `admission.go::estimatePerImageRSS`, `importer.go::Import` (preflight ordering), and a new per-image baseline-completeness preflight | Streaming reads instead of slurped bytes; verifying close instead of trusting close; preflight-baselines before pool. |

No new packages. One new public type (`*diff.ErrBlobIncompletelyConsumed`).

## 5. Components

### 5.1 P0 #4 — Sidecar digest validation

**Location.** `pkg/diff/sidecar.go::validate` and `validateBlobEntry`.

**Contract.** Every `digest.Digest` field on the parsed sidecar must satisfy `digest.Digest.Validate()` before the sidecar is considered well-formed. Validation runs for:
- `Sidecar.Blobs` map keys
- `ImageEntry.Target.ManifestDigest`
- `ImageEntry.Baseline.ManifestDigest`
- `BlobEntry.PatchFromDigest` (only when `Encoding == EncodingPatch`)

Failure surfaces as `*ErrSidecarSchema{Reason: "<field> not a valid digest: <go-digest err>"}`, surfaced from both `Sidecar.Marshal` (write path) and `ParseSidecar` (read path) so a malicious sidecar cannot be produced by `bundle` or consumed by `apply`.

**Attack variants the regression corpus covers.**

1. Path-traversal algorithm: `"../../etc/passwd:abcdef"` — `digest.Validate()` rejects the algorithm token because it does not match the `[A-Za-z][A-Za-z0-9]*(?:[+._-][A-Za-z0-9]+)*` grammar.
2. Non-canonical encoded form: `"sha256:ABCDEF..UPPERCASE.."` — go-digest enforces lowercase encoded form for `sha256` / `sha512`.
3. Empty algorithm: `":abcdef"` — empty algorithm token rejected.
4. Malformed encoded form: `"sha256:nothex"` — go-digest verifies encoded length and hex purity per algorithm.
5. Unknown hash algorithm: `"md5:abcdef"` — go-digest accepts only registered algorithms (`sha256`, `sha512`).

The corpus lives at `pkg/diff/testdata/malicious-sidecars/<variant>.json`; the regression test iterates the directory so adding a new attack variant is one file.

**Why this is safe to add.** `digest.Validate()` is the canonical check shipped by `opencontainers/go-digest`; any sidecar that fails it was already broken under any digest-aware downstream tool (containers/image, OCI registries, distribution daemons). The concern that a legitimate diffah sidecar might fail validation is mitigated by adding a round-trip test: a canonical sidecar built by `pkg/exporter` must `Marshal` and `ParseSidecar` cleanly under the new validation.

### 5.2 P0 #1 — Baseline-only reuse streams

**Location.** `pkg/importer/compose.go::fetchVerifiedBaselineBlob`, `bundleImageSource.GetBlob`, and `pkg/importer/admission.go::estimatePerImageRSS`.

**Contract change to `fetchVerifiedBaselineBlob`.** Returns `(io.ReadCloser, int64, error)` backed by `*os.File` from `os.Open` + `Stat`. The error-wrapping path (re-populating `ImageName` on `*diff.ErrBaselineBlobDigestMismatch`) is preserved verbatim; only the success-path return type changes. `GetBlob`'s baseline-only branch propagates the `io.ReadCloser` directly — no `bytes.NewReader`, no `io.NopCloser`.

**Estimator change.** `estimatePerImageRSS` is currently "max across shipped layers, skip baseline-only." That is incorrect for the operator's mental model of `--memory-budget`: baseline-only layers ALSO contribute to apply-time RSS — the spool path is read into the destination, so OS page cache for the layer counts against the budget envelope. Within one image, copy.Image processes layers serially (`MaxParallelDownloads: 1` is forced in `pkg/importer/compose.go:431`), so per-image peak is `max(shipped_layer_rss, baseline_only_layer_size)` — not `max(shipped_layer_rss)`.

The new estimator walks every layer; for shipped layers it computes `EstimateRSSForWindowLog(ResolveWindowLog(...))` as today; for baseline-only layers it uses `LayerRef.Size` (the uncompressed layer size already returned by `readManifestLayers`). Per-image result is the max across both bucket types. Per-bundle aggregation is unchanged (admission semaphore sums across concurrent images).

**Why max-not-sum still works.** `MaxParallelDownloads: 1` is enforced inside `composeImage`; intra-image fan-out is serial. The pool sums per-image estimates across CONCURRENT images via the admission semaphore, which is unchanged. A future PR that introduces intra-image fan-out (none planned) would need to revisit this.

**RSS regression test.** Apply-side scale-bench fixture `pkg/importer/scale_bench_test.go::TestImport_ScaleBaselineOnlyReuse4GiB`: builds a synthetic two-image bundle where both images reference a single 4 GiB baseline-only layer, runs `Import()` with `--workers=8 --memory-budget=8GiB`, asserts `peak_rss_kb <= 12_000_000` (12 GiB ceiling = 8 GiB budget × 1.5 overhead allowance). A second sub-case asserts `--memory-budget=2GiB` rejects with a `*errs.UserError{Cat: errs.CategoryUser}` BEFORE any worker spins up.

### 5.3 P0 #3 + Conc C — Verification cannot be skipped

**Location P0 #3.** `pkg/importer/compose.go::verifyingReadCloser`.

**Contract change.** `verifyingReadCloser` gains a `verified bool` field. `Read` sets it to `true` on the existing `io.EOF` branch where the digest check runs. `Close`:

- Closes the underlying `*os.File` (existing behavior).
- Removes the scratch path for `kindAssembled` (existing behavior).
- If the file close did not error AND `!r.verified`, returns `*diff.ErrBlobIncompletelyConsumed{Kind, Digest, ImageName}` instead of `nil`.

The new error type lives in `pkg/diff/errors.go`:

```go
type ErrBlobIncompletelyConsumed struct {
    Kind      string // "shipped" | "assembled"
    Digest    string
    ImageName string // empty for kindAssembled
}

func (e *ErrBlobIncompletelyConsumed) Error() string { ... }
func (e *ErrBlobIncompletelyConsumed) Category() errs.Category { return errs.CategoryContent }
```

The Error() string reads "<kind> blob <digest> closed before EOF; integrity unverified (image=<name>)" with the image-name suffix omitted when empty.

**Regression test (P0 #3).** `pkg/importer/compose_test.go::TestVerifyingReadCloser_CloseBeforeEOFSurfacesError` constructs a 1024-byte payload, opens via `verifyingReadCloser`, reads 512 bytes, calls `Close`, asserts the typed `*diff.ErrBlobIncompletelyConsumed` is returned. A second sub-case reads to EOF then closes; asserts `Close` returns `nil`.

**Location Conc C.** `internal/archive/reader.go::writeFile` lines 129-138.

**Contract change.** Drop the `&& !errors.Is(err, io.EOF)` clause. `io.CopyN` returns `io.EOF` when the source could not deliver `size` bytes; at the archive boundary that ALWAYS means corruption (the tar header committed to a size that the entry body did not honor). Wrap-message is updated to mention truncation explicitly so the operator-facing root cause is correct.

```go
// After:
if _, err := io.CopyN(f, r, size); err != nil {
    return fmt.Errorf("write %s (truncated tar entry?): %w", path, err)
}
```

**Regression test (Conc C).** `internal/archive/reader_test.go::TestExtract_TruncatedTarEntryRejected` constructs a tar archive whose entry header claims `size=1024` but the entry body is 512 bytes followed by the next header; asserts `Extract` returns an error matching the substring "truncated tar entry."

### 5.4 P0 #2 — Per-image baseline-completeness preflight

**Location.** New helper `pkg/importer/baseline_preflight.go` (clearer than co-locating in the existing `preflight.go` which already runs schema + sidecar consistency checks). Wired into `pkg/importer/importer.go::Import` after `RunPreflight` and `splitPreflightResults`, BEFORE `checkSingleImageFitsInBudget`.

**Contract.** For each image in `applyList`:
1. Parse the target manifest (or reuse the parsed-layers cache that the admission estimator already builds).
2. Compute the set of baseline-only layer digests = `target_layers \ sidecar.Blobs`.
3. For each baseline-only digest, check the resolved baseline source for THAT image. The check uses `types.ImageSource.GetBlob` with a small 1-byte read followed by Close to confirm the blob is fetchable; this mirrors what `fetchVerifiedBaselineBlob` does at apply time but does not spool.
4. Missing digests surface as `*ErrMissingBaselineReuseLayer{ImageName, LayerDigest}` and the image is moved from `applyList` to `skippedByPreflight` with a new `PreflightStatus` value `PreflightBaselineMissing`.

The check uses the same `resolvedByName` map `Import` already builds, so no new resolution work. The cost is `O(images × baseline-only-layers)` cheap fetch-check operations against baseline transports; for typical bundles (10 images × 2-3 baseline-only layers per image) this is well under a second on local registries and a few seconds on remote.

**Why fetch-check, not HEAD.** `types.ImageSource` does not expose HEAD directly; `GetBlob` is the portable shape across `oci:`, `docker://`, `dir:`, and `containers-storage:`. Reading 1 byte triggers any 4xx and is cheap on every transport tested. If a future operator profiles preflight as too slow, an opt-out flag (`--no-baseline-preflight`) can be added; this initiative ships preflight always-on.

**Spec amendment to import-streaming spec.** `docs/superpowers/specs/2026-05-04-import-streaming-io-design.md` §13 acceptance gains a new item #11: "Per-image baseline-completeness preflight runs before the admission pool; cross-image baseline masking is impossible at any worker count." §13 acceptance #6 already mentions baseline preflight; clarify it covers per-image, not just bundle-level. Amendment lands in the same PR (PR4) that implements the preflight.

**Tests A10 unpins.** `cmd/unbundle_preflight_integration_test.go` lines 59-68 and 122-127 currently pin `--workers=1`; A10 explicitly named these as deferred follow-ups to a per-image baseline preflight. With this PR, the pins are removed and the tests run at the default `--workers=8`.

**Regression test.** New integration test `cmd/unbundle_preflight_baseline_completeness_integration_test.go` constructs a two-image bundle where:
- Image A's baseline contains digest X (baseline-only layer in A).
- Image B's baseline does NOT contain digest X (baseline-only layer in B).
- Both images reference X.

With `--workers=8`, the apply must:
- Partial mode: succeed A; report B as `PreflightBaselineMissing` with the typed error in the apply report.
- Strict mode: abort before any apply with the same error classified as preflight-skipped (matches existing `abortWithPreflightSummary` shape).

### 5.5 (Optional, PR5) Drift cleanup

The three small drift items below are cheap to bundle; they are NOT required for v0.3 readiness:

1. **Import extraction relocates under `<workdir>/bundle/`.** `pkg/importer/extract.go::extractBundle` accepts the resolved workdir and uses `filepath.Join(workdir, "bundle")` instead of `os.MkdirTemp("", "diffah-import-")`. `Import()` calls `workdir.Ensure` BEFORE `extractBundle`. Aligns with the import-streaming spec §4.2 layout. Existing `bundle.cleanup()` is preserved (RemoveAll on the bundle subdir); workdir cleanup teardown still removes the parent.
2. **Admission budget check uses `applyList`.** `pkg/importer/importer.go:248-251` is moved below `splitPreflightResults` and passes the post-preflight image list. Closes drift D from the retrospective: a preflight-skipped oversized image no longer rejects its small siblings.
3. **Error wrapping stops mutating in place.** `pkg/importer/compose.go:177-179` and `:316-318` construct a NEW `*diff.ErrBaselineBlobDigestMismatch` with the populated `ImageName`, instead of mutating the existing object. Removes the antipattern; no observable change.

These can land independently of P0 #1-#4 and depend only on PR4 having landed (the budget-check move in #2 must happen after the preflight-ordering changes).

## 6. CLI surface changes

None. All five fixes are internal. Help text updates are limited to:

- `--memory-budget` description on `apply` / `unbundle` gains a one-line append: "Includes baseline-only reuse layer sizes."

The change lives in `cmd/spool_flags.go::importSpoolHelp` and `installImportSpoolFlags` flag-help string. Bundle-side help text is unchanged because the export-side estimator was already correct.

## 7. Cleanup contract

PR4 (per-image baseline preflight) introduces a new failure mode that returns `*ErrMissingBaselineReuseLayer` from preflight. The existing cleanup contract — `defer cleanupWorkdir()` and `defer bundle.cleanup()` in `Import()` — already covers this path. No new cleanup work.

PR5 drift item #1 consolidates extraction inside the workdir; `bundle.cleanup()` removes the extraction directory; `cleanupWorkdir` removes the workdir tree which now contains it. The two cleanups become nested but both remain idempotent.

## 8. Testing strategy

### 8.1 Unit tests

- **§5.1 sidecar validation.** Six sub-tests in `pkg/diff/sidecar_test.go`: five malicious-sidecar variants (table-driven over the testdata corpus) + one canonical round-trip.
- **§5.2 streaming + estimator.** Three sub-tests:
  - `pkg/importer/admission_test.go::TestEstimatePerImageRSS_CountsBaselineOnlyLayers` — estimator MAX includes baseline-only sizes.
  - `pkg/importer/admission_test.go::TestEstimatePerImageRSS_ZeroLayers` — image with no layers returns 0 (regression).
  - `pkg/importer/compose_test.go::TestGetBlob_BaselineOnlyReturnsPathBackedReader` — type-asserts the returned reader is `*os.File`-backed (does NOT pass `*bytes.Reader` shape check).
- **§5.3 verify-on-close.** Two sub-tests in `pkg/importer/compose_test.go`: close-before-EOF surfaces typed error; close-after-EOF returns nil.
- **§5.3 short-copy.** One sub-test in `internal/archive/reader_test.go`: truncated entry rejected.
- **§5.4 baseline preflight.** Three sub-tests in `pkg/importer/baseline_preflight_test.go`: all baselines complete (passes); image B missing (B fails, A succeeds); baseline transport error other than missing (surfaces with classification).

### 8.2 Integration tests

- **§5.4 cross-image masking.** New file `cmd/unbundle_preflight_baseline_completeness_integration_test.go` with partial + strict mode sub-cases.
- **§5.4 unpin A10 tests.** Edit existing `cmd/unbundle_preflight_integration_test.go` lines 59-68 and 122-127 to remove `--workers=1` overrides.

### 8.3 Scale-bench

Extend `pkg/importer/scale_bench_test.go` with `TestImport_ScaleBaselineOnlyReuse4GiB`. Wire into `.github/workflows/scale-bench.yml` as a new step after the existing 4-GiB-layer apply step. Acceptance: peak RSS ≤ 12 GiB under `--memory-budget=8GiB`.

### 8.4 Spec compliance

After each PR's implementer reports DONE, a spec-compliance subagent reads the PR's diff against this spec and asserts every contract change in §5.X matches the cited file:line. Single-pass; not iterative.

## 9. Backward compatibility

No breaking changes. All five fixes are conservative tightening:

- **§5.1 (digest validation).** Sidecars produced by any prior diffah version that satisfy go-digest's grammar will still parse. Sidecars containing non-canonical digests have always been broken downstream; surfacing them at parse time is only a UX improvement.
- **§5.2 (streaming).** `GetBlob` callers within diffah are limited to copy.Image's internal flow, which already consumes the returned `io.ReadCloser` correctly. `pkg/importer.bundleImageSource` is unexported; no out-of-tree callers.
- **§5.3 (verify-on-close, short-copy).** Error paths that previously succeeded silently now fail loudly. Any downstream code that relied on the silent path was already broken.
- **§5.4 (preflight).** Strict-mode failure mode is unchanged for completeness errors (they fail; just earlier, with a clearer error). Partial-mode success rate goes UP, not down (cross-image masking is no longer possible, so previously-broken cases now surface; previously-passing B2 cases continue to pass).

## 10. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| §5.1 digest validation rejects a legitimate sidecar built by an old exporter | Low | High (operator can't apply) | go-digest's grammar is unchanged across versions; canonical-sidecar round-trip test asserts pkg/exporter's output round-trips. |
| §5.2 streaming change leaks file descriptors when copy.Image errors mid-Read | Medium | Medium (FD leak per failed apply) | Mirror `serveFull`'s ownership model: `verifyingReadCloser` already owns the `*os.File` and removes scratch on Close. The new path-backed reader uses the same shape. Regression test exercises ctx-cancel mid-Read and asserts goroutine + FD count returns to baseline. |
| §5.3 short-copy reject breaks a benign tar quirk we don't know about | Low | Low | `io.CopyN` with positive `size` returns `io.EOF` only on actual truncation per Go stdlib contract. If a real archive ever hit this, it was already corrupting downstream digest checks. |
| §5.4 per-image preflight adds startup latency for large multi-image bundles | Medium | Low | Fetch-check is cheap per-blob and parallelizable. Add `--no-baseline-preflight` opt-out flag if a real bundle exhibits >5s latency (deferred until empirically needed). |
| Implementer subagent confuses streaming I/O patterns with hardening fixes | Medium | Medium | Plan dispatches one PR at a time with explicit "do NOT touch X" boundaries; lessons doc A1-A14 + A15-A16 baked into each PR's prompt. |

## 11. Phased landing (PR plan summary)

| PR | Scope | Size | Risk |
|---|---|---|---|
| PR1 | §5.1 — sidecar digest validation + corpus | XS | Low |
| PR2 | §5.2 — baseline-only streaming + estimator + RSS bench | M | Medium (FD ownership) |
| PR3 | §5.3 — verify-on-close + archive short-copy reject + new error type | S | Low |
| PR4 | §5.4 — per-image baseline preflight + unpin A10 tests + spec amendment | M-L | Medium (latency) |
| PR5 (optional) | §5.5 — drift cleanup (extraction, admission scope, error mutation) | S | Low |

PR1-PR4 are the v0.3 readiness gate. PR5 is best-effort within the cycle.

Detailed step-by-step for each PR is in the [companion plan](../plans/2026-05-07-streaming-hardening.md).

## 12. Open questions

1. **`*diff.ErrBlobIncompletelyConsumed` shape.** Single type with `Kind` discriminator (proposed) vs. two sibling types (`ErrShippedBlobIncompletelyConsumed` / `ErrAssembledBlobIncompletelyConsumed`). Concrete vote: single type — mirrors `ErrBaselineBlobDigestMismatch`'s shape and avoids two new exported names. Decision deferred to PR3's implementer; if they choose split, update §5.3 above accordingly.
2. **Baseline preflight check shape.** 1-byte `GetBlob`-and-Close (proposed) vs. transport-specific HEAD where available. Concrete vote: 1-byte fetch — portable across `oci:` / `docker://` / `dir:` / `containers-storage:`; no transport-feature detection required. Defer transport-aware optimization until empirical latency demands it.

## 13. Acceptance

A change set satisfies this hardening initiative when:

1. `pkg/diff/testdata/malicious-sidecars/` corpus exists with five JSON files; `pkg/diff.ParseSidecar` rejects each one with `*ErrSidecarSchema`. A canonical round-trip test passes.
2. `pkg/importer/compose.go::fetchVerifiedBaselineBlob` returns `(io.ReadCloser, int64, error)`; its concrete type IS `*os.File`; the per-image admission estimator counts baseline-only layer sizes.
3. `pkg/importer/scale_bench_test.go::TestImport_ScaleBaselineOnlyReuse4GiB` passes under `--memory-budget=8GiB` and its no-budget-reject sub-case passes under `--memory-budget=2GiB`. The nightly scale-bench workflow runs the new test.
4. `verifyingReadCloser.Close` returns `*diff.ErrBlobIncompletelyConsumed` when closed before EOF; close-after-EOF returns nil.
5. `internal/archive.Extract` returns a typed truncated-entry error on a malformed tar fixture.
6. `cmd/unbundle_preflight_baseline_completeness_integration_test.go` passes at default `--workers=8`. The two `--workers=1` pins in `cmd/unbundle_preflight_integration_test.go` are removed; A10's lessons doc note is updated to "closed by hardening PR4."
7. `go test ./...` is green.
8. `golangci-lint run ./...` reports no new findings beyond pre-existing nolints.

## See also

- [Post-merge codex retrospective](../lessons-learned/2026-05-07-codex-streaming-retrospective.md) — source of the four P0 findings.
- [Import streaming I/O lessons-learned](../lessons-learned/2026-05-04-import-streaming-lessons.md) §A15-A16 — amendments that motivate this initiative.
- [Import streaming I/O design](2026-05-04-import-streaming-io-design.md) — the contract this initiative hardens.
- [Companion plan](../plans/2026-05-07-streaming-hardening.md) — step-by-step for each PR.
