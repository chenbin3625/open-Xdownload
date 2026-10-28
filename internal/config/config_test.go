package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDownloadDirUsesEnvironment(t *testing.T) {
	t.Setenv("OPEN_XDOWNLOAD_DOWNLOAD_DIR", "/tmp/open-xdownload-test")

	cfg := Default()
	if cfg.DownloadDir != "/tmp/open-xdownload-test" {
		t.Fatalf("DownloadDir = %q, want env value", cfg.DownloadDir)
	}
}

func TestRedactedUsesStableMask(t *testing.T) {
	cfg := AppConfig{
		AuthToken: "real-auth-token",
		CSRFToken: "real-csrf-token",
	}

	redacted := cfg.Redacted()
	if redacted.AuthToken != "********" || redacted.CSRFToken != "********" {
		t.Fatalf("unexpected redacted config: %#v", redacted)
	}
}

func TestRedactedURLUserinfoRoundTrip(t *testing.T) {
	redactCases := []struct {
		name string
		url  string
		want string
	}{
		{"proxy with user:pass", "http://alice:s3cret@proxy.local:3128", "http://" + SecretPlaceholder + "@proxy.local:3128"},
		{"webdav with user:pass and path", "https://alice:s3cret@webdav.example/dav/files", "https://" + SecretPlaceholder + "@webdav.example/dav/files"},
		{"url without userinfo unchanged", "http://proxy.local:3128", "http://proxy.local:3128"},
		{"empty url", "", ""},
	}
	for _, tc := range redactCases {
		t.Run("redact/"+tc.name, func(t *testing.T) {
			got := redactURLUserinfo(tc.url)
			if got != tc.want {
				t.Fatalf("redactURLUserinfo(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}

	restoreCases := []struct {
		name     string
		redacted string
		stored   string
		want     string
	}{
		{"placeholder restores stored creds", "http://" + SecretPlaceholder + "@proxy.local:3128", "http://alice:s3cret@proxy.local:3128", "http://alice:s3cret@proxy.local:3128"},
		{"placeholder with no stored creds drops userinfo", "http://" + SecretPlaceholder + "@proxy.local:3128", "http://proxy.local:3128", "http://proxy.local:3128"},
		{"placeholder with changed host drops userinfo", "http://" + SecretPlaceholder + "@evil.local:3128", "http://alice:s3cret@proxy.local:3128", "http://evil.local:3128"},
		{"placeholder with changed scheme drops userinfo", "https://" + SecretPlaceholder + "@proxy.local:3128", "http://alice:s3cret@proxy.local:3128", "https://proxy.local:3128"},
		{"edited url with new creds kept", "http://bob:newpass@proxy.local:3128", "http://alice:s3cret@proxy.local:3128", "http://bob:newpass@proxy.local:3128"},
		{"edited url without creds kept", "http://proxy.local:3128", "http://alice:s3cret@proxy.local:3128", "http://proxy.local:3128"},
		{"empty", "", "", ""},
	}
	for _, tc := range restoreCases {
		t.Run("restore/"+tc.name, func(t *testing.T) {
			got := RestoreURLUserinfo(tc.redacted, tc.stored)
			if got != tc.want {
				t.Fatalf("RestoreURLUserinfo(%q, %q) = %q, want %q", tc.redacted, tc.stored, got, tc.want)
			}
		})
	}
}

func TestNormalizedFileNamingDefaultsAndLimits(t *testing.T) {
	cfg := AppConfig{
		FileNamingMode:    "unknown",
		MaxFilenameLength: 8,
	}.Normalized()
	if cfg.FileNamingMode != FileNamingTweetText {
		t.Fatalf("FileNamingMode = %q, want %q", cfg.FileNamingMode, FileNamingTweetText)
	}
	if cfg.MaxFilenameLength != MinFilenameLength {
		t.Fatalf("MaxFilenameLength = %d, want %d", cfg.MaxFilenameLength, MinFilenameLength)
	}

	cfg = AppConfig{
		FileNamingMode:    FileNamingUserTweet,
		MaxFilenameLength: MaxFilenameLength + 1,
	}.Normalized()
	if cfg.FileNamingMode != FileNamingUserTweet {
		t.Fatalf("FileNamingMode = %q, want %q", cfg.FileNamingMode, FileNamingUserTweet)
	}
	if cfg.MaxFilenameLength != MaxFilenameLength {
		t.Fatalf("MaxFilenameLength = %d, want %d", cfg.MaxFilenameLength, MaxFilenameLength)
	}

	cfg = AppConfig{FileNamingMode: legacyFileNamingTweetIDText}.Normalized()
	if cfg.FileNamingMode != FileNamingUserTweet {
		t.Fatalf("legacy FileNamingMode = %q, want %q", cfg.FileNamingMode, FileNamingUserTweet)
	}
}

func TestNormalizedForcesSerialConcurrency(t *testing.T) {
	cfg := AppConfig{MaxConcurrency: 32}.Normalized()
	if cfg.MaxConcurrency != 1 {
		t.Fatalf("MaxConcurrency = %d, want serial concurrency 1", cfg.MaxConcurrency)
	}

	cfg = AppConfig{MaxConcurrency: 0}.Normalized()
	if cfg.MaxConcurrency != 1 {
		t.Fatalf("default MaxConcurrency = %d, want serial concurrency 1", cfg.MaxConcurrency)
	}
}

func TestRedactAndRestoreAdditionalCookies(t *testing.T) {
	raw := "auth_token=tok1; ct0=csrf1\nauth_token=tok2; ct0=csrf2"
	redacted := RedactAdditionalCookies(raw)
	redactedPairs := ParseCookiePairs(redacted)
	if len(redactedPairs) != 2 || redactedPairs[0].ID == redactedPairs[1].ID || redactedPairs[0].AuthToken != SecretPlaceholder {
		t.Fatalf("redacted pairs = %+v, want two distinct stable masked pairs", redactedPairs)
	}

	// Case 1: Unmodified redacted cookies submitted -> restored identically
	restored := RestoreAdditionalCookies(redacted, raw)
	if pairs := ParseCookiePairs(restored); len(pairs) != 2 || pairs[0].AuthToken != "tok1" || pairs[1].CSRFToken != "csrf2" {
		t.Fatalf("RestoreAdditionalCookies(unmodified) = %+v, want original credentials", pairs)
	}

	// Case 2: User added a 3rd cookie
	addedPairs := append(redactedPairs, AuthCookie{ID: "local-cookie-3", AuthToken: "tok3", CSRFToken: "csrf3"})
	addedBytes, _ := json.Marshal(addedPairs)
	added := string(addedBytes)
	restoredAdded := RestoreAdditionalCookies(added, raw)
	if pairs := ParseCookiePairs(restoredAdded); len(pairs) != 3 || pairs[2].AuthToken != "tok3" {
		t.Fatalf("RestoreAdditionalCookies(added) = %+v, want three credentials", pairs)
	}

	// Case 3: User removes the first row; the second row keeps its own credentials by ID.
	editedPairs := []AuthCookie{redactedPairs[1]}
	editedBytes, _ := json.Marshal(editedPairs)
	edited := string(editedBytes)
	restoredEdited := RestoreAdditionalCookies(edited, raw)
	if pairs := ParseCookiePairs(restoredEdited); len(pairs) != 1 || pairs[0].AuthToken != "tok2" || pairs[0].CSRFToken != "csrf2" {
		t.Fatalf("RestoreAdditionalCookies(edited) = %+v, want second credential", pairs)
	}

	// Case 4: User cleared cookies
	if got := RestoreAdditionalCookies("", raw); got != "" {
		t.Fatalf("RestoreAdditionalCookies(empty) = %q, want empty", got)
	}

	// Case 5 (P1-4): Placeholder values must not survive into merged output.
	// Simulates a submitted cookie with unknown ID where no stored match exists —
	// the placeholder should be stripped, not written as a real credential.
	orphanPairs := []AuthCookie{{ID: "orphan-1", AuthToken: SecretPlaceholder, CSRFToken: SecretPlaceholder}}
	orphanBytes, _ := json.Marshal(orphanPairs)
	restoredOrphan := RestoreAdditionalCookies(string(orphanBytes), raw)
	if restoredOrphan != "" {
		orphanResult := ParseCookiePairs(restoredOrphan)
		for _, p := range orphanResult {
			if p.AuthToken == SecretPlaceholder || p.CSRFToken == SecretPlaceholder {
				t.Fatalf("placeholder leaked into restored cookies: %+v", orphanResult)
			}
		}
	}

	// Case 6 (P1-4): Mixed — one valid new cookie + one orphan placeholder.
	mixedPairs := []AuthCookie{
		{ID: "new-1", AuthToken: "real-tok", CSRFToken: "real-csrf"},
		{ID: "orphan-2", AuthToken: SecretPlaceholder, CSRFToken: SecretPlaceholder},
	}
	mixedBytes, _ := json.Marshal(mixedPairs)
	restoredMixed := RestoreAdditionalCookies(string(mixedBytes), "")
	if pairs := ParseCookiePairs(restoredMixed); len(pairs) != 1 || pairs[0].AuthToken != "real-tok" {
		t.Fatalf("RestoreAdditionalCookies(mixed) = %+v, want only the real cookie", pairs)
	}
}

func TestRedactAdditionalCookiesEmptyParsedReturnsEmpty(t *testing.T) {
	// P1-4: When stored cookies parse to zero entries, RedactAdditionalCookies
	// should return "" instead of "********" to prevent the whole-placeholder
	// expansion path on the frontend.
	got := RedactAdditionalCookies("invalid-not-a-cookie")
	pairs := ParseCookiePairs(got)
	if len(pairs) == 0 && got == SecretPlaceholder {
		t.Fatal("RedactAdditionalCookies returned placeholder for zero parsed entries; want empty string")
	}
}

func TestAdditionalCookiesDoNotRestoreMaskedRowsWithoutIDs(t *testing.T) {
	stored := "auth_token=first; ct0=first-csrf\nauth_token=second; ct0=second-csrf"
	if got := RestoreAdditionalCookies("auth_token=********; ct0=********", stored); got != "" {
		t.Fatalf("anonymous masked row restored credentials by position: %q", got)
	}
}

func TestClearSentinel(t *testing.T) {
	if ClearSentinel != "__CLEAR__" {
		t.Fatalf("ClearSentinel = %q, want __CLEAR__", ClearSentinel)
	}
}

func TestParseCookiePairsAcceptsYAMLColonFormat(t *testing.T) {
	// 与 xclient 的 yaml 用例一致（M2 收敛后统一走这里）。
	raw := "- auth_token: auth-a\n  ct0: csrf-a\n- auth_token: auth-b\n  ct0: csrf-b"
	pairs := ParseCookiePairs(raw)
	if len(pairs) != 2 {
		t.Fatalf("pairs = %#v, want 2", pairs)
	}
	if pairs[0].AuthToken != "auth-a" || pairs[0].CSRFToken != "csrf-a" {
		t.Fatalf("pairs[0] = %#v", pairs[0])
	}
	if pairs[1].AuthToken != "auth-b" || pairs[1].CSRFToken != "csrf-b" {
		t.Fatalf("pairs[1] = %#v", pairs[1])
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	t.Setenv(EnvAuthToken, "env-auth")
	t.Setenv(EnvCSRFToken, "env-ct0")
	t.Setenv(EnvProxyURL, "http://env-proxy:8080")
	t.Setenv(EnvAdditionalCookies, "auth_token=a; ct0=b")

	cfg := ApplyEnvOverrides(AppConfig{
		AuthToken: "db-auth",
	})
	if cfg.AuthToken != "env-auth" || cfg.CSRFToken != "env-ct0" {
		t.Fatalf("env overrides not applied: %#v", cfg)
	}
	if cfg.ProxyURL != "http://env-proxy:8080" {
		t.Fatalf("proxy = %q", cfg.ProxyURL)
	}
	if cfg.AdditionalCookies != "auth_token=a; ct0=b" {
		t.Fatalf("additional cookies = %q", cfg.AdditionalCookies)
	}
}

func TestRevertEnvOnlyEcho(t *testing.T) {
	t.Setenv(EnvProxyURL, "http://env-proxy:8080")
	if got := RevertEnvOnlyEcho("http://env-proxy:8080", "http://db-proxy:3128", EnvProxyURL); got != "http://db-proxy:3128" {
		t.Fatalf("env echo = %q, want db value", got)
	}
	if got := RevertEnvOnlyEcho("http://user-set:9090", "http://db-proxy:3128", EnvProxyURL); got != "http://user-set:9090" {
		t.Fatalf("user value = %q", got)
	}
	t.Setenv(EnvProxyURL, "")
	if got := RevertEnvOnlyEcho("http://env-proxy:8080", "", EnvProxyURL); got != "http://env-proxy:8080" {
		t.Fatalf("no env = %q, want submitted", got)
	}
}

// 逐字段脱敏：有值字段才变占位符，空字段保持空串。
func TestRedactAdditionalCookiesKeepsEmptyFields(t *testing.T) {
	raw := "auth_token=only-auth\n\nauth_token=a2; ct0=c2"
	redacted := RedactAdditionalCookies(raw)
	pairs := ParseCookiePairs(redacted)
	if len(pairs) != 2 {
		t.Fatalf("pairs = %#v, want 2", pairs)
	}
	if pairs[0].AuthToken != SecretPlaceholder {
		t.Fatalf("auth[0] = %q, want placeholder", pairs[0].AuthToken)
	}
	if pairs[0].CSRFToken != "" {
		t.Fatalf("csrf[0] = %q, want empty (was empty in stored)", pairs[0].CSRFToken)
	}
	if pairs[1].AuthToken != SecretPlaceholder || pairs[1].CSRFToken != SecretPlaceholder {
		t.Fatalf("pairs[1] = %#v, want both masked", pairs[1])
	}
}

// 宽松解析：重复 id 保留第一条，不报错。
func TestRestoreAdditionalCookiesDuplicateIDKeepsFirst(t *testing.T) {
	stored := `[{"id":"a","authToken":"tok-a","csrfToken":"csrf-a"}]`
	submitted := `[{"id":"a","authToken":"********","csrfToken":"********"},{"id":"a","authToken":"evil","csrfToken":"evil"}]`
	got := RestoreAdditionalCookies(submitted, stored)
	pairs := ParseCookiePairs(got)
	if len(pairs) != 1 {
		t.Fatalf("pairs = %#v, want 1 (duplicate id dropped)", pairs)
	}
	if pairs[0].AuthToken != "tok-a" || pairs[0].CSRFToken != "csrf-a" {
		t.Fatalf("pairs[0] = %#v, want stored credentials", pairs[0])
	}
}

// 宽松解析：还原不到的占位符置空，半行保留，不返回错误。
func TestRestoreAdditionalCookiesHalfRowAndOrphan(t *testing.T) {
	// 未知 id 的两个占位符 → 整对为空 → 丢弃。
	if got := RestoreAdditionalCookies(`[{"id":"ghost","authToken":"********","csrfToken":"********"}]`, ``); got != "" {
		t.Fatalf("orphan = %q, want empty", got)
	}
	// 半行：只有 auth_token 的真实值 → 保留，csrf 为空。
	got := RestoreAdditionalCookies(`[{"id":"x","authToken":"real","csrfToken":""}]`, ``)
	pairs := ParseCookiePairs(got)
	if len(pairs) != 1 || pairs[0].AuthToken != "real" || pairs[0].CSRFToken != "" {
		t.Fatalf("half row = %#v, want half row preserved", pairs)
	}
	// ClearSentinel 清空整串。
	if got := RestoreAdditionalCookies(ClearSentinel, `[{"id":"a","authToken":"t","csrfToken":"c"}]`); got != "" {
		t.Fatalf("clear sentinel = %q, want empty", got)
	}
}

// ResolveSubmission 不再因占位符/重复 id/半行返回 400。
func TestResolveSubmissionNeverBlocksSaving(t *testing.T) {
	stored := AppConfig{AuthToken: "stored-auth", AdditionalCookies: `[{"id":"a","authToken":"t","csrfToken":"c"}]`}
	cases := []AppConfig{
		{AdditionalCookies: `[{"id":"ghost","authToken":"********","csrfToken":"********"}]`},
		{AdditionalCookies: `[{"id":"a","authToken":"********","csrfToken":"********"},{"id":"a","authToken":"x","csrfToken":"y"}]`},
		{AdditionalCookies: `[{"id":"half","authToken":"only","csrfToken":""}]`},
		{AuthToken: SecretPlaceholder, CSRFToken: SecretPlaceholder},
		{},
	}
	for i, submitted := range cases {
		got, err := ResolveSubmission(submitted, stored)
		if err != nil {
			t.Fatalf("case %d returned error: %v", i, err)
		}
		if got.AuthToken != "stored-auth" {
			t.Fatalf("case %d AuthToken = %q, want stored", i, got.AuthToken)
		}
		for _, pair := range ParseCookiePairs(got.AdditionalCookies) {
			if pair.AuthToken == SecretPlaceholder || pair.CSRFToken == SecretPlaceholder {
				t.Fatalf("case %d leaked placeholder: %#v", i, pair)
			}
		}
	}
}

// env 接管的字段保存时一律保留 stored（含 ClearSentinel 提交）。
func TestResolveSubmissionKeepsStoredForEnvFields(t *testing.T) {
	t.Setenv(EnvAuthToken, "env-auth")
	t.Setenv(EnvCSRFToken, "env-ct0")
	t.Setenv(EnvProxyURL, "http://env-proxy:8080")
	t.Setenv(EnvAdditionalCookies, "auth_token=env; ct0=envcsrf")
	t.Setenv(EnvDownloadDir, "/tmp/env-download")

	stored := AppConfig{
		DownloadDir:       "/data/downloads",
		AuthToken:         "db-auth",
		CSRFToken:         "db-ct0",
		ProxyURL:          "http://db-proxy:3128",
		AdditionalCookies: `[{"id":"a","authToken":"db","csrfToken":"dbc"}]`,
	}
	got, err := ResolveSubmission(AppConfig{
		DownloadDir:       "/tmp/env-download",
		AuthToken:         ClearSentinel,
		CSRFToken:         ClearSentinel,
		ProxyURL:          "http://evil:1",
		AdditionalCookies: `[{"id":"a","authToken":"x","csrfToken":"y"}]`,
	}, stored)
	if err != nil {
		t.Fatal(err)
	}
	if got.DownloadDir != stored.DownloadDir || got.AuthToken != stored.AuthToken ||
		got.CSRFToken != stored.CSRFToken || got.ProxyURL != stored.ProxyURL ||
		got.AdditionalCookies != stored.AdditionalCookies {
		t.Fatalf("env-overridden fields not preserved: %#v", got)
	}
}

func TestEnvOverridesHelpers(t *testing.T) {
	t.Setenv(EnvDownloadDir, "/tmp/env-dl")
	t.Setenv(EnvAuthToken, "   ") // 纯空白视为未设置
	t.Setenv(EnvCSRFToken, "")
	t.Setenv(EnvProxyURL, "")
	t.Setenv(EnvAdditionalCookies, "")

	overrides := EnvOverrides()
	if !overrides["DownloadDir"] {
		t.Fatalf("DownloadDir not overridden: %#v", overrides)
	}
	if overrides["AuthToken"] {
		t.Fatalf("blank env must not count as override: %#v", overrides)
	}
	if !IsEnvOverridden("DownloadDir") || IsEnvOverridden("AuthToken") || IsEnvOverridden("NoSuchField") {
		t.Fatal("IsEnvOverridden returned wrong结果")
	}
	if got := EffectiveDownloadDir(AppConfig{DownloadDir: "/data/dl"}); got != "/tmp/env-dl" {
		t.Fatalf("EffectiveDownloadDir = %q, want env value", got)
	}

	t.Setenv(EnvAuthToken, "env-auth")
	fields := EnvOverriddenFields()
	if len(fields) != 2 || fields[0] != "authToken" || fields[1] != "downloadDir" {
		t.Fatalf("EnvOverriddenFields = %#v, want [authToken downloadDir]", fields)
	}
	redacted := AppConfig{AuthToken: "secret"}.Redacted()
	if len(redacted.EnvOverridden) != 2 || redacted.AuthToken != SecretPlaceholder {
		t.Fatalf("Redacted() = %#v, want envOverridden filled + token masked", redacted)
	}

	t.Setenv(EnvDownloadDir, "")
	if got := EffectiveDownloadDir(AppConfig{DownloadDir: "/data/dl"}); got != "/data/dl" {
		t.Fatalf("EffectiveDownloadDir without env = %q, want stored", got)
	}
}

// ValidateDownloadRoot 用哨兵错误包装"根/家目录"这一存量配置不可用的情形，
// 让 storage/main 能用 errors.Is 区分并降级修复，而不是笼统拒绝启动。
func TestValidateDownloadRootSentinel(t *testing.T) {
	if err := ValidateDownloadRoot("/"); !errors.Is(err, ErrInvalidDownloadRoot) {
		t.Fatalf("root dir error = %v, want ErrInvalidDownloadRoot", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if err := ValidateDownloadRoot(home); !errors.Is(err, ErrInvalidDownloadRoot) {
		t.Fatalf("home dir error = %v, want ErrInvalidDownloadRoot", err)
	}
	dir := t.TempDir()
	if err := ValidateDownloadRoot(dir); err != nil {
		t.Fatalf("temp dir rejected: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "nested")); statErr == nil {
		t.Fatal("unexpected nested dir")
	}
}
