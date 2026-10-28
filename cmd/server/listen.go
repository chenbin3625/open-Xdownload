package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

type listenOptions struct {
	addr     string
	certFile string
	keyFile  string
}

func (o listenOptions) validate() error {
	if o.addr != "" {
		if _, err := net.ResolveTCPAddr("tcp", o.addr); err != nil {
			return fmt.Errorf("invalid listen address: %w", err)
		}
	}
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
		// 限制读完整个请求体的时间，避免慢速 body 长期占用连接。
		// 刻意不设 WriteTimeout：SSE（/api/events）与媒体文件下载都是长连接，
		// 全局写超时会把它们直接掐断；SSE 已在处理器内设置单次写截止时间。
		ReadTimeout: 60 * time.Second,
		IdleTimeout: 90 * time.Second,
		Protocols:   protocols,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"},
		},
	}
}

func withHTTP3AltSvc(next http.Handler, port string, available ...*atomic.Bool) http.Handler {
	value := altSvcHeader(port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(available) > 0 && !available[0].Load() {
			w.Header().Set("Alt-Svc", "clear")
		} else {
			w.Header().Set("Alt-Svc", value)
		}
		next.ServeHTTP(w, r)
	})
}

// listenTCP binds a TCP listener so the caller can log the actual address
// before starting the server. Returns the listener or a bind error.
func listenTCP(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// serveHTTP starts serving on an already-bound listener. For TLS it uses
// ServeTLS; for plain HTTP it uses Serve.
func serveHTTP(server *http.Server, ln net.Listener, opts listenOptions) error {
	if opts.tlsEnabled() {
		return server.ServeTLS(ln, opts.certFile, opts.keyFile)
	}
	return server.Serve(ln)
}
