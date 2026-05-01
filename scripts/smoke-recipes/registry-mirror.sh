#!/usr/bin/env bash
# Smoke for docs/recipes/registry-mirror.md.
#
# Drives the registry-mirror recipe end-to-end against two in-process
# registries: the source side holds fixtures/v1 + fixtures/v2, the
# mirror side already holds fixtures/v1 (representing yesterday's
# state). The script computes a delta from the source side and applies
# it into the mirror under the v2 tag. The Go driver then validates
# the mirror has the new tag.
#
# Required envvars (provided by cmd/recipes_smoke_integration_test.go):
#   DIFFAH_BIN        absolute path to the diffah binary under test
#   WORK_DIR          writable scratch directory (test-scoped)
#   SOURCE_REGISTRY   host:port of the source-side registry
#   MIRROR_REGISTRY   host:port of the mirror-side registry

set -euo pipefail

: "${DIFFAH_BIN:?DIFFAH_BIN must be set}"
: "${WORK_DIR:?WORK_DIR must be set}"
: "${SOURCE_REGISTRY:?SOURCE_REGISTRY must be set}"
: "${MIRROR_REGISTRY:?MIRROR_REGISTRY must be set}"

cd "${WORK_DIR}"

# Step 1 — compute the delta against the source registry.
"${DIFFAH_BIN}" diff \
    "docker://${SOURCE_REGISTRY}/fixtures/v1" \
    "docker://${SOURCE_REGISTRY}/fixtures/v2" \
    ./delta.tar \
    --tls-verify=false \
    --no-creds

test -s ./delta.tar

# Step 2 — apply the delta into the mirror, using the mirror's
# existing v1 tag as the baseline.
"${DIFFAH_BIN}" apply \
    ./delta.tar \
    "docker://${MIRROR_REGISTRY}/fixtures/v1" \
    "docker://${MIRROR_REGISTRY}/fixtures/v2" \
    --tls-verify=false \
    --no-creds
