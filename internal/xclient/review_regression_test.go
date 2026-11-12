package xclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSelfCheckAcceptsNormalHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><script>window.user={"screen_name":"owner"}</script></body></html>`))
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	client.disable(&HTTPError{StatusCode: http.StatusUnauthorized})
	screen, err := client.GetSelfScreenName(context.Background())
	if err != nil || screen != "owner" {
		t.Fatalf("self check = %q, %v, want owner", screen, err)
	}
	if client.isDisabled() {
		t.Fatal("successful HTML self check did not re-enable the client")
	}
}

func TestSelfCheckSuccessfulChallengeStartsCooldown(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><title>Just a moment...</title><body>cf-chl challenge</body></html>`))
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	for range 2 {
		if _, err := client.GetSelfScreenName(context.Background()); !errors.Is(err, ErrWAFChallenge) {
			t.Fatalf("self check error = %v, want WAF challenge", err)
		}
	}
	if requests.Load() != 1 || client.isDisabled() {
		t.Fatalf("challenge handling: requests=%d disabled=%v", requests.Load(), client.isDisabled())
	}
}

func TestPoolRequestYieldsWhenSelectedClientBecomesRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("rate-limited client sent an HTTP request")
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	client.limiter.markRateLimitedFor(userByScreenNamePath, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.graphQL(withYieldOnRateLimit(ctx), userByScreenNamePath, nil)
	if !errors.Is(err, errRateLimitBusyWait) {
		t.Fatalf("selected client error = %v, want immediate local rate-limit handoff", err)
	}
	if ctx.Err() != nil {
		t.Fatal("pool request waited for its context deadline")
	}
}

// TestBeforeYieldsLongLocalWait 覆盖 P0：不走 Pool 的单账号路径也不能把唯一执行槽
// 卡在限流窗口里，等待超过阈值时返回带恢复时间的结构化错误。
func TestBeforeYieldsLongLocalWait(t *testing.T) {
	limiter := newRateLimiter()
	limiter.limits["/test"] = rateLimitState{remaining: 0, limit: 20, reset: time.Now().Add(time.Minute), ready: true}
	started := time.Now()
	err := limiter.before(context.Background(), "/test")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("before blocked for %s, want an immediate yield", elapsed)
	}
	retryAt, ok := RateLimitRetryAt(err)
	if !ok {
		t.Fatalf("error = %v, want a rate-limit error with retry time", err)
	}
	if wait := time.Until(retryAt); wait <= 0 {
		t.Fatalf("retryAt = %s, want a future recovery time", retryAt)
	}
}

func TestConcurrentPoolReservationsYieldAfterLastAvailableRequest(t *testing.T) {
	limiter := newRateLimiter()
	limiter.limits["/test"] = rateLimitState{remaining: 2, limit: 20, reset: time.Now().Add(time.Minute), ready: true}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() { results <- limiter.before(withYieldOnRateLimit(ctx), "/test") })
	}
	group.Wait()
	close(results)
	reserved, yielded := 0, 0
	for err := range results {
		switch {
		case err == nil:
			reserved++
		case errors.Is(err, errRateLimitBusyWait):
			yielded++
		default:
			t.Fatalf("reservation error = %v", err)
		}
	}
	if reserved != 1 || yielded != 1 {
		t.Fatalf("reserved=%d yielded=%d, want one of each", reserved, yielded)
	}
}

func TestPoolAuthenticationErrorsSwitchToValidCookie(t *testing.T) {
	for _, code := range []int{32, 64, 89, 215, apiErrAccountLocked} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var primaryRequests, backupRequests atomic.Int64
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryRequests.Add(1)
				_, _ = fmt.Fprintf(w, `{"errors":[{"code":%d,"message":"invalid credentials"}]}`, code)
			}))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupRequests.Add(1)
				_, _ = w.Write([]byte(`{"data":{"user":{"result":{"rest_id":"u2","legacy":{"screen_name":"backup"}}}}}`))
			}))
			defer backup.Close()
			pool := &Pool{clients: []*Client{newHeaderTestClient(primary), newHeaderTestClient(backup)}}
			user, err := pool.GetUserByInput(context.Background(), "backup")
			if err != nil || user.ID != "u2" {
				t.Fatalf("get user = %+v, %v, want backup user", user, err)
			}
			if !pool.clients[0].isDisabled() || primaryRequests.Load() != 1 || backupRequests.Load() != 1 {
				t.Fatalf("failover: disabled=%v primary=%d backup=%d", pool.clients[0].isDisabled(), primaryRequests.Load(), backupRequests.Load())
			}
		})
	}
}
