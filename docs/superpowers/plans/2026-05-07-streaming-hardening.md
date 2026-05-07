# Streaming I/O Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Amendments live in [`../lessons-learned/2026-05-04-import-streaming-lessons.md`](../lessons-learned/2026-05-04-import-streaming-lessons.md)** — read it before implementing any PR in this series. Plan body stays static; amendments evolve there. A15 + A16 (post-merge retrospective) are the direct source of this initiative.

**Goal:** Close the four P0-class correctness gaps surfaced in the [post-merge codex retrospective](../lessons-learned/2026-05-07-codex-streaming-retrospective.md) before v0.3 piles new features on top. The headline `--memory-budget` UX must be empirically true (not just nominally), and the bundle-extraction path must not be vulnerable to attacker-controlled sidecar content.

**Architecture:** Fixes-shaped, not architecture-shaped. The streaming pipeline already exists; this work lands targeted contract changes inside `pkg/diff/sidecar.go`, `pkg/importer/{compose.go, admission.go, importer.go}`, `internal/archive/reader.go`, and adds one new helper file `pkg/importer/baseline_preflight.go`. One new exported type `*diff.ErrBlobIncompletelyConsumed`. No new packages, no new CLI flags.

**Tech Stack:** Go 1.25, `opencontainers/go-digest` (`Digest.Validate()` is the canonical check), existing streaming pipeline (`internal/admission`, `pkg/importer.BaselineSpool`), existing scale-bench harness (`.github/workflows/scale-bench.yml` + `pkg/importer/scale_bench_test.go`).

**Spec:** [`../specs/2026-05-07-streaming-hardening-design.md`](../specs/2026-05-07-streaming-hardening-design.md). Every task in this plan traces to a section number in the spec.

**Branching:** Each PR below is a feature branch off `master`. Names: `feat/hardening-pr1-digest-validate`, `feat/hardening-pr2-baseline-stream`, `feat/hardening-pr3-verify-on-close`, `feat/hardening-pr4-baseline-preflight`, `feat/hardening-pr5-drift`. Merge order matches PR order; later PRs rebase if earlier ones land first.

**Out of scope (deferred to a separate hygiene cycle):** workerpool post-cancel goroutine noise, AdmissionPool goroutine-before-gate, BaselineSpool surface unification, atomic-publication symmetry, config precedence docstring. Severity is low and grouping them yields a cleaner narrative than scattering across this hardening cycle.

---

## File map

**New files:**
- `pkg/diff/testdata/malicious-sidecars/path-traversal-algorithm.json`
- `pkg/diff/testdata/malicious-sidecars/uppercase-encoded.json`
- `pkg/diff/testdata/malicious-sidecars/empty-algorithm.json`
- `pkg/diff/testdata/malicious-sidecars/malformed-encoded.json`
- `pkg/diff/testdata/malicious-sidecars/unknown-algorithm.json`
- `pkg/importer/baseline_preflight.go` — per-image baseline-completeness check.
- `pkg/importer/baseline_preflight_test.go` — unit coverage.
- `cmd/unbundle_preflight_baseline_completeness_integration_test.go` — cross-image masking integration test.

**Modified files:**
- `pkg/diff/sidecar.go` — `digest.Validate()` calls in `validate` + `validateBlobEntry`.
- `pkg/diff/sidecar_test.go` — table-driven malicious-sidecar test + canonical round-trip.
- `pkg/diff/errors.go` — `*ErrBlobIncompletelyConsumed` type.
- `pkg/importer/compose.go` — `fetchVerifiedBaselineBlob` streaming return; `verifyingReadCloser.verified` field + `Close` fail-closed.
- `pkg/importer/compose_test.go` — close-before-EOF + path-backed-reader regressions.
- `pkg/importer/admission.go` — estimator counts baseline-only layer sizes.
- `pkg/importer/admission_test.go` — estimator regression tests.
- `pkg/importer/importer.go` — preflight ordering (PR4); admission scope (PR5).
- `pkg/importer/extract.go` — extraction relocation (PR5).
- `pkg/importer/preflight.go` — new `PreflightBaselineMissing` status + integration with baseline preflight.
- `internal/archive/reader.go` — `writeFile` short-copy reject.
- `internal/archive/reader_test.go` — truncated tar regression.
- `pkg/importer/scale_bench_test.go` — `TestImport_ScaleBaselineOnlyReuse4GiB`.
- `.github/workflows/scale-bench.yml` — wire the new bench step.
- `cmd/unbundle_preflight_integration_test.go` — remove `--workers=1` pins.
- `cmd/spool_flags.go` — `--memory-budget` help text appendix on import side.
- `docs/superpowers/specs/2026-05-04-import-streaming-io-design.md` — §13 acceptance #11 amendment (PR4).
- `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md` — A10 closure note (PR4); A15-A16 already landed.

---

## Pre-flight (run once before PR1)

- [ ] **Step 0.1: Read the spec end-to-end.** [`../specs/2026-05-07-streaming-hardening-design.md`](../specs/2026-05-07-streaming-hardening-design.md). Every PR below traces to spec §5.X.
- [ ] **Step 0.2: Read the lessons-learned doc end-to-end.** [`../lessons-learned/2026-05-04-import-streaming-lessons.md`](../lessons-learned/2026-05-04-import-streaming-lessons.md). A1-A14 carry over; A15-A16 motivate this work. Pay attention to:
  - **A1** — `errgroup.WithContext` does NOT auto-record `ctx.Err` on cancel. Any code path that drops a Submit on cancel must do `eg.Go(func() error { return err })`.
  - **A4** — re-wrap `*diff.ErrBaselineBlobDigestMismatch` at call sites that surface to operators. PR2 must preserve this.
  - **A6** — `BlobInfoCache` MUST be forwarded into spool fetch closures; nil panics in docker registry transport.
  - **A8** — `MaxParallelDownloads = 1` is required when `HasThreadSafeGetBlob = true`; never weaken this.
  - **A14 / amendment-13** — don't ship lifted helpers without callers. Every new helper must be wired the same PR.
- [ ] **Step 0.3: Read the codex retrospective.** [`../lessons-learned/2026-05-07-codex-streaming-retrospective.md`](../lessons-learned/2026-05-07-codex-streaming-retrospective.md). The verdict table grounds each finding to actual file:line.
- [ ] **Step 0.4: Confirm baseline tests pass on master.** Run `go test ./...` from repo root. Expected: all green. If not, the master branch has a regression to fix BEFORE starting any hardening PR.
- [ ] **Step 0.5: Confirm tooling.** `golangci-lint --version`, `go version` (≥ 1.25), `zstd --version` (≥ 1.5).

---

## PR 1: Sidecar digest validation (P0 #4)

**Goal of this PR:** Add `digest.Validate()` calls in `pkg/diff/sidecar.go::validate` and `validateBlobEntry` against every `digest.Digest` field. Reject any sidecar that defines a non-canonical digest, before the importer's path-build sites can consume it. Ship a five-variant malicious-sidecar regression corpus.

**Why first:** smallest fix; security boundary; no upstream dependencies on streaming work; lands in two pure-domain files.

### Critical files

| File | Action | Insertion site |
|---|---|---|
| `pkg/diff/sidecar.go` | MODIFY | `validate()` lines 60-101 — add `digest.Validate()` calls on Blobs map keys, Target/Baseline ManifestDigest. `validateBlobEntry` lines 103-134 — add `digest.Validate()` on `b.PatchFromDigest` for `EncodingPatch`. |
| `pkg/diff/sidecar_test.go` | MODIFY | Append `TestSidecarValidate_RejectsMaliciousDigests` (table-driven) and `TestSidecarValidate_CanonicalRoundTrip`. |
| `pkg/diff/testdata/malicious-sidecars/` | NEW DIR | Five `.json` fixture files; one per attack variant. |

### Lessons-from-A* to bake into the implementer prompt

- **A14 / amendment-13** — every test added must execute on the actual code path; don't ship unreachable helpers.
- **No `Co-Authored-By: Claude` and no `🤖 Generated with Claude Code` trailers.**
- **Pre-commit hooks auto-fix EOF/whitespace** — re-stage and re-commit, never `--no-verify`.

### Steps

- [ ] **Step 1.1: Create the malicious-sidecar fixture directory.** `mkdir -p pkg/diff/testdata/malicious-sidecars/`.

- [ ] **Step 1.2: Write `path-traversal-algorithm.json`.** Minimum-valid-shape sidecar with `images[0].target.manifest_digest` set to `"../../etc/passwd:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"`. The encoded portion is a syntactically-valid 64-char hex; the algorithm is the attack. Keep `images`, `blobs`, `feature: "bundle"`, `version: "v1"`, `platform: "linux/amd64"`.

- [ ] **Step 1.3: Write `uppercase-encoded.json`.** Same shape; `manifest_digest` set to `"sha256:ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890"`. go-digest enforces lowercase encoded form for `sha256`.

- [ ] **Step 1.4: Write `empty-algorithm.json`.** `manifest_digest` set to `":abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"`.

- [ ] **Step 1.5: Write `malformed-encoded.json`.** `manifest_digest` set to `"sha256:nothex"` — encoded portion is too short and non-hex.

- [ ] **Step 1.6: Write `unknown-algorithm.json`.** `manifest_digest` set to `"md5:abcdef1234567890abcdef1234567890"` — md5 is not a registered go-digest algorithm.

- [ ] **Step 1.7: Modify `pkg/diff/sidecar.go::validate`.** Add a helper `validateDigest(field string, d digest.Digest) error`:

```go
func validateDigest(field string, d digest.Digest) error {
    if err := d.Validate(); err != nil {
        return &ErrSidecarSchema{Reason: fmt.Sprintf(
            "%s %q is not a valid digest: %v", field, d, err)}
    }
    return nil
}
```

In `validate()`, after the existing `nameRegex` + non-empty checks for image i, call `validateDigest("images[i].target.manifest_digest", img.Target.ManifestDigest)` and `validateDigest("images[i].baseline.manifest_digest", img.Baseline.ManifestDigest)`. After the existing `s.Blobs == nil` check, iterate `s.Blobs` keys and call `validateDigest(fmt.Sprintf("blobs[%s] key", d), d)` on each.

- [ ] **Step 1.8: Modify `pkg/diff/sidecar.go::validateBlobEntry`.** In the `EncodingPatch` branch, after the existing `b.PatchFromDigest == ""` check, call `validateDigest(fmt.Sprintf("blobs[%s].patch_from_digest", d), b.PatchFromDigest)`.

- [ ] **Step 1.9: Write `TestSidecarValidate_RejectsMaliciousDigests`.** Table-driven over the five fixture files in `testdata/malicious-sidecars/`:

```go
func TestSidecarValidate_RejectsMaliciousDigests(t *testing.T) {
    entries, err := os.ReadDir("testdata/malicious-sidecars")
    require.NoError(t, err)
    require.NotEmpty(t, entries)
    for _, e := range entries {
        if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") { continue }
        t.Run(strings.TrimSuffix(e.Name(), ".json"), func(t *testing.T) {
            raw, err := os.ReadFile(filepath.Join("testdata/malicious-sidecars", e.Name()))
            require.NoError(t, err)
            _, err = ParseSidecar(raw)
            require.Error(t, err)
            var sErr *ErrSidecarSchema
            require.ErrorAs(t, err, &sErr,
                "want *ErrSidecarSchema for %s, got %T", e.Name(), err)
        })
    }
}
```

- [ ] **Step 1.10: Write `TestSidecarValidate_CanonicalRoundTrip`.** Build a minimum-valid Sidecar in-process (matching the shape `pkg/exporter/assemble.go::assembleSidecar` produces), `Marshal` it, then `ParseSidecar` the result; assert no error and equality on the surface fields. This guards against the fixture-only test passing while breaking real exporter output.

- [ ] **Step 1.11: Run unit tests.** `go test -count=1 -run TestSidecarValidate ./pkg/diff/...` — expect 6 passing sub-tests (5 malicious + 1 canonical).

- [ ] **Step 1.12: Run full pkg/diff suite.** `go test -count=1 ./pkg/diff/...`. Existing tests must stay green (no schema-shape regressions).

- [ ] **Step 1.13: Run full importer suite.** `go test -count=1 ./pkg/importer/...`. The importer should not regress; sidecars produced by current tests are canonical and parse fine.

- [ ] **Step 1.14: Run lint.** `golangci-lint run ./pkg/diff/...`.

- [ ] **Step 1.15: Commit.** Message:

```
feat(diff): validate digest fields in sidecar schema (hardening PR1)

Closes the path-traversal vector surfaced by the post-merge codex
retrospective: Sidecar.Blobs keys, Target/Baseline manifest digests,
and BlobEntry.PatchFromDigest are now validated via go-digest's
Digest.Validate() before any downstream consumer joins them into a
filesystem path. Bundle signature verification does not protect
against a malicious sidecar — the signature covers the attacker's
bytes — so the validation must happen at parse time on both write
(Sidecar.Marshal) and read (ParseSidecar) paths.

A five-variant regression corpus in testdata/malicious-sidecars/
covers path-traversal algorithm, uppercase encoded form, empty
algorithm, malformed encoded form, and unknown hash algorithm.
TestSidecarValidate_CanonicalRoundTrip guards against the corpus
test passing while breaking real exporter output.

Spec: docs/superpowers/specs/2026-05-07-streaming-hardening-design.md §5.1
```

- [ ] **Step 1.16: Push and open PR.** `git push -u origin feat/hardening-pr1-digest-validate`; `gh pr create --base master`. Title: `feat(diff): sidecar digest validation (hardening PR1)`. Body cites spec §5.1 + lessons doc A15-A16 source.

### Verification (gates the merge)

```bash
go test -count=1 ./...                                     # green across all packages
go test -count=1 -run TestSidecarValidate ./pkg/diff/... -v # 6 sub-tests pass
golangci-lint run ./pkg/diff/...                           # no new findings
```

After CI green and review approve, merge. Pause for user.

---

## PR 2: Baseline-only reuse streams (P0 #1, A16)

**Goal of this PR:** Stop slurping baseline-only reuse blobs into RAM. Change `fetchVerifiedBaselineBlob` to return a path-backed `io.ReadCloser`; teach the per-image admission estimator that baseline-only layers contribute their uncompressed size to peak RSS. Land an apply-side scale-bench fixture that proves the budget contract holds for baseline-only reuse.

### Critical files

| File | Action | Insertion site |
|---|---|---|
| `pkg/importer/compose.go` | MODIFY | `fetchVerifiedBaselineBlob` (lines 307-322) — return `(io.ReadCloser, int64, error)` from `os.Open` + `Stat`. `GetBlob` baseline-only branch (lines 97-110) — propagate the reader directly; remove `bytes.NewReader` + `io.NopCloser`. |
| `pkg/importer/admission.go` | MODIFY | `estimatePerImageRSS` (lines 45-69) — extend the layer loop to compute baseline-only contribution = `LayerRef.Size`; per-image result = max(shipped_rss, baseline_only_size). |
| `pkg/importer/admission_test.go` | MODIFY | Append `TestEstimatePerImageRSS_CountsBaselineOnlyLayers` and `TestEstimatePerImageRSS_ZeroLayers`. |
| `pkg/importer/compose_test.go` | MODIFY | Append `TestGetBlob_BaselineOnlyReturnsPathBackedReader`. |
| `pkg/importer/scale_bench_test.go` | MODIFY | Append `TestImport_ScaleBaselineOnlyReuse4GiB` (fixture + budget assertion + RSS measurement). |
| `.github/workflows/scale-bench.yml` | MODIFY | Add a step "Apply (baseline-only-reuse scale)" after the existing 4-GiB-layer apply step. |
| `cmd/spool_flags.go` | MODIFY | `importSpoolHelp` constant + `installImportSpoolFlags`'s `--memory-budget` flag-help string — append "Includes baseline-only reuse layer sizes." |

### Lessons-from-A* to bake into the implementer prompt

- **A4** — re-wrap `*diff.ErrBaselineBlobDigestMismatch` with `ImageName` at the call site. Existing `errors.As` mutation pattern stays intact for THIS PR; PR5 is where it's refactored to non-mutating. Do NOT touch the mutation in PR2.
- **A6** — forward `BlobInfoCache` into the spool fetch closure unchanged.
- **A14 / amendment-13** — the new return shape must be wired through `GetBlob` in the SAME PR. No partial migration.

### Steps

- [ ] **Step 2.1: `advisor()` pre-flight.** Run with the spec §5.2 + this PR's task text loaded. Expect 1-2 plan corrections; surface them in the PR description as "post-advisor" notes.

- [ ] **Step 2.2: Modify `fetchVerifiedBaselineBlob` signature.** Change return type from `([]byte, error)` to `(io.ReadCloser, int64, error)`. New body:

```go
func (s *bundleImageSource) fetchVerifiedBaselineBlob(
    ctx context.Context, d digest.Digest, cache types.BlobInfoCache,
) (io.ReadCloser, int64, error) {
    path, err := s.spool.GetOrSpool(ctx, d, func() (io.ReadCloser, error) {
        rc, _, gerr := s.baseline.GetBlob(ctx, types.BlobInfo{Digest: d}, cache)
        return rc, gerr
    })
    if err != nil {
        var mismatch *diff.ErrBaselineBlobDigestMismatch
        if errors.As(err, &mismatch) && mismatch.ImageName == "" {
            mismatch.ImageName = s.imageName
        }
        return nil, 0, err
    }
    f, err := os.Open(path)
    if err != nil {
        return nil, 0, fmt.Errorf("open baseline spool %s: %w", d, err)
    }
    st, err := f.Stat()
    if err != nil {
        _ = f.Close()
        return nil, 0, fmt.Errorf("stat baseline spool %s: %w", d, err)
    }
    return f, st.Size(), nil
}
```

The TODO comment block at lines 298-301 is REMOVED (its claim is now resolved).

- [ ] **Step 2.3: Modify `GetBlob` baseline-only branch.** Replace lines 99-109:

```go
rc, size, err := s.fetchVerifiedBaselineBlob(ctx, info.Digest, cache)
if err != nil {
    if isBlobNotFound(err) {
        return nil, 0, &ErrMissingBaselineReuseLayer{
            ImageName:   s.imageName,
            LayerDigest: info.Digest,
        }
    }
    return nil, 0, fmt.Errorf("baseline serve %s: %w", info.Digest, err)
}
return rc, size, nil
```

Drop the `bytes.NewReader` + `io.NopCloser` wrapping. The new `rc` is a `*os.File` and is a valid `io.ReadCloser` directly. Remove `"bytes"` from the imports if it becomes unused.

- [ ] **Step 2.4: Modify `estimatePerImageRSS`.** Replace the layer-walk loop (lines 55-67):

```go
var maxEst int64
for _, ld := range layers {
    var contribution int64
    if entry, ok := blobs[ld.Digest]; ok {
        wl := exporter.ResolveWindowLog(userWindowLog, entry.Size)
        contribution = exporter.EstimateRSSForWindowLog(wl)
    } else {
        // Baseline-only reuse: the spooled file is read into the
        // destination during apply; OS page cache for those bytes
        // counts against the per-image RSS envelope.
        contribution = ld.Size
    }
    if contribution > maxEst {
        maxEst = contribution
    }
}
return maxEst, nil
```

Note: the existing `for _, ld := range layers` iterates `LayerRef` (digest + size). The current code accesses `blobs[ld]` which is a type error — the current `blobs` map is keyed on `digest.Digest`, and `ld` is `LayerRef`. **Read the existing code carefully**; the loop variable shape may be `for _, ld := range layers` with `ld` already being `digest.Digest`. Match the existing iteration shape. If `LayerRef` was returned, both `.Digest` and `.Size` are accessible.

- [ ] **Step 2.5: Update `estimatePerImageRSS` doc comment.** The block doc currently says baseline-only layers "produce no decoder RSS" and "skip layers absent from sidecar.Blobs." Rewrite to:

> walks every layer in the image's target manifest and contributes the layer's RSS envelope: shipped layers contribute `EstimateRSSForWindowLog(ResolveWindowLog(...))` (the encoder-committed window); baseline-only layers contribute `LayerRef.Size` (the page cache held while copy.Image streams the spooled file). copy.Image processes layers serially within one image (`MaxParallelDownloads: 1` enforced in `composeImage`), so per-image peak is the MAX across both bucket types.

- [ ] **Step 2.6: Write `TestEstimatePerImageRSS_CountsBaselineOnlyLayers`.** Build a minimum target manifest with two layers: one shipped (in `Blobs`) at `windowLog=27` size, one baseline-only at `LayerRef.Size = 4 << 30` (4 GiB). Assert estimator returns `>= 4 << 30`. Without the change, the test fails (estimator returns the shipped layer's RSS only).

- [ ] **Step 2.7: Write `TestEstimatePerImageRSS_ZeroLayers`.** Image with empty layers list → estimator returns `0` (no panic). Lock the existing behavior so the rewrite doesn't drop it.

- [ ] **Step 2.8: Write `TestGetBlob_BaselineOnlyReturnsPathBackedReader`.** Construct a `bundleImageSource` whose spool already contains a baseline-only digest's spooled file. Call `GetBlob(ctx, BlobInfo{Digest: d}, nil)`. Assert the returned `io.ReadCloser` is `*os.File`-typed (use a type assertion). Assert reading + closing works without buffering the full content (compare RSS before and after; or simply assert `Stat` on the underlying file matches the size returned).

- [ ] **Step 2.9: Write `TestImport_ScaleBaselineOnlyReuse4GiB`.** New scale-bench-style test in `pkg/importer/scale_bench_test.go`:
  - **Build phase.** Construct a bundle with two images. Each image has one baseline-only layer of 4 GiB (synthetic blob — random bytes hashed to a digest the baseline source serves). Bundle ships zero blobs (everything is baseline reuse).
  - **Run phase.** Call `Import()` with `--workers=2 --memory-budget=8GiB`. Use `runtime.MemStats` peak heap measurement around the call.
  - **Assert.** Peak HeapAlloc + HeapInuse stays ≤ 6 GiB (8 GiB budget × 0.75 — heap-only excludes OS page cache; the test does NOT assert RSS-via-rusage because that is OS-dependent and includes page cache).
  - **Reject sub-case.** Same fixture, `--workers=2 --memory-budget=2GiB`. Assert `Import()` returns `*errs.UserError` with `Cat == errs.CategoryUser` and the error message references both image names.
  - Mark with `t.Skip` UNLESS `DIFFAH_SCALE_BENCH=1` env (matches existing scale-bench convention) — this test allocates 8 GiB of fixture data and is nightly-only.

- [ ] **Step 2.10: Wire the new bench step into `.github/workflows/scale-bench.yml`.** Add after the existing apply step:

```yaml
      - name: Apply (baseline-only-reuse scale)
        env:
          DIFFAH_SCALE_BENCH: "1"
        run: go test -count=1 -run TestImport_ScaleBaselineOnlyReuse4GiB ./pkg/importer/... -v -timeout 30m
```

- [ ] **Step 2.11: Update `--memory-budget` help text.** In `cmd/spool_flags.go`, append " Includes baseline-only reuse layer sizes." to the import-side `--memory-budget` flag description string in `installImportSpoolFlags` (around line 82) AND to the `importSpoolHelp` constant (around line 67). Bundle/diff side help text is unchanged.

- [ ] **Step 2.12: Run targeted unit tests.** `go test -count=1 -run "TestEstimatePerImageRSS|TestGetBlob_BaselineOnly" ./pkg/importer/... -v`. Expect 3 passing sub-tests.

- [ ] **Step 2.13: Run scale-bench locally (skipped by default).** `DIFFAH_SCALE_BENCH=1 go test -count=1 -run TestImport_ScaleBaselineOnlyReuse4GiB ./pkg/importer/... -v -timeout 30m`. Skip on dev machine if 8 GiB RAM is unavailable; the nightly CI is the gate.

- [ ] **Step 2.14: Run full suite.** `go test -count=1 ./...`.

- [ ] **Step 2.15: Verify no FD leak under cancellation.** Add a sub-test `TestGetBlob_BaselineOnlyClosesFileOnEarlyReturn` that opens a baseline-only reader, closes it without reading, then asserts the spool path's open-file count via `lsof`-equivalent (use `runtime.NumGoroutine` baseline + a lightweight Read-of-1-byte on the same path to confirm the file descriptor freed). Skip if the harness cannot reliably check; document why.

- [ ] **Step 2.16: Run lint.** `golangci-lint run ./pkg/importer/...`.

- [ ] **Step 2.17: Commit.** Message:

```
feat(importer): stream baseline-only reuse + count layer size in budget (hardening PR2)

Closes A16: fetchVerifiedBaselineBlob no longer slurps the spooled
file into a []byte. The function returns a path-backed *os.File via
os.Open + Stat, propagated through bundleImageSource.GetBlob. The
per-image admission estimator at estimatePerImageRSS now contributes
baseline-only layer sizes (LayerRef.Size from the parsed target
manifest) into the per-image peak RSS envelope, alongside shipped
layers' windowLog-based estimates. Per-image peak is MAX across both
bucket types, matching copy.Image's serial intra-image layer copy.

Worst case before this PR: 8 workers x 4 GiB baseline-only layer
allocated ~32 GiB while the budget gate reported near-zero estimate.
After this PR: heap stays bounded by --memory-budget; nightly
TestImport_ScaleBaselineOnlyReuse4GiB asserts the contract.

Spec: docs/superpowers/specs/2026-05-07-streaming-hardening-design.md §5.2
```

- [ ] **Step 2.18: Push and open PR.** Title: `feat(importer): stream baseline-only reuse (hardening PR2)`.

### Verification

```bash
go test -count=1 ./...                                              # green
go test -count=1 -run "TestEstimatePerImageRSS|TestGetBlob_BaselineOnly" ./pkg/importer/... -v
DIFFAH_SCALE_BENCH=1 go test -count=1 -run TestImport_ScaleBaselineOnlyReuse4GiB ./pkg/importer/... -v -timeout 30m
golangci-lint run ./pkg/importer/...
diffah apply --help | grep -A1 -- "--memory-budget"   # help text now mentions baseline-only
```

---

## PR 3: Verify-on-close + archive short-copy reject (P0 #3 + Conc C)

**Goal of this PR:** Make integrity verification non-skippable. `verifyingReadCloser.Close` must return a typed error if the reader was closed without reaching `io.EOF`. `internal/archive/reader.go::writeFile` must reject truncated tar entries instead of silently accepting them. Add the new `*diff.ErrBlobIncompletelyConsumed` type.

### Critical files

| File | Action | Insertion site |
|---|---|---|
| `pkg/diff/errors.go` | MODIFY | Add `ErrBlobIncompletelyConsumed` struct, `Error()`, `Category()`. Wire into the existing classification table if there is one (check `pkg/diff/errors.go` and `pkg/diff/errs/`). |
| `pkg/importer/compose.go` | MODIFY | `verifyingReadCloser` (lines 239-285) — add `verified bool` field; set in `Read` on EOF branch; consult in `Close`. |
| `pkg/importer/compose_test.go` | MODIFY | Append `TestVerifyingReadCloser_CloseBeforeEOFSurfacesError` and `TestVerifyingReadCloser_CloseAfterEOFReturnsNil`. |
| `internal/archive/reader.go` | MODIFY | `writeFile` (lines 129-138) — drop `&& !errors.Is(err, io.EOF)` clause; update wrap message to mention truncation. |
| `internal/archive/reader_test.go` | MODIFY | Append `TestExtract_TruncatedTarEntryRejected`. |

### Lessons-from-A* to bake into the implementer prompt

- **A7** — `verifyingReadCloser.mismatchErr` already has a defensive `default` case for unknown `readerKind`. The new `incompleteErr` path must mirror this defensive shape — adding a new kind without wiring an incomplete-error variant must NOT silently downgrade an unverified close to "no error."
- **No `Co-Authored-By: Claude` and no `🤖 Generated with Claude Code` trailers.**

### Steps

- [ ] **Step 3.1: `advisor()` pre-flight.** Spec §5.3 + this PR text.

- [ ] **Step 3.2: Add `ErrBlobIncompletelyConsumed` to `pkg/diff/errors.go`.** Read the existing error type pattern in that file; mirror it. Sketch:

```go
// ErrBlobIncompletelyConsumed is returned by verifyingReadCloser.Close
// when the reader was closed without ever reaching io.EOF. The digest
// integrity check runs only on EOF, so an early close means the blob
// was never verified — a corrupt payload could otherwise be silently
// trusted by a downstream consumer that errored out mid-read.
//
// Kind is "shipped" for full bundle blobs (kindShipped in
// verifyingReadCloser) or "assembled" for patch-decode outputs
// (kindAssembled). ImageName is empty for assembled blobs because
// patch reconstruction is image-agnostic at the spool layer.
type ErrBlobIncompletelyConsumed struct {
    Kind      string
    Digest    string
    ImageName string
}

func (e *ErrBlobIncompletelyConsumed) Error() string {
    if e.ImageName != "" {
        return fmt.Sprintf("%s blob %s closed before EOF; integrity unverified (image=%s)",
            e.Kind, e.Digest, e.ImageName)
    }
    return fmt.Sprintf("%s blob %s closed before EOF; integrity unverified",
        e.Kind, e.Digest)
}

func (e *ErrBlobIncompletelyConsumed) Category() errs.Category {
    return errs.CategoryContent
}
```

- [ ] **Step 3.3: Modify `verifyingReadCloser` struct.** Add `verified bool` field after `kind` / `scratchPath`:

```go
type verifyingReadCloser struct {
    f           *os.File
    expected    digest.Digest
    hasher      hash.Hash
    imageName   string
    kind        readerKind
    scratchPath string
    verified    bool // set true on the io.EOF branch in Read
}
```

- [ ] **Step 3.4: Modify `Read`.** In the existing `if errors.Is(err, io.EOF)` branch, set `r.verified = true` IMMEDIATELY before the digest comparison so a mismatch still flips the flag (the consumer saw EOF; verification ran; the only ambiguity is whether digest matched, and that has its own error path):

```go
if errors.Is(err, io.EOF) {
    r.verified = true
    got := digest.NewDigest(r.expected.Algorithm(), r.hasher)
    if got != r.expected {
        return n, r.mismatchErr(got)
    }
}
```

- [ ] **Step 3.5: Modify `Close`.** Add the unverified branch:

```go
func (r *verifyingReadCloser) Close() error {
    err := r.f.Close()
    if r.scratchPath != "" {
        _ = os.Remove(r.scratchPath)
    }
    if err == nil && !r.verified {
        return r.incompleteErr()
    }
    return err
}

func (r *verifyingReadCloser) incompleteErr() error {
    switch r.kind {
    case kindShipped:
        return &diff.ErrBlobIncompletelyConsumed{
            Kind:      "shipped",
            Digest:    r.expected.String(),
            ImageName: r.imageName,
        }
    case kindAssembled:
        return &diff.ErrBlobIncompletelyConsumed{
            Kind:      "assembled",
            Digest:    r.expected.String(),
        }
    }
    // Defensive: a future kind addition that forgets to wire a sentinel
    // must not silently downgrade an unverified close to "no error".
    return fmt.Errorf("verifyingReadCloser: closed unverified, unknown kind %d for %s",
        r.kind, r.expected)
}
```

- [ ] **Step 3.6: Write `TestVerifyingReadCloser_CloseBeforeEOFSurfacesError`.**

```go
func TestVerifyingReadCloser_CloseBeforeEOFSurfacesError(t *testing.T) {
    payload := []byte(strings.Repeat("verifier-test-payload-", 64)) // ~1.4 KiB
    d := digest.FromBytes(payload)
    dir := t.TempDir()
    path := filepath.Join(dir, d.Encoded())
    require.NoError(t, os.WriteFile(path, payload, 0o600))
    f, err := os.Open(path)
    require.NoError(t, err)
    r := &verifyingReadCloser{
        f: f, expected: d, hasher: d.Algorithm().Hash(),
        imageName: "svc-test", kind: kindShipped,
    }
    // Read half; do NOT reach EOF.
    buf := make([]byte, len(payload)/2)
    n, readErr := r.Read(buf)
    require.NoError(t, readErr)
    require.Equal(t, len(payload)/2, n)
    closeErr := r.Close()
    var inc *diff.ErrBlobIncompletelyConsumed
    require.ErrorAs(t, closeErr, &inc)
    require.Equal(t, "shipped", inc.Kind)
    require.Equal(t, "svc-test", inc.ImageName)
}
```

- [ ] **Step 3.7: Write `TestVerifyingReadCloser_CloseAfterEOFReturnsNil`.** Same fixture; read all bytes (look for `io.EOF`); call `Close`; assert nil error. Locks in the happy path.

- [ ] **Step 3.8: Modify `internal/archive/reader.go::writeFile`.** Replace lines 135-138:

```go
// Before:
if _, err := io.CopyN(f, r, size); err != nil && !errors.Is(err, io.EOF) {
    return fmt.Errorf("write %s: %w", path, err)
}

// After:
if _, err := io.CopyN(f, r, size); err != nil {
    return fmt.Errorf("write %s (truncated tar entry?): %w", path, err)
}
```

- [ ] **Step 3.9: Write `TestExtract_TruncatedTarEntryRejected`.** Construct a tar archive in-memory:
  - Header for entry "blob.bin" with `Size: 1024`.
  - Body: only 512 bytes of content (followed by next header instead of completion).
  - Write to a temp file.
  - Call `archive.Extract(path, t.TempDir())`.
  - Assert error returned; assert `strings.Contains(err.Error(), "truncated tar entry")`.

- [ ] **Step 3.10: Run targeted unit tests.** `go test -count=1 -run "TestVerifyingReadCloser|TestExtract_Truncated" ./pkg/importer/... ./internal/archive/... -v`.

- [ ] **Step 3.11: Run full suite.** `go test -count=1 ./...`.

- [ ] **Step 3.12: Run lint.** `golangci-lint run ./pkg/importer/... ./internal/archive/... ./pkg/diff/...`.

- [ ] **Step 3.13: Commit.** Message:

```
feat(importer,archive): verify-on-close + reject truncated tar entries (hardening PR3)

verifyingReadCloser.Close now returns *diff.ErrBlobIncompletelyConsumed
when the reader was closed without reaching io.EOF. Previously the
digest check ran ONLY on the EOF branch in Read; an early close (e.g.
copy.Image errors mid-stream, ctx cancellation, destination put fail)
silently trusted the unverified payload. The new sentinel surfaces as
a CategoryContent error, so the operator-facing failure points at the
real cause instead of a downstream symptom.

internal/archive/reader.go::writeFile no longer ignores io.EOF from
io.CopyN. CopyN returns EOF only on actual truncation when size is
positive; treating it as success extracted short blob files for
truncated bundles, deferring the failure to a downstream digest
mismatch with the wrong root cause. Now Extract returns an error
mentioning "truncated tar entry" when the bundle is malformed.

Spec: docs/superpowers/specs/2026-05-07-streaming-hardening-design.md §5.3
```

### Verification

```bash
go test -count=1 ./...
go test -count=1 -run "TestVerifyingReadCloser|TestExtract_Truncated" ./pkg/importer/... ./internal/archive/... -v
golangci-lint run ./pkg/importer/... ./internal/archive/... ./pkg/diff/...
```

---

## PR 4: Per-image baseline-completeness preflight (P0 #2)

**Goal of this PR:** Run a per-image baseline-completeness check before `importEachImage` constructs its admission pool, so cross-image baseline masking through the digest-keyed shared spool is impossible at any worker count. Unpin the two `--workers=1` tests A10 named. Amend the import-streaming spec.

### Critical files

| File | Action | Insertion site |
|---|---|---|
| `pkg/importer/baseline_preflight.go` | NEW | `runBaselinePreflight(ctx, applyList, resolvedByName, bundle)` returns updated `[]string` (filtered) + `map[string]PreflightResult` (additions). |
| `pkg/importer/baseline_preflight_test.go` | NEW | Three sub-tests covering complete / B-missing / transport-error paths. |
| `pkg/importer/preflight.go` | MODIFY | Add `PreflightBaselineMissing` status constant and its mapping in `preflightResultToErr`. |
| `pkg/importer/importer.go` | MODIFY | `Import()` (lines 124-148) — call `runBaselinePreflight` after `splitPreflightResults`, merge into the existing flow. |
| `cmd/unbundle_preflight_baseline_completeness_integration_test.go` | NEW | Two-image cross-image-masking test, partial + strict modes. |
| `cmd/unbundle_preflight_integration_test.go` | MODIFY | Remove `--workers=1` overrides at lines 59-68 and 122-127; assert default `--workers=8` behavior. |
| `docs/superpowers/specs/2026-05-04-import-streaming-io-design.md` | MODIFY | Append `§13.11` acceptance item; clarify §13.6 to mention per-image. |
| `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md` | MODIFY | Append "Closure note: A10 closed by hardening PR4" inline at the end of A10. |

### Lessons-from-A* to bake into the implementer prompt

- **A1** — any errgroup-style fan-out for the preflight checks must do `eg.Go(func() error { return err })` post-cancel. Recommended: keep preflight single-goroutine to avoid this hazard entirely; the cost is small.
- **A6** — every `GetBlob` call in the preflight must forward a non-nil `BlobInfoCache`. Reuse `none.NoCache` from `containers/image` if a per-Import cache is unavailable.
- **A10 reverse pointer** — this PR closes A10's deferred follow-up. Update the lessons doc inline.

### Steps

- [ ] **Step 4.1: `advisor()` pre-flight.** Spec §5.4 + this PR text.

- [ ] **Step 4.2: Add `PreflightBaselineMissing` to `pkg/importer/preflight.go`.** Find the existing `PreflightStatus` type and constants (likely an iota block or string-typed constants). Add:

```go
const (
    ... // existing values
    PreflightBaselineMissing PreflightStatus = "baseline_missing"
)
```

In `preflightResultToErr`, add a case mapping `PreflightBaselineMissing` → `*ErrMissingBaselineReuseLayer{ImageName: r.ImageName, LayerDigest: r.LayerDigest}`. The `LayerDigest` field on `PreflightResult` may not exist yet; add it.

- [ ] **Step 4.3: Create `pkg/importer/baseline_preflight.go`.** Sketch:

```go
package importer

import (
    "context"
    "fmt"
    "io"

    "github.com/opencontainers/go-digest"
    "go.podman.io/image/v5/pkg/blobinfocache/none"
    "go.podman.io/image/v5/types"

    "github.com/leosocy/diffah/pkg/diff"
)

// runBaselinePreflight checks each image in applyList against its
// resolved baseline source for the presence of every baseline-only
// layer the target manifest declares. Closes the cross-image masking
// hole (A10): the shared per-Import BaselineSpool is digest-keyed and
// cannot distinguish "image A's baseline has X" from "image B's
// baseline has X." Without this preflight, image B can read a
// previously-spooled blob from image A's fetch even though B's own
// baseline lacks it, silently bypassing B2-completeness.
//
// Returns:
//   - filteredApplyList: subset of applyList whose images all passed
//   - skipped: map[imageName]PreflightResult{Status: PreflightBaselineMissing}
//
// Existing preflight failures are NOT inspected here — this function
// runs AFTER splitPreflightResults, so applyList already excludes
// schema/sidecar failures.
func runBaselinePreflight(
    ctx context.Context,
    applyList []string,
    bundle *extractedBundle,
    resolvedByName map[string]resolvedBaseline,
) ([]string, map[string]PreflightResult) {
    var pass []string
    skipped := make(map[string]PreflightResult)

    for _, name := range applyList {
        if err := ctx.Err(); err != nil {
            // Treat ctx cancellation as a preflight failure for the
            // remaining images so the apply phase doesn't run them.
            skipped[name] = PreflightResult{
                ImageName: name,
                Status:    PreflightBaselineMissing,
                LayerDigest: "",
                CauseErr:    err,
            }
            continue
        }
        rb, ok := resolvedByName[name]
        if !ok {
            // Should not happen — splitPreflightResults guarantees
            // applyList only contains images Resolve found a baseline
            // for. Belt-and-braces: skip.
            skipped[name] = PreflightResult{
                ImageName: name,
                Status:    PreflightBaselineMissing,
                CauseErr:  fmt.Errorf("internal: no resolved baseline"),
            }
            continue
        }
        img, ok := findImage(bundle.sidecar.Images, name)
        if !ok {
            skipped[name] = PreflightResult{
                ImageName: name,
                Status:    PreflightBaselineMissing,
                CauseErr:  fmt.Errorf("internal: image not in sidecar"),
            }
            continue
        }
        layers, _, err := readSidecarTargetLayers(bundle, img)
        if err != nil {
            skipped[name] = PreflightResult{
                ImageName: name,
                Status:    PreflightBaselineMissing,
                CauseErr:  err,
            }
            continue
        }
        var missing digest.Digest
        for _, ld := range layers {
            if _, ok := bundle.sidecar.Blobs[ld.Digest]; ok {
                continue // shipped, not baseline-only
            }
            if !blobAvailable(ctx, rb.Src, ld.Digest) {
                missing = ld.Digest
                break
            }
        }
        if missing == "" {
            pass = append(pass, name)
            continue
        }
        skipped[name] = PreflightResult{
            ImageName:   name,
            Status:      PreflightBaselineMissing,
            LayerDigest: missing,
        }
    }
    return pass, skipped
}

// blobAvailable returns true if src.GetBlob succeeds for d. Reads at
// most 1 byte before closing; any 4xx/5xx surfaces as false. Uses the
// transport's own BlobInfoCache via none.NoCache (per-Import caching
// is not worth wiring for a one-shot preflight).
func blobAvailable(ctx context.Context, src types.ImageSource, d digest.Digest) bool {
    rc, _, err := src.GetBlob(ctx, types.BlobInfo{Digest: d}, none.NoCache)
    if err != nil {
        return false
    }
    var one [1]byte
    _, _ = rc.Read(one[:])
    _ = rc.Close()
    return true
}

func findImage(images []diff.ImageEntry, name string) (diff.ImageEntry, bool) {
    for _, img := range images {
        if img.Name == name {
            return img, true
        }
    }
    return diff.ImageEntry{}, false
}
```

The `PreflightResult` struct may need a `LayerDigest digest.Digest` and `CauseErr error` field — add them if missing.

- [ ] **Step 4.4: Wire into `Import()`.** In `pkg/importer/importer.go::Import` after the existing `splitPreflightResults` line (~line 139) and BEFORE `newImportSpool` / `importEachImage`:

```go
applyList, skippedByPreflight := splitPreflightResults(preflightResults)

// Per-image baseline-completeness preflight. Closes the cross-image
// masking hole the digest-keyed shared spool created (A10/A15-A16
// retrospective). Runs serially against resolved baseline sources;
// each image's baseline-only digests are availability-checked
// against THAT image's own baseline transport.
filteredApplyList, baselineSkipped := runBaselinePreflight(
    ctx, applyList, bundle, resolvedByName,
)
applyList = filteredApplyList
for name, r := range baselineSkipped {
    skippedByPreflight[name] = r
}

spool, err := newImportSpool(wd)
```

- [ ] **Step 4.5: Move admission budget check below preflight.** The existing `checkSingleImageFitsInBudget(bundle.sidecar.Images, ...)` call at lines 248-251 of `importer.go` lives inside `importEachImage`; this PR keeps it there but in PR5 it gets moved AND switched to use `applyList`. For PR4, leave the call as-is — the preflight runs before `importEachImage`, so the only behavior change is that previously-failed-baseline images are now in `skippedByPreflight` rather than reaching the budget check.

- [ ] **Step 4.6: Write `pkg/importer/baseline_preflight_test.go`.**
  - `TestRunBaselinePreflight_AllComplete` — two images, all baselines complete; assert filtered list == input list; assert empty skipped.
  - `TestRunBaselinePreflight_OneImageBMissing` — image A baseline complete, image B baseline missing one digest; assert filtered list == [A]; assert skipped[B].LayerDigest matches the missing digest.
  - `TestRunBaselinePreflight_TransportError` — mock baseline source returns an error other than not-found; assert skipped[B].CauseErr is set and Status is PreflightBaselineMissing.

- [ ] **Step 4.7: Write `cmd/unbundle_preflight_baseline_completeness_integration_test.go`.** End-to-end:
  - Build a two-image bundle. Image A: baseline "fixtures/A.tar" containing layer X. Image B: baseline "fixtures/B.tar" NOT containing X. Both target manifests reference X as baseline-only.
  - Run `diffah unbundle <bundle> ./out --workers=8` (partial mode).
  - Assert exit code 0 (partial mode allows skipped images), stderr contains B's name with PreflightBaselineMissing, exit-code mapping is per partial-mode semantics.
  - Repeat with `--strict`: assert non-zero exit; stderr contains the categorized preflight failure.

- [ ] **Step 4.8: Unpin A10 tests.** `cmd/unbundle_preflight_integration_test.go` lines 59-68 and 122-127 currently set `--workers=1`. Remove the override (or change to `--workers=8` explicitly). Existing assertions should hold; if they don't, the test was relying on the cross-image masking and needs to be re-thought (consult with the user before silently fixing).

- [ ] **Step 4.9: Amend the import-streaming spec.** `docs/superpowers/specs/2026-05-04-import-streaming-io-design.md`:

  In §13 acceptance, append:

  > 11. **Per-image baseline-completeness preflight (NEW, hardening PR4).** Before `importEachImage` constructs its admission pool, every image in `applyList` is checked against its resolved baseline source for the presence of all baseline-only layer digests declared in its target manifest. Cross-image masking through the digest-keyed shared spool is impossible at any worker count. Surfaces as `*ErrMissingBaselineReuseLayer` in the apply report under `PreflightBaselineMissing` status.

  In §13.6, change "baseline preflight" to "per-image baseline preflight" and add a parenthetical pointer to acceptance #11.

- [ ] **Step 4.10: Append A10 closure note in the lessons doc.** In `docs/superpowers/lessons-learned/2026-05-04-import-streaming-lessons.md`, at the end of the A10 paragraph add a single sentence: "**Closure: A10 follow-up landed in hardening PR4 (`feat/hardening-pr4-baseline-preflight`); the `--workers=1` pins are removed.**"

- [ ] **Step 4.11: Run targeted unit tests.** `go test -count=1 -run "TestRunBaselinePreflight|TestUnbundle_Baseline" ./pkg/importer/... ./cmd/... -v`.

- [ ] **Step 4.12: Run full suite.** `go test -count=1 ./...`.

- [ ] **Step 4.13: Run lint.** `golangci-lint run ./...`.

- [ ] **Step 4.14: Commit.** Single commit including code + doc edits:

```
feat(importer): per-image baseline-completeness preflight (hardening PR4)

Closes the cross-image masking hole the digest-keyed BaselineSpool
created (A10, A15-A16 retrospective). The shared per-Import spool
keys entries by digest only; in a multi-image bundle where image A's
baseline contains digest X but image B's baseline does NOT, image B
could read A's spooled bytes and bypass B2-completeness — observable
at --workers > 1, which is why two preflight integration tests were
pinned to --workers=1 as a stopgap.

This PR adds runBaselinePreflight (pkg/importer/baseline_preflight.go)
which walks each image in applyList serially and checks its own
resolved baseline source for the presence of every baseline-only
layer in its target manifest. Failures surface as
ErrMissingBaselineReuseLayer with the new PreflightBaselineMissing
status. The two --workers=1 pins in unbundle_preflight_integration_test.go
are removed.

Spec amendment: docs/superpowers/specs/2026-05-04-import-streaming-io-design.md
adds §13 acceptance #11. Lessons doc A10 gets an inline closure note.

Spec: docs/superpowers/specs/2026-05-07-streaming-hardening-design.md §5.4
```

### Verification

```bash
go test -count=1 ./...
go test -count=1 -run "TestRunBaselinePreflight" ./pkg/importer/... -v
go test -count=1 -run "TestUnbundle_Baseline" ./cmd/... -v
go test -count=1 ./cmd/...    # the unpinned tests must pass at --workers=8
golangci-lint run ./...
```

---

## PR 5 (optional): Drift cleanup

**Goal of this PR:** Land three architectural drift items the codex retrospective surfaced. NOT required for v0.3 readiness; ships when the cycle has slack.

### Critical files

| File | Action | Drift item |
|---|---|---|
| `pkg/importer/extract.go` | MODIFY | #1 — `extractBundle` accepts workdir; uses `filepath.Join(workdir, "bundle")`. |
| `pkg/importer/importer.go` | MODIFY | #1 — `Import()` calls `workdir.Ensure` before `extractBundle`; passes wd through. #2 — `checkSingleImageFitsInBudget` moved below `splitPreflightResults` and passes filtered `applyList` images. |
| `pkg/importer/compose.go` | MODIFY | #3 — error wrapping at lines 177-179 and 316-318 constructs new `*diff.ErrBaselineBlobDigestMismatch` instead of mutating. |
| `pkg/importer/extract_test.go` | MODIFY | Update for new signature. |
| `pkg/importer/importer_test.go` (or equivalent) | MODIFY | Test that the budget check now respects `applyList`: bundle with one oversized image (preflight-skippable) + one small image; partial mode applies the small image. |

### Lessons-from-A* to bake into the implementer prompt

- **All A1-A14 + A15-A16** — drift cleanup touches subtle paths; baseline expected behavior is the merged streaming I/O contract.
- **A1** — error mutation uses `errors.As` which is goroutine-unsafe in principle but goroutine-safe in current usage. Constructing a new error preserves single-owner semantics.

### Steps

- [ ] **Step 5.1: `advisor()` pre-flight.** Spec §5.5 + this PR text.

- [ ] **Step 5.2: Modify `extractBundle` signature.** Add a `workdir string` parameter; replace `os.MkdirTemp("", "diffah-import-")` with `filepath.Join(workdir, "bundle")` plus `os.MkdirAll(..., 0o700)`. Adjust `b.tmpDir` to point at the new location. Cleanup unchanged: `os.RemoveAll(b.tmpDir)` still removes the bundle subdir; the workdir cleanup removes the parent.

- [ ] **Step 5.3: Update `Import()` ordering.** Move `extractBundle` AFTER `ensureImportWorkdir` (already true today) and pass `wd` through. Adjust the `defer bundle.cleanup()` ordering — cleanup the bundle subdir before the workdir is torn down (already correct via LIFO defer ordering, but verify by reading the current code).

- [ ] **Step 5.4: Move `checkSingleImageFitsInBudget` below preflight.** In `importEachImage` (currently at lines ~242-258 of `importer.go`), the budget check passes `bundle.sidecar.Images`. This PR:
  - Moves the check OUT of `importEachImage` and INTO `Import()` after `runBaselinePreflight` filters `applyList`.
  - Passes only the `applyList` images: `imageEntriesFor(applyList, bundle.sidecar.Images)` — a small helper that filters the slice by name.
  - Existing semantics: `errs.UserError` returned BEFORE any worker spins up. Now the rejection only applies to images that actually plan to run.

- [ ] **Step 5.5: Stop mutating `*diff.ErrBaselineBlobDigestMismatch` in place.** Replace the two `errors.As` mutation blocks at `pkg/importer/compose.go:177-179` and `:316-318` with:

```go
var mismatch *diff.ErrBaselineBlobDigestMismatch
if errors.As(err, &mismatch) && mismatch.ImageName == "" {
    err = &diff.ErrBaselineBlobDigestMismatch{
        ImageName: s.imageName,
        Digest:    mismatch.Digest,
        Got:       mismatch.Got,
    }
}
```

(Construct a new instance with the populated `ImageName`. The `errors.Is` chain preserves `errors.As` lookup of the new instance because the err variable is rebound.)

- [ ] **Step 5.6: Write a test for drift item #2.** `pkg/importer/admission_test.go::TestAdmission_BudgetCheckUsesApplyList`: bundle with two images, image A's max layer estimate exceeds budget, image B fits. Mock RunPreflight to mark A as skipped. Assert `Import()` runs B successfully under partial mode (previously: budget check rejected ALL because A's pre-skip estimate triggered fail-fast).

- [ ] **Step 5.7: Run full suite.** `go test -count=1 ./...`.

- [ ] **Step 5.8: Run lint.** `golangci-lint run ./...`.

- [ ] **Step 5.9: Commit.**

```
refactor(importer): drift cleanup post-hardening (PR5)

Three architectural drift items the post-merge codex retrospective
surfaced:

1. Import extraction relocates under <workdir>/bundle/. Previously
   extractBundle used os.MkdirTemp("", "diffah-import-") regardless
   of --workdir, splitting cleanup observability and risking ENOSPC
   on tmpfs-backed /tmp while the operator's --workdir disk sat
   unused. Aligns with import-streaming spec §4.2.

2. Admission budget check uses applyList, not all sidecar images.
   Previously a preflight-skippable oversized image triggered a
   fail-fast rejection of its small siblings under partial mode.
   The check is moved below splitPreflightResults and runBaselinePreflight.

3. Importer error wrapping for ErrBaselineBlobDigestMismatch stops
   mutating the matched error in place. The new pattern constructs
   a new instance with the populated ImageName.

Spec: docs/superpowers/specs/2026-05-07-streaming-hardening-design.md §5.5
```

### Verification

```bash
go test -count=1 ./...
golangci-lint run ./...
diffah apply --workdir=/tmp/diffah-test ...   # verify extraction lands under /tmp/diffah-test/bundle
```

---

## Summary

Five PRs total, four required for v0.3 readiness, one optional. Each PR:

- Lands behind one feature branch.
- Has a single conventional-commit message that traces to spec §5.X.
- Includes regression tests for the failure mode being closed.
- Updates the lessons doc only when an existing amendment closes (PR4 closes A10).
- Does NOT touch out-of-scope drift items (admission goroutine ordering, BaselineSpool surface unification, etc.).

The v0.3 readiness gate is "PR1-PR4 merged and the nightly scale-bench is green for two consecutive runs after PR2 lands." PR5 is best-effort.

## Spec

[`../specs/2026-05-07-streaming-hardening-design.md`](../specs/2026-05-07-streaming-hardening-design.md)

## Test plan

| PR | New unit tests | New integration tests | New scale-bench | Spec amendment |
|---|---|---|---|---|
| PR1 | 6 (5 malicious + 1 round-trip) | 0 | 0 | none |
| PR2 | 3 (estimator x2, GetBlob x1) | 0 | 1 (`TestImport_ScaleBaselineOnlyReuse4GiB`) | none |
| PR3 | 3 (verify-on-close x2, archive truncation x1) | 0 | 0 | none |
| PR4 | 3 (preflight x3) | 1 (`unbundle_preflight_baseline_completeness`) | 0 | import-streaming §13.11 |
| PR5 | 1 (admission applyList) | 0 | 0 | none |

---

## Self-review checklist (run after writing the plan)

- [ ] Every PR's "Critical files" table matches the actual files touched in its Steps.
- [ ] Every Step that introduces a new helper has a corresponding Step that wires it (amendment-13).
- [ ] Every PR's commit message references spec §5.X correctly.
- [ ] Every regression test is named and described concretely (no "add a test for X" placeholders).
- [ ] No PR touches out-of-scope items listed in spec §3 or in the "Out of scope" header above.
- [ ] PR1-PR4 are independent: PR2 does NOT depend on PR1 (P0 #1 fix doesn't need digest validation; they're parallel-mergeable in principle, though sequential is cleaner for review).
- [ ] PR5 explicitly depends on PR4 (the budget-check move requires the preflight ordering to exist first).
- [ ] No `Co-Authored-By: Claude` and no `🤖 Generated with Claude Code` trailers anywhere.
- [ ] Lessons doc A1-A14 + A15-A16 are referenced; new amendments are NOT needed in this initiative (the work closes existing ones; new finds belong in a future retrospective).
