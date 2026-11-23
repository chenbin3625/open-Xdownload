package xclient

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// TestPoolGraphQLFailoverOnErrClientDisabledWithoutRateLimitBlock verifies that when a
// selected client becomes disabled before doOnce executes (so limiter.wouldBlock is false),
// graphQLWithClient immediately fails over to the next client instead of aborting.
func TestPoolGraphQLFailoverOnErrClientDisabledWithoutRateLimitBlock(t *testing.T) {
	var backupHits atomic.Int64
	backupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupHits.Add(1)
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer backupSrv.Close()

	first := &Client{
		http:         backupSrv.Client(),
		baseURL:      backupSrv.URL,
		limiter:      newRateLimiter(),
		retryBackoff: func(int) time.Duration { return 0 },
	}
	// Make first pass Select() cleanly, then become disabled right before doOnce
	// by using a pre-disabled check via a custom transport? Even cleaner:
	// Select() checks client.isDisabled(), then doOnce() checks client.isDisabled() again.
	// Simulate the race by having first's Select() succeed, then disabling first in a
	// limiter/RoundTripper? Wait: doOnce checks c.isDisabled() BEFORE RoundTrip!
	// We can reproduce the exact race deterministically using a single-step window:
	// set disabledUntil right after Select returns by wrapping requests or by calling
	// Select? Wait: we can set first.http.Transport to nil and test via a pool where
	// first.disabledUntil is set in a goroutine? No, races shouldn't be timing-dependent.
	// Instead, use a RoundTripper that returns ErrClientDisabled directly (or disable
	// first during beginRequest? No, doOnce checks isDisabled before beginRequest).
	// Actually, returning ErrClientDisabled from a custom RoundTripper hits the exact
	// `if errors.Is(err, ErrClientDisabled)` branch inside graphQLWithClient while
	// `first.limiter.wouldBlock(path) == false` and `first.isDisabled() == true`!
	first.http = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			first.disable(ErrClientDisabled)
			return nil, ErrClientDisabled
		}),
	}
	backup := newHeaderTestClient(backupSrv)
	pool := &Pool{clients: []*Client{first, backup}}

	payload, err := pool.GraphQL(context.Background(), "/i/api/graphql/x/Test", nil)
	if err != nil {
		t.Fatalf("GraphQL failed to failover on ErrClientDisabled: %v", err)
	}
	if string(payload) != `{"data":{"ok":true}}` {
		t.Fatalf("payload = %s, want ok", payload)
	}
	if backupHits.Load() != 1 {
		t.Fatalf("backup hits = %d, want 1", backupHits.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestPoolGraphQLPrioritizesContextCancelOverLastErr verifies that caller cancellation
// is never swallowed behind an earlier client's transient lastErr.
func TestPoolGraphQLPrioritizesContextCancelOverLastErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			cancel()
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c1 := newHeaderTestClient(srv)
	c2 := newHeaderTestClient(srv)
	c2.limiter.markRateLimitedFor("/i/api/graphql/x/Test", time.Minute)
	pool := &Pool{clients: []*Client{c1, c2}}

	_, err := pool.GraphQL(ctx, "/i/api/graphql/x/Test", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestPoolSelectReturnsUntruncatedRetryAt verifies that Select computes RetryAt from the
// untruncated earliest reset time even when the sleep step is clamped to maxPoolSelectWaitStep.
func TestPoolSelectReturnsUntruncatedRetryAt(t *testing.T) {
	prevMaxWait := maxPoolSelectWait
	prevStep := maxPoolSelectWaitStep
	maxPoolSelectWait = 20 * time.Millisecond
	maxPoolSelectWaitStep = 50 * time.Millisecond
	t.Cleanup(func() {
		maxPoolSelectWait = prevMaxWait
		maxPoolSelectWaitStep = prevStep
	})

	limiter := newRateLimiter()
	limiter.markRateLimitedFor("/p", 10*time.Minute)
	pool := &Pool{clients: []*Client{{limiter: limiter}}}

	_, err := pool.Select(context.Background(), "/p")
	retryAt, ok := RateLimitRetryAt(err)
	if !ok {
		t.Fatalf("err = %v, want RateLimitedError", err)
	}
	if !IsAllClientsRateLimited(err) {
		t.Fatalf("err = %v, want ErrAllClientsRateLimited", err)
	}
	if wait := time.Until(retryAt); wait < 9*time.Minute || wait > 11*time.Minute {
		t.Fatalf("RetryAt wait = %s, want untruncated ~10m (not clamped to step 50ms)", wait)
	}
}

// TestRateLimitResetCapsAndHandlesMilliseconds verifies that X-Rate-Limit-Reset handles
// millisecond timestamps safely and caps huge/overflowing headers at maxRateLimitWait (24h).
func TestRateLimitResetCapsAndHandlesMilliseconds(t *testing.T) {
	now := time.Now()

	// Millisecond timestamp (5 minutes from now) should NOT lock for thousands of years.
	msReset := fmt.Sprint(now.Add(5 * time.Minute).UnixMilli())
	rl := newRateLimiter()
	rl.after("/ms", http.Header{
		"X-Rate-Limit-Limit":     []string{"50"},
		"X-Rate-Limit-Remaining": []string{"0"},
		"X-Rate-Limit-Reset":     []string{msReset},
	})
	reset, ok := rl.blockedReset("/ms")
	if !ok {
		t.Fatal("expected /ms to be blocked")
	}
	if wait := time.Until(reset); wait < 4*time.Minute || wait > 6*time.Minute {
		t.Fatalf("ms reset wait = %s, want ~5m", wait)
	}

	// Huge timestamp (> 24h) is capped at 24h.
	hugeReset := fmt.Sprint(now.Add(365 * 24 * time.Hour).Unix())
	rl.after("/huge", http.Header{
		"X-Rate-Limit-Limit":     []string{"50"},
		"X-Rate-Limit-Remaining": []string{"0"},
		"X-Rate-Limit-Reset":     []string{hugeReset},
	})
	reset, ok = rl.blockedReset("/huge")
	if !ok {
		t.Fatal("expected /huge to be blocked")
	}
	if wait := time.Until(reset); wait < maxRateLimitWait-10*time.Second || wait > maxRateLimitWait+10*time.Second {
		t.Fatalf("huge reset wait = %s, want capped ~24h", wait)
	}

	// Int64 positive overflow is also capped at 24h.
	rl.after("/overflow", http.Header{
		"X-Rate-Limit-Limit":     []string{"50"},
		"X-Rate-Limit-Remaining": []string{"0"},
		"X-Rate-Limit-Reset":     []string{"99999999999999999999999"},
	})
	reset, ok = rl.blockedReset("/overflow")
	if !ok {
		t.Fatal("expected /overflow to be blocked")
	}
	if wait := time.Until(reset); wait < maxRateLimitWait-10*time.Second || wait > maxRateLimitWait+10*time.Second {
		t.Fatalf("overflow reset wait = %s, want capped ~24h", wait)
	}
}

// TestGetSelfScreenNameMultiListProbeCases verifies alignment with xapi/probe.go:
// path, required headers, twid matching, suspended/353/401 disabling, and code 88 rate limit.
func TestGetSelfScreenNameMultiListProbeCases(t *testing.T) {
	t.Run("prefers twid user and sends OAuth2Session headers", func(t *testing.T) {
		var gotPath, gotAuth, gotAuthType, gotCSRF string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			gotAuthType = r.Header.Get("X-Twitter-Auth-Type")
			gotCSRF = r.Header.Get("X-Csrf-Token")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"users":[
				{"user_id":"100","screen_name":"first_user","is_auth_valid":true},
				{"user_id":"200","screen_name":"twid_owner","is_auth_valid":true}
			]}`))
		}))
		defer srv.Close()
		client := newHeaderTestClient(srv)
		client.csrfToken = "ct0-val"
		client.extraCookies = []*http.Cookie{{Name: "twid", Value: "u%3D200"}}
		got, err := client.GetSelfScreenName(context.Background())
		if err != nil || got != "twid_owner" {
			t.Fatalf("GetSelfScreenName = %q, %v, want twid_owner", got, err)
		}
		if gotPath != homeCheckPath || gotAuth == "" || gotAuthType != "OAuth2Session" || gotCSRF != "ct0-val" {
			t.Fatalf("headers/path mismatch: path=%q auth=%q authType=%q csrf=%q", gotPath, gotAuth, gotAuthType, gotCSRF)
		}
	})

	t.Run("suspended user disables client", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"users":[{"user_id":"1","screen_name":"banned","is_auth_valid":true,"is_suspended":true}]}`))
		}))
		defer srv.Close()
		client := newHeaderTestClient(srv)
		if _, err := client.GetSelfScreenName(context.Background()); err == nil {
			t.Fatal("expected error for suspended user")
		}
		if !client.isDisabled() {
			t.Fatal("suspended user must disable client")
		}
	})

	t.Run("code 353 ct0 mismatch disables client", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":353,"message":"This request requires a matching csrf cookie and header."}`))
		}))
		defer srv.Close()
		client := newHeaderTestClient(srv)
		if _, err := client.GetSelfScreenName(context.Background()); err == nil {
			t.Fatal("expected error for code 353")
		}
		if !client.isDisabled() {
			t.Fatal("code 353 must disable client")
		}
	})

	t.Run("HTTP 200 with code 88 rate limits without disabling", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
		}))
		defer srv.Close()
		client := newHeaderTestClient(srv)
		_, err := client.GetSelfScreenName(context.Background())
		if !xapi.IsRateLimitError(err) {
			t.Fatalf("err = %v, want rate limit error", err)
		}
		if client.isDisabled() {
			t.Fatal("code 88 rate limit must not disable client")
		}
	})
}

// TestPoolGraphQLFailsOverFastOnBadProxyDialTimeout verifies that a hanging proxy dial
// hits proxyDialTimeout once and immediately fails over to the next account.
func TestPoolGraphQLFailsOverFastOnBadProxyDialTimeout(t *testing.T) {
	prevTimeout := proxyDialTimeout
	proxyDialTimeout = 40 * time.Millisecond
	t.Cleanup(func() { proxyDialTimeout = prevTimeout })

	var badDials atomic.Int64
	badTransport := &http.Transport{
		DialContext: withProxyDialTimeout(func(ctx context.Context, network, addr string) (net.Conn, error) {
			badDials.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	}
	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer goodSrv.Close()

	badClient := &Client{
		http:         newHTTPClient(badTransport),
		baseURL:      goodSrv.URL,
		limiter:      newRateLimiter(),
		retryBackoff: func(int) time.Duration { return 0 },
	}
	goodClient := newHeaderTestClient(goodSrv)
	pool := &Pool{clients: []*Client{badClient, goodClient}}

	start := time.Now()
	payload, err := pool.GraphQL(context.Background(), "/i/api/graphql/x/Test", nil)
	if err != nil {
		t.Fatalf("GraphQL failed to failover from hanging proxy: %v", err)
	}
	if string(payload) != `{"data":{"ok":true}}` {
		t.Fatalf("payload = %s, want ok", payload)
	}
	if badDials.Load() != 1 {
		t.Fatalf("bad proxy dial attempts = %d, want 1 (fast failover without 3x retry)", badDials.Load())
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("failover took %s, want fast failover near %s", elapsed, proxyDialTimeout)
	}
}

// TestAccountRequestsPrunesExpiredIdleEntries verifies that the global accountRequests
// map evicts expired idle entries while keeping active or rate-limit-blocked entries.
func TestAccountRequestsPrunesExpiredIdleEntries(t *testing.T) {
	prevTTL := accountRequestLimitTTL
	prevInterval := accountRequestPruneInterval
	accountRequestLimitTTL = 30 * time.Millisecond
	accountRequestPruneInterval = 1 * time.Millisecond
	t.Cleanup(func() {
		accountRequestLimitTTL = prevTTL
		accountRequestPruneInterval = prevInterval
	})

	idleToken := "prune-idle-" + t.Name()
	blockedToken := "prune-blocked-" + t.Name()
	inFlightToken := "prune-inflight-" + t.Name()
	triggerToken := "prune-trigger-" + t.Name()

	idleKey := sha256.Sum256([]byte(idleToken))
	blockedKey := sha256.Sum256([]byte(blockedToken))
	inFlightKey := sha256.Sum256([]byte(inFlightToken))
	triggerKey := sha256.Sum256([]byte(triggerToken))
	t.Cleanup(func() {
		accountRequests.Lock()
		delete(accountRequests.items, idleKey)
		delete(accountRequests.items, blockedKey)
		delete(accountRequests.items, inFlightKey)
		delete(accountRequests.items, triggerKey)
		accountRequests.Unlock()
	})

	idle := requestLimitForAccount(idleToken)
	blocked := requestLimitForAccount(blockedToken)
	inFlight := requestLimitForAccount(inFlightToken)

	blocked.limiter.markRateLimitedFor("/p", 5*time.Minute)
	inFlight.inFlight.Add(1)
	defer inFlight.inFlight.Add(-1)

	past := time.Now().Add(-time.Second).UnixNano()
	idle.lastAccess.Store(past)
	blocked.lastAccess.Store(past)
	inFlight.lastAccess.Store(past)

	accountRequests.Lock()
	accountRequests.lastPrune = time.Now().Add(-time.Second)
	accountRequests.Unlock()

	_ = requestLimitForAccount(triggerToken)

	accountRequests.Lock()
	_, idleExists := accountRequests.items[idleKey]
	_, blockedExists := accountRequests.items[blockedKey]
	_, inFlightExists := accountRequests.items[inFlightKey]
	accountRequests.Unlock()

	if idleExists {
		t.Fatal("expired idle entry was not pruned from accountRequests")
	}
	if !blockedExists {
		t.Fatal("rate-limit-blocked entry was prematurely pruned")
	}
	if !inFlightExists {
		t.Fatal("in-flight entry was prematurely pruned")
	}
}
