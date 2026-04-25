// Package signer produces and verifies cosign-compatible signatures
// over a diffah delta archive's diffah.json sidecar.
//
// The signature payload is sha256(jcs(sidecar.json)) per
// RFC 8785 — see [PayloadDigestFromSidecar] and [JCSCanonical] for
// the canonicalization helpers. Producers call [Sign] with a
// [SignRequest]; verifiers call [Verify] with a [VerifyRequest]. The
// on-disk wire format is the cosign 2.x sidecar set:
//
//   - <archive>.sig         — base64-encoded DER ECDSA signature
//     (always written when signing).
//   - <archive>.cert        — PEM x509 certificate (reserved for
//     future keyless / Fulcio mode; not written by keyed signing).
//   - <archive>.rekor.json  — Rekor inclusion-proof bundle (written
//     only when --rekor-url is supplied; producer-side upload to
//     Rekor is currently registered but not yet implemented).
//
// Supported producer key formats:
//
//   - Plain ECDSA-P256 PEM ("-----BEGIN EC PRIVATE KEY-----").
//   - cosign-boxed PEM (scrypt + nacl/secretbox), the same envelope
//     `cosign generate-key-pair` writes.
//
// Supported verifier key format: PEM-encoded ECDSA-P256 PKIX public key.
//
// Typed errors: [ErrKeyEncrypted], [ErrKeyPassphraseIncorrect],
// [ErrKeyUnsupportedKDF], [ErrSignatureInvalid], [ErrArchiveUnsigned].
//
// Forward-compat reservations: keyless signing (Fulcio / OIDC /
// ephemeral certs), KMS URIs (cosign://), inline-embedded signatures,
// and pre-existing certificate attachment are recognized but rejected
// with "reserved but not yet implemented".
package signer
