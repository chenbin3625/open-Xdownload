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
	if filename, ok := InferredFilename(rawURL, filenameHint, maxFilenameLength); ok {
		if result, ok, err := existingFileResult(filepath.Join(dir, filename)); err != nil {
			return Result{}, err
		} else if ok {
			return result, nil
		}
	}
	response, err := d.Open(ctx, rawURL, options)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()

	basePath := filepath.Join(dir, Filename(rawURL, filenameHint, response.Header.Get("Content-Type"), maxFilenameLength))
	if result, ok, err := existingFileResult(basePath); err != nil {
		return Result{}, err
	} else if ok {
		return result, nil
	}
	file, err := os.OpenFile(basePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			if result, ok, statErr := existingFileResult(basePath); statErr != nil {
				return Result{}, statErr
			} else if ok {
				return result, nil
			}
		}
		return Result{}, err
	}

	bytes, err := io.Copy(file, response.Body)
	closeErr := file.Close()
	if err != nil {
		// 网络中断等情况下删除半截文件，避免残留垃圾并在重试时被 uniquePath 改名成 (1) 副本。
		_ = os.Remove(basePath)
		return Result{}, err
	}
	if closeErr != nil {
		_ = os.Remove(basePath)
		return Result{}, closeErr
	}
	if !options.ModTime.IsZero() {
		_ = os.Chtimes(basePath, time.Now(), options.ModTime)
	}
	return Result{Path: basePath, Bytes: bytes}, nil
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

func createUniqueFile(path string, maxFilenameLength int) (*os.File, string, error) {
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	for index := 0; ; index++ {
		candidate := path
		if index > 0 {
			suffix := fmt.Sprintf("(%d)", index)
			candidate = filepath.Join(dir, composeFilename(stem, ext, suffix, maxFilenameLength))
		}
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return file, candidate, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, "", err
	}
}

func existingFileResult(path string) (Result, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	if info.IsDir() {
		return Result{}, false, nil
	}
	return Result{Path: path, Bytes: info.Size(), Skipped: true}, true, nil
}
