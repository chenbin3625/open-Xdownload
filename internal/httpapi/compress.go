package httpapi

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
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
// whether to compress. Real API payloads here are at most a few hundred KB
// (dashboard), so 8 MiB is a generous safety valve: anything larger streams
// through uncompressed instead of being held in memory.
const maxBufferedJSON = 8 << 20

// minCompressJSON is the size threshold below which compression is skipped:
// tiny payloads (a few hundred bytes) gain almost nothing and cost encoder time.
const minCompressJSON = 1024

// compressJSON wraps the API router so every JSON response is served compressed
// when the client accepts an encoding. The response body is buffered so the
// wrapper can (a) sniff Content-Type and only compress application/json, and
// (b) decide the encoding before emitting headers. Streaming endpoints are
// excluded: /api/events must never be buffered or compressed.
func compressJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || strings.HasPrefix(r.URL.Path, "/api/events") {
			next.ServeHTTP(w, r)
			return
		}
		encoding := httpx.NegotiateEncoding(r.Header.Get("Accept-Encoding"), httpx.EncodingBrotli, httpx.EncodingGzip)
		if encoding == "" {
			next.ServeHTTP(w, r)
			return
		}
		writer := newSniffCompressWriter(w, encoding)
		next.ServeHTTP(writer, r)
		writer.finish()
	})
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
	buffer             bytes.Buffer
	contentType        string
	handlerStatus      int
	handlerWroteHeader bool
	emitted            bool
	forceRaw           bool
}

func newSniffCompressWriter(w http.ResponseWriter, encoding string) *sniffCompressWriter {
	return &sniffCompressWriter{
		ResponseWriter: w,
		encoding:       encoding,
		handlerStatus:  http.StatusOK,
	}
}

func (g *sniffCompressWriter) WriteHeader(code int) {
	if g.handlerWroteHeader {
		return
	}
	g.handlerWroteHeader = true
	g.handlerStatus = code
	g.contentType = g.Header().Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(g.contentType), "text/event-stream") {
		// Defensive: never buffer a stream, even if it bypasses the /api/events
		// exclusion (e.g. a future handler).
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
	if g.encoding == "" || len(body) < minCompressJSON || !isJSONContentType(g.contentType) {
		g.emitRaw()
		_, _ = g.ResponseWriter.Write(body)
		return
	}

	header := g.ResponseWriter.Header()
	header.Set("Content-Encoding", g.encoding)
	header.Add("Vary", "Accept-Encoding")
	header.Del("Content-Length")
	g.ResponseWriter.WriteHeader(g.handlerStatus)
	writeCompressed(g.ResponseWriter, g.encoding, body)
}

func writeCompressed(w io.Writer, encoding string, body []byte) {
	switch encoding {
	case httpx.EncodingBrotli:
		bw := brotliWriterPool.Get().(*brotli.Writer)
		bw.Reset(w)
		_, _ = bw.Write(body)
		_ = bw.Close()
		brotliWriterPool.Put(bw)
	default:
		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w)
		_, _ = gz.Write(body)
		_ = gz.Close()
		gzipWriterPool.Put(gz)
	}
}
