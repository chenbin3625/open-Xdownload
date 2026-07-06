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
