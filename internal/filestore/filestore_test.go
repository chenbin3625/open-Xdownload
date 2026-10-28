package filestore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

func TestNewReturnsLocalStoreForDefaultConfig(t *testing.T) {
	cfg := config.Default()
	cfg.DownloadDir = t.TempDir()
	store, err := New(cfg)
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	if store.Type() != config.StorageLocal {
		t.Fatalf("Type() = %v, want local", store.Type())
	}
	if store.Root() != cfg.DownloadDir {
		t.Fatalf("Root() = %q, want %q", store.Root(), cfg.DownloadDir)
	}
}

func TestLocalStoreMkdirAllUsesPrivateMode(t *testing.T) {
	root := t.TempDir()
	store, err := New(config.AppConfig{DownloadDir: root})
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	dir := filepath.Join(root, "a", "b")
	if err := store.MkdirAll(context.Background(), dir); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat() err = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("perm = %o, want 700", perm)
	}
}

// newRenameTestStore 返回一个以临时目录为根、并分别创建了如下布局的 store：
//
//	root/old/  (目录，含文件)
//	root/new/  (目标，含文件)
//	root/existing-file
//	root/target-file
func newRenameTestStore(t *testing.T) (Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := New(config.AppConfig{DownloadDir: root})
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	for _, dir := range []string{filepath.Join(root, "old"), filepath.Join(root, "new")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, file := range []string{filepath.Join(root, "existing-file"), filepath.Join(root, "target-file")} {
		if err := os.WriteFile(file, []byte(file), 0o600); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	return store, root
}

func TestLocalStoreRenameMovesDirectory(t *testing.T) {
	store, root := newRenameTestStore(t)
	oldPath := filepath.Join(root, "old")
	newPath := filepath.Join(root, "renamed")
	if err := os.WriteFile(filepath.Join(oldPath, "media.jpg"), []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Rename(context.Background(), oldPath, newPath); err != nil {
		t.Fatalf("Rename() err = %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new path missing: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old path still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newPath, "media.jpg")); err != nil {
		t.Fatalf("media lost after rename: %v", err)
	}
}

func TestLocalStoreRenameMissingSourceWrapsNotExist(t *testing.T) {
	store, root := newRenameTestStore(t)
	err := store.Rename(context.Background(), filepath.Join(root, "missing"), filepath.Join(root, "renamed"))
	if err == nil {
		t.Fatal("Rename() err = nil, want wrapped os.ErrNotExist")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Rename() err = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
	// 语义 2：jobs 用 errors.Is(err, os.ErrNotExist) 判断"源已不在，可继续 DB 重写"，
	// 因此这里不能是 ErrTargetExists 之类的目标冲突错误。
	if errors.Is(err, ErrTargetExists) || errors.Is(err, fs.ErrExist) {
		t.Fatalf("missing source misreported as target conflict: %v", err)
	}
}

func TestLocalStoreRenameExistingTargetIsConflict(t *testing.T) {
	store, root := newRenameTestStore(t)
	oldPath := filepath.Join(root, "existing-file")
	newPath := filepath.Join(root, "target-file")
	before, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Rename(context.Background(), oldPath, newPath)
	if err == nil {
		t.Fatal("Rename() err = nil, want target-exists conflict")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target conflict must not be classified as missing source: %v", err)
	}
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("Rename() err = %v, want errors.Is(err, ErrTargetExists)", err)
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Rename() err = %v, want errors.Is(err, fs.ErrExist)", err)
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		t.Fatalf("Rename() err = %v, want *os.LinkError", err)
	}
	// 语义 3：绝不覆盖目标，也绝不删除源（jobs 保留旧名并回滚）。
	if _, statErr := os.Stat(oldPath); statErr != nil {
		t.Fatalf("source was removed on conflict: %v", statErr)
	}
	after, readErr := os.ReadFile(newPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("target overwritten: got %q want %q (%v)", after, before, readErr)
	}
}

func TestLocalStoreRenameIdempotentWhenTargetIsSameFile(t *testing.T) {
	store, root := newRenameTestStore(t)
	source := filepath.Join(root, "source-file")
	target := filepath.Join(root, "target-alias")
	if err := os.WriteFile(source, []byte("same inode"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, target); err != nil {
		t.Skipf("hard links unsupported: %v", err)
	}
	// 语义 1 的幂等分支：目标与源是同一 inode 视为已改完，返回 nil 让 jobs 继续 DB 重写。
	if err := store.Rename(context.Background(), source, target); err != nil {
		t.Fatalf("Rename() err = %v, want nil for same-inode target", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source missing after idempotent rename: %v", err)
	}
}

func TestLocalStoreRenameRejectsMissingTargetParent(t *testing.T) {
	store, root := newRenameTestStore(t)
	oldPath := filepath.Join(root, "old")
	newPath := filepath.Join(root, "missing-parent", "new")
	err := store.Rename(context.Background(), oldPath, newPath)
	if err == nil {
		t.Fatal("Rename() err = nil, want target-parent error")
	}
	// 目标父目录不存在不能被归入"源缺失"语义（否则 jobs 会继续 DB 重写）。
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing target parent classified as missing source: %v", err)
	}
	if _, statErr := os.Stat(oldPath); statErr != nil {
		t.Fatalf("source removed on failed rename: %v", statErr)
	}
}

// 其他 I/O 错误（权限、只读目录、跨设备 EXDEV 等）必须原样包装返回：既不能被误判成
// "源缺失"（jobs 会继续 DB 重写），也不能被误判成"目标冲突"。跨设备重命名在单机测试里
// 无法可靠构造，这里用只读目标父目录覆盖同类错误传播路径。
func TestLocalStoreRenamePropagatesOtherIOErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are ineffective as root")
	}
	store, root := newRenameTestStore(t)
	readOnly := filepath.Join(root, "readonly")
	if err := os.MkdirAll(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnly, 0o700) }()
	oldPath := filepath.Join(root, "existing-file")
	err := store.Rename(context.Background(), oldPath, filepath.Join(readOnly, "moved"))
	if err == nil {
		t.Skip("filesystem allowed rename into a read-only directory")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("I/O error classified as missing source: %v", err)
	}
	if errors.Is(err, ErrTargetExists) || errors.Is(err, fs.ErrExist) {
		t.Fatalf("I/O error classified as target conflict: %v", err)
	}
	if _, statErr := os.Stat(oldPath); statErr != nil {
		t.Fatalf("source lost after failed rename: %v", statErr)
	}
}
