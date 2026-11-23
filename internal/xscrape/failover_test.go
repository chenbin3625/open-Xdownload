package xscrape

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

type fixedTID struct{}

func (fixedTID) Generate(context.Context, string, string) (string, error) { return "tid", nil }
func (fixedTID) Invalidate()                                              {}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGraphQLWithAccountFailsOverAfterDeactivation(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "xscrape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	accA, err := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokA", CSRFToken: "ctA", Active: true, IsPrimary: true})
	if err != nil {
		t.Fatal(err)
	}
	accB, err := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokB", CSRFToken: "ctB", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	var hitsA, hitsB atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("auth_token")
		if cookie != nil && cookie.Value == "tokA" {
			hitsA.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you"}]}`))
			return
		}
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	engine := NewEngine(store, Options{
		BaseURL:        srv.URL,
		DisablePacing:  true,
		TransactionIDs: fixedTID{},
	})
	rawSrc, err := engine.Source(ctx, config.AppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	src := rawSrc.(*source)
	payload, used, err := src.graphQLWithAccount(ctx, xapi.UserByScreenNamePath, url.Values{}, nil)
	if err != nil {
		t.Fatalf("expected failover to account B, got error: %v", err)
	}
	if used.ID != accB.ID {
		t.Fatalf("used account = %d, want %d (hitsA=%d hitsB=%d)", used.ID, accB.ID, hitsA.Load(), hitsB.Load())
	}
	if string(payload) != `{"data":{"ok":true}}` {
		t.Fatalf("unexpected payload: %s", payload)
	}
	reloadedA, err := store.GetXAccount(ctx, accA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedA.Active {
		t.Fatal("expected account A to be deactivated after fatal 401")
	}
}

func TestGraphQLWithAccountAllRateLimitedReturnsPoolExhausted(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "xscrape-rl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, err = store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokA", CSRFToken: "ctA", Active: true, IsPrimary: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokB", CSRFToken: "ctB", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	resetA := now.Add(15 * time.Minute)
	resetB := now.Add(3 * time.Minute)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("auth_token")
		reset := resetA
		if cookie != nil && cookie.Value == "tokB" {
			reset = resetB
		}
		w.Header().Set("x-rate-limit-reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
	}))
	defer srv.Close()

	engine := NewEngine(store, Options{
		BaseURL:        srv.URL,
		DisablePacing:  true,
		TransactionIDs: fixedTID{},
	})
	engine.now = func() time.Time { return now }
	rawSrc, err := engine.Source(ctx, config.AppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	src := rawSrc.(*source)
	_, _, err = src.graphQLWithAccount(xapi.WithYieldToScheduler(ctx), xapi.UserByScreenNamePath, url.Values{}, nil)
	if !xapi.IsAllClientsRateLimited(err) {
		t.Fatalf("expected IsAllClientsRateLimited=true, got err=%v", err)
	}
	retryAt, ok := xapi.RateLimitRetryAt(err)
	if !ok || !retryAt.Equal(resetB) {
		t.Fatalf("expected earliest unlock %v, got %v (ok=%v)", resetB, retryAt, ok)
	}
}

func TestGraphQLWithAccountPrioritizesContextCancelOverLastErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var hits atomic.Int32
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			// After first account fails with 500, cancel ctx before nextAccount picks account 2.
			cancel()
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	})
	h.account("tok-1", "ct-1", "")
	h.account("tok-2", "ct-2", "")

	_, _, err := h.source().graphQLWithAccount(ctx, xapi.UserByScreenNamePath, url.Values{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestGraphQLWithAccountWaitsForRateLimitedAccountUnlockInNonSchedulerMode(t *testing.T) {
	var hits atomic.Int32
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			reset := h.clock.Now().Add(5 * time.Second)
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(reset.Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"ok":true}}`)
	})
	h.account("tok-only", "ct-only", "")
	var slept time.Duration
	h.engine.sleep = func(ctx context.Context, d time.Duration) error {
		slept += d
		h.clock.Advance(d)
		return ctx.Err()
	}

	payload, _, err := h.source().graphQLWithAccount(context.Background(), xapi.UserByScreenNamePath, url.Values{}, nil)
	if err != nil {
		t.Fatalf("expected non-scheduler request to wait for unlock and succeed, got %v", err)
	}
	if string(payload) != `{"data":{"ok":true}}` || hits.Load() != 2 || slept < 5*time.Second {
		t.Fatalf("payload=%s hits=%d slept=%v, want ok, 2 hits, slept>=5s", payload, hits.Load(), slept)
	}
}

func TestGraphQLWithAccountMixed429AndNonRateLimitDoesNotReportPoolExhausted(t *testing.T) {
	t.Run("429 plus 500", func(t *testing.T) {
		h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
			if requestAuthToken(r) == "tok-1" {
				reset := h.clock.Now().Add(5 * time.Minute)
				w.Header().Set("x-rate-limit-reset", strconv.FormatInt(reset.Unix(), 10))
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("backend failure"))
		})
		h.account("tok-1", "ct-1", "")
		h.account("tok-2", "ct-2", "")

		_, _, err := h.source().graphQLWithAccount(xapi.WithYieldToScheduler(context.Background()), xapi.UserByScreenNamePath, url.Values{}, nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if xapi.IsAllClientsRateLimited(err) {
			t.Fatalf("mixed 429 + 500 must NOT report PoolExhausted/IsAllClientsRateLimited, got %v", err)
		}
	})

	t.Run("429 plus target-level 401", func(t *testing.T) {
		h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == probePath {
				writeJSON(w, http.StatusOK, `{"users":[{"user_id":"1","screen_name":"ok","is_auth_valid":true}]}`)
				return
			}
			if requestAuthToken(r) == "tok-1" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("target unauthorized"))
				return
			}
			reset := h.clock.Now().Add(5 * time.Minute)
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(reset.Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
		})
		h.account("tok-1", "ct-1", "")
		h.account("tok-2", "ct-2", "")

		_, _, err := h.source().graphQLWithAccount(xapi.WithYieldToScheduler(context.Background()), xapi.UserByScreenNamePath, url.Values{}, nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if xapi.IsAllClientsRateLimited(err) {
			t.Fatalf("mixed 429 + active 401 must NOT report PoolExhausted/IsAllClientsRateLimited, got %v", err)
		}
	})
}

func TestGuestTokenSharesErrorAndSurvivesLeaderCancelAndPanic(t *testing.T) {
	t.Run("shared error across concurrent waiters", func(t *testing.T) {
		gate := make(chan struct{})
		started := make(chan struct{})
		var startOnce sync.Once
		var calls atomic.Int32
		h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/1.1/guest/activate.json" {
				calls.Add(1)
				startOnce.Do(func() { close(started) })
				<-gate
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("upstream down"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})

		const n = 6
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			go func() {
				_, err := h.engine.guestToken(context.Background(), "")
				errs <- err
			}()
		}
		<-started
		time.Sleep(25 * time.Millisecond)
		close(gate)

		for i := 0; i < n; i++ {
			if err := <-errs; err == nil || !errors.Is(err, errFetchGuestToken) {
				t.Fatalf("waiter %d err = %v, want errFetchGuestToken", i, err)
			}
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("activate calls = %d, want 1 (no thundering herd on error)", got)
		}
	})

	t.Run("leader cancel does not abort waiter", func(t *testing.T) {
		gate := make(chan struct{})
		started := make(chan struct{})
		var startOnce sync.Once
		h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/1.1/guest/activate.json" {
				startOnce.Do(func() { close(started) })
				<-gate
				writeJSON(w, http.StatusOK, `{"guest_token":"shared-tok"}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		leaderErr := make(chan error, 1)
		go func() {
			_, err := h.engine.guestToken(leaderCtx, "")
			leaderErr <- err
		}()
		<-started

		waiterTok := make(chan string, 1)
		waiterErr := make(chan error, 1)
		go func() {
			tok, err := h.engine.guestToken(context.Background(), "")
			waiterTok <- tok
			waiterErr <- err
		}()
		time.Sleep(25 * time.Millisecond)

		cancelLeader()
		if err := <-leaderErr; !errors.Is(err, context.Canceled) {
			t.Fatalf("leader err = %v, want context.Canceled", err)
		}
		close(gate)
		if err := <-waiterErr; err != nil {
			t.Fatalf("waiter err = %v, want nil", err)
		}
		if tok := <-waiterTok; tok != "shared-tok" {
			t.Fatalf("waiter token = %q, want shared-tok", tok)
		}
	})

	t.Run("panic closes wait channel without deadlock", func(t *testing.T) {
		engine := NewEngine(nil, Options{
			APIBaseURL:    "https://api.example",
			DisablePacing: true,
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				panic("boom transport")
			}),
		})
		_, err := engine.guestToken(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "panic") {
			t.Fatalf("err = %v, want panic error", err)
		}
	})
}

func TestRateLimitResetCapsOverflowAndMillis(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	maxReset := now.Add(maxRateLimitWait)

	cases := []struct {
		name   string
		header http.Header
		want   time.Time
	}{
		{
			name:   "unix millis within cap",
			header: http.Header{"X-Rate-Limit-Reset": []string{strconv.FormatInt(now.Add(10*time.Minute).UnixMilli(), 10)}},
			want:   now.Add(10 * time.Minute),
		},
		{
			name:   "unix seconds over cap",
			header: http.Header{"X-Rate-Limit-Reset": []string{strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10)}},
			want:   maxReset,
		},
		{
			name:   "unix overflow ErrRange",
			header: http.Header{"X-Rate-Limit-Reset": []string{"999999999999999999999999"}},
			want:   maxReset,
		},
		{
			name:   "Retry-After overflow ErrRange",
			header: http.Header{"Retry-After": []string{"18446744073709551615"}},
			want:   maxReset,
		},
		{
			name:   "Retry-After huge seconds",
			header: http.Header{"Retry-After": []string{"999999999"}},
			want:   maxReset,
		},
		{
			name:   "Retry-After far future HTTP-date",
			header: http.Header{"Retry-After": []string{now.Add(72 * time.Hour).Format(http.TimeFormat)}},
			want:   maxReset,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rateLimitReset(tc.header, now)
			if !got.Equal(tc.want) {
				t.Fatalf("rateLimitReset = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNextAccountRetriesOnPickUnlockTOCTOU(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, emptyGraphQL)
	acc := h.account("tok-1", "ct-1", "")

	// Lock expires at base + 1s. First call to engine.now() (PickXAccount) returns base,
	// second call (EarliestXAccountUnlock) returns base + 2s so the lock has just expired.
	base := h.clock.Now()
	if err := h.store.LockXAccount(ctx, acc.ID, "UserMedia", base.Add(time.Second), "rate limit"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	h.engine.now = func() time.Time {
		if calls.Add(1) == 1 {
			return base
		}
		return base.Add(2 * time.Second)
	}

	picked, release, err := h.engine.nextAccount(ctx, "UserMedia", nil)
	if err != nil {
		t.Fatalf("nextAccount should retry after TOCTOU unlock, got %v", err)
	}
	release()
	if picked.ID != acc.ID {
		t.Fatalf("picked account = %d, want %d", picked.ID, acc.ID)
	}
}

func TestNextAccountDoesNotSwallowTransientDBErrorAfterSlotWait(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, emptyGraphQL)
	acc := h.account("tok-1", "ct-1", "")

	_, release, _, err := h.engine.slots.acquire(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, rel, err := h.engine.nextAccount(ctx, "UserMedia", nil)
		if rel != nil {
			rel()
		}
		done <- err
	}()
	time.Sleep(25 * time.Millisecond)
	// Close DB to simulate a transient DB error (not sql.ErrNoRows) while waiting on slot.
	_ = h.store.Close()
	release()

	err = <-done
	if err == nil || strings.Contains(err.Error(), "没有可用于本次请求的 X Cookie") {
		t.Fatalf("expected DB error to propagate, got %v", err)
	}
}

func TestGraphQLWithAccountFailsOverFastOnBadProxyDialTimeout(t *testing.T) {
	prev := proxyDialTimeout
	proxyDialTimeout = 120 * time.Millisecond
	t.Cleanup(func() { proxyDialTimeout = prev })

	// Start a TCP listener that accepts nothing or use a custom wrapped transport whose dial hangs for tok-bad.
	var dialAttemptsA atomic.Int32
	goodServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"data":{"ok":true}}`)
	}))
	defer goodServer.Close()

	// Create a blackhole TCP proxy that accepts TCP? No, dial timeout happens when DialContext hangs.
	// Exercise wrapDialTimeout directly with a hanging baseDial on account A's transport.
	badTransport := wrapDialTimeout(&http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialAttemptsA.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	goodTransport := goodServer.Client().Transport

	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "proxy-failover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	accA, err := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokA", CSRFToken: "ctA", Proxy: "http://127.0.0.1:18080", Active: true, IsPrimary: true})
	if err != nil {
		t.Fatal(err)
	}
	accB, err := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokB", CSRFToken: "ctB", Proxy: "http://127.0.0.1:18081", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(store, Options{
		BaseURL:        goodServer.URL,
		DisablePacing:  true,
		TransactionIDs: fixedTID{},
	})
	// Pre-populate per-proxy transport cache so A hits badTransport and B hits goodTransport.
	engine.transports = map[string]*http.Transport{
		accA.Proxy: badTransport,
		accB.Proxy: goodTransport.(*http.Transport).Clone(),
	}
	engine.transportOrder = []string{accA.Proxy, accB.Proxy}

	rawSrc, err := engine.Source(ctx, config.AppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	payload, used, err := rawSrc.(*source).graphQLWithAccount(ctx, xapi.UserByScreenNamePath, url.Values{}, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected fast failover to account B, got %v", err)
	}
	if used.ID != accB.ID || string(payload) != `{"data":{"ok":true}}` {
		t.Fatalf("used=%d payload=%s, want accB (%d)", used.ID, payload, accB.ID)
	}
	if dialAttemptsA.Load() != 1 {
		t.Fatalf("bad proxy dial attempts = %d, want 1 (fast failover without 3x retry)", dialAttemptsA.Load())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("failover took %v, expected quick dial timeout failover", elapsed)
	}
}

func TestLookupFetcherReturnsLastAgeRestrictedAndChecksActiveCandidates(t *testing.T) {
	ageRestrictedBody := `{"data":{"user":{"result":{"__typename":"UserUnavailable","reason":"Suspended","message":"User is age-restricted"}}}}`

	t.Run("falls back to last age-restricted payload when second account fails", func(t *testing.T) {
		h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
			if requestAuthToken(r) == "tok-1" {
				writeJSON(w, http.StatusOK, ageRestrictedBody)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("second account 500"))
		})
		h.account("tok-1", "ct-1", "")
		h.account("tok-2", "ct-2", "")

		_, err := h.source().GetUserByInput(context.Background(), "target")
		if err == nil || !strings.Contains(err.Error(), "年龄限制") {
			t.Fatalf("err = %v, want age-restricted error from last payload", err)
		}
	})

	t.Run("retries remaining active account even when excluded account was deactivated", func(t *testing.T) {
		var acc1ID int64
		h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
			if requestAuthToken(r) == "tok-1" {
				// Deactivate acc1 right before responding so active count becomes 1 while excluded len is 1.
				_ = h.store.DeactivateXAccount(context.Background(), acc1ID, "deactivated")
				writeJSON(w, http.StatusOK, ageRestrictedBody)
				return
			}
			writeJSON(w, http.StatusOK, `{"data":{"user":{"result":{"__typename":"User","rest_id":"u42","legacy":{"screen_name":"target","name":"Target"}}}}}`)
		})
		acc1 := h.account("tok-1", "ct-1", "")
		acc1ID = acc1.ID
		h.account("tok-2", "ct-2", "")

		user, err := h.source().GetUserByInput(context.Background(), "target")
		if err != nil {
			t.Fatalf("expected retry on active tok-2 to succeed, got %v", err)
		}
		if user.ID != "u42" {
			t.Fatalf("user = %+v, want u42", user)
		}
	})
}

func TestDoPinnedRespectsAccountLocks(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "xscrape-pinned.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	acc, err := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokP", CSRFToken: "ctP", Active: true, IsPrimary: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	until := now.Add(10 * time.Minute)
	if err := store.LockXAccount(ctx, acc.ID, "*", until, "waf"); err != nil {
		t.Fatal(err)
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	engine := NewEngine(store, Options{
		BaseURL:        srv.URL,
		DisablePacing:  true,
		TransactionIDs: fixedTID{},
	})
	engine.now = func() time.Time { return now }
	rawSrc, err := engine.Source(ctx, config.AppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	src := rawSrc.(*source)
	_, err = src.doPinned(ctx, acc, http.MethodGet, xapi.UserByRestIDPath, url.Values{}, nil)
	if !xapi.IsAllClientsRateLimited(err) {
		t.Fatalf("expected RateLimitedError for locked pinned account, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected 0 HTTP requests to locked pinned account, got %d", hits.Load())
	}
}

func TestCode353DeactivatesAndFailsOver(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if requestAuthToken(r) == "tok-1" {
			writeJSON(w, http.StatusForbidden, `{"code":353,"message":"This request requires a matching csrf cookie and header."}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"ok":true}}`)
	})
	acc1 := h.account("tok-1", "bad-ct0", "")
	h.account("tok-2", "good-ct0", "")

	payload, err := h.source().GraphQL(ctx, xapi.UserByScreenNamePath, url.Values{})
	if err != nil {
		t.Fatalf("expected failover after code 353, got %v", err)
	}
	if string(payload) != `{"data":{"ok":true}}` {
		t.Fatalf("unexpected payload: %s", payload)
	}
	reloaded, _ := h.store.GetXAccount(ctx, acc1.ID)
	if reloaded.Active {
		t.Fatal("account with code 353 should be deactivated")
	}
}

func TestHTTP200TransientAPIErrorRetriesAndSucceeds(t *testing.T) {
	var attempts atomic.Int32
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			writeJSON(w, http.StatusOK, `{"errors":[{"code":130,"message":"Over capacity"}]}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"ok":true}}`)
	})
	h.account("tok-1", "ct0-1", "")
	payload, err := h.source().GraphQL(context.Background(), xapi.UserByScreenNamePath, url.Values{})
	if err != nil {
		t.Fatalf("expected retry on code 130, got %v", err)
	}
	if string(payload) != `{"data":{"ok":true}}` || attempts.Load() != 3 {
		t.Fatalf("attempts=%d payload=%s", attempts.Load(), payload)
	}
}

func TestGuestRequestRetriesTransientAPIErrorAndPropagatesTransportError(t *testing.T) {
	var attempts atomic.Int32
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.1/guest/activate.json" {
			writeJSON(w, http.StatusOK, `{"guest_token":"g-1"}`)
			return
		}
		if attempts.Add(1) == 1 {
			writeJSON(w, http.StatusOK, `{"errors":[{"code":130,"message":"Over capacity"}]}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"tweet_result_by_rest_id":{"result":`+tweetFixture("789", "author")+`}}}`)
	})
	tweet, err := h.source().FetchTweet(context.Background(), "789", parser.ParseOptions{})
	if err != nil || tweet.ID != "789" || attempts.Load() != 2 {
		t.Fatalf("tweet=%+v err=%v attempts=%d", tweet, err, attempts.Load())
	}

	// Transport error must not be swallowed as (nil, nil).
	h.server.Close()
	if _, err := h.source().guestGraphQL(context.Background(), xapi.UserByScreenNamePath, url.Values{}); err == nil {
		t.Fatal("expected transport error from closed server, got nil")
	}
}

func TestInvalidateGuestTokenChecksTokenMatch(t *testing.T) {
	e := NewEngine(nil, Options{})
	e.guest.token = "new-token"
	e.guest.expires = time.Now().Add(time.Hour)
	e.invalidateGuestToken("old-token")
	if e.guest.token != "new-token" {
		t.Fatal("mismatching token invalidated newer guest token")
	}
	e.invalidateGuestToken("new-token")
	if e.guest.token != "" {
		t.Fatal("matching token was not invalidated")
	}
}

func TestDoPinnedReloadsAccountAndSetsPoolExhaustedOn429(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
	})
	acc := h.account("tok-p", "ct-p", "")
	_, err := h.source().doPinned(ctx, acc, http.MethodGet, xapi.UserByRestIDPath, url.Values{}, nil)
	if !xapi.IsAllClientsRateLimited(err) {
		t.Fatalf("pinned 429 error = %v, want IsAllClientsRateLimited=true", err)
	}

	if err := h.store.DeactivateXAccount(ctx, acc.ID, "deactivated"); err != nil {
		t.Fatal(err)
	}
	_, err = h.source().doPinned(ctx, acc, http.MethodGet, xapi.UserByRestIDPath, url.Values{}, nil)
	if !errors.Is(err, xapi.ErrClientDisabled) {
		t.Fatalf("deactivated pinned account error = %v, want ErrClientDisabled", err)
	}
}

func TestPageRequesterFailsOverAndBindsSucceededViewer(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if requestAuthToken(r) == "tok-1" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"list":{"members_timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[{"content":{"entryType":"TimelineTimelineItem","itemContent":{"user_results":{"result":{"rest_id":"u1","legacy":{"screen_name":"member","name":"Member"}}}}}}]}]}}}}}`)
	})
	h.account("tok-1", "ct-1", "")
	h.account("tok-2", "ct-2", "")

	page, err := h.source().GetListMembersPage(context.Background(), xapi.List{ID: "L1"}, "", "")
	if err != nil {
		t.Fatalf("GetListMembersPage: %v", err)
	}
	if want := xapi.ViewerKey("tok-2"); page.ViewerKey != want {
		t.Fatalf("ViewerKey = %q, want %q", page.ViewerKey, want)
	}
}

func TestNextAccountDoesNotMutateCallerExcludedMap(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, emptyGraphQL)
	acc := h.account("tok-1", "ct-1", "")
	_, release, _, err := h.engine.slots.acquire(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	callerExcluded := map[int64]bool{999: true}
	done := make(chan error, 1)
	go func() {
		_, rel, err := h.engine.nextAccount(ctx, "UserMedia", callerExcluded)
		if rel != nil {
			rel()
		}
		done <- err
	}()
	time.Sleep(25 * time.Millisecond)
	inactive := false
	_, _ = h.store.UpdateXAccount(ctx, acc.ID, storage.XAccountPatch{Active: &inactive})
	release()
	<-done
	if len(callerExcluded) != 1 || !callerExcluded[999] {
		t.Fatalf("callerExcluded was mutated: %v", callerExcluded)
	}
}
