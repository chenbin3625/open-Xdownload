package jobs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

type spyMediaTimelineSource struct {
	xapi.Source
	mu            sync.Mutex
	stopIDs       []string
	recheckCalls  int
	walkErr       error
	available     int
	primaryRL     error
	archivedUsers []string
	fetchTweet    func(ctx context.Context, id string, opts parser.ParseOptions) (parser.TweetData, error)
}

func (s *spyMediaTimelineSource) HasPrimary() bool { return true }

func (s *spyMediaTimelineSource) Diagnostics() xapi.PoolDiagnostics {
	avail := s.available
	if avail == 0 {
		avail = 1
	}
	return xapi.PoolDiagnostics{Total: avail, Available: avail}
}

func (s *spyMediaTimelineSource) RecheckDisabledClients(context.Context) bool {
	s.mu.Lock()
	s.recheckCalls++
	s.mu.Unlock()
	return true
}

func (s *spyMediaTimelineSource) GetUserByIDAsPrimary(_ context.Context, id string) (xapi.User, error) {
	if s.primaryRL != nil {
		return xapi.User{}, s.primaryRL
	}
	return xapi.User{ID: id, ScreenName: "priv", Protected: true, Following: true}, nil
}

func (s *spyMediaTimelineSource) WalkUserMedia(_ context.Context, user xapi.User, opts xapi.UserMediaTimelineOptions, save func(xapi.UserMediaTimelinePage) error) (xapi.MediaFetchStats, error) {
	s.mu.Lock()
	s.stopIDs = append(s.stopIDs, opts.StopID)
	s.archivedUsers = append(s.archivedUsers, user.ScreenName)
	walkErr := s.walkErr
	s.mu.Unlock()
	if walkErr != nil {
		return xapi.MediaFetchStats{Requests: 1}, walkErr
	}
	if err := save(xapi.UserMediaTimelinePage{Complete: true}); err != nil {
		return xapi.MediaFetchStats{Requests: 1, Pages: 1}, err
	}
	return xapi.MediaFetchStats{Requests: 1, Pages: 1}, nil
}

func (s *spyMediaTimelineSource) FetchTweet(ctx context.Context, id string, opts parser.ParseOptions) (parser.TweetData, error) {
	if s.fetchTweet != nil {
		return s.fetchTweet(ctx, id, opts)
	}
	return parser.TweetData{}, errors.New("not implemented")
}

func TestManualIncrementalArchiveConfigAndBackfillOverride(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "inc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	dir := t.TempDir()
	cfg := config.AppConfig{DownloadDir: dir, StorageType: config.StorageLocal, IncrementalArchive: true}
	if _, err := store.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	src := &spyMediaTimelineSource{}
	mgr := NewManager(store, parser.NewService(), NewEventBus())
	user := xapi.User{ID: "u1", ScreenName: "alice", Name: "Alice", MediaCount: 5}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	entity, _, err := mgr.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserEntityLastSeenTweet(ctx, entity.ID, "500"); err != nil {
		t.Fatal(err)
	}

	// Manual job with IncrementalArchive=true and Backfill=false must pass StopID="500".
	manualJob, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "alice", ArchiveMode: storage.ArchiveMedia})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.archiveUserWithBudget(ctx, ctx, manualJob, cfg, src, user, nil, nil, nil); err != nil {
		t.Fatalf("manual incremental archive: %v", err)
	}
	if len(src.stopIDs) != 1 || src.stopIDs[0] != "500" {
		t.Fatalf("manual incremental stopIDs = %v, want [500]", src.stopIDs)
	}

	// Backfill=true must override IncrementalArchive and do full scan (StopID="").
	backfillJob, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "alice", ArchiveMode: storage.ArchiveMedia, Backfill: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.archiveUserWithBudget(ctx, ctx, backfillJob, cfg, src, user, nil, nil, nil); err != nil {
		t.Fatalf("backfill archive: %v", err)
	}
	if len(src.stopIDs) != 2 || src.stopIDs[1] != "" {
		t.Fatalf("backfill stopIDs = %v, want [500, \"\"]", src.stopIDs)
	}
}

func TestArchiveMediaTimelineSkipsRecheckWhenStartCursorEmpty(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "recheck.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	dir := t.TempDir()
	user := xapi.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	entity, err := store.EnsureUserEntity(ctx, user.ID, filepath.Join(dir, "users"), "Alice(alice)")
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "alice", ArchiveMode: storage.ArchiveMedia})
	if err != nil {
		t.Fatal(err)
	}
	src := &spyMediaTimelineSource{walkErr: &xapi.HTTPError{StatusCode: http.StatusUnauthorized}}
	mgr := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: dir, StorageType: config.StorageLocal}

	_, err = mgr.archiveMediaTimeline(ctx, ctx, job, cfg, src, user, entity, filepath.Join(entity.ParentDir, entity.Name), parser.ParseOptions{}, nil, nil)
	if !xapi.IsUnauthorized(err) {
		t.Fatalf("err = %v, want 401", err)
	}
	if src.recheckCalls != 0 {
		t.Fatalf("recheckCalls = %d, want 0 when startCursor is empty", src.recheckCalls)
	}
}

func TestArchiveUsersDefersPrimaryRateLimitedUserWhileArchivingPublicUsers(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "defer-primary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	dir := t.TempDir()
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindList, Input: "L1", ArchiveMode: storage.ArchiveMedia})
	if err != nil {
		t.Fatal(err)
	}
	limited := &xapi.RateLimitedError{
		RetryAt:       time.Now().Add(5 * time.Minute),
		PoolExhausted: true,
		Cause:         &xapi.HTTPError{StatusCode: http.StatusTooManyRequests},
	}
	src := &spyMediaTimelineSource{available: 2, primaryRL: limited}
	mgr := NewManager(store, parser.NewService(), NewEventBus())
	cfg := config.AppConfig{DownloadDir: dir, StorageType: config.StorageLocal}

	users := []xapi.User{
		{ID: "p1", ScreenName: "priv", Protected: true, Following: true, MediaCount: 10},
		{ID: "u1", ScreenName: "pub1", MediaCount: 5},
		{ID: "u2", ScreenName: "pub2", MediaCount: 3},
	}
	_, err = mgr.archiveUsers(ctx, ctx, job, cfg, src, users, nil, 0, 1)
	if !errors.Is(err, limited) {
		t.Fatalf("err = %v, want primary RateLimitedError after public users finish", err)
	}
	if len(src.archivedUsers) != 2 || src.archivedUsers[0] != "pub1" || src.archivedUsers[1] != "pub2" {
		t.Fatalf("archivedUsers = %v, want [pub1 pub2] completed before yielding for primary", src.archivedUsers)
	}
	// Checkpoint must reflect public users done and only the deferred protected user remaining!
	cp, ok, err := mgr.loadArchiveCheckpoint(ctx, job.ID)
	if err != nil || !ok {
		t.Fatalf("checkpoint load: ok=%v err=%v", ok, err)
	}
	if cp.Next != 2 || len(cp.Tasks) != 3 || cp.Tasks[2].User.ScreenName != "priv" {
		t.Fatalf("checkpoint = next:%d tasks:%+v, want next=2 with priv at index 2", cp.Next, cp.Tasks)
	}
}

func TestArchiveTweetsBatchDownloadsConcurrentlyAndDeterministically(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "concurrent-batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	allStarted := make(chan struct{})
	var startOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		if cur >= 2 {
			startOnce.Do(func() { close(allStarted) })
		}
		select {
		case <-allStarted:
		case <-time.After(400 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("img-" + r.URL.Path))
	}))
	defer server.Close()

	dir := t.TempDir()
	cfg := config.AppConfig{DownloadDir: dir, StorageType: config.StorageLocal, MaxFilenameLength: 120}
	if _, err := store.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(store, parser.NewService(), NewEventBus())
	mgr.downloader = downloader.NewWithAllowedHostForTest(func(string) bool { return true })

	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "alice", ArchiveMode: storage.ArchiveMedia})
	if err != nil {
		t.Fatal(err)
	}
	user := xapi.User{ID: "u1", ScreenName: "alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	entity, folderPath, err := mgr.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(folderPath, 0o700); err != nil {
		t.Fatal(err)
	}
	tweets := make([]parser.TweetData, 4)
	for i := range tweets {
		id := fmt.Sprintf("100%d", i)
		tweets[i] = parser.TweetData{
			ID:        id,
			Text:      "post " + id,
			CreatedAt: time.Date(2026, 11, 1, 0, 0, i, 0, time.UTC),
			Author:    parser.Author{ID: "u1", ScreenName: "alice"},
			Media:     []parser.Media{{ID: "m" + id, Type: parser.MediaPhoto, URL: server.URL + "/" + id + ".jpg"}},
		}
	}

	stats, err := mgr.archiveTweetsBatch(ctx, ctx, job, cfg, user, entity, folderPath, tweets, nil)
	if err != nil {
		t.Fatalf("archiveTweetsBatch: %v", err)
	}
	if stats.Downloaded != 4 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want 4 downloaded, 0 failed", stats)
	}
	if maxInFlight.Load() < 2 {
		t.Fatalf("maxInFlight = %d, want >= 2 concurrent CDN downloads", maxInFlight.Load())
	}
}

func TestProcessTweetLinkArchiveMediaFallsBackToAuthGraphQL(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "tweet-fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	mediaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/photo.jpg" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("protected-photo"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mediaServer.Close()

	dir := t.TempDir()
	cfg := config.AppConfig{
		DownloadDir: dir,
		StorageType: config.StorageLocal,
		ProxyURL:    mediaServer.URL, // causes syndication request to get 404
	}
	if _, err := store.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(store, parser.NewService(), NewEventBus())
	mgr.downloader = downloader.NewWithAllowedHostForTest(func(string) bool { return true })
	var fetchedID string
	mgr.SetXSourceFactory(func(context.Context, config.AppConfig) (xapi.Source, error) {
		return &spyMediaTimelineSource{
			available: 1,
			fetchTweet: func(_ context.Context, id string, _ parser.ParseOptions) (parser.TweetData, error) {
				fetchedID = id
				return parser.TweetData{
					ID:        id,
					Text:      "protected media",
					CreatedAt: time.Now().UTC(),
					Author:    parser.Author{ID: "u1", ScreenName: "alice"},
					Media:     []parser.Media{{ID: "m1", Type: parser.MediaPhoto, URL: mediaServer.URL + "/photo.jpg"}},
				}, nil
			},
		}, nil
	})

	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind:        storage.JobKindTweetLink,
		Input:       "https://x.com/alice/status/987654321",
		ArchiveMode: storage.ArchiveMedia,
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.processTweetLink(ctx, ctx, job)
	updated, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != storage.JobCompleted {
		t.Fatalf("job status = %s err=%q msg=%q, want completed via auth GraphQL fallback", updated.Status, updated.Error, updated.Message)
	}
	if fetchedID != "987654321" {
		t.Fatalf("fetchedID = %q, want 987654321", fetchedID)
	}
}
