package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
	"time"
)

func TestEnsureDevTLSWritesLocalhostCert(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certFile, keyFile, err := ensureDevTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("missing cert pem")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Fatalf("localhost: %v", err)
	}
	if err := cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatalf("127.0.0.1: %v", err)
	}
	again, _, err := ensureDevTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != certFile {
		t.Fatalf("expected reuse of %s", certFile)
	}
}

func TestValidDevTLSRejectsMismatchedPrivateKey(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := ensureDevTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		t.Fatal(err)
	}
	if validDevTLS(certFile, keyFile) {
		t.Fatal("validDevTLS accepted a certificate with a mismatched private key")
	}
}

func TestValidDevTLSRejectsFutureCertificate(t *testing.T) {
	certFile, keyFile, err := ensureDevTLS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	cert.NotBefore = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, cert.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if validDevTLS(certFile, keyFile) {
		t.Fatal("accepted a certificate that is not yet valid")
	}
}

func TestListenPort(t *testing.T) {
	t.Parallel()
	if got := listenPort("0.0.0.0:8787"); got != "8787" {
		t.Fatalf("got %q", got)
	}
	if got := listenPort("127.0.0.1:443"); got != "443" {
		t.Fatalf("got %q", got)
	}
}

func TestAltSvcHeader(t *testing.T) {
	t.Parallel()
	if got := altSvcHeader("8787"); got != `h3=":8787"; ma=86400` {
		t.Fatalf("got %q", got)
	}
}

func TestEnvEnabled(t *testing.T) {
	t.Setenv("OPEN_XDOWNLOAD_TLS_AUTO", "1")
	if !envEnabled("OPEN_XDOWNLOAD_TLS_AUTO") {
		t.Fatal("expected enabled")
	}
	t.Setenv("OPEN_XDOWNLOAD_TLS_AUTO", "")
	if envEnabled("OPEN_XDOWNLOAD_TLS_AUTO") {
		t.Fatal("empty should be disabled")
	}
}
