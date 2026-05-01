# Recipe: Air-gapped customer delivery

## Goal

Deliver a new container image to an offline customer by sending only
the `diffah` delta archive over the air-gap. The customer applies the
delta against their existing baseline and reconstructs the new image
locally — no registry pull required.

## When to use

Your customer operates in a network-isolated environment (regulated
deployment, classified site, ship-at-sea, factory floor, sneakernet
behind an air-gap diode). They already hold the previous release of
your image; they cannot reach your registry. The only thing crossing
the air-gap is a small file you can hand-carry on USB or push through
a one-way data diode.

## Prerequisites

- **Producer side** — connected machine with `diffah` and access to
  both the previous and new image manifests (a registry, an OCI
  archive on disk, or a local docker-daemon image).
- **Customer side** — air-gapped machine with `diffah` installed and
  the previous release stored locally as either an OCI archive
  (`oci-archive:./baseline-v1.tar`) or a docker archive
  (`docker-archive:./baseline-v1.tar`).
- A method to physically move ~MB of data across the air-gap (USB
  drive, removable media, one-way diode, sneakernet).

## Setup

```sh
# Producer side
export BASELINE_REF="oci-archive:./baseline-v1.tar"   # already shipped to customer
export TARGET_REF="oci-archive:./target-v2.tar"       # the new release you're cutting
export DELTA_OUT="./customer-update.delta.tar"

# Customer side (after the file arrives)
export BASELINE_REF="oci-archive:./baseline-v1.tar"   # what they have on disk
export DELTA_IN="./customer-update.delta.tar"
export OUTPUT_REF="oci-archive:./target-v2.restored.tar"
```

## Steps

### 1. Producer — compute the delta

```sh
diffah diff "${BASELINE_REF}" "${TARGET_REF}" "${DELTA_OUT}"
```

Only one file (`${DELTA_OUT}`) is produced. Optionally pin the
reproducible-encoding flags so the customer can audit the artifact
before applying it:

```sh
diffah diff "${BASELINE_REF}" "${TARGET_REF}" "${DELTA_OUT}" \
    --intra-layer=lz4 \
    --workers=4
```

### 2. Cross the air-gap

```sh
# Illustrative — substitute your actual transport.
sha256sum "${DELTA_OUT}" > "${DELTA_OUT}.sha256"
cp "${DELTA_OUT}" "${DELTA_OUT}.sha256" /mnt/usb/
```

Hand the USB drive to the courier. On the customer side, verify the
checksum before consuming the delta.

### 3. Customer — reconstruct the new image

```sh
sha256sum -c "${DELTA_IN}.sha256"

diffah apply "${DELTA_IN}" "${BASELINE_REF}" "${OUTPUT_REF}"
```

`diffah apply` reads `${BASELINE_REF}` for any layers the delta
references, decodes the patches, and writes a self-contained OCI
archive at `${OUTPUT_REF}`. No network calls.

## Verify

Inspect the delta's recorded baseline before applying so you don't
silently apply a delta against the wrong source:

```sh
diffah inspect "${DELTA_IN}"
```

The first lines tell you the encoding mode, version, and per-layer
breakdown. Confirm the baseline digest matches what you actually
have. If the delta was signed (see the
[offline-verify recipe](offline-verify.md)), pass `--verify pub.pem`
to `diffah apply` and trust the exit code.

## Cleanup

```sh
# After the customer has loaded the restored image into their runtime,
# the delta archive can be discarded.
rm "${DELTA_IN}" "${DELTA_IN}.sha256"
```

The reconstructed image at `${OUTPUT_REF}` is the artifact that
matters — it is byte-equivalent to `${TARGET_REF}` on the producer side.

## Variations

### Customer holds the baseline in their own registry

If the customer's air-gapped environment has a private registry, both
sides of the operation can use a `docker://` reference instead of an
on-disk archive:

```sh
# Producer side
diffah diff \
  "docker://producer.local/app:v1" \
  "docker://producer.local/app:v2" \
  ./delta.tar

# Customer side (still air-gapped, but with their own registry)
diffah apply \
  ./delta.tar \
  "docker://customer.internal/app:v1" \
  "docker://customer.internal/app:v2"
```

### Multiple images per delivery

Use `diffah bundle` to ship a single archive containing deltas for
several images:

```sh
diffah bundle pairs.json ./customer-update.bundle.tar

# On the customer side:
diffah unbundle ./customer-update.bundle.tar baselines.json outputs.json
```

`baselines.json` and `outputs.json` map each pair name to the
customer's local references for that image. See the
[multi-image quick start](../../README.md#multi-image-bundle) for
the JSON shape.

## Troubleshooting

- **`baseline mismatch` on apply** — the customer's
  `${BASELINE_REF}` doesn't match what the delta was computed against.
  `diffah inspect` on the producer-side delta shows the expected
  baseline digest; cross-check that against the customer's archive
  with `skopeo inspect docker-archive:./baseline-v1.tar | jq .Digest`.
- **`apply` succeeds but the image won't run** — verify the
  reconstructed archive's manifest matches the producer side with
  `diffah inspect ${OUTPUT_REF}` (or `skopeo inspect`). If the
  digests differ, recompute the delta with the same `--intra-layer`
  mode the producer used originally.
