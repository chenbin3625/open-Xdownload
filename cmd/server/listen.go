package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"
)

type listenOptions struct {
	addr     string
	certFile string
	keyFile  string
}

func (o listenOptions) validate() error {
	if (o.certFile == "") != (o.keyFile == "") {
		return fmt.Errorf("tls-cert and tls-key must both be set")
	}
	return nil
}

func (o listenOptions) tlsEnabled() bool {
	return o.certFile != "" && o.keyFile != ""
}

func (o listenOptions) scheme() string {
	if o.tlsEnabled() {
		return "https"
	}
	return "http"
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		Protocols:         protocols,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"},
		},
	}
}

func withHTTP3AltSvc(next http.Handler, port string) http.Handler {
	value := altSvcHeader(port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", value)
		next.ServeHTTP(w, r)
	})
}

func serveHTTP(server *http.Server, opts listenOptions) error {
	if opts.tlsEnabled() {
		return server.ListenAndServeTLS(opts.certFile, opts.keyFile)
	}
	return server.ListenAndServe()
}
