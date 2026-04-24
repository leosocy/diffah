package signer_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

const (
	unencryptedKey = "testdata/test_ec_p256.key"
	pubKey         = "testdata/test_ec_p256.pub"
	encryptedKey   = "testdata/test_ec_p256_enc.key"
	passphrasePath = "testdata/test_ec_p256_enc.pass"
)

// TestSignVerify_UnencryptedKeyRoundTrip exercises the core contract:
// Sign produces a signature, Verify accepts it, and tampering with
// either the payload or the signature flips the verify result. The
// pubKey is left un-mutated — pubkey-tamper coverage is lower value
// here because a flipped pubkey byte still often decodes to a valid
// point on the curve; we rely on the cryptographic verify check to
// reject the mismatch.
func TestSignVerify_UnencryptedKeyRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	payload := sha256.Sum256([]byte(`{"k":"v"}`))

	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath: unencryptedKey,
		Payload: payload[:],
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig.Raw) == 0 {
		t.Fatal("empty signature")
	}
	if len(sig.CertPEM) != 0 {
		t.Fatal("cert should be empty in keyed mode")
	}
	if len(sig.RekorBundle) != 0 {
		t.Fatal("rekor bundle should be empty with no --rekor-url")
	}

	if err := signer.Verify(ctx, pubKey, payload[:], sig, ""); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// flip one byte of payload → verify fails with ErrSignatureInvalid
	tampered := append([]byte{}, payload[:]...)
	tampered[0] ^= 0xFF
	err = signer.Verify(ctx, pubKey, tampered, sig, "")
	if err == nil {
		t.Fatal("expected verify to fail on tampered payload")
	}
	if !errors.Is(err, signer.ErrSignatureInvalid) {
		t.Errorf("tampered payload: want ErrSignatureInvalid, got %v", err)
	}

	// flip one byte of signature → verify fails with ErrSignatureInvalid
	sig2 := *sig
	sig2.Raw = append([]byte{}, sig.Raw...)
	sig2.Raw[0] ^= 0xFF
	err = signer.Verify(ctx, pubKey, payload[:], &sig2, "")
	if err == nil {
		t.Fatal("expected verify to fail on tampered signature")
	}
	if !errors.Is(err, signer.ErrSignatureInvalid) {
		t.Errorf("tampered signature: want ErrSignatureInvalid, got %v", err)
	}
}

// TestSignVerify_EncryptedKeyRoundTrip asserts that a cosign-boxed
// encrypted key decrypts with the committed passphrase and produces a
// signature verifiable by the matching public key. This is the
// happy-path for the cosign-compat encrypted-key support.
func TestSignVerify_EncryptedKeyRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pass, err := os.ReadFile(passphrasePath)
	if err != nil {
		t.Fatalf("read passphrase fixture: %v", err)
	}
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath:         encryptedKey,
		PassphraseBytes: append([]byte{}, pass...),
		Payload:         payload[:],
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := signer.Verify(ctx, pubKey, payload[:], sig, ""); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// TestSign_EncryptedKey_WrongPassphrase asserts that a wrong passphrase
// surfaces the typed ErrKeyPassphraseIncorrect so the CLI can route it
// to CategoryUser (exit 2).
func TestSign_EncryptedKey_WrongPassphrase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	_, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath:         encryptedKey,
		PassphraseBytes: []byte("wrong-passphrase-not-the-real-one"),
		Payload:         payload[:],
	})
	if err == nil {
		t.Fatal("expected error on wrong passphrase")
	}
	if !errors.Is(err, signer.ErrKeyPassphraseIncorrect) {
		t.Errorf("want ErrKeyPassphraseIncorrect, got %v", err)
	}
}

// TestSign_EncryptedKey_MissingPassphrase asserts that an encrypted key
// with no passphrase surfaces the typed ErrKeyEncrypted so the CLI can
// hint at --sign-key-password-stdin.
func TestSign_EncryptedKey_MissingPassphrase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	_, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath: encryptedKey,
		Payload: payload[:],
	})
	if err == nil {
		t.Fatal("expected error on missing passphrase")
	}
	if !errors.Is(err, signer.ErrKeyEncrypted) {
		t.Errorf("want ErrKeyEncrypted, got %v", err)
	}
}

// TestSign_PassphraseZeroedAfterUse pins the SignRequest contract: the
// PassphraseBytes slice is zeroed in place after Sign returns. Callers
// rely on this to minimise the lifetime of secret material in memory.
func TestSign_PassphraseZeroedAfterUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pass, err := os.ReadFile(passphrasePath)
	if err != nil {
		t.Fatalf("read passphrase fixture: %v", err)
	}
	passSlice := append([]byte{}, pass...)
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	if _, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath:         encryptedKey,
		PassphraseBytes: passSlice,
		Payload:         payload[:],
	}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	zero := make([]byte, len(passSlice))
	for i, b := range passSlice {
		if b != 0 {
			t.Fatalf("passphrase not zeroed: index %d = 0x%02x, want 0 (expected %x)", i, b, zero)
		}
	}
}
