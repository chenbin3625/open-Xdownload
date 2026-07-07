package config

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SecretPlaceholder is the sentinel substituted for secret values when
// redacting a config for display. Callers that round-trip a redacted config
// back through the API (mergeSecretPlaceholders / RestoreURLUserinfo) restore
// the real value when a submitted field still equals this placeholder.
const SecretPlaceholder = "********"

type FileNamingMode string
type StorageType string

const (
	FileNamingTweetText FileNamingMode = "tweet_text"
	FileNamingUserTweet FileNamingMode = "user_tweet"

	StorageLocal  StorageType = "local"
	StorageSMB    StorageType = "smb"
	StorageWebDAV StorageType = "webdav"

	DefaultMaxFilenameLength = 120
	MinFilenameLength        = 16
	MaxFilenameLength        = 240
	DefaultSMBPort           = 445
)

const (
	legacyFileNamingTweetID     FileNamingMode = "tweet_id"
	legacyFileNamingTweetIDText FileNamingMode = "tweet_id_text"
)

type AppConfig struct {
	DownloadDir             string         `json:"downloadDir" db:"download_dir"`
	MaxConcurrency          int            `json:"maxConcurrency" db:"max_concurrency"`
	ProxyURL                string         `json:"proxyUrl" db:"proxy_url"`
	AuthToken               string         `json:"authToken,omitempty" db:"auth_token"`
	CSRFToken               string         `json:"csrfToken,omitempty" db:"csrf_token"`
	AdditionalCookies       string         `json:"additionalCookies,omitempty" db:"additional_cookies"`
	AutoRetryFailed         bool           `json:"autoRetryFailed" db:"auto_retry_failed"`
	AutoFollowProtected     bool           `json:"autoFollowProtected" db:"auto_follow_protected"`
	IncludeNestedTweetMedia bool           `json:"includeNestedTweetMedia" db:"include_nested_tweet_media"`
	FileNamingMode          FileNamingMode `json:"fileNamingMode" db:"file_naming_mode"`
	MaxFilenameLength       int            `json:"maxFilenameLength" db:"max_filename_length"`
	StorageType             StorageType    `json:"storageType" db:"storage_type"`
	SMBHost                 string         `json:"smbHost" db:"smb_host"`
	SMBPort                 int            `json:"smbPort" db:"smb_port"`
	SMBShare                string         `json:"smbShare" db:"smb_share"`
	SMBPath                 string         `json:"smbPath" db:"smb_path"`
	SMBDomain               string         `json:"smbDomain" db:"smb_domain"`
	SMBUsername             string         `json:"smbUsername" db:"smb_username"`
	SMBPassword             string         `json:"smbPassword,omitempty" db:"smb_password"`
	WebDAVURL               string         `json:"webdavUrl" db:"webdav_url"`
	WebDAVPath              string         `json:"webdavPath" db:"webdav_path"`
	WebDAVUsername          string         `json:"webdavUsername" db:"webdav_username"`
	WebDAVPassword          string         `json:"webdavPassword,omitempty" db:"webdav_password"`
}

type AuthCookie struct {
	AuthToken string `json:"authToken"`
	CSRFToken string `json:"csrfToken"`
}

func Default() AppConfig {
	return AppConfig{
		DownloadDir:       defaultDownloadDir(),
		MaxConcurrency:    min(8, max(2, runtime.GOMAXPROCS(0))),
		AutoRetryFailed:   true,
		FileNamingMode:    FileNamingTweetText,
		MaxFilenameLength: DefaultMaxFilenameLength,
		StorageType:       StorageLocal,
		SMBPort:           DefaultSMBPort,
	}
}

func (cfg AppConfig) Normalized() AppConfig {
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = defaultDownloadDir()
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = Default().MaxConcurrency
	}
	if cfg.MaxConcurrency > 64 {
		cfg.MaxConcurrency = 64
	}
	switch cfg.FileNamingMode {
	case FileNamingTweetText, FileNamingUserTweet:
	case legacyFileNamingTweetID, legacyFileNamingTweetIDText:
		cfg.FileNamingMode = FileNamingUserTweet
	default:
		cfg.FileNamingMode = FileNamingTweetText
	}
	if cfg.MaxFilenameLength <= 0 {
		cfg.MaxFilenameLength = DefaultMaxFilenameLength
	}
	if cfg.MaxFilenameLength < MinFilenameLength {
		cfg.MaxFilenameLength = MinFilenameLength
	}
	if cfg.MaxFilenameLength > MaxFilenameLength {
		cfg.MaxFilenameLength = MaxFilenameLength
	}
	switch cfg.StorageType {
	case StorageLocal, StorageSMB, StorageWebDAV:
	default:
		cfg.StorageType = StorageLocal
	}
	if cfg.SMBPort <= 0 {
		cfg.SMBPort = DefaultSMBPort
	}
	cfg.SMBHost = strings.TrimSpace(cfg.SMBHost)
	cfg.SMBShare = strings.Trim(strings.TrimSpace(cfg.SMBShare), `/\`)
	cfg.SMBPath = cleanSlashPath(cfg.SMBPath)
	cfg.SMBDomain = strings.TrimSpace(cfg.SMBDomain)
	cfg.SMBUsername = strings.TrimSpace(cfg.SMBUsername)
	cfg.WebDAVURL = strings.TrimRight(strings.TrimSpace(cfg.WebDAVURL), "/")
	cfg.WebDAVPath = cleanSlashPath(cfg.WebDAVPath)
	cfg.WebDAVUsername = strings.TrimSpace(cfg.WebDAVUsername)
	return cfg
}

func (cfg AppConfig) Redacted() AppConfig {
	cfg.AuthToken = redact(cfg.AuthToken)
	cfg.CSRFToken = redact(cfg.CSRFToken)
	cfg.AdditionalCookies = redact(cfg.AdditionalCookies)
	cfg.SMBPassword = redact(cfg.SMBPassword)
	cfg.WebDAVPassword = redact(cfg.WebDAVPassword)
	cfg.ProxyURL = redactURLUserinfo(cfg.ProxyURL)
	cfg.WebDAVURL = redactURLUserinfo(cfg.WebDAVURL)
	return cfg
}

func defaultDownloadDir() string {
	if value := os.Getenv("OPEN_XDOWNLOAD_DOWNLOAD_DIR"); value != "" {
		return value
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "downloads"
	}
	return filepath.Join(cwd, "downloads")
}

func redact(value string) string {
	if value == "" {
		return ""
	}
	return SecretPlaceholder
}

// redactURLUserinfo strips embedded credentials from a URL for display,
// replacing the userinfo with SecretPlaceholder while leaving the host and
// path visible. URLs without userinfo (and unparseable values) are returned
// unchanged so non-credential URLs are not re-encoded. The placeholder is
// spliced in manually because url.String() percent-encodes "*" in userinfo,
// which obscures the host in the UI.
func redactURLUserinfo(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil || u.Scheme == "" {
		return rawURL
	}
	var b strings.Builder
	b.WriteString(u.Scheme)
	b.WriteString("://")
	b.WriteString(SecretPlaceholder)
	b.WriteString("@")
	b.WriteString(u.Host)
	if p := u.EscapedPath(); p != "" {
		b.WriteString(p)
	}
	if u.RawQuery != "" {
		b.WriteString("?")
		b.WriteString(u.RawQuery)
	}
	if u.Fragment != "" {
		b.WriteString("#")
		b.WriteString(u.EscapedFragment())
	}
	return b.String()
}

// SameURLAuthority reports whether two URLs point at the same scheme/host/port.
// It intentionally ignores path/query so a user can edit the WebDAV path on the
// same server without losing saved credentials.
func SameURLAuthority(a, b string) bool {
	au, err := url.Parse(a)
	if err != nil {
		return false
	}
	bu, err := url.Parse(b)
	if err != nil {
		return false
	}
	return au.Scheme != "" &&
		strings.EqualFold(au.Scheme, bu.Scheme) &&
		strings.EqualFold(au.Host, bu.Host)
}

// RestoreURLUserinfo reverses redactURLUserinfo when a redacted config is
// submitted back for saving. If redacted's userinfo is the placeholder and the
// URL still targets the same scheme/host/port, the stored URL's userinfo is
// substituted back in; otherwise redacted is returned unchanged (the user edited
// the URL, possibly with new credentials). A placeholder with no matching stored
// userinfo is dropped entirely so credentials are never replayed to a new host.
func RestoreURLUserinfo(redacted, stored string) string {
	if redacted == "" {
		return ""
	}
	ru, err := url.Parse(redacted)
	if err != nil || ru.User == nil {
		return redacted
	}
	if ru.User.Username() != SecretPlaceholder {
		return redacted
	}
	su, err := url.Parse(stored)
	if err != nil || su.User == nil || !SameURLAuthority(redacted, stored) {
		ru.User = nil
		return ru.String()
	}
	ru.User = su.User
	return ru.String()
}

func cleanSlashPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.Trim(value, "/")
	return value
}
