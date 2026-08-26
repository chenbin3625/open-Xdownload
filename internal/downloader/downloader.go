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
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
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
		client: httpx.Client("", 10*time.Minute),
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
			if len(via) == 0 {
				return nil
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("拒绝非 HTTP 媒体重定向: %s", req.URL.String())
			}
			if !isAllowedTwimgHost(req.URL.Hostname()) {
				return fmt.Errorf("拒绝媒体重定向到非 twimg.com 域名: %s", req.URL.Hostname())
			}
			return nil
		},
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

// transportForProxy 返回按代理地址复用的 Transport（底层拨号带链路本地防护）。
// 空代理也返回受保护的直连 Transport，并尊重环境变量代理。
func transportForProxy(proxyURL string) (*http.Transport, error) {
	return httpx.Transport(proxyURL)
}

func isAllowedTwimgHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "twimg.com" || strings.HasSuffix(host, ".twimg.com")
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
	if err != nil {
		return rawURL
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || !isAllowedTwimgHost(host) || (host != "pbs.twimg.com" && !strings.HasSuffix(host, ".pbs.twimg.com")) {
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
