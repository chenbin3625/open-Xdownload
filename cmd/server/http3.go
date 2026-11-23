package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go/http3"
)

type http3Closer interface {
	Serve(conn net.PacketConn) error
	Shutdown(ctx context.Context) error
	Close() error
}

func newHTTP3Server(addr string, handler http.Handler, base *tls.Config) http3Closer {
	cfg := base.Clone()
	cfg.NextProtos = []string{http3.NextProtoH3}
	return &http3.Server{
		Addr:        addr,
		Handler:     handler,
		TLSConfig:   cfg,
		IdleTimeout: 90 * time.Second,
	}
}

func isHTTPServerClosed(err error) bool {
	return errors.Is(err, http.ErrServerClosed)
}
