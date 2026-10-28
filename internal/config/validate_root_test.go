package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDownloadRootRejectsSymlinkedHome(t *testing.T) {
	base := t.TempDir()
	realHome := filepath.Join(base, "real-home")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "home-link")
	if err := os.Symlink(realHome, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", oldHome) })

	// 未解析符号链接的写法与解析后的真实路径都必须被拒绝。
	for _, candidate := range []string{link, realHome} {
		err := ValidateDownloadRoot(candidate)
		if err == nil {
			t.Fatalf("ValidateDownloadRoot(%q) accepted symlinked home", candidate)
		}
		if !errors.Is(err, ErrInvalidDownloadRoot) {
			t.Fatalf("ValidateDownloadRoot(%q) err = %v, want ErrInvalidDownloadRoot", candidate, err)
		}
	}

	// 家目录下的子目录仍然合法。
	if err := ValidateDownloadRoot(filepath.Join(realHome, "downloads")); err != nil {
		t.Fatalf("subdirectory rejected: %v", err)
	}
}

func TestValidateDownloadRootRejectsFilesystemRoot(t *testing.T) {
	if err := ValidateDownloadRoot(string(filepath.Separator)); err == nil {
		t.Fatal("filesystem root accepted")
	}
}
