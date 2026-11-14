package xscrape

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

const (
	// requestMaxAttempts 是单个账号上一次"逻辑请求"的瞬时错误重试次数（与 legacy 一致）。
	requestMaxAttempts = 3
	// forbiddenLock 是非 WAF 的 403 与无法确认的 401 的账号冷却。
	forbiddenLock = 5 * time.Minute
	// wafLock 是 WAF/挑战页的账号短退避。
	wafLock = 60 * time.Second
)

var (
	// maxPoolSelectWait 限制"所有账号都被锁"时的等待预算，覆盖 X 常见的 15 分钟窗口。
	maxPoolSelectWait = 15*time.Minute + 10*time.Second
	// maxPoolSelectStep 是单次等待上限，到点后重新评估锁状态。
	maxPoolSelectStep = 60 * time.Second
)

// GraphQL 实现 xapi.Requester：从账号池选号、限流/换号后返回响应体。
func (s *source) GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	payload, _, err := s.graphQLWithAccount(ctx, path, values, nil)
	return payload, err
}

// graphQLWithAccount 执行一次逻辑 GraphQL 请求，返回实际服务该请求的账号。
// excluded 是调用方已确认拿不到数据的账号（如目标账号受年龄限制）；失败换号时
// 会在它之外再累积本次请求的失败账号。
func (s *source) graphQLWithAccount(ctx context.Context, path string, values url.Values, excluded map[int64]bool) ([]byte, storage.XAccount, error) {
	queue := queueForPath(path)
	failed := map[int64]bool{}
	var lastErr error
	var lastAccount storage.XAccount
	for {
		if err := ctx.Err(); err != nil {
			return nil, storage.XAccount{}, err
		}
		skip := mergeExclusions(excluded, failed)
		account, release, err := s.engine.nextAccount(ctx, queue, skip)
		if err != nil {
			if lastErr != nil {
				// "全部限流"要保留为可分类错误，同时带上最后一次真实失败作为上下文。
				if xapi.IsAllClientsRateLimited(err) {
					return nil, lastAccount, fmt.Errorf("%w: %w", err, lastErr)
				}
				return nil, lastAccount, lastErr
			}
			return nil, storage.XAccount{}, err
		}
		lastAccount = account
		payload, err := s.executeAccountRequest(ctx, account, release, queue, http.MethodGet, path, values, nil, false)
		if err == nil {
			return payload, account, nil
		}
		lastErr = err
		failed[account.ID] = true
		if err := ctx.Err(); err != nil {
			return nil, account, err
		}
		if !shouldSwitchAccount(err) {
			return nil, account, err
		}
		active, countErr := s.engine.activeAccountCount(ctx)
		if countErr == nil && len(failed)+len(excluded) >= active {
			return nil, account, err
		}
	}
}

func shouldSwitchAccount(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, httpx.ErrBodyTooLarge) {
		return false
	}
	return xapi.IsPermanentClientError(err) || xapi.IsYieldableError(err) || xapi.IsTransientError(err)
}

func mergeExclusions(base, failed map[int64]bool) map[int64]bool {
	if len(failed) == 0 {
		return base
	}
	merged := make(map[int64]bool, len(base)+len(failed))
	for id := range base {
		merged[id] = true
	}
	for id := range failed {
		merged[id] = true
	}
	return merged
}

// nextAccount 选一个可执行请求的账号并占用它的内存执行槽。返回的 release 必须调用。
// 全部账号被锁时按预算等待；调度器上下文立即返回结构化限流错误。
func (e *Engine) nextAccount(ctx context.Context, queue string, excluded map[int64]bool) (storage.XAccount, func(), error) {
	if excluded == nil {
		excluded = map[int64]bool{}
	}
	excludeIDs := make([]int64, 0, len(excluded))
	for id := range excluded {
		excludeIDs = append(excludeIDs, id)
	}
	var waited time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return storage.XAccount{}, nil, err
		}
		account, ok, err := e.store.PickXAccount(ctx, queue, excludeIDs, e.now())
		if err != nil {
			return storage.XAccount{}, nil, err
		}
		if ok {
			_, release, slotWaited, err := e.slots.acquire(ctx, account.ID)
			if err != nil {
				return storage.XAccount{}, nil, err
			}
			if slotWaited {
				// 等待期间账号可能已被停用或加锁：重新读取后再决定是否使用。
				current, err := e.store.GetXAccount(ctx, account.ID)
				if err != nil {
					release()
					excluded[account.ID] = true
					excludeIDs = appendUnique(excludeIDs, account.ID)
					continue
				}
				locked, err := e.accountLocked(ctx, account.ID, queue)
				if err != nil {
					release()
					return storage.XAccount{}, nil, err
				}
				if !current.Active || locked {
					release()
					if !current.Active {
						excluded[account.ID] = true
						excludeIDs = appendUnique(excludeIDs, account.ID)
					}
					continue
				}
				account = current
			}
			return account, release, nil
		}

		accounts, err := e.store.ListXAccounts(ctx)
		if err != nil {
			return storage.XAccount{}, nil, err
		}
		active := 0
		available := 0
		for _, candidate := range accounts {
			if !candidate.Active {
				continue
			}
			active++
			if !excluded[candidate.ID] {
				available++
			}
		}
		if active == 0 {
			return storage.XAccount{}, nil, errors.New("没有可用 X Cookie")
		}
		if available == 0 {
			return storage.XAccount{}, nil, errors.New("没有可用于本次请求的 X Cookie")
		}
		until, found, err := e.store.EarliestXAccountUnlock(ctx, queue, excludeIDs, e.now())
		if err != nil {
			return storage.XAccount{}, nil, err
		}
		if !found {
			return storage.XAccount{}, nil, errors.New("没有可用于本次请求的 X Cookie")
		}
		wait := until.Sub(e.now())
		if wait <= 0 {
			wait = 200 * time.Millisecond
		}
		if xapi.YieldsToScheduler(ctx) {
			return storage.XAccount{}, nil, &xapi.RateLimitedError{Path: queue, RetryAt: until}
		}
		if wait > maxPoolSelectStep {
			wait = maxPoolSelectStep
		}
		if waited+wait > maxPoolSelectWait {
			return storage.XAccount{}, nil, &xapi.RateLimitedError{Path: queue, RetryAt: until}
		}
		waited += wait
		if err := e.sleep(ctx, wait); err != nil {
			return storage.XAccount{}, nil, err
		}
	}
}

func appendUnique(ids []int64, id int64) []int64 {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func (e *Engine) accountLocked(ctx context.Context, id int64, queue string) (bool, error) {
	locks, err := e.store.ListXAccountLocks(ctx, e.now())
	if err != nil {
		return false, err
	}
	for _, lock := range locks {
		if lock.AccountID == id && (lock.Queue == queue || lock.Queue == "*") {
			return true, nil
		}
	}
	return false, nil
}

func (e *Engine) activeAccountCount(ctx context.Context) (int, error) {
	accounts, err := e.store.ListXAccounts(ctx)
	if err != nil {
		return 0, err
	}
	active := 0
	for _, account := range accounts {
		if account.Active {
			active++
		}
	}
	return active, nil
}

// executeAccountRequest 在已占用的执行槽上执行一次逻辑请求（含瞬时错误重试）。
func (s *source) executeAccountRequest(ctx context.Context, account storage.XAccount, release func(), queue, method, path string, query, form url.Values, pinned bool) ([]byte, error) {
	if release != nil {
		defer release()
	}
	e := s.engine
	if pinned {
		_ = e.store.MarkXAccountUsed(ctx, account.ID, e.now())
	}
	proxy := e.proxyFor(account.Proxy)
	transport, err := e.transport(proxy)
	if err != nil {
		return nil, err
	}
	client := newHTTPClient(transport)
	slot := e.slots.slot(account.ID)
	extra, _ := account.ExtraCookieMap()

	attempt := 0
	retried404 := false
	for {
		if err := e.pace(ctx, slot); err != nil {
			return nil, err
		}
		rewritten := e.queryIDs.rewrite(path)
		request, err := newAccountRequest(ctx, e.baseURL, method, rewritten, query, form, account, extra)
		if err != nil {
			return nil, err
		}
		// xtid 生成器绑定全局代理（Engine 只保留一个）；账号级代理只影响请求本身，
		// 否则多账号不同代理时每次请求都会重建生成器并重新抓取首页脚本。
		e.applyTransactionID(ctx, request, e.generatorFor(s.proxy), method, rewritten)
		reply := doRequest(client, request)

		if reply.err != nil {
			if errors.Is(reply.err, httpx.ErrBodyTooLarge) || !xapi.IsTransientError(reply.err) {
				return nil, reply.err
			}
			if attempt+1 >= requestMaxAttempts {
				return nil, reply.err
			}
			if err := e.retrySleep(ctx, attempt); err != nil {
				return nil, err
			}
			attempt++
			continue
		}

		httpErr := &xapi.HTTPError{StatusCode: reply.status, Payload: xapi.LimitedErrorPayload(reply.payload)}

		switch {
		case reply.status == http.StatusOK:
			if apiErr := xapi.ParseAPIError(reply.payload); apiErr != nil {
				var parsed *xapi.APIError
				if errors.As(apiErr, &parsed) && parsed.Code == xapi.APIErrRateLimit {
					return nil, e.rateLimited(ctx, account.ID, queue, reply)
				}
				if errors.As(apiErr, &parsed) && xapi.IsFatalAPIErrorCode(parsed.Code) {
					e.deactivateAccount(ctx, account, parsed.Code)
					return nil, apiErr
				}
				return nil, apiErr
			}
			_ = e.store.IncrementXAccountStat(ctx, account.ID, queue, e.now())
			return reply.payload, nil

		case reply.status == http.StatusTooManyRequests:
			return nil, e.rateLimited(ctx, account.ID, queue, reply)

		case reply.status == http.StatusUnauthorized:
			e.confirmUnauthorized(ctx, account, proxy, extra)
			return nil, httpErr

		case reply.status == http.StatusForbidden:
			if xapi.LooksLikeWAFChallenge(reply.contentType(), reply.payload) {
				httpErr.WAFChallenge = true
				_ = e.store.LockXAccount(ctx, account.ID, "*", e.now().Add(wafLock), "WAF/挑战页")
				e.slowXRequests()
				return nil, fmt.Errorf("%w: %w", xapi.ErrWAFChallenge, httpErr)
			}
			_ = e.store.LockXAccount(ctx, account.ID, "*", e.now().Add(forbiddenLock), "HTTP 403")
			return nil, httpErr

		case reply.status == http.StatusNotFound:
			if _, _, ok := splitGraphQLPath(path); !ok {
				return nil, httpErr
			}
			e.invalidateTransactionIDs()
			e.queryIDs.refreshFor404(ctx, e, proxy)
			if !retried404 {
				retried404 = true
				continue
			}
			return nil, fmt.Errorf("%w: %w", xapi.ErrQueryIDStale, httpErr)

		case reply.status >= 500 || reply.status == http.StatusRequestTimeout || reply.status == 425:
			if attempt+1 >= requestMaxAttempts {
				return nil, httpErr
			}
			if err := e.retrySleep(ctx, attempt); err != nil {
				return nil, err
			}
			attempt++
			continue

		default:
			return nil, httpErr
		}
	}
}

func newAccountRequest(ctx context.Context, baseURL, method, path string, query, form url.Values, account storage.XAccount, extra map[string]string) (*http.Request, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	endpoint = endpoint.JoinPath(path)
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	var request *http.Request
	if body != nil {
		request, err = http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	} else {
		request, err = http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	}
	if err != nil {
		return nil, err
	}
	userAgent := strings.TrimSpace(account.UserAgent)
	if userAgent == "" {
		userAgent = xapi.UserAgent
	}
	request.Header.Set("Authorization", "Bearer "+xapi.GuestBearer())
	request.Header.Set("X-Csrf-Token", account.CSRFToken)
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/json")
	// 与浏览器会话/twscrape 保持一致；legacy 没设这三个头也能工作，但账号请求走
	// OAuth2 session 语义时它们能避免部分接口把请求当成匿名访问。
	request.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
	request.Header.Set("X-Twitter-Active-User", "yes")
	request.Header.Set("X-Twitter-Client-Language", "en")
	if strings.Contains(path, "/graphql/") {
		request.Header.Set("Content-Type", "application/json")
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	addAccountCookies(request, account.AuthToken, account.CSRFToken, extra)
	return request, nil
}

// rateLimited 记录按 queue 的限流锁并返回可分类的限流错误。
func (e *Engine) rateLimited(ctx context.Context, accountID int64, queue string, reply *response) error {
	reset := rateLimitReset(reply.header, e.now())
	_ = e.store.LockXAccount(ctx, accountID, queue, reset, "rate limit")
	e.slowXRequests()
	return &xapi.RateLimitedError{Path: queue, RetryAt: reset, Cause: &xapi.HTTPError{StatusCode: reply.status, Payload: xapi.LimitedErrorPayload(reply.payload)}}
}

// confirmUnauthorized 在接口 401 后用登录校验区分"Cookie 失效"与"目标/接口级 401"。
func (e *Engine) confirmUnauthorized(ctx context.Context, account storage.XAccount, proxy string, extra map[string]string) {
	if e.probes.recent(account.ID, e.now()) {
		return
	}
	result := e.probe(ctx, xapi.AccountCredentials{
		AuthToken:    account.AuthToken,
		CSRFToken:    account.CSRFToken,
		ExtraCookies: extra,
		UserAgent:    account.UserAgent,
		Proxy:        proxy,
	})
	if ctx.Err() != nil {
		return
	}
	switch {
	case result.Unauthorized:
		message := "X 拒绝该账号（HTTP 401）"
		if result.Err != nil {
			message = result.Err.Error()
		}
		_ = e.store.DeactivateXAccount(ctx, account.ID, message)
		e.probes.forget(account.ID)
	case result.Err == nil:
		e.probes.mark(account.ID, e.now())
	default:
		reason := "401 无法确认"
		if message := result.Err.Error(); message != "" {
			reason += ": " + message
		}
		_ = e.store.LockXAccount(ctx, account.ID, "*", e.now().Add(forbiddenLock), reason)
	}
}

// deactivateAccount 处理非 401 响应里的致命 API 错误码。
func (e *Engine) deactivateAccount(ctx context.Context, account storage.XAccount, code int) {
	_ = e.store.DeactivateXAccount(ctx, account.ID, fatalAccountMessage(code))
	e.probes.forget(account.ID)
}

func fatalAccountMessage(code int) string {
	switch code {
	case 32:
		return "X 拒绝该账号（code 32）"
	case 64:
		return "账号已被封禁（code 64）"
	case 89:
		return "Cookie 已失效（code 89）"
	case 215:
		return "Cookie 无效（code 215）"
	case xapi.APIErrAccountLocked:
		return "账号已锁定（code 326）"
	default:
		return fmt.Sprintf("X 拒绝该账号（code %d）", code)
	}
}

func queueForPath(path string) string {
	if _, operation, ok := splitGraphQLPath(path); ok {
		return operation
	}
	if trimmed := strings.TrimPrefix(path, "/"); trimmed != "" {
		return trimmed
	}
	return path
}

func requestRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	attempt = min(attempt, 3)
	base := 750 * time.Millisecond * time.Duration(1<<attempt)
	return base + time.Duration(rand.Int63n(int64(base/2)+1))
}

// retrySleep 是瞬时错误重试前的退避；DisablePacing 时直接返回，便于测试。
func (e *Engine) retrySleep(ctx context.Context, attempt int) error {
	if e.opts.DisablePacing {
		return ctx.Err()
	}
	return e.sleep(ctx, requestRetryDelay(attempt))
}

// slowXRequests 在限流/挑战页后让进程内所有 X 请求整体慢下来。
func (e *Engine) slowXRequests() {
	if !e.opts.DisablePacing {
		httpx.SlowXRequests()
	}
}
