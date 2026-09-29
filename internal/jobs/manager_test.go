package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
	_ "modernc.org/sqlite"
)

func TestCancelJobStopsActiveDownload(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test response writer does not support flushing")
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir:     t.TempDir(),
		MaxConcurrency:  1,
		AutoRetryFailed: true,
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, server.URL+"/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	managerCtx, stop := context.WithCancel(context.Background())
	defer stop()
	manager := NewManager(store, parser.NewService(), NewEventBus())
	manager.Start(managerCtx)
	manager.Notify()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("download did not start")
	}
	if _, err := manager.CancelJob(ctx, job.ID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("active HTTP request was not canceled")
	}
	eventually(t, func() bool {
		got, err := store.GetJob(ctx, job.ID)
		return err == nil && got.Status == storage.JobCanceled
	})
}

func TestShutdownLeavesJobRequeueable(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test response writer does not support flushing")
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		close(started)
		<-r.Context().Done() // 阻塞直到下载被取消
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir:     t.TempDir(),
		MaxConcurrency:  1,
		AutoRetryFailed: true,
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, server.URL+"/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	managerCtx, stop := context.WithCancel(context.Background())
	manager := NewManager(store, parser.NewService(), NewEventBus())
	manager.Start(managerCtx)
	manager.Notify()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("download did not start")
	}
	// 模拟 SIGTERM 关停：取消 manager context（非用户取消任务）。
	stop()
	manager.Stop()

	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status == storage.JobCanceled {
		t.Fatalf("status = canceled; graceful shutdown must leave the job requeueable (Downloading/Resolving), not canceled — otherwise graceful stop is less recoverable than a hard kill")
	}
}

func TestAvailableSlotsIgnoresConfiguredConcurrency(t *testing.T) {
	manager := NewManager(nil, parser.NewService(), NewEventBus())
	if got := manager.availableSlots(8); got != 1 {
		t.Fatalf("availableSlots(8) = %d, want serial slot 1", got)
	}

	manager.mu.Lock()
	manager.active[1] = func() {}
	manager.mu.Unlock()

	if got := manager.availableSlots(8); got != 0 {
		t.Fatalf("availableSlots(8) with active job = %d, want 0", got)
	}
}

func TestTweetFilenameUsesConfiguredNamingMode(t *testing.T) {
	tweet := parser.TweetData{
		ID:   "12345",
		Text: "hello world",
		Author: parser.Author{
			ID:         "44196397",
			ScreenName: "openai",
		},
		Media: []parser.Media{
			{ID: "1", Type: parser.MediaPhoto},
			{ID: "2", Type: parser.MediaPhoto},
		},
	}

	tests := []struct {
		name string
		cfg  config.AppConfig
		want string
	}{
		{
			name: "tweet only",
			cfg:  config.AppConfig{FileNamingMode: config.FileNamingTweetText},
			want: "hello world-01",
		},
		{
			name: "user and tweet",
			cfg:  config.AppConfig{FileNamingMode: config.FileNamingUserTweet},
			want: "openai-44196397-hello world-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tweetFilename(tt.cfg, tweet, 0); got != tt.want {
				t.Fatalf("tweetFilename() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTweetFilenameFallsBackToIDWhenTextIsEmpty(t *testing.T) {
	tweet := parser.TweetData{ID: "12345"}
	if got := tweetFilename(config.AppConfig{FileNamingMode: config.FileNamingTweetText}, tweet, 0); got != "12345" {
		t.Fatalf("tweetFilename() = %q, want 12345", got)
	}
}

func TestTweetFilenameRemovesShortLinks(t *testing.T) {
	tweet := parser.TweetData{
		ID:   "12345",
		Text: "hello https://t.co/0ERpJ8OLAP world",
		Author: parser.Author{
			ID:         "44196397",
			ScreenName: "openai",
		},
	}

	tests := []struct {
		name string
		cfg  config.AppConfig
		want string
	}{
		{
			name: "tweet only",
			cfg:  config.AppConfig{FileNamingMode: config.FileNamingTweetText},
			want: "hello world",
		},
		{
			name: "user and tweet",
			cfg:  config.AppConfig{FileNamingMode: config.FileNamingUserTweet},
			want: "openai-44196397-hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tweetFilename(tt.cfg, tweet, 0); got != tt.want {
				t.Fatalf("tweetFilename() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTweetFilenameFallsBackToIDWhenOnlyShortLinksRemain(t *testing.T) {
	tweet := parser.TweetData{ID: "12345", Text: "https://t.co/0ERpJ8OLAP"}
	if got := tweetFilename(config.AppConfig{FileNamingMode: config.FileNamingTweetText}, tweet, 0); got != "12345" {
		t.Fatalf("tweetFilename() = %q, want 12345", got)
	}
}

func TestTweetFilenameSkipsEmptyUserParts(t *testing.T) {
	tweet := parser.TweetData{
		ID:   "12345",
		Text: "hello world",
		Author: parser.Author{
			ID: "44196397",
		},
	}
	if got := tweetFilename(config.AppConfig{FileNamingMode: config.FileNamingUserTweet}, tweet, 0); got != "44196397-hello world" {
		t.Fatalf("tweetFilename() = %q, want 44196397-hello world", got)
	}
}

func TestDownloadMediaSkipsExistingTweetMedia(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("media"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, server.URL+"/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.CreateDownload(ctx, storage.DownloadRecord{
		JobID:    job.ID,
		TweetID:  "tweet-1",
		MediaURL: server.URL + "/media.mp4",
		FilePath: filepath.Join(root, "existing.mp4"),
		Bytes:    10,
	}); err != nil {
		t.Fatalf("seed download: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing.mp4"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	result, err := manager.downloadMedia(ctx, ctx, job, config.AppConfig{DownloadDir: root}, server.URL+"/media.mp4", "tweet-1", "", "media", false, time.Time{})
	if err != nil {
		t.Fatalf("download media: %v", err)
	}
	if !result.skipped {
		t.Fatal("skipped = false, want true")
	}
	if requests.Load() != 0 {
		t.Fatalf("HTTP requests = %d, want 0", requests.Load())
	}
}

func TestDownloadMediaRedownloadsStaleTweetMediaRecord(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("media"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, server.URL+"/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	stalePath := filepath.Join(root, "missing.mp4")
	if _, err := store.CreateDownload(ctx, storage.DownloadRecord{
		JobID:    job.ID,
		TweetID:  "tweet-1",
		MediaURL: server.URL + "/media.mp4",
		FilePath: stalePath,
		Bytes:    10,
	}); err != nil {
		t.Fatalf("seed stale download: %v", err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	result, err := manager.downloadMedia(ctx, ctx, job, config.AppConfig{DownloadDir: root}, server.URL+"/media.mp4", "tweet-1", root, "media", false, time.Time{})
	if err != nil {
		t.Fatalf("download media: %v", err)
	}
	if result.skipped {
		t.Fatal("skipped = true, want false")
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1", requests.Load())
	}
	record, err := store.GetDownloadByTweetMedia(ctx, "tweet-1", server.URL+"/media.mp4")
	if err != nil {
		t.Fatalf("get updated download: %v", err)
	}
	if record == nil || record.FilePath == stalePath {
		t.Fatalf("download record was not refreshed: %+v", record)
	}
	if _, err := os.Stat(record.FilePath); err != nil {
		t.Fatalf("updated file does not exist: %v", err)
	}
}

func TestDownloadMediaSuffixedPathWhenAnotherTweetOwnsFilename(t *testing.T) {
	var requests atomic.Int64
	bodies := map[string][]byte{
		"/one.mp4": []byte("first tweet media"),
		"/two.mp4": []byte("second tweet media"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(bodies[r.URL.Path])
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, server.URL+"/one.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	firstURL := server.URL + "/one.mp4"
	secondURL := server.URL + "/two.mp4"

	// 第一条推文：文件名落到基础路径。
	first, err := manager.downloadMedia(ctx, ctx, job, config.AppConfig{DownloadDir: root}, firstURL, "tweet-1", "", "identical text", false, time.Time{})
	if err != nil {
		t.Fatalf("download first media: %v", err)
	}
	if first.skipped {
		t.Fatal("first download skipped = true, want false")
	}

	// 第二条推文文本与第一条完全相同 → 生成同名文件。第二条媒体既不能被跳过（否则静默
	// 丢失），也不能覆盖第一条的文件（否则破坏第一条推文的下载记录指向的文件），必须写
	// 入带编号后缀的路径并各自记录一条下载。
	second, err := manager.downloadMedia(ctx, ctx, job, config.AppConfig{DownloadDir: root}, secondURL, "tweet-2", "", "identical text", false, time.Time{})
	if err != nil {
		t.Fatalf("download second media: %v", err)
	}
	if second.skipped {
		t.Fatal("second download skipped = true, want false for a colliding filename")
	}

	firstPath := filepath.Join(root, "identical text.mp4")
	secondPath := filepath.Join(root, "identical text(1).mp4")
	if got, err := os.ReadFile(firstPath); err != nil || string(got) != string(bodies["/one.mp4"]) {
		t.Fatalf("first tweet media missing or overwritten: %q, %v", got, err)
	}
	if got, err := os.ReadFile(secondPath); err != nil || string(got) != string(bodies["/two.mp4"]) {
		t.Fatalf("second tweet media not at suffixed path: %q, %v", got, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("HTTP requests = %d, want 2 (both tweets downloaded)", requests.Load())
	}
	firstRecord, err := store.GetDownloadByTweetMedia(ctx, "tweet-1", firstURL)
	if err != nil || firstRecord == nil || firstRecord.FilePath != firstPath {
		t.Fatalf("first download record = %+v, err = %v", firstRecord, err)
	}
	secondRecord, err := store.GetDownloadByTweetMedia(ctx, "tweet-2", secondURL)
	if err != nil || secondRecord == nil || secondRecord.FilePath != secondPath {
		t.Fatalf("second download record = %+v, err = %v", secondRecord, err)
	}
}

func TestDownloadMediaCachesPermanentlyUnavailableMedia(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error_code":2,"error_response":"Dmcaed"}`))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, server.URL+"/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root}

	first, err := manager.downloadMedia(ctx, ctx, job, cfg, server.URL+"/media.mp4", "tweet-1", root, "media", false, time.Time{})
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	if !first.skipped || !first.unavailable {
		t.Fatalf("first result = %+v, want skipped permanently unavailable", first)
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests after first download = %d, want 1", requests.Load())
	}

	second, err := manager.downloadMedia(ctx, ctx, job, cfg, server.URL+"/media.mp4", "tweet-1", root, "media", false, time.Time{})
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	if !second.skipped || !second.unavailable {
		t.Fatalf("second result = %+v, want cached permanently unavailable", second)
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests after cached download = %d, want 1", requests.Load())
	}

	failed, err := store.ListFailedMediaForJobs(ctx, []int64{job.ID})
	if err != nil {
		t.Fatalf("list failed media: %v", err)
	}
	if len(failed) != 1 || !strings.Contains(failed[0].Error, "Dmcaed") {
		t.Fatalf("failed media = %#v, want one DMCA record", failed)
	}
}

func TestDownloadMediaDoesNotCacheGenericForbidden(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, server.URL+"/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root}

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := manager.downloadMedia(ctx, ctx, job, cfg, server.URL+"/media.mp4", "tweet-1", root, "media", false, time.Time{}); err == nil {
			t.Fatal("generic HTTP 403 was treated as permanently unavailable")
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("HTTP requests = %d, want 2", requests.Load())
	}
}

func TestNewestTweetIDIgnoresPinnedOrdering(t *testing.T) {
	// timeline 按时间倒序，但置顶推文（ID 较旧）排在 index 0；游标必须取数值最大的，
	// 否则下次增量归档会在首页对置顶推文精确命中而早停，漏掉更新的推文。
	tweets := []parser.TweetData{
		{ID: "100"}, // 置顶，较旧
		{ID: "300"}, // 最新
		{ID: "200"},
	}
	if got := newestTweetID(tweets); got != "300" {
		t.Fatalf("newestTweetID = %q, want 300（不应取置顶的 tweets[0]）", got)
	}
}

func TestNewestTweetIDPicksFirstWhenAlreadyDescending(t *testing.T) {
	tweets := []parser.TweetData{{ID: "300"}, {ID: "200"}, {ID: "100"}}
	if got := newestTweetID(tweets); got != "300" {
		t.Fatalf("newestTweetID = %q, want 300", got)
	}
}

// TestShouldRetryMediaErrorQueuesTransientForbidden：X 对过期的 video.twimg.com 签名例行
// 返回裸 403，那是瞬时状态，必须进失败队列等待重试（与 retryFailedTweets 的约定一致）；
// 只有 404/410/DMCA 这类永久失效才不重试。
func TestShouldRetryMediaErrorQueuesTransientForbidden(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare forbidden", err: &downloader.HTTPStatusError{StatusCode: http.StatusForbidden, Payload: "Forbidden"}, want: true},
		{name: "dmca forbidden", err: &downloader.HTTPStatusError{StatusCode: http.StatusForbidden, Payload: `{"error_response":"Dmcaed"}`}, want: false},
		{name: "not found", err: &downloader.HTTPStatusError{StatusCode: http.StatusNotFound}, want: false},
		{name: "gone", err: &downloader.HTTPStatusError{StatusCode: http.StatusGone}, want: false},
		{name: "too many requests", err: &downloader.HTTPStatusError{StatusCode: http.StatusTooManyRequests}, want: true},
		{name: "plain error", err: context.DeadlineExceeded, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryMediaError(tt.err); got != tt.want {
				t.Fatalf("shouldRetryMediaError() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPermanentMediaErrorRequiresTerminalStatusOrDMCA(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "dmca", err: &downloader.HTTPStatusError{StatusCode: http.StatusForbidden, Payload: `{"error_response":"Dmcaed"}`}, want: true},
		{name: "generic forbidden", err: &downloader.HTTPStatusError{StatusCode: http.StatusForbidden, Payload: "Forbidden"}, want: false},
		{name: "not found", err: &downloader.HTTPStatusError{StatusCode: http.StatusNotFound}, want: true},
		{name: "gone", err: &downloader.HTTPStatusError{StatusCode: http.StatusGone}, want: true},
		{name: "server error", err: &downloader.HTTPStatusError{StatusCode: http.StatusServiceUnavailable}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermanentlyUnavailableMediaError(tt.err); got != tt.want {
				t.Fatalf("isPermanentlyUnavailableMediaError() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCompleteArchivePersistsUserIssueDetails(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, storage.JobKindFollowing, "owner", "following")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	manager.completeArchive(ctx, job, archiveStats{
		Users:  1,
		Failed: 1,
		Issues: []string{"读取 @alice 的媒体时间线失败: timeout"},
	}, 0)

	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != storage.JobCompletedWithErrors {
		t.Fatalf("status = %s, want %s", got.Status, storage.JobCompletedWithErrors)
	}
	if !strings.Contains(got.Error, "@alice") || !strings.Contains(got.Error, "timeout") {
		t.Fatalf("error = %q, want user-level issue detail", got.Error)
	}
}

func TestArchiveIssueSummaryAggregation(t *testing.T) {
	tests := []struct {
		name   string
		issues []string
		want   []string
	}{
		{
			name:   "empty",
			issues: nil,
			want:   nil,
		},
		{
			name:   "single issue",
			issues: []string{"读取 @alice 的媒体时间线失败: timeout"},
			want:   []string{"读取 @alice 的媒体时间线失败: timeout"},
		},
		{
			name: "multiple identical rate limits aggregated",
			issues: []string{
				"读取 @user1 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
				"读取 @user2 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
				"读取 @user3 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
				"读取 @user4 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
				"读取 @user5 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
				"读取 @user6 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
				"读取 @user7 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
			},
			want: []string{
				"读取媒体时间线失败: X 客户端暂时全部限流，请稍后重试 (共 7 个账号: @user1、@user2、@user3、@user4、@user5、@user6、@user7)",
			},
		},
		{
			name: "distinct errors kept in separate lines",
			issues: []string{
				"读取 @user1 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
				"读取 @user2 的媒体时间线失败: X 客户端暂时全部限流，请稍后重试",
				"读取 @user3 的媒体时间线失败: 404 Not Found",
			},
			want: []string{
				"读取媒体时间线失败: X 客户端暂时全部限流，请稍后重试 (共 2 个账号: @user1、@user2)",
				"读取媒体时间线失败 (@user3): 404 Not Found",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := archiveIssueSummary(tt.issues)
			if len(tt.want) == 0 {
				if got != "" {
					t.Fatalf("archiveIssueSummary() = %q, want empty", got)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Fatalf("archiveIssueSummary() = %q, want to contain %q", got, w)
				}
			}
		})
	}
}

func TestArchiveUserTasksPrioritizesPrimaryOnlyAndMissingMedia(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	cfg := config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	seedUser := xclient.User{ID: "existing", Name: "Existing", ScreenName: "existing"}
	if _, err := store.UpsertUser(ctx, storageUser(seedUser)); err != nil {
		t.Fatalf("upsert existing user: %v", err)
	}
	entity, _, err := manager.ensureUserEntity(ctx, cfg, seedUser)
	if err != nil {
		t.Fatalf("ensure existing entity: %v", err)
	}
	if err := store.UpdateUserEntityMediaCount(ctx, entity.ID, 90); err != nil {
		t.Fatalf("seed existing media count: %v", err)
	}

	users := []xclient.User{
		{ID: "public-small", Name: "Small", ScreenName: "small", MediaCount: 3},
		{ID: "protected", Name: "Protected", ScreenName: "protected", Protected: true, Following: true, MediaCount: 1},
		{ID: "public-large", Name: "Large", ScreenName: "large", MediaCount: 100},
		{ID: "existing", Name: "Existing", ScreenName: "existing", MediaCount: 95},
		{ID: "public-large", Name: "Large", ScreenName: "large", MediaCount: 100},
	}

	tasks, skipped, err := manager.archiveUserTasks(ctx, cfg, users)
	if err != nil {
		t.Fatalf("archive user tasks: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	got := make([]string, 0, len(tasks))
	for _, task := range tasks {
		got = append(got, task.user.ID)
	}
	want := []string{"protected", "public-large", "existing", "public-small"}
	if len(got) != len(want) {
		t.Fatalf("tasks = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("tasks = %#v, want %#v", got, want)
		}
	}
	if tasks[2].missingMedia != 5 {
		t.Fatalf("existing missing media = %d, want 5", tasks[2].missingMedia)
	}
}

func TestArchiveUsersDeduplicatesOnSerialPath(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	users := []xclient.User{
		{ID: "user-1", Name: "Alice", ScreenName: "alice", Protected: true},
		{ID: "user-1", Name: "Alice", ScreenName: "alice", Protected: true},
		{ID: "user-2", Name: "Bob", ScreenName: "bob", Protected: true},
	}

	stats, err := manager.archiveUsers(ctx, ctx, job, config.AppConfig{
		DownloadDir:         root,
		MaxFilenameLength:   config.DefaultMaxFilenameLength,
		FileNamingMode:      config.FileNamingTweetText,
		StorageType:         config.StorageLocal,
		AutoFollowProtected: false,
	}, nil, users, nil, 0.1, 0.9)
	if err != nil {
		t.Fatalf("archive users: %v", err)
	}
	if stats.Users != 2 || stats.Skipped != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want users=2 skipped=1 failed=0", stats)
	}
	for _, name := range []string{"Alice(alice)", "Bob(bob)"} {
		if _, err := os.Stat(filepath.Join(root, "users", name)); err != nil {
			t.Fatalf("user directory %q was not created: %v", name, err)
		}
	}
}

func TestShouldAbortArchiveUsersOnAllClientsRateLimited(t *testing.T) {
	if !shouldAbortArchiveUsers(xclient.ErrAllClientsRateLimited) {
		t.Fatal("all-clients rate limit should abort the current archive run")
	}
	if shouldAbortArchiveUsers(fmt.Errorf("读取失败")) {
		t.Fatal("ordinary user read errors should stay scoped to that user")
	}
}

func TestRefreshUserLinksSkipsLegacyFollowingLinks(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	cfg := config.AppConfig{DownloadDir: root}
	user := xclient.User{ID: "user-1", Name: "Alice", ScreenName: "alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	followingEntity, err := store.EnsureListEntity(ctx, "following:owner-1", filepath.Join(root, "following"), "owner-following")
	if err != nil {
		t.Fatalf("ensure following entity: %v", err)
	}
	listEntity, err := store.EnsureListEntity(ctx, "list-1", filepath.Join(root, "lists"), "news(1)")
	if err != nil {
		t.Fatalf("ensure list entity: %v", err)
	}
	if _, err := store.EnsureUserLink(ctx, user.ID, followingEntity.ID, "old-alice"); err != nil {
		t.Fatalf("ensure following user link: %v", err)
	}
	if _, err := store.EnsureUserLink(ctx, user.ID, listEntity.ID, "old-alice"); err != nil {
		t.Fatalf("ensure list user link: %v", err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	_, userDir, err := manager.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatalf("ensure user entity: %v", err)
	}

	name := safeName(user.Title())
	if _, err := os.Lstat(filepath.Join(root, "following", "owner-following", name)); !os.IsNotExist(err) {
		t.Fatalf("following link was created unexpectedly: %v", err)
	}
	listLink := filepath.Join(root, "lists", "news(1)", name)
	target, err := os.Readlink(listLink)
	if err != nil {
		t.Fatalf("read list link: %v", err)
	}
	if target != userDir {
		t.Fatalf("list link target = %q, want %q", target, userDir)
	}
}

func TestSyncLinkDoesNotRemoveRealDirectory(t *testing.T) {
	root := t.TempDir()
	linkPath := filepath.Join(root, "link")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(linkPath, 0o755); err != nil {
		t.Fatalf("seed existing directory: %v", err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("seed target directory: %v", err)
	}

	if err := syncLink(linkPath, target); err == nil {
		t.Fatal("syncLink succeeded for existing real directory, want error")
	}
	if info, err := os.Lstat(linkPath); err != nil || !info.IsDir() {
		t.Fatalf("existing directory was not preserved: info=%v err=%v", info, err)
	}
}

func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not met before deadline")
}

// TestDownloadMediaSkipsSameMediaWithSameNameAndSize 验证同一目录下同一份媒体（转推、
// 引用推文等不同 tweet_id 复用同一条媒体 URL）已有可用文件时直接跳过：
// 不重复下载、不创建硬链接、也不再产生第二份文件。
func TestDownloadMediaSkipsSameMediaWithSameNameAndSize(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("shared media"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength}
	mediaURL := server.URL + "/media.mp4"

	first, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "tweet-1", root, "same text", false, time.Time{})
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	if first.skipped {
		t.Fatal("first download skipped = true, want false")
	}
	second, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "tweet-2", root, "same text", false, time.Time{})
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	if !second.skipped {
		t.Fatal("second download skipped = false, want true (identical media already archived)")
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (same media must be downloaded once)", requests.Load())
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read archive dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive dir has %d entries, want 1", len(entries))
	}
	firstRecord, err := store.GetDownloadByTweetMedia(ctx, "tweet-1", mediaURL)
	if err != nil || firstRecord == nil {
		t.Fatalf("first record = %+v, err = %v", firstRecord, err)
	}
	secondRecord, err := store.GetDownloadByTweetMedia(ctx, "tweet-2", mediaURL)
	if err != nil || secondRecord == nil {
		t.Fatalf("second record = %+v, err = %v", secondRecord, err)
	}
	if secondRecord.FilePath != firstRecord.FilePath {
		t.Fatalf("second record path = %q, want reused %q", secondRecord.FilePath, firstRecord.FilePath)
	}
}

func TestDownloadMediaFindsLiveCopyBeyondStaleHistory(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("shared media"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatal(err)
	}
	mediaURL := server.URL + "/shared.mp4"
	for i := 0; i < 501; i++ {
		path := filepath.Join(root, fmt.Sprintf("copy-%d.mp4", i))
		if i == 500 {
			if err := os.WriteFile(path, []byte("shared media"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.CreateDownload(ctx, storage.DownloadRecord{
			JobID: job.ID, TweetID: fmt.Sprintf("tweet-%d", i),
			MediaURL: mediaURL, FilePath: path,
		}); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength}
	result, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "retweet", root, "retweet", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.skipped || requests.Load() != 0 {
		t.Fatalf("skipped=%v HTTP requests=%d, want reuse without downloading", result.skipped, requests.Load())
	}
	record, err := store.GetDownloadByTweetMedia(ctx, "retweet", mediaURL)
	if err != nil || record == nil || record.FilePath != filepath.Join(root, "copy-500.mp4") {
		t.Fatalf("reused record=%+v err=%v", record, err)
	}
}

func TestDownloadMediaReusesMatchingContentBeyondStaleHistory(t *testing.T) {
	body := []byte("identical bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength}
	firstURL := server.URL + "/first.mp4"
	if _, err := manager.downloadMedia(ctx, ctx, job, cfg, firstURL, "original", root, "original", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	original, err := store.GetDownloadByTweetMedia(ctx, "original", firstURL)
	if err != nil || original == nil {
		t.Fatalf("original=%+v err=%v", original, err)
	}
	for i := 0; i < 500; i++ {
		if _, err := store.CreateDownload(ctx, storage.DownloadRecord{
			JobID: job.ID, TweetID: fmt.Sprintf("stale-%d", i),
			MediaURL:    fmt.Sprintf("%s/stale-%d.mp4", server.URL, i),
			ContentHash: original.ContentHash, FilePath: filepath.Join(root, fmt.Sprintf("stale-%d.mp4", i)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// All early hash matches are stale; the later record is the only live file.
	// Insert the live copy after the stale rows with a separate path.
	livePath := filepath.Join(root, "live.mp4")
	if err := os.WriteFile(livePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDownload(ctx, storage.DownloadRecord{
		JobID: job.ID, TweetID: "live", MediaURL: server.URL + "/live.mp4",
		ContentHash: original.ContentHash, FilePath: livePath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(original.FilePath); err != nil {
		t.Fatal(err)
	}
	secondURL := server.URL + "/second.mp4"
	result, err := manager.downloadMedia(ctx, ctx, job, cfg, secondURL, "retweet", root, "retweet", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetDownloadByTweetMedia(ctx, "retweet", secondURL)
	if err != nil || record == nil || record.FilePath != livePath || !result.skipped {
		t.Fatalf("result=%+v record=%+v err=%v, want reuse of %s", result, record, err, livePath)
	}
	if _, err := os.Stat(filepath.Join(root, "retweet.mp4")); !os.IsNotExist(err) {
		t.Fatalf("duplicate media remains on disk: %v", err)
	}
}

func TestDownloadMediaReusesSameContentFromDifferentURLs(t *testing.T) {
	body := []byte("same video bytes behind different urls")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength}
	firstURL := server.URL + "/video-a.mp4"
	secondURL := server.URL + "/video-b.mp4"

	first, err := manager.downloadMedia(ctx, ctx, job, cfg, firstURL, "tweet-1", root, "same text", false, time.Time{})
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	if first.skipped {
		t.Fatal("first download skipped = true, want false")
	}
	second, err := manager.downloadMedia(ctx, ctx, job, cfg, secondURL, "tweet-2", root, "same text", false, time.Time{})
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	if !second.skipped {
		t.Fatal("second download skipped = false, want true for identical content")
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read archive dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive dir has %d entries, want 1 after content dedupe", len(entries))
	}
	firstRecord, err := store.GetDownloadByTweetMedia(ctx, "tweet-1", firstURL)
	if err != nil || firstRecord == nil {
		t.Fatalf("first record = %+v, err = %v", firstRecord, err)
	}
	secondRecord, err := store.GetDownloadByTweetMedia(ctx, "tweet-2", secondURL)
	if err != nil || secondRecord == nil {
		t.Fatalf("second record = %+v, err = %v", secondRecord, err)
	}
	if secondRecord.FilePath != firstRecord.FilePath {
		t.Fatalf("second record path = %q, want reused %q", secondRecord.FilePath, firstRecord.FilePath)
	}
}

// TestDownloadMediaSkipsSameMediaAcrossDirectories 验证批量归档不同用户时同一份媒体
// 只下载一次；后续目标即使在不同目录，也复用已有下载记录并跳过远程下载。
func TestDownloadMediaSkipsSameMediaAcrossDirectories(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("viral media"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindFollowing, "alice", "following")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength}
	mediaURL := server.URL + "/media.mp4"
	aliceDir := filepath.Join(root, "users", "Alice(alice)")
	bobDir := filepath.Join(root, "users", "Bob(bob)")

	if _, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "tweet-1", aliceDir, "photo", false, time.Time{}); err != nil {
		t.Fatalf("download for alice: %v", err)
	}
	result, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "tweet-2", bobDir, "photo", false, time.Time{})
	if err != nil {
		t.Fatalf("download for bob: %v", err)
	}
	if !result.skipped {
		t.Fatal("bob download skipped = false, want true for identical media")
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (identical media must be downloaded once)", requests.Load())
	}

	aliceFile := filepath.Join(aliceDir, "photo.mp4")
	if payload, err := os.ReadFile(aliceFile); err != nil || string(payload) != "viral media" {
		t.Fatalf("alice file = %q, err = %v", payload, err)
	}
	if _, err := os.Stat(filepath.Join(bobDir, "photo.mp4")); !os.IsNotExist(err) {
		t.Fatalf("bob file exists or stat failed with unexpected error: %v", err)
	}
	bobRecord, err := store.GetDownloadByTweetMedia(ctx, "tweet-2", mediaURL)
	if err != nil || bobRecord == nil {
		t.Fatalf("bob record = %+v, err = %v", bobRecord, err)
	}
	if bobRecord.FilePath != aliceFile {
		t.Fatalf("bob record path = %q, want reused %q", bobRecord.FilePath, aliceFile)
	}
}

// TestDownloadMediaSameMediaDifferentNameSkips 验证同一份媒体即使本次文件名不同
// （例如转推文本不同）也直接跳过，不重复下载。
func TestDownloadMediaSameMediaDifferentNameSkips(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("shared media"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength}
	mediaURL := server.URL + "/media.mp4"

	if _, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "tweet-1", root, "original text", false, time.Time{}); err != nil {
		t.Fatalf("first download: %v", err)
	}
	result, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "tweet-2", root, "retweet text", false, time.Time{})
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	if !result.skipped {
		t.Fatal("second download skipped = false, want true for identical media")
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1", requests.Load())
	}
	firstPath := filepath.Join(root, "original text.mp4")
	if payload, err := os.ReadFile(firstPath); err != nil || string(payload) != "shared media" {
		t.Fatalf("first file = %q, err = %v", payload, err)
	}
	if _, err := os.Stat(filepath.Join(root, "retweet text.mp4")); !os.IsNotExist(err) {
		t.Fatalf("retweet file exists or stat failed with unexpected error: %v", err)
	}
	secondRecord, err := store.GetDownloadByTweetMedia(ctx, "tweet-2", mediaURL)
	if err != nil || secondRecord == nil {
		t.Fatalf("second record = %+v, err = %v", secondRecord, err)
	}
	if secondRecord.FilePath != firstPath {
		t.Fatalf("second record path = %q, want reused %q", secondRecord.FilePath, firstPath)
	}
}

// TestSkipArchivedMediaReusesExistingMediaPath 验证同一媒体已有可用文件时直接复用
// 既有记录路径并跳过下载；历史记录指向的文件不存在时则不能跳过。
func TestSkipArchivedMediaReusesExistingMediaPath(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	target, err := filestore.New(config.AppConfig{DownloadDir: root})
	if err != nil {
		t.Fatalf("filestore: %v", err)
	}
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength}

	// 同一媒体的另一条推文副本：文件真实存在，大小 5 字节。
	other := filepath.Join(root, "elsewhere", "photo.mp4")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(other, []byte("media"), 0o600); err != nil {
		t.Fatalf("write copy: %v", err)
	}
	if _, err := store.CreateDownload(ctx, storage.DownloadRecord{
		JobID:    job.ID,
		TweetID:  "tweet-1",
		MediaURL: "https://pbs.twimg.com/media/abc.jpg",
		FilePath: other,
		Bytes:    5,
	}); err != nil {
		t.Fatalf("seed copy: %v", err)
	}

	// 目标目录下同名文件即使不同，也不影响同一媒体内容的复用：新记录应指向已有媒体。
	targetPath := filepath.Join(root, "photo.mp4")
	if err := os.WriteFile(targetPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	mediaKey := downloader.MediaIdentity("https://pbs.twimg.com/media/abc.jpg")
	skipped, err := manager.skipArchivedMedia(ctx, ctx, job, cfg, target, nil, mediaKey, "https://pbs.twimg.com/media/abc.jpg", "tweet-2", "")
	if err != nil {
		t.Fatalf("skip archived media: %v", err)
	}
	if !skipped {
		t.Fatal("skipped = false, want true when identical media already exists")
	}
	record, err := store.GetDownloadByTweetMedia(ctx, "tweet-2", "https://pbs.twimg.com/media/abc.jpg")
	if err != nil || record == nil {
		t.Fatalf("record = %+v, err = %v", record, err)
	}
	if record.FilePath != other {
		t.Fatalf("record file path = %q, want reused %q", record.FilePath, other)
	}

	if err := os.Remove(other); err != nil {
		t.Fatalf("remove copy: %v", err)
	}
	skipped, err = manager.skipArchivedMedia(ctx, ctx, job, cfg, target, nil, mediaKey, "https://pbs.twimg.com/media/abc.jpg", "tweet-3", "")
	if err != nil {
		t.Fatalf("skip archived media: %v", err)
	}
	if skipped {
		t.Fatal("skipped = true, want false when the known media file is missing")
	}
}

// TestDownloadMediaJobSkipsAlreadyDownloadedURL 验证 tweet_id 为空的直接媒体 URL 任务
// 也会查下载记录并跳过：重复执行同一 URL 任务不再下载，也不产生第二份副本。
func TestDownloadMediaJobSkipsAlreadyDownloadedURL(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("media"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	mediaURL := server.URL + "/media.mp4"
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, mediaURL, "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength}

	first, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "", root, "media", false, time.Time{})
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	if first.skipped {
		t.Fatal("first download skipped = true, want false")
	}
	second, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "", root, "media", false, time.Time{})
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	if !second.skipped {
		t.Fatal("second download skipped = false, want true")
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1", requests.Load())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive dir has %d entries, want 1", len(entries))
	}
}

// TestDownloadMediaConcurrentSameTargetDownloadsOnce 验证并发归档同一目录下的同一份
// 媒体只下载一次：媒体锁按媒体身份串行化，先到者下载，其余任务命中已有媒体记录并跳过，
// 不会并发写出 (1)/(2) 之类的副本。
func TestDownloadMediaConcurrentSameTargetDownloadsOnce(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("viral media"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: root, MaxFilenameLength: config.DefaultMaxFilenameLength, MaxConcurrency: 8}
	mediaURL := server.URL + "/media.mp4"

	const archives = 6
	results := make([]mediaDownloadResult, archives)
	errs := make([]error, archives)
	var wg sync.WaitGroup
	for index := 0; index < archives; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, fmt.Sprintf("tweet-%d", index), root, "photo", false, time.Time{})
		}(index)
	}
	wg.Wait()

	for index, err := range errs {
		if err != nil {
			t.Fatalf("archive %d: %v", index, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (same media must be downloaded once)", requests.Load())
	}
	downloaded := 0
	for _, result := range results {
		if !result.skipped {
			downloaded++
		}
	}
	if downloaded != 1 {
		t.Fatalf("downloads = %d, want exactly 1", downloaded)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read archive dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive dir has %d entries, want 1", len(entries))
	}
	if payload, err := os.ReadFile(filepath.Join(root, "photo.mp4")); err != nil || string(payload) != "viral media" {
		t.Fatalf("archived media = %q, err = %v", payload, err)
	}
}

func TestIsCancellationOnlyTrustsJobContext(t *testing.T) {
	live := context.Background()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	// net/http 的 Client.Timeout / ResponseHeaderTimeout 错误满足
	// errors.Is(err, context.DeadlineExceeded)。它们是网络故障，不是用户取消：
	// 若按取消处理，handleInterrupt 发现任务并未被取消便什么都不写，任务会
	// 永久停在 downloading，直到重启。
	client := &http.Client{Timeout: 50 * time.Millisecond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	_, timeoutErr := client.Get(server.URL)
	if timeoutErr == nil || !errors.Is(timeoutErr, context.DeadlineExceeded) {
		t.Fatalf("precondition: expected an http timeout matching DeadlineExceeded, got %v", timeoutErr)
	}

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{name: "http client timeout on live job", ctx: live, err: timeoutErr, want: false},
		{name: "bare deadline exceeded on live job", ctx: live, err: context.DeadlineExceeded, want: false},
		{name: "wrapped canceled on live job", ctx: live, err: fmt.Errorf("download: %w", context.Canceled), want: false},
		{name: "job context canceled", ctx: canceled, err: context.Canceled, want: true},
		{name: "job context canceled with other error", ctx: canceled, err: errors.New("read: connection reset"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCancellation(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("isCancellation() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestMediaJobFailsInsteadOfHangingOnNetworkTimeout(t *testing.T) {
	// 服务端先返回响应头，随后不再发送任何数据：请求在 body 读取阶段撞上客户端超时。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: t.TempDir(), MaxConcurrency: 1}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, server.URL+"/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	manager.downloader = downloader.NewWithTimeout(100 * time.Millisecond)
	managerCtx, stop := context.WithCancel(context.Background())
	defer stop()
	manager.Start(managerCtx)
	manager.Notify()

	eventually(t, func() bool {
		got, err := store.GetJob(ctx, job.ID)
		return err == nil && got.Status == storage.JobFailed
	})
}

func TestEnsureUserEntityRenameRewritesDownloadPaths(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.AppConfig{DownloadDir: root}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	before := xclient.User{ID: "u1", Name: "Alice", ScreenName: "alice"}
	if _, err := store.UpsertUser(ctx, storageUser(before)); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	_, oldDir, err := manager.ensureUserEntity(ctx, cfg, before)
	if err != nil {
		t.Fatalf("ensure user entity: %v", err)
	}
	oldFile := filepath.Join(oldDir, "a.jpg")
	if err := os.WriteFile(oldFile, []byte("img"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	record, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: job.ID, TweetID: "t1", MediaURL: "https://pbs.twimg.com/media/a.jpg", FilePath: oldFile})
	if err != nil {
		t.Fatalf("create download: %v", err)
	}

	after := xclient.User{ID: "u1", Name: "Alice2", ScreenName: "alice"}
	_, newDir, err := manager.ensureUserEntity(ctx, cfg, after)
	if err != nil {
		t.Fatalf("ensure renamed user entity: %v", err)
	}
	if newDir == oldDir {
		t.Fatalf("expected directory to change on display name change")
	}
	got, err := store.GetDownload(ctx, record.ID)
	if err != nil || got == nil {
		t.Fatalf("get download: %v", err)
	}
	if want := filepath.Join(newDir, "a.jpg"); got.FilePath != want {
		t.Fatalf("file_path = %q, want %q (media library would 404 after rename)", got.FilePath, want)
	}
	if _, err := os.Stat(got.FilePath); err != nil {
		t.Fatalf("rewritten file_path does not exist on disk: %v", err)
	}
}

func TestEnsureUserEntityKeepsOldNameWhenRenameFails(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.AppConfig{DownloadDir: root}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	before := xclient.User{ID: "u1", Name: "Alice", ScreenName: "alice"}
	if _, err := store.UpsertUser(ctx, storageUser(before)); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	_, oldDir, err := manager.ensureUserEntity(ctx, cfg, before)
	if err != nil {
		t.Fatalf("ensure user entity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "a.jpg"), []byte("img"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	// 目标名已被一个非空目录占用：os.Rename 会失败。
	after := xclient.User{ID: "u1", Name: "Alice2", ScreenName: "alice"}
	blocker := filepath.Join(root, "users", safeName(after.Title()))
	if err := os.MkdirAll(blocker, 0o700); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "other.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	entity, dir, err := manager.ensureUserEntity(ctx, cfg, after)
	if err != nil {
		t.Fatalf("ensure user entity: %v", err)
	}
	// 改名失败时继续使用旧目录，避免同一用户的文件分散到两个目录。
	if dir != oldDir || entity.Name != filepath.Base(oldDir) {
		t.Fatalf("dir = %q (entity %q), want old dir %q kept after failed rename", dir, entity.Name, oldDir)
	}
}

func TestFailingDueScheduleIsBackedOff(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	schedule, err := store.CreateArchiveSchedule(ctx, storage.ArchiveSchedule{
		Name:            "broken",
		Enabled:         true,
		IntervalMinutes: storage.MinArchiveScheduleIntervalMinutes,
		Items:           []storage.ArchiveScheduleItem{{Kind: storage.JobKindUser, Input: "alice"}},
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	// 让它到期，并把库里的目标清单改坏（模拟校验常量收紧后遗留的旧计划）：领取会失败。
	due := time.Now().UTC().Add(-time.Minute)
	if _, err := store.RescheduleArchiveSchedule(ctx, schedule.ID, due); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	raw, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE archive_schedules SET items_json = '[{"kind":"media_url","input":"x"}]' WHERE id = ?`, schedule.ID); err != nil {
		t.Fatalf("corrupt items: %v", err)
	}
	_ = raw.Close()

	manager := NewManager(store, parser.NewService(), NewEventBus())
	manager.enqueueDueArchiveSchedules(ctx)

	got, err := store.GetArchiveSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	// 失败的计划必须被推后，否则每 2 秒 tick 都会重试，并按 next_run_at 升序挤占
	// ListDueArchiveSchedules 的 LIMIT 10，让其他计划永远轮不到。
	if !got.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("next_run_at = %v, want pushed into the future after a failed claim", got.NextRunAt)
	}
}

func TestRetryFailedTweetsRequeuesStillFailingItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), AutoRetryFailed: true}
	job, err := store.CreateJob(ctx, storage.JobKindFailedRetry, "retry", "retry")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.UpsertUser(ctx, storage.User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	entity, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(cfg.DownloadDir, "users"), "Alice(alice)")
	if err != nil {
		t.Fatalf("ensure entity: %v", err)
	}
	payload := func(id string) string {
		return `{"id":"` + id + `","media":[{"type":"video","bestUrl":"` + server.URL + `/` + id + `.mp4"}]}`
	}
	head, err := store.CreateFailedTweet(ctx, storage.FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "1", Payload: payload("1"), Error: "old"})
	if err != nil {
		t.Fatalf("create head: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := store.CreateFailedTweet(ctx, storage.FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "2", Payload: payload("2"), Error: "old"}); err != nil {
		t.Fatalf("create second: %v", err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	if retried := manager.retryFailedTweets(ctx, ctx, job, cfg, true); retried != 0 {
		t.Fatalf("retried = %d, want 0 while upstream is down", retried)
	}

	items, err := store.ListFailedTweets(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("failed tweets = %d, want both kept for a later retry", len(items))
	}
	for _, item := range items {
		if item.ID == head.ID && !item.UpdatedAt.After(head.UpdatedAt) {
			t.Fatalf("head item updated_at not refreshed after a failed retry: %v", item.UpdatedAt)
		}
		if item.Error == "old" {
			t.Fatalf("item %s error not refreshed after a failed retry", item.TweetID)
		}
	}
}

func TestArchiveTweetsAdvancesCursorWhenFailuresAreQueued(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bad") {
			http.Error(w, "Forbidden", http.StatusForbidden) // 过期签名的裸 403：瞬时失败
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-bytes"))
	}))
	defer server.Close()

	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), MaxFilenameLength: config.DefaultMaxFilenameLength, FileNamingMode: config.FileNamingTweetText, StorageType: config.StorageLocal}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	user := xclient.User{ID: "u1", Name: "Alice", ScreenName: "alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	entity, dir, err := manager.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatalf("ensure entity: %v", err)
	}
	tweets := []parser.TweetData{
		{ID: "200", Text: "ok", Media: []parser.Media{{Type: parser.MediaVideo, BestURL: server.URL + "/good.mp4"}}},
		{ID: "100", Text: "bad", Media: []parser.Media{{Type: parser.MediaVideo, BestURL: server.URL + "/bad.mp4"}}},
	}

	stats, err := manager.archiveTweets(ctx, ctx, job, cfg, user, entity, dir, tweets, nil)
	if err != nil {
		t.Fatalf("archive tweets: %v", err)
	}
	if stats.Failed != 1 || stats.Downloaded != 1 {
		t.Fatalf("stats = %+v, want 1 downloaded and 1 failed", stats)
	}
	queued, err := store.ListFailedTweets(ctx, 10)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(queued) != 1 || queued[0].TweetID != "100" {
		t.Fatalf("failed queue = %+v, want tweet 100 queued for retry", queued)
	}
	got, err := store.GetUserEntity(ctx, entity.ID)
	if err != nil {
		t.Fatalf("get entity: %v", err)
	}
	// 失败已由失败队列负责补齐，游标必须前进；否则开了增量归档也会每次全量扫描。
	if got.LastSeenTweetID != "200" {
		t.Fatalf("last_seen_tweet_id = %q, want 200", got.LastSeenTweetID)
	}
}

func TestDownloadableMediaCountSkipsUnplayableVideos(t *testing.T) {
	tweet := parser.TweetData{Media: []parser.Media{
		{Type: parser.MediaVideo, URL: "https://pbs.twimg.com/ext_tw_video_thumb/t.jpg", PreviewURL: "https://pbs.twimg.com/ext_tw_video_thumb/t.jpg"},
		{Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/a.jpg", BestURL: "https://pbs.twimg.com/media/a.jpg"},
	}}
	if got := downloadableMediaCount(tweet); got != 1 {
		t.Fatalf("downloadableMediaCount = %d, want 1 (video has no mp4 variant)", got)
	}
	onlyVideo := parser.TweetData{Media: tweet.Media[:1]}
	if got := downloadableMediaCount(onlyVideo); got != 0 {
		t.Fatalf("downloadableMediaCount = %d, want 0", got)
	}
}

func TestSyncLinkUsesPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "users", "Alice(alice)")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	listDir := filepath.Join(root, "lists", "private list(1)")
	if err := syncLink(filepath.Join(listDir, "Alice(alice)"), target); err != nil {
		t.Fatalf("sync link: %v", err)
	}
	info, err := os.Stat(listDir)
	if err != nil {
		t.Fatal(err)
	}
	// 与媒体目录的 0700 加固一致：列表目录名会暴露私密列表名与成员。
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("list directory permissions = %o, want no group/other access", perm)
	}
}
