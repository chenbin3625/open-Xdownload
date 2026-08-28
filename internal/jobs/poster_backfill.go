package jobs

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

// posterBackfillConcurrency 封面批量回填的并发抓取数。与预览端点的远程抓取限流
// 同级，避免大规模缺失封面时对代理或 CDN 造成瞬时压力。
const posterBackfillConcurrency = 4

// PosterBackfillStatus 是媒体库"补齐视频封面"批量任务的进度快照，经
// GET /api/library/posters/backfill 暴露给前端轮询。
type PosterBackfillStatus struct {
	Running    bool       `json:"running"`
	Total      int        `json:"total"`
	Done       int        `json:"done"`
	Fetched    int        `json:"fetched"`
	Skipped    int        `json:"skipped"`
	Failed     int        `json:"failed"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// StartPosterBackfill 启动一轮封面批量回填：扫描全部视频/GIF 记录，逐条补齐
// preview_url 与本地海报文件（并发受限、走代理、仅 twimg）。已有海报的记录直接
// 跳过，因此重复点击是安全的。同一时间只允许一轮在跑。
func (m *Manager) StartPosterBackfill(ctx context.Context) (PosterBackfillStatus, error) {
	m.posterBackfillMu.Lock()
	defer m.posterBackfillMu.Unlock()
	if m.posterBackfillCancel != nil {
		return m.posterBackfillStatus, errors.New("封面补齐已在进行中")
	}
	runCtx, cancel := context.WithCancel(ctx)
	now := time.Now().UTC()
	m.posterBackfillCancel = cancel
	m.posterBackfillStatus = PosterBackfillStatus{Running: true, StartedAt: &now}
	go m.runPosterBackfill(runCtx, cancel)
	return m.posterBackfillStatus, nil
}

// PosterBackfillStatus 返回当前（或最近一轮）封面回填的进度快照。
func (m *Manager) PosterBackfillStatus() PosterBackfillStatus {
	m.posterBackfillMu.Lock()
	defer m.posterBackfillMu.Unlock()
	return m.posterBackfillStatus
}

func (m *Manager) runPosterBackfill(ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("poster backfill panic: %v", r)
		}
		m.posterBackfillMu.Lock()
		now := time.Now().UTC()
		m.posterBackfillStatus.Running = false
		m.posterBackfillStatus.FinishedAt = &now
		m.posterBackfillCancel = nil
		m.posterBackfillMu.Unlock()
		cancel()
	}()

	cfg, err := m.store.GetConfig(ctx)
	if err != nil {
		log.Printf("poster backfill load config: %v", err)
		return
	}
	target, err := filestore.New(cfg)
	if err != nil {
		log.Printf("poster backfill init storage: %v", err)
		return
	}
	records, err := m.store.ListVideoDownloadsForPosterBackfill(ctx)
	if err != nil {
		log.Printf("poster backfill list downloads: %v", err)
		return
	}
	m.updatePosterBackfillStatus(func(status *PosterBackfillStatus) { status.Total = len(records) })

	var wg sync.WaitGroup
	sem := make(chan struct{}, posterBackfillConcurrency)
loop:
	for index := range records {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		wg.Add(1)
		go func(record storage.DownloadRecord) {
			defer wg.Done()
			defer func() { <-sem }()
			if r := recover(); r != nil {
				log.Printf("poster backfill panic for %s: %v", record.FilePath, r)
				m.updatePosterBackfillStatus(func(status *PosterBackfillStatus) {
					status.Done++
					status.Failed++
				})
				return
			}
			fetched, skipped := m.ensureVideoPoster(ctx, cfg, target, &record, "")
			m.updatePosterBackfillStatus(func(status *PosterBackfillStatus) {
				status.Done++
				switch {
				case fetched:
					status.Fetched++
				case skipped:
					status.Skipped++
				default:
					status.Failed++
				}
			})
		}(records[index])
	}
	wg.Wait()
}

func (m *Manager) updatePosterBackfillStatus(fn func(status *PosterBackfillStatus)) {
	m.posterBackfillMu.Lock()
	defer m.posterBackfillMu.Unlock()
	fn(&m.posterBackfillStatus)
}
