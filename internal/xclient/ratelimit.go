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
	remaining   int
	limit       int
	reset       time.Time
	ready       bool
	nextRequest time.Time
	failures    int
}

type rateLimiter struct {
	mu            sync.Mutex
	limits        map[string]rateLimitState
	lastPrune     time.Time
	accountUntil  time.Time
	limitedStreak int
	lastLimited   time.Time
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

// maxLocalRateLimitWait 是 before() 在直接使用单个 Client（不经过 Pool）时原地
// 阻塞的上限。超过它说明本次限流窗口较长，继续占用任务执行槽只会空耗时间；
// 直接返回带恢复时间的限流错误，让调度器把任务重新入队。
const maxLocalRateLimitWait = 3 * time.Second

func (rl *rateLimiter) before(ctx context.Context, path string) error {
	rl.prune()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rl.mu.Lock()
		now := time.Now()
		readyAt := rl.readyAtLocked(path, now)
		if !readyAt.After(now) {
			state, ok := rl.limits[path]
			if ok && state.ready && now.Before(state.reset) {
				state.remaining--
				rl.limits[path] = state
			}
			rl.mu.Unlock()
			return nil
		}
		rl.mu.Unlock()
		wait := time.Until(readyAt)
		if yieldsOnRateLimit(ctx) || wait > maxLocalRateLimitWait {
			return &RateLimitedError{Path: path, RetryAt: readyAt, Cause: errRateLimitBusyWait}
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

// 调用方持有 mu；账号冷却优先，接口配额和主动匀速都使用同一个可恢复时间。
func (rl *rateLimiter) readyAtLocked(path string, now time.Time) time.Time {
	var readyAt time.Time
	if !rl.accountUntil.IsZero() {
		readyAt = rl.accountUntil.Add(5 * time.Second)
	}
	state, ok := rl.limits[path]
	if ok && state.ready {
		if state.remaining <= max(1, state.limit/50) {
			if reset := state.reset.Add(5 * time.Second); reset.After(readyAt) {
				readyAt = reset
			}
		} else if now.Before(state.reset) && state.nextRequest.After(readyAt) {
			readyAt = state.nextRequest
		}
	}
	return readyAt
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
	reset := time.Unix(resetUnix, 0)
	if limit <= 0 {
		return
	}
	remaining = max(0, min(remaining, limit))
	var next time.Time
	if remaining > max(1, limit/50) && time.Now().Before(reset) {
		interval := time.Duration(float64(time.Until(reset)) / (float64(remaining) * 0.8))
		next = time.Now().Add(max(3*time.Second, interval))
	}
	rl.mu.Lock()
	failures := rl.limits[path].failures
	rl.limits[path] = rateLimitState{
		remaining:   remaining,
		limit:       limit,
		reset:       reset,
		ready:       true,
		nextRequest: next,
		failures:    failures,
	}
	rl.mu.Unlock()
}

// markRateLimited records an API-level rate-limit error even when the response
// omitted the standard X-Rate-Limit headers. Pool callers can then yield the
// request to another cookie instead of disabling this valid credential.
func (rl *rateLimiter) markRateLimited(path string, header http.Header) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	state := rl.limits[path]
	if now.Sub(rl.lastLimited) > 30*time.Minute {
		rl.limitedStreak = 0
		state.failures = 0
	}
	state.failures = min(state.failures+1, 5)
	block := parseRetryAfter(header)
	if block <= 0 {
		block = min(time.Minute*time.Duration(1<<uint(state.failures-1)), 15*time.Minute)
	}
	state.reset = maxTime(state.reset, now.Add(block))
	state.limit = max(1, state.limit)
	state.remaining = 0
	state.ready = true
	rl.limits[path] = state
	rl.limitedStreak++
	rl.lastLimited = now
	if rl.limitedStreak >= 2 {
		rl.accountUntil = maxTime(rl.accountUntil, state.reset)
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (rl *rateLimiter) markAccountCooldown(block time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.accountUntil = maxTime(rl.accountUntil, time.Now().Add(block))
}

func (rl *rateLimiter) succeeded(path string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limitedStreak = 0
	state, ok := rl.limits[path]
	if ok {
		state.failures = 0
		rl.limits[path] = state
	}
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

// parseRetryAfter 解析 Retry-After 响应头（秒数或 HTTP-date），无法解析返回 0。
// 保留完整冷却期限；长等待由调度器延迟入队，不占用任务执行槽。
func parseRetryAfter(header http.Header) time.Duration {
	raw := header.Get("Retry-After")
	if raw == "" {
		return 0
	}
	if secs, err := strconv.ParseUint(raw, 10, 64); err == nil {
		const maxDuration = time.Duration(1<<63 - 1)
		if secs > uint64(maxDuration/time.Second) {
			return maxDuration
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
	now := time.Now()
	return rl.readyAtLocked(path, now).After(now)
}

// blockedReset 返回本限流器所有（任一路径）受限状态中最快结束限流的时间（reset+5s 裕量），
// 供 Pool.Select 在全部客户端受限时据此等待；无受限返回 false。
func (rl *rateLimiter) blockedReset(paths ...string) (time.Time, bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	var earliest time.Time
	found := false
	now := time.Now()
	if len(paths) > 0 {
		reset := rl.readyAtLocked(paths[0], now)
		return reset, reset.After(now)
	}
	for path := range rl.limits {
		reset := rl.readyAtLocked(path, now)
		if !reset.After(now) {
			continue
		}
		if !found || reset.Before(earliest) {
			earliest = reset
			found = true
		}
	}
	if !found && rl.accountUntil.Add(5*time.Second).After(now) {
		return rl.accountUntil.Add(5 * time.Second), true
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
		items = append(items, RateLimitSnapshot{
			Path:      path,
			Limit:     state.limit,
			Remaining: state.remaining,
			Reset:     state.reset,
			Blocked:   rl.readyAtLocked(path, time.Now()).After(time.Now()),
		})
	}
	if rl.accountUntil.After(time.Now()) {
		items = append(items, RateLimitSnapshot{Path: "*", Reset: rl.accountUntil, Blocked: true})
	}
	return items
}
