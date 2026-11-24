package xtid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestStaleGraceFallbackOnRefreshErrorAndTimeout(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)

	var mu sync.Mutex
	now := time.Unix(golden.Timestamp, 0)
	opts := options{
		now:      func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		randByte: func() byte { return golden.RandomByte },
	}
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, opts)
	path := golden.Cases[0].Path

	// Initial fetch populates cache.
	if _, err := gen.Generate(context.Background(), "GET", path); err != nil {
		t.Fatalf("initial Generate: %v", err)
	}

	// Expire cacheTTL (1h), break homepage so refresh fails, verify stale key is served within staleGraceTTL.
	fs.deleteRoute("/")
	mu.Lock()
	now = now.Add(cacheTTL + time.Minute)
	mu.Unlock()

	got, err := gen.Generate(context.Background(), "GET", path)
	if err != nil {
		t.Fatalf("expected stale fallback on refresh failure, got err: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty transaction id from stale fallback")
	}

	// Also verify caller DeadlineExceeded during a slow refresh falls back to stale keys.
	gate := make(chan struct{})
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-gate
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer func() {
		close(gate)
		slowServer.Close()
	}()
	slowURL, _ := url.Parse(slowServer.URL)
	gen.client = &http.Client{
		Transport:     rewriteTransport{target: slowURL, base: http.DefaultTransport},
		CheckRedirect: gen.checkRedirect,
	}
	mu.Lock()
	now = now.Add(failureBackoff + time.Second)
	mu.Unlock()

	shortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	got, err = gen.Generate(shortCtx, "GET", path)
	if err != nil {
		t.Fatalf("expected stale fallback on caller DeadlineExceeded, got err: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty transaction id on timeout stale fallback")
	}
}
