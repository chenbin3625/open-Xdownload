package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestUnavailableMediaRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	const mediaURL = "https://video.twimg.com/media.mp4"

	got, err := store.GetUnavailableMedia(ctx, mediaURL)
	if err != nil {
		t.Fatalf("get missing unavailable media: %v", err)
	}
	if got != nil {
		t.Fatalf("missing unavailable media = %+v, want nil", got)
	}
	if err := store.UpsertUnavailableMedia(ctx, UnavailableMedia{
		MediaURL: mediaURL,
		TweetID:  "tweet-1",
		Error:    "DMCA",
	}); err != nil {
		t.Fatalf("upsert unavailable media: %v", err)
	}
	got, err = store.GetUnavailableMedia(ctx, mediaURL)
	if err != nil {
		t.Fatalf("get unavailable media: %v", err)
	}
	if got == nil || got.TweetID != "tweet-1" || got.Error != "DMCA" || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("unavailable media = %+v, want persisted record", got)
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

	meta, failedTweetCount, err := store.DashboardMeta(ctx)
	if err != nil {
		t.Fatalf("dashboard meta: %v", err)
	}
	if meta != stats {
		t.Fatalf("DashboardMeta stats = %+v, want %+v", meta, stats)
	}
	counted, err := store.CountFailedTweets(ctx)
	if err != nil {
		t.Fatalf("count failed tweets: %v", err)
	}
	if failedTweetCount != counted {
		t.Fatalf("failed tweet count = %d, want %d", failedTweetCount, counted)
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
	if second.ID != first.ID || second.FilePath != "/tmp/two.mp4" || second.Bytes != 200 || second.JobID != firstJob.ID {
		t.Fatalf("duplicate record = %+v, want existing ID %d with original job_id %d", second, first.ID, firstJob.ID)
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
	if items[0].JobID != firstJob.ID {
		t.Fatalf("stored job_id = %d, want original job_id %d", items[0].JobID, firstJob.ID)
	}
	// 历史保留：该下载仍属于首次记录的 job，而不是后来重复下载的 job。
	firstJobItems, err := store.ListDownloadsForJobs(ctx, []int64{firstJob.ID})
	if err != nil {
		t.Fatalf("list downloads for first job: %v", err)
	}
	if len(firstJobItems) != 1 || firstJobItems[0].ID != first.ID {
		t.Fatalf("first job downloads = %+v, want 1 record %d", firstJobItems, first.ID)
	}
	secondJobItems, err := store.ListDownloadsForJobs(ctx, []int64{secondJob.ID})
	if err != nil {
		t.Fatalf("list downloads for second job: %v", err)
	}
	if len(secondJobItems) != 0 {
		t.Fatalf("second job downloads = %+v, want 0 records", secondJobItems)
	}
}

func TestCreateDownloadEmptyTweetIDKeepsOriginalJobOnDuplicate(t *testing.T) {
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
		MediaURL: "https://example.com/direct.mp4",
		FilePath: "/tmp/one.mp4",
		Bytes:    100,
	})
	if err != nil {
		t.Fatalf("create first download: %v", err)
	}
	second, err := store.CreateDownload(ctx, DownloadRecord{
		JobID:    secondJob.ID,
		MediaURL: "https://example.com/direct.mp4",
		FilePath: "/tmp/two.mp4",
		Bytes:    200,
	})
	if err != nil {
		t.Fatalf("create duplicate download: %v", err)
	}
	// tweet_id 为空时同样按 media_url 去重，且保留首次记录的 job_id，只更新文件信息。
	if second.ID != first.ID || second.FilePath != "/tmp/two.mp4" || second.Bytes != 200 || second.JobID != firstJob.ID {
		t.Fatalf("duplicate record = %+v, want existing ID %d with original job_id %d", second, first.ID, firstJob.ID)
	}
	stored, err := store.GetDownloadByTweetMedia(ctx, "", "https://example.com/direct.mp4")
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if stored == nil || stored.JobID != firstJob.ID || stored.FilePath != "/tmp/two.mp4" {
		t.Fatalf("stored download = %+v, want job_id %d with updated path", stored, firstJob.ID)
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
		ProxyURL: "http://alice:s3cret@proxy.local:3128",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// 模拟前端流程：GET 配置得到 Redacted() 形式（URL 内嵌凭据被替换为占位符），
	// 改了别的字段后把 Redacted 形式原样 PUT 回去。UpdateConfig 必须还原真实凭据，
	// 否则会把 "********" 当作真实代理地址保存，破坏下载。
	submitted := config.AppConfig{
		ProxyURL: "http://" + config.SecretPlaceholder + "@proxy.local:3128",
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
}

func TestUpdateConfigDoesNotRestoreSecretsForChangedTargets(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		ProxyURL: "http://alice:s3cret@proxy.local:3128",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		ProxyURL: "http://" + config.SecretPlaceholder + "@evil.local:3128",
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

func TestUpdateConfigDoesNotPersistEnvOnlyValues(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	t.Setenv(config.EnvAuthToken, "env-auth-token")
	t.Setenv(config.EnvProxyURL, "http://env-proxy:8080")

	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir:    t.TempDir(),
		MaxConcurrency: 2,
		AuthToken:      "env-auth-token",
		CSRFToken:      "env-ct0",
		ProxyURL:       "http://env-proxy:8080",
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	stored, err := store.GetStoredConfig(ctx)
	if err != nil {
		t.Fatalf("get stored config: %v", err)
	}
	if stored.AuthToken != "" || stored.ProxyURL != "" {
		t.Fatalf("env-only values persisted to db: auth=%q proxy=%q", stored.AuthToken, stored.ProxyURL)
	}

	effective, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if effective.AuthToken != "env-auth-token" || effective.ProxyURL != "http://env-proxy:8080" {
		t.Fatalf("runtime env overrides lost: auth=%q proxy=%q", effective.AuthToken, effective.ProxyURL)
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

func TestPruneFailedRecordsRemovesOnlyOldRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindMediaURL, "https://example.com/m.mp4", "m")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	entity, err := store.EnsureUserEntity(ctx, "u1", t.TempDir(), "Alice(alice)")
	if err != nil {
		t.Fatalf("ensure entity: %v", err)
	}

	if _, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "old", Payload: `{}`, Error: "old"}); err != nil {
		t.Fatalf("create old failed tweet: %v", err)
	}
	if _, err := store.CreateFailedMedia(ctx, FailedMedia{JobID: job.ID, MediaURL: "https://example.com/old.mp4", Error: "old"}); err != nil {
		t.Fatalf("create old failed media: %v", err)
	}
	// 把唯一一条记录改写到很久以前。
	oldTime := time.Now().UTC().Add(-400 * 24 * time.Hour)
	if _, err := store.db.Exec(`UPDATE failed_tweets SET updated_at = ? WHERE tweet_id = 'old'`, oldTime); err != nil {
		t.Fatalf("backdate failed tweet: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE failed_media SET created_at = ? WHERE media_url = ?`, oldTime, "https://example.com/old.mp4"); err != nil {
		t.Fatalf("backdate failed media: %v", err)
	}

	if _, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "fresh", Payload: `{}`, Error: "fresh"}); err != nil {
		t.Fatalf("create fresh failed tweet: %v", err)
	}

	pruned, err := store.PruneFailedRecords(ctx, time.Now().UTC().Add(-180*24*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 2 {
		t.Fatalf("pruned = %d, want 2 (1 failed tweet + 1 failed media)", pruned)
	}
	if left, err := store.CountFailedTweets(ctx); err != nil || left != 1 {
		t.Fatalf("remaining failed tweets = %d, %v; want 1", left, err)
	}
	media, err := store.ListFailedMedia(ctx, 10)
	if err != nil || len(media) != 0 {
		t.Fatalf("remaining failed media = %#v, %v; want none", media, err)
	}
}

func TestLocateUserEntitiesBatchesExistingArchiveState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	parent := t.TempDir()

	for _, user := range []User{
		{ID: "u1", ScreenName: "one", Name: "One"},
		{ID: "u2", ScreenName: "two", Name: "Two"},
	} {
		if _, err := store.UpsertUser(ctx, user); err != nil {
			t.Fatalf("upsert %s: %v", user.ID, err)
		}
	}
	first, err := store.EnsureUserEntity(ctx, "u1", parent, "One(one)")
	if err != nil {
		t.Fatalf("ensure u1 entity: %v", err)
	}
	if err := store.UpdateUserEntityMediaCount(ctx, first.ID, 12); err != nil {
		t.Fatalf("update u1 entity: %v", err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u2", parent, "Two(two)"); err != nil {
		t.Fatalf("ensure u2 entity: %v", err)
	}

	entities, err := store.LocateUserEntities(ctx, []string{"u1", "u2", "u1", "missing"}, parent)
	if err != nil {
		t.Fatalf("locate entities: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("entity count = %d, want 2", len(entities))
	}
	if got := entities["u1"].MediaCount.Int64; got != 12 {
		t.Fatalf("u1 media count = %d, want 12", got)
	}
}

func TestListFailedTweetViewsPageReturnsTotal(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindMediaURL, "https://example.com/m.mp4", "m")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	entity, err := store.EnsureUserEntity(ctx, "u1", t.TempDir(), "Alice(alice)")
	if err != nil {
		t.Fatalf("ensure entity: %v", err)
	}
	for _, tweetID := range []string{"1", "2", "3"} {
		if _, err := store.CreateFailedTweet(ctx, FailedTweet{
			JobID:    job.ID,
			EntityID: entity.ID,
			TweetID:  tweetID,
			Payload:  `{}`,
			Error:    "boom",
		}); err != nil {
			t.Fatalf("create failed tweet %s: %v", tweetID, err)
		}
	}

	items, total, err := store.ListFailedTweetViewsPage(ctx, 2, 2)
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if total != 3 || len(items) != 1 || items[0].TweetID == "" {
		t.Fatalf("page = %+v total=%d, want 1 item with tweet id and total 3", items, total)
	}

	empty, emptyTotal, err := store.ListFailedTweetViewsPage(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if emptyTotal != 3 || len(empty) != 3 {
		t.Fatalf("all = %d total=%d, want 3/3", len(empty), emptyTotal)
	}

	past, pastTotal, err := store.ListFailedTweetViewsPage(ctx, 10, 40)
	if err != nil {
		t.Fatalf("list past end: %v", err)
	}
	if pastTotal != 3 || len(past) != 0 {
		t.Fatalf("past end = %d total=%d, want 0/3", len(past), pastTotal)
	}
}

func TestOpenAppliesPerformancePragmas(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	var journal string
	if err := store.db.Get(&journal, `PRAGMA journal_mode`); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}

	var synchronous int
	if err := store.db.Get(&synchronous, `PRAGMA synchronous`); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Fatalf("synchronous = %d, want NORMAL(1)", synchronous)
	}

	var tempStore int
	if err := store.db.Get(&tempStore, `PRAGMA temp_store`); err != nil {
		t.Fatalf("temp_store: %v", err)
	}
	if tempStore != 2 {
		t.Fatalf("temp_store = %d, want MEMORY(2)", tempStore)
	}

	// foreign_keys / busy_timeout 也必须断言：DSN 打错字时它们会静默失效
	// （外键不再级联删除、写锁冲突直接 SQLITE_BUSY），而其余用例全绿。
	var foreignKeys int
	if err := store.db.Get(&foreignKeys, `PRAGMA foreign_keys`); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := store.db.Get(&busyTimeout, `PRAGMA busy_timeout`); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	// 唯一索引必须在 Open 之后存在：CreateDownload 的 ON CONFLICT 依赖它，
	// 缺失时每次写入都会失败（见 normalizeDownloadsMediaURL 里的索引重建）。
	for _, index := range []string{"idx_downloads_tweet_media_unique", "idx_downloads_media_url_unique"} {
		var count int
		if err := store.db.Get(&count, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index); err != nil {
			t.Fatalf("check %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("%s exists = %d, want 1", index, count)
		}
	}
}

func TestGetStoredConfigCachesUntilUpdate(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	downloadDir := filepath.Join(t.TempDir(), "cached-downloads")
	updated, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: downloadDir})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	first, err := store.GetStoredConfig(ctx)
	if err != nil {
		t.Fatalf("get stored config: %v", err)
	}
	if first.DownloadDir != updated.DownloadDir {
		t.Fatalf("cached dir = %q, want %q", first.DownloadDir, updated.DownloadDir)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE app_config SET download_dir = ? WHERE id = 1`, "/bypassed-cache"); err != nil {
		t.Fatalf("bypass cache: %v", err)
	}
	cached, err := store.GetStoredConfig(ctx)
	if err != nil {
		t.Fatalf("get cached config: %v", err)
	}
	if cached.DownloadDir == "/bypassed-cache" {
		t.Fatal("GetStoredConfig served a disk write that skipped the cache")
	}
	if cached.DownloadDir != updated.DownloadDir {
		t.Fatalf("cached dir = %q, want %q", cached.DownloadDir, updated.DownloadDir)
	}

	refreshedDir := filepath.Join(t.TempDir(), "refreshed-downloads")
	refreshed, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: refreshedDir})
	if err != nil {
		t.Fatalf("refresh config: %v", err)
	}
	after, err := store.GetStoredConfig(ctx)
	if err != nil {
		t.Fatalf("get refreshed config: %v", err)
	}
	if after.DownloadDir != refreshed.DownloadDir {
		t.Fatalf("cache not invalidated: %q vs %q", after.DownloadDir, refreshed.DownloadDir)
	}
}

func TestCreateDownloadPersistsMediaKey(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "alice", "用户 alice")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	record, err := store.CreateDownload(ctx, DownloadRecord{
		JobID:    job.ID,
		TweetID:  "tweet-1",
		MediaURL: "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=12",
		FilePath: "/archive/alice/abc.mp4",
		Bytes:    100,
	})
	if err != nil {
		t.Fatalf("create download: %v", err)
	}
	if want := "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc"; record.MediaKey != want {
		t.Fatalf("media_key = %q, want %q", record.MediaKey, want)
	}
	if record.MediaKey != "" && strings.Contains(record.MediaKey, "tag=") {
		t.Fatalf("media_key 保留了易变参数: %q", record.MediaKey)
	}
}

func TestFindDownloadsByMediaKeyFindsEquivalentURLs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindList, "list-1", "列表")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// 两条推文（转推/引用场景）复用同一份媒体，URL 写法不同但内容相同。
	for index, item := range []struct {
		tweetID  string
		mediaURL string
		filePath string
	}{
		{"tweet-1", "https://pbs.twimg.com/media/abc.jpg", "/archive/alice/abc.jpg"},
		{"tweet-2", "https://pbs.twimg.com/media/abc?format=jpg", "/archive/bob/abc.jpg"},
		{"tweet-3", "https://pbs.twimg.com/media/other.jpg", "/archive/bob/other.jpg"},
	} {
		if _, err := store.CreateDownload(ctx, DownloadRecord{
			JobID:    job.ID,
			TweetID:  item.tweetID,
			MediaURL: item.mediaURL,
			FilePath: item.filePath,
			Bytes:    int64(index + 1),
		}); err != nil {
			t.Fatalf("create download %s: %v", item.tweetID, err)
		}
	}

	items, err := store.FindDownloadsByMediaKey(ctx, "https://pbs.twimg.com/media/abc", 20)
	if err != nil {
		t.Fatalf("find by media key: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("matched %d records, want 2 (both tweet copies of the same media)", len(items))
	}
	if items[0].TweetID != "tweet-1" || items[1].TweetID != "tweet-2" {
		t.Fatalf("unexpected order: %q, %q", items[0].TweetID, items[1].TweetID)
	}

	empty, err := store.FindDownloadsByMediaKey(ctx, "   ", 20)
	if err != nil {
		t.Fatalf("find with empty key: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty key matched %d records, want 0", len(empty))
	}
}

func TestCleanupLibraryDownloadsRemovesMissingAndDuplicateContent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindList, "list-1", "列表")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	root := t.TempDir()
	keeperPath := filepath.Join(root, "video-a.mp4")
	duplicatePath := filepath.Join(root, "video-a(1).mp4")
	uniquePath := filepath.Join(root, "video-b.mp4")
	missingPath := filepath.Join(root, "missing.mp4")
	for path, body := range map[string]string{
		keeperPath:    "same video body",
		duplicatePath: "same video body",
		uniquePath:    "unique video body",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for index, item := range []struct {
		tweetID  string
		mediaURL string
		path     string
	}{
		{"tweet-1", "https://video.twimg.com/a.mp4", keeperPath},
		{"tweet-2", "https://video.twimg.com/b.mp4", duplicatePath},
		{"tweet-3", "https://video.twimg.com/c.mp4", uniquePath},
		{"tweet-4", "https://video.twimg.com/d.mp4", missingPath},
	} {
		if _, err := store.CreateDownload(ctx, DownloadRecord{
			JobID:    job.ID,
			TweetID:  item.tweetID,
			MediaURL: item.mediaURL,
			FilePath: item.path,
			Bytes:    int64(index + 1),
		}); err != nil {
			t.Fatalf("create download %s: %v", item.tweetID, err)
		}
	}

	result, err := store.CleanupLibraryDownloads(ctx, root)
	if err != nil {
		t.Fatalf("cleanup library downloads: %v", err)
	}
	if result.Scanned != 4 || result.MissingRecords != 1 || result.DuplicateRecords != 1 || result.DuplicateFiles != 1 {
		t.Fatalf("cleanup result = %+v, want scanned=4 missing=1 duplicateRecords=1 duplicateFiles=1", result)
	}
	if result.BytesFreed != int64(len("same video body")) {
		t.Fatalf("bytes freed = %d, want duplicate file size", result.BytesFreed)
	}
	if _, err := os.Stat(duplicatePath); !os.IsNotExist(err) {
		t.Fatalf("duplicate file still exists or unexpected error: %v", err)
	}
	if _, err := os.Stat(keeperPath); err != nil {
		t.Fatalf("keeper missing: %v", err)
	}
	if _, err := os.Stat(uniquePath); err != nil {
		t.Fatalf("unique missing: %v", err)
	}
	items, err := store.ListDownloads(ctx, 10)
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("downloads count = %d, want 2", len(items))
	}
	for _, item := range items {
		if item.ContentHash == "" {
			t.Fatalf("content hash was not backfilled for %+v", item)
		}
		if item.FilePath == missingPath || item.FilePath == duplicatePath {
			t.Fatalf("stale record still present: %+v", item)
		}
	}
}
