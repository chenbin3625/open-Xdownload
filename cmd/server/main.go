package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
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
	flag.StringVar(&addr, "addr", envOrDefault("OPEN_XDOWNLOAD_ADDR", "0.0.0.0:8787"), "HTTP listen address")
	flag.StringVar(&dataDir, "data-dir", envOrDefault("OPEN_XDOWNLOAD_DATA_DIR", "data"), "application data directory")
	flag.StringVar(&webDir, "web-dir", envOrDefault("OPEN_XDOWNLOAD_WEB_DIR", "apps/web/dist"), "built web app directory")
	flag.Parse()

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
	handler := withWebApp(api.Routes(), webDir)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("open-Xdownload API listening on http://%s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// 触发优雅关停而不是 os.Exit，确保 manager.Stop()/store.Close() 等
			// 清理得以执行（manager.Start 已启动调度循环并恢复了 job）。
			log.Printf("serve: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
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
