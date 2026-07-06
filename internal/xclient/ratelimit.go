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
