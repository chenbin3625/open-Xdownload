package xclient

import (
	"context"
	"errors"
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

// 配额内的主动匀速只等几十毫秒时，Pool.Select 应原地吸收，而不是把它当成
// “账号池全部限流”让任务重新入队。
func TestPoolSelectAbsorbsShortQuotaPacing(t *testing.T) {
	limiter := newRateLimiter()
	now := time.Now()
	limiter.limits["/media"] = rateLimitState{
		remaining:   90,
		limit:       100,
		reset:       now.Add(15 * time.Minute),
		ready:       true,
		nextRequest: now.Add(25 * time.Millisecond),
	}
	pool := &Pool{clients: []*Client{{limiter: limiter}}}
	ctx, cancel := context.WithTimeout(WithYieldToScheduler(context.Background()), time.Second)
	defer cancel()
	client, err := pool.Select(ctx, "/media")
	if err != nil || client == nil {
		t.Fatalf("Select = %v, %v; want a client after pacing wait", client, err)
	}
}

// 匀速等待较长时交还调度器，但必须标记为 Pacing：它不是全池限流，也不应累计
// 限流推迟。
func TestPoolSelectLongQuotaPacingIsNotPoolExhaustion(t *testing.T) {
	limiter := newRateLimiter()
	now := time.Now()
	limiter.limits["/media"] = rateLimitState{
		remaining:   90,
		limit:       100,
		reset:       now.Add(15 * time.Minute),
		ready:       true,
		nextRequest: now.Add(30 * time.Second),
	}
	pool := &Pool{clients: []*Client{{limiter: limiter}}}
	_, err := pool.Select(WithYieldToScheduler(context.Background()), "/media")
	if err == nil {
		t.Fatal("expected pacing yield error")
	}
	if IsAllClientsRateLimited(err) {
		t.Fatalf("pacing error classified as pool exhaustion: %v", err)
	}
	if !IsPacingRateLimit(err) {
		t.Fatalf("error = %v, want Pacing marker", err)
	}
	var limited *RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("error = %v, want RateLimitedError", err)
	}
	if wait := time.Until(limited.RetryAt); wait < 29*time.Second || wait > 31*time.Second {
		t.Fatalf("retryAt in %s, want about 30s", wait)
	}
}

func TestPoolGraphQLPreservesPacingAfterAnotherAccountRateLimit(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("paced account must wait before sending a request")
	}))
	defer backup.Close()
	second := newHeaderTestClient(backup)
	second.limiter.limits[userByScreenNamePath] = rateLimitState{
		remaining: 90, limit: 100, ready: true,
		reset: time.Now().Add(15 * time.Minute), nextRequest: time.Now().Add(30 * time.Second),
	}
	pool := &Pool{clients: []*Client{newHeaderTestClient(primary), second}}
	_, err := pool.GraphQL(WithYieldToScheduler(context.Background()), userByScreenNamePath, nil)
	if !IsPacingRateLimit(err) {
		t.Fatalf("error = %v, want the backup account's pacing yield", err)
	}
	retryAt, ok := RateLimitRetryAt(err)
	if wait := time.Until(retryAt); !ok || wait < 29*time.Second || wait > 31*time.Second {
		t.Fatalf("retryAt in %s, want about 30s", wait)
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

func TestBlockedResetChecksAllRequestedPaths(t *testing.T) {
	rl := newRateLimiter()
	rl.markRateLimitedFor("/b", 2*time.Minute)
	reset, ok := rl.blockedReset("/a", "/b")
	if !ok || time.Until(reset) < time.Minute {
		t.Fatalf("blockedReset(/a, /b) = %v, %v; want /b cooldown", reset, ok)
	}
}

func TestAfterRetryAfterPreservesFailuresAndLimit(t *testing.T) {
	rl := newRateLimiter()
	rl.limits["/p"] = rateLimitState{remaining: 5, limit: 50, reset: time.Now().Add(time.Minute), ready: true, failures: 3}
	rl.after("/p", http.Header{"Retry-After": []string{"30"}})
	state := rl.limits["/p"]
	if state.failures != 3 || state.limit != 50 {
		t.Fatalf("state = %+v, want failures=3 limit=50 preserved", state)
	}
}
