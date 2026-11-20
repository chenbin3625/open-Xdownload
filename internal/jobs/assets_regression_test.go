package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestDownloadAssetMediaRespectsUnavailableMediaIdentityAndModTime(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var hits atomic.Int32
	imgBytes := archiveTestPNG(t)
	cfg := config.AppConfig{
		DownloadDir:       filepath.Join(dir, "downloads"),
		MaxConcurrency:    1,
		MaxFilenameLength: 120,
		StorageType:       config.StorageLocal,
	}
	cfg = archiveMediaRefreshClient(t, store, cfg, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imgBytes)
	})

	doc := config.DefaultStorage(cfg.DownloadDir)
	doc, err = store.SaveStorageDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindTweetLink, "https://x.com/alice/status/555", "t555")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Pre-blacklisted unavailable media must be skipped without hitting network.
	unavailURL := "https://pbs.twimg.com/media/dead404.png"
	if err := store.UpsertUnavailableMedia(ctx, storage.UnavailableMedia{MediaURL: unavailURL, TweetID: "555", Error: "HTTP 404"}); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(store, nil, nil)
	res, err := mgr.downloadAssetMedia(ctx, ctx, job, cfg, doc, unavailURL, "555", true, time.Now().UTC())
	if err != nil {
		t.Fatalf("unavailable media error: %v", err)
	}
	if !res.skipped || !res.unavailable || hits.Load() != 0 {
		t.Fatalf("expected unavailable skip with 0 network hits, got res=%+v hits=%d", res, hits.Load())
	}

	// 2. Archived tweet has ?format=png&name=small while mediaURL has different query;
	// MediaIdentity match should still resolve media.ID ("mid-99") and index (1 -> "2"),
	// and ModTime should be applied to the saved local asset.
	published := time.Date(2025, 6, 15, 10, 20, 30, 0, time.UTC)
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID:        "555",
		Text:      "hello",
		CreatedAt: published,
		Author:    parser.Author{ID: "u1", ScreenName: "alice", Name: "Alice"},
		Media: []parser.Media{
			{ID: "mid-00", Type: parser.MediaPhoto, BestURL: "https://pbs.twimg.com/media/other.png"},
			{ID: "mid-99", Type: parser.MediaPhoto, BestURL: "https://pbs.twimg.com/media/target99.png?name=small"},
		},
	}, "pending"); err != nil {
		t.Fatal(err)
	}

	res, err = mgr.downloadAssetMedia(ctx, ctx, job, cfg, doc, "https://pbs.twimg.com/media/target99.png", "555", true, published)
	if err != nil {
		t.Fatalf("downloadAssetMedia: %v", err)
	}
	if res.skipped {
		t.Fatal("expected fresh download, got skipped")
	}
	downloads, err := store.ListDownloadsForJobs(ctx, []int64{job.ID})
	if err != nil || len(downloads) != 1 {
		t.Fatalf("downloads = %+v, err=%v", downloads, err)
	}
	base := filepath.Base(downloads[0].FilePath)
	if !strings.Contains(base, "555_mid-99_2.") {
		t.Fatalf("expected filename to contain 555_mid-99_2., got %q", base)
	}
	info, err := os.Stat(downloads[0].FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().UTC().Equal(published) {
		t.Fatalf("file ModTime = %v, want %v", info.ModTime().UTC(), published)
	}
}

func TestFetchPosterImageAcceptsUppercaseContentType(t *testing.T) {
	imgBytes := archiveTestPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "IMAGE/PNG; charset=binary")
		_, _ = w.Write(imgBytes)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "poster.jpg")
	if err := fetchPosterImage(context.Background(), srv.Client(), srv.URL, dest, 1<<20); err != nil {
		t.Fatalf("expected uppercase IMAGE/PNG Content-Type to succeed, got %v", err)
	}
}
