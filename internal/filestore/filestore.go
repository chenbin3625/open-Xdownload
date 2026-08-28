// Package filestore 提供媒体文件落地实现。SMB/WebDAV 后端已移除，
// 仅保留本地目录存储；接口保留 Store 形式以便未来扩展。
package filestore

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

type Store interface {
	Type() config.StorageType
	Root() string
	Join(base string, elems ...string) string
	Exists(ctx context.Context, path string) (bool, error)
	MkdirAll(ctx context.Context, dir string) error
	Rename(ctx context.Context, oldPath string, newPath string) error
	SaveMedia(ctx context.Context, d *downloader.Downloader, mediaURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error)
	TestWritable(ctx context.Context) (string, error)
}

func New(cfg config.AppConfig) (Store, error) {
	cfg = cfg.Normalized()
	if err := httpx.ValidateProxyURL(cfg.ProxyURL); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(cfg.DownloadDir)
	if err != nil {
		return nil, err
	}
	return localStore{root: root}, nil
}

type localStore struct {
	root string
}

func (s localStore) Type() config.StorageType { return config.StorageLocal }
func (s localStore) Root() string             { return s.root }

func (s localStore) Join(base string, elems ...string) string {
	items := append([]string{base}, elems...)
	return filepath.Join(items...)
}

func (s localStore) Exists(_ context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (s localStore) MkdirAll(_ context.Context, dir string) error {
	// 本地目录收紧为 0700，与数据目录加固保持一致：私有账号下载的媒体
	// 不应被其他本地用户读取/遍历。MkdirAll 仅在创建目录时设置该模式。
	return os.MkdirAll(dir, 0o700)
}

func (s localStore) Rename(_ context.Context, oldPath string, newPath string) error {
	if _, err := os.Lstat(oldPath); os.IsNotExist(err) {
		return nil
	}
	return os.Rename(oldPath, newPath)
}

func (s localStore) SaveMedia(ctx context.Context, d *downloader.Downloader, mediaURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error) {
	return d.DownloadWithOptions(ctx, mediaURL, dir, filenameHint, options)
}

func (s localStore) TestWritable(_ context.Context) (string, error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", err
	}
	filePath := filepath.Join(s.root, probeFilename())
	if err := os.WriteFile(filePath, probePayload, 0o600); err != nil {
		return "", err
	}
	if err := os.Remove(filePath); err != nil {
		return filePath, err
	}
	return filePath, nil
}

var probePayload = []byte("open-Xdownload storage test\n")

func probeFilename() string {
	return ".open-xdownload-storage-test-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".tmp"
}
