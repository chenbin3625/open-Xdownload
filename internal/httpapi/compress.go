package httpapi

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

var brotliWriterPool = sync.Pool{
	New: func() any {
		return brotli.NewWriter(io.Discard)
	},
}

// maxBufferedJSON caps how much of a JSON response we buffer before deciding
// whether to compress. The largest JSON payloads this API produces are paged
// lists (≤200 rows) and the dashboard, i.e. well under a few hundred KB, so
// 2 MiB is a generous safety valve: anything larger streams through
// uncompressed instead of being held in memory. Media endpoints never reach
// this buffer at all (see isStreamingPath / Content-Type sniffing).
const maxBufferedJSON = 2 << 20

// minCompressJSON is the size threshold below which compression is skipped:
// tiny payloads (a few hundred bytes) gain almost nothing and cost encoder time.
const minCompressJSON = 1024

// isStreamingPath reports whether the response must be streamed through without
// buffering or compression:
//   - /api/events is SSE and must never be buffered;
//   - media read endpoints under /api/library/ (record file/preview and the
//     orphan-file endpoint) can be hundreds of MB, so buffering them until the
//     8 MiB overflow point only adds first-byte latency and, on an
//     unauthenticated deployment, memory amplification.
//
// The JSON list endpoint /api/library/downloads is deliberately NOT excluded:
// it is still JSON and benefits from compression.
func isStreamingPath(path string) bool {
	if strings.HasPrefix(path, "/api/events") || strings.HasPrefix(path, "/api/assets/") {
		return true
	}
	return strings.HasPrefix(path, "/api/library/") && path != "/api/library/downloads" && path != "/api/library/users"
}

// compressJSON wraps the API router so every JSON response is served compressed
// when the client accepts an encoding. The response body is buffered so the
// wrapper can (a) sniff Content-Type and only compress application/json, and
// (b) decide the encoding before emitting headers. Streaming endpoints are
// excluded: /api/events must never be buffered or compressed.
func compressJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStreamingPath(r.URL.Path) {
			serveWithRecovery(next, newStreamingResponseWriter(w), r)
			return
		}
		// Vary 只在这里加一次：finish() 不再重复 Add（重复 Vary 虽然语义等价，
		// 但会让响应出现重复头，也让缓存/调试输出产生噪声）。
		w.Header().Add("Vary", "Accept-Encoding")
		encoding := httpx.NegotiateEncoding(r.Header.Get("Accept-Encoding"), httpx.EncodingBrotli, httpx.EncodingGzip)
		// identity 请求同样走缓冲：包装层的 panic 恢复依赖"尚未写出任何字节"才能
		// 换成干净的 500 JSON，否则客户端会拿到半截 JSON + 200（见
		// TestCompressJSONRecoversWithoutPublishingPartialJSON）。缓冲的代价只落在
		// 非流式 JSON 上，体量有限。
		writer := newSniffCompressWriter(w, encoding, r.Method == http.MethodHead)
		completed := false
		defer func() {
			if !completed {
				return
			}
			writer.finish()
		}()
		serveWithRecovery(next, writer, r)
		completed = true
	})
}

type streamingResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func newStreamingResponseWriter(w http.ResponseWriter) *streamingResponseWriter {
	return &streamingResponseWriter{ResponseWriter: w}
}

func (w *streamingResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *streamingResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *streamingResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (w *streamingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func clearRecoveryHeaders(header http.Header, preserveVaryAcceptEncoding bool) {
	clear(header)
	if preserveVaryAcceptEncoding {
		header.Add("Vary", "Accept-Encoding")
	}
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Cache-Control", "no-store")
}

func serveWithRecovery(next http.Handler, w http.ResponseWriter, r *http.Request) {
	defer func() {
		if value := recover(); value != nil {
			if value == http.ErrAbortHandler {
				if writer, ok := w.(*sniffCompressWriter); ok && !writer.emitted {
					writer.buffer.Reset()
					clear(w.Header())
				} else if writer, ok := w.(*streamingResponseWriter); ok && !writer.wroteHeader {
					clear(w.Header())
				}
				panic(value)
			}
			log.Printf("http panic: %v\n%s", value, debug.Stack())
			preserveVary := false
			if writer, ok := w.(*sniffCompressWriter); ok {
				if writer.emitted {
					panic(http.ErrAbortHandler)
				}
				preserveVary = true
				writer.buffer.Reset()
				writer.handlerWroteHeader = false
				writer.handlerStatus = http.StatusOK
				writer.forceRaw = false
				writer.contentType = ""
			}
			if writer, ok := w.(*streamingResponseWriter); ok && writer.wroteHeader {
				panic(http.ErrAbortHandler)
			}
			clearRecoveryHeaders(w.Header(), preserveVary)
			writeError(w, http.StatusInternalServerError, fmt.Errorf("handler panic"))
		}
	}()
	next.ServeHTTP(w, r)
}

func isJSONContentType(contentType string) bool {
	if separator := strings.IndexByte(contentType, ';'); separator >= 0 {
		contentType = contentType[:separator]
	}
	return strings.EqualFold(strings.TrimSpace(contentType), "application/json")
}

// sniffCompressWriter buffers a response until it knows the Content-Type. If
// the payload is compressible JSON and large enough it re-serializes it with
// the negotiated encoding; otherwise it plays the buffered bytes back raw.
// Any stream-like use (event-stream Content-Type, an explicit Flush, or an
// oversized body) flips it to raw passthrough so streaming is never harmed.
type sniffCompressWriter struct {
	http.ResponseWriter
	encoding           string
	isHead             bool
	buffer             bytes.Buffer
	contentType        string
	handlerStatus      int
	handlerWroteHeader bool
	emitted            bool
	forceRaw           bool
}

func newSniffCompressWriter(w http.ResponseWriter, encoding string, isHead bool) *sniffCompressWriter {
	return &sniffCompressWriter{
		ResponseWriter: w,
		encoding:       encoding,
		isHead:         isHead,
		handlerStatus:  http.StatusOK,
	}
}

func statusForbidsBody(status int) bool {
	return (status >= 100 && status < 200) || status == http.StatusNoContent || status == http.StatusNotModified
}

func (g *sniffCompressWriter) WriteHeader(code int) {
	if g.handlerWroteHeader {
		return
	}
	g.handlerWroteHeader = true
	g.handlerStatus = code
	g.contentType = g.Header().Get("Content-Type")
	if !isJSONContentType(g.contentType) {
		// Only JSON is ever compressed, so anything else (media files served by
		// http.ServeFile, event streams, bodies left to net/http's sniffing)
		// streams through untouched. Buffering it would hold up to
		// maxBufferedJSON per request in memory and delay the first byte until
		// the whole file was read.
		g.forceRaw = true
	}
}

func (g *sniffCompressWriter) Write(p []byte) (int, error) {
	if !g.handlerWroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.forceRaw {
		g.emitRaw()
		return g.ResponseWriter.Write(p)
	}
	if g.buffer.Len()+len(p) > maxBufferedJSON {
		g.forceRaw = true
		g.emitRaw()
		if _, err := g.ResponseWriter.Write(g.buffer.Bytes()); err != nil {
			return 0, err
		}
		g.buffer.Reset()
		return g.ResponseWriter.Write(p)
	}
	return g.buffer.Write(p)
}

func (g *sniffCompressWriter) Unwrap() http.ResponseWriter {
	return g.ResponseWriter
}

// Flush forces raw passthrough so a handler that streams (or proxies) is never
// left holding the response in the buffer.

func (g *sniffCompressWriter) Flush() {
	if !g.handlerWroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	g.forceRaw = true
	g.emitRaw()
	if g.buffer.Len() > 0 {
		_, _ = g.ResponseWriter.Write(g.buffer.Bytes())
		g.buffer.Reset()
	}
	if err := http.NewResponseController(g.ResponseWriter).Flush(); err != nil {
		if flusher, ok := g.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (g *sniffCompressWriter) emitRaw() {
	if g.emitted {
		return
	}
	g.emitted = true
	g.ResponseWriter.WriteHeader(g.handlerStatus)
}

// finish runs after the handler returns: emit the real response, compressed or
// raw depending on what was buffered.
func (g *sniffCompressWriter) finish() {
	if !g.handlerWroteHeader {
		return
	}
	if g.forceRaw {
		g.emitRaw()
		return
	}
	body := g.buffer.Bytes()
	noBody := statusForbidsBody(g.handlerStatus)
	if g.encoding != "" && !noBody && len(body) >= minCompressJSON && isJSONContentType(g.contentType) {
		var compressed bytes.Buffer
		if err := writeCompressed(&compressed, g.encoding, body); err == nil {
			header := g.ResponseWriter.Header()
			header.Set("Content-Encoding", g.encoding)
			header.Set("Content-Length", strconv.Itoa(compressed.Len()))
			g.emitted = true
			g.ResponseWriter.WriteHeader(g.handlerStatus)
			if !g.isHead && compressed.Len() > 0 {
				_, _ = g.ResponseWriter.Write(compressed.Bytes())
			}
			return
		}
	}

	header := g.ResponseWriter.Header()
	if !noBody {
		header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	g.emitRaw()
	if !g.isHead && !noBody && len(body) > 0 {
		_, _ = g.ResponseWriter.Write(body)
	}
}

func writeCompressed(w io.Writer, encoding string, body []byte) error {
	switch encoding {
	case httpx.EncodingBrotli:
		bw := brotliWriterPool.Get().(*brotli.Writer)
		bw.Reset(w)
		defer func() {
			bw.Reset(io.Discard)
			brotliWriterPool.Put(bw)
		}()
		if _, err := bw.Write(body); err != nil {
			_ = bw.Close()
			return err
		}
		return bw.Close()
	default:
		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Reset(io.Discard)
			gzipWriterPool.Put(gz)
		}()
		if _, err := gz.Write(body); err != nil {
			_ = gz.Close()
			return err
		}
		return gz.Close()
	}
}
