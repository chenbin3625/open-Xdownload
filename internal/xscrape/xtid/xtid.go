package xtid

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL   = "https://x.com"
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	// 与参考实现保持一致。
	keyword                = "obfiowerehiring"
	additionalRandomNumber = byte(3)
	timestampEpoch         = int64(1682924400) // x-client-transaction-id 的时钟起点

	cacheTTL       = time.Hour
	failureBackoff = 30 * time.Second
	fetchTimeout   = 45 * time.Second

	// 初始化最多抓取的脚本数量，避免在异常页面里无限爬取。
	maxCrawlChunks = 24
	maxCrawlDepth  = 3
	// 单个响应体上限，正常脚本远小于此值。
	maxBodyBytes = 16 << 20
)

// Generator 生成 X 的 x-client-transaction-id 请求头。
//
// 初始化（抓首页、定位签名脚本、计算动画 key）的结果缓存 1 小时；
// 初始化失败时会进入短暂退避，避免每次请求同步阻塞等待超时。
// 并发调用 Generate 时只会有一个 goroutine 执行初始化。
type Generator struct {
	client    *http.Client
	baseURL   string
	userAgent string

	now      func() time.Time
	randByte func() byte

	mu        sync.Mutex
	cached    *keys
	fetchedAt time.Time
	failedAt  time.Time
	lastErr   error
	version   uint64
	init      *initCall
}

type keys struct {
	verification []byte
	animationKey string
}

type initCall struct {
	done chan struct{}
	keys *keys
	err  error
}

type options struct {
	now      func() time.Time
	randByte func() byte
}

// New 创建生成器。client 为 nil 时使用内置 http.Client。
// baseURL 为空时使用 https://x.com，userAgent 为空时使用内置的桌面 Chrome UA。
func New(client *http.Client, baseURL, userAgent string) *Generator {
	return newWithOptions(client, baseURL, userAgent, options{})
}

func newWithOptions(client *http.Client, baseURL, userAgent string, opts options) *Generator {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	randByte := opts.randByte
	if randByte == nil {
		randByte = defaultRandByte
	}
	g := &Generator{
		baseURL:   baseURL,
		userAgent: userAgent,
		now:       now,
		randByte:  randByte,
	}
	if client == nil {
		g.client = &http.Client{
			Timeout:       fetchTimeout,
			CheckRedirect: g.checkRedirect,
		}
	} else {
		copied := *client
		copied.CheckRedirect = g.checkRedirect
		g.client = &copied
	}
	return g
}

// Invalidate 清空缓存与失败退避，下次 Generate 会重新抓取并计算。
func (g *Generator) Invalidate() {
	g.mu.Lock()
	g.cached = nil
	g.fetchedAt = time.Time{}
	g.failedAt = time.Time{}
	g.lastErr = nil
	g.version++
	// 若已有抓取正在进行，让它自然结束，但结果不再写入缓存。
	g.init = nil
	g.mu.Unlock()
}

// Generate 为指定的 HTTP 方法和 URL path 生成 x-client-transaction-id。
// path 不包含 query，例如 /i/api/graphql/xxx/UserMedia。
func (g *Generator) Generate(ctx context.Context, method, path string) (string, error) {
	k, err := g.keys(ctx)
	if err != nil {
		return "", err
	}
	now := g.now()
	ts := int64(math.Floor((float64(now.UnixMilli()) - float64(timestampEpoch*1000)) / 1000.0))
	tsBytes := [4]byte{
		byte(ts),
		byte(ts >> 8),
		byte(ts >> 16),
		byte(ts >> 24),
	}

	payload := asciiUpper(method) + "!" + path + "!" + strconv.FormatInt(ts, 10) + keyword + k.animationKey
	sum := sha256.Sum256([]byte(payload))

	random := g.randByte()
	out := make([]byte, 0, 1+len(k.verification)+4+16+1)
	out = append(out, random)
	for _, b := range k.verification {
		out = append(out, b^random)
	}
	for _, b := range tsBytes {
		out = append(out, b^random)
	}
	for _, b := range sum[:16] {
		out = append(out, b^random)
	}
	out = append(out, additionalRandomNumber^random)

	return strings.TrimRight(base64.StdEncoding.EncodeToString(out), "="), nil
}

func (g *Generator) keys(ctx context.Context) (*keys, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	now := g.now()
	if g.cached != nil && now.Sub(g.fetchedAt) < cacheTTL {
		k := g.cached
		g.mu.Unlock()
		return k, nil
	}
	if g.lastErr != nil && !g.failedAt.IsZero() {
		if elapsed := now.Sub(g.failedAt); elapsed >= 0 && elapsed < failureBackoff {
			err := g.lastErr
			g.mu.Unlock()
			return nil, err
		}
	}
	if c := g.init; c != nil {
		g.mu.Unlock()
		select {
		case <-c.done:
			return c.keys, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &initCall{done: make(chan struct{})}
	g.init = call
	version := g.version
	g.mu.Unlock()

	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	go func() {
		defer cancel()
		var k *keys
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("初始化 x-client-transaction-id 异常: %v", r)
			}
			g.mu.Lock()
			if g.version == version {
				if err == nil {
					g.cached = k
					g.fetchedAt = g.now()
					g.failedAt = time.Time{}
					g.lastErr = nil
				} else {
					g.failedAt = g.now()
					g.lastErr = err
				}
			}
			if g.init == call {
				g.init = nil
			}
			call.keys, call.err = k, err
			close(call.done)
			g.mu.Unlock()
		}()
		k, err = g.fetch(fetchCtx)
	}()

	select {
	case <-call.done:
		return call.keys, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *Generator) fetch(ctx context.Context) (*keys, error) {
	pageURL := g.baseURL + "/"
	page, err := g.get(ctx, pageURL)
	if err != nil {
		return nil, fmt.Errorf("获取首页失败: %w", err)
	}
	verification, err := parseVerificationKey(page)
	if err != nil {
		return nil, err
	}
	animationPaths, err := parseAnimationPaths(page)
	if err != nil {
		return nil, err
	}
	signingJS, err := g.loadSigningJS(ctx, page)
	if err != nil {
		return nil, err
	}
	indices, err := parseKeyByteIndices(signingJS)
	if err != nil {
		return nil, err
	}
	animationKey, err := calcAnimationKey(animationPaths, indices, verification)
	if err != nil {
		return nil, err
	}
	return &keys{verification: verification, animationKey: animationKey}, nil
}

func (g *Generator) loadSigningJS(ctx context.Context, page []byte) ([]byte, error) {
	scripts := extractScriptURLs(string(page))
	if len(scripts) == 0 {
		return nil, fmt.Errorf("首页未找到任何 JS 脚本")
	}

	// 旧版页面直接引用 ondemand.s / sign.o，或可通过 chunk 映射重建出来；
	// 将其排在最前并统一经 crawlForIndices 去重和限流爬取。
	seeds := make([]string, 0, len(scripts))
	for _, u := range scripts {
		if matchesIndicesFile(u) {
			seeds = append(seeds, u)
		}
	}
	for _, u := range scripts {
		if !matchesIndicesFile(u) {
			seeds = append(seeds, u)
		}
	}
	return g.crawlForIndices(ctx, seeds)
}

func (g *Generator) crawlForIndices(ctx context.Context, seeds []string) ([]byte, error) {
	type item struct {
		url   string
		depth int
	}
	queue := make([]item, 0, len(seeds))
	for _, u := range seeds {
		queue = append(queue, item{url: u})
	}
	seen := make(map[string]bool)
	tried := 0
	var lastErr error

	for len(queue) > 0 && tried < maxCrawlChunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		it := queue[0]
		queue = queue[1:]
		if seen[it.url] {
			continue
		}
		seen[it.url] = true
		tried++

		body, err := g.get(ctx, it.url)
		if err != nil {
			lastErr = err
			continue
		}
		if matchesKeyByteIndices(body) {
			return body, nil
		}
		if it.depth >= maxCrawlDepth {
			continue
		}

		priority, normal := chunkReferences(body, it.url)
		if len(priority) > 0 {
			next := make([]item, 0, len(priority)+len(queue)+len(normal))
			for _, u := range priority {
				next = append(next, item{url: u, depth: it.depth + 1})
			}
			next = append(next, queue...)
			for _, u := range normal {
				next = append(next, item{url: u, depth: it.depth + 1})
			}
			queue = next
		} else {
			for _, u := range normal {
				queue = append(queue, item{url: u, depth: it.depth + 1})
			}
		}
	}

	msg := fmt.Sprintf("未能定位签名 JS（ondemand.s/sign.o，已尝试 %d 个脚本）", tried)
	if lastErr != nil {
		return nil, fmt.Errorf("%s: %w", msg, lastErr)
	}
	return nil, fmt.Errorf("%s", msg)
}

func (g *Generator) get(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !g.allowedURL(parsed) {
		return nil, fmt.Errorf("拒绝请求非 X 脚本主机: %s", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", g.userAgent)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("请求 %s 返回 HTTP %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 响应失败: %w", rawURL, err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("响应 %s 超过 %d 字节上限", rawURL, maxBodyBytes)
	}
	return body, nil
}

func (g *Generator) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("重定向次数过多")
	}
	if len(via) > 0 && strings.EqualFold(via[0].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("拒绝 HTTPS 降级重定向: %s", req.URL)
	}
	if !g.allowedURL(req.URL) {
		return fmt.Errorf("拒绝重定向到非 X 脚本主机: %s", req.URL)
	}
	return nil
}

func (g *Generator) allowedURL(parsed *url.URL) bool {
	if parsed == nil || parsed.User != nil {
		return false
	}
	base, err := url.Parse(g.baseURL)
	if err == nil && sameOrigin(parsed, base) {
		return true
	}
	if !strings.EqualFold(parsed.Scheme, "https") || urlPort(parsed) != "443" {
		return false
	}
	return g.allowedHost(parsed.Hostname())
}

func (g *Generator) allowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	base, err := url.Parse(g.baseURL)
	if err == nil && strings.EqualFold(host, strings.TrimSuffix(base.Hostname(), ".")) {
		return true
	}
	return host == "x.com" || host == "www.x.com" || host == "abs.twimg.com" || host == "pbs.twimg.com"
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || !strings.EqualFold(a.Scheme, b.Scheme) || !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	return urlPort(a) == urlPort(b)
}

func urlPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return ""
}

func defaultRandByte() byte {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return byte(time.Now().UnixNano())
	}
	return b[0]
}

func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}
