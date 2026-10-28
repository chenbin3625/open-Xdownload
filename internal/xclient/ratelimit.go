package xclient

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type rateLimitState struct {
	remaining int
	limit     int
	reset     time.Time
	ready     bool
}

type rateLimiter struct {
	mu        sync.Mutex
	limits    map[string]rateLimitState
	lastPrune time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{limits: map[string]rateLimitState{}, lastPrune: time.Now()}
}

// prune removes stale entries whose reset time is more than 5 minutes in the past.
// Caller must NOT hold mu.
func (rl *rateLimiter) prune() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if now.Sub(rl.lastPrune) < 5*time.Minute {
		return
	}
	cutoff := now.Add(-5 * time.Minute)
	for path, state := range rl.limits {
		if state.ready && state.reset.Before(cutoff) {
			delete(rl.limits, path)
		}
	}
	rl.lastPrune = now
}

var errRateLimitBusyWait = errors.New("rate limiter: 连续等待次数超限，放弃本次请求")

func (rl *rateLimiter) before(ctx context.Context, path string) error {
	rl.prune()
	const maxWaitIterations = 3
	for i := 0; ; i++ {
		rl.mu.Lock()
		state, ok := rl.limits[path]
		if !ok || !state.ready || time.Now().After(state.reset) {
			rl.mu.Unlock()
			return nil
		}
		threshold := max(1, state.limit/50)
		if state.remaining > threshold {
			state.remaining--
			rl.limits[path] = state
			rl.mu.Unlock()
			return nil
		}
		wait := time.Until(state.reset.Add(5 * time.Second))
		rl.mu.Unlock()
		if wait <= 0 {
			return nil
		}
		if i >= maxWaitIterations {
			return errRateLimitBusyWait
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (rl *rateLimiter) after(path string, header http.Header) {
	limitRaw := header.Get("X-Rate-Limit-Limit")
	remainingRaw := header.Get("X-Rate-Limit-Remaining")
	resetRaw := header.Get("X-Rate-Limit-Reset")
	if limitRaw == "" || remainingRaw == "" || resetRaw == "" {
		// 无 X-Rate-Limit 头时（如 CDN/边缘层 429），若带 Retry-After，据此阻塞相应时长，
		// 否则 before() 不会阻塞、do() 仅以固定退避重试，对活动 429 形成速射。
		if ra := parseRetryAfter(header); ra > 0 {
			rl.mu.Lock()
			rl.limits[path] = rateLimitState{remaining: 0, limit: 1, reset: time.Now().Add(ra), ready: true}
			rl.mu.Unlock()
		}
		return
	}
	limit, err := strconv.Atoi(limitRaw)
	if err != nil {
		return
	}
	remaining, err := strconv.Atoi(remainingRaw)
	if err != nil {
		return
	}
	resetUnix, err := strconv.ParseInt(resetRaw, 10, 64)
	if err != nil {
		return
	}
	// 与 Retry-After 同样截断：时钟偏差或异常的 reset 值会让 before() 一直阻塞到该时刻，
	// 任务带着并发槽位卡死（只有 ctx 能打断）。
	reset := time.Unix(resetUnix, 0)
	if latest := time.Now().Add(maxRetryAfter); reset.After(latest) {
		reset = latest
	}
	rl.mu.Lock()
	rl.limits[path] = rateLimitState{
		remaining: remaining,
		limit:     limit,
		reset:     reset,
		ready:     true,
	}
	rl.mu.Unlock()
}

// markRateLimited records an API-level rate-limit error even when the response
// omitted the standard X-Rate-Limit headers. Pool callers can then yield the
// request to another cookie instead of disabling this valid credential.
func (rl *rateLimiter) markRateLimited(path string, header http.Header) {
	rl.markRateLimitedFor(path, parseRetryAfter(header))
}

// markRateLimitedFor 以指定冷却时长把 path 标记为不可用；block<=0 时默认 1 分钟。
// WAF 挑战页用固定短退避（60s），不采信挑战页可能携带的 Retry-After。
func (rl *rateLimiter) markRateLimitedFor(path string, block time.Duration) {
	if block <= 0 {
		block = time.Minute
	}
	reset := time.Now().Add(block)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	state := rl.limits[path]
	if state.limit <= 0 {
		state.limit = 1
	}
	if state.reset.After(reset) {
		reset = state.reset
	}
	state.remaining = 0
	state.reset = reset
	state.ready = true
	rl.limits[path] = state
}

// refund 归还 before() 预扣的 1 个配额，用于请求在收到响应头前就失败（连接重置/超时等）
// 的情况——此时 after() 不会被调用，预扣若不归还会永久消耗本地速率预算，累积后触发
// 不必要的长阻塞。并发场景下与 after() 的覆盖竞态可接受：after() 用响应头（权威值）
// 覆盖；refund 多还 1 个至多导致一次额外请求被服务端 429 纠正。
func (rl *rateLimiter) refund(path string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	state, ok := rl.limits[path]
	if !ok || !state.ready {
		return
	}
	if state.remaining < state.limit {
		state.remaining++
		rl.limits[path] = state
	}
}

// maxRetryAfter 限制 Retry-After 的采纳上限。上游（或中间 CDN）可能返回
// Retry-After: 86400，而 before() 会一直等到该时刻——任务会带着并发槽位阻塞
// 一整天，表现为永远卡在 resolving。Pool.Select 早已有 maxPoolSelectWait 这类
// 上界，这里补齐同样的约束。
const maxRetryAfter = 15 * time.Minute

// parseRetryAfter 解析 Retry-After 响应头（秒数或 HTTP-date），无法解析返回 0。
// 返回值上限为 maxRetryAfter。
func parseRetryAfter(header http.Header) time.Duration {
	raw := header.Get("Retry-After")
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs < 0 {
			return 0
		}
		return min(time.Duration(secs)*time.Second, maxRetryAfter)
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			return min(d, maxRetryAfter)
		}
	}
	return 0
}

func (rl *rateLimiter) wouldBlock(path string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	state, ok := rl.limits[path]
	if !ok || !state.ready || time.Now().After(state.reset) {
		return false
	}
	threshold := max(1, state.limit/50)
	return state.remaining <= threshold
}

// blockedReset 返回本限流器所有（任一路径）受限状态中最快结束限流的时间（reset+5s 裕量），
// 供 Pool.Select 在全部客户端受限时据此等待；无受限返回 false。
func (rl *rateLimiter) blockedReset() (time.Time, bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	var earliest time.Time
	found := false
	for _, state := range rl.limits {
		if !state.ready || time.Now().After(state.reset) {
			continue
		}
		if state.remaining > max(1, state.limit/50) {
			continue
		}
		reset := state.reset.Add(5 * time.Second)
		if !found || reset.Before(earliest) {
			earliest = reset
			found = true
		}
	}
	return earliest, found
}

type RateLimitSnapshot struct {
	Path      string    `json:"path"`
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
	Blocked   bool      `json:"blocked"`
}

func (rl *rateLimiter) snapshot() []RateLimitSnapshot {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	items := make([]RateLimitSnapshot, 0, len(rl.limits))
	for path, state := range rl.limits {
		if !state.ready {
			continue
		}
		threshold := max(1, state.limit/50)
		items = append(items, RateLimitSnapshot{
			Path:      path,
			Limit:     state.limit,
			Remaining: state.remaining,
			Reset:     state.reset,
			Blocked:   state.remaining <= threshold && time.Now().Before(state.reset),
		})
	}
	return items
}
