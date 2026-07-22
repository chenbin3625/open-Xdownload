package xclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/tidwall/gjson"
)

const (
	host      = "https://x.com"
	bearer    = "AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

const timelineFeatures = `{"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_timeline_navigation_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"communities_web_enable_tweet_community_results_fetch":true,"c9s_tweet_anatomy_moderator_badge_enabled":true,"articles_preview_enabled":true,"tweetypie_unmention_optimization_enabled":true,"responsive_web_edit_tweet_api_enabled":true,"graphql_is_translatable_rweb_tweet_is_translatable_enabled":true,"view_counts_everywhere_api_enabled":true,"longform_notetweets_consumption_enabled":true,"responsive_web_twitter_article_tweet_consumption_enabled":true,"tweet_awards_web_tipping_enabled":false,"creator_subscriptions_quote_tweet_preview_enabled":false,"freedom_of_speech_not_reach_fetch_enabled":true,"standardized_nudges_misinfo":true,"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled":true,"rweb_video_timestamps_enabled":true,"longform_notetweets_rich_text_read_enabled":true,"longform_notetweets_inline_media_enabled":true,"responsive_web_enhance_cards_enabled":false}`

const (
	userMediaPath      = "/i/api/graphql/MOLbHrtk8Ovu7DUNOLcXiA/UserMedia"
	requestMaxAttempts = 5
)

const (
	apiErrDependency      = 0
	apiErrTimeout         = 29
	apiErrExceedPostLimit = 88
	apiErrOverCapacity    = 130
	apiErrAccountLocked   = 326
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
	disabled     bool
	lastError    string
	requestCount atomic.Int64
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

func NewClient(cred Credentials, proxyURL string) (*Client, error) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if strings.TrimSpace(proxyURL) != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &Client{
		http:         &http.Client{Transport: transport, Timeout: 90 * time.Second},
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

func (p *Pool) Next() *Client {
	if p == nil || len(p.clients) == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	client := p.clients[p.next%len(p.clients)]
	p.next++
	return client
}

func (p *Pool) Select(ctx context.Context, path string) (*Client, error) {
	if p == nil || len(p.clients) == 0 {
		return nil, errors.New("没有可用 X 客户端")
	}
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
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
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

func ParseAdditionalCookies(raw string) []Credentials {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "********" {
		return nil
	}
	type cookieJSON struct {
		AuthToken  string `json:"authToken"`
		CSRFToken  string `json:"csrfToken"`
		AuthToken2 string `json:"auth_token"`
		CT0        string `json:"ct0"`
	}
	var decoded []cookieJSON
	if json.Unmarshal([]byte(raw), &decoded) == nil {
		creds := make([]Credentials, 0, len(decoded))
		for _, item := range decoded {
			auth := firstNonEmpty(item.AuthToken, item.AuthToken2)
			csrf := firstNonEmpty(item.CSRFToken, item.CT0)
			if auth != "" && csrf != "" {
				creds = append(creds, Credentials{AuthToken: auth, CSRFToken: csrf})
			}
		}
		return creds
	}
	blocks := strings.Split(raw, "\n")
	creds := []Credentials{}
	current := Credentials{}
	flush := func() {
		if current.AuthToken != "" && current.CSRFToken != "" {
			creds = append(creds, current)
		}
		current = Credentials{}
	}
	for _, line := range blocks {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line == "" {
			flush()
			continue
		}
		if strings.Contains(line, ":") && !strings.ContainsAny(line, ";,") {
			if setCredentialValue(&current, line) {
				if current.AuthToken != "" && current.CSRFToken != "" {
					flush()
				}
				continue
			}
		}
		for _, token := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ';' || r == ',' || r == ' '
		}) {
			setCredentialValue(&current, token)
		}
		if current.AuthToken != "" && current.CSRFToken != "" {
			flush()
		}
	}
	flush()
	return creds
}

func setCredentialValue(current *Credentials, raw string) bool {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		key, value, ok = strings.Cut(raw, ":")
	}
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if value == "" {
		return false
	}
	switch key {
	case "auth_token", "authToken":
		current.AuthToken = value
	case "ct0", "csrfToken":
		current.CSRFToken = value
	default:
		return false
	}
	return true
}

func (c *Client) GetSelfScreenName(ctx context.Context) (string, error) {
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
	if response.StatusCode >= 400 {
		err := fmt.Errorf("X 登录校验失败: HTTP %d", response.StatusCode)
		// 仅认证类失败（401/403）才禁用客户端；429/5xx 视为瞬时，禁用会误判有效 cookie 不可用。
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			c.disable(err)
		}
		return "", err
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", err
	}
	matches := regexp.MustCompile(`"screen_name":"(\S+?)"`).FindSubmatch(payload)
	if len(matches) < 2 {
		err := errors.New("无法从 X 首页识别登录账号")
		c.disable(err)
		return "", err
	}
	c.screen = string(matches[1])
	c.clearError()
	return c.screen, nil
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
	values.Set("variables", fmt.Sprintf(`{"userId":%q,"withSafetyModeUserFields":true}`, id))
	values.Set("features", `{"hidden_profile_likes_enabled":true,"hidden_profile_subscriptions_enabled":true,"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"highlights_tweets_tab_ui_enabled":true,"responsive_web_twitter_article_notes_tab_enabled":true,"subscriptions_feature_can_gift_premium":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	payload, err := requester.graphQL(ctx, "/i/api/graphql/CO4_gU4G_MRREoqfiTh6Hg/UserByRestId", values)
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
	values.Set("variables", fmt.Sprintf(`{"screen_name":%q,"withSafetyModeUserFields":true}`, screenName))
	values.Set("features", `{"hidden_profile_subscriptions_enabled":true,"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"subscriptions_verification_info_is_identity_verified_enabled":true,"subscriptions_verification_info_verified_since_enabled":true,"highlights_tweets_tab_ui_enabled":true,"responsive_web_twitter_article_notes_tab_enabled":true,"subscriptions_feature_can_gift_premium":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	values.Set("fieldToggles", `{"withAuxiliaryUserLabels":false}`)
	payload, err := requester.graphQL(ctx, "/i/api/graphql/xmU6X_CKVnQ5lSrCbAmJsg/UserByScreenName", values)
	if err != nil {
		return User{}, err
	}
	return parseUser(gjson.GetBytes(payload, "data.user"))
}

func (c *Client) GetListByID(ctx context.Context, id string) (List, error) {
	values := url.Values{}
	values.Set("variables", fmt.Sprintf(`{"listId":%q}`, id))
	values.Set("features", `{"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	payload, err := c.graphQL(ctx, "/i/api/graphql/ZMQOSpxDo0cP5Cdt8MgEVA/ListByRestId", values)
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

func (p *Pool) GetUserMedia(ctx context.Context, user User) ([]parser.TweetData, error) {
	return p.GetUserMediaWithOptions(ctx, user, parser.ParseOptions{})
}

func (p *Pool) GetUserMediaWithOptions(ctx context.Context, user User, options parser.ParseOptions) ([]parser.TweetData, error) {
	if userMediaNeedsPrimaryClient(user) {
		client := p.Primary()
		if client == nil {
			return nil, errors.New("没有可用 X 客户端")
		}
		return getUserTimeline(ctx, client, user, options)
	}
	return p.getUserTimeline(ctx, user, options)
}

func (p *Pool) getUserTimeline(ctx context.Context, user User, options parser.ParseOptions) ([]parser.TweetData, error) {
	return getUserTimeline(ctx, p, user, options)
}

func userMediaNeedsPrimaryClient(user User) bool {
	return user.Protected && user.Following
}

type timelineRequester interface {
	graphQL(ctx context.Context, path string, values url.Values) ([]byte, error)
}

func getUserTimeline(ctx context.Context, requester timelineRequester, user User, options parser.ParseOptions) ([]parser.TweetData, error) {
	if !user.Visible() {
		return nil, nil
	}
	cursor := ""
	seenCursor := map[string]struct{}{}
	tweets := []parser.TweetData{}
	for page := 0; page < 1000; page++ {
		values := url.Values{}
		values.Set("variables", fmt.Sprintf(`{"userId":%q,"count":100,"cursor":%q,"includePromotedContent":false,"withClientEventToken":false,"withBirdwatchNotes":false,"withVoice":true,"withV2Timeline":true}`, user.ID, cursor))
		values.Set("features", timelineFeatures)
		values.Set("fieldToggles", `{"withArticlePlainText":false}`)
		payload, err := requester.graphQL(ctx, userMediaPath, values)
		if err != nil {
			return nil, err
		}
		items, next, err := timelineItems(payload, "data.user.result.timeline_v2.timeline.instructions")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			result := item.Get("tweet_results.result")
			if !result.Exists() {
				continue
			}
			// 先判断早停（在 media/parse 跳过之前）：直接从 rest_id 取 ID，使无媒体或解析
			// 失败的停止推文仍能触发早停，避免切换 IncludeNestedTweetMedia 或停止推文被删
			// 时全量重翻页、空耗 X API 配额。
			if options.StopAtTweetID != "" && shouldStopAt(tweetResultID(result), options.StopAtTweetID, page) {
				// timeline 按时间倒序，已翻到上次归档过的推文，更旧的均已处理过，提前停止。
				return tweets, nil
			}
			tweet, err := parser.TweetFromGraphQLResultWithOptions("", user.ScreenName, "", result, options)
			if err != nil || len(tweet.Media) == 0 {
				continue
			}
			tweets = append(tweets, tweet)
		}
		if next == "" {
			break
		}
		if _, ok := seenCursor[next]; ok {
			break
		}
		seenCursor[next] = struct{}{}
		cursor = next
	}
	return tweets, nil
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
	cursor := ""
	seenCursor := map[string]struct{}{}
	users := []User{}
	for page := 0; page < 1000; page++ {
		values := url.Values{}
		values.Set("variables", fmt.Sprintf(`{"listId":%q,"count":200,"withSafetyModeUserFields":true,"cursor":%q}`, list.ID, cursor))
		values.Set("features", timelineFeatures)
		payload, err := c.graphQL(ctx, "/i/api/graphql/3dQPyRyAj6Lslp4e0ClXzg/ListMembers", values)
		if err != nil {
			return nil, err
		}
		items, next, err := timelineItems(payload, "data.list.members_timeline.timeline.instructions")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			user, err := parseUser(item.Get("user_results"))
			if err == nil && user.ID != "" {
				users = append(users, user)
			}
		}
		if next == "" {
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
	cursor := ""
	seenCursor := map[string]struct{}{}
	users := []User{}
	for page := 0; page < 1000; page++ {
		values := url.Values{}
		values.Set("variables", fmt.Sprintf(`{"userId":%q,"count":200,"includePromotedContent":false,"cursor":%q}`, user.ID, cursor))
		values.Set("features", timelineFeatures)
		payload, err := c.graphQL(ctx, "/i/api/graphql/7FEKOPNAvxWASt6v9gfCXw/Following", values)
		if err != nil {
			return nil, err
		}
		items, next, err := timelineItems(payload, "data.user.result.timeline.timeline.instructions")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			user, err := parseUser(item.Get("user_results"))
			if err == nil && user.ID != "" {
				users = append(users, user)
			}
		}
		if next == "" {
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

func (c *Client) FollowUser(ctx context.Context, user User) error {
	values := url.Values{}
	values.Set("user_id", user.ID)
	_, err := c.do(ctx, http.MethodPost, "/i/api/1.1/friendships/create.json", nil, values)
	return err
}

func (c *Client) graphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, values, nil)
}

func (p *Pool) graphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	var lastErr error
	for attempts := 0; attempts < max(1, len(p.clients)); attempts++ {
		client, err := p.Select(ctx, path)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		payload, err := client.graphQL(ctx, path, values)
		if err == nil {
			return payload, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !client.isDisabled() && !isTransientError(err) {
			return nil, err
		}
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
			c.disable(err)
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
		return nil, errors.New("X Cookie 已被标记为不可用")
	}
	if c.limiter != nil {
		if err := c.limiter.before(ctx, path); err != nil {
			return nil, err
		}
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
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("X-Csrf-Token", c.csrfToken)
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/json")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	payload, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		return nil, &HTTPError{StatusCode: response.StatusCode, Payload: limitedErrorPayload(payload)}
	}
	if apiErr := apiError(payload); apiErr != nil {
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
	return 750 * time.Millisecond * time.Duration(1<<attempt)
}

func (c *Client) setCookieHeaders(request *http.Request) {
	request.AddCookie(&http.Cookie{Name: "auth_token", Value: c.authToken})
	request.AddCookie(&http.Cookie{Name: "ct0", Value: c.csrfToken})
}

func apiError(payload []byte) error {
	errorsResult := gjson.GetBytes(payload, "errors")
	if !errorsResult.Exists() {
		return nil
	}
	code := -1
	if raw := errorsResult.Get("0.code"); raw.Exists() {
		code = int(raw.Int())
	}
	return &APIError{Code: code, Raw: limitedErrorPayload(payload)}
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

func (c *Client) isDisabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disabled
}

func (c *Client) disable(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabled = true
	if err != nil {
		c.lastError = err.Error()
	}
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
	if !c.disabled {
		c.lastError = ""
	}
}

func (c *Client) status(index int, primary bool) ClientStatus {
	c.mu.Lock()
	screenName := c.screen
	disabled := c.disabled
	lastError := c.lastError
	c.mu.Unlock()
	return ClientStatus{
		Index:        index,
		Primary:      primary,
		ScreenName:   screenName,
		OK:           !disabled,
		Disabled:     disabled,
		Error:        lastError,
		RequestCount: c.requestCount.Load(),
		RateLimits:   c.limiter.snapshot(),
	}
}

func isPermanentClientError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == apiErrExceedPostLimit || apiErr.Code == apiErrAccountLocked
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden
	}
	return false
}

func isTransientError(err error) bool {
	// 上下文取消/超时不是"可重试的瞬时错误"：重试只会再失败。把它判为非瞬时，
	// 让 do()/Pool.graphQL 立即返回，避免取消时遍历重试所有客户端并白白消耗速率预算。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == apiErrDependency || apiErr.Code == apiErrTimeout || apiErr.Code == apiErrOverCapacity
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500
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
	return User{
		ID:           raw.Get("rest_id").String(),
		Name:         legacy.Get("name").String(),
		ScreenName:   legacy.Get("screen_name").String(),
		Protected:    legacy.Get("protected").Bool(),
		FriendsCount: int(legacy.Get("friends_count").Int()),
		MediaCount:   int(legacy.Get("media_count").Int()),
		Muting:       legacy.Get("muting").Bool(),
		Blocking:     legacy.Get("blocking").Bool(),
		Following:    legacy.Get("following").Bool(),
		Requested:    legacy.Get("follow_request_sent").Bool(),
	}, nil
}

func timelineItems(payload []byte, instructionsPath string) ([]gjson.Result, string, error) {
	instructions := gjson.GetBytes(payload, instructionsPath)
	if !instructions.Exists() {
		typeName := gjson.GetBytes(payload, "data.user.result.__typename").String()
		if typeName != "" && typeName != "User" {
			return nil, "", fmt.Errorf("用户不可用: %s", typeName)
		}
		return nil, "", fmt.Errorf("无法解析 timeline instructions")
	}
	entries := gjson.Result{}
	moduleItems := gjson.Result{}
	for _, instruction := range instructions.Array() {
		switch instruction.Get("type").String() {
		case "TimelineAddEntries":
			entries = instruction.Get("entries")
		case "TimelineAddToModule":
			moduleItems = instruction.Get("moduleItems")
		}
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstString(values ...gjson.Result) string {
	for _, value := range values {
		if raw := value.String(); raw != "" {
			return raw
		}
	}
	return ""
}
