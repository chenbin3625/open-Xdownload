package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andybalholm/brotli"
)

const jsBody = "console.log('hello world');"

func testWebFS() http.FileSystem {
	return http.FS(fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><html></html>")},
		"icon.svg":         {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")},
		"assets/app.js":    {Data: []byte(jsBody)},
		"assets/app.css":   {Data: []byte("body{margin:0}")},
		"assets/image.png": {Data: []byte("not really a png but binary-ish")},
	})
}

func TestWebAppHandlerReturns404ForMissingStaticAssets(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("SPA route status = %d, want 200", response.Code)
	}
}

func TestHashedAssetsAreCachedImmutably(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if got := response.Header().Get("Cache-Control"); got != hashedAssetCacheControl {
		t.Fatalf("asset Cache-Control = %q, want %q", got, hashedAssetCacheControl)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", response.Code)
	}
}

func TestHtmlAndPublicFilesAreNotLongCached(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)
	for _, target := range []string{"/", "/icon.svg"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if got := response.Header().Get("Cache-Control"); got != htmlCacheControl {
			t.Fatalf("%s Cache-Control = %q, want %q", target, got, htmlCacheControl)
		}
	}
}

func TestGzipServesCompressedAssetWhenAccepted(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !stringsVaryIncludes(response.Header().Values("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want it to include Accept-Encoding", response.Header().Get("Vary"))
	}

	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := response.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q, want %d (compressed size)", got, len(body))
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if string(decoded) != jsBody {
		t.Fatalf("decoded body = %q, want %q", decoded, jsBody)
	}
}

func TestGzipSkippedWithoutAcceptEncoding(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want empty", response.Header().Get("Content-Encoding"))
	}
	if response.Body.String() != jsBody {
		t.Fatalf("body = %q, want raw %q", response.Body.String(), jsBody)
	}
}

func TestGzipSkippedForBinaryAssets(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	request := httptest.NewRequest(http.MethodGet, "/assets/image.png", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want empty for binary asset", response.Header().Get("Content-Encoding"))
	}
}

func TestGzipSkippedWhenRangeRequested(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Range", "bytes=0-4")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want empty for range request", response.Header().Get("Content-Encoding"))
	}
}

func TestGzipServesCompressedIndexHtml(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := response.Header().Get("Cache-Control"); got != htmlCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, htmlCacheControl)
	}
}

// decodeBody decodes a compressed response body into plain text based on the
// response's Content-Encoding header.
func decodeBody(t *testing.T, body []byte, encoding string) string {
	t.Helper()
	switch encoding {
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		defer reader.Close()
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("gunzip: %v", err)
		}
		return string(decoded)
	case "br":
		reader := brotli.NewReader(bytes.NewReader(body))
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("brotli read: %v", err)
		}
		return string(decoded)
	default:
		return string(body)
	}
}

func TestBrotliPreferredOverGzip(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	request.Header.Set("Accept-Encoding", "gzip, deflate, br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := response.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br", got)
	}
	if !stringsVaryIncludes(response.Header().Values("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want it to include Accept-Encoding", response.Header().Get("Vary"))
	}
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := decodeBody(t, body, "br"); got != jsBody {
		t.Fatalf("decoded body = %q, want %q", got, jsBody)
	}
}

func TestBrotliRejectedFallsBackToGzip(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	request.Header.Set("Accept-Encoding", "br;q=0, gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip after br;q=0", got)
	}
}

func TestBrotliIndexHtml(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got := response.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br", got)
	}
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := decodeBody(t, body, "br"); got != "<!doctype html><html></html>" {
		t.Fatalf("decoded body = %q", got)
	}
}

func stringsVaryIncludes(values []string, needle string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.TrimSpace(part) == needle {
				return true
			}
		}
	}
	return false
}

func TestHashedAssetETagReturns304(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	request.Header.Set("Accept-Encoding", "br")
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag on first response")
	}
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.Code)
	}

	repeat := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	repeat.Header.Set("Accept-Encoding", "br")
	repeat.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, repeat)
	if second.Code != http.StatusNotModified {
		t.Fatalf("revalidate status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 body should be empty, got %d bytes", second.Body.Len())
	}
}

func TestCompressedAssetBytesAreStableAcrossRequests(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)

	read := func() []byte {
		request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		request.Header.Set("Accept-Encoding", "gzip, br")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if got := response.Header().Get("Content-Encoding"); got != "br" {
			t.Fatalf("Content-Encoding = %q, want br", got)
		}
		return response.Body.Bytes()
	}

	first := read()
	second := read()
	if !bytes.Equal(first, second) {
		t.Fatal("cached brotli body changed between requests")
	}
}

func gzipWithComment(body, comment string) []byte {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	writer.Header.Comment = comment
	_, _ = writer.Write([]byte(body))
	_ = writer.Close()
	return buf.Bytes()
}

func TestPrecompressedSidecarIsServedWithoutReencoding(t *testing.T) {
	sidecar := gzipWithComment(jsBody, "prebuilt")
	fsys := http.FS(fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><html></html>")},
		"assets/app.js":    {Data: []byte(jsBody)},
		"assets/app.js.gz": {Data: sidecar},
	})
	handler := webAppHandler(http.NotFoundHandler(), fsys, nil)

	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", response.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(response.Body.Bytes(), sidecar) {
		t.Fatal("gzip body was re-encoded instead of using the sidecar")
	}

	direct := httptest.NewRecorder()
	handler.ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "/assets/app.js.gz", nil))
	if direct.Code != http.StatusNotFound {
		t.Fatalf("sidecar URL status = %d, want 404", direct.Code)
	}
}

func TestSidecarOnlyAssetServesWithoutIdentityFile(t *testing.T) {
	sidecar := gzipWithComment(jsBody, "embed-slim")
	fsys := http.FS(fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><html></html>")},
		"assets/app.js.gz": {Data: sidecar},
	})
	handler := webAppHandler(http.NotFoundHandler(), fsys, nil)

	gzipReq := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	gzipReq.Header.Set("Accept-Encoding", "gzip")
	gzipRes := httptest.NewRecorder()
	handler.ServeHTTP(gzipRes, gzipReq)
	if gzipRes.Code != http.StatusOK {
		t.Fatalf("gzip status = %d, want 200", gzipRes.Code)
	}
	if gzipRes.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", gzipRes.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(gzipRes.Body.Bytes(), sidecar) {
		t.Fatal("gzip body was re-encoded instead of using the sidecar")
	}

	identity := httptest.NewRecorder()
	handler.ServeHTTP(identity, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if identity.Code != http.StatusOK {
		t.Fatalf("identity status = %d, want 200", identity.Code)
	}
	if identity.Header().Get("Content-Encoding") != "" {
		t.Fatalf("identity Content-Encoding = %q, want empty", identity.Header().Get("Content-Encoding"))
	}
	if identity.Body.String() != jsBody {
		t.Fatalf("decompressed identity = %q, want %q", identity.Body.String(), jsBody)
	}

	ranged := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	ranged.Header.Set("Range", "bytes=0-4")
	ranged.Header.Set("Accept-Encoding", "gzip")
	rangeRes := httptest.NewRecorder()
	handler.ServeHTTP(rangeRes, ranged)
	if rangeRes.Code != http.StatusOK {
		t.Fatalf("sidecar-only range status = %d, want 200 (ignored)", rangeRes.Code)
	}
	if rangeRes.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("sidecar-only range Content-Encoding = %q, want gzip", rangeRes.Header().Get("Content-Encoding"))
	}
}

func TestInjectedIndexHTMLBypassesStaticETag(t *testing.T) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), func(_ *http.Request, html []byte) []byte {
		return append(html, []byte(`<script type="application/json" id="app-bootstrap">{"ok":true}</script>`)...)
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if !strings.Contains(response.Body.String(), `id="app-bootstrap"`) {
		t.Fatalf("missing bootstrap: %s", response.Body.String())
	}
}

func BenchmarkServeHashedJSBrotli(b *testing.B) {
	handler := webAppHandler(http.NotFoundHandler(), testWebFS(), nil)
	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	request.Header.Set("Accept-Encoding", "gzip, deflate, br")
	// Warm the cache so the benchmark measures the hit path (production steady state).
	warm := httptest.NewRecorder()
	handler.ServeHTTP(warm, request)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			b.Fatalf("status = %d", response.Code)
		}
	}
}
