// Package filestore 提供媒体文件落地实现。SMB/WebDAV 后端已移除，
// 仅保留本地目录存储；接口保留 Store 形式以便未来扩展。
package filestore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

// ErrTargetExists 表示 Rename 的目标路径已存在且不是源本身。调用方（jobs 的
// renameEntityDir）据此保留旧目录名并回滚，绝不允许把两条实体记录合并到同一目录。
var ErrTargetExists = errors.New("改名目标已存在")

// MediaDownloader 是 filestore 需要的最小下载能力。定义在本包内，Store 接口不再直接
// 依赖 downloader 的具体类型（*downloader.Downloader），实现与测试都可以替换；
// *downloader.Downloader 的方法集恰好满足本接口。
type MediaDownloader interface {
	DownloadWithOptions(ctx context.Context, rawURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error)
}

type Store interface {
	Type() config.StorageType
	Root() string
	Join(base string, elems ...string) string
	Exists(ctx context.Context, path string) (bool, error)
	MkdirAll(ctx context.Context, dir string) error
	Rename(ctx context.Context, oldPath string, newPath string) error
	SaveMedia(ctx context.Context, d MediaDownloader, mediaURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error)
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

// Rename 把 oldPath 改名为 newPath（目录或文件），三种结果语义与 jobs 约定如下：
//
//  1. 成功：返回 nil。包含"目标已是同一文件/目录（同一 inode）"的幂等重试——
//     此时不做任何操作，调用方可以继续把 DB 里的路径前缀改写到 newPath。
//  2. 源不存在：返回包装了 os.ErrNotExist 的错误（errors.Is 可识别）。jobs 据此
//     认为"改名已经发生过/源已不在"，继续 DB 路径重写而不是回滚。
//  3. 目标已存在且不是源本身：返回 *os.LinkError（其 Err 同时满足
//     errors.Is(err, fs.ErrExist) 与 errors.Is(err, ErrTargetExists)）。jobs 保留
//     旧目录名并回滚，避免把两个实体的媒体合并进同一目录。
//
// 其他 I/O 错误（权限、跨设备、只读文件系统等）原样包装返回，由调用方决定回滚。
// 与直接 os.Rename 不同，本方法绝不覆盖已存在的目标。
//
// 改名成功后尽力 fsync 父目录，降低断电/崩溃导致目录项丢失的概率
// （Windows 不支持对目录 fsync，这类错误被忽略）。
func (s localStore) Rename(_ context.Context, oldPath string, newPath string) error {
	oldInfo, err := os.Lstat(oldPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, os.ErrNotExist)
		}
		return fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, err)
	}
	targetInfo, err := os.Lstat(newPath)
	switch {
	case err == nil:
		if os.SameFile(oldInfo, targetInfo) {
			// 已是同一文件/目录：幂等处理，视为成功。
			return nil
		}
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: fmt.Errorf("%w: %w", ErrTargetExists, fs.ErrExist)}
	case !os.IsNotExist(err):
		return fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, err)
	}
	// 目标父目录必须已经存在：否则 os.Rename 会返回 ENOENT，而 errors.Is(err,
	// os.ErrNotExist) 会被调用方误读成"源已不存在"。这里提前用不包装 %w 的错误拒绝
	// （刻意不保留 ErrNotExist 身份），让 jobs 走回滚分支。
	parent := filepath.Dir(newPath)
	if info, statErr := os.Stat(parent); statErr != nil || !info.IsDir() {
		return fmt.Errorf("rename %s -> %s: 目标父目录不可用: %v", oldPath, newPath, statErr)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		if os.IsNotExist(err) {
			// 源在 Lstat 之后被并发删除：归入"源缺失"语义，jobs 可继续 DB 重写。
			return fmt.Errorf("rename %s -> %s: source vanished: %w", oldPath, newPath, os.ErrNotExist)
		}
		return fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, err)
	}
	syncDir(filepath.Dir(newPath))
	return nil
}

// syncDir 尽力把目录项变更刷到磁盘。部分平台（Windows）不支持对目录 fsync，
// 该类错误被忽略：改名本身已经成功，返回错误反而会误导调用方回滚。
func syncDir(dir string) {
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = handle.Sync()
	_ = handle.Close()
}

func (s localStore) SaveMedia(ctx context.Context, d MediaDownloader, mediaURL string, dir string, filenameHint string, options downloader.Options) (downloader.Result, error) {
	return d.DownloadWithOptions(ctx, mediaURL, dir, filenameHint, options)
}
