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
	"unicode"
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
	wg         sync.WaitGroup
	retryMu    sync.Mutex
	userMu     sync.Mutex
	userLocks  map[string]*keyLock
	mediaMu    sync.Mutex
	mediaLocks map[string]*keyLock
	xPoolMu    sync.Mutex
	xPoolKey   string
	cachedPool *xclient.Pool
	// autoRetryPausedUntil：用户取消自动重试任务后的暂停窗口，避免 rollOnce 每 2s
	// 把它重建出来（尊重"取消"语义，直到下个调度周期/窗口结束）。
	autoRetryPausedUntil time.Time
	// lastAutoRetryCheck 限流自动重试任务的写事务频率（默认每 2s tick 一次
	// INSERT/UPDATE 会持续抢 SQLite 写锁，即使没有到期条目）。
	lastAutoRetryCheck time.Time
	// autoRetryFailedUntil 是自动重试任务本轮失败后的退避截止时间：任务行会被
	// ReserveAutomaticRetryJob 复用重置，没有这个窗口就会每 30s 重试一次失败任务。
	autoRetryFailedUntil time.Time
}

const (
	// failedRecordRetention 失败推文/失败媒体记录的保留期；超过后由后台维护清理。
	failedRecordRetention = 180 * 24 * time.Hour
	// maintenanceInterval 后台维护执行间隔。
	maintenanceInterval = 12 * time.Hour
	// progressWriteInterval 归档进度写入的最小时隔，避免每个媒体一次 DB 写 + SSE
	// 发布造成写放大（P1）。终态与每用户完成仍即时写入。
	progressWriteInterval = 400 * time.Millisecond
	// staleTempFileAge 超过该年龄的下载临时文件视为崩溃残留，由后台维护清理。
	staleTempFileAge = time.Hour
	// failedScheduleRetryDelay 是到期计划领取失败后推迟重试的时长。
	failedScheduleRetryDelay = 10 * time.Minute
	// managerStopTimeout 关停时等待调度循环与活跃任务退出的最长时间。
	managerStopTimeout = 5 * time.Second
	// automaticRetryJobInput 是自动失败重试任务的固定 input 标记，单一来源在 storage。
	automaticRetryJobInput = storage.AutomaticFailedRetryInput
	// autoRetryJobRetention 是自动重试任务历史行的保留期（复用单行后仍需清理历史脏数据）。
	autoRetryJobRetention = 7 * 24 * time.Hour
	// autoRetryBackoffWindow 是自动重试任务失败后的退避窗口。
	autoRetryBackoffWindow = 10 * time.Minute
	// autoRetryCheckInterval 是自动重试任务的检查间隔：它每次检查都要开一个写事务
	// （INSERT…SELECT），2s tick 全量执行会与任务进度写争抢 SQLite 单写者。
	autoRetryCheckInterval = 30 * time.Second
	// autoRetryCancelPause 是"用户取消自动重试任务"后的暂停时长（一个调度周期量级）。
	autoRetryCancelPause = 10 * time.Minute
	// retryBatchLimit 是单个失败重试任务处理的条目上限。批量小会让每个批次都新建一个
	// 任务行（自动重试任务行随失败条目膨胀），恢复为 200 同时修正手动"全部重试"
	// 每次只处理 20 条的行为。
	retryBatchLimit = 200
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
		m.mu.Unlock()
		m.wg.Add(2)
		go func() {
			defer m.wg.Done()
			m.loop(runCtx)
		}()
		go func() {
			defer m.wg.Done()
			ticker := time.NewTicker(maintenanceInterval)
			defer ticker.Stop()
			for {
				m.maybeMaintain(runCtx)
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

// Stop 取消调度循环及所有活跃任务，并在有界时间内等待它们退出。
// 返回 false 时仍有任务在退出，调用方不可关闭其数据库。
func (m *Manager) Stop() bool {
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
		return true
	case <-time.After(managerStopTimeout):
		m.mu.Lock()
		ids := make([]int64, 0, len(m.active))
		for id := range m.active {
			ids = append(ids, id)
		}
		m.mu.Unlock()
		log.Printf("manager.Stop: %d task(s) did not drain within %s: %v", len(ids), managerStopTimeout, ids)
		return false
	}
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
	// 终态集合的单一来源在 storage（新增状态时只需改 storage.JobStatus 常量与集合）。
	return storage.IsTerminalJobStatus(status)
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
	if job.Kind == storage.JobKindFailedRetry && job.Input == automaticRetryJobInput {
		// 取消后调度器立刻重建会让"取消"形同虚设，并不断新增任务行。
		m.pauseAutomaticRetry()
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
	defer func() {
		if value := recover(); value != nil {
			log.Printf("scheduler panic: %v\n%s", value, debug.Stack())
		}
	}()
	m.enqueueDueArchiveSchedules(ctx)

	cfg, err := m.store.GetConfig(ctx)
	if err != nil {
		log.Printf("scheduler config: %v", err)
		return
	}
	if cfg.AutoRetryFailed && m.shouldCheckAutomaticRetry() {
		// Reserve 复用同一条自动重试任务行（终态行重置为 pending），避免每批重试都
		// 新建 jobs 行导致任务中心/dashboard 被历史行刷屏。
		if job, err := m.store.ReserveAutomaticRetryJob(ctx); err != nil {
			log.Printf("schedule failed retry: %v", err)
		} else if job != nil {
			m.publishJob(ctx, "job.created", *job)
		}
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
		log.Printf("list due archive schedules: %v", err)
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
		jobs, err := m.store.CreateScheduledArchiveJobs(ctx, schedule, now)
		if err != nil {
			if errors.Is(err, storage.ErrArchiveScheduleAlreadyClaimed) || ctx.Err() != nil {
				continue
			}
			// 领取失败（计划数据已不合法、DB 错误等）时必须推后：否则每个 tick 都会重试，
			// 且它的 next_run_at 最早，会一直占住 ListDueArchiveSchedules 的名额。
			log.Printf("archive schedule %d: create jobs: %v", schedule.ID, err)
			next := now.Add(failedScheduleRetryDelay)
			if _, err := m.store.RescheduleArchiveSchedule(ctx, schedule.ID, next); err == nil {
				m.publish(Event{Type: "archive_schedule.updated", Payload: map[string]any{"id": schedule.ID, "nextRunAt": next}})
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

// shouldCheckAutomaticRetry 决定本轮调度是否尝试创建自动重试任务：既做频率限流
// （避免每 2s 一次空写事务），也尊重用户取消（暂停窗口内不再重建）。
func (m *Manager) shouldCheckAutomaticRetry() bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if now.Before(m.autoRetryPausedUntil) || now.Before(m.autoRetryFailedUntil) {
		return false
	}
	if now.Sub(m.lastAutoRetryCheck) < autoRetryCheckInterval {
		return false
	}
	m.lastAutoRetryCheck = now
	return true
}

// pauseAutomaticRetry 在用户取消自动重试任务后开启暂停窗口；调用方已确认任务类型。
func (m *Manager) pauseAutomaticRetry() {
	m.mu.Lock()
	m.autoRetryPausedUntil = time.Now().Add(autoRetryCancelPause)
	m.mu.Unlock()
}

// availableSlots 返回当前可领取的任务数。串行（并发=1）是**产品决策**：同一份媒体被
// 并发归档会各自落盘产生 "(1)" 副本，且 SQLite 也只有一个写者。配置项
// MaxConcurrency 仅为 API/DB 兼容而保留，不参与调度计算——修改它不会提升并发。
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
	if pruned, err := m.store.PruneFailedRecords(ctx, now.Add(-failedRecordRetention)); err != nil {
		log.Printf("prune stale failed records: %v", err)
	} else if pruned > 0 {
		log.Printf("pruned %d stale failed records", pruned)
	}
	// 自动重试任务行已被复用为单行，但历史版本/异常路径可能留下旧行，定期清掉。
	if pruned, err := m.store.PruneAutomaticRetryJobs(ctx, now.Add(-autoRetryJobRetention)); err != nil {
		log.Printf("prune automatic retry jobs: %v", err)
	} else if pruned > 0 {
		log.Printf("pruned %d stale automatic retry job rows", pruned)
	}
	// 进程在下载/海报写入中途被杀时 .part 临时文件会永久残留（大视频可达数百 MB）。
	if cfg, err := m.store.GetConfig(ctx); err == nil && cfg.DownloadDir != "" {
		if removed, err := downloader.RemoveStaleTempFiles(ctx, cfg.DownloadDir, staleTempFileAge); err != nil && ctx.Err() == nil {
			log.Printf("remove stale temp files: %v", err)
		} else if removed > 0 {
			log.Printf("removed %d stale temp files", removed)
		}
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
	m.Notify()
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
	if downloadableMediaCount(tweet) == 0 {
		// 包括"只有缩略图、没有 mp4 变体的视频"：不能报下载完成，否则用户以为已存档。
		job.Status = storage.JobFailed
		job.Progress = 1
		job.Message = "链接已识别，但还没有解析到媒体 URL"
		job.Error = "这条推文没有可下载媒体"
		m.save(saveCtx, job)
		return
	}
	unavailable, unresolved := 0, 0
	lastProgressSave := time.Time{}
	for index, media := range tweet.Media {
		if ctx.Err() != nil {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		mediaURL := bestMediaURL(media)
		if mediaURL == "" {
			unresolved++
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
	if unresolved > 0 {
		job.Status = storage.JobCompletedWithErrors
		job.Error = fmt.Sprintf("%d 个媒体未解析到下载地址", unresolved)
		job.Message = "下载部分完成：" + job.Error
	}
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
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	m.completeArchive(saveCtx, job, stats, 0)
}

// failRetryJob 处理失败重试任务的失败：自动重试任务额外开启退避窗口（任务行会被
// Reserve 复用到下一次，没有窗口会每 30s 重试一次必然失败的任务）。
func (m *Manager) failRetryJob(ctx context.Context, job storage.Job, err error) {
	if job.Kind == storage.JobKindFailedRetry && job.Input == automaticRetryJobInput {
		m.mu.Lock()
		m.autoRetryFailedUntil = time.Now().Add(autoRetryBackoffWindow)
		m.mu.Unlock()
	}
	m.fail(ctx, job, "", err)
}

func (m *Manager) processFailedRetry(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		m.failRetryJob(saveCtx, job, err)
		return
	}
	job.Status = storage.JobDownloading
	job.Progress = 0.2
	job.Message = "正在重试失败推文"
	job.Error = ""
	m.save(saveCtx, job)
	retried, err := m.retryFailedTweets(ctx, saveCtx, job, cfg, job.Input != automaticRetryJobInput)
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	if err != nil {
		m.failRetryJob(saveCtx, job, err)
		return
	}
	remaining, err := m.store.CountFailedTweets(saveCtx)
	if err != nil {
		m.failRetryJob(saveCtx, job, err)
		return
	}
	// 自动重试是后台维护行为：remaining 里包含仍在退避期/其它任务的条目，把每一批都
	// 记成 completed_with_errors 会让 dashboard 的 failed 计数单调增长。因此自动重试
	// 任务始终 completed；手动触发的重试任务仍保留 completed_with_errors 语义。
	if remaining > 0 && job.Input != automaticRetryJobInput {
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
	list, err := pool.GetListByID(ctx, strings.TrimSpace(job.Input))
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
	members, err := pool.GetListMembers(ctx, list)
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
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	m.completeArchive(saveCtx, job, stats, 0)
}

func (m *Manager) processFollowing(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, pool, err := m.xPool(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
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
	members, err := pool.GetFollowing(ctx, owner)
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
	if ctx.Err() != nil {
		m.handleInterrupt(ctx, saveCtx, job)
		return
	}
	m.completeArchive(saveCtx, job, stats, 0)
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
	// 或在已有媒体记录判定完成前同时落盘出 photo.jpg / photo(1).jpg 两份副本。
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
	// 磁盘上已经有同一份媒体时直接跳过，不再下载（既不创建硬链接也不复制副本）。
	skipped, err := m.skipArchivedMedia(ctx, saveCtx, job, cfg, target, existing, mediaKey, mediaURL, tweetID, previewURL)
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
	if existing != nil {
		// 已有下载记录时把 content_hash 传给下载器：跳过已存在文件时无需为算哈希重读
		// 整个大文件（历史行为是 Skipped 路径写入空 hash）。
		options.ExistingContentHash = strings.TrimSpace(existing.ContentHash)
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
			// unavailable_media 以 media_url 为键、对全部推文生效且没有到期时间：一旦
			// 误判（签名 URL 瞬时 404/403 被归类为永久）该媒体将永久不再下载。当前只在
			// 明确 404/410/DMCA 时写入；"N 天后重试一次"需要 storage 侧增加列/清理策略。
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
			JobID:       job.ID,
			TweetID:     tweetID,
			MediaURL:    mediaURL,
			ContentHash: result.ContentHash,
			PreviewURL:  previewURL,
			FilePath:    result.Path,
			Bytes:       result.Bytes,
		}); err != nil {
			return mediaDownloadResult{}, err
		}
		return mediaDownloadResult{skipped: true}, nil
	}
	if result.ContentHash != "" {
		// 不同 URL 的媒体锁互不相干，内容判重与记录写入必须按哈希串行化，
		// 避免两个任务同时查不到已有内容后各自保留一份文件。下载已完成，
		// 使用 saveCtx 保证任务取消后仍能完成去重和入库。
		releaseContent, err := m.lockMedia(saveCtx, "sha256:"+result.ContentHash)
		if err != nil {
			return mediaDownloadResult{}, err
		}
		defer releaseContent()
	}
	if skipped, err := m.skipSameContentMedia(ctx, saveCtx, job, cfg, target, result, mediaURL, tweetID, previewURL); err != nil {
		return mediaDownloadResult{}, err
	} else if skipped {
		return mediaDownloadResult{skipped: true}, nil
	}
	_, err = m.store.CreateDownload(saveCtx, storage.DownloadRecord{
		JobID:       job.ID,
		TweetID:     tweetID,
		MediaURL:    mediaURL,
		ContentHash: result.ContentHash,
		PreviewURL:  previewURL,
		FilePath:    result.Path,
		Bytes:       result.Bytes,
	})
	return mediaDownloadResult{}, err
}

// skipArchivedMedia 判断本次要归档的媒体是否已经存在，命中时直接跳过：
// 不下载、不创建硬链接、也不复制副本。
//
//  1. 本推文+本媒体已有记录且文件仍在 → 已归档，跳过（沿用既有行为）；
//  2. 同一份媒体的其他副本（转推、引用推文与卡片媒体会复用同一条媒体 URL）中存在
//     仍在磁盘上的文件时 → 视为已归档，跳过。
//
// 第 2 步按媒体身份键复用既有下载记录的 file_path：同一媒体内容只保留一份本地文件，
// 新记录指向已有文件路径。返回 true 表示本次已跳过。
func (m *Manager) skipArchivedMedia(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, target filestore.Store, existing *storage.DownloadRecord, mediaKey string, mediaURL string, tweetID string, previewURL string) (bool, error) {
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
	for offset := 0; ; offset += storage.MaxMediaCopyLookup {
		copies, err := m.store.FindDownloadsByMediaKeyPage(saveCtx, mediaKey, storage.MaxMediaCopyLookup, offset)
		if err != nil {
			return false, err
		}
		for _, copy := range copies {
			path := strings.TrimSpace(copy.FilePath)
			archived, size, err := archivedFileState(path)
			if err != nil {
				return false, err
			}
			if !archived {
				continue
			}
			record, err := m.store.CreateDownload(saveCtx, storage.DownloadRecord{
				JobID:       job.ID,
				TweetID:     tweetID,
				MediaURL:    mediaURL,
				ContentHash: copy.ContentHash,
				PreviewURL:  previewURL,
				FilePath:    filepath.Clean(path),
				Bytes:       size,
			})
			if err != nil {
				return false, err
			}
			m.ensureVideoPoster(ctx, cfg, target, &record, previewURL)
			return true, nil
		}
		if len(copies) < storage.MaxMediaCopyLookup {
			return false, nil
		}
	}
}

func (m *Manager) skipSameContentMedia(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, target filestore.Store, result downloader.Result, mediaURL string, tweetID string, previewURL string) (bool, error) {
	contentHash := strings.TrimSpace(result.ContentHash)
	if contentHash == "" || strings.TrimSpace(result.Path) == "" {
		return false, nil
	}
	resultPath := filepath.Clean(result.Path)
	for offset := 0; ; offset += storage.MaxMediaCopyLookup {
		copies, err := m.store.FindDownloadsByContentHashPage(saveCtx, contentHash, storage.MaxMediaCopyLookup, offset)
		if err != nil {
			return false, err
		}
		for _, copy := range copies {
			path := strings.TrimSpace(copy.FilePath)
			if path == "" || filepath.Clean(path) == resultPath {
				continue
			}
			archived, size, err := archivedFileState(path)
			if err != nil {
				return false, err
			}
			if !archived {
				continue
			}
			if err := os.Remove(resultPath); err != nil && !os.IsNotExist(err) {
				return false, err
			}
			record, err := m.store.CreateDownload(saveCtx, storage.DownloadRecord{
				JobID:       job.ID,
				TweetID:     tweetID,
				MediaURL:    mediaURL,
				ContentHash: contentHash,
				PreviewURL:  previewURL,
				FilePath:    filepath.Clean(path),
				Bytes:       size,
			})
			if err != nil {
				return false, err
			}
			m.ensureVideoPoster(ctx, cfg, target, &record, previewURL)
			return true, nil
		}
		if len(copies) < storage.MaxMediaCopyLookup {
			return false, nil
		}
	}
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
	// SkippedUsers / SkippedMedia 分开统计：前者是"没有被归档的用户"（重复、blocked/muted、
	// 等待中的 list 成员），后者是"没有重新下载的媒体"（本地已有）。对外文案仍只展示合计，
	// 保持 Job.Message 的 "跳过 N" 形状不变，避免破坏前端展示契约。
	SkippedUsers int
	SkippedMedia int
	Failed       int
	Issues       []string
}

// skipped 是跳过总数的对外口径（Job.Message 用）。
func (s archiveStats) skipped() int { return s.SkippedUsers + s.SkippedMedia }

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
	pool, err := m.XClientPool(cfg)
	return cfg, pool, err
}

// XClientPool shares credential cooldowns and successful checks with running jobs.
func (m *Manager) XClientPool(cfg config.AppConfig) (*xclient.Pool, error) {
	key := xPoolConfigKey(cfg)
	m.xPoolMu.Lock()
	defer m.xPoolMu.Unlock()
	if m.cachedPool != nil && m.xPoolKey == key {
		return m.cachedPool, nil
	}
	pool, err := xclient.NewPool(cfg)
	if err != nil {
		return nil, err
	}
	m.cachedPool = pool
	m.xPoolKey = key
	return pool, nil
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
	tasks, skippedUsers, skippedIssues, err := m.archiveUserTasks(ctx, cfg, users)
	if err != nil {
		return stats, err
	}
	stats.SkippedUsers += skippedUsers
	stats.Issues = append(stats.Issues, skippedIssues...)
	if len(tasks) == 0 {
		return stats, nil
	}

	progressJob := job

	addStats := func(delta archiveStats) {
		stats.Users += delta.Users
		stats.Tweets += delta.Tweets
		stats.Downloaded += delta.Downloaded
		stats.SkippedUsers += delta.SkippedUsers
		stats.SkippedMedia += delta.SkippedMedia
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
			var pathError *os.PathError
			var linkError *os.LinkError
			if isCancellation(ctx, err) || shouldAbortArchiveUsers(err) ||
				(!errors.As(err, &pathError) && !errors.As(err, &linkError)) {
				return stats, err
			}
			// 单用户文件系统错误记录后继续；存储等基础设施错误必须上抛。
			stats.Failed++
			stats.Issues = append(stats.Issues, fmt.Sprintf("归档 @%s 失败: %v", fallbackUserName(task.user), err))
			log.Printf("archiveUser @%s: %v", fallbackUserName(task.user), err)
			finishTask()
			continue
		}
		finishTask()
	}
	return stats, nil
}

// archiveUserTasks 过滤/去重待归档用户，返回任务列表、被跳过的用户数以及跳过的原因
// 条目（blocked/muted 以前被静默丢弃，现在计入 SkippedUsers + Issues，便于排障）。
func (m *Manager) archiveUserTasks(ctx context.Context, cfg config.AppConfig, users []xclient.User) ([]archiveUserTask, int, []string, error) {
	target, err := filestore.New(cfg)
	if err != nil {
		return nil, 0, nil, err
	}
	parent := target.Join(target.Root(), "users")
	seen := map[string]struct{}{}
	tasks := make([]archiveUserTask, 0, len(users))
	userIDs := make([]string, 0, len(users))
	skipped := 0
	issues := []string{}
	for index, user := range users {
		if err := ctx.Err(); err != nil {
			return nil, skipped, issues, err
		}
		if user.ID == "" {
			continue
		}
		if user.Blocking || user.Muting {
			skipped++
			issues = append(issues, fmt.Sprintf("跳过 @%s 的归档: 用户屏蔽或静音了本账号", fallbackUserName(user)))
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
		return nil, skipped, issues, err
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
	return tasks, skipped, issues, nil
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
			var databaseError interface{ Code() int }
			if errors.As(err, &databaseError) {
				return stats, err
			}
			stats.Failed++
			stats.Issues = append(stats.Issues, fmt.Sprintf("创建 @%s 的归档链接失败: %v", fallbackUserName(user), err))
		}
	}
	if waited {
		stats.SkippedUsers++
		return stats, nil
	}
	if user.Protected && !user.Following {
		if cfg.AutoFollowProtected && !user.Requested && pool != nil && pool.Primary() != nil {
			_ = pool.FollowUser(ctx, user)
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
	tweets, fetchStats, err := pool.GetUserMediaWithOptionsStats(ctx, user, opts)
	if err != nil {
		if isCancellation(ctx, err) {
			return stats, err
		}
		if shouldAbortArchiveUsers(err) {
			return stats, err
		}
		stats.Failed++
		stats.Issues = append(stats.Issues, fmt.Sprintf("读取 @%s 的媒体时间线失败: %v", fallbackUserName(user), err))
		return stats, nil
	}
	if fetchStats.SkippedTweets > 0 {
		// 单条推文解析失败被 xclient 跳过（毒丸）：只记 Issue，任务继续；游标靠
		// fetchStats.LastSeenTweetID 越过它们，避免下一轮重复抓同一页。
		stats.Issues = append(stats.Issues, fmt.Sprintf("跳过 @%s 的 %d 条无法解析的推文", fallbackUserName(user), fetchStats.SkippedTweets))
	}
	if len(tweets) == 0 {
		// 没有可用推文也必须推进游标，否则毒丸是哪一轮的最新推文就会永远重扫。
		if fetchStats.LastSeenTweetID != "" {
			if err := m.store.UpdateUserEntityLastSeenTweet(saveCtx, entity.ID, fetchStats.LastSeenTweetID); err != nil {
				return stats, err
			}
		}
		return stats, m.syncUserMediaCount(saveCtx, entity.ID, user.MediaCount)
	}
	downloaded, err := m.archiveTweetsWithSeen(ctx, saveCtx, job, cfg, user, entity, dir, tweets, updateDownloading, fetchStats.LastSeenTweetID)
	stats.Tweets += downloaded.Tweets
	stats.Downloaded += downloaded.Downloaded
	stats.SkippedUsers += downloaded.SkippedUsers
	stats.SkippedMedia += downloaded.SkippedMedia
	stats.Failed += downloaded.Failed
	if err != nil {
		return stats, err
	}
	return stats, m.syncUserMediaCount(saveCtx, entity.ID, user.MediaCount)
}

// archiveTweets 下载一批时间线推文的媒体，并推进该用户的增量游标。
//
// 游标只要求"本轮的失败都已交给失败队列"：可重试的失败由 retryFailedTweets 补齐，
// 永久失效的媒体再怎么重扫也拿不回来。若要求零失败，一条长期失败的推文会让游标永远
// 停在旧位置，开了增量归档也等于每次全量扫描。只有失败未能入队时才不推进游标，
// 让下次归档重新扫到它。
func (m *Manager) archiveTweets(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, user xclient.User, entity storage.UserEntity, dir string, tweets []parser.TweetData, updateDownloading func(message string)) (archiveStats, error) {
	return m.archiveTweetsWithSeen(ctx, saveCtx, job, cfg, user, entity, dir, tweets, updateDownloading, "")
}

// archiveTweetsWithSeen 与 archiveTweets 相同，但额外接收"本轮 timeline 见到的最新推文 ID"
// （含被 xclient 跳过的解析失败推文）。游标取它与成功解析推文 ID 的较大者，保证毒丸不会
// 让增量归档退化为全量重扫。
func (m *Manager) archiveTweetsWithSeen(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, user xclient.User, entity storage.UserEntity, dir string, tweets []parser.TweetData, updateDownloading func(message string), seenLastID string) (archiveStats, error) {
	stats := archiveStats{Tweets: len(tweets)}
	for _, tweet := range tweets {
		for mediaIndex, media := range tweet.Media {
			if ctx.Err() != nil {
				return stats, context.Canceled
			}
			mediaURL := bestMediaURL(media)
			if mediaURL == "" {
				// 结构上不可解析（例如只有缩略图、没有 mp4 变体的视频）：重试同一 payload
				// 永远不会成功，因此只记失败与 Issue，不写失败队列（否则条目会按 24h
				// 退避被反复重试而永不清理），也不阻塞增量游标（否则该用户会永久全量
				// 重扫）。是否重新解析取决于 xclient 未来能否给出可下载地址。
				stats.Failed++
				stats.Issues = append(stats.Issues, fmt.Sprintf("推文 %s 的媒体 %d 未解析到下载地址", tweet.ID, mediaIndex+1))
				continue
			}
			if updateDownloading != nil {
				updateDownloading(fmt.Sprintf("下载 @%s 的媒体", fallbackUserName(user)))
			}
			result, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, dir, tweetFilename(cfg, tweet, mediaIndex), media.Type == parser.MediaPhoto, tweet.CreatedAt, media.PreviewURL)
			if err != nil {
				var databaseError interface{ Code() int }
				if isCancellation(ctx, err) || errors.As(err, &databaseError) {
					return stats, err
				}
				stats.Failed++
				if _, recordErr := m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: err.Error()}); recordErr != nil {
					return stats, recordErr
				}
				if shouldRetryMediaError(err) {
					if rememberErr := m.rememberFailedTweet(saveCtx, job, entity, tweet, err); rememberErr != nil {
						return stats, rememberErr
					}
				}
				continue
			}
			if result.skipped {
				stats.SkippedMedia++
			} else {
				stats.Downloaded++
			}
		}
	}
	if len(tweets) > 0 {
		// 游标取本轮见到的最新推文 ID（数值最大）。timeline 按时间倒序，但首页可能含
		// 置顶推文（ID 较旧却排在最前），若用 tweets[0].ID 作游标，下次增量归档会在首页
		// 对其精确命中而立即早停，漏掉比置顶更新的推文。无论开关是否开启都写入，切换
		// 开关后游标始终可用。写入失败需上抛日志，否则增量归档会因游标缺失而全量重扫。
		//
		// 策略：本函数成功返回时（可重试失败已入队、不可解析媒体只记 Issue）无条件推进
		// 游标——否则一条长期失败的推文会让"增量归档"退化为每次全量扫描。真正需要阻塞
		// 推进的只有"失败未能入队/写入失败"，那些路径直接 return error（本轮不写游标）。
		cursor := newestTweetID(tweets)
		if seenLastID > cursor {
			// snowflake ID 是等长十进制字符串，字符串比较即数值比较（与 newestTweetID 一致）。
			cursor = seenLastID
		}
		// 游标只前进不回退：并发/重扫场景下（例如同一用户被两个任务先后处理，或上层传入
		// 的 entity 快照较旧）不能让一次旧结果把已推进的游标写回去，否则会重复归档。
		if current, err := m.store.GetUserEntity(saveCtx, entity.ID); err == nil && current.LastSeenTweetID >= cursor {
			cursor = current.LastSeenTweetID
		}
		if err := m.store.UpdateUserEntityLastSeenTweet(saveCtx, entity.ID, cursor); err != nil {
			return stats, err
		}
	}
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

func (m *Manager) retryFailedTweets(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, force bool) (int, error) {
	if !force && !cfg.AutoRetryFailed {
		return 0, nil
	}
	// 全局重试队列：用 TryLock 避免多个并发任务同时重试同一批 failed tweets，
	// 否则会因并发下载同一媒体而产生 (1) 副本。抢不到锁说明已有任务在重试，本次跳过。
	if !m.retryMu.TryLock() {
		return 0, nil
	}
	defer m.retryMu.Unlock()
	items, err := m.store.ListFailedTweetsForRetry(saveCtx, retryBatchLimit, force)
	if err != nil {
		return 0, err
	}
	retried := 0
	for _, item := range items {
		if ctx.Err() != nil {
			return retried, ctx.Err()
		}
		entity, err := m.store.GetUserEntity(saveCtx, item.EntityID)
		if err != nil {
			if touchErr := m.store.TouchFailedTweet(saveCtx, item.ID, err.Error()); touchErr != nil {
				return retried, touchErr
			}
			continue
		}
		var tweet parser.TweetData
		if err := json.Unmarshal([]byte(item.Payload), &tweet); err != nil {
			if touchErr := m.store.TouchFailedTweet(saveCtx, item.ID, err.Error()); touchErr != nil {
				return retried, touchErr
			}
			continue
		}
		target, err := filestore.New(cfg)
		if err != nil {
			if touchErr := m.store.TouchFailedTweet(saveCtx, item.ID, err.Error()); touchErr != nil {
				return retried, touchErr
			}
			continue
		}
		dir := target.Join(entity.ParentDir, entity.Name)
		failed := false
		downloadable := false
		var lastErr error
		for index, media := range tweet.Media {
			mediaURL := bestMediaURL(media)
			if mediaURL == "" {
				// 该媒体不可解析，跳过它继续处理同一推文的其它媒体；若整条推文都没有
				// 可下载媒体，重试永远不会成功，直接清理队列条目（见下方 downloadable）。
				continue
			}
			downloadable = true
			// 跳过已下载的媒体，避免重试时把之前已成功的部分再下一遍。
			if _, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, dir, tweetFilename(cfg, tweet, index), media.Type == parser.MediaPhoto, tweet.CreatedAt, media.PreviewURL); err != nil {
				var databaseError interface{ Code() int }
				if isCancellation(ctx, err) || errors.As(err, &databaseError) {
					return retried, err
				}
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
						return retried, markErr
					}
					if _, recordErr := m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: err.Error()}); recordErr != nil {
						return retried, recordErr
					}
					continue
				}
				failed, lastErr = true, err
				break
			}
		}
		if !downloadable {
			// 没有任何可下载地址：写一条失败媒体记录留痕后删除队列条目，避免它按
			// 24h 退避被永久重试（历史版本已可能留下这类条目）。
			if _, err := m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: "", Error: "媒体未解析到下载地址"}); err != nil {
				return retried, err
			}
			if err := m.store.DeleteFailedTweet(saveCtx, item.ID); err != nil {
				return retried, err
			}
			continue
		}
		if !failed {
			if err := m.store.DeleteFailedTweet(saveCtx, item.ID); err != nil {
				return retried, err
			}
			retried++
		} else if ctx.Err() == nil {
			// 重试失败的条目续期排到队尾，避免持续失败的头部条目永远挡住后面的条目。
			if err := m.store.TouchFailedTweet(saveCtx, item.ID, lastErr.Error()); err != nil {
				return retried, err
			}
		}
	}
	return retried, nil
}

func parserOptionsFromConfig(cfg config.AppConfig) parser.ParseOptions {
	return parser.ParseOptions{IncludeNestedTweets: cfg.IncludeNestedTweetMedia}
}

func shouldAbortArchiveUsers(err error) bool {
	// X 轮换 queryId 后，逐个用户重试只会把同一个 404 重复 N 次并刷满 Issues；
	// 这是"需要升级程序"的配置级错误，直接中止整个任务并由 fail() 给出明确文案。
	if errors.Is(err, xclient.ErrQueryIDStale) {
		return true
	}
	return xclient.IsAllClientsRateLimited(err)
}

// queryIDStaleHint 给 ErrQueryIDStale 加上面向运维的明确提示，避免用户看到裸 404。
func queryIDStaleHint(err error) error {
	if !errors.Is(err, xclient.ErrQueryIDStale) {
		return err
	}
	return fmt.Errorf("X 接口 queryId 可能已失效，请升级程序后重试: %w", err)
}

// shouldRetryMediaError 判断下载失败的推文是否进入失败队列等待重试。只有永久失效
// （404/410/DMCA）不重试：X 对过期的 video.twimg.com 签名例行返回裸 403，那是瞬时
// 状态。403 若不入队，这条推文既不会被重试，又会让增量游标永远停在旧位置。
func shouldRetryMediaError(err error) bool {
	// 无法确定媒体类型（未知 Content-Type 且 URL/hint 都没有允许扩展名）是永久失败：
	// downloader 已改为直接报错而不是产出 .bin，重试同一 URL 结果不变。注意它**不**写入
	// 全局 unavailable_media（那份黑名单用于 X 侧永久失效，误判代价更高）。
	if errors.Is(err, downloader.ErrUnknownMediaType) {
		return false
	}
	return !isPermanentlyUnavailableMediaError(err)
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
		if name, err = m.renameEntityDir(ctx, target, existing.ParentDir, existing.Name, name); err != nil {
			return storage.UserEntity{}, "", err
		}
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

// renameEntityDir 把实体目录从 oldName 改名为 newName，并同步改写 downloads.file_path，
// 返回之后应使用的目录名。只改目录不改记录，媒体库会整体 404，下次归档还会因找不到
// 旧文件把全部媒体重下成 "(1)" 副本。改名失败（如新名目录已存在且非空）时沿用旧名，
// 避免同一实体的文件分散到两个目录。
func (m *Manager) renameEntityDir(ctx context.Context, target filestore.Store, parentDir string, oldName string, newName string) (string, error) {
	oldPath := target.Join(parentDir, oldName)
	newPath := target.Join(parentDir, newName)
	if err := target.Rename(ctx, oldPath, newPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 旧目录已经不存在：典型是上次进程在 os.Rename 成功后、DB 重写前被杀（磁盘
			// 已是新名，DB 仍是旧名），或者用户手工改过目录名。此时必须继续做 DB 前缀
			// 重写把记录指向新目录，否则会留下"媒体库 404 + 每次重下副本"的分裂状态。
			// 若新目录其实也不存在，重写不会命中任何记录，同样安全。
			log.Printf("rename %s -> %s: source missing (%v); treating rename as already applied", oldPath, newPath, err)
			if _, rewriteErr := m.store.RewriteDownloadPathPrefix(ctx, oldPath, newPath); rewriteErr != nil {
				return "", fmt.Errorf("rewrite paths after missing source: %w", rewriteErr)
			}
			return newName, nil
		}
		log.Printf("rename %s -> %s: %v; keep old directory name", oldPath, newPath, err)
		return oldName, nil
	}
	if _, err := m.store.RewriteDownloadPathPrefix(ctx, oldPath, newPath); err != nil {
		if rollbackErr := target.Rename(context.Background(), newPath, oldPath); rollbackErr != nil {
			return "", fmt.Errorf("rewrite paths: %w; rollback directory: %v", err, rollbackErr)
		}
		return "", err
	}
	return newName, nil
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
		if name, err = m.renameEntityDir(ctx, target, existing.ParentDir, existing.Name, name); err != nil {
			return storage.ListEntity{}, err
		}
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

// syncUserMediaCount 把用户在 X 上报告的媒体数写入实体。MediaCount 为 0 时通常意味着
// 本次 timeline 结果不可信（受保护/限流/接口降级），写 0 会覆盖掉历史值使"缺多少媒体"
// 的估算失真，因此只在 >0 时更新。
func (m *Manager) syncUserMediaCount(ctx context.Context, entityID int64, mediaCount int) error {
	if mediaCount <= 0 {
		return nil
	}
	return m.store.UpdateUserEntityMediaCount(ctx, entityID, mediaCount)
}

func (m *Manager) completeArchive(ctx context.Context, job storage.Job, stats archiveStats, retried int) {
	remainingFailedTweets, err := m.store.CountFailedTweetsForJob(ctx, job.ID)
	if stats.Failed > 0 || err != nil || remainingFailedTweets > 0 {
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
	err = queryIDStaleHint(err)
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
	updated, err := m.store.UpdateJobAndGet(ctx, job)
	if err != nil {
		log.Printf("save job %d: %v", job.ID, err)
		return
	}
	if updated != nil {
		m.publishJob(ctx, "job.updated", *updated)
	}
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

// isCancellation 只以任务自身 ctx 为准判断是否被取消或关停。不能看 err：net/http 的
// Client.Timeout / ResponseHeaderTimeout 错误同样满足 errors.Is(err,
// context.DeadlineExceeded)，把网络超时当成取消会走 handleInterrupt，而任务并未被
// 取消，于是什么都不写，任务永久停在 downloading/resolving。
func isCancellation(ctx context.Context, _ error) bool {
	return ctx.Err() != nil
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

// downloadableMediaCount 统计推文中有可下载地址的媒体数量。
func downloadableMediaCount(tweet parser.TweetData) int {
	count := 0
	for _, media := range tweet.Media {
		if bestMediaURL(media) != "" {
			count++
		}
	}
	return count
}

func bestMediaURL(media parser.Media) string {
	raw := parser.MediaDownloadURL(media)
	if raw == "" {
		return ""
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
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	return (host == "twimg.com" || strings.HasSuffix(host, ".twimg.com")) && !strings.EqualFold(path.Ext(parsed.Path), ".mp4")
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
	if total := stats.skipped(); total > 0 {
		parts = append(parts, fmt.Sprintf("跳过 %d", total))
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

// archiveIssueSummary 把 Issue 列表聚合成人可读的中文多行文案，直接写入 Job.Error。
//
// 输出形状是前端契约（apps/web/src/lib/jobErrors.ts 的 parseAggregatedErrors 已对齐）：
//   - "action: reason (共 N 次)"
//   - "action: reason (共 N 个账号: @a、@b)"
//   - "另有 N 种其他异常"
//
// 刻意不在末尾追加结构化 JSON 段：前端当前会把无法识别的行当成一条单条错误，追加
// "---\n{json}" 会凭空多出一条错误条目。若将来需要结构化字段，必须先与前端约定剥离规则。
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
				targetList := strings.Join(g.targets, "、")
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
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
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
	// 与媒体目录的 0700/0600 加固一致：列表目录与 sidecar 会暴露私密列表名和成员。
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
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
	linkErr := os.Symlink(targetAbs, linkPath)
	if os.IsExist(linkErr) {
		current, err := os.Readlink(linkPath)
		if err != nil || current != targetAbs {
			return linkErr
		}
		linkErr = nil
	}
	if linkErr == nil {
		if sidecarErr := os.Remove(linkPath + ".link"); sidecarErr != nil && !os.IsNotExist(sidecarErr) {
			return sidecarErr
		}
		return nil
	}
	return os.WriteFile(linkPath+".link", []byte(targetAbs+"\n"), 0o600)
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
	return &os.PathError{Op: "symlink", Path: linkPath, Err: os.ErrExist}
}
