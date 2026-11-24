package httpx

import (
	"context"
	"sync"
	"time"
)

type xWaiter struct {
	wake chan struct{}
}

type xRequestLimiter struct {
	sync.Mutex
	last      time.Time
	slowUntil time.Time
	waiters   []*xWaiter
}

var xRequests xRequestLimiter

const maxScopedXRequestLimiters = 64

var scopedXRequests = struct {
	sync.Mutex
	items map[string]*xRequestLimiter
}{
	items: map[string]*xRequestLimiter{},
}

func xLimiterForScope(scope string) *xRequestLimiter {
	if scope == "" {
		return &xRequests
	}
	scopedXRequests.Lock()
	defer scopedXRequests.Unlock()
	if limiter, ok := scopedXRequests.items[scope]; ok {
		return limiter
	}
	if len(scopedXRequests.items) >= maxScopedXRequestLimiters {
		var oldestKey string
		var oldestTime time.Time
		for key, item := range scopedXRequests.items {
			item.Lock()
			idle := len(item.waiters) == 0
			last := item.last
			item.Unlock()
			if !idle {
				continue
			}
			if oldestKey == "" || last.Before(oldestTime) {
				oldestKey = key
				oldestTime = last
			}
		}
		if oldestKey != "" {
			delete(scopedXRequests.items, oldestKey)
		}
	}
	limiter := &xRequestLimiter{}
	scopedXRequests.items[scope] = limiter
	return limiter
}

// nextSlotLocked 计算在当前 last 与 slowUntil 约束下，下一次最早可放行的时间点。
// 当 slowUntil 在 last+3s 之前到期时，到期后间隔恢复为 1s，因此最早可放行时间为
// max(slowUntil, last+1s)，避免慢速模式结束前多睡或误判 DeadlineExceeded。
func (l *xRequestLimiter) nextSlotLocked(now time.Time) time.Time {
	normalSlot := l.last.Add(time.Second)
	if !now.Before(l.slowUntil) {
		return normalSlot
	}
	slowSlot := l.last.Add(3 * time.Second)
	if !slowSlot.After(l.slowUntil) {
		return slowSlot
	}
	if normalSlot.After(l.slowUntil) {
		return normalSlot
	}
	return l.slowUntil
}

func signalXWaiter(w *xWaiter) {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (l *xRequestLimiter) removeWaiterLocked(w *xWaiter) {
	for i, cur := range l.waiters {
		if cur == w {
			copy(l.waiters[i:], l.waiters[i+1:])
			l.waiters[len(l.waiters)-1] = nil
			l.waiters = l.waiters[:len(l.waiters)-1]
			if len(l.waiters) == 0 {
				l.waiters = nil
			} else if i == 0 {
				signalXWaiter(l.waiters[0])
			}
			return
		}
	}
}

func (l *xRequestLimiter) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, hasDeadline := ctx.Deadline()

	l.Lock()
	now := time.Now()
	slot := l.nextSlotLocked(now)
	if len(l.waiters) == 0 && !slot.After(now) {
		l.last = now
		l.Unlock()
		return nil
	}
	// slot 是绝对下界（即便前方所有等待者立即取消，也无法早于 slot 放行）。
	// 仅在绝对不可能赶上 deadline 时提前失败，避免前方等待者取消时误拒后续请求。
	if hasDeadline && slot.After(deadline) {
		l.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return context.DeadlineExceeded
	}

	waiter := &xWaiter{wake: make(chan struct{}, 1)}
	l.waiters = append(l.waiters, waiter)
	l.Unlock()

	for {
		l.Lock()
		idx := -1
		for i, cur := range l.waiters {
			if cur == waiter {
				idx = i
				break
			}
		}
		if idx < 0 {
			l.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.Canceled
		}
		if err := ctx.Err(); err != nil {
			l.removeWaiterLocked(waiter)
			l.Unlock()
			return err
		}
		if idx == 0 {
			now = time.Now()
			slot = l.nextSlotLocked(now)
			if !slot.After(now) {
				l.last = now
				l.removeWaiterLocked(waiter)
				l.Unlock()
				return nil
			}
			if hasDeadline && slot.After(deadline) {
				l.removeWaiterLocked(waiter)
				l.Unlock()
				if err := ctx.Err(); err != nil {
					return err
				}
				return context.DeadlineExceeded
			}
			// 在持锁状态下排空已消费的唤醒信号，防止后续直接命中旧信号做一次空转。
			select {
			case <-waiter.wake:
			default:
			}
			wait := slot.Sub(now)
			l.Unlock()

			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				l.Lock()
				l.removeWaiterLocked(waiter)
				l.Unlock()
				return ctx.Err()
			case <-timer.C:
			case <-waiter.wake:
				timer.Stop()
			}
			continue
		}
		l.Unlock()

		select {
		case <-ctx.Done():
			l.Lock()
			l.removeWaiterLocked(waiter)
			l.Unlock()
			return ctx.Err()
		case <-waiter.wake:
		}
	}
}

func (l *xRequestLimiter) slow() {
	l.Lock()
	defer l.Unlock()
	l.slowUntil = time.Now().Add(15 * time.Minute)
	if len(l.waiters) > 0 {
		signalXWaiter(l.waiters[0])
	}
}

// WaitXRequest 为默认出口的 X 元数据请求提供无突发的进程级节流器，媒体 CDN 不经过这里。
// 使用 FIFO 队列按真实放行时间更新 last，并在入队前校验 ctx.Deadline()，
// 避免超时级联或队列中部取消把 last 单调推向未来导致活锁。
func WaitXRequest(ctx context.Context) error {
	return xRequests.wait(ctx)
}

// WaitXRequestForScope 按出口作用域（例如独立代理或 syndication 域名）隔离节流队列，
// 避免多代理池或不同上游域名共享同一把全局串行锁。空作用域退回默认全局队列。
func WaitXRequestForScope(ctx context.Context, scope string) error {
	return xLimiterForScope(scope).wait(ctx)
}

// SlowXRequests 在上游限流或挑战后降低默认出口的请求速率。
func SlowXRequests() {
	xRequests.slow()
}

// SlowXRequestsForScope 仅在指定出口作用域内降速，不影响其他独立代理或上游域名。
func SlowXRequestsForScope(scope string) {
	xLimiterForScope(scope).slow()
}
