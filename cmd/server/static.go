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

func webAppHandler(api http.Handler, fsys http.FileSystem, inject htmlInjector) http.Handler {
	cache := newAssetCache(fsys)
	fileServer := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
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
			if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
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
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
		header.Add("Vary", "Accept-Encoding")
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
	body := item.body(encoding)
	etag := item.etag(encoding)
	header := w.Header()
	setCacheControl(w, name)
	if item.ctype != "" {
		header.Set("Content-Type", item.ctype)
	}
	header.Set("ETag", etag)
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
		header.Add("Vary", "Accept-Encoding")
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
		if strings.TrimSpace(part) == etag {
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
	hash     uint64
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

func (a *cachedAsset) etag(encoding string) string {
	if encoding == "" {
		return fmt.Sprintf(`"%x"`, a.hash)
	}
	return fmt.Sprintf(`"%x-%s"`, a.hash, encoding)
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
	c.mu.Lock()
	if item, ok := c.items[name]; ok {
		c.mu.Unlock()
		return item, true
	}
	c.mu.Unlock()

	identity := readOptional(c.fsys, name)
	gzipBody := readOptional(c.fsys, name+".gz")
	brotliBody := readOptional(c.fsys, name+".br")
	if len(identity) == 0 && len(gzipBody) == 0 && len(brotliBody) == 0 {
		return nil, false
	}
	hasher := fnv.New64a()
	switch {
	case len(identity) > 0:
		_, _ = hasher.Write(identity)
	case len(brotliBody) > 0:
		_, _ = hasher.Write(brotliBody)
	default:
		_, _ = hasher.Write(gzipBody)
	}
	item := &cachedAsset{
		gzip:   gzipBody,
		brotli: brotliBody,
		ctype:  contentTypeFor(name),
		hash:   hasher.Sum64(),
	}
	// Sidecar 在时不把 identity 留在 RSS：浏览器走 br/gzip；无压缩客户端首次再解压。
	if len(gzipBody) == 0 && len(brotliBody) == 0 {
		item.identity = identity
	}

	c.mu.Lock()
	if existing, ok := c.items[name]; ok {
		c.mu.Unlock()
		return existing, true
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
