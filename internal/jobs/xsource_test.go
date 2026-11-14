package jobs

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

// fakeXSource 只用于区分缓存身份，不实现任何方法（嵌入 nil 接口即可满足 xapi.Source）。
type fakeXSource struct{ xapi.Source }

func TestInvalidateXSourceRebuildsCachedSource(t *testing.T) {
	manager := &Manager{}
	calls := 0
	factory := func(context.Context, config.AppConfig) (xapi.Source, error) {
		calls++
		return &fakeXSource{}, nil
	}
	manager.SetXSourceFactory(factory)
	cfg := config.AppConfig{ProxyURL: "http://127.0.0.1:7890"}

	first, err := manager.XSource(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.XSource(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first != second {
		t.Fatalf("unequal config should reuse cached source: calls=%d reused=%t", calls, first == second)
	}

	manager.InvalidateXSource()
	third, err := manager.XSource(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || third == second {
		t.Fatalf("InvalidateXSource should rebuild source: calls=%d reused=%t", calls, third == second)
	}

	// 替换工厂同样要丢弃当前缓存。
	manager.SetXSourceFactory(factory)
	fourth, err := manager.XSource(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || fourth == third {
		t.Fatalf("SetXSourceFactory should invalidate cached source: calls=%d reused=%t", calls, fourth == third)
	}
}

// 缓存键只含全局代理：已废弃的 Cookie 字段变化不触发重建，代理变化才会。
func TestXSourceCacheKeyIsGlobalProxyOnly(t *testing.T) {
	manager := &Manager{}
	calls := 0
	manager.SetXSourceFactory(func(context.Context, config.AppConfig) (xapi.Source, error) {
		calls++
		return &fakeXSource{}, nil
	})
	ctx := context.Background()
	base := config.AppConfig{ProxyURL: "http://127.0.0.1:7890"}
	first, err := manager.XSource(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	legacy := base
	legacy.AuthToken, legacy.CSRFToken, legacy.AdditionalCookies = "old-auth", "old-ct0", "auth_token=a; ct0=b"
	second, err := manager.XSource(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first != second {
		t.Fatalf("legacy cookie fields changed the cache key: calls=%d reused=%t", calls, first == second)
	}
	proxied := base
	proxied.ProxyURL = "http://127.0.0.1:7891"
	if _, err := manager.XSource(ctx, proxied); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("proxy change did not rebuild the source: calls=%d", calls)
	}
	if got := xPoolConfigKey(legacy); got != base.ProxyURL {
		t.Fatalf("xPoolConfigKey = %q, want only the proxy", got)
	}
}

// 默认工厂只用 active 账号，active 主账号排第一位，额外 Cookie 损坏时忽略它们。
func TestLegacyXAccountsKeepsActivePrimaryFirst(t *testing.T) {
	stored := []storage.XAccount{
		{ID: 1, AuthToken: "a1", CSRFToken: "c1", Active: true},
		{ID: 2, AuthToken: "a2", CSRFToken: "c2", Active: false, IsPrimary: false},
		{ID: 3, AuthToken: "a3", CSRFToken: "c3", Active: true, IsPrimary: true, UserAgent: "ua-3", Proxy: "http://127.0.0.1:1080", ExtraCookies: `{"lang":"en"}`},
		{ID: 4, AuthToken: "a4", CSRFToken: "c4", Active: true, ExtraCookies: `{broken`},
	}
	got := legacyXAccounts(stored)
	want := []xclient.Account{
		{AuthToken: "a3", CSRFToken: "c3", ExtraCookies: map[string]string{"lang": "en"}, UserAgent: "ua-3", Proxy: "http://127.0.0.1:1080", Primary: true},
		{AuthToken: "a1", CSRFToken: "c1"},
		{AuthToken: "a4", CSRFToken: "c4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacyXAccounts = %+v\nwant %+v", got, want)
	}

	// 主账号停用：没有任何账号被标记为 Primary，备用账号不会顶替主账号。
	stored[2].Active = false
	for _, account := range legacyXAccounts(stored) {
		if account.Primary {
			t.Fatalf("inactive primary still marked primary: %+v", account)
		}
	}
}

// 默认工厂从 x_accounts 构建 legacy 池：主账号停用时池里没有主账号；没有 active 账号时
// 退回只支持 FetchTweet 的 guest source。
func TestDefaultFactoryBuildsLegacyPoolFromXAccounts(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	manager := NewManager(store, nil, NewEventBus())
	cfg := config.AppConfig{AuthToken: "deprecated-auth", CSRFToken: "deprecated-ct0"}

	empty, err := manager.NewXSource(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if empty.HasPrimary() || empty.Diagnostics().Total != 0 {
		t.Fatalf("no accounts should fall back to the guest source, got %+v", empty.Diagnostics())
	}
	if _, err := empty.GetUserByInput(ctx, "someone"); err == nil {
		t.Fatal("guest source must not serve account-only requests")
	}

	primary, err := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "primary-token", CSRFToken: "primary-ct0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "backup-token", CSRFToken: "backup-ct0"}); err != nil {
		t.Fatal(err)
	}
	source, err := manager.NewXSource(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := source.Diagnostics()
	if !source.HasPrimary() || diagnostics.Total != 2 || !diagnostics.Clients[0].Primary || diagnostics.Clients[1].Primary {
		t.Fatalf("pool = %+v, want primary first plus one backup", diagnostics)
	}

	inactive := false
	if _, err := store.UpdateXAccount(ctx, primary.ID, storage.XAccountPatch{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	source, err = manager.NewXSource(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics = source.Diagnostics()
	if source.HasPrimary() || diagnostics.Total != 1 || diagnostics.Clients[0].Primary {
		t.Fatalf("pool with disabled primary = %+v, want backup only and no primary", diagnostics)
	}
}
