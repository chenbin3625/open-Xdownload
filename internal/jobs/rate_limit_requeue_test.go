package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

// TestRateLimitedJobReturnsExecutionSlot 覆盖 P1：账号池全部限流时任务不是 failed，
// 而是回到 pending 并带 available_at，调度器可立即去跑其他任务。
func TestRateLimitedJobReturnsExecutionSlot(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPendingJobs(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	retryAt := time.Now().Add(2 * time.Minute)
	manager.fail(ctx, claimed[0], "", &xclient.RateLimitedError{Path: "/i/api/graphql/x/UserMedia", RetryAt: retryAt})

	updated, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != storage.JobPending {
		t.Fatalf("status = %s, want pending", updated.Status)
	}
	if updated.AvailableAt == nil || updated.AvailableAt.Before(time.Now().Add(time.Minute)) {
		t.Fatalf("availableAt = %v, want ~+2m", updated.AvailableAt)
	}
	if updated.AvailableNS <= time.Now().UnixNano() {
		t.Fatalf("available_ns = %d, want future", updated.AvailableNS)
	}
	if updated.Error != "" {
		t.Fatalf("error = %q, want empty (not a failure)", updated.Error)
	}
	if claimedNow, err := store.ClaimPendingJobs(ctx, 1); err != nil || len(claimedNow) != 0 {
		t.Fatalf("cooling job was claimed: %+v %v", claimedNow, err)
	}
	manager.metricsMu.Lock()
	metrics := manager.metrics[job.ID]
	manager.metricsMu.Unlock()
	if metrics.RateLimitYields != 1 {
		t.Fatalf("rate-limit yield metric = %d, want 1", metrics.RateLimitYields)
	}
}

// TestPlainAllClientsRateLimitedUsesConservativeCooldown 保证没有结构化恢复时间的
// 旧错误也会退避，而不是立即被再次领取形成忙循环。
func TestPlainAllClientsRateLimitedUsesConservativeCooldown(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, storage.JobKindList, "123", "list")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPendingJobs(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	manager.fail(ctx, claimed[0], "", xclient.ErrAllClientsRateLimited)

	updated, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != storage.JobPending || updated.AvailableAt == nil {
		t.Fatalf("job = %+v, want pending with availableAt", updated)
	}
	if time.Until(*updated.AvailableAt) < 30*time.Second {
		t.Fatalf("cooldown = %s, want conservative >= 30s", time.Until(*updated.AvailableAt))
	}
}

// TestRateLimitDeferCapFailsJobWithReason 覆盖 P0：累计推迟超过上限后不再无限
// 重排，任务明确失败并在 Error 中写明原因。
func TestRateLimitDeferCapFailsJobWithReason(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPendingJobs(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	retryAt := time.Now().Add(maxRateLimitDefer + time.Minute)
	manager.fail(ctx, claimed[0], "", &xclient.RateLimitedError{Path: "/i/api/graphql/x/UserMedia", RetryAt: retryAt})

	updated, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != storage.JobFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	if !strings.Contains(updated.Error, "限流累计推迟超过") || !strings.Contains(updated.Error, maxRateLimitDefer.String()) {
		t.Fatalf("error = %q, want cumulative defer reason", updated.Error)
	}
}
