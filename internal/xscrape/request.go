package xscrape

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
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
	// maxRateLimitedRetriesPerAccount 限制同一账号在单次逻辑请求里因限流等待后重试的次数，
	// 避免解锁后持续 429 时无限循环。
	maxRateLimitedRetriesPerAccount = 2
	// maxTOCTOURetries 限制 PickXAccount 与 EarliestXAccountUnlock 之间锁刚好过期时的重试次数。
	maxTOCTOURetries = 3
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
	// proxyDialLock 是池化账号遇到死代理/建连超时后的短冷却时间，避免后续请求反复撞死代理。
	proxyDialLock = 2 * time.Minute
)

// GraphQL 实现 xapi.Requester：从账号池选号、限流/换号后返回响应体。
func (s *source) GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	payload, _, err := s.graphQLWithAccount(ctx, path, values, nil)
	return payload, err
}

// graphQLWithAccount 执行一次逻辑 GraphQL 请求，返回实际服务该请求的账号。
// excluded 是调用方已确认拿不到数据的账号（如目标账号受年龄限制）；失败换号时
// 会在它之外再累积本次请求的非限流失败账号。限流账号通过持久化锁与独立计数跟踪，
// 不塞进 failed，以免绕过非调度器上下文的解锁等待。
func (s *source) graphQLWithAccount(ctx context.Context, path string, values url.Values, excluded map[int64]bool) ([]byte, storage.XAccount, error) {
	queue := queueForPath(path)
	failed := map[int64]bool{}
	dialLocked := map[int64]bool{}
	rateLimited := map[int64]int{}
	validAccount401s := 0
	var lastErr error
	var lastAccount storage.XAccount
	for {
		if err := ctx.Err(); err != nil {
			return nil, storage.XAccount{}, err
		}
		skip := mergeExclusions(excluded, failed, dialLocked, rateLimited)
		account, release, err := s.engine.nextAccount(ctx, queue, skip)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, storage.XAccount{}, ctxErr
			}
			if lastErr != nil {
				// 仅当所有未被调用方排除的健康活跃账号确实都在限流中（不含 500/401 等非限流
				// 失败但仍活跃的账号；死代理冷却账号仅在存在至少一个真实限流账号时不阻塞池限流上报）
				// 时，才上报 PoolExhausted=true。
				if until, ok := s.engine.allHealthyAccountsRateLimited(ctx, queue, excluded, failed, dialLocked); ok {
					return nil, lastAccount, &xapi.RateLimitedError{Path: queue, RetryAt: until, Cause: lastErr, PoolExhausted: true}
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, account, ctxErr
		}
		var dialTimeout *proxyDialTimeoutError
		if xapi.IsRateLimitError(err) || xapi.IsAllClientsRateLimited(err) {
			rateLimited[account.ID]++
			// 限流通常已写入账号锁；若写锁失败导致账号未锁定，退回本次请求级排除，避免死循环。
			if locked, lockErr := s.engine.accountLocked(ctx, account.ID, queue); lockErr != nil || !locked {
				failed[account.ID] = true
			}
		} else if errors.As(err, &dialTimeout) {
			dialLocked[account.ID] = true
		} else {
			failed[account.ID] = true
			// 若接口返回 401 但 confirmUnauthorized 已确认该账号 Cookie 有效，
			// 说明大概率是目标/游标级 401；最多允许换 1 个账号确认视角，避免 N 账号池整池风暴。
			if xapi.IsUnauthorized(err) && s.engine.probes.recent(account.ID, s.engine.now()) {
				validAccount401s++
				if validAccount401s >= 2 {
					return nil, account, err
				}
			}
		}
		if !shouldSwitchAccount(ctx, err) {
			return nil, account, err
		}
	}
}

func isDeactivatingAPICode(code int) bool {
	return xapi.IsFatalAPIErrorCode(code) || code == 353
}

func shouldSwitchAccount(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// 调用方 ctx 未超时但单次请求超时（例如坏代理挂起），允许切换其他账号。
		return true
	}
	if errors.Is(err, httpx.ErrBodyTooLarge) {
		return false
	}
	var apiErr *xapi.APIError
	if errors.As(err, &apiErr) && apiErr.Code == 353 {
		return true
	}
	return xapi.IsPermanentClientError(err) || xapi.IsYieldableError(err) || xapi.IsTransientError(err)
}

func mergeExclusions(base, failed, dialLocked map[int64]bool, rateLimited map[int64]int) map[int64]bool {
	exhaustedRL := 0
	for _, count := range rateLimited {
		if count >= maxRateLimitedRetriesPerAccount {
			exhaustedRL++
		}
	}
	if len(failed) == 0 && len(dialLocked) == 0 && exhaustedRL == 0 {
		return base
	}
	merged := make(map[int64]bool, len(base)+len(failed)+len(dialLocked)+exhaustedRL)
	for id := range base {
		merged[id] = true
	}
	for id := range failed {
		merged[id] = true
	}
	for id := range dialLocked {
		merged[id] = true
	}
	for id, count := range rateLimited {
		if count >= maxRateLimitedRetriesPerAccount {
			merged[id] = true
		}
	}
	return merged
}

// allHealthyAccountsRateLimited 仅在所有未被调用方排除的健康活跃账号都没有非限流失败
// 且当前全部处于有效限流锁状态时，返回最早解锁时间和 true。
// 遇到坏代理冷却（dialLocked）的账号如果池里还有其他被限流的健康账号，则视为已锁账号不阻断池限流判断；
// 若所有账号都是坏代理冷却而没有真实限流账号，返回 false 以直接上抛代理错误。
func (e *Engine) allHealthyAccountsRateLimited(ctx context.Context, queue string, excluded, failed, dialLocked map[int64]bool) (time.Time, bool) {
	accounts, err := e.store.ListXAccounts(ctx)
	if err != nil {
		return time.Time{}, false
	}
	locks, err := e.store.ListXAccountLocks(ctx, e.now())
	if err != nil {
		return time.Time{}, false
	}
	lockUntil := make(map[int64]time.Time, len(locks))
	for _, lock := range locks {
		if lock.Queue == queue || lock.Queue == "*" {
			if lock.Until.After(lockUntil[lock.AccountID]) {
				lockUntil[lock.AccountID] = lock.Until
			}
		}
	}
	var earliest time.Time
	rateLimitedHealthy := 0
	for _, acc := range accounts {
		if !acc.Active || excluded[acc.ID] {
			continue
		}
		if failed[acc.ID] {
			return time.Time{}, false
		}
		until, locked := lockUntil[acc.ID]
		if !locked {
			return time.Time{}, false
		}
		if !dialLocked[acc.ID] {
			rateLimitedHealthy++
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	if rateLimitedHealthy == 0 {
		return time.Time{}, false
	}
	return earliest, true
}

// nextAccount 选一个可执行请求的账号并占用它的内存执行槽。返回的 release 必须调用。
// 全部账号被锁时按预算等待；调度器上下文立即返回结构化限流错误。
func (e *Engine) nextAccount(ctx context.Context, queue string, excluded map[int64]bool) (storage.XAccount, func(), error) {
	if excluded == nil {
		excluded = map[int64]bool{}
	} else {
		excluded = maps.Clone(excluded)
	}
	excludeIDs := make([]int64, 0, len(excluded))
	for id := range excluded {
		excludeIDs = append(excludeIDs, id)
	}
	var waited time.Duration
	toctouRetries := 0
	for {
		if err := ctx.Err(); err != nil {
			return storage.XAccount{}, nil, err
		}
		now := e.now()
		account, ok, err := e.pickAccountWithPrimaryReserve(ctx, queue, excludeIDs, excluded, now)
		if err != nil {
			return storage.XAccount{}, nil, err
		}
		if ok {
			_, release, slotWaited, err := e.slots.acquire(ctx, account.ID)
			if err != nil {
				return storage.XAccount{}, nil, err
			}
			if slotWaited {
				// 等待期间账号可能已被停用、删除或加锁：重新读取后再决定是否使用。
				// 仅在账号确实不存在（sql.ErrNoRows）时排除重试；瞬时 DB 错误直接上报。
				current, err := e.store.GetXAccount(ctx, account.ID)
				if err != nil {
					release()
					if errors.Is(err, sql.ErrNoRows) {
						excluded[account.ID] = true
						excludeIDs = appendUnique(excludeIDs, account.ID)
						continue
					}
					return storage.XAccount{}, nil, err
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
			// PickXAccount 与 EarliestXAccountUnlock 之间锁可能刚好过期，重选一次。
			if toctouRetries < maxTOCTOURetries {
				toctouRetries++
				continue
			}
			return storage.XAccount{}, nil, errors.New("没有可用于本次请求的 X Cookie")
		}
		toctouRetries = 0
		wait := until.Sub(e.now())
		if wait <= 0 {
			wait = 200 * time.Millisecond
		}
		if xapi.YieldsToScheduler(ctx) {
			return storage.XAccount{}, nil, &xapi.RateLimitedError{Path: queue, RetryAt: until, PoolExhausted: true}
		}
		if wait > maxPoolSelectStep {
			wait = maxPoolSelectStep
		}
		if waited+wait > maxPoolSelectWait {
			return storage.XAccount{}, nil, &xapi.RateLimitedError{Path: queue, RetryAt: until, PoolExhausted: true}
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

// pickAccountWithPrimaryReserve 在主账号当前 queue 剩余配额偏低时，优先从非主账号中选取，
// 为私密用户和关注请求保留主账号配额；若所有非主账号均不可用，再回退到包含主账号的完整池。
func (e *Engine) pickAccountWithPrimaryReserve(ctx context.Context, queue string, excludeIDs []int64, excluded map[int64]bool, now time.Time) (storage.XAccount, bool, error) {
	if accounts, err := e.store.ListXAccounts(ctx); err == nil {
		var primaryID int64
		nonPrimaryActive := 0
		for _, acc := range accounts {
			if !acc.Active || excluded[acc.ID] {
				continue
			}
			if acc.IsPrimary {
				primaryID = acc.ID
			} else {
				nonPrimaryActive++
			}
		}
		if primaryID != 0 && nonPrimaryActive > 0 && e.rateLimits.isLowQuota(primaryID, queue, now) {
			withPrimaryExcluded := append(append([]int64(nil), excludeIDs...), primaryID)
			account, ok, err := e.store.PickXAccount(ctx, queue, withPrimaryExcluded, now)
			if err != nil || ok {
				return account, ok, err
			}
		}
	}
	return e.store.PickXAccount(ctx, queue, excludeIDs, now)
}

func (e *Engine) accountLockUntil(ctx context.Context, id int64, queue string) (bool, time.Time, error) {
	locks, err := e.store.ListXAccountLocks(ctx, e.now())
	if err != nil {
		return false, time.Time{}, err
	}
	var latest time.Time
	locked := false
	for _, lock := range locks {
		if lock.AccountID == id && (lock.Queue == queue || lock.Queue == "*") {
			locked = true
			if lock.Until.After(latest) {
				latest = lock.Until
			}
		}
	}
	return locked, latest, nil
}

func (e *Engine) accountLocked(ctx context.Context, id int64, queue string) (bool, error) {
	locked, _, err := e.accountLockUntil(ctx, id, queue)
	return locked, err
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
	metaProxy := e.metaProxyFor(account.Proxy)
	if !e.opts.DisablePacing {
		if _, _, ok := splitGraphQLPath(path); ok {
			e.queryIDs.ensureFresh(ctx, e, metaProxy)
		}
	}
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
		if err := e.pace(ctx, slot, proxy); err != nil {
			return nil, err
		}
		rewritten := e.queryIDs.rewrite(path)
		request, err := newAccountRequest(ctx, e.baseURL, method, rewritten, query, form, account, extra)
		if err != nil {
			return nil, err
		}
		// xtid 生成器优先绑定全局代理；未配置全局代理时复用已选中的账号代理并保持粘性，
		// 避免多账号不同代理时每次请求重建生成器。
		e.applyTransactionID(ctx, request, e.generatorFor(metaProxy), method, rewritten)
		reply := doRequest(client, request)

		if reply.err != nil {
			var dialTimeout *proxyDialTimeoutError
			if !pinned && errors.As(reply.err, &dialTimeout) {
				// 池化请求遇到死代理/建连超时：短冷却该账号后立即交还池子换号，
				// 避免后续请求每轮重复撞死代理。
				_ = e.store.LockXAccount(ctx, account.ID, "*", e.now().Add(proxyDialLock), "代理连接超时")
				_ = e.store.SetXAccountError(ctx, account.ID, "代理连接超时")
				return nil, reply.err
			}
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

		// 非 200/401 的响应也可能带致命 API 错误码（X 有时用 400/403 包装 32/64/89/215/326）。
		// 这些错误码说明账号本身不可用，必须停用而不是只做 403 冷却。
		if reply.status != http.StatusOK && reply.status != http.StatusUnauthorized {
			if code, _ := probeErrorCode(reply.payload); code != 0 {
				if code == xapi.APIErrRateLimit {
					return nil, e.rateLimited(ctx, account.ID, queue, proxy, reply, pinned)
				}
				if isDeactivatingAPICode(code) {
					e.deactivateAccount(ctx, account, code)
					return nil, &xapi.APIError{Code: code, Raw: xapi.LimitedErrorPayload(reply.payload)}
				}
			}
		}

		switch {
		case reply.status == http.StatusOK:
			if apiErr := xapi.ParseAPIError(reply.payload); apiErr != nil {
				var parsed *xapi.APIError
				if errors.As(apiErr, &parsed) && parsed.Code == xapi.APIErrRateLimit {
					return nil, e.rateLimited(ctx, account.ID, queue, proxy, reply, pinned)
				}
				if errors.As(apiErr, &parsed) && isDeactivatingAPICode(parsed.Code) {
					e.deactivateAccount(ctx, account, parsed.Code)
					return nil, apiErr
				}
				if xapi.IsTransientError(apiErr) {
					if attempt+1 >= requestMaxAttempts {
						return nil, apiErr
					}
					if err := e.retrySleep(ctx, attempt); err != nil {
						return nil, err
					}
					attempt++
					continue
				}
				return nil, apiErr
			}
			e.recordRateLimit(ctx, account.ID, queue, reply.header)
			_ = e.store.IncrementXAccountStat(ctx, account.ID, queue, e.now())
			return reply.payload, nil

		case reply.status == http.StatusTooManyRequests:
			return nil, e.rateLimited(ctx, account.ID, queue, proxy, reply, pinned)

		case reply.status == http.StatusUnauthorized:
			e.confirmUnauthorized(ctx, account, proxy, extra)
			return nil, httpErr

		case reply.status == http.StatusForbidden:
			if xapi.LooksLikeWAFChallenge(reply.contentType(), reply.payload) {
				httpErr.WAFChallenge = true
				_ = e.store.LockXAccount(ctx, account.ID, "*", e.now().Add(wafLock), "WAF/挑战页")
				e.slowXRequestsForProxy(proxy)
				return nil, fmt.Errorf("%w: %w", xapi.ErrWAFChallenge, httpErr)
			}
			_ = e.store.LockXAccount(ctx, account.ID, "*", e.now().Add(forbiddenLock), "HTTP 403")
			return nil, httpErr

		case reply.status == http.StatusNotFound:
			e.recordRateLimit(ctx, account.ID, queue, reply.header)
			if _, _, ok := splitGraphQLPath(path); !ok {
				return nil, httpErr
			}
			if !retried404 {
				tidInvalidated := e.invalidateTransactionIDs()
				refreshed := e.queryIDs.refreshFor404(ctx, e, proxy)
				retried404 = true
				if tidInvalidated || refreshed || e.queryIDs.rewrite(path) != rewritten {
					continue
				}
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
	request.Header.Set("Referer", "https://x.com/")
	if method == http.MethodPost {
		request.Header.Set("Origin", "https://x.com")
	}
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

// recordRateLimit 解析响应的 X-Rate-Limit-* 头并更新内存配额视图；
// 剩余配额耗尽时提前为该账号写限流锁，且不触发出口级降速。
func (e *Engine) recordRateLimit(ctx context.Context, accountID int64, queue string, header http.Header) {
	now := e.now()
	reset, shouldLock := e.rateLimits.recordHeaders(accountID, queue, header, now)
	if shouldLock && reset.After(now) {
		_ = e.store.LockXAccount(ctx, accountID, queue, reset, "quota exhausted")
	}
}

// rateLimited 记录按 queue 的限流锁并返回可分类的限流错误。
func (e *Engine) rateLimited(ctx context.Context, accountID int64, queue, proxy string, reply *response, poolExhausted bool) error {
	now := e.now()
	reset := rateLimitReset(reply.header, now)
	e.rateLimits.markExhausted(accountID, queue, reply.header, reset, now)
	_ = e.store.LockXAccount(ctx, accountID, queue, reset, "rate limit")
	// 仅在固定账号请求或同代理下已无其他未锁定可用账号时才对该代理出口降速，
	// 避免多账号池中单个账号用满个人窗口就拖慢同代理上其余健康账号 15 分钟。
	if poolExhausted || !e.hasOtherUnlockedAccountOnProxy(ctx, accountID, queue, proxy, now) {
		e.slowXRequestsForProxy(proxy)
	}
	return &xapi.RateLimitedError{
		Path:          queue,
		RetryAt:       reset,
		Cause:         &xapi.HTTPError{StatusCode: reply.status, Payload: xapi.LimitedErrorPayload(reply.payload)},
		PoolExhausted: poolExhausted,
	}
}

func (e *Engine) hasOtherUnlockedAccountOnProxy(ctx context.Context, excludeID int64, queue, proxy string, now time.Time) bool {
	accounts, err := e.store.ListXAccounts(ctx)
	if err != nil {
		return false
	}
	locks, err := e.store.ListXAccountLocks(ctx, now)
	if err != nil {
		return false
	}
	locked := make(map[int64]bool, len(locks))
	for _, lock := range locks {
		if lock.Queue == queue || lock.Queue == "*" {
			locked[lock.AccountID] = true
		}
	}
	targetScope := xRequestScope(proxy)
	for _, acc := range accounts {
		if !acc.Active || acc.ID == excludeID || locked[acc.ID] {
			continue
		}
		if xRequestScope(e.proxyFor(acc.Proxy)) == targetScope {
			return true
		}
	}
	return false
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
		_ = e.store.SetXAccountError(ctx, account.ID, reason)
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
	case 353:
		return "ct0 与 auth_token 不匹配（code 353）"
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

// slowXRequestsForProxy 在限流/挑战页后仅让对应代理出口的 X 请求慢下来，
// 不影响使用其他独立代理的账号。
func (e *Engine) slowXRequestsForProxy(proxy string) {
	if !e.opts.DisablePacing {
		httpx.SlowXRequestsForScope(xRequestScope(proxy))
	}
}
