package jobs

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

// X 对过期的时间线游标可能返回 401（而不是 400 Invalid cursor）。Cookie 本身通过
// /home 校验时，不能把它禁用 30 分钟，应丢弃旧游标从顶部重扫。
func TestFullTimelineStaleCursorUnauthorizedRestartsFromTop(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	var cursors []string
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/home" {
			fmt.Fprint(w, `{"screen_name":"me"}`)
			return
		}
		cursor := timelineCursor(t, r)
		cursors = append(cursors, cursor)
		if cursor != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, fullReviewTimelinePayload("105", ""))
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTweetSync(ctx, "42"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTweetSync(ctx, storage.TweetSync{UserID: "42", HeadID: "110", Cursor: "stale", Count: 513, Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	stats, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, xclient.User{ID: "42", ScreenName: "author"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("stale cursor 401 aborted user: %v", err)
	}
	if stats.Failed != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	state, err := store.GetTweetSync(ctx, "42")
	if err != nil || state.Status != "completed" || state.LastID != "105" {
		t.Fatalf("state = %+v, %v", state, err)
	}
	if len(cursors) != 2 || cursors[0] != "stale" || cursors[1] != "" {
		t.Fatalf("cursors = %q", cursors)
	}
	if diag := pool.Diagnostics(); diag.Available != 1 {
		t.Fatalf("cookie left disabled: %+v", diag)
	}
}

// 单个用户的时间线持续 401、但 Cookie 校验通过：记为该用户失败并继续后面的用户，
// 不能让整个关注/列表任务失败。
func TestArchiveUsersSkipsUserWhenTimelineUnauthorizedButCookieValid(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/home" {
			fmt.Fprint(w, `{"screen_name":"me"}`)
			return
		}
		if strings.Contains(r.URL.Query().Get("variables"), `"screenName":"bad"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, fullReviewTimelinePayload("205", ""))
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindFollowing, Input: "me", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	users := []xclient.User{{ID: "1", ScreenName: "bad", MediaCount: 10}, {ID: "2", ScreenName: "good"}}
	stats, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1)
	if err != nil {
		t.Fatalf("one user's 401 failed the whole job: %v", err)
	}
	if stats.Failed != 1 || len(stats.Issues) != 1 || !strings.Contains(stats.Issues[0], "@bad") {
		t.Fatalf("stats = %+v", stats)
	}
	good, err := store.GetTweetSync(ctx, "2")
	if err != nil || good.Status != "completed" {
		t.Fatalf("second user not archived: %+v, %v", good, err)
	}
	if diag := pool.Diagnostics(); diag.Available != 1 {
		t.Fatalf("cookie left disabled: %+v", diag)
	}
}

// Cookie 真的失效（/home 也 401）时仍要中止任务，避免对后面每个用户重复报错。
func TestArchiveUsersAbortsWhenCookieReallyRejected(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var timelineCalls int
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/home" {
			timelineCalls++
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindFollowing, Input: "me", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	users := []xclient.User{{ID: "1", ScreenName: "a"}, {ID: "2", ScreenName: "b"}}
	if _, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 abort, got %v", err)
	}
	if timelineCalls != 1 {
		t.Fatalf("timeline calls = %d, want 1", timelineCalls)
	}
}
