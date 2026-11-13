package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newXAccountTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "xaccounts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustCreateXAccount(t *testing.T, store *Store, account XAccount) XAccount {
	t.Helper()
	created, err := store.CreateXAccount(context.Background(), account)
	if err != nil {
		t.Fatalf("create x account: %v", err)
	}
	return created
}

func isValidationError(err error) bool {
	var userErr *UserFacingError
	return errors.As(err, &userErr) && userErr.Status == 400
}

func TestMigrateCookiesToXAccounts(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()

	const additional = `[{"auth_token":"at-2","ct0":"c2"},` +
		`{"auth_token":"at-primary","ct0":"cp"},` +
		`{"auth_token":"at-2","ct0":"c2b"}]`
	if _, err := store.db.Exec(`
UPDATE app_config SET auth_token = ?, csrf_token = ?, additional_cookies = ? WHERE id = 1`,
		"at-primary", "ct0-primary", additional); err != nil {
		t.Fatal(err)
	}
	// Open 时迁移已跑过一次（空配置），这里清掉登记与账号后重放真实数据。
	if _, err := store.db.Exec(`DELETE FROM x_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE name = 'migrate_cookies_to_x_accounts'`); err != nil {
		t.Fatal(err)
	}
	if err := store.runMigrationOnce("migrate_cookies_to_x_accounts", store.migrateCookiesToXAccounts); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	accounts, err := store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d, want 2: %+v", len(accounts), accounts)
	}
	if accounts[0].AuthToken != "at-primary" || !accounts[0].IsPrimary || accounts[0].CSRFToken != "ct0-primary" {
		t.Fatalf("primary account = %+v", accounts[0])
	}
	if accounts[1].AuthToken != "at-2" || accounts[1].IsPrimary {
		t.Fatalf("secondary account = %+v", accounts[1])
	}

	// 幂等：清理登记后重跑，不新增也不报错。
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE name = 'migrate_cookies_to_x_accounts'`); err != nil {
		t.Fatal(err)
	}
	if err := store.runMigrationOnce("migrate_cookies_to_x_accounts", store.migrateCookiesToXAccounts); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}
	accounts, err = store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts after re-run = %d, want 2", len(accounts))
	}
	var primaries int
	for _, account := range accounts {
		if account.IsPrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("primary count = %d, want 1", primaries)
	}
}

func TestPickXAccountRotation(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	a2 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-2", CSRFToken: "c2"})
	a3 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-3", CSRFToken: "c3"})

	base := time.Now().UTC().Truncate(time.Microsecond)
	want := []int64{a1.ID, a2.ID, a3.ID, a1.ID, a2.ID, a3.ID}
	for index, wantID := range want {
		account, ok, err := store.PickXAccount(ctx, "UserMedia", nil, base.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatalf("pick %d: %v", index, err)
		}
		if !ok {
			t.Fatalf("pick %d: no account", index)
		}
		if account.ID != wantID {
			t.Fatalf("pick %d = %d, want %d", index, account.ID, wantID)
		}
		if !account.LastUsed.Valid {
			t.Fatalf("pick %d: last_used not set", index)
		}
	}
}

func TestPickXAccountQueueAndWildcardLocks(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	a2 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-2", CSRFToken: "c2"})
	base := time.Now().UTC().Truncate(time.Microsecond)

	if err := store.LockXAccount(ctx, a1.ID, "UserMedia", base.Add(5*time.Minute), "429"); err != nil {
		t.Fatal(err)
	}
	account, ok, err := store.PickXAccount(ctx, "UserMedia", nil, base)
	if err != nil || !ok {
		t.Fatalf("pick after queue lock: %+v ok=%v err=%v", account, ok, err)
	}
	if account.ID != a2.ID {
		t.Fatalf("picked %d, want %d", account.ID, a2.ID)
	}

	if err := store.LockXAccount(ctx, a2.ID, "*", base.Add(5*time.Minute), "401"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.PickXAccount(ctx, "UserMedia", nil, base); err != nil || ok {
		t.Fatalf("pick with all locked: ok=%v err=%v", ok, err)
	}

	// 只解 a1 的 queue 锁，a2 仍被 '*' 锁住。
	if err := store.UnlockXAccount(ctx, a1.ID, "UserMedia"); err != nil {
		t.Fatal(err)
	}
	account, ok, err = store.PickXAccount(ctx, "UserMedia", nil, base.Add(time.Second))
	if err != nil || !ok || account.ID != a1.ID {
		t.Fatalf("pick after unlock = %+v ok=%v err=%v", account, ok, err)
	}

	// 其他 queue 的锁不应阻止指定 queue 的选取。
	if err := store.UnlockXAccount(ctx, a2.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.LockXAccount(ctx, a2.ID, "Other", base.Add(time.Hour), "cooldown"); err != nil {
		t.Fatal(err)
	}
	account, ok, err = store.PickXAccount(ctx, "UserMedia", nil, base.Add(2*time.Second))
	if err != nil || !ok || account.ID != a2.ID {
		t.Fatalf("pick ignoring other queue lock = %+v ok=%v err=%v", account, ok, err)
	}
}

func TestPickXAccountExcludeAndExpiredLocks(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	a2 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-2", CSRFToken: "c2"})
	base := time.Now().UTC().Truncate(time.Microsecond)

	account, ok, err := store.PickXAccount(ctx, "Q", []int64{a1.ID}, base)
	if err != nil || !ok || account.ID != a2.ID {
		t.Fatalf("exclude pick = %+v ok=%v err=%v", account, ok, err)
	}
	if _, ok, err := store.PickXAccount(ctx, "Q", []int64{a1.ID, a2.ID}, base); err != nil || ok {
		t.Fatalf("exclude all: ok=%v err=%v", ok, err)
	}

	// 过期锁（queue 与 '*'）都不应再阻止选取。
	if err := store.LockXAccount(ctx, a1.ID, "Q", base.Add(-time.Minute), "expired"); err != nil {
		t.Fatal(err)
	}
	account, ok, err = store.PickXAccount(ctx, "Q", []int64{a2.ID}, base)
	if err != nil || !ok || account.ID != a1.ID {
		t.Fatalf("pick with expired queue lock = %+v ok=%v err=%v", account, ok, err)
	}
	if err := store.LockXAccount(ctx, a1.ID, "*", base.Add(-time.Minute), "expired"); err != nil {
		t.Fatal(err)
	}
	account, ok, err = store.PickXAccount(ctx, "Q", []int64{a2.ID}, base)
	if err != nil || !ok || account.ID != a1.ID {
		t.Fatalf("pick with expired wildcard lock = %+v ok=%v err=%v", account, ok, err)
	}
}

func TestEarliestXAccountUnlock(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	a2 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-2", CSRFToken: "c2"})
	base := time.Now().UTC().Truncate(time.Microsecond)
	first := base.Add(5 * time.Minute)
	second := base.Add(10 * time.Minute)

	if _, ok, err := store.EarliestXAccountUnlock(ctx, "Q", nil, base); err != nil || ok {
		t.Fatalf("no locks: ok=%v err=%v", ok, err)
	}
	if err := store.LockXAccount(ctx, a1.ID, "Q", second, "429"); err != nil {
		t.Fatal(err)
	}
	if err := store.LockXAccount(ctx, a2.ID, "Q", first, "429"); err != nil {
		t.Fatal(err)
	}
	until, ok, err := store.EarliestXAccountUnlock(ctx, "Q", nil, base)
	if err != nil || !ok || !until.UTC().Equal(first) {
		t.Fatalf("earliest = %v ok=%v err=%v, want %v", until, ok, err, first)
	}
	until, ok, err = store.EarliestXAccountUnlock(ctx, "Q", []int64{a2.ID}, base)
	if err != nil || !ok || !until.UTC().Equal(second) {
		t.Fatalf("earliest excluding a2 = %v ok=%v err=%v, want %v", until, ok, err, second)
	}
	// '*' 锁也参与最早解锁时间；过期锁不参与。
	wildcard := base.Add(3 * time.Minute)
	if err := store.LockXAccount(ctx, a2.ID, "*", wildcard, "401"); err != nil {
		t.Fatal(err)
	}
	if err := store.LockXAccount(ctx, a1.ID, "Other", base.Add(-time.Minute), "expired"); err != nil {
		t.Fatal(err)
	}
	until, ok, err = store.EarliestXAccountUnlock(ctx, "Q", nil, base)
	if err != nil || !ok || !until.UTC().Equal(wildcard) {
		t.Fatalf("earliest with wildcard = %v ok=%v err=%v, want %v", until, ok, err, wildcard)
	}
}

func TestPrimaryUniquenessAndPromotion(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	if !a1.IsPrimary {
		t.Fatal("first account should be primary")
	}
	a2, err := store.CreateXAccount(ctx, XAccount{AuthToken: "at-2", CSRFToken: "c2", IsPrimary: true})
	if err != nil {
		t.Fatal(err)
	}
	a1, err = store.GetXAccount(ctx, a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a1.IsPrimary || !a2.IsPrimary {
		t.Fatalf("primary not moved: a1=%v a2=%v", a1.IsPrimary, a2.IsPrimary)
	}
	if err := store.DeleteXAccount(ctx, a2.ID); err != nil {
		t.Fatal(err)
	}
	a1, err = store.GetXAccount(ctx, a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !a1.IsPrimary {
		t.Fatal("min active account should be promoted to primary")
	}

	// 删除 primary 后没有 active 账号时保持无 primary。
	a3 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-3", CSRFToken: "c3"})
	primary := true
	if _, err := store.UpdateXAccount(ctx, a3.ID, XAccountPatch{IsPrimary: &primary}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeactivateXAccount(ctx, a1.ID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteXAccount(ctx, a3.ID); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.IsPrimary {
			t.Fatalf("no active account should be primary: %+v", account)
		}
	}
}

func TestCreateXAccountRejectsDuplicate(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	_, err := store.CreateXAccount(ctx, XAccount{AuthToken: "at-1", CSRFToken: "c2"})
	if !isValidationError(err) {
		t.Fatalf("duplicate auth_token err = %v, want validation", err)
	}
}

func TestEnvXAccountProtection(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEnvXAccount(ctx, "env-token", "env-ct0"); err != nil {
		t.Fatal(err)
	}
	dbAccount := mustCreateXAccount(t, store, XAccount{AuthToken: "db-token", CSRFToken: "db-ct0"})

	accounts, err := store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var envAccount XAccount
	for _, account := range accounts {
		if account.Source == "env" {
			envAccount = account
		}
	}
	if envAccount.ID == 0 || !envAccount.IsPrimary || envAccount.AuthToken != "env-token" {
		t.Fatalf("env account = %+v", envAccount)
	}
	dbAccount, err = store.GetXAccount(ctx, dbAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbAccount.IsPrimary {
		t.Fatal("env upsert should clear other primaries")
	}

	// 重复 upsert 按 auth_token 更新而不是新增。
	if err := store.UpsertEnvXAccount(ctx, "env-token", "env-ct0-new"); err != nil {
		t.Fatal(err)
	}
	accounts, err = store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts after re-upsert = %d, want 2", len(accounts))
	}

	newToken, newCSRF, extra := "other", "other-ct0", "{}"
	for _, patch := range []XAccountPatch{
		{AuthToken: &newToken},
		{CSRFToken: &newCSRF},
		{ExtraCookies: &extra},
	} {
		if _, err := store.UpdateXAccount(ctx, envAccount.ID, patch); !isValidationError(err) {
			t.Fatalf("env cookie patch err = %v, want validation", err)
		}
	}
	inactive := false
	if _, err := store.UpdateXAccount(ctx, envAccount.ID, XAccountPatch{Active: &inactive}); err != nil {
		t.Fatalf("env active patch should be allowed: %v", err)
	}
	if err := store.DeleteXAccount(ctx, envAccount.ID); !isValidationError(err) {
		t.Fatalf("delete env account err = %v, want validation", err)
	}
}

func TestSyncEnvXAccounts(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	dbAccount := mustCreateXAccount(t, store, XAccount{AuthToken: "db-token", CSRFToken: "db-ct0"})

	if err := store.SyncEnvXAccounts(ctx, []EnvXAccount{
		{AuthToken: "env-main", CSRFToken: "m1", Primary: true},
		{AuthToken: "env-extra", CSRFToken: "e1"},
		{AuthToken: "env-main", CSRFToken: "dup"},
	}); err != nil {
		t.Fatal(err)
	}
	byToken := func() map[string]XAccount {
		t.Helper()
		accounts, err := store.ListXAccounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := make(map[string]XAccount, len(accounts))
		for _, account := range accounts {
			out[account.AuthToken] = account
		}
		return out
	}
	accounts := byToken()
	if len(accounts) != 3 {
		t.Fatalf("accounts = %d, want 3", len(accounts))
	}
	if main := accounts["env-main"]; main.Source != "env" || !main.IsPrimary || main.CSRFToken != "m1" {
		t.Fatalf("env-main = %+v", main)
	}
	if extra := accounts["env-extra"]; extra.Source != "env" || extra.IsPrimary {
		t.Fatalf("env-extra = %+v", extra)
	}
	if accounts["db-token"].IsPrimary {
		t.Fatal("db account should lose primary to env-main")
	}

	// 重启时同一组 Cookie 不能把已停用的 env 账号重新启用；换了 ct0 才重新启用。
	if err := store.DeactivateXAccount(ctx, accounts["env-extra"].ID, "Cookie 已失效"); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncEnvXAccounts(ctx, []EnvXAccount{
		{AuthToken: "env-main", CSRFToken: "m1", Primary: true},
		{AuthToken: "env-extra", CSRFToken: "e1"},
	}); err != nil {
		t.Fatal(err)
	}
	if extra := byToken()["env-extra"]; extra.Active || extra.ErrorMsg == "" {
		t.Fatalf("same cookie should stay deactivated: %+v", extra)
	}
	if err := store.SyncEnvXAccounts(ctx, []EnvXAccount{
		{AuthToken: "env-main", CSRFToken: "m1", Primary: true},
		{AuthToken: "env-extra", CSRFToken: "e2"},
	}); err != nil {
		t.Fatal(err)
	}
	if extra := byToken()["env-extra"]; !extra.Active || extra.ErrorMsg != "" || extra.CSRFToken != "e2" {
		t.Fatalf("new ct0 should reactivate: %+v", extra)
	}

	// 移除环境变量后删除对应 env 账号，主账号回落到最小 id 的 active 账号。
	if err := store.SyncEnvXAccounts(ctx, nil); err != nil {
		t.Fatal(err)
	}
	accounts = byToken()
	if len(accounts) != 1 {
		t.Fatalf("accounts after removing env = %d, want 1", len(accounts))
	}
	if remaining := accounts["db-token"]; remaining.ID != dbAccount.ID || !remaining.IsPrimary {
		t.Fatalf("db account should be promoted: %+v", remaining)
	}
}

func TestApplyXAccountCheck(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	account := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	future := time.Now().UTC().Add(time.Hour)
	if err := store.LockXAccount(ctx, account.ID, "*", future, "401"); err != nil {
		t.Fatal(err)
	}
	if err := store.LockXAccount(ctx, account.ID, "UserMedia", future, "429"); err != nil {
		t.Fatal(err)
	}

	// 无法判断：只记录错误，不停用。
	got, err := store.ApplyXAccountCheck(ctx, account.ID, XAccountCheck{Err: "network"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active || got.ErrorMsg != "network" {
		t.Fatalf("inconclusive check = %+v", got)
	}

	// 确认失效：停用并记录原因。
	got, err = store.ApplyXAccountCheck(ctx, account.ID, XAccountCheck{Unauthorized: true, Err: "code 32"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.ErrorMsg != "code 32" {
		t.Fatalf("unauthorized check = %+v", got)
	}

	// 成功：被系统停用的账号重新启用，清 '*' 锁，保留 queue 锁。
	got, err = store.ApplyXAccountCheck(ctx, account.ID, XAccountCheck{ScreenName: "alice"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active || got.ErrorMsg != "" || got.ScreenName != "alice" || !got.VerifiedAt.Valid {
		t.Fatalf("successful check = %+v", got)
	}
	locks, err := store.ListXAccountLocks(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 1 || locks[0].Queue != "UserMedia" {
		t.Fatalf("locks after success = %+v, want only UserMedia", locks)
	}

	// 用户手动停用（PATCH active=false 会清空 error_msg）后，检测成功不能重新启用。
	inactive := false
	got, err = store.UpdateXAccount(ctx, account.ID, XAccountPatch{Active: &inactive})
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.ErrorMsg != "" {
		t.Fatalf("manual deactivate = %+v", got)
	}
	got, err = store.ApplyXAccountCheck(ctx, account.ID, XAccountCheck{ScreenName: "alice"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Active {
		t.Fatal("manually deactivated account must stay inactive after a successful check")
	}
	// 失败的检测也不能抹掉手动停用标记。
	for _, check := range []XAccountCheck{{Err: "network"}, {Unauthorized: true, Err: "code 32"}, {ScreenName: "alice"}} {
		got, err = store.ApplyXAccountCheck(ctx, account.ID, check, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if got.Active || got.ErrorMsg != "" {
		t.Fatalf("manual deactivation marker lost: %+v", got)
	}

	if _, err := store.ApplyXAccountCheck(ctx, 9999, XAccountCheck{}, time.Time{}); err == nil {
		t.Fatal("missing account should return an error")
	}
}

func TestUpdateXAccountCredentialsReactivatesSystemDisabled(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	account := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	if _, err := store.ApplyXAccountCheck(ctx, account.ID, XAccountCheck{ScreenName: "alice"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(time.Hour)
	for _, queue := range []string{"*", "UserMedia"} {
		if err := store.LockXAccount(ctx, account.ID, queue, future, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeactivateXAccount(ctx, account.ID, ""); err != nil {
		t.Fatal(err)
	}

	token, ct0 := "at-2", "c2"
	got, err := store.UpdateXAccount(ctx, account.ID, XAccountPatch{AuthToken: &token, CSRFToken: &ct0})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active || got.ErrorMsg != "" || got.VerifiedAt.Valid || got.AuthToken != "at-2" {
		t.Fatalf("system-disabled account after cookie replacement = %+v", got)
	}
	locks, err := store.ListXAccountLocks(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 1 || locks[0].Queue != "UserMedia" {
		t.Fatalf("locks after cookie replacement = %+v, want only UserMedia", locks)
	}

	// 手动停用的账号换 Cookie 后保持停用。
	inactive := false
	if _, err := store.UpdateXAccount(ctx, account.ID, XAccountPatch{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	ct0 = "c3"
	got, err = store.UpdateXAccount(ctx, account.ID, XAccountPatch{CSRFToken: &ct0})
	if err != nil {
		t.Fatal(err)
	}
	if got.Active {
		t.Fatal("manually deactivated account must stay inactive after cookie replacement")
	}
}

func TestMarkXAccountUsed(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	account := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.MarkXAccountUsed(ctx, account.ID, at); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetXAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastUsed.Valid || !got.LastUsed.Time.Equal(at) {
		t.Fatalf("last_used = %+v, want %v", got.LastUsed, at)
	}
	if err := store.MarkXAccountUsed(ctx, 9999, at); err == nil {
		t.Fatal("missing account should return an error")
	}
}

func TestXAccountExtraCookieMap(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"", 0, false},
		{"{}", 0, false},
		{`{"guest_id":"v1","lang":"en"}`, 2, false},
		{"not-json", 0, true},
	} {
		extra, err := XAccount{ExtraCookies: tc.raw}.ExtraCookieMap()
		if (err != nil) != tc.wantErr || len(extra) != tc.want {
			t.Fatalf("ExtraCookieMap(%q) = %v, %v", tc.raw, extra, err)
		}
	}
}

func TestLockXAccountKeepsLaterUntil(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	account := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	base := time.Now().UTC().Truncate(time.Microsecond)

	later := base.Add(10 * time.Minute)
	if err := store.LockXAccount(ctx, account.ID, "Q", later, "first"); err != nil {
		t.Fatal(err)
	}
	if err := store.LockXAccount(ctx, account.ID, "Q", base.Add(1*time.Minute), "earlier"); err != nil {
		t.Fatal(err)
	}
	locks, err := store.ListXAccountLocks(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 1 || !locks[0].Until.UTC().Equal(later) || locks[0].Reason != "first" {
		t.Fatalf("locks = %+v, want later until and original reason", locks)
	}
}

func TestXAccountStats(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	a2 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-2", CSRFToken: "c2"})
	at := time.Date(2026, 11, 13, 10, 30, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if err := store.IncrementXAccountStat(ctx, a1.ID, "UserMedia", at); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.IncrementXAccountStat(ctx, a1.ID, "UserTweets", at); err != nil {
		t.Fatal(err)
	}
	if err := store.IncrementXAccountStat(ctx, a2.ID, "UserMedia", at); err != nil {
		t.Fatal(err)
	}

	stats, err := store.ListXAccountStats(ctx, "2026-11-13")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 3 {
		t.Fatalf("stats = %+v, want 3 rows", stats)
	}
	want := []XAccountStat{
		{AccountID: a1.ID, Queue: "UserMedia", Day: "2026-11-13", Requests: 3},
		{AccountID: a1.ID, Queue: "UserTweets", Day: "2026-11-13", Requests: 1},
		{AccountID: a2.ID, Queue: "UserMedia", Day: "2026-11-13", Requests: 1},
	}
	for index, row := range want {
		if stats[index] != row {
			t.Fatalf("stats[%d] = %+v, want %+v", index, stats[index], row)
		}
	}
	if other, err := store.ListXAccountStats(ctx, "2026-11-14"); err != nil || len(other) != 0 {
		t.Fatalf("other day = %+v err=%v", other, err)
	}
}

func TestPurgeExpiredXAccountLocks(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	account := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := store.LockXAccount(ctx, account.ID, "expired", now.Add(-time.Minute), "old"); err != nil {
		t.Fatal(err)
	}
	if err := store.LockXAccount(ctx, account.ID, "active", now.Add(time.Minute), "new"); err != nil {
		t.Fatal(err)
	}
	if err := store.PurgeExpiredXAccountLocks(ctx, now); err != nil {
		t.Fatal(err)
	}
	locks, err := store.ListXAccountLocks(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 1 || locks[0].Queue != "active" {
		t.Fatalf("locks after purge = %+v", locks)
	}
}

func TestPickXAccountConcurrent(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	a2 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-2", CSRFToken: "c2"})
	a3 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-3", CSRFToken: "c3"})

	const workers = 20
	base := time.Now().UTC()
	type pickResult struct {
		id  int64
		ok  bool
		err error
	}
	results := make(chan pickResult, workers)
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			account, ok, err := store.PickXAccount(ctx, "UserMedia", nil, base.Add(time.Duration(index)*time.Millisecond))
			results <- pickResult{id: account.ID, ok: ok, err: err}
		}(index)
	}
	wg.Wait()
	close(results)

	counts := map[int64]int{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent pick: %v", result.err)
		}
		if !result.ok {
			t.Fatal("concurrent pick returned no account")
		}
		counts[result.id]++
	}
	if len(counts) != 3 {
		t.Fatalf("picked accounts = %v, want all three", counts)
	}
	for _, id := range []int64{a1.ID, a2.ID, a3.ID} {
		if counts[id] == 0 {
			t.Fatalf("account %d never picked: %v", id, counts)
		}
	}
	var total int
	for _, count := range counts {
		total += count
	}
	if total != workers {
		t.Fatalf("total picks = %d, want %d", total, workers)
	}
}

func TestGetXAccountNotFound(t *testing.T) {
	store := newXAccountTestStore(t)
	_, err := store.GetXAccount(context.Background(), 999)
	var userErr *UserFacingError
	if !errors.As(err, &userErr) || userErr.Status != 404 {
		t.Fatalf("err = %v, want 404 UserFacingError", err)
	}
}

func TestXAccountListOrdering(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	a2 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-2", CSRFToken: "c2"})
	primary := true
	if _, err := store.UpdateXAccount(ctx, a2.ID, XAccountPatch{IsPrimary: &primary}); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListXAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != a2.ID || accounts[1].ID != a1.ID {
		t.Fatalf("ordering = %+v, want primary first", []int64{accounts[0].ID, accounts[1].ID})
	}
}
