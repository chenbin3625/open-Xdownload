package jobs

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
	"github.com/minio/minio-go/v7"
)

func archiveTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	img.Set(3, 3, color.RGBA{R: 200, G: 90, B: 40, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func archiveMediaRefreshClient(t *testing.T, store *storage.Store, cfg config.AppConfig, handler http.HandlerFunc) config.AppConfig {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	cfg.ProxyURL = server.URL
	transport, err := httpx.Transport(cfg.ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.Contains(address, "twimg.com:") {
			address = server.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	t.Cleanup(transport.CloseIdleConnections)
	cfg, err = store.UpdateConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestArchiveBodySurvivesMediaFailureAndOfflineRetryDeduplicates(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: t.TempDir(), MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	cfg = archiveMediaRefreshClient(t, store, cfg, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "offline", http.StatusServiceUnavailable) })
	document := config.DefaultStorage(t.TempDir())
	document.Enabled = true
	if _, err := store.SaveStorageDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	data := archiveTestPNG(t)
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	manager := NewManager(store, parser.NewService(), NewEventBus())
	useTestDownloader(t, manager)
	if _, err := store.UpsertUser(ctx, storage.User{ID: "42", ScreenName: "author"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	entity, err := store.EnsureUserEntity(ctx, "42", cfg.DownloadDir, "author")
	if err != nil {
		t.Fatal(err)
	}
	tweet := parser.TweetData{ID: "123", URL: "https://x.com/author/status/123", Text: "Body persists while media is offline", Author: parser.Author{ID: "42", ScreenName: "author"}, CreatedAt: time.Now().UTC(), Media: []parser.Media{{ID: "p1", Type: parser.MediaPhoto, URL: server.URL + "/photo.png"}}}
	tweet.QuotedTweetID = "122"
	tweet.Related = []parser.TweetData{{ID: "122", URL: "https://x.com/quoted/status/122", Text: "Quoted media", Author: parser.Author{ID: "43", ScreenName: "quoted"}, CreatedAt: tweet.CreatedAt, Media: tweet.Media}}
	stats, err := manager.archiveTweets(ctx, ctx, job, cfg, xclient.User{ID: "42", ScreenName: "author"}, entity, cfg.DownloadDir, []parser.TweetData{tweet}, nil)
	if err != nil || stats.Failed != 1 {
		t.Fatalf("media failure: %+v %v", stats, err)
	}
	archived, err := store.GetTweet(ctx, tweet.ID)
	if err != nil || archived.Text != tweet.Text || archived.MediaStatus != "partial" {
		t.Fatalf("body lost: %+v %v", archived, err)
	}
	failedRelated, err := store.GetTweet(ctx, "122")
	if err != nil || failedRelated.MediaStatus != "failed" {
		t.Fatalf("related failure not retryable: %+v %v", failedRelated, err)
	}
	fail.Store(false)
	retry, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindTweetLink, Input: tweet.URL, ArchiveMode: storage.ArchiveMedia})
	if err != nil {
		t.Fatal(err)
	}
	manager.processTweetLink(ctx, ctx, retry)
	gotJob, err := store.GetJob(ctx, retry.ID)
	if err != nil || gotJob.Status != storage.JobCompleted {
		t.Fatalf("offline retry: %+v %v", gotJob, err)
	}
	if remaining, err := store.CountFailedTweets(ctx); err != nil || remaining != 0 {
		t.Fatalf("successful retry retained stale failure: %d %v", remaining, err)
	}
	related, err := store.GetTweet(ctx, "122")
	if err != nil || len(related.Downloads) != 1 || related.MediaStatus != "completed" {
		t.Fatalf("related media association: %+v %v", related, err)
	}
	afterRetry := calls.Load()
	for _, id := range []string{"123", "124"} {
		tweet.ID = id
		if _, err := manager.archiveTweets(ctx, ctx, job, cfg, xclient.User{ID: "42"}, entity, cfg.DownloadDir, []parser.TweetData{tweet}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != afterRetry {
		t.Fatal("duplicate media was downloaded again")
	}
	first, err := store.GetTweet(ctx, "123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.GetTweet(ctx, "124")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Downloads) != 1 || len(second.Downloads) != 1 {
		t.Fatalf("tweet associations lost: %d %d", len(first.Downloads), len(second.Downloads))
	}
	one, err := store.DownloadAssets(ctx, first.Downloads[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.DownloadAssets(ctx, second.Downloads[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if one[config.AssetImage] == "" || one[config.AssetImage] != two[config.AssetImage] {
		t.Fatal("file dedup did not preserve both tweet associations")
	}
	relatedAssets, err := store.DownloadAssets(ctx, related.Downloads[0].ID)
	if err != nil || relatedAssets[config.AssetImage] != one[config.AssetImage] {
		t.Fatalf("related tweet lost shared asset: %+v %v", relatedAssets, err)
	}
}

func TestMediaRefreshKeepsOriginalItemsMissingFromPartialResponse(t *testing.T) {
	original := []parser.Media{{ID: "a", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/old.png"}, {ID: "b", Type: parser.MediaVideo, BestURL: "https://video.twimg.com/ext_tw_video/2/old.mp4"}}
	fresh := []parser.Media{{ID: "a", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/new.png"}}
	merged := mergeRefreshedMedia(original, fresh)
	if len(merged) != 2 || merged[0].URL != fresh[0].URL || merged[1].BestURL != original[1].BestURL {
		t.Fatalf("partial refresh dropped requested media: %+v", merged)
	}
}

func TestMediaRetryRefreshesURLsAndPreservesArchivedBody(t *testing.T) {
	for _, mode := range []string{"single", "queue", "empty-refresh"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, err := storage.Open(filepath.Join(t.TempDir(), "refresh.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cfg, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: t.TempDir(), MaxConcurrency: 1})
			if err != nil {
				t.Fatal(err)
			}
			var oldCalls, newCalls atomic.Int32
			cfg = archiveMediaRefreshClient(t, store, cfg, func(w http.ResponseWriter, r *http.Request) {
				if r.Host == "cdn.syndication.twimg.com" {
					if mode == "empty-refresh" {
						fmt.Fprint(w, `{"id_str":"123","text":"truncated","user":{"id_str":"42","screen_name":"author"}}`)
					} else {
						fmt.Fprint(w, `{"id_str":"123","text":"truncated","user":{"id_str":"42","screen_name":"author"},"mediaDetails":[{"id_str":"photo","type":"photo","media_url_https":"https://pbs.twimg.com/media/fresh.png"}]}`)
					}
					return
				}
				if strings.Contains(r.URL.Path, "fresh") {
					newCalls.Add(1)
				} else {
					oldCalls.Add(1)
					if mode != "empty-refresh" {
						http.Error(w, "expired", http.StatusForbidden)
						return
					}
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(archiveTestPNG(t))
			})
			job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveBoth})
			if err != nil {
				t.Fatal(err)
			}
			tweet := parser.TweetData{ID: "123", URL: "https://x.com/author/status/123", Text: strings.Repeat("complete original body ", 100), Author: parser.Author{ID: "42", ScreenName: "author"}, CreatedAt: time.Now().UTC(), ReplyToID: "100", Media: []parser.Media{{ID: "photo", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/expired.png"}}}
			if err := store.ArchiveTweet(ctx, job.ID, tweet, "failed"); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(store, parser.NewService(), NewEventBus())
			if mode == "queue" {
				if _, err := store.UpsertUser(ctx, storage.User{ID: "42", ScreenName: "author"}); err != nil {
					t.Fatal(err)
				}
				entity, err := store.EnsureUserEntity(ctx, "42", cfg.DownloadDir, "author")
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.rememberFailedTweet(ctx, job, entity, tweet, fmt.Errorf("expired")); err != nil {
					t.Fatal(err)
				}
				retry, err := store.CreateJob(ctx, storage.JobKindFailedRetry, "failed-tweets", "retry")
				if err != nil {
					t.Fatal(err)
				}
				if count, err := manager.retryFailedTweets(ctx, ctx, retry, cfg, true); err != nil || count != 1 {
					t.Fatalf("queue refresh: %d %v", count, err)
				}
			} else {
				retry, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindTweetLink, Input: tweet.URL, ArchiveMode: storage.ArchiveMedia})
				if err != nil {
					t.Fatal(err)
				}
				manager.processTweetLink(ctx, ctx, retry)
				result, err := store.GetJob(ctx, retry.ID)
				if err != nil || result.Status != storage.JobCompleted {
					t.Fatalf("single refresh: %+v %v", result, err)
				}
			}
			archived, err := store.GetTweet(ctx, tweet.ID)
			if err != nil || archived.Text != tweet.Text || archived.ReplyToID != tweet.ReplyToID || len(archived.Downloads) != 1 {
				t.Fatalf("archive changed during media refresh: %+v %v", archived, err)
			}
			if mode == "empty-refresh" {
				if oldCalls.Load() != 1 || newCalls.Load() != 0 {
					t.Fatal("empty refresh discarded the offline media")
				}
			} else if newCalls.Load() != 1 || oldCalls.Load() != 0 {
				t.Fatalf("stale URL retried: old=%d new=%d", oldCalls.Load(), newCalls.Load())
			}
		})
	}
}

func TestMediaOnlyArchiveKeepsMediaStatusAndSupportsRetry(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "media-only.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: t.TempDir(), MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	cfg = archiveMediaRefreshClient(t, store, cfg, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "offline", http.StatusServiceUnavailable) })
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(archiveTestPNG(t))
	}))
	defer server.Close()
	if _, err := store.UpsertUser(ctx, storage.User{ID: "42", ScreenName: "author"}); err != nil {
		t.Fatal(err)
	}
	entity, err := store.EnsureUserEntity(ctx, "42", cfg.DownloadDir, "author")
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveMedia})
	if err != nil {
		t.Fatal(err)
	}
	tweet := parser.TweetData{ID: "1", URL: "https://x.com/author/status/1", Text: "body is not requested", Author: parser.Author{ID: "42", ScreenName: "author"}, CreatedAt: time.Now().UTC(), Media: []parser.Media{{ID: "photo-1", Type: parser.MediaPhoto, URL: server.URL + "/photo.png"}}}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	useTestDownloader(t, manager)
	stats, err := manager.archiveTweets(ctx, ctx, job, cfg, xclient.User{ID: "42", ScreenName: "author"}, entity, cfg.DownloadDir, []parser.TweetData{tweet}, nil)
	if err != nil || stats.Failed != 1 {
		t.Fatalf("media-only failure: %+v %v", stats, err)
	}
	archived, err := store.GetTweet(ctx, tweet.ID)
	if err != nil || archived.BodyStatus != "not_requested" || archived.MediaStatus != "partial" {
		t.Fatalf("media-only status: %+v %v", archived, err)
	}
	if len(archived.JobIDs) != 1 {
		t.Fatalf("media-only job association missing: %+v", archived.JobIDs)
	}
	fail.Store(false)
	retry, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindTweetLink, Input: tweet.URL, ArchiveMode: storage.ArchiveMedia})
	if err != nil {
		t.Fatal(err)
	}
	manager.processTweetLink(ctx, ctx, retry)
	gotJob, err := store.GetJob(ctx, retry.ID)
	if err != nil || gotJob.Status != storage.JobCompleted {
		t.Fatalf("media-only retry: %+v %v", gotJob, err)
	}
	archived, err = store.GetTweet(ctx, tweet.ID)
	if err != nil || archived.BodyStatus != "not_requested" || archived.MediaStatus != "completed" || len(archived.Downloads) != 1 {
		t.Fatalf("media-only retry status: %+v %v", archived, err)
	}
}

func TestNewPolicyRelocatesExistingMediaAsset(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	legacyRoot, currentRoot := t.TempDir(), t.TempDir()
	cfg, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: legacyRoot, MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	legacy := config.DefaultStorage(legacyRoot)
	legacy.Enabled = true
	legacy, err = store.SaveStorageDocument(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	oldJob, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	legacyRule, legacyTarget, err := legacy.Rule(config.AssetImage)
	if err != nil {
		t.Fatal(err)
	}
	legacyKey, err := filestore.TemplatePath(legacyRule, config.AssetImage, parser.TweetData{ID: "tweet-1", Author: parser.Author{ID: "author", ScreenName: "author"}}, parser.Media{ID: "media-1", Type: parser.MediaPhoto}, 0, "png")
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyTarget.Root, filepath.FromSlash(legacyKey))
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, archiveTestPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	oldAsset, err := store.CreateAsset(ctx, storage.Asset{ID: "old-asset", Kind: config.AssetImage, PolicyVersion: legacy.Version, TargetID: legacyTarget.ID, TargetVersion: legacyTarget.Version, Key: legacyKey, ContentHash: "old-hash", Size: int64(len(archiveTestPNG(t))), ContentType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	mediaURL := "https://pbs.twimg.com/media/media-1.png"
	mediaKey := downloader.MediaIdentity(mediaURL)
	if err := store.SetMediaAsset(ctx, storage.MediaAsset{MediaKey: mediaKey, Kind: config.AssetImage, AssetID: oldAsset.ID, Status: "ready", JobID: oldJob.ID}); err != nil {
		t.Fatal(err)
	}
	tweet := parser.TweetData{ID: "tweet-1", URL: "https://x.com/author/status/1", Text: "body", Author: parser.Author{ID: "author", ScreenName: "author"}, CreatedAt: time.Now().UTC(), Media: []parser.Media{{ID: "media-1", Type: parser.MediaPhoto, URL: mediaURL}}}
	if err := store.RememberMediaContext(ctx, tweet); err != nil {
		t.Fatal(err)
	}
	current := legacy
	current.Targets = []config.StorageTarget{{ID: "local", Name: "Current", Type: "local", Root: currentRoot}}
	current.Default.TargetID = "local"
	current, err = store.SaveStorageDocument(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	result, err := manager.downloadAssetMedia(ctx, ctx, job, cfg, current, mediaURL, tweet.ID, true, tweet.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.skipped {
		t.Fatal("relocating an old-policy asset should create a current-policy asset")
	}
	records, err := store.FindDownloadsByMediaKey(ctx, mediaKey, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("download records = %d, want 1", len(records))
	}
	links, err := store.DownloadAssets(ctx, records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	newID := links[config.AssetImage]
	if newID == "" || newID == oldAsset.ID {
		t.Fatalf("download linked old asset: %+v", links)
	}
	newAsset, err := store.GetAsset(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	if newAsset.PolicyVersion != current.Version || newAsset.TargetID != "local" {
		t.Fatalf("new asset policy = %+v, want version %d on local", newAsset, current.Version)
	}
	if _, _, err := filestore.OpenAsset(ctx, legacyTarget, legacyKey); err != nil {
		t.Fatalf("historical asset was lost: %v", err)
	}
}

func TestFailedQueueRetryUpdatesArchivedMediaResult(t *testing.T) {
	for _, scenario := range []struct {
		name, status string
		code         int
		partial      bool
		empty        bool
		wantRetried  int
	}{
		{name: "success", code: http.StatusOK, status: "completed", wantRetried: 1},
		{name: "permanent", code: http.StatusNotFound, status: "failed"},
		{name: "partial", code: http.StatusGone, status: "partial", partial: true},
		{name: "empty", status: "failed", empty: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := storage.Open(filepath.Join(t.TempDir(), "retry.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cfg, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: t.TempDir(), MaxConcurrency: 1})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/available.png" || scenario.code == http.StatusOK {
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write([]byte("media bytes"))
					return
				}
				http.Error(w, "removed", scenario.code)
			}))
			defer server.Close()
			if _, err := store.UpsertUser(ctx, storage.User{ID: "42", ScreenName: "author"}); err != nil {
				t.Fatal(err)
			}
			entity, err := store.EnsureUserEntity(ctx, "42", cfg.DownloadDir, "author")
			if err != nil {
				t.Fatal(err)
			}
			original, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveBoth})
			if err != nil {
				t.Fatal(err)
			}
			tweet := parser.TweetData{ID: "123", Text: "Saved body", Author: parser.Author{ID: "42", ScreenName: "author"}, CreatedAt: time.Now(), Media: []parser.Media{{ID: "failed", Type: parser.MediaPhoto, URL: server.URL + "/failed.png"}}}
			if scenario.partial {
				tweet.Media = append([]parser.Media{{ID: "available", Type: parser.MediaPhoto, URL: server.URL + "/available.png"}}, tweet.Media...)
			}
			if scenario.empty {
				tweet.Media = nil
			}
			if err := store.ArchiveTweet(ctx, original.ID, tweet, "partial"); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(store, parser.NewService(), NewEventBus())
			useTestDownloader(t, manager)
			if err := manager.rememberFailedTweet(ctx, original, entity, tweet, fmt.Errorf("offline")); err != nil {
				t.Fatal(err)
			}
			if scenario.code != http.StatusOK && !scenario.empty {
				if _, err := store.CreateFailedMedia(ctx, storage.FailedMedia{JobID: original.ID, MediaURL: server.URL + "/failed.png", Error: "offline"}); err != nil {
					t.Fatal(err)
				}
			}
			retry, err := store.CreateJob(ctx, storage.JobKindFailedRetry, "failed-tweets", "retry")
			if err != nil {
				t.Fatal(err)
			}
			count, err := manager.retryFailedTweets(ctx, ctx, retry, cfg, true)
			if err != nil || count != scenario.wantRetried {
				t.Fatalf("retry count=%d want=%d err=%v", count, scenario.wantRetried, err)
			}
			archived, err := store.GetTweet(ctx, tweet.ID)
			if err != nil || archived.MediaStatus != scenario.status || archived.Text != tweet.Text {
				t.Fatalf("archived result: %+v %v", archived, err)
			}
			remaining, err := store.ListFailedTweets(ctx, 10)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("queue did not settle: %+v %v", remaining, err)
			}
		})
	}
}

func TestFourAssetCategoriesAndHistoricalMigrationRecovery(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	legacyRoot := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: legacyRoot, MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	oldJob, err := store.CreateJob(ctx, storage.JobKindMediaURL, "legacy", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	photoPath := filepath.Join(legacyRoot, "old.png")
	if err := os.WriteFile(photoPath, archiveTestPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(legacyRoot, "old.mp4")
	command := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=green:s=64x48:d=0.4", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-threads", "1", videoPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("video fixture: %v %s", err, output)
	}
	photo, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: oldJob.ID, TweetID: "10", MediaURL: "https://pbs.twimg.com/media/old.png", FilePath: photoPath})
	if err != nil {
		t.Fatal(err)
	}
	video, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: oldJob.ID, TweetID: "11", MediaURL: "https://video.twimg.com/old.mp4", FilePath: videoPath})
	if err != nil {
		t.Fatal(err)
	}
	document := config.DefaultStorage(t.TempDir())
	document.Enabled = true
	document.Targets = nil
	for _, kind := range config.AssetKinds {
		target := config.StorageTarget{ID: string(kind), Type: "local", Root: t.TempDir()}
		if kind == config.AssetVideo {
			target.Type = "nas"
		}
		if kind == config.AssetThumbnail && os.Getenv("OPEN_XDOWNLOAD_TEST_S3_ENDPOINT") != "" {
			target = config.StorageTarget{ID: string(kind), Type: "s3", Endpoint: os.Getenv("OPEN_XDOWNLOAD_TEST_S3_ENDPOINT"), Region: "us-east-1", Bucket: "archive-integration", AccessKey: os.Getenv("OPEN_XDOWNLOAD_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("OPEN_XDOWNLOAD_TEST_S3_SECRET_KEY")}
			client, err := filestore.S3Client(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.MakeBucket(ctx, target.Bucket, minio.MakeBucketOptions{Region: target.Region}); err != nil {
				if exists, _ := client.BucketExists(ctx, target.Bucket); !exists {
					t.Fatal(err)
				}
			}
		}
		document.Targets = append(document.Targets, target)
		document.Rules[kind] = config.AssetRule{TargetID: target.ID, Prefix: "root-" + string(kind), DirectoryTemplate: "{year}/{tweet_id}", FilenameTemplate: "{kind}_{media_id}_{index}.{ext}"}
	}
	document.Default = document.Rules[config.AssetImage]
	document, err = store.SaveStorageDocument(ctx, document)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	job, err := store.CreateJob(ctx, storage.JobKindAssetMigration, "historical-assets", "migration")
	if err != nil {
		t.Fatal(err)
	}
	manager.processAssetMigration(ctx, ctx, job)
	got, err := store.GetJob(ctx, job.ID)
	if err != nil || got.Status != storage.JobCompleted {
		t.Fatalf("migration: %+v %v", got, err)
	}
	imageLinks, err := store.DownloadAssets(ctx, photo.ID)
	if err != nil {
		t.Fatal(err)
	}
	videoLinks, err := store.DownloadAssets(ctx, video.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, target, err := document.Rule(config.AssetThumbnail); err == nil && target.Type == "s3" {
		client, err := filestore.S3Client(target)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{imageLinks[config.AssetThumbnail], videoLinks[config.AssetThumbnail]} {
			asset, err := store.GetAsset(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			defer client.RemoveObject(ctx, target.Bucket, asset.Key, minio.RemoveObjectOptions{})
		}
	}
	for _, kind := range config.AssetKinds {
		id := videoLinks[kind]
		if kind == config.AssetImage {
			id = imageLinks[kind]
		}
		asset, err := store.GetAsset(ctx, id)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		_, target, err := document.Rule(kind)
		if err != nil {
			t.Fatal(err)
		}
		if asset.TargetID != target.ID {
			t.Fatalf("%s stored at wrong target: %+v", kind, asset)
		}
		file, _, err := filestore.OpenAsset(ctx, target, asset.Key)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(file)
		file.Close()
		if err != nil || len(body) == 0 {
			t.Fatalf("%s unreadable: %v", kind, err)
		}
	}
	manager.processAssetMigration(ctx, ctx, job)
	recovered, err := store.DownloadAssets(ctx, video.ID)
	if err != nil || recovered[config.AssetVideo] != videoLinks[config.AssetVideo] || recovered[config.AssetVideoCover] != videoLinks[config.AssetVideoCover] {
		t.Fatal("repeat migration changed completed associations")
	}
	if _, err := os.Stat(photoPath); err != nil {
		t.Fatal("legacy source removed")
	}
	if _, err := os.Stat(videoPath); err != nil {
		t.Fatal("legacy video removed")
	}
	oldImage, err := store.GetAsset(ctx, imageLinks[config.AssetImage])
	if err != nil {
		t.Fatal(err)
	}
	_, oldTarget, err := document.Rule(config.AssetImage)
	if err != nil {
		t.Fatal(err)
	}
	document.Targets = append([]config.StorageTarget(nil), document.Targets...)
	for i := range document.Targets {
		document.Targets[i].Root = t.TempDir()
	}
	document, err = store.SaveStorageDocument(ctx, document)
	if err != nil {
		t.Fatal(err)
	}
	newJob, err := store.CreateJob(ctx, storage.JobKindAssetMigration, "historical-assets", "new policy")
	if err != nil {
		t.Fatal(err)
	}
	manager.processAssetMigration(ctx, ctx, newJob)
	newLinks, err := store.DownloadAssets(ctx, photo.ID)
	if err != nil || newLinks[config.AssetImage] == oldImage.ID {
		t.Fatalf("new policy did not relocate asset: %+v %v", newLinks, err)
	}
	currentImage, err := store.GetAsset(ctx, newLinks[config.AssetImage])
	if err != nil || currentImage.PolicyVersion != document.Version {
		t.Fatalf("new version not recorded: %+v %v", currentImage, err)
	}
	oldFile, _, err := filestore.OpenAsset(ctx, oldTarget, oldImage.Key)
	if err != nil {
		t.Fatalf("historical asset became unreadable: %v", err)
	}
	oldFile.Close()
	_, currentTarget, err := document.Rule(config.AssetImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(currentTarget.Root, filepath.FromSlash(currentImage.Key))); err != nil {
		t.Fatal(err)
	}
	repairJob, err := store.CreateJob(ctx, storage.JobKindAssetMigration, "historical-assets", "repair missing assets")
	if err != nil {
		t.Fatal(err)
	}
	manager.processAssetMigration(ctx, ctx, repairJob)
	got, err = store.GetJob(ctx, repairJob.ID)
	if err != nil || got.Status != storage.JobCompleted {
		t.Fatalf("missing asset fallback: %+v %v", got, err)
	}
	repaired, err := store.DownloadAssets(ctx, photo.ID)
	if err != nil || repaired[config.AssetImage] != currentImage.ID {
		t.Fatalf("repair changed the stable asset ID: %+v %v", repaired, err)
	}
	repairedFile, _, err := filestore.OpenAsset(ctx, currentTarget, currentImage.Key)
	if err != nil {
		t.Fatalf("missing current asset was not repaired from retained source: %v", err)
	}
	repairedFile.Close()
}

func TestMigrationCheckpointResumesAndReportsMissingSources(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatal("ffmpeg is required for asset migration verification")
	}
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "resume.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatal(err)
	}
	document := config.DefaultStorage(t.TempDir())
	document.Enabled = true
	if _, err := store.SaveStorageDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	oldJob, err := store.CreateJob(ctx, storage.JobKindMediaURL, "old", "old")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: oldJob.ID, TweetID: "1", MediaURL: "https://example.com/already-processed.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "image.avif")
	if err := os.WriteFile(filePath, archiveTestPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: oldJob.ID, TweetID: "2", MediaURL: "https://example.com/image.avif", FilePath: filePath})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: oldJob.ID, TweetID: "3", MediaURL: "https://example.com/missing.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindAssetMigration, "historical-assets", "resume")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.GetAssetMigrationProgress(ctx, job.ID)
	if err != nil || state.Total != 3 || state.MaxID != empty.ID {
		t.Fatalf("migration snapshot: %+v %v", state, err)
	}
	state.LastID, state.Processed, state.Failed = first.ID, 1, 1
	if err := store.SaveAssetMigrationProgress(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store, parser.NewService(), NewEventBus())
	manager.processAssetMigration(ctx, ctx, job)
	result, err := store.GetJob(ctx, job.ID)
	if err != nil || result.Status != storage.JobCompletedWithErrors || result.Progress != 1 {
		t.Fatalf("resumed migration: %+v %v", result, err)
	}
	state, err = store.GetAssetMigrationProgress(ctx, job.ID)
	if err != nil || state.LastID != empty.ID || state.Processed != 3 || state.Failed != 2 {
		t.Fatalf("checkpoint reset on reopen: %+v %v", state, err)
	}
	links, err := store.DownloadAssets(ctx, second.ID)
	if err != nil || links[config.AssetImage] == "" || links[config.AssetVideo] != "" {
		t.Fatalf("AVIF classified as video: %+v %v", links, err)
	}
	failures, err := store.ListFailedMediaForJobs(ctx, []int64{job.ID})
	if err != nil || len(failures) != 1 || !strings.Contains(failures[0].Error, "没有资产位置") {
		t.Fatalf("missing source not reported or checkpoint repeated: %+v %v", failures, err)
	}
}
