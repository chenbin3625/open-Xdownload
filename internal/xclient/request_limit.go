package xclient

import (
	"context"
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

const accountRequestInterval = 3 * time.Second

// accountRequestLimitTTL 与 accountRequestPruneInterval 控制全局账号限速表
// accountRequests 的过期回收，避免长期轮换或移除账号后残留条目无限增长。
var (
	accountRequestLimitTTL      = time.Hour
	accountRequestPruneInterval = 5 * time.Minute
)

type accountRequestLimit struct {
	slot       chan struct{}
	next       atomic.Int64 // UnixNano
	lastAccess atomic.Int64 // UnixNano
	inFlight   atomic.Int32
	limiter    *rateLimiter
}

var accountRequests = struct {
	sync.Mutex
	items     map[[32]byte]*accountRequestLimit
	lastPrune time.Time
}{items: make(map[[32]byte]*accountRequestLimit)}

// 以账号 token 的摘要共享状态，配置保存和检测用的新客户端不能重置限速窗口。
func requestLimitForAccount(authToken string) *accountRequestLimit {
	key := sha256.Sum256([]byte(authToken))
	now := time.Now()
	accountRequests.Lock()
	defer accountRequests.Unlock()
	if accountRequests.lastPrune.IsZero() || now.Sub(accountRequests.lastPrune) >= accountRequestPruneInterval {
		pruneAccountRequestsLocked(now)
		accountRequests.lastPrune = now
	}
	limit := accountRequests.items[key]
	if limit == nil {
		limit = &accountRequestLimit{slot: make(chan struct{}, 1), limiter: newRateLimiter()}
		accountRequests.items[key] = limit
	}
	limit.lastAccess.Store(now.UnixNano())
	return limit
}

// pruneAccountRequestsLocked 清理长期未访问、无在途请求、无未到期节奏/限流阻塞的条目。
// 调用方必须持有 accountRequests.Mutex。
func pruneAccountRequestsLocked(now time.Time) {
	ttl := accountRequestLimitTTL
	if ttl <= 0 {
		return
	}
	for key, item := range accountRequests.items {
		if item == nil {
			delete(accountRequests.items, key)
			continue
		}
		if item.inFlight.Load() > 0 {
			continue
		}
		last := time.Unix(0, item.lastAccess.Load())
		if now.Sub(last) < ttl {
			continue
		}
		if nextNano := item.next.Load(); nextNano > 0 && time.Unix(0, nextNano).After(now) {
			continue
		}
		if item.limiter != nil && item.limiter.hasActiveBlocks(now) {
			continue
		}
		delete(accountRequests.items, key)
	}
}

// 执行槽覆盖响应体的读取；所有实际尝试（包括重试和登录检测）共用此入口。
func (c *Client) beginRequest(ctx context.Context, path string) (func(), error) {
	release := func() {}
	if c.requests != nil {
		c.requests.lastAccess.Store(time.Now().UnixNano())
		c.requests.inFlight.Add(1)
		select {
		case c.requests.slot <- struct{}{}:
			release = func() {
				c.requests.lastAccess.Store(time.Now().UnixNano())
				<-c.requests.slot
				c.requests.inFlight.Add(-1)
			}
		case <-ctx.Done():
			c.requests.inFlight.Add(-1)
			return nil, ctx.Err()
		}
	}
	if c.limiter != nil {
		if err := c.limiter.before(ctx, path); err != nil {
			release()
			return nil, err
		}
	}
	if c.requests != nil {
		if nextNano := c.requests.next.Load(); nextNano > 0 {
			if wait := time.Until(time.Unix(0, nextNano)); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					if c.limiter != nil {
						c.limiter.refund(path)
					}
					release()
					return nil, ctx.Err()
				case <-timer.C:
				}
			}
		}
		if err := httpx.WaitXRequest(ctx); err != nil {
			if c.limiter != nil {
				c.limiter.refund(path)
			}
			release()
			return nil, err
		}
		now := time.Now()
		c.requests.lastAccess.Store(now.UnixNano())
		c.requests.next.Store(now.Add(accountRequestInterval).UnixNano())
	}
	return release, nil
}
