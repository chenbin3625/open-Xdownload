package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/chenbin3625/open-Xdownload/internal/xscrape"
)

// syncEnvXAccounts 把环境变量里的 Cookie 同步成 source='env' 账号（设计见
// docs/xscrape-design.md「环境变量账号」）：OPEN_XDOWNLOAD_AUTH_TOKEN/CT0 是主账号，
// OPEN_XDOWNLOAD_ADDITIONAL_COOKIES 逐条导入为非主账号；不再出现在环境变量里的 env
// 账号会被删除。没设置任何相关环境变量时不动库里的账号。
func syncEnvXAccounts(ctx context.Context, store *storage.Store) error {
	authToken := envTrimmed("OPEN_XDOWNLOAD_AUTH_TOKEN")
	csrfToken := envTrimmed("OPEN_XDOWNLOAD_CT0")
	additional := envTrimmed("OPEN_XDOWNLOAD_ADDITIONAL_COOKIES")
	// 即使一个环境变量都没设置也要同步：SyncEnvXAccounts 以"本次列出的集合"为准，
	// 之前由环境变量写入、现在已消失的账号要删掉。
	accounts := make([]storage.EnvXAccount, 0, 1)
	if authToken != "" && csrfToken != "" {
		accounts = append(accounts, storage.EnvXAccount{AuthToken: authToken, CSRFToken: csrfToken, Primary: true})
	} else if authToken != "" || csrfToken != "" {
		// 只配了一半时按"Cookie 不完整"处理：告警后照常同步其余环境变量，
		// 这组残缺的 Cookie 不会被写成账号。
		log.Printf("env: OPEN_XDOWNLOAD_AUTH_TOKEN 与 OPEN_XDOWNLOAD_CT0 必须同时设置，忽略这组 Cookie")
	}
	for _, cookie := range config.ParseCookiePairs(additional) {
		accounts = append(accounts, storage.EnvXAccount{
			AuthToken: cookie.AuthToken,
			CSRFToken: cookie.CSRFToken,
		})
	}
	return store.SyncEnvXAccounts(ctx, accounts)
}

// envTrimmed 读取环境变量并去掉首尾空白；纯空白视为未设置。
func envTrimmed(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// newXScrapeEngine 创建 xscrape 引擎。无论 scraper 模式都用它做账号检测：探测走的是
// 独立的账号接口，不碰生产池；xscrape 模式下再把 Engine.Source 设为 jobs 的工厂。
// 引擎在进程内只建一个：xtid、queryId 表、账号执行槽要跨 Source 重建复用。
func newXScrapeEngine(store *storage.Store) *xscrape.Engine {
	return xscrape.NewEngine(store, xscrape.Options{})
}

// homeProber 返回账号检测用的探测函数（设计见 docs/xscrape-design.md：两种模式都注入）。
func homeProber(engine *xscrape.Engine) xapi.HomeProber {
	if engine == nil {
		return nil
	}
	return engine.HomeProber()
}

// purgeExpiredXAccountLocks 清掉已过期的锁，并定期重复：锁的判定本身会过滤过期项，
// 这里只是不让表无限增长。ctx 结束时停止。
func purgeExpiredXAccountLocks(ctx context.Context, store *storage.Store) {
	purge := func() {
		if err := store.PurgeExpiredXAccountLocks(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Printf("purge x account locks: %v", err)
		}
	}
	purge()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purge()
		}
	}
}
