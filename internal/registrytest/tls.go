package registrytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// WithTLS enables HTTPS with a generated self-signed certificate.
// The server exposes the CA cert PEM via CACertPEM() and writes the
// CA cert into ClientCertDir() as "registry.crt" so --cert-dir-style
// consumers find the file.
func WithTLS() Option {
	return func(c *config) { c.tls = true }
}

// generateTLSMaterial is called from New() when cfg.tls is set.
// It generates a per-test self-signed ECDSA certificate, writes
// registry.crt into a t.TempDir(), and populates cfg.caPEM / cfg.certDir.
func generateTLSMaterial(t *testing.T, cfg *config) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"127.0.0.1", "localhost"},
		IsCA:         true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("x509 keypair: %v", err)
	}

	// Containers-image --cert-dir expects a directory containing per-host
	// subdirs with *.crt / *.cert / *.key files. We write a single
	// registry.crt so a flat --cert-dir works.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.crt"), certPEM, 0o644); err != nil {
		t.Fatalf("write registry.crt: %v", err)
	}
	cfg.caPEM = certPEM
	cfg.certDir = dir
	return cert
}
