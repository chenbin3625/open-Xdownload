package xclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/tidwall/gjson"
)

const (
	host      = "https://x.com"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
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

const timelineFeatures = `{"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_timeline_navigation_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"communities_web_enable_tweet_community_results_fetch":true,"c9s_tweet_anatomy_moderator_badge_enabled":true,"articles_preview_enabled":true,"tweetypie_unmention_optimization_enabled":true,"responsive_web_edit_tweet_api_enabled":true,"graphql_is_translatable_rweb_tweet_is_translatable_enabled":true,"view_counts_everywhere_api_enabled":true,"longform_notetweets_consumption_enabled":true,"responsive_web_twitter_article_tweet_consumption_enabled":true,"tweet_awards_web_tipping_enabled":false,"creator_subscriptions_quote_tweet_preview_enabled":false,"freedom_of_speech_not_reach_fetch_enabled":true,"standardized_nudges_misinfo":true,"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled":true,"rweb_video_timestamps_enabled":true,"longform_notetweets_rich_text_read_enabled":true,"longform_notetweets_inline_media_enabled":true,"responsive_web_enhance_cards_enabled":false}`

// graphQLQueryIDs 集中管理 X GraphQL 的 queryId。X 轮换 queryId 时只需改这一处；
// 请求如果因 queryId 失效返回 404，会得到 ErrQueryIDStale（不自动猜测新 id）。
var graphQLQueryIDs = struct {
	UserMedia        string
	UserByRestId     string
	UserByScreenName string
	ListByRestId     string
	ListMembers      string
	Following        string
}{
	UserMedia:        "MOLbHrtk8Ovu7DUNOLcXiA",
	UserByRestId:     "CO4_gU4G_MRREoqfiTh6Hg",
	UserByScreenName: "xmU6X_CKVnQ5lSrCbAmJsg",
	ListByRestId:     "ZMQOSpxDo0cP5Cdt8MgEVA",
	ListMembers:      "3dQPyRyAj6Lslp4e0ClXzg",
	Following:        "7FEKOPNAvxWASt6v9gfCXw",
}

// graphQLPath 由 queryId + operation 构造 GraphQL 端点路径。
func graphQLPath(queryID, operation string) string {
	return "/i/api/graphql/" + queryID + "/" + operation
}

var (
	userMediaPath        = graphQLPath(graphQLQueryIDs.UserMedia, "UserMedia")
	userByRestIDPath     = graphQLPath(graphQLQueryIDs.UserByRestId, "UserByRestId")
	userByScreenNamePath = graphQLPath(graphQLQueryIDs.UserByScreenName, "UserByScreenName")
	listByRestIDPath     = graphQLPath(graphQLQueryIDs.ListByRestId, "ListByRestId")
	listMembersPath      = graphQLPath(graphQLQueryIDs.ListMembers, "ListMembers")
	followingPath        = graphQLPath(graphQLQueryIDs.Following, "Following")
)

const requestMaxAttempts = 5

// 空页（items 为空但 Bottom cursor 仍在前进）连续出现多少次后停止翻页，防止异常响应
// 让单个用户翻满 1000 页、空耗 API 配额。
const maxConsecutiveEmptyPages = 3

// 列表/关注分页的页间间隔，与用户媒体时间线保持一致，避免连续高频请求触发短时限流/WAF。
const paginationPageDelay = 250 * time.Millisecond

var ErrAllClientsRateLimited = errors.New("X 客户端暂时全部限流，请稍后重试")

// ErrClientDisabled 表示该 Cookie 近期因认证失败被标记为不可用。它不是瞬时错误：
// 同一客户端重试只会立刻再次命中，应交给 Pool 换用其他 Cookie。
var ErrClientDisabled = errors.New("X Cookie 已被标记为不可用")

// ErrQueryIDStale 表示 GraphQL 端点返回 404，通常意味着 X 轮换了 queryId/接口路径。
// jobs 可识别该错误并提示用户升级程序；程序不会自动猜测新 queryId。
var ErrQueryIDStale = errors.New("X GraphQL queryId 可能已失效（X 接口可能已变更，请升级程序）")

// ErrWAFChallenge 表示 403 响应更像 WAF/登录挑战页（HTML、Cloudflare 等），
// 不能据此判定 Cookie 失效：只做短退避，不 disable 客户端。
var ErrWAFChallenge = errors.New("X 返回 WAF/挑战页（HTTP 403），已短退避且未禁用 Cookie")

// clientDisableCooldown 是认证失败（401/账号锁定）后客户端被禁用的时长。
var clientDisableCooldown = 30 * time.Minute

// clientForbiddenDisableCooldown 是非 WAF 的 403 被禁用时的较短冷却，避免一次误封把
// 唯一 Cookie 锁 30 分钟。
var clientForbiddenDisableCooldown = 5 * time.Minute

// wafBackoffDuration 是 WAF 挑战页命中后的短退避：只阻塞该 path 一段时间，不 disable。
const wafBackoffDuration = 60 * time.Second

// homeCheckPath 是 /home 登录自检在本地限流器里的 path key。
const homeCheckPath = "/home"

func IsAllClientsRateLimited(err error) bool {
	return errors.Is(err, ErrAllClientsRateLimited)
}

const (
	apiErrDependency    = 0
	apiErrTimeout       = 29
	apiErrRateLimit     = 88
	apiErrOverCapacity  = 130
	apiErrAccountLocked = 326
)

const maxErrorPayloadBytes = 8 << 10

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
	screen       string
	mu           sync.Mutex
	// disabledUntil 非零且晚于当前时间时客户端不可用。禁用带冷却期而不是永久：403 也可能
	// 来自 WAF 等瞬时拦截，Pool 按配置长期缓存，永久禁用会让主 Cookie 直到重启都不可用。
	disabledUntil time.Time
	lastError     string
	requestCount  atomic.Int64
}

type Pool struct {
	clients []*Client
	next    int
	mu      sync.Mutex
}

type User struct {
	ID           string
	Name         string
	ScreenName   string
	Protected    bool
	FriendsCount int
	MediaCount   int
	Muting       bool
	Blocking     bool
	Following    bool
	Requested    bool
}

func (u User) Title() string {
	if u.ScreenName == "" {
		return u.ID
	}
	if u.Name == "" {
		return u.ScreenName
	}
	return fmt.Sprintf("%s(%s)", u.Name, u.ScreenName)
}

func (u User) Visible() bool {
	return !u.Protected || u.Following
}

type List struct {
	ID          string
	Name        string
	MemberCount int
	Creator     User
}

type APIError struct {
	Code int
	Raw  string
}

func (e *APIError) Error() string {
	if e.Code == 0 {
		return e.Raw
	}
	return fmt.Sprintf("X API error %d: %s", e.Code, e.Raw)
}

type HTTPError struct {
	StatusCode int
	Payload    string
	// WAFChallenge 为 true 表示该 403 更像 WAF/挑战页（HTML/Cloudflare 等），
	// 不代表 Cookie 失效：调用方应短退避并尝试其他 Cookie，而不是 disable。
	WAFChallenge bool
}

func (e *HTTPError) Error() string {
	if e.Payload == "" {
		return fmt.Sprintf("X API 请求失败: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("X API 请求失败: HTTP %d %s", e.StatusCode, e.Payload)
}

type ClientStatus struct {
	Index        int                 `json:"index"`
	Primary      bool                `json:"primary"`
	ScreenName   string              `json:"screenName"`
	OK           bool                `json:"ok"`
	Disabled     bool                `json:"disabled"`
	Error        string              `json:"error,omitempty"`
	RequestCount int64               `json:"requestCount"`
	RateLimits   []RateLimitSnapshot `json:"rateLimits"`
}

type PoolDiagnostics struct {
	Total     int            `json:"total"`
	Available int            `json:"available"`
	Clients   []ClientStatus `json:"clients"`
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
	return &Pool{clients: clients}, nil
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
	return &Client{
		http:         newHTTPClient(transport),
		baseURL:      host,
		retryBackoff: requestRetryDelay,
		authToken:    cred.AuthToken,
		csrfToken:    cred.CSRFToken,
		limiter:      newRateLimiter(),
	}, nil
}

func (p *Pool) Primary() *Client {
	if p == nil || len(p.clients) == 0 {
		return nil
	}
	return p.clients[0]
}

// maxPoolSelectWait 限制 Select 因"所有客户端限流"而阻塞等待的总时长。X GraphQL
// 常见窗口接近 15 分钟，等待预算必须覆盖这个窗口，否则任务会在复位前批量失败。
var maxPoolSelectWait = maxRetryAfter + 10*time.Second

// maxPoolSelectWaitStep 单次等待上限：若最早复位时间很远，只等待一个上限时长后重新评估。
var maxPoolSelectWaitStep = 60 * time.Second

func (p *Pool) Select(ctx context.Context, path string) (*Client, error) {
	if p == nil || len(p.clients) == 0 {
		return nil, errors.New("没有可用 X 客户端")
	}
	var waited time.Duration
	for {
		var blocked int
		var disabled int
		p.mu.Lock()
		start := p.next
		for offset := 0; offset < len(p.clients); offset++ {
			index := (start + offset) % len(p.clients)
			client := p.clients[index]
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
		if disabled == len(p.clients) {
			return nil, errors.New("所有 X Cookie 都不可用")
		}
		if blocked == 0 {
			return nil, errors.New("没有可用 X 客户端")
		}
		// 全部候选被限流：等最早复位时刻（有界），避免按固定 3s 空转碰运气。
		wait := p.earliestBlockedWait()
		if wait <= 0 {
			wait = 200 * time.Millisecond
		}
		if wait > maxPoolSelectWaitStep {
			wait = maxPoolSelectWaitStep
		}
		if waited+wait > maxPoolSelectWait {
			return nil, ErrAllClientsRateLimited
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
func (p *Pool) earliestBlockedWait() time.Duration {
	var earliest time.Time
	found := false
	for _, client := range p.clients {
		if client.isDisabled() || client.limiter == nil {
			continue
		}
		if reset, ok := client.limiter.blockedReset(); ok {
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
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	// 先读 body 再判断状态：403 需要按响应内容区分 WAF/挑战页与真正凭证失效。
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode >= 400 {
		err := fmt.Errorf("X 登录校验失败: HTTP %d", response.StatusCode)
		switch response.StatusCode {
		case http.StatusUnauthorized:
			c.disable(err)
		case http.StatusForbidden:
			if looksLikeWAFChallenge(response.Header.Get("Content-Type"), payload) {
				// 挑战页 ≠ 凭证失效：短退避即可，不能把唯一 Cookie 锁 30 分钟。
				if c.limiter != nil {
					c.limiter.markRateLimitedFor(homeCheckPath, wafBackoffDuration)
				}
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
	if looksLikeWAFChallenge(response.Header.Get("Content-Type"), payload) {
		// HTTP 200 的挑战页既不能证明 Cookie 失效，也不能从 HTML 里猜账号。
		err := fmt.Errorf("%w: /home 返回挑战页", ErrWAFChallenge)
		c.recordError(err)
		return "", err
	}
	matches := screenNamePattern.FindSubmatch(payload)
	if len(matches) < 2 {
		// HTTP 200 却识别不到账号，多是首页改版或返回了挑战页，不能证明 Cookie 失效：
		// 只记录错误供诊断，不禁用。
		err := errors.New("无法从 X 首页识别登录账号")
		c.recordError(err)
		return "", err
	}
	screenName := string(matches[1])
	c.mu.Lock()
	c.screen = screenName
	c.mu.Unlock()
	c.enable()
	return screenName, nil
}

func (c *Client) GetUserByInput(ctx context.Context, input string) (User, error) {
	return getUserByInput(ctx, c, input)
}

func (p *Pool) GetUserByInput(ctx context.Context, input string) (User, error) {
	return getUserByInput(ctx, p, input)
}

func getUserByInput(ctx context.Context, requester timelineRequester, input string) (User, error) {
	input = strings.TrimSpace(strings.TrimPrefix(input, "@"))
	if input == "" {
		return User{}, errors.New("用户不能为空")
	}
	if _, err := strconv.ParseUint(input, 10, 64); err == nil {
		return getUserByID(ctx, requester, input)
	}
	return getUserByScreenName(ctx, requester, input)
}

func (c *Client) GetUserByID(ctx context.Context, id string) (User, error) {
	return getUserByID(ctx, c, id)
}

func getUserByID(ctx context.Context, requester timelineRequester, id string) (User, error) {
	values := url.Values{}
	values.Set("variables", encodeVariables(map[string]any{"userId": id, "withSafetyModeUserFields": true}))
	values.Set("features", `{"hidden_profile_likes_enabled":true,"hidden_profile_subscriptions_enabled":true,"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"highlights_tweets_tab_ui_enabled":true,"responsive_web_twitter_article_notes_tab_enabled":true,"subscriptions_feature_can_gift_premium":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	payload, err := requester.graphQL(ctx, userByRestIDPath, values)
	if err != nil {
		return User{}, err
	}
	return parseUser(gjson.GetBytes(payload, "data.user"))
}

func (c *Client) GetUserByScreenName(ctx context.Context, screenName string) (User, error) {
	return getUserByScreenName(ctx, c, screenName)
}

func getUserByScreenName(ctx context.Context, requester timelineRequester, screenName string) (User, error) {
	values := url.Values{}
	values.Set("variables", encodeVariables(map[string]any{"screen_name": screenName, "withSafetyModeUserFields": true}))
	values.Set("features", `{"hidden_profile_subscriptions_enabled":true,"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"subscriptions_verification_info_is_identity_verified_enabled":true,"subscriptions_verification_info_verified_since_enabled":true,"highlights_tweets_tab_ui_enabled":true,"responsive_web_twitter_article_notes_tab_enabled":true,"subscriptions_feature_can_gift_premium":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	values.Set("fieldToggles", `{"withAuxiliaryUserLabels":false}`)
	payload, err := requester.graphQL(ctx, userByScreenNamePath, values)
	if err != nil {
		return User{}, err
	}
	return parseUser(gjson.GetBytes(payload, "data.user"))
}

func (c *Client) GetListByID(ctx context.Context, id string) (List, error) {
	return getListByID(ctx, c, id)
}

func (p *Pool) GetListByID(ctx context.Context, id string) (List, error) {
	return getListByID(ctx, p, id)
}

func getListByID(ctx context.Context, requester timelineRequester, id string) (List, error) {
	values := url.Values{}
	values.Set("variables", encodeVariables(map[string]any{"listId": id}))
	values.Set("features", `{"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	payload, err := requester.graphQL(ctx, listByRestIDPath, values)
	if err != nil {
		return List{}, err
	}
	raw := gjson.GetBytes(payload, "data.list")
	if !raw.Exists() {
		return List{}, errors.New("列表不存在")
	}
	creator, _ := parseUser(raw.Get("user_results"))
	return List{
		ID:          firstString(raw.Get("id_str"), raw.Get("id")),
		Name:        raw.Get("name").String(),
		MemberCount: int(raw.Get("member_count").Int()),
		Creator:     creator,
	}, nil
}

// MediaFetchStats 描述一次用户媒体时间线抓取的附加统计。既有 API
// （GetUserMedia/GetUserMediaWithOptions）保持原签名、不暴露统计；需要"越过毒丸"
// 推进增量游标的调用方使用 GetUserMediaWithOptionsStats。
type MediaFetchStats struct {
	// LastSeenTweetID 是本轮 timeline 中见到的所有条目（含被跳过/置顶/早停边界）里
	// 数值最大的推文 ID。空串表示本轮没有见到任何带 ID 的推文条目。
	LastSeenTweetID string
	// SkippedTweets 是因解析失败被跳过的推文数量（不含 TweetUnavailable）。
	SkippedTweets int
	// SkippedTweetIDs 是被跳过的推文 ID，便于上层写入任务 Issues / 日志。
	SkippedTweetIDs []string
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
	return user.Protected && user.Following
}

type timelineRequester interface {
	graphQL(ctx context.Context, path string, values url.Values) ([]byte, error)
}

// getUserTimeline 保留旧的两返回值形状，供既有测试/包内调用使用。
func getUserTimeline(ctx context.Context, requester timelineRequester, user User, options parser.ParseOptions) ([]parser.TweetData, error) {
	tweets, _, err := getUserTimelineWithStats(ctx, requester, user, options)
	return tweets, err
}

func getUserTimelineWithStats(ctx context.Context, requester timelineRequester, user User, options parser.ParseOptions) ([]parser.TweetData, MediaFetchStats, error) {
	stats := MediaFetchStats{}
	if !user.Visible() {
		return nil, stats, nil
	}
	cursor := ""
	seenCursor := map[string]struct{}{}
	seenTweetIDs := map[string]struct{}{}
	tweets := []parser.TweetData{}
	emptyPages := 0
	// 首页命中的 stopID 可能是置顶项；页尾仍比 stopID 新时必须继续翻页。
	// 仅在配置开启增量归档时才会传入 StopAtTweetID；为空时全程翻页（全量扫描）。
	reachedStop := false
	for page := 0; page < 1000 && !reachedStop; page++ {
		if page > 0 {
			// 翻页间微小间隔，防止连续高频请求触发 X 接口短时限流与 WAF
			select {
			case <-ctx.Done():
				return nil, stats, ctx.Err()
			case <-time.After(paginationPageDelay):
			}
		}
		values := url.Values{}
		values.Set("variables", encodeVariables(map[string]any{
			"userId":                 user.ID,
			"count":                  100,
			"cursor":                 cursor,
			"includePromotedContent": false,
			"withClientEventToken":   false,
			"withBirdwatchNotes":     false,
			"withVoice":              true,
			"withV2Timeline":         true,
		}))
		values.Set("features", timelineFeatures)
		values.Set("fieldToggles", `{"withArticlePlainText":false}`)
		payload, err := requester.graphQL(ctx, userMediaPath, values)
		if err != nil {
			return nil, stats, err
		}
		items, next, err := timelineItems(payload, "data.user.result.timeline_v2.timeline.instructions")
		if err != nil {
			return nil, stats, err
		}
		if len(items) == 0 {
			if next == "" {
				break
			}
			// 空页但 cursor 仍在前进：连续多次才停，避免单页被服务端过滤就提前结束；
			// 同时防止异常响应让单个用户翻满 1000 页。
			emptyPages++
			if emptyPages >= maxConsecutiveEmptyPages {
				log.Printf("xclient: 时间线连续 %d 页为空但 cursor 仍在前进，停止翻页 (user=%s)", emptyPages, user.ID)
				break
			}
		} else {
			emptyPages = 0
		}
		lastTweetID := ""
		for _, item := range items {
			result := item.Get("tweet_results.result")
			if !result.Exists() {
				continue
			}
			// 先判断早停（在 media/parse 跳过之前）：直接从 rest_id 取 ID，使无媒体或解析
			// 失败的停止推文仍能触发早停，避免切换 IncludeNestedTweetMedia 或停止推文被删
			// 时全量重翻页、空耗 X API 配额。
			id := tweetResultID(result)
			if id != "" {
				lastTweetID = id
				stats.LastSeenTweetID = maxTweetID(stats.LastSeenTweetID, id)
				if _, ok := seenTweetIDs[id]; ok {
					// 翻页边界/置顶重复返回同一条推文：只保留一份。
					continue
				}
				seenTweetIDs[id] = struct{}{}
			}
			if options.StopAtTweetID != "" && !reachedStop && shouldStopAt(id, options.StopAtTweetID, page) {
				// 不能立即 return：page 0 的精确命中可能落在置顶推文上——上次归档时它恰为
				// 最新推文（newestTweetID 游标就是它），而其后仍排着本次新增的推文。继续
				// 扫完本页，只保留数值更新的项，再根据页尾判断是否继续翻页。
				reachedStop = true
			}
			if reachedStop {
				// 已到上次归档位置：数值不新于 stopID 的推文均已归档（含 stopID 本身），跳过；
				// 仅保留比 stopID 新的（置顶命中场景）。
				if !tweetIDNewerThan(id, options.StopAtTweetID) {
					continue
				}
			}
			tweet, err := parser.TweetFromGraphQLResultWithOptions("", user.ScreenName, "", result, options)
			if errors.Is(err, parser.ErrTweetUnavailable) {
				continue
			}
			if err != nil {
				// 单条推文解析失败只跳过并计数：不再让整个用户的时间线归档失败
				// （否则一条无法解析的推文会让增量游标永远无法推进）。
				stats.SkippedTweets++
				stats.SkippedTweetIDs = append(stats.SkippedTweetIDs, id)
				log.Printf("xclient: 跳过无法解析的推文 %s (user=%s): %v", id, user.ID, err)
				continue
			}
			if len(tweet.Media) == 0 {
				continue
			}
			tweets = append(tweets, tweet)
		}
		if page == 0 && reachedStop && tweetIDNewerThan(lastTweetID, options.StopAtTweetID) {
			reachedStop = false
		}
		if reachedStop {
			break
		}
		if next == "" {
			break
		}
		if next == cursor {
			// 服务端没有推进 cursor：立即停止，避免死循环。
			break
		}
		if _, ok := seenCursor[next]; ok {
			break
		}
		seenCursor[next] = struct{}{}
		cursor = next
	}
	return tweets, stats, nil
}

// shouldStopAt 判断 timeline 翻页是否应在 tweetID 处停止。timeline 按时间倒序：
// 精确匹配 stopID 总是停止；page>0 时再按数值比较（tweetID <= stopID 表示已翻到不新于
// 上次归档的推文），以处理 stopID 推文被删除、精确匹配永不命中的情况。首页可能含置顶
// 推文（时间较旧但排在最前），故首页仅用精确匹配，避免误停而漏归档更新的推文。
func shouldStopAt(tweetID, stopID string, page int) bool {
	if tweetID != "" && tweetID == stopID {
		return true
	}
	if page == 0 {
		return false
	}
	t, err1 := strconv.ParseInt(tweetID, 10, 64)
	s, err2 := strconv.ParseInt(stopID, 10, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	return t <= s
}

// tweetIDNewerThan 报告 tweetID 是否数值上大于 stopID。任一 ID 解析失败时返回 true：
// 保守保留该推文交给下游去重（downloads 表 / 媒体锁），避免 ID 异常时漏归档。
// X 的雪花 ID 均为数字，该分支正常不会触发。
func tweetIDNewerThan(tweetID, stopID string) bool {
	t, err1 := strconv.ParseInt(tweetID, 10, 64)
	s, err2 := strconv.ParseInt(stopID, 10, 64)
	if err1 != nil || err2 != nil {
		return true
	}
	return t > s
}

func tweetResultID(result gjson.Result) string {
	const maxDepth = 8
	for depth := 0; depth < maxDepth && result.Exists(); depth++ {
		if id := result.Get("rest_id").String(); id != "" {
			return id
		}
		if result.Get("__typename").String() == "TweetWithVisibilityResults" && result.Get("tweet").Exists() {
			result = result.Get("tweet")
			continue
		}
		if nested := result.Get("result"); nested.Exists() {
			result = nested
			continue
		}
		return ""
	}
	return ""
}

func (c *Client) GetListMembers(ctx context.Context, list List) ([]User, error) {
	return getListMembers(ctx, c, list)
}

// GetListMembers 先由 Pool 选择一次客户端，再用同一个客户端拉完所有分页。
// 列表成员对"观看者身份"敏感（私有列表只有成员可见），分页中途换 Cookie 会导致
// 视角不一致/列表截断；因此这里只做一次 select，不在每页重新挑选。
func (p *Pool) GetListMembers(ctx context.Context, list List) ([]User, error) {
	client, err := p.Select(ctx, listMembersPath)
	if err != nil {
		return nil, err
	}
	return getListMembers(ctx, client, list)
}

func getListMembers(ctx context.Context, requester timelineRequester, list List) ([]User, error) {
	cursor := ""
	seenCursor := map[string]struct{}{}
	seenUserIDs := map[string]struct{}{}
	users := []User{}
	emptyPages := 0
	for page := 0; page < 1000; page++ {
		if page > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(paginationPageDelay):
			}
		}
		values := url.Values{}
		values.Set("variables", encodeVariables(map[string]any{
			"listId":                   list.ID,
			"count":                    200,
			"withSafetyModeUserFields": true,
			"cursor":                   cursor,
		}))
		values.Set("features", timelineFeatures)
		payload, err := requester.graphQL(ctx, listMembersPath, values)
		if err != nil {
			return nil, err
		}
		items, next, err := timelineItems(payload, "data.list.members_timeline.timeline.instructions")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			if next == "" {
				break
			}
			emptyPages++
			if emptyPages >= maxConsecutiveEmptyPages {
				log.Printf("xclient: 列表成员连续 %d 页为空但 cursor 仍在前进，停止翻页 (list=%s)", emptyPages, list.ID)
				break
			}
		} else {
			emptyPages = 0
		}
		for _, item := range items {
			user, err := parseUser(item.Get("user_results"))
			if err != nil || user.ID == "" {
				continue
			}
			if _, ok := seenUserIDs[user.ID]; ok {
				continue
			}
			seenUserIDs[user.ID] = struct{}{}
			users = append(users, user)
		}
		if next == "" {
			break
		}
		if next == cursor {
			break
		}
		if _, ok := seenCursor[next]; ok {
			break
		}
		seenCursor[next] = struct{}{}
		cursor = next
	}
	return users, nil
}

func (c *Client) GetFollowing(ctx context.Context, user User) ([]User, error) {
	return getFollowing(ctx, c, user)
}

// GetFollowing 与 GetListMembers 相同：一次 select + 同一个客户端拉完所有分页，
// 保证受保护账号的"关注列表"始终以同一账号视角读取。
func (p *Pool) GetFollowing(ctx context.Context, user User) ([]User, error) {
	client, err := p.Select(ctx, followingPath)
	if err != nil {
		return nil, err
	}
	return getFollowing(ctx, client, user)
}

func getFollowing(ctx context.Context, requester timelineRequester, user User) ([]User, error) {
	cursor := ""
	seenCursor := map[string]struct{}{}
	seenUserIDs := map[string]struct{}{}
	users := []User{}
	emptyPages := 0
	for page := 0; page < 1000; page++ {
		if page > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(paginationPageDelay):
			}
		}
		values := url.Values{}
		values.Set("variables", encodeVariables(map[string]any{
			"userId":                 user.ID,
			"count":                  200,
			"includePromotedContent": false,
			"cursor":                 cursor,
		}))
		values.Set("features", timelineFeatures)
		payload, err := requester.graphQL(ctx, followingPath, values)
		if err != nil {
			return nil, err
		}
		items, next, err := timelineItems(payload, "data.user.result.timeline.timeline.instructions")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			if next == "" {
				break
			}
			emptyPages++
			if emptyPages >= maxConsecutiveEmptyPages {
				log.Printf("xclient: 关注列表连续 %d 页为空但 cursor 仍在前进，停止翻页 (user=%s)", emptyPages, user.ID)
				break
			}
		} else {
			emptyPages = 0
		}
		for _, item := range items {
			parsed, err := parseUser(item.Get("user_results"))
			if err != nil || parsed.ID == "" {
				continue
			}
			if _, ok := seenUserIDs[parsed.ID]; ok {
				continue
			}
			seenUserIDs[parsed.ID] = struct{}{}
			users = append(users, parsed)
		}
		if next == "" {
			break
		}
		if next == cursor {
			break
		}
		if _, ok := seenCursor[next]; ok {
			break
		}
		seenCursor[next] = struct{}{}
		cursor = next
	}
	return users, nil
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
		if !errors.Is(err, ErrClientDisabled) && !isYieldableError(err) {
			return err
		}
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

func (p *Pool) graphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	var lastErr error
	failures, handoffs := 0, 0
	// 限流交还不计入失败次数，但总数有界：限流复位后若仍反复 429，最终按失败处理。
	maxHandoffs := max(1, len(p.clients)) * requestMaxAttempts
	for failures < max(1, len(p.clients)) {
		client, err := p.Select(ctx, path)
		if err != nil {
			// "全部限流"必须原样上抛（保留最后一次失败作上下文）：manager 靠这个分类中止
			// 批量归档，被 lastErr（通常是一个 429）覆盖后会继续对后面每个用户撞限流。
			if IsAllClientsRateLimited(err) && lastErr != nil {
				return nil, fmt.Errorf("%w: %w", err, lastErr)
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		payload, err := client.graphQL(withYieldOnRateLimit(ctx), path, values)
		if err == nil {
			return payload, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 限流/本地限流忙碌/WAF 挑战：立即交还 Pool，换用其他 Cookie，而不是在原
		// client 上重试等待（Client-only 路径已在 do() 里直接返回，无重试放大）。
		if isYieldableError(err) {
			if client.limiter != nil && client.limiter.wouldBlock(path) && handoffs < maxHandoffs {
				handoffs++
				continue
			}
			return nil, err
		}
		if !client.isDisabled() && !isTransientError(err) {
			return nil, err
		}
		failures++
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("没有可用 X 客户端")
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values, form url.Values) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < requestMaxAttempts; attempt++ {
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
				// 避免把唯一有效 Cookie 锁 30 分钟。
				c.disableFor(err, clientForbiddenDisableCooldown)
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
		if !isTransientError(err) || ctx.Err() != nil || attempt == requestMaxAttempts-1 {
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
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c.limiter != nil {
		if err := c.limiter.before(ctx, path); err != nil {
			return nil, err
		}
	}
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
		case http.StatusForbidden:
			// 403 需要读 body 区分 WAF/挑战页与真正的鉴权失败：挑战页不能 disable。
			if looksLikeWAFChallenge(response.Header.Get("Content-Type"), payload) {
				httpErr.WAFChallenge = true
				if c.limiter != nil {
					c.limiter.markRateLimitedFor(path, wafBackoffDuration)
				}
				return nil, fmt.Errorf("%w: %w", ErrWAFChallenge, httpErr)
			}
		}
		return nil, httpErr
	}
	if apiErr := apiError(payload); apiErr != nil {
		var parsed *APIError
		if errors.As(apiErr, &parsed) && parsed.Code == apiErrRateLimit && c.limiter != nil {
			c.limiter.markRateLimited(path, response.Header)
		}
		return nil, apiErr
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

// encodeVariables 用 encoding/json 构造 GraphQL variables：%q 是 Go 字面量转义，
// 遇到非 JSON 字符（如 \x 形式）会产生非法 JSON，交给 json.Marshal 更稳妥。
func encodeVariables(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func (c *Client) setCookieHeaders(request *http.Request) {
	request.AddCookie(&http.Cookie{Name: "auth_token", Value: c.authToken})
	request.AddCookie(&http.Cookie{Name: "ct0", Value: c.csrfToken})
}

// 限流/账号锁定的 API 错误码：即使 data 非空也必须上报，不能当成可容忍的 partial error。
var fatalAPIErrorCodes = map[int]struct{}{
	32:  {}, // Could not authenticate you
	64:  {}, // Account suspended
	88:  {}, // Rate limit exceeded
	89:  {}, // Invalid or expired token
	215: {}, // Bad authentication data
	326: {}, // Account locked
}

func isFatalAPIErrorCode(code int) bool {
	_, ok := fatalAPIErrorCodes[code]
	return ok
}

func apiError(payload []byte) error {
	errorsResult := gjson.GetBytes(payload, "errors")
	if !errorsResult.Exists() {
		return nil
	}
	raw := limitedErrorPayload(payload)
	// errors 不是数组（对象/字符串）时不能静默当成功：X 正常都是数组，出现异常形态
	// 说明响应不符合预期，按未知 API 错误上报。
	if !errorsResult.IsArray() {
		code := -1
		if value := errorsResult.Get("code"); value.Exists() {
			code = int(value.Int())
		}
		return &APIError{Code: code, Raw: raw}
	}
	data := gjson.GetBytes(payload, "data")
	hasData := data.IsObject() && len(data.Map()) > 0
	for _, entry := range errorsResult.Array() {
		code := -1
		if value := entry.Get("code"); value.Exists() {
			code = int(value.Int())
		}
		err := &APIError{Code: code, Raw: raw}
		// GraphQL 允许部分成功：data 可用时，针对个别子项的缺失/无 kind 的
		// partial error 可以容忍（记日志后继续）。但限流/账号锁定/凭证类错误
		// （fatalAPIErrorCodes）与瞬时错误必须上报，供 Pool 换 Cookie 或让任务中止。
		if hasData && isTolerablePartialError(entry, err) {
			log.Printf("xclient: 忽略可容忍的 GraphQL 部分错误 code=%d kind=%q: %s", code, entry.Get("kind").String(), entry.Get("message").String())
			continue
		}
		return err
	}
	return nil
}

// isTolerablePartialError 判定 data 非空响应中的单条 GraphQL 错误能否忽略。
// 容忍范围：无 kind、kind=NonFatal、或没有 code（-1）的**非致命**错误；
// 限流/锁定/凭证错误与瞬时（0/29/130）错误一律不容忍。
func isTolerablePartialError(entry gjson.Result, err *APIError) bool {
	if isFatalAPIErrorCode(err.Code) || isPermanentClientError(err) || isTransientError(err) {
		return false
	}
	kind := entry.Get("kind").String()
	return kind == "" || kind == "NonFatal" || err.Code == -1
}

// isRateLimitError 报告该错误是否代表"账号/路径被限流"。API-88 与 HTTP 429 都算；
// 这类错误不再做客户端内重试（88 以前被当 transient，会让 Client-only 路径等 15 分钟）。
func isRateLimitError(err error) bool {
	if errors.Is(err, errRateLimitBusyWait) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == apiErrRateLimit
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// isWAFChallengeError 报告 403 是否是 WAF/挑战页（不能据此 disable Cookie）。
func isWAFChallengeError(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.WAFChallenge
}

// isYieldableError 报告该错误应"立即交还 Pool 换 Cookie / 立即返回"而不是原地重试。
func isYieldableError(err error) bool {
	return isRateLimitError(err) || isWAFChallengeError(err) || errors.Is(err, ErrClientDisabled)
}

func limitedErrorPayload(payload []byte) string {
	if len(payload) <= maxErrorPayloadBytes {
		return string(payload)
	}
	clipped := payload[:maxErrorPayloadBytes]
	for len(clipped) > 0 && !utf8.Valid(clipped) {
		clipped = clipped[:len(clipped)-1]
	}
	return fmt.Sprintf("%s... [truncated %d bytes]", string(clipped), len(payload)-len(clipped))
}

// looksLikeWAFChallenge 判断 4xx 响应是否更像 WAF/登录挑战页：Content-Type 为 HTML，
// 或正文包含常见挑战页标记（Cloudflare / HTML / challenge / security）。
// 真实 GraphQL 403 通常是 JSON 错误体，不会命中；命中时不能把 Cookie 判死。
func looksLikeWAFChallenge(contentType string, payload []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	if len(payload) == 0 {
		return false
	}
	const maxScan = 8 << 10
	if len(payload) > maxScan {
		payload = payload[:maxScan]
	}
	body := strings.ToLower(string(payload))
	for _, marker := range []string{"<html", "<!doctype html", "challenge", "cf-chl", "cloudflare", "security check", "just a moment", "attention required"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
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

// disableFor 按指定冷却期禁用客户端：401/账号锁定用 30 分钟，非 WAF 的 403 用较短
// 冷却（5 分钟），避免一次误封把唯一 Cookie 锁死。
func (c *Client) disableFor(err error, cooldown time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabledUntil = time.Now().Add(cooldown)
	if err != nil {
		c.lastError = err.Error()
	}
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

func isPermanentClientError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == apiErrAccountLocked
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == http.StatusUnauthorized {
			return true
		}
		// WAF/挑战页 403 不代表凭证失效：交给 do()/Pool 短退避与换 Cookie。
		return httpErr.StatusCode == http.StatusForbidden && !httpErr.WAFChallenge
	}
	return false
}

func isTransientError(err error) bool {
	// 上下文取消/超时不是"可重试的瞬时错误"：重试只会再失败。把它判为非瞬时，
	// 让 do()/Pool.graphQL 立即返回，避免取消时遍历重试所有客户端并白白消耗速率预算。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrClientDisabled) {
		return false
	}
	// 响应体超限、限流器连续等待失败、限流、WAF 挑战都不适合原地重试：
	// 前两者是确定性失败，后两者应交给 Pool 换 Cookie / 调用方短退避。
	if errors.Is(err, httpx.ErrBodyTooLarge) || errors.Is(err, errRateLimitBusyWait) {
		return false
	}
	if isRateLimitError(err) || isWAFChallengeError(err) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == apiErrDependency || apiErr.Code == apiErrTimeout || apiErr.Code == apiErrOverCapacity
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500
	}
	return err != nil && !isPermanentClientError(err)
}

func parseUser(raw gjson.Result) (User, error) {
	if raw.Get("result").Exists() {
		raw = raw.Get("result")
	}
	if raw.Get("__typename").String() == "UserUnavailable" {
		return User{}, errors.New("用户不可用")
	}
	legacy := raw.Get("legacy")
	if !legacy.Exists() {
		return User{}, errors.New("响应中没有用户 legacy 数据")
	}
	// X 已把部分字段从 legacy 迁到 core / privacy / relationship_perspectives，不同接口
	// 返回的结构并不一致：新位置优先，旧位置兜底。
	relationship := raw.Get("relationship_perspectives")
	return User{
		ID:           raw.Get("rest_id").String(),
		Name:         firstString(raw.Get("core.name"), legacy.Get("name")),
		ScreenName:   firstString(raw.Get("core.screen_name"), legacy.Get("screen_name")),
		Protected:    firstBool(raw.Get("privacy.protected"), legacy.Get("protected")),
		FriendsCount: int(legacy.Get("friends_count").Int()),
		MediaCount:   int(legacy.Get("media_count").Int()),
		Muting:       firstBool(relationship.Get("muting"), legacy.Get("muting")),
		Blocking:     firstBool(relationship.Get("blocking"), legacy.Get("blocking")),
		Following:    firstBool(relationship.Get("following"), legacy.Get("following")),
		Requested:    firstBool(relationship.Get("follow_request_sent"), legacy.Get("follow_request_sent")),
	}, nil
}

// firstBool 返回第一个存在的布尔字段的值；都不存在时为 false。
func firstBool(values ...gjson.Result) bool {
	for _, value := range values {
		if value.Type == gjson.True || value.Type == gjson.False {
			return value.Bool()
		}
	}
	return false
}

func timelineItems(payload []byte, instructionsPath string) ([]gjson.Result, string, error) {
	instructions := gjson.GetBytes(payload, instructionsPath)
	if !instructions.Exists() {
		if strings.HasPrefix(instructionsPath, "data.user.") {
			typeName := gjson.GetBytes(payload, "data.user.result.__typename").String()
			if typeName != "" && typeName != "User" {
				return nil, "", fmt.Errorf("用户不可用: %s", typeName)
			}
		}
		return nil, "", fmt.Errorf("无法解析 timeline instructions（%s）", instructionsPath)
	}
	entries := gjson.Result{}
	moduleItems := gjson.Result{}
	recognized := false
	for _, instruction := range instructions.Array() {
		switch instruction.Get("type").String() {
		case "TimelineAddEntries":
			recognized = true
			entries = instruction.Get("entries")
		case "TimelineAddToModule":
			recognized = true
			moduleItems = instruction.Get("moduleItems")
		}
	}
	// instructions 非空但没有任何已知类型：说明 X 改了 timeline 结构。此时若仍返回空结果，
	// 上层会当成"该用户没有推文"静默成功（历史问题）；改为明确报错以便尽早发现接口变更。
	// 空 instructions 数组按"确实没有内容"处理（返回空结果由调用方判断是否结束）。
	if !recognized && len(instructions.Array()) > 0 {
		return nil, "", fmt.Errorf("无法识别 timeline 结构（instructions 无已知类型，path=%s）", instructionsPath)
	}
	items := []gjson.Result{}
	if entries.IsArray() {
		for _, entry := range entries.Array() {
			if entry.Get("content.entryType").String() == "TimelineTimelineCursor" {
				continue
			}
			content := entry.Get("content")
			switch content.Get("entryType").String() {
			case "TimelineTimelineModule":
				for _, item := range content.Get("items").Array() {
					if itemContent := item.Get("item.itemContent"); itemContent.Exists() {
						items = append(items, itemContent)
					}
				}
			case "TimelineTimelineItem":
				if itemContent := content.Get("itemContent"); itemContent.Exists() {
					items = append(items, itemContent)
				}
			}
		}
	}
	if moduleItems.IsArray() {
		for _, item := range moduleItems.Array() {
			if itemContent := item.Get("item.itemContent"); itemContent.Exists() {
				items = append(items, itemContent)
			}
		}
	}
	return items, bottomCursor(entries), nil
}

func bottomCursor(entries gjson.Result) string {
	array := entries.Array()
	for i := len(array) - 1; i >= 0; i-- {
		if array[i].Get("content.entryType").String() == "TimelineTimelineCursor" &&
			array[i].Get("content.cursorType").String() == "Bottom" {
			return array[i].Get("content.value").String()
		}
	}
	return ""
}

// maxTweetID 返回两个推文 ID 中数值较大的一个；任一无法解析为数字时退回字符串
// 比较（雪花 ID 等长时字符串比较等价于数值比较）。空串视为"无"。
func maxTweetID(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	ai, err1 := strconv.ParseInt(a, 10, 64)
	bi, err2 := strconv.ParseInt(b, 10, 64)
	if err1 == nil && err2 == nil {
		if ai >= bi {
			return a
		}
		return b
	}
	if a > b {
		return a
	}
	return b
}

func firstString(values ...gjson.Result) string {
	for _, value := range values {
		if raw := value.String(); raw != "" {
			return raw
		}
	}
	return ""
}
