package httpx

import (
	"context"
	"sync"
	"time"
)

var xRequests struct {
	sync.Mutex
	last      time.Time
	slowUntil time.Time
}

// WaitXRequest 为所有 X 元数据请求共用一个无突发的进程级节流器，媒体 CDN 不经过这里。
func WaitXRequest(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		xRequests.Lock()
		now := time.Now()
		interval := time.Second
		if now.Before(xRequests.slowUntil) {
			interval = 3 * time.Second
		}
		wait := xRequests.last.Add(interval).Sub(now)
		if wait <= 0 {
			xRequests.last = now
			xRequests.Unlock()
			return nil
		}
		xRequests.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// SlowXRequests 在上游限流或挑战后降低整个出口的请求速率。
func SlowXRequests() {
	xRequests.Lock()
	defer xRequests.Unlock()
	xRequests.slowUntil = time.Now().Add(15 * time.Minute)
}
