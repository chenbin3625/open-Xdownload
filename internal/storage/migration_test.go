package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
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

	if err := store.normalizeDownloadsMediaURL(store.db); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if err := store.deduplicateDownloads(store.db); err != nil {
		t.Fatalf("dedup: %v", err)
	}
	if err := store.backfillDownloadPreviewURLs(store.db); err != nil {
		t.Fatalf("backfill previews: %v", err)
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
	if got, wantPreview := items[0].PreviewURL, "https://pbs.twimg.com/ext_tw_video_thumb/123/pu/img/abc.jpg?name=small"; got != wantPreview {
		t.Fatalf("preview_url = %q, want %q", got, wantPreview)
	}
}

func TestDeriveVideoPreviewURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "extended video",
			in:   "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=12",
			want: "https://pbs.twimg.com/ext_tw_video_thumb/123/pu/img/abc.jpg?name=small",
		},
		{
			name: "amplify video",
			in:   "https://video.twimg.com/amplify_video/456/vid/640x360/clip.mp4",
			want: "https://pbs.twimg.com/amplify_video_thumb/456/img/clip.jpg?name=small",
		},
		{
			name: "animated gif",
			in:   "https://video.twimg.com/tweet_video/789/pu/vid/320x180/anim.mp4",
			want: "https://pbs.twimg.com/tweet_video_thumb/789/pu/img/anim.png?name=small",
		},
		{name: "non Twitter", in: "https://example.com/video.mp4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveVideoPreviewURL(tt.in); got != tt.want {
				t.Fatalf("deriveVideoPreviewURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRederiveDownloadPreviewURLs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindMediaURL, "one", "one")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	rows := []struct {
		tweetID    string
		mediaURL   string
		previewURL string
	}{
		// 旧版推导写入的死链（/vid/ 段 + name=small 特征）：应被重写为 /img/ 推导。
		{"t1", "https://video.twimg.com/ext_tw_video/123/pu/vid/avc1/1280x720/abc.mp4",
			"https://pbs.twimg.com/ext_tw_video_thumb/123/pu/vid/avc1/1280x720/abc.jpg?name=small"},
		// API 解析得到的真实海报（无 name=small 查询串）：保持不动。
		{"t2", "https://video.twimg.com/ext_tw_video/456/pu/vid/avc1/720x1280/def.mp4",
			"https://pbs.twimg.com/ext_tw_video_thumb/456/pu/img/def.jpg"},
		// 无预览地址的历史记录：由 backfill_download_preview_urls 迁移负责，此处不动。
		{"t3", "https://video.twimg.com/ext_tw_video/789/pu/vid/avc1/1280x720/ghi.mp4", ""},
	}
	for _, row := range rows {
		if _, err := store.db.Exec(`INSERT INTO downloads (job_id, tweet_id, media_url, preview_url, file_path, bytes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			job.ID, row.tweetID, row.mediaURL, row.previewURL, "/tmp/x.mp4", 100, time.Now().UTC()); err != nil {
			t.Fatalf("insert download %s: %v", row.tweetID, err)
		}
	}
	if err := store.rederiveDownloadPreviewURLs(store.db); err != nil {
		t.Fatalf("rederive: %v", err)
	}
	items, err := store.ListDownloads(ctx, 10)
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}
	byTweet := map[string]DownloadRecord{}
	for _, item := range items {
		byTweet[item.TweetID] = item
	}
	if got := byTweet["t1"].PreviewURL; got != "https://pbs.twimg.com/ext_tw_video_thumb/123/pu/img/abc.jpg?name=small" {
		t.Fatalf("stale derived preview_url = %q, want re-derived /img/ URL", got)
	}
	if got := byTweet["t2"].PreviewURL; got != "https://pbs.twimg.com/ext_tw_video_thumb/456/pu/img/def.jpg" {
		t.Fatalf("parsed poster preview_url changed: %q", got)
	}
	if got := byTweet["t3"].PreviewURL; got != "" {
		t.Fatalf("empty preview_url unexpectedly filled: %q", got)
	}
}

func TestListLibraryDownloadsIncludesUnrecordedLocalMedia(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "untracked.mp4"), []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.jpg"), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if _, err := store.UpdateConfig(context.Background(), config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	items, err := store.ListLibraryDownloads(context.Background(), 20)
	if err != nil {
		t.Fatalf("list library: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("library count = %d, want 2", len(items))
	}
	for _, item := range items {
		if item.ID != 0 {
			t.Fatalf("untracked item has database id %d", item.ID)
		}
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

	for _, name := range []string{"normalize_downloads_media_url", "deduplicate_downloads", "deduplicate_downloads_media_url_only", "dashboard_counters_v1"} {
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
	if err := store.runMigrationOnce("deduplicate_downloads", func(migrationExecutor) error {
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

// TestListLibraryDownloadsAttributesUsersByExactPrefix 验证用户归属按路径前缀精确
// 匹配（不区分大小写）：目录名中的 `_` 不再被当作 LIKE 通配符导致误归属。
func TestListLibraryDownloadsAttributesUsersByExactPrefix(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO users (id, screen_name, name, updated_at) VALUES ('u1', 'john_smith', 'John Smith', ?)`, time.Now().UTC()); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// EnsureUserEntity 会把 parent_dir 规范化为绝对路径（与 manager 的调用方式一致）。
	if _, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(root, "users"), "john_smith"); err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	job, err := store.CreateJob(ctx, JobKindMediaURL, "one", "one")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// 直接落盘两个文件：一个在实体目录内，一个在名字相近（含 X）但无实体的目录内。
	inside := filepath.Join(root, "users", "john_smith", "a.mp4")
	confusable := filepath.Join(root, "users", "johnXsmith", "b.mp4")
	for _, dir := range []string{filepath.Dir(inside), filepath.Dir(confusable)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, item := range []struct{ url, path string }{
		{"https://video.twimg.com/a.mp4", inside},
		{"https://video.twimg.com/b.mp4", confusable},
	} {
		if _, err := store.CreateDownload(ctx, DownloadRecord{
			JobID: job.ID, MediaURL: item.url, FilePath: item.path, Bytes: 1,
		}); err != nil {
			t.Fatalf("create download %s: %v", item.path, err)
		}
	}

	items, err := store.ListLibraryDownloads(ctx, 100)
	if err != nil {
		t.Fatalf("list library: %v", err)
	}
	byPath := map[string]DownloadRecord{}
	for _, item := range items {
		byPath[item.FilePath] = item
	}
	if got := byPath[inside]; got.UserScreenName != "john_smith" || got.UserName != "John Smith" {
		t.Fatalf("inside record user = %q/%q, want john_smith/John Smith", got.UserScreenName, got.UserName)
	}
	if got := byPath[confusable]; got.UserScreenName != "" || got.UserName != "" {
		t.Fatalf("confusable record user = %q/%q, want empty (old LIKE matched `_` as wildcard)", got.UserScreenName, got.UserName)
	}
}

