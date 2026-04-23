# diffah

`diffah` computes layer-level diffs between container images and packages them
as portable archives. It is designed for environments where
registry-to-registry replication is not available and images must travel as
files (air-gapped deployments, customer deliveries, offline mirrors).

A v2 image that shares base layers with a v1 baseline typically ships as a
delta archive that is **10% or less** of the full image size — only the layers
that actually changed travel.

## How it works

**Producer side** — `diffah export` reads one or more target images paired
with their baselines, computes which layers are new, and packages only the
unique blobs plus each target's manifest and config into a portable `.tar`
along with a sidecar (`diffah.json`) describing which blobs the consumer must
resolve from its local baselines. A content-addressed blob pool deduplicates
layers, manifests, and configs across images — shared blobs are stored once.

**Consumer side** — `diffah import` extracts the delta, opens the local
baseline images, verifies every required baseline blob is reachable
(fail-fast), then reconstructs each target image by overlaying the delta
blobs over the baseline and writing the result in the requested format.

## How diffah picks baselines for intra-layer patches

For each shipped layer, diffah picks the baseline layer that shares the
most *bytes* of content, measured by tar-entry digest intersection on
the decompressed layer bytes. Ties on byte-weight break by size-closest,
then by baseline digest order for determinism.

diffah falls back to picking the baseline closest in compressed byte
size in three cases: (1) the shipped layer is not a parseable tar (rare
— typically only foreign OCI configs routed as layer blobs), (2) none
of the baseline layers fingerprint successfully, or (3) the shipped
layer shares no tar entries with any baseline.

## Install

Download the latest release from the [releases page][releases] or build from
source:

```bash
go install -tags containers_image_openpgp github.com/leosocy/diffah@latest
```

[releases]: https://github.com/leosocy/diffah/releases

## Requirements

- Go 1.25+ (build only).
- `zstd ≥ 1.5` on `$PATH` — **recommended for best compression**. Required only when:
  - using `--intra-layer=required` on `diffah export`;
  - importing an archive that was produced with intra-layer patches.

Archives produced with `--intra-layer=off` (or `auto` on a host without
zstd — `auto` downgrades silently and warns on stderr) import anywhere,
including hosts with no `zstd` binary at all.

Run `diffah inspect <archive>` to see whether a given archive requires
`zstd` at import time.

## Usage

### Single-image delta

Produce a delta archive from a baseline and target:

```bash
diffah export \
  --pair app=v1.tar,v2.tar \
  --platform linux/amd64 \
  ./app_v1_to_v2.tar
```

Reconstruct the full image on the consumer side:

```bash
diffah import \
  --baseline app=v1.tar \
  ./app_v1_to_v2.tar \
  ./out/
# output at ./out/app.tar
```

### Multi-image bundle

Package multiple image deltas into a single deduplicated bundle:

```bash
diffah export \
  --pair svc-a=v1a.tar,v2a.tar \
  --pair svc-b=v1b.tar,v2b.tar \
  --platform linux/amd64 \
  ./bundle.tar
```

Import every image from the bundle in one command:

```bash
diffah import \
  --baseline svc-a=v1a.tar \
  --baseline svc-b=v1b.tar \
  ./bundle.tar \
  ./out/
# output at ./out/svc-a.tar and ./out/svc-b.tar
```

`OUTPUT` is always a directory. Per-image output lands at
`OUTPUT/<name>.tar` for archive output formats, or `OUTPUT/<name>/` for
`--output-format dir`.

### Using spec files

For complex bundles, use a JSON spec file instead of repeating `--pair`:

```bash
diffah export --bundle bundle.json ./bundle.tar
diffah import --baseline-spec baselines.json ./bundle.tar ./output.tar
```

### Output format

By default `--output-format` is `auto`: the output format is chosen to
match the source image's manifest media type so the reconstructed bytes
(and therefore the manifest digest) are identical to the original. Pass
`--output-format docker-archive`, `oci-archive`, or `dir` to override.

`dir` preserves bytes regardless of source. Explicitly picking
`docker-archive` for an OCI source (or `oci-archive` for a Docker schema 2
source) triggers a manifest media-type conversion, which changes every
layer and manifest digest. diffah refuses this by default; pass
`--allow-convert` to acknowledge the digest drift.

### Inspect and dry-run

Preview what a delta contains and how much it saves:

```bash
diffah inspect ./bundle.tar
```

Sample inspect output:

```
archive: ./bundle.tar
version: v1
feature: bundle
tool: diffah
tool_version: 0.3.0
platform: linux/amd64
images: 2
blobs: 5 (full: 4, patch: 1)
avg patch ratio: 12.3%
total archive: 123456 bytes
patch savings: 87654 bytes (41.5% vs full)

--- image: svc-a ---
  target manifest digest: sha256:ef053f... (application/vnd.oci.image.manifest.v1+json)
  baseline manifest digest: sha256:937a56... (application/vnd.oci.image.manifest.v1+json)
  baseline source: svc-a-baseline
```

Verify baseline reachability without writing anything:

```bash
diffah export --dry-run --pair app=v1.tar,v2.tar ./bundle.tar
diffah import --dry-run --baseline app=v1.tar ./bundle.tar ./output.tar
```

### Strict mode

By default, `diffah import` skips images whose baselines are not provided.
Pass `--strict` to require all baselines:

```bash
diffah import --strict \
  --baseline svc-a=v1a.tar \
  --baseline svc-b=v1b.tar \
  ./bundle.tar ./output.tar
```

## Supported transports

Both baseline and target paths accept local archive files (OCI or Docker
schema 2 format):

| Format           | Example            |
|------------------|--------------------|
| OCI archive      | `./app-v1.tar`     |
| Docker archive   | `./app-v1.tar`     |

## Design

See [`docs/superpowers/specs/2026-04-20-diffah-design.md`][spec] for the full
design, including the delta archive format, export/import algorithms, error
contracts, and the testing strategy.

[spec]: docs/superpowers/specs/2026-04-20-diffah-design.md

## Compatibility

Exit codes, sidecar schema evolution rules, and log/progress output
stability guarantees are documented in [docs/compat.md](docs/compat.md).

## License

Apache-2.0 — see [LICENSE](LICENSE).
