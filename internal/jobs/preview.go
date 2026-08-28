package jobs

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

const (
	// posterFetchTimeout bounds a single poster image fetch.
	posterFetchTimeout = 20 * time.Second
	// maxPosterBytes caps the poster image size；name=small 缩略图通常 <100KB。
	maxPosterBytes = 10 << 20
)

// backfillExistingMedia 在归档重新遇到“已下载且文件仍在”的媒体时运行。
// preview_url 是较晚上线的字段，历史记录里常为空：这里用本次解析重新读到的
// 预览图地址回填数据库（仅更新 preview_url，不改创建时间，媒体库排序不变），
// 并在本地存储下把缩略图保存为 <视频文件>.preview.jpg —— 媒体库预览端点会直接
// 命中该文件，浏览器不再逐个回源 twimg 拉图。仅视频/GIF 需要海报（图片的预览
// 就是文件本身）。所有失败只记日志，绝不影响归档任务本身。
func (m *Manager) backfillExistingMedia(ctx context.Context, cfg config.AppConfig, target filestore.Store, record *storage.DownloadRecord, freshPreviewURL string) {
	if record == nil || record.ID <= 0 || ctx.Err() != nil {
		return
	}
	if !isVideoLikePath(record.FilePath) {
		return
	}
	posterURL := strings.TrimSpace(freshPreviewURL)
	if posterURL == "" || isVideoLikePath(posterURL) {
		posterURL = storage.VideoPreviewURL(record.MediaURL)
	}
	if posterURL != "" && posterURL != record.PreviewURL {
		if err := m.store.UpdateDownloadPreviewURL(ctx, record.ID, posterURL); err != nil {
			log.Printf("backfill download %d preview url: %v", record.ID, err)
		} else {
			record.PreviewURL = posterURL
		}
	}
	if posterURL == "" || target.Type() != config.StorageLocal {
		return
	}
	posterPath := record.FilePath + ".preview.jpg"
	exists, err := target.Exists(ctx, posterPath)
	if err != nil || exists {
		return
	}
	if err := savePosterImage(ctx, cfg.ProxyURL, posterURL, posterPath); err != nil {
		log.Printf("backfill video poster %s: %v", record.FilePath, err)
	}
}

// isVideoLikePath reports whether value looks like a video/GIF media path or
// URL（X 的 GIF 实际落地为 mp4，同样纳入海报回填范围）。
func isVideoLikePath(value string) bool {
	switch strings.ToLower(filepath.Ext(strings.SplitN(value, "?", 2)[0])) {
	case ".mp4", ".mov", ".m4v", ".webm", ".ogv", ".gif":
		return true
	default:
		return false
	}
}

// savePosterImage fetches an image over HTTP（代理感知）and publishes it
// atomically to destPath. 预览图地址只允许 twimg.com：即便 downloads 表被篡改，
// 也不能把本服务变成任意地址的抓取代理。
func savePosterImage(ctx context.Context, proxyURL string, rawURL string, destPath string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !isTwimgPreviewHost(parsed.Hostname()) {
		return fmt.Errorf("预览图地址无效")
	}
	fetchCtx, cancel := context.WithTimeout(ctx, posterFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	response, err := httpx.Client(proxyURL, posterFetchTimeout).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "image/") {
		return fmt.Errorf("内容类型无效: %s", contentType)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destPath), ".preview-*.jpg")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if _, err = io.Copy(temporary, io.LimitReader(response.Body, maxPosterBytes)); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if err = temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err = os.Rename(temporaryPath, destPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func isTwimgPreviewHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "twimg.com" || strings.HasSuffix(host, ".twimg.com")
}
