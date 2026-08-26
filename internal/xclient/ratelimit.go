package xclient

import (
	"context"
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
	mu     sync.Mutex
	limits map[string]rateLimitState
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{limits: map[string]rateLimitState{}}
}

func (rl *rateLimiter) before(ctx context.Context, path string) error {
	for {
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
	rl.mu.Lock()
	rl.limits[path] = rateLimitState{
		remaining: remaining,
		limit:     limit,
		reset:     time.Unix(resetUnix, 0),
		ready:     true,
	}
	rl.mu.Unlock()
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

// parseRetryAfter 解析 Retry-After 响应头（秒数或 HTTP-date），无法解析返回 0。
func parseRetryAfter(header http.Header) time.Duration {
	raw := header.Get("Retry-After")
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			return d
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
