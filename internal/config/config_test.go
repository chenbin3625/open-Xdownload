package config

import "testing"

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
	wantRedacted := "auth_token=********; ct0=********\nauth_token=********; ct0=********"
	if redacted != wantRedacted {
		t.Fatalf("RedactAdditionalCookies() = %q, want %q", redacted, wantRedacted)
	}

	// Case 1: Unmodified redacted cookies submitted -> restored identically
	restored := RestoreAdditionalCookies(redacted, raw)
	if restored != raw {
		t.Fatalf("RestoreAdditionalCookies(unmodified) = %q, want %q", restored, raw)
	}

	// Case 2: User added a 3rd cookie
	added := redacted + "\nauth_token=tok3; ct0=csrf3"
	restoredAdded := RestoreAdditionalCookies(added, raw)
	wantAdded := raw + "\nauth_token=tok3; ct0=csrf3"
	if restoredAdded != wantAdded {
		t.Fatalf("RestoreAdditionalCookies(added) = %q, want %q", restoredAdded, wantAdded)
	}

	// Case 3: User edited the 1st cookie and kept 2nd redacted
	edited := "auth_token=newtok; ct0=newcsrf\nauth_token=********; ct0=********"
	restoredEdited := RestoreAdditionalCookies(edited, raw)
	wantEdited := "auth_token=newtok; ct0=newcsrf\nauth_token=tok2; ct0=csrf2"
	if restoredEdited != wantEdited {
		t.Fatalf("RestoreAdditionalCookies(edited) = %q, want %q", restoredEdited, wantEdited)
	}

	// Case 4: User cleared cookies
	if got := RestoreAdditionalCookies("", raw); got != "" {
		t.Fatalf("RestoreAdditionalCookies(empty) = %q, want empty", got)
	}
}
