package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func TestUpdateConfigClearSentinelRemovesPrimaryCookies(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir: t.TempDir(),
		AuthToken:   "auth-token",
		CSRFToken:   "csrf-token",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateConfig(ctx, config.AppConfig{
		DownloadDir: t.TempDir(),
		AuthToken:   config.ClearSentinel,
		CSRFToken:   config.ClearSentinel,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetStoredConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthToken != "" || got.CSRFToken != "" {
		t.Fatalf("primary cookies were not cleared: auth=%q csrf=%q", got.AuthToken, got.CSRFToken)
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

func TestUpdateConfigInvalidatesLibraryDownloadsCache(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: firstRoot}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstRoot, "first.jpg"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := store.ListLibraryDownloads(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].FilePath != filepath.Join(firstRoot, "first.jpg") {
		t.Fatalf("first library result = %+v", first)
	}
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: secondRoot}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondRoot, "second.jpg"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := store.ListLibraryDownloads(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].FilePath != filepath.Join(secondRoot, "second.jpg") {
		t.Fatalf("stale library cache after config update = %+v", second)
	}
}

// 新语义（config.ResolveSubmission 宽松化）：无法还原的占位符不再阻塞保存——无法还原的
// 行被丢弃、空字段保持空，绝不能把 ******** 写进库。主 Cookie 也必须保留。
func TestUpdateConfigStripsUnresolvableMaskedCookie(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg, err := store.UpdateConfig(ctx, config.AppConfig{AuthToken: "primary", AdditionalCookies: "auth_token=first; ct0=csrf"})
	if err != nil {
		t.Fatal(err)
	}
	for _, submitted := range []string{
		"auth_token=********; ct0=********",
		`[{"id":"unknown","authToken":"********","csrfToken":"********"}]`,
	} {
		updated := cfg
		updated.AdditionalCookies = submitted // 不再改 AuthToken，断言主 Cookie 保留
		got, err := store.UpdateConfig(ctx, updated)
		if err != nil {
			t.Fatalf("lenient parse must accept %q: %v", submitted, err)
		}
		if strings.Contains(got.AdditionalCookies, config.SecretPlaceholder) {
			t.Fatalf("persisted placeholder for %q: %q", submitted, got.AdditionalCookies)
		}
		if got.AuthToken != "primary" {
			t.Fatalf("primary cookie changed: %q", got.AuthToken)
		}
	}
}

func TestLibraryLimitDoesNotTurnRecordedFilesIntoOrphans(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, JobKindMediaURL, "media", "media")
	if err != nil {
		t.Fatal(err)
	}
	var latest DownloadRecord
	for i := 0; i < 2; i++ {
		path := filepath.Join(root, fmt.Sprintf("%d.jpg", i))
		if err := os.WriteFile(path, []byte("photo"), 0o600); err != nil {
			t.Fatal(err)
		}
		latest, err = store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, FilePath: path, MediaURL: fmt.Sprintf("https://pbs.twimg.com/%d.jpg", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(root, "0.jpg"), time.Now(), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListLibraryDownloads(ctx, 1)
	if err != nil || len(items) != 1 || items[0].ID != latest.ID {
		t.Fatalf("limit returned recorded file as orphan: %+v %v", items, err)
	}
}

func TestStoredConfigRefreshesAcrossStoreInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	if _, err := first.GetStoredConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := second.UpdateConfig(ctx, config.AppConfig{AuthToken: "new-token"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		cfg, err := first.GetStoredConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AuthToken == "new-token" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("configuration cache never refreshed external write")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOpenDatabasePathWithURICharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive?#.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database was opened at a different path: %v", err)
	}
}

func TestHydrateSchedulesKeepsHealthyRows(t *testing.T) {
	items, err := hydrateArchiveSchedules([]ArchiveSchedule{
		{ID: 1, ItemsJSON: "broken"},
		{ID: 2, ItemsJSON: `[{"kind":"user","input":"alice"}]`, LastJobIDsJSON: "[]"},
	})
	if err != nil || len(items) != 1 || items[0].ID != 2 {
		t.Fatalf("healthy schedules lost: %+v %v", items, err)
	}
}

// 旧库 / env 指向文件系统根或家目录时，Open 不能失败（否则升级即不可用、用户没有
// UI 可改）：降级为 <dataDir>/downloads 并持久化，同时绝不把 `/` 登记为可信根。
func TestOpenRepairsUnsafeDownloadRoot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	t.Setenv("OPEN_XDOWNLOAD_DOWNLOAD_DIR", string(filepath.Separator))
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("unsafe legacy download root must be repaired, not fatal: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	wantFallback := filepath.Join(dir, "downloads")
	stored, err := store.GetStoredConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DownloadDir != wantFallback {
		t.Fatalf("stored download dir = %q, want fallback %q", stored.DownloadDir, wantFallback)
	}
	effective, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if effective.DownloadDir != wantFallback {
		t.Fatalf("effective download dir = %q, want fallback %q", effective.DownloadDir, wantFallback)
	}
	roots, err := store.DownloadRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if root == string(filepath.Separator) {
			t.Fatalf("filesystem root registered as trusted download root: %+v", roots)
		}
	}
	if !containsString(roots, wantFallback) {
		t.Fatalf("fallback root not seeded: %+v", roots)
	}
}

// user_previous_names 只写不读：v5 迁移必须删除表与触发器（且幂等）。
func TestMigrateDropsUserPreviousNamesTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	// 构造 v4 遗留库：把表/触发器加回来并清掉迁移记录。
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
CREATE TABLE IF NOT EXISTS user_previous_names (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id TEXT NOT NULL,
	screen_name TEXT NOT NULL,
	name TEXT NOT NULL,
	recorded_at DATETIME NOT NULL
);
CREATE TRIGGER IF NOT EXISTS trg_users_name_history AFTER UPDATE OF screen_name, name ON users
WHEN OLD.screen_name <> NEW.screen_name OR OLD.name <> NEW.name
BEGIN
	INSERT INTO user_previous_names (user_id, screen_name, name, recorded_at)
	VALUES (OLD.id, OLD.screen_name, OLD.name, NEW.updated_at);
END;
DELETE FROM schema_migrations WHERE name = 'drop_user_previous_names';`); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var tables int
	if err := store.db.Get(&tables, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'user_previous_names'`); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("user_previous_names table was not dropped")
	}
	var triggers int
	if err := store.db.Get(&triggers, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'trg_users_name_history'`); err != nil {
		t.Fatal(err)
	}
	if triggers != 0 {
		t.Fatal("trg_users_name_history trigger was not dropped")
	}
}

func TestScheduleClaimAcceptsEquivalentTimeRepresentation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	schedule, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{Name: "test", Enabled: true, IntervalMinutes: 5, Items: []ArchiveScheduleItem{{Kind: JobKindUser, Input: "alice"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 领取改用 claim_version 后与 next_run_at 的文本表示无关：这里把 next_run_at 改写成
	// 另一种时区表示，用旧快照仍能领取（旧实现按时间等值比较，遇到表示差异会永远失败）。
	const timestamp = "2026-10-28T09:00:00+08:00"
	if _, err := store.db.Exec(`UPDATE archive_schedules SET next_run_at = ? WHERE id = ?`, timestamp, schedule.ID); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateScheduledArchiveJobs(ctx, schedule, time.Now().UTC())
	if err != nil || len(created) != 1 {
		t.Fatalf("claim must not depend on timestamp text representation: %v %+v", err, created)
	}
}

// 失败推文的退避：TouchFailedTweet 之后自动重试列表应跳过（冷却期内），手动重试
// （force=true）必须忽略退避。任务行本身由 jobs 侧通过 ReserveAutomaticRetryJob 复用，
// 见 TestReserveAutomaticRetryJobReusesRow。
func TestFailedTweetRetryBackoffAndForce(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	source, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice"}); err != nil {
		t.Fatal(err)
	}
	entity, err := store.EnsureUserEntity(ctx, "u1", t.TempDir(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: source.ID, EntityID: entity.ID, TweetID: "1", Payload: "{}", Error: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TouchFailedTweet(ctx, failed.ID, "failed again"); err != nil {
		t.Fatal(err)
	}
	due, err := store.ListFailedTweetsForRetry(ctx, 20, false)
	if err != nil || len(due) != 0 {
		t.Fatalf("automatic retry ignored backoff: %+v %v", due, err)
	}
	manual, err := store.ListFailedTweetsForRetry(ctx, 20, true)
	if err != nil || len(manual) != 1 {
		t.Fatalf("manual retry did not override backoff: %+v %v", manual, err)
	}
	if manual[0].RetryCount != 1 || manual[0].NextRetryAt == nil {
		t.Fatalf("retry state = %+v", manual[0])
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
	if want := "https://video.twimg.com/ext_tw_video/123"; record.MediaKey != want {
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

	items, err := store.FindDownloadsByMediaKey(ctx, "https://pbs.twimg.com/media/abc?format=jpg", 20)
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

func TestRewriteDownloadPathPrefixMovesOnlyRecordsInsideDirectory(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	root := t.TempDir()
	oldDir := filepath.Join(root, "users", "Alice(alice)")
	newDir := filepath.Join(root, "users", "Alice2(alice)")
	// 下划线与百分号若走 LIKE 会被当作通配符：确保前缀按字面量比较。
	sibling := filepath.Join(root, "users", "Alice(alice)_backup", "x.jpg")
	wildcard := filepath.Join(root, "users", "AliceXalice)", "y.jpg")
	records := map[string]string{
		"t1": filepath.Join(oldDir, "a.jpg"),
		"t2": filepath.Join(oldDir, "nested", "b.mp4"),
		"t3": sibling,
		"t4": wildcard,
	}
	ids := map[string]int64{}
	for tweetID, path := range records {
		record, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: tweetID, MediaURL: "https://pbs.twimg.com/media/" + tweetID + ".jpg", FilePath: path})
		if err != nil {
			t.Fatalf("create download: %v", err)
		}
		ids[tweetID] = record.ID
	}

	rewritten, err := store.RewriteDownloadPathPrefix(ctx, oldDir, newDir)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if rewritten != 2 {
		t.Fatalf("rewritten = %d, want 2", rewritten)
	}
	want := map[string]string{
		"t1": filepath.Join(newDir, "a.jpg"),
		"t2": filepath.Join(newDir, "nested", "b.mp4"),
		"t3": sibling,
		"t4": wildcard,
	}
	for tweetID, path := range want {
		record, err := store.GetDownload(ctx, ids[tweetID])
		if err != nil || record == nil {
			t.Fatalf("get download %s: %v", tweetID, err)
		}
		if record.FilePath != path {
			t.Errorf("%s file_path = %q, want %q", tweetID, record.FilePath, path)
		}
	}
}

func TestRewriteDownloadPathPrefixHandlesMultibyteNames(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "zhangsan", "zhangsan")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// 显示名常含中文/emoji：SQLite 的 substr 按字符计数，而 Go len() 按字节计数。
	root := t.TempDir()
	oldDir := filepath.Join(root, "users", "张三🎨(zhangsan)")
	newDir := filepath.Join(root, "users", "张三丰(zhangsan)")
	record, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "t1", MediaURL: "https://pbs.twimg.com/media/t1.jpg", FilePath: filepath.Join(oldDir, "照片.jpg")})
	if err != nil {
		t.Fatalf("create download: %v", err)
	}
	rewritten, err := store.RewriteDownloadPathPrefix(ctx, oldDir, newDir)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got, err := store.GetDownload(ctx, record.ID)
	if err != nil || got == nil {
		t.Fatalf("get download: %v", err)
	}
	if want := filepath.Join(newDir, "照片.jpg"); rewritten != 1 || got.FilePath != want {
		t.Fatalf("rewritten = %d, file_path = %q, want 1 and %q", rewritten, got.FilePath, want)
	}
}

func TestUpsertUserAllowsRecycledScreenName(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// 用户 1 曾叫 foo，改名后没有再归档过，库里仍是旧名；foo 随后被用户 2 注册。
	if _, err := store.UpsertUser(ctx, User{ID: "1", ScreenName: "foo", Name: "Old"}); err != nil {
		t.Fatalf("upsert user 1: %v", err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "2", ScreenName: "foo", Name: "New"}); err != nil {
		t.Fatalf("recycled screen_name must not fail archiving: %v", err)
	}
	// 两个 screen_name 都为空的用户（解析缺字段）同样不能互相冲突。
	if _, err := store.UpsertUser(ctx, User{ID: "3"}); err != nil {
		t.Fatalf("upsert user 3: %v", err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "4"}); err != nil {
		t.Fatalf("empty screen_name must not conflict: %v", err)
	}
}

func TestMigrateDropsLegacyUniqueScreenNameIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// 模拟旧库：唯一索引仍然存在。
	if _, err := store.db.Exec(`DROP INDEX IF EXISTS idx_users_screen_name; CREATE UNIQUE INDEX idx_users_screen_name ON users (screen_name); DELETE FROM schema_migrations WHERE name = 'drop_unique_users_screen_name'`); err != nil {
		t.Fatalf("recreate legacy index: %v", err)
	}
	store.Close()

	store, err = Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	var unique int
	if err := store.db.Get(&unique, `SELECT COUNT(*) FROM pragma_index_list('users') WHERE name = 'idx_users_screen_name' AND "unique" = 1`); err != nil {
		t.Fatalf("inspect index: %v", err)
	}
	if unique != 0 {
		t.Fatal("legacy unique index on users.screen_name should be replaced by a plain index")
	}
}

func TestTouchFailedTweetMovesItToBackOfRetryQueue(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
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
	first, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "1", Payload: "{}", Error: "old"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "2", Payload: "{}", Error: "old"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	// 用显式回写时间建立顺序，而不是 time.Sleep：sleep 依赖调度与时钟分辨率，CI 上会偶发。
	base := time.Now().UTC().Add(-time.Hour)
	if _, err := store.db.Exec(`UPDATE failed_tweets SET updated_at = ? WHERE id = ?`, base, first.ID); err != nil {
		t.Fatalf("backdate first: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE failed_tweets SET updated_at = ? WHERE id = ?`, base.Add(time.Minute), second.ID); err != nil {
		t.Fatalf("backdate second: %v", err)
	}

	if err := store.TouchFailedTweet(ctx, first.ID, "retry failed again"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	items, err := store.ListFailedTweets(ctx, 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 重试失败的条目必须排到队尾，否则前 LIMIT 条一直失败就会永远挡住后面的条目。
	if len(items) != 1 || items[0].TweetID != "2" {
		t.Fatalf("head of retry queue = %+v, want tweet 2 after tweet 1 was requeued", items)
	}
	all, err := store.ListFailedTweetsForRetry(ctx, 10, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if last := all[len(all)-1]; last.TweetID != "1" || last.Error != "retry failed again" {
		t.Fatalf("requeued item = %+v, want tweet 1 with the latest error", last)
	}
	if all[len(all)-1].NextRetryAt == nil || all[len(all)-1].NextRetryAt.Before(time.Now().Add(9*time.Minute)) {
		t.Fatal("retry failure did not establish a ten-minute backoff")
	}
}

func TestLibraryRecognizesRecordedPathWithDifferentCase(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "Photo.jpg")
	if err := os.WriteFile(actual, []byte("photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorded := filepath.Join(root, "photo.jpg")
	if _, err := os.Stat(recorded); os.IsNotExist(err) {
		t.Skip("case-sensitive filesystem")
	} else if err != nil {
		t.Fatal(err)
	}
	items, err := collectOrphanLibraryFiles(context.Background(), root, map[string]struct{}{recorded: {}}, nil)
	if err != nil || len(items) != 0 {
		t.Fatalf("recorded file returned as an orphan: %+v %v", items, err)
	}
}

func TestRetryJobConcurrentDoubleClickCreatesOneJob(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	original, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	original.Status = JobFailed
	original.Error = "boom"
	if err := store.UpdateJob(ctx, original); err != nil {
		t.Fatalf("fail job: %v", err)
	}

	// 双击"重试"或两个标签页同时点：只能生成一个新任务，否则同一目标被重复归档。
	const clicks = 8
	var wg sync.WaitGroup
	var created atomic.Int64
	for i := 0; i < clicks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.RetryJob(ctx, original.ID); err == nil {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("concurrent retries created %d jobs, want 1", created.Load())
	}
	var active int
	if err := store.db.Get(&active, `SELECT COUNT(*) FROM jobs WHERE kind = ? AND input = ? AND status = ?`, JobKindUser, "alice", JobPending); err != nil {
		t.Fatalf("count: %v", err)
	}
	if active != 1 {
		t.Fatalf("pending retry jobs = %d, want 1", active)
	}
}

func TestCollectOrphanLibraryFilesStopsWhenCanceled(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		dir := filepath.Join(root, "users", strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.jpg"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	items, err := collectOrphanLibraryFiles(context.Background(), root, map[string]struct{}{}, nil)
	if err != nil || len(items) != 50 {
		t.Fatalf("live walk = %d items, err %v; want 50", len(items), err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	// 请求被取消（用户切走页面）后不能继续遍历整个下载目录，结果也不能进缓存。
	if _, err := collectOrphanLibraryFiles(canceled, root, map[string]struct{}{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
