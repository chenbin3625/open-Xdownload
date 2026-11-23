package filestore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	for _, directory := range []string{"{username}", "{kind}/{username}"} {
		for _, prefix := range []string{"", "archive"} {
			rule := config.AssetRule{Prefix: prefix, DirectoryTemplate: directory, FilenameTemplate: "{media_id}.{ext}", SeparateFolder: &on}
			for _, kind := range config.AssetKinds {
				key, err := TemplatePath(rule, kind, parser.TweetData{Author: parser.Author{ScreenName: "author"}}, parser.Media{ID: "media"}, 0, "jpg")
				want := string(kind) + "/author/media.jpg"
				if prefix != "" {
					want = prefix + "/" + want
				}
				if err != nil || key != want {
					t.Fatalf("prefix %q, directory %q, kind %s: %q, %v; want %q", prefix, directory, kind, key, err, want)
				}
			}
		}
	}
}

func TestDefaultAssetPathsGroupMediaTypesBeforeUsers(t *testing.T) {
	document := config.DefaultStorage(t.TempDir())
	for _, username := range []string{"alice", "bob"} {
		for _, kind := range config.AssetKinds {
			rule, _, err := document.Rule(kind)
			if err != nil {
				t.Fatal(err)
			}
			ext := "jpg"
			if kind == config.AssetVideo {
				ext = "mp4"
			}
			key, err := TemplatePath(rule, kind, parser.TweetData{ID: "123", Author: parser.Author{ScreenName: username}}, parser.Media{ID: "media"}, 0, ext)
			want := string(kind) + "/" + username + "/123_media_1." + ext
			if err != nil || key != want {
				t.Fatalf("kind %s, user %s: %q, %v; want %q", kind, username, key, err, want)
			}
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

func TestOpenAssetValidatesRootAndAbs(t *testing.T) {
	ctx := context.Background()
	if _, _, err := OpenAsset(ctx, config.StorageTarget{Type: "local", Root: "relative/dir"}, "a.jpg"); err == nil {
		t.Fatal("expected error for relative root")
	}
	if _, _, err := OpenAsset(ctx, config.StorageTarget{Type: "local", Root: "/"}, "a.jpg"); err == nil {
		t.Fatal("expected error for filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if _, _, err := OpenAsset(ctx, config.StorageTarget{Type: "local", Root: home}, "a.jpg"); err == nil {
			t.Fatal("expected error for home directory root")
		}
	}
}

func TestS3ClientValidationAndLRUCache(t *testing.T) {
	invalidTargets := []config.StorageTarget{
		{Type: "s3", Endpoint: "ftp://s3.example.com"},
		{Type: "s3", Endpoint: "https://s3.example.com?foo=bar"},
		{Type: "s3", Endpoint: "https://s3.example.com#frag"},
		{Type: "s3", Endpoint: "https://s3.example.com/subpath"},
		{Type: "s3", Endpoint: "http://169.254.169.254:9000"},
		{Type: "s3", Endpoint: "http://[fe80::1]:9000"},
		{Type: "s3", Endpoint: "http://[fd00:ec2::254]:9000"},
		{Type: "s3", Endpoint: "http://100.100.100.200:9000"},
		{Type: "s3", Endpoint: "http://[::ffff:100.100.100.200]:9000"},
		{Type: "s3", Endpoint: "http://0.0.0.0:9000"},
		{Type: "s3", Endpoint: "http://[::]:9000"},
		{Type: "s3", Endpoint: "http://0xa9fea9fe:9000"},
		{Type: "s3", Endpoint: "http://0251.0376.0251.0376:9000"},
		{Type: "s3", Endpoint: "http://2852039166:9000"},
		{Type: "s3", Endpoint: "http://0x64.0x64.0x64.0xc8:9000"},
	}
	for _, target := range invalidTargets {
		if _, err := S3Client(target); err == nil {
			t.Errorf("S3Client(%q) = nil error, want rejection", target.Endpoint)
		}
	}

	s3Clients.Lock()
	s3Clients.byKey = map[s3ClientKey]*s3ClientEntry{}
	s3Clients.seq = 0
	s3Clients.Unlock()

	for i := 0; i < maxS3Clients+5; i++ {
		target := config.StorageTarget{
			Type:      "s3",
			Endpoint:  "https://s3.example.com",
			Region:    "us-east-1",
			AccessKey: fmt.Sprintf("ak-%d", i),
			SecretKey: fmt.Sprintf("sk-%d", i),
		}
		if _, err := S3Client(target); err != nil {
			t.Fatalf("S3Client(%d) err = %v", i, err)
		}
	}
	s3Clients.Lock()
	cacheLen := len(s3Clients.byKey)
	s3Clients.Unlock()
	if cacheLen > maxS3Clients {
		t.Fatalf("S3Client cache size = %d, want <= %d", cacheLen, maxS3Clients)
	}
}

func TestS3PutAssetRetries409AndReturnsEmptyKeyOnError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			attempts++
			if attempts == 1 {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`<Error><Code>ConditionalRequestConflict</Code></Error>`))
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<Error><Code>InternalError</Code></Error>`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	source := filepath.Join(t.TempDir(), "src.jpg")
	if err := os.WriteFile(source, []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := config.StorageTarget{
		Type:      "s3",
		Endpoint:  server.URL,
		Region:    "us-east-1",
		Bucket:    "bkt",
		AccessKey: "ak",
		SecretKey: "sk",
	}
	gotKey, err := PutAsset(context.Background(), target, "dir/file.jpg", source, "image/jpeg", "asset-1")
	if err == nil {
		t.Fatal("expected S3 error on 500")
	}
	if gotKey != "" {
		t.Fatalf("PutAsset returned non-empty key %q on error", gotKey)
	}
	if attempts < 2 {
		t.Fatalf("expected 409 ConditionalRequestConflict retry, attempts = %d", attempts)
	}
}

func TestPutAssetSanitizesAssetIDNonceAndRejectsNonRegular(t *testing.T) {
	root := t.TempDir()
	target := config.StorageTarget{ID: "local", Type: "local", Root: root}
	ctx := context.Background()

	// Non-regular source (directory) must be rejected.
	dirSrc := t.TempDir()
	if _, err := PutAsset(ctx, target, "dir/file.jpg", dirSrc, "image/jpeg", "id1"); err == nil {
		t.Fatal("expected non-regular source rejection")
	}

	source := filepath.Join(t.TempDir(), "src.jpg")
	if err := os.WriteFile(source, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := PutAsset(ctx, target, "sub/item.jpg", source, "image/jpeg", "../bad/id")
	if err != nil || first != "sub/item.jpg" {
		t.Fatalf("first PutAsset = %q, %v", first, err)
	}
	// Seed a stale .part file; nonce ensures PutAsset doesn't fail with EEXIST.
	if err := os.WriteFile(filepath.Join(root, "sub", ".asset-__bad_id.part"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := PutAsset(ctx, target, "sub/item.jpg", source, "image/jpeg", "../bad/id")
	if err != nil {
		t.Fatalf("second PutAsset with traversal assetID failed: %v", err)
	}
	if !strings.HasPrefix(second, "sub/item_") || !validAssetKey(second) {
		t.Fatalf("unexpected collision key %q", second)
	}
}

func TestPutAssetMultiCollisionTopLevelAndTempCleanup(t *testing.T) {
	root := t.TempDir()
	target := config.StorageTarget{ID: "local", Type: "local", Root: root}
	ctx := context.Background()

	source := filepath.Join(t.TempDir(), "src.jpg")
	if err := os.WriteFile(source, []byte("payload-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Top-level key (parentDir == ".") must work cleanly.
	topKey, err := PutAsset(ctx, target, "top.jpg", source, "image/jpeg", "a1")
	if err != nil || topKey != "top.jpg" {
		t.Fatalf("top-level PutAsset = %q, %v", topKey, err)
	}

	// Pre-populate 3 colliding candidates; PutAsset writes temp once and publishes at attempt 3.
	subDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		cand := assetCollisionCandidate("sub/item.jpg", "id1", i)
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(cand)), []byte(fmt.Sprintf("existing-%d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := PutAsset(ctx, target, "sub/item.jpg", source, "image/jpeg", "id1")
	if err != nil {
		t.Fatalf("multi-collision PutAsset err = %v", err)
	}
	if want := assetCollisionCandidate("sub/item.jpg", "id1", 3); got != want {
		t.Fatalf("multi-collision key = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(got))); err != nil || string(data) != "payload-bytes" {
		t.Fatalf("published asset content = %q, %v", data, err)
	}
	if leftover, _ := filepath.Glob(filepath.Join(subDir, ".asset-*.part")); len(leftover) != 0 {
		t.Fatalf("leftover temp files after success: %v", leftover)
	}

	// Exhaust all 10 candidates: must fail and clean up the single temp file.
	for i := 4; i < 10; i++ {
		cand := assetCollisionCandidate("sub/item.jpg", "id1", i)
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(cand)), []byte("busy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PutAsset(ctx, target, "sub/item.jpg", source, "image/jpeg", "id1"); err == nil {
		t.Fatal("expected collision exhaustion error")
	}
	if leftover, _ := filepath.Glob(filepath.Join(subDir, ".asset-*.part")); len(leftover) != 0 {
		t.Fatalf("leftover temp files after exhaustion: %v", leftover)
	}
}

func TestTemplatePathAndSegmentEdgeCases(t *testing.T) {
	if validAssetKey(".") {
		t.Fatal(`validAssetKey(".") = true, want false`)
	}
	if validAssetKey("a/b\x00c") {
		t.Fatal("validAssetKey with NUL = true, want false")
	}
	if got := cleanAssetSegment("CONIN$"); got != "_CONIN$" {
		t.Fatalf("cleanAssetSegment(CONIN$) = %q", got)
	}
	if got := cleanAssetSegment("CONOUT$.txt"); got != "_CONOUT$.txt" {
		t.Fatalf("cleanAssetSegment(CONOUT$.txt) = %q", got)
	}
	if got := cleanAssetSegment("CON .jpg"); got != "_CON .jpg" {
		t.Fatalf("cleanAssetSegment('CON .jpg') = %q", got)
	}

	// Leading dots mixed with Unicode whitespace / format controls must never yield hidden .preview-* names.
	for _, raw := range []string{
		". .preview-123",
		"\u00a0.preview-123",
		"\u3000.\u200b.preview-123",
		"\u200d.preview-123",
		"\ufeff\u202e.xdl-123",
	} {
		seg := cleanAssetSegment(raw)
		if strings.HasPrefix(seg, ".") {
			t.Fatalf("cleanAssetSegment(%q) = %q starts with dot", raw, seg)
		}
		key, err := TemplatePath(config.AssetRule{FilenameTemplate: "{text}.{ext}"}, config.AssetImage, parser.TweetData{Text: raw}, parser.Media{}, 0, "jpg")
		if err != nil || strings.HasPrefix(filepath.Base(key), ".") {
			t.Fatalf("TemplatePath(%q) = %q, %v; must not start with dot", raw, key, err)
		}
	}

	key, err := TemplatePath(config.AssetRule{FilenameTemplate: "{tweet_id}.{ext}"}, config.AssetImage, parser.TweetData{ID: "123"}, parser.Media{}, 0, "")
	if err != nil || key != "123" {
		t.Fatalf("empty ext TemplatePath = %q, %v; want '123'", key, err)
	}
	longDotted := "v1." + strings.Repeat("x", 300)
	key2, err := TemplatePath(config.AssetRule{FilenameTemplate: strings.Repeat("p", 200) + "{text}_{ext}"}, config.AssetImage, parser.TweetData{Text: longDotted}, parser.Media{}, 0, "jpg")
	if err != nil || !strings.HasSuffix(key2, "_jpg") || len(key2) > config.MaxFilenameLength {
		t.Fatalf("{text}_{ext} truncation failed: %q, %v", key2, err)
	}
}
