package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"mime"
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
	return mediaDownloadResult{skipped: skipped}, nil
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
