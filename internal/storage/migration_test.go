package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigrateNormalizesAndDeduplicatesDownloadsMediaURL 验证历史 downloads 记录中
// 因 ?tag= 不同而重复的同一条视频，在重新打开库（触发 migrate）后被规范化并去重。
func TestMigrateNormalizesAndDeduplicatesDownloadsMediaURL(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// 第一次打开：插入两条同 tweet_id、不同 ?tag= 的记录，模拟历史数据。
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindMediaURL, "one", "one")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	for _, u := range []string{
		"https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=12",
		"https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=14",
	} {
		if _, err := store.CreateDownload(ctx, DownloadRecord{
			JobID:    job.ID,
			TweetID:  "tweet-1",
			MediaURL: u,
			FilePath: "/tmp/abc.mp4",
			Bytes:    100,
		}); err != nil {
			t.Fatalf("create download %q: %v", u, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// 第二次打开：migrate 应规范化 media_url（去 ?tag=）并按 (tweet_id, media_url) 去重。
	store, err = Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
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
