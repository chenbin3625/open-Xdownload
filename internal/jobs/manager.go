package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

type Manager struct {
	store      *storage.Store
	parser     *parser.Service
	eventBus   *EventBus
	downloader *downloader.Downloader
	wake       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	active     map[int64]context.CancelFunc
	stopCancel context.CancelFunc
	// runCtx 是调度循环的根上下文。后台任务（如封面回填）从它派生，
	// 这样 Stop() 才能真正取消它们，而不是只等超时。
	runCtx     context.Context
	wg         sync.WaitGroup
	retryMu    sync.Mutex
	userMu     sync.Mutex
	userLocks  map[string]*keyLock
	mediaMu    sync.Mutex
	mediaLocks map[string]*keyLock
	xPoolMu    sync.Mutex
	xPoolKey   string
	cachedPool *xclient.Pool

	// lastMaintenance 记录上次后台维护时间（M7：定期清理过期的失败记录）。仅在
	// 调度循环单 goroutine 中读写，无需加锁。
	lastMaintenance time.Time

	// 封面批量回填（媒体库按钮触发）的运行状态与取消句柄。同一时间只允许一个
	// 回填任务；状态经 /api/library/posters/backfill 暴露给前端轮询。
	posterBackfillMu     sync.Mutex
	posterBackfillCancel context.CancelFunc
	posterBackfillStatus PosterBackfillStatus
}

const (
	// failedRecordRetention 失败推文/失败媒体记录的保留期；超过后由后台维护清理。
	failedRecordRetention = 180 * 24 * time.Hour
	// maintenanceInterval 后台维护执行间隔。
	maintenanceInterval = 12 * time.Hour
	// progressWriteInterval 归档进度写入的最小时隔，避免每个媒体一次 DB 写 + SSE
	// 发布造成写放大（P1）。终态与每用户完成仍即时写入。
	progressWriteInterval = 400 * time.Millisecond
	// managerStopTimeout 关停时等待调度循环与活跃任务退出的最长时间。
	managerStopTimeout = 15 * time.Second
)

func NewManager(store *storage.Store, parserService *parser.Service, eventBus *EventBus) *Manager {
	return &Manager{
		store:      store,
		parser:     parserService,
		eventBus:   eventBus,
		downloader: downloader.New(),
		wake:       make(chan struct{}, 1),
		active:     make(map[int64]context.CancelFunc),
		userLocks:  make(map[string]*keyLock),
		mediaLocks: make(map[string]*keyLock),
	}
}

func (m *Manager) Start(ctx context.Context) {
	m.once.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		m.mu.Lock()
		m.stopCancel = cancel
		m.runCtx = runCtx
		m.mu.Unlock()
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.loop(runCtx)
		}()
	})
}

// Stop 取消调度循环及所有活跃任务，并在有界时间内等待它们退出。
// 超时后仍返回，避免 SIGTERM 因个别阻塞路径无法结束进程；调用方随后可关闭数据库。
func (m *Manager) Stop() {
	m.mu.Lock()
	stop := m.stopCancel
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(managerStopTimeout):
		m.mu.Lock()
		ids := make([]int64, 0, len(m.active))
		for id := range m.active {
			ids = append(ids, id)
		}
		m.mu.Unlock()
		log.Printf("manager.Stop: %d task(s) did not drain within %s: %v", len(ids), managerStopTimeout, ids)
	}
}

// backgroundParent 返回后台任务应当派生的父上下文：优先用调度循环的根上下文，
// 使 Stop() 能取消这些任务；Start() 未被调用时（测试）退回调用方传入的上下文。
func (m *Manager) backgroundParent(fallback context.Context) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runCtx != nil {
		return m.runCtx
	}
	return fallback
}

func (m *Manager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) publish(event Event) {
	if m == nil || m.eventBus == nil {
		return
	}
	m.eventBus.Publish(event)
}

func (m *Manager) attachMeta(ctx context.Context, event *Event) {
	if m.store == nil || event == nil {
		return
	}
	stats, count, err := m.store.DashboardMeta(ctx)
	if err != nil {
		return
	}
	event.Meta = storage.DashboardMetaView{Stats: stats, FailedTweetCount: count}
}

func isTerminalJobStatus(status storage.JobStatus) bool {
	switch status {
	case storage.JobCompleted, storage.JobCompletedWithErrors, storage.JobFailed, storage.JobCanceled:
		return true
	default:
		return false
	}
}

func (m *Manager) publishJob(ctx context.Context, typ string, job storage.Job) {
	event := Event{Type: typ, JobID: job.ID, Payload: job}
	if typ == "job.created" || isTerminalJobStatus(job.Status) {
		m.attachMeta(ctx, &event)
	}
	m.publish(event)
}

func (m *Manager) publishJobsCreated(ctx context.Context, created []storage.Job) {
	if len(created) == 0 {
		return
	}
	if len(created) == 1 {
		m.publishJob(ctx, "job.created", created[0])
		return
	}
	event := Event{Type: "jobs.created", Payload: created}
	m.attachMeta(ctx, &event)
	m.publish(event)
}

func (m *Manager) CancelJob(ctx context.Context, id int64) (storage.Job, error) {
	job, err := m.store.CancelJob(ctx, id)
	if err != nil {
		return storage.Job{}, err
	}
	m.mu.Lock()
	cancel := m.active[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.publishJob(ctx, "job.updated", job)
	return job, nil
}

func (m *Manager) RetryFailedTweetsNow(ctx context.Context) (storage.Job, error) {
	job, err := m.store.CreateJob(ctx, storage.JobKindFailedRetry, "failed-tweets", "失败推文重试")
	if err != nil {
		return storage.Job{}, err
	}
	m.Notify()
	m.publishJob(ctx, "job.created", job)
	return job, nil
}

func (m *Manager) RunArchiveScheduleNow(ctx context.Context, id int64) ([]storage.Job, error) {
	schedule, err := m.store.GetArchiveSchedule(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(schedule.LastJobIDs) > 0 {
		active, err := m.store.HasActiveJobs(ctx, schedule.LastJobIDs)
		if err != nil {
			return nil, err
		}
		if active {
			return nil, fmt.Errorf("该计划上次创建的任务仍在运行")
		}
	}
	jobs, err := m.store.CreateJobsForArchiveSchedule(ctx, schedule, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	m.publishJobsCreated(ctx, jobs)
	m.publish(Event{Type: "archive_schedule.ran", Payload: map[string]any{"id": schedule.ID, "jobCount": len(jobs)}})
	m.Notify()
	return jobs, nil
}

func (m *Manager) loop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runOnce(ctx)
		case <-m.wake:
			m.runOnce(ctx)
		}
	}
}

func (m *Manager) runOnce(ctx context.Context) {
	m.maybeMaintain(ctx)
	m.enqueueDueArchiveSchedules(ctx)

	cfg, err := m.store.GetConfig(ctx)
	if err != nil {
		return
	}
	available := m.availableSlots(cfg.MaxConcurrency)
	if available <= 0 {
		return
	}
	jobs, err := m.store.ClaimPendingJobs(ctx, available)
	if err != nil || len(jobs) == 0 {
		return
	}
	for _, job := range jobs {
		m.startJob(ctx, job)
	}
}

func (m *Manager) enqueueDueArchiveSchedules(ctx context.Context) {
	now := time.Now().UTC()
	schedules, err := m.store.ListDueArchiveSchedules(ctx, now, 10)
	if err != nil {
		return
	}
	createdAny := false
	for _, schedule := range schedules {
		if len(schedule.LastJobIDs) > 0 {
			active, err := m.store.HasActiveJobs(ctx, schedule.LastJobIDs)
			if err != nil {
				continue
			}
			if active {
				next := now.Add(1 * time.Minute)
				if _, err := m.store.RescheduleArchiveSchedule(ctx, schedule.ID, next); err == nil {
					m.publish(Event{Type: "archive_schedule.updated", Payload: map[string]any{"id": schedule.ID, "nextRunAt": next}})
				}
				continue
			}
		}
		jobs, err := m.store.CreateJobsForArchiveSchedule(ctx, schedule, now)
		if err != nil {
			if errors.Is(err, storage.ErrArchiveScheduleAlreadyClaimed) {
				continue
			}
			continue
		}
		if len(jobs) == 0 {
			continue
		}
		createdAny = true
		m.publishJobsCreated(ctx, jobs)
		m.publish(Event{Type: "archive_schedule.ran", Payload: map[string]any{"id": schedule.ID, "jobCount": len(jobs)}})
	}
	if createdAny {
		m.Notify()
	}
}

func (m *Manager) availableSlots(_ int) int {
	const limit = 1
	m.mu.Lock()
	defer m.mu.Unlock()
	return limit - len(m.active)
}

// maybeMaintain 周期性执行后台维护（M7）：清理超过保留期的失败推文/失败媒体记录，
// 防止 failed_tweets / failed_media 无界增长。downloads 历史保留。
func (m *Manager) maybeMaintain(ctx context.Context) {
	now := time.Now()
	if !m.lastMaintenance.IsZero() && now.Sub(m.lastMaintenance) < maintenanceInterval {
		return
	}
	m.lastMaintenance = now
	if pruned, err := m.store.PruneFailedRecords(ctx, now.Add(-failedRecordRetention)); err != nil {
		log.Printf("prune stale failed records: %v", err)
	} else if pruned > 0 {
		log.Printf("pruned %d stale failed records", pruned)
	}
}

func (m *Manager) startJob(parent context.Context, job storage.Job) {
	jobCtx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	if _, exists := m.active[job.ID]; exists {
		m.mu.Unlock()
		cancel()
		return
	}
	m.active[job.ID] = cancel
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		defer cancel()
		defer m.finishJob(job.ID)
		// 处理不可信外部数据（推文/远端响应）时第三方库可能 panic；recover 防止
		// 单个坏 payload 崩溃整个进程并把任务留在 resolving/downloading。
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic in job %d: %v\n%s", job.ID, r, debug.Stack())
				m.fail(context.Background(), job, "", fmt.Errorf("internal panic: %v", r))
			}
		}()
		m.process(jobCtx, context.Background(), job)
	}()
}

func (m *Manager) finishJob(id int64) {
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}

func (m *Manager) process(ctx context.Context, saveCtx context.Context, job storage.Job) {
	if m.jobCanceled(ctx, saveCtx, job.ID) {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	m.save(saveCtx, job)

	switch job.Kind {
	case storage.JobKindTweetLink:
		m.processTweetLink(ctx, saveCtx, job)
	case storage.JobKindMediaURL:
		m.processMediaURL(ctx, saveCtx, job, job.Input, "")
	case storage.JobKindUser:
		m.processUser(ctx, saveCtx, job)
	case storage.JobKindList:
		m.processList(ctx, saveCtx, job)
	case storage.JobKindFollowing:
		m.processFollowing(ctx, saveCtx, job)
	case storage.JobKindFailedRetry:
		m.processFailedRetry(ctx, saveCtx, job)
	default:
		job.Status = storage.JobFailed
		job.Progress = 1
		job.Message = "当前任务类型还在迁移中"
		job.Error = fmt.Sprintf("unsupported job kind: %s", job.Kind)
		m.save(saveCtx, job)
	}
}

func (m *Manager) processTweetLink(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	tweet, err := m.parser.ParseTweetLinkWithClient(ctx, job.Input, httpx.Client(cfg.ProxyURL, 20*time.Second), parserOptionsFromConfig(cfg))
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	if len(tweet.Media) == 0 {
		job.Status = storage.JobFailed
		job.Progress = 1
		job.Message = "链接已识别，但还没有解析到媒体 URL"
		job.Error = "这条推文没有可下载媒体"
		m.save(saveCtx, job)
		return
	}
	unavailable := 0
	lastProgressSave := time.Time{}
	for index, media := range tweet.Media {
		if ctx.Err() != nil {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		mediaURL := bestMediaURL(media)
		if mediaURL == "" {
			continue
		}
		job.Status = storage.JobDownloading
		job.Progress = 0.2 + 0.7*float64(index)/float64(len(tweet.Media))
		job.Message = fmt.Sprintf("正在下载 %d/%d", index+1, len(tweet.Media))
		job.Error = ""
		if lastProgressSave.IsZero() || time.Since(lastProgressSave) >= progressWriteInterval {
			lastProgressSave = time.Now()
			m.save(saveCtx, job)
		}
		result, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, "", tweetFilename(cfg, tweet, index), media.Type == parser.MediaPhoto, tweet.CreatedAt, media.PreviewURL)
		if err != nil {
			if isCancellation(ctx, err) {
				m.handleInterrupt(ctx, saveCtx, job)
				return
			}
			m.fail(saveCtx, job, mediaURL, err)
			return
		}
		if result.unavailable {
			unavailable++
		}
	}
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	job.Status = storage.JobCompleted
	job.Progress = 1
	job.Message = "下载完成"
	if unavailable > 0 {
		job.Message = fmt.Sprintf("下载完成：永久不可用 %d 个，已跳过", unavailable)
	}
	job.Error = ""
	m.save(saveCtx, job)
}

// singleURLFilenameHint derives a filename hint from the media URL's last path
// segment so distinct single-URL downloads get distinct filenames instead of
// all colliding on "media" (which silently dropped the second distinct media
// as a "duplicate"). Falls back to "media" when the URL has no usable basename.
func singleURLFilenameHint(mediaURL string) string {
	if u, err := url.Parse(mediaURL); err == nil && u.Path != "" {
		if base := path.Base(u.Path); base != "" && base != "/" && base != "." {
			return base
		}
	}
	return "media"
}

func (m *Manager) processMediaURL(ctx context.Context, saveCtx context.Context, job storage.Job, mediaURL string, tweetID string) {
	job.Status = storage.JobDownloading
	job.Progress = 0.25
	job.Message = "正在下载媒体"
	job.Error = ""
	m.save(saveCtx, job)
	result, err := m.download(ctx, saveCtx, job, mediaURL, tweetID, singleURLFilenameHint(mediaURL))
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, mediaURL, err)
		return
	}
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	job.Status = storage.JobCompleted
	job.Progress = 1
	job.Message = "下载完成"
	if result.unavailable {
		job.Message = "媒体永久不可用，已跳过"
	}
	job.Error = ""
	m.save(saveCtx, job)
}

func (m *Manager) processUser(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, pool, err := m.xPool(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	user, err := pool.GetUserByInput(ctx, job.Input)
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	stats, err := m.archiveUsers(ctx, saveCtx, job, cfg, pool, []xclient.User{user}, nil, 0.12, 0.94)
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	retried := m.retryFailedTweets(ctx, saveCtx, job, cfg, false)
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	m.completeArchive(saveCtx, job, stats, retried)
}

func (m *Manager) processFailedRetry(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	job.Status = storage.JobDownloading
	job.Progress = 0.2
	job.Message = "正在重试失败推文"
	job.Error = ""
	m.save(saveCtx, job)
	retried := m.retryFailedTweets(ctx, saveCtx, job, cfg, true)
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	remaining, err := m.store.CountFailedTweets(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	if remaining > 0 {
		job.Status = storage.JobCompletedWithErrors
	} else {
		job.Status = storage.JobCompleted
	}
	job.Progress = 1
	job.Message = fmt.Sprintf("失败推文重试完成：成功 %d，剩余 %d", retried, remaining)
	job.Error = ""
	m.save(saveCtx, job)
}

func (m *Manager) processList(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, pool, err := m.xPool(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	client := pool.Primary()
	list, err := client.GetListByID(ctx, strings.TrimSpace(job.Input))
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	if _, err := m.store.UpsertList(saveCtx, storage.List{ID: list.ID, Name: list.Name, OwnerUserID: list.Creator.ID}); err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	listEntity, err := m.ensureListEntity(saveCtx, cfg, "lists", list.ID, fmt.Sprintf("%s(%s)", list.Name, list.ID))
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	job.Message = "正在获取列表成员"
	job.Progress = 0.12
	m.save(saveCtx, job)
	members, err := client.GetListMembers(ctx, list)
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	stats, err := m.archiveUsers(ctx, saveCtx, job, cfg, pool, members, &listEntity, 0.18, 0.94)
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	retried := m.retryFailedTweets(ctx, saveCtx, job, cfg, false)
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	m.completeArchive(saveCtx, job, stats, retried)
}

func (m *Manager) processFollowing(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, pool, err := m.xPool(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	client := pool.Primary()
	owner, err := pool.GetUserByInput(ctx, job.Input)
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	if _, err := m.store.UpsertUser(saveCtx, storageUser(owner)); err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	job.Message = "正在获取关注用户"
	job.Progress = 0.12
	m.save(saveCtx, job)
	members, err := client.GetFollowing(ctx, owner)
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	stats, err := m.archiveUsers(ctx, saveCtx, job, cfg, pool, members, nil, 0.18, 0.94)
	if err != nil {
		if isCancellation(ctx, err) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	retried := m.retryFailedTweets(ctx, saveCtx, job, cfg, false)
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	m.completeArchive(saveCtx, job, stats, retried)
}

func (m *Manager) download(ctx context.Context, saveCtx context.Context, job storage.Job, mediaURL string, tweetID string, filenameHint string) (mediaDownloadResult, error) {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	// 与推文归档路径保持一致的去重键：去掉 ?tag= 这类易变参数，使直接媒体 URL 任务与
	// 推文归档对同一媒体使用同一个 (tweet_id, media_url) 记录，重复执行时能直接跳过。
	mediaURL = downloader.NormalizeMediaURL(mediaURL)
	return m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweetID, "", filenameHint, isPhotoURL(mediaURL), time.Time{})
}

type mediaDownloadResult struct {
	skipped     bool
	unavailable bool
}

func (m *Manager) downloadMedia(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, mediaURL string, tweetID string, dir string, filenameHint string, largePhoto bool, modTime time.Time, previewURLs ...string) (mediaDownloadResult, error) {
	previewURL := ""
	if len(previewURLs) > 0 {
		previewURL = previewURLs[0]
	}
	// 媒体锁按媒体身份（而不是 URL 字符串）串行化：同一份媒体的不同 URL 写法、以及
	// tweet_id 为空的直接媒体 URL 任务都落到同一把锁上，避免并发各自下载同一媒体、
	// 或在同名同大小判定完成前同时落盘出 photo.jpg / photo(1).jpg 两份副本。
	mediaKey := downloader.MediaIdentity(mediaURL)
	release, err := m.lockMedia(ctx, mediaKey)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	if release != nil {
		defer release()
	}
	existing, unavailable, err := m.store.GetMediaDownloadState(saveCtx, tweetID, mediaURL)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	if unavailable {
		return mediaDownloadResult{skipped: true, unavailable: true}, nil
	}
	target, err := filestore.New(cfg)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	if dir == "" {
		dir = target.Root()
	}
	// 磁盘上已经有同一份媒体时直接跳过，不再下载（既不创建硬链接也不复制副本）：
	// 目标目录下确实存在同名同大小的文件时才算已归档。
	skipped, err := m.skipArchivedMedia(ctx, saveCtx, job, cfg, target, existing, mediaKey, mediaURL, tweetID, dir, filenameHint, previewURL)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	if skipped {
		return mediaDownloadResult{skipped: true}, nil
	}
	options := downloader.Options{
		ModTime:           modTime,
		LargePhoto:        largePhoto,
		ProxyURL:          cfg.ProxyURL,
		MaxFilenameLength: cfg.MaxFilenameLength,
	}
	if target.Type() == config.StorageLocal {
		// 磁盘上的同名文件可能来自另一条推文（相同文本命名导致路径冲突）。只有 downloads 表
		// 中已存在本推文+本媒体且 file_path 与该路径一致的记录，才认定该文件属于本次下载，
		// 允许跳过/覆盖；否则视为他推文的文件，下载器会写入带编号后缀的路径，绝不复用。
		options.ExistingFileOwner = func(path string) bool {
			record, err := m.store.GetDownloadByTweetMedia(saveCtx, tweetID, mediaURL)
			if err != nil || record == nil {
				return false
			}
			return strings.TrimSpace(record.FilePath) != "" && filepath.Clean(record.FilePath) == filepath.Clean(path)
		}
	}
	result, err := target.SaveMedia(ctx, m.downloader, mediaURL, dir, filenameHint, options)
	if err != nil {
		if isPermanentlyUnavailableMediaError(err) {
			if markErr := m.store.UpsertUnavailableMedia(saveCtx, storage.UnavailableMedia{
				MediaURL: mediaURL,
				TweetID:  tweetID,
				Error:    err.Error(),
			}); markErr != nil {
				return mediaDownloadResult{}, fmt.Errorf("记录永久不可用媒体失败: %v: %w", markErr, err)
			}
			_, _ = m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: err.Error()})
			return mediaDownloadResult{skipped: true, unavailable: true}, nil
		}
		return mediaDownloadResult{}, err
	}
	if result.Skipped {
		if _, err := m.store.CreateDownload(saveCtx, storage.DownloadRecord{
			JobID:      job.ID,
			TweetID:    tweetID,
			MediaURL:   mediaURL,
			PreviewURL: previewURL,
			FilePath:   result.Path,
			Bytes:      result.Bytes,
		}); err != nil {
			return mediaDownloadResult{}, err
		}
		return mediaDownloadResult{skipped: true}, nil
	}
	_, err = m.store.CreateDownload(saveCtx, storage.DownloadRecord{
		JobID:      job.ID,
		TweetID:    tweetID,
		MediaURL:   mediaURL,
		PreviewURL: previewURL,
		FilePath:   result.Path,
		Bytes:      result.Bytes,
	})
	return mediaDownloadResult{}, err
}

// skipArchivedMedia 判断本次要归档的媒体是否已经存在于目标目录，命中时直接跳过：
// 不下载、不创建硬链接、也不复制副本。
//
//  1. 本推文+本媒体已有记录且文件仍在 → 已归档，跳过（沿用既有行为）；
//  2. 同一份媒体的其他副本（转推、引用推文与卡片媒体会复用同一条媒体 URL）中存在
//     "目标目录下已有同名同大小的文件"时 → 视为已归档，跳过。
//
// 第 2 步用已知副本作为参照：文件名由本次的文件名提示与副本扩展名推导（与下载落盘
// 命名规则一致），只有目标目录下确实存在同名且字节数一致的文件才算命中，避免把内容
// 不同但同名的文件误判为已下载。返回 true 表示本次已跳过。
func (m *Manager) skipArchivedMedia(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, target filestore.Store, existing *storage.DownloadRecord, mediaKey string, mediaURL string, tweetID string, dir string, filenameHint string, previewURL string) (bool, error) {
	if existing != nil {
		if path := strings.TrimSpace(existing.FilePath); path != "" {
			info, err := os.Stat(path)
			if err != nil && !os.IsNotExist(err) {
				return false, err
			}
			if err == nil && info.Mode().IsRegular() {
				// 已下载过的媒体也要补预览：历史记录的 preview_url 与本地海报可能缺失，
				// 用本次解析重新读到的预览图地址回填（失败只记日志，不影响任务）。
				m.ensureVideoPoster(ctx, cfg, target, existing, previewURL)
				return true, nil
			}
		}
	}
	copies, err := m.store.FindDownloadsByMediaKey(saveCtx, mediaKey, 20)
	if err != nil {
		return false, err
	}
	inDir, elsewhere := splitCopiesByDir(copies, dir)
	for _, group := range [][]storage.DownloadRecord{inDir, elsewhere} {
		for _, copy := range group {
			archived, size, err := archivedFileState(copy.FilePath)
			if err != nil {
				return false, err
			}
			if !archived {
				continue
			}
			expectedPath := filepath.Join(dir, downloader.Filename(copy.FilePath, filenameHint, "", cfg.MaxFilenameLength))
			expected, expectedSize, err := archivedFileState(expectedPath)
			if err != nil {
				return false, err
			}
			if !expected || expectedSize != size {
				continue
			}
			record, err := m.store.CreateDownload(saveCtx, storage.DownloadRecord{
				JobID:      job.ID,
				TweetID:    tweetID,
				MediaURL:   mediaURL,
				PreviewURL: previewURL,
				FilePath:   expectedPath,
				Bytes:      expectedSize,
			})
			if err != nil {
				return false, err
			}
			m.ensureVideoPoster(ctx, cfg, target, &record, previewURL)
			return true, nil
		}
	}
	return false, nil
}

// splitCopiesByDir 把同一媒体的历史副本按"是否位于本次目标目录"分成两组，优先用目标
// 目录内的副本作为同名同大小判定的参照（同目录内的副本最可靠）。
func splitCopiesByDir(copies []storage.DownloadRecord, dir string) ([]storage.DownloadRecord, []storage.DownloadRecord) {
	inDir := make([]storage.DownloadRecord, 0, len(copies))
	elsewhere := make([]storage.DownloadRecord, 0, len(copies))
	seen := make(map[string]struct{}, len(copies))
	for _, copy := range copies {
		path := strings.TrimSpace(copy.FilePath)
		if path == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		if pathWithinDir(cleaned, dir) {
			inDir = append(inDir, copy)
			continue
		}
		elsewhere = append(elsewhere, copy)
	}
	return inDir, elsewhere
}

// archivedFileState 报告 path 处是否已经放着普通文件，并返回其字节数。
func archivedFileState(path string) (bool, int64, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	if !info.Mode().IsRegular() {
		return false, 0, nil
	}
	return true, info.Size(), nil
}

// pathWithinDir 报告 path 是否位于 dir 目录内（含 dir 本身），用于判断已有媒体是否
// 就在本次归档目标目录里；比较时统一按绝对路径与文件分隔符处理。
func pathWithinDir(path string, dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if cleanPath == cleanDir {
		return false
	}
	relative, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// keyLock 是按 key 串行化的令牌锁，带 waiter 计数（持有者 + 等待者）。在无人持有时从
// map 中淘汰，避免 mediaLocks/userLocks 随处理过的媒体/用户数无界增长。计数在父 mutex
// 下增减，确保淘汰时不会有等待者滞留在旧 channel 上。
type keyLock struct {
	ch      chan struct{}
	waiters int
}

func (m *Manager) lockMedia(ctx context.Context, mediaKey string) (func(), error) {
	if mediaKey == "" {
		return nil, nil
	}
	key := mediaKey
	m.mediaMu.Lock()
	lk, ok := m.mediaLocks[key]
	if !ok {
		lk = &keyLock{ch: make(chan struct{}, 1)}
		lk.ch <- struct{}{}
		m.mediaLocks[key] = lk
	}
	lk.waiters++
	m.mediaMu.Unlock()

	select {
	case <-lk.ch:
		return func() { m.releaseKeyLock(&m.mediaMu, m.mediaLocks, key, lk) }, nil
	case <-ctx.Done():
		m.abortKeyLock(&m.mediaMu, m.mediaLocks, key, lk)
		return nil, ctx.Err()
	}
}

// releaseKeyLock 归还令牌；若已无等待者则从 map 淘汰该 key，否则把令牌交给下一个等待者。
func (m *Manager) releaseKeyLock(mu *sync.Mutex, table map[string]*keyLock, key string, lk *keyLock) {
	mu.Lock()
	lk.waiters--
	if lk.waiters <= 0 {
		delete(table, key)
	} else {
		lk.ch <- struct{}{}
	}
	mu.Unlock()
}

// abortKeyLock 在等待者因 ctx 取消而放弃时递减计数，并在无人持有/等待时淘汰。
func (m *Manager) abortKeyLock(mu *sync.Mutex, table map[string]*keyLock, key string, lk *keyLock) {
	mu.Lock()
	lk.waiters--
	if lk.waiters <= 0 {
		delete(table, key)
	}
	mu.Unlock()
}

type archiveStats struct {
	Users      int
	Tweets     int
	Downloaded int
	Skipped    int
	Failed     int
	Issues     []string
}

type archiveUserTask struct {
	index        int
	order        int
	missingMedia int
	primaryOnly  bool
	user         xclient.User
}

func (m *Manager) xPool(ctx context.Context) (config.AppConfig, *xclient.Pool, error) {
	cfg, err := m.store.GetConfig(ctx)
	if err != nil {
		return config.AppConfig{}, nil, err
	}
	key := xPoolConfigKey(cfg)
	m.xPoolMu.Lock()
	defer m.xPoolMu.Unlock()
	if m.cachedPool != nil && m.xPoolKey == key {
		return cfg, m.cachedPool, nil
	}
	pool, err := xclient.NewPool(cfg)
	if err != nil {
		return config.AppConfig{}, nil, err
	}
	m.cachedPool = pool
	m.xPoolKey = key
	return cfg, pool, nil
}

func xPoolConfigKey(cfg config.AppConfig) string {
	return strings.Join([]string{
		cfg.AuthToken,
		cfg.CSRFToken,
		cfg.AdditionalCookies,
		cfg.ProxyURL,
	}, "\x00")
}

func (m *Manager) archiveUsers(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, pool *xclient.Pool, users []xclient.User, listEntity *storage.ListEntity, start float64, end float64) (archiveStats, error) {
	stats := archiveStats{}
	if len(users) == 0 {
		return stats, nil
	}
	tasks, skipped, err := m.archiveUserTasks(ctx, cfg, users)
	if err != nil {
		return stats, err
	}
	stats.Skipped += skipped
	if len(tasks) == 0 {
		return stats, nil
	}

	progressJob := job

	addStats := func(delta archiveStats) {
		stats.Users += delta.Users
		stats.Tweets += delta.Tweets
		stats.Downloaded += delta.Downloaded
		stats.Skipped += delta.Skipped
		stats.Failed += delta.Failed
		stats.Issues = append(stats.Issues, delta.Issues...)
	}

	completed := 0
	lastProgressWrite := time.Time{}
	// saveProgress 用于进度类中间状态，按 progressWriteInterval 节流（P1），丢弃中间
	// 更新不会影响正确性：终态由 finishTask/completeArchive 即时写入。
	saveProgress := func(status storage.JobStatus, message string) {
		if now := time.Now(); now.Sub(lastProgressWrite) < progressWriteInterval {
			return
		} else {
			lastProgressWrite = now
		}
		progressJob.Status = status
		progressJob.Progress = start + (end-start)*float64(completed)/float64(len(tasks))
		progressJob.Message = message
		progressJob.Error = ""
		m.save(saveCtx, progressJob)
	}
	// finishTask 每完成一个用户即时写入：写放大主要来自"每个媒体"的进度保存；
	// 每用户的完成事件频率低，即时写入可让进度条保持准确。
	finishTask := func() {
		completed++
		progressJob.Status = storage.JobResolving
		progressJob.Progress = start + (end-start)*float64(completed)/float64(len(tasks))
		progressJob.Message = fmt.Sprintf("已同步用户 %d/%d", completed, len(tasks))
		progressJob.Error = ""
		m.save(saveCtx, progressJob)
	}

	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		saveProgress(
			storage.JobResolving,
			fmt.Sprintf("同步用户 %d/%d @%s", task.order+1, len(tasks), fallbackUserName(task.user)),
		)
		delta, err := m.archiveUser(ctx, saveCtx, job, cfg, pool, task.user, listEntity, func(message string) {
			saveProgress(storage.JobDownloading, message)
		})
		addStats(delta)
		if err != nil {
			return stats, err
		}
		finishTask()
	}
	return stats, nil
}

func (m *Manager) archiveUserTasks(ctx context.Context, cfg config.AppConfig, users []xclient.User) ([]archiveUserTask, int, error) {
	target, err := filestore.New(cfg)
	if err != nil {
		return nil, 0, err
	}
	parent := target.Join(target.Root(), "users")
	seen := map[string]struct{}{}
	tasks := make([]archiveUserTask, 0, len(users))
	userIDs := make([]string, 0, len(users))
	skipped := 0
	for index, user := range users {
		if err := ctx.Err(); err != nil {
			return nil, skipped, err
		}
		if user.ID == "" || user.Blocking || user.Muting {
			continue
		}
		if _, ok := seen[user.ID]; ok {
			skipped++
			continue
		}
		seen[user.ID] = struct{}{}
		tasks = append(tasks, archiveUserTask{
			index: index,
			user:  user,
		})
		userIDs = append(userIDs, user.ID)
	}
	existingEntities, err := m.store.LocateUserEntities(ctx, userIDs, parent)
	if err != nil {
		return nil, skipped, err
	}
	for index := range tasks {
		user := tasks[index].user
		missingMedia := user.MediaCount
		if existing, ok := existingEntities[user.ID]; ok && existing.MediaCount.Valid {
			missingMedia = max(0, user.MediaCount-int(existing.MediaCount.Int64))
		}
		tasks[index].missingMedia = missingMedia
		tasks[index].primaryOnly = user.Protected && user.Following
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].primaryOnly != tasks[j].primaryOnly {
			return tasks[i].primaryOnly
		}
		if tasks[i].missingMedia != tasks[j].missingMedia {
			return tasks[i].missingMedia > tasks[j].missingMedia
		}
		return tasks[i].index < tasks[j].index
	})
	for index := range tasks {
		tasks[index].order = index
	}
	return tasks, skipped, nil
}

func (m *Manager) archiveUser(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, pool *xclient.Pool, user xclient.User, listEntity *storage.ListEntity, updateDownloading func(message string)) (archiveStats, error) {
	stats := archiveStats{}
	release, waited, err := m.lockUser(ctx, user.ID)
	if err != nil {
		return stats, err
	}
	defer release()

	if _, err := m.store.UpsertUser(saveCtx, storageUser(user)); err != nil {
		return stats, err
	}
	entity, dir, err := m.ensureUserEntity(saveCtx, cfg, user)
	if err != nil {
		return stats, err
	}
	stats.Users++
	if listEntity != nil {
		if err := m.ensureUserLink(saveCtx, cfg, *listEntity, user, dir); err != nil {
			stats.Failed++
			stats.Issues = append(stats.Issues, fmt.Sprintf("创建 @%s 的归档链接失败: %v", fallbackUserName(user), err))
		}
	}
	if waited {
		stats.Skipped++
		return stats, nil
	}
	if user.Protected && !user.Following {
		if cfg.AutoFollowProtected && !user.Requested && pool != nil && pool.Primary() != nil {
			_ = pool.Primary().FollowUser(ctx, user)
		}
		return stats, nil
	}
	if pool == nil {
		stats.Failed++
		stats.Issues = append(stats.Issues, fmt.Sprintf("读取 @%s 的媒体时间线失败: 没有可用 X 客户端", fallbackUserName(user)))
		return stats, nil
	}

	opts := parserOptionsFromConfig(cfg)
	if cfg.IncrementalArchive {
		// 增量归档（配置开关）：从上次成功位置早停，节省 X API 配额。游标随每次
		// 归档无条件写入，因此开关切换后无需迁移即可生效。
		opts.StopAtTweetID = entity.LastSeenTweetID
	}
	tweets, err := pool.GetUserMediaWithOptions(ctx, user, opts)
	if err != nil {
		if isCancellation(ctx, err) {
			return stats, err
		}
		stats.Failed++
		stats.Issues = append(stats.Issues, fmt.Sprintf("读取 @%s 的媒体时间线失败: %v", fallbackUserName(user), err))
		return stats, nil
	}
	if len(tweets) == 0 {
		_ = m.store.UpdateUserEntityMediaCount(saveCtx, entity.ID, user.MediaCount)
		return stats, nil
	}
	stats.Tweets += len(tweets)
	failedBefore := stats.Failed
	for _, tweet := range tweets {
		for mediaIndex, media := range tweet.Media {
			if ctx.Err() != nil {
				return stats, context.Canceled
			}
			mediaURL := bestMediaURL(media)
			if mediaURL == "" {
				continue
			}
			if updateDownloading != nil {
				updateDownloading(fmt.Sprintf("下载 @%s 的媒体", fallbackUserName(user)))
			}
			result, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, dir, tweetFilename(cfg, tweet, mediaIndex), media.Type == parser.MediaPhoto, tweet.CreatedAt, media.PreviewURL)
			if err != nil {
				if isCancellation(ctx, err) {
					return stats, err
				}
				stats.Failed++
				_, _ = m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: err.Error()})
				if shouldRetryMediaError(err) {
					_ = m.rememberFailedTweet(saveCtx, job, entity, tweet, err)
				}
				continue
			}
			if result.skipped {
				stats.Skipped++
			} else {
				stats.Downloaded++
			}
		}
	}
	if len(tweets) > 0 && stats.Failed == failedBefore {
		// 游标取本轮见到的最新推文 ID（数值最大）。timeline 按时间倒序，但首页可能含
		// 置顶推文（ID 较旧却排在最前），若用 tweets[0].ID 作游标，下次增量归档会在首页
		// 对其精确命中而立即早停，漏掉比置顶更新的推文。无论开关是否开启都写入，切换
		// 开关后游标始终可用。写入失败需上抛日志，否则增量归档会因游标缺失而全量重扫。
		if err := m.store.UpdateUserEntityLastSeenTweet(saveCtx, entity.ID, newestTweetID(tweets)); err != nil {
			log.Printf("update user entity %d last_seen_tweet_id: %v", entity.ID, err)
		}
	}
	_ = m.store.UpdateUserEntityMediaCount(saveCtx, entity.ID, user.MediaCount)
	return stats, nil
}

func (m *Manager) lockUser(ctx context.Context, userID string) (func(), bool, error) {
	if userID == "" {
		return func() {}, false, nil
	}
	m.userMu.Lock()
	lk, ok := m.userLocks[userID]
	if !ok {
		lk = &keyLock{ch: make(chan struct{}, 1)}
		lk.ch <- struct{}{}
		m.userLocks[userID] = lk
	}
	lk.waiters++
	m.userMu.Unlock()

	// 非阻塞尝试：立即拿到说明该用户当前未被归档。
	select {
	case <-lk.ch:
		return func() { m.releaseKeyLock(&m.userMu, m.userLocks, userID, lk) }, false, nil
	default:
	}
	// 需要等待：该用户正在被归档（"已在运行"）。
	select {
	case <-lk.ch:
		return func() { m.releaseKeyLock(&m.userMu, m.userLocks, userID, lk) }, true, nil
	case <-ctx.Done():
		m.abortKeyLock(&m.userMu, m.userLocks, userID, lk)
		return nil, true, ctx.Err()
	}
}

func (m *Manager) rememberFailedTweet(ctx context.Context, job storage.Job, entity storage.UserEntity, tweet parser.TweetData, err error) error {
	payload, marshalErr := json.Marshal(tweet)
	if marshalErr != nil {
		return marshalErr
	}
	_, createErr := m.store.CreateFailedTweet(ctx, storage.FailedTweet{
		JobID:    job.ID,
		EntityID: entity.ID,
		TweetID:  tweet.ID,
		Payload:  string(payload),
		Error:    err.Error(),
	})
	return createErr
}

func (m *Manager) retryFailedTweets(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, force bool) int {
	if !force && !cfg.AutoRetryFailed {
		return 0
	}
	// 全局重试队列：用 TryLock 避免多个并发任务同时重试同一批 failed tweets，
	// 否则会因并发下载同一媒体而产生 (1) 副本。抢不到锁说明已有任务在重试，本次跳过。
	if !m.retryMu.TryLock() {
		return 0
	}
	defer m.retryMu.Unlock()
	items, err := m.store.ListFailedTweets(saveCtx, 200)
	if err != nil {
		return 0
	}
	retried := 0
	for _, item := range items {
		if ctx.Err() != nil {
			return retried
		}
		entity, err := m.store.GetUserEntity(saveCtx, item.EntityID)
		if err != nil {
			continue
		}
		var tweet parser.TweetData
		if err := json.Unmarshal([]byte(item.Payload), &tweet); err != nil {
			continue
		}
		target, err := filestore.New(cfg)
		if err != nil {
			continue
		}
		dir := target.Join(entity.ParentDir, entity.Name)
		failed := false
		for index, media := range tweet.Media {
			mediaURL := bestMediaURL(media)
			if mediaURL == "" {
				continue
			}
			// 跳过已下载的媒体，避免重试时把之前已成功的部分再下一遍。
			if _, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, dir, tweetFilename(cfg, tweet, index), media.Type == parser.MediaPhoto, tweet.CreatedAt, media.PreviewURL); err != nil {
				// 只有确实永久失效（404/410/DMCA）才写入 unavailable_media：该表以
				// media_url 为键且对所有推文生效，一旦写错就永久拦截。X 对过期的
				// video.twimg.com 签名例行返回裸 403，那是瞬时状态，必须留在失败
				// 队列里等下次重试，而不是拉黑。
				if isPermanentlyUnavailableMediaError(err) {
					if markErr := m.store.UpsertUnavailableMedia(saveCtx, storage.UnavailableMedia{
						MediaURL: mediaURL,
						TweetID:  tweet.ID,
						Error:    err.Error(),
					}); markErr != nil {
						failed = true
						break
					}
					_, _ = m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: err.Error()})
					continue
				}
				failed = true
				break
			}
		}
		if !failed {
			_ = m.store.DeleteFailedTweet(saveCtx, item.ID)
			retried++
		}
	}
	return retried
}

func parserOptionsFromConfig(cfg config.AppConfig) parser.ParseOptions {
	return parser.ParseOptions{IncludeNestedTweets: cfg.IncludeNestedTweetMedia}
}

func shouldRetryMediaError(err error) bool {
	var statusErr *downloader.HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode != http.StatusForbidden && statusErr.StatusCode != http.StatusNotFound && statusErr.StatusCode != http.StatusGone
	}
	return true
}

func isPermanentlyUnavailableMediaError(err error) bool {
	var statusErr *downloader.HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	if statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusGone {
		return true
	}
	return statusErr.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(statusErr.Payload), "dmca")
}

func (m *Manager) ensureUserEntity(ctx context.Context, cfg config.AppConfig, user xclient.User) (storage.UserEntity, string, error) {
	target, err := filestore.New(cfg)
	if err != nil {
		return storage.UserEntity{}, "", err
	}
	parent := target.Join(target.Root(), "users")
	name := safeName(user.Title())
	existing, err := m.store.LocateUserEntity(ctx, user.ID, parent)
	if err != nil {
		return storage.UserEntity{}, "", err
	}
	if existing != nil && existing.Name != "" && existing.Name != name {
		oldPath := target.Join(existing.ParentDir, existing.Name)
		newPath := target.Join(existing.ParentDir, name)
		_ = target.Rename(ctx, oldPath, newPath)
	}
	entity, err := m.store.EnsureUserEntity(ctx, user.ID, parent, name)
	if err != nil {
		return storage.UserEntity{}, "", err
	}
	dir := target.Join(entity.ParentDir, entity.Name)
	if err := target.MkdirAll(ctx, dir); err != nil {
		return storage.UserEntity{}, "", err
	}
	if err := m.refreshUserLinks(ctx, cfg, user.ID, entity.Name, dir); err != nil {
		return storage.UserEntity{}, "", err
	}
	return entity, dir, nil
}

func (m *Manager) refreshUserLinks(ctx context.Context, cfg config.AppConfig, userID string, name string, targetDir string) error {
	links, err := m.store.GetUserLinkTargets(ctx, userID)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	target, err := filestore.New(cfg)
	if err != nil {
		return err
	}
	for _, link := range links {
		if strings.HasPrefix(link.ListID, "following:") {
			continue
		}
		if _, err := m.store.EnsureUserLink(ctx, userID, link.ListEntityID, name); err != nil {
			return err
		}
		listDir := target.Join(link.ListParentDir, link.ListName)
		if link.Name != "" && link.Name != name {
			_ = removeLinkPlaceholder(target.Join(listDir, link.Name))
		}
		if err := syncLink(target.Join(listDir, name), targetDir); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ensureListEntity(ctx context.Context, cfg config.AppConfig, group string, id string, title string) (storage.ListEntity, error) {
	target, err := filestore.New(cfg)
	if err != nil {
		return storage.ListEntity{}, err
	}
	parent := target.Join(target.Root(), group)
	name := safeName(title)
	existing, err := m.store.LocateListEntity(ctx, id, parent)
	if err != nil {
		return storage.ListEntity{}, err
	}
	if existing != nil && existing.Name != "" && existing.Name != name {
		oldPath := target.Join(existing.ParentDir, existing.Name)
		newPath := target.Join(existing.ParentDir, name)
		_ = target.Rename(ctx, oldPath, newPath)
	}
	entity, err := m.store.EnsureListEntity(ctx, id, parent, name)
	if err != nil {
		return storage.ListEntity{}, err
	}
	return entity, target.MkdirAll(ctx, target.Join(entity.ParentDir, entity.Name))
}

func (m *Manager) ensureUserLink(ctx context.Context, cfg config.AppConfig, listEntity storage.ListEntity, user xclient.User, targetDir string) error {
	name := safeName(user.Title())
	if _, err := m.store.EnsureUserLink(ctx, user.ID, listEntity.ID, name); err != nil {
		return err
	}
	target, err := filestore.New(cfg)
	if err != nil {
		return err
	}
	linkPath := target.Join(listEntity.ParentDir, listEntity.Name, name)
	return syncLink(linkPath, targetDir)
}

func (m *Manager) complete(ctx context.Context, job storage.Job, message string) {
	job.Status = storage.JobCompleted
	job.Progress = 1
	job.Message = message
	job.Error = ""
	m.save(ctx, job)
}

func (m *Manager) completeArchive(ctx context.Context, job storage.Job, stats archiveStats, retried int) {
	if stats.Failed > 0 {
		job.Status = storage.JobCompletedWithErrors
	} else {
		job.Status = storage.JobCompleted
	}
	job.Progress = 1
	job.Message = completionMessage(stats, retried)
	job.Error = archiveIssueSummary(stats.Issues)
	m.save(ctx, job)
}

func (m *Manager) fail(ctx context.Context, job storage.Job, mediaURL string, err error) {
	if mediaURL != "" {
		_, _ = m.store.CreateFailedMedia(ctx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: err.Error()})
	}
	job.Status = storage.JobFailed
	job.Progress = 1
	job.Message = "任务失败"
	job.Error = err.Error()
	m.save(ctx, job)
}

func (m *Manager) save(ctx context.Context, job storage.Job) {
	if err := m.store.UpdateJob(ctx, job); err != nil {
		log.Printf("save job %d: %v", job.ID, err)
		return
	}
	// 直接用内存中的 job 作为事件载荷，省去一次 GetJob 往返；
	// 前端收到事件后会通过 dashboard 重新拉取最新值，UpdatedAt 在此近似设置即可。
	job.UpdatedAt = time.Now().UTC()
	m.publishJob(ctx, "job.updated", job)
}

func (m *Manager) cancel(ctx context.Context, job storage.Job) {
	job.Status = storage.JobCanceled
	job.Progress = 1
	job.Message = "已取消"
	job.Error = ""
	m.save(ctx, job)
}

// handleInterrupt 收尾被取消或因关停中断的任务。仅当 DB 状态为 canceled（用户主动取消）
// 时持久化 canceled；进程关停（ctx 取消但 DB 未取消）时保留 Downloading/Resolving 中间
// 状态，由 RequeueInterruptedJobs 在下次启动恢复——避免优雅关停的任务被标记为已取消而
// 不再重排（反而比硬杀更不可恢复）。
func (m *Manager) handleInterrupt(ctx context.Context, saveCtx context.Context, job storage.Job) {
	current, err := m.store.GetJob(saveCtx, job.ID)
	if err == nil && current.Status == storage.JobCanceled {
		m.cancel(saveCtx, job)
	}
}

func (m *Manager) jobCanceled(ctx context.Context, saveCtx context.Context, id int64) bool {
	if ctx.Err() != nil {
		return true
	}
	job, err := m.store.GetJob(saveCtx, id)
	return err == nil && job.Status == storage.JobCanceled
}

func isCancellation(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

var (
	tweetShortLinkPattern = regexp.MustCompile(`(?i)\bhttps?://t\.co/[A-Za-z0-9_-]+`)
	filenameSpacePattern  = regexp.MustCompile(`\s+`)
)

func tweetFilename(cfg config.AppConfig, tweet parser.TweetData, index int) string {
	text := cleanTweetFilenameText(tweet.Text)
	base := text
	switch cfg.FileNamingMode {
	case config.FileNamingUserTweet:
		base = strings.Join(nonEmptyStrings(tweet.Author.ScreenName, tweet.Author.ID, text), "-")
	default:
		if base == "" {
			base = tweet.ID
		}
	}
	if base == "" {
		base = tweet.ID
	}
	if len(tweet.Media) > 1 {
		base = fmt.Sprintf("%s-%02d", base, index+1)
	}
	return base
}

func cleanTweetFilenameText(text string) string {
	text = tweetShortLinkPattern.ReplaceAllString(text, " ")
	text = filenameSpacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func nonEmptyStrings(values ...string) []string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, value)
		}
	}
	return parts
}

func bestMediaURL(media parser.Media) string {
	raw := media.BestURL
	if raw == "" {
		raw = media.URL
	}
	if raw == "" {
		for _, variant := range media.Variants {
			if variant.URL != "" {
				raw = variant.URL
				break
			}
		}
	}
	return downloader.NormalizeMediaURL(raw)
}

// newestTweetID 返回 tweets 中数值最大的推文 ID，作为增量归档的早停游标。timeline 按
// 时间倒序，但首页可能含置顶推文（ID 较旧却排在最前），直接取 tweets[0] 会让下次增量
// 归档在首页对该置顶推文精确命中而立即早停，漏掉比它更新的推文。雪花 ID 等长，字符串
// 比较等价于数值比较。调用方保证 tweets 非空。
func newestTweetID(tweets []parser.TweetData) string {
	newest := tweets[0].ID
	for _, t := range tweets[1:] {
		if t.ID > newest {
			newest = t.ID
		}
	}
	return newest
}

func isPhotoURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.Contains(parsed.Host, "pbs.twimg.com") || strings.Contains(parsed.Host, "twimg.com") && !strings.Contains(parsed.Path, ".mp4")
}

func storageUser(user xclient.User) storage.User {
	return storage.User{
		ID:           user.ID,
		ScreenName:   user.ScreenName,
		Name:         user.Name,
		Protected:    user.Protected,
		FriendsCount: user.FriendsCount,
		MediaCount:   user.MediaCount,
	}
}

func fallbackUserName(user xclient.User) string {
	if user.ScreenName != "" {
		return user.ScreenName
	}
	if user.Name != "" {
		return user.Name
	}
	return user.ID
}

func completionMessage(stats archiveStats, retried int) string {
	parts := []string{
		fmt.Sprintf("用户 %d", stats.Users),
		fmt.Sprintf("推文 %d", stats.Tweets),
		fmt.Sprintf("下载 %d", stats.Downloaded),
	}
	if stats.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("跳过 %d", stats.Skipped))
	}
	if stats.Failed > 0 {
		parts = append(parts, fmt.Sprintf("失败 %d", stats.Failed))
	}
	if retried > 0 {
		parts = append(parts, fmt.Sprintf("重试成功 %d", retried))
	}
	return "归档完成：" + strings.Join(parts, "，")
}

func isUsernameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func archiveIssueSummary(issues []string) string {
	if len(issues) == 0 {
		return ""
	}
	if len(issues) == 1 {
		return issues[0]
	}

	type issueGroup struct {
		action  string
		reason  string
		targets []string
		count   int
	}

	var groups []issueGroup
	groupIndex := make(map[string]int)

	for _, issue := range issues {
		issue = strings.TrimSpace(issue)
		if issue == "" {
			continue
		}

		var action string
		var target string
		reason := issue

		idx := strings.Index(issue, ": ")
		if idx != -1 {
			prefix := strings.TrimSpace(issue[:idx])
			reason = strings.TrimSpace(issue[idx+2:])

			if atIdx := strings.Index(prefix, "@"); atIdx != -1 {
				end := atIdx + 1
				for end < len(prefix) && isUsernameChar(prefix[end]) {
					end++
				}
				target = prefix[atIdx:end]
				before := strings.TrimSpace(prefix[:atIdx])
				after := strings.TrimSpace(prefix[end:])
				after = strings.TrimPrefix(after, "的")
				after = strings.TrimSpace(after)
				action = strings.TrimSpace(before + after)
			} else {
				action = prefix
			}
		}

		key := action + "::: " + reason
		if pos, ok := groupIndex[key]; ok {
			groups[pos].count++
			if target != "" {
				groups[pos].targets = append(groups[pos].targets, target)
			}
		} else {
			groupIndex[key] = len(groups)
			var targets []string
			if target != "" {
				targets = append(targets, target)
			}
			groups = append(groups, issueGroup{
				action:  action,
				reason:  reason,
				targets: targets,
				count:   1,
			})
		}
	}

	if len(groups) == 0 {
		return ""
	}

	const maxVisibleGroups = 5
	limit := min(len(groups), maxVisibleGroups)
	var lines []string

	for i := 0; i < limit; i++ {
		g := groups[i]
		if g.count == 1 {
			if len(g.targets) == 1 && g.action != "" {
				lines = append(lines, fmt.Sprintf("%s (%s): %s", g.action, g.targets[0], g.reason))
			} else if g.action != "" {
				lines = append(lines, fmt.Sprintf("%s: %s", g.action, g.reason))
			} else {
				lines = append(lines, g.reason)
			}
		} else {
			if len(g.targets) > 0 {
				shown := g.targets
				if len(shown) > 5 {
					shown = shown[:5]
				}
				targetList := strings.Join(shown, "、")
				if len(g.targets) > 5 {
					targetList += " 等"
				}
				if g.action != "" {
					lines = append(lines, fmt.Sprintf("%s: %s (共 %d 个账号: %s)", g.action, g.reason, g.count, targetList))
				} else {
					lines = append(lines, fmt.Sprintf("%s (共 %d 个账号: %s)", g.reason, g.count, targetList))
				}
			} else {
				if g.action != "" {
					lines = append(lines, fmt.Sprintf("%s: %s (共 %d 次)", g.action, g.reason, g.count))
				} else {
					lines = append(lines, fmt.Sprintf("%s (共 %d 次)", g.reason, g.count))
				}
			}
		}
	}

	if len(groups) > limit {
		lines = append(lines, fmt.Sprintf("另有 %d 种其他异常", len(groups)-limit))
	}

	return strings.Join(lines, "\n")
}

var unsupportedPathChars = regexp.MustCompile(`[/\\:*?"<>\|]`)

func safeName(name string) string {
	name = strings.TrimSpace(name)
	name = unsupportedPathChars.ReplaceAllString(name, "")
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "unknown"
	}
	const maxBytes = 180
	var builder strings.Builder
	for _, ch := range name {
		if builder.Len()+utf8.RuneLen(ch) > maxBytes {
			break
		}
		builder.WriteRune(ch)
	}
	// 截断之后才做点号与保留名收敛：截断本身可能产出 ".." 或以点结尾的新名字。
	return sanitizePathSegment(strings.TrimSpace(builder.String()))
}

// sanitizePathSegment 收敛单个路径段中会改变路径语义的形态。调用方会把返回值
// 直接 Join 到下载根下，因此这里必须保证结果不会被解释成路径跳转。
func sanitizePathSegment(name string) string {
	// 纯点号段（"." / ".." / "..."）会被 filepath.Join 当作路径跳转：显示名为 ".."
	// 的账号会把媒体写进下载根而不是自己的目录，破坏按用户隔离。
	if strings.Trim(name, ".") == "" {
		return "unknown"
	}
	// Windows 会静默丢弃结尾的点与空格，导致落盘名与库内记录不一致。
	name = strings.TrimRight(name, ". ")
	if strings.Trim(name, ".") == "" {
		return "unknown"
	}
	// Windows 保留设备名：以这些名字建目录会失败。
	base := name
	if dot := strings.IndexByte(base, '.'); dot > 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "_" + name
	}
	if name == "" {
		return "unknown"
	}
	return name
}

func syncLink(linkPath string, target string) error {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	if current, err := os.Readlink(linkPath); err == nil && current == targetAbs {
		if sidecarErr := os.Remove(linkPath + ".link"); sidecarErr != nil && !os.IsNotExist(sidecarErr) {
			return sidecarErr
		}
		return nil
	}
	if err := removeLinkPlaceholder(linkPath); err != nil {
		return err
	}
	if err := os.Symlink(targetAbs, linkPath); err == nil || os.IsExist(err) {
		if sidecarErr := os.Remove(linkPath + ".link"); sidecarErr != nil && !os.IsNotExist(sidecarErr) {
			return sidecarErr
		}
		return nil
	}
	return os.WriteFile(linkPath+".link", []byte(targetAbs+"\n"), 0o644)
}

func removeLinkPlaceholder(linkPath string) error {
	info, err := os.Lstat(linkPath)
	if os.IsNotExist(err) {
		err := os.Remove(linkPath + ".link")
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(linkPath)
	}
	return fmt.Errorf("链接路径 %s 已存在且不是符号链接或文件", linkPath)
}
