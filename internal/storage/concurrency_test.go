package storage

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestUpdateConfigSurvivesConcurrentWriters 覆盖"事务里先读后写"的锁升级问题：
// 默认的 BEGIN DEFERRED 会让事务以读者身份开始，写第一条语句时才升级为写者，而
// busy_timeout 对锁升级不生效，并发写者会立刻拿到 SQLITE_BUSY(5)/SQLITE_BUSY_SNAPSHOT(517)。
// 修复前 8 个 goroutine 全部失败。
func TestUpdateConfigSurvivesConcurrentWriters(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	base, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}

	const writers, iterations = 8, 25
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				cfg := base
				cfg.MaxConcurrency = 2 + (w+i)%8
				if _, err := store.UpdateConfig(ctx, cfg); err != nil && errs[w] == nil {
					errs[w] = err
				}
			}
		}(w)
	}
	wg.Wait()
	for w, err := range errs {
		if err != nil {
			t.Fatalf("concurrent UpdateConfig writer %d failed: %v", w, err)
		}
	}
}

// TestOpenSurvivesConcurrentOpens 覆盖启动期竞态：main.go 在 Open 出错时直接退出，
// 因此多个进程同时首次启动必须都能成功。修复前 4 个并发 Open 会有多个失败
// （journal_mode 切换的 SQLITE_BUSY、app_config.id 唯一约束、duplicate column name、
// trigger already exists）。
func TestOpenSurvivesConcurrentOpens(t *testing.T) {
	for trial := 0; trial < 5; trial++ {
		path := filepath.Join(t.TempDir(), "test.db")
		stores := make([]*Store, 4)
		errs := make([]error, 4)
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				stores[i], errs[i] = Open(path)
			}(i)
		}
		wg.Wait()
		for i, store := range stores {
			if store != nil {
				store.Close()
			}
			if errs[i] != nil {
				t.Fatalf("trial %d: concurrent Open %d failed: %v", trial, i, errs[i])
			}
		}
	}
}

// TestDownloadLookupsUseUniqueIndexes 锁住归档热路径的查询计划。downloads 的两个唯一
// 索引都是部分索引，SQLite 只在查询能"证明"满足索引谓词时才会使用它们；一旦谓词被
// 改回纯绑定参数，这些每个媒体文件都要跑一次的查找会静默退化为全表扫描
// （5 万行约 2.5ms/次，1000 个媒体的归档要多花数秒）。
func TestDownloadLookupsUseUniqueIndexes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	cases := []struct {
		name  string
		query string
		index string
		args  []any
	}{
		{"GetDownloadByTweetMedia", downloadByTweetMediaQuery, "idx_downloads_tweet_media_unique", []any{"tweet-1", "https://example.com/a.jpg"}},
		{"GetDownloadByTweetMedia/空 tweet_id", downloadByMediaURLQuery, "idx_downloads_media_url_unique", []any{"https://example.com/a.jpg"}},
		{"GetMediaDownloadState", mediaDownloadStateByTweetQuery, "idx_downloads_tweet_media_unique", []any{"tweet-1", "https://example.com/a.jpg"}},
		{"GetMediaDownloadState/空 tweet_id", mediaDownloadStateByMediaURLQuery, "idx_downloads_media_url_unique", []any{"https://example.com/a.jpg"}},
		{"HasDownload", hasDownloadByTweetMediaQuery, "idx_downloads_tweet_media_unique", []any{"tweet-1", "https://example.com/a.jpg"}},
		{"HasDownload/空 tweet_id", hasDownloadByMediaURLQuery, "idx_downloads_media_url_unique", []any{"https://example.com/a.jpg"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := store.db.Query("EXPLAIN QUERY PLAN "+tc.query, tc.args...)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			defer rows.Close()
			plan := []string{}
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatalf("scan plan: %v", err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("plan rows: %v", err)
			}
			joined := strings.Join(plan, "\n")
			// 必须点名索引：全表扫描在 JOIN 里显示为 "SCAN d"（别名）而不是
			// "SCAN downloads"，只查后者会漏掉 GetMediaDownloadState 的退化。
			if !strings.Contains(joined, tc.index) {
				t.Fatalf("查询没有用到 %s，计划:\n%s\n查询:\n%s", tc.index, joined, tc.query)
			}
			for _, scan := range []string{"SCAN downloads", "SCAN d "} {
				if strings.Contains(joined+" ", scan) {
					t.Fatalf("查询里 downloads 仍是全表扫描（%q），计划:\n%s\n查询:\n%s", scan, joined, tc.query)
				}
			}
		})
	}
}
