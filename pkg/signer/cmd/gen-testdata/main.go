//go:build ignore

// gen-testdata writes the four fixture files consumed by the
// pkg/signer unit tests. See pkg/signer/testdata/README.md for the
// schema of the encrypted-key envelope. This program is tagged
// //go:build ignore so it never enters the package build; run it
// manually:
//
//	go run ./pkg/signer/cmd/gen-testdata/main.go
//
// The output paths are hard-coded relative to the repository root, so
// run from there (not from inside the cmd/gen-testdata directory).
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
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

const (
	outDir   = "pkg/signer/testdata"
	passText = "diffah-testdata-passphrase-do-not-use-anywhere"
	scryptN  = 32768
	scryptR  = 8
	scryptP  = 1
)

// envelope mirrors cosign 2.x's on-disk JSON format for encrypted keys.
// Field order matches cosign so diffs against golden cosign-emitted
// envelopes are small; the actual parser in pkg/signer is
// order-insensitive.
type envelope struct {
	KDF struct {
		Name   string    `json:"name"`
		Params kdfParams `json:"params"`
		Salt   string    `json:"salt"`
	} `json:"kdf"`
	Cipher struct {
		Name  string `json:"name"`
		Nonce string `json:"nonce"`
	} `json:"cipher"`
	Ciphertext string `json:"ciphertext"`
}

type kdfParams struct {
	N int `json:"N"`
	R int `json:"r"`
	P int `json:"p"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-testdata:", err)
		os.Exit(1)
	}
}

func run() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal pkcs8: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	if err := os.WriteFile(outDir+"/test_ec_p256.key", keyPEM, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal pub: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	if err := os.WriteFile(outDir+"/test_ec_p256.pub", pubPEM, 0o644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	env, err := buildEncryptedEnvelope(pkcs8, []byte(passText))
	if err != nil {
		return fmt.Errorf("encrypt key: %w", err)
	}
	if err := os.WriteFile(outDir+"/test_ec_p256_enc.key", env, 0o600); err != nil {
		return fmt.Errorf("write encrypted key: %w", err)
	}

	// No trailing newline — pkg/signer reads the .pass file verbatim.
	if err := os.WriteFile(outDir+"/test_ec_p256_enc.pass", []byte(passText), 0o600); err != nil {
		return fmt.Errorf("write passphrase: %w", err)
	}

	fmt.Println("wrote:")
	for _, p := range []string{"test_ec_p256.key", "test_ec_p256.pub", "test_ec_p256_enc.key", "test_ec_p256_enc.pass"} {
		fmt.Printf("  %s/%s\n", outDir, p)
	}
	return nil
}

// buildEncryptedEnvelope builds the cosign-boxed JSON envelope for
// pkcs8DER encrypted with passphrase. scrypt(N=32768, r=8, p=1)
// matches cosign 2.x defaults.
func buildEncryptedEnvelope(pkcs8DER, passphrase []byte) ([]byte, error) {
	var salt [32]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, fmt.Errorf("read salt: %w", err)
	}
	derived, err := scrypt.Key(passphrase, salt[:], scryptN, scryptR, scryptP, 32)
	if err != nil {
		return nil, fmt.Errorf("scrypt: %w", err)
	}
	var key32 [32]byte
	copy(key32[:], derived)

	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	sealed := secretbox.Seal(nil, pkcs8DER, &nonce, &key32)

	var env envelope
	env.KDF.Name = "scrypt"
	env.KDF.Params = kdfParams{N: scryptN, R: scryptR, P: scryptP}
	env.KDF.Salt = base64.StdEncoding.EncodeToString(salt[:])
	env.Cipher.Name = "nacl/secretbox"
	env.Cipher.Nonce = base64.StdEncoding.EncodeToString(nonce[:])
	env.Ciphertext = base64.StdEncoding.EncodeToString(sealed)

	return json.Marshal(&env)
}
