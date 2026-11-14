// Package xscrape 是 twscrape 风格的 X 数据源实现：账号与限流锁持久化在 SQLite
// （storage 的 x_accounts 系列表），请求带 x-client-transaction-id，queryId 支持自动
// 发现。它是默认实现；OPEN_XDOWNLOAD_SCRAPER=legacy 可退回 xclient。设计见
// docs/xscrape-design.md。
//
// 导出的类型和函数签名是固定契约，cmd/server 按它接线。
package xscrape

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/chenbin3625/open-Xdownload/internal/xscrape/xtid"
)

// TransactionIDGenerator 生成 x-client-transaction-id。*xtid.Generator 实现了它。
type TransactionIDGenerator interface {
	Generate(ctx context.Context, method, path string) (string, error)
	Invalidate()
}

// Options 配置 Engine。零值即生产配置；字段只用于把请求指向测试服务。
type Options struct {
	// BaseURL 是 X 站点根地址（GraphQL、REST、首页、账号探测），默认 https://x.com。
	BaseURL string
	// APIBaseURL 是 guest token 激活接口所在主机，默认 https://api.x.com。
	APIBaseURL string
	// Transport 非空时所有请求都使用它，并忽略全局与账号级代理。只用于测试。
	Transport http.RoundTripper
	// TransactionIDs 非空时替代内置的 xtid 生成器。只用于测试。
	TransactionIDs TransactionIDGenerator
	// DisablePacing 关闭每账号 3 秒请求间隔和进程级节流（httpx.WaitXRequest）。只用于测试。
	DisablePacing bool
}

const (
	defaultBaseURL    = "https://x.com"
	defaultAPIBaseURL = "https://api.x.com"
)

// Engine 持有跨 Source 重建共享的状态：xtid 生成器、queryId 表、每个账号的内存
// 执行槽与请求间隔、guest token 缓存和账号探测记录。进程内只创建一个，jobs 每次
// 重建 Source 时复用。所有方法都可以并发调用。
type Engine struct {
	store      *storage.Store
	opts       Options
	baseURL    string
	apiBaseURL string

	// now 与 sleep 是可替换的时钟。SQLite 里的时间按文本比较，只在不同秒之间可靠，
	// 测试用整秒推进的假时钟驱动锁、等待和退避。
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	tidMu    sync.Mutex
	tid      TransactionIDGenerator
	tidProxy string

	// globalProxy 是最近一次 Source 使用的全局代理，作为账号未单独配置代理时的
	// 探测/请求回退。HomeProber 可能在 Source 之前被调用，此时为空。
	globalMu    sync.Mutex
	globalProxy string

	queryIDs queryIDTable
	slots    accountSlots
	guest    guestTokens
	probes   probeRecords
	logs     rateLimitedLog
}

// NewEngine 创建 Engine。构造时不发起任何网络请求，也不访问数据库。
func NewEngine(store *storage.Store, opts Options) *Engine {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	apiBaseURL := strings.TrimRight(strings.TrimSpace(opts.APIBaseURL), "/")
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	return &Engine{
		store:      store,
		opts:       opts,
		baseURL:    baseURL,
		apiBaseURL: apiBaseURL,
		now:        time.Now,
		sleep:      sleepContext,
	}
}

// Source 返回绑定到当前全局配置（全局代理等）的 xapi.Source，签名与
// jobs.XSourceFactory 一致。账号每次请求时从 x_accounts 读取，账号变化无需重建。
// 已废弃的 cfg.AuthToken/CSRFToken/AdditionalCookies 不会被读取。
func (e *Engine) Source(ctx context.Context, cfg config.AppConfig) (xapi.Source, error) {
	if e == nil || e.store == nil {
		return nil, errors.New("xscrape: Engine 未初始化")
	}
	proxy := strings.TrimSpace(cfg.ProxyURL)
	if err := httpx.ValidateProxyURL(proxy); err != nil {
		return nil, err
	}
	// 全局代理变化时重建 xtid 生成器；首次使用时在后台发现 queryId。
	e.globalMu.Lock()
	e.globalProxy = proxy
	e.globalMu.Unlock()
	e.generatorFor(proxy)
	e.queryIDs.ensureFresh(ctx, e, proxy)
	return &source{engine: e, proxy: proxy}, nil
}

// HomeProber 返回账号检测用的探测函数，规则见 xapi.HomeProbeResult。探测不读写
// 账号表；legacy 模式下账号管理页的检测同样使用它。
func (e *Engine) HomeProber() xapi.HomeProber {
	return func(ctx context.Context, creds xapi.AccountCredentials) xapi.HomeProbeResult {
		return e.probe(ctx, creds)
	}
}

// transport 返回请求使用的 RoundTripper：测试注入的 Transport 优先，否则按代理取
// httpx 的共享 Transport（空代理时尊重环境变量代理）。
func (e *Engine) transport(proxy string) (http.RoundTripper, error) {
	if e.opts.Transport != nil {
		return e.opts.Transport, nil
	}
	transport, err := httpx.Transport(proxy)
	if err != nil {
		return nil, err
	}
	return transport, nil
}

// generatorFor 返回绑定到全局代理的 xtid 生成器；代理变化时重建。
func (e *Engine) generatorFor(proxy string) TransactionIDGenerator {
	if e.opts.TransactionIDs != nil {
		return e.opts.TransactionIDs
	}
	e.tidMu.Lock()
	defer e.tidMu.Unlock()
	if e.tid != nil && e.tidProxy == proxy {
		return e.tid
	}
	transport, err := e.transport(proxy)
	if err != nil {
		return e.tid
	}
	e.tid = xtid.New(newHTTPClient(transport), e.baseURL, xapi.UserAgent)
	e.tidProxy = proxy
	return e.tid
}

// currentGenerator 返回已有的生成器（可能为 nil），不会新建。账号探测只在已有
// 生成器时附带 transaction id，避免 legacy 模式下的检测额外抓取 x.com 首页。
func (e *Engine) currentGenerator() TransactionIDGenerator {
	if e.opts.TransactionIDs != nil {
		return e.opts.TransactionIDs
	}
	e.tidMu.Lock()
	defer e.tidMu.Unlock()
	return e.tid
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
