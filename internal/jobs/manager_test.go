package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
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
	skipped, err := manager.downloadMedia(ctx, ctx, job, config.AppConfig{DownloadDir: root}, server.URL+"/media.mp4", "tweet-1", "", "media", false, time.Time{})
	if err != nil {
		t.Fatalf("download media: %v", err)
	}
	if !skipped {
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
	skipped, err := manager.downloadMedia(ctx, ctx, job, config.AppConfig{DownloadDir: root}, server.URL+"/media.mp4", "tweet-1", root, "media", false, time.Time{})
	if err != nil {
		t.Fatalf("download media: %v", err)
	}
	if skipped {
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
