package xclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryAfterPreservesServerCooldown(t *testing.T) {
	for _, value := range []string{"86400", time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat)} {
		header := http.Header{"Retry-After": []string{value}}
		if got := parseRetryAfter(header); got < 24*time.Hour-time.Second || got > 24*time.Hour {
			t.Fatalf("Retry-After %q = %s, want full server cooldown", value, got)
		}
	}
}

func TestQuotaPacingSpreadsRemainingRequestsAcrossWindow(t *testing.T) {
	limiter := newRateLimiter()
	limiter.after("/media", http.Header{
		"X-Rate-Limit-Limit":     []string{"100"},
		"X-Rate-Limit-Remaining": []string{"90"},
		"X-Rate-Limit-Reset":     []string{fmt.Sprint(time.Now().Add(900 * time.Second).Unix())},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := limiter.before(WithYieldToScheduler(ctx), "/media")
	retryAt, ok := RateLimitRetryAt(err)
	if !ok || time.Until(retryAt) < 12*time.Second || time.Until(retryAt) > 13*time.Second {
		t.Fatalf("quota pacing error=%v retryAt=%s, want about 12.5s", err, retryAt)
	}
	if limiter.wouldBlock("/lookup") {
		t.Fatal("one endpoint's pacing blocked an unrelated endpoint")
	}
}

func TestRateLimitFallbackExpandsAndCoolsAccount(t *testing.T) {
	limiter := newRateLimiter()
	for attempt, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		limiter.markRateLimited("/media", http.Header{})
		reset, ok := limiter.blockedReset()
		if !ok || time.Until(reset) < want || time.Until(reset) > want+6*time.Second {
			t.Fatalf("attempt=%d reset=%s, want cooldown %s", attempt, reset, want)
		}
	}
	if !limiter.wouldBlock("/lookup") {
		t.Fatal("repeated rate limits did not cool the entire account")
	}
}

func TestPoolCooldownUsesRequestedEndpoint(t *testing.T) {
	limiter := newRateLimiter()
	now := time.Now()
	limiter.limits["/slow"] = rateLimitState{remaining: 0, limit: 100, reset: now.Add(time.Hour), ready: true}
	limiter.limits["/other"] = rateLimitState{remaining: 0, limit: 100, reset: now.Add(time.Minute), ready: true}
	pool := &Pool{clients: []*Client{{limiter: limiter}}}
	_, err := pool.Select(WithYieldToScheduler(context.Background()), "/slow")
	retryAt, ok := RateLimitRetryAt(err)
	if !ok || retryAt.Before(now.Add(time.Hour)) {
		t.Fatalf("error=%v retryAt=%s, want requested endpoint's cooldown", err, retryAt)
	}
}

func TestWAFChallengeCoolsAllAccountEndpoints(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html>cf-chl challenge</html>`))
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	ctx := WithYieldToScheduler(context.Background())
	_, _ = client.graphQL(ctx, "/media", nil)
	_, _ = client.graphQL(ctx, "/lookup", nil)
	if requests.Load() != 1 {
		t.Fatalf("requests=%d, want account cooldown after WAF", requests.Load())
	}
}

func TestPoolTransientRetriesHaveThreeAttemptTotalBudget(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	pool := &Pool{clients: []*Client{
		{http: server.Client(), baseURL: server.URL, retryBackoff: func(int) time.Duration { return 0 }},
		{http: server.Client(), baseURL: server.URL, retryBackoff: func(int) time.Duration { return 0 }},
		{http: server.Client(), baseURL: server.URL, retryBackoff: func(int) time.Duration { return 0 }},
	}}
	_, _ = pool.graphQL(context.Background(), "/test", nil)
	if requests.Load() != 3 {
		t.Fatalf("requests=%d, want 3 attempts shared across cookies", requests.Load())
	}
}

func TestNewClientsShareAccountPacingAcrossEndpoints(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":{},"screen_name":"owner"}`))
	}))
	defer server.Close()
	first, err := NewClient(Credentials{AuthToken: t.Name(), CSRFToken: "csrf-a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewClient(Credentials{AuthToken: t.Name(), CSRFToken: "csrf-b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []*Client{first, second} {
		client.http, client.baseURL = server.Client(), server.URL
	}
	if _, err := first.graphQL(context.Background(), "/first", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := second.GetSelfScreenName(ctx); err != context.DeadlineExceeded {
		t.Fatalf("second client error=%v, want cancellable account pacing", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d, cookie check bypassed account pacing", requests.Load())
	}
	first.limiter.markRateLimitedFor("/media", time.Hour)
	if !second.limiter.wouldBlock("/media") {
		t.Fatal("recreating a client discarded the account's cooldown")
	}
}

func TestNewClientsShareProcessRequestPacing(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()
	for i := range 2 {
		client, err := NewClient(Credentials{AuthToken: fmt.Sprintf("%s-%d", t.Name(), i), CSRFToken: "csrf"}, "")
		if err != nil {
			t.Fatal(err)
		}
		client.http, client.baseURL = server.Client(), server.URL
		ctx := context.Background()
		if i == 1 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
		}
		_, err = client.graphQL(ctx, "/test", nil)
		if i == 0 && err != nil {
			t.Fatal(err)
		}
		if i == 1 && err != context.DeadlineExceeded {
			t.Fatalf("second account error=%v, want process pacing", err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d, want process-wide pacing", requests.Load())
	}
}
