package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 环境变量覆盖项（S3）：允许部署方用环境变量注入密钥，避免凭据必须明文存库 / 经 HTTP
// 回传。仅在读取配置（GetConfig / 使用点）覆盖显示与使用值；UpdateConfig 的合并基准依然
// 是数据库中已存值，因此 UI 保存不会把环境变量持久化进 SQLite。
const (
	EnvAuthToken         = "OPEN_XDOWNLOAD_AUTH_TOKEN"
	EnvCSRFToken         = "OPEN_XDOWNLOAD_CT0"
	EnvProxyURL          = "OPEN_XDOWNLOAD_PROXY_URL"
	EnvAdditionalCookies = "OPEN_XDOWNLOAD_ADDITIONAL_COOKIES"
)

// ApplyEnvOverrides 用环境变量覆盖敏感配置字段。环境变量为空时保持原值。
func ApplyEnvOverrides(cfg AppConfig) AppConfig {
	if value := os.Getenv(EnvAuthToken); value != "" {
		cfg.AuthToken = strings.TrimSpace(value)
	}
	if value := os.Getenv(EnvCSRFToken); value != "" {
		cfg.CSRFToken = strings.TrimSpace(value)
	}
	if value := os.Getenv(EnvProxyURL); value != "" {
		cfg.ProxyURL = strings.TrimSpace(value)
	}
	if value := os.Getenv(EnvAdditionalCookies); value != "" {
		cfg.AdditionalCookies = value
	}
	return cfg.Normalized()
}

// RevertEnvOnlyEcho 阻止 UI 把仅由环境变量注入、未落库的值原样 POST 回写进 SQLite。
// 提交值与 env 完全一致且库内另有存值（含空串）时，保留库内值。
func RevertEnvOnlyEcho(submitted, stored, envKey string) string {
	envValue := strings.TrimSpace(os.Getenv(envKey))
	if envValue == "" || submitted != envValue {
		return submitted
	}
	return stored
}

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

	StorageLocal StorageType = "local"

	DefaultMaxFilenameLength = 120
	MinFilenameLength        = 16
	MaxFilenameLength        = 240
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
	// IncrementalArchive 开启后用户/列表/关注归档使用早停游标，从上次成功位置继续，
	// 节省 X API 配额；默认关闭，每次全量扫描时间线，已存在媒体自动跳过并补齐预览图。
	IncrementalArchive bool           `json:"incrementalArchive" db:"incremental_archive"`
	FileNamingMode     FileNamingMode `json:"fileNamingMode" db:"file_naming_mode"`
	MaxFilenameLength  int            `json:"maxFilenameLength" db:"max_filename_length"`
	StorageType        StorageType    `json:"storageType" db:"storage_type"`
}

type AuthCookie struct {
	ID        string `json:"id,omitempty"`
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
	case StorageLocal:
	default:
		// SMB/WebDAV 存储已移除：历史库里遗留的值一律回落到本地存储。
		cfg.StorageType = StorageLocal
	}
	return cfg
}

func (cfg AppConfig) Redacted() AppConfig {
	cfg.AuthToken = redact(cfg.AuthToken)
	cfg.CSRFToken = redact(cfg.CSRFToken)
	cfg.AdditionalCookies = RedactAdditionalCookies(cfg.AdditionalCookies)
	cfg.ProxyURL = redactURLUserinfo(cfg.ProxyURL)
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

// RedactAdditionalCookies returns a JSON redacted version of additional cookies,
// preserving a stable id for each configured pair so row deletion cannot shift credentials.
func RedactAdditionalCookies(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if raw == SecretPlaceholder {
		return SecretPlaceholder
	}
	cookies := withCookieIDs(ParseCookiePairs(raw))
	if len(cookies) == 0 {
		return SecretPlaceholder
	}
	redacted := make([]AuthCookie, 0, len(cookies))
	for _, cookie := range cookies {
		redacted = append(redacted, AuthCookie{ID: cookie.ID, AuthToken: SecretPlaceholder, CSRFToken: SecretPlaceholder})
	}
	payload, err := json.Marshal(redacted)
	if err != nil {
		return SecretPlaceholder
	}
	return string(payload)
}

// RestoreAdditionalCookies merges submitted additional cookies with previously stored ones,
// restoring any SecretPlaceholder values from the corresponding stored cookie pair.
func RestoreAdditionalCookies(submitted, stored string) string {
	submitted = strings.TrimSpace(submitted)
	if submitted == "" {
		return ""
	}
	if submitted == SecretPlaceholder {
		return stored
	}
	subPairs := withCookieIDs(ParseCookiePairs(submitted))
	if len(subPairs) == 0 {
		return submitted
	}
	storedPairs := withCookieIDs(ParseCookiePairs(stored))
	storedByID := make(map[string]AuthCookie, len(storedPairs))
	for _, pair := range storedPairs {
		storedByID[pair.ID] = pair
	}
	merged := make([]AuthCookie, 0, len(subPairs))
	for i, sub := range subPairs {
		auth := sub.AuthToken
		csrf := sub.CSRFToken
		storedPair, hasStored := storedByID[sub.ID]
		if !hasStored && i < len(storedPairs) && sub.ID == fmt.Sprintf("cookie-%d", i) {
			storedPair = storedPairs[i]
			hasStored = true
		}
		if hasStored {
			if auth == SecretPlaceholder || auth == "" {
				auth = storedPair.AuthToken
			}
			if csrf == SecretPlaceholder || csrf == "" {
				csrf = storedPair.CSRFToken
			}
		}
		if auth != "" || csrf != "" {
			merged = append(merged, AuthCookie{ID: sub.ID, AuthToken: auth, CSRFToken: csrf})
		}
	}
	payload, err := json.Marshal(merged)
	if err != nil {
		return submitted
	}
	return string(payload)
}

// ParseCookiePairs parses cookie pairs from JSON or text representations.
func ParseCookiePairs(raw string) []AuthCookie {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == SecretPlaceholder {
		return nil
	}
	type cookieJSON struct {
		ID         string `json:"id"`
		AuthToken  string `json:"authToken"`
		CSRFToken  string `json:"csrfToken"`
		AuthToken2 string `json:"auth_token"`
		CT0        string `json:"ct0"`
	}
	var decoded []cookieJSON
	if json.Unmarshal([]byte(raw), &decoded) == nil {
		pairs := make([]AuthCookie, 0, len(decoded))
		for _, item := range decoded {
			auth := item.AuthToken
			if auth == "" {
				auth = item.AuthToken2
			}
			csrf := item.CSRFToken
			if csrf == "" {
				csrf = item.CT0
			}
			if auth != "" || csrf != "" {
				pairs = append(pairs, AuthCookie{ID: strings.TrimSpace(item.ID), AuthToken: strings.TrimSpace(auth), CSRFToken: strings.TrimSpace(csrf)})
			}
		}
		if len(pairs) > 0 {
			return pairs
		}
	}
	blocks := strings.Split(raw, "\n")
	pairs := make([]AuthCookie, 0, len(blocks))
	current := AuthCookie{}
	flush := func() {
		if current.AuthToken != "" || current.CSRFToken != "" {
			pairs = append(pairs, current)
		}
		current = AuthCookie{}
	}
	for _, line := range blocks {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line == "" {
			flush()
			continue
		}
		// 形如 "auth_token: xxx" 的 YAML/冒号行：整行按 key: value 解析；行内含 ;/, 时
		// 落入下面的 token 分解，避免把 "auth_token=a; ct0=b" 里的 ":" 误当分隔符。
		if strings.Contains(line, ":") && !strings.ContainsAny(line, ";,") {
			if setAuthCookieValue(&current, line) {
				if current.AuthToken != "" && current.CSRFToken != "" {
					flush()
				}
				continue
			}
		}
		for _, token := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ';' || r == ',' || r == ' '
		}) {
			setAuthCookieValue(&current, token)
		}
		if current.AuthToken != "" && current.CSRFToken != "" {
			flush()
		}
	}
	flush()
	return pairs
}

func withCookieIDs(pairs []AuthCookie) []AuthCookie {
	for index := range pairs {
		if strings.TrimSpace(pairs[index].ID) == "" {
			pairs[index].ID = fmt.Sprintf("cookie-%d", index)
		}
	}
	return pairs
}

func setAuthCookieValue(current *AuthCookie, raw string) bool {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		key, value, ok = strings.Cut(raw, ":")
	}
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if value == "" {
		return false
	}
	switch key {
	case "auth_token", "authToken":
		current.AuthToken = value
	case "ct0", "csrfToken":
		current.CSRFToken = value
	default:
		return false
	}
	return true
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
// It intentionally ignores path/query so a user can edit the URL path on the
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
