package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrateNormalizesAndDeduplicatesDownloadsMediaURL 验证历史 downloads 记录中
// 因 ?tag= 不同而重复的同一条视频，经规范化与去重后合并为一行。直接调用迁移函数
// （与 migrate 中的调用一致），因为一次性迁移在 Open 后已登记、不会自动重跑。
func TestMigrateNormalizesAndDeduplicatesDownloadsMediaURL(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindMediaURL, "one", "one")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// 直接写入未规范化的历史记录（绕过 CreateDownload 的去重与 manager 的 NormalizeMediaURL），
	// 模拟升级前遗留的数据。先去掉 (tweet_id, media_url) 唯一索引，否则两条 ?tag= 不同的记录插不进去。
	if _, err := store.db.Exec(`DROP INDEX IF EXISTS idx_downloads_tweet_media_unique`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	for _, u := range []string{
		"https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=12",
		"https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=14",
	} {
		if _, err := store.db.Exec(`INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			job.ID, "tweet-1", u, "/tmp/abc.mp4", 100, time.Now().UTC()); err != nil {
			t.Fatalf("insert download %q: %v", u, err)
		}
	}

	if err := store.normalizeDownloadsMediaURL(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if err := store.deduplicateDownloads(); err != nil {
		t.Fatalf("dedup: %v", err)
	}
	if err := store.addMissingIndexes(); err != nil {
		t.Fatalf("add indexes: %v", err)
	}

	items, err := store.ListDownloads(ctx, 10)
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("after migrate: download count = %d, want 1", len(items))
	}
	want := "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4"
	if items[0].MediaURL != want {
		t.Fatalf("media_url = %q, want %q", items[0].MediaURL, want)
	}
}

// TestMigrationsRunOnceAfterOpen 验证一次性数据迁移在 Open 后被登记，
// 后续启动不会重跑（避免每次冷启动全表扫描）。
func TestMigrationsRunOnceAfterOpen(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	for _, name := range []string{"normalize_downloads_media_url", "deduplicate_downloads", "deduplicate_downloads_media_url_only"} {
		var count int
		if err := store.db.Get(&count, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name); err != nil {
			t.Fatalf("check %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("%s recorded %d times, want 1", name, count)
		}
	}

	// 再次调用 runMigrationOnce 应直接跳过（不重复执行 fn）。
	before := time.Now()
	if err := store.runMigrationOnce("deduplicate_downloads", func() error {
		t.Fatal("already-applied migration must not re-run")
		return nil
	}); err != nil {
		t.Fatalf("runMigrationOnce on applied migration: %v", err)
	}
	if elapsed := time.Since(before); elapsed > 100*time.Millisecond {
		t.Fatalf("runMigrationOnce on applied migration took %v, should be near-instant", elapsed)
	}
}

// TestCreateDownloadDeduplicatesMediaURLJobs 验证 tweet_id 为空的直接媒体 URL 任务
// 按 media_url 去重：同一 URL 多次记录只保留一行（更新为最新 file_path），不产生重复行。
func TestCreateDownloadDeduplicatesMediaURLJobs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindMediaURL, "https://video.twimg.com/x.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	url := "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4"
	for i := 0; i < 3; i++ {
		if _, err := store.CreateDownload(ctx, DownloadRecord{
			JobID:    job.ID,
			TweetID:  "",
			MediaURL: url,
			FilePath: fmt.Sprintf("/tmp/abc-%d.mp4", i),
			Bytes:    100,
		}); err != nil {
			t.Fatalf("create download %d: %v", i, err)
		}
	}
	items, err := store.ListDownloads(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("download count = %d, want 1 (media_url jobs dedup by media_url)", len(items))
	}
	if items[0].FilePath != "/tmp/abc-2.mp4" {
		t.Fatalf("file_path = %q, want last write /tmp/abc-2.mp4", items[0].FilePath)
	}
}
