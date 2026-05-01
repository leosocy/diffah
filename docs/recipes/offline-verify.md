# Recipe: Offline signature verification

## Goal

Sign a `diffah` delta on the producer side with a static EC P256 key,
verify it on the consumer side without contacting any external KMS or
transparency log. Suitable for air-gapped deployments where every
trust artifact must travel as a file.

## When to use

You ship deltas to a downstream consumer who must reject any delta
that wasn't signed by you, but who **cannot** call out to cosign's
public Rekor instance, an internal KMS, or any other online verifier.
The signature must be self-contained and the verification must work
with only files on disk.

## Prerequisites

- An EC P256 key pair (private PEM and public PEM). If you don't
  have one, the **Setup** section below shows the openssl
  invocation to generate it.
- `diffah` ≥ Phase 3 on both producer and consumer.
- The consumer must already trust the public key — typically by
  checking it into a private repo, baking it into a base image, or
  pinning it in their secret store. The recipe assumes the public
  key is available on disk before any delta arrives.

## Setup

If you don't already hold a key pair, generate one once:

```sh
openssl ecparam -genkey -name prime256v1 -noout -out priv.pem
openssl ec -in priv.pem -pubout -out pub.pem
```

Keep `priv.pem` in your CI secret manager. Distribute `pub.pem` to
every consumer that will verify your deltas. The recipe assumes
both files exist:

```sh
export SIGN_KEY_PEM="./priv.pem"   # producer side
export VERIFY_KEY_PEM="./pub.pem"  # consumer side
```

## Steps

### 1. Producer — sign at diff time

```sh
diffah diff --sign-key "${SIGN_KEY_PEM}" \
    "docker://producer.local/app:v1" \
    "docker://producer.local/app:v2" \
    ./delta.tar
```

`--sign-key` writes a `.sig` sidecar next to the delta archive — in
the example above, `./delta.tar.sig`. Both files travel together to
the consumer.

### 2. Cross to the consumer

Ship `delta.tar` **and** `delta.tar.sig` together. If you also pin a
checksum (`delta.tar.sha256`), ship that too. The signature alone
won't help if the archive itself is missing or corrupted.

### 3. Consumer — verify at apply time

```sh
diffah apply --verify "${VERIFY_KEY_PEM}" \
    ./delta.tar \
    "docker-archive:./baseline-v1.tar" \
    "oci-archive:./restored-v2.tar"
```

`diffah apply --verify` reads `delta.tar.sig`, validates it against
the public key, and **only proceeds with the apply if verification
passes**. A mismatched key, missing `.sig`, or tampered archive all
short-circuit before any baseline blob is read.

## Verify

Exit codes are stable contract:

| Scenario | Exit | Behavior |
|---|---|---|
| Signed delta, correct public key | `0` | Apply succeeds. |
| Signed delta, wrong public key | `4` | Apply aborts; stderr mentions `signature`. |
| Unsigned delta, `--verify` set | `4` | Apply aborts; stderr mentions `no signature` / `unsigned`. |
| Signed delta, no `--verify` | `0` | Apply succeeds; signature ignored. |
| Tampered archive or `.sig` | `4` | Apply aborts. |

A consumer who runs `apply --verify` and gets a non-zero exit MUST
treat the delta as untrusted. Do not retry without first
re-confirming the public key fingerprint with the producer.

## Cleanup

```sh
# After successfully reconstructing and adopting the new image,
# the delta + signature pair can be discarded.
rm ./delta.tar ./delta.tar.sig
```

## Variations

### Encrypted private key on the producer side

If your CI policy wraps the private key with a passphrase, the
diffah binary expects the decrypted PEM on disk. Decrypt at sign
time and shred the plaintext immediately:

```sh
openssl ec -in priv-enc.pem -out /tmp/priv-clear.pem -passin env:KEY_PASSPHRASE
diffah diff --sign-key /tmp/priv-clear.pem ... ./delta.tar
shred -u /tmp/priv-clear.pem
```

`pkg/signer/testdata/test_ec_p256_enc.key` is an example of the
encrypted-PEM shape the project's test suite handles.

### Bundles instead of single deltas

`diffah bundle --sign-key <PEM>` and `diffah unbundle --verify <PEM>`
work the same way for multi-image deliveries. The signature covers
the entire bundle, not individual images.

## Troubleshooting

- **`signature mismatch` even though the key is correct** — the
  archive bytes changed between sign and verify (network corruption,
  USB write error, partial copy). Recompute `sha256sum delta.tar`
  on both sides; if those differ, re-transfer.
- **`signature missing` after a clean transfer** — `delta.tar.sig`
  did not arrive alongside `delta.tar`. Verify the producer side
  ran `diff --sign-key` (not just `diff`), and that the courier
  carried both files.
- **`reserved scheme: cosign://`** (exit 2) — `--sign-key cosign://...`
  and `--verify cosign://...` are reserved for future KMS / cosign
  integration and currently fail fast. Use a PEM file path instead.
- **Ed25519 / RSA keys** — only EC P256 is wired up today.
  Other curves return a configuration error before any signing
  attempt. Generate a P256 key with `openssl ecparam -name prime256v1`.
