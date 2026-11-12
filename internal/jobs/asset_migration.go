package jobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

// Copy and switch associations one record at a time. Sources and old asset IDs
// remain available; a recovered job can repeat the scan without moving them again.
func (m *Manager) processAssetMigration(ctx, saveCtx context.Context, job storage.Job) {
	document, err := m.store.FreezeJobStorage(saveCtx, job.ID)
	if err != nil || !document.Enabled {
		m.fail(saveCtx, job, "", fmt.Errorf("请先启用分类存储: %v", err))
		return
	}
	roots, err := m.store.DownloadRoots(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	progress, err := m.store.GetAssetMigrationProgress(saveCtx, job.ID)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	for {
		records, err := m.store.AssetMigrationDownloads(saveCtx, progress.LastID, progress.MaxID)
		if err != nil {
			m.fail(saveCtx, job, "", err)
			return
		}
		if len(records) == 0 {
			break
		}
		for _, record := range records {
			if ctx.Err() != nil {
				m.handleInterrupt(ctx, saveCtx, job)
				return
			}
			if err := m.migrateDownloadAsset(ctx, saveCtx, job, document, record, roots); err != nil {
				if ctx.Err() != nil {
					m.handleInterrupt(ctx, saveCtx, job)
					return
				}
				progress.Failed++
				if _, saveErr := m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: record.MediaURL, Error: fmt.Sprintf("download %d: %v", record.ID, err)}); saveErr != nil {
					m.fail(saveCtx, job, "", saveErr)
					return
				}
			}
			progress.Processed++
			progress.LastID = record.ID
			if err := m.store.SaveAssetMigrationProgress(saveCtx, progress); err != nil {
				m.fail(saveCtx, job, "", err)
				return
			}
			job.Status = storage.JobDownloading
			job.Progress = float64(progress.Processed) / float64(max(1, progress.Total))
			job.Message = fmt.Sprintf("历史资源迁移：已处理 %d/%d，失败 %d", progress.Processed, progress.Total, progress.Failed)
			m.save(saveCtx, job)
		}
	}
	job.Status, job.Progress, job.Error = storage.JobCompleted, 1, ""
	job.Message = fmt.Sprintf("历史资源迁移完成：%d 条，源文件已保留", progress.Processed)
	if progress.Failed > 0 {
		job.Status = storage.JobCompletedWithErrors
		job.Error = fmt.Sprintf("%d 条迁移失败，可重试任务", progress.Failed)
	}
	m.save(saveCtx, job)
}

func (m *Manager) migrateDownloadAsset(ctx, saveCtx context.Context, job storage.Job, document config.StorageDocument, record storage.DownloadRecord, roots []string) error {
	key := downloader.MediaIdentity(record.MediaURL)
	if key == "" {
		key = fmt.Sprintf("legacy-%d", record.ID)
	}
	release, err := m.lockMedia(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	links, err := m.store.DownloadAssets(saveCtx, record.ID)
	if err != nil {
		return err
	}
	if len(links) == 0 && strings.TrimSpace(record.FilePath) == "" {
		return fmt.Errorf("历史记录没有资产位置或源文件路径")
	}
	kind := config.AssetVideo
	ext := strings.ToLower(filepath.Ext(record.FilePath))
	if links[config.AssetImage] != "" || (links[config.AssetVideo] == "" && (isPhotoURL(record.MediaURL) || ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".avif" || ext == ".gif")) {
		kind = config.AssetImage
	}
	dir, err := os.MkdirTemp("", "open-xdownload-migration-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var source string
	var original storage.Asset
	if id := links[kind]; id != "" {
		original, err = m.store.GetAsset(saveCtx, id)
		if err != nil {
			return err
		}
		source, err = m.copyAssetToTemp(ctx, original, dir)
		if err != nil {
			original = storage.Asset{}
			source = ""
			err = nil
		}
	}
	if source == "" {
		// Only registered legacy download roots are eligible as migration sources.
		for _, rootPath := range roots {
			relative, relErr := filepath.Rel(rootPath, record.FilePath)
			if relErr != nil || !filepath.IsLocal(relative) {
				continue
			}
			root, openErr := os.OpenRoot(rootPath)
			if openErr != nil {
				continue
			}
			file, openErr := root.Open(relative)
			root.Close()
			if openErr != nil {
				err = openErr
				continue
			}
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				file.Close()
				return fmt.Errorf("历史文件不是普通文件")
			}
			source = filepath.Join(dir, "source"+ext)
			out, createErr := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if createErr != nil {
				file.Close()
				return createErr
			}
			_, err = io.Copy(out, &migrationReader{ctx: ctx, source: file})
			closeErr := out.Close()
			file.Close()
			if err == nil {
				err = closeErr
			}
			break
		}
		if source == "" {
			return fmt.Errorf("历史文件不在可信目录内或不可读取")
		}
	}
	if err != nil {
		return err
	}
	tweet, media, index, err := m.store.AssetContext(saveCtx, key)
	if err != nil {
		return err
	}
	if tweet.ID == "" {
		tweet.ID, tweet.CreatedAt = record.TweetID, record.CreatedAt
		media.ID = key
		media.Type = parser.MediaVideo
		if kind == config.AssetImage {
			media.Type = parser.MediaPhoto
		}
	}
	if archived, loadErr := m.store.GetTweet(saveCtx, record.TweetID); loadErr == nil {
		tweet = archived.TweetData
	}
	if original.ID == "" || original.PolicyVersion != document.Version {
		original, err = m.saveAsset(ctx, saveCtx, document, kind, tweet, media, index, source)
		if err != nil {
			return err
		}
		if err := m.store.LinkDownloadAsset(saveCtx, record.ID, kind, original.ID); err != nil {
			return err
		}
	}
	if err := m.store.SetMediaAsset(saveCtx, storage.MediaAsset{MediaKey: key, Kind: kind, AssetID: original.ID, JobID: job.ID, Status: "ready"}); err != nil {
		return err
	}
	return m.archiveDerivatives(ctx, saveCtx, job, document, original, tweet, media, index, source, dir, key, record.ID, true)
}

type migrationReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *migrationReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
