package xscrape

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

type rateLimitKey struct {
	accountID int64
	queue     string
}

type rateLimitMeta struct {
	limit     int
	remaining int
	reset     time.Time
	updatedAt time.Time
}

type rateLimitTracker struct {
	mu      sync.Mutex
	entries map[rateLimitKey]rateLimitMeta
}

func (t *rateLimitTracker) recordHeaders(accountID int64, queue string, header http.Header, now time.Time) (reset time.Time, shouldLock bool) {
	if header == nil {
		return time.Time{}, false
	}
	limit, hasLimit := parseNonNegativeInt(header.Get("X-Rate-Limit-Limit"))
	remaining, hasRemaining := parseNonNegativeInt(header.Get("X-Rate-Limit-Remaining"))
	parsedReset, hasReset := parseRateLimitResetStrict(header, now)
	if !hasLimit && !hasRemaining && !hasReset {
		return time.Time{}, false
	}

	key := rateLimitKey{accountID: accountID, queue: queue}
	t.mu.Lock()
	if t.entries == nil {
		t.entries = make(map[rateLimitKey]rateLimitMeta)
	}
	meta := t.entries[key]
	if hasLimit {
		meta.limit = limit
	}
	if hasRemaining {
		meta.remaining = remaining
	}
	if hasReset {
		meta.reset = parsedReset
	}
	meta.updatedAt = now
	t.entries[key] = meta
	t.mu.Unlock()

	if !hasRemaining || !hasReset || !parsedReset.After(now) {
		return parsedReset, false
	}
	// 当剩余配额已归零（或大窗口仅剩 1 次）时主动提前进入冷却锁，
	// 避免下一请求撞 429 触发整池/代理降速。
	if remaining == 0 || (meta.limit >= 20 && remaining <= 1) {
		return parsedReset, true
	}
	return parsedReset, false
}

func (t *rateLimitTracker) markExhausted(accountID int64, queue string, header http.Header, reset, now time.Time) {
	key := rateLimitKey{accountID: accountID, queue: queue}
	limit, hasLimit := parseNonNegativeInt(header.Get("X-Rate-Limit-Limit"))
	t.mu.Lock()
	if t.entries == nil {
		t.entries = make(map[rateLimitKey]rateLimitMeta)
	}
	meta := t.entries[key]
	if hasLimit {
		meta.limit = limit
	}
	meta.remaining = 0
	meta.reset = reset
	meta.updatedAt = now
	t.entries[key] = meta
	t.mu.Unlock()
}

// isLowQuota 报告某账号在指定 queue 上剩余配额是否偏低（主账号预留保护用）。
// 未收到过限流头或重置时间已过时返回 false，不影响正常 LRU 轮换。
func (t *rateLimitTracker) isLowQuota(accountID int64, queue string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	meta, ok := t.entries[rateLimitKey{accountID: accountID, queue: queue}]
	if !ok || meta.limit <= 0 {
		return false
	}
	if !meta.reset.IsZero() && !meta.reset.After(now) {
		return false
	}
	reserve := max(3, meta.limit/5)
	if reserve >= meta.limit {
		reserve = max(1, meta.limit/2)
	}
	return meta.remaining <= reserve
}

func (t *rateLimitTracker) snapshot(accountID int64, now time.Time) map[string]xapi.RateLimitSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.entries) == 0 {
		return nil
	}
	out := map[string]xapi.RateLimitSnapshot{}
	for key, meta := range t.entries {
		if key.accountID != accountID {
			continue
		}
		if !meta.reset.IsZero() && !meta.reset.After(now) {
			continue
		}
		out[key.queue] = xapi.RateLimitSnapshot{
			Path:      key.queue,
			Limit:     meta.limit,
			Remaining: meta.remaining,
			Reset:     meta.reset,
		}
	}
	return out
}

func parseNonNegativeInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func parseRateLimitResetStrict(header http.Header, now time.Time) (time.Time, bool) {
	if header == nil {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(header.Get("X-Rate-Limit-Reset"))
	if raw == "" {
		return time.Time{}, false
	}
	return rateLimitReset(header, now), true
}
