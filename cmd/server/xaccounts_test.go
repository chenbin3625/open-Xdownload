package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func openXAccountsTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSyncEnvXAccountsImportsPrimaryAndBackupCookies(t *testing.T) {
	store := openXAccountsTestStore(t)
	ctx := context.Background()
	t.Setenv("OPEN_XDOWNLOAD_AUTH_TOKEN", "primary-auth-token-1111")
	t.Setenv("OPEN_XDOWNLOAD_CT0", "primary-ct0-1111")
	t.Setenv("OPEN_XDOWNLOAD_ADDITIONAL_COOKIES", "auth_token=backup-auth-token-2222; ct0=backup-ct0-2222")

	if err := syncEnvXAccounts(ctx, store); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %+v, want primary + backup", accounts)
	}
	if accounts[0].AuthToken != "primary-auth-token-1111" || !accounts[0].IsPrimary || !accounts[0].Active || accounts[0].Source != "env" {
		t.Fatalf("primary = %+v", accounts[0])
	}
	if accounts[1].AuthToken != "backup-auth-token-2222" || accounts[1].IsPrimary || !accounts[1].Active || accounts[1].Source != "env" {
		t.Fatalf("backup = %+v", accounts[1])
	}

	// 环境变量移除后，对应的 env 账号跟着消失；库里导入的账号不受影响。
	if _, err := store.CreateXAccount(ctx, storage.XAccount{AuthToken: "db-auth-token-3333", CSRFToken: "db-ct0-3333"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPEN_XDOWNLOAD_ADDITIONAL_COOKIES", "")
	t.Setenv("OPEN_XDOWNLOAD_AUTH_TOKEN", "")
	t.Setenv("OPEN_XDOWNLOAD_CT0", "")
	if err := syncEnvXAccounts(ctx, store); err != nil {
		t.Fatal(err)
	}
	accounts, err = store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].AuthToken != "db-auth-token-3333" || !accounts[0].IsPrimary {
		t.Fatalf("accounts after clearing env = %+v, want only the db account promoted to primary", accounts)
	}
}

func TestSyncEnvXAccountsIgnoresHalfConfiguredPrimary(t *testing.T) {
	store := openXAccountsTestStore(t)
	ctx := context.Background()
	// 只有 auth_token 没有 ct0：这组 Cookie 不完整，不能写成账号。
	t.Setenv("OPEN_XDOWNLOAD_AUTH_TOKEN", "half-auth-token-4444")
	t.Setenv("OPEN_XDOWNLOAD_CT0", "")
	if err := syncEnvXAccounts(ctx, store); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %+v, want none for half-configured cookie", accounts)
	}
}

func TestHomeProberAvailableInBothModes(t *testing.T) {
	store := openXAccountsTestStore(t)
	// 账号检测与数据源实现无关：两种模式下都要注入探测函数。
	for _, mode := range []string{jobs.ScraperLegacy, jobs.ScraperXScrape} {
		t.Setenv("OPEN_XDOWNLOAD_SCRAPER", mode)
		if homeProber(newXScrapeEngine(store)) == nil {
			t.Fatalf("%s mode must provide a home prober", mode)
		}
	}
	if homeProber(nil) != nil {
		t.Fatal("nil engine must not provide a home prober")
	}
}
