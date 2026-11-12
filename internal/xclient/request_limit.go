package xclient

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

const accountRequestInterval = 3 * time.Second

type accountRequestLimit struct {
	slot    chan struct{}
	next    time.Time
	limiter *rateLimiter
}

var accountRequests = struct {
	sync.Mutex
	items map[[32]byte]*accountRequestLimit
}{items: make(map[[32]byte]*accountRequestLimit)}

// 以账号 token 的摘要共享状态，配置保存和检测用的新客户端不能重置限速窗口。
func requestLimitForAccount(authToken string) *accountRequestLimit {
	key := sha256.Sum256([]byte(authToken))
	accountRequests.Lock()
	defer accountRequests.Unlock()
	limit := accountRequests.items[key]
	if limit == nil {
		limit = &accountRequestLimit{slot: make(chan struct{}, 1), limiter: newRateLimiter()}
		accountRequests.items[key] = limit
	}
	return limit
}

// 执行槽覆盖响应体的读取；所有实际尝试（包括重试和登录检测）共用此入口。
func (c *Client) beginRequest(ctx context.Context, path string) (func(), error) {
	release := func() {}
	if c.requests != nil {
		select {
		case c.requests.slot <- struct{}{}:
			release = func() { <-c.requests.slot }
		case <-ctx.Done():
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
		if wait := time.Until(c.requests.next); wait > 0 {
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
		if err := httpx.WaitXRequest(ctx); err != nil {
			if c.limiter != nil {
				c.limiter.refund(path)
			}
			release()
			return nil, err
		}
		c.requests.next = time.Now().Add(accountRequestInterval)
	}
	return release, nil
}
