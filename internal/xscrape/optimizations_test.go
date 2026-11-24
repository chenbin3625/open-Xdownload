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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func TestProactiveRateLimitHeaderLocksBefore429AndPopulatesDiagnostics(t *testing.T) {
	ctx := context.Background()
	var tokens []string
	var mu sync.Mutex
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		tok := requestAuthToken(r)
		mu.Lock()
		tokens = append(tokens, tok)
		mu.Unlock()
		reset := h.clock.Now().Add(10 * time.Minute)
		w.Header().Set("X-Rate-Limit-Limit", "50")
		w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(reset.Unix(), 10))
		if tok == "tok-1" {
			w.Header().Set("X-Rate-Limit-Remaining", "0")
		} else {
			w.Header().Set("X-Rate-Limit-Remaining", "42")
		}
		writeJSON(w, http.StatusOK, `{"data":{"ok":true}}`)
	})
	acc1 := h.account("tok-1", "ct-1", "")
	h.account("tok-2", "ct-2", "")

	src := h.source()
	// Request 1 uses tok-1, receives Remaining=0 on HTTP 200 -> proactively locks tok-1!
	if _, err := src.GraphQL(ctx, xapi.UserMediaPath, url.Values{}); err != nil {
		t.Fatalf("req 1: %v", err)
	}
	// Request 2 & 3 should both route to tok-2 because tok-1 is proactively locked.
	if _, err := src.GraphQL(ctx, xapi.UserMediaPath, url.Values{}); err != nil {
		t.Fatalf("req 2: %v", err)
	}
	if _, err := src.GraphQL(ctx, xapi.UserMediaPath, url.Values{}); err != nil {
		t.Fatalf("req 3: %v", err)
	}

	mu.Lock()
	gotTokens := append([]string(nil), tokens...)
	mu.Unlock()
	if len(gotTokens) != 3 || gotTokens[0] != "tok-1" || gotTokens[1] != "tok-2" || gotTokens[2] != "tok-2" {
		t.Fatalf("tokens = %v, want [tok-1 tok-2 tok-2]", gotTokens)
	}

	locks, err := h.store.ListXAccountLocks(ctx, h.clock.Now())
	if err != nil || len(locks) != 1 || locks[0].AccountID != acc1.ID || locks[0].Queue != "UserMedia" {
		t.Fatalf("locks = %+v, err = %v", locks, err)
	}

	diag := src.Diagnostics()
	if len(diag.Clients) != 2 {
		t.Fatalf("clients = %+v", diag.Clients)
	}
	if len(diag.Clients[0].RateLimits) != 1 || diag.Clients[0].RateLimits[0].Limit != 50 || diag.Clients[0].RateLimits[0].Remaining != 0 || !diag.Clients[0].RateLimits[0].Blocked {
		t.Fatalf("primary rate limits = %+v", diag.Clients[0].RateLimits)
	}
	if len(diag.Clients[1].RateLimits) != 1 || diag.Clients[1].RateLimits[0].Limit != 50 || diag.Clients[1].RateLimits[0].Remaining != 42 || diag.Clients[1].RateLimits[0].Blocked {
		t.Fatalf("secondary rate limits = %+v", diag.Clients[1].RateLimits)
	}
}

func TestPrimaryQuotaReservePrefersSecondaryWhenPrimaryIsLow(t *testing.T) {
	ctx := context.Background()
	var tokens []string
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		tok := requestAuthToken(r)
		tokens = append(tokens, tok)
		reset := h.clock.Now().Add(10 * time.Minute)
		w.Header().Set("X-Rate-Limit-Limit", "50")
		w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(reset.Unix(), 10))
		if tok == "tok-primary" {
			// 5 <= reserve (50/5=10), but > 1 so not proactively locked yet.
			w.Header().Set("X-Rate-Limit-Remaining", "5")
		} else {
			w.Header().Set("X-Rate-Limit-Remaining", "45")
		}
		writeJSON(w, http.StatusOK, `{"data":{"ok":true}}`)
	})
	h.account("tok-primary", "ct-1", "")
	h.account("tok-secondary", "ct-2", "")

	src := h.source()
	// Req 1: primary (LRU), reports low remaining=5.
	// Req 2: secondary (LRU), reports remaining=45.
	// Req 3: normally LRU would pick primary again, but primary quota reserve keeps routing to secondary!
	for i := 0; i < 3; i++ {
		h.clock.Advance(time.Second)
		if _, err := src.GraphQL(ctx, xapi.UserMediaPath, url.Values{}); err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
	}
	if len(tokens) != 3 || tokens[0] != "tok-primary" || tokens[1] != "tok-secondary" || tokens[2] != "tok-secondary" {
		t.Fatalf("tokens = %v, want [tok-primary tok-secondary tok-secondary]", tokens)
	}
}

func TestTarget401CapsFailoverAcrossLargeAccountPool(t *testing.T) {
	var graphqlHits atomic.Int32
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probePath {
			writeJSON(w, http.StatusOK, `{"users":[{"user_id":"1","screen_name":"valid","is_auth_valid":true}]}`)
			return
		}
		graphqlHits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("target unauthorized"))
	})
	for i := 1; i <= 5; i++ {
		h.account("tok-"+strconv.Itoa(i), "ct-"+strconv.Itoa(i), "")
	}

	_, err := h.source().GraphQL(context.Background(), xapi.UserMediaPath, url.Values{})
	if !xapi.IsUnauthorized(err) {
		t.Fatalf("err = %v, want HTTP 401", err)
	}
	if got := graphqlHits.Load(); got != 2 {
		t.Fatalf("target 401 across 5 valid accounts made %d requests, want capped at 2", got)
	}
}

func TestRecheckDisabledClientsSkipsManuallyDisabledAndActivePool(t *testing.T) {
	ctx := context.Background()
	var probeHits atomic.Int32
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probePath {
			probeHits.Add(1)
			writeJSON(w, http.StatusUnauthorized, `{"errors":[{"code":32}]}`)
			return
		}
		emptyGraphQL(h, w, r)
	})
	activeAcc := h.account("tok-active", "ct-1", "")
	manualOff := h.account("tok-manual", "ct-2", "")
	sysOff := h.account("tok-sys", "ct-3", "")

	inactive := false
	if _, err := h.store.UpdateXAccount(ctx, manualOff.ID, storage.XAccountPatch{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeactivateXAccount(ctx, sysOff.ID, "expired"); err != nil {
		t.Fatal(err)
	}

	// While an active account exists, RecheckDisabledClients must return true immediately without probing.
	if !h.source().RecheckDisabledClients(ctx) {
		t.Fatal("expected true when active account exists")
	}
	if probeHits.Load() != 0 {
		t.Fatalf("expected 0 probe requests while pool has active account, got %d", probeHits.Load())
	}

	// Deactivate the remaining active account: now RecheckDisabledClients probes ONLY system-disabled accounts,
	// never the manually disabled account, and obeys cooldown on immediate repeat calls.
	if err := h.store.DeactivateXAccount(ctx, activeAcc.ID, "expired"); err != nil {
		t.Fatal(err)
	}
	if h.source().RecheckDisabledClients(ctx) {
		t.Fatal("expected false when all accounts fail probe")
	}
	if got := probeHits.Load(); got != 2 {
		t.Fatalf("probeHits = %d, want 2 (activeAcc + sysOff, skipping manualOff)", got)
	}
	// Immediate second call within cooldown must not re-hammer dead accounts.
	_ = h.source().RecheckDisabledClients(ctx)
	if got := probeHits.Load(); got != 2 {
		t.Fatalf("second RecheckDisabledClients within cooldown made extra probes: %d", got)
	}
}

func TestBadProxyDialTimeoutCooldownAndPoolExhaustedWithRateLimitedPeer(t *testing.T) {
	prevDial := proxyDialTimeout
	prevLock := proxyDialLock
	proxyDialTimeout = 80 * time.Millisecond
	proxyDialLock = 2 * time.Minute
	t.Cleanup(func() {
		proxyDialTimeout = prevDial
		proxyDialLock = prevLock
	})

	var dialAttemptsA atomic.Int32
	badTransport := wrapDialTimeout(&http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialAttemptsA.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	resetB := now.Add(5 * time.Minute)
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(resetB.Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
	}))
	defer srvB.Close()

	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "dial-lock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accA, _ := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokA", CSRFToken: "ctA", Proxy: "http://127.0.0.1:19990", Active: true, IsPrimary: true})
	accB, _ := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "tokB", CSRFToken: "ctB", Proxy: "http://127.0.0.1:19991", Active: true})

	engine := NewEngine(store, Options{BaseURL: srvB.URL, DisablePacing: true, TransactionIDs: fixedTID{}})
	engine.now = func() time.Time { return now }
	engine.transports = map[string]*http.Transport{
		accA.Proxy: badTransport,
		accB.Proxy: srvB.Client().Transport.(*http.Transport).Clone(),
	}
	engine.transportOrder = []string{accA.Proxy, accB.Proxy}

	rawSrc, err := engine.Source(ctx, config.AppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = rawSrc.(*source).graphQLWithAccount(xapi.WithYieldToScheduler(ctx), xapi.UserMediaPath, url.Values{}, nil)
	if !xapi.IsAllClientsRateLimited(err) {
		t.Fatalf("expected PoolExhausted when accA has dial-timeout lock and accB is 429, got %v", err)
	}
	lockedA, err := engine.accountLocked(ctx, accA.ID, "*")
	if err != nil || !lockedA {
		t.Fatalf("expected accA to have wildcard dial-timeout cooldown lock, locked=%v err=%v", lockedA, err)
	}
	if dialAttemptsA.Load() != 1 {
		t.Fatalf("dialAttemptsA = %d, want 1", dialAttemptsA.Load())
	}
}

func TestListFetcherSwitchesAccountOnEmptyPrivateList(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if requestAuthToken(r) == "tok-1" {
			writeJSON(w, http.StatusOK, `{"data":{"list":{"id_str":"L9"}}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"list":{"id_str":"L9","name":"Secret List","member_count":3}}}`)
	})
	h.account("tok-1", "ct-1", "")
	h.account("tok-2", "ct-2", "")

	list, err := h.source().GetListByID(context.Background(), "L9")
	if err != nil {
		t.Fatalf("GetListByID should failover from empty list on tok-1 to tok-2: %v", err)
	}
	if list.Name != "Secret List" || list.ViewerKey != xapi.ViewerKey("tok-2") {
		t.Fatalf("list = %+v, want Secret List with tok-2 viewer", list)
	}

	// Single account returning empty list must return an explicit error instead of blank List.
	h2 := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"data":{"list":{"id_str":"L9"}}}`)
	})
	h2.account("tok-only", "ct-1", "")
	if _, err := h2.source().GetListByID(context.Background(), "L9"); err == nil {
		t.Fatal("expected error for empty inaccessible list payload")
	}
}

func TestAssetCacheDeduplicatesDiscoveryAndEmptyDiscoveryBacksOff(t *testing.T) {
	var homeHits atomic.Int32
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			homeHits.Add(1)
			writeJSON(w, http.StatusOK, `<html><script src="/i18n/zh.js"></script><script src="/app.js"></script></html>`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`console.log("no query ids here")`))
	})

	// Two consecutive discoverQueryIDs calls within assetCacheTTL hit "/" only once!
	if _, err := discoverQueryIDs(context.Background(), h.engine, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverQueryIDs(context.Background(), h.engine, ""); err != nil {
		t.Fatal(err)
	}
	if got := homeHits.Load(); got != 1 {
		t.Fatalf("homeHits = %d, want 1 due to shared assetCache", got)
	}

	// ensureFresh with 0 discovered IDs sets queryIDEmptyTTL (6h) backoff.
	table := &queryIDTable{}
	table.ensureFresh(context.Background(), h.engine, "")
	deadline := time.Now().Add(2 * time.Second)
	for {
		table.mu.Lock()
		done := !table.discovering && !table.nextRefreshAt.IsZero()
		nextAt := table.nextRefreshAt
		table.mu.Unlock()
		if done {
			if nextAt.Sub(h.clock.Now()) < queryIDEmptyTTL-time.Second {
				t.Fatalf("empty discovery nextRefreshAt = %v, want ~%v after now (%v)", nextAt, queryIDEmptyTTL, h.clock.Now())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ensureFresh did not finish in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGraphQL404SkipsIdenticalRetryWhenBothInvalidationAndDiscoveryThrottled(t *testing.T) {
	var hits atomic.Int32
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			writeJSON(w, http.StatusOK, "<html></html>")
			return
		}
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Not Found"}]}`))
	})
	h.account("tok-1", "ct-1", "")

	// Pre-trigger both throttles so neither xtid nor queryIDs refreshes on 404.
	h.engine.invalidateTransactionIDs()
	h.engine.queryIDs.mu.Lock()
	h.engine.queryIDs.last404At = h.clock.Now()
	h.engine.queryIDs.mu.Unlock()

	_, err := h.source().GraphQL(context.Background(), xapi.UserByRestIDPath, url.Values{})
	if !errors.Is(err, xapi.ErrQueryIDStale) {
		t.Fatalf("err = %v, want ErrQueryIDStale", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("graphql 404 hits = %d, want 1 (skip pointless identical retry when both throttled)", got)
	}
}

func TestMetaProxyFallbackUsesAccountProxyWhenGlobalEmpty(t *testing.T) {
	e := NewEngine(nil, Options{})
	if got := e.metaProxyFor("http://127.0.0.1:7890"); got != "http://127.0.0.1:7890" {
		t.Fatalf("metaProxyFor = %q, want account proxy fallback", got)
	}
	e.globalMu.Lock()
	e.globalProxy = "http://127.0.0.1:8888"
	e.globalMu.Unlock()
	if got := e.metaProxyFor("http://127.0.0.1:7890"); got != "http://127.0.0.1:8888" {
		t.Fatalf("metaProxyFor with global proxy = %q, want global proxy", got)
	}
}
