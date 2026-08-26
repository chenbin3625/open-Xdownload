package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quic-go/quic-go/http3"
)

func TestListenOptionsValidate(t *testing.T) {
	t.Parallel()
	if err := (listenOptions{}).validate(); err != nil {
		t.Fatalf("empty tls files should be valid: %v", err)
	}
	if err := (listenOptions{certFile: "cert.pem", keyFile: "key.pem"}).validate(); err != nil {
		t.Fatalf("paired tls files should be valid: %v", err)
	}
	if err := (listenOptions{certFile: "cert.pem"}).validate(); err == nil {
		t.Fatal("cert without key should be invalid")
	}
	if err := (listenOptions{keyFile: "key.pem"}).validate(); err == nil {
		t.Fatal("key without cert should be invalid")
	}
}

func TestListenOptionsScheme(t *testing.T) {
	t.Parallel()
	if got := (listenOptions{}).scheme(); got != "http" {
		t.Fatalf("scheme = %q, want http", got)
	}
	if got := (listenOptions{certFile: "c", keyFile: "k"}).scheme(); got != "https" {
		t.Fatalf("scheme = %q, want https", got)
	}
}

func TestNewHTTPServerEnablesHTTP2(t *testing.T) {
	t.Parallel()
	server := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if server.TLSConfig == nil || server.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatal("expected TLS 1.2 minimum")
	}
	if len(server.TLSConfig.NextProtos) < 2 || server.TLSConfig.NextProtos[0] != "h2" {
		t.Fatalf("NextProtos = %v, want h2 first", server.TLSConfig.NextProtos)
	}
	if server.Protocols == nil || !server.Protocols.HTTP2() {
		t.Fatal("expected HTTP/2 protocol enabled")
	}
	if server.IdleTimeout == 0 {
		t.Fatal("expected keep-alive IdleTimeout")
	}
}

func TestWithHTTP3AltSvc(t *testing.T) {
	t.Parallel()
	handler := withHTTP3AltSvc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "8787")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := response.Header().Get("Alt-Svc"); got != `h3=":8787"; ma=86400` {
		t.Fatalf("Alt-Svc = %q", got)
	}
}

func TestNewHTTP3ServerUsesH3ALPN(t *testing.T) {
	t.Parallel()
	base := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
	server := newHTTP3Server("127.0.0.1:0", http.NotFoundHandler(), base)
	h3, ok := server.(*http3.Server)
	if !ok {
		t.Fatalf("type %T", server)
	}
	if h3.TLSConfig == nil || len(h3.TLSConfig.NextProtos) != 1 || h3.TLSConfig.NextProtos[0] != "h3" {
		t.Fatalf("HTTP/3 NextProtos = %v", h3.TLSConfig.NextProtos)
	}
	if len(base.NextProtos) != 2 {
		t.Fatal("HTTP/2 TLSConfig should stay unchanged")
	}
}
