package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestAutomaticRetryReenqueueDoesNotOvertakeManualJob 覆盖 P0：自动重试复用旧任务行时
// 必须刷新 enqueued_at，不能凭首次 created_at 插到之后创建的手动任务前面。
func TestAutomaticRetryReenqueueDoesNotOvertakeManualJob(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	first, err := store.ReserveAutomaticRetryJob(ctx)
	if err != nil || first == nil {
		t.Fatalf("reserve: %+v %v", first, err)
	}
	first.Status = JobCompleted
	first.Progress = 1
	if _, err := store.UpdateJobAndGet(ctx, *first); err != nil {
		t.Fatal(err)
	}

	// 手动任务比自动重试行创建得晚（id 更大）。
	manual, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}

	// 再次启用自动重试：复用旧行并刷新 enqueued_at（当前时间晚于手动任务）。
	reused, err := store.ReserveAutomaticRetryJob(ctx)
	if err != nil || reused == nil {
		t.Fatalf("reuse: %+v %v", reused, err)
	}
	if reused.ID != first.ID {
		t.Fatalf("reused id = %d, want %d", reused.ID, first.ID)
	}
	if reused.CreatedAt.After(manual.CreatedAt) {
		t.Fatalf("reused row kept created_at unexpectedly: %+v", reused)
	}
	if !reused.EnqueuedAt.After(manual.EnqueuedAt) {
		t.Fatalf("enqueued_at not refreshed: reused=%s manual=%s", reused.EnqueuedAt, manual.EnqueuedAt)
	}

	claimed, err := store.ClaimPendingJobs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != manual.ID {
		t.Fatalf("claimed %+v, want newest manual job %d (auto retry must not jump the queue)", claimed, manual.ID)
	}
}

// TestClaimPendingJobsRespectsAvailableAt 覆盖限流冷却/分段让出：未到 available_at 的
// 任务不会被领取，到期后自动恢复。
func TestClaimPendingJobsRespectsAvailableAt(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	requeued, err := store.RequeueJobAt(ctx, job.ID, time.Now().Add(time.Hour), "限流冷却")
	if err != nil || requeued == nil {
		t.Fatalf("requeue: %+v %v", requeued, err)
	}
	if claimed, err := store.ClaimPendingJobs(ctx, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("claimed cooling job: %+v %v", claimed, err)
	}

	ready, err := store.RequeueJobAt(ctx, job.ID, time.Now().Add(-time.Second), "冷却结束")
	if err != nil || ready == nil {
		t.Fatalf("requeue ready: %+v %v", ready, err)
	}
	claimed, err := store.ClaimPendingJobs(ctx, 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != job.ID {
		t.Fatalf("claimed after cooldown: %+v %v", claimed, err)
	}
	if claimed[0].AvailableAt != nil {
		t.Fatalf("available_at not cleared on claim: %+v", claimed[0].AvailableAt)
	}
}

// TestRequeueJobAtIgnoresCanceledJob 保证冷却让出不会复活用户已取消的任务。
func TestRequeueJobAtIgnoresCanceledJob(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	requeued, err := store.RequeueJobAt(ctx, job.ID, time.Now(), "限流冷却")
	if err != nil || requeued != nil {
		t.Fatalf("canceled job requeued: %+v %v", requeued, err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil || got.Status != JobCanceled {
		t.Fatalf("status = %s %v, want canceled", got.Status, err)
	}
}

// TestJobSourceDefaultsAndScheduleSource 保证来源标记可持久化，供加权轮转使用。
func TestJobSourceDefaultsAndScheduleSource(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	manual, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if manual.Source != JobSourceManual {
		t.Fatalf("manual source = %q, want %q", manual.Source, JobSourceManual)
	}

	scheduled, err := store.CreateJobs(ctx, []JobDraft{{Kind: JobKindUser, Input: "bob", Title: "bob", Source: JobSourceSchedule}})
	if err != nil || len(scheduled) != 1 {
		t.Fatalf("create scheduled: %+v %v", scheduled, err)
	}
	if scheduled[0].Source != JobSourceSchedule {
		t.Fatalf("schedule source = %q, want %q", scheduled[0].Source, JobSourceSchedule)
	}
}

// TestJobCheckpointRoundTrip 覆盖分段执行检查点的保存/读取/删除。
func TestJobCheckpointRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindList, "123", "list")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.LoadJobCheckpoint(ctx, job.ID); err != nil || ok {
		t.Fatalf("empty checkpoint: ok=%v err=%v", ok, err)
	}
	if err := store.SaveJobCheckpoint(ctx, job.ID, []byte(`{"next":2}`)); err != nil {
		t.Fatal(err)
	}
	payload, ok, err := store.LoadJobCheckpoint(ctx, job.ID)
	if err != nil || !ok || string(payload) != `{"next":2}` {
		t.Fatalf("load checkpoint: %q ok=%v err=%v", payload, ok, err)
	}
	if err := store.SaveJobCheckpoint(ctx, job.ID, []byte(`{"next":3}`)); err != nil {
		t.Fatal(err)
	}
	payload, _, _ = store.LoadJobCheckpoint(ctx, job.ID)
	if string(payload) != `{"next":3}` {
		t.Fatalf("overwrite checkpoint = %q", payload)
	}
	if err := store.DeleteJobCheckpoint(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.LoadJobCheckpoint(ctx, job.ID); ok {
		t.Fatal("checkpoint not deleted")
	}
}

// TestClaimPendingJobsAgesBackgroundSources 覆盖 P0 优先级老化：后台任务刚入队时
// 让手动任务先跑；等待超过偏移量后，后台任务能越过新入队任务被领取。
func TestClaimPendingJobsAgesBackgroundSources(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	drafts := make([]JobDraft, 0, 50)
	for index := 0; index < 50; index++ {
		drafts = append(drafts, JobDraft{
			Kind:   JobKindUser,
			Input:  fmt.Sprintf("scheduled-%d", index),
			Title:  "scheduled",
			Source: JobSourceSchedule,
		})
	}
	scheduled, err := store.CreateJobs(ctx, drafts)
	if err != nil || len(scheduled) != len(drafts) {
		t.Fatalf("create scheduled: %d %v", len(scheduled), err)
	}
	manual, err := store.CreateJob(ctx, JobKindUser, "manual", "manual")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPendingJobs(ctx, 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != manual.ID {
		t.Fatalf("claimed %+v %v, want manual job %d", claimed, err, manual.ID)
	}

	aged := scheduled[0]
	agedAt := time.Now().UTC().Add(-31 * time.Minute)
	if _, err := store.db.ExecContext(ctx, `UPDATE jobs SET enqueue_ns = ?, enqueued_at = ? WHERE id = ?`, agedAt.UnixNano(), agedAt, aged.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := store.CreateJob(ctx, JobKindUser, "manual-fresh", "manual")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimPendingJobs(ctx, 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != aged.ID {
		t.Fatalf("claimed %+v %v, want aged scheduled job %d to overtake fresh manual job %d", claimed, err, aged.ID, fresh.ID)
	}
}

// TestCreateJobsAndScheduleSkipActiveDuplicates 覆盖 P0 入队去重：批量粘贴与计划
// 触发都不能为已排队的同 kind+input 再插入一行。
func TestCreateJobsAndScheduleSkipActiveDuplicates(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	active, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.CreateJobs(ctx, []JobDraft{
		{Kind: JobKindUser, Input: "alice", Title: "alice"},
		{Kind: JobKindUser, Input: "alice", Title: "alice"},
		{Kind: JobKindUser, Input: "bob", Title: "bob"},
	})
	if err != nil || len(jobs) != 1 || jobs[0].Input != "bob" {
		t.Fatalf("batch dedupe = %+v %v, want only bob", jobs, err)
	}

	schedule, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name:            "daily",
		Enabled:         true,
		IntervalMinutes: MinArchiveScheduleIntervalMinutes,
		Items:           []ArchiveScheduleItem{{Kind: JobKindUser, Input: "alice"}, {Kind: JobKindUser, Input: "carol"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = store.CreateScheduledArchiveJobs(ctx, schedule, time.Now().UTC())
	if err != nil || len(jobs) != 1 || jobs[0].Input != "carol" {
		t.Fatalf("schedule dedupe = %+v %v, want only carol (alice already active as %d)", jobs, err, active.ID)
	}
}
