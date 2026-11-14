package xclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// recordedRequest 记录假 X 服务收到的一次请求。
type recordedRequest struct {
	path      string
	cookie    string
	userAgent string
	csrf      string
	variables string
}

// fakeXServer 记录请求并按路径返回固定响应。
type fakeXServer struct {
	mu       sync.Mutex
	requests []recordedRequest
	server   *httptest.Server
}

func newFakeXServer(t *testing.T) *fakeXServer {
	t.Helper()
	fake := &fakeXServer{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.requests = append(fake.requests, recordedRequest{
			path:      r.URL.Path,
			cookie:    r.Header.Get("Cookie"),
			userAgent: r.Header.Get("User-Agent"),
			csrf:      r.Header.Get("X-Csrf-Token"),
			variables: r.URL.Query().Get("variables"),
		})
		fake.mu.Unlock()
		switch {
		case r.URL.Path == "/home":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<script>{"screen_name":"owner"}</script>`))
		case strings.HasSuffix(r.URL.Path, "/TweetResultByRestId"):
			_, _ = w.Write([]byte(`{"data":{"tweet_result_by_rest_id":{"result":` + fullTweetFixture("123", "author") + `}}}`))
		case strings.HasSuffix(r.URL.Path, "/UserByRestId"), strings.HasSuffix(r.URL.Path, "/UserByScreenName"):
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"rest_id":"42","legacy":{"screen_name":"target","name":"Target"}}}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeXServer) snapshot() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func testAccountOptions(fake *fakeXServer) Options {
	return Options{BaseURL: fake.server.URL, Transport: fake.server.Client().Transport, DisablePacing: true}
}

func TestNewSourceFromAccountsPrimarySemantics(t *testing.T) {
	cfg := config.AppConfig{}
	source, err := NewSourceFromAccounts(cfg, []Account{
		{AuthToken: "primary-" + t.Name(), CSRFToken: "c1", Primary: true},
		{AuthToken: "backup-" + t.Name(), CSRFToken: "c2"},
		{AuthToken: "primary-" + t.Name(), CSRFToken: "dup"}, // 重复 auth_token 只保留第一条
		{AuthToken: "incomplete-" + t.Name()},                // 缺 ct0 跳过
	}, Options{DisablePacing: true})
	if err != nil {
		t.Fatal(err)
	}
	pool, ok := source.(*Pool)
	if !ok {
		t.Fatalf("source = %T, want *Pool", source)
	}
	if len(pool.clients) != 2 || !pool.HasPrimary() || pool.Primary() != pool.clients[0] {
		t.Fatalf("pool clients=%d hasPrimary=%v", len(pool.clients), pool.HasPrimary())
	}
	diagnostics := pool.Diagnostics()
	if !diagnostics.Clients[0].Primary || diagnostics.Clients[1].Primary {
		t.Fatalf("diagnostics primary flags = %+v", diagnostics.Clients)
	}

	// 第一位账号没有 Primary 标记（主账号停用或不存在）：池里没有主账号，备用账号不顶替。
	source, err = NewSourceFromAccounts(cfg, []Account{
		{AuthToken: "backup-a-" + t.Name(), CSRFToken: "c1"},
		{AuthToken: "backup-b-" + t.Name(), CSRFToken: "c2", Primary: true},
	}, Options{DisablePacing: true})
	if err != nil {
		t.Fatal(err)
	}
	pool = source.(*Pool)
	if pool.HasPrimary() || pool.Primary() != nil {
		t.Fatal("pool without a leading primary account reported a primary")
	}
	for _, client := range pool.Diagnostics().Clients {
		if client.Primary {
			t.Fatalf("diagnostics marked a backup as primary: %+v", client)
		}
	}
	ctx := context.Background()
	if _, err := pool.GetUserByIDAsPrimary(ctx, "42"); err == nil {
		t.Fatal("GetUserByIDAsPrimary succeeded without a primary account")
	}
	if err := pool.FollowUserAsPrimary(ctx, User{ID: "42"}); err == nil {
		t.Fatal("FollowUserAsPrimary succeeded without a primary account")
	}
	if _, err := pool.GetFollowingPage(ctx, User{ID: "42", Protected: true}, "", ""); err == nil {
		t.Fatal("protected following page succeeded without a primary account")
	}
	protected := User{ID: "42", Protected: true, Following: true}
	if _, err := pool.WalkUserMedia(ctx, protected, UserMediaTimelineOptions{}, func(UserMediaTimelinePage) error { return nil }); err == nil {
		t.Fatal("protected media timeline succeeded without a primary account")
	}
	if err := pool.WalkUserTweets(ctx, protected, TweetTimelineOptions{}, func(TweetTimelinePage) error { return nil }); err == nil {
		t.Fatal("protected tweet timeline succeeded without a primary account")
	}
}

// 账号级 UA 与额外 Cookie 对所有请求生效，包括 /home 自检；额外 Cookie 按名字排序，
// 排在 auth_token/ct0 之后，非法名字被丢弃。
func TestAccountSettingsApplyToEveryRequest(t *testing.T) {
	fake := newFakeXServer(t)
	authToken := "auth-" + t.Name()
	source, err := NewSourceFromAccounts(config.AppConfig{}, []Account{{
		AuthToken:    authToken,
		CSRFToken:    "csrf-value",
		ExtraCookies: map[string]string{"twid": "u%3D1", "guest_id": "g1", "bad name": "x", "ct0": "shadow", "lang": "a;b"},
		UserAgent:    "custom-agent/1.0",
		Primary:      true,
	}}, testAccountOptions(fake))
	if err != nil {
		t.Fatal(err)
	}
	pool := source.(*Pool)
	ctx := context.Background()
	if _, err := pool.GetUserByInput(ctx, "42"); err != nil {
		t.Fatalf("GetUserByInput: %v", err)
	}
	if _, err := pool.FetchTweet(ctx, "123", parser.ParseOptions{}); err != nil {
		t.Fatalf("FetchTweet: %v", err)
	}
	if screen, err := pool.Primary().GetSelfScreenName(ctx); err != nil || screen != "owner" {
		t.Fatalf("GetSelfScreenName = %q, %v", screen, err)
	}
	requests := fake.snapshot()
	if len(requests) != 3 {
		t.Fatalf("requests = %+v, want 3", requests)
	}
	wantCookie := "auth_token=" + authToken + "; ct0=csrf-value; guest_id=g1; twid=u%3D1"
	for _, request := range requests {
		if request.cookie != wantCookie {
			t.Fatalf("%s cookie = %q, want %q", request.path, request.cookie, wantCookie)
		}
		if request.userAgent != "custom-agent/1.0" {
			t.Fatalf("%s user-agent = %q", request.path, request.userAgent)
		}
	}
	if requests[2].path != "/home" {
		t.Fatalf("last request path = %q, want /home", requests[2].path)
	}

	// 没有账号级 UA 时用默认 UA。
	defaults, err := NewSourceFromAccounts(config.AppConfig{}, []Account{{AuthToken: "default-" + t.Name(), CSRFToken: "c", Primary: true}}, testAccountOptions(fake))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaults.(*Pool).Primary().GetSelfScreenName(ctx); err != nil {
		t.Fatal(err)
	}
	requests = fake.snapshot()
	if last := requests[len(requests)-1]; last.userAgent != userAgent || last.cookie != "auth_token=default-"+t.Name()+"; ct0=c" {
		t.Fatalf("default request = %+v", last)
	}
}

// 账号代理优先，未设置时用全局代理；两者都经过同一套 Transport 校验。
func TestAccountProxyFallsBackToGlobalProxy(t *testing.T) {
	cfg := config.AppConfig{ProxyURL: "http://127.0.0.1:7890"}
	source, err := NewSourceFromAccounts(cfg, []Account{
		{AuthToken: "own-proxy-" + t.Name(), CSRFToken: "c", Primary: true, Proxy: "socks5://127.0.0.1:1080"},
		{AuthToken: "global-proxy-" + t.Name(), CSRFToken: "c"},
	}, Options{DisablePacing: true})
	if err != nil {
		t.Fatal(err)
	}
	pool := source.(*Pool)
	request := httptest.NewRequest(http.MethodGet, "https://x.com/home", nil)
	for index, want := range []string{"socks5://127.0.0.1:1080", "http://127.0.0.1:7890"} {
		transport, ok := pool.clients[index].http.Transport.(*http.Transport)
		if !ok || transport.Proxy == nil {
			t.Fatalf("client %d transport = %T", index, pool.clients[index].http.Transport)
		}
		proxyURL, err := transport.Proxy(request)
		if err != nil || proxyURL == nil || proxyURL.String() != want {
			t.Fatalf("client %d proxy = %v, %v, want %s", index, proxyURL, err, want)
		}
	}

	if _, err := NewSourceFromAccounts(cfg, []Account{{AuthToken: "bad-" + t.Name(), CSRFToken: "c", Proxy: "ftp://proxy"}}, Options{}); err == nil {
		t.Fatal("invalid account proxy was accepted")
	}
}

// 没有账号时退回 guest source：未登录请求单条推文，不带任何 Cookie，也不读取 cfg 里
// 已废弃的 Cookie 字段；需要账号池的方法返回错误。
func TestNewSourceFromAccountsGuestFallbackIgnoresConfigCookies(t *testing.T) {
	fake := newFakeXServer(t)
	cfg := config.AppConfig{AuthToken: "deprecated-auth", CSRFToken: "deprecated-ct0", AdditionalCookies: "auth_token=a; ct0=b"}
	source, err := NewSourceFromAccounts(cfg, nil, testAccountOptions(fake))
	if err != nil {
		t.Fatal(err)
	}
	if source.HasPrimary() {
		t.Fatal("guest source reported a primary account")
	}
	ctx := context.Background()
	tweet, err := source.FetchTweet(ctx, "123", parser.ParseOptions{})
	if err != nil || tweet.ID != "123" {
		t.Fatalf("FetchTweet = %+v, %v", tweet, err)
	}
	requests := fake.snapshot()
	if len(requests) != 1 {
		t.Fatalf("requests = %+v", requests)
	}
	if requests[0].cookie != "" || requests[0].csrf != "" {
		t.Fatalf("guest request carried credentials: cookie=%q csrf=%q", requests[0].cookie, requests[0].csrf)
	}
	if !strings.Contains(requests[0].variables, `"__relay_internal__pv__xclientsfeaturesappviewerisloggedinrelayprovider":false`) {
		t.Fatalf("guest request variables = %s, want loggedIn=false", requests[0].variables)
	}
	if _, err := source.GetUserByInput(ctx, "someone"); !errors.Is(err, errNoAccounts) {
		t.Fatalf("GetUserByInput error = %v, want errNoAccounts", err)
	}

	// 只有不完整的账号同样退回 guest。
	if fallback, err := NewSourceFromAccounts(cfg, []Account{{AuthToken: "only-auth"}}, Options{DisablePacing: true}); err != nil {
		t.Fatal(err)
	} else if _, isGuest := fallback.(*guestSource); !isGuest {
		t.Fatalf("source = %T, want guest fallback", fallback)
	}
}

// DisablePacing 去掉每账号 3 秒间隔与重试退避，但保留按接口的限流器。
func TestDisablePacingKeepsRateLimiter(t *testing.T) {
	fake := newFakeXServer(t)
	source, err := NewSourceFromAccounts(config.AppConfig{}, []Account{{AuthToken: "pacing-" + t.Name(), CSRFToken: "c", Primary: true}}, testAccountOptions(fake))
	if err != nil {
		t.Fatal(err)
	}
	client := source.(*Pool).Primary()
	if client.requests != nil || client.limiter == nil {
		t.Fatalf("DisablePacing client requests=%v limiter=%v", client.requests, client.limiter)
	}
	if delay := client.requestRetryDelay(2); delay != 0 {
		t.Fatalf("retry delay = %s, want 0", delay)
	}
	ctx := context.Background()
	start := time.Now()
	for range 3 {
		if _, err := client.graphQL(ctx, "/i/api/graphql/x/UserMedia", nil); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("three requests took %s; per-account pacing still active", elapsed)
	}
	client.limiter.markRateLimitedFor("/i/api/graphql/x/UserMedia", time.Minute)
	if _, err := client.graphQL(withYieldOnRateLimit(ctx), "/i/api/graphql/x/UserMedia", nil); !xapi.IsRateLimitError(err) {
		t.Fatalf("rate-limited request error = %v, want rate limit", err)
	}

	// 生产配置下客户端占用每账号执行槽，限流状态按 auth_token 共享。
	production, err := NewSourceFromAccounts(config.AppConfig{}, []Account{{AuthToken: "pacing-prod-" + t.Name(), CSRFToken: "c", Primary: true}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if production.(*Pool).Primary().requests == nil {
		t.Fatal("production client lost per-account pacing")
	}
}
