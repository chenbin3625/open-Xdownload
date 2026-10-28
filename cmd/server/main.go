package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
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
	if code := run(); code != 0 {
		os.Exit(code)
	}
}

func run() int {
	var addr string
	var dataDir string
	var webDir string
	var tlsCert string
	var tlsKey string
	var tlsAuto bool
	// Default to loopback so account setup can be completed before network exposure.
	// Docker sets 0.0.0.0 explicitly to support port mapping.
	flag.StringVar(&addr, "addr", envOrDefault("OPEN_XDOWNLOAD_ADDR", "127.0.0.1:8787"), "HTTP listen address")
	flag.StringVar(&dataDir, "data-dir", envOrDefault("OPEN_XDOWNLOAD_DATA_DIR", "data"), "application data directory")
	flag.StringVar(&webDir, "web-dir", envOrDefault("OPEN_XDOWNLOAD_WEB_DIR", "apps/web/dist"), "built web app directory")
	flag.StringVar(&tlsCert, "tls-cert", envOrDefault("OPEN_XDOWNLOAD_TLS_CERT", ""), "TLS certificate file; enables HTTPS, HTTP/2 and HTTP/3")
	flag.StringVar(&tlsKey, "tls-key", envOrDefault("OPEN_XDOWNLOAD_TLS_KEY", ""), "TLS private key file")
	flag.BoolVar(&tlsAuto, "tls-auto", envEnabled("OPEN_XDOWNLOAD_TLS_AUTO"), "issue a localhost self-signed cert (HTTPS + HTTP/2 + HTTP/3)")
	flag.Parse()
	listen := listenOptions{addr: addr, certFile: tlsCert, keyFile: tlsKey}
	if err := listen.validate(); err != nil {
		log.Print(err)
		return 1
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Printf("create data dir: %v", err)
		return 1
	}
	// MkdirAll only sets the mode when creating the directory; tighten an
	// already-existing data dir so the secrets inside (DB with auth tokens and
	// storage passwords) are not traversable by other local users.
	if err := os.Chmod(dataDir, 0o700); err != nil {
		log.Printf("chmod data dir: %v", err)
	}

	dbPath := filepath.Join(dataDir, "open-xdownload.db")
	lock, err := acquireDataLock(dbPath + ".lock")
	if err != nil {
		log.Print(err)
		return 1
	}
	defer lock.Close()
	store, err := storage.Open(dbPath)
	if err != nil {
		log.Printf("open storage: %v", err)
		return 1
	}
	defer store.Close()
	// Restrict the SQLite file and its -wal/-shm sidecars to the owner; the DB
	// stores X auth_token/ct0 in cleartext, which modernc.org/sqlite otherwise
	// creates world-readable (0644).
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(dbPath+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			log.Printf("chmod db%s: %v", suffix, err)
		}
	}

	if tlsAuto && !listen.tlsEnabled() {
		host, _, _ := net.SplitHostPort(addr)
		certFile, keyFile, err := ensureDevTLS(filepath.Join(dataDir, "tls"), host)
		if err != nil {
			log.Printf("tls-auto: %v", err)
			return 1
		}
		listen.certFile = certFile
		listen.keyFile = keyFile
		log.Printf("tls-auto: using %s", certFile)
	}
	if listen.tlsEnabled() {
		if _, err := tls.LoadX509KeyPair(listen.certFile, listen.keyFile); err != nil {
			log.Printf("load TLS: %v", err)
			return 1
		}
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

	ln, err := listenTCP(addr)
	if err != nil {
		log.Printf("bind %s: %v", addr, err)
		return 1
	}
	defer ln.Close()
	boundAddr := ln.Addr().String()
	api := httpapi.NewServer(store, resolver, manager, eventBus)
	handler := withWebApp(api.Routes(), webDir, api.InjectIndexHTML)
	h3Available := new(atomic.Bool)
	h3Available.Store(true)
	if listen.tlsEnabled() {
		handler = withHTTP3AltSvc(handler, listenPort(boundAddr), h3Available)
	}
	server := newHTTPServer(boundAddr, handler)
	// Shutdown 不会取消处理中请求的 ctx：关闭事件总线让 /api/events 长连接立即结束，
	// 否则只要浏览器开着页面，Shutdown 就要等满超时，manager.Stop() 来不及执行。
	server.RegisterOnShutdown(eventBus.CloseAll)
	var http3Server http3Closer
	if listen.tlsEnabled() {
		http3Server = newHTTP3Server(boundAddr, handler, server.TLSConfig)
	}

	manager.Start(ctx)
	if recoveredCount > 0 {
		manager.Notify()
	}
	log.Printf("open-Xdownload version=%s commit=%s date=%s", version, commit, date)
	log.Printf("open-Xdownload API listening on %s://%s", listen.scheme(), ln.Addr().(*net.TCPAddr).String())
	if listen.tlsEnabled() {
		log.Printf("HTTP/2 + HTTP/3 enabled (UDP %s)", boundAddr)
	}

	// errCh collects fatal errors from server goroutines; buffered so sends
	// never block even if main is not reading yet.
	errCh := make(chan error, 2)

	go func() {
		if err := serveHTTP(server, ln, listen); err != nil && !isHTTPServerClosed(err) {
			log.Printf("serve: %v", err)
			errCh <- err
			stop()
		}
	}()
	if http3Server != nil {
		go func() {
			if err := http3Server.ListenAndServeTLS(listen.certFile, listen.keyFile); err != nil && !isHTTPServerClosed(err) {
				h3Available.Store(false)
				// HTTP/3 is optional — log a warning but let TCP continue serving.
				log.Printf("http3: %v (TCP server continues)", err)
			}
		}()
	}

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
	drained := manager.Stop()
	// 服务端致命错误优先：即使排空也失败，仍然以非零退出，便于 docker/systemd 发现异常。
	select {
	case err := <-errCh:
		if err != nil {
			return 1
		}
	default:
	}
	if !drained {
		// 主动停机（SIGTERM/SIGINT）时个别任务可能来不及收尾：只告警、以 0 退出，
		// 不能把"按了停止"编码成失败，否则 systemd Restart=on-failure /
		// docker --restart=on-failure 会把正常停机当成崩溃反复重启。
		// 这里立刻退出以保留仍可能写库的 worker，避免与 store.Close() 抢写。
		log.Printf("warning: 停机超时，仍有任务未退净；跳过数据库关闭直接退出（WAL 可在下次启动恢复）")
		os.Exit(0)
	}
	return 0
}

func envOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
