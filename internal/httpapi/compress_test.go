package httpapi

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

// jsonHandler writes a JSON response big enough to pass the compression
// threshold (via the real writeJSON path).
func jsonHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"payload": strings.Repeat("open-Xdownload", 512),
	})
}

func decodeCompressed(t *testing.T, body []byte, encoding string) string {
	t.Helper()
	switch encoding {
	case "br":
		reader := brotli.NewReader(bytes.NewReader(body))
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("brotli read: %v", err)
		}
		return string(decoded)
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
	default:
		return string(body)
	}
}

func TestCompressJSONBrotli(t *testing.T) {
	handler := compressJSON(http.HandlerFunc(jsonHandler))
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Accept-Encoding", "gzip, deflate, br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := response.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br", got)
	}
	if !strings.Contains(strings.Join(response.Header().Values("Vary"), ","), "Accept-Encoding") {
		t.Fatalf("Vary = %v, want Accept-Encoding", response.Header().Values("Vary"))
	}
	if response.Header().Get("Content-Length") != "" {
		t.Fatalf("Content-Length should be absent for compressed JSON")
	}
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := decodeCompressed(t, body, "br"); !strings.Contains(got, `"payload"`) {
		t.Fatalf("decoded body does not look like JSON: %q", got[:min(120, len(got))])
	}
}

func TestCompressJSONGzip(t *testing.T) {
	handler := compressJSON(http.HandlerFunc(jsonHandler))
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := decodeCompressed(t, body, "gzip"); !strings.Contains(got, `"payload"`) {
		t.Fatalf("decoded body does not look like JSON: %q", got[:min(120, len(got))])
	}
}

func TestCompressJSONIdentityWhenNotAccepted(t *testing.T) {
	handler := compressJSON(http.HandlerFunc(jsonHandler))
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want empty", response.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(response.Body.String(), `"payload"`) {
		t.Fatalf("raw body missing payload")
	}
}

func TestCompressJSONSkipsTinyPayloads(t *testing.T) {
	handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Accept-Encoding", "br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("tiny payload should not be compressed, got %q", response.Header().Get("Content-Encoding"))
	}
}

func TestCompressJSONSkipsEventStream(t *testing.T) {
	payload := strings.Repeat("data", 512)
	handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, payload)
	}))
	request := httptest.NewRequest(http.MethodGet, "/events", nil)
	request.Header.Set("Accept-Encoding", "br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("event-stream must not be compressed, got %q", response.Header().Get("Content-Encoding"))
	}
	if response.Body.String() != payload {
		t.Fatalf("event-stream body was altered")
	}
}

func TestCompressJSONSkipsSSERoute(t *testing.T) {
	payload := strings.Repeat("data", 512)
	handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	request.Header.Set("Accept-Encoding", "br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Header().Get("Content-Encoding") != "" || response.Body.String() != payload {
		t.Fatalf("/api/events must pass through raw")
	}
}

func TestCompressJSONFlushForcesRaw(t *testing.T) {
	handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"a": strings.Repeat("x", 4096)})
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("writer is not a flusher")
		}
		flusher.Flush()
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Accept-Encoding", "br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("flushed response should not be compressed, got %q", response.Header().Get("Content-Encoding"))
	}
}
