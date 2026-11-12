package filestore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/minio/minio-go/v7"
)

func TestAssetFilenameCompositionPreservesOrderAndExtensionWithinSystemLimit(t *testing.T) {
	tweet := parser.TweetData{ID: "2120206875581186293", Text: strings.Repeat("中文😀正文", 100)}
	media := parser.Media{ID: "1920206875581186293"}
	for _, template := range []string{"{text}.{ext}", "{tweet_id}.{ext}", "{media_id}.{ext}", "{text}_{tweet_id}_{media_id}.{ext}", "{media_id}_{text}_{tweet_id}.{ext}"} {
		t.Run(template, func(t *testing.T) {
			key, err := TemplatePath(config.AssetRule{FilenameTemplate: template}, config.AssetImage, tweet, media, 0, "jpg")
			if err != nil || len(key) > 240 || !utf8.ValidString(key) || !strings.HasSuffix(key, ".jpg") {
				t.Fatalf("invalid filename (%d bytes): %q, %v", len(key), key, err)
			}
			if strings.Contains(template, "{tweet_id}") && !strings.Contains(key, tweet.ID) {
				t.Fatalf("tweet ID lost: %q", key)
			}
			if strings.HasPrefix(template, "{media_id}") && !strings.HasPrefix(key, media.ID) {
				t.Fatalf("field order lost: %q", key)
			}
			if template == "{text}.{ext}" && len(key) < 230 {
				t.Fatalf("text was truncated before the filename budget was used: %q", key)
			}
		})
	}
}

func TestAssetLongLiteralKeepsExtension(t *testing.T) {
	key, err := TemplatePath(config.AssetRule{FilenameTemplate: strings.Repeat("a", 300) + ".{ext}"}, config.AssetImage, parser.TweetData{}, parser.Media{}, 0, "jpg")
	if err != nil || len(key) > 240 || !strings.HasSuffix(key, ".jpg") {
		t.Fatalf("long filename lost extension: %q, %v", key, err)
	}
}

func TestAssetSeparateFolderByKind(t *testing.T) {
	on := true
	rule := config.AssetRule{DirectoryTemplate: "{username}", FilenameTemplate: "{media_id}.{ext}", SeparateFolder: &on}
	for _, kind := range config.AssetKinds {
		key, err := TemplatePath(rule, kind, parser.TweetData{Author: parser.Author{ScreenName: "author"}}, parser.Media{ID: "media"}, 0, "jpg")
		if err != nil || key != "author/"+string(kind)+"/media.jpg" {
			t.Fatalf("kind %s: %q, %v", kind, key, err)
		}
	}
}

func TestAssetTemplatesConfinementAndCollisions(t *testing.T) {
	rule := config.AssetRule{Prefix: "archive", DirectoryTemplate: "{username}/{year}/{month}", FilenameTemplate: "{author_id}_{tweet_id}_{media_id}_{index}_{text}.{ext}"}
	tweet := parser.TweetData{ID: "123", Author: parser.Author{ID: "42", ScreenName: "../NUL/author"}, Text: strings.Repeat("文本/../<>:\x00", 100), CreatedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	key, err := TemplatePath(rule, config.AssetImage, tweet, parser.Media{ID: "../p1"}, 2, "jpg")
	if err != nil || !strings.Contains(key, "/2026/11/") || !strings.Contains(key, "42_123__p1_3_") || !strings.HasSuffix(key, ".jpg") || len(filepath.Base(key)) > config.MaxFilenameLength {
		t.Fatalf("render: %q %v", key, err)
	}
	for _, invalid := range []string{"../outside", "/absolute", "a/../../b", `a\b`} {
		bad := rule
		bad.DirectoryTemplate = invalid
		if _, err := TemplatePath(bad, config.AssetImage, tweet, parser.Media{}, 0, "jpg"); err == nil {
			t.Errorf("accepted template %q", invalid)
		}
	}
	root := t.TempDir()
	target := config.StorageTarget{ID: "local", Type: "local", Root: root}
	source := filepath.Join(t.TempDir(), "source.jpg")
	if err := os.WriteFile(source, []byte("original bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := PutAsset(ctx, target, "same/name.jpg", source, "image/jpeg", "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PutAsset(ctx, target, "same/name.jpg", source, "image/jpeg", "two")
	if err != nil || first == second {
		t.Fatalf("collision overwrote original: %q %q %v", first, second, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := PutAsset(ctx, target, "escape/file.jpg", source, "image/jpeg", "three"); err == nil {
		t.Fatal("symlink write escaped root")
	}
	if _, _, err := OpenAsset(ctx, target, "../source.jpg"); err == nil {
		t.Fatal("traversal read accepted")
	}
	file, size, err := OpenAsset(ctx, target, first)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Seek(9, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil || size != 14 || string(data) != "bytes" {
		t.Fatalf("seek/read: %q %d %v", data, size, err)
	}
}

func TestS3AssetConditionalWriteAndSeek(t *testing.T) {
	endpoint := os.Getenv("OPEN_XDOWNLOAD_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3 integration endpoint not configured")
	}
	target := config.StorageTarget{ID: "s3", Type: "s3", Endpoint: endpoint, Region: "us-east-1", Bucket: "archive-integration", AccessKey: os.Getenv("OPEN_XDOWNLOAD_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("OPEN_XDOWNLOAD_TEST_S3_SECRET_KEY")}
	ctx := context.Background()
	client, err := S3Client(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.MakeBucket(ctx, target.Bucket, minio.MakeBucketOptions{Region: target.Region}); err != nil {
		if exists, _ := client.BucketExists(ctx, target.Bucket); !exists {
			t.Fatal(err)
		}
	}
	source := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(source, []byte("0123456789-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := "range/" + filepath.Base(t.TempDir()) + "/video.mp4"
	first, err := PutAsset(ctx, target, key, source, "video/mp4", "one")
	if err != nil {
		t.Fatal(err)
	}
	defer client.RemoveObject(ctx, target.Bucket, first, minio.RemoveObjectOptions{})
	second, err := PutAsset(ctx, target, key, source, "video/mp4", "two")
	if err != nil || second == first {
		t.Fatalf("conditional write: %q %q %v", first, second, err)
	}
	defer client.RemoveObject(ctx, target.Bucket, second, minio.RemoveObjectOptions{})
	file, size, err := OpenAsset(ctx, target, first)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Seek(10, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil || size != 16 || string(data) != "-video" {
		t.Fatalf("S3 seek: %q %d %v", data, size, err)
	}
}
