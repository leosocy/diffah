# diffah v2 — Multi-Image Bundle Archive (II.a + II.b)

**Date:** 2026-04-20
**Status:** Design approved, pending implementation plan
**Scope owner:** diffah core (leosocy)
**Phase:** v2 Phase 2 — scope expansion, increment #2
**Depends on:** Phase 1 intra-layer feature (merged). Independent of I.a (content-similarity) and Track A (zstd backend resilience).
**Replaces:** Phase 1 sidecar schema (`diffah.json` flat shape).

## 1. Purpose and motivation

Phase 1 exports exactly one `(baseline, target)` image pair per archive.
Realistic release workflows ship several services together — a
quarterly product update covers five to ten service images at once,
often sharing base layers and common configuration blobs. With the
current single-image format, operators fan out to N runs of
`diffah export`, carry N archives across an air-gap, and lose every
byte of cross-image deduplication (identical base layers, identical
sidecar metadata, identical configs). Meanwhile, anyone doing smoke
testing has to re-assemble the release from N scattered files.

This design unifies the archive format around **bundles**: every
archive is a bundle, a single archive holds one or more image pairs,
and any blob (layer, manifest, config) that appears under the same
digest in multiple images is stored once. Single-image exports become
bundles of length one and use the same code path. Cross-image
dedup lands as a structural property of the format rather than an
optional optimisation.

Scope is the format, the two-input export surface (JSON spec and
`--pair` flags), and partial imports. Cross-image *patch-from*
(target-B's layer patched against target-A's layer) is II.c and stays
deferred.

## 2. Scope and non-goals

**In scope:**

- Unified bundle archive format: `sidecar.json` with `feature: "bundle"`
  marker + content-addressed `blobs/` directory.
- Retirement of the Phase 1 flat sidecar shape. Phase 1 archives
  already on disk become unimportable with a clear error pointing at
  re-export.
- Two input syntaxes on `diffah export`:
  - JSON bundle spec file via `--bundle FILE`.
  - Repeatable `--pair NAME:BASELINE=TARGET` flag.
  - Mutually exclusive with each other and with the positional
    single-image form.
- Positional single-image export retained: `diffah export BASELINE.tar
  TARGET.tar OUT.tar` emits a bundle of one with `name = "default"`.
- Cross-image **blob** dedup at every level: layers, manifests,
  configs. When a shipped-layer digest is referenced by ≥ 2 images,
  encoding is forced to `full` (patch-from dedup deferred to II.c).
- `diffah import` accepts `--baseline NAME=PATH` (repeatable),
  `--baseline-spec FILE` (JSON mirror of the export spec), and
  positional single-baseline for bundles of one.
- Partial import is the default: images whose baselines are not
  provided are skipped with a stderr log line; `--strict` opts in to
  all-or-nothing.
- Output layout: `OUTPUT_DIR/<name>/` per imported image, always
  (including for single-image bundles) — one consistent mental model.
- `DryRunReport.Images[]` enumeration so `diffah inspect` can show
  per-image state before a real import.
- Deterministic, byte-reproducible archive output (stable blob order,
  stable JSON key order).

**Explicitly out of scope:**

- Cross-image patch-from (II.c).
- Per-pair overrides (different `--intra-layer` mode per image).
- Bundle spec schema validation (JSON Schema). Parser is lenient:
  unknown fields ignored, missing required fields rejected with a
  specific message.
- Phase 1 archive migration tool. Clear rejection only.
- Progress bars and parallel pipeline (Track III).
- Registry / OCI direct I/O (Track III).
- Structured logs (Track IV).
- Partial-layer import (importing only some layers of an image).
- `--flatten` output layout for N = 1.

## 3. Decision log

| # | Decision | Reason |
|---|---|---|
| 1 | Every archive is a bundle. Single-image is a bundle of one. | One schema, one code path, one mental model. Cheap given pre-release tool. |
| 2 | Phase 1 flat sidecar retired. Importer rejects with a migration hint. | No known in-the-wild archives; keeping dual support doubles the importer for a hypothetical user. |
| 3 | `sidecar.json` gains `feature: "bundle"` marker alongside the existing `version: "v1"`. | `feature` discriminates format families; `version` continues to track schema evolution within a family. |
| 4 | Content-addressed `blobs/` pool is the only archive payload directory (no separate `manifests/`). | Every non-baseline byte is already digest-keyed. Separate directories are organisational noise for a diff archive. |
| 5 | Manifests and configs live in `blobs/` alongside layers. | Keeps the pool uniform; simplifies dedup when two images share a config or manifest digest. |
| 6 | Bundle-spec JSON **and** `--pair` flag both accepted, mutually exclusive with each other. | User chose both on 2026-04-20. Spec file scales for large bundles; flag is ergonomic for quick 2-3 image bundles. |
| 7 | `--baseline-spec` available on import, mirroring the export spec. | Symmetry. Keeps user muscle memory consistent. |
| 8 | Partial import is default; `--strict` opts into all-or-nothing. | User chose partial on 2026-04-20. `--strict` covers CI pipelines that must fail on any missing baseline. |
| 9 | When a shipped-layer digest is referenced by ≥ 2 images, encoding is forced to `full`. | Preserves correctness without per-baseline reachability analysis. Reachability-aware patch dedup belongs to II.c. |
| 10 | Output is always `OUTPUT_DIR/<name>/` per image, including N = 1. | Consistent mental model; avoids disclosing N-ness to downstream tools. |
| 11 | Name regex `^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`. | OCI-image-ish identifier without embedding any syntax that conflicts with `:` / `=` in the `--pair` grammar. |
| 12 | `name = "default"` is tolerated for user-specified pairs (collides with positional single-image default). | Resolving by error is ergonomically painful; collision is not dangerous because each archive has exactly one positional-default OR explicit names, never both. |
| 13 | `blobs{}` emitted in JSON sorted by digest; files in `blobs/` emitted in tar with the same order. | Deterministic archive output is a testable property and builds operator trust. |
| 14 | No per-pair mode overrides in MVP. `--intra-layer` applies globally. | Usable MVP; per-pair overrides add bundle-spec schema complexity without a current user request. |

## 4. Archive format

### 4.1 On-disk layout

```
out.tar
├── sidecar.json               // the only format discriminator
└── blobs/
    └── sha256:<hex>           // one file per unique archive-resident digest
```

No other paths. `blobs/` holds layers, manifests, and configs; its sole
key is the content digest.

### 4.2 Sidecar schema

```json
{
  "version": "v1",
  "feature": "bundle",
  "tool": "diffah",
  "tool_version": "<semver>",
  "created_at": "<RFC3339 UTC>",
  "platform": "linux/amd64",
  "blobs": {
    "sha256:<hex>": {
      "size": 12345,
      "media_type": "application/vnd.oci.image.layer.v1.tar+gzip",
      "encoding": "full" | "patch",
      "codec": "zstd-patch",
      "patch_from_digest": "sha256:<hex>",
      "archive_size": 2345
    }
  },
  "images": [
    {
      "name": "service-a",
      "baseline": {
        "manifest_digest": "sha256:<hex>",
        "media_type": "application/vnd.oci.image.manifest.v1+json",
        "source_hint": "svc-a-5.2.tar"
      },
      "target": {
        "manifest_digest": "sha256:<hex>",
        "manifest_size": 4321,
        "media_type": "application/vnd.oci.image.manifest.v1+json"
      }
    }
  ]
}
```

**Blob entry rules (validated at marshal time, Phase 1 semantics preserved):**

- `encoding: "full"` → `codec` and `patch_from_digest` MUST be absent;
  `archive_size == size` (the blob is stored as the raw layer bytes
  exactly as they appear in the target image — no re-compression).
  This matches `pkg/diff/sidecar.go` Phase 1 rule; re-compressing
  OCI layers that are already gzip- or zstd-encoded rarely wins
  bytes.
- `encoding: "patch"` → both `codec` and `patch_from_digest` MUST be
  set; `0 < archive_size < size` enforced (patch that isn't smaller
  than the raw layer is an exporter bug).
- `patch_from_digest` MUST be a digest that the importer can resolve
  for every image referencing the blob — validated at export time by
  the force-full rule in §4.5 (shipped blobs with `refCount > 1` are
  forced to `encoding: "full"`).

**Image entry rules:**

- `name` unique across `images[]`.
- `baseline.manifest_digest` and `target.manifest_digest` required.
- Target manifest digest MUST appear as a key in `blobs{}` —
  every image carries its manifest in the archive.

**Fields removed from Phase 1:**
`Target`, `Baseline`, `RequiredFromBaseline`, `ShippedInDelta` at the
top level disappear. Target/baseline pointers move into `images[]`;
required/shipped classification is derived (see §4.3).

### 4.3 Layer classification (derived, not stored)

For each image, layers are classified by presence:

```
for each layer digest L in target.manifest:
    if L ∈ blobs{}       → archive-resident (encoding=full | encoding=patch)
    else                 → required from baseline
```

No explicit `shipped_in_delta` / `required_from_baseline` lists are
persisted — they can always be re-derived by intersecting the target
manifest's layers with the `blobs{}` keyset.

### 4.4 Global dedup primitive

`blobs{}` is a content-addressed set keyed by digest. Exporter
operation on every blob (layer, manifest, config):

```
if digest not in globalBlobs:
    globalBlobs[digest] = encode(blob)
# else: reuse — correct because digest ≡ bytes
```

Cross-image dedup is structural; no per-image duplication possible.

### 4.5 Force-full rule for cross-image shared shipped blobs

```
refCount[digest] := number of images referencing this shipped blob

for each shipped blob b with refCount[b.digest] > 1:
    globalBlobs[b.digest].encoding = "full"   // no patch, full-zstd only
```

Single-referenced shipped blobs can still be encoded as patches using
their image's baseline (standard Phase 1 behaviour). The rule ensures
any patch blob's `patch_from_digest` is reachable by the one image
that needs it, without per-baseline reachability analysis.

## 5. Export surface

### 5.1 CLI grammar

```
diffah export [OPTIONS]
    [BASELINE.tar TARGET.tar OUT.tar]       # positional single-image
    [--bundle FILE.json]                    # JSON bundle spec
    [--pair NAME:BASELINE=TARGET]...        # repeatable flag pairs
    [--output PATH | -o PATH]               # required with --bundle/--pair
    [--intra-layer auto|off|required]       # Track A, global

Rules:
  * Exactly one of {positional, --bundle, --pair} must be provided.
  * --bundle and --pair are mutually exclusive.
  * --output MUST be set for --bundle/--pair and MUST NOT be set for
    positional (positional already takes OUT.tar as its third arg).
```

### 5.2 `--pair` grammar

`--pair NAME:BASELINE=TARGET`

- Split on first `:` → `NAME`, `rest`.
- Split `rest` on first `=` → `BASELINE`, `TARGET`.
- `NAME` matches `^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`.
- `BASELINE` and `TARGET` must be existing regular files (resolved to
  absolute paths for error messages).
- Reject duplicate `NAME`s with `ErrDuplicateBundleName`.

### 5.3 Bundle spec JSON

```json
{
  "pairs": [
    { "name": "service-a", "baseline": "svc-a-5.2.tar", "target": "svc-a-5.3.tar" },
    { "name": "service-b", "baseline": "svc-b-5.2.tar", "target": "svc-b-5.3.tar" }
  ]
}
```

- Every pair entry requires all three fields.
- Relative paths are resolved against the spec file's directory.
- Unknown top-level or per-pair fields are tolerated (lenient parsing;
  future-friendly).
- Malformed JSON → `ErrInvalidBundleSpec`.

### 5.4 Pipeline

```
1. Resolve inputs → []Pair{Name, BaselinePath, TargetPath}

2. For each Pair sequentially (stable order):
   - Open baseline + target as image.Source
   - Compute Phase-1-style plan (shipped vs required-from-baseline)
   - Retain plan keyed by Name

3. Build global pool:
   refCount := map[digest.Digest]int
   for each Pair:
       addIfAbsent(globalBlobs, target.ManifestDigest, manifestBytes)
       addIfAbsent(globalBlobs, target.ConfigDigest, configBytes)
       for each shipped layer L:
           refCount[L.Digest]++

   for each Pair:
       for each shipped layer L:
           if L.Digest already encoded: continue
           if refCount[L.Digest] > 1:
               globalBlobs[L.Digest] = encodeFull(L)
           else:
               globalBlobs[L.Digest] = encodePatchOrFull(L, pair.Baseline)

4. Emit archive:
   - write blobs/<digest> for every entry in globalBlobs (sorted)
   - build Sidecar{version=v1, feature=bundle, …, blobs, images}
   - tar emit in deterministic order
```

### 5.5 stderr progress

One line per pair start, one line per pair end, one bundle summary at
the end:

```
[1/2] service-a: planning…
[1/2] service-a: 12 layers · 3 patched · 9 from baseline
[2/2] service-b: planning…
[2/2] service-b: 10 layers · 2 patched · 8 from baseline
bundle: 2 images · 20 unique blobs · 2 dedup · archive 412 MB
```

No structured-log flag (Track IV).

## 6. Import surface

### 6.1 CLI grammar

```
diffah import ARCHIVE.tar [OPTIONS]
    [BASELINE.tar]                         # positional, bundle-of-one only
    [--baseline NAME=PATH]...              # repeatable
    [--baseline-spec FILE.json]            # mirror of export spec
    [--output DIR | -o DIR]                # required
    [--strict]                             # reject on any missing baseline

Rules:
  * Exactly one of {positional BASELINE, --baseline, --baseline-spec}
    must supply baselines; they are mutually exclusive.
  * Positional BASELINE only allowed for bundle-of-one archives;
    rejected with ErrMultiImageNeedsNamedBaselines otherwise.
```

### 6.2 Baseline-spec JSON

```json
{
  "baselines": {
    "service-a": "/path/baseline-a.tar",
    "service-b": "/path/baseline-b.tar"
  }
}
```

Same parsing rules as the export bundle spec: relative paths resolved
against the spec file's directory; unknown fields ignored; malformed
JSON → `ErrInvalidBundleSpec`.

### 6.3 Pipeline

```
1. Open archive; read + validate sidecar
   - feature != "bundle" → ErrPhase1Archive (hint: re-export)
   - version != "v1"     → ErrUnknownBundleVersion
   - malformed           → ErrInvalidBundleFormat

2. Parse user-provided baselines into baselineMap[name] → path.
   - Unknown name in baselineMap → ErrBaselineNameUnknown
     (error lists names available in the bundle)

3. If --strict and any sidecar image has no matching baseline →
   ErrBaselineMissing  (before any output is written)

4. For each image in sidecar.images (stable order):
   if image.name not in baselineMap:
       fprintln(stderr, "service-X skipped: no baseline provided")
       continue

   baseline := image.Source.Open(baselineMap[image.name])
   if baseline.manifest_digest != image.baseline.manifest_digest:
       → ErrBaselineMismatch
         (hint: "wrong baseline for service-X; expected sha256:…")

   composeImage(image, baseline, archive.blobs, outputDir/image.name)

5. Print summary:
   "imported N of M images; skipped: [names]"
```

`composeImage` reuses Phase 1's `CompositeSource.GetBlob`: per-layer
dispatch on `encoding` (`full` → `DecodeFull`, `patch` → `Decode`
against the referenced baseline blob, otherwise pass through from
baseline).

### 6.4 Output layout

```
OUTPUT_DIR/
├── service-a/       # full OCI image layout or docker-schema-2 dir
├── service-b/
└── default/         # for positional single-image invocations
```

Always one subdirectory per imported image. No flattening for N = 1.

### 6.5 DryRun extensions

```go
type DryRunReport struct {
    // Existing fields from Track A (RequiresZstd, ZstdAvailable) preserved.
    Feature      string               // "bundle"
    Version      string               // "v1"
    Tool         string
    ToolVersion  string
    CreatedAt    time.Time
    Platform     string
    Images       []ImageDryRun
    Blobs        BlobStats            // counts: full/patch; byte totals
    ArchiveBytes int64
}

type ImageDryRun struct {
    Name                    string
    BaselineManifestDigest  digest.Digest
    TargetManifestDigest    digest.Digest
    BaselineProvided        bool
    WouldImport             bool
    SkipReason              string    // "no baseline provided" etc.
    LayerCount              int
    ArchiveLayerCount       int       // layers resident in archive
    BaselineLayerCount      int       // layers required from baseline
    PatchLayerCount         int       // subset of ArchiveLayerCount with encoding=patch
}
```

`DryRun` accepts the same flags as `Import` so it is a genuine
pre-flight — users can run it to see exactly what `Import --strict`
would reject.

## 7. Error model

| Error | Kind | Raised by | Trigger |
|---|---|---|---|
| `ErrPhase1Archive` | new sentinel | importer | Sidecar missing `feature` field or not equal to `"bundle"` |
| `ErrUnknownBundleVersion` | new sentinel | importer | `feature == "bundle"` but `version` not recognised |
| `ErrInvalidBundleFormat` | new sentinel | importer | Sidecar JSON malformed or required field missing |
| `ErrMultiImageNeedsNamedBaselines` | new sentinel | importer | Positional `BASELINE` supplied for archive with `images[] ` length > 1 |
| `ErrBaselineNameUnknown` | new sentinel | importer | `--baseline NAME=…` or spec entry references a name not in the bundle |
| `ErrBaselineMismatch` | new sentinel | importer | Provided baseline's manifest digest ≠ sidecar's `baseline.manifest_digest` for that name |
| `ErrBaselineMissing` | new sentinel | importer | `--strict` and ≥ 1 bundle image has no matching baseline |
| `ErrInvalidBundleSpec` | new sentinel | exporter / importer | Bundle spec or baseline spec JSON malformed / missing required fields |
| `ErrDuplicateBundleName` | new sentinel | exporter | Two `--pair` flags (or two spec entries) share a name |
| Existing Phase 1 errors (`ErrIntraLayerAssemblyMismatch`, `ErrBaselineMissingPatchRef`, etc.) | unchanged | unchanged | Same triggers, now raised per-image |

All error messages carry actionable hints (expected digest, available
names, re-export instruction) rather than opaque codes.

## 8. Testing strategy

### 8.1 Unit

| Area | File | Tests |
|---|---|---|
| Sidecar schema | `pkg/diff/sidecar_test.go` | Round-trip marshal/unmarshal; `feature` required; rule-table validation; unknown fields ignored |
| Blob pool dedup | `pkg/diff/sidecar_test.go` | Two images with shared shipped-layer digest ⇒ one entry, `encoding=full` |
| Export CLI | `cmd/export_test.go` | Mutex matrix (positional vs `--bundle` vs `--pair`); `--pair` grammar happy + sad |
| Bundle spec JSON | `pkg/exporter/bundle_spec_test.go` | Happy path; missing fields; malformed JSON; relative-path resolution |
| Import CLI | `cmd/import_test.go` | Mutex matrix; positional-on-multi rejection; baseline-spec parsing |
| Baseline validation | `pkg/importer/importer_test.go` | Unknown name; manifest-digest mismatch; strict vs non-strict |
| DryRun report shape | `pkg/importer/dryrun_test.go` | `Images[]` populated; `WouldImport`/`SkipReason` consistent across flag combos |

### 8.2 Integration (`pkg/importer/integration_test.go`)

| Scenario | Inputs | Assertion |
|---|---|---|
| Happy-path full bundle | v5 fixture, both baselines provided | both `OUTPUT_DIR/{a,b}/` reconstructed byte-exact |
| Partial import | v5, only baseline `a` provided (no `--strict`) | only `OUTPUT_DIR/a/` exists; stderr contains `service-b skipped`; exit 0 |
| `--strict` missing baseline | v5, only baseline `a`, `--strict` | `ErrBaselineMissing`; output dir empty |
| Force-full dedup | v5 (shared shipped layer) | sidecar blob entry has `encoding=full` and both images decode correctly |
| Unknown baseline name | v5, `--baseline service-foo=…` | `ErrBaselineNameUnknown`; message lists `[a, b]` |
| Manifest-digest mismatch | v5, baseline `a` passed for image `b` | `ErrBaselineMismatch` with expected digest |
| Legacy Phase 1 rejection | `testdata/legacy/phase1_oci.tar` | `ErrPhase1Archive`; output dir untouched |
| Determinism | export v5 twice | archives are byte-identical |
| Bundle-of-one positional | single-image positional invocation | emits bundle with `images[0].name == "default"` |

### 8.3 Fixtures

New in `scripts/build_fixtures/main.go`:

- `v5_bundle_sources/{a,b}_baseline.{oci,s2}.tar` — two baselines.
- `v5_bundle_sources/{a,b}_target.{oci,s2}.tar` — two targets
  constructed so:
  - One shipped layer is identical across targets
    (→ force-full dedup path).
  - One layer is patchable against the image's own baseline
    (→ per-image patch-from path).
  - One layer comes from a shared base layer in both baselines
    (→ `required_from_baseline` × 2 pointing at the same digest).
- `v5_bundle_spec.json` — static spec referencing the four files
  above.

`testdata/legacy/phase1_oci.tar` is **pinned once**: the
`scripts/build_fixtures` target that generates it is removed after the
first production, and the file is committed as an immutable artefact.
The rejection test asserts on its bytes.

## 9. Backward compatibility

**Phase 1 on-disk archives become unimportable.** This is the
intentional break authorised by the pre-release status and Phase 1
decision #1. Breakage presents as a clear error:

```
diffah: archive uses Phase 1 schema (feature marker missing); this
  version requires bundle format — please re-export with the current
  diffah.
```

**CLI backward compatibility**:
- Positional `diffah export BASELINE TARGET OUT` still works (emits
  bundle of one).
- Positional `diffah import ARCHIVE BASELINE --output DIR` still works
  for bundle-of-one archives (emits into `DIR/default/`).
- The import output layout changes slightly even for single-image:
  Phase 1 wrote the image into `DIR` directly; bundle writes into
  `DIR/<name>/`. Documented in CHANGELOG.

**API backward compatibility**:
- `pkg/diff.Sidecar` is replaced by a new shape (`BundleSidecar` with
  `Blobs`, `Images`, etc.). The package rename discussion is deferred
  to the plan (keep `pkg/diff` or move to `pkg/bundle`).
- `Plan` struct (`pkg/diff/plan.go`) remains internal to exporter
  per-pair planning; it no longer appears in the sidecar directly.
- `DryRunReport` gains the multi-image fields; existing fields are
  preserved but their semantics shift to "totals across all images."

## 10. Risks

### 10.1 Force-full rule leaves bytes on the table

Severity: low. Two target images shipping an identical unique-layer
digest is a rare scenario; the rule is conservative but correct.
Mitigation: II.c replaces it with reachability-aware patch dedup.

### 10.2 Breaking change ambushes early adopters

Severity: low (pre-release). Mitigation: CHANGELOG callout, README
section, and the rejection error message literally names the fix.

### 10.3 CLI surface explosion

Severity: medium. Export alone now has three invocation shapes with a
mutex matrix; import has four. Typos on the boundary (e.g.,
`--pair service-a:baseline.tar` without `=TARGET`) can fail
confusingly.

Mitigation: §8.1 CLI parsing tests are intentionally large; each
rejection message is covered by an individual test case. `diffah help
export` gains an EXAMPLES section covering all three invocation shapes
with commentary.

### 10.4 Output-layout surprise (always subdirs)

Severity: low. Single-image users who scripted around
`--output IMG_DIR` now get `IMG_DIR/default/`. Mitigation: CHANGELOG;
the rename is a one-line `mv` fix for anyone impacted.

### 10.5 Sequential processing of large bundles

Severity: low for MVP. A 10-image 500 MB/image bundle exports in
minutes. Not a correctness issue; parallelism lives in Track III.

### 10.6 Legacy Phase 1 fixture rots

Severity: low. The fixture is immutable by design — we pin its bytes
once and never regenerate it. The rejection test asserts on message
text (stable) and file presence (by hash in a Go test helper, not
git-generated).

## 11. Rollout

- Single patch in an atomic commit series: sidecar schema rewrite →
  exporter rewrite → importer rewrite → CLI rewrite → fixtures → tests.
- Staged commits so each intermediate state still builds (a failing
  test on an intermediate commit is allowed; a non-building commit is
  not).
- CHANGELOG entry: "Breaking: all archives are now bundles. Phase 1
  single-image archives no longer import. Re-export with this
  version."
- README gets a "Bundle format" subsection and a migration paragraph.
- `diffah help export` and `diffah help import` updated with all
  invocation shapes and an EXAMPLES section.

## 12. Open questions (deferred to plan)

1. Whether to rename `pkg/diff` to `pkg/bundle` given the schema shift.
   Default: keep `pkg/diff` to minimise churn; the types inside it
   evolve.
2. Whether the `force-full` dedup count should surface in
   `diffah inspect` as a first-class line. Default: yes, one new line
   (`forced-full due to dedup: N blobs`). Plan confirms.
3. Whether to reserve `name == "default"` (currently tolerated per
   decision #12). Default: tolerate; document.
4. Whether per-pair `intra_layer` overrides in bundle spec are worth a
   follow-up spec (II.a.1). Defer; revisit after first production use.
5. Whether positional single-image import should emit into `--output`
   directly (Phase 1 behaviour) instead of `--output/default/`.
   Default: always subdirs (decision #10). Revisit if user pushback
   materialises post-release.
