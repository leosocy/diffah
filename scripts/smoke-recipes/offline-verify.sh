#!/usr/bin/env bash
# Smoke for docs/recipes/offline-verify.md.
#
# Drives the offline-verify recipe end-to-end against on-disk OCI
# archive fixtures and the project's static EC P256 test key pair
# (pkg/signer/testdata/test_ec_p256.key + test_ec_p256.pub). Asserts
# the happy-path apply succeeds, then re-runs apply against a tampered
# archive and asserts a non-zero exit.
#
# Required envvars (provided by cmd/recipes_smoke_integration_test.go):
#   DIFFAH_BIN          absolute path to the diffah binary under test
#   WORK_DIR            writable scratch directory (test-scoped)
#   BASELINE_OCI_TAR    absolute path to the v1 OCI archive fixture
#   TARGET_OCI_TAR      absolute path to the v2 OCI archive fixture
#   SIGN_KEY_PEM        absolute path to the producer's private PEM
#   VERIFY_KEY_PEM      absolute path to the consumer's public PEM

set -euo pipefail

: "${DIFFAH_BIN:?DIFFAH_BIN must be set}"
: "${WORK_DIR:?WORK_DIR must be set}"
: "${BASELINE_OCI_TAR:?BASELINE_OCI_TAR must be set}"
: "${TARGET_OCI_TAR:?TARGET_OCI_TAR must be set}"
: "${SIGN_KEY_PEM:?SIGN_KEY_PEM must be set}"
: "${VERIFY_KEY_PEM:?VERIFY_KEY_PEM must be set}"

cd "${WORK_DIR}"

# Producer — sign at diff time. Produces ./delta.tar + ./delta.tar.sig.
"${DIFFAH_BIN}" diff --sign-key "${SIGN_KEY_PEM}" \
    "oci-archive:${BASELINE_OCI_TAR}" \
    "oci-archive:${TARGET_OCI_TAR}" \
    ./delta.tar

test -s ./delta.tar
test -s ./delta.tar.sig

# Consumer happy-path — apply with the matching public key.
"${DIFFAH_BIN}" apply --verify "${VERIFY_KEY_PEM}" \
    ./delta.tar \
    "oci-archive:${BASELINE_OCI_TAR}" \
    oci-archive:./restored.tar

test -s ./restored.tar

# Negative — overwrite the .sig sidecar with a different (still
# base64-shaped) value so the signature decodes but no longer matches
# the archive contents. Apply --verify must exit non-zero. We disable
# `set -e` for the single failing command so the script can capture
# the exit and assert on it explicitly.
cp ./delta.tar ./tampered.tar
printf 'AAAA-not-the-real-signature-AAAA' > ./tampered.tar.sig

set +e
"${DIFFAH_BIN}" apply --verify "${VERIFY_KEY_PEM}" \
    ./tampered.tar \
    "oci-archive:${BASELINE_OCI_TAR}" \
    oci-archive:./tampered-restored.tar
tampered_exit=$?
set -e

if [ "${tampered_exit}" -eq 0 ]; then
    echo "offline-verify smoke: tampered apply unexpectedly exited 0" >&2
    exit 1
fi
