package parser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyndicationRequestsShareProcessPacing(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"__typename":"TweetTombstone"}`))
	}))
	defer server.Close()
	service := NewService()
	service.syndicationURL = server.URL
	_, _ = service.parseSyndicationTweetWithClient(context.Background(), "https://x.com/user/status/123", "user", "123", ParseOptions{}, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := service.parseSyndicationTweetWithClient(ctx, "https://x.com/user/status/124", "user", "124", ParseOptions{}, server.Client())
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 {
		t.Fatalf("err=%v requests=%d, want cancellable process pacing", err, requests.Load())
	}
}
