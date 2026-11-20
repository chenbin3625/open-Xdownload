package storage

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestDeleteJobInvalidatesLibraryCacheAndPreservesSharedMediaAssets(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	doc, err := store.SaveStorageDocument(ctx, config.DefaultStorage(filepath.Join(dir, "downloads")))
	if err != nil {
		t.Fatal(err)
	}
	job1, err := store.CreateJob(ctx, JobKindTweetLink, "t1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	job2, err := store.CreateJob(ctx, JobKindTweetLink, "t2", "t2")
	if err != nil {
		t.Fatal(err)
	}
	job1.Status = JobCompleted
	if err := store.UpdateJob(ctx, job1); err != nil {
		t.Fatal(err)
	}
	asset, err := store.CreateAsset(ctx, Asset{
		ID: "shared-asset", Kind: config.AssetImage, PolicyVersion: doc.Version,
		TargetID: "local", TargetVersion: doc.Targets[0].Version,
		Key: "photo.jpg", ContentHash: "abc", Size: 100, ContentType: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMediaAsset(ctx, MediaAsset{MediaKey: "media/shared", Kind: config.AssetImage, AssetID: asset.ID, Status: "ready", JobID: job1.ID}); err != nil {
		t.Fatal(err)
	}
	d1, err := store.CreateDownload(ctx, DownloadRecord{JobID: job1.ID, TweetID: "1", MediaURL: "https://pbs.twimg.com/media/shared.jpg", FilePath: filepath.Join(dir, "downloads", "only1.jpg"), Bytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := store.CreateDownload(ctx, DownloadRecord{JobID: job2.ID, TweetID: "2", MediaURL: "https://pbs.twimg.com/media/shared.jpg", FilePath: filepath.Join(dir, "downloads", "shared.jpg"), Bytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkDownloadAsset(ctx, d1.ID, config.AssetImage, asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkDownloadAsset(ctx, d2.ID, config.AssetImage, asset.ID); err != nil {
		t.Fatal(err)
	}

	// Warm library cache before DeleteJob.
	before, _, err := store.ListLibraryDownloadsWithStatus(ctx, 100)
	if err != nil || len(before) != 2 {
		t.Fatalf("before DeleteJob = %d, err=%v", len(before), err)
	}

	if err := store.DeleteJob(ctx, job1.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	// Library cache must be invalidated immediately.
	after, _, err := store.ListLibraryDownloadsWithStatus(ctx, 100)
	if err != nil || len(after) != 1 {
		t.Fatalf("after DeleteJob = %d, want 1 (err=%v)", len(after), err)
	}

	// Shared media_assets dedup entry must still resolve because job2 still references it.
	kept, err := store.GetMediaAsset(ctx, "media/shared", config.AssetImage)
	if err != nil || kept == nil || kept.ID != asset.ID {
		t.Fatalf("expected shared media asset to survive DeleteJob, got %+v err=%v", kept, err)
	}
}

func TestListJobsPageFilteredMatchesDashboardCounters(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	job.Status = JobCompleted
	job.Error = "已跳过 1 个屏蔽账号"
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	stats, _, err := store.DashboardMeta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListJobsPageFiltered(ctx, 10, 1, JobListFilter{Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Completed != 1 || stats.Failed != 0 {
		t.Fatalf("dashboard stats = %+v", stats)
	}
	if page.Counts.Completed != stats.Completed || page.Counts.Failed != stats.Failed || len(page.Items) != 1 {
		t.Fatalf("filtered page mismatch with dashboard counters: page=%+v stats=%+v", page, stats)
	}
}

func TestListLibraryUsersDeduplicatesFilePathAndCaseInsensitiveFilter(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	job, err := store.CreateJob(ctx, JobKindUser, "Alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID: "100", Text: "hello",
		Author: parser.Author{ID: "u1", ScreenName: "Alice", Name: "Alice"},
	}, "completed"); err != nil {
		t.Fatal(err)
	}
	sharedPath := filepath.Join(dir, "downloads", "same.jpg")
	for _, url := range []string{"https://pbs.twimg.com/media/same.jpg", "https://pbs.twimg.com/media/same.jpg?name=orig"} {
		if _, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "100", MediaURL: url, FilePath: sharedPath, Bytes: 512}); err != nil {
			t.Fatal(err)
		}
	}

	users, err := store.ListLibraryUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].MediaCount != 1 || users[0].TotalBytes != 512 {
		t.Fatalf("expected deduplicated user summary (count=1 bytes=512), got %+v", users)
	}

	page, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{
		Page: math.MaxInt, PageSize: 20, Filter: LibraryFilter{User: "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Pagination.Page != 1 {
		t.Fatalf("expected case-insensitive user match & safe clamped page, got %+v", page)
	}
}
