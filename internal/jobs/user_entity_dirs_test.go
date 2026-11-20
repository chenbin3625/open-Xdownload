package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

// 启用媒体存储策略时，归档用户实体目录应跟随 {kind}/{username} 布局，落在
// <root>/image/<handle> 与 <root>/video/<handle>，不再创建额外的 users/ 层级。
func TestEnsureUserEntityUsesStorageKindDirs(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "dirs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	cfg, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, storage.User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	target, err := filestore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	entity, dir, err := manager.ensureUserEntityWithStore(ctx, target, xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(root, "image", "alice")
	if filepath.Clean(dir) != wantDir {
		t.Fatalf("primary dir = %q, want %q", dir, wantDir)
	}
	if entity.ParentDir != filepath.Join(root, "image") || entity.Name != "alice" {
		t.Fatalf("primary entity = %+v", entity)
	}
	for _, kind := range config.AssetKinds {
		parent := filepath.Join(root, string(kind))
		got, err := store.LocateUserEntity(ctx, "u1", parent)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Name != "alice" {
			t.Fatalf("%s entity = %+v", kind, got)
		}
		if _, err := os.Stat(filepath.Join(parent, "alice")); err != nil {
			t.Fatalf("%s dir was not created: %v", kind, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "users")); !os.IsNotExist(err) {
		t.Fatalf("legacy users dir should not be created, stat err = %v", err)
	}
}

// 关闭媒体存储（或目录模板无法只按用户确定）时保持旧的 users/ 布局。
func TestEnsureUserEntityFallsBackToUsersDirWithoutStoragePolicy(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "dirs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	cfg, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root})
	if err != nil {
		t.Fatal(err)
	}
	useLegacyStorage(t, store)
	if _, err := store.UpsertUser(ctx, storage.User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	target, err := filestore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, dir, err := manager.ensureUserEntityWithStore(ctx, target, xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(root, "users", "Alice(alice)")
	if filepath.Clean(dir) != wantDir {
		t.Fatalf("dir = %q, want %q", dir, wantDir)
	}
}
