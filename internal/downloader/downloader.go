package downloader

import (
	"context"
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
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("download failed: HTTP %d", e.StatusCode)
}

type Options struct {
	ModTime           time.Time
	LargePhoto        bool
	ProxyURL          string
	MaxFilenameLength int
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
		return existing, nil
	}
	// 原子写入：先写临时文件，fsync 后以不覆盖已有文件的方式发布到最终路径，避免
	// 崩溃留下被当作完整下载的残缺文件。临时文件名带随机串，避免并发下载互相覆盖。
	tempFile, err := os.CreateTemp(dir, "."+filepath.Base(basePath)+".*.part")
	if err != nil {
		return Result{}, err
	}
	tempPath := tempFile.Name()
	if err := tempFile.Chmod(0o644); err != nil {
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

func existingCompleteResult(path string, contentLength int64) (Result, bool, error) {
	result, complete, _, err := existingFileState(path, contentLength)
	return result, complete, err
}

func publishTempFile(tempPath string, basePath string, bytes int64, contentLength int64, maxFilenameLength int, replaceBase bool) (Result, bool, error) {
	triedReplaceBase := false
	for index := 0; ; index++ {
		candidate := suffixedPath(basePath, index, maxFilenameLength)
		err := os.Link(tempPath, candidate)
		if err == nil {
			if err := os.Remove(tempPath); err != nil {
				return Result{}, false, err
			}
			return Result{Path: candidate, Bytes: bytes}, false, nil
		}
		if !os.IsExist(err) {
			return Result{}, false, err
		}
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
		return nil, &HTTPStatusError{StatusCode: response.StatusCode}
	}
	return response, nil
}

// proxyTransports 缓存按代理地址复用的 Transport，使代理下载也能复用空闲连接。
var proxyTransports sync.Map // map[string]*http.Transport

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
	if v, ok := proxyTransports.Load(key); ok {
		return v.(*http.Transport), nil
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsed),
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	actual, _ := proxyTransports.LoadOrStore(key, transport)
	return actual.(*http.Transport), nil
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
	if baseExt := normalizeMediaExtension(filepath.Ext(base)); baseExt != "" {
		if ext == ".bin" {
			ext = baseExt
		}
		base = strings.TrimSuffix(base, baseExt)
	}
	return composeFilename(base, ext, "", maxFilenameLength)
}

func filenameFromURL(rawURL string, hint string, contentType string, maxFilenameLength int) string {
	return Filename(rawURL, hint, contentType, maxFilenameLength)
}

func InferredFilename(rawURL string, hint string, maxFilenameLength int) (string, bool) {
	if mediaExtension(rawURL, "") == "" && normalizeMediaExtension(filepath.Ext(sanitizeFilename(hint))) == "" {
		return "", false
	}
	return Filename(rawURL, hint, "", maxFilenameLength), true
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

func uniquePath(path string, maxFilenameLength int) (string, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	for index := 1; ; index++ {
		suffix := fmt.Sprintf("(%d)", index)
		candidate := filepath.Join(dir, composeFilename(stem, ext, suffix, maxFilenameLength))
		_, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
}
