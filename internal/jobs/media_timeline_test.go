package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

// archiveSingleClientPool 与 archiveTestPool 相同，但只配置主账号。限流测试需要
// 确认"一次 429 = 一次请求"，备用 Cookie 会自动把请求数翻倍。
func archiveSingleClientPool(t *testing.T, cfg *config.AppConfig, handler http.HandlerFunc) *xclient.Pool {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	cfg.ProxyURL = server.URL
	cfg.AuthToken, cfg.CSRFToken = "primary-"+t.Name(), "csrf"
	cfg.AdditionalCookies = ""
	transport, err := httpx.Transport(cfg.ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	t.Cleanup(transport.CloseIdleConnections)
	pool, err := xclient.NewPool(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func mediaTimelinePayload(tweetIDs []string, cursor string) string {
	entries := make([]string, 0, len(tweetIDs)+1)
	for _, id := range tweetIDs {
		entries = append(entries, fmt.Sprintf(
			`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":%q,"legacy":{"full_text":"tweet %s","extended_entities":{"media":[{"id_str":"m-%s","type":"photo","media_url_https":"https://pbs.twimg.com/media/%s.jpg"}]}}}}}}}`,
			id, id, id, id))
	}
	if cursor != "" {
		entries = append(entries, fmt.Sprintf(
			`{"content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":%q}}`, cursor))
	}
	return fmt.Sprintf(
		`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`,
		strings.Join(entries, ","))
}

func timelineCursor(t *testing.T, r *http.Request) string {
	t.Helper()
	var variables map[string]any
	if err := json.Unmarshal([]byte(r.URL.Query().Get("variables")), &variables); err != nil {
		t.Fatalf("decode variables: %v", err)
	}
	cursor, _ := variables["cursor"].(string)
	return cursor
}

// TestMediaTimelineResumesFromSavedPageAfterRateLimit 覆盖 P0 续传验收：第 2 页注入
// 限流后，第 1 页的游标已落库；恢复时直接从第 2 页继续，2 页时间线总请求数为 3。
func TestMediaTimelineResumesFromSavedPageAfterRateLimit(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	root := t.TempDir()
	cfg := config.AppConfig{
		DownloadDir: root,
		StorageType: config.StorageLocal,
		AuthToken:   "primary",
		CSRFToken:   "csrf",
	}
	var timelineRequests atomic.Int64
	var resumedCursor atomic.Value
	var page2Limited atomic.Bool
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/UserMedia"):
			timelineRequests.Add(1)
			cursor := timelineCursor(t, r)
			if cursor == "" {
				fmt.Fprint(w, mediaTimelinePayload([]string{"300"}, "page-2"))
				return
			}
			if cursor == "page-2" && !page2Limited.Swap(true) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			resumedCursor.Store(cursor)
			fmt.Fprint(w, mediaTimelinePayload([]string{"200"}, ""))
		case strings.HasPrefix(r.URL.Path, "/media/"):
			w.Header().Set("Content-Type", "image/jpeg")
			fmt.Fprintf(w, "jpeg-bytes-%s", r.URL.Path)
		default:
			http.NotFound(w, r)
		}
	}
	pool := archiveSingleClientPool(t, &cfg, handler)
	if _, err := store.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	user := xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	yieldCtx := xclient.WithYieldToScheduler(ctx)

	stats, err := manager.archiveUserWithBudget(yieldCtx, ctx, job, cfg, pool, user, nil, nil, nil)
	retryAt, limited := xclient.RateLimitRetryAt(err)
	if !limited {
		t.Fatalf("first run error = %v, want a rate-limit error", err)
	}
	if stats.Downloaded != 1 {
		t.Fatalf("first run downloads = %d, want 1 committed page", stats.Downloaded)
	}
	state, err := store.GetMediaSync(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Cursor != "page-2" || state.Status != "interrupted" {
		t.Fatalf("saved media cursor = %+v, want page-2/interrupted", state)
	}

	// 账号限流跨 Pool 保留，等窗口结束后再模拟重启；store 仍保存页游标。
	time.Sleep(time.Until(retryAt) + 20*time.Millisecond)
	pool = archiveSingleClientPool(t, &cfg, handler)
	stats, err = manager.archiveUserWithBudget(yieldCtx, ctx, job, cfg, pool, user, nil, nil, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if stats.Downloaded != 1 {
		t.Fatalf("resume downloads = %d, want the second page only", stats.Downloaded)
	}
	if got := resumedCursor.Load(); got != "page-2" {
		t.Fatalf("resumed with cursor %v, want page-2", got)
	}
	if calls := timelineRequests.Load(); calls != 3 {
		t.Fatalf("timeline requests = %d, want 3 (2 pages + 1 rate-limited retry)", calls)
	}
	state, err = store.GetMediaSync(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Cursor != "" || state.Status != "completed" {
		t.Fatalf("completed media cursor = %+v", state)
	}
	entity, err := store.LocateUserEntity(ctx, user.ID, filepath.Join(root, "users"))
	if err != nil || entity == nil {
		t.Fatalf("locate entity: %+v %v", entity, err)
	}
	if entity.LastSeenTweetID != "300" {
		t.Fatalf("incremental cursor = %q, want 300 (max across resumed pages)", entity.LastSeenTweetID)
	}
	manager.metricsMu.Lock()
	metrics, ok := manager.metrics[job.ID]
	manager.metricsMu.Unlock()
	if !ok {
		t.Fatal("job metrics were not recorded")
	}
	if metrics.APIRequests != 3 || metrics.Pages != 2 || metrics.APITime <= 0 || metrics.DownloadTime <= 0 {
		t.Fatalf("job metrics = %+v, want 3 requests, 2 pages, non-zero api/download time", metrics)
	}
}

// TestInvalidMediaCursorRestartsFromFirstPage 覆盖失效游标回退：第 0 页重翻，
// 已下载媒体由去重逻辑跳过。
func TestInvalidMediaCursorRestartsFromFirstPage(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	root := t.TempDir()
	cfg := config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal, AuthToken: "primary", CSRFToken: "csrf"}
	var requests atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/UserMedia"):
			requests.Add(1)
			cursor := timelineCursor(t, r)
			if cursor == "stale" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"errors":[{"message":"invalid cursor"}]}`)
				return
			}
			fmt.Fprint(w, mediaTimelinePayload([]string{"100"}, ""))
		case strings.HasPrefix(r.URL.Path, "/media/"):
			w.Header().Set("Content-Type", "image/jpeg")
			fmt.Fprintf(w, "jpeg-bytes-%s", r.URL.Path)
		default:
			http.NotFound(w, r)
		}
	}
	pool := archiveSingleClientPool(t, &cfg, handler)
	if _, err := store.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMediaSync(ctx, storage.MediaSync{UserID: "u1", Cursor: "stale", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	stats, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Downloaded != 1 {
		t.Fatalf("downloads = %d, want 1", stats.Downloaded)
	}
	if calls := requests.Load(); calls != 2 {
		t.Fatalf("timeline requests = %d, want 2 (stale cursor + page 0)", calls)
	}
}

// TestMediaTimelineYieldsAtPageBoundary 覆盖 P0：大账号在页边界归还执行槽，
// 下一页不计入本段，恢复后从保存的游标继续。
func TestMediaTimelineYieldsAtPageBoundary(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal, AuthToken: "primary", CSRFToken: "csrf"}
	var requests atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/UserMedia"):
			requests.Add(1)
			if timelineCursor(t, r) == "" {
				fmt.Fprint(w, mediaTimelinePayload([]string{"300"}, "page-2"))
			} else {
				fmt.Fprint(w, mediaTimelinePayload([]string{"200"}, ""))
			}
		case strings.HasPrefix(r.URL.Path, "/media/"):
			w.Header().Set("Content-Type", "image/jpeg")
			fmt.Fprintf(w, "jpeg-bytes-%s", r.URL.Path)
		default:
			http.NotFound(w, r)
		}
	}
	pool := archiveSingleClientPool(t, &cfg, handler)
	if _, err := store.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	user := xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice"}

	stats, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, user, nil, nil, func() bool { return true })
	if !errors.Is(err, errYieldExecution) {
		t.Fatalf("first run error = %v, want errYieldExecution", err)
	}
	if stats.Downloaded != 1 || requests.Load() != 1 {
		t.Fatalf("first slice = %+v, requests=%d; want one page only", stats, requests.Load())
	}

	pool = archiveSingleClientPool(t, &cfg, handler)
	stats, err = manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, user, nil, nil, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if stats.Downloaded != 1 || requests.Load() != 2 {
		t.Fatalf("resume = %+v, requests=%d; want the second page only", stats, requests.Load())
	}
	manager.metricsMu.Lock()
	metrics := manager.metrics[job.ID]
	manager.metricsMu.Unlock()
	if metrics.Pages != 2 || metrics.APIRequests != 2 {
		t.Fatalf("metrics = %+v, want two pages across both slices", metrics)
	}
}

func TestScheduledArchiveSkipsUnchangedMediaCountAndPeriodicallyFullScans(t *testing.T) {
	for _, tc := range []struct {
		name         string
		source       storage.JobSource
		lastFullScan time.Duration
		mediaCount   int
		wantTimeline int64
		wantSkipped  int
	}{
		{name: "scheduled unchanged skips", source: storage.JobSourceSchedule, lastFullScan: time.Hour, mediaCount: 100, wantTimeline: 0, wantSkipped: 1},
		{name: "scheduled growth scans", source: storage.JobSourceSchedule, lastFullScan: time.Hour, mediaCount: 101, wantTimeline: 1},
		{name: "scheduled seven days due full scans", source: storage.JobSourceSchedule, lastFullScan: 8 * 24 * time.Hour, mediaCount: 100, wantTimeline: 1},
		{name: "manual always scans", source: storage.JobSourceManual, lastFullScan: time.Hour, mediaCount: 100, wantTimeline: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			root := t.TempDir()
			cfg := config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal, AuthToken: "primary", CSRFToken: "csrf"}
			var timelineRequests atomic.Int64
			archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/UserByScreenName"):
					fmt.Fprintf(w, `{"data":{"user":{"result":{"rest_id":"u1","legacy":{"name":"Alice","screen_name":"alice","media_count":%d}}}}}`, tc.mediaCount)
				case strings.HasSuffix(r.URL.Path, "/UserMedia"):
					timelineRequests.Add(1)
					fmt.Fprint(w, mediaTimelinePayload(nil, ""))
				default:
					http.NotFound(w, r)
				}
			})
			if _, err := store.UpdateConfig(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(store, parser.NewService(), NewEventBus())
			user := xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice", MediaCount: 100}
			if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
				t.Fatal(err)
			}
			entity, _, err := manager.ensureUserEntity(ctx, cfg, user)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateUserEntityMediaCount(ctx, entity.ID, 100); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateUserEntityLastFullScan(ctx, entity.ID, time.Now().UTC().Add(-tc.lastFullScan)); err != nil {
				t.Fatal(err)
			}
			job, err := store.CreateJobFromSource(ctx, storage.JobKindUser, "alice", "alice", tc.source)
			if err != nil {
				t.Fatal(err)
			}
			manager.processUser(ctx, ctx, job)
			if got := timelineRequests.Load(); got != tc.wantTimeline {
				t.Fatalf("timeline requests = %d, want %d", got, tc.wantTimeline)
			}
			got, err := store.GetJob(ctx, job.ID)
			if err != nil || got.Status != storage.JobCompleted {
				t.Fatalf("job = %+v %v", got, err)
			}
			if !strings.Contains(got.Message, fmt.Sprintf("跳过 %d", tc.wantSkipped)) && tc.wantSkipped > 0 {
				t.Fatalf("message = %q, want skipped count", got.Message)
			}
		})
	}
}

// TestMediaFullScanStaggerIsStableAndBounded 覆盖 P1：首次全量后的下一轮到期时间
// 按用户稳定错开，且至少留出一天，避免同一天集中全量或刚扫完立刻重扫。
func TestMediaFullScanStaggerIsStableAndBounded(t *testing.T) {
	distinct := make(map[time.Duration]struct{})
	now := time.Now().UTC()
	for index := 0; index < 64; index++ {
		userID := fmt.Sprintf("user-%d", index)
		stagger := mediaFullScanStagger(userID)
		if stagger < 0 || stagger >= mediaFullScanStaggerWindow {
			t.Fatalf("stagger for %s = %s, want [0, %s)", userID, stagger, mediaFullScanStaggerWindow)
		}
		if again := mediaFullScanStagger(userID); again != stagger {
			t.Fatalf("stagger for %s is not stable: %s then %s", userID, stagger, again)
		}
		if due := now.Add(-stagger).Add(mediaFullScanInterval); !due.After(now.Add(24 * time.Hour)) {
			t.Fatalf("next full scan for %s due at %s, want at least one day out", userID, due)
		}
		distinct[stagger] = struct{}{}
	}
	if len(distinct) < 8 {
		t.Fatalf("stagger only produced %d distinct offsets", len(distinct))
	}
}
