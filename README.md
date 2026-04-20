# diffah

`diffah` computes layer-level diffs between container images and packages them
as portable archives. It is designed for environments where
registry-to-registry replication is not available and images must travel as
files (air-gapped deployments, customer deliveries, offline mirrors).

A v2 image that shares base layers with a v1 baseline typically ships as a
delta archive that is **10% or less** of the full image size — only the layers
that actually changed travel.

## How it works

**Producer side** — `diffah export` reads the target image and the baseline
manifest, computes which layers are new, and packages only the new blobs plus
the target manifest and config into a portable `.tar` along with a sidecar
(`diffah.json`) describing which blobs the consumer must resolve from its
local baseline.

**Consumer side** — `diffah import` extracts the delta, opens the local
baseline image, verifies every required baseline blob is reachable
(fail-fast), then reconstructs the full target image by overlaying the delta
over the baseline and writing the result in the requested format.

## Install

Download the latest release from the [releases page][releases] or build from
source:

```bash
go install -tags containers_image_openpgp github.com/leosocy/diffah@latest
```

[releases]: https://github.com/leosocy/diffah/releases

## Usage

Produce a delta archive from two image references:

```bash
diffah export \
  --target   docker://registry.example.com/app:v2 \
  --baseline docker://registry.example.com/app:v1 \
  --platform linux/amd64 \
  --output   ./app_v1_to_v2.tar
```

Reconstruct the full image on the consumer side:

```bash
diffah import \
  --delta    ./app_v1_to_v2.tar \
  --baseline docker://registry.internal/app:v1 \
  --output   ./app_v2.tar
```

The output is a `docker-archive` by default (compatible with `docker load`).
Use `--output-format oci-archive` or `--output-format dir` to emit other
formats.

Preview what a delta contains and how much it saves:

```bash
diffah inspect ./app_v1_to_v2.tar
```

Sample inspect output:

```
archive: ./app_v1_to_v2.tar
version: v1
platform: linux/amd64
target manifest digest: sha256:ef053f...
baseline manifest digest: sha256:937a56...
shipped: 1 blobs (2076 bytes)
required: 1 blobs (34332 bytes)
saved 94.3% vs full image
```

Verify baseline reachability without writing anything:

```bash
diffah export --dry-run --target ... --baseline ... --output /dev/null
diffah import --dry-run --delta ... --baseline ... --output /dev/null
```

## Supported transports

Both `--target` and `--baseline` accept any `containers-image` transport:

| Transport        | Example                                            |
|------------------|----------------------------------------------------|
| `docker`         | `docker://registry.example.com/app:v1`             |
| `docker-archive` | `docker-archive:./app-v1.tar`                      |
| `oci-archive`    | `oci-archive:./app-v1.tar`                         |
| `dir`            | `dir:/var/images/app-v1/`                          |

Additionally, `--baseline-manifest` accepts a path to a standalone
`manifest.json` for cases where the original baseline image is no longer
available but its manifest digest set is known.

## Design

See [`docs/superpowers/specs/2026-04-20-diffah-design.md`][spec] for the full
design, including the delta archive format, export/import algorithms, error
contracts, and the testing strategy.

[spec]: docs/superpowers/specs/2026-04-20-diffah-design.md

## License

Apache-2.0 — see [LICENSE](LICENSE).
