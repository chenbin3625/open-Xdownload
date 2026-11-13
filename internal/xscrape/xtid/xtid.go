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

	cacheTTL = time.Hour

	// 初始化最多抓取的脚本数量，避免在异常页面里无限爬取。
	maxCrawlChunks = 24
	maxCrawlDepth  = 3
	// 单个响应体上限，正常脚本远小于此值。
	maxBodyBytes = 16 << 20
)

// Generator 生成 X 的 x-client-transaction-id 请求头。
//
// 初始化（抓首页、定位签名脚本、计算动画 key）的结果缓存 1 小时。
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

// New 创建生成器。client 为 nil 时使用 http.DefaultClient。
// baseURL 为空时使用 https://x.com，userAgent 为空时使用内置的桌面 Chrome UA。
func New(client *http.Client, baseURL, userAgent string) *Generator {
	return newWithOptions(client, baseURL, userAgent, options{})
}

func newWithOptions(client *http.Client, baseURL, userAgent string, opts options) *Generator {
	if client == nil {
		client = http.DefaultClient
	}
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
	return &Generator{
		client:    client,
		baseURL:   baseURL,
		userAgent: userAgent,
		now:       now,
		randByte:  randByte,
	}
}

// Invalidate 清空缓存，下次 Generate 会重新抓取并计算。
func (g *Generator) Invalidate() {
	g.mu.Lock()
	g.cached = nil
	g.fetchedAt = time.Time{}
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
	g.mu.Lock()
	if g.cached != nil && g.now().Sub(g.fetchedAt) < cacheTTL {
		k := g.cached
		g.mu.Unlock()
		return k, nil
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

	k, err := g.fetch(ctx)

	g.mu.Lock()
	if err == nil && g.version == version {
		g.cached = k
		g.fetchedAt = g.now()
	}
	if g.init == call {
		g.init = nil
	}
	call.keys, call.err = k, err
	close(call.done)
	g.mu.Unlock()
	return k, err
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
	scripts := extractScriptURLs(string(page), g.baseURL)
	if len(scripts) == 0 {
		return nil, fmt.Errorf("首页未找到任何 JS 脚本")
	}

	// 旧版页面直接引用 ondemand.s / sign.o，或可通过 chunk 映射重建出来。
	for _, u := range scripts {
		if matchesIndicesFile(u) {
			body, err := g.get(ctx, u)
			if err != nil {
				return nil, fmt.Errorf("下载签名脚本失败: %w", err)
			}
			return body, nil
		}
	}

	// 当前 x-web 构建把签名 chunk 藏在 bundle 的动态 import 里，需要顺着
	// 入口脚本的静态 import 图找过去。
	return g.crawlForIndices(ctx, scripts)
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
		next := make([]item, 0, len(priority)+len(normal))
		for _, u := range priority {
			next = append(next, item{url: u, depth: it.depth + 1})
		}
		for _, u := range normal {
			next = append(next, item{url: u, depth: it.depth + 1})
		}
		queue = append(next, queue...)
	}

	msg := fmt.Sprintf("未能定位签名 JS（ondemand.s/sign.o，已尝试 %d 个脚本）", tried)
	if lastErr != nil {
		return nil, fmt.Errorf("%s: %w", msg, lastErr)
	}
	return nil, fmt.Errorf("%s", msg)
}

func (g *Generator) get(ctx context.Context, rawURL string) ([]byte, error) {
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
