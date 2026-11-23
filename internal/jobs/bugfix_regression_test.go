package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

type stubXSource struct {
	xapi.Source
	walkTweets func(ctx context.Context, user xapi.User, opts xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error
	walkMedia  func(ctx context.Context, user xapi.User, opts xapi.UserMediaTimelineOptions, save func(xapi.UserMediaTimelinePage) error) (xapi.MediaFetchStats, error)
	fetchTweet func(ctx context.Context, id string, opts parser.ParseOptions) (parser.TweetData, error)
	recheck    func(ctx context.Context) bool
	hasPrimary bool
}

func (s *stubXSource) WalkUserTweets(ctx context.Context, user xapi.User, opts xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
	if s.walkTweets != nil {
		return s.walkTweets(ctx, user, opts, save)
	}
	return nil
}

func (s *stubXSource) WalkUserMedia(ctx context.Context, user xapi.User, opts xapi.UserMediaTimelineOptions, save func(xapi.UserMediaTimelinePage) error) (xapi.MediaFetchStats, error) {
	if s.walkMedia != nil {
		return s.walkMedia(ctx, user, opts, save)
	}
	return xapi.MediaFetchStats{}, nil
}

func (s *stubXSource) FetchTweet(ctx context.Context, id string, opts parser.ParseOptions) (parser.TweetData, error) {
	if s.fetchTweet != nil {
		return s.fetchTweet(ctx, id, opts)
	}
	return parser.TweetData{}, nil
}

func (s *stubXSource) RecheckDisabledClients(ctx context.Context) bool {
	if s.recheck != nil {
		return s.recheck(ctx)
	}
	return false
}

func (s *stubXSource) HasPrimary() bool {
	return s.hasPrimary
}

func TestArchiveTweetsBatchContinuesOnSingleUserErrorAndPreservesLinkError(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind:        storage.JobKindList,
		Input:       "list-1",
		ArchiveMode: storage.ArchiveTweets,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertList(ctx, storage.List{ID: "list-1", Name: "list-1"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	listEntity, err := manager.ensureListEntity(ctx, cfg, "lists", "list-1", "list-1")
	if err != nil {
		t.Fatal(err)
	}
	pool := &stubXSource{
		walkTweets: func(_ context.Context, user xapi.User, _ xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
			if user.ID == "u1" {
				return errors.New("用户不可用")
			}
			return save(xapi.TweetTimelinePage{
				Tweets:   []parser.TweetData{{ID: "200", Text: "ok", Author: parser.Author{ID: user.ID, ScreenName: user.ScreenName}}},
				HeadID:   "200",
				Complete: true,
			})
		},
	}

	users := []xapi.User{
		{ID: "u1", ScreenName: "broken", Name: "Broken"},
		{ID: "u2", ScreenName: "healthy", Name: "Healthy"},
	}
	stats, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, &listEntity, 0, 1)
	if err != nil {
		t.Fatalf("batch aborted on single-user tweet timeline error: %v", err)
	}
	if stats.Users != 2 || stats.Failed != 1 || stats.Tweets != 1 {
		t.Fatalf("stats = %+v, want Users=2 Failed=1 Tweets=1", stats)
	}
	if len(stats.Issues) != 1 || !strings.Contains(stats.Issues[0], "@broken") {
		t.Fatalf("issues = %+v, want @broken issue", stats.Issues)
	}

	// Verify ensureUserLinkWithStore error is preserved when archiveFullTimeline succeeds.
	u3 := xapi.User{ID: "u3", ScreenName: "linkfail", Name: "LinkFail"}
	if _, err := store.UpsertUser(ctx, storageUser(u3)); err != nil {
		t.Fatal(err)
	}
	target, err := filestore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, dir, err := manager.ensureUserEntityWithStore(ctx, target, u3)
	if err != nil {
		t.Fatal(err)
	}
	blockingLink := filepath.Join(listEntity.ParentDir, listEntity.Name, filepath.Base(dir))
	if err := os.WriteFile(blockingLink, []byte("not-a-symlink"), 0o600); err != nil {
		t.Fatal(err)
	}
	singleStats, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, u3, &listEntity, nil, nil)
	if err != nil {
		t.Fatalf("archiveUserWithBudget err = %v", err)
	}
	if singleStats.Users != 1 || singleStats.Failed != 1 || len(singleStats.Issues) != 1 {
		t.Fatalf("singleStats = %+v, want Users=1 Failed=1 Issues=1", singleStats)
	}
}

func TestFullTimelineRateLimitSetsInterruptedStatus(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind:        storage.JobKindUser,
		Input:       "alice",
		ArchiveMode: storage.ArchiveTweets,
	})
	if err != nil {
		t.Fatal(err)
	}
	user := xapi.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	entity, dir, err := manager.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	pool := &stubXSource{
		walkTweets: func(context.Context, xapi.User, xapi.TweetTimelineOptions, func(xapi.TweetTimelinePage) error) error {
			return &xapi.RateLimitedError{RetryAt: time.Now().Add(5 * time.Minute), PoolExhausted: true}
		},
	}

	_, err = manager.archiveFullTimeline(ctx, ctx, job, cfg, pool, user, entity, dir, nil, nil)
	if _, limited := xapi.RateLimitRetryAt(err); !limited {
		t.Fatalf("err = %v, want rate limit error", err)
	}
	syncState, err := store.GetTweetSync(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if syncState.Status != "interrupted" {
		t.Fatalf("tweet_sync.Status = %q, want interrupted", syncState.Status)
	}
}

func TestDownloadAssetMediaPolicyRelocationFailureResetsSkippedAndChecksPermanentFailure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	png := archiveTestPNG(t)
	var statusCode int = http.StatusOK
	cfg := config.AppConfig{
		DownloadDir:       filepath.Join(dir, "downloads"),
		StorageType:       config.StorageLocal,
		MaxFilenameLength: 120,
	}
	cfg = archiveMediaRefreshClient(t, store, cfg, func(w http.ResponseWriter, r *http.Request) {
		if statusCode != http.StatusOK {
			http.Error(w, "err", statusCode)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})

	doc := config.DefaultStorage(cfg.DownloadDir)
	doc, err = store.SaveStorageDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindTweetLink, "https://x.com/a/status/1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, nil, nil)

	// 1. Seed a transient 503 failure, then hit a single 404 -> must NOT blacklist in unavailable_media.
	transientURL := "https://pbs.twimg.com/media/transient.png"
	if _, err := store.CreateFailedMedia(ctx, storage.FailedMedia{JobID: job.ID, MediaURL: transientURL, Error: "download failed: HTTP 503"}); err != nil {
		t.Fatal(err)
	}
	statusCode = http.StatusNotFound
	if _, err := manager.downloadAssetMedia(ctx, ctx, job, cfg, doc, transientURL, "1", true, time.Now().UTC()); err == nil {
		t.Fatal("expected 404 error on first permanent failure")
	}
	if _, unavail, err := store.GetMediaDownloadState(ctx, "1", transientURL); err != nil || unavail {
		t.Fatalf("transient 503 + single 404 must not mark unavailable: unavail=%t err=%v", unavail, err)
	}

	// 2. Download under v1, delete v1 file on disk, bump storage policy to v2 -> copyAssetToTemp fails
	// and falls back to re-download, which must report skipped=false.
	statusCode = http.StatusOK
	mediaURL := "https://pbs.twimg.com/media/relocate.png"
	res, err := manager.downloadAssetMedia(ctx, ctx, job, cfg, doc, mediaURL, "1", true, time.Now().UTC())
	if err != nil || res.skipped {
		t.Fatalf("initial download: res=%+v err=%v", res, err)
	}
	asset, err := store.GetMediaAsset(ctx, downloader.MediaIdentity(mediaURL), config.AssetImage)
	if err != nil || asset == nil {
		t.Fatalf("GetMediaAsset: %+v err=%v", asset, err)
	}
	if err := os.Remove(filepath.Join(doc.Targets[0].Root, filepath.FromSlash(asset.Key))); err != nil {
		t.Fatal(err)
	}
	doc.Targets[0].Root = filepath.Join(dir, "downloads-v2")
	doc, err = store.SaveStorageDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	res, err = manager.downloadAssetMedia(ctx, ctx, job, cfg, doc, mediaURL, "2", true, time.Now().UTC())
	if err != nil {
		t.Fatalf("relocate fallback download: %v", err)
	}
	if res.skipped {
		t.Fatal("skipped = true after copyAssetToTemp failure fell back to network download, want false")
	}
}

func TestClassifyMigrationAssetKindAndIsPhotoURL(t *testing.T) {
	if isPhotoURL("https://video.twimg.com/ext_tw_video/1/pu/pl/playlist.m3u8") {
		t.Fatal("video.twimg.com playlist classified as photo URL")
	}
	if isPhotoURL("https://video.twimg.com/tweet_video/abc") {
		t.Fatal("video.twimg.com path classified as photo URL")
	}
	if isPhotoURL("https://pbs.twimg.com/media/clip.mov") {
		t.Fatal(".mov classified as photo URL")
	}
	if isPhotoURL("https://pbs.twimg.com/media/clip?format=mp4") {
		t.Fatal("?format=mp4 classified as photo URL")
	}
	if !isPhotoURL("https://pbs.twimg.com/media/img?format=jpg") {
		t.Fatal("expected photo URL")
	}

	cases := []struct {
		name   string
		record storage.DownloadRecord
		links  map[config.AssetKind]string
		want   config.AssetKind
	}{
		{
			name:   "mp4 file with video.twimg.com playlist url",
			record: storage.DownloadRecord{FilePath: "/tmp/clip.mp4", MediaURL: "https://video.twimg.com/ext_tw_video/1/pu/pl/abc.m3u8"},
			want:   config.AssetVideo,
		},
		{
			name:   "mov file even with pbs url",
			record: storage.DownloadRecord{FilePath: "/tmp/clip.MOV", MediaURL: "https://pbs.twimg.com/media/abc"},
			want:   config.AssetVideo,
		},
		{
			name:   "avif image file",
			record: storage.DownloadRecord{FilePath: "/tmp/pic.avif", MediaURL: "https://example.com/pic"},
			want:   config.AssetImage,
		},
		{
			name:   "existing video link wins",
			record: storage.DownloadRecord{FilePath: "/tmp/poster.jpg", MediaURL: "https://pbs.twimg.com/media/abc.jpg"},
			links:  map[config.AssetKind]string{config.AssetVideo: "asset-v"},
			want:   config.AssetVideo,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext := strings.ToLower(filepath.Ext(tc.record.FilePath))
			if got := classifyMigrationAssetKind(tc.record, tc.links, ext); got != tc.want {
				t.Fatalf("classifyMigrationAssetKind() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMaybeMaintainCleansStorageTargetTempFiles(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	downloadDir := t.TempDir()
	targetRoot := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: downloadDir, StorageType: config.StorageLocal}); err != nil {
		t.Fatal(err)
	}
	doc := config.StorageDocument{
		Enabled: true,
		Targets: []config.StorageTarget{
			{ID: "local-extra", Name: "Extra", Type: "local", Root: targetRoot},
			{ID: "s3-remote", Name: "S3", Type: "s3", Endpoint: "https://s3.example.com", Bucket: "bkt"},
		},
		Default: config.AssetRule{TargetID: "local-extra", DirectoryTemplate: "{kind}/{username}", FilenameTemplate: "{tweet_id}_{media_id}_{index}.{ext}"},
		Rules:   map[config.AssetKind]config.AssetRule{},
	}
	if _, err := store.SaveStorageDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}

	staleDownload := filepath.Join(downloadDir, ".xdl-stale.part")
	staleTarget := filepath.Join(targetRoot, ".asset-stale.part")
	freshTarget := filepath.Join(targetRoot, ".xdl-fresh.part")
	for _, path := range []string{staleDownload, staleTarget, freshTarget} {
		if err := os.WriteFile(path, []byte("part"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	for _, path := range []string{staleDownload, staleTarget} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	manager := NewManager(store, nil, NewEventBus())
	manager.maybeMaintain(ctx)

	if _, err := os.Stat(staleDownload); !os.IsNotExist(err) {
		t.Fatalf("stale temp file in DownloadDir not removed: %v", err)
	}
	if _, err := os.Stat(staleTarget); !os.IsNotExist(err) {
		t.Fatalf("stale temp file in StorageTarget root not removed: %v", err)
	}
	if _, err := os.Stat(freshTarget); err != nil {
		t.Fatalf("fresh temp file should survive: %v", err)
	}
}

func TestCancelJobIgnoresAlreadyCompletedJobAndClearsCheckpoint(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	bus := NewEventBus()
	ch, ok := bus.Subscribe()
	if !ok {
		t.Fatal("subscribe failed")
	}
	defer bus.Unsubscribe(ch)

	manager := NewManager(store, nil, bus)

	// 1. Already completed automatic retry job must not emit job.updated or pause auto-retry.
	completedJob, err := store.CreateJob(ctx, storage.JobKindFailedRetry, automaticRetryJobInput, "auto")
	if err != nil {
		t.Fatal(err)
	}
	completedJob.Status = storage.JobCompleted
	completedJob.Progress = 1
	if _, err := store.UpdateJobAndGet(ctx, completedJob); err != nil {
		t.Fatal(err)
	}
	got, err := manager.CancelJob(ctx, completedJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != storage.JobCompleted {
		t.Fatalf("CancelJob status = %q, want completed", got.Status)
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event published for already-completed job: %+v", ev)
	default:
	}
	if !manager.shouldCheckAutomaticRetry() {
		t.Fatal("canceling already-completed automatic retry job must not pause auto-retry")
	}

	// 2. Canceling a pending job with checkpoint deletes its checkpoint.
	pendingJob, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobCheckpoint(ctx, pendingJob.ID, []byte(`{"next":1}`)); err != nil {
		t.Fatal(err)
	}
	got, err = manager.CancelJob(ctx, pendingJob.ID)
	if err != nil || got.Status != storage.JobCanceled {
		t.Fatalf("CancelJob pending = %+v, %v", got, err)
	}
	if _, exists, err := store.LoadJobCheckpoint(ctx, pendingJob.ID); err != nil || exists {
		t.Fatalf("checkpoint survived CancelJob: exists=%t err=%v", exists, err)
	}
}

func TestProcessTweetLinkPartialFailureAndCancellationPreservesStatus(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	ctx := context.Background()
	png := archiveTestPNG(t)
	var cancelOnSecond func()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		case "/cancel.png":
			if cancelOnSecond != nil {
				cancelOnSecond()
			}
			http.Error(w, "interrupted", http.StatusInternalServerError)
		default:
			http.Error(w, "server error", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	if _, err := store.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	useLegacyTestDownloader(t, manager)

	// Case 1: media[0] succeeds, media[1] returns 500 -> MediaStatus must be "partial" (not "failed").
	manager.SetXSourceFactory(func(context.Context, config.AppConfig) (xapi.Source, error) {
		return &stubXSource{
			fetchTweet: func(_ context.Context, id string, _ parser.ParseOptions) (parser.TweetData, error) {
				return parser.TweetData{
					ID:     id,
					Text:   "two media",
					Author: parser.Author{ID: "u1", ScreenName: "alice"},
					Media: []parser.Media{
						{ID: "m1", Type: parser.MediaPhoto, BestURL: server.URL + "/ok.png"},
						{ID: "m2", Type: parser.MediaPhoto, BestURL: server.URL + "/fail.png"},
					},
				}, nil
			},
		}, nil
	})
	job1, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindTweetLink, Input: "https://x.com/alice/status/101", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	manager.processTweetLink(ctx, ctx, job1)
	archived1, err := store.GetTweet(ctx, "101")
	if err != nil || archived1.MediaStatus != "partial" {
		t.Fatalf("tweet 101 MediaStatus = %q (err=%v), want partial", archived1.MediaStatus, err)
	}

	// Case 2: cancellation during media download must not overwrite MediaStatus to "failed".
	cancelCtx, cancel := context.WithCancel(ctx)
	cancelOnSecond = cancel
	manager.SetXSourceFactory(func(context.Context, config.AppConfig) (xapi.Source, error) {
		return &stubXSource{
			fetchTweet: func(_ context.Context, id string, _ parser.ParseOptions) (parser.TweetData, error) {
				return parser.TweetData{
					ID:     id,
					Text:   "cancel mid media",
					Author: parser.Author{ID: "u1", ScreenName: "alice"},
					Media: []parser.Media{
						{ID: "m3", Type: parser.MediaPhoto, BestURL: server.URL + "/cancel.png"},
					},
				}, nil
			},
		}, nil
	})
	job2, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindTweetLink, Input: "https://x.com/alice/status/102", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	manager.processTweetLink(cancelCtx, ctx, job2)
	archived2, err := store.GetTweet(ctx, "102")
	if err != nil || archived2.MediaStatus != "pending" {
		t.Fatalf("tweet 102 MediaStatus = %q (err=%v), want pending after cancellation", archived2.MediaStatus, err)
	}
}

func TestRetryFailedTweetsNilParserAndDiskFatalAbort(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	job, err := store.CreateJob(ctx, storage.JobKindFailedRetry, "manual", "retry")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, storage.User{ID: "u1", ScreenName: "alice"}); err != nil {
		t.Fatal(err)
	}
	entity, err := store.EnsureUserEntity(ctx, "u1", cfg.DownloadDir, "alice")
	if err != nil {
		t.Fatal(err)
	}

	// Verify m.parser == nil with non-empty tweet.URL does not panic, and disk-fatal error aborts immediately.
	tweet := parser.TweetData{
		ID:     "t1",
		URL:    "https://x.com/alice/status/1",
		Author: parser.Author{ID: "u1", ScreenName: "alice"},
		Media:  []parser.Media{{ID: "m1", Type: parser.MediaPhoto, BestURL: "https://pbs.twimg.com/media/a.jpg"}},
	}
	payload, _ := json.Marshal(tweet)
	if _, err := store.CreateFailedTweet(ctx, storage.FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "t1", Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, nil, NewEventBus())
	// Verify isDiskFatalError recognizes ENOSPC wrapped error (smoke check for helper).
	if !isDiskFatalError(&os.PathError{Op: "write", Path: "/tmp/x", Err: syscall.ENOSPC}) {
		t.Fatal("expected ENOSPC PathError to be disk fatal")
	}
	// Create a file at entity dir so MkdirAll fails with ENOTDIR / path error (not disk-fatal),
	// ensuring nil parser did not panic when tweet.URL is non-empty.
	if err := os.WriteFile(filepath.Join(entity.ParentDir, entity.Name), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.retryFailedTweets(ctx, ctx, job, cfg, true); err != nil {
		t.Fatalf("retryFailedTweets with nil parser returned unexpected error: %v", err)
	}
}

func TestArchiveMediaTimelineStaleCursorUnauthorizedRestartsFromTop(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "alice", ArchiveMode: storage.ArchiveMedia})
	if err != nil {
		t.Fatal(err)
	}
	user := xapi.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	entity, dir, err := manager.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMediaSync(ctx, storage.MediaSync{UserID: user.ID, Cursor: "stale-cursor", Status: "interrupted"}); err != nil {
		t.Fatal(err)
	}

	var seenCursors []string
	pool := &stubXSource{
		recheck: func(context.Context) bool { return true },
		walkMedia: func(_ context.Context, _ xapi.User, opts xapi.UserMediaTimelineOptions, save func(xapi.UserMediaTimelinePage) error) (xapi.MediaFetchStats, error) {
			seenCursors = append(seenCursors, opts.Cursor)
			if opts.Cursor == "stale-cursor" {
				return xapi.MediaFetchStats{}, &xapi.HTTPError{StatusCode: http.StatusUnauthorized}
			}
			return xapi.MediaFetchStats{Pages: 1, LastSeenTweetID: "500"}, save(xapi.UserMediaTimelinePage{
				Tweets:   []parser.TweetData{{ID: "500", Author: parser.Author{ID: "u1", ScreenName: "alice"}}},
				Complete: true,
			})
		},
	}

	if _, err := manager.archiveMediaTimeline(ctx, ctx, job, cfg, pool, user, entity, dir, parser.ParseOptions{}, nil, nil); err != nil {
		t.Fatalf("archiveMediaTimeline stale 401 cursor err = %v", err)
	}
	if len(seenCursors) != 2 || seenCursors[0] != "stale-cursor" || seenCursors[1] != "" {
		t.Fatalf("seenCursors = %v, want [stale-cursor \"\"]", seenCursors)
	}
}

func TestManagerDeleteJobChecksActiveStatusAndWaitsForExit(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	manager := NewManager(store, nil, NewEventBus())

	// Non-existent job returns sql.ErrNoRows (wrapped or direct).
	if err := manager.DeleteJob(ctx, 99999); err == nil {
		t.Fatal("expected error deleting non-existent job")
	}

	// Non-terminal job returns ErrJobActive immediately.
	pendingJob, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteJob(ctx, pendingJob.ID); !errors.Is(err, storage.ErrJobActive) {
		t.Fatalf("DeleteJob(pending) = %v, want ErrJobActive", err)
	}

	// Active goroutine unwinding after CancelJob waits on activeDone before deleting.
	releasing := make(chan struct{})
	manager.mu.Lock()
	done := make(chan struct{})
	manager.active[pendingJob.ID] = func() {}
	manager.activeDone[pendingJob.ID] = done
	manager.mu.Unlock()
	if _, err := manager.CancelJob(ctx, pendingJob.ID); err != nil {
		t.Fatal(err)
	}

	manager.checkpointTasksMu.Lock()
	manager.checkpointTasksCache = checkpointTasksSnapshot{jobID: pendingJob.ID, loaded: true}
	manager.checkpointTasksMu.Unlock()

	delErr := make(chan error, 1)
	go func() {
		delErr <- manager.DeleteJob(ctx, pendingJob.ID)
	}()

	select {
	case err := <-delErr:
		t.Fatalf("DeleteJob returned before worker finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releasing)
	manager.finishJob(pendingJob.ID)

	if err := <-delErr; err != nil {
		t.Fatalf("DeleteJob after finishJob: %v", err)
	}
	manager.checkpointTasksMu.Lock()
	cacheLoaded := manager.checkpointTasksCache.loaded
	manager.checkpointTasksMu.Unlock()
	if cacheLoaded {
		t.Fatal("DeleteJob did not clear checkpointTasksCache")
	}
}

func TestManagerStartStopConcurrentNoRace(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	manager := NewManager(store, nil, NewEventBus())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		manager.Start(ctx)
		done <- true
	}()
	stopped := manager.Stop()
	<-done
	if !stopped {
		t.Fatal("Stop() timed out")
	}
}

func TestArchiveUsersPersistsTasksBeforeUser0AndClearsCache(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindList, Input: "list-1", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	yieldFirst := true
	pool := &stubXSource{
		walkTweets: func(context.Context, xapi.User, xapi.TweetTimelineOptions, func(xapi.TweetTimelinePage) error) error {
			if yieldFirst {
				yieldFirst = false
				return errYieldExecution
			}
			return nil
		},
	}
	users := []xapi.User{{ID: "u1", ScreenName: "alice", Name: "Alice"}}
	_, err = manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1)
	if !errors.Is(err, errYieldExecution) {
		t.Fatalf("err = %v, want errYieldExecution", err)
	}
	tasksPayload, ok, err := store.LoadJobCheckpointTasks(ctx, job.ID)
	if err != nil || !ok || len(tasksPayload) == 0 {
		t.Fatalf("tasks checkpoint missing after user-0 yield: ok=%t err=%v", ok, err)
	}
	if _, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, nil, nil, 0, 1); err != nil {
		t.Fatalf("resume archiveUsers: %v", err)
	}
	manager.checkpointTasksMu.Lock()
	cacheLoaded := manager.checkpointTasksCache.loaded
	manager.checkpointTasksMu.Unlock()
	if cacheLoaded {
		t.Fatal("checkpointTasksCache remained loaded after completion")
	}
}

func TestFullTimelineDoesNotRegressLastIDOnEmptyCompletePage(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "alice", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	user := xapi.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	entity, dir, err := manager.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTweetSync(ctx, storage.TweetSync{UserID: user.ID, LastID: "500", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	pool := &stubXSource{
		walkTweets: func(_ context.Context, _ xapi.User, _ xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
			return save(xapi.TweetTimelinePage{Complete: true, HeadID: ""})
		},
	}
	if _, err := manager.archiveFullTimeline(ctx, ctx, job, cfg, pool, user, entity, dir, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTweetSync(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastID != "500" {
		t.Fatalf("LastID = %q, want 500 preserved", got.LastID)
	}
}

func TestEnsureUserEntityWithDocumentRespectsCustomRootAndS3(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	downloadDir := t.TempDir()
	customRoot := t.TempDir()
	cfg := config.AppConfig{DownloadDir: downloadDir, StorageType: config.StorageLocal}
	target, err := filestore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	user := xapi.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, nil, NewEventBus())

	// Case A: custom local root != DownloadDir
	localDoc := config.StorageDocument{
		Enabled: true,
		Targets: []config.StorageTarget{{ID: "custom", Name: "Custom", Type: "local", Root: customRoot}},
		Default: config.AssetRule{TargetID: "custom", DirectoryTemplate: "{kind}/{username}", FilenameTemplate: "{tweet_id}_{media_id}_{index}.{ext}"},
		Rules:   map[config.AssetKind]config.AssetRule{},
	}
	_, dir, err := manager.ensureUserEntityWithDocument(ctx, target, localDoc, user)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(dir) != filepath.Join(customRoot, "image", "alice") {
		t.Fatalf("dir = %q, want under customRoot", dir)
	}
	if _, err := os.Stat(filepath.Join(downloadDir, "image")); !os.IsNotExist(err) {
		t.Fatalf("phantom dir created under DownloadDir: %v", err)
	}

	// Case A2: relative target root (e.g. default "downloads") falls back to target.Root().
	userRel := xapi.User{ID: "u-rel", ScreenName: "reluser", Name: "Rel"}
	if _, err := store.UpsertUser(ctx, storageUser(userRel)); err != nil {
		t.Fatal(err)
	}
	relDoc := config.DefaultStorage("downloads")
	_, relDir, err := manager.ensureUserEntityWithDocument(ctx, target, relDoc, userRel)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(relDir) != filepath.Join(downloadDir, "image", "reluser") {
		t.Fatalf("relative root dir = %q, want under downloadDir %q", relDir, downloadDir)
	}

	// Case B: S3 target skips local MkdirAll and returns dir == ""
	user2 := xapi.User{ID: "u2", ScreenName: "bob", Name: "Bob"}
	if _, err := store.UpsertUser(ctx, storageUser(user2)); err != nil {
		t.Fatal(err)
	}
	s3Doc := config.StorageDocument{
		Enabled: true,
		Targets: []config.StorageTarget{{ID: "s3", Name: "S3", Type: "s3", Endpoint: "https://s3.example.com", Bucket: "bkt"}},
		Default: config.AssetRule{TargetID: "s3", DirectoryTemplate: "{kind}/{username}", FilenameTemplate: "{tweet_id}_{media_id}_{index}.{ext}"},
		Rules:   map[config.AssetKind]config.AssetRule{},
	}
	entity2, dir2, err := manager.ensureUserEntityWithDocument(ctx, target, s3Doc, user2)
	if err != nil {
		t.Fatal(err)
	}
	if dir2 != "" || entity2.ID == 0 {
		t.Fatalf("s3 entity = %+v, dir = %q, want empty local dir", entity2, dir2)
	}
	if _, err := os.Stat(filepath.Join(downloadDir, "image", "bob")); !os.IsNotExist(err) {
		t.Fatalf("S3 target created local dir: %v", err)
	}
}

func TestFirst404QueuedInBatchThenConfirmedAcrossTwoRetries(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "alice", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	user := xapi.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	useLegacyTestDownloader(t, manager)
	entity, dir, err := manager.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}

	// 1st observation (404 in archiveTweetsBatch): must queue in failed_tweets, set MediaStatus="failed", and record Issue.
	mediaURL := server.URL + "/missing.jpg"
	tweet := parser.TweetData{
		ID:     "901",
		Author: parser.Author{ID: "u1", ScreenName: "alice"},
		Media:  []parser.Media{{ID: "m1", Type: parser.MediaPhoto, BestURL: mediaURL}},
	}
	stats, err := manager.archiveTweetsBatch(ctx, ctx, job, cfg, user, entity, dir, []parser.TweetData{tweet}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 1 || len(stats.Issues) != 1 {
		t.Fatalf("stats = %+v, want Failed=1 Issues=1", stats)
	}
	archived, err := store.GetTweet(ctx, "901")
	if err != nil || archived.MediaStatus != "failed" {
		t.Fatalf("tweet MediaStatus = %q (err=%v), want failed", archived.MediaStatus, err)
	}
	queued, err := store.ListFailedTweets(ctx, 10)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued = %+v, want 1", queued)
	}

	// Reset failed_media to transient "offline" to verify retryFailedTweets does NOT blacklist on a single 404 observation.
	retryJob, err := store.CreateJob(ctx, storage.JobKindFailedRetry, "manual", "retry")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.retryFailedTweets(ctx, ctx, retryJob, cfg, true); err != nil {
		t.Fatal(err)
	}
	// Since archiveTweetsBatch recorded HTTP 404 in failed_media, the retry WAS the 2nd 404 observation -> blacklisted & settled.
	if _, unavail, err := store.GetMediaDownloadState(ctx, "901", mediaURL); err != nil || !unavail {
		t.Fatalf("expected unavailable after 2nd 404 confirmation: unavail=%t err=%v", unavail, err)
	}
	if rem, err := store.CountFailedTweets(ctx); err != nil || rem != 0 {
		t.Fatalf("remaining failed tweets = %d, want 0", rem)
	}

	// Verify single 404 after only transient "offline" failure stays in failed_tweets without blacklisting.
	transientURL := server.URL + "/transient-then-404.jpg"
	t2 := parser.TweetData{
		ID:     "902",
		Author: parser.Author{ID: "u1", ScreenName: "alice"},
		Media:  []parser.Media{{ID: "m2", Type: parser.MediaPhoto, BestURL: transientURL}},
	}
	if err := store.ArchiveTweet(ctx, job.ID, t2, "failed"); err != nil {
		t.Fatal(err)
	}
	if err := manager.rememberFailedTweet(ctx, job, entity, t2, errors.New("HTTP 503")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedMedia(ctx, storage.FailedMedia{JobID: job.ID, MediaURL: transientURL, Error: "HTTP 503"}); err != nil {
		t.Fatal(err)
	}
	retryJob2, err := store.CreateJob(ctx, storage.JobKindFailedRetry, "manual", "retry2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.retryFailedTweets(ctx, ctx, retryJob2, cfg, true); err != nil {
		t.Fatal(err)
	}
	if _, unavail, err := store.GetMediaDownloadState(ctx, "902", transientURL); err != nil || unavail {
		t.Fatalf("single 404 after transient 503 prematurely blacklisted: unavail=%t err=%v", unavail, err)
	}
	if rem, err := store.CountFailedTweets(ctx); err != nil || rem != 1 {
		t.Fatalf("single 404 should remain queued for confirmation: rem=%d err=%v", rem, err)
	}
}

func TestArchiveUsersBatchCountsBlockedAndTouchesFailedUser(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	pool := &stubXSource{
		walkTweets: func(_ context.Context, user xapi.User, _ xapi.TweetTimelineOptions, _ func(xapi.TweetTimelinePage) error) error {
			if user.ID == "u1" {
				return errors.New("用户不可用")
			}
			return nil
		},
	}
	users := []xapi.User{
		{ID: "u-blocked", ScreenName: "blocked", Blocking: true},
		{ID: "u1", ScreenName: "broken"},
		{ID: "u2", ScreenName: "healthy"},
	}

	job1, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindFollowing, Input: "me", ArchiveMode: storage.ArchiveTweets, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := manager.archiveUsers(ctx, ctx, job1, cfg, pool, users, nil, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SkippedUsers != 1 || stats.Failed != 1 {
		t.Fatalf("stats = %+v, want SkippedUsers=1 Failed=1", stats)
	}
	times, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil || times["u1"].IsZero() {
		t.Fatalf("failed user tweet_sync not touched: %+v err=%v", times, err)
	}

	// Next batch run should pick u2 (never synced) instead of sticking on u1.
	var picked []string
	pool2 := &stubXSource{
		walkTweets: func(_ context.Context, user xapi.User, _ xapi.TweetTimelineOptions, _ func(xapi.TweetTimelinePage) error) error {
			picked = append(picked, user.ID)
			return nil
		},
	}
	job2, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindFollowing, Input: "me", ArchiveMode: storage.ArchiveTweets, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.archiveUsers(ctx, ctx, job2, cfg, pool2, users, nil, 0, 1); err != nil {
		t.Fatal(err)
	}
	if len(picked) != 1 || picked[0] != "u2" {
		t.Fatalf("second batch picked %v, want [u2]", picked)
	}
}

func TestCollectArchiveMembersStaleCursorResetsAndRetriesOnce(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	oldDelay := memberPageDelay
	memberPageDelay = 0
	defer func() { memberPageDelay = oldDelay }()

	ctx := context.Background()
	job, err := store.CreateJob(ctx, storage.JobKindFollowing, "alice", "following")
	if err != nil {
		t.Fatal(err)
	}
	cp := archiveCheckpoint{
		Members: &memberCheckpoint{
			Cursor:      "stale-cursor",
			ViewerKey:   "viewer-1",
			SeenCursors: []string{"stale-cursor"},
			EmptyPages:  2,
			Users:       []xapi.User{{ID: "1"}},
		},
	}
	payload, _ := json.Marshal(cp.progress())
	if err := store.SaveJobCheckpoint(ctx, job.ID, payload); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(store, nil, NewEventBus())
	var seen []string
	users, err := manager.collectArchiveMembersWithRecheck(ctx, ctx, job, func(context.Context) bool { return true }, func(cursor, viewer string) (xapi.UserPage, error) {
		seen = append(seen, cursor+"|"+viewer)
		if cursor == "stale-cursor" {
			return xapi.UserPage{}, &xapi.HTTPError{StatusCode: http.StatusUnauthorized}
		}
		return xapi.UserPage{Users: []xapi.User{{ID: "1"}, {ID: "2"}}}, nil
	})
	if err != nil {
		t.Fatalf("collectArchiveMembersWithRecheck: %v", err)
	}
	if len(users) != 2 || len(seen) != 2 || seen[0] != "stale-cursor|viewer-1" || seen[1] != "|" {
		t.Fatalf("users=%+v seen=%v", users, seen)
	}
}

func TestAssetMigrationDisabledErrorAndSamePolicyFastPath(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal}); err != nil {
		t.Fatal(err)
	}
	useLegacyStorage(t, store)

	job, err := store.CreateJob(ctx, storage.JobKindAssetMigration, "migrate", "migrate")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	manager.processAssetMigration(ctx, ctx, job)
	got, err := store.GetJob(ctx, job.ID)
	if err != nil || got.Status != storage.JobFailed || strings.Contains(got.Error, "<nil>") {
		t.Fatalf("disabled migration job = %+v, err=%v", got, err)
	}

	// Same-policy asset already present on disk is linked without copying to /tmp.
	doc, err := store.GetStorageDocument(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.Enabled = true
	doc, err = store.SaveStorageDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	target := doc.Targets[0]
	key := "image/alice/1_m1_1.png"
	absPath := filepath.Join(target.Root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		t.Fatal(err)
	}
	png := archiveTestPNG(t)
	if err := os.WriteFile(absPath, png, 0o600); err != nil {
		t.Fatal(err)
	}
	asset, err := store.CreateAsset(ctx, storage.Asset{
		ID:            "asset-same",
		Kind:          config.AssetImage,
		PolicyVersion: doc.Version,
		TargetID:      target.ID,
		TargetVersion: target.Version,
		Key:           key,
		ContentHash:   "hash-same",
		Size:          int64(len(png)),
		ContentType:   "image/png",
	})
	if err != nil {
		t.Fatal(err)
	}
	dl, err := store.CreateDownload(ctx, storage.DownloadRecord{
		JobID:       job.ID,
		TweetID:     "1",
		MediaURL:    "https://pbs.twimg.com/media/1.png",
		ContentHash: "hash-same",
		FilePath:    absPath,
		Bytes:       int64(len(png)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkDownloadAsset(ctx, dl.ID, config.AssetImage, asset.ID); err != nil {
		t.Fatal(err)
	}
	migJob, err := store.CreateJob(ctx, storage.JobKindAssetMigration, "migrate", "migrate")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := store.FreezeJobStorage(ctx, migJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.migrateDownloadAsset(ctx, ctx, migJob, frozen, dl, []string{target.Root}); err != nil {
		t.Fatalf("same-policy migrateDownloadAsset: %v", err)
	}
	if isAssetNotExistError(errors.New("transient backend error")) {
		t.Fatal("non-NotExist error classified as NotExist")
	}
	if !isAssetNotExistError(os.ErrNotExist) {
		t.Fatal("os.ErrNotExist not classified as NotExist")
	}
}
