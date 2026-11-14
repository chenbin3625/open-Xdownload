package xscrape

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/tidwall/gjson"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeGenerator struct {
	mu          sync.Mutex
	generated   int
	invalidates int
}

func (g *fakeGenerator) Generate(context.Context, string, string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.generated++
	return "test-transaction-id", nil
}

func (g *fakeGenerator) Invalidate() {
	g.mu.Lock()
	g.invalidates++
	g.mu.Unlock()
}

func (g *fakeGenerator) counts() (generated, invalidates int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.generated, g.invalidates
}

type testHarness struct {
	t      *testing.T
	store  *storage.Store
	engine *Engine
	clock  *testClock
	gen    *fakeGenerator
	server *httptest.Server
}

func newHarness(t *testing.T, handler func(*testHarness, http.ResponseWriter, *http.Request)) *testHarness {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "xscrape.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	clock := &testClock{now: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	h := &testHarness{t: t, store: store, clock: clock, gen: &fakeGenerator{}}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(h, w, r)
	}))
	h.engine = NewEngine(store, Options{
		BaseURL:        h.server.URL,
		APIBaseURL:     h.server.URL,
		Transport:      &http.Transport{},
		TransactionIDs: h.gen,
		DisablePacing:  true,
	})
	h.engine.now = clock.Now
	h.engine.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	t.Cleanup(func() {
		h.server.Close()
		_ = store.Close()
	})
	return h
}

func (h *testHarness) source() *source { return &source{engine: h.engine} }

func (h *testHarness) account(authToken, ct0 string, extra string) storage.XAccount {
	h.t.Helper()
	account, err := h.store.CreateXAccount(context.Background(), storage.XAccount{AuthToken: authToken, CSRFToken: ct0, ExtraCookies: extra})
	if err != nil {
		h.t.Fatalf("create account %s: %v", authToken, err)
	}
	return account
}

func requestAuthToken(r *http.Request) string {
	cookie, err := r.Cookie("auth_token")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func emptyGraphQL(h *testHarness, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, `{"data":{}}`)
}

func TestGraphQLRotatesAccountsByLRUAndCountsStats(t *testing.T) {
	var mu sync.Mutex
	var tokens, transactionIDs []string
	var missingSessionHeaders []string
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/i/api/graphql/") {
			mu.Lock()
			tokens = append(tokens, requestAuthToken(r))
			transactionIDs = append(transactionIDs, r.Header.Get("x-client-transaction-id"))
			if r.Header.Get("X-Twitter-Auth-Type") != "OAuth2Session" || r.Header.Get("X-Twitter-Active-User") != "yes" || r.Header.Get("X-Twitter-Client-Language") != "en" {
				missingSessionHeaders = append(missingSessionHeaders, r.Header.Get("X-Twitter-Auth-Type")+"/"+r.Header.Get("X-Twitter-Active-User"))
			}
			mu.Unlock()
		}
		emptyGraphQL(h, w, r)
	})
	h.account("token-1", "ct0-1", "")
	h.account("token-2", "ct0-2", "")
	src := h.source()
	ctx := context.Background()

	if _, err := src.GraphQL(ctx, xapi.UserByRestIDPath, url.Values{"variables": {"{}"}}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Second)
	if _, err := src.GraphQL(ctx, xapi.UserByRestIDPath, url.Values{"variables": {"{}"}}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(tokens) != 2 || tokens[0] != "token-1" || tokens[1] != "token-2" {
		t.Fatalf("selected accounts = %v, want LRU order token-1, token-2", tokens)
	}
	for _, id := range transactionIDs {
		if id != "test-transaction-id" {
			t.Fatalf("x-client-transaction-id = %q", id)
		}
	}
	if len(missingSessionHeaders) != 0 {
		t.Fatalf("requests missing X session headers: %v", missingSessionHeaders)
	}
	stats, err := h.store.ListXAccountStats(ctx, h.clock.Now().UTC().Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("stats = %+v, want one row per account", stats)
	}
	for _, stat := range stats {
		if stat.Queue != "UserByRestId" || stat.Requests != 1 {
			t.Fatalf("stat = %+v", stat)
		}
	}
	if generated, _ := h.gen.counts(); generated != 2 {
		t.Fatalf("transaction id generated %d times, want 2", generated)
	}
}

func TestGraphQLRateLimitLocksQueueAndRotates(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	limited := false
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/i/api/graphql/") {
			emptyGraphQL(h, w, r)
			return
		}
		token := requestAuthToken(r)
		mu.Lock()
		tokens = append(tokens, token)
		shouldLimit := token == "token-1" && !limited
		if shouldLimit {
			limited = true
		}
		mu.Unlock()
		if shouldLimit {
			w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(h.clock.Now().Add(time.Minute).Unix(), 10))
			writeJSON(w, http.StatusTooManyRequests, `{"errors":[{"code":88}]}`)
			return
		}
		emptyGraphQL(h, w, r)
	})
	first := h.account("token-1", "ct0-1", "")
	h.account("token-2", "ct0-2", "")
	src := h.source()
	ctx := context.Background()

	if _, err := src.GraphQL(ctx, xapi.UserByRestIDPath, url.Values{}); err != nil {
		t.Fatalf("request should fail over to the second account: %v", err)
	}
	locks, err := h.store.ListXAccountLocks(ctx, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 1 || locks[0].AccountID != first.ID || locks[0].Queue != "UserByRestId" {
		t.Fatalf("locks = %+v, want queue lock on first account", locks)
	}
	if !locks[0].Until.After(h.clock.Now()) {
		t.Fatalf("lock until = %v, want in the future", locks[0].Until)
	}

	h.clock.Advance(61 * time.Second)
	if _, err := src.GraphQL(ctx, xapi.UserByRestIDPath, url.Values{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tokens) != 3 || tokens[0] != "token-1" || tokens[1] != "token-2" || tokens[2] != "token-1" {
		t.Fatalf("tokens = %v, want token-1, token-2, then token-1 after reset", tokens)
	}
}

func TestGraphQLAllLockedYieldsImmediatelyForScheduler(t *testing.T) {
	h := newHarness(t, emptyGraphQL)
	account := h.account("token-1", "ct0-1", "")
	reset := time.Unix(h.clock.Now().Add(time.Minute).Unix(), 0).UTC()
	if err := h.store.LockXAccount(context.Background(), account.ID, "UserByRestId", reset, "test"); err != nil {
		t.Fatal(err)
	}
	ctx := xapi.WithYieldToScheduler(context.Background())
	started := time.Now()
	_, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{})
	if !xapi.IsAllClientsRateLimited(err) {
		t.Fatalf("error = %v, want all clients rate limited", err)
	}
	retryAt, ok := xapi.RateLimitRetryAt(err)
	if !ok || !retryAt.Equal(reset) {
		t.Fatalf("retry at = %v (%t), want %v", retryAt, ok, reset)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("scheduler context waited %v instead of yielding", elapsed)
	}
}

func TestGraphQLUnauthorizedConfirmedInvalidDeactivates(t *testing.T) {
	var probes int
	var mu sync.Mutex
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probePath {
			mu.Lock()
			probes++
			mu.Unlock()
			writeJSON(w, http.StatusUnauthorized, `{"errors":[{"code":32,"message":"Could not authenticate you"}]}`)
			return
		}
		writeJSON(w, http.StatusUnauthorized, `{"errors":[{"code":32}]}`)
	})
	account := h.account("token-1", "ct0-1", "")
	_, err := h.source().GraphQL(context.Background(), xapi.UserByRestIDPath, url.Values{})
	if !xapi.IsUnauthorized(err) {
		t.Fatalf("error = %v, want IsUnauthorized", err)
	}
	current, err := h.store.GetXAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Active {
		t.Fatal("confirmed invalid account should be deactivated")
	}
	if !strings.Contains(current.ErrorMsg, "32") {
		t.Fatalf("error message = %q, want code 32 reason", current.ErrorMsg)
	}
	mu.Lock()
	defer mu.Unlock()
	if probes != 1 {
		t.Fatalf("probe requests = %d, want 1", probes)
	}
}

func TestGraphQLUnauthorizedWithValidProbeKeepsAccountAndCaches(t *testing.T) {
	var probes int
	var mu sync.Mutex
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probePath {
			mu.Lock()
			probes++
			mu.Unlock()
			writeJSON(w, http.StatusOK, `{"users":[{"user_id":"u1","screen_name":"me","is_auth_valid":true,"is_suspended":false}]}`)
			return
		}
		writeJSON(w, http.StatusUnauthorized, `{"errors":[{"code":32}]}`)
	})
	account := h.account("token-1", "ct0-1", "")
	ctx := context.Background()
	if _, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{}); !xapi.IsUnauthorized(err) {
		t.Fatalf("first error = %v, want IsUnauthorized", err)
	}
	if _, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{}); !xapi.IsUnauthorized(err) {
		t.Fatalf("second error = %v, want IsUnauthorized", err)
	}
	current, err := h.store.GetXAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !current.Active {
		t.Fatal("valid probe must not deactivate the account")
	}
	mu.Lock()
	defer mu.Unlock()
	if probes != 1 {
		t.Fatalf("probe requests = %d, want 1 within the verification TTL", probes)
	}
}

func TestGraphQLUnauthorizedUnconfirmedLocksWildcard(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probePath {
			writeJSON(w, http.StatusInternalServerError, `{"error":"boom"}`)
			return
		}
		writeJSON(w, http.StatusUnauthorized, `{"errors":[{"code":32}]}`)
	})
	account := h.account("token-1", "ct0-1", "")
	ctx := context.Background()
	if _, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{}); !xapi.IsUnauthorized(err) {
		t.Fatalf("error = %v, want IsUnauthorized", err)
	}
	current, err := h.store.GetXAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !current.Active {
		t.Fatal("unconfirmed 401 must not deactivate the account")
	}
	locks, err := h.store.ListXAccountLocks(ctx, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 1 || locks[0].Queue != "*" || !locks[0].Until.Equal(h.clock.Now().Add(forbiddenLock)) {
		t.Fatalf("locks = %+v, want a 5 minute wildcard lock", locks)
	}
}

func TestGraphQLForbiddenWAFAndHardLock(t *testing.T) {
	t.Run("waf", func(t *testing.T) {
		h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<html><body>challenge</body></html>")
		})
		account := h.account("token-1", "ct0-1", "")
		ctx := context.Background()
		_, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{})
		if !xapi.IsWAFChallengeError(err) || !errors.Is(err, xapi.ErrWAFChallenge) {
			t.Fatalf("error = %v, want WAF challenge", err)
		}
		current, _ := h.store.GetXAccount(ctx, account.ID)
		if !current.Active {
			t.Fatal("WAF challenge must not deactivate the account")
		}
		locks, _ := h.store.ListXAccountLocks(ctx, h.clock.Now())
		if len(locks) != 1 || locks[0].Queue != "*" || !locks[0].Until.Equal(h.clock.Now().Add(wafLock)) {
			t.Fatalf("locks = %+v, want a short wildcard lock", locks)
		}
	})

	t.Run("hard", func(t *testing.T) {
		h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusForbidden, `{"errors":[{"message":"Forbidden"}]}`)
		})
		account := h.account("token-1", "ct0-1", "")
		ctx := context.Background()
		_, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{})
		var httpErr *xapi.HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusForbidden {
			t.Fatalf("error = %v, want HTTP 403", err)
		}
		current, _ := h.store.GetXAccount(ctx, account.ID)
		if !current.Active {
			t.Fatal("non-WAF 403 must not deactivate the account")
		}
		locks, _ := h.store.ListXAccountLocks(ctx, h.clock.Now())
		if len(locks) != 1 || locks[0].Queue != "*" || !locks[0].Until.Equal(h.clock.Now().Add(forbiddenLock)) {
			t.Fatalf("locks = %+v, want a 5 minute wildcard lock", locks)
		}
	})
}

func TestGraphQLFatalAPICodeDeactivates(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"errors":[{"code":64,"message":"Account suspended"}]}`)
	})
	account := h.account("token-1", "ct0-1", "")
	ctx := context.Background()
	_, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{})
	var apiErr *xapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 64 {
		t.Fatalf("error = %v, want API code 64", err)
	}
	current, _ := h.store.GetXAccount(ctx, account.ID)
	if current.Active || !strings.Contains(current.ErrorMsg, "封禁") {
		t.Fatalf("account = %+v, want deactivated with suspension reason", current)
	}
}

func TestGraphQLFatalAPICodeOnNon200Deactivates(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, `{"errors":[{"code":32,"message":"Could not authenticate you"}]}`)
	})
	account := h.account("token-1", "ct0-1", "")
	ctx := context.Background()
	_, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{})
	var apiErr *xapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 32 {
		t.Fatalf("error = %v, want API code 32", err)
	}
	current, _ := h.store.GetXAccount(ctx, account.ID)
	if current.Active {
		t.Fatal("fatal API code on a non-200 response should deactivate the account")
	}
}

func TestGraphQLAPI88LocksQueue(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(h.clock.Now().Add(2*time.Minute).Unix(), 10))
		writeJSON(w, http.StatusOK, `{"errors":[{"code":88}]}`)
	})
	account := h.account("token-1", "ct0-1", "")
	ctx := context.Background()
	_, err := h.source().GraphQL(ctx, xapi.UserByRestIDPath, url.Values{})
	if !xapi.IsRateLimitError(err) {
		t.Fatalf("error = %v, want rate limit", err)
	}
	locks, _ := h.store.ListXAccountLocks(ctx, h.clock.Now())
	if len(locks) != 1 || locks[0].Queue != "UserByRestId" || locks[0].AccountID != account.ID {
		t.Fatalf("locks = %+v", locks)
	}
}

func TestGraphQL404InvalidatesAndReturnsQueryIDStale(t *testing.T) {
	var graphqlRequests int
	var mu sync.Mutex
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			writeJSON(w, http.StatusOK, "<html></html>")
			return
		}
		mu.Lock()
		graphqlRequests++
		mu.Unlock()
		writeJSON(w, http.StatusNotFound, `{"errors":[{"message":"Not Found"}]}`)
	})
	h.account("token-1", "ct0-1", "")
	_, err := h.source().GraphQL(context.Background(), xapi.UserByRestIDPath, url.Values{})
	if !errors.Is(err, xapi.ErrQueryIDStale) {
		t.Fatalf("error = %v, want ErrQueryIDStale", err)
	}
	if _, invalidates := h.gen.counts(); invalidates == 0 {
		t.Fatal("xtid generator was not invalidated on 404")
	}
	mu.Lock()
	defer mu.Unlock()
	if graphqlRequests != 2 {
		t.Fatalf("graphql requests = %d, want one retry after rediscovery", graphqlRequests)
	}
}

func TestNextAccountWaitedThenDisabledDoesNotPanic(t *testing.T) {
	h := newHarness(t, emptyGraphQL)
	account := h.account("token-1", "ct0-1", "")
	ctx := context.Background()

	// 先占住执行槽，让 nextAccount 在 acquire 上等待；期间停用账号，验证它唤醒后
	// 不会对 nil excluded map 赋值，而是重新选择并返回"没有可用 X Cookie"。
	_, release, _, err := h.engine.slots.acquire(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, releaseUsed, err := h.engine.nextAccount(ctx, "UserMedia", nil)
		if releaseUsed != nil {
			releaseUsed()
		}
		result <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := h.store.GetXAccount(ctx, account.ID)
		if err == nil && current.LastUsed.Valid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nextAccount did not select the account before the deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
	inactive := false
	if _, err := h.store.UpdateXAccount(ctx, account.ID, storage.XAccountPatch{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	release()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "没有可用 X Cookie") {
			t.Fatalf("nextAccount error = %v, want no active account", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nextAccount blocked after the account was disabled")
	}
}

func TestGraphQLWithoutAccounts(t *testing.T) {
	h := newHarness(t, emptyGraphQL)
	_, err := h.source().GraphQL(context.Background(), xapi.UserByRestIDPath, url.Values{})
	if err == nil || !strings.Contains(err.Error(), "没有可用 X Cookie") {
		t.Fatalf("error = %v", err)
	}
}

func TestSourceReturnsSourceAndValidatesProxy(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, "<html></html>")
	})
	src, err := h.engine.Source(context.Background(), configForTest())
	if err != nil {
		t.Fatal(err)
	}
	if src == nil {
		t.Fatal("nil source")
	}
	if _, err := h.engine.Source(context.Background(), configWithProxy("ftp://example.com")); err == nil {
		t.Fatal("unsupported proxy scheme should be rejected")
	}
}

func TestFetchTweetUsesGuestWithoutAccounts(t *testing.T) {
	var activates, graphql int
	var guestTokenHeader, cookieHeader, variables string
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			writeJSON(w, http.StatusOK, "<html></html>")
		case r.URL.Path == "/1.1/guest/activate.json":
			activates++
			writeJSON(w, http.StatusOK, `{"guest_token":"guest-1"}`)
		case strings.HasSuffix(r.URL.Path, "/TweetResultByRestId"):
			graphql++
			guestTokenHeader = r.Header.Get("X-Guest-Token")
			cookieHeader = r.Header.Get("Cookie")
			variables = r.URL.Query().Get("variables")
			writeJSON(w, http.StatusOK, `{"data":{"tweet_result_by_rest_id":{"result":`+tweetFixture("123", "author")+`}}}`)
		default:
			writeJSON(w, http.StatusNotFound, `{}`)
		}
	})
	src := h.source()
	for index := 0; index < 2; index++ {
		tweet, err := src.FetchTweet(context.Background(), "123", parser.ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if tweet.ID != "123" {
			t.Fatalf("tweet id = %q", tweet.ID)
		}
	}
	if activates != 1 {
		t.Fatalf("guest activate requests = %d, want cached after the first", activates)
	}
	if graphql != 2 {
		t.Fatalf("graphql requests = %d", graphql)
	}
	if guestTokenHeader != "guest-1" {
		t.Fatalf("X-Guest-Token = %q", guestTokenHeader)
	}
	if cookieHeader != "" {
		t.Fatalf("guest request carried cookies: %q", cookieHeader)
	}
	loggedIn := gjson.Get(variables, "__relay_internal__pv__xclientsfeaturesappviewerisloggedinrelayprovider")
	if loggedIn.Bool() {
		t.Fatalf("guest tweet request used loggedIn=true: %s", variables)
	}
}

func TestFetchTweetUsesPoolAccountWhenPresent(t *testing.T) {
	var token, variables string
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/TweetResultByRestId") {
			token = requestAuthToken(r)
			variables = r.URL.Query().Get("variables")
			writeJSON(w, http.StatusOK, `{"data":{"tweet_result_by_rest_id":{"result":`+tweetFixture("456", "author")+`}}}`)
			return
		}
		writeJSON(w, http.StatusNotFound, `{}`)
	})
	h.account("token-1", "ct0-1", "")
	tweet, err := h.source().FetchTweet(context.Background(), "456", parser.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tweet.ID != "456" || token != "token-1" {
		t.Fatalf("tweet = %q token = %q", tweet.ID, token)
	}
	if !gjson.Get(variables, "__relay_internal__pv__xclientsfeaturesappviewerisloggedinrelayprovider").Bool() {
		t.Fatalf("pooled tweet request should use loggedIn=true: %s", variables)
	}
}

func TestLookupSwitchesAccountsOnAgeRestricted(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/UserByRestId") {
			emptyGraphQL(h, w, r)
			return
		}
		token := requestAuthToken(r)
		mu.Lock()
		tokens = append(tokens, token)
		mu.Unlock()
		if token == "token-1" {
			writeJSON(w, http.StatusOK, `{"data":{"user":{"result":{"__typename":"UserUnavailable","reason":"Suspended","message":"User is age-restricted"}}}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"user":{"result":{"rest_id":"123","legacy":{"screen_name":"alice","name":"Alice"}}}}}`)
	})
	h.account("token-1", "ct0-1", "")
	h.account("token-2", "ct0-2", "")
	user, err := h.source().GetUserByInput(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if user.ScreenName != "alice" {
		t.Fatalf("user = %+v", user)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tokens) != 2 || tokens[0] != "token-1" || tokens[1] != "token-2" {
		t.Fatalf("tokens = %v, want one retry on the other account", tokens)
	}
}

func TestListAndMemberPagesBindViewer(t *testing.T) {
	var memberToken string
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ListByRestId"):
			writeJSON(w, http.StatusOK, `{"data":{"list":{"id_str":"L1","name":"List","member_count":1,"user_results":{"result":{"rest_id":"u0","legacy":{"screen_name":"owner","name":"Owner"}}}}}}`)
		case strings.HasSuffix(r.URL.Path, "/ListMembers"):
			memberToken = requestAuthToken(r)
			writeJSON(w, http.StatusOK, `{"data":{"list":{"members_timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[{"content":{"entryType":"TimelineTimelineItem","itemContent":{"user_results":{"result":{"rest_id":"u1","legacy":{"screen_name":"member","name":"Member"}}}}}}]}]}}}}}`)
		default:
			emptyGraphQL(h, w, r)
		}
	})
	h.account("token-1", "ct0-1", "")
	src := h.source()
	list, err := src.GetListByID(context.Background(), "L1")
	if err != nil {
		t.Fatal(err)
	}
	wantViewer := xapi.ViewerKey("token-1")
	if list.ViewerKey != wantViewer {
		t.Fatalf("viewer key = %q, want %q", list.ViewerKey, wantViewer)
	}
	page, err := src.GetListMembersPage(context.Background(), list, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if page.ViewerKey != wantViewer || memberToken != "token-1" {
		t.Fatalf("page viewer = %q token = %q", page.ViewerKey, memberToken)
	}
	if len(page.Users) != 1 || page.Users[0].ScreenName != "member" {
		t.Fatalf("page users = %+v", page.Users)
	}
	if _, err := src.GetListMembersPage(context.Background(), list, "", "deadbeef"); err == nil || !strings.Contains(err.Error(), "已移除") {
		t.Fatalf("unknown viewer error = %v", err)
	}
}

func TestCheckAllAppliesProbeResultsAndDiagnostics(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != probePath {
			writeJSON(w, http.StatusOK, "<html></html>")
			return
		}
		if requestAuthToken(r) == "token-2" {
			writeJSON(w, http.StatusUnauthorized, `{"errors":[{"code":32}]}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"users":[{"user_id":"u1","screen_name":"alice","is_auth_valid":true}]}`)
	})
	first := h.account("token-1", "ct0-1", "")
	second := h.account("token-2", "ct0-2", "")
	diagnostics := h.source().CheckAll(context.Background())
	if diagnostics.Total != 2 || diagnostics.Available != 1 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	updated, _ := h.store.GetXAccount(context.Background(), first.ID)
	if !updated.Active || updated.ScreenName != "alice" || !updated.VerifiedAt.Valid {
		t.Fatalf("verified account = %+v", updated)
	}
	failed, _ := h.store.GetXAccount(context.Background(), second.ID)
	if failed.Active || !strings.Contains(failed.ErrorMsg, "32") {
		t.Fatalf("rejected account = %+v", failed)
	}
}

func TestDiagnosticsReportsLocksAndStats(t *testing.T) {
	h := newHarness(t, emptyGraphQL)
	account := h.account("token-1", "ct0-1", "")
	ctx := context.Background()
	if err := h.store.LockXAccount(ctx, account.ID, "UserMedia", h.clock.Now().Add(10*time.Minute), "test"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := h.store.IncrementXAccountStat(ctx, account.ID, "UserMedia", h.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	diagnostics := h.source().Diagnostics()
	if diagnostics.Total != 1 || diagnostics.Available != 1 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	status := diagnostics.Clients[0]
	if status.RequestCount != 2 || !status.Primary || !status.OK || len(status.RateLimits) != 1 || status.RateLimits[0].Path != "UserMedia" {
		t.Fatalf("status = %+v", status)
	}
	if !h.source().HasPrimary() {
		t.Fatal("HasPrimary should report the primary account")
	}
}

func TestPrimaryMethodsUsePrimaryAccount(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	var followForm url.Values
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokens = append(tokens, requestAuthToken(r))
		if strings.HasSuffix(r.URL.Path, "/friendships/create.json") {
			_ = r.ParseForm()
			followForm = r.PostForm
			mu.Unlock()
			writeJSON(w, http.StatusOK, `{}`)
			return
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, `{"data":{"user":{"result":{"rest_id":"u9","legacy":{"screen_name":"target","name":"Target"}}}}}`)
	})
	h.account("primary-token", "ct0-1", "")
	h.account("backup-token", "ct0-2", "")
	src := h.source()
	if _, err := src.GetUserByIDAsPrimary(context.Background(), "u9"); err != nil {
		t.Fatal(err)
	}
	if err := src.FollowUserAsPrimary(context.Background(), xapi.User{ID: "u9"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tokens) != 2 || tokens[0] != "primary-token" || tokens[1] != "primary-token" {
		t.Fatalf("tokens = %v, want primary account for both methods", tokens)
	}
	if followForm.Get("user_id") != "u9" {
		t.Fatalf("follow form = %v", followForm)
	}
}

func TestWalkUserMediaUsesPrimaryForProtectedFollowingUser(t *testing.T) {
	var token string
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		token = requestAuthToken(r)
		writeJSON(w, http.StatusOK, `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`)
	})
	h.account("primary-token", "ct0-1", "")
	h.account("backup-token", "ct0-2", "")
	user := xapi.User{ID: "42", ScreenName: "author", Protected: true, Following: true}
	if _, err := h.source().WalkUserMedia(context.Background(), user, xapi.UserMediaTimelineOptions{}, func(xapi.UserMediaTimelinePage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if token != "primary-token" {
		t.Fatalf("timeline token = %q, want primary", token)
	}
}

func tweetFixture(id, author string) string {
	return `{"__typename":"Tweet","rest_id":"` + id + `","legacy":{"full_text":"pure text","created_at":"Tue Nov 10 12:00:00 +0000 2026"},"core":{"user_results":{"result":{"rest_id":"` + author + `","core":{"screen_name":"author","name":"Author"}}}}}`
}

func configForTest() config.AppConfig { return config.AppConfig{} }

func configWithProxy(proxy string) config.AppConfig {
	return config.AppConfig{ProxyURL: proxy}
}
