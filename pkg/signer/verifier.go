package signer

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// Verify checks an ECDSA-P256 signature over payload using the PEM
// public key at pubKeyPath. A nil return means the signature is valid;
// a non-nil return means either the key could not be loaded, the
// signature is structurally invalid, or the cryptographic check failed
// (ErrSignatureInvalid, CategoryContent).
//
// When rekorURL is non-empty and sig carries a RekorBundle, the bundle
// is verified against the log at rekorURL. The "rekorURL set but no
// bundle" warn-only case is handled at the CLI layer; the signer stays
// pure (no logging, no stderr writes).
func Verify(ctx context.Context, pubKeyPath string, payload []byte, sig *Signature, rekorURL string) error {
	pub, err := loadPublicKey(pubKeyPath)
	if err != nil {
		return err
	}
	if !ecdsa.VerifyASN1(pub, payload, sig.Raw) {
		return ErrSignatureInvalid
	}
	if rekorURL != "" && sig.RekorBundle != nil {
		if err := verifyRekorBundle(ctx, rekorURL, sig.RekorBundle, payload, pub); err != nil {
			return fmt.Errorf("rekor verify: %w", err)
		}
	}
	return nil
}

// loadPublicKey reads a PEM-encoded PKIX public key and returns the
// *ecdsa.PublicKey. Non-ECDSA keys fail fast — Phase 3 only supports
// ECDSA P-256.
func loadPublicKey(path string) (*ecdsa.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pub key %s: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in pub key file %s", path)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX pub key: %w", err)
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("pub key is not ECDSA")
	}
	return pub, nil
}

// verifyRekorBundle is a stub. Phase 3 emits .rekor.json only on
// explicit --rekor-url opt-in (not wired through the Phase 7 CLI yet);
// this stub returns nil so a reader passing --verify-rekor-url against
// an archive that happens to carry a .rekor.json does not spuriously
// fail. A follow-on PR lands the live transparency-log verification.
func verifyRekorBundle(ctx context.Context, rekorURL string, bundle, payload []byte, pub *ecdsa.PublicKey) error {
	_ = ctx
	_ = rekorURL
	_ = bundle
	_ = payload
	_ = pub
	return nil
}
