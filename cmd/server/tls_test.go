package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
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
