package xclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// defaultGuestBearerValue 是 X 的公开 guest token（公开已知常量）。
const defaultGuestBearerValue = "AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"

// guestBearer 每次进程启动读取一次；可用环境变量 OPEN_XDOWNLOAD_GUEST_BEARER 覆盖，
// 以便官方 token 变更时无需改代码即可轮换（S4）。
var guestBearer = defaultGuestBearer()

func defaultGuestBearer() string {
	if value := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_GUEST_BEARER")); value != "" {
		return value
	}
	return defaultGuestBearerValue
}

// screenNamePattern 用于从首页识别登录账号；包级编译，避免每次调用重复编译正则（M3）。
var screenNamePattern = regexp.MustCompile(`"screen_name":"(\S+?)"`)

const requestMaxAttempts = 3

// requestTotalBudget 是一次“逻辑请求”跨 Cookie 重试的总耗时上限。次数预算见
// newRequestBudget：所有 Cookie 合计最多 requestMaxAttempts 次尝试。
const requestTotalBudget = 5 * time.Minute

type requestBudget struct {
	mu        sync.Mutex
	remaining int
	deadline  time.Time
	perClient int
}

func newRequestBudget(attempts int, timeout time.Duration) *requestBudget {
	if attempts < 1 {
		attempts = 1
	}
	return &requestBudget{remaining: attempts, deadline: time.Now().Add(timeout), perClient: requestMaxAttempts}
}

func (b *requestBudget) acquire() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.remaining <= 0 || time.Now().After(b.deadline) {
		return false
	}
	b.remaining--
	return true
}

type requestBudgetKey struct{}

func withRequestBudget(ctx context.Context, budget *requestBudget) context.Context {
	return context.WithValue(ctx, requestBudgetKey{}, budget)
}

func requestBudgetFrom(ctx context.Context) *requestBudget {
	budget, _ := ctx.Value(requestBudgetKey{}).(*requestBudget)
	return budget
}

func (c *Client) rateLimitedError(path string, cause error) error {
	retryAt := time.Now().Add(time.Minute)
	if c != nil && c.limiter != nil {
		if reset, ok := c.limiter.blockedReset(path); ok && reset.After(time.Now()) {
			retryAt = reset
		}
	}
	return &RateLimitedError{Path: path, RetryAt: retryAt, Cause: cause}
}

// clientDisableCooldown 是认证失败（/home 也确认 401、账号锁定）后客户端被禁用的时长。
// 冷却到期后下一次请求会重新探测，真正失效的 Cookie 会再次被禁用，因此不需要锁太久。
var clientDisableCooldown = 10 * time.Minute

// clientUnverifiedDisableCooldown 用于接口 401 但 /home 校验无法给出结论（网络错误、
// 挑战页、限流）的情况：既不能确认失效，也不能放任继续撞接口。
var clientUnverifiedDisableCooldown = 5 * time.Minute

// homeVerifiedTTL 内 /home 校验通过过，接口 401 直接视为接口/目标级问题，不重复探测。
const homeVerifiedTTL = 2 * time.Minute

// clientForbiddenDisableCooldown 是非 WAF 的 403 被禁用时的较短冷却，避免一次误封把
// 唯一 Cookie 锁 30 分钟。
var clientForbiddenDisableCooldown = 5 * time.Minute

// wafBackoffDuration 是 WAF 挑战页命中后的账号级短退避，不 disable。
const wafBackoffDuration = 60 * time.Second

// homeCheckPath 是 /home 登录自检在本地限流器里的 path key。
const homeCheckPath = "/home"

type Credentials struct {
	AuthToken string
	CSRFToken string
}

type Client struct {
	http         *http.Client
	baseURL      string
	retryBackoff func(attempt int) time.Duration
	authToken    string
	csrfToken    string
	limiter      *rateLimiter
	requests     *accountRequestLimit
	screen       string
	mu           sync.Mutex
	// disabledUntil 非零且晚于当前时间时客户端不可用。禁用带冷却期而不是永久：403 也可能
	// 来自 WAF 等瞬时拦截，Pool 按配置长期缓存，永久禁用会让主 Cookie 直到重启都不可用。
	disabledUntil time.Time
	lastError     string
	// homeVerifiedAt 是最近一次 /home 登录校验成功的时间。
	homeVerifiedAt time.Time
	requestCount   atomic.Int64
}

type Pool struct {
	clients []*Client
	next    int
	mu      sync.Mutex
	// primaryCredentials/proxyURL 用于 FetchTweet：与包级 FetchTweet 一致，
	// 每次用主 Cookie 新建客户端直接请求，不经过 Select 轮换、也不受主账号
	// 临时禁用状态影响。
	primaryCredentials *Credentials
	proxyURL           string
}

func NewPool(cfg config.AppConfig) (*Pool, error) {
	if strings.TrimSpace(cfg.AuthToken) == "" || strings.TrimSpace(cfg.CSRFToken) == "" {
		return nil, errors.New("需要先配置 auth_token 和 ct0 才能调用 X GraphQL")
	}
	creds := []Credentials{{AuthToken: cfg.AuthToken, CSRFToken: cfg.CSRFToken}}
	creds = append(creds, ParseAdditionalCookies(cfg.AdditionalCookies)...)
	clients := make([]*Client, 0, len(creds))
	seen := map[string]struct{}{}
	for _, cred := range creds {
		if cred.AuthToken == "" || cred.CSRFToken == "" {
			continue
		}
		key := cred.AuthToken + "\x00" + cred.CSRFToken
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		client, err := NewClient(cred, cfg.ProxyURL)
		if err != nil {
			return nil, err
		}
		clients = append(clients, client)
	}
	if len(clients) == 0 {
		return nil, errors.New("没有可用 Cookie")
	}
	return &Pool{
		clients:            clients,
		primaryCredentials: &Credentials{AuthToken: cfg.AuthToken, CSRFToken: cfg.CSRFToken},
		proxyURL:           cfg.ProxyURL,
	}, nil
}

// sameOrigin checks the scheme, hostname and effective port before forwarding credentials.
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || !strings.EqualFold(a.Scheme, b.Scheme) || !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		if strings.EqualFold(u.Scheme, "http") {
			return "80"
		}
		return ""
	}
	return port(a) == port(b)
}

// checkRedirect 在跟随重定向前校验目标：拒绝 HTTPS 降级、跨源与超过 10 次跳转。
// 标准库会在跨域重定向时剥离 Cookie/Authorization，但 X-Csrf-Token 是自定义头，
// 会被原样带到重定向目标——ct0 因此可能泄漏给第三方主机；同时拒绝跨域跳转也避免
// 被重定向到内网地址。独立成函数便于测试直接驱动 CheckRedirect。
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("重定向次数过多")
	}
	if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("拒绝 HTTPS 降级重定向: %s", req.URL)
	}
	if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
		return fmt.Errorf("拒绝跨域重定向: %s", req.URL.Host)
	}
	return nil
}

// newHTTPClient 构造 X API 使用的 http.Client（90s 总超时 + 重定向防护）。
func newHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport:     transport,
		Timeout:       90 * time.Second,
		CheckRedirect: checkRedirect,
	}
}

func NewClient(cred Credentials, proxyURL string) (*Client, error) {
	// 复用带连接池与链路本地地址拨号防护的 Transport（M1/S2）；空代理时尊重环境变量代理。
	transport, err := httpx.Transport(proxyURL)
	if err != nil {
		return nil, err
	}
	requests := requestLimitForAccount(cred.AuthToken)
	return &Client{
		http:         newHTTPClient(transport),
		baseURL:      host,
		retryBackoff: requestRetryDelay,
		authToken:    cred.AuthToken,
		csrfToken:    cred.CSRFToken,
		limiter:      requests.limiter,
		requests:     requests,
	}, nil
}

func (p *Pool) Primary() *Client {
	if p == nil || len(p.clients) == 0 {
		return nil
	}
	return p.clients[0]
}

// HasPrimary 实现 xapi.Source。
func (p *Pool) HasPrimary() bool { return p.Primary() != nil }

// GetUserByIDAsPrimary 用主账号查询用户，保持受保护账号关系字段的读取视角。
func (p *Pool) GetUserByIDAsPrimary(ctx context.Context, id string) (User, error) {
	client := p.Primary()
	if client == nil {
		return User{}, errors.New("没有可用 X 客户端")
	}
	return client.GetUserByID(ctx, id)
}

// FollowUserAsPrimary 用主账号关注用户，不经过 Pool 轮换。
func (p *Pool) FollowUserAsPrimary(ctx context.Context, user User) error {
	client := p.Primary()
	if client == nil {
		return errors.New("没有可用 X 客户端")
	}
	return client.FollowUser(ctx, user)
}

// FetchTweet 实现 xapi.Source：使用主 Cookie 直接发请求，不经过 Pool 轮换。
// 为与包级 FetchTweet 的既有语义一致，这里每次新建客户端（共享同一账号的
// 限流/节流状态），不受主账号临时 disabled 状态影响。
func (p *Pool) FetchTweet(ctx context.Context, id string, options parser.ParseOptions) (parser.TweetData, error) {
	if p == nil || p.primaryCredentials == nil {
		return parser.TweetData{}, errors.New("没有可用 X 客户端")
	}
	client, err := NewClient(*p.primaryCredentials, p.proxyURL)
	if err != nil {
		return parser.TweetData{}, err
	}
	return xapi.FetchTweet(ctx, client, id, p.primaryCredentials.AuthToken != "", options)
}

// maxPoolSelectWait 限制 Select 因"所有客户端限流"而阻塞等待的总时长。X GraphQL
// 常见窗口接近 15 分钟，等待预算必须覆盖这个窗口，否则任务会在复位前批量失败。
var maxPoolSelectWait = 15*time.Minute + 10*time.Second

// maxPoolSelectWaitStep 单次等待上限：若最早复位时间很远，只等待一个上限时长后重新评估。
var maxPoolSelectWaitStep = 60 * time.Second

func (p *Pool) Select(ctx context.Context, path string) (*Client, error) {
	if p == nil || len(p.clients) == 0 {
		return nil, errors.New("没有可用 X 客户端")
	}
	var waited time.Duration
	excluded := excludedClients(ctx)
	for {
		var blocked int
		var disabled int
		var skipped int
		p.mu.Lock()
		start := p.next
		for offset := 0; offset < len(p.clients); offset++ {
			index := (start + offset) % len(p.clients)
			client := p.clients[index]
			if excluded[client] {
				// 本次查询已确认该 Cookie 拿不到数据（如目标账号受年龄限制），
				// 换下一个账号，而不是原样重试同一个。
				skipped++
				continue
			}
			if client.isDisabled() {
				disabled++
				continue
			}
			if path != "" && client.limiter != nil && client.limiter.wouldBlock(path) {
				blocked++
				continue
			}
			p.next = index + 1
			p.mu.Unlock()
			return client, nil
		}
		p.mu.Unlock()
		if skipped+disabled == len(p.clients) {
			if disabled == len(p.clients) {
				return nil, errors.New("所有 X Cookie 都不可用")
			}
			return nil, errors.New("没有可用于本次请求的 X Cookie")
		}
		if blocked == 0 {
			return nil, errors.New("没有可用 X 客户端")
		}
		// 全部候选被限流：等最早复位时刻（有界），避免按固定 3s 空转碰运气。
		wait := p.earliestBlockedWait(path)
		if wait <= 0 {
			wait = 200 * time.Millisecond
		}
		// 调度器模式下不在这里等：把最早的恢复时间交回去，让任务释放执行槽。
		// 直接媒体下载、其他接口或下一个任务可以先运行。
		if yieldsToScheduler(ctx) {
			return nil, &RateLimitedError{Path: path, RetryAt: time.Now().Add(wait), Cause: ErrAllClientsRateLimited}
		}
		if wait > maxPoolSelectWaitStep {
			wait = maxPoolSelectWaitStep
		}
		if waited+wait > maxPoolSelectWait {
			return nil, &RateLimitedError{Path: path, RetryAt: time.Now().Add(wait), Cause: ErrAllClientsRateLimited}
		}
		waited += wait
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// earliestBlockedWait 返回当前所有客户端中最快结束限流的时间距离；无人限流返回 0。
func (p *Pool) earliestBlockedWait(paths ...string) time.Duration {
	var earliest time.Time
	found := false
	for _, client := range p.clients {
		if client.isDisabled() || client.limiter == nil {
			continue
		}
		if reset, ok := client.limiter.blockedReset(paths...); ok {
			if !found || reset.Before(earliest) {
				earliest = reset
				found = true
			}
		}
	}
	if !found {
		return 0
	}
	return time.Until(earliest)
}

func (p *Pool) Diagnostics() PoolDiagnostics {
	if p == nil {
		return PoolDiagnostics{}
	}
	items := make([]ClientStatus, 0, len(p.clients))
	available := 0
	for index, client := range p.clients {
		status := client.status(index, index == 0)
		if status.OK {
			available++
		}
		items = append(items, status)
	}
	return PoolDiagnostics{Total: len(items), Available: available, Clients: items}
}

// RecheckDisabledClients 立即对被禁用的 Cookie 做一次 /home 登录校验，通过即解除禁用，
// 返回此后是否至少有一个 Cookie 可用。用于区分"某个目标/游标返回 401"与"Cookie 真失效"：
// X 对深翻页的私密时间线也会回 401，不能因此把唯一 Cookie 锁 30 分钟。
func (p *Pool) RecheckDisabledClients(ctx context.Context) bool {
	if p == nil {
		return false
	}
	available := false
	for _, client := range p.clients {
		if client.isDisabled() {
			if _, err := client.GetSelfScreenName(ctx); err != nil {
				client.recordError(err)
			}
		}
		if !client.isDisabled() {
			available = true
		}
	}
	return available
}

func (p *Pool) CheckAll(ctx context.Context) PoolDiagnostics {
	if p == nil {
		return PoolDiagnostics{}
	}
	var wg sync.WaitGroup
	for _, client := range p.clients {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			if _, err := client.GetSelfScreenName(ctx); err != nil {
				// GetSelfScreenName 内部已在认证失败时 disable；这里只记录瞬时错误用于诊断，
				// 不再对 429/5xx/网络错误 disable（否则有效 cookie 会被误判不可用）。
				client.recordError(err)
			}
		}(client)
	}
	wg.Wait()
	return p.Diagnostics()
}

// ParseAdditionalCookies 解析备用 Cookie（JSON 数组或文本/每行一个账号），收敛到
// config.ParseCookiePairs 统一实现（M2），仅保留成对完整的凭据。
func ParseAdditionalCookies(raw string) []Credentials {
	pairs := config.ParseCookiePairs(raw)
	creds := make([]Credentials, 0, len(pairs))
	for _, pair := range pairs {
		if pair.AuthToken == "" || pair.CSRFToken == "" {
			continue
		}
		creds = append(creds, Credentials{AuthToken: pair.AuthToken, CSRFToken: pair.CSRFToken})
	}
	return creds
}

func (c *Client) GetSelfScreenName(ctx context.Context) (string, error) {
	// WAF 挑战页命中后 60s 内直接返回，不再继续撞 /home；挑战不等于 Cookie 失效。
	if c.limiter != nil && c.limiter.wouldBlock(homeCheckPath) {
		return "", fmt.Errorf("%w: /home 短退避冷却中", ErrWAFChallenge)
	}
	endpoint := c.requestBaseURL() + "/home"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	c.setCookieHeaders(request)
	request.Header.Set("User-Agent", userAgent)
	release, err := c.beginRequest(ctx, homeCheckPath)
	if err != nil {
		return "", err
	}
	defer release()
	c.requestCount.Add(1)
	response, err := c.http.Do(request)
	if err != nil {
		if c.limiter != nil {
			c.limiter.refund(homeCheckPath)
		}
		return "", err
	}
	defer response.Body.Close()
	if c.limiter != nil {
		c.limiter.after(homeCheckPath, response.Header)
	}
	// 先读 body 再判断状态：403 需要按响应内容区分 WAF/挑战页与真正凭证失效。
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode >= 400 {
		err := fmt.Errorf("X 登录校验失败: HTTP %d", response.StatusCode)
		switch response.StatusCode {
		case http.StatusUnauthorized:
			c.disable(err)
		case http.StatusTooManyRequests:
			if c.limiter != nil {
				c.limiter.markRateLimited(homeCheckPath, response.Header)
			}
			httpx.SlowXRequests()
			err = c.rateLimitedError(homeCheckPath, err)
		case http.StatusForbidden:
			if looksLikeWAFChallenge(response.Header.Get("Content-Type"), payload) {
				// 挑战页 ≠ 凭证失效：短退避即可，不能把唯一 Cookie 锁 30 分钟。
				if c.limiter != nil {
					c.limiter.markRateLimitedFor(homeCheckPath, wafBackoffDuration)
					c.limiter.markAccountCooldown(wafBackoffDuration)
				}
				httpx.SlowXRequests()
				err = fmt.Errorf("%w: HTTP 403", ErrWAFChallenge)
			} else {
				c.disableFor(err, clientForbiddenDisableCooldown)
			}
		}
		return "", err
	}
	if readErr != nil {
		return "", readErr
	}
	matches := screenNamePattern.FindSubmatch(payload)
	if len(matches) < 2 {
		if looksLikeWAFChallenge(response.Header.Get("Content-Type"), payload) {
			if c.limiter != nil {
				c.limiter.markRateLimitedFor(homeCheckPath, wafBackoffDuration)
				c.limiter.markAccountCooldown(wafBackoffDuration)
			}
			httpx.SlowXRequests()
			err := fmt.Errorf("%w: /home 返回挑战页", ErrWAFChallenge)
			c.recordError(err)
			return "", err
		}
		// HTTP 200 却识别不到账号，多是首页改版或返回了挑战页，不能证明 Cookie 失效：
		// 只记录错误供诊断，不禁用。
		err := errors.New("无法从 X 首页识别登录账号")
		c.recordError(err)
		return "", err
	}
	screenName := string(matches[1])
	c.mu.Lock()
	c.screen = screenName
	c.homeVerifiedAt = time.Now()
	c.mu.Unlock()
	c.enable()
	if c.limiter != nil {
		c.limiter.succeeded(homeCheckPath)
	}
	return screenName, nil
}

func (c *Client) GetUserByInput(ctx context.Context, input string) (User, error) {
	return getUserByInput(ctx, c, input)
}

func (p *Pool) GetUserByInput(ctx context.Context, input string) (User, error) {
	return getUserByInput(ctx, p, input)
}

func getUserByInput(ctx context.Context, requester timelineRequester, input string) (User, error) {
	return xapi.GetUserByInput(ctx, requesterAdapter{requester}, input, lookupFetcher(requester))
}

func (c *Client) GetUserByID(ctx context.Context, id string) (User, error) {
	return getUserByID(ctx, c, id)
}

func getUserByID(ctx context.Context, requester timelineRequester, id string) (User, error) {
	return xapi.GetUserByID(ctx, requesterAdapter{requester}, id, lookupFetcher(requester))
}

func (c *Client) GetUserByScreenName(ctx context.Context, screenName string) (User, error) {
	return getUserByScreenName(ctx, c, screenName)
}

func getUserByScreenName(ctx context.Context, requester timelineRequester, screenName string) (User, error) {
	return xapi.GetUserByScreenName(ctx, requesterAdapter{requester}, screenName, lookupFetcher(requester))
}

func (c *Client) GetListByID(ctx context.Context, id string) (List, error) {
	return getListByID(ctx, c, id)
}

func (p *Pool) GetListByID(ctx context.Context, id string) (List, error) {
	return getListByID(ctx, p, id)
}

// getListByID 查询列表元信息，并把读取它的账号绑定到 List.ViewerKey。
// Pool 通过 graphQLWithClient 记录实际选中的账号；单 Client 直接使用该账号。
func getListByID(ctx context.Context, requester timelineRequester, id string) (List, error) {
	var fetch xapi.ListFetcher
	switch typed := requester.(type) {
	case *Pool:
		fetch = func(ctx context.Context, path string, values url.Values) ([]byte, string, error) {
			payload, client, err := typed.graphQLWithClient(ctx, path, values)
			if err != nil {
				return nil, "", err
			}
			return payload, viewerKeyForClient(client), nil
		}
	case *Client:
		fetch = func(ctx context.Context, path string, values url.Values) ([]byte, string, error) {
			payload, err := typed.graphQL(ctx, path, values)
			if err != nil {
				return nil, "", err
			}
			return payload, viewerKeyForClient(typed), nil
		}
	}
	return xapi.GetListByID(ctx, requesterAdapter{requester}, id, fetch)
}

// lookupFetcher 为 Pool 提供"目标账号受年龄限制时排除当前 Cookie 换号重试"的
// 策略；单 Client 或测试假实现返回 nil，直接走 Requester.GraphQL。
func lookupFetcher(requester timelineRequester) xapi.LookupFetcher {
	pool, ok := requester.(*Pool)
	if !ok {
		return nil
	}
	// excluded 跨尝试累积，与旧实现的 map 生命周期一致。
	var excluded map[*Client]bool
	return func(ctx context.Context, path string, values url.Values, _ []byte) ([]byte, bool, error) {
		payload, client, err := pool.graphQLWithClient(withExcludedClients(ctx, excluded), path, values)
		if err != nil {
			return nil, false, err
		}
		if !xapi.AgeRestrictedPayload(payload) {
			return payload, false, nil
		}
		if excluded == nil {
			excluded = make(map[*Client]bool, len(pool.clients))
		}
		excluded[client] = true
		if len(excluded) >= len(pool.clients) {
			// 所有 Cookie 都被拒绝：返回最后一次响应，由 ParseUser 生成带提示的错误。
			return payload, false, nil
		}
		return payload, true, nil
	}
}

func (p *Pool) GetUserMedia(ctx context.Context, user User) ([]parser.TweetData, error) {
	tweets, _, err := p.GetUserMediaWithOptionsStats(ctx, user, parser.ParseOptions{})
	return tweets, err
}

func (p *Pool) GetUserMediaWithOptions(ctx context.Context, user User, options parser.ParseOptions) ([]parser.TweetData, error) {
	tweets, _, err := p.GetUserMediaWithOptionsStats(ctx, user, options)
	return tweets, err
}

// GetUserMediaWithOptionsStats 与 GetUserMediaWithOptions 行为一致，额外返回本轮抓取
// 统计。单条推文解析失败时会记录日志并跳过（不返回错误），避免一条坏数据让整个用户
// 的时间线归档永久失败；调用方可用 stats.LastSeenTweetID 推进增量游标越过被跳过条目。
func (p *Pool) GetUserMediaWithOptionsStats(ctx context.Context, user User, options parser.ParseOptions) ([]parser.TweetData, MediaFetchStats, error) {
	if userMediaNeedsPrimaryClient(user) {
		client := p.Primary()
		if client == nil {
			return nil, MediaFetchStats{}, errors.New("没有可用 X 客户端")
		}
		return getUserTimelineWithStats(ctx, client, user, options)
	}
	return p.getUserTimeline(ctx, user, options)
}

func (p *Pool) getUserTimeline(ctx context.Context, user User, options parser.ParseOptions) ([]parser.TweetData, MediaFetchStats, error) {
	return getUserTimelineWithStats(ctx, p, user, options)
}

func userMediaNeedsPrimaryClient(user User) bool {
	return xapi.UserMediaNeedsPrimaryClient(user)
}

type timelineRequester interface {
	graphQL(ctx context.Context, path string, values url.Values) ([]byte, error)
}

// getUserTimeline 保留旧的两返回值形状，供既有测试/包内调用使用。
func getUserTimeline(ctx context.Context, requester timelineRequester, user User, options parser.ParseOptions) ([]parser.TweetData, error) {
	return xapi.GetUserTimeline(ctx, requesterAdapter{requester}, user, options)
}

func getUserTimelineWithStats(ctx context.Context, requester timelineRequester, user User, options parser.ParseOptions) ([]parser.TweetData, MediaFetchStats, error) {
	return xapi.GetUserTimelineWithStats(ctx, requesterAdapter{requester}, user, options)
}

// walkUserMediaPages 是 xapi.WalkUserMediaPages 的包内适配版本（保留未导出的
// timelineRequester 参数，供既有测试直接驱动）。
func walkUserMediaPages(ctx context.Context, requester timelineRequester, user User, options UserMediaTimelineOptions, save func(UserMediaTimelinePage) error) (MediaFetchStats, error) {
	return xapi.WalkUserMediaPages(ctx, requesterAdapter{requester}, user, options, save)
}

func (c *Client) GetListMembers(ctx context.Context, list List) ([]User, error) {
	return getListMembers(ctx, c, list)
}

// GetListMembers 复用读取列表元信息的客户端，并用同一个客户端拉完所有分页。
// 列表成员对"观看者身份"敏感（私有列表只有成员可见），分页中途换 Cookie 会导致
// 视角不一致/列表截断；因此这里只做一次 select，不在每页重新挑选。
func (p *Pool) GetListMembers(ctx context.Context, list List) ([]User, error) {
	if list.ViewerKey != "" {
		client := p.clientByViewerKey(list.ViewerKey)
		if client == nil {
			return nil, errors.New("成员分页使用的 X 账号已移除，请重新创建归档任务")
		}
		return getListMembers(ctx, client, list)
	}
	client, err := p.Select(ctx, listMembersPath)
	if err != nil {
		return nil, err
	}
	return getListMembers(ctx, client, list)
}

func getListMembers(ctx context.Context, requester timelineRequester, list List) ([]User, error) {
	return xapi.GetListMembers(ctx, requesterAdapter{requester}, list)
}

func (c *Client) GetFollowing(ctx context.Context, user User) ([]User, error) {
	return getFollowing(ctx, c, user)
}

// GetFollowing 与 GetListMembers 相同：一次 select + 同一个客户端拉完所有分页，
// 保证受保护账号的"关注列表"始终以同一账号视角读取。受保护用户的关注列表只有
// 关注者（即 Primary）能看到，与 userMediaNeedsPrimaryClient 的逻辑一致。
func (p *Pool) GetFollowing(ctx context.Context, user User) ([]User, error) {
	var client *Client
	var err error
	if user.Protected {
		client = p.Primary()
		if client == nil {
			return nil, errors.New("没有可用 X 客户端")
		}
	} else {
		client, err = p.Select(ctx, followingPath)
		if err != nil {
			return nil, err
		}
	}
	return getFollowing(ctx, client, user)
}

func getFollowing(ctx context.Context, requester timelineRequester, user User) ([]User, error) {
	return xapi.GetFollowing(ctx, requesterAdapter{requester}, user)
}

// FollowUser 新增的 Pool 版本：优先用 Primary（保持操作身份），Primary 被禁用或遇到
// 限流（429/88）时换用其他 Cookie；非限流类错误（权限/参数等）直接返回。
func (p *Pool) FollowUser(ctx context.Context, user User) error {
	if p == nil || len(p.clients) == 0 {
		return errors.New("没有可用 X 客户端")
	}
	order := make([]*Client, 0, len(p.clients))
	if primary := p.Primary(); primary != nil {
		order = append(order, primary)
	}
	for _, client := range p.clients {
		if len(order) > 0 && client == order[0] {
			continue
		}
		order = append(order, client)
	}
	var lastErr error
	for _, client := range order {
		if client.isDisabled() {
			lastErr = ErrClientDisabled
			continue
		}
		err := client.FollowUser(ctx, user)
		if err == nil {
			return nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// 凭证失效/账号锁定/限流/WAF 都交给下一个 client 重试；只有非此类的确定性
		// 业务错误（如用户不存在/参数错误）才直接返回，换 Cookie 也不会改变结果。
		if errors.Is(err, ErrClientDisabled) || isYieldableError(err) || isPermanentClientError(err) {
			continue
		}
		return err
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("没有可用 X 客户端")
}

func (c *Client) FollowUser(ctx context.Context, user User) error {
	values := url.Values{}
	values.Set("user_id", user.ID)
	_, err := c.do(ctx, http.MethodPost, "/i/api/1.1/friendships/create.json", nil, values)
	return err
}

// GraphQL 实现 xapi.Requester。
func (c *Client) GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return c.graphQL(ctx, path, values)
}

func (c *Client) graphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	payload, err := c.do(ctx, http.MethodGet, path, values, nil)
	if err != nil {
		// GraphQL 端点 404 基本只有两种原因：queryId/operation 被 X 轮换，或路径写错。
		// 包一层专用错误，便于 jobs/运维识别并提示升级（而不是当作普通用户级失败）。
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %w", ErrQueryIDStale, err)
		}
		return nil, err
	}
	return payload, nil
}

// yieldOnRateLimitKey 标记请求由 Pool 发起：客户端被限流时应立即把请求交还给 Pool
// 换用别的 Cookie，而不是在本客户端的 before() 里阻塞到限流复位（可达 15 分钟）。
type yieldOnRateLimitKey struct{}

func withYieldOnRateLimit(ctx context.Context) context.Context {
	return context.WithValue(ctx, yieldOnRateLimitKey{}, true)
}

func yieldsOnRateLimit(ctx context.Context) bool {
	yield, _ := ctx.Value(yieldOnRateLimitKey{}).(bool)
	return yield
}

// excludedClientsKey 标记本次查询中已确认拿不到数据的 Cookie，让 Pool.Select 跳过它们。
// 目前用于"目标账号受年龄限制"：X 返回 HTTP 200，Pool 层面看不出失败，必须由调用方
// 识别后重试别的账号（不同账号的年龄验证状态/地区不同）。
type excludedClientsKey struct{}

func withExcludedClients(ctx context.Context, excluded map[*Client]bool) context.Context {
	if len(excluded) == 0 {
		return ctx
	}
	return context.WithValue(ctx, excludedClientsKey{}, excluded)
}

func excludedClients(ctx context.Context) map[*Client]bool {
	excluded, _ := ctx.Value(excludedClientsKey{}).(map[*Client]bool)
	return excluded
}

// GraphQL 实现 xapi.Requester（在账号池内自动选号/换号）。
func (p *Pool) GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return p.graphQL(ctx, path, values)
}

func (p *Pool) graphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	payload, _, err := p.graphQLWithClient(ctx, path, values)
	return payload, err
}

func (p *Pool) graphQLWithClient(ctx context.Context, path string, values url.Values) ([]byte, *Client, error) {
	var lastErr error
	failures, handoffs := 0, 0
	// 为备用账号保留尝试机会，同时限制所有 Cookie 的总请求次数。
	if requestBudgetFrom(ctx) == nil {
		budget := newRequestBudget(requestMaxAttempts, requestTotalBudget)
		budget.perClient = max(1, requestMaxAttempts-len(p.clients)+1)
		ctx = withRequestBudget(ctx, budget)
	}
	// 限流交还不计入失败次数，但总数有界：限流复位后若仍反复 429，最终按失败处理。
	maxHandoffs := max(1, len(p.clients)) * requestMaxAttempts
	for failures < max(1, len(p.clients)) {
		client, err := p.Select(ctx, path)
		if err != nil {
			// "全部限流"必须原样上抛（保留最后一次失败作上下文）：manager 靠这个分类中止
			// 批量归档，被 lastErr（通常是一个 429）覆盖后会继续对后面每个用户撞限流。
			if IsAllClientsRateLimited(err) && lastErr != nil {
				return nil, nil, fmt.Errorf("%w: %w", err, lastErr)
			}
			if lastErr != nil {
				return nil, nil, lastErr
			}
			return nil, nil, err
		}
		payload, err := client.graphQL(withYieldOnRateLimit(ctx), path, values)
		if err == nil {
			return payload, client, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		// 限流/本地限流忙碌/WAF 挑战：立即交还 Pool，换用其他 Cookie，而不是在原
		// client 上重试等待（Client-only 路径已在 do() 里直接返回，无重试放大）。
		if isYieldableError(err) {
			if client.limiter != nil && client.limiter.wouldBlock(path) && handoffs < maxHandoffs {
				handoffs++
				continue
			}
			return nil, nil, err
		}
		if !client.isDisabled() && !isTransientError(err) {
			return nil, nil, err
		}
		failures++
	}
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, errors.New("没有可用 X 客户端")
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values, form url.Values) ([]byte, error) {
	var lastErr error
	attempts := requestMaxAttempts
	if budget := requestBudgetFrom(ctx); budget != nil {
		attempts = min(attempts, budget.perClient)
	}
	for attempt := 0; attempt < attempts; attempt++ {
		// 跨 Cookie 共享的重试预算：耗尽后不再发起请求，把最后一次错误上抛。
		if budget := requestBudgetFrom(ctx); budget != nil && !budget.acquire() {
			if lastErr == nil {
				lastErr = errors.New("请求重试预算耗尽")
			}
			return nil, lastErr
		}
		payload, err := c.doOnce(ctx, method, path, query, form)
		if err == nil {
			c.clearError()
			return payload, nil
		}
		lastErr = err
		c.recordError(err)
		if isPermanentClientError(err) {
			var httpErr *HTTPError
			switch {
			case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden:
				// 非 WAF 的 403：可能是凭证失效，也可能是服务端风控；用较短冷却，
				// 避免把唯一有效 Cookie 锁太久。
				c.disableFor(err, clientForbiddenDisableCooldown)
			case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized:
				c.handleUnauthorized(ctx, err)
			default:
				c.disable(err)
			}
			return nil, err
		}
		// 限流（429/API-88）、本地限流忙碌、WAF 挑战都不在本客户端重试：
		// 要么等 Pool 换 Cookie，要么由调用方短退避。
		if isYieldableError(err) {
			return nil, err
		}
		if !isTransientError(err) || ctx.Err() != nil || attempt == attempts-1 {
			return nil, err
		}
		timer := time.NewTimer(c.requestRetryDelay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func (c *Client) doOnce(ctx context.Context, method string, path string, query url.Values, form url.Values) ([]byte, error) {
	if c.isDisabled() {
		return nil, ErrClientDisabled
	}
	endpoint, err := url.Parse(c.requestBaseURL())
	if err != nil {
		return nil, err
	}
	endpoint = endpoint.JoinPath(path)
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	c.setCookieHeaders(request)
	request.Header.Set("Authorization", "Bearer "+guestBearer)
	request.Header.Set("X-Csrf-Token", c.csrfToken)
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/json")
	// 真实 GraphQL 路径由 graphQLPath 构造为 /i/api/graphql/...，只匹配
	// "/graphql/" 前缀会让这里永远不生效。
	if strings.Contains(path, "/graphql/") {
		request.Header.Set("Content-Type", "application/json")
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	release, err := c.beginRequest(ctx, path)
	if err != nil {
		return nil, err
	}
	defer release()
	c.requestCount.Add(1)
	response, err := c.http.Do(request)
	if err != nil {
		// 请求在收到响应头前就失败：after() 不会被调用，归还 before() 预扣的配额，
		// 避免失败请求永久消耗本地速率预算。
		if c.limiter != nil {
			c.limiter.refund(path)
		}
		return nil, err
	}
	defer response.Body.Close()
	if c.limiter != nil {
		c.limiter.after(path, response.Header)
	}
	const maxResponseBytes = 32 << 20
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxResponseBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	if response.StatusCode >= 400 {
		httpErr := &HTTPError{StatusCode: response.StatusCode, Payload: limitedErrorPayload(payload)}
		switch response.StatusCode {
		case http.StatusTooManyRequests:
			// 429 可能不带 X-Rate-Limit 头：统一标记本地限流，供 Pool 换 Cookie。
			if c.limiter != nil {
				c.limiter.markRateLimited(path, response.Header)
			}
			httpx.SlowXRequests()
			return nil, c.rateLimitedError(path, httpErr)
		case http.StatusForbidden:
			// 403 需要读 body 区分 WAF/挑战页与真正的鉴权失败：挑战页不能 disable。
			if looksLikeWAFChallenge(response.Header.Get("Content-Type"), payload) {
				httpErr.WAFChallenge = true
				if c.limiter != nil {
					c.limiter.markRateLimitedFor(path, wafBackoffDuration)
					c.limiter.markAccountCooldown(wafBackoffDuration)
				}
				httpx.SlowXRequests()
				return nil, fmt.Errorf("%w: %w", ErrWAFChallenge, httpErr)
			}
		}
		return nil, httpErr
	}
	if apiErr := apiError(payload); apiErr != nil {
		var parsed *APIError
		if errors.As(apiErr, &parsed) && parsed.Code == apiErrRateLimit {
			if c.limiter != nil {
				c.limiter.markRateLimited(path, response.Header)
			}
			httpx.SlowXRequests()
			return nil, c.rateLimitedError(path, apiErr)
		}
		return nil, apiErr
	}
	if c.limiter != nil {
		c.limiter.succeeded(path)
	}
	return payload, nil
}

func (c *Client) requestBaseURL() string {
	if strings.TrimSpace(c.baseURL) == "" {
		return host
	}
	return strings.TrimRight(c.baseURL, "/")
}

func (c *Client) requestRetryDelay(attempt int) time.Duration {
	if c.retryBackoff != nil {
		return c.retryBackoff(attempt)
	}
	return requestRetryDelay(attempt)
}

func requestRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	attempt = min(attempt, 3)
	base := 750 * time.Millisecond * time.Duration(1<<attempt)
	// 全抖动（0..base/2）：多任务/多实例同时重试时避免同步脉冲；最坏 1.5×base。
	return base + time.Duration(rand.Int63n(int64(base/2)+1))
}

func (c *Client) setCookieHeaders(request *http.Request) {
	if c.authToken != "" {
		request.AddCookie(&http.Cookie{Name: "auth_token", Value: c.authToken})
	}
	if c.csrfToken != "" {
		request.AddCookie(&http.Cookie{Name: "ct0", Value: c.csrfToken})
	}
}

func (c *Client) isDisabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disabledLocked()
}

func (c *Client) disabledLocked() bool {
	return !c.disabledUntil.IsZero() && time.Now().Before(c.disabledUntil)
}

func (c *Client) disable(err error) {
	c.disableFor(err, clientDisableCooldown)
}

// disableFor 按指定冷却期禁用客户端：确认失效/账号锁定用 10 分钟，非 WAF 的 403 与
// 无法确认的 401 用 5 分钟，避免一次误封把唯一 Cookie 锁死。
func (c *Client) disableFor(err error, cooldown time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabledUntil = time.Now().Add(cooldown)
	if err != nil {
		c.lastError = err.Error()
	}
}

// handleUnauthorized 区分"接口/目标 401"与"Cookie 失效"：X 停用旧接口路径、目标账号
// 不可见等都会返回 401，单凭接口响应禁用 Cookie 会让所有任务误报"Cookie 不可用"。
// 只有 /home 也返回 401 才按失效禁用（GetSelfScreenName 内部完成）。
func (c *Client) handleUnauthorized(ctx context.Context, err error) {
	c.mu.Lock()
	recentlyVerified := !c.homeVerifiedAt.IsZero() && time.Since(c.homeVerifiedAt) < homeVerifiedTTL
	c.mu.Unlock()
	if recentlyVerified || ctx.Err() != nil {
		return
	}
	if _, checkErr := c.GetSelfScreenName(ctx); checkErr == nil || c.isDisabled() || ctx.Err() != nil {
		// 成功：Cookie 有效，不禁用；/home 401：已按失效禁用；取消：不做判断。
		return
	}
	c.disableFor(err, clientUnverifiedDisableCooldown)
}

// enable 在登录校验成功后解除禁用，让用户在设置页重新检测即可恢复被误禁的 Cookie。
func (c *Client) enable() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabledUntil = time.Time{}
	c.lastError = ""
}

func (c *Client) recordError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastError = err.Error()
	}
}

func (c *Client) clearError() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.disabledLocked() {
		c.lastError = ""
	}
}

func (c *Client) status(index int, primary bool) ClientStatus {
	c.mu.Lock()
	screenName := c.screen
	disabled := c.disabledLocked()
	lastError := c.lastError
	c.mu.Unlock()
	var rateLimits []RateLimitSnapshot
	if c.limiter != nil {
		rateLimits = c.limiter.snapshot()
	}
	return ClientStatus{
		Index:        index,
		Primary:      primary,
		ScreenName:   screenName,
		OK:           !disabled,
		Disabled:     disabled,
		Error:        lastError,
		RequestCount: c.requestCount.Load(),
		RateLimits:   rateLimits,
	}
}
