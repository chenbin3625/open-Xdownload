package jobs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

// TestArchiveUsersSegmentsAtUserBoundary 覆盖 P1：列表/关注归档在用户边界保存检查点
// 并归还执行槽；下一次调用从检查点继续，已完成用户不会重复请求。
func TestArchiveUsersSegmentsAtUserBoundary(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var timelineCalls atomic.Int64
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		timelineCalls.Add(1)
		fmt.Fprint(w, `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`)
	})

	job, err := store.CreateJob(ctx, storage.JobKindList, "123", "list")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	oldBudget := archiveSliceBudget
	archiveSliceBudget = 0
	defer func() { archiveSliceBudget = oldBudget }()

	users := []xclient.User{
		{ID: "u1", ScreenName: "a", Name: "A"},
		{ID: "u2", ScreenName: "b", Name: "B"},
		{ID: "u3", ScreenName: "c", Name: "C"},
	}
	var last archiveStats
	slices, yielded := 0, 0
	for slices = 0; slices < 10; slices++ {
		stats, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1)
		last = stats
		if errors.Is(err, errYieldExecution) {
			yielded++
			if stats.Users != yielded {
				t.Fatalf("after slice %d stats.Users = %d, want %d", slices, stats.Users, yielded)
			}
			continue
		}
		if err != nil {
			t.Fatalf("archiveUsers: %v", err)
		}
		break
	}
	if slices == 10 {
		t.Fatal("archiveUsers never completed")
	}
	if yielded != len(users)-1 {
		t.Fatalf("yielded %d times, want %d (one per user boundary)", yielded, len(users)-1)
	}
	if last.Users != len(users) {
		t.Fatalf("final stats.Users = %d, want %d", last.Users, len(users))
	}
	if calls := timelineCalls.Load(); calls != int64(len(users)) {
		t.Fatalf("timeline calls = %d, want %d (no user reprocessed)", calls, len(users))
	}
	if _, ok, err := store.LoadJobCheckpoint(ctx, job.ID); err != nil || ok {
		t.Fatalf("checkpoint not cleared after completion: ok=%v err=%v", ok, err)
	}
}

// TestArchiveUsersResumesFromCommittedCheckpoint 覆盖进程重启/限流让出后的恢复：
// 检查点里的 Next 之前用户不再请求时间线。
func TestArchiveUsersResumesFromCommittedCheckpoint(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var timelineCalls atomic.Int64
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		timelineCalls.Add(1)
		fmt.Fprint(w, `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`)
	})
	job, err := store.CreateJob(ctx, storage.JobKindList, "123", "list")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	users := []xclient.User{
		{ID: "u1", ScreenName: "a", Name: "A"},
		{ID: "u2", ScreenName: "b", Name: "B"},
	}
	tasks, _, _, err := manager.archiveUserTasks(ctx, cfg, users)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟上一轮已提交 u1。
	if err := manager.saveArchiveCheckpoint(ctx, job.ID, tasks, 1, archiveStats{Users: 1}); err != nil {
		t.Fatal(err)
	}

	stats, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Users != 2 {
		t.Fatalf("stats.Users = %d, want cumulative 2", stats.Users)
	}
	if calls := timelineCalls.Load(); calls != 1 {
		t.Fatalf("timeline calls = %d, want 1 (only the remaining user)", calls)
	}
}

// TestListResumeUsesCheckpointWithoutFetchingMembers 覆盖 P0：列表任务恢复时先读
// 检查点，已有用户快照就不再翻页拉取成员列表。
func TestListResumeUsesCheckpointWithoutFetchingMembers(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var memberCalls, timelineCalls atomic.Int64
	archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ListByRestId"):
			fmt.Fprint(w, `{"data":{"list":{"id_str":"123","name":"List","member_count":1,"user_results":{"result":{"rest_id":"owner","legacy":{"name":"Owner","screen_name":"owner"}}}}}}`)
		case strings.HasSuffix(r.URL.Path, "/ListMembers"):
			memberCalls.Add(1)
			fmt.Fprint(w, `{"data":{"list":{"members_timeline":{"timeline":{"instructions":[]}}}}}`)
		case strings.HasSuffix(r.URL.Path, "/UserMedia"):
			timelineCalls.Add(1)
			fmt.Fprint(w, `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`)
		default:
			http.NotFound(w, r)
		}
	})
	if _, err := store.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	seedXAccountsFromTestConfig(t, store, cfg)
	job, err := store.CreateJob(ctx, storage.JobKindList, "123", "list")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	users := []xclient.User{{ID: "u1", ScreenName: "alice", Name: "Alice"}}
	tasks, _, _, err := manager.archiveUserTasks(ctx, cfg, users)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveArchiveCheckpoint(ctx, job.ID, tasks, 0, archiveStats{}); err != nil {
		t.Fatal(err)
	}

	manager.processList(ctx, ctx, job)

	if memberCalls.Load() != 0 {
		t.Fatalf("member requests = %d, want 0 when resuming from checkpoint", memberCalls.Load())
	}
	if timelineCalls.Load() != 1 {
		t.Fatalf("timeline requests = %d, want 1", timelineCalls.Load())
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil || got.Status != storage.JobCompleted {
		t.Fatalf("job = %+v %v", got, err)
	}
}
