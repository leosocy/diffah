# diffah v2 — Multi-Image Bundle Import Cleanup

**Date:** 2026-04-22
**Status:** Design approved, pending implementation plan
**Scope owner:** diffah core (leosocy)
**Phase:** v2 Phase 2 — follow-up to II.a + II.b
**Depends on:** merged multi-image bundle branch (`feat/v2-multi-image-bundle`, HEAD `700488b`).
**Supersedes scope:** none. Completes deferred pieces of `2026-04-20-diffah-v2-multi-image-bundle-design.md`.

## 1. Purpose and motivation

The merged multi-image bundle branch delivered the export pipeline, the
sidecar schema, the blob pool, the CLI rewrite, and the full test matrix
for *structural* import paths (strict mode, baseline mismatch,
positional rejection, unknown-name, dry-run stats, force-full dedup).
It did **not** deliver:

1. The actual multi-image import loop from the plan's Task 25. A
   late-stage code-review fix (`74f87fe`) gated `len(Images) > 1` with
   `"multi-image bundle import is not yet supported"` and froze that
   limit as an asserted behaviour in
   `TestIntegration_MultiImageBundle_RejectsImport`.
2. The digest-verification step on blob reads. Baseline bytes are
   copied blind; patch-decoded bytes are written without checking
   `digest.FromBytes(out) == expected`.
3. The `bundleImageSource` abstraction the plan sketched at Task 24. The
   current `composeImage` writes every needed blob to a tmpdir and uses
   `directory.NewReference`, which generates three of the four gosec
   G306 warnings and duplicates logic between full-blob, patched-blob,
   and baseline-blob write paths.
4. The `DryRunReport` shape from the plan's Task 26 § 6.5. The report
   today has three unpopulated fields (`ShippedBlobs`, `RequiredBlobs`,
   `ArchiveSize`) and no per-image layer breakdown, blob-encoding
   breakdown, or `WouldImport` / `SkipReason` signal.
5. Documentation alignment. `CHANGELOG.md` describes multi-image import
   as shipped; `README.md` shows a single-image import example without
   flagging that `OUTPUT` is a directory under the final design.

Additionally, 24 linter issues were introduced on this branch and
silently suppressed by deferring `make lint` to CI. The export
pipeline's `encodeSingleShipped` fallback swallows every error class,
collapsing the expected "no suitable baseline" signal with real bugs
(zstd crash, fingerprinter panic).

This design closes all five items above plus the lint and
error-handling gaps as one coherent cleanup increment. Every decision
below is locked — no new open questions.

## 2. Scope and non-goals

**In scope:**

- Replace `composeImage`'s tmpdir-and-`directory.NewReference` path
  with a streaming `bundleImageSource` that implements
  `types.ImageSource` and answers `GetBlob` from disk-backed bundle
  blobs plus a wrapped baseline source. Digest verification on every
  decoded patched blob and every baseline-served blob.
- Remove the `len(Images) > 1` guard from `importer.Import`. Implement
  the per-image loop specified in plan Task 25. For every resolved
  image, compose into `OUTPUT_DIR/<name>/` using the image's baseline
  and the existing resolved output format.
- `OUTPUT` positional argument becomes a directory uniformly
  (including for bundles of one). Write one sub-entry per image:
  a file (`<name>.tar`) for archive output formats, a directory
  (`<name>/`) for `dir` format.
- `DryRunReport` restructured to match plan Task 26 § 6.5 exactly.
  Populate all fields. CLI `--dry-run` output shows per-image layer
  counts, patch ratios, and skip reasons.
- `TestIntegration_MultiImageBundle_RejectsImport` deleted. Two
  replacement tests: a happy-path multi-image round-trip that imports
  both images and asserts target manifests match, and a
  partial-skip case (one baseline missing, non-strict) that imports
  only the provided image and lists the skipped one.
- Typed sentinel `errNoBaselineMatch` in `pkg/exporter` so
  `encodeShipped` distinguishes the expected "fingerprinter found no
  suitable baseline" signal from real errors. Expected signal stays
  silent; any other error logs to `opts.Progress` as a warning and
  falls back to full encoding. Export itself never aborts on per-layer
  encode failure.
- `BaselineRef.SourceHint` populated with `filepath.Base(p.BaselinePath)`
  instead of the placeholder `<name>-baseline` suffix.
- CHANGELOG and README updated inline with the final state:
  multi-image import documented, `OUTPUT` as directory flagged as a
  breaking change.
- 24 linter issues resolved in a single sweep commit: gofmt /
  goimports / gosec G306 / gocyclo / funlen / lll / goconst / gocritic
  / revive / staticcheck / unused. Dead code (`blobPool.setEntry`)
  removed. Broken test helper (`bundleHarness.baselinePath` ignoring
  its `name` argument) either wired up to return per-name paths or
  deleted if unused.

**Explicitly out of scope:**

- Cross-image patch-from (II.c from the original spec).
- Parallel compose (importing multiple images concurrently).
- Registry / OCI direct I/O.
- Phase 1 archive migration tool.
- Per-pair intra-layer mode override.
- Output-format polymorphism (file-for-one, dir-for-many). `OUTPUT` is
  uniformly a directory.

## 3. Design decisions (locked)

| # | Decision                                                      | Alternatives considered                                                            |
|---|---------------------------------------------------------------|------------------------------------------------------------------------------------|
| 1 | Finish Task 25's multi-image import loop                      | Ship single-image-only and document restriction                                    |
| 2 | Verify digest on every blob read (baseline and patched)       | Verify only on patched blobs; verify lazily at assemble time                       |
| 3 | Refactor compose to `bundleImageSource` (pure streaming)      | Keep tmpdir; hybrid disk-materialise-then-stream                                   |
| 4 | `OUTPUT` positional is always a directory                     | Polymorphic file-for-one/dir-for-many; require `--output-dir` flag for multi       |
| 5 | `DryRunReport` matches plan Task 26 § 6.5 in full             | Populate unused fields in current shape; leave as-is                               |
| 6 | `encodeShipped`: typed sentinel + log-and-continue warning    | Fail loudly on any error; stay silent                                              |
| 7 | Replace rejection test with happy-path + partial-skip pair    | Delete outright and rely on existing structural tests                              |
| 8 | `BaselineRef.SourceHint = filepath.Base(baselinePath)`        | Drop the write; keep the `-baseline` suffix                                        |
| 9 | Update CHANGELOG and README inline in the Task 25 commit      | Staged update with a transitional "coming soon" note                               |
| 10 | 24 lint issues fixed in a single sweep commit                | Per-category; fold into feature commits                                            |

## 4. Architecture

### 4.1 Package boundaries — unchanged

Package layout from the merged branch stays as-is:

- `pkg/diff` — sidecar types, validation, error sentinels, spec parsers.
- `pkg/exporter` — blob pool, per-pair plan, encode, assemble, write.
- `pkg/importer` — extract, resolve, guard, compose, Import/DryRun.
- `internal/zstdpatch` — codec.
- `internal/imageio` — archive sniffing, `OpenArchiveRef`.
- `internal/archive` — tar extraction.
- `cmd` — CLI (export, import, inspect).

No new packages. No renames.

### 4.2 `pkg/importer/compose.go` — new `bundleImageSource`

Replace the current mix of `writeBlobAsDigestFile` / `fetchBaselineBlob`
/ `applyPatchAndWrite` with one streaming source:

```go
// bundleImageSource implements go.podman.io/image/v5/types.ImageSource.
// It serves the target image's manifest + blobs for a single resolved
// image in a bundle. Shipped blobs come from the extracted bundle's
// blobs/ directory (with patch decode); required blobs come from a
// wrapped baseline source. Every served blob is digest-verified before
// return.
type bundleImageSource struct {
    blobsDir     string
    manifest     []byte
    manifestMime string
    sidecar      *diff.Sidecar
    baseline     types.ImageSource
    ref          types.ImageReference // the wrapping ref for Reference()
}
```

`GetBlob` logic:

1. Look up `sidecar.Blobs[info.Digest]`.
2. **Not in bundle** → delegate to `baseline.GetBlob`. Read the result,
   check `digest.FromBytes(bytes) == info.Digest`. On mismatch return
   a new `diff.ErrBaselineBlobDigestMismatch{Digest, Got}` error.
3. **In bundle, encoding=full** → read `blobsDir/<algo>/<hex>`, verify
   its digest matches the key, return a `bytes.Reader`.
4. **In bundle, encoding=patch** → read the patch bytes from
   `blobsDir/...`, fetch `entry.PatchFromDigest` from baseline (with
   its own digest verification step as in path 2), decode via
   `zstdpatch.Decode`, verify `digest.FromBytes(out) == info.Digest`.
   Mismatch returns `diff.ErrIntraLayerAssemblyMismatch`.

`applyPatch`'s unused `codec` parameter is removed. Codec is an
informational tag in the sidecar today; there is no second codec to
dispatch on, and adding one is II.c scope. The function becomes
`zstdpatch.Decode(base, patch)` inline at the one call site.

Trivial `ImageSource` methods (`Reference`, `Close`,
`HasThreadSafeGetBlob`, `GetSignatures`, `LayerInfosForCopy`) delegate
to the baseline source where safe.

`staticSourceRef` mirrors the existing `compositeRef` pattern and wraps
a `bundleImageSource` so `copy.Image` can take it as a source.

### 4.3 `pkg/importer/importer.go` — the Task 25 loop

Drop the `len(sc.Images) > 1` rejection. `Import` becomes:

```go
func Import(ctx context.Context, opts Options) error {
    bundle, err := extractBundle(opts.DeltaPath)
    if err != nil { return err }
    defer bundle.cleanup()

    if err := validatePositionalBaseline(bundle.sidecar, opts.Baselines); err != nil {
        return err
    }
    resolved, err := resolveBaselines(ctx, bundle.sidecar, opts.Baselines, opts.Strict)
    if err != nil { return err }

    progress := opts.Progress
    if progress == nil { progress = io.Discard }

    if err := os.MkdirAll(opts.OutputPath, 0o755); err != nil {
        return fmt.Errorf("mkdir output: %w", err)
    }

    imported, skipped := 0, []string{}
    resolvedByName := indexResolved(resolved)
    for _, img := range bundle.sidecar.Images {
        rb, ok := resolvedByName[img.Name]
        if !ok {
            fmt.Fprintf(progress, "%s: skipped (no baseline provided)\n", img.Name)
            skipped = append(skipped, img.Name)
            continue
        }
        subdir := filepath.Join(opts.OutputPath, img.Name)
        if err := composeImage(ctx, img, bundle, rb,
            subdir, opts.OutputFormat, opts.AllowConvert); err != nil {
            return fmt.Errorf("compose %q: %w", img.Name, err)
        }
        imported++
    }
    fmt.Fprintf(progress, "imported %d of %d images; skipped: %v\n",
        imported, len(bundle.sidecar.Images), skipped)
    return nil
}
```

The output layout becomes `OUTPUT/<name>/` unconditionally (bundle of
one ⇒ `OUTPUT/default/...`), with the filesystem shape dictated by
`--output-format`:

| `--output-format` | On disk for image `svc-a` |
|-------------------|---------------------------|
| `oci-archive`     | `OUTPUT/svc-a.tar` (one tar file)                 |
| `docker-archive`  | `OUTPUT/svc-a.tar` (one tar file)                 |
| `dir`             | `OUTPUT/svc-a/` (directory tree)                  |

`composeImage` accepts `outputFormat` and `allowConvert` and uses the
existing `resolveOutputFormat` + `buildOutputRef` helpers.

### 4.4 `pkg/importer/importer.go` — `DryRunReport` v2

New shape, per plan Task 26 § 6.5, exactly:

```go
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
}

type BlobStats struct {
    FullCount, PatchCount int
    FullBytes, PatchBytes int64
}

type ImageDryRun struct {
    Name                   string
    BaselineManifestDigest digest.Digest
    TargetManifestDigest   digest.Digest
    BaselineProvided       bool
    WouldImport            bool
    SkipReason             string
    LayerCount             int // total layers in target manifest
    ArchiveLayerCount      int // layers shipped in the bundle
    BaselineLayerCount     int // layers required from baseline
    PatchLayerCount        int // layers shipped as encoding=patch
}
```

`DryRun` reads every target manifest from disk (same cost as `Import`
minus the `copy.Image` call) and produces a per-image layer-count
breakdown. `BlobStats` is computed over `sidecar.Blobs` by scanning
encoding once.

`ArchiveBytes` is `os.Stat(opts.DeltaPath).Size()`, i.e. the bundle tar
file size — not the sum of in-archive blob sizes. This matches the
plan.

CLI `cmd/import.go` dry-run rendering is rewritten to show:

```
archive: bundle.tar (feature=bundle version=v1 platform=linux/amd64)
tool: diffah v0.3.0, created 2026-04-21T12:34:56Z
archive bytes: 1234567
blobs: 5 (full: 4, patch: 1) — full: 1200000 B, patch: 34567 B
images: 2
  svc-a  target=sha256:aa... (would import, baseline=v1a.tar)
    layers: 8 total — 1 shipped, 7 from baseline, 1 patched
  svc-b  target=sha256:bb... (skip — no baseline provided)
    layers: 8 total — 0 shipped, 8 from baseline
```

### 4.5 `pkg/exporter/encode.go` — typed sentinel + warning path

Add to `pkg/exporter`:

```go
// errNoBaselineMatch is a sentinel returned by the per-layer encoder
// when content-similarity fingerprinting rejects every baseline layer.
// It's not a bug — the caller should silently fall back to full.
var errNoBaselineMatch = errors.New("no suitable baseline match")
```

`encodeSingleShipped` returns `errNoBaselineMatch` on the specific
"planner found no candidate" path. Any other error (zstd failure,
fingerprinter panic, read failure) is a real error.

`encodeShipped` loop:

```go
_, payload, entry, err := encodeSingleShipped(ctx, p, s, layerBytes, fp)
switch {
case err == nil:
    pool.addIfAbsent(s.Digest, payload, entry)
case errors.Is(err, errNoBaselineMatch):
    pool.addIfAbsent(s.Digest, layerBytes, fullEntry(s))
default:
    // Real bug. Log, fall back to full, keep going.
    fmt.Fprintf(progress, "warning: %s: patch encode failed (%v), falling back to full\n",
        p.Name, err)
    pool.addIfAbsent(s.Digest, layerBytes, fullEntry(s))
}
```

Where `fullEntry(s)` is the existing full-encoding `diff.BlobEntry`
literal pulled into a helper. `opts.Progress` wiring already exists on
`exporter.Options`.

Export itself still never aborts on per-layer failures — the per-blob
fallback-to-full keeps the archive producible — but the progress
stream now tells the operator when something went wrong.

### 4.6 `pkg/exporter/exporter.go` — deduplicated pipeline

`Export` and `DryRun` currently duplicate the entire
plan-then-seed-then-count-then-encode pipeline. Extract to:

```go
type builtBundle struct {
    plans []*pairPlan
    pool  *blobPool
}

func buildBundle(ctx context.Context, opts Options) (*builtBundle, error) { ... }
```

`Export` calls `buildBundle`, calls `assembleSidecar`, calls
`writeBundleArchive`. `DryRun` calls `buildBundle`, calls
`assembleSidecar`, reads the sidecar for the new `DryRunStats`. No new
behaviour — just the duplication removed.

### 4.7 `pkg/exporter/pool.go` — dead code removal

`blobPool.setEntry` is unused. Delete.

### 4.8 `pkg/exporter/assemble.go` — `SourceHint`

```go
SourceHint: filepath.Base(p.BaselinePath),
```

Ship the base filename (e.g. `"v1_oci.tar"`). Useful for inspect output
when the archive is shipped without its producing bundle spec.

## 5. Data model — `diff.BlobEntry` and sidecar

No changes. The existing sidecar schema absorbs all the above changes
without wire-format modification.

## 6. Error model additions

Two new sentinels in `pkg/diff/errors.go`:

```go
// ErrBaselineBlobDigestMismatch is returned when a baseline-served blob
// does not match the digest the sidecar expects. Bytes are never
// written to the output when this fires.
type ErrBaselineBlobDigestMismatch struct {
    ImageName string
    Digest    string
    Got       string
}

func (e *ErrBaselineBlobDigestMismatch) Error() string {
    return fmt.Sprintf("image %q: baseline blob %s has digest %s",
        e.ImageName, e.Digest, e.Got)
}
```

`ErrIntraLayerAssemblyMismatch` already exists for the patch-decode
case and is reused.

## 7. CLI behaviour changes

### 7.1 `diffah import` — `OUTPUT` argument

- **Before:** `OUTPUT` is a file path; a single tar file is written there.
- **After:** `OUTPUT` is a directory; per-image output lands at
  `OUTPUT/<name>.tar` (archive formats) or `OUTPUT/<name>/` (`dir`).
- **Migration message:** `diffah import` does *not* auto-detect
  legacy usage. If `OUTPUT` exists and is a regular file, import
  returns `"OUTPUT must be a directory (bundle output is OUTPUT/<name>/...)"`
  with a one-line hint.

### 7.2 `diffah import --dry-run`

New output format as sketched in § 4.4.

### 7.3 `diffah inspect`

No behavioural change.

### 7.4 `diffah export`

No behavioural change other than the new warning line on fallback.

## 8. Testing strategy

### 8.1 New tests

- `pkg/importer/compose_test.go`
  - `TestBundleImageSource_GetBlob_Full` — returns exact bytes, digest verified.
  - `TestBundleImageSource_GetBlob_Patched` — applies patch, verifies recovered digest, fails `ErrIntraLayerAssemblyMismatch` on synthesized corruption.
  - `TestBundleImageSource_GetBlob_Baseline` — delegates to baseline, verifies digest, fails `ErrBaselineBlobDigestMismatch` on digest drift.
  - `TestBundleImageSource_GetManifest` — returns stored manifest + mime.
- `pkg/importer/integration_bundle_test.go`
  - Replace `TestIntegration_MultiImageBundle_RejectsImport` with `TestIntegration_MultiImageBundle_ImportsBoth` — asserts `OUTPUT/svc-a.tar` and `OUTPUT/svc-b.tar` both exist and decode to the target manifest digests in the sidecar.
  - Add `TestIntegration_MultiImageBundle_PartialSkip` — one baseline provided, non-strict; asserts only the provided image is written, the skipped name is recorded in the progress output.
  - Update `TestIntegration_MultiImageBundle_DryRunReport` to the new `DryRunReport` fields.
  - Add `TestIntegration_MultiImageBundle_OutputMustBeDirectory` — pre-existing file at `OUTPUT` returns the new error.
- `pkg/exporter/encode_test.go`
  - `TestEncodeShipped_NoMatch_SilentFull` — stub planner returns `errNoBaselineMatch`, no warning on Progress, blob stored as full.
  - `TestEncodeShipped_OtherError_LoggedFull` — stub planner returns `io.ErrUnexpectedEOF`, warning line on Progress, blob stored as full.
- `pkg/exporter/exporter_test.go`
  - `TestExportDryRun_SharedPipeline` — assert the `buildBundle` path runs the planner once and the pool is seeded identically in both `Export` and `DryRun`.

### 8.2 Deleted tests

- `TestIntegration_MultiImageBundle_RejectsImport` — replaced.

### 8.3 Determinism

`TestIntegration_Determinism` keeps passing. The compose refactor is on
the import side and does not touch archive bytes.

### 8.4 Broken helper

`bundleHarness.baselinePath(string) string` in
`integration_bundle_test.go:55-57` is unused by every call site today.
Delete it.

## 9. Documentation changes

### 9.1 CHANGELOG

Current CHANGELOG already claims multi-image import works. Keep the
claim (it becomes true after this cleanup) and add under
"Breaking changes":

```
- **`diffah import`**: `OUTPUT` positional argument is now a directory.
  Per-image output lands at `OUTPUT/<name>.tar` (archive formats) or
  `OUTPUT/<name>/` (`dir` format). Single-image bundles still use a
  `default/` sub-entry.
```

### 9.2 README

- Add a multi-image import example showing `OUTPUT/` directory
  produced after `diffah import --baseline svc-a=... --baseline svc-b=...`.
- Update the single-image example to use the new directory layout.
- Add one sentence under "Output format" noting the per-image
  sub-entry naming.

## 10. Lint sweep (single commit)

One commit, one message (`style: resolve 24 lint issues from bundle
cleanup`), running `make lint` → 0 issues.

Tracked:

- gofmt (5 files): `cmd/export.go`, `cmd/inspect_test.go`,
  `pkg/exporter/perpair.go`, `pkg/importer/compose.go`,
  `pkg/importer/resolve.go`.
- goimports (4): `pkg/exporter/assemble_test.go`,
  `pkg/exporter/writer_test.go`, `pkg/importer/resolve.go`,
  `pkg/importer/resolve_test.go`.
- gosec G306 (4): all inside `compose.go`, all obviated by the
  `bundleImageSource` refactor (no more tmpdir writes) — lint issue
  resolved naturally by § 4.2, not by changing `0o644` to `0o600`.
- gocyclo `composeImage` 18: split by § 4.2.
- funlen `resolveBaselines` 68 lines: extract the post-loop
  `knownNames` unknown-name check into `rejectUnknownBaselineNames(sc,
  expanded)`.
- lll `errors.go:117`: wrap the `ErrMultiImageNeedsNamedBaselines`
  message.
- goconst: introduce `const codecZstdPatch = "zstd-patch"` in
  `pkg/diff` and replace test strings; replace `"sha256:bb"` with a
  test helper constant.
- gocritic `appendAssign` at `pair_test.go:16`: assign `dup := append(...)`
  slice back to a new variable.
- staticcheck QF1003: convert the `if entry.Encoding == diff.EncodingFull`
  chain to a tagged `switch entry.Encoding`.
- revive `unused-parameter` — two items: `applyPatch(codec, ...)`
  dropped by § 4.2; `bundleHarness.baselinePath(name)` deleted by § 8.4.
- revive `context-as-argument` at `compose.go:163`: obviated by § 4.2.
- unused `blobPool.setEntry`: deleted by § 4.7.

## 11. Out-of-scope reminders

- Cross-image patch-from (II.c) stays deferred.
- Parallel per-image compose stays deferred.
- Registry / OCI direct I/O stays deferred.
- The Progress streamer interface stays the existing `io.Writer`. No
  structured events object.
- No feature flag or rollback gate — this is a cleanup increment on
  an unreleased branch.

## 12. Risks

- **`bundleImageSource` method surface drift.** `types.ImageSource`
  has several optional methods (`LayerInfosForCopy`,
  `GetSignatures`). The wrapped baseline source may implement them
  differently from what `copy.Image` expects for a bundle-composed
  source. Mitigation: delegate to the baseline where safe, return
  the trivial `nil, nil` for signature-style methods, and gate with
  an integration test that round-trips a real `v1_oci.tar` fixture.
- **`OUTPUT`-as-directory breaking change.** Anyone scripting
  `diffah import` today against a file path will fail. Mitigation:
  the error message is explicit and one-line; the CHANGELOG flags it.
- **Digest verification cost.** Every blob read now includes a
  SHA-256 over the bytes. For a 200 MB layer that is ~500 ms on a
  laptop. Mitigation: this is import, not a hot path; the cost is
  paid once per imported blob, and skipping verification is the
  silent-corruption risk we're explicitly closing.
- **Lint sweep ordering.** The compose refactor resolves 6 of the 24
  issues naturally. If the lint-sweep commit lands first, those
  items need manual fixes that then get reverted. Mitigation: do
  the refactor first, then lint sweep.

## 13. Open questions

None. All design decisions were locked during brainstorming.
