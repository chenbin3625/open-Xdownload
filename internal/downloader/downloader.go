package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

const (
	// defaultDownloadTimeout 是单次媒体请求（含读取 body）的总超时。清理残留临时文件的
	// 最小年龄与它绑定：小于 2 倍超时的阈值会误删仍在写入的 .part 文件。
	defaultDownloadTimeout = 10 * time.Minute
	// minStaleTempAge 是 RemoveStaleTempFiles 允许的最小清理阈值（2 倍下载超时）。
	minStaleTempAge = 2 * defaultDownloadTimeout
)

// ErrUnknownMediaType 表示响应既没有可识别的 Content-Type，URL 与文件名提示里也没有
// 允许的媒体扩展名。历史上这种情况会退化成 `.bin` 落盘，但 `.bin` 既不在媒体库扩展名
// 白名单内、也无法正确播放，因此改为直接失败，让上游记为可重试的失败而不是静默产出垃圾文件。
var ErrUnknownMediaType = errors.New("无法确定媒体类型")

type Result struct {
	Path        string
	Bytes       int64
	Skipped     bool
	ContentHash string
}

type Downloader struct {
	client *http.Client
	// allowedHost 判定"这个主机是否属于允许的媒体 CDN"。生产实例固定为
	// isAllowedTwimgHost；测试用 newTestDownloader 注入回环主机，避免单元测试依赖
	// 真实 twimg.com 域名。为 nil 时按 isAllowedTwimgHost 处理（零值 Downloader 安全）。
	allowedHost func(host string) bool
}

type HTTPStatusError struct {
	StatusCode int
	Payload    string
}

func (e *HTTPStatusError) Error() string {
	if e.Payload != "" {
		return fmt.Sprintf("download failed: HTTP %d %s", e.StatusCode, e.Payload)
	}
	return fmt.Sprintf("download failed: HTTP %d", e.StatusCode)
}

type Options struct {
	ModTime           time.Time
	LargePhoto        bool
	ProxyURL          string
	MaxFilenameLength int
	// ExistingFileOwner 判定 path 处的文件是否属于本次下载（同一推文同一媒体）。
	// 为 nil 时保持原有行为：已存在且完整则跳过，残缺文件则视为崩溃残留并覆盖。
	// 返回 false 表示该文件属于其他推文（命名冲突）：不得跳过也不得覆盖，应下载并
	// 发布到带编号后缀的路径，保证两个推文的媒体都保留。
	ExistingFileOwner func(path string) bool
	// ExistingContentHash 是调用方已知的、同一媒体已落盘文件的内容哈希
	// （jobs 可从 downloads.content_hash 取得）。跳过已存在文件时优先用它回填
	// Result.ContentHash，避免为了算哈希把大文件重新读一遍。
	// 零值表示未知：此时会读取已存在文件计算 sha256。
	ExistingContentHash string
}

func New() *Downloader {
	return NewWithTimeout(defaultDownloadTimeout)
}

// NewWithTimeout 返回单次媒体请求（含读取 body）总超时为 timeout 的下载器。
func NewWithTimeout(timeout time.Duration) *Downloader {
	return &Downloader{
		client:      httpx.Client("", timeout),
		allowedHost: isAllowedTwimgHost,
	}
}

// NewWithAllowedHostForTest 返回一个允许自定义媒体主机白名单的下载器，**仅供测试使用**。
//
// 生产代码禁止调用：New()/NewWithTimeout() 的白名单硬编码为 isAllowedTwimgHost，没有
// 任何配置/环境变量能关闭它；Open 对初始 URL 与每次重定向都使用同一判定，因此不存在
// "生产路径被绕过"的可能。此入口存在的原因：httptest 的回环地址（127.0.0.1）无法解析
// 成 twimg.com，跨包测试（如 internal/jobs）需要把 manager.downloader 换成允许回环
// 主机的实例。allow 为 nil 时退回严格白名单，避免误用成"允许一切"。
//
// 约定：只应在 _test.go 或测试辅助代码里使用，不要把它接进任何生产装配路径。
func NewWithAllowedHostForTest(allow func(host string) bool) *Downloader {
	downloader := New()
	if allow != nil {
		downloader.allowedHost = allow
	}
	return downloader
}

// hostAllowed 报告 host 是否在允许的媒体 CDN 白名单内。
func (d *Downloader) hostAllowed(host string) bool {
	if d != nil && d.allowedHost != nil {
		return d.allowedHost(host)
	}
	return isAllowedTwimgHost(host)
}

func (d *Downloader) Download(ctx context.Context, rawURL string, dir string, filenameHint string) (Result, error) {
	return d.DownloadWithOptions(ctx, rawURL, dir, filenameHint, Options{})
}

func (d *Downloader) DownloadWithOptions(ctx context.Context, rawURL string, dir string, filenameHint string, options Options) (Result, error) {
	// 目录收紧为 0700：与数据目录/SQLite 的加固一致，私有账号下载的媒体
	// 不应被其他本地用户读取或遍历。MkdirAll 仅在创建目录时设置该模式。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, err
	}
	maxFilenameLength := normalizedMaxFilenameLength(options.MaxFilenameLength)
	response, err := d.Open(ctx, rawURL, options)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()

	filename := Filename(rawURL, filenameHint, response.Header.Get("Content-Type"), maxFilenameLength)
	if filename == "" {
		// 不再退化成 .bin：扩展名未知时失败（见 ErrUnknownMediaType 注释）。
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownMediaType, strings.TrimSpace(rawURL))
	}
	basePath := filepath.Join(dir, filename)
	// 已存在且大小与 Content-Length 一致 → 视为完整下载，跳过；大小不一致说明是崩溃/中断
	// 留下的残缺文件，需要重新下载（覆盖）。Content-Length 未知时不凭文件存在判断完整性，
	// 重新下载并在发布前替换旧文件。
	existing, complete, replaceExisting, err := existingFileState(basePath, response.ContentLength)
	if err != nil {
		return Result{}, err
	}
	if complete {
		// 磁盘上已存在的完整文件可能属于其他推文（同名文本命名导致路径冲突）：此时既不能
		// 跳过也不能覆盖，需要照常下载并发布到带编号后缀的路径，保证两个推文的媒体都在。
		if options.ExistingFileOwner == nil || options.ExistingFileOwner(basePath) {
			existing.ContentHash, err = existingContentHash(existing.Path, options.ExistingContentHash)
			if err != nil {
				return Result{}, err
			}
			return existing, nil
		}
		replaceExisting = false
	} else if replaceExisting && options.ExistingFileOwner != nil && !options.ExistingFileOwner(basePath) {
		// 残缺文件同样可能属于其他推文（其下载尚未完成）：不覆盖它，改写到编号后缀路径。
		replaceExisting = false
	}
	// 原子写入：先写临时文件，fsync 后以不覆盖已有文件的方式发布到最终路径，避免
	// 崩溃留下被当作完整下载的残缺文件。临时文件名用短前缀 + 随机串，避免并发下载
	// 互相覆盖；长度不随最终文件名增长，保证最终文件名接近 NAME_MAX 时也不会触发
	// ENAMETOOLONG。
	tempFile, err := os.CreateTemp(dir, ".xdl-*.part")
	if err != nil {
		return Result{}, err
	}
	tempPath := tempFile.Name()
	// 临时文件收紧为 0600：发布走硬链接（os.Link），链接共享同一 inode，故临时文件的
	// 权限会原样传递到最终文件，确保私有媒体不被其他本地用户读取。
	if err := tempFile.Chmod(0o600); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return Result{}, err
	}
	hasher := sha256.New()
	bytes, copyErr := io.Copy(io.MultiWriter(tempFile, hasher), response.Body)
	syncErr := tempFile.Sync()
	closeErr := tempFile.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return Result{}, copyErr
	}
	if syncErr != nil {
		_ = os.Remove(tempPath)
		return Result{}, syncErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return Result{}, closeErr
	}
	contentHash := hex.EncodeToString(hasher.Sum(nil))
	result, skipped, err := publishTempFile(tempPath, basePath, bytes, response.ContentLength, maxFilenameLength, replaceExisting)
	if err != nil {
		_ = os.Remove(tempPath)
		return Result{}, err
	}
	if skipped {
		// 冲突分支在磁盘上发现了完整文件：返回它的哈希，而不是刚下载的临时文件哈希
		// （两者内容可能不同，例如同名但内容不同的媒体）。
		result.ContentHash, err = existingContentHash(result.Path, options.ExistingContentHash)
		if err != nil {
			return Result{}, err
		}
	} else {
		result.ContentHash = contentHash
	}
	if !options.ModTime.IsZero() {
		_ = os.Chtimes(result.Path, time.Now(), options.ModTime)
	}
	return result, nil
}

// existingContentHash 返回已存在完整文件的内容哈希：优先用调用方已知的哈希
// （Options.ExistingContentHash，jobs 可由 downloads.content_hash 提供），否则读取
// 文件计算。跳过路径不写空 ContentHash，避免 storage 侧 upsert 的 CASE 兜底成为唯一
// 保障（大文件会多一次读盘，但只在"文件已完整、本次不下载"时发生）。
func existingContentHash(path string, provided string) (string, error) {
	if hash := strings.TrimSpace(provided); hash != "" {
		return hash, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// existingFileState reports whether path already holds a complete download. If
// path existed before this download and its size mismatches contentLength, the
// caller may replace it as a stale partial file. Files that appear later during
// this download are treated as filename conflicts instead of partials.
func existingFileState(path string, contentLength int64) (Result, bool, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Result{}, false, false, nil
	}
	if err != nil {
		return Result{}, false, false, err
	}
	if info.IsDir() {
		return Result{}, false, false, nil
	}
	if contentLength < 0 {
		return Result{}, false, true, nil
	}
	if contentLength >= 0 && info.Size() != contentLength {
		return Result{}, false, true, nil
	}
	return Result{Path: path, Bytes: info.Size(), Skipped: true}, true, false, nil
}

// maxFilenameConflicts bounds the suffix search in publishTempFile: if this many
// numbered variants (basePath, basePath(1), basePath(2), ...) already exist, we
// give up rather than loop unbounded on a pathologically full directory.
const maxFilenameConflicts = 10000

func publishTempFile(tempPath string, basePath string, bytes int64, contentLength int64, maxFilenameLength int, replaceBase bool) (Result, bool, error) {
	triedReplaceBase := false
	for index := 0; index <= maxFilenameConflicts; index++ {
		candidate := suffixedPath(basePath, index, maxFilenameLength)
		handled, err := publishFile(tempPath, candidate, bytes)
		if err != nil {
			return Result{}, false, err
		}
		if handled {
			return Result{Path: candidate, Bytes: bytes}, false, nil
		}
		// 冲突分支：candidate 已存在（硬链接报 EEXIST，或退化路径下 Lstat 命中），
		// 与原有 os.IsExist 分支行为一致：必要时先处理崩溃残留覆盖，再尝试下一编号。
		if index == 0 && replaceBase && !triedReplaceBase {
			result, complete, removed, err := removeIncompleteLocalTarget(candidate, contentLength)
			if err != nil {
				return Result{}, false, err
			}
			if complete {
				_ = os.Remove(tempPath)
				return result, true, nil
			}
			triedReplaceBase = true
			if removed {
				index--
			}
		}
	}
	return Result{}, false, fmt.Errorf("too many filename conflicts publishing %s", basePath)
}

// linkFile 是 os.Link 的间接引用，便于测试注入不支持硬链接的错误。
var linkFile = os.Link

// publishFile 尝试把 tempPath 发布为 candidate：优先走硬链接（原子，且绝不覆盖
// 已有文件）。在不支持硬链接的文件系统（exFAT/FAT32、部分网络盘/FUSE 挂载）上
// os.Link 会返回 EPERM/EOPNOTSUPP 等错误，此时退化为先用 O_EXCL 原子占位，再
// os.Rename 替换占位文件，保住不覆盖已有文件的语义。
// 返回 handled=false 且 err=nil 表示 candidate 已存在（文件名冲突），调用方应继续
// 尝试下一个编号后缀；返回 err 非 nil 表示发布失败。
func publishFile(tempPath string, candidate string, bytes int64) (bool, error) {
	err := linkFile(tempPath, candidate)
	if err == nil {
		if err := os.Remove(tempPath); err != nil {
			return false, err
		}
		return true, nil
	}
	if os.IsExist(err) {
		return false, nil
	}
	if !hardLinksUnsupported(err) {
		return false, err
	}
	// 先创建独占占位文件。即使多个进程同时发布同一候选名，也只有一个能拿到占位，
	// 之后 Rename 只会替换我们自己创建的占位文件，而不会覆盖原有文件。
	reservation, reserveErr := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if reserveErr != nil {
		if os.IsExist(reserveErr) {
			return false, nil
		}
		return false, reserveErr
	}
	if err := reservation.Close(); err != nil {
		_ = os.Remove(candidate)
		return false, err
	}
	if err := os.Rename(tempPath, candidate); err != nil {
		_ = os.Remove(candidate)
		return false, err
	}
	return true, nil
}

// hardLinksUnsupported 报告 err 是否表示文件系统不支持硬链接。这些错误不是
// os.IsExist，原实现会直接失败导致下载全部中断。
func hardLinksUnsupported(err error) bool {
	return errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EXDEV)
}

func removeIncompleteLocalTarget(path string, contentLength int64) (Result, bool, bool, error) {
	result, complete, replace, err := existingFileState(path, contentLength)
	if err != nil || complete || !replace {
		return result, complete, false, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return Result{}, false, false, err
	}
	return Result{}, false, true, nil
}

func suffixedPath(path string, index int, maxFilenameLength int) string {
	if index == 0 {
		return path
	}
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	suffix := fmt.Sprintf("(%d)", index)
	return filepath.Join(dir, composeFilename(stem, ext, suffix, maxFilenameLength))
}

func (d *Downloader) Open(ctx context.Context, rawURL string, options Options) (*http.Response, error) {
	requestURL := rawURL
	if options.LargePhoto {
		requestURL = largePhotoURL(rawURL)
	}
	// 初始 URL 与重定向使用同一套主机白名单：解析出的 X 媒体 URL 已在 parser 侧过滤，
	// 但下载器是最后一道 SSRF 边界，任何调用方（任务、重试、测试工具）都不应能把
	// 请求发到非 twimg.com 主机。
	parsedURL, err := url.Parse(strings.TrimSpace(requestURL))
	if err != nil {
		return nil, fmt.Errorf("媒体 URL 无效: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("拒绝非 HTTP 媒体 URL: %s", parsedURL.String())
	}
	if !d.hostAllowed(parsedURL.Hostname()) {
		return nil, fmt.Errorf("拒绝非 twimg.com 媒体 URL: %s", parsedURL.Hostname())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	transport, err := transportForProxy(options.ProxyURL)
	if err != nil {
		return nil, err
	}
	// 空代理也必须使用 httpx.Transport，确保直连和代理两条路径都经过拨号防护。
	client := &http.Client{
		Timeout:   d.client.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("媒体重定向超过 10 次")
			}
			if len(via) == 0 {
				return nil
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("拒绝非 HTTP 媒体重定向: %s", req.URL.String())
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
				return errors.New("拒绝 HTTPS 媒体降级重定向")
			}
			if !d.hostAllowed(req.URL.Hostname()) {
				return fmt.Errorf("拒绝媒体重定向到非 twimg.com 域名: %s", req.URL.Hostname())
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, &HTTPStatusError{
			StatusCode: response.StatusCode,
			Payload:    strings.Join(strings.Fields(string(payload)), " "),
		}
	}
	return response, nil
}

// transportForProxy 返回按代理地址复用的 Transport（底层拨号带链路本地防护）。
// 空代理也返回受保护的直连 Transport，并尊重环境变量代理。
func transportForProxy(proxyURL string) (*http.Transport, error) {
	return httpx.Transport(proxyURL)
}

// isAllowedTwimgHost 报告 host 是否为 twimg.com 或其子域。
//
// 注意：本函数与 internal/parser.isTwimgHost、internal/httpapi.isAllowedPreviewHost、
// internal/jobs.isTwimgPreviewHost 是同一策略的 4 份实现（另见 downloader.MediaIdentity
// 内的等价判断）。本轮只统一 downloader 内部的两处调用点，未做跨包去重；统一时抽到
// internal/httpx 或独立的 twimg 白名单包，避免任一包先改导致漂移。
func isAllowedTwimgHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "twimg.com" || strings.HasSuffix(host, ".twimg.com")
}

// isPhotoSizingHost 报告该主机是否用 ?name= 表示图片尺寸。仅 pbs.twimg.com（图片与
// 视频海报）如此；video.twimg.com 的清晰度体现在各自独立的变体 URL 上。
func isPhotoSizingHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "pbs.twimg.com" || strings.HasSuffix(host, ".pbs.twimg.com")
}

var unsupportedFilenameChars = regexp.MustCompile(`[/\\:*?"<>\|]`)

// Filename 根据 Content-Type、URL（含 ?format=）与文件名提示推导落盘文件名。
// 扩展名未知时返回空字符串（不再退化为 .bin）：调用方应报 ErrUnknownMediaType，
// 而不是产出一个既不在媒体库白名单、也无法播放的 .bin 文件。
func Filename(rawURL string, hint string, contentType string, maxFilenameLength int) string {
	base := sanitizeFilename(hint)
	hintExt := filepath.Ext(base)
	ext := mediaExtension(rawURL, contentType)
	if ext == "" {
		// 提示名里的媒体扩展名兜底：海报/卡片 URL 常常没有扩展名，但 hint 来自原文件名。
		ext = normalizeMediaExtension(hintExt)
	}
	if ext == "" {
		return ""
	}
	// 按原始拼写去掉提示中的扩展名，避免 `.jpeg` / 大写扩展名（如 `.MP4`）
	// 与规范化后的扩展名不匹配导致双重扩展名（如 `photo.jpeg.jpg`）。
	if normalizeMediaExtension(hintExt) != "" {
		base = strings.TrimSuffix(base, hintExt)
	}
	if base == "" {
		base = "media"
	}
	return composeFilename(base, ext, "", maxFilenameLength)
}

func FilenameWithSuffix(filename string, suffix string, maxFilenameLength int) string {
	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)
	return composeFilename(stem, ext, suffix, maxFilenameLength)
}

func mediaExtension(rawURL string, contentType string) string {
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		if ext := extensionFromContentType(mediaType); ext != "" {
			return ext
		}
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		if format := parsed.Query().Get("format"); format != "" {
			if ext := normalizeMediaExtension(format); ext != "" {
				return ext
			}
		}
		if ext := normalizeMediaExtension(filepath.Ext(parsed.Path)); ext != "" {
			return ext
		}
	}
	return ""
}

func extensionFromContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/avif":
		return ".avif"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	}
	return ""
}

func normalizeMediaExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" || ext == "." {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	switch ext {
	case ".jpeg":
		return ".jpg"
	case ".jpg", ".png", ".gif", ".webp", ".avif", ".mp4", ".mov", ".m4v", ".webm":
		return ext
	}
	return ""
}

func largePhotoURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || !isPhotoSizingHost(parsed.Hostname()) {
		return rawURL
	}
	query := parsed.Query()
	// 无条件改写 name 尺寸参数，而不是仅在缺省时补上：X 在卡片图片、syndication photos
	// 与 component image_url 里常常给出 ?name=small / ?name=large 这类缩略图写法，指向的
	// 是同一张图片。沿用请求里的小尺寸会归档一张缩略图，且与同一张图片的其他写法（无 name
	// 参数，按 4096x4096 取回）字节不同，内容哈希去重也无法合并，于是同一张照片落盘两次。
	// 统一取最大尺寸后，同一张照片的各种写法都取回同一份字节。
	// name=orig 是原始上传件，本身已是最佳，改成 4096x4096 反而会降级，故保留。
	if !strings.EqualFold(query.Get("name"), "orig") {
		query.Set("name", "4096x4096")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// MediaIdentity 返回媒体内容的稳定身份键。同一份媒体即使 URL 写法不同（?tag= 这类
// 易变参数、`.jpg` 与 `?format=jpg` 这类等价扩展名写法、主机大小写）也映射到同一个键，
// 供跨推文去重使用：转推、引用推文与卡片媒体经常复用同一条媒体 URL，仅按
// (tweet_id, media_url) 判重会漏判，导致同一份媒体被重复下载多份。
//
// 保留 format：不同编码（JPG 与 PNG）视为不同媒体。但 pbs.twimg.com 上的 name 参数
// 只是尺寸指令（small/medium/large/orig/4096x4096 都是同一张图片的不同分辨率），故不
// 参与身份：保留它会让同一张照片的不同写法拿到不同身份键而各自下载一份，且两份字节
// 不同（缩略图对原图），内容哈希去重也合并不了。下载侧由 largePhotoURL 统一按最大
// 尺寸取回，保证收敛到同一份字节。
//
// video.twimg.com 上带媒体 ID 的视频（ext_tw_video/<id>/、amplify_video/<id>/）只按
// 媒体 ID 识别，见 videoMediaID。
//
// 非 twimg.com URL 原样返回：没有已知的等价改写规则，只按完全相同的 URL 去重。
// 幂等；返回空字符串表示该 URL 不能用于去重。
func MediaIdentity(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !isAllowedTwimgHost(parsed.Hostname()) {
		return trimmed
	}
	if id := videoMediaID(parsed); id != "" {
		return "https://video.twimg.com/" + id
	}
	query := parsed.Query()
	// 将路径扩展名和 format 参数统一表示，避免将 JPG 与 PNG 当成同一份文件。
	mediaFormat := query.Get("format")
	if mediaFormat == "" {
		mediaFormat = filepath.Ext(parsed.Path)
	}
	if ext := normalizeMediaExtension(mediaFormat); ext != "" {
		query.Set("format", strings.TrimPrefix(ext, "."))
	}
	// tag 随 Twitter 重新编码变化，不代表不同媒体。
	query.Del("tag")
	// name 是 pbs.twimg.com 的尺寸指令，同一张图片的不同分辨率写法必须收敛到同一身份。
	// 只对图片主机生效：video.twimg.com 不用 name 表示尺寸（清晰度体现在各自独立的
	// 变体 URL 上），不应在那里改变去重语义。
	if isPhotoSizingHost(parsed.Hostname()) {
		query.Del("name")
	}
	parsed.Scheme = "https"
	parsed.Host = strings.ToLower(parsed.Hostname())
	parsed.User = nil
	parsed.Path = stripMediaExtension(parsed.Path)
	parsed.RawPath = ""
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String()
}

// videoMediaID 返回 video.twimg.com 视频 URL 中的媒体 ID 段（如 "ext_tw_video/123"），
// 不是此类 URL 时返回空字符串。
//
// 同一个视频的各个 mp4 变体只在清晰度、编码目录与文件名上不同，媒体 ID 相同；转推、
// 引用推文与复用媒体发出的新推文也指向同一个媒体 ID。把变体 URL 当身份会让"挑中的
// 最佳变体不同"（syndication 与 GraphQL 数据不一致、X 调整路径布局）时查不到已有副本，
// 而不同清晰度的字节也不同，内容哈希去重同样合并不了，同一个视频就会落盘多份。
// tweet_video/（GIF）的路径里没有媒体 ID，不在此列。只认 mp4：同一媒体 ID 下的 HLS
// 播放列表（.m3u8）不可下载，与 mp4 共用身份会让推文内合并时丢掉可下载的 mp4。
func videoMediaID(parsed *url.URL) string {
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "video.twimg.com" && !strings.HasSuffix(host, ".video.twimg.com") {
		return ""
	}
	if !strings.EqualFold(filepath.Ext(parsed.Path), ".mp4") {
		return ""
	}
	// 至少要有 <类型>/<媒体ID>/<文件>：只有两段的路径不是变体 URL，无法确认 ID 的含义。
	segments := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(segments) < 3 || (segments[0] != "ext_tw_video" && segments[0] != "amplify_video") {
		return ""
	}
	if !isDecimalID(segments[1]) {
		return ""
	}
	return segments[0] + "/" + segments[1]
}

func isDecimalID(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// stripMediaExtension 去掉路径末尾的媒体扩展名（.jpg/.mp4 等），使 `.jpg` 与
// `?format=jpg` 两种写法收敛到同一个身份；非媒体扩展名与空路径原样保留。
func stripMediaExtension(pathValue string) string {
	rawExt := filepath.Ext(pathValue)
	if normalizeMediaExtension(rawExt) == "" {
		return pathValue
	}
	trimmed := pathValue[:len(pathValue)-len(rawExt)]
	if trimmed == "" || strings.HasSuffix(trimmed, "/") {
		return pathValue
	}
	return trimmed
}

// NormalizeMediaURL 去掉 Twitter 视频 URL 中易变且不影响内容的查询参数（?tag=N，
// Twitter 重新编码后该值会变），使同一媒体在多次解析中获得稳定的去重键。
// 仅对 video.twimg.com / twimg.com 生效；其他域名的 URL 原样返回。
// 保留 format/name 等决定图片格式或尺寸的参数。幂等。
func NormalizeMediaURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.RawQuery == "" {
		return rawURL
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || !isAllowedTwimgHost(parsed.Hostname()) {
		return rawURL
	}
	query := parsed.Query()
	if !query.Has("tag") {
		return rawURL
	}
	query.Del("tag")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// sanitizeFilename 把任意提示文本清理成可跨平台安全落盘的文件名基名。
// 处理：控制字符（含 NUL/换行）、路径分隔符与 Windows 非法字符、隐藏文件前导点、
// Windows 保留设备名（CON/PRN/AUX/NUL/COM1-9/LPT1-9/CONIN$/CONOUT$）、结尾点与空格
// （Windows 会静默截断，导致同名冲突）。清理后为空时返回 ""，由调用方退回 "media"。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = unsupportedFilenameChars.ReplaceAllString(name, "")
	// 先删控制字符再处理空白：\r\n\t 之外还有大量 C0/DEL 控制符会破坏日志与终端，
	// 也会被 Windows 拒绝。
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)

	// Strip leading dots to prevent hidden files on Unix.
	name = strings.TrimLeft(name, ".")
	// Windows 不允许文件名以点或空格结尾（. 与空格会被静默丢弃），这里显式去掉。
	name = strings.TrimRight(name, ". ")
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	// Detect Windows reserved device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9).
	// If the base name (without extension) matches, prefix with underscore.
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if isWindowsReservedName(base) || isWindowsReservedName(name) {
		name = "_" + name
	}

	return name
}

// isWindowsReservedName reports whether name (case-insensitive) is a Windows
// reserved device name: CON, PRN, AUX, NUL, CONIN$, CONOUT$, COM1-COM9, LPT1-LPT9.
func isWindowsReservedName(name string) bool {
	if len(name) == 0 {
		return false
	}
	upper := strings.ToUpper(name)
	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) {
		digit := upper[3]
		if digit >= '1' && digit <= '9' {
			return true
		}
	}
	return false
}

// normalizedMaxFilenameLength 复用 internal/config 的边界常量，避免本包再维护一套
// 16/120/240 的硬编码（历史问题：config 与 downloader 各一份，改一处即漂移）。
func normalizedMaxFilenameLength(value int) int {
	if value <= 0 {
		return config.DefaultMaxFilenameLength
	}
	if value < config.MinFilenameLength {
		return config.MinFilenameLength
	}
	if value > config.MaxFilenameLength {
		return config.MaxFilenameLength
	}
	return value
}

func composeFilename(stem string, ext string, suffix string, maxFilenameLength int) string {
	maxFilenameLength = normalizedMaxFilenameLength(maxFilenameLength)
	stem = strings.TrimSpace(stem)
	if stem == "" {
		stem = "media"
	}
	if len(ext)+len(suffix) >= maxFilenameLength {
		ext = ""
	}
	if len(suffix) >= maxFilenameLength {
		suffix = truncateUTF8Bytes(suffix, maxFilenameLength-1)
	}
	available := maxFilenameLength - len(ext) - len(suffix)
	if available < 1 {
		available = 1
	}
	stem = truncateUTF8Bytes(stem, available)
	if stem == "" {
		stem = truncateUTF8Bytes("media", available)
	}
	return stem + suffix + ext
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	var builder strings.Builder
	for _, ch := range value {
		size := utf8.RuneLen(ch)
		if size < 0 {
			size = len(string(ch))
		}
		if builder.Len()+size > maxBytes {
			break
		}
		builder.WriteRune(ch)
	}
	return strings.TrimSpace(builder.String())
}

// staleTempFilePatterns 匹配下载器与海报回填写出的临时文件（见 DownloadWithOptions 与
// jobs/preview.go）。它们正常情况下会被重命名或删除，只有进程在写入中途被杀才会残留。
var staleTempFilePatterns = []string{".xdl-*.part", ".preview-*.jpg"}

// RemoveStaleTempFiles 删除 root 下修改时间早于 olderThan 的残留临时文件，返回删除数量。
//
// olderThan 会被抬到至少 minStaleTempAge（2 倍默认下载超时）：小于它就可能删掉仍在
// 写入的临时文件（例如超大视频下载超过调用方给的 1 小时阈值）。调用方（jobs 维护任务）
// 的 1h 阈值不受影响，因为 1h > 20min。
//
// 删除前还会对候选文件再 stat 一次并与 WalkDir 快照比对：只要期间发生了写入
// （mtime/size 变化）或 inode 被替换，就跳过——避免误删正在下载的 .part。
func RemoveStaleTempFiles(ctx context.Context, root string, olderThan time.Duration) (int, error) {
	if olderThan < minStaleTempAge {
		olderThan = minStaleTempAge
	}
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			// 单个目录不可读不影响其余目录的清理。
			if entry != nil && entry.IsDir() && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !isStaleTempFileName(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		if !staleTempFileReady(path, info, cutoff) {
			return nil
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
		return nil
	})
	if os.IsNotExist(err) {
		return removed, nil
	}
	return removed, err
}

// staleTempFileReady 判断快照 info 对应的临时文件此刻是否仍可安全删除。
// snapshot 来自 WalkDir（可能已过时），这里重新 Lstat 并比对同一 inode、大小与
// mtime：任何差异都说明文件正在被写入或已被替换，应保守跳过。同时用最新 mtime
// 复核一次 cutoff（遍历大目录可能持续很久）。
func staleTempFileReady(path string, snapshot os.FileInfo, cutoff time.Time) bool {
	fresh, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if !fresh.Mode().IsRegular() || !os.SameFile(snapshot, fresh) {
		return false
	}
	if fresh.Size() != snapshot.Size() || !fresh.ModTime().Equal(snapshot.ModTime()) {
		return false
	}
	return fresh.ModTime().Before(cutoff)
}

func isStaleTempFileName(name string) bool {
	for _, pattern := range staleTempFilePatterns {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
	}
	return false
}
