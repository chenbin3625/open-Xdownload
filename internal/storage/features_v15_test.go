package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestV15FeaturesStorageEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads")
	if err := os.MkdirAll(downloadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir:          downloadDir,
		WriteMetadataSidecar: true,
		OfflineThumbnails:    true,
		ArchiveFilter:        &config.ArchiveFilter{ExcludeRetweets: true, MediaTypes: []string{"photo"}},
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || !cfg.WriteMetadataSidecar || !cfg.OfflineThumbnails || cfg.ArchiveFilter == nil || !cfg.ArchiveFilter.ExcludeRetweets {
		t.Fatalf("GetConfig mismatch: %+v err=%v", cfg, err)
	}

	// API Tokens
	tok, plaintext, err := store.CreateAPIToken(ctx, "Userscript", "all", nil)
	if err != nil || plaintext == "" || tok.ID <= 0 {
		t.Fatalf("CreateAPIToken: %v %+v", err, tok)
	}
	ok, err := store.ValidateAPIToken(ctx, plaintext, time.Now(), true)
	if err != nil || !ok {
		t.Fatalf("ValidateAPIToken failed: ok=%v err=%v", ok, err)
	}
	if err := store.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	ok, _ = store.ValidateAPIToken(ctx, plaintext, time.Now(), false)
	if ok {
		t.Fatal("deleted token still valid")
	}

	// Webhooks
	wh, err := store.CreateWebhook(ctx, Webhook{
		Name: "Telegram", Kind: "telegram", URL: "https://api.telegram.org/bot123/sendMessage?chat_id=1",
		Secret: "s3cr3t", Events: []string{"job.failed", "account.disabled"}, Enabled: true,
	})
	if err != nil || wh.ID <= 0 {
		t.Fatalf("CreateWebhook: %v", err)
	}
	if wh.Redacted().Secret != config.SecretPlaceholder {
		t.Fatalf("webhook secret not redacted: %q", wh.Redacted().Secret)
	}

	// Cron Schedule + Bookmarks/Search kinds
	sched, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name:     "Nightly Bookmarks",
		Enabled:  true,
		CronExpr: "0 3 * * *",
		Timezone: "Asia/Shanghai",
		Items: []ArchiveScheduleItem{
			{Kind: JobKindBookmarks, Input: "all", ArchiveMode: ArchiveBoth},
			{Kind: JobKindSearch, Input: "#golang filter:media", ArchiveMode: ArchiveBoth},
		},
	})
	if err != nil || sched.CronExpr != "0 3 * * *" || sched.NextRunAt.IsZero() {
		t.Fatalf("CreateArchiveSchedule cron: %+v err=%v", sched, err)
	}

	// Archive tweets with metrics, alt texts, conversation thread, star, tags, collections, FTS5
	job, err := store.CreateArchiveJob(ctx, JobDraft{
		Kind: JobKindUser, Input: "alice", Title: "alice", ArchiveMode: ArchiveBoth,
		Filter: &config.ArchiveFilter{MediaTypes: []string{"photo"}},
	})
	if err != nil || job.Filter == nil {
		t.Fatalf("CreateArchiveJob with filter: %+v err=%v", job, err)
	}
	t1 := parser.TweetData{
		ID: "1001", URL: "https://x.com/alice/status/1001", Text: "Golang concurrency patterns",
		CreatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), ConversationID: "1001",
		Author:  parser.Author{ID: "u1", Name: "Alice", ScreenName: "alice"},
		Media:   []parser.Media{{ID: "m1", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/1.jpg", AltText: "Goroutine diagram"}},
		Metrics: &parser.TweetMetrics{Likes: 500, Retweets: 100, Views: 12000},
	}
	t2 := parser.TweetData{
		ID: "1002", URL: "https://x.com/alice/status/1002", Text: "Part 2 of the thread",
		CreatedAt: time.Date(2026, 11, 1, 10, 5, 0, 0, time.UTC), ConversationID: "1001", ReplyToID: "1001",
		Author:  parser.Author{ID: "u1", Name: "Alice", ScreenName: "alice"},
		Metrics: &parser.TweetMetrics{Likes: 900, Retweets: 20, Views: 5000},
	}
	if err := store.ArchiveTweet(ctx, job.ID, t1, "completed"); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveTweet(ctx, job.ID, t2, "completed"); err != nil {
		t.Fatal(err)
	}

	// FTS search on alt_text
	page, err := store.ListTweets(ctx, TweetFilter{Search: "Goroutine"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "1001" {
		t.Fatalf("FTS alt_text search failed: %+v err=%v", page, err)
	}
	// Metrics sort
	sorted, err := store.ListTweets(ctx, TweetFilter{Sort: "likes_desc"})
	if err != nil || len(sorted.Items) != 2 || sorted.Items[0].ID != "1002" {
		t.Fatalf("likes_desc sort failed: %+v err=%v", sorted.Items, err)
	}
	// Thread lookup
	thread, err := store.ListConversationTweets(ctx, "1002")
	if err != nil || len(thread) != 2 || thread[0].ID != "1001" || thread[1].ID != "1002" {
		t.Fatalf("ListConversationTweets failed: %+v err=%v", thread, err)
	}

	// Star, Tags, Collections
	tag, err := store.CreateTag(ctx, "GoLang", "cyan")
	if err != nil {
		t.Fatal(err)
	}
	col, err := store.CreateCollection(ctx, "Favorites", "Best posts")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTweetStarred(ctx, "1001", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTweetTags(ctx, "1001", []int64{tag.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCollectionItems(ctx, col.ID, "tweet", []string{"1001"}); err != nil {
		t.Fatal(err)
	}
	starredTrue := true
	filtered, err := store.ListTweets(ctx, TweetFilter{Starred: &starredTrue, TagID: tag.ID, CollectionID: col.ID})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != "1001" || !filtered.Items[0].Starred || len(filtered.Items[0].Tags) != 1 {
		t.Fatalf("star/tag/collection filter failed: %+v err=%v", filtered, err)
	}

	// Downloads, Maintenance & Online Backup
	img1 := filepath.Join(downloadDir, "a.jpg")
	img2 := filepath.Join(downloadDir, "b.jpg")
	if err := os.WriteFile(img1, []byte("same-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img2, []byte("same-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	dl1, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "1001", MediaURL: "https://pbs.twimg.com/media/a.jpg", FilePath: img1, Bytes: 10, ContentHash: "hash1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "1002", MediaURL: "https://pbs.twimg.com/media/b.jpg", FilePath: img2, Bytes: 10, ContentHash: "hash1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDownloadStarred(ctx, dl1.ID, true); err != nil {
		t.Fatal(err)
	}
	stats, err := store.GetStorageStats(ctx)
	if err != nil || stats.TotalTweets != 2 || stats.DuplicateGroups != 1 {
		t.Fatalf("GetStorageStats: %+v err=%v", stats, err)
	}
	dedupRes, err := store.RunMaintenanceCleanup(ctx, "dedup_hardlink")
	if err != nil || dedupRes.Affected != 1 {
		t.Fatalf("dedup_hardlink: %+v err=%v", dedupRes, err)
	}
	backupPath := filepath.Join(dir, "backup.db")
	if err := store.CreateOnlineBackup(ctx, backupPath); err != nil {
		t.Fatalf("CreateOnlineBackup: %v", err)
	}
	if st, err := os.Stat(backupPath); err != nil || st.Size() == 0 {
		t.Fatalf("backup file invalid: %v", err)
	}
	delRes, err := store.DeleteTweets(ctx, []string{"1001"}, true)
	if err != nil || delRes.DeletedRecords != 1 || delRes.DeletedFiles != 1 {
		t.Fatalf("DeleteTweets: %+v err=%v", delRes, err)
	}
}
