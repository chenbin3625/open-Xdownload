package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpapi"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	var addr string
	var dataDir string
	var webDir string
	var tlsCert string
	var tlsKey string
	var tlsAuto bool
	// 服务本身无内置鉴权：默认只绑定回环地址，避免裸跑时把 X Cookie、下载目录
	// 与任务接口暴露给局域网。Docker 镜像通过 ENV 显式设置 0.0.0.0 以便端口映射；
	// 需要局域网访问时由部署方显式指定，并建议前置带鉴权的反向代理。
	flag.StringVar(&addr, "addr", envOrDefault("OPEN_XDOWNLOAD_ADDR", "127.0.0.1:8787"), "HTTP listen address")
	flag.StringVar(&dataDir, "data-dir", envOrDefault("OPEN_XDOWNLOAD_DATA_DIR", "data"), "application data directory")
	flag.StringVar(&webDir, "web-dir", envOrDefault("OPEN_XDOWNLOAD_WEB_DIR", "apps/web/dist"), "built web app directory")
	flag.StringVar(&tlsCert, "tls-cert", envOrDefault("OPEN_XDOWNLOAD_TLS_CERT", ""), "TLS certificate file; enables HTTPS, HTTP/2 and HTTP/3")
	flag.StringVar(&tlsKey, "tls-key", envOrDefault("OPEN_XDOWNLOAD_TLS_KEY", ""), "TLS private key file")
	flag.BoolVar(&tlsAuto, "tls-auto", envEnabled("OPEN_XDOWNLOAD_TLS_AUTO"), "issue a localhost self-signed cert (HTTPS + HTTP/2 + HTTP/3)")
	flag.Parse()
	listen := listenOptions{addr: addr, certFile: tlsCert, keyFile: tlsKey}
	if err := listen.validate(); err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	// MkdirAll only sets the mode when creating the directory; tighten an
	// already-existing data dir so the secrets inside (DB with auth tokens and
	// storage passwords) are not traversable by other local users.
	if err := os.Chmod(dataDir, 0o700); err != nil {
		log.Printf("chmod data dir: %v", err)
	}

	dbPath := filepath.Join(dataDir, "open-xdownload.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		log.Fatalf("open storage: %v", err)
	}
	defer store.Close()
	// Restrict the SQLite file and its -wal/-shm sidecars to the owner; the DB
	// stores X auth_token/ct0 and SMB/WebDAV passwords in cleartext, which
	// modernc.org/sqlite otherwise creates world-readable (0644).
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(dbPath+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			log.Printf("chmod db%s: %v", suffix, err)
		}
	}

	if tlsAuto && !listen.tlsEnabled() {
		certFile, keyFile, err := ensureDevTLS(filepath.Join(dataDir, "tls"))
		if err != nil {
			log.Fatalf("tls-auto: %v", err)
		}
		listen.certFile = certFile
		listen.keyFile = keyFile
		log.Printf("tls-auto: using %s", certFile)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	eventBus := jobs.NewEventBus()
	resolver := parser.NewService()
	manager := jobs.NewManager(store, resolver, eventBus)
	recoveredCount := 0
	if recovered, err := store.RequeueInterruptedJobs(ctx); err != nil {
		log.Printf("recover interrupted jobs: %v", err)
	} else if len(recovered) > 0 {
		recoveredCount = len(recovered)
		log.Printf("requeued %d interrupted jobs", len(recovered))
	}
	manager.Start(ctx)
	if recoveredCount > 0 {
		manager.Notify()
	}

	api := httpapi.NewServer(store, resolver, manager, eventBus)
	handler := withWebApp(api.Routes(), webDir, api.InjectIndexHTML)
	if listen.tlsEnabled() {
		handler = withHTTP3AltSvc(handler, listenPort(addr))
	}
	server := newHTTPServer(addr, handler)
	var http3Server http3Closer
	if listen.tlsEnabled() {
		http3Server = newHTTP3Server(addr, handler, server.TLSConfig)
	}

	go func() {
		log.Printf("open-Xdownload API listening on %s://%s", listen.scheme(), addr)
		if listen.tlsEnabled() {
			log.Printf("HTTP/2 + HTTP/3 enabled (UDP %s)", addr)
		}
		if err := serveHTTP(server, listen); err != nil && !isHTTPServerClosed(err) {
			log.Printf("serve: %v", err)
			stop()
		}
	}()
	if http3Server != nil {
		go func() {
			if err := http3Server.ListenAndServeTLS(listen.certFile, listen.keyFile); err != nil && !isHTTPServerClosed(err) {
				log.Printf("http3: %v", err)
				stop()
			}
		}()
	}

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	if http3Server != nil {
		if err := http3Server.Close(); err != nil {
			log.Printf("http3 shutdown: %v", err)
		}
	}
	// 等待调度循环与活跃任务退出，再让 defer store.Close() 关闭数据库，
	// 避免 task goroutine 仍在写库时 DB 被关闭。
	manager.Stop()
}

func envOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
