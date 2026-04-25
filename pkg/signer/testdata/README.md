# signer testdata

Throwaway ECDSA P-256 keypairs used only by unit tests. These are not
production secrets; committing them to version control is intentional.

## Files

- `test_ec_p256.key`        — unencrypted PKCS8 PEM private key
- `test_ec_p256.pub`        — matching PKIX PEM public key
- `test_ec_p256_enc.key`    — cosign-boxed (scrypt + nacl/secretbox) private key
- `test_ec_p256_enc.pass`   — passphrase for the encrypted key (no trailing newline)

The encrypted key is a JSON envelope in cosign 2.x format:

```json
{
  "kdf":        {"name": "scrypt", "params": {"N": 32768, "r": 8, "p": 1}, "salt": "<base64 of 32-byte salt>"},
  "cipher":     {"name": "nacl/secretbox", "nonce": "<base64 of 24-byte nonce>"},
  "ciphertext": "<base64 of secretbox-sealed PKCS8 DER private key>"
}
```

All three base64 fields use standard (not URL-safe) encoding.

## Regenerate

```sh
go run ./pkg/signer/cmd/gen-testdata/main.go
```

The generator has a `//go:build ignore` tag so it never enters the
package build — it is a one-shot development tool. Regeneration churns
all four files and any test that depends on an exact signature byte
sequence will need to be updated accordingly (as of this writing none
do — tests assert round-trip behaviour, not fixed bytes).
