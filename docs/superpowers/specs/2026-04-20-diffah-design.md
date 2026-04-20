# diffah — Design Spec (v1)

- **Status**: Approved (brainstorming → ready for implementation plan)
- **Date**: 2026-04-20
- **Repository**: https://github.com/leosocy/diffah
- **Module**: `github.com/leosocy/diffah`

---

## 1. Background

Container images are typically distributed through registries. When source and destination registries are both reachable, layer-level deduplication on push/pull keeps incremental transfers small. In air-gapped, edge, or otherwise network-isolated environments the typical workaround is to ship the full image as an archive (`docker save | gzip`, `skopeo copy ... oci-archive:...`), which retransmits every byte on every release — even when only a few layers actually changed.

`skopeo` and the underlying `containers-image` library already perform "skip blob if destination has it" inside a single `copy.Image` call, but that behavior is intra-operation: source and destination must both be reachable from the same process. There is no first-class workflow for "produce a portable delta archive in environment A, apply it in environment B".

`diffah` fills that gap. It computes a layer-set diff between a baseline and a target image, exports only the new layers (plus the target manifest and config) into a portable archive, and reconstructs the full target image from any baseline source on the receiving side.

---

## 2. Scope and Non-goals

### 2.1 In scope (v1)

- Single (baseline, target) pair per `diffah export` invocation; one delta archive per call.
- **Export baseline input**: any `containers-image` transport (`docker://`, `oci-archive:`, `oci:`, `containers-storage:`, `dir:`, …) **or** a manifest JSON file via `--baseline-manifest` (when the baseline image itself is no longer accessible).
- **Import baseline input**: any transport. Three primary paths must be validated end-to-end: a private registry (`docker://`), a containerd content store (`containers-storage:`), and a previously delivered archive (`oci-archive:` / `dir:`).
- **Multi-arch images**: manifest lists are accepted but `--platform os/arch[/variant]` is required to pick a single instance. One delta archive corresponds to one platform.
- **Output formats**: `docker-archive` (default), `oci-archive`, or `dir`, selectable via `--output-format`.
- **Internal delta layout**: `containers-image` `dir:` layout plus a `diffah.json` sidecar, wrapped in a `tar` (optionally `--compress=zstd`).
- **Manifest formats**: both Docker schema 2 (`application/vnd.docker.distribution.manifest.v2+json`) and OCI (`application/vnd.oci.image.manifest.v1+json`) inputs are supported with no implicit conversion. Manifest bytes are preserved verbatim inside the delta archive.
- **`--dry-run`**: read-only mode for both `export` and `import`, used to validate inputs and report savings or baseline reachability without writing output.
- **`diffah inspect <delta>`**: prints the sidecar plus per-blob size statistics.
- **Release artifacts**: cross-compiled binaries for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.

### 2.2 Out of scope (v1)

- Producing a full image (no baseline). Use `skopeo copy` for the bootstrap delivery; `diffah` only handles the incremental case.
- Intra-layer binary delta (`bsdiff`, zstd dictionary, etc.). Deferred to v2 pending real-world data on compression gains.
- Multi-image batching. Driven by the caller (Makefile, shell loop) to keep `diffah` single-purpose.
- Image signing. Existing signatures attached to the target are passed through; `diffah` does not generate new signatures.
- Encrypted images.
- Direct writes into a runtime (containerd push, docker daemon load, registry push). The caller's existing tooling consumes the output file.
- Windows builds.

---

## 3. High-level architecture

```
┌────────────────── Producer side ────────────────────┐
│                                                     │
│  baseline:src ──┐                                   │
│                 │   ┌─────────────────┐             │
│                 ├──▶│ diffah export   │──▶ delta.tar│
│                 │   │  diff layers    │  (dir layout│
│  target:src ────┘   │  copy.Image     │   + sidecar)│
│  (--platform)       │  skip baseline  │             │
│                     └─────────────────┘             │
└─────────────────────────────────────────────────────┘
                           │
                  (out-of-band transfer)
                           │
                           ▼
┌────────────────── Consumer side ────────────────────┐
│                                                     │
│  delta.tar ─────┐   ┌─────────────────┐             │
│                 │   │ diffah import   │             │
│                 ├──▶│  probe baseline │──▶ new.tar  │
│                 │   │  composite src  │  (--output- │
│  baseline:any ──┘   │  copy.Image     │   format)   │
│  (registry /        └─────────────────┘             │
│   containerd /                                      │
│   prior archive)                                    │
│                                                     │
│  → existing tooling consumes new.tar (docker load,  │
│    nerdctl load, registry push, etc.)               │
└─────────────────────────────────────────────────────┘
```

Dependencies flow strictly `Interface → Service → Domain → Infrastructure`. All external I/O (`containers-image` transports, filesystem, archive packing) is reached through interfaces injected into the service layer.

---

## 4. Module layout

```
diffah/
├── main.go                     # bootstrap → cmd.Execute()
├── cmd/                        # Interface (cobra)
│   ├── root.go                 # root command, global flags, version
│   ├── export.go
│   ├── import.go
│   └── inspect.go
├── pkg/diff/                   # Domain: pure types, no framework deps
│   ├── plan.go                 # DeltaPlan, RequiredBlob, etc.
│   ├── sidecar.go              # diffah.json schema and (de)serialization
│   └── errors.go               # domain error types
├── pkg/exporter/               # Service: orchestrates export
│   ├── exporter.go
│   ├── baseline.go             # BaselineSet interface (image / manifest-only)
│   └── known_dest.go           # ImageDestination wrapper: skip known blobs
├── pkg/importer/               # Service: orchestrates import
│   ├── importer.go
│   └── composite_src.go        # ImageSource wrapper: delta first, baseline fallback
├── internal/imageio/           # Infrastructure: containers-image adapters
│   ├── reference.go            # parse "transport:reference" strings
│   └── policy.go               # signature policy (default insecureAcceptAnything)
├── internal/archive/           # Infrastructure: tar / optional zstd outer wrapper
│   ├── writer.go
│   └── reader.go
├── internal/oci/               # Infrastructure: dir / oci layout helpers
│   └── layout.go
├── testdata/                   # fixture images for unit and integration tests
└── docs/superpowers/specs/     # design and plan documents
```

`pkg/diff` is unit-testable in isolation. The service layer takes its dependencies through constructor-injected interfaces, so tests can swap in fakes without touching real I/O.

---

## 5. CLI surface

```
diffah export \
  --target           <transport:ref> \
  --baseline         <transport:ref> | --baseline-manifest <path>   # mutually exclusive
  [--platform        os/arch[/variant]] \
  [--compress        none|zstd]                  (default: none)
  [--dry-run] \
  --output           <path>

diffah import \
  --delta            <path> \
  --baseline         <transport:ref> \
  [--output-format   docker-archive|oci-archive|dir]   (default: docker-archive)
  [--dry-run] \
  --output           <path>

diffah inspect <delta-archive-path>
```

**Global flags** (`cmd/root.go`): `--log-level (info|debug|warn|error)`, `--version`.

**Exit codes**: `0` success; `1` runtime error; `2` argument error (so callers can distinguish misuse from operational failure).

**Examples**:

```bash
# Producer: diff two versions stored in a registry.
diffah export \
  --target   docker://registry.example.com/app:v2 \
  --baseline docker://registry.example.com/app:v1 \
  --platform linux/amd64 \
  --output   ./out/app_v1_to_v2.tar

# Producer: baseline image is gone, only its manifest is retained.
diffah export \
  --target            docker://registry.example.com/app:v2 \
  --baseline-manifest ./manifests/app_v1.json \
  --platform          linux/amd64 \
  --output            ./out/app_v1_to_v2.tar

# Consumer: baseline served by a private registry; default docker-archive output.
diffah import \
  --delta    ./inbox/app_v1_to_v2.tar \
  --baseline docker://registry.internal/app:v1 \
  --output   ./staging/app_v2.tar

# Consumer: baseline read from a previously delivered archive; OCI output.
diffah import \
  --delta         ./inbox/app_v1_to_v2.tar \
  --baseline      oci-archive:./previous/app_v1.tar \
  --output-format oci-archive \
  --output        ./staging/app_v2.tar

# Consumer: dry-run baseline reachability check.
diffah import --dry-run \
  --delta    ./inbox/app_v1_to_v2.tar \
  --baseline docker://registry.internal/app:v1
```

---

## 6. Delta archive format

### 6.1 Physical layout

```
delta.tar               (delta.tar.zst when --compress=zstd)
├── version             # containers-image dir-layout schema marker
├── manifest.json       # raw target manifest bytes (schema 2 or OCI)
├── <config-blob>       # config blob; filename per containers-image dir transport
├── <new-layer-blob>…   # only layers absent from baseline; same naming convention
└── diffah.json         # sidecar, top-level
```

The internal layout is the `containers-image` `dir:` layout rather than `oci:` because the `oci-archive:` destination forces OCI media types and rewrites schema 2 manifests, which changes the manifest digest. The `dir:` layout is media-type-agnostic and writes manifest bytes verbatim. Inspection from outside diffah is straightforward:

```
tar -xf delta.tar -C tmp/
skopeo inspect dir:tmp/
```

### 6.2 Sidecar `diffah.json` schema (v1)

```json
{
  "version": "v1",
  "tool": "diffah",
  "tool_version": "v0.1.0",
  "created_at": "2026-04-20T13:21:00Z",
  "platform": "linux/amd64",
  "target": {
    "manifest_digest": "sha256:aaa...",
    "manifest_size": 1234,
    "media_type": "application/vnd.docker.distribution.manifest.v2+json"
  },
  "baseline": {
    "manifest_digest": "sha256:bbb...",
    "media_type":      "application/vnd.docker.distribution.manifest.v2+json",
    "source_hint":     "docker://registry.example.com/app:v1"
  },
  "required_from_baseline": [
    {"digest": "sha256:ccc...", "size": 31457280, "media_type": "application/vnd.docker.image.rootfs.diff.tar.gzip"}
  ],
  "shipped_in_delta": [
    {"digest": "sha256:eee...", "size": 524288000, "media_type": "..."}
  ]
}
```

**Reader contract**:

- Reject any `version` not in the implementation's known set (a v1 reader accepts only `"v1"`).
- Validate that all required fields are present (`target.manifest_digest`, `required_from_baseline`, `platform`).
- In `--baseline-manifest` mode, `baseline.manifest_digest` is the SHA-256 of the file's raw bytes.
- `platform`: when `--platform` is provided, it is recorded verbatim; for single-platform images without the flag, derive the value from the image config's `architecture` and `os` fields.

`shipped_in_delta` is redundant with the blob set inside the archive but lets `diffah inspect` report savings without scanning the tar.

---

## 7. Export algorithm

**Inputs**: `target`, `baseline | baseline-manifest`, `--platform`, `--output`, optional `--compress`, `--dry-run`.

```
Step 1 — Resolve baseline layer-digest set
  if baseline is an image reference:
      img := imageio.Open(baseline)
      manifest := img.Manifest()
      if manifest is a list:
          --platform required → pick sub-manifest
      baselineDigests        := set(manifest.Layers.map(.Digest))
      baselineManifestDigest := manifest.Digest()
      baselineMime           := manifest.MediaType
  else (manifest-only mode):
      raw := readFile(baseline-manifest-file)
      manifest := parse(raw)
      // manifest list handling identical to above
      baselineDigests        := set(manifest.Layers.map(.Digest))
      baselineManifestDigest := digest(raw)
      baselineMime           := manifest.MediaType

Step 2 — Open target
  src := imageio.Open(target)
  if src.Manifest() is a list:
      --platform required → pick instance

Step 3 — Build a "skip known baseline blobs" destination
  ociDir   := tmpDir
  dirDest  := imageio.NewDirDest(ociDir)
  knownDest := exporter.NewKnownBlobsDest(dirDest, baselineDigests)
  // TryReusingBlob(d):
  //   if d ∈ baselineDigests: return (true, BlobInfo{Digest:d}, nil)  // copy.Image skips
  //   else: delegate to dirDest.TryReusingBlob (returns false → triggers PutBlob)
  // PutBlob / PutManifest / AddSignature / Commit: delegated to dirDest.

Step 4 — copy.Image
  copy.Image(ctx, policyCtx, knownDest, src, &copy.Options{
      ReportWriter:          os.Stderr,
      ImageListSelection:    CopySpecificImages | CopySystemImage,
      Instances:             [chosen sub-manifest digest, when applicable],
      PreserveDigests:       true,
      ForceManifestMIMEType: "",   // no format conversion
  })
  // ociDir now contains: target manifest, target config, only the new layers.

Step 5 — Build sidecar
  shipped  := target.Layers \ baselineDigests
  required := target.Layers ∩ baselineDigests
  sidecar  := diff.NewSidecar(target, baseline, shipped, required, platform)

Step 6 — Pack to output
  archive.Pack(ociDir, sidecar, output, compress)
  // Write to <output>.tmp, then atomic rename to <output>.

Step 7 — Verify
  re-open output → read manifest → assert digest equals expected
  // Defensive check against archive.Pack bugs.
```

**Dry-run**: execute steps 1, 2, 5; skip `copy.Image` and any disk writes. Print the computed `required`, `shipped`, and the resulting savings. Exit 0.

**Design points**:

- `PreserveDigests: true` and an unset `ForceManifestMIMEType` together guarantee bit-exact pass-through of manifest, config, and layer bytes.
- Atomic rename (`<output>.tmp` → `<output>`) prevents partial files from being consumed.
- All retry, progress reporting, and blob caching are inherited from `copy.Image`.

---

## 8. Import algorithm

**Inputs**: `--delta`, `--baseline`, `--output`, optional `--output-format`, `--dry-run`.

```
Step 1 — Open delta and read sidecar
  archive.Extract(delta, tmpDir, options{compress: auto-detect})
  sidecar := diff.ParseSidecar(tmpDir/diffah.json)
  if sidecar.Version != "v1": ERROR ErrUnsupportedSchemaVersion

Step 2 — Treat delta as a dir source
  deltaSrc := imageio.NewDirSource(tmpDir)
  if deltaSrc.Manifest().Digest() != sidecar.Target.ManifestDigest: ERROR

Step 3 — Open baseline
  baselineSrc := imageio.Open(baseline)
  if baselineSrc.Manifest() is a list:
      pick sub-manifest using sidecar.Platform (no extra --platform flag needed)

Step 4 — Fail-fast probe
  // types.ImageSource has no HasBlob; preferred path is to compare digest sets.
  baselineLayerDigests := set(baselineSrc.Manifest().Layers.map(.Digest))
  for blob in sidecar.RequiredFromBaseline:
      if blob.Digest ∉ baselineLayerDigests:
          return ErrBaselineMissingBlob{Digest:blob.Digest, Source:baseline}
  // Fallback: open GetBlob and immediately close to verify presence,
  // used only by transports where Manifest().Layers may be incomplete.

Step 5 — Composite source
  composite := importer.NewCompositeSource(deltaSrc, baselineSrc, sidecar)
  // GetManifest()        → deltaSrc.GetManifest()
  // GetBlob(d):
  //   if delta has d: serve from delta
  //   else:           serve from baseline
  // LayerInfosForCopy(): pass through deltaSrc.LayerInfosForCopy

Step 6 — copy.Image to output (write <output>.tmp, atomic rename on success)
  outputDest := imageio.NewDest(<output>.tmp, --output-format)
  opts := &copy.Options{}
  if --output-format == dir:
      opts.PreserveDigests = true   // pass-through; manifest digest must remain bit-exact
  // For non-dir outputs, leave PreserveDigests unset:
  // containers-image must rewrite the manifest media type when emitting OCI or
  // docker-archive from a schema 2 source. PreserveDigests=true would refuse.
  // Layer content and digests are still verified by the library itself.
  copy.Image(ctx, policyCtx, outputDest, composite, opts)
  rename(<output>.tmp, <output>)

Step 7 — Post verification
  if --output-format == dir:
      assert output manifest digest == sidecar.Target.ManifestDigest
  else:
      // mime conversion path: manifest digest necessarily changes;
      // verify the layer digest set instead.
      outputLayers   := set(reopen(output).Manifest().Layers.map(.Digest))
      expectedLayers := set(sidecar.RequiredFromBaseline ∪ sidecar.ShippedInDelta)
      assert outputLayers == expectedLayers
```

**Dry-run**: execute steps 1–4; skip `copy.Image`. Print baseline reachability per required digest. Exit 0 if and only if every required digest is reachable.

**Design points**:

- The fail-fast probe in step 4 surfaces a missing-baseline situation before any large I/O.
- The composite source is the only piece of glue code (~50 lines).
- `--output-format` selects the output transport in `imageio.NewDest`.

---

## 9. Error handling

### 9.1 Domain error types (`pkg/diff/errors.go`)

```go
type ErrManifestListUnselected   struct{ Ref string }
type ErrUnsupportedSchemaVersion struct{ Got string }
type ErrSidecarSchema            struct{ Reason string }
type ErrBaselineMissingBlob      struct{ Digest, Source string }
type ErrIncompatibleOutputFormat struct{ SourceMime, OutputFormat string }
type ErrSourceManifestUnreadable struct{ Ref string; Cause error }
type ErrDigestMismatch           struct{ Where, Want, Got string }
```

Each type implements `Error()` and (where applicable) `Unwrap()` for `errors.Is` / `errors.As` compatibility.

### 9.2 Wrapping conventions

- The service layer never returns raw transport errors; all are wrapped into a domain error.
- The infrastructure layer uses `fmt.Errorf("…: %w", err)` for cause chains.
- Every error carries operational context (image reference, digest, phase).

### 9.3 CLI mapping

| Category | Exit code | Output |
|---|---|---|
| Argument parsing (cobra) | 2 | `Error: <msg>\n<usage>` to stderr |
| Recoverable domain error | 1 | One-line human-readable message plus a remediation hint |
| Infrastructure error | 1 | `%+v` expansion of the cause chain |

---

## 10. Testing strategy

Pyramid: **unit ≥70% / integration ~25% / e2e ~5%**.

### 10.1 Unit tests (no I/O, no network)

| Package | Key scenarios |
|---|---|
| `pkg/diff/sidecar` | schema validation, version rejection, JSON round-trip, missing required fields |
| `pkg/diff/plan` | digest set ∩, \, ∪ |
| `pkg/exporter/baseline` | image and manifest-only paths produce identical digest sets for equivalent inputs |
| `pkg/exporter/known_dest` | `TryReusingBlob` known/unknown branches; remaining methods delegate correctly |
| `pkg/importer/composite_src` | `GetBlob` delta / baseline / missing branches |
| `internal/imageio/reference` | transport string parsing across formats; error shapes |

Mocking discipline: mock only at the `types.ImageSource` / `types.ImageDestination` boundary; never mock first-party code.

### 10.2 Integration tests (fixture images, no network)

Fixtures generated by `make fixtures` (using `skopeo` or `crane` to layer deterministic content on top of `scratch`), committed under `testdata/`, total size under 5 MB:

- `busybox-tiny:v1` and `busybox-tiny:v2` in both OCI and schema 2 variants.
- A shared base layer plus version-specific layers.

Core scenarios:

1. **OCI happy path**: oci-archive baseline + oci-archive target → oci-archive output; round-trip preserves manifest digest.
2. **Schema 2 happy path**: same as above but schema 2 throughout; verifies media-type pass-through.
3. **Mixed output**: schema 2 source → docker-archive output, and schema 2 source → oci-archive output (exercises the conversion path in `copy.Image`).
4. **Manifest-only baseline mode**.
5. **Missing baseline blob**: fail-fast error contains the specific digest.
6. **Manifest list without `--platform`**: clear error.
7. **`--dry-run`** for both export and import: writes nothing, statistics correct.

### 10.3 E2E tests (compiled binary)

- Smoke test invoking the real binary against fixtures.
- `goreleaser release --snapshot --clean` to verify release artifacts run.

### 10.4 Coverage

- Service and domain packages: line coverage ≥ 80%.
- Infrastructure packages: ≥ 60% (I/O paths primarily exercised by integration tests).

---

## 11. Project structure and tooling

### 11.1 `go.mod`

```
module github.com/leosocy/diffah

go 1.25.4

require (
    github.com/spf13/cobra v1.8.x
    go.podman.io/image/v5 v5.39.x
    github.com/klauspost/compress v1.17.x   // outer zstd wrapper
)
```

The legacy `bakgo.mod` is removed.

### 11.2 Build tags

Enabled at compile time to keep the binary small and CGo-free:

- `containers_image_openpgp` — pure-Go OpenPGP, avoiding the GnuPG CGo dependency.
- `exclude_graphdriver_btrfs`, `exclude_graphdriver_devicemapper` — drop graph drivers (only relevant if `containers/storage` is pulled transitively; apply on demand).

### 11.3 `.golangci.yaml` rewrite

The current configuration references several linters that have been removed in newer `golangci-lint` releases (`interfacer`, `varcheck`, `structcheck`, `deadcode`, `scopelint`, `golint`, `maligned`). The first commit migrates to:

`gofmt`, `govet`, `staticcheck`, `gosec`, `revive`, `unused`, `errcheck`, `goconst`, `gocritic`, `gocyclo`, `goimports`, `misspell`, `funlen` (60 hard limit, 40 statements), `lll` (120), `bodyclose`, `whitespace`, `prealloc`, `errorlint`, `nestif`, `nolintlint`.

`.pre-commit-config.yaml` is updated to a current `golangci-lint` hook revision.

### 11.4 `.goreleaser.yaml`

- `CGO_ENABLED=0` (relies on the build tags above for pure-Go paths).
- Targets: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.
- Archives: `tar.gz` for Linux, `zip` for macOS.
- Generated checksum file.
- SBOM (`syft`) deferred to v2.

### 11.5 CI (GitHub Actions)

| Workflow | Trigger | Contents |
|---|---|---|
| `lint.yml` | PR / push | `golangci-lint run ./...` |
| `test.yml` | PR / push | `go test ./... -race -cover`, Go 1.25.x, ubuntu + macos matrix |
| `release.yml` | tag `v*` | `goreleaser release --clean` |
| `integration.yml` | manual / nightly | integration tests requiring a docker daemon or registry |

### 11.6 Makefile

`make build`, `make test`, `make test-integration`, `make lint`, `make fixtures`, `make snapshot`.

---

## 12. Future work (v2+ backlog, explicitly not in v1)

- Intra-layer binary delta (bsdiff or zstd dictionary). Requires real-world data on compression gains before committing.
- Multi-image batching with cross-image layer deduplication in a single combined archive.
- Sigstore / cosign signature verification and generation.
- Encrypted images (containers-image already supports `ImageEncryption`; pass-through where applicable).
- `--auto-convert-to-oci` with explicit before/after digest fields in the sidecar, if schema 2 → OCI conversion becomes a common request.
- Direct push: `--output docker://...` writing straight to a registry, eliminating the intermediate file.

---

## 13. Design decisions and rationale

| Decision | Choice | Rationale |
|---|---|---|
| Import output format | docker-archive / oci-archive / dir, default docker-archive | Best fit for the `docker save` / `docker load` workflows still common in offline delivery |
| Export input | Any transport, plus manifest-only fallback | The baseline image may be evicted by registry retention before a delta is computed |
| Single vs. multi-image | Single pair per invocation | Unix philosophy; batching is composable from the caller |
| Baseline source on consumer side | Registry, containerd content store, or prior archive — all supported | Consumer environments vary widely |
| Multi-arch | Manifest lists supported, but `--platform` required | Keeps the data model single-image; sufficient for delivery scenarios |
| Intra-layer diff | Not in v1 | Application layers typically change wholesale; complexity is high and gain uncertain |
| Manifest media type | Pass-through, no conversion; both schema 2 and OCI inputs | Production images are still frequently schema 2; preserving digests keeps the integrity contract simple |
| Upstream library | `go.podman.io/image/v5` (not deprecated `containers/image/v5`) | `skopeo` v1.22.2 has migrated; the upstream repo is now `github.com/containers/container-libs` |
