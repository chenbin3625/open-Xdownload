package xscrape

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/tidwall/gjson"
)

const (
	// guestTokenTTL 是 guest token 的缓存时长。
	guestTokenTTL = time.Hour
	// guestTokenFetchTimeout 是后台共享激活 guest token 的超时上限。
	guestTokenFetchTimeout = 30 * time.Second
	// rateLimitFallback 是响应未带 x-rate-limit-reset 时的默认锁定时长。
	rateLimitFallback = time.Minute
	// maxRateLimitWait 限制异常或畸形限流头导致超长锁定。
	maxRateLimitWait = 24 * time.Hour
)

type guestTokenCall struct {
	done  chan struct{}
	token string
	err   error
}

// guestTokens 缓存 guest token。并发请求共享同一次激活请求及其结果。
type guestTokens struct {
	mu       sync.Mutex
	token    string
	expires  time.Time
	fetching *guestTokenCall
}

// guestToken 返回缓存中的 token，过期时重新激活。
func (e *Engine) guestToken(ctx context.Context, proxy string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	e.guest.mu.Lock()
	if e.guest.token != "" && e.now().Before(e.guest.expires) {
		token := e.guest.token
		e.guest.mu.Unlock()
		return token, nil
	}
	if call := e.guest.fetching; call != nil {
		e.guest.mu.Unlock()
		select {
		case <-call.done:
			return call.token, call.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	call := &guestTokenCall{done: make(chan struct{})}
	e.guest.fetching = call
	e.guest.mu.Unlock()

	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guestTokenFetchTimeout)
	go func() {
		defer cancel()
		var token string
		var ttl time.Duration
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%w: panic: %v", errFetchGuestToken, r)
			}
			e.guest.mu.Lock()
			if err == nil {
				e.guest.token = token
				e.guest.expires = e.now().Add(ttl)
			}
			if e.guest.fetching == call {
				e.guest.fetching = nil
			}
			call.token, call.err = token, err
			close(call.done)
			e.guest.mu.Unlock()
		}()
		token, ttl, err = e.fetchGuestToken(fetchCtx, proxy)
	}()

	select {
	case <-call.done:
		return call.token, call.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (e *Engine) invalidateGuestToken(failedToken string) {
	e.guest.mu.Lock()
	if e.guest.token == failedToken {
		e.guest.token = ""
		e.guest.expires = time.Time{}
	}
	e.guest.mu.Unlock()
}

func (e *Engine) fetchGuestToken(ctx context.Context, proxy string) (string, time.Duration, error) {
	transport, err := e.transport(proxy)
	if err != nil {
		return "", 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apiBaseURL+"/1.1/guest/activate.json", nil)
	if err != nil {
		return "", 0, err
	}
	request.Header.Set("Authorization", "Bearer "+xapi.GuestBearer())
	request.Header.Set("User-Agent", xapi.UserAgent)
	request.Header.Set("Accept", "application/json")
	if err := e.paceProcess(ctx); err != nil {
		return "", 0, err
	}
	reply := doRequest(newHTTPClient(transport), request)
	if reply.err != nil {
		return "", 0, fmt.Errorf("%w: %v", errFetchGuestToken, reply.err)
	}
	if reply.status >= 400 {
		return "", 0, fmt.Errorf("%w: HTTP %d %s", errFetchGuestToken, reply.status, xapi.LimitedErrorPayload(reply.payload))
	}
	token := gjson.GetBytes(reply.payload, "guest_token").String()
	if token == "" {
		return "", 0, fmt.Errorf("%w: 响应中没有 guest_token", errFetchGuestToken)
	}
	return token, guestTokenTTL, nil
}

// guestRequester 让 xapi 的纯解析/分页函数在无 active 账号时以 guest 身份请求。
// 只有 FetchTweet 会使用它；其他需要账号视角的接口直接返回"没有可用 X Cookie"。
type guestRequester struct {
	source *source
}

func (g guestRequester) GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return g.source.guestGraphQL(ctx, path, values)
}

func (s *source) guestGraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	token, err := s.engine.guestToken(ctx, s.proxy)
	if err != nil {
		return nil, err
	}
	refreshed := false
	retried404 := false
	transient := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reply, err := s.doGuestRequest(ctx, path, values, token)
		if err != nil {
			if transient < 2 && xapi.IsTransientError(err) {
				transient++
				if err := s.engine.retrySleep(ctx, transient-1); err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}
		switch {
		case reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden:
			if refreshed {
				return nil, guestHTTPError(reply)
			}
			// guest token 可能已过期：换一个再试一次。
			refreshed = true
			s.engine.invalidateGuestToken(token)
			token, err = s.engine.guestToken(ctx, s.proxy)
			if err != nil {
				return nil, err
			}
			continue
		case reply.status == http.StatusNotFound:
			if retried404 {
				return nil, fmt.Errorf("%w: %w", xapi.ErrQueryIDStale, guestHTTPError(reply))
			}
			retried404 = true
			s.engine.invalidateTransactionIDs()
			s.engine.queryIDs.refreshFor404(ctx, s.engine, s.proxy)
			continue
		case reply.status == http.StatusTooManyRequests:
			s.engine.slowXRequests()
			return nil, &xapi.RateLimitedError{Path: queueForPath(path), RetryAt: rateLimitReset(reply.header, s.engine.now()), Cause: guestHTTPError(reply)}
		case reply.status >= 500 || reply.status == http.StatusRequestTimeout || reply.status == 425:
			if transient < 2 {
				transient++
				if err := s.engine.retrySleep(ctx, transient-1); err != nil {
					return nil, err
				}
				continue
			}
			return nil, guestHTTPError(reply)
		case reply.status >= 400:
			return nil, guestHTTPError(reply)
		}
		if apiErr := xapi.ParseAPIError(reply.payload); apiErr != nil {
			var parsed *xapi.APIError
			if errors.As(apiErr, &parsed) && parsed.Code == xapi.APIErrRateLimit {
				s.engine.slowXRequests()
				return nil, &xapi.RateLimitedError{Path: queueForPath(path), RetryAt: rateLimitReset(reply.header, s.engine.now()), Cause: apiErr}
			}
			if transient < 2 && xapi.IsTransientError(apiErr) {
				transient++
				if err := s.engine.retrySleep(ctx, transient-1); err != nil {
					return nil, err
				}
				continue
			}
			return nil, apiErr
		}
		return reply.payload, nil
	}
}

func (s *source) doGuestRequest(ctx context.Context, path string, values url.Values, token string) (*response, error) {
	if !s.engine.opts.DisablePacing {
		if _, _, ok := splitGraphQLPath(path); ok {
			s.engine.queryIDs.ensureFresh(ctx, s.engine, s.proxy)
		}
	}
	transport, err := s.engine.transport(s.proxy)
	if err != nil {
		return nil, err
	}
	rewritten := s.engine.queryIDs.rewrite(path)
	endpoint, err := url.Parse(s.engine.baseURL)
	if err != nil {
		return nil, err
	}
	endpoint = endpoint.JoinPath(rewritten)
	if values != nil {
		endpoint.RawQuery = values.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+xapi.GuestBearer())
	request.Header.Set("X-Guest-Token", token)
	request.Header.Set("User-Agent", xapi.UserAgent)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	s.engine.applyTransactionID(ctx, request, s.engine.generatorFor(s.proxy), http.MethodGet, rewritten)

	if err := s.engine.paceProcess(ctx); err != nil {
		return nil, err
	}
	reply := doRequest(newHTTPClient(transport), request)
	if reply.err != nil {
		return nil, reply.err
	}
	return reply, nil
}

func guestHTTPError(reply *response) error {
	httpErr := &xapi.HTTPError{StatusCode: reply.status, Payload: xapi.LimitedErrorPayload(reply.payload)}
	if reply.status == http.StatusForbidden && xapi.LooksLikeWAFChallenge(reply.contentType(), reply.payload) {
		httpErr.WAFChallenge = true
	}
	return httpErr
}

// rateLimitReset 解析 x-rate-limit-reset（unix 秒或毫秒）与 Retry-After，缺失时用默认值，并限制最长 24 小时。
func rateLimitReset(header http.Header, now time.Time) time.Time {
	now = now.UTC()
	maxReset := now.Add(maxRateLimitWait)
	if header != nil {
		if raw := strings.TrimSpace(header.Get("X-Rate-Limit-Reset")); raw != "" {
			if unix, err := strconv.ParseInt(raw, 10, 64); err == nil && unix > 0 {
				if unix > 1_000_000_000_000 {
					if unix >= maxReset.UnixMilli() {
						return maxReset
					}
					if reset := time.UnixMilli(unix).UTC(); reset.After(now) {
						return reset
					}
				} else {
					if unix >= maxReset.Unix() {
						return maxReset
					}
					if reset := time.Unix(unix, 0).UTC(); reset.After(now) {
						return reset
					}
				}
			} else if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(raw, "-") {
				return maxReset
			}
		}
		if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
			if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
				maxSeconds := int64(maxRateLimitWait / time.Second)
				if seconds > maxSeconds {
					seconds = maxSeconds
				}
				return now.Add(time.Duration(seconds) * time.Second)
			} else if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(raw, "-") {
				return maxReset
			}
			if at, err := http.ParseTime(raw); err == nil {
				at = at.UTC()
				if at.After(now) {
					if at.After(maxReset) {
						return maxReset
					}
					return at
				}
			}
		}
	}
	return now.Add(rateLimitFallback)
}
