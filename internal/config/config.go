package config

import (
	"encoding/json"
	"errors"
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
	EnvDownloadDir       = "OPEN_XDOWNLOAD_DOWNLOAD_DIR"
)

// envValue 返回已 trim 的环境变量值；纯空白视为未设置，避免 " " 被当成凭据。
func envValue(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// envFieldKeys 把配置字段名（Go 结构体字段名）映射到接管它的环境变量。
// httpapi / storage 据此判断某字段本次保存是否应保留库内值。
var envFieldKeys = map[string]string{
	"DownloadDir":       EnvDownloadDir,
	"AuthToken":         EnvAuthToken,
	"CSRFToken":         EnvCSRFToken,
	"ProxyURL":          EnvProxyURL,
	"AdditionalCookies": EnvAdditionalCookies,
}

// envFieldJSONNames 把结构体字段名映射到对外 JSON 字段名，切片顺序即 API 输出顺序。
var envFieldJSONNames = []struct {
	field string
	json  string
}{
	{"AuthToken", "authToken"},
	{"CSRFToken", "csrfToken"},
	{"ProxyURL", "proxyUrl"},
	{"AdditionalCookies", "additionalCookies"},
	{"DownloadDir", "downloadDir"},
}

// EnvOverrides 报告当前进程有哪些配置字段由环境变量提供。
// key 为结构体字段名（DownloadDir/AuthToken/CSRFToken/ProxyURL/AdditionalCookies）。
// 返回新 map，调用方可安全修改。
func EnvOverrides() map[string]bool {
	overrides := make(map[string]bool, len(envFieldKeys))
	for field, key := range envFieldKeys {
		if envValue(key) != "" {
			overrides[field] = true
		}
	}
	return overrides
}

// IsEnvOverridden 报告单个配置字段是否由环境变量提供；未知字段返回 false。
// field 用结构体字段名，例如 IsEnvOverridden("DownloadDir")。
func IsEnvOverridden(field string) bool {
	key, ok := envFieldKeys[field]
	if !ok {
		return false
	}
	return envValue(key) != ""
}

// EnvOverriddenFields 返回被环境变量接管的字段的 JSON 名称（camelCase），顺序固定。
// 供 API 标记"该字段由环境变量提供、保存时不会写入库"。
func EnvOverriddenFields() []string {
	fields := make([]string, 0, len(envFieldJSONNames))
	for _, item := range envFieldJSONNames {
		if IsEnvOverridden(item.field) {
			fields = append(fields, item.json)
		}
	}
	return fields
}

// EffectiveDownloadDir 返回运行时真正生效的下载目录：环境变量优先，否则用库内值。
// httpapi 用它构造本地目录允许根；storage 用它播种可信下载根。
func EffectiveDownloadDir(stored AppConfig) string {
	if value := envValue(EnvDownloadDir); value != "" {
		return value
	}
	return strings.TrimSpace(stored.DownloadDir)
}

// ApplyEnvOverrides 用环境变量覆盖敏感配置字段。环境变量为空（或纯空白）时保持原值。
func ApplyEnvOverrides(cfg AppConfig) AppConfig {
	if value := envValue(EnvDownloadDir); value != "" {
		cfg.DownloadDir = value
	}
	if value := envValue(EnvAuthToken); value != "" {
		cfg.AuthToken = value
	}
	if value := envValue(EnvCSRFToken); value != "" {
		cfg.CSRFToken = value
	}
	if value := envValue(EnvProxyURL); value != "" {
		cfg.ProxyURL = value
	}
	if value := envValue(EnvAdditionalCookies); value != "" {
		cfg.AdditionalCookies = value
	}
	return cfg.Normalized()
}

// ErrInvalidDownloadRoot 表示下载目录落在不允许的位置（文件系统根或家目录本身）。
// 调用方可用 errors.Is 区分"库内存量配置已不可用"与其它存储错误，从而降级修复
// （例如退回安全默认目录并告警）而不是直接拒绝启动。
var ErrInvalidDownloadRoot = errors.New("下载目录必须是文件系统根与家目录之外的目录")

// resolveForCompare 把路径规范成"可直接比较"的绝对路径：先 Abs，再尽量解析软链。
// 解析失败（路径尚不存在等）时保留 Abs 结果，保证调用方拿到确定的绝对路径。
func resolveForCompare(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}

// samePath 判断两个已规范化的路径是否指向同一位置。
// 除了字符串比较，还用 os.SameFile 兜底：软链/等价挂载点/大小写差异（Windows 与
// 不区分大小写的 macOS 卷）都能识别，避免"家目录换个写法就绕过校验"。
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if runtime.GOOS == "windows" && strings.EqualFold(a, b) {
		return true
	}
	aInfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}

// ValidateDownloadRoot 校验下载目录不能是文件系统根或家目录本身：
// 该目录同时是媒体库沙箱根，指向根目录会让任意文件都可被读取。
// 注意家目录自身也要解析软链：macOS 上 /tmp 是 /private/tmp 的软链，
// 只解析候选路径而不同时解析 HOME 会让"软链形式的家目录"绕过校验。
func ValidateDownloadRoot(root string) error {
	abs := resolveForCompare(root)
	home := ""
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		home = resolveForCompare(h)
	}
	if filepath.Dir(abs) == abs || samePath(abs, home) {
		return fmt.Errorf("%w（当前值：%s）", ErrInvalidDownloadRoot, abs)
	}
	return nil
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

// ClearSentinel is submitted by the frontend to explicitly clear a saved secret field.
// The storage layer recognizes this value and stores an empty string instead of
// preserving the old value (which is the default behavior for empty submissions).
const ClearSentinel = "__CLEAR__"

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
	DownloadDir             string `json:"downloadDir" db:"download_dir"`
	MaxConcurrency          int    `json:"maxConcurrency" db:"max_concurrency"`
	ProxyURL                string `json:"proxyUrl" db:"proxy_url"`
	AuthToken               string `json:"authToken,omitempty" db:"auth_token"`
	CSRFToken               string `json:"csrfToken,omitempty" db:"csrf_token"`
	AdditionalCookies       string `json:"additionalCookies,omitempty" db:"additional_cookies"`
	AutoRetryFailed         bool   `json:"autoRetryFailed" db:"auto_retry_failed"`
	AutoFollowProtected     bool   `json:"autoFollowProtected" db:"auto_follow_protected"`
	IncludeNestedTweetMedia bool   `json:"includeNestedTweetMedia" db:"include_nested_tweet_media"`
	// IncrementalArchive 开启后用户/列表/关注归档使用早停游标，从上次成功位置继续，
	// 节省 X API 配额；默认关闭，每次全量扫描时间线，已存在媒体自动跳过并补齐预览图。
	IncrementalArchive bool           `json:"incrementalArchive" db:"incremental_archive"`
	FileNamingMode     FileNamingMode `json:"fileNamingMode" db:"file_naming_mode"`
	MaxFilenameLength  int            `json:"maxFilenameLength" db:"max_filename_length"`
	StorageType        StorageType    `json:"storageType" db:"storage_type"`
	// EnvOverridden 只用于 API 展示：列出被环境变量接管、保存时会被忽略的字段
	// （JSON 字段名，camelCase）。由 Redacted() 填充，不落库。
	EnvOverridden []string `json:"envOverridden,omitempty" db:"-"`
}

type AuthCookie struct {
	ID        string `json:"id,omitempty"`
	AuthToken string `json:"authToken"`
	CSRFToken string `json:"csrfToken"`
}

func Default() AppConfig {
	return AppConfig{
		DownloadDir:       defaultDownloadDir(),
		MaxConcurrency:    1,
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
	cfg.MaxConcurrency = 1
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
	cfg.EnvOverridden = EnvOverriddenFields()
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
// 逐字段脱敏：有值的字段替换为占位符，空字段保持空串（不再把空 csrf 涂成 ********），
// 这样前端能区分"该行只有 auth_token"与"两个字段都已保存"。
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
		return ""
	}
	redacted := make([]AuthCookie, 0, len(cookies))
	for _, cookie := range cookies {
		redacted = append(redacted, AuthCookie{
			ID:        cookie.ID,
			AuthToken: redact(cookie.AuthToken),
			CSRFToken: redact(cookie.CSRFToken),
		})
	}
	payload, err := json.Marshal(redacted)
	if err != nil {
		return ""
	}
	return string(payload)
}

// RestoreAdditionalCookies 宽松合并提交的备用 Cookie：
//   - 按稳定 id 从 stored 还原占位符字段；还原不到（未知/缺失 id）的占位符一律置空，
//     绝不把 ******** 写进库；
//   - 整行 authToken 与 csrfToken 都为空时丢弃该行，其余情况允许"半行"存在
//     （storage 可存，xclient 使用时会忽略非完整凭据）；
//   - 重复 id 保留第一条；
//   - 提交值为 ClearSentinel 时清空；为整串占位符时返回 stored。
//
// 该函数不再返回错误：任何提交都不应因为 Cookie 结构不完整而阻塞整份配置保存。
func RestoreAdditionalCookies(submitted, stored string) string {
	submitted = strings.TrimSpace(submitted)
	if submitted == "" || submitted == ClearSentinel {
		return ""
	}
	if submitted == SecretPlaceholder {
		return stored
	}
	subPairs := ParseCookiePairs(submitted)
	if len(subPairs) == 0 {
		// 非结构化文本：按原样保存（xclient 会忽略无法解析的凭据）。
		return submitted
	}
	storedByID := make(map[string]AuthCookie, len(subPairs))
	for _, pair := range withCookieIDs(ParseCookiePairs(stored)) {
		if _, exists := storedByID[pair.ID]; !exists {
			storedByID[pair.ID] = pair
		}
	}
	merged := make([]AuthCookie, 0, len(subPairs))
	seen := make(map[string]bool, len(subPairs))
	for _, sub := range subPairs {
		if sub.ID != "" {
			if seen[sub.ID] {
				continue
			}
			seen[sub.ID] = true
		}
		auth, csrf := sub.AuthToken, sub.CSRFToken
		if old, ok := storedByID[sub.ID]; ok {
			if auth == SecretPlaceholder {
				auth = old.AuthToken
			}
			if csrf == SecretPlaceholder {
				csrf = old.CSRFToken
			}
		}
		if auth == SecretPlaceholder {
			auth = ""
		}
		if csrf == SecretPlaceholder {
			csrf = ""
		}
		if auth == "" && csrf == "" {
			continue
		}
		merged = append(merged, AuthCookie{ID: sub.ID, AuthToken: auth, CSRFToken: csrf})
	}
	if len(merged) == 0 {
		return ""
	}
	payload, err := json.Marshal(withCookieIDs(merged))
	if err != nil {
		return submitted
	}
	return string(payload)
}

// ResolveSubmission 在写库前合并凭据，语义为"尽量修复、不阻塞保存"：
//   - 环境变量接管的字段一律保留 stored（不把 env 回显值写进 SQLite）；
//   - AuthToken/CSRFToken：ClearSentinel 表示清除，空值/占位符表示沿用 stored；
//   - AdditionalCookies：见 RestoreAdditionalCookies（按 id 还原、半行允许、重复 id 取首条）；
//   - ProxyURL：沿用 RestoreURLUserinfo 的同源还原规则；
//   - DownloadDir：空提交沿用 stored（PATCH 语义），env 生效时同样保留 stored。
//
// 返回值恒为 nil error：保留 error 供将来真正不可恢复的错误使用，调用方的 400 映射可保留。
func ResolveSubmission(submitted, stored AppConfig) (AppConfig, error) {
	overrides := EnvOverrides()

	if overrides["DownloadDir"] || strings.TrimSpace(submitted.DownloadDir) == "" {
		submitted.DownloadDir = stored.DownloadDir
	}
	if overrides["AuthToken"] {
		submitted.AuthToken = stored.AuthToken
	} else {
		switch submitted.AuthToken {
		case ClearSentinel:
			submitted.AuthToken = ""
		case "", SecretPlaceholder:
			submitted.AuthToken = stored.AuthToken
		}
	}
	if overrides["CSRFToken"] {
		submitted.CSRFToken = stored.CSRFToken
	} else {
		switch submitted.CSRFToken {
		case ClearSentinel:
			submitted.CSRFToken = ""
		case "", SecretPlaceholder:
			submitted.CSRFToken = stored.CSRFToken
		}
	}
	if overrides["ProxyURL"] {
		submitted.ProxyURL = stored.ProxyURL
	} else {
		submitted.ProxyURL = RestoreURLUserinfo(submitted.ProxyURL, stored.ProxyURL)
	}
	if overrides["AdditionalCookies"] {
		submitted.AdditionalCookies = stored.AdditionalCookies
	} else {
		submitted.AdditionalCookies = RestoreAdditionalCookies(submitted.AdditionalCookies, stored.AdditionalCookies)
	}
	return submitted.Normalized(), nil
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
