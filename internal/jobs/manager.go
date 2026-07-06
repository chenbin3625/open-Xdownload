package jobs

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

type Store interface {
	GetConfig(ctx context.Context) (configLike, error)
	ClaimPendingJobs(ctx context.Context, limit int) ([]storage.Job, error)
	GetJob(ctx context.Context, id int64) (storage.Job, error)
	UpdateJob(ctx context.Context, job storage.Job) error
	CreateDownload(ctx context.Context, record storage.DownloadRecord) (storage.DownloadRecord, error)
	CreateFailedMedia(ctx context.Context, failed storage.FailedMedia) (storage.FailedMedia, error)
}

type configLike interface {
	DownloadDirectory() string
}

type Manager struct {
	store      *storage.Store
	parser     *parser.Service
	eventBus   *EventBus
	downloader *downloader.Downloader
	wake       chan struct{}
	once       sync.Once
}

func NewManager(store *storage.Store, parserService *parser.Service, eventBus *EventBus) *Manager {
	return &Manager{
		store:      store,
		parser:     parserService,
		eventBus:   eventBus,
		downloader: downloader.New(),
		wake:       make(chan struct{}, 1),
	}
}

func (m *Manager) Start(ctx context.Context) {
	m.once.Do(func() {
		go m.loop(ctx)
	})
}

func (m *Manager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
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
	jobs, err := m.store.ClaimPendingJobs(ctx, 1)
	if err != nil || len(jobs) == 0 {
		return
	}
	job := jobs[0]
	m.process(ctx, job)
}

func (m *Manager) process(ctx context.Context, job storage.Job) {
	job.Status = storage.JobResolving
	job.Progress = 0.1
	job.Message = "正在解析"
	job.Error = ""
	m.save(ctx, job)

	switch job.Kind {
	case storage.JobKindTweetLink:
		m.processTweetLink(ctx, job)
	case storage.JobKindMediaURL:
		m.processMediaURL(ctx, job, job.Input, "")
	default:
		job.Status = storage.JobFailed
		job.Progress = 1
		job.Message = "当前任务类型还在迁移中"
		job.Error = fmt.Sprintf("unsupported job kind: %s", job.Kind)
		m.save(ctx, job)
	}
}

func (m *Manager) processTweetLink(ctx context.Context, job storage.Job) {
	tweet, err := m.parser.ParseTweetLink(ctx, job.Input)
	if err != nil {
		m.fail(ctx, job, "", err)
		return
	}
	urls := tweet.BestMediaURLs()
	if len(urls) == 0 {
		job.Status = storage.JobFailed
		job.Progress = 1
		job.Message = "链接已识别，但还没有解析到媒体 URL"
		job.Error = "推文详情 GraphQL 接入尚未完成；可先用媒体原始 URL 创建下载任务"
		m.save(ctx, job)
		return
	}
	for index, mediaURL := range urls {
		job.Status = storage.JobDownloading
		job.Progress = 0.2 + 0.7*float64(index)/float64(len(urls))
		job.Message = fmt.Sprintf("正在下载 %d/%d", index+1, len(urls))
		m.save(ctx, job)
		m.download(ctx, job, mediaURL, tweet.ID, tweetFilename(tweet, index))
	}
	job.Status = storage.JobCompleted
	job.Progress = 1
	job.Message = "下载完成"
	m.save(ctx, job)
}

func (m *Manager) processMediaURL(ctx context.Context, job storage.Job, mediaURL string, tweetID string) {
	job.Status = storage.JobDownloading
	job.Progress = 0.25
	job.Message = "正在下载媒体"
	m.save(ctx, job)
	if err := m.download(ctx, job, mediaURL, tweetID, "media"); err != nil {
		m.fail(ctx, job, mediaURL, err)
		return
	}
	job.Status = storage.JobCompleted
	job.Progress = 1
	job.Message = "下载完成"
	m.save(ctx, job)
}

func (m *Manager) download(ctx context.Context, job storage.Job, mediaURL string, tweetID string, filenameHint string) error {
	cfg, err := m.store.GetConfig(ctx)
	if err != nil {
		return err
	}
	result, err := m.downloader.Download(ctx, mediaURL, cfg.DownloadDir, filenameHint)
	if err != nil {
		_, _ = m.store.CreateFailedMedia(ctx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: err.Error()})
		return err
	}
	_, err = m.store.CreateDownload(ctx, storage.DownloadRecord{
		JobID:    job.ID,
		TweetID:  tweetID,
		MediaURL: mediaURL,
		FilePath: result.Path,
		Bytes:    result.Bytes,
	})
	return err
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
	updated, err := m.store.GetJob(ctx, job.ID)
	if err != nil {
		updated = job
	}
	m.eventBus.Publish(Event{Type: "job.updated", JobID: job.ID, Payload: updated})
}

func tweetFilename(tweet parser.TweetData, index int) string {
	base := strings.TrimSpace(tweet.Text)
	if base == "" {
		base = tweet.ID
	}
	if len(tweet.Media) > 1 {
		base = fmt.Sprintf("%s-%02d", base, index+1)
	}
	return base
}

func IsHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func MediaTitle(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "媒体下载"
	}
	base := filepath.Base(parsed.Path)
	if base == "." || base == "/" || base == "" {
		return parsed.Host
	}
	return base
}
