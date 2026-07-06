package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

func TestClaimPendingJobsMarksClaimedJobs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	for _, input := range []string{"one", "two", "three"} {
		if _, err := store.CreateJob(ctx, JobKindMediaURL, input, input); err != nil {
			t.Fatalf("create job: %v", err)
		}
	}

	first, err := store.ClaimPendingJobs(ctx, 2)
	if err != nil {
		t.Fatalf("claim first batch: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first batch length = %d, want 2", len(first))
	}
	claimed := map[int64]struct{}{}
	for _, job := range first {
		if job.Status != JobResolving {
			t.Fatalf("claimed job status = %s, want %s", job.Status, JobResolving)
		}
		claimed[job.ID] = struct{}{}
	}

	second, err := store.ClaimPendingJobs(ctx, 2)
	if err != nil {
		t.Fatalf("claim second batch: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second batch length = %d, want 1", len(second))
	}
	if _, ok := claimed[second[0].ID]; ok {
		t.Fatalf("second batch re-claimed job %d", second[0].ID)
	}
	if second[0].Status != JobResolving {
		t.Fatalf("second claimed status = %s, want %s", second[0].Status, JobResolving)
	}
}

func TestRequeueInterruptedJobsOnlyResetsActiveStatuses(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	for _, input := range []string{"one", "two", "three"} {
		if _, err := store.CreateJob(ctx, JobKindMediaURL, input, input); err != nil {
			t.Fatalf("create job: %v", err)
		}
	}
	active, err := store.ClaimPendingJobs(ctx, 2)
	if err != nil {
		t.Fatalf("claim jobs: %v", err)
	}
	downloading := active[0]
	downloading.Status = JobDownloading
	downloading.Progress = 0.5
	if err := store.UpdateJob(ctx, downloading); err != nil {
		t.Fatalf("mark downloading: %v", err)
	}
	completed, err := store.GetJob(ctx, 3)
	if err != nil {
		t.Fatalf("get completed job: %v", err)
	}
	completed.Status = JobCompleted
	if err := store.UpdateJob(ctx, completed); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	requeued, err := store.RequeueInterruptedJobs(ctx)
	if err != nil {
		t.Fatalf("requeue interrupted jobs: %v", err)
	}
	if len(requeued) != 2 {
		t.Fatalf("requeued length = %d, want 2", len(requeued))
	}
	for _, job := range requeued {
		if job.Status != JobPending || job.Progress != 0 {
			t.Fatalf("requeued job = %+v, want pending progress 0", job)
		}
	}
	gotCompleted, err := store.GetJob(ctx, completed.ID)
	if err != nil {
		t.Fatalf("get completed after requeue: %v", err)
	}
	if gotCompleted.Status != JobCompleted {
		t.Fatalf("completed status = %s, want %s", gotCompleted.Status, JobCompleted)
	}
}

func TestRetryJobRejectsActiveJobs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindMediaURL, "one", "one")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.RetryJob(ctx, job.ID); err == nil {
		t.Fatal("RetryJob on pending job succeeded, want error")
	}
	claimed, err := store.ClaimPendingJobs(ctx, 1)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := store.RetryJob(ctx, claimed[0].ID); err == nil {
		t.Fatal("RetryJob on resolving job succeeded, want error")
	}
	claimed[0].Status = JobFailed
	claimed[0].Error = "boom"
	if err := store.UpdateJob(ctx, claimed[0]); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	retried, err := store.RetryJob(ctx, claimed[0].ID)
	if err != nil {
		t.Fatalf("retry failed job: %v", err)
	}
	if retried.Status != JobPending || retried.Error != "" {
		t.Fatalf("retried job = %+v, want clean pending", retried)
	}
}

func TestListJobsPageAndJobStats(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	ids := make([]int64, 0, 5)
	for _, input := range []string{"one", "two", "three", "four", "five"} {
		job, err := store.CreateJob(ctx, JobKindMediaURL, input, input)
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		ids = append(ids, job.ID)
	}

	completed, err := store.GetJob(ctx, ids[0])
	if err != nil {
		t.Fatalf("get completed job: %v", err)
	}
	completed.Status = JobCompleted
	if err := store.UpdateJob(ctx, completed); err != nil {
		t.Fatalf("update completed job: %v", err)
	}

	failed, err := store.GetJob(ctx, ids[1])
	if err != nil {
		t.Fatalf("get failed job: %v", err)
	}
	failed.Status = JobFailed
	if err := store.UpdateJob(ctx, failed); err != nil {
		t.Fatalf("update failed job: %v", err)
	}

	page, err := store.ListJobsPage(ctx, 2, 2)
	if err != nil {
		t.Fatalf("list jobs page: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page length = %d, want 2", len(page))
	}
	if page[0].ID != ids[2] || page[1].ID != ids[1] {
		t.Fatalf("page IDs = [%d %d], want [%d %d]", page[0].ID, page[1].ID, ids[2], ids[1])
	}

	stats, err := store.JobStats(ctx)
	if err != nil {
		t.Fatalf("job stats: %v", err)
	}
	if stats.Total != 5 || stats.Active != 3 || stats.Completed != 1 || stats.Failed != 1 {
		t.Fatalf("stats = %+v, want total=5 active=3 completed=1 failed=1", stats)
	}
}

func TestCreateDownloadDeduplicatesTweetMedia(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	firstJob, err := store.CreateJob(ctx, JobKindMediaURL, "one", "one")
	if err != nil {
		t.Fatalf("create first job: %v", err)
	}
	secondJob, err := store.CreateJob(ctx, JobKindMediaURL, "two", "two")
	if err != nil {
		t.Fatalf("create second job: %v", err)
	}
	first, err := store.CreateDownload(ctx, DownloadRecord{
		JobID:    firstJob.ID,
		TweetID:  "tweet-1",
		MediaURL: "https://example.com/media.mp4",
		FilePath: "/tmp/one.mp4",
		Bytes:    100,
	})
	if err != nil {
		t.Fatalf("create first download: %v", err)
	}
	second, err := store.CreateDownload(ctx, DownloadRecord{
		JobID:    secondJob.ID,
		TweetID:  "tweet-1",
		MediaURL: "https://example.com/media.mp4",
		FilePath: "/tmp/two.mp4",
		Bytes:    200,
	})
	if err != nil {
		t.Fatalf("create duplicate download: %v", err)
	}
	if second.ID != first.ID || second.FilePath != first.FilePath || second.Bytes != first.Bytes {
		t.Fatalf("duplicate record = %+v, want existing %+v", second, first)
	}
	items, err := store.ListDownloads(ctx, 10)
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("download count = %d, want 1", len(items))
	}
}

func TestUpdateConfigPreservesRedactedTokens(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	_, err = store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir:     t.TempDir(),
		MaxConcurrency:  4,
		AuthToken:       "real-auth-token",
		CSRFToken:       "real-csrf-token",
		AutoRetryFailed: true,
	})
	if err != nil {
		t.Fatalf("seed config: %v", err)
	}

	_, err = store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir:     t.TempDir(),
		MaxConcurrency:  2,
		AuthToken:       "********",
		CSRFToken:       "********",
		AutoRetryFailed: true,
	})
	if err != nil {
		t.Fatalf("update redacted config: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if got.AuthToken != "real-auth-token" || got.CSRFToken != "real-csrf-token" {
		t.Fatalf("tokens were not preserved: auth=%q csrf=%q", got.AuthToken, got.CSRFToken)
	}
}

func TestUpdateConfigPersistsFilenameSettings(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	_, err = store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir:       t.TempDir(),
		MaxConcurrency:    4,
		FileNamingMode:    config.FileNamingUserTweet,
		MaxFilenameLength: 96,
		AutoRetryFailed:   true,
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if got.FileNamingMode != config.FileNamingUserTweet {
		t.Fatalf("FileNamingMode = %q, want %q", got.FileNamingMode, config.FileNamingUserTweet)
	}
	if got.MaxFilenameLength != 96 {
		t.Fatalf("MaxFilenameLength = %d, want 96", got.MaxFilenameLength)
	}
}
