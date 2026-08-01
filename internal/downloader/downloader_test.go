package downloader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFilenameHonorsMaxLengthAndExtension(t *testing.T) {
	got := Filename("https://video.twimg.com/ext_tw_video/movie.mp4", strings.Repeat("a", 60), "", 24)
	if len(got) > 24 {
		t.Fatalf("filename length = %d, want <= 24: %q", len(got), got)
	}
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestFilenameKeepsMediaExtensionWhenHintHasNonMediaExtension(t *testing.T) {
	got := Filename(
		"https://video.twimg.com/amplify_video/2073131761341460480/vid/avc1/720x1280/gAEB-Ux6zk1_4Z-t.mp4?tag=14",
		"正文里可能带短链 https://t.co/example",
		"",
		180,
	)
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestFilenameUsesContentTypeWhenURLHasNoExtension(t *testing.T) {
	got := Filename("https://pbs.twimg.com/media/example?format=small", "photo", "video/mp4; charset=utf-8", 180)
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestDownloadWithOptionsSkipsCompleteLocalFile(t *testing.T) {
	var requests atomic.Int64
	body := []byte("new media")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	existing := filepath.Join(dir, "media.mp4")
	// Seed a complete file (same size as the server response) — should be skipped.
	if err := os.WriteFile(existing, body, 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !result.Skipped {
		t.Fatal("skipped = false, want true for complete file")
	}
	if result.Path != existing {
		t.Fatalf("path = %q, want %q", result.Path, existing)
	}
	if result.Bytes != int64(len(body)) {
		t.Fatalf("bytes = %d, want %d", result.Bytes, len(body))
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (Open to verify size)", requests.Load())
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != string(body) {
		t.Fatalf("existing file overwritten: got %q, want %q", got, body)
	}
}

func TestDownloadWithOptionsRedownloadsPartialLocalFile(t *testing.T) {
	var requests atomic.Int64
	body := []byte("new media")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	existing := filepath.Join(dir, "media.mp4")
	// Seed a partial file (size mismatch) — must be re-downloaded, not skipped.
	if err := os.WriteFile(existing, []byte("par"), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.Skipped {
		t.Fatal("skipped = true, want false for partial file")
	}
	if result.Bytes != int64(len(body)) {
		t.Fatalf("bytes = %d, want %d", result.Bytes, len(body))
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != string(body) {
		t.Fatalf("file content = %q, want %q (partial overwritten)", got, body)
	}
	// No leftover .part temp files.
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.part"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}

func TestDownloadWithOptionsConcurrentSameFilenameCreatesDistinctFiles(t *testing.T) {
	bodies := map[string][]byte{
		"/one.mp4": []byte("first media body"),
		"/two.mp4": []byte("second media body with a different size"),
	}
	var arrived atomic.Int32
	var once sync.Once
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if arrived.Add(1) == int32(len(bodies)) {
			once.Do(func() { close(release) })
		}
		<-release
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	type outcome struct {
		result Result
		err    error
	}
	outcomes := make(chan outcome, len(bodies))
	for path := range bodies {
		go func(path string) {
			result, err := New().DownloadWithOptions(context.Background(), server.URL+path, dir, "media", Options{})
			outcomes <- outcome{result: result, err: err}
		}(path)
	}

	seenPaths := map[string]struct{}{}
	seenBodies := map[string]struct{}{}
	for range bodies {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("download: %v", got.err)
		}
		if got.result.Skipped {
			t.Fatalf("download was skipped; concurrent filename conflict must publish a distinct file")
		}
		if _, ok := seenPaths[got.result.Path]; ok {
			t.Fatalf("duplicate result path %q", got.result.Path)
		}
		seenPaths[got.result.Path] = struct{}{}
		content, err := os.ReadFile(got.result.Path)
		if err != nil {
			t.Fatalf("read result: %v", err)
		}
		seenBodies[string(content)] = struct{}{}
	}
	for _, body := range bodies {
		if _, ok := seenBodies[string(body)]; !ok {
			t.Fatalf("missing downloaded body %q; got bodies %#v", body, seenBodies)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.part"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}

func TestOpenIncludesHTTPErrorPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("{\n  \"error_response\": \"Dmcaed\"\n}"))
	}))
	defer server.Close()

	_, err := New().Open(context.Background(), server.URL+"/media.mp4", Options{})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want HTTPStatusError", err)
	}
	if statusErr.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", statusErr.StatusCode, http.StatusForbidden)
	}
	if !strings.Contains(statusErr.Payload, "Dmcaed") || !strings.Contains(statusErr.Error(), "Dmcaed") {
		t.Fatalf("status error did not preserve response payload: %+v", statusErr)
	}
}
