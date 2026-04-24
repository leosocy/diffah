//go:build integration

// cosign_compat_test exercises the wire-format promise from spec §5.5:
// our WriteSidecars output is byte-for-byte what stock `cosign
// verify-blob` expects. The test is gated behind both an explicit
// DIFFAH_SIGN_COMPAT=1 env var AND a cosign binary probe, so it runs
// only in the release-signing CI job that installs sigstore tooling.
// Developer laptops skip this by default.

package signer_test

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leosocy/diffah/pkg/signer"
)

func TestCosignCompat_OurSigAcceptedByCosign(t *testing.T) {
	if os.Getenv("DIFFAH_SIGN_COMPAT") != "1" {
		t.Skip("DIFFAH_SIGN_COMPAT=1 required; bypassed by default")
	}
	if _, err := exec.LookPath("cosign"); err != nil {
		t.Skip("cosign binary not on PATH")
	}

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload")
	payload := []byte(`{"hello":"world"}`)
	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)

	ctx := context.Background()
	sig, err := signer.Sign(ctx, signer.SignRequest{
		KeyPath: unencryptedKey,
		Payload: digest[:],
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	archive := filepath.Join(dir, "blob")
	if err := os.WriteFile(archive, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := signer.WriteSidecars(archive, sig); err != nil {
		t.Fatalf("WriteSidecars: %v", err)
	}

	cmd := exec.Command("cosign", "verify-blob",
		"--key", pubKey,
		"--signature", archive+".sig",
		payloadPath)
	cmd.Env = append(cmd.Environ(), "COSIGN_EXPERIMENTAL=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cosign verify-blob failed: %v\n%s", err, out)
	}
}
