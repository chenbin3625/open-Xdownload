package xclient

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// Account 是构建 legacy 账号池用的一条账号，由调用方从 x_accounts 读取并转换。
type Account struct {
	AuthToken string
	CSRFToken string
	// ExtraCookies 是 auth_token/ct0 以外的 Cookie，请求时按名字排序附加在它们之后。
	ExtraCookies map[string]string
	// UserAgent 为空时使用默认 UA。
	UserAgent string
	// Proxy 为空时使用全局代理 cfg.ProxyURL。
	Proxy string
	// Primary 标记主账号。只有排在第一位的账号可以是主账号。
	Primary bool
}

// Options 调整 legacy 账号池的请求行为。零值即生产配置；字段只用于把请求指向测试服务。
type Options struct {
	// BaseURL 是 X 站点根地址（GraphQL、REST、/home 自检），默认 https://x.com。
	BaseURL string
	// Transport 非空时所有请求都使用它，并忽略全局与账号级代理。只用于测试。
	Transport http.RoundTripper
	// DisablePacing 关闭每账号 3 秒请求间隔、进程级节流和重试退避。按接口的限流器
	// （X-Rate-Limit 头、429 冷却）仍然生效。只用于测试。
	DisablePacing bool
}

// NewSourceFromAccounts 用已加载的账号构建 legacy 数据源。调用方只传 active 账号，主账号
// 排第一位；只有第一位账号 Primary 为 true 时池里才有主账号。缺少 auth_token 或 ct0 的
// 账号被跳过，重复的 auth_token 只保留第一条。
//
// 没有可用账号时返回只支持 FetchTweet 的 guest 数据源：它以未登录身份请求单条推文，
// 不读取 cfg 里已废弃的 Cookie 字段；其他方法返回"没有可用账号"的错误。
func NewSourceFromAccounts(cfg config.AppConfig, accounts []Account, opts Options) (xapi.Source, error) {
	pool, err := newPool(cfg, accounts, opts)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return &guestSource{cfg: cfg, opts: opts, err: errNoAccounts}, nil
	}
	return pool, nil
}

// newPool 按顺序为每个完整账号建客户端。没有可用账号时返回 (nil, nil)。
func newPool(cfg config.AppConfig, accounts []Account, opts Options) (*Pool, error) {
	clients := make([]*Client, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	hasPrimary := false
	for _, account := range accounts {
		account.AuthToken = strings.TrimSpace(account.AuthToken)
		account.CSRFToken = strings.TrimSpace(account.CSRFToken)
		if account.AuthToken == "" || account.CSRFToken == "" {
			continue
		}
		if _, duplicate := seen[account.AuthToken]; duplicate {
			continue
		}
		seen[account.AuthToken] = struct{}{}
		proxy := strings.TrimSpace(account.Proxy)
		if proxy == "" {
			proxy = cfg.ProxyURL
		}
		client, err := newAccountClient(account, proxy, opts)
		if err != nil {
			return nil, err
		}
		if len(clients) == 0 {
			hasPrimary = account.Primary
		}
		clients = append(clients, client)
	}
	if len(clients) == 0 {
		return nil, nil
	}
	return &Pool{clients: clients, hasPrimary: hasPrimary}, nil
}

// newAccountClient 构建一个账号的客户端。proxyURL 是已解析好的代理（账号代理优先，
// 否则全局代理）；opts.Transport 非空时忽略代理。
func newAccountClient(account Account, proxyURL string, opts Options) (*Client, error) {
	var transport http.RoundTripper = opts.Transport
	if transport == nil {
		// 复用带连接池与链路本地地址拨号防护的 Transport（M1/S2）；空代理时尊重环境变量代理。
		shared, err := httpx.Transport(proxyURL)
		if err != nil {
			return nil, err
		}
		transport = shared
	}
	client := &Client{
		http:             newHTTPClient(transport),
		baseURL:          strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		retryBackoff:     requestRetryDelay,
		authToken:        account.AuthToken,
		csrfToken:        account.CSRFToken,
		accountUserAgent: strings.TrimSpace(account.UserAgent),
		extraCookies:     sortedExtraCookies(account.ExtraCookies),
	}
	if client.baseURL == "" {
		client.baseURL = host
	}
	if opts.DisablePacing {
		// 去掉每账号执行槽、3 秒间隔、进程级节流和重试退避；按接口的限流器保留，但每次
		// 构建都新建，不与生产客户端或其他测试按 auth_token 共享状态。
		client.retryBackoff = func(int) time.Duration { return 0 }
		client.limiter = newRateLimiter()
		return client, nil
	}
	// 生产客户端的执行槽与限流器按 auth_token 在进程内共享：重建账号池
	// （InvalidateXSource）不能重置账号的请求间隔和限流冷却。
	requests := requestLimitForAccount(account.AuthToken)
	client.limiter = requests.limiter
	client.requests = requests
	return client, nil
}

// sortedExtraCookies 把额外 Cookie 按名字排序。auth_token/ct0 由客户端单独发送，名字或
// 值不合法（库里被手工改坏）的条目直接丢弃，避免拼出畸形的 Cookie 头。
func sortedExtraCookies(extra map[string]string) []*http.Cookie {
	if len(extra) == 0 {
		return nil
	}
	names := make([]string, 0, len(extra))
	for name, value := range extra {
		switch name {
		case "auth_token", "ct0":
			continue
		}
		if !config.IsValidCookieName(name) || strings.ContainsAny(value, ";\r\n") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	cookies := make([]*http.Cookie, 0, len(names))
	for _, name := range names {
		cookies = append(cookies, &http.Cookie{Name: name, Value: extra[name]})
	}
	return cookies
}
