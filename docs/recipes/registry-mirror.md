# Recipe: Nightly registry-to-registry mirror

## Goal

Mirror a new image tag from a source registry to a downstream mirror
registry by sending only the `diffah` delta across the WAN. The mirror
ends up with the full new image (manifest + all layers), but the
delta is the only thing that traverses the slow network link.

## When to use

You operate a mirror or air-gap relay between two registries — for
example, a public ECR repo on the source side and a customer's
private Harbor on the mirror side, with a constrained leased line
in between. Pulling the new tag end-to-end every night is wasteful
when most layers haven't changed.

This recipe assumes both registries are already mutually reachable
from the host running the cron job. If the mirror side is truly
offline, see [air-gapped delivery](airgap-delivery.md) instead.

## Prerequisites

- Read access to the source registry (anonymous or via `--authfile`).
- Write access to the mirror registry.
- `diffah` installed on the cron host.
- The mirror already holds the previous tag (`v1`); the delta is
  computed against it. If the mirror is empty, the very first
  sync has to push the full image — the delta strategy only kicks
  in from the second tag onward.

## Setup

```sh
export SOURCE_REGISTRY="ghcr.io/source-org"          # source registry + namespace
export MIRROR_REGISTRY="harbor.customer.local/dest"  # mirror registry + namespace
export OLD_TAG="v1.2.0"
export NEW_TAG="v1.3.0"
```

If either registry needs auth, point `diffah` at an authfile or use
the inline credential flags (see
[`diffah doctor --probe`](../../README.md#inspect-dry-run-doctor) to
sanity-check connectivity before scheduling the cron):

```sh
export REGISTRY_AUTH_FILE="$HOME/.config/containers/auth.json"
```

## Steps

### 1. Compute the delta against the source registry

```sh
diffah diff \
    "docker://${SOURCE_REGISTRY}/app:${OLD_TAG}" \
    "docker://${SOURCE_REGISTRY}/app:${NEW_TAG}" \
    ./delta.tar
```

`diff` pulls only the bytes it needs from the source registry — both
manifests and the new layers' content, but not unchanged baseline
layers. The result is the smallest WAN-friendly artifact.

### 2. Apply the delta into the mirror registry

```sh
diffah apply \
    ./delta.tar \
    "docker://${MIRROR_REGISTRY}/app:${OLD_TAG}" \
    "docker://${MIRROR_REGISTRY}/app:${NEW_TAG}"
```

`apply` reads any baseline layers it needs **directly from the
mirror's previous tag** — they're already there from yesterday's run.
The new tag is pushed back to the mirror under `${NEW_TAG}`. After
this step, the mirror is byte-equivalent to the source for that tag.

### 3. Hand-off

A successful exit-zero from step 2 means the mirror is updated.
Trigger downstream rollouts (Argo CD sync, Watchtower, OS-level
update notifier — whatever your fleet uses) the same way you would
after a normal mirror.

## Verify

Cross-check the mirror's new manifest against the source's:

```sh
crane manifest "${SOURCE_REGISTRY}/app:${NEW_TAG}" | sha256sum
crane manifest "${MIRROR_REGISTRY}/app:${NEW_TAG}" | sha256sum
```

The two digests should match. If they don't, `diffah inspect ./delta.tar`
reveals which layers were patched vs. reused; layer reordering or a
rebuilt base image on the source side is the most common reason a
mirror diverges.

## Cleanup

```sh
# Once the mirror push succeeds, the local delta archive can be
# deleted — tomorrow's cron will compute a fresh one against
# whatever tag is "previous" then.
rm ./delta.tar
```

## Variations

### Multi-image bundle

If the source registry ships several related images per release
(application + sidecar + worker), use `diffah bundle` once instead
of running `diff` per pair:

```sh
cat > pairs.json <<EOF
{
  "pairs": [
    {"name": "app",     "baseline": "docker://${SOURCE_REGISTRY}/app:${OLD_TAG}",     "target": "docker://${SOURCE_REGISTRY}/app:${NEW_TAG}"},
    {"name": "worker",  "baseline": "docker://${SOURCE_REGISTRY}/worker:${OLD_TAG}",  "target": "docker://${SOURCE_REGISTRY}/worker:${NEW_TAG}"}
  ]
}
EOF

diffah bundle pairs.json ./bundle.tar

cat > baselines.json <<EOF
{"baselines": {
  "app":    "docker://${MIRROR_REGISTRY}/app:${OLD_TAG}",
  "worker": "docker://${MIRROR_REGISTRY}/worker:${OLD_TAG}"
}}
EOF

cat > outputs.json <<EOF
{"outputs": {
  "app":    "docker://${MIRROR_REGISTRY}/app:${NEW_TAG}",
  "worker": "docker://${MIRROR_REGISTRY}/worker:${NEW_TAG}"
}}
EOF

diffah unbundle ./bundle.tar baselines.json outputs.json
```

### Cron snippet

```cron
# Run every day at 02:00 local time. Captures stderr to a rotating
# log so a regression doesn't silently bit-rot in /var/log.
0 2 * * * /usr/local/bin/diffah-mirror-nightly.sh \
  >>/var/log/diffah-mirror.out 2>>/var/log/diffah-mirror.err
```

The wrapper script just sources the env-var block from the **Setup**
section above, runs steps 1 and 2, and exits non-zero on any failure
so cron / your monitoring system can alert.

### Signed deltas

Combine this recipe with the [offline-verify recipe](offline-verify.md)
when the mirror operator and the source operator are different
parties. Sign on the source side with `--sign-key`; verify on the
mirror cron host with `--verify` before pushing.

## Troubleshooting

- **`apply` succeeds but the mirror returns the old image** — the
  registry-side cache may still be serving the previous manifest.
  Force a `crane manifest` against the digest, not the tag, to
  bypass any HTTP cache between you and the registry.
- **`apply` fails with `baseline not found`** — yesterday's run
  didn't push to the mirror under `${OLD_TAG}` (or it was garbage-
  collected). Bootstrap the mirror by doing a one-time full pull /
  push (`crane copy`, `skopeo copy`) for the previous tag, then
  resume the delta-based cron.
- **TLS errors on a self-signed mirror** — pass `--tls-verify=false`
  for testing, or mount the mirror's CA bundle on the cron host
  (`--cert-dir`). The recipe's smoke test uses `--tls-verify=false`
  because it runs against an in-process registry.
