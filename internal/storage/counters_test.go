package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDashboardCountersTrackJobAndFailedTweetMutations(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	assertMeta := func(want JobStats, wantFailed int) {
		t.Helper()
		stats, failed, err := store.DashboardMeta(ctx)
		if err != nil {
			t.Fatalf("DashboardMeta: %v", err)
		}
		if stats != want || failed != wantFailed {
			t.Fatalf("meta = %+v failed=%d, want %+v failed=%d", stats, failed, want, wantFailed)
		}
	}

	assertMeta(JobStats{}, 0)

	job, err := store.CreateJob(ctx, JobKindMediaURL, "https://example.com/a.mp4", "a")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	assertMeta(JobStats{Total: 1, Active: 1}, 0)

	job.Status = JobCompleted
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	assertMeta(JobStats{Total: 1, Completed: 1}, 0)

	if _, err := store.RetryJob(ctx, job.ID); err != nil {
		t.Fatalf("retry job: %v", err)
	}
	assertMeta(JobStats{Total: 2, Active: 1, Completed: 1}, 0)

	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	entity, err := store.EnsureUserEntity(ctx, "u1", t.TempDir(), "Alice(alice)")
	if err != nil {
		t.Fatalf("ensure entity: %v", err)
	}
	failedTweet := FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "t1", Payload: "{}", Error: "boom"}
	if _, err := store.CreateFailedTweet(ctx, failedTweet); err != nil {
		t.Fatalf("create failed tweet: %v", err)
	}
	assertMeta(JobStats{Total: 2, Active: 1, Completed: 1}, 1)

	if _, err := store.CreateFailedTweet(ctx, failedTweet); err != nil {
		t.Fatalf("upsert failed tweet: %v", err)
	}
	assertMeta(JobStats{Total: 2, Active: 1, Completed: 1}, 1)

	if err := store.DeleteAllFailedTweets(ctx); err != nil {
		t.Fatalf("clear failed tweets: %v", err)
	}
	assertMeta(JobStats{Total: 2, Active: 1, Completed: 1}, 0)
}

func TestJobFilesReturnsNotFoundForMissingJob(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	_, _, err = store.JobFiles(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for missing job")
	}
}
