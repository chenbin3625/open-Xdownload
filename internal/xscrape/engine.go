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
	"fmt"
	"net"
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
	defaultBaseURL      = "https://x.com"
	defaultAPIBaseURL   = "https://api.x.com"
	maxCachedTransports = 64
)

// proxyDialTimeout 限制单次代理/TCP 建连耗时，避免死代理在 30s×3 次重试里卡住多账号故障转移。
var proxyDialTimeout = 10 * time.Second

// proxyDialTimeoutError 表示单次代理/建连超时，不包装 context.DeadlineExceeded，
// 防止上层误判为调用方 ctx 超时而中止多账号故障转移。
type proxyDialTimeoutError struct {
	err error
}

func (e *proxyDialTimeoutError) Error() string {
	return fmt.Sprintf("连接代理或远端超时: %v", e.err)
}
func (e *proxyDialTimeoutError) Timeout() bool   { return true }
func (e *proxyDialTimeoutError) Temporary() bool { return true }

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

	tidMu            sync.Mutex
	tid              TransactionIDGenerator
	tidProxy         string
	tidInvalidatedAt time.Time

	// globalProxy 是最近一次 Source 使用的全局代理，作为账号未单独配置代理时的
	// 探测/请求回退。HomeProber 可能在 Source 之前被调用，此时为空。
	globalMu    sync.Mutex
	globalProxy string

	transportMu    sync.Mutex
	transports     map[string]*http.Transport
	transportOrder []string

	queryIDs   queryIDTable
	slots      accountSlots
	guest      guestTokens
	probes     probeRecords
	assets     assetCache
	rateLimits rateLimitTracker
	logs       rateLimitedLog
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
	// 全局代理变化时重建 xtid 生成器；queryId 发现延迟到首个真实 GraphQL 请求时后台触发。
	e.globalMu.Lock()
	e.globalProxy = proxy
	e.globalMu.Unlock()
	e.generatorFor(proxy)
	return &source{engine: e, proxy: proxy}, nil
}

// HomeProber 返回账号检测用的探测函数，规则见 xapi.HomeProbeResult。探测不读写
// 账号表；legacy 模式下账号管理页的检测同样使用它。
func (e *Engine) HomeProber() xapi.HomeProber {
	return func(ctx context.Context, creds xapi.AccountCredentials) xapi.HomeProbeResult {
		return e.probe(ctx, creds)
	}
}

// transport 返回请求使用的 RoundTripper：测试注入的 Transport 优先，否则基于
// httpx 共享 Transport 克隆一份带短建连超时的 Transport（按代理 LRU 缓存以复用连接池）。
func (e *Engine) transport(proxy string) (http.RoundTripper, error) {
	if e.opts.Transport != nil {
		return e.opts.Transport, nil
	}
	key := strings.TrimSpace(proxy)
	e.transportMu.Lock()
	if cached, ok := e.transports[key]; ok {
		e.touchTransportOrderLocked(key)
		e.transportMu.Unlock()
		return cached, nil
	}
	e.transportMu.Unlock()

	shared, err := httpx.Transport(key)
	if err != nil {
		return nil, err
	}
	cloned := wrapDialTimeout(shared.Clone())

	e.transportMu.Lock()
	defer e.transportMu.Unlock()
	if cached, ok := e.transports[key]; ok {
		cloned.CloseIdleConnections()
		e.touchTransportOrderLocked(key)
		return cached, nil
	}
	if e.transports == nil {
		e.transports = make(map[string]*http.Transport, maxCachedTransports)
	}
	if len(e.transportOrder) >= maxCachedTransports {
		oldest := e.transportOrder[0]
		e.transportOrder = e.transportOrder[1:]
		if evicted, ok := e.transports[oldest]; ok {
			delete(e.transports, oldest)
			evicted.CloseIdleConnections()
		}
	}
	e.transports[key] = cloned
	e.transportOrder = append(e.transportOrder, key)
	return cloned, nil
}

func (e *Engine) touchTransportOrderLocked(key string) {
	n := len(e.transportOrder)
	if n <= 1 || e.transportOrder[n-1] == key {
		return
	}
	for i, existing := range e.transportOrder {
		if existing == key {
			copy(e.transportOrder[i:], e.transportOrder[i+1:])
			e.transportOrder[n-1] = key
			return
		}
	}
}

func wrapDialTimeout(tr *http.Transport) *http.Transport {
	baseDial := tr.DialContext
	if baseDial == nil {
		baseDial = (&net.Dialer{Timeout: proxyDialTimeout, KeepAlive: 30 * time.Second}).DialContext
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		timeout := proxyDialTimeout
		if timeout <= 0 {
			return baseDial(ctx, network, addr)
		}
		dialCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		conn, err := baseDial(dialCtx, network, addr)
		if err != nil && ctx.Err() == nil && errors.Is(dialCtx.Err(), context.DeadlineExceeded) {
			return nil, &proxyDialTimeoutError{err: err}
		}
		return conn, err
	}
	return tr
}

// metaProxyFor 返回 xtid / queryId 元数据抓取应使用的代理：优先全局代理，
// 未配置全局代理时复用已绑定的 tidProxy 或回退到当前账号的代理，避免无全局代理时
// 元数据请求直连 x.com 失败。
func (e *Engine) metaProxyFor(accountProxy string) string {
	if global := e.proxyFor(""); global != "" {
		return global
	}
	e.tidMu.Lock()
	sticky := e.tidProxy
	e.tidMu.Unlock()
	if sticky != "" {
		return sticky
	}
	return strings.TrimSpace(accountProxy)
}

// generatorFor 返回绑定到指定代理的 xtid 生成器；代理变化时重建。
func (e *Engine) generatorFor(proxy string) TransactionIDGenerator {
	if e.opts.TransactionIDs != nil {
		return e.opts.TransactionIDs
	}
	proxy = strings.TrimSpace(proxy)
	e.tidMu.Lock()
	defer e.tidMu.Unlock()
	if e.tid != nil && e.tidProxy == proxy {
		return e.tid
	}
	transport, err := e.transport(proxy)
	if err != nil {
		return e.tid
	}
	e.tid = xtid.New(newHTTPClient(e.assetRoundTripper(proxy, transport)), e.baseURL, xapi.UserAgent)
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
