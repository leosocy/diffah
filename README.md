<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/diffah-logo-dark.svg">
    <img alt="diffah — only what changed" src="docs/assets/diffah-logo.svg" width="420">
  </picture>
</p>

# diffah

[![test](https://github.com/leosocy/diffah/actions/workflows/test.yml/badge.svg)](https://github.com/leosocy/diffah/actions/workflows/test.yml)
[![lint](https://github.com/leosocy/diffah/actions/workflows/lint.yml/badge.svg)](https://github.com/leosocy/diffah/actions/workflows/lint.yml)
[![integration](https://github.com/leosocy/diffah/actions/workflows/integration.yml/badge.svg)](https://github.com/leosocy/diffah/actions/workflows/integration.yml)
[![release](https://img.shields.io/github/v/release/leosocy/diffah?sort=semver)](https://github.com/leosocy/diffah/releases)
[![license](https://img.shields.io/github/license/leosocy/diffah)](LICENSE)

`diffah` is a CLI for **layer-level diffs between OCI / Docker container
images**. It packages only the changed bytes into a portable archive,
then reconstructs the full target image from any baseline source on the
consumer side — registry, on-disk archive, or an OCI layout directory.

A v2 image that shares base layers with a v1 baseline typically ships
as a delta archive that is **10 % or less** of the full image size —
only layers that actually changed travel.

`diffah` is built for environments where registry-to-registry
replication is not available and images must travel as files: air-gapped
deployments, customer deliveries, offline mirrors, and bandwidth-metered
edge sites.

## Why diffah

Tools like [`skopeo`](https://github.com/containers/skopeo) already
deduplicate layers within a single `copy` operation when both source
and destination are reachable. They do **not** produce a portable
"only what changed" artifact you can hand-carry. `docker save | gzip`
retransmits every byte on every release — even when only a few layers
actually changed.

`diffah` fills that gap. It complements rather than replaces `skopeo`:
the bootstrap delivery still uses a full image (any tool); every
incremental release after that ships as a `diffah` delta.

## Install

### Supported platforms

| OS / arch        | Prebuilt binary | Container image |
|------------------|:---------------:|:---------------:|
| `linux/amd64`    |        ✅       |        ✅       |
| `linux/arm64`    |        ✅       |        ✅       |
| `darwin/amd64`   |        ✅       |        —        |
| `darwin/arm64`   |        ✅       |        —        |
| `windows/*`      |        —        |        —        |

Windows is intentionally out of scope.

### Prebuilt binaries

Download the latest release from the
[releases page](https://github.com/leosocy/diffah/releases) and put
`diffah` on your `$PATH`. Release archives are signed with `cosign`
(keyless / Sigstore); see [`SECURITY.md`](SECURITY.md) for the
verification workflow.

### Container image

```sh
docker run --rm ghcr.io/leosocy/diffah:latest version
```

Multi-arch (`linux/amd64` + `linux/arm64`) — Docker / containerd
selects the right one automatically.

### From source

```sh
go install -tags containers_image_openpgp github.com/leosocy/diffah@latest
```

Or build from a checkout:

```sh
git clone https://github.com/leosocy/diffah.git
cd diffah
make build           # ./bin/diffah
```

### Requirements

- **Go ≥ 1.25** (build only)
- **`zstd` ≥ 1.5** on `$PATH` — required only when:
  - producing deltas with `--intra-layer=required` on `diff` / `bundle`,
  - or applying a delta whose sidecar reports `requires_zstd: yes`.

  Archives produced with `--intra-layer=off` (or `auto` on a host
  without zstd — `auto` downgrades silently and warns on stderr) can
  be applied anywhere, including hosts without `zstd`.

Run `diffah doctor` to check the local environment, or
`diffah inspect <archive>` to see whether a given delta requires
`zstd` at apply time.

## Commands at a glance

| Command           | What it does                                                      |
|-------------------|-------------------------------------------------------------------|
| `diffah diff`     | Compute a single-image delta archive.                             |
| `diffah apply`    | Reconstruct a single image from a delta + a baseline.             |
| `diffah bundle`   | Compute a multi-image delta bundle from a JSON spec.              |
| `diffah unbundle` | Reconstruct every image in a bundle.                              |
| `diffah inspect`  | Print sidecar metadata and savings for a delta archive.           |
| `diffah doctor`   | Run environment preflight checks.                                 |
| `diffah version`  | Print the diffah version.                                         |

Every verb supports `--help` with a copy-paste-ready example, an
`Arguments` block, and the per-verb flag set.

## Quick start

### Single-image delta

```sh
# Producer side: compute a delta archive between two image archives.
diffah diff \
  docker-archive:./app-v1.tar \
  docker-archive:./app-v2.tar \
  ./app_v1_to_v2.delta.tar

# Consumer side: reconstruct app v2 from app v1 + the delta.
diffah apply \
  ./app_v1_to_v2.delta.tar \
  docker-archive:./app-v1.tar \
  oci-archive:./app-v2.restored.tar
```

### Multi-image bundle

```sh
# bundle.json:
# {
#   "pairs": [
#     {"name": "svc-a", "baseline": "docker-archive:v1a.tar", "target": "docker-archive:v2a.tar"},
#     {"name": "svc-b", "baseline": "docker-archive:v1b.tar", "target": "docker-archive:v2b.tar"}
#   ]
# }

diffah bundle bundle.json ./bundle.tar

# baselines.json:
# {"baselines": {"svc-a": "docker-archive:v1a.tar",
#                "svc-b": "docker-archive:v1b.tar"}}
#
# outputs.json:
# {"outputs": {"svc-a": "oci-archive:./out/svc-a.tar",
#              "svc-b": "docker://harbor.local/svc-b:v2"}}

diffah unbundle ./bundle.tar baselines.json outputs.json
```

### Registry round-trip

`diffah` accepts the same transport grammar on both sides — local
archives, OCI layouts, or a registry:

```sh
diffah diff \
  docker://ghcr.io/org/app:v1 \
  docker://ghcr.io/org/app:v2 \
  ./app.delta.tar

diffah apply \
  ./app.delta.tar \
  docker://ghcr.io/org/app:v1 \
  docker://harbor.local/org/app:v2
```

When a baseline is a registry reference, only blobs referenced by
patch-encoded entries cross the wire on the apply side (lazy fetch).
On the producer side every baseline blob is fetched **at most once**
per `diff` / `bundle` invocation, regardless of pair count or top-K.

## Image references

`diffah` mirrors `skopeo`'s `transport:reference` grammar. **A bare
filesystem path without a transport prefix is rejected** — intent is
always explicit.

| Transport            | Syntax                       | `diff` / `bundle` | `apply` / `unbundle` |
|----------------------|------------------------------|:-----------------:|:--------------------:|
| `docker-archive:`    | `docker-archive:PATH`        |        ✅         |          ✅          |
| `oci-archive:`       | `oci-archive:PATH`           |        ✅         |          ✅          |
| `docker://`          | `docker://HOST/REPO[:TAG]`   |        ✅         |          ✅          |
| `oci:`               | `oci:DIR[:REF]`              |        ✅         |          ✅          |
| `dir:`               | `dir:PATH`                   |        ✅         |          ✅          |
| `docker-daemon:`, `containers-storage:`, `ostree:`, `sif:`, `tarball:` | reserved | reserved |

Reserved transports are recognised and parsed but the verb returns
`reserved but not yet implemented` — the grammar is forward-compatible.

### Registry credentials and TLS

Whenever any image reference uses a `docker://`, `oci:`, or `dir:`
transport, the verb installs the standard registry/transport flag block:

| Flag                       | Default | Description                                      |
|----------------------------|---------|--------------------------------------------------|
| `--authfile PATH`          | see below | Path to a containers auth file.                |
| `--creds USER[:PASS]`      | —       | Inline credentials.                              |
| `--username USER`          | —       | Registry username (paired with `--password`).    |
| `--password PASS`          | —       | Registry password (paired with `--username`).    |
| `--no-creds`               | `false` | Disable all credential lookup.                   |
| `--registry-token TOKEN`   | —       | Bearer token (Authorization header).             |
| `--tls-verify`             | `true`  | Verify TLS certificates.                         |
| `--cert-dir PATH`          | —       | Directory of additional CA certs (PEM).          |
| `--retry-times N`          | `3`     | Retries for transient failures (5xx / 429).      |
| `--retry-delay DURATION`   | —       | Fixed retry delay (default: exponential, ≤ 30s). |

**Authfile precedence** (highest to lowest):
`--authfile` → `$REGISTRY_AUTH_FILE` →
`$XDG_RUNTIME_DIR/containers/auth.json` → `$HOME/.docker/config.json`.

Mutual exclusion mirrors `skopeo`'s rules: at most one of `--creds`,
`--username/--password`, `--no-creds`, or `--registry-token` may be
set per invocation.

Non-retryable errors (auth 401/403, 404, manifest schema errors) fail
immediately without consuming the retry budget.

## Spec file formats

### `BUNDLE-SPEC` (input to `diffah bundle`)

```json
{
  "pairs": [
    {"name": "svc-a",
     "baseline": "docker-archive:v1a.tar",
     "target":   "docker-archive:v2a.tar"},
    {"name": "svc-b",
     "baseline": "docker://ghcr.io/org/svc-b:v1",
     "target":   "docker://ghcr.io/org/svc-b:v2"}
  ]
}
```

Names must match `[a-z0-9][a-z0-9_-]*`; both `baseline` and `target`
must carry a transport prefix; relative paths inside file-backed
transports resolve against the spec file's directory.

### `BASELINE-SPEC` (input to `diffah unbundle`)

```json
{
  "baselines": {
    "svc-a": "docker-archive:v1a.tar",
    "svc-b": "docker://ghcr.io/org/svc-b:v1"
  }
}
```

### `OUTPUT-SPEC` (input to `diffah unbundle`)

```json
{
  "outputs": {
    "svc-a": "oci-archive:./out/svc-a.tar",
    "svc-b": "docker://harbor.local/org/svc-b:v2"
  }
}
```

`unbundle` reconstructs each image to its destination by name. Names
in `BASELINE-SPEC` and `OUTPUT-SPEC` must agree with the bundle's
sidecar.

## Encoding tuning

`diff` and `bundle` expose four producer-side knobs that trade CPU /
memory for smaller deltas. All four are documented in
`<verb> --help`.

| Flag                  | Default  | Effect                                                                           |
|-----------------------|----------|----------------------------------------------------------------------------------|
| `--workers N`         | `8`      | Layers fingerprinted and encoded in parallel.                                    |
| `--candidates K`      | `3`      | Top-K most content-similar baseline layers tried per shipped layer; smallest wins. |
| `--zstd-level N`      | `22`     | zstd compression level 1..22 (`22` = "ultra").                                   |
| `--zstd-window-log N` | `auto`   | Long-mode window in log2 bytes, or `auto` (≤128 MiB → 27, ≤1 GiB → 30, > 1 GiB → 31). |

**Determinism guarantee.** For a fixed
`(baseline, target, --candidates, --zstd-level, --zstd-window-log)`
tuple the produced archive is byte-identical regardless of
`--workers`. Pinned by
[`pkg/exporter/determinism_test.go`](pkg/exporter/determinism_test.go).

**Recipes:**

| Goal                              | Flags                                                                  |
|-----------------------------------|------------------------------------------------------------------------|
| Match Phase-3 output exactly      | `--zstd-level=3 --zstd-window-log=27 --candidates=1 --workers=1`       |
| Speed-prioritised CI              | `--zstd-level=12 --candidates=2`                                       |
| Maximum compression               | `--zstd-level=22 --zstd-window-log=31 --candidates=5`                  |
| Phase-3 importer compatibility    | `--zstd-window-log=27` (any other Phase-4 flags ok)                    |

Encoder memory is approximately `2 × 2^WindowLog` bytes per running
encode. With `--workers=8 --zstd-window-log=auto`, worst case across
> 1 GiB layers is ≈ 32 GiB resident — build-farm-class hosts are the
target. Pin `--zstd-window-log=27` (≈ 2 GiB at 8 workers) on
laptop-class environments. See
[`docs/performance.md`](docs/performance.md) for details.

## Signing and verification

`diff` and `bundle` produce a cosign-compatible `.sig` sidecar next
to the archive when `--sign-key` is supplied. `apply` and `unbundle`
verify signatures when `--verify` is supplied.

```sh
# Producer:
diffah diff \
  docker-archive:v1.tar docker-archive:v2.tar app.delta.tar \
  --sign-key ./key.pem --sign-key-password-stdin   # passphrase via stdin

# → writes app.delta.tar and app.delta.tar.sig

# Consumer:
diffah apply app.delta.tar \
  docker-archive:v1.tar oci-archive:./out.tar \
  --verify ./key.pub
```

Supported producer key formats:

- Plain ECDSA-P256 PEM (`-----BEGIN EC PRIVATE KEY-----`).
- cosign-boxed PEM (scrypt + `nacl/secretbox`) — same format `cosign
  generate-key-pair` writes.

Supported verifier key format: ECDSA-P256 PEM public key.

### Verification matrix

| Archive  | `--verify`            | Outcome              |
|----------|-----------------------|----------------------|
| signed   | supplied, key matches | exit 0               |
| signed   | supplied, key differs | exit 4 (content)     |
| signed   | absent                | exit 0 (back-compat) |
| unsigned | supplied              | exit 4 (content)     |
| unsigned | absent                | exit 0               |

### Rekor (optional)

`--rekor-url URL` is wired on both producers and verifiers. Producer-side
upload to a Rekor transparency log is **registered but not yet
implemented** in this build — the flag currently errors with a
"not yet implemented" hint. Verifier-side, `--verify-rekor-url URL`
checks the inclusion proof when a `.rekor.json` sidecar is present.

The signature payload is `sha256( jcs( sidecar.json ) )` per
[RFC 8785](https://www.rfc-editor.org/rfc/rfc8785) — see
[`docs/compat.md`](docs/compat.md) for the full sidecar-format
contract.

### Forward-compat reservations

`--keyless`, `cosign://` URIs on `--sign-key` / `--verify`, inline
embedded signatures (`--sign-inline`), and pre-existing certificate
attachments (`--sign-cert`) are recognised but rejected with
`reserved but not yet implemented`.

## Inspect, dry-run, doctor

```sh
diffah inspect ./bundle.tar          # text summary + per-image manifest digests
diffah inspect ./bundle.tar -o json  # machine-readable envelope (see below)
```

Sample text output:

```
archive: ./bundle.tar
version: v1
feature: bundle
tool: diffah
tool_version: v0.1.0
platform: linux/amd64
images: 2
blobs: 5 (full: 4, patch: 1)
avg patch ratio: 12.3%
total archive: 123456 bytes
intra-layer patches required: yes
zstd available: yes
patch savings: 87654 bytes (41.5% vs full)

--- image: svc-a ---
  target manifest digest: sha256:ef053f… (application/vnd.oci.image.manifest.v1+json)
  baseline manifest digest: sha256:937a56… (application/vnd.oci.image.manifest.v1+json)
  baseline source: svc-a-baseline
```

`--dry-run` (`-n`) plans without writing on every verb that produces
output:

```sh
diffah diff -n     docker-archive:v1.tar docker-archive:v2.tar /dev/null
diffah bundle -n   bundle.json           /dev/null
diffah apply -n    delta.tar docker-archive:v1.tar docker-archive:/dev/null
diffah unbundle -n delta.tar baselines.json outputs.json
```

`diffah doctor` runs environment preflight checks (today: zstd
availability) and exits non-zero if any check fails:

```sh
$ diffah doctor
zstd                                     ok (zstd 1.5.6)
```

## Output formats

`apply` / `unbundle` always write the reconstructed image to the
transport in the destination reference: `docker-archive:`,
`oci-archive:`, `dir:`, `oci:`, or `docker://`.

When the source-image manifest media type and the destination format
disagree (e.g. an OCI source written to `docker-archive:`), `diffah`
treats it as a manifest media-type conversion — every layer and
manifest digest changes. The default is to **refuse** the conversion;
pass `--allow-convert` to acknowledge the digest drift.

## Logging, progress, structured output

| Flag             | Values                       | Default | Notes                                                    |
|------------------|------------------------------|---------|----------------------------------------------------------|
| `-q`, `--quiet`  | bool                         | `false` | Suppress info logs and progress bars (level → `warn`).   |
| `-v`, `--verbose`| bool                         | `false` | Enable debug logs (level → `debug`).                     |
| `--log-level`    | `debug\|info\|warn\|error`   | `info`  | Override (env: `DIFFAH_LOG_LEVEL`).                      |
| `--log-format`   | `auto\|text\|json`           | `auto`  | `json` emits stdlib `slog` records (env: `DIFFAH_LOG_FORMAT`). |
| `--progress`     | `auto\|bars\|lines\|off`     | `auto`  | Bars on TTY, lines elsewhere.                            |
| `-o`, `--format` | `text\|json`                 | `text`  | Renders `inspect`, `doctor`, dry-run reports, and error bodies. |

`-o json` produces a stable envelope per
[`docs/compat.md`](docs/compat.md):

```json
{"schema_version": 1, "data": { ... }}
{"schema_version": 1, "error": {"category": "...", "message": "...", "next_action": "..."}}
```

## Exit codes

| Code | Category    | When                                                                           |
|:----:|-------------|--------------------------------------------------------------------------------|
| `0`  | success     | operation completed                                                            |
| `1`  | internal    | bug, panic, or unclassified error                                              |
| `2`  | user        | bad flag, missing positional, missing transport prefix, removed verb, registry 401 / 403 |
| `3`  | environment | missing `zstd`, network failure, filesystem permission, registry DNS / TLS    |
| `4`  | content     | sidecar schema mismatch, blob digest mismatch, unsupported schema version, signature failure |

Stable across minor versions — see
[`docs/compat.md`](docs/compat.md) for the full contract.

## Compatibility

- **Sidecar version** is `v1`. A reader that does not know the version
  exits with code `4` and a message asking the operator to upgrade.
- **Phase 3 archives** decode byte-identically through the Phase 4
  apply path.
- **Phase 4 archives** with `--zstd-window-log ≤ 27` decode through
  Phase 3 importers; larger windows fail-closed in older importers
  with `Frame requires too much memory for decoding`.
- **CLI deprecations** require one full minor-version cycle as
  warnings before removal.

Full sidecar evolution rules, structured-log key stability, and the
JSON envelope contract live in
[`docs/compat.md`](docs/compat.md).

## How baseline matching works

For each shipped layer `diffah` fingerprints the set of tar entries
inside every baseline layer and ranks candidates by **byte weight of
shared entries** on the decompressed bytes. The top `--candidates K`
candidates are each tried as a zstd `--patch-from` reference; the
smallest emitted patch wins. Ties on byte weight break by
size-closest, then by baseline digest order — the choice is fully
deterministic.

`diffah` falls back to picking the baseline closest in compressed
byte size in three cases:

1. The shipped layer is not a parseable tar (rare — typically only
   foreign OCI configs routed as layer blobs).
2. None of the baseline layers fingerprint successfully.
3. The shipped layer shares no tar entries with any baseline.

## Library usage

`diffah` is also a Go module. Import the high-level entry points:

```go
import (
    "context"

    "github.com/leosocy/diffah/pkg/exporter"
    "github.com/leosocy/diffah/pkg/importer"
)

func main() {
    _ = exporter.Export(context.Background(), exporter.Options{
        Pairs: []exporter.Pair{{
            Name:        "default",
            BaselineRef: "docker-archive:./v1.tar",
            TargetRef:   "docker-archive:./v2.tar",
        }},
        Platform:   "linux/amd64",
        OutputPath: "./delta.tar",
        Workers:    8,
        Candidates: 3,
        ZstdLevel:  22,
    })
    _ = importer.Import(context.Background(), importer.Options{
        DeltaPath: "./delta.tar",
        Baselines: map[string]string{"default": "docker-archive:./v1.tar"},
        Outputs:   map[string]string{"default": "oci-archive:./out.tar"},
        Strict:    true,
    })
}
```

API surface and stability live on
[pkg.go.dev](https://pkg.go.dev/github.com/leosocy/diffah). The
`pkg/...` packages are public; `internal/...` are not.

## Build from source

```sh
make build              # → ./bin/diffah
make test               # unit tests with -race -cover
make test-integration   # adds the integration build tag
make lint               # golangci-lint
make fixtures           # rebuild test image fixtures
make snapshot           # local goreleaser snapshot under ./dist/
```

The Go build tags `containers_image_openpgp exclude_graphdriver_btrfs
exclude_graphdriver_devicemapper` are required to avoid pulling in
storage-driver C dependencies. The `Makefile` targets set them for you.

## Contributing

Issues and pull requests are welcome. See
[`CONTRIBUTING.md`](CONTRIBUTING.md) for the development workflow and
[`SECURITY.md`](SECURITY.md) for vulnerability reporting. By
contributing you agree to the [`Code of Conduct`](CODE_OF_CONDUCT.md).

The full design history (per-phase specs and ADRs) lives under
[`docs/superpowers/`](docs/superpowers/).

## License

[Apache-2.0](LICENSE)
