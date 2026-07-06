package downloader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilenameFromURLHonorsMaxLengthAndExtension(t *testing.T) {
	got := filenameFromURL("https://video.twimg.com/ext_tw_video/movie.mp4", strings.Repeat("a", 60), "", 24)
	if len(got) > 24 {
		t.Fatalf("filename length = %d, want <= 24: %q", len(got), got)
	}
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestFilenameFromURLKeepsMediaExtensionWhenHintHasNonMediaExtension(t *testing.T) {
	got := filenameFromURL(
		"https://video.twimg.com/amplify_video/2073131761341460480/vid/avc1/720x1280/gAEB-Ux6zk1_4Z-t.mp4?tag=14",
		"正文里可能带短链 https://t.co/example",
		"",
		180,
	)
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestFilenameFromURLUsesContentTypeWhenURLHasNoExtension(t *testing.T) {
	got := filenameFromURL("https://pbs.twimg.com/media/example?format=small", "photo", "video/mp4; charset=utf-8", 180)
	if filepath.Ext(got) != ".mp4" {
		t.Fatalf("extension = %q, want .mp4: %q", filepath.Ext(got), got)
	}
}

func TestUniquePathKeepsConflictSuffixWithinMaxLength(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, filenameFromURL("https://example.com/media.jpg", strings.Repeat("a", 60), "", 24))
	if err := os.WriteFile(first, []byte("exists"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	got, err := uniquePath(first, 24)
	if err != nil {
		t.Fatalf("unique path: %v", err)
	}
	base := filepath.Base(got)
	if len(base) > 24 {
		t.Fatalf("filename length = %d, want <= 24: %q", len(base), base)
	}
	if !strings.Contains(base, "(1)") {
		t.Fatalf("filename %q does not contain conflict suffix", base)
	}
	if filepath.Ext(base) != ".jpg" {
		t.Fatalf("extension = %q, want .jpg: %q", filepath.Ext(base), base)
	}
}
