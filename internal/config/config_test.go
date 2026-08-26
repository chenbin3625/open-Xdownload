package config

import (
	"encoding/json"
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
	t.Setenv(EnvSMBPassword, "smb-secret")
	t.Setenv(EnvWebDAVPassword, "webdav-secret")
	t.Setenv(EnvAdditionalCookies, "auth_token=a; ct0=b")

	cfg := ApplyEnvOverrides(AppConfig{
		AuthToken: "db-auth",
		SMBHost:   "nas.local",
	})
	if cfg.AuthToken != "env-auth" || cfg.CSRFToken != "env-ct0" {
		t.Fatalf("env overrides not applied: %#v", cfg)
	}
	if cfg.ProxyURL != "http://env-proxy:8080" {
		t.Fatalf("proxy = %q", cfg.ProxyURL)
	}
	if cfg.SMBPassword != "smb-secret" || cfg.WebDAVPassword != "webdav-secret" {
		t.Fatalf("storage secrets = %#v", cfg.SMBPassword)
	}
	if cfg.AdditionalCookies != "auth_token=a; ct0=b" {
		t.Fatalf("additional cookies = %q", cfg.AdditionalCookies)
	}
	// 未设置的环境变量不得清空既有值。
	if cfg.SMBHost != "nas.local" {
		t.Fatalf("SMBHost lost: %q", cfg.SMBHost)
	}
}

func TestMergeStorageSecretSemantics(t *testing.T) {
	if got := MergeStorageSecret("", "stored", true); got != "stored" {
		t.Fatalf("empty+unchanged = %q, want stored", got)
	}
	if got := MergeStorageSecret(SecretPlaceholder, "stored", true); got != "stored" {
		t.Fatalf("placeholder+unchanged = %q, want stored", got)
	}
	if got := MergeStorageSecret("", "stored", false); got != "" {
		t.Fatalf("empty+changed = %q, want empty (no credential replay)", got)
	}
	if got := MergeStorageSecret("newpass", "stored", false); got != "newpass" {
		t.Fatalf("newpass = %q", got)
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
