package signer

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
)

// WriteSidecars emits the cosign-compatible sidecar files next to
// archivePath:
//
//   - archivePath + ".sig"        — base64(DER(signature)) + "\n" (always)
//   - archivePath + ".cert"       — only if sig.CertPEM is non-empty (keyless mode)
//   - archivePath + ".rekor.json" — only if sig.RekorBundle is non-empty
//
// Mode 0644 matches cosign's defaults (spec §5.3); the .sig contents are
// byte-identical to what `cosign sign-blob > archive.sig` produces, which
// keeps `cosign verify-blob` compat.
func WriteSidecars(archivePath string, sig *Signature) error {
	files := []struct {
		ext     string
		content []byte
	}{
		{".sig", []byte(base64.StdEncoding.EncodeToString(sig.Raw) + "\n")},
		{".cert", sig.CertPEM},
		{".rekor.json", sig.RekorBundle},
	}
	for _, f := range files {
		if len(f.content) == 0 {
			continue
		}
		//nolint:gosec // G306: spec §5.3 requires 0o644 for cosign compat (public artifact).
		if err := os.WriteFile(archivePath+f.ext, f.content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// LoadSidecars reads the sidecar trio at archivePath. If no .sig is
// present, (nil, nil) is returned — the caller treats that as "archive
// is unsigned" rather than a filesystem error. The .cert and
// .rekor.json are optional; missing files are silently accepted.
func LoadSidecars(archivePath string) (*Signature, error) {
	rawB64, err := os.ReadFile(archivePath + ".sig")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // unsigned — not an error
	}
	if err != nil {
		return nil, err
	}
	// Tolerate any trailing run of newline/space (cosign emits a single
	// "\n"; some editors append more) so a hand-edited .sig still parses.
	trimmed := bytes.TrimRight(rawB64, " \n")
	raw, err := base64.StdEncoding.DecodeString(string(trimmed))
	if err != nil {
		return nil, err
	}
	sig := &Signature{Raw: raw}
	if certPEM, err := os.ReadFile(archivePath + ".cert"); err == nil {
		sig.CertPEM = certPEM
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if rekor, err := os.ReadFile(archivePath + ".rekor.json"); err == nil {
		sig.RekorBundle = rekor
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return sig, nil
}
