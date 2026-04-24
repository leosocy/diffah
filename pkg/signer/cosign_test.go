package signer_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

// TestSidecars_WriteLoadRoundTrip writes a signature trio and reads it
// back. The .cert and .rekor.json files must NOT exist in keyed mode
// with no --rekor-url; only .sig is written.
func TestSidecars_WriteLoadRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "delta.tar")
	if err := os.WriteFile(archivePath, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	sig := &signer.Signature{Raw: []byte{0x30, 0x45, 0x01, 0x02, 0x03}}
	if err := signer.WriteSidecars(archivePath, sig); err != nil {
		t.Fatalf("WriteSidecars: %v", err)
	}

	if _, err := os.Stat(archivePath + ".sig"); err != nil {
		t.Errorf(".sig missing: %v", err)
	}
	if _, err := os.Stat(archivePath + ".cert"); !os.IsNotExist(err) {
		t.Errorf(".cert should not exist, err=%v", err)
	}
	if _, err := os.Stat(archivePath + ".rekor.json"); !os.IsNotExist(err) {
		t.Errorf(".rekor.json should not exist, err=%v", err)
	}

	loaded, err := signer.LoadSidecars(archivePath)
	if err != nil {
		t.Fatalf("LoadSidecars: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded nil but .sig was written")
	}
	if !bytes.Equal(loaded.Raw, sig.Raw) {
		t.Errorf("Raw round-trip mismatch: got %x, want %x", loaded.Raw, sig.Raw)
	}
	if len(loaded.CertPEM) != 0 {
		t.Errorf("CertPEM should be empty, got %d bytes", len(loaded.CertPEM))
	}
	if len(loaded.RekorBundle) != 0 {
		t.Errorf("RekorBundle should be empty, got %d bytes", len(loaded.RekorBundle))
	}
}

// TestSidecars_LoadAbsent asserts that an unsigned archive returns
// (nil, nil) — the callers in the importer rely on this to route the
// "archive has no signature" case through ErrArchiveUnsigned rather
// than a bare filesystem error.
func TestSidecars_LoadAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sig, err := signer.LoadSidecars(filepath.Join(dir, "delta.tar"))
	if err != nil {
		t.Fatalf("LoadSidecars on unsigned: %v", err)
	}
	if sig != nil {
		t.Errorf("want nil sig on absent .sig, got %+v", sig)
	}
}

// TestSidecars_WriteWithCertAndRekor pins the conditional-write
// behaviour: when CertPEM and RekorBundle are populated, both sidecars
// are written and LoadSidecars round-trips them unchanged.
func TestSidecars_WriteWithCertAndRekor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "delta.tar")
	if err := os.WriteFile(archivePath, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	sig := &signer.Signature{
		Raw:         []byte{0x30, 0x45},
		CertPEM:     []byte("-----BEGIN CERTIFICATE-----\nSTUB\n-----END CERTIFICATE-----\n"),
		RekorBundle: []byte(`{"stub":"rekor-bundle"}`),
	}
	if err := signer.WriteSidecars(archivePath, sig); err != nil {
		t.Fatalf("WriteSidecars: %v", err)
	}
	loaded, err := signer.LoadSidecars(archivePath)
	if err != nil {
		t.Fatalf("LoadSidecars: %v", err)
	}
	if !bytes.Equal(loaded.Raw, sig.Raw) {
		t.Errorf("Raw mismatch")
	}
	if !bytes.Equal(loaded.CertPEM, sig.CertPEM) {
		t.Errorf("CertPEM mismatch")
	}
	if !bytes.Equal(loaded.RekorBundle, sig.RekorBundle) {
		t.Errorf("RekorBundle mismatch")
	}
}

// TestSidecars_TamperedSigFails asserts that a one-byte tamper in the
// sidecar file surfaces through Verify as ErrSignatureInvalid. This is
// the "flip one byte of .sig" case from spec §7.1.
func TestSidecars_TamperedSigFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "delta.tar")
	if err := os.WriteFile(archivePath, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := sha256.Sum256([]byte(`{"k":"v"}`))
	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath: unencryptedKey,
		Payload: payload[:],
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := signer.WriteSidecars(archivePath, sig); err != nil {
		t.Fatalf("WriteSidecars: %v", err)
	}

	// Tamper: flip a byte in the DER signature before WriteSidecars'd
	// base64 decoding. We rewrite the .sig file with a mutated signature.
	mutated := append([]byte{}, sig.Raw...)
	mutated[len(mutated)/2] ^= 0xFF
	if err := signer.WriteSidecars(archivePath, &signer.Signature{Raw: mutated}); err != nil {
		t.Fatalf("WriteSidecars mutated: %v", err)
	}

	loaded, err := signer.LoadSidecars(archivePath)
	if err != nil {
		t.Fatalf("LoadSidecars: %v", err)
	}
	if err := signer.Verify(ctx, pubKey, payload[:], loaded, ""); err == nil {
		t.Fatal("expected verify to fail on tampered .sig")
	}
}
