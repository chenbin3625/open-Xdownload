package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestNormalizeSortableTimestampsMigrationAndIndexedFastPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "normalize-ts.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "alice", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	// Seed mixed legacy timestamps directly and clear the migration marker to simulate upgrading an existing NAS DB.
	if _, err := store.db.Exec(`
		INSERT INTO tweets(id, author_id, screen_name, author_name, body, published_at, payload, archived_at, updated_at, like_count)
		VALUES
		('t-old', 'u1', 'alice', 'Alice', 'old', '2026-05-01T10:00:00Z', '{}', '2026-05-01T10:00:00Z', '2026-05-01 10:00:00 +0000 UTC', 5),
		('t-mid', 'u1', 'alice', 'Alice', 'mid', '2026-05-01 10:00:00.25 +0000 UTC', '{}', '2026-05-01T10:00:00Z', '2026-05-01T10:00:00Z', 50),
		('t-new', 'u1', 'alice_new', 'Alice New', 'new', '2026-05-01T10:00:00.75Z', '{}', '2026-05-01T10:00:00Z', '2026-05-01T10:00:01Z', 20)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO downloads(job_id, tweet_id, media_url, file_path, bytes, created_at)
		VALUES
		(?, 't-old', 'https://pbs.twimg.com/a.jpg', '/tmp/a.jpg', 100, '2026-05-01T10:00:00Z'),
		(?, 't-new', 'https://pbs.twimg.com/b.jpg', '/tmp/b.jpg', 200, '2026-05-01 10:00:00.5 +0000 UTC')`,
		job.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	if !store.hasLegacyTweetPublishedAt(ctx) || !store.hasLegacyTweetTimestamps(ctx) || !store.hasLegacyDownloadCreatedAt(ctx) {
		t.Fatalf("expected legacy timestamp detection before migration")
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE name = 'normalize_sortable_timestamps'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	if reopened.hasLegacyTweetPublishedAt(ctx) {
		t.Fatalf("expected tweets.published_at to be normalized")
	}
	if reopened.hasLegacyTweetTimestamps(ctx) {
		t.Fatalf("expected tweets.updated_at to be normalized")
	}
	if reopened.hasLegacyDownloadCreatedAt(ctx) {
		t.Fatalf("expected downloads.created_at to be normalized")
	}

	// Verify normalized ordering in ListTweets (desc, likes_desc, sub-second From) and ListDownloads.
	descPage, err := reopened.ListTweets(ctx, TweetFilter{Sort: "desc", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(descPage.Items) != 3 || descPage.Items[0].ID != "t-new" || descPage.Items[1].ID != "t-mid" || descPage.Items[2].ID != "t-old" {
		t.Fatalf("desc order = %+v", descPage.Items)
	}
	likesPage, err := reopened.ListTweets(ctx, TweetFilter{Sort: "likes_desc", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(likesPage.Items) != 3 || likesPage.Items[0].ID != "t-mid" || likesPage.Items[1].ID != "t-new" {
		t.Fatalf("likes_desc order = %+v", likesPage.Items)
	}
	subSecPage, err := reopened.ListTweets(ctx, TweetFilter{From: "2026-05-01T10:00:00.5Z", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(subSecPage.Items) != 1 || subSecPage.Items[0].ID != "t-new" {
		t.Fatalf("sub-second From = %+v, want only t-new", subSecPage.Items)
	}
	dlItems, err := reopened.ListDownloads(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dlItems) != 2 || dlItems[0].TweetID != "t-new" {
		t.Fatalf("ListDownloads order = %+v, want t-new first", dlItems)
	}

	// Verify EXPLAIN QUERY PLAN uses indexes without TEMP B-TREE for ORDER BY.
	assertPlan := func(query, wantSubstr, forbidSubstr string) {
		t.Helper()
		type planRow struct {
			ID      int    `db:"id"`
			Parent  int    `db:"parent"`
			NotUsed int    `db:"notused"`
			Detail  string `db:"detail"`
		}
		var rows []planRow
		if err := reopened.db.Select(&rows, `EXPLAIN QUERY PLAN `+query); err != nil {
			t.Fatalf("explain %q: %v", query, err)
		}
		var details []string
		for _, r := range rows {
			details = append(details, r.Detail)
		}
		joined := strings.Join(details, " | ")
		if wantSubstr != "" && !strings.Contains(joined, wantSubstr) {
			t.Fatalf("plan %q missing %q: %s", query, wantSubstr, joined)
		}
		if forbidSubstr != "" && strings.Contains(joined, forbidSubstr) {
			t.Fatalf("plan %q unexpectedly contains %q: %s", query, forbidSubstr, joined)
		}
	}
	assertPlan(
		`SELECT t.id FROM tweets t ORDER BY t.published_at DESC, t.id DESC LIMIT 20`,
		"idx_tweets_date",
		"USE TEMP B-TREE FOR ORDER BY",
	)
	assertPlan(
		`SELECT t.id FROM tweets t ORDER BY t.like_count DESC, t.published_at DESC, t.id DESC LIMIT 20`,
		"idx_tweets_likes_date",
		"USE TEMP B-TREE FOR ORDER BY",
	)
	assertPlan(
		`SELECT author_id, screen_name, author_name, ROW_NUMBER() OVER (PARTITION BY author_id ORDER BY published_at DESC, updated_at DESC, id DESC) AS rn FROM tweets WHERE screen_name <> '' AND author_id <> ''`,
		"idx_tweets_authors_cover",
		"USE TEMP B-TREE",
	)
}

func TestTweetAuthorsCacheAndInvalidationOnArchiveAndDelete(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "authors-cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "alice", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID: "tw-1", Text: "first", CreatedAt: t0,
		Author: parser.Author{ID: "u1", ScreenName: "alice", Name: "Alice"},
	}, "completed"); err != nil {
		t.Fatal(err)
	}

	authors, err := store.ListTweetAuthors(ctx)
	if err != nil || len(authors) != 1 || authors[0].Value != "alice" {
		t.Fatalf("initial authors = %+v err=%v", authors, err)
	}
	if !store.tweetAuthorsOK {
		t.Fatalf("expected tweetAuthorsCache to be populated")
	}
	genBefore := store.tweetAuthorsGen.Load()

	// Archiving another tweet from the same unchanged author should keep the cache warm.
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID: "tw-2", Text: "second", CreatedAt: t0.Add(time.Minute),
		Author: parser.Author{ID: "u1", ScreenName: "alice", Name: "Alice"},
	}, "completed"); err != nil {
		t.Fatal(err)
	}
	if !store.tweetAuthorsOK || store.tweetAuthorsGen.Load() != genBefore {
		t.Fatalf("expected unchanged author archive to keep tweetAuthorsCache warm")
	}

	// Author rename must invalidate cache and reflect latest screen_name.
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID: "tw-3", Text: "renamed", CreatedAt: t0.Add(2 * time.Minute),
		Author: parser.Author{ID: "u1", ScreenName: "alice_v2", Name: "Alice V2"},
	}, "completed"); err != nil {
		t.Fatal(err)
	}
	authors, err = store.ListTweetAuthors(ctx)
	if err != nil || len(authors) != 1 || authors[0].Value != "alice_v2" || authors[0].Label != "Alice V2" {
		t.Fatalf("renamed authors = %+v err=%v", authors, err)
	}

	// New author must invalidate cache and appear.
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID: "tw-4", Text: "bob post", CreatedAt: t0.Add(3 * time.Minute),
		Author: parser.Author{ID: "u2", ScreenName: "bob", Name: "Bob"},
	}, "completed"); err != nil {
		t.Fatal(err)
	}
	authors, err = store.ListTweetAuthors(ctx)
	if err != nil || len(authors) != 2 || authors[0].Value != "alice_v2" || authors[1].Value != "bob" {
		t.Fatalf("two authors = %+v err=%v", authors, err)
	}

	// Deleting bob's only tweet must invalidate cache and remove bob.
	if _, err := store.DeleteTweets(ctx, []string{"tw-4"}, false); err != nil {
		t.Fatal(err)
	}
	authors, err = store.ListTweetAuthors(ctx)
	if err != nil || len(authors) != 1 || authors[0].Value != "alice_v2" {
		t.Fatalf("after delete authors = %+v err=%v", authors, err)
	}
}

func TestLibrarySnapshotOrphanCacheOmitUsersAndNoChurn(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(t.TempDir(), "snap-opt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice", FriendsCount: 10}); err != nil {
		t.Fatal(err)
	}
	aliceDir := filepath.Join(root, "image", "alice")
	if err := os.MkdirAll(aliceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(root, "image"), "alice"); err != nil {
		t.Fatal(err)
	}

	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "alice", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	p1 := filepath.Join(aliceDir, "small.jpg")
	p2 := filepath.Join(aliceDir, "large.jpg")
	orphanPath := filepath.Join(aliceDir, "orphan.jpg")
	for p, size := range map[string]int{p1: 100, p2: 500, orphanPath: 300} {
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "tw-1", MediaURL: "https://pbs.twimg.com/1.jpg", FilePath: p1, Bytes: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "tw-2", MediaURL: "https://pbs.twimg.com/2.jpg", FilePath: p2, Bytes: 500}); err != nil {
		t.Fatal(err)
	}

	// Prime snapshot and orphan cache.
	page1, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 2, Sort: "size_desc"})
	if err != nil {
		t.Fatal(err)
	}
	if page1.Pagination.Total != 3 || len(page1.Users) == 0 {
		t.Fatalf("page1 = %+v", page1)
	}
	if filepath.Base(page1.Items[0].FilePath) != "large.jpg" || filepath.Base(page1.Items[1].FilePath) != "orphan.jpg" {
		t.Fatalf("size_desc page 1 = %+v", page1.Items)
	}
	page2, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 2, PageSize: 2, Sort: "size_desc", OmitUsers: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || filepath.Base(page2.Items[0].FilePath) != "small.jpg" {
		t.Fatalf("size_desc page 2 = %+v", page2.Items)
	}
	if len(page2.Users) != 0 {
		t.Fatalf("OmitUsers=true returned %d users, want 0", len(page2.Users))
	}

	// No-op user/entity updates and archiving a tweet without downloads must not churn libraryCacheGen.
	genBefore := store.libraryCacheGen.Load()
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice", FriendsCount: 99, MediaCount: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(root, "image"), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID: "tw-text-only", Text: "no media", CreatedAt: time.Now().UTC(),
		Author: parser.Author{ID: "u1", ScreenName: "alice", Name: "Alice"},
	}, "not_requested"); err != nil {
		t.Fatal(err)
	}
	if store.libraryCacheGen.Load() != genBefore || store.libraryStale.Load() {
		t.Fatalf("expected no cache churn for unchanged metadata / text-only tweet")
	}

	// Recording the former orphan in downloads marks cache stale but preserves orphanCache while deduplicating on read.
	if _, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "tw-3", MediaURL: "https://pbs.twimg.com/3.jpg", FilePath: orphanPath, Bytes: 300}); err != nil {
		t.Fatal(err)
	}
	afterPage, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if afterPage.Pagination.Total != 3 {
		t.Fatalf("expected orphan promoted to recorded download without duplication, total=%d", afterPage.Pagination.Total)
	}
	store.orphanCacheMu.RLock()
	orphanCached := store.orphanCacheOK
	store.orphanCacheMu.RUnlock()
	if !orphanCached {
		t.Fatalf("expected markLibraryDownloadsCacheStale to preserve orphanCache")
	}
	store.invalidateLibraryDownloadsCache()
	store.orphanCacheMu.RLock()
	orphanCachedAfterFull := store.orphanCacheOK
	store.orphanCacheMu.RUnlock()
	if orphanCachedAfterFull {
		t.Fatalf("expected full invalidateLibraryDownloadsCache to clear orphanCache")
	}
}
