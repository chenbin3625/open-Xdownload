package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveScheduleBatchSizeRoundTripAndValidation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	schedule, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name: "weekly", Enabled: true, IntervalMinutes: 10080,
		Items: []ArchiveScheduleItem{{
			Kind: JobKindFollowing, Input: "me", ArchiveMode: ArchiveTweets, BatchSize: 25,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetArchiveSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].BatchSize != 25 {
		t.Fatalf("items = %+v, want batchSize 25", got.Items)
	}

	legacy, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name: "daily", Enabled: true, IntervalMinutes: 60,
		Items: []ArchiveScheduleItem{{Kind: JobKindUser, Input: "bob"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Items[0].BatchSize != 0 {
		t.Fatalf("missing batchSize decoded as %d, want 0", legacy.Items[0].BatchSize)
	}

	for _, bad := range []int{-1, MaxArchiveBatchSize + 1} {
		_, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
			Name: "bad", Enabled: true, IntervalMinutes: 60,
			Items: []ArchiveScheduleItem{{Kind: JobKindUser, Input: "alice", BatchSize: bad}},
		})
		var uf *UserFacingError
		if !errors.As(err, &uf) || uf.Status != 400 {
			t.Fatalf("batchSize %d: err = %v, want 400 validation", bad, err)
		}
	}
}

func TestJobBatchSizePersistsThroughCreateRetryAndScheduleClaim(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, JobDraft{
		Kind: JobKindFollowing, Input: "me", ArchiveMode: ArchiveTweets, BatchSize: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.BatchSize != 25 {
		t.Fatalf("created batchSize = %d, want 25", job.BatchSize)
	}
	// RetryJob 只为终态任务创建新任务；先把原任务标记为失败。
	job.Status = JobFailed
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.BatchSize != 25 {
		t.Fatalf("retried batchSize = %d, want 25", retried.BatchSize)
	}

	schedule, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name: "weekly", Enabled: true, IntervalMinutes: 10080,
		Items: []ArchiveScheduleItem{{
			Kind: JobKindFollowing, Input: "you", ArchiveMode: ArchiveTweets, BatchSize: 50,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.CreateScheduledArchiveJobs(ctx, schedule, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].BatchSize != 50 {
		t.Fatalf("scheduled jobs = %+v, want batchSize 50", jobs)
	}
}

func TestMigrateArchiveAddsBatchSizeColumn(t *testing.T) {
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
	if _, err := store.db.Exec(`ALTER TABLE jobs DROP COLUMN batch_size`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateArchive(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.Get(&count, `SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name='batch_size'`); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("batch_size column count = %d, want 1", count)
	}
	reloaded, err := store.GetJob(ctx, job.ID)
	if err != nil || reloaded.BatchSize != 0 {
		t.Fatalf("legacy job = %+v, err = %v; want batchSize 0", reloaded, err)
	}
}
