# Recipe: CI-driven delta release

## Goal

In a CI pipeline, compute a `diffah` delta between the previous and
current release tags of a container image, and publish that delta
alongside the image release so downstream consumers can apply it
against their existing copy of the previous tag.

## When to use

You ship container-image releases on a regular cadence (weekly, on
every merge to `main`, etc.) and your customers pull updates over
constrained pipes. Shipping a delta-only artifact lets them update by
applying ~MB of change instead of pulling the full image again.

## Prerequisites

- A registry holding both the previous tag (e.g. `:v1.2.0`) and the
  new tag (e.g. `:v1.3.0`). GitHub Container Registry, Harbor, ECR,
  or anything else `skopeo` understands all work.
- `diffah` installed on the CI runner (build from source or pull a
  release binary).
- `gh` CLI configured if you want to upload the delta to a GitHub
  release. Substitute your release-artifact tooling otherwise.

## Setup

```sh
# Three values pin every variant of this recipe:
export SOURCE_REGISTRY="ghcr.io/your-org"   # registry host + namespace
export OLD_TAG="v1.2.0"                     # already-released tag
export NEW_TAG="v1.3.0"                     # the tag you're cutting
```

## Steps

### 1. Compute the delta

```sh
diffah diff \
  "docker://${SOURCE_REGISTRY}/app:${OLD_TAG}" \
  "docker://${SOURCE_REGISTRY}/app:${NEW_TAG}" \
  "./app-${NEW_TAG}.delta.tar"
```

`diffah` pulls only the manifest and the layers it actually needs to
encode the diff. On rebuilds where most layers haven't changed, the
delta is a small fraction of the full image.

### 2. Surface savings in the release notes

```sh
diffah inspect "./app-${NEW_TAG}.delta.tar"
```

Pipe `--output json` and `jq` if you want to embed the totals into a
release-notes template programmatically.

### 3. Publish the delta as a release artifact

```sh
gh release upload "${NEW_TAG}" "./app-${NEW_TAG}.delta.tar" --clobber
```

This step is illustrative — substitute `aws s3 cp`, an internal
artifact server, or whatever your team uses. The smoke test for this
recipe skips the upload (CI shouldn't push to GitHub on every run).

## Verify

Re-apply the delta against the previous tag locally and confirm the
reconstructed image matches the new tag's manifest digest:

```sh
diffah apply \
  "./app-${NEW_TAG}.delta.tar" \
  "docker://${SOURCE_REGISTRY}/app:${OLD_TAG}" \
  "oci-archive:./restored.tar"
```

`diffah apply` exits 0 on success; the resulting `restored.tar` is a
self-contained OCI archive equivalent to `${NEW_TAG}`.

## Variations

### Bundle multiple images per release

If your release ships several images together (an app plus its
sidecars, for example), use `diffah bundle` instead of running `diff`
per pair:

```sh
cat > pairs.json <<EOF
{
  "pairs": [
    {"name": "app",     "baseline": "docker://${SOURCE_REGISTRY}/app:${OLD_TAG}",     "target": "docker://${SOURCE_REGISTRY}/app:${NEW_TAG}"},
    {"name": "sidecar", "baseline": "docker://${SOURCE_REGISTRY}/sidecar:${OLD_TAG}", "target": "docker://${SOURCE_REGISTRY}/sidecar:${NEW_TAG}"}
  ]
}
EOF

diffah bundle pairs.json "./release-${NEW_TAG}.bundle.tar"
```

The consumer side runs `diffah unbundle` against the baselines they
already have.

### GitHub Actions workflow snippet

```yaml
# .github/workflows/delta-release.yml
name: delta-release
on:
  release:
    types: [published]

jobs:
  delta:
    runs-on: ubuntu-latest
    steps:
      - name: Install diffah
        run: |
          curl -L "https://github.com/leosocy/diffah/releases/latest/download/diffah_linux_amd64.tar.gz" \
            | tar -xz -C /usr/local/bin diffah

      - name: Compute delta
        env:
          SOURCE_REGISTRY: ghcr.io/${{ github.repository_owner }}
          OLD_TAG: ${{ github.event.release.target_commitish }}
          NEW_TAG: ${{ github.event.release.tag_name }}
        run: |
          diffah diff \
            "docker://${SOURCE_REGISTRY}/app:${OLD_TAG}" \
            "docker://${SOURCE_REGISTRY}/app:${NEW_TAG}" \
            "./app-${NEW_TAG}.delta.tar"

      - name: Upload to release
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          gh release upload "${{ github.event.release.tag_name }}" \
            "./app-${{ github.event.release.tag_name }}.delta.tar" \
            --clobber
```

## Troubleshooting

- **`error: manifest unknown` on `diff`** — the registry rejected one
  of the references. Run `diffah doctor --probe docker://${SOURCE_REGISTRY}/app:${OLD_TAG}`
  to surface the specific failure.
- **Delta is suspiciously large** — run `diffah inspect --output json`
  and look at the per-layer breakdown. A layer that landed as `[F]ull`
  instead of `[P]atch` usually means the v1 / v2 builds reshuffled
  layer order or rebuilt a base layer. The fix is on the build side
  (use a stable base image / lockfile), not on `diffah`'s side.
