package filestore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

func TestPutAssetFromTempZeroCopyWithinSameRoot(t *testing.T) {
	root := t.TempDir()
	target := config.StorageTarget{ID: "local", Type: "local", Root: root}
	tmpDir := filepath.Join(root, ".xdl-tmp", "asset-1")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(tmpDir, "source.mp4")
	if err := os.WriteFile(src, []byte("video-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}

	key, err := PutAssetFromTemp(context.Background(), target, "video/alice/t1.mp4", src, "video/mp4", "asset1")
	if err != nil {
		t.Fatalf("PutAssetFromTemp: %v", err)
	}
	if key != "video/alice/t1.mp4" {
		t.Fatalf("key = %q, want video/alice/t1.mp4", key)
	}
	dest := filepath.Join(root, filepath.FromSlash(key))
	destInfo, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(srcInfo, destInfo) {
		t.Fatal("expected PutAssetFromTemp within same root to zero-copy hardlink the inode")
	}

	// Regular PutAsset must still copy (not share inode with legacy source).
	legacy := filepath.Join(root, "legacy.jpg")
	if err := os.WriteFile(legacy, []byte("photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyInfo, _ := os.Stat(legacy)
	copiedKey, err := PutAsset(context.Background(), target, "image/alice/p1.jpg", legacy, "image/jpeg", "asset2")
	if err != nil {
		t.Fatalf("PutAsset: %v", err)
	}
	copiedInfo, _ := os.Stat(filepath.Join(root, filepath.FromSlash(copiedKey)))
	if os.SameFile(legacyInfo, copiedInfo) {
		t.Fatal("PutAsset for legacy source must copy instead of sharing inode")
	}
}
