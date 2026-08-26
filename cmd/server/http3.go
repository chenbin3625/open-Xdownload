package main

import (
	"crypto/tls"
	"errors"
	"net/http"

	"github.com/quic-go/quic-go/http3"
)

type http3Closer interface {
	ListenAndServeTLS(certFile, keyFile string) error
	Close() error
}

func newHTTP3Server(addr string, handler http.Handler, base *tls.Config) http3Closer {
	cfg := base.Clone()
	cfg.NextProtos = []string{http3.NextProtoH3}
	return &http3.Server{
		Addr:      addr,
		Handler:   handler,
		TLSConfig: cfg,
	}
}

func isHTTPServerClosed(err error) bool {
	return errors.Is(err, http.ErrServerClosed)
}
