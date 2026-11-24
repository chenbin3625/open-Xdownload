package downloader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenRetriesTransientCDN5xxAndSkips4xx(t *testing.T) {
	origBackoff := openRetryBackoff
	openRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { openRetryBackoff = origBackoff })

	t.Run("retries 503 and succeeds with User-Agent", func(t *testing.T) {
		var attempts atomic.Int32
		var gotUA atomic.Value
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUA.Store(r.Header.Get("User-Agent"))
			if attempts.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("busy"))
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpegdata"))
		}))
		defer server.Close()

		resp, err := newTestDownloader().Open(context.Background(), server.URL+"/pic.jpg", Options{})
		if err != nil {
			t.Fatalf("expected transient 503 retry to succeed, got %v", err)
		}
		resp.Body.Close()
		if attempts.Load() != 3 {
			t.Fatalf("attempts = %d, want 3", attempts.Load())
		}
		if ua, _ := gotUA.Load().(string); ua != defaultUserAgent {
			t.Fatalf("User-Agent = %q, want %q", ua, defaultUserAgent)
		}
	})

	t.Run("does not retry permanent 404", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		_, err := newTestDownloader().Open(context.Background(), server.URL+"/missing.jpg", Options{})
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusNotFound {
			t.Fatalf("err = %v, want 404 HTTPStatusError", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("attempts = %d, want 1 (no retry on 404)", attempts.Load())
		}
	})
}

func TestRemoveStaleTempFilesCleansXdlTmpSubdir(t *testing.T) {
	root := t.TempDir()
	staleDir := filepath.Join(root, ".xdl-tmp", "asset-old")
	freshDir := filepath.Join(root, ".xdl-tmp", "asset-new")
	if err := os.MkdirAll(staleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(freshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(staleDir, "source.mp4")
	freshFile := filepath.Join(freshDir, "source.mp4")
	if err := os.WriteFile(staleFile, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freshFile, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldStamp := time.Now().Add(-3 * time.Hour)
	_ = os.Chtimes(staleFile, oldStamp, oldStamp)
	_ = os.Chtimes(staleDir, oldStamp, oldStamp)

	removed, err := RemoveStaleTempFiles(context.Background(), root, time.Hour)
	if err != nil {
		t.Fatalf("RemoveStaleTempFiles: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatal("stale .xdl-tmp file should have been removed")
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatal("empty stale .xdl-tmp subdir should have been removed")
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Fatalf("fresh .xdl-tmp file should be kept: %v", err)
	}
}
