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
	"syscall"
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
	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, hint, Options{MaxFilenameLength: 240})
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
	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingFileOwner: owner})
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
	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingFileOwner: owner})
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
	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingFileOwner: owner})
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
	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{ExistingFileOwner: owner})
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
	result, err := New().DownloadWithOptions(context.Background(), server.URL+"/movie.mp4", dir, "media", Options{})
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
