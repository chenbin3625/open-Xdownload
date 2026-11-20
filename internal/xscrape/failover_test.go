package xscrape

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

type fixedTID struct{}

func (fixedTID) Generate(context.Context, string, string) (string, error) { return "tid", nil }
func (fixedTID) Invalidate()                                              {}

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
