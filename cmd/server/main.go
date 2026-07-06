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
	flag.StringVar(&addr, "addr", envOrDefault("OPEN_XDOWNLOAD_ADDR", "127.0.0.1:8787"), "HTTP listen address")
	flag.StringVar(&dataDir, "data-dir", envOrDefault("OPEN_XDOWNLOAD_DATA_DIR", "data"), "application data directory")
	flag.StringVar(&webDir, "web-dir", envOrDefault("OPEN_XDOWNLOAD_WEB_DIR", "apps/web/dist"), "built web app directory")
	flag.Parse()

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	dbPath := filepath.Join(dataDir, "open-xdownload.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		log.Fatalf("open storage: %v", err)
	}
	defer store.Close()

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
			log.Fatalf("serve: %v", err)
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
