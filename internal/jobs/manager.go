package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

type Store interface {
	ClaimPendingJobs(ctx context.Context, limit int) ([]storage.Job, error)
	GetJob(ctx context.Context, id int64) (storage.Job, error)
	UpdateJob(ctx context.Context, job storage.Job) error
	CreateDownload(ctx context.Context, record storage.DownloadRecord) (storage.DownloadRecord, error)
	CreateFailedMedia(ctx context.Context, failed storage.FailedMedia) (storage.FailedMedia, error)
}

type Manager struct {
	store      *storage.Store
	parser     *parser.Service
	eventBus   *EventBus
	downloader *downloader.Downloader
	wake       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	active     map[int64]context.CancelFunc
	wg         sync.WaitGroup
	retryMu    sync.Mutex
	userMu     sync.Mutex
	userLocks  map[string]chan struct{}
	mediaMu    sync.Mutex
	mediaLocks map[string]chan struct{}
	xPoolMu    sync.Mutex
	xPoolKey   string
	cachedPool *xclient.Pool
}

func NewManager(store *storage.Store, parserService *parser.Service, eventBus *EventBus) *Manager {
	return &Manager{
		store:      store,
		parser:     parserService,
		eventBus:   eventBus,
		downloader: downloader.New(),
		wake:       make(chan struct{}, 1),
		active:     make(map[int64]context.CancelFunc),
		userLocks:  make(map[string]chan struct{}),
		mediaLocks: make(map[string]chan struct{}),
	}
}

func (m *Manager) Start(ctx context.Context) {
	m.once.Do(func() {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.loop(ctx)
		}()
	})
}

// Stop 等待调度循环与所有活跃任务退出（最多等待 15 秒），
// 供主进程在关闭数据库前调用，避免 store.Close 与运行中的任务竞争。
func (m *Manager) Stop() {
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	}
}

func (m *Manager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
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
	m.eventBus.Publish(Event{Type: "job.updated", JobID: job.ID, Payload: job})
	return job, nil
}

func (m *Manager) RetryFailedTweetsNow(ctx context.Context) (storage.Job, error) {
	job, err := m.store.CreateJob(ctx, storage.JobKindFailedRetry, "failed-tweets", "失败推文重试")
	if err != nil {
		return storage.Job{}, err
	}
	m.Notify()
	m.eventBus.Publish(Event{Type: "job.updated", JobID: job.ID, Payload: job})
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
	for _, job := range jobs {
		m.eventBus.Publish(Event{Type: "job.updated", JobID: job.ID, Payload: job})
	}
	m.eventBus.Publish(Event{Type: "archive_schedule.ran", Payload: map[string]any{"id": schedule.ID, "jobCount": len(jobs)}})
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
					m.eventBus.Publish(Event{Type: "archive_schedule.updated", Payload: map[string]any{"id": schedule.ID, "nextRunAt": next}})
				}
				continue
			}
		}
		jobs, err := m.store.CreateJobsForArchiveSchedule(ctx, schedule, now)
		if err != nil {
			continue
		}
		if len(jobs) == 0 {
			continue
		}
		createdAny = true
		for _, job := range jobs {
			m.eventBus.Publish(Event{Type: "job.updated", JobID: job.ID, Payload: job})
		}
		m.eventBus.Publish(Event{Type: "archive_schedule.ran", Payload: map[string]any{"id": schedule.ID, "jobCount": len(jobs)}})
	}
	if createdAny {
		m.Notify()
	}
}

func (m *Manager) availableSlots(limit int) int {
	if limit <= 0 {
		limit = 1
	}
	if limit > 64 {
		limit = 64
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return limit - len(m.active)
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
		defer m.finishJob(job.ID)
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
		m.cancel(saveCtx, job)
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
	tweet, err := m.parser.ParseTweetLinkWithOptions(ctx, job.Input, parserOptionsFromConfig(cfg))
	if err != nil {
		if isCancellation(ctx, err) {
			m.cancel(saveCtx, job)
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
	for index, media := range tweet.Media {
		if m.jobCanceled(ctx, saveCtx, job.ID) {
			m.cancel(saveCtx, job)
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
		m.save(saveCtx, job)
		if _, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, "", tweetFilename(cfg, tweet, index), media.Type == parser.MediaPhoto, tweet.CreatedAt); err != nil {
			if isCancellation(ctx, err) {
				m.cancel(saveCtx, job)
				return
			}
			m.fail(saveCtx, job, mediaURL, err)
			return
		}
	}
	if m.jobCanceled(ctx, saveCtx, job.ID) {
		m.cancel(saveCtx, job)
		return
	}
	job.Status = storage.JobCompleted
	job.Progress = 1
	job.Message = "下载完成"
	job.Error = ""
	m.save(saveCtx, job)
}

func (m *Manager) processMediaURL(ctx context.Context, saveCtx context.Context, job storage.Job, mediaURL string, tweetID string) {
	job.Status = storage.JobDownloading
	job.Progress = 0.25
	job.Message = "正在下载媒体"
	job.Error = ""
	m.save(saveCtx, job)
	if err := m.download(ctx, saveCtx, job, mediaURL, tweetID, "media"); err != nil {
		if isCancellation(ctx, err) {
			m.cancel(saveCtx, job)
			return
		}
		m.fail(saveCtx, job, mediaURL, err)
		return
	}
	if m.jobCanceled(ctx, saveCtx, job.ID) {
		m.cancel(saveCtx, job)
		return
	}
	job.Status = storage.JobCompleted
	job.Progress = 1
	job.Message = "下载完成"
	job.Error = ""
	m.save(saveCtx, job)
}

func (m *Manager) processUser(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, pool, err := m.xPool(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	user, err := pool.Primary().GetUserByInput(ctx, job.Input)
	if err != nil {
		if isCancellation(ctx, err) {
			m.cancel(saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	stats, err := m.archiveUsers(ctx, saveCtx, job, cfg, pool, []xclient.User{user}, nil, 0.12, 0.94)
	if err != nil {
		if isCancellation(ctx, err) {
			m.cancel(saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	retried := m.retryFailedTweets(ctx, saveCtx, job, cfg, false)
	m.complete(saveCtx, job, completionMessage(stats, retried))
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
	if m.jobCanceled(ctx, saveCtx, job.ID) {
		m.cancel(saveCtx, job)
		return
	}
	remaining, err := m.store.CountFailedTweets(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	job.Status = storage.JobCompleted
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
			m.cancel(saveCtx, job)
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
			m.cancel(saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	stats, err := m.archiveUsers(ctx, saveCtx, job, cfg, pool, members, &listEntity, 0.18, 0.94)
	if err != nil {
		if isCancellation(ctx, err) {
			m.cancel(saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	retried := m.retryFailedTweets(ctx, saveCtx, job, cfg, false)
	m.complete(saveCtx, job, completionMessage(stats, retried))
}

func (m *Manager) processFollowing(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, pool, err := m.xPool(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	client := pool.Primary()
	owner, err := client.GetUserByInput(ctx, job.Input)
	if err != nil {
		if isCancellation(ctx, err) {
			m.cancel(saveCtx, job)
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
			m.cancel(saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	stats, err := m.archiveUsers(ctx, saveCtx, job, cfg, pool, members, nil, 0.18, 0.94)
	if err != nil {
		if isCancellation(ctx, err) {
			m.cancel(saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	retried := m.retryFailedTweets(ctx, saveCtx, job, cfg, false)
	m.complete(saveCtx, job, completionMessage(stats, retried))
}

func (m *Manager) download(ctx context.Context, saveCtx context.Context, job storage.Job, mediaURL string, tweetID string, filenameHint string) error {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		return err
	}
	_, err = m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweetID, "", filenameHint, isPhotoURL(mediaURL), time.Time{})
	return err
}

func (m *Manager) downloadMedia(ctx context.Context, saveCtx context.Context, job storage.Job, cfg config.AppConfig, mediaURL string, tweetID string, dir string, filenameHint string, largePhoto bool, modTime time.Time) (bool, error) {
	target, err := filestore.New(cfg)
	if err != nil {
		return false, err
	}
	release, err := m.lockMedia(ctx, tweetID, mediaURL)
	if err != nil {
		return false, err
	}
	if release != nil {
		defer release()
		if already, err := m.existingDownloadExists(ctx, saveCtx, target, tweetID, mediaURL); err != nil {
			return false, err
		} else if already {
			return true, nil
		}
	}
	if dir == "" {
		dir = target.Root()
	}
	result, err := target.SaveMedia(ctx, m.downloader, mediaURL, dir, filenameHint, downloader.Options{
		ModTime:           modTime,
		LargePhoto:        largePhoto,
		ProxyURL:          cfg.ProxyURL,
		MaxFilenameLength: cfg.MaxFilenameLength,
	})
	if err != nil {
		return false, err
	}
	if result.Skipped {
		return true, nil
	}
	_, err = m.store.CreateDownload(saveCtx, storage.DownloadRecord{
		JobID:    job.ID,
		TweetID:  tweetID,
		MediaURL: mediaURL,
		FilePath: result.Path,
		Bytes:    result.Bytes,
	})
	return false, err
}

func (m *Manager) existingDownloadExists(ctx context.Context, saveCtx context.Context, target filestore.Store, tweetID string, mediaURL string) (bool, error) {
	if strings.TrimSpace(tweetID) == "" || strings.TrimSpace(mediaURL) == "" {
		return false, nil
	}
	record, err := m.store.GetDownloadByTweetMedia(saveCtx, tweetID, mediaURL)
	if err != nil || record == nil {
		return false, err
	}
	if strings.TrimSpace(record.FilePath) == "" {
		return false, nil
	}
	return target.Exists(ctx, record.FilePath)
}

func (m *Manager) lockMedia(ctx context.Context, tweetID string, mediaURL string) (func(), error) {
	if tweetID == "" || mediaURL == "" {
		return nil, nil
	}
	key := tweetID + "\x00" + mediaURL
	m.mediaMu.Lock()
	lock, ok := m.mediaLocks[key]
	if !ok {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		m.mediaLocks[key] = lock
	}
	m.mediaMu.Unlock()

	select {
	case <-lock:
		return func() { lock <- struct{}{} }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type archiveStats struct {
	Users      int
	Tweets     int
	Downloaded int
	Skipped    int
	Failed     int
}

const maxArchiveUserConcurrency = 8

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

	workerCount := archiveUserConcurrency(cfg, len(tasks))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	baseJob := job
	progressJob := job

	var statsMu sync.Mutex
	addStats := func(delta archiveStats) {
		statsMu.Lock()
		stats.Users += delta.Users
		stats.Tweets += delta.Tweets
		stats.Downloaded += delta.Downloaded
		stats.Skipped += delta.Skipped
		stats.Failed += delta.Failed
		statsMu.Unlock()
	}

	var jobMu sync.Mutex
	completed := 0
	saveProgress := func(status storage.JobStatus, message string) {
		jobMu.Lock()
		defer jobMu.Unlock()
		progressJob.Status = status
		progressJob.Progress = start + (end-start)*float64(completed)/float64(len(tasks))
		progressJob.Message = message
		progressJob.Error = ""
		m.save(saveCtx, progressJob)
	}
	finishTask := func() {
		jobMu.Lock()
		defer jobMu.Unlock()
		completed++
		progressJob.Status = storage.JobResolving
		progressJob.Progress = start + (end-start)*float64(completed)/float64(len(tasks))
		progressJob.Message = fmt.Sprintf("已同步用户 %d/%d", completed, len(tasks))
		progressJob.Error = ""
		m.save(saveCtx, progressJob)
	}

	var errMu sync.Mutex
	var firstErr error
	setErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		errMu.Unlock()
	}

	taskCh := make(chan archiveUserTask)
	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskCh {
				if workCtx.Err() != nil {
					return
				}
				if m.jobCanceled(workCtx, saveCtx, baseJob.ID) {
					setErr(context.Canceled)
					return
				}
				saveProgress(
					storage.JobResolving,
					fmt.Sprintf("同步用户 %d/%d @%s", task.order+1, len(tasks), fallbackUserName(task.user)),
				)
				delta, err := m.archiveUser(workCtx, saveCtx, baseJob, cfg, pool, task.user, listEntity, func(message string) {
					saveProgress(storage.JobDownloading, message)
				})
				addStats(delta)
				if err != nil {
					setErr(err)
					return
				}
				finishTask()
			}
		}()
	}

sendLoop:
	for _, task := range tasks {
		select {
		case taskCh <- task:
		case <-workCtx.Done():
			break sendLoop
		}
	}
	close(taskCh)
	wg.Wait()

	errMu.Lock()
	err = firstErr
	errMu.Unlock()
	if err != nil {
		return stats, err
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
		missingMedia := user.MediaCount
		if existing, err := m.store.LocateUserEntity(ctx, user.ID, parent); err != nil {
			return nil, skipped, err
		} else if existing != nil && existing.MediaCount.Valid {
			missingMedia = max(0, user.MediaCount-int(existing.MediaCount.Int64))
		}
		tasks = append(tasks, archiveUserTask{
			index:        index,
			missingMedia: missingMedia,
			primaryOnly:  user.Protected && user.Following,
			user:         user,
		})
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

func archiveUserConcurrency(cfg config.AppConfig, userCount int) int {
	if userCount <= 1 {
		return 1
	}
	limit := cfg.MaxConcurrency
	if limit <= 0 {
		limit = config.Default().MaxConcurrency
	}
	limit = min(limit, maxArchiveUserConcurrency)
	limit = min(limit, userCount)
	return max(1, limit)
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
		return stats, nil
	}

	opts := parserOptionsFromConfig(cfg)
	if !cfg.IncludeNestedTweetMedia {
		opts.StopAtTweetID = entity.LastSeenTweetID
	}
	tweets, err := pool.GetUserMediaWithOptions(ctx, user, opts)
	if err != nil {
		if isCancellation(ctx, err) {
			return stats, err
		}
		stats.Failed++
		return stats, nil
	}
	if len(tweets) == 0 {
		_ = m.store.UpdateUserEntityMediaCount(saveCtx, entity.ID, user.MediaCount)
		return stats, nil
	}
	target, err := filestore.New(cfg)
	if err != nil {
		return stats, err
	}
	stats.Tweets += len(tweets)
	failedBefore := stats.Failed
	for _, tweet := range tweets {
		for mediaIndex, media := range tweet.Media {
			if m.jobCanceled(ctx, saveCtx, job.ID) {
				return stats, context.Canceled
			}
			mediaURL := bestMediaURL(media)
			if mediaURL == "" {
				continue
			}
			already, err := m.existingDownloadExists(ctx, saveCtx, target, tweet.ID, mediaURL)
			if err != nil {
				stats.Failed++
				_, _ = m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: err.Error()})
				continue
			}
			if already {
				stats.Skipped++
				continue
			}
			if updateDownloading != nil {
				updateDownloading(fmt.Sprintf("下载 @%s 的媒体", fallbackUserName(user)))
			}
			skipped, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, dir, tweetFilename(cfg, tweet, mediaIndex), media.Type == parser.MediaPhoto, tweet.CreatedAt)
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
			if skipped {
				stats.Skipped++
			} else {
				stats.Downloaded++
			}
		}
	}
	if len(tweets) > 0 && stats.Failed == failedBefore {
		_ = m.store.UpdateUserEntityLastSeenTweet(saveCtx, entity.ID, tweets[0].ID)
	}
	_ = m.store.UpdateUserEntityMediaCount(saveCtx, entity.ID, user.MediaCount)
	return stats, nil
}

func (m *Manager) lockUser(ctx context.Context, userID string) (func(), bool, error) {
	if userID == "" {
		return func() {}, false, nil
	}
	m.userMu.Lock()
	lock, ok := m.userLocks[userID]
	if !ok {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		m.userLocks[userID] = lock
	}
	m.userMu.Unlock()

	select {
	case <-lock:
		return func() { lock <- struct{}{} }, false, nil
	default:
	}
	select {
	case <-lock:
		return func() { lock <- struct{}{} }, true, nil
	case <-ctx.Done():
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
		if m.jobCanceled(ctx, saveCtx, job.ID) {
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
			if already, err := m.existingDownloadExists(ctx, saveCtx, target, tweet.ID, mediaURL); err == nil && already {
				continue
			}
			if _, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, dir, tweetFilename(cfg, tweet, index), media.Type == parser.MediaPhoto, tweet.CreatedAt); err != nil {
				if !shouldRetryMediaError(err) {
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
		return statusErr.StatusCode != http.StatusForbidden && statusErr.StatusCode != http.StatusNotFound
	}
	return true
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
		if !target.SupportsLinks() {
			continue
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
	if !target.SupportsLinks() {
		return nil
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
		return
	}
	// 直接用内存中的 job 作为事件载荷，省去一次 GetJob 往返；
	// 前端收到事件后会通过 dashboard 重新拉取最新值，UpdatedAt 在此近似设置即可。
	job.UpdatedAt = time.Now().UTC()
	m.eventBus.Publish(Event{Type: "job.updated", JobID: job.ID, Payload: job})
}

func (m *Manager) cancel(ctx context.Context, job storage.Job) {
	job.Status = storage.JobCanceled
	job.Progress = 1
	job.Message = "已取消"
	job.Error = ""
	m.save(ctx, job)
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
	return strings.TrimSpace(builder.String())
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
		return nil
	}
	if err := removeLinkPlaceholder(linkPath); err != nil {
		return err
	}
	if err := os.Symlink(targetAbs, linkPath); err == nil || os.IsExist(err) {
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
