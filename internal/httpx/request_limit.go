package httpx

import (
	"context"
	"sync"
	"time"
)

type xWaiter struct {
	wake chan struct{}
}

var xRequests struct {
	sync.Mutex
	last      time.Time
	slowUntil time.Time
	waiters   []*xWaiter
}

// nextXRequestSlotLocked 计算在当前 last 与 slowUntil 约束下，下一次最早可放行的时间点。
// 当 slowUntil 在 last+3s 之前到期时，到期后间隔恢复为 1s，因此最早可放行时间为
// max(slowUntil, last+1s)，避免慢速模式结束前多睡或误判 DeadlineExceeded。
func nextXRequestSlotLocked(now time.Time) time.Time {
	normalSlot := xRequests.last.Add(time.Second)
	if !now.Before(xRequests.slowUntil) {
		return normalSlot
	}
	slowSlot := xRequests.last.Add(3 * time.Second)
	if !slowSlot.After(xRequests.slowUntil) {
		return slowSlot
	}
	if normalSlot.After(xRequests.slowUntil) {
		return normalSlot
	}
	return xRequests.slowUntil
}

func signalXWaiter(w *xWaiter) {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func removeXWaiterLocked(w *xWaiter) {
	for i, cur := range xRequests.waiters {
		if cur == w {
			copy(xRequests.waiters[i:], xRequests.waiters[i+1:])
			xRequests.waiters[len(xRequests.waiters)-1] = nil
			xRequests.waiters = xRequests.waiters[:len(xRequests.waiters)-1]
			if len(xRequests.waiters) == 0 {
				xRequests.waiters = nil
			} else if i == 0 {
				signalXWaiter(xRequests.waiters[0])
			}
			return
		}
	}
}

// WaitXRequest 为所有 X 元数据请求共用一个无突发的进程级节流器，媒体 CDN 不经过这里。
// 使用 FIFO 队列按真实放行时间更新 xRequests.last，并在入队前校验 ctx.Deadline()，
// 避免超时级联或队列中部取消把 xRequests.last 单调推向未来导致活锁。
func WaitXRequest(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, hasDeadline := ctx.Deadline()

	xRequests.Lock()
	now := time.Now()
	slot := nextXRequestSlotLocked(now)
	if len(xRequests.waiters) == 0 && !slot.After(now) {
		xRequests.last = now
		xRequests.Unlock()
		return nil
	}
	// slot 是绝对下界（即便前方所有等待者立即取消，也无法早于 slot 放行）。
	// 仅在绝对不可能赶上 deadline 时提前失败，避免前方等待者取消时误拒后续请求。
	if hasDeadline && slot.After(deadline) {
		xRequests.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return context.DeadlineExceeded
	}

	waiter := &xWaiter{wake: make(chan struct{}, 1)}
	xRequests.waiters = append(xRequests.waiters, waiter)
	xRequests.Unlock()

	for {
		xRequests.Lock()
		idx := -1
		for i, cur := range xRequests.waiters {
			if cur == waiter {
				idx = i
				break
			}
		}
		if idx < 0 {
			xRequests.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.Canceled
		}
		if err := ctx.Err(); err != nil {
			removeXWaiterLocked(waiter)
			xRequests.Unlock()
			return err
		}
		if idx == 0 {
			now = time.Now()
			slot = nextXRequestSlotLocked(now)
			if !slot.After(now) {
				xRequests.last = now
				removeXWaiterLocked(waiter)
				xRequests.Unlock()
				return nil
			}
			if hasDeadline && slot.After(deadline) {
				removeXWaiterLocked(waiter)
				xRequests.Unlock()
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
			xRequests.Unlock()

			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				xRequests.Lock()
				removeXWaiterLocked(waiter)
				xRequests.Unlock()
				return ctx.Err()
			case <-timer.C:
			case <-waiter.wake:
				timer.Stop()
			}
			continue
		}
		xRequests.Unlock()

		select {
		case <-ctx.Done():
			xRequests.Lock()
			removeXWaiterLocked(waiter)
			xRequests.Unlock()
			return ctx.Err()
		case <-waiter.wake:
		}
	}
}

// SlowXRequests 在上游限流或挑战后降低整个出口的请求速率。
func SlowXRequests() {
	xRequests.Lock()
	defer xRequests.Unlock()
	xRequests.slowUntil = time.Now().Add(15 * time.Minute)
	if len(xRequests.waiters) > 0 {
		signalXWaiter(xRequests.waiters[0])
	}
}
