#!/usr/bin/env bash
# Smoke for docs/recipes/ci-delta-release.md.
#
# Drives diffah through the recipe's commands against fixture tags
# fixtures/v1 and fixtures/v2 in $SOURCE_REGISTRY, asserting the delta
# archive is non-empty and re-applying it produces a non-empty restored
# OCI archive. Skips the gh-release-upload step (illustrative-only in
# the recipe).
#
# Required envvars (provided by cmd/recipes_smoke_integration_test.go):
#   DIFFAH_BIN       absolute path to the diffah binary under test
#   WORK_DIR         writable scratch directory (test-scoped)
#   SOURCE_REGISTRY  host:port of the in-process registry (no scheme)

set -euo pipefail

: "${DIFFAH_BIN:?DIFFAH_BIN must be set}"
: "${WORK_DIR:?WORK_DIR must be set}"
: "${SOURCE_REGISTRY:?SOURCE_REGISTRY must be set}"

cd "${WORK_DIR}"

# Step 1 — produce the delta archive.
"${DIFFAH_BIN}" diff \
    "docker://${SOURCE_REGISTRY}/fixtures/v1" \
    "docker://${SOURCE_REGISTRY}/fixtures/v2" \
    ./delta.tar \
    --tls-verify=false \
    --no-creds

test -s ./delta.tar

# Step 2 — surface savings (recipe shows this in release notes).
"${DIFFAH_BIN}" inspect ./delta.tar

# Step 3 — `gh release upload` is illustrative-only in the recipe and
# is intentionally skipped in the smoke (CI shouldn't push to GitHub).

# Verify — re-apply against v1 and assert the restored archive exists.
"${DIFFAH_BIN}" apply \
    ./delta.tar \
    "docker://${SOURCE_REGISTRY}/fixtures/v1" \
    oci-archive:./restored.tar \
    --tls-verify=false \
    --no-creds

test -s ./restored.tar
