package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"hash/fnv"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/webui"
)

const (
	// hashedAssetCacheControl targets /assets/* which Vite emits with
	// content-hashed filenames. The bytes are immutable, so browsers and
	// intermediary caches can hold them for a year without revalidation —
	// a fresh index.html switches to new hashes automatically.
	hashedAssetCacheControl = "public, max-age=31536000, immutable"
	// htmlCacheControl forces browsers to revalidate index.html on every
	// navigation so newly shipped builds (with new asset hashes) are picked
	// up immediately, while still allowing ETag short-circuiting (304).
	htmlCacheControl = "no-cache"
)

// compressibleExt reports whether a file's content is a text-like asset that
// compresses well. Binary media (images/videos/woff2) are already compressed
// or must keep their raw byte ranges.
var compressibleExt = map[string]bool{
	".js":          true,
	".mjs":         true,
	".css":         true,
	".html":        true,
	".htm":         true,
	".svg":         true,
	".json":        true,
	".map":         true,
	".txt":         true,
	".xml":         true,
	".webmanifest": true,
}

type htmlInjector func(*http.Request, []byte) []byte

func withWebApp(api http.Handler, webDir string, inject htmlInjector) http.Handler {
	if fsys, ok := externalWebApp(webDir); ok {
		return webAppHandler(api, fsys, inject)
	}
	if fsys, ok := webui.FileSystem(); ok {
		return webAppHandler(api, fsys, inject)
	}
	return api
}

func externalWebApp(webDir string) (http.FileSystem, bool) {
	if webDir == "" {
		return nil, false
	}
	info, err := os.Stat(webDir)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	fsys := http.Dir(webDir)
	if !fileExists(fsys, "index.html") {
		return nil, false
	}
	return fsys, true
}

// webAppCSP 约束 SPA 可加载的资源。构建产物中没有内联可执行脚本（bootstrap 注入
// 的是 <script type="application/json"> 数据块，不受 script-src 限制），因此
// script-src 可以收到 'self'。style-src 需要 'unsafe-inline'：antd 的 CSS-in-JS
// 会在运行时注入 <style>。字体与字体样式表来自 Google Fonts（index.html 中已有
// preconnect），媒体与图片需允许 twimg 以显示远端海报。
const webAppCSP = "default-src 'self'; " +
	"base-uri 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"form-action 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' data: https://fonts.gstatic.com; " +
	"img-src 'self' data: blob: https://pbs.twimg.com https://*.twimg.com; " +
	"media-src 'self' blob: https://*.twimg.com; " +
	"connect-src 'self'"

func setWebAppSecurityHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Security-Policy", webAppCSP)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	// 禁止 iframe 嵌套，防止点击劫持。
	header.Set("X-Frame-Options", "DENY")
}

func webAppHandler(api http.Handler, fsys http.FileSystem, inject htmlInjector) http.Handler {
	cache := newAssetCache(fsys)
	fileServer := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		setWebAppSecurityHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		// Sidecar 只给内容协商用，禁止直接下载 .br/.gz（错误 Content-Type）。
		if isCompressionSidecar(name) {
			http.NotFound(w, r)
			return
		}

		item, ok := cache.get(name)
		if !ok {
			if strings.HasPrefix(name, "assets/") || contentTypeFor(name) != "" {
				http.NotFound(w, r)
				return
			}
			item, ok = cache.get("index.html")
			if !ok {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
		}
		if compressibleExt[strings.ToLower(path.Ext(name))] {
			w.Header().Add("Vary", "Accept-Encoding")
		}

		if name == "index.html" && inject != nil {
			identity := item.body("")
			injected := inject(r, identity)
			if len(injected) > 0 && !bytes.Equal(injected, identity) {
				encoding := httpx.NegotiateEncoding(r.Header.Get("Accept-Encoding"), httpx.EncodingBrotli, httpx.EncodingGzip)
				serveInjectedHTML(w, r, injected, encoding)
				return
			}
		}

		// Byte ranges apply to the identity file. Sidecar-only embed has no
		// identity on disk — ignore Range and serve the negotiated body.
		if r.Header.Get("Range") != "" && fileExists(fsys, name) {
			setCacheControl(w, name)
			fileServer.ServeHTTP(w, r)
			return
		}

		encoding := ""
		if compressibleExt[strings.ToLower(path.Ext(name))] {
			encoding = httpx.NegotiateEncoding(r.Header.Get("Accept-Encoding"), httpx.EncodingBrotli, httpx.EncodingGzip)
		}
		serveAsset(w, r, name, item, encoding)
	})
}

func serveInjectedHTML(w http.ResponseWriter, r *http.Request, html []byte, encoding string) {
	body := html
	if encoding != "" {
		body = compressBytes(html, encoding)
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write(html)
	sum := hasher.Sum64()
	etag := fmt.Sprintf(`"%x-html"`, sum)
	if encoding != "" {
		etag = fmt.Sprintf(`"%x-html-%s"`, sum, encoding)
	}
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("ETag", etag)
	// Vary 已由 webAppHandler 按可压缩扩展名统一添加；这里不再重复 Add，
	// 避免响应出现两行相同的 Vary。
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
	}
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(body)
}

func serveAsset(w http.ResponseWriter, r *http.Request, name string, item *cachedAsset, encoding string) {
	body, etag := item.bodyAndETag(encoding)
	header := w.Header()
	setCacheControl(w, name)
	if item.ctype != "" {
		header.Set("Content-Type", item.ctype)
	}
	header.Set("ETag", etag)
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
	}
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(body)
}

func setCacheControl(w http.ResponseWriter, name string) {
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", hashedAssetCacheControl)
	} else {
		w.Header().Set("Cache-Control", htmlCacheControl)
	}
}

func etagMatch(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}

// cachedAsset holds the raw file plus lazily compressed encodings. Hashed
// Vite assets are immutable for the process lifetime, so paying compression
// once (instead of on every request) is the dominant static-serving win:
// antd.js ~477KB Brotli is tens of milliseconds of CPU per hit without a cache.
type cachedAsset struct {
	mu       sync.Mutex
	identity []byte
	gzip     []byte
	brotli   []byte
	ctype    string
	sources  [3]assetSource
	// hashes 缓存"该表示实际发送的字节"的哈希。ETag 必须由真正发出的字节算出：
	// 外部 webDir 部分重建时 identity 与 .br/.gz 可能来自不同版本，若 ETag 取自
	// identity 而 body 取自 sidecar，就会出现同一 ETag 对应两份内容（缓存串味）。
	hashes map[string]uint64
}

type assetSource struct {
	size     int64
	modified time.Time
}

func (a *cachedAsset) ensureIdentity() []byte {
	if len(a.identity) > 0 {
		return a.identity
	}
	if len(a.brotli) > 0 {
		a.identity = decompressBytes(a.brotli, httpx.EncodingBrotli)
		return a.identity
	}
	if len(a.gzip) > 0 {
		a.identity = decompressBytes(a.gzip, httpx.EncodingGzip)
		return a.identity
	}
	return nil
}

func (a *cachedAsset) body(encoding string) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch encoding {
	case httpx.EncodingBrotli:
		if len(a.brotli) == 0 {
			a.brotli = compressBytes(a.ensureIdentity(), encoding)
		}
		return a.brotli
	case httpx.EncodingGzip:
		if len(a.gzip) == 0 {
			a.gzip = compressBytes(a.ensureIdentity(), encoding)
		}
		return a.gzip
	default:
		return a.ensureIdentity()
	}
}

// bodyAndETag 返回要发送的字节与该字节对应的 ETag。两者由同一次 body() 结果派生，
// 因此不会出现 ETag 与 body 不是同一版本的问题。
func (a *cachedAsset) bodyAndETag(encoding string) ([]byte, string) {
	body := a.body(encoding)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hashes == nil {
		a.hashes = make(map[string]uint64, 3)
	}
	sum, ok := a.hashes[encoding]
	if !ok {
		hasher := fnv.New64a()
		_, _ = hasher.Write(body)
		sum = hasher.Sum64()
		a.hashes[encoding] = sum
	}
	if encoding == "" {
		return body, fmt.Sprintf(`"%x"`, sum)
	}
	return body, fmt.Sprintf(`"%x-%s"`, sum, encoding)
}

type assetCache struct {
	fsys  http.FileSystem
	mu    sync.Mutex
	items map[string]*cachedAsset
}

func newAssetCache(fsys http.FileSystem) *assetCache {
	return &assetCache{fsys: fsys, items: make(map[string]*cachedAsset)}
}

func (c *assetCache) get(name string) (*cachedAsset, bool) {
	var sources [3]assetSource
	for i, suffix := range []string{"", ".gz", ".br"} {
		if file, err := c.fsys.Open(name + suffix); err == nil {
			if info, err := file.Stat(); err == nil && !info.IsDir() {
				sources[i] = assetSource{size: info.Size(), modified: info.ModTime()}
			}
			_ = file.Close()
		}
	}
	c.mu.Lock()
	if item, ok := c.items[name]; ok && item.sources == sources {
		c.mu.Unlock()
		return item, true
	}
	c.mu.Unlock()

	identity := readOptional(c.fsys, name)
	gzipBody := readOptional(c.fsys, name+".gz")
	brotliBody := readOptional(c.fsys, name+".br")
	if len(gzipBody) > 0 && len(decompressBytes(gzipBody, httpx.EncodingGzip)) == 0 {
		gzipBody = nil
	}
	if len(brotliBody) > 0 && len(decompressBytes(brotliBody, httpx.EncodingBrotli)) == 0 {
		brotliBody = nil
	}
	if len(identity) == 0 && len(gzipBody) == 0 && len(brotliBody) == 0 {
		return nil, false
	}
	// identity 文件存在时始终保留（即使有 sidecar）：identity 请求必须返回 identity
	// 文件的真实字节，否则 sidecar 陈旧时会用旧内容回答新 identity 的请求。
	// 内嵌资源没有 identity 文件（sync 脚本按设计删除），因此不会额外占 RSS。
	item := &cachedAsset{
		identity: identity,
		gzip:     gzipBody,
		brotli:   brotliBody,
		ctype:    contentTypeFor(name),
		sources:  sources,
	}

	c.mu.Lock()
	if existing, ok := c.items[name]; ok && existing.sources == sources {
		c.mu.Unlock()
		return existing, true
	}
	if len(c.items) >= 128 {
		clear(c.items)
	}
	c.items[name] = item
	c.mu.Unlock()
	return item, true
}

func isCompressionSidecar(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".br")
}

func readOptional(fsys http.FileSystem, name string) []byte {
	file, err := fsys.Open(name)
	if err != nil {
		return nil
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		return nil
	}
	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		return nil
	}
	return data
}

func contentTypeFor(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ctype := mime.TypeByExtension(ext); ctype != "" {
		return ctype
	}
	switch ext {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".json", ".webmanifest":
		return "application/json"
	default:
		return ""
	}
}

func decompressBytes(data []byte, encoding string) []byte {
	if len(data) == 0 {
		return nil
	}
	switch encoding {
	case httpx.EncodingGzip:
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		defer reader.Close()
		decoded, err := io.ReadAll(reader)
		if err != nil {
			return nil
		}
		return decoded
	case httpx.EncodingBrotli:
		decoded, err := io.ReadAll(brotli.NewReader(bytes.NewReader(data)))
		if err != nil {
			return nil
		}
		return decoded
	default:
		return nil
	}
}

func compressBytes(data []byte, encoding string) []byte {
	var buf bytes.Buffer
	var writer compressor
	switch encoding {
	case httpx.EncodingBrotli:
		writer = brotli.NewWriter(&buf)
	default:
		writer = gzip.NewWriter(&buf)
	}
	_, _ = writer.Write(data)
	_ = writer.Close()
	return buf.Bytes()
}

type compressor interface {
	io.Writer
	io.Closer
	Flush() error
}

func fileExists(fsys http.FileSystem, name string) bool {
	if name == "" {
		return false
	}
	file, err := fsys.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	stat, err := file.Stat()
	return err == nil && !stat.IsDir()
}
