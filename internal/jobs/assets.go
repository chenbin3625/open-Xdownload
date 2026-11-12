package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func newAssetID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func (m *Manager) saveAsset(ctx, saveCtx context.Context, document config.StorageDocument, kind config.AssetKind, tweet parser.TweetData, media parser.Media, index int, source string) (storage.Asset, error) {
	rule, target, err := document.Rule(kind)
	if err != nil {
		return storage.Asset{}, err
	}
	hash, size, err := filestore.AssetHash(ctx, source)
	if err != nil {
		return storage.Asset{}, err
	}
	if existing, err := m.store.FindAssetHash(ctx, hash, kind, target, document.Version); err != nil {
		return storage.Asset{}, err
	} else if existing != nil && existing.PolicyVersion == document.Version {
		file, _, openErr := filestore.OpenAsset(ctx, target, existing.Key)
		if openErr == nil {
			file.Close()
			return *existing, nil
		}
		id, err := newAssetID()
		if err != nil {
			return storage.Asset{}, err
		}
		restoredKey, err := filestore.PutAsset(ctx, target, existing.Key, source, existing.ContentType, id)
		if err != nil {
			return storage.Asset{}, err
		}
		if restoredKey == existing.Key {
			return *existing, nil
		}
		replacement := *existing
		replacement.ID, replacement.Key = id, restoredKey
		return m.store.CreateAsset(saveCtx, replacement)
	}
	id, err := newAssetID()
	if err != nil {
		return storage.Asset{}, err
	}
	ext := strings.TrimPrefix(filepath.Ext(source), ".")
	if ext == "jpeg" {
		ext = "jpg"
	}
	key, err := filestore.TemplatePath(rule, kind, tweet, media, index, ext)
	if err != nil {
		return storage.Asset{}, err
	}
	contentType := mime.TypeByExtension("." + ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	key, err = filestore.PutAsset(ctx, target, key, source, contentType, id)
	if err != nil {
		return storage.Asset{}, err
	}
	return m.store.CreateAsset(saveCtx, storage.Asset{ID: id, Kind: kind, PolicyVersion: document.Version, TargetID: target.ID, TargetVersion: target.Version, Key: key, ContentHash: hash, Size: size, ContentType: contentType})
}

func (m *Manager) downloadAssetMedia(ctx, saveCtx context.Context, job storage.Job, cfg config.AppConfig, document config.StorageDocument, mediaURL, tweetID string, photo bool, published time.Time, previewURLs ...string) (mediaDownloadResult, error) {
	key := downloader.MediaIdentity(mediaURL)
	release, err := m.lockMedia(ctx, key)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	defer release()
	kind := config.AssetVideo
	mediaType := parser.MediaVideo
	if photo {
		kind = config.AssetImage
		mediaType = parser.MediaPhoto
	}
	tweet := parser.TweetData{ID: tweetID, CreatedAt: published}
	if contextTweet, _, _, contextErr := m.store.AssetContext(saveCtx, key); contextErr != nil {
		return mediaDownloadResult{}, contextErr
	} else if contextTweet.ID != "" {
		tweet = contextTweet
	}
	if archived, err := m.store.GetTweet(saveCtx, tweetID); err == nil {
		tweet = archived.TweetData
	}
	media := parser.Media{ID: key, Type: mediaType, BestURL: mediaURL}
	index := 0
	for i, item := range tweet.Media {
		if parser.MediaDownloadURL(item) == mediaURL {
			media = item
			index = i
			break
		}
	}
	if media.ID == "" {
		media.ID = key
	}
	asset, err := m.store.GetMediaAsset(saveCtx, key, kind)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	tempDir, err := os.MkdirTemp("", "open-xdownload-asset-")
	if err != nil {
		return mediaDownloadResult{}, err
	}
	defer os.RemoveAll(tempDir)
	var source string
	skipped := asset != nil
	if asset != nil {
		prior, err := m.store.GetStorageVersion(saveCtx, asset.PolicyVersion)
		if err != nil {
			return mediaDownloadResult{}, err
		}
		var target config.StorageTarget
		for _, t := range prior.Targets {
			if t.ID == asset.TargetID && t.Version == asset.TargetVersion {
				target = t
				break
			}
		}
		file, _, openErr := filestore.OpenAsset(ctx, target, asset.Key)
		if openErr != nil {
			asset = nil
			skipped = false
		} else {
			file.Close()
			if asset.PolicyVersion != document.Version {
				// A new job freezes the current policy. Reuse the old bytes as
				// the source, but create a policy-versioned asset at the new
				// target/key instead of linking the historical asset directly.
				source, err = m.copyAssetToTemp(ctx, *asset, tempDir)
				if err != nil {
					asset = nil
					source = ""
				} else {
					asset = nil
					skipped = false
				}
			}
		}
	}
	if asset == nil {
		if source == "" {
			result, downloadErr := m.downloader.DownloadWithOptions(ctx, mediaURL, tempDir, "source", downloader.Options{LargePhoto: photo, ProxyURL: cfg.ProxyURL, MaxFilenameLength: 120})
			if downloadErr != nil {
				return mediaDownloadResult{}, downloadErr
			}
			source = result.Path
		}
		created, err := m.saveAsset(ctx, saveCtx, document, kind, tweet, media, index, source)
		if err != nil {
			return mediaDownloadResult{}, err
		}
		asset = &created
		if err := m.store.SetMediaAsset(saveCtx, storage.MediaAsset{MediaKey: key, Kind: kind, AssetID: asset.ID, Status: "ready", JobID: job.ID}); err != nil {
			return mediaDownloadResult{}, err
		}
	}
	var preview string
	if len(previewURLs) > 0 {
		preview = previewURLs[0]
	}
	filePath := "asset:" + asset.ID + "/" + path.Base(asset.Key)
	prior, err := m.store.GetStorageVersion(saveCtx, asset.PolicyVersion)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	for _, t := range prior.Targets {
		if t.ID == asset.TargetID && t.Type != "s3" {
			filePath = filepath.Join(t.Root, filepath.FromSlash(asset.Key))
		}
	}
	record, err := m.store.CreateDownload(saveCtx, storage.DownloadRecord{JobID: job.ID, TweetID: tweetID, MediaURL: mediaURL, PreviewURL: preview, FilePath: filePath, Bytes: asset.Size, ContentHash: asset.ContentHash})
	if err != nil {
		return mediaDownloadResult{}, err
	}
	if err := m.store.LinkDownloadAsset(saveCtx, record.ID, kind, asset.ID); err != nil {
		return mediaDownloadResult{}, err
	}
	err = m.archiveDerivatives(ctx, saveCtx, job, document, *asset, tweet, media, index, source, tempDir, key, record.ID, false)
	return mediaDownloadResult{skipped: skipped}, err
}

func (m *Manager) archiveDerivatives(ctx, saveCtx context.Context, job storage.Job, document config.StorageDocument, original storage.Asset, tweet parser.TweetData, media parser.Media, index int, source, tempDir, key string, downloadID int64, relocate bool) error {
	derivativeKinds := []config.AssetKind{config.AssetThumbnail}
	if original.Kind == config.AssetVideo {
		derivativeKinds = append(derivativeKinds, config.AssetVideoCover)
	}
	var failures []error
	for _, derivativeKind := range derivativeKinds {
		derivative, err := m.store.GetMediaAsset(saveCtx, key, derivativeKind)
		if err != nil {
			return err
		}
		if derivative != nil && derivative.PolicyVersion != document.Version {
			derivative = nil
		}
		if derivative != nil {
			prior, loadErr := m.store.GetStorageVersion(saveCtx, derivative.PolicyVersion)
			if loadErr != nil {
				return loadErr
			}
			var target config.StorageTarget
			for _, t := range prior.Targets {
				if t.ID == derivative.TargetID && t.Version == derivative.TargetVersion {
					target = t
					break
				}
			}
			file, _, openErr := filestore.OpenAsset(ctx, target, derivative.Key)
			if openErr != nil {
				derivative = nil
			} else {
				file.Close()
			}
		}
		if derivative == nil {
			if source == "" {
				source, err = m.copyAssetToTemp(ctx, original, tempDir)
				if err != nil {
					if saveErr := m.store.SetMediaAsset(saveCtx, storage.MediaAsset{MediaKey: key, Kind: derivativeKind, JobID: job.ID, Status: "failed", Error: err.Error()}); saveErr != nil {
						return saveErr
					}
					failures = append(failures, err)
					continue
				}
			}
			created, err := m.generateDerivative(ctx, saveCtx, job, document, derivativeKind, tweet, media, index, source, tempDir, key)
			if err != nil {
				failures = append(failures, fmt.Errorf("原文件已归档，%s 生成失败: %w", derivativeKind, err))
				continue
			}
			derivative = &created
		}
		if err := m.store.LinkDownloadAsset(saveCtx, downloadID, derivativeKind, derivative.ID); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) copyAssetToTemp(ctx context.Context, asset storage.Asset, dir string) (string, error) {
	document, err := m.store.GetStorageVersion(ctx, asset.PolicyVersion)
	if err != nil {
		return "", err
	}
	var target config.StorageTarget
	for _, t := range document.Targets {
		if t.ID == asset.TargetID && t.Version == asset.TargetVersion {
			target = t
			break
		}
	}
	source, _, err := filestore.OpenAsset(ctx, target, asset.Key)
	if err != nil {
		return "", err
	}
	defer source.Close()
	filename := filepath.Join(dir, "source"+path.Ext(asset.Key))
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(file, &migrationReader{ctx: ctx, source: source})
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(filename)
	}
	return filename, err
}

func (m *Manager) generateDerivative(ctx, saveCtx context.Context, job storage.Job, document config.StorageDocument, kind config.AssetKind, tweet parser.TweetData, media parser.Media, index int, source, dir, key string) (storage.Asset, error) {
	format := document.Thumbnail.Format
	width, height := document.Thumbnail.Width, document.Thumbnail.Height
	if kind == config.AssetVideoCover {
		format = "jpeg"
		width = 1920
		height = 1920
	}
	ext := format
	if ext == "jpeg" {
		ext = "jpg"
	}
	destination := filepath.Join(dir, string(kind)+"."+ext)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-threads", "1", "-i", source, "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", width, height), "-filter_threads", "1"}
	if format == "jpeg" {
		args = append(args, "-q:v", strconv.Itoa(max(2, 31-document.Thumbnail.Quality*29/100)))
	}
	if format == "webp" {
		args = append(args, "-quality", strconv.Itoa(document.Thumbnail.Quality))
	}
	args = append(args, destination)
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("ffmpeg: %w: %.500s", err, output)
	}
	var asset storage.Asset
	if err == nil {
		asset, err = m.saveAsset(ctx, saveCtx, document, kind, tweet, media, index, destination)
	}
	state := storage.MediaAsset{MediaKey: key, Kind: kind, AssetID: asset.ID, Status: "ready", JobID: job.ID}
	if err != nil {
		state.Status = "failed"
		state.Error = err.Error()
	}
	if saveErr := m.store.SetMediaAsset(saveCtx, state); saveErr != nil {
		return asset, saveErr
	}
	return asset, err
}

func (m *Manager) processAssetDerivative(ctx, saveCtx context.Context, job storage.Job) {
	parts := strings.SplitN(job.Input, "|", 2)
	if len(parts) != 2 {
		m.fail(saveCtx, job, "", fmt.Errorf("无效的衍生任务"))
		return
	}
	kind := config.AssetKind(parts[0])
	key := parts[1]
	if kind != config.AssetThumbnail && kind != config.AssetVideoCover {
		m.fail(saveCtx, job, "", fmt.Errorf("无效的衍生类型"))
		return
	}
	release, err := m.lockMedia(ctx, key)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	defer release()
	asset, err := m.store.GetMediaAsset(saveCtx, key, config.AssetImage)
	if err == nil && asset == nil {
		asset, err = m.store.GetMediaAsset(saveCtx, key, config.AssetVideo)
	}
	if err != nil || asset == nil {
		m.fail(saveCtx, job, "", fmt.Errorf("原始资源不可用: %v", err))
		return
	}
	document, err := m.store.FreezeJobStorage(saveCtx, job.ID)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	dir, err := os.MkdirTemp("", "open-xdownload-derivative-")
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	defer os.RemoveAll(dir)
	source, err := m.copyAssetToTemp(ctx, *asset, dir)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	tweet, media, index, err := m.store.AssetContext(saveCtx, key)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	created, err := m.generateDerivative(ctx, saveCtx, job, document, kind, tweet, media, index, source, dir, key)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	if err := m.store.LinkMediaDerivative(saveCtx, key, kind, created.ID); err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	job.Status = storage.JobCompleted
	job.Progress = 1
	job.Error = ""
	job.Message = "衍生资源已生成"
	m.save(saveCtx, job)
}

func mediaIDFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "media"
	}
	return strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path))
}
