package xclient

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxRateLimitWait caps externally supplied Retry-After / X-Rate-Limit-Reset
// headers so a malformed or millisecond timestamp cannot lock a client for years.
const maxRateLimitWait = 24 * time.Hour

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
	wafUntil      time.Time
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
		readyAt, pacing := rl.readyReasonLocked(path, now)
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
			return &RateLimitedError{Path: path, RetryAt: readyAt, Cause: errRateLimitBusyWait, Pacing: pacing}
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
	readyAt, _ := rl.readyReasonLocked(path, now)
	return readyAt
}

// readyReasonLocked 与 readyAtLocked 相同，并报告阻塞是否仅来自"配额内的主动匀速"
// （nextRequest），而不是账号冷却或配额耗尽。只有后者才是真正的限流。
func (rl *rateLimiter) readyReasonLocked(path string, now time.Time) (time.Time, bool) {
	var readyAt time.Time
	pacing := false
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
			// 账号仍在冷却时不能把等待归因为匀速。
			pacing = !rl.accountUntil.Add(5 * time.Second).After(now)
		}
	}
	return readyAt, pacing
}

// pacingOnly 报告 path 当前是否只因主动匀速而暂时不可用。
func (rl *rateLimiter) pacingOnly(path string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	readyAt, pacing := rl.readyReasonLocked(path, now)
	return pacing && readyAt.After(now)
}

func (rl *rateLimiter) after(path string, header http.Header) {
	now := time.Now()
	limitRaw := header.Get("X-Rate-Limit-Limit")
	remainingRaw := header.Get("X-Rate-Limit-Remaining")
	resetRaw := header.Get("X-Rate-Limit-Reset")
	if limitRaw == "" || remainingRaw == "" || resetRaw == "" {
		// 无 X-Rate-Limit 头时（如 CDN/边缘层 429），若带 Retry-After，据此阻塞相应时长，
		// 否则 before() 不会阻塞、do() 仅以固定退避重试，对活动 429 形成速射。
		if ra := parseRetryAfter(header); ra > 0 {
			rl.mu.Lock()
			state := rl.limits[path]
			state.remaining = 0
			state.limit = max(1, state.limit)
			state.reset = maxTime(state.reset, now.Add(ra))
			state.ready = true
			rl.limits[path] = state
			rl.mu.Unlock()
		}
		return
	}
	limit, err := strconv.Atoi(strings.TrimSpace(limitRaw))
	if err != nil {
		return
	}
	remaining, err := strconv.Atoi(strings.TrimSpace(remainingRaw))
	if err != nil {
		return
	}
	reset, ok := parseRateLimitReset(resetRaw, now)
	if !ok {
		return
	}
	if limit <= 0 {
		return
	}
	remaining = max(0, min(remaining, limit))
	var next time.Time
	if remaining > max(1, limit/50) && now.Before(reset) {
		interval := time.Duration(float64(reset.Sub(now)) / (float64(remaining) * 0.8))
		next = now.Add(max(3*time.Second, interval))
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

func (rl *rateLimiter) markWAFCooldown(path string, block time.Duration) {
	if block <= 0 {
		block = time.Minute
	}
	now := time.Now()
	until := now.Add(block)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.accountUntil = maxTime(rl.accountUntil, until)
	rl.wafUntil = maxTime(rl.wafUntil, until)
	state := rl.limits[path]
	if state.limit <= 0 {
		state.limit = 1
	}
	state.remaining = 0
	state.reset = maxTime(state.reset, until)
	state.ready = true
	rl.limits[path] = state
}

func (rl *rateLimiter) isWAFBlocked() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return !rl.wafUntil.IsZero() && time.Now().Before(rl.wafUntil.Add(5*time.Second))
}

func (rl *rateLimiter) hasActiveBlocks(now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if !rl.accountUntil.IsZero() && rl.accountUntil.Add(5*time.Second).After(now) {
		return true
	}
	if !rl.wafUntil.IsZero() && rl.wafUntil.Add(5*time.Second).After(now) {
		return true
	}
	for path := range rl.limits {
		if rl.readyAtLocked(path, now).After(now) {
			return true
		}
	}
	return false
}

func (rl *rateLimiter) succeeded(path string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limitedStreak = 0
	rl.wafUntil = time.Time{}
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

// parseRateLimitReset 解析 X-Rate-Limit-Reset（秒级或毫秒级 Unix 时间戳），
// 并将复位时间封顶在 now+maxRateLimitWait，防止异常大值或毫秒时间戳锁死客户端。
func parseRateLimitReset(raw string, now time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	maxReset := now.Add(maxRateLimitWait)
	resetUnix, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(raw, "-") {
			return maxReset, true
		}
		return time.Time{}, false
	}
	if resetUnix <= 0 {
		return time.Time{}, false
	}
	var reset time.Time
	if resetUnix > 1_000_000_000_000 {
		reset = time.UnixMilli(resetUnix)
	} else {
		reset = time.Unix(resetUnix, 0)
	}
	if reset.After(maxReset) {
		return maxReset, true
	}
	return reset, true
}

// parseRetryAfter 解析 Retry-After 响应头（秒数或 HTTP-date），无法解析返回 0。
// 保留服务器冷却期限，但封顶在 maxRateLimitWait（24 小时），避免异常头值锁死账号数百年。
func parseRetryAfter(header http.Header) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if secs, err := strconv.ParseUint(raw, 10, 64); err == nil {
		if secs > uint64(maxRateLimitWait/time.Second) {
			return maxRateLimitWait
		}
		return time.Duration(secs) * time.Second
	} else if errors.Is(err, strconv.ErrRange) {
		return maxRateLimitWait
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			if d > maxRateLimitWait {
				return maxRateLimitWait
			}
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
		for _, path := range paths {
			reset := rl.readyAtLocked(path, now)
			if !reset.After(now) {
				continue
			}
			if !found || reset.Before(earliest) {
				earliest = reset
				found = true
			}
		}
		return earliest, found
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

func (rl *rateLimiter) snapshot() []RateLimitSnapshot {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
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
			Blocked:   rl.readyAtLocked(path, now).After(now),
		})
	}
	if rl.accountUntil.After(now) {
		items = append(items, RateLimitSnapshot{Path: "*", Reset: rl.accountUntil, Blocked: true})
	}
	return items
}
