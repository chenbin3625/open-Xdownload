package xscrape

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

// accountRequestInterval 是同一账号两次请求之间的最小间隔，沿用 legacy 的 3 秒。
const accountRequestInterval = 3 * time.Second

// accountSlot 是一个账号的内存执行槽：同一时刻只允许一个请求在途。next 只由持有
// 执行槽的 goroutine 读写，因此不需要额外加锁。
//
// "正在使用"只放在内存里，不写 SQLite：并发任务撞上同一个账号时应该排队等执行槽，
// 而不是被当成限流交还调度器（见 docs/xscrape-design.md）。
type accountSlot struct {
	sem  chan struct{}
	next time.Time
}

type accountSlots struct {
	mu    sync.Mutex
	items map[int64]*accountSlot
}

func (s *accountSlots) slot(id int64) *accountSlot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[int64]*accountSlot)
	}
	slot := s.items[id]
	if slot == nil {
		slot = &accountSlot{sem: make(chan struct{}, 1)}
		s.items[id] = slot
	}
	return slot
}

// acquire 占用账号的执行槽。waited 表示执行槽当时被别的请求占用、本次等待过：
// 等待期间账号可能已被加锁或停用，调用方需要重新确认。
func (s *accountSlots) acquire(ctx context.Context, id int64) (slot *accountSlot, release func(), waited bool, err error) {
	slot = s.slot(id)
	select {
	case slot.sem <- struct{}{}:
	default:
		waited = true
		select {
		case slot.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, nil, true, ctx.Err()
		}
	}
	var once sync.Once
	release = func() { once.Do(func() { <-slot.sem }) }
	return slot, release, waited, nil
}

// pace 在持有执行槽时调用：保证同一账号两次请求间隔至少 3 秒，再经过进程级节流。
// DisablePacing 时直接返回。
func (e *Engine) pace(ctx context.Context, slot *accountSlot) error {
	if e.opts.DisablePacing {
		return ctx.Err()
	}
	if slot != nil {
		if wait := slot.next.Sub(e.now()); wait > 0 {
			if err := e.sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	if err := httpx.WaitXRequest(ctx); err != nil {
		return err
	}
	if slot != nil {
		slot.next = e.now().Add(accountRequestInterval)
	}
	return nil
}

// paceProcess 只做进程级节流，用于不绑定执行槽的请求（guest、账号探测）。
func (e *Engine) paceProcess(ctx context.Context) error {
	if e.opts.DisablePacing {
		return ctx.Err()
	}
	return httpx.WaitXRequest(ctx)
}

// rateLimitedLog 让同一类日志在一段时间内只打印一次，避免 xtid 失败等问题刷屏。
type rateLimitedLog struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *rateLimitedLog) printf(key string, every time.Duration, format string, args ...any) {
	now := time.Now()
	l.mu.Lock()
	if l.last == nil {
		l.last = make(map[string]time.Time)
	}
	if last, ok := l.last[key]; ok && now.Sub(last) < every {
		l.mu.Unlock()
		return
	}
	l.last[key] = now
	l.mu.Unlock()
	log.Printf(format, args...)
}
