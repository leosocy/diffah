package signer

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

// Signature carries the bytes that land in the on-disk sidecar files.
// Raw is always populated; CertPEM and RekorBundle are conditional on
// keyless mode (out of scope in Phase 3) and --rekor-url respectively.
type Signature struct {
	Raw         []byte // DER ECDSA (r,s) — base64-encoded into OUT.sig
	CertPEM     []byte // PEM-encoded Fulcio cert; nil in keyed mode
	RekorBundle []byte // cosign 2.x Rekor bundle JSON; nil when no --rekor-url
}

// SignRequest carries everything Sign needs to produce a signature.
// Payload must already be the 32-byte sha256(jcs(sidecar.json)) digest;
// the signer does not re-hash. PassphraseBytes is zeroed in place after
// the key is decrypted (caller must not read it post-Sign).
type SignRequest struct {
	KeyPath         string
	PassphraseBytes []byte // zeroed after key decryption; caller must not reuse
	RekorURL        string // empty → no upload
	Payload         []byte // sha256(jcs(sidecar.json)); MUST be 32 bytes
}

// Sign produces an ECDSA-P256 signature over req.Payload using the key
// at req.KeyPath. Cosign-boxed (scrypt + nacl/secretbox) keys require a
// passphrase; plain PEM keys do not. When req.RekorURL is non-empty the
// resulting transparency-log bundle is attached to the returned
// Signature; otherwise RekorBundle is nil.
func Sign(ctx context.Context, req SignRequest) (*Signature, error) {
	priv, err := loadPrivateKey(req.KeyPath, req.PassphraseBytes)
	if err != nil {
		return nil, err
	}
	// Zero the passphrase in-place; the caller has agreed via the
	// SignRequest contract that it will not read the slice again.
	for i := range req.PassphraseBytes {
		req.PassphraseBytes[i] = 0
	}

	sig, err := ecdsa.SignASN1(rand.Reader, priv, req.Payload)
	if err != nil {
		return nil, fmt.Errorf("ecdsa sign: %w", err)
	}
	out := &Signature{Raw: sig}
	if req.RekorURL != "" {
		bundle, err := UploadEntry(ctx, req.RekorURL, sig, req.Payload, &priv.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("rekor upload: %w", err)
		}
		out.RekorBundle = bundle
	}
	return out, nil
}

// loadPrivateKey sniffs the key-file envelope: a leading '{' indicates a
// cosign-boxed JSON envelope, anything else is assumed to be PEM.
func loadPrivateKey(path string, passphrase []byte) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	if len(raw) > 0 && raw[0] == '{' {
		return decryptCosignBoxedKey(raw, passphrase)
	}
	// Plain PEM path. A passphrase supplied for an unencrypted key is
	// silently ignored — the cmd layer documents that --sign-key-password-stdin
	// only applies to cosign-boxed keys.
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in key file %s", path)
	}
	return parseECPrivateKeyBlock(block)
}

// parseECPrivateKeyBlock handles both SEC1 ("EC PRIVATE KEY") and PKCS8
// ("PRIVATE KEY") PEM blocks. Only ECDSA keys are accepted; RSA or
// Ed25519 keys fail fast at this boundary.
func parseECPrivateKeyBlock(block *pem.Block) (*ecdsa.PrivateKey, error) {
	switch block.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS8: %w", err)
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key is not ECDSA")
		}
		return ec, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
}

// cosignBoxedKey mirrors the JSON envelope that cosign 2.x emits for
// encrypted keys. All base64-encoded fields use standard (not URL-safe)
// encoding.
type cosignBoxedKey struct {
	KDF struct {
		Name   string `json:"name"`
		Params struct {
			N int `json:"N"`
			R int `json:"r"`
			P int `json:"p"`
		} `json:"params"`
		Salt string `json:"salt"`
	} `json:"kdf"`
	Cipher struct {
		Name  string `json:"name"`
		Nonce string `json:"nonce"`
	} `json:"cipher"`
	Ciphertext string `json:"ciphertext"`
}

// decryptCosignBoxedKey unseals a cosign-format encrypted private key:
// scrypt(passphrase, salt, N, r, p, 32) → 32-byte key, then
// secretbox.Open(nonce, ciphertext) → PKCS8 DER private-key bytes.
func decryptCosignBoxedKey(envelope, passphrase []byte) (*ecdsa.PrivateKey, error) {
	if len(passphrase) == 0 {
		return nil, ErrKeyEncrypted
	}
	var boxed cosignBoxedKey
	if err := json.Unmarshal(envelope, &boxed); err != nil {
		return nil, fmt.Errorf("parse cosign-boxed key: %w", err)
	}
	if boxed.KDF.Name != "scrypt" {
		return nil, ErrKeyUnsupportedKDF
	}
	salt, err := base64.StdEncoding.DecodeString(boxed.KDF.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	derived, err := scrypt.Key(passphrase, salt, boxed.KDF.Params.N, boxed.KDF.Params.R, boxed.KDF.Params.P, 32)
	if err != nil {
		return nil, fmt.Errorf("scrypt: %w", err)
	}
	var key32 [32]byte
	copy(key32[:], derived)

	nonceRaw, err := base64.StdEncoding.DecodeString(boxed.Cipher.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	if len(nonceRaw) != 24 {
		return nil, fmt.Errorf("nonce length %d, want 24", len(nonceRaw))
	}
	var nonce [24]byte
	copy(nonce[:], nonceRaw)

	ct, err := base64.StdEncoding.DecodeString(boxed.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	pt, ok := secretbox.Open(nil, ct, &nonce, &key32)
	if !ok {
		return nil, ErrKeyPassphraseIncorrect
	}
	// Re-use the PEM parser helper by synthesizing a PKCS8 block.
	return parseECPrivateKeyBlock(&pem.Block{Type: "PRIVATE KEY", Bytes: pt})
}
