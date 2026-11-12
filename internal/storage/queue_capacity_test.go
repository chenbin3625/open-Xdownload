package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fillJobQueue(t *testing.T, store *Store, count int) []Job {
	t.Helper()
	drafts := make([]JobDraft, count)
	for i := range drafts {
		drafts[i] = JobDraft{Kind: JobKindUser, Input: fmt.Sprintf("queued-%d", i), Title: "queued"}
	}
	jobs, err := store.CreateJobs(context.Background(), drafts)
	if err != nil || len(jobs) != count {
		t.Fatalf("fill queue: count=%d err=%v", len(jobs), err)
	}
	return jobs
}

func TestQueueCapacityRejectsNewJobsAtomically(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	fillJobQueue(t, store, 499)
	_, err = store.CreateJobs(ctx, []JobDraft{
		{Kind: JobKindUser, Input: "extra-a"}, {Kind: JobKindUser, Input: "extra-b"},
	})
	if err == nil || !strings.Contains(err.Error(), "队列已满") {
		t.Fatalf("oversized batch error = %v, want queue full", err)
	}
	var count int
	if err := store.db.Get(&count, `SELECT COUNT(*) FROM jobs`); err != nil || count != 499 {
		t.Fatalf("partially inserted batch: count=%d err=%v", count, err)
	}
	jobs, err := store.CreateJobs(ctx, []JobDraft{
		{Kind: JobKindUser, Input: "queued-0"}, {Kind: JobKindUser, Input: "last"},
	})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("deduplication must precede capacity check: jobs=%d err=%v", len(jobs), err)
	}
	for name, create := range map[string]func() error{
		"plain": func() error { _, err := store.CreateJob(ctx, JobKindUser, "plain", "plain"); return err },
		"archive": func() error {
			_, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "archive"})
			return err
		},
		"automatic retry": func() error { _, err := store.ReserveAutomaticRetryJob(ctx); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := create(); err == nil || !strings.Contains(err.Error(), "队列已满") {
				t.Fatalf("error = %v, want queue full", err)
			}
		})
	}
}

func TestQueueCapacityIncludesRunningAndCoolingJobs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	fillJobQueue(t, store, 500)
	claimed, err := store.ClaimPendingJobs(ctx, 2)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if _, err := store.RequeueJobAt(ctx, claimed[0].ID, time.Now().Add(time.Hour), "cooling"); err != nil {
		t.Fatalf("already admitted job must requeue when full: %v", err)
	}
	if _, err := store.CreateJob(ctx, JobKindUser, "overflow", "overflow"); err == nil {
		t.Fatal("running and cooling jobs did not count toward queue capacity")
	}
	if _, err := store.CancelJob(ctx, claimed[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, JobKindUser, "replacement", "replacement"); err != nil {
		t.Fatalf("terminal job did not release queue capacity: %v", err)
	}
}

func TestQueueCapacityConcurrentSubmissions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fillJobQueue(t, store, 499)
	results := make(chan error, 12)
	var group sync.WaitGroup
	for i := range 12 {
		group.Go(func() {
			_, err := store.CreateJob(context.Background(), JobKindUser, fmt.Sprintf("concurrent-%d", i), "concurrent")
			results <- err
		})
	}
	group.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !strings.Contains(err.Error(), "队列已满") {
			t.Fatalf("unexpected submission error: %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted=%d, want exactly one remaining slot", accepted)
	}
}

func TestQueueCapacityAppliesToManualAndReusedAutomaticRetries(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	manual, err := store.CreateJob(ctx, JobKindUser, "retry-target", "retry-target")
	if err != nil {
		t.Fatal(err)
	}
	automatic, err := store.ReserveAutomaticRetryJob(ctx)
	if err != nil || automatic == nil {
		t.Fatalf("automatic retry: %v %v", automatic, err)
	}
	for _, job := range []Job{manual, *automatic} {
		job.Status = JobCompleted
		if _, err := store.UpdateJobAndGet(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	fillJobQueue(t, store, 500)
	if _, err := store.RetryJob(ctx, manual.ID); err == nil {
		t.Fatal("manual retry bypassed full queue")
	}
	if _, err := store.ReserveAutomaticRetryJob(ctx); err == nil {
		t.Fatal("reused automatic retry bypassed full queue")
	}
	job, err := store.GetJob(ctx, automatic.ID)
	if err != nil || job.Status != JobCompleted {
		t.Fatalf("rejected automatic retry changed status: %s %v", job.Status, err)
	}
}
