package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNormalizeDownloadsMediaURLKeepsUniqueIndex 覆盖迁移窗口：normalizeDownloadsMediaURL
// 会临时 DROP 唯一索引，若索引缺失被提交出去，后续每个 runMigrationOnce 各自提交，在
// addMissingIndexes 重建之前 CreateDownload 的 ON CONFLICT 会全部失败。
func TestNormalizeDownloadsMediaURLKeepsUniqueIndex(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// 造两条只差 ?tag= 的历史行，规范化后会互相冲突。
	if _, err := store.db.Exec(`DROP INDEX IF EXISTS idx_downloads_tweet_media_unique`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	for _, u := range []string{
		"https://video.twimg.com/ext_tw_video/1/pu/vid/720x720/a.mp4?tag=12",
		"https://video.twimg.com/ext_tw_video/1/pu/vid/720x720/a.mp4?tag=14",
	} {
		if _, err := store.db.Exec(`INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at) VALUES (?, 'tweet-1', ?, '/tmp/a.mp4', 1, ?)`,
			job.ID, u, time.Now().UTC()); err != nil {
			t.Fatalf("seed %q: %v", u, err)
		}
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE name = 'normalize_downloads_media_url'`); err != nil {
		t.Fatalf("reset migration: %v", err)
	}

	if err := store.runMigrationOnce("normalize_downloads_media_url", store.normalizeDownloadsMediaURL); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	// 事务提交后索引必须已经在，且 CreateDownload 立刻可用。
	var indexCount int
	if err := store.db.Get(&indexCount, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_downloads_tweet_media_unique'`); err != nil {
		t.Fatalf("check index: %v", err)
	}
	if indexCount != 1 {
		t.Fatal("唯一索引在迁移事务提交后缺失")
	}
	if _, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: "tweet-2", MediaURL: "https://example.com/b.jpg", FilePath: "/tmp/b.jpg", Bytes: 2,
	}); err != nil {
		t.Fatalf("CreateDownload 在迁移窗口内失败: %v", err)
	}
	// 规范化产生的重复行也应在同一事务里清理掉。
	var dupes int
	if err := store.db.Get(&dupes, `SELECT COUNT(*) FROM downloads WHERE tweet_id = 'tweet-1'`); err != nil {
		t.Fatalf("count dupes: %v", err)
	}
	if dupes != 1 {
		t.Fatalf("tweet-1 剩余 %d 行，want 1", dupes)
	}
}

// TestOpenRepairsDriftedDashboardCounters 覆盖计数器自愈：DashboardMeta 只读物化行，
// 过去只在首次启动对账一次，偏差会永久留下。
func TestOpenRepairsDriftedDashboardCounters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := store.CreateJob(ctx, JobKindMediaURL, fmt.Sprintf("https://example.com/%d.mp4", i), "m"); err != nil {
			t.Fatalf("create job: %v", err)
		}
	}
	if _, err := store.db.Exec(`UPDATE dashboard_counters SET total = 999, active = 42, failed_tweet_count = 7 WHERE id = 1`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	stats, failedTweets, err := reopened.DashboardMeta(ctx)
	if err != nil {
		t.Fatalf("dashboard meta: %v", err)
	}
	if stats.Total != 5 || stats.Active != 5 || failedTweets != 0 {
		t.Fatalf("重开后 stats = %#v, failedTweets = %d; want total/active 5, failedTweets 0", stats, failedTweets)
	}

	// 运行期也能显式修复。
	if _, err := reopened.db.Exec(`UPDATE dashboard_counters SET total = 123 WHERE id = 1`); err != nil {
		t.Fatalf("tamper again: %v", err)
	}
	if err := reopened.RecountDashboardCounters(ctx); err != nil {
		t.Fatalf("recount: %v", err)
	}
	stats, _, err = reopened.DashboardMeta(ctx)
	if err != nil {
		t.Fatalf("dashboard meta: %v", err)
	}
	if stats.Total != 5 {
		t.Fatalf("recount 后 total = %d, want 5", stats.Total)
	}
}

// TestOpenRefusesNewerSchema 覆盖降级保护：旧二进制打开新库时约 20 处 SELECT * 会在
// 运行期报 "missing destination name ..."，任务列表与媒体库直接不可用。
func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	var version int
	if err := store.db.Get(&version, `PRAGMA user_version`); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	// 模拟一次更高版本写入的库。
	if _, err := store.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion+1)); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if reopened != nil {
		reopened.Close()
	}
	if err == nil {
		t.Fatal("打开更高版本的库应当失败")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
	for _, want := range []string{fmt.Sprintf("v%d", schemaVersion+1), fmt.Sprintf("v%d", schemaVersion)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息 %q 未点明版本 %s", err.Error(), want)
		}
	}
}

// TestJobFilesCapsRowsPerKind 覆盖无界结果集：JobFiles 会被整体序列化进一个 JSON 响应，
// 五万个媒体的归档任务不加上限会产生极大的响应体。两个明细分支各自限流，失败记录不会
// 因为下载记录超额而被整段挤掉。
func TestJobFilesCapsRowsPerKind(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	now := time.Now().UTC()
	tx, err := store.db.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < MaxJobFilesPerKind+10; i++ {
		if _, err := tx.Exec(`INSERT INTO downloads (job_id, tweet_id, media_url, media_key, preview_url, file_path, bytes, created_at) VALUES (?, ?, ?, '', '', ?, 1, ?)`,
			job.ID, fmt.Sprintf("tweet-%d", i), fmt.Sprintf("https://example.com/%d.jpg", i), fmt.Sprintf("/tmp/%d.jpg", i), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed download %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := tx.Exec(`INSERT INTO failed_media (job_id, media_url, error, created_at) VALUES (?, ?, 'boom', ?)`,
			job.ID, fmt.Sprintf("https://example.com/failed-%d.jpg", i), now); err != nil {
			t.Fatalf("seed failed %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	downloads, failed, err := store.JobFiles(ctx, job.ID)
	if err != nil {
		t.Fatalf("job files: %v", err)
	}
	if len(downloads) != MaxJobFilesPerKind {
		t.Fatalf("downloads = %d, want %d", len(downloads), MaxJobFilesPerKind)
	}
	// 失败记录不能被下载记录挤掉。
	if len(failed) != 3 {
		t.Fatalf("failed = %d, want 3", len(failed))
	}
}

// TestPruneFailedRecordsAgesOutUnavailableMedia 覆盖 unavailable_media 的老化：命中该表的
// 媒体会被无条件跳过且不再重试，误判写进去后必须有自愈路径。按 updated_at 老化意味着
// 仍在持续失败的媒体每次被记录都会续期。
func TestPruneFailedRecordsAgesOutUnavailableMedia(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	const staleURL, freshURL = "https://example.com/stale.jpg", "https://example.com/fresh.jpg"
	for _, u := range []string{staleURL, freshURL} {
		if err := store.UpsertUnavailableMedia(ctx, UnavailableMedia{MediaURL: u, TweetID: "tweet-1", Error: "404"}); err != nil {
			t.Fatalf("upsert %s: %v", u, err)
		}
	}
	oldTime := time.Now().UTC().Add(-400 * 24 * time.Hour)
	if _, err := store.db.Exec(`UPDATE unavailable_media SET updated_at = ? WHERE media_url = ?`, oldTime, staleURL); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	pruned, err := store.PruneFailedRecords(ctx, time.Now().UTC().Add(-180*24*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	stale, err := store.GetUnavailableMedia(ctx, staleURL)
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if stale != nil {
		t.Fatalf("过期的 unavailable_media 未清理: %#v", stale)
	}
	fresh, err := store.GetUnavailableMedia(ctx, freshURL)
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	if fresh == nil {
		t.Fatal("未过期的 unavailable_media 被误删")
	}
	// 清理后判定恢复为"可下载"，即误判可以自愈。
	_, unavailable, err := store.GetMediaDownloadState(ctx, "tweet-1", staleURL)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if unavailable {
		t.Fatal("清理后仍判定为永久不可用")
	}
}
