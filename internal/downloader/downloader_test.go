package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFilenameFromURLHonorsMaxLengthAndExtension(t *testing.T) {
	got := filenameFromURL("https://video.twimg.com/ext_tw_video/movie.mp4", strings.Repeat("a", 60), "", 24)
	if len(got) > 24 {
		t.Fatalf("filename length = %d, want <= 24: %q", len(got), got)
	}
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestFilenameFromURLKeepsMediaExtensionWhenHintHasNonMediaExtension(t *testing.T) {
	got := filenameFromURL(
		"https://video.twimg.com/amplify_video/2073131761341460480/vid/avc1/720x1280/gAEB-Ux6zk1_4Z-t.mp4?tag=14",
		"正文里可能带短链 https://t.co/example",
		"",
		180,
	)
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestFilenameFromURLUsesContentTypeWhenURLHasNoExtension(t *testing.T) {
	got := filenameFromURL("https://pbs.twimg.com/media/example?format=small", "photo", "video/mp4; charset=utf-8", 180)
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestUniquePathKeepsConflictSuffixWithinMaxLength(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, filenameFromURL("https://example.com/media.jpg", strings.Repeat("a", 60), "", 24))
	if err := os.WriteFile(first, []byte("exists"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	got, err := uniquePath(first, 24)
	if err != nil {
		t.Fatalf("unique path: %v", err)
	}
	base := filepath.Base(got)
	if len(base) > 24 {
		t.Fatalf("filename length = %d, want <= 24: %q", len(base), base)
	}
	if !strings.Contains(base, "(1)") {
		t.Fatalf("filename %q does not contain conflict suffix", base)
	}
	if filepath.Ext(base) != ".jpg" {
		t.Fatalf("extension = %q, want .jpg: %q", filepath.Ext(base), base)
	}
}

func TestDownloadWithOptionsSkipsExistingLocalFile(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("new media"))
	}))
	defer server.Close()

	dir := t.TempDir()
	existing := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(existing, []byte("existing media"), 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !result.Skipped {
		t.Fatal("skipped = false, want true")
	}
	if result.Path != existing {
		t.Fatalf("path = %q, want %q", result.Path, existing)
	}
	if result.Bytes != int64(len("existing media")) {
		t.Fatalf("bytes = %d, want existing file size", result.Bytes)
	}
	if requests.Load() != 0 {
		t.Fatalf("HTTP requests = %d, want 0", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "media(1).mp4")); !os.IsNotExist(err) {
		t.Fatalf("duplicate file was created: %v", err)
	}
}
