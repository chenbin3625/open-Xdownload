package jobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/minio/minio-go/v7"
)

func newAssetID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

// assetInput 是待写入资产的源文件。Hash/Size 已知时（下载器在写盘时已经算过、
// 或复制源文件时边拷边算）跳过 filestore.AssetHash 的整文件重读。
type assetInput struct {
	path string
	hash string
	size int64
}

// removeWrittenAsset 在 CreateAsset 入库失败时尽力回滚刚刚新写入且尚未被数据库追踪的
// 物理文件/对象，避免在存储目标留下孤儿文件。调用方必须只在确认 key 不是既有已追踪记录
// 时才调用本函数。
func removeWrittenAsset(target config.StorageTarget, key string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	if target.Type == "s3" {
		client, err := filestore.S3Client(target)
		if err != nil {
			log.Printf("rollback s3 asset %s/%s: %v", target.ID, key, err)
			return
		}
		cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.RemoveObject(cleanCtx, target.Bucket, key, minio.RemoveObjectOptions{}); err != nil && !isAssetNotExistError(err) {
			log.Printf("rollback s3 asset %s/%s: %v", target.ID, key, err)
		}
		return
	}
	root, err := os.OpenRoot(target.Root)
	if err != nil {
		log.Printf("rollback local asset %s/%s: %v", target.ID, key, err)
		return
	}
	defer root.Close()
	if err := root.Remove(filepath.FromSlash(key)); err != nil && !os.IsNotExist(err) {
		log.Printf("rollback local asset %s/%s: %v", target.ID, key, err)
	}
}

func (m *Manager) saveAsset(ctx, saveCtx context.Context, document config.StorageDocument, kind config.AssetKind, tweet parser.TweetData, media parser.Media, index int, source assetInput) (storage.Asset, error) {
	rule, target, err := document.Rule(kind)
	if err != nil {
		return storage.Asset{}, err
	}
	hash, size := source.hash, source.size
	if hash == "" || size <= 0 {
		if hash, size, err = filestore.AssetHash(ctx, source.path); err != nil {
			return storage.Asset{}, err
		}
	}
	ext := strings.TrimPrefix(filepath.Ext(source.path), ".")
	if ext == "jpeg" {
		ext = "jpg"
	}
	contentType := mime.TypeByExtension("." + ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	existing, err := m.store.FindAssetHash(saveCtx, hash, kind, target, document.Version)
	if err != nil {
		return storage.Asset{}, err
	}
	if existing != nil && existing.PolicyVersion == document.Version {
		file, _, openErr := filestore.OpenAsset(ctx, target, existing.Key)
		if openErr == nil {
			file.Close()
			return *existing, nil
		}
		if !isAssetNotExistError(openErr) {
			return storage.Asset{}, openErr
		}
		id, err := newAssetID()
		if err != nil {
			return storage.Asset{}, err
		}
		ct := existing.ContentType
		if ct == "" {
			ct = contentType
		}
		restoredKey, err := filestore.PutAsset(ctx, target, existing.Key, source.path, ct, id)
		if err != nil {
			return storage.Asset{}, err
		}
		if restoredKey == existing.Key {
			return *existing, nil
		}
		replacement := *existing
		replacement.ID, replacement.Key = id, restoredKey
		replacement.Size, replacement.ContentType = size, ct
		created, err := m.store.CreateAsset(saveCtx, replacement)
		if err != nil {
			removeWrittenAsset(target, restoredKey)
			return storage.Asset{}, err
		}
		return created, nil
	}
	desiredKey, err := filestore.TemplatePath(rule, kind, tweet, media, index, ext)
	if err != nil {
		return storage.Asset{}, err
	}
	if existing != nil && desiredKey == existing.Key {
		// 策略升级但目标与模板渲染后的 object_key 与既有同哈希资产一致：
		// 直接复用（或在物理文件丢失时恢复到）既有 key，再通过 CreateAsset 的
		// ON CONFLICT UPSERT 升级 policy_version 并保留原有 Asset.ID，避免碰撞改名。
		ct := existing.ContentType
		if ct == "" {
			ct = contentType
		}
		file, _, openErr := filestore.OpenAsset(ctx, target, existing.Key)
		if openErr == nil {
			file.Close()
			upgraded := *existing
			upgraded.PolicyVersion = document.Version
			upgraded.Size = size
			upgraded.ContentType = ct
			return m.store.CreateAsset(saveCtx, upgraded)
		}
		if !isAssetNotExistError(openErr) {
			return storage.Asset{}, openErr
		}
		id, err := newAssetID()
		if err != nil {
			return storage.Asset{}, err
		}
		restoredKey, err := filestore.PutAsset(ctx, target, existing.Key, source.path, ct, id)
		if err != nil {
			return storage.Asset{}, err
		}
		candidate := *existing
		candidate.PolicyVersion = document.Version
		candidate.Size = size
		candidate.ContentType = ct
		if restoredKey != existing.Key {
			candidate.ID, candidate.Key = id, restoredKey
		}
		created, err := m.store.CreateAsset(saveCtx, candidate)
		if err != nil {
			if restoredKey != existing.Key {
				removeWrittenAsset(target, restoredKey)
			}
			return storage.Asset{}, err
		}
		return created, nil
	}
	id, err := newAssetID()
	if err != nil {
		return storage.Asset{}, err
	}
	writtenKey, err := filestore.PutAsset(ctx, target, desiredKey, source.path, contentType, id)
	if err != nil {
		return storage.Asset{}, err
	}
	created, err := m.store.CreateAsset(saveCtx, storage.Asset{
		ID:            id,
		Kind:          kind,
		PolicyVersion: document.Version,
		TargetID:      target.ID,
		TargetVersion: target.Version,
		Key:           writtenKey,
		ContentHash:   hash,
		Size:          size,
		ContentType:   contentType,
	})
	if err != nil {
		removeWrittenAsset(target, writtenKey)
		return storage.Asset{}, err
	}
	return created, nil
}

func (m *Manager) downloadAssetMedia(ctx, saveCtx context.Context, job storage.Job, cfg config.AppConfig, document config.StorageDocument, mediaURL, tweetID string, photo bool, published time.Time, previewURLs ...string) (mediaDownloadResult, error) {
	key := downloader.MediaIdentity(mediaURL)
	release, err := m.lockMedia(ctx, key)
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
		itemURL := parser.MediaDownloadURL(item)
		if itemURL == mediaURL || (key != "" && downloader.MediaIdentity(itemURL) == key) {
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
	source := assetInput{}
	skipped := asset != nil
	if asset != nil {
		prior, err := m.store.GetStorageVersion(saveCtx, asset.PolicyVersion)
		if err != nil {
			return mediaDownloadResult{}, err
		}
		target, found := findStorageTarget(prior, asset.TargetID, asset.TargetVersion)
		if !found {
			asset = nil
			skipped = false
		} else if asset.PolicyVersion != document.Version {
			// A new job freezes the current policy. Reuse the old bytes as
			// the source, but create a policy-versioned asset at the new
			// target/key instead of linking the historical asset directly.
			skipped = false
			source, err = m.copyAssetToTemp(ctx, *asset, tempDir)
			if err != nil {
				if ctx.Err() != nil {
					return mediaDownloadResult{}, ctx.Err()
				}
				if !isAssetNotExistError(err) && !errors.Is(err, errStorageTargetNotFound) {
					return mediaDownloadResult{}, err
				}
				source = assetInput{}
			}
			asset = nil
		} else {
			file, _, openErr := filestore.OpenAsset(ctx, target, asset.Key)
			if openErr != nil {
				if !isAssetNotExistError(openErr) {
					return mediaDownloadResult{}, openErr
				}
				asset = nil
				skipped = false
			} else {
				file.Close()
			}
		}
	}
	if asset == nil {
		if source.path == "" {
			result, downloadErr := m.downloader.DownloadWithOptions(ctx, mediaURL, tempDir, "source", downloader.Options{ModTime: published, LargePhoto: photo, ProxyURL: cfg.ProxyURL, MaxFilenameLength: 120})
			if downloadErr != nil {
				if isPermanentlyUnavailableMediaError(downloadErr) {
					previouslyFailed, checkErr := m.store.HasPermanentFailedMediaForURL(saveCtx, mediaURL)
					if checkErr != nil {
						log.Printf("check permanent failed media for %s: %v", mediaURL, checkErr)
					} else if previouslyFailed {
						if markErr := m.store.UpsertUnavailableMedia(saveCtx, storage.UnavailableMedia{
							MediaURL: mediaURL,
							TweetID:  tweetID,
							Error:    downloadErr.Error(),
						}); markErr != nil {
							return mediaDownloadResult{}, fmt.Errorf("记录永久不可用媒体失败: %v: %w", markErr, downloadErr)
						}
						_, _ = m.store.CreateFailedMedia(saveCtx, storage.FailedMedia{JobID: job.ID, MediaURL: mediaURL, Error: downloadErr.Error()})
						return mediaDownloadResult{skipped: true, unavailable: true}, nil
					}
				}
				return mediaDownloadResult{}, downloadErr
			}
			// 下载器写盘时已经流过 sha256 并统计了字节数，直接复用，避免再整文件读一遍。
			source = assetInput{path: result.Path, hash: result.ContentHash, size: result.Bytes}
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
	isLocalTarget := false
	prior, err := m.store.GetStorageVersion(saveCtx, asset.PolicyVersion)
	if err != nil {
		return mediaDownloadResult{}, err
	}
	if target, found := findStorageTarget(prior, asset.TargetID, asset.TargetVersion); found && target.Type != "s3" {
		filePath = filepath.Join(target.Root, filepath.FromSlash(asset.Key))
		isLocalTarget = true
	}
	if isLocalTarget && !published.IsZero() && !skipped {
		_ = os.Chtimes(filePath, published, published)
	}
	var record storage.DownloadRecord
	if skipped && existing != nil {
		// 命中既有缓存且本条推文+媒体已有下载记录：直接复用，不再调用 CreateDownload
		// 刷新 created_at（会打乱媒体库按时间倒序的展示顺序）。
		record = *existing
		links, linkErr := m.store.DownloadAssets(saveCtx, record.ID)
		if linkErr != nil {
			return mediaDownloadResult{}, linkErr
		}
		if links[kind] != asset.ID {
			if err := m.store.LinkDownloadAsset(saveCtx, record.ID, kind, asset.ID); err != nil {
				return mediaDownloadResult{}, err
			}
		}
	} else {
		record, err = m.store.CreateDownload(saveCtx, storage.DownloadRecord{JobID: job.ID, TweetID: tweetID, MediaURL: mediaURL, PreviewURL: preview, FilePath: filePath, Bytes: asset.Size, ContentHash: asset.ContentHash})
		if err != nil {
			return mediaDownloadResult{}, err
		}
		if err := m.store.LinkDownloadAsset(saveCtx, record.ID, kind, asset.ID); err != nil {
			return mediaDownloadResult{}, err
		}
	}
	if isLocalTarget && !photo {
		if localStore, storeErr := filestore.New(cfg); storeErr == nil {
			posterRecord := record
			posterRecord.FilePath = filePath
			m.ensureVideoPoster(ctx, cfg, localStore, &posterRecord, preview)
		}
	}
	return mediaDownloadResult{skipped: skipped}, nil
}

func (m *Manager) copyAssetToTemp(ctx context.Context, asset storage.Asset, dir string) (assetInput, error) {
	document, err := m.store.GetStorageVersion(ctx, asset.PolicyVersion)
	if err != nil {
		return assetInput{}, err
	}
	target, found := findStorageTarget(document, asset.TargetID, asset.TargetVersion)
	if !found {
		return assetInput{}, fmt.Errorf("%w: %s (v%d)", errStorageTargetNotFound, asset.TargetID, asset.TargetVersion)
	}
	source, _, err := filestore.OpenAsset(ctx, target, asset.Key)
	if err != nil {
		return assetInput{}, err
	}
	defer source.Close()
	filename := filepath.Join(dir, "source"+path.Ext(asset.Key))
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return assetInput{}, err
	}
	// 复制时顺便算哈希：随后 saveAsset 不必再把整个文件读一遍。
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hasher), &migrationReader{ctx: ctx, source: source})
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(filename)
		return assetInput{}, err
	}
	return assetInput{path: filename, hash: hex.EncodeToString(hasher.Sum(nil)), size: size}, nil
}

func findStorageTarget(doc config.StorageDocument, targetID string, targetVersion int64) (config.StorageTarget, bool) {
	for _, t := range doc.Targets {
		if t.ID == targetID && t.Version == targetVersion {
			return t, true
		}
	}
	return config.StorageTarget{}, false
}

var errStorageTargetNotFound = errors.New("存储目标不存在")

func isAssetNotExistError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	response := minio.ToErrorResponse(err)
	return response.StatusCode == http.StatusNotFound || response.Code == "NoSuchKey" || response.Code == "NotFound"
}
