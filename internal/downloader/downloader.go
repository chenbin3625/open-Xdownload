package downloader

import (
	"context"
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
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type Result struct {
	Path    string
	Bytes   int64
	Skipped bool
}

type Downloader struct {
	client *http.Client
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
}

func New() *Downloader {
	return &Downloader{
		client: &http.Client{Timeout: 10 * time.Minute},
	}
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

	basePath := filepath.Join(dir, Filename(rawURL, filenameHint, response.Header.Get("Content-Type"), maxFilenameLength))
	// 已存在且大小与 Content-Length 一致 → 视为完整下载，跳过；大小不一致说明是崩溃/中断
	// 留下的残缺文件，需要重新下载（覆盖）。Content-Length 未知时保守按已存在跳过。
	existing, complete, replaceExisting, err := existingFileState(basePath, response.ContentLength)
	if err != nil {
		return Result{}, err
	}
	if complete {
		// 磁盘上已存在的完整文件可能属于其他推文（同名文本命名导致路径冲突）：此时既不能
		// 跳过也不能覆盖，需要照常下载并发布到带编号后缀的路径，保证两个推文的媒体都在。
		if options.ExistingFileOwner == nil || options.ExistingFileOwner(basePath) {
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
	bytes, copyErr := io.Copy(tempFile, response.Body)
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
	result, skipped, err := publishTempFile(tempPath, basePath, bytes, response.ContentLength, maxFilenameLength, replaceExisting)
	if err != nil {
		_ = os.Remove(tempPath)
		return Result{}, err
	}
	if !options.ModTime.IsZero() {
		_ = os.Chtimes(result.Path, time.Now(), options.ModTime)
	}
	if skipped {
		return result, nil
	}
	return result, nil
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
// os.Link 会返回 EPERM/EOPNOTSUPP 等错误，此时退化为"先 Lstat 确认目标不存在、
// 再 os.Rename"的发布方式，保住不覆盖已有文件的语义。
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
	// 不支持硬链接：os.Rename 会覆盖已存在的目标，发布前必须确认目标不存在。
	if _, statErr := os.Lstat(candidate); statErr == nil {
		return false, nil
	} else if !os.IsNotExist(statErr) {
		return false, statErr
	}
	if err := os.Rename(tempPath, candidate); err != nil {
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	client := d.client
	transport, err := transportForProxy(options.ProxyURL)
	if err != nil {
		return nil, err
	}
	if transport != nil {
		// 复用带连接池的 Transport，避免每个请求都重新做 TCP/TLS 握手。
		client = &http.Client{Timeout: d.client.Timeout, Transport: transport}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		defer response.Body.Close()
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, &HTTPStatusError{
			StatusCode: response.StatusCode,
			Payload:    strings.Join(strings.Fields(string(payload)), " "),
		}
	}
	return response, nil
}

// proxyTransports 缓存按代理地址复用的 Transport，使代理下载也能复用空闲连接。
// 有界：反复变更代理配置时，超出 maxProxyTransports 的旧 Transport 会被驱逐并
// 关闭其空闲连接（不影响在途请求），避免连接随配置变更无界累积。
const maxProxyTransports = 8

var (
	proxyTransportsMu sync.Mutex
	proxyTransports   = map[string]*http.Transport{}
)

func transportForProxy(proxyURL string) (*http.Transport, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	key := parsed.String()
	proxyTransportsMu.Lock()
	defer proxyTransportsMu.Unlock()
	if t, ok := proxyTransports[key]; ok {
		return t, nil
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsed),
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if len(proxyTransports) >= maxProxyTransports {
		for k, t := range proxyTransports {
			t.CloseIdleConnections()
			delete(proxyTransports, k)
			break
		}
	}
	proxyTransports[key] = transport
	return transport, nil
}

var unsupportedFilenameChars = regexp.MustCompile(`[/\\:*?"<>\|]`)

func Filename(rawURL string, hint string, contentType string, maxFilenameLength int) string {
	ext := mediaExtension(rawURL, contentType)
	if ext == "" {
		ext = ".bin"
	}
	base := sanitizeFilename(hint)
	if base == "" {
		base = "media"
	}
	// 按原始拼写去掉提示中的扩展名，避免 `.jpeg` / 大写扩展名（如 `.MP4`）
	// 与规范化后的扩展名不匹配导致双重扩展名（如 `photo.jpeg.jpg`）。
	origExt := filepath.Ext(base)
	if norm := normalizeMediaExtension(origExt); norm != "" {
		if ext == ".bin" {
			ext = norm
		}
		base = strings.TrimSuffix(base, origExt)
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
	if err != nil || !strings.Contains(parsed.Host, "pbs.twimg.com") {
		return rawURL
	}
	query := parsed.Query()
	if query.Get("name") == "" {
		query.Set("name", "4096x4096")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
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
	if !strings.Contains(parsed.Host, "twimg.com") {
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

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = unsupportedFilenameChars.ReplaceAllString(name, "")
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	return strings.TrimSpace(name)
}

func normalizedMaxFilenameLength(value int) int {
	if value <= 0 {
		return 120
	}
	if value < 16 {
		return 16
	}
	if value > 240 {
		return 240
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
