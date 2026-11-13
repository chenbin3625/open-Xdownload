package xtid

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testUserAgent = "xtid-test-agent/1.0"

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = ""
	return t.base.RoundTrip(clone)
}

type fixtureServer struct {
	*httptest.Server
	mu        sync.Mutex
	counts    map[string]int
	userAgent map[string][]string
	routes    map[string][]byte
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{
		counts:    make(map[string]int),
		userAgent: make(map[string][]string),
		routes:    make(map[string][]byte),
	}
	fs.routes["/"] = loadFixture(t, "home.html")
	fs.routes["/x-web/x-web/entry-client-logged-out-9yhzkDVy.js"] = loadFixture(t, "entry-client-logged-out-9yhzkDVy.js")
	fs.routes["/x-web/x-web/assets/D4OZfXWC.js"] = loadFixture(t, "D4OZfXWC.js.gz")
	fs.routes["/x-web/x-web/assets/DIQrwHty.js"] = loadFixture(t, "DIQrwHty.js")
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.counts[r.URL.Path]++
		fs.userAgent[r.URL.Path] = append(fs.userAgent[r.URL.Path], r.Header.Get("User-Agent"))
		body, ok := fs.routes[r.URL.Path]
		fs.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fixtureServer) client() *http.Client {
	target, err := url.Parse(fs.URL)
	if err != nil {
		panic(err)
	}
	return &http.Client{Transport: rewriteTransport{target: target, base: http.DefaultTransport}}
}

func (fs *fixtureServer) count(path string) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.counts[path]
}

func (fs *fixtureServer) setRoute(path string, body []byte) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.routes[path] = body
}

func (fs *fixtureServer) deleteRoute(path string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	delete(fs.routes, path)
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("读取 fixture %s: %v", name, err)
	}
	if !strings.HasSuffix(name, ".gz") {
		return raw
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解压 fixture %s: %v", name, err)
	}
	defer zr.Close()
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("读取 gzip fixture %s: %v", name, err)
	}
	return body
}

type goldenFile struct {
	Reference       string `json:"reference"`
	Timestamp       int64  `json:"timestamp"`
	RandomByte      byte   `json:"randomByte"`
	Keyword         string `json:"keyword"`
	VerificationKey string `json:"verificationKey"`
	AnimationKey    string `json:"animationKey"`
	Cases           []struct {
		Method        string `json:"method"`
		Path          string `json:"path"`
		TransactionID string `json:"transactionId"`
	} `json:"cases"`
}

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatalf("读取 golden.json: %v", err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("解析 golden.json: %v", err)
	}
	return g
}

func testOptions(timestamp int64, randomByte byte) options {
	return options{
		now:      func() time.Time { return time.Unix(timestamp, 0) },
		randByte: func() byte { return randomByte },
	}
}

func TestGolden(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))

	ctx := context.Background()
	k, err := gen.keys(ctx)
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if got := base64.StdEncoding.EncodeToString(k.verification); got != golden.VerificationKey {
		t.Fatalf("verification key = %q, want %q", got, golden.VerificationKey)
	}
	if k.animationKey != golden.AnimationKey {
		t.Fatalf("animation key = %q, want %q", k.animationKey, golden.AnimationKey)
	}

	for _, tc := range golden.Cases {
		t.Run(tc.Method+" "+tc.Path, func(t *testing.T) {
			got, err := gen.Generate(ctx, tc.Method, tc.Path)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got != tc.TransactionID {
				t.Fatalf("transaction id 不一致\n got: %s\nwant: %s", got, tc.TransactionID)
			}
		})
	}

	// method 大小写与参考实现一致：内部会转成大写。
	first := golden.Cases[0]
	got, err := gen.Generate(ctx, strings.ToLower(first.Method), first.Path)
	if err != nil {
		t.Fatalf("小写 method Generate: %v", err)
	}
	if got != first.TransactionID {
		t.Fatalf("小写 method 结果不一致\n got: %s\nwant: %s", got, first.TransactionID)
	}
}

func TestCachingTTLAndInvalidate(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)

	var mu sync.Mutex
	now := time.Unix(golden.Timestamp, 0)
	opts := options{
		now:      func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		randByte: func() byte { return golden.RandomByte },
	}
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, opts)
	ctx := context.Background()
	path := golden.Cases[0].Path

	if _, err := gen.Generate(ctx, "GET", path); err != nil {
		t.Fatalf("首次 Generate: %v", err)
	}
	if got := fs.count("/"); got != 1 {
		t.Fatalf("首次后首页请求数 = %d, want 1", got)
	}

	// 缓存命中。
	if _, err := gen.Generate(ctx, "GET", path); err != nil {
		t.Fatalf("第二次 Generate: %v", err)
	}
	if got := fs.count("/"); got != 1 {
		t.Fatalf("缓存命中时首页请求数 = %d, want 1", got)
	}

	// 59 分钟内仍然命中。
	mu.Lock()
	now = now.Add(59 * time.Minute)
	mu.Unlock()
	if _, err := gen.Generate(ctx, "GET", path); err != nil {
		t.Fatalf("TTL 内 Generate: %v", err)
	}
	if got := fs.count("/"); got != 1 {
		t.Fatalf("TTL 内首页请求数 = %d, want 1", got)
	}

	// 超过 1 小时后重新抓取。
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	if _, err := gen.Generate(ctx, "GET", path); err != nil {
		t.Fatalf("TTL 过期 Generate: %v", err)
	}
	if got := fs.count("/"); got != 2 {
		t.Fatalf("TTL 过期后首页请求数 = %d, want 2", got)
	}

	// Invalidate 后立即重新抓取。
	gen.Invalidate()
	if _, err := gen.Generate(ctx, "GET", path); err != nil {
		t.Fatalf("Invalidate 后 Generate: %v", err)
	}
	if got := fs.count("/"); got != 3 {
		t.Fatalf("Invalidate 后首页请求数 = %d, want 3", got)
	}
}

func TestConcurrentGenerateFetchesOnce(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))

	const n = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	var failed atomic.Int32
	results := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got, err := gen.Generate(context.Background(), "GET", golden.Cases[0].Path)
			if err != nil {
				failed.Add(1)
				return
			}
			results[i] = got
		}(i)
	}
	close(start)
	wg.Wait()

	if failed.Load() != 0 {
		t.Fatalf("有 %d 个并发调用失败", failed.Load())
	}
	if got := fs.count("/"); got != 1 {
		t.Fatalf("并发初始化应该只抓取一次首页，实际 %d 次", got)
	}
	for i, got := range results {
		if got != golden.Cases[0].TransactionID {
			t.Fatalf("第 %d 个并发结果不一致: %s", i, got)
		}
	}
}

func TestUserAgentOnAllRequests(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))
	if _, err := gen.Generate(context.Background(), "GET", golden.Cases[0].Path); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.userAgent) < 2 {
		t.Fatalf("请求数太少，无法验证 UA: %d", len(fs.userAgent))
	}
	for path, uas := range fs.userAgent {
		for _, ua := range uas {
			if ua != testUserAgent {
				t.Fatalf("%s 的 User-Agent = %q, want %q", path, ua, testUserAgent)
			}
		}
	}
}

func TestMissingVerificationKey(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)
	home := loadFixture(t, "home.html")
	broken := []byte(strings.Replace(string(home),
		`<meta name="twitter-site-verification" content="`+golden.VerificationKey+`"/>`, "", 1))
	if string(broken) == string(home) {
		t.Fatal("未能从 fixture 中移除 twitter-site-verification")
	}
	fs.setRoute("/", broken)

	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))
	_, err := gen.Generate(context.Background(), "GET", golden.Cases[0].Path)
	if err == nil || !strings.Contains(err.Error(), "twitter-site-verification") {
		t.Fatalf("错误 = %v, 期望包含 twitter-site-verification", err)
	}

	// 失败结果不缓存，下次调用应重新抓取。
	_, _ = gen.Generate(context.Background(), "GET", golden.Cases[0].Path)
	if got := fs.count("/"); got != 2 {
		t.Fatalf("失败后首页请求数 = %d, want 2", got)
	}
}

func TestMissingAnimationData(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)
	page := fmt.Sprintf(`<!DOCTYPE html><html><head><meta name="twitter-site-verification" content="%s"/></head><body></body></html>`, golden.VerificationKey)
	fs.setRoute("/", []byte(page))
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))
	_, err := gen.Generate(context.Background(), "GET", golden.Cases[0].Path)
	if err == nil || !strings.Contains(err.Error(), "loading-x-anim") {
		t.Fatalf("错误 = %v, 期望包含 loading-x-anim", err)
	}
}

func TestMissingSigningScript(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)
	// 首页引用的脚本全部 404，且没有任何 chunk 映射。
	for _, p := range []string{
		"/x-web/x-web/entry-client-logged-out-9yhzkDVy.js",
		"/x-web/x-web/assets/D4OZfXWC.js",
		"/x-web/x-web/assets/DIQrwHty.js",
	} {
		fs.deleteRoute(p)
	}
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))
	_, err := gen.Generate(context.Background(), "GET", golden.Cases[0].Path)
	if err == nil || !strings.Contains(err.Error(), "签名 JS") {
		t.Fatalf("错误 = %v, 期望包含 签名 JS", err)
	}
}

func TestExtractScriptURLsLegacyChunkMaps(t *testing.T) {
	// 同时覆盖 7 位（旧）与 16 位（新）chunk hash 映射。
	page := `<script>,3:"ondemand.s",4:"chunk-name",3:"abcdef1",4:"0123456789abcdef"</script>`
	urls := extractScriptURLs(page, "https://x.com")
	joined := strings.Join(urls, "\n")

	for _, want := range []string{
		"https://abs.twimg.com/responsive-web/client-web/ondemand.s.abcdef1a.js",
		"https://abs.twimg.com/responsive-web/client-web/chunk-name.0123456789abcdefa.js",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("脚本列表缺少 %q\n实际:\n%s", want, joined)
		}
	}
	// 7 位 hash 不能被误判成 chunk 名。
	if strings.Contains(joined, "abcdef1.0123456789abcdefa.js") {
		t.Fatalf("hash 映射解析错误:\n%s", joined)
	}
	if got := len(urls); got != 2 {
		t.Fatalf("脚本数 = %d, want 2:\n%s", got, joined)
	}

	var ondemand string
	for _, u := range urls {
		if matchesIndicesFile(u) {
			ondemand = u
		}
	}
	if ondemand == "" {
		t.Fatalf("没有识别出 ondemand.s:\n%s", joined)
	}
}

func TestLiveGenerate(t *testing.T) {
	if os.Getenv("XTID_LIVE") != "1" {
		t.Skip("设置 XTID_LIVE=1 才运行在线测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Minute}
	gen := New(client, "https://x.com", defaultUserAgent)
	tid, err := gen.Generate(ctx, "GET", "/i/api/graphql/abc123/UserByScreenName")
	if err != nil {
		t.Fatalf("在线 Generate: %v", err)
	}
	if len(tid) < 60 {
		t.Fatalf("transaction id 太短: %q", tid)
	}
	if strings.HasSuffix(tid, "=") {
		t.Fatalf("transaction id 不应包含 padding: %q", tid)
	}
}
