package xscrape

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// assetCacheTTL 让同一冷启动或周期刷新窗口内的 xtid 与 queryId 发现共享首页 HTML 与 JS 响应，
	// 避免把 x.com 首页和相同 JS bundle 抓两遍。
	assetCacheTTL           = 2 * time.Minute
	maxAssetCacheEntries    = 32
	maxAssetCacheEntryBytes = 16 << 20
)

type assetCacheEntry struct {
	status    int
	header    http.Header
	body      []byte
	expiresAt time.Time
}

type assetCache struct {
	mu      sync.Mutex
	entries map[string]*assetCacheEntry
}

func (c *assetCache) get(key string, now time.Time) (*assetCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return entry, true
}

func (c *assetCache) put(key string, status int, header http.Header, body []byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*assetCacheEntry, 8)
	}
	for k, existing := range c.entries {
		if !now.Before(existing.expiresAt) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) >= maxAssetCacheEntries {
		var oldestKey string
		var oldestExp time.Time
		for k, existing := range c.entries {
			if oldestKey == "" || existing.expiresAt.Before(oldestExp) {
				oldestKey = k
				oldestExp = existing.expiresAt
			}
		}
		if oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = &assetCacheEntry{
		status:    status,
		header:    header.Clone(),
		body:      bytes.Clone(body),
		expiresAt: now.Add(assetCacheTTL),
	}
}

func (c *assetCache) clear() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

type cachingAssetTransport struct {
	engine *Engine
	proxy  string
	base   http.RoundTripper
}

func (e *Engine) assetRoundTripper(proxy string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return cachingAssetTransport{
		engine: e,
		proxy:  strings.TrimSpace(proxy),
		base:   base,
	}
}

func (t cachingAssetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.Method != http.MethodGet || req.URL == nil {
		return t.base.RoundTrip(req)
	}
	key := t.proxy + "\x00" + req.URL.String()
	now := t.engine.now()
	if cached, ok := t.engine.assets.get(key, now); ok {
		return &http.Response{
			StatusCode:    cached.status,
			Status:        http.StatusText(cached.status),
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        cached.header.Clone(),
			Body:          io.NopCloser(bytes.NewReader(cached.body)),
			ContentLength: int64(len(cached.body)),
			Request:       req,
		}, nil
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK || resp.Body == nil {
		return resp, err
	}
	buf, readErr := io.ReadAll(io.LimitReader(resp.Body, maxAssetCacheEntryBytes+1))
	if readErr != nil {
		_ = resp.Body.Close()
		return nil, readErr
	}
	if len(buf) <= maxAssetCacheEntryBytes {
		_ = resp.Body.Close()
		t.engine.assets.put(key, resp.StatusCode, resp.Header, buf, t.engine.now())
		resp.Body = io.NopCloser(bytes.NewReader(buf))
		resp.ContentLength = int64(len(buf))
		return resp, nil
	}
	origBody := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: io.MultiReader(bytes.NewReader(buf), origBody),
		Closer: origBody,
	}
	return resp, nil
}
