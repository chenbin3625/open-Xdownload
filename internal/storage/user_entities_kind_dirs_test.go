package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

// 旧版归档目录（<root>/users/<显示名(handle)>）升级后应改写为媒体存储的按类型
// 目录（<root>/image/<handle>、<root>/video/<handle>），并保留增量游标与统计。
func TestMigrateUserEntitiesToKindDirs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal, MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveStorageDocument(ctx, config.DefaultStorage(root)); err != nil {
		t.Fatal(err)
	}
	for _, user := range []User{
		{ID: "u1", ScreenName: "alice", Name: "Alice"},
		{ID: "u2", ScreenName: "bob", Name: "Bob"},
	} {
		if _, err := store.UpsertUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	oldParent := filepath.Join(root, "users")
	alice, err := store.EnsureUserEntity(ctx, "u1", oldParent, "Alice(alice)")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserEntityMediaCount(ctx, alice.ID, 42); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserEntityLastSeenTweet(ctx, alice.ID, "900"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u2", oldParent, "Bob(bob)"); err != nil {
		t.Fatal(err)
	}

	if err := store.migrateUserEntitiesToKindDirs(store.db); err != nil {
		t.Fatal(err)
	}

	imageParent := filepath.Join(root, "image")
	videoParent := filepath.Join(root, "video")
	imageEntity, err := store.LocateUserEntity(ctx, "u1", imageParent)
	if err != nil {
		t.Fatal(err)
	}
	if imageEntity == nil || imageEntity.Name != "alice" {
		t.Fatalf("image entity = %+v, want alice under %s", imageEntity, imageParent)
	}
	if !imageEntity.MediaCount.Valid || imageEntity.MediaCount.Int64 != 42 {
		t.Fatalf("media count = %+v, want 42", imageEntity.MediaCount)
	}
	if imageEntity.LastSeenTweetID != "900" {
		t.Fatalf("last seen tweet = %q, want 900", imageEntity.LastSeenTweetID)
	}
	videoEntity, err := store.LocateUserEntity(ctx, "u1", videoParent)
	if err != nil {
		t.Fatal(err)
	}
	if videoEntity == nil || videoEntity.Name != "alice" {
		t.Fatalf("video entity = %+v, want alice under %s", videoEntity, videoParent)
	}
	if old, err := store.LocateUserEntity(ctx, "u1", oldParent); err != nil {
		t.Fatal(err)
	} else if old != nil {
		t.Fatalf("legacy users entity still present: %+v", old)
	}
	bob, err := store.LocateUserEntity(ctx, "u2", imageParent)
	if err != nil {
		t.Fatal(err)
	}
	if bob == nil || bob.Name != "bob" {
		t.Fatalf("bob image entity = %+v", bob)
	}
}

// 目录模板不使用 {kind}（所有资源放同一目录）时迁移必须保持旧布局，避免把
// users/<...> 改写到与模板不对应的目录。
func TestMigrateUserEntitiesKeepsLayoutWhenTemplateHasNoKind(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal, MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	document := config.DefaultStorage(root)
	document.Default.DirectoryTemplate = "{username}"
	if _, err := store.SaveStorageDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	oldParent := filepath.Join(root, "users")
	if _, err := store.EnsureUserEntity(ctx, "u1", oldParent, "Alice(alice)"); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateUserEntitiesToKindDirs(store.db); err != nil {
		t.Fatal(err)
	}
	if entity, err := store.LocateUserEntity(ctx, "u1", oldParent); err != nil {
		t.Fatal(err)
	} else if entity == nil {
		t.Fatal("legacy entity was moved despite a template without {kind}")
	}
}
