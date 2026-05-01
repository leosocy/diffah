#!/usr/bin/env bash
# Smoke for docs/recipes/airgap-delivery.md.
#
# Drives diffah through the airgap recipe entirely against on-disk
# OCI archive fixtures — no registry involved. Exercises the producer
# `diff`, the sneakernet step (cp), and the customer `apply`, then
# asserts the restored archive is non-empty.
#
# Required envvars (provided by cmd/recipes_smoke_integration_test.go):
#   DIFFAH_BIN          absolute path to the diffah binary under test
#   WORK_DIR            writable scratch directory (test-scoped)
#   BASELINE_OCI_TAR    absolute path to the v1 OCI archive fixture
#   TARGET_OCI_TAR      absolute path to the v2 OCI archive fixture

set -euo pipefail

: "${DIFFAH_BIN:?DIFFAH_BIN must be set}"
: "${WORK_DIR:?WORK_DIR must be set}"
: "${BASELINE_OCI_TAR:?BASELINE_OCI_TAR must be set}"
: "${TARGET_OCI_TAR:?TARGET_OCI_TAR must be set}"

cd "${WORK_DIR}"

# Producer side — compute the delta from on-disk archives.
"${DIFFAH_BIN}" diff \
    "oci-archive:${BASELINE_OCI_TAR}" \
    "oci-archive:${TARGET_OCI_TAR}" \
    ./delta.tar

test -s ./delta.tar

# Sneakernet — illustrative cp into a separate "customer" directory.
mkdir -p ./customer
cp ./delta.tar ./customer/delta.tar
sha256sum ./delta.tar > ./customer/delta.tar.sha256
( cd ./customer && sha256sum -c delta.tar.sha256 )

# Customer side — reconstruct the new image with no network.
"${DIFFAH_BIN}" apply \
    ./customer/delta.tar \
    "oci-archive:${BASELINE_OCI_TAR}" \
    oci-archive:./customer/restored.tar

test -s ./customer/restored.tar

# Sanity — inspect surfaces non-empty metadata for the customer's audit step.
"${DIFFAH_BIN}" inspect ./customer/delta.tar
