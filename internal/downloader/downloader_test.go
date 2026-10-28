package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
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

func TestOpenRejectsResponsesWithoutCompleteMedia(t *testing.T) {
	for _, status := range []int{209, http.StatusNotModified, http.StatusNoContent, http.StatusPartialContent} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()

			_, err := newTestDownloader().Open(context.Background(), server.URL+"/media.mp4", Options{})
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("Open error = %v, want HTTPStatusError", err)
			}
			if statusErr.StatusCode != status {
				t.Fatalf("status = %d, want %d", statusErr.StatusCode, status)
			}
		})
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

func TestFilenameNormalizesJpegHintWithoutDoubleExtension(t *testing.T) {
	got := Filename("https://example.com/photo", "photo.jpeg", "", 120)
	if got != "photo.jpg" {
		t.Fatalf("filename = %q, want %q", got, "photo.jpg")
	}
	if filepath.Ext(got) != ".jpg" {
		t.Fatalf("extension = %q, want .jpg: %q", filepath.Ext(got), got)
	}
}

func TestFilenameNormalizesUppercaseExtensionHint(t *testing.T) {
	got := Filename("https://example.com/video", "photo.MP4", "", 120)
	if got != "photo.mp4" {
		t.Fatalf("filename = %q, want %q", got, "photo.mp4")
	}
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestFilenameKeepsNormalJpgHintUnchanged(t *testing.T) {
	got := Filename("https://example.com/photo", "photo.jpg", "", 120)
	if got != "photo.jpg" {
		t.Fatalf("filename = %q, want %q", got, "photo.jpg")
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

	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{})
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

	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{})
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
			result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+path, dir, "media", Options{})
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

func TestDownloadWithMaxLengthFilenameSucceeds(t *testing.T) {
	body := []byte("max length media")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	// 240 字节的最终文件名接近 NAME_MAX；临时文件名必须不随其增长，否则触发
	// ENAMETOOLONG。
	hint := strings.Repeat("a", 300)
	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, hint, Options{MaxFilenameLength: 240})
	if err != nil {
		t.Fatalf("download with max-length filename: %v", err)
	}
	if result.Skipped {
		t.Fatal("skipped = true, want download")
	}
	if got := filepath.Base(result.Path); len(got) > 240 {
		t.Fatalf("published filename length = %d, want <= 240: %q", len(got), got)
	}
	if got, err := os.ReadFile(result.Path); err != nil || string(got) != string(body) {
		t.Fatalf("file content = %q, want %q", got, body)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.part"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}

func TestDownloadWithOptionsDoesNotSkipCompleteFileOwnedByOtherDownload(t *testing.T) {
	body := []byte("distinct media body")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	first := filepath.Join(dir, "media.mp4")
	// 磁盘上已有一份同名的完整文件，但它属于"其他推文"（ExistingFileOwner 返回 false）。
	if err := os.WriteFile(first, body, 0o644); err != nil {
		t.Fatalf("seed first file: %v", err)
	}

	owner := func(string) bool { return false }
	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingFileOwner: owner})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.Skipped {
		t.Fatal("skipped = true, want false when the file belongs to another download")
	}
	wantPath := filepath.Join(dir, "media(1).mp4")
	if result.Path != wantPath {
		t.Fatalf("path = %q, want %q", result.Path, wantPath)
	}
	if got, err := os.ReadFile(result.Path); err != nil || string(got) != string(body) {
		t.Fatalf("result content = %q, want %q", got, body)
	}
	if got, err := os.ReadFile(first); err != nil || string(got) != string(body) {
		t.Fatalf("first file overwritten or lost: %q, %v", got, err)
	}
}

func TestDownloadWithOptionsDoesNotOverwritePartialFileOwnedByOtherDownload(t *testing.T) {
	body := []byte("full media body longer than the stale partial")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	partial := filepath.Join(dir, "media.mp4")
	// 残缺文件同样可能属于其他推文（其下载尚未完成）：不得覆盖，改写后缀路径。
	if err := os.WriteFile(partial, []byte("part"), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	owner := func(string) bool { return false }
	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingFileOwner: owner})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.Skipped {
		t.Fatal("skipped = true, want false when the file belongs to another download")
	}
	wantPath := filepath.Join(dir, "media(1).mp4")
	if result.Path != wantPath {
		t.Fatalf("path = %q, want %q", result.Path, wantPath)
	}
	if got, err := os.ReadFile(partial); err != nil || string(got) != "part" {
		t.Fatalf("partial file overwritten: %q, %v", got, err)
	}
	if got, err := os.ReadFile(result.Path); err != nil || string(got) != string(body) {
		t.Fatalf("result content = %q, want %q", got, body)
	}
}

func TestDownloadWithOptionsSkipsOwnedCompleteFile(t *testing.T) {
	var requests atomic.Int64
	body := []byte("media")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	existing := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(existing, body, 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	owner := func(path string) bool { return path == existing }
	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingFileOwner: owner})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !result.Skipped {
		t.Fatal("skipped = false, want true when the file is owned by this download")
	}
	if result.Path != existing {
		t.Fatalf("path = %q, want %q", result.Path, existing)
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (Open to verify size)", requests.Load())
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != string(body) {
		t.Fatalf("existing file overwritten: %q, %v", got, err)
	}
}

func TestDownloadWithOptionsReplacesOwnedPartialFile(t *testing.T) {
	body := []byte("complete body")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	existing := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(existing, []byte("part"), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	// 文件属于本次下载（崩溃残留）：保持原有行为，原地覆盖重下。
	owner := func(path string) bool { return path == existing }
	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingFileOwner: owner})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.Skipped {
		t.Fatal("skipped = true, want false for an owned partial file")
	}
	if result.Path != existing {
		t.Fatalf("path = %q, want %q (owned partial should be replaced in place)", result.Path, existing)
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != string(body) {
		t.Fatalf("file content = %q, want %q", got, body)
	}
}

func TestOpenIncludesHTTPErrorPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("{\n  \"error_response\": \"Dmcaed\"\n}"))
	}))
	defer server.Close()

	_, err := newTestDownloader().Open(context.Background(), server.URL+"/media.mp4", Options{})
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

func TestPublishFileLinkSucceeds(t *testing.T) {
	dir := t.TempDir()
	tempPath := filepath.Join(dir, ".xdl-test.part")
	candidate := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(tempPath, []byte("media data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	handled, err := publishFile(tempPath, candidate, 10)
	if err != nil {
		t.Fatalf("publishFile: %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true")
	}
	if got, err := os.ReadFile(candidate); err != nil || string(got) != "media data" {
		t.Fatalf("candidate content = %q, %v; want %q", got, err, "media data")
	}
	if _, statErr := os.Lstat(tempPath); !os.IsNotExist(statErr) {
		t.Fatalf("temp file not removed after link: %v", statErr)
	}
}

func TestPublishFileExistingCandidateIsConflict(t *testing.T) {
	dir := t.TempDir()
	tempPath := filepath.Join(dir, ".xdl-test.part")
	candidate := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(tempPath, []byte("media data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("other data"), 0o644); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	handled, err := publishFile(tempPath, candidate, 10)
	if err != nil {
		t.Fatalf("publishFile: %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false (existing candidate is a conflict)")
	}
	if got, err := os.ReadFile(candidate); err != nil || string(got) != "other data" {
		t.Fatalf("candidate overwritten: %q, %v", got, err)
	}
	if _, statErr := os.Lstat(tempPath); statErr != nil {
		t.Fatalf("temp file missing after conflict: %v", statErr)
	}
}

func TestPublishFileFallsBackToRenameWhenHardLinksUnsupported(t *testing.T) {
	origLink := linkFile
	defer func() { linkFile = origLink }()

	dir := t.TempDir()
	tempPath := filepath.Join(dir, ".xdl-test.part")
	candidate := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(tempPath, []byte("media data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// 模拟不支持硬链接的文件系统：os.Link 一律返回 EPERM。
	linkFile = func(oldname, newname string) error { return syscall.EPERM }

	handled, err := publishFile(tempPath, candidate, 10)
	if err != nil {
		t.Fatalf("publishFile: %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true (rename fallback should publish)")
	}
	if got, err := os.ReadFile(candidate); err != nil || string(got) != "media data" {
		t.Fatalf("candidate content = %q, %v; want %q", got, err, "media data")
	}
	if _, statErr := os.Lstat(tempPath); !os.IsNotExist(statErr) {
		t.Fatalf("temp file not removed after rename: %v", statErr)
	}
}

func TestPublishFileTreatsExistingCandidateAsConflictInFallback(t *testing.T) {
	origLink := linkFile
	defer func() { linkFile = origLink }()

	dir := t.TempDir()
	tempPath := filepath.Join(dir, ".xdl-test.part")
	candidate := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(tempPath, []byte("media data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("other data"), 0o644); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	linkFile = func(oldname, newname string) error { return syscall.EPERM }

	handled, err := publishFile(tempPath, candidate, 10)
	if err != nil {
		t.Fatalf("publishFile: %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false (existing candidate is a conflict)")
	}
	// 已存在的候选文件不得被覆盖，临时文件必须保留供下一编号重试。
	if got, err := os.ReadFile(candidate); err != nil || string(got) != "other data" {
		t.Fatalf("candidate overwritten: %q, %v", got, err)
	}
	if _, statErr := os.Lstat(tempPath); statErr != nil {
		t.Fatalf("temp file missing after conflict: %v", statErr)
	}
}

func TestPublishFileReturnsUnrelatedLinkErrors(t *testing.T) {
	origLink := linkFile
	defer func() { linkFile = origLink }()

	dir := t.TempDir()
	tempPath := filepath.Join(dir, ".xdl-test.part")
	candidate := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(tempPath, []byte("media data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// 与硬链接支持无关的错误必须原样返回，不进入退化分支。
	linkFile = func(oldname, newname string) error { return os.ErrNotExist }

	handled, err := publishFile(tempPath, candidate, 10)
	if err == nil {
		t.Fatal("err = nil, want unrelated link error to be returned")
	}
	if handled {
		t.Fatal("handled = true, want false for an error")
	}
}

func TestDownloadWithOptionsFallsBackToRenameWhenHardLinksUnsupported(t *testing.T) {
	origLink := linkFile
	defer func() { linkFile = origLink }()
	// 端到端回归：模拟 exFAT/FAT32 等不支持硬链接的文件系统。
	linkFile = func(oldname, newname string) error { return syscall.EPERM }

	body := []byte("media body on exfat")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.Skipped {
		t.Fatal("skipped = true, want false")
	}
	wantPath := filepath.Join(dir, "media.mp4")
	if result.Path != wantPath {
		t.Fatalf("path = %q, want %q", result.Path, wantPath)
	}
	if got, err := os.ReadFile(result.Path); err != nil || string(got) != string(body) {
		t.Fatalf("content = %q, %v; want %q", got, err, body)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.part"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}

func TestOpenDirectConnectionUsesGuardedTransport(t *testing.T) {
	_, err := newDownloaderAllowingHosts(func(string) bool { return true }).Open(context.Background(), "http://169.254.169.254/media.mp4", Options{})
	if !httpx.IsBlockedTargetError(err) {
		t.Fatalf("Open(link-local) error = %v, want blocked target error", err)
	}
}

func TestOpenRejectsRedirectOutsideTwimg(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/media.mp4", http.StatusFound)
	}))
	defer server.Close()

	_, err := newTestDownloader().Open(context.Background(), server.URL+"/media.mp4", Options{})
	if err == nil || !strings.Contains(err.Error(), "非 twimg.com") {
		t.Fatalf("Open(redirect) error = %v, want non-twimg redirect rejection", err)
	}
}

func TestOpenStopsTwimgRedirectLoop(t *testing.T) {
	requests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 15 {
			w.WriteHeader(http.StatusLoopDetected)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("http://pbs.twimg.com/%d.jpg", requests), http.StatusFound)
	}))
	defer proxy.Close()
	_, err := New().Open(context.Background(), "http://pbs.twimg.com/start.jpg", Options{ProxyURL: proxy.URL})
	if err == nil || requests > 10 {
		t.Fatalf("redirect loop was not stopped after ten requests: requests=%d err=%v", requests, err)
	}
}

func TestRemoveStaleTempFilesKeepsFreshAndRegularFiles(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "users", "Alice(alice)")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, age time.Duration) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-age)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	stalePart := filepath.Join(nested, ".xdl-123.part")
	stalePreview := filepath.Join(nested, ".preview-456.jpg")
	freshPart := filepath.Join(nested, ".xdl-789.part") // 可能是正在进行的下载
	media := filepath.Join(nested, "photo.jpg")
	lookalike := filepath.Join(nested, "xdl-notes.part")
	write(stalePart, 3*time.Hour)
	write(stalePreview, 3*time.Hour)
	write(freshPart, time.Minute)
	write(media, 3*time.Hour)
	write(lookalike, 3*time.Hour)

	removed, err := RemoveStaleTempFiles(context.Background(), root, time.Hour)
	if err != nil {
		t.Fatalf("remove stale temp files: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for _, path := range []string{stalePart, stalePreview} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", filepath.Base(path))
		}
	}
	for _, path := range []string{freshPart, media, lookalike} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s should be kept: %v", filepath.Base(path), err)
		}
	}
}

// newTestDownloader 返回允许回环主机的下载器：httptest 的 URL 是 127.0.0.1，
// 而生产 New() 的初始 URL 白名单只允许 twimg.com（与重定向策略一致）。
func newTestDownloader() *Downloader {
	return newDownloaderAllowingHosts(func(host string) bool {
		lower := strings.ToLower(host)
		return isAllowedTwimgHost(host) || lower == "127.0.0.1" || lower == "localhost" || lower == "::1"
	})
}

// newDownloaderAllowingHosts 用自定义主机白名单构造下载器（委托给导出的测试入口）。
func newDownloaderAllowingHosts(allow func(string) bool) *Downloader {
	return NewWithAllowedHostForTest(allow)
}

// 初始 URL 必须和重定向一样经过 twimg 白名单：下载器是最后一道 SSRF 边界，
// 不允许调用方把请求发到任意主机。
func TestOpenRejectsInitialURLOutsideTwimg(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1:1/media.mp4",
		"https://evil.example/media.mp4",
		"http://pbs.twimg.com.evil.example/a.jpg",
		"http://notwimg.com/a.jpg",
	} {
		_, err := New().Open(context.Background(), rawURL, Options{})
		if err == nil || !strings.Contains(err.Error(), "非 twimg.com") {
			t.Fatalf("Open(%q) error = %v, want non-twimg rejection", rawURL, err)
		}
	}
}

func TestOpenRejectsNonHTTPInitialURL(t *testing.T) {
	for _, rawURL := range []string{"file:///etc/passwd", "gopher://pbs.twimg.com/x", "ftp://video.twimg.com/x.mp4"} {
		_, err := New().Open(context.Background(), rawURL, Options{})
		if err == nil || !strings.Contains(err.Error(), "拒绝非 HTTP") {
			t.Fatalf("Open(%q) error = %v, want non-HTTP rejection", rawURL, err)
		}
	}
}

// 只有 200 才算成功：204/206/304 与任何非标准 2xx（如 209）都不能发布 0 字节文件。
func TestOpenAcceptsOnlyStatusOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	response, err := newTestDownloader().Open(context.Background(), server.URL+"/movie.mp4", Options{})
	if err != nil {
		t.Fatalf("Open(200) error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}

// 未知媒体类型不再退化成 .bin：直接失败，且不落盘任何文件。
func TestDownloadRejectsUnknownMediaType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("binary payload"))
	}))
	defer server.Close()

	dir := t.TempDir()
	_, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/media", dir, "photo", Options{})
	if !errors.Is(err, ErrUnknownMediaType) {
		t.Fatalf("download error = %v, want ErrUnknownMediaType", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("unknown media type wrote files: %v", entries)
	}
}

func TestFilenameReturnsEmptyForUnknownMediaType(t *testing.T) {
	cases := []struct{ url, hint, contentType string }{
		{"https://pbs.twimg.com/media/abc", "photo", "application/octet-stream"},
		{"https://pbs.twimg.com/media/abc", "", ""},
		{"https://pbs.twimg.com/media/abc", "note", "text/plain; charset=utf-8"},
	}
	for _, tc := range cases {
		if got := Filename(tc.url, tc.hint, tc.contentType, 120); got != "" {
			t.Fatalf("Filename(%q,%q,%q) = %q, want empty (never .bin)", tc.url, tc.hint, tc.contentType, got)
		}
	}
	// 文件名提示里的媒体扩展名仍是合法兜底（海报/卡片 URL 常无扩展名）。
	if got := Filename("https://pbs.twimg.com/media/abc", "photo.jpeg", "application/octet-stream", 120); got != "photo.jpg" {
		t.Fatalf("hint fallback filename = %q, want photo.jpg", got)
	}
}

// 跳过已存在文件时必须返回该文件的内容哈希（跳过路径历史上写空串，只能依赖 storage
// upsert 的 CASE 兜底）。
func TestDownloadSkippedFileReturnsContentHash(t *testing.T) {
	body := []byte("existing complete media body")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	existing := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(existing, body, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !result.Skipped {
		t.Fatal("skipped = false, want true for complete file")
	}
	if result.ContentHash != want {
		t.Fatalf("content hash = %q, want sha256 of existing file %q", result.ContentHash, want)
	}

	// 调用方已知哈希时（jobs 可从 downloads.content_hash 提供）直接复用，不再读盘。
	known := strings.Repeat("a", 64)
	result, err = newTestDownloader().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingContentHash: known})
	if err != nil {
		t.Fatalf("download with known hash: %v", err)
	}
	if result.ContentHash != known {
		t.Fatalf("content hash = %q, want provided %q", result.ContentHash, known)
	}
}

// ctx 取消后必须清理临时文件，不能留下 .part。
func TestDownloadCancelCleansTempFile(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer server.Close()

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newTestDownloader().DownloadWithOptions(ctx, server.URL+"/media.mp4", dir, "media", Options{})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download request never reached server")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("download error = nil, want cancellation error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download did not stop after cancel")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.part"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp files after cancel: %v", matches)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
		t.Fatalf("unexpected files after cancel: %v", entries)
	}
}

func TestSanitizeFilenameHandlesControlAndWindowsNames(t *testing.T) {
	cases := map[string]string{
		"CON":             "_CON",
		"nul.mp4":         "_nul.mp4",
		"COM1":            "_COM1",
		"lpt9.txt":        "_lpt9.txt",
		"CONIN$":          "_CONIN$",
		"conout$.jpg":     "_conout$.jpg",
		"a\x00b\x01c\x7f": "abc",
		"line\nbreak":     "linebreak",
		"trailing...  ":   "trailing",
		".hidden":         "hidden",
		"..":              "",
		"   ":             "",
		"正常标题 emoji 🚀":    "正常标题 emoji 🚀",
	}
	for input, want := range cases {
		if got := sanitizeFilename(input); got != want {
			t.Fatalf("sanitizeFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

// 文件名长度边界必须来自 internal/config，避免 config 与 downloader 两套常量漂移。
func TestNormalizedMaxFilenameLengthUsesConfigBounds(t *testing.T) {
	if got := normalizedMaxFilenameLength(0); got != config.DefaultMaxFilenameLength {
		t.Fatalf("normalizedMaxFilenameLength(0) = %d, want %d", got, config.DefaultMaxFilenameLength)
	}
	if got := normalizedMaxFilenameLength(1); got != config.MinFilenameLength {
		t.Fatalf("normalizedMaxFilenameLength(1) = %d, want %d", got, config.MinFilenameLength)
	}
	if got := normalizedMaxFilenameLength(1 << 20); got != config.MaxFilenameLength {
		t.Fatalf("normalizedMaxFilenameLength(1<<20) = %d, want %d", got, config.MaxFilenameLength)
	}
	if got := normalizedMaxFilenameLength(180); got != 180 {
		t.Fatalf("normalizedMaxFilenameLength(180) = %d, want 180", got)
	}
}

// 阈值小于 2 倍下载超时时必须被抬高，否则会删掉仍在写入的大文件下载。
func TestRemoveStaleTempFilesNeverUsesShortThreshold(t *testing.T) {
	root := t.TempDir()
	writeAged := func(name string, age time.Duration) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-age)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return path
	}
	inProgress := writeAged(".xdl-inprogress.part", 5*time.Minute)
	removed, err := RemoveStaleTempFiles(context.Background(), root, time.Minute)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 (short threshold must be raised to 2× timeout)", removed)
	}
	if _, err := os.Stat(inProgress); err != nil {
		t.Fatalf("in-progress temp file removed: %v", err)
	}
	// 抬高后的阈值仍然生效：30 分钟前的残留会被清理。
	stale := writeAged(".xdl-stale.part", 30*time.Minute)
	removed, err = RemoveStaleTempFiles(context.Background(), root, time.Minute)
	if err != nil {
		t.Fatalf("remove stale: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file not removed: %v", err)
	}
}

// WalkDir 快照过期（期间有写入/被替换）时不能删除。
func TestStaleTempFileReadyDetectsActiveWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".xdl-active.part")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-time.Hour)
	snapshot, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !staleTempFileReady(path, snapshot, cutoff) {
		t.Fatal("idle stale temp file should be removable")
	}
	// 快照之后发生写入：大小变化 → 不可删。
	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if staleTempFileReady(path, snapshot, cutoff) {
		t.Fatal("temp file changed after snapshot must not be removed")
	}
	// 刷新快照后 mtime 变新（仍在写入）→ 不可删。
	fresh, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if staleTempFileReady(path, fresh, cutoff) {
		t.Fatal("recently written temp file must not be removed")
	}
}

// 生产构造函数（及测试入口传 nil）的白名单必须不可绕过：初始 URL 与重定向都只允许
// twimg.com。测试入口只影响"允许哪些主机"，不会把校验整个关掉。
func TestAllowedHostInjectionKeepsProductionWhitelist(t *testing.T) {
	for name, d := range map[string]*Downloader{
		"New":                     New(),
		"NewWithTimeout":          NewWithTimeout(time.Second),
		"TestConstructorNilAllow": NewWithAllowedHostForTest(nil),
	} {
		_, err := d.Open(context.Background(), "http://127.0.0.1:1/media.mp4", Options{})
		if err == nil || !strings.Contains(err.Error(), "非 twimg.com") {
			t.Fatalf("%s: Open(loopback) error = %v, want non-twimg rejection", name, err)
		}
	}
	// 注入回环主机后：请求能通过白名单（随后是连接失败，而不是白名单拒绝）。
	allowed := NewWithAllowedHostForTest(func(host string) bool {
		return host == "127.0.0.1" || isAllowedTwimgHost(host)
	})
	_, err := allowed.Open(context.Background(), "http://127.0.0.1:1/media.mp4", Options{})
	if err == nil {
		t.Fatal("Open(loopback:1) error = nil, want dial error")
	}
	if strings.Contains(err.Error(), "非 twimg.com") {
		t.Fatalf("injected host still rejected by whitelist: %v", err)
	}
	// 未注入的 host 仍被拒绝。
	if _, err := allowed.Open(context.Background(), "https://evil.example/media.mp4", Options{}); err == nil || !strings.Contains(err.Error(), "非 twimg.com") {
		t.Fatalf("Open(evil.example) error = %v, want non-twimg rejection", err)
	}
}
