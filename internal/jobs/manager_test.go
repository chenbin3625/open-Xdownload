package jobs

import (
	"context"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("identical tweet media"))
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
	if got, err := os.ReadFile(firstPath); err != nil || string(got) != "identical tweet media" {
		t.Fatalf("first tweet media missing or overwritten: %q, %v", got, err)
	}
	if got, err := os.ReadFile(secondPath); err != nil || string(got) != "identical tweet media" {
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

func TestShouldRetryMediaErrorSkipsForbiddenAndNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "forbidden", err: &downloader.HTTPStatusError{StatusCode: http.StatusForbidden}, want: false},
		{name: "not found", err: &downloader.HTTPStatusError{StatusCode: http.StatusNotFound}, want: false},
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
				"读取媒体时间线失败: X 客户端暂时全部限流，请稍后重试 (共 7 个账号: @user1、@user2、@user3、@user4、@user5 等)",
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

func TestArchiveUserConcurrency(t *testing.T) {
	defaultLimit := min(config.Default().MaxConcurrency, maxArchiveUserConcurrency)
	tests := []struct {
		name      string
		cfg       config.AppConfig
		userCount int
		want      int
	}{
		{name: "single user stays serial", cfg: config.AppConfig{MaxConcurrency: 8}, userCount: 1, want: 1},
		{name: "uses configured limit", cfg: config.AppConfig{MaxConcurrency: 3}, userCount: 20, want: 3},
		{name: "caps at backend limit", cfg: config.AppConfig{MaxConcurrency: 64}, userCount: 20, want: maxArchiveUserConcurrency},
		{name: "caps at user count", cfg: config.AppConfig{MaxConcurrency: 10}, userCount: 3, want: 3},
		{name: "falls back to default", cfg: config.AppConfig{}, userCount: 20, want: defaultLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := archiveUserConcurrency(tt.cfg, tt.userCount); got != tt.want {
				t.Fatalf("archiveUserConcurrency() = %d, want %d", got, tt.want)
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

func TestArchiveUsersDeduplicatesOnWorkerPath(t *testing.T) {
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
		MaxConcurrency:      4,
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
// 引用推文等不同 tweet_id 复用同一条媒体 URL）已存在同名同大小的文件时直接跳过：
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
		t.Fatal("second download skipped = false, want true (same name and size already archived)")
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

// TestDownloadMediaDoesNotHardLinkAcrossDirectories 验证批量归档不同用户时不会创建硬链接：
// 目标目录下没有同名同大小的文件就正常下载一份独立文件（内容相同也各自占磁盘）。
func TestDownloadMediaDoesNotHardLinkAcrossDirectories(t *testing.T) {
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
	if result.skipped {
		t.Fatal("bob download skipped = true, want false (no cross-directory reuse)")
	}
	if requests.Load() != 2 {
		t.Fatalf("HTTP requests = %d, want 2 (each archive directory downloads its own copy)", requests.Load())
	}

	aliceFile := filepath.Join(aliceDir, "photo.mp4")
	bobFile := filepath.Join(bobDir, "photo.mp4")
	aliceInfo, err := os.Stat(aliceFile)
	if err != nil {
		t.Fatalf("alice file: %v", err)
	}
	bobInfo, err := os.Stat(bobFile)
	if err != nil {
		t.Fatalf("bob file: %v", err)
	}
	if os.SameFile(aliceInfo, bobInfo) {
		t.Fatal("files are hard linked, want independent copies")
	}
	for path, want := range map[string]string{aliceFile: "viral media", bobFile: "viral media"} {
		if payload, err := os.ReadFile(path); err != nil || string(payload) != want {
			t.Fatalf("file %s = %q, err = %v", path, payload, err)
		}
	}
}

// TestDownloadMediaSameMediaDifferentNameStillDownloads 验证"同名同大小才跳过"：同一份
// 媒体但本次落盘文件名不同（例如转推文本不同）时照常下载自己的文件，不跳过也不硬链接。
func TestDownloadMediaSameMediaDifferentNameStillDownloads(t *testing.T) {
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
	if result.skipped {
		t.Fatal("second download skipped = true, want false for a different file name")
	}
	if requests.Load() != 2 {
		t.Fatalf("HTTP requests = %d, want 2", requests.Load())
	}
	for _, name := range []string{"original text.mp4", "retweet text.mp4"} {
		if payload, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(payload) != "shared media" {
			t.Fatalf("file %s = %q, err = %v", name, payload, err)
		}
	}
}

// TestSkipArchivedMediaRequiresSameSize 验证同名但大小不同（残缺/被替换的文件）不会
// 被当成已归档：必须重新下载，避免旧文件内容被误认为本次媒体。
func TestSkipArchivedMediaRequiresSameSize(t *testing.T) {
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

	// 目标目录下同名文件已被替换成另一份更短的内容：大小不一致，不能跳过。
	targetPath := filepath.Join(root, "photo.mp4")
	if err := os.WriteFile(targetPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	mediaKey := downloader.MediaIdentity("https://pbs.twimg.com/media/abc.jpg")
	skipped, err := manager.skipArchivedMedia(ctx, ctx, job, cfg, target, nil, mediaKey, "https://pbs.twimg.com/media/abc.jpg", "tweet-2", root, "photo", "")
	if err != nil {
		t.Fatalf("skip archived media: %v", err)
	}
	if skipped {
		t.Fatal("skipped = true, want false when the target file size differs")
	}

	// 大小一致时跳过。
	if err := os.WriteFile(targetPath, []byte("media"), 0o600); err != nil {
		t.Fatalf("rewrite target: %v", err)
	}
	skipped, err = manager.skipArchivedMedia(ctx, ctx, job, cfg, target, nil, mediaKey, "https://pbs.twimg.com/media/abc.jpg", "tweet-2", root, "photo", "")
	if err != nil {
		t.Fatalf("skip archived media: %v", err)
	}
	if !skipped {
		t.Fatal("skipped = false, want true when the target file has the same name and size")
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
// 媒体只下载一次：媒体锁按媒体身份串行化，先到者下载，其余任务命中"同名同大小"跳过，
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
