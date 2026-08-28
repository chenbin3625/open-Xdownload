package filestore

import (
	"context"
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

func TestLocalStoreTestWritable(t *testing.T) {
	root := t.TempDir()
	store, err := New(config.AppConfig{DownloadDir: root})
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	path, err := store.TestWritable(context.Background())
	if err != nil {
		t.Fatalf("TestWritable() err = %v", err)
	}
	if filepath.Dir(path) != root {
		t.Fatalf("path = %q, want under %q", path, root)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("probe file not cleaned up: %v", err)
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
