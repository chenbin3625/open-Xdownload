package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	if retried.ID == claimed[0].ID {
		t.Fatalf("retried job ID = %d, want a new job", retried.ID)
	}
	if retried.Status != JobPending || retried.Error != "" || retried.Progress != 0 {
		t.Fatalf("retried job = %+v, want a new clean pending job", retried)
	}
	if retried.Kind != claimed[0].Kind || retried.Input != claimed[0].Input || retried.Title != claimed[0].Title {
		t.Fatalf("retried job = %+v, want source task fields preserved", retried)
	}
	original, err := store.GetJob(ctx, claimed[0].ID)
	if err != nil {
		t.Fatalf("get original job: %v", err)
	}
	if original.Status != JobFailed || original.Error != "boom" {
		t.Fatalf("original job = %+v, want failed history unchanged", original)
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

	partial, err := store.GetJob(ctx, ids[2])
	if err != nil {
		t.Fatalf("get partial job: %v", err)
	}
	partial.Status = JobCompletedWithErrors
	if err := store.UpdateJob(ctx, partial); err != nil {
		t.Fatalf("update partial job: %v", err)
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
	if stats.Total != 5 || stats.Active != 2 || stats.Completed != 1 || stats.Failed != 2 {
		t.Fatalf("stats = %+v, want total=5 active=2 completed=1 failed=2", stats)
	}
}

func TestCreateDownloadUpdatesDuplicateTweetMediaRecord(t *testing.T) {
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
	if second.ID != first.ID || second.FilePath != "/tmp/two.mp4" || second.Bytes != 200 || second.JobID != secondJob.ID {
		t.Fatalf("duplicate record = %+v, want updated existing ID %d", second, first.ID)
	}
	items, err := store.ListDownloads(ctx, 10)
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("download count = %d, want 1", len(items))
	}
	if items[0].FilePath != "/tmp/two.mp4" {
		t.Fatalf("stored file path = %q, want updated path", items[0].FilePath)
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

func TestUpdateConfigRestoresRedactedURLUserinfo(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		ProxyURL:  "http://alice:s3cret@proxy.local:3128",
		WebDAVURL: "https://bob:pw@webdav.example/dav",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// 模拟前端流程：GET 配置得到 Redacted() 形式（URL 内嵌凭据被替换为占位符），
	// 改了别的字段后把 Redacted 形式原样 PUT 回去。UpdateConfig 必须还原真实凭据，
	// 否则会把 "********" 当作真实代理/WebDAV 地址保存，破坏下载。
	submitted := config.AppConfig{
		ProxyURL:  "http://" + config.SecretPlaceholder + "@proxy.local:3128",
		WebDAVURL: "https://" + config.SecretPlaceholder + "@webdav.example/dav",
	}
	if _, err := store.UpdateConfig(ctx, submitted); err != nil {
		t.Fatalf("update redacted config: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if got.ProxyURL != "http://alice:s3cret@proxy.local:3128" {
		t.Fatalf("ProxyURL = %q, want restored credentials", got.ProxyURL)
	}
	if got.WebDAVURL != "https://bob:pw@webdav.example/dav" {
		t.Fatalf("WebDAVURL = %q, want restored credentials", got.WebDAVURL)
	}
}

func TestUpdateConfigDoesNotRestoreSecretsForChangedTargets(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		ProxyURL:       "http://alice:s3cret@proxy.local:3128",
		StorageType:    config.StorageWebDAV,
		SMBHost:        "nas.local",
		SMBPort:        445,
		SMBPassword:    "smb-secret",
		WebDAVURL:      "https://webdav.example/dav",
		WebDAVPassword: "webdav-secret",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		ProxyURL:       "http://" + config.SecretPlaceholder + "@evil.local:3128",
		StorageType:    config.StorageWebDAV,
		SMBHost:        "other-nas.local",
		SMBPort:        445,
		SMBPassword:    config.SecretPlaceholder,
		WebDAVURL:      "https://evil.example/dav",
		WebDAVPassword: config.SecretPlaceholder,
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if got.ProxyURL != "http://evil.local:3128" {
		t.Fatalf("ProxyURL = %q, want placeholder credentials dropped for changed host", got.ProxyURL)
	}
	if got.SMBPassword != "" {
		t.Fatalf("SMBPassword = %q, want cleared for changed host", got.SMBPassword)
	}
	if got.WebDAVPassword != "" {
		t.Fatalf("WebDAVPassword = %q, want cleared for changed host", got.WebDAVPassword)
	}
}

func TestCancelJobDoesNotOverrideTerminalStatus(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindTweetLink, "input", "title")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	job.Status = JobCompleted
	job.Progress = 1
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	// 取消一个已完成的任务：条件 UPDATE 不应覆盖终态为 canceled（避免与 worker 的终态保存竞争）。
	got, err := store.CancelJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("cancel completed job: %v", err)
	}
	if got.Status != JobCompleted {
		t.Fatalf("status = %q, want completed (cancel must not override terminal status)", got.Status)
	}
}

func TestUpdateJobDoesNotOverrideCanceledStatus(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindMediaURL, "input", "title")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := store.ClaimPendingJobs(ctx, 1)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := store.CancelJob(ctx, job.ID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	workerCopy := claimed[0]
	workerCopy.Status = JobCompleted
	workerCopy.Progress = 1
	workerCopy.Message = "下载完成"
	if err := store.UpdateJob(ctx, workerCopy); err != nil {
		t.Fatalf("worker update job: %v", err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != JobCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
}

func TestCreateJobsForArchiveScheduleClaimsOnce(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	schedule, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name:            "daily",
		Enabled:         true,
		IntervalMinutes: MinArchiveScheduleIntervalMinutes,
		Items: []ArchiveScheduleItem{{
			Kind:  JobKindUser,
			Input: "openai",
		}},
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	runAt := time.Now().UTC()
	jobs, err := store.CreateJobsForArchiveSchedule(ctx, schedule, runAt)
	if err != nil {
		t.Fatalf("first create jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs length = %d, want 1", len(jobs))
	}
	if _, err := store.CreateJobsForArchiveSchedule(ctx, schedule, runAt); !errors.Is(err, ErrArchiveScheduleAlreadyClaimed) {
		t.Fatalf("second create jobs error = %v, want ErrArchiveScheduleAlreadyClaimed", err)
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

func TestUpdateConfigPersistsNestedTweetMediaSetting(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	_, err = store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir:             t.TempDir(),
		MaxConcurrency:          4,
		AutoRetryFailed:         true,
		IncludeNestedTweetMedia: true,
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if !got.IncludeNestedTweetMedia {
		t.Fatal("IncludeNestedTweetMedia = false, want true")
	}
}
