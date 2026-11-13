// Package xscrape 是 twscrape 风格的 X 数据源实现：账号与限流锁持久化在 SQLite
// （storage 的 x_accounts 系列表），请求带 x-client-transaction-id，queryId 支持自动
// 发现。由 OPEN_XDOWNLOAD_SCRAPER=xscrape 启用，设计见 docs/xscrape-design.md。
//
// 本文件目前是契约桩：导出的类型和函数签名是固定的，cmd/server 按它接线；
// 实现完成前 Source 返回错误、HomeProber 返回无法判断。
package xscrape

import (
	"context"
	"errors"
	"net/http"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

var errNotImplemented = errors.New("xscrape 数据源尚未实现")

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

// Engine 持有跨 Source 重建共享的状态：xtid 生成器、queryId 表、每个账号的内存
// 执行槽与请求间隔。进程内只创建一个，jobs 每次重建 Source 时复用。
type Engine struct {
	store *storage.Store
	opts  Options
}

// NewEngine 创建 Engine。构造时不发起任何网络请求。
func NewEngine(store *storage.Store, opts Options) *Engine {
	return &Engine{store: store, opts: opts}
}

// Source 返回绑定到当前全局配置（全局代理等）的 xapi.Source，签名与
// jobs.XSourceFactory 一致。账号每次请求时从 x_accounts 读取，账号变化无需重建。
func (e *Engine) Source(ctx context.Context, cfg config.AppConfig) (xapi.Source, error) {
	return nil, errNotImplemented
}

// HomeProber 返回账号检测用的探测函数，规则见 xapi.HomeProbeResult。探测不读写
// 账号表；legacy 模式下账号管理页的检测同样使用它。
func (e *Engine) HomeProber() xapi.HomeProber {
	return func(context.Context, xapi.AccountCredentials) xapi.HomeProbeResult {
		return xapi.HomeProbeResult{Err: errNotImplemented}
	}
}
