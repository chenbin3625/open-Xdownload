package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/minio/minio-go/v7"
)

func TestStableAssetAuthenticationRangeAndHistoricalVersion(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	document := config.DefaultStorage(t.TempDir())
	document.Enabled = true
	document, err = store.SaveStorageDocument(ctx, document)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("0123456789video")
	if err := os.WriteFile(filepath.Join(document.Targets[0].Root, "video.mp4"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	asset, err := store.CreateAsset(ctx, storage.Asset{ID: "stable-id", Kind: config.AssetVideo, PolicyVersion: document.Version, TargetID: "local", TargetVersion: document.Targets[0].Version, Key: "video.mp4", Size: int64(len(data)), ContentType: "video/mp4", ContentHash: hex.EncodeToString(hash[:])})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, nil, nil, nil)
	handler := authenticatedRoutes(t, server)
	request := newLoopbackRequest(http.MethodGet, "/api/assets/"+asset.ID, nil)
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 206 || response.Body.String() != "2345" || response.Header().Get("Content-Range") != "bytes 2-5/15" {
		t.Fatalf("Range: %d %q %+v", response.Code, response.Body.String(), response.Header())
	}
	draft := document.Redacted()
	draft.Targets[0].Root = t.TempDir()
	if _, err := store.SaveStorageDocument(ctx, draft); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, newLoopbackRequest(http.MethodGet, "/api/assets/"+asset.ID, nil))
	if response.Code != 200 || response.Body.String() != string(data) {
		t.Fatalf("historical policy: %d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.Routes().ServeHTTP(response, newLoopbackRequest(http.MethodGet, "/api/assets/"+asset.ID, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated asset: %d", response.Code)
	}
}

func TestDownloadEndpointsFallBackFromUnavailableAssets(t *testing.T) {
	for _, state := range []string{"missing", "wrong-size"} {
		t.Run(state, func(t *testing.T) {
			store, handler, root := newLibraryTestServer(t)
			ctx := context.Background()
			legacyPath := filepath.Join(root, "legacy.mp4")
			if err := os.WriteFile(legacyPath, []byte("legacy video"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacyPath+".preview.jpg", []byte("legacy poster"), 0o600); err != nil {
				t.Fatal(err)
			}
			document := config.DefaultStorage(t.TempDir())
			document.Enabled = true
			document, err := store.SaveStorageDocument(ctx, document)
			if err != nil {
				t.Fatal(err)
			}
			if state == "wrong-size" {
				if err := os.WriteFile(filepath.Join(document.Targets[0].Root, "stale.mp4"), []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			job, err := store.CreateJob(ctx, storage.JobKindMediaURL, "legacy", "legacy")
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: job.ID, FilePath: legacyPath, MediaURL: "https://video.twimg.com/legacy.mp4"})
			if err != nil {
				t.Fatal(err)
			}
			asset, err := store.CreateAsset(ctx, storage.Asset{ID: "stale", Kind: config.AssetVideo, PolicyVersion: document.Version, TargetID: "local", TargetVersion: document.Targets[0].Version, Key: "stale.mp4", Size: 999, ContentType: "video/mp4", ContentHash: "old"})
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []config.AssetKind{config.AssetVideo} {
				if err := store.LinkDownloadAsset(ctx, record.ID, kind, asset.ID); err != nil {
					t.Fatal(err)
				}
			}
			for endpoint, body := range map[string]string{"file": "legacy video", "preview": "legacy poster"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, newLoopbackRequest(http.MethodGet, fmt.Sprintf("/api/library/downloads/%d/%s", record.ID, endpoint), nil))
				if response.Code != http.StatusOK || response.Body.String() != body {
					t.Fatalf("%s fallback: %d %q", endpoint, response.Code, response.Body.String())
				}
			}
			records := []storage.DownloadRecord{record}
			NewServer(store, nil, nil, nil).enrichDownloads(newLoopbackRequest(http.MethodGet, "/", nil), records)
			if records[0].FileURL != fmt.Sprintf("/api/library/downloads/%d/file", record.ID) || records[0].AssetURLs[config.AssetVideo] != fmt.Sprintf("/api/library/downloads/%d/file", record.ID) {
				t.Fatalf("download URLs bypass fallback: %+v", records[0])
			}
		})
	}
}

func TestImagePreviewFallsBackToRetainedLegacyOriginal(t *testing.T) {
	store, handler, root := newLibraryTestServer(t)
	ctx := context.Background()
	filePath := filepath.Join(root, "legacy.jpg")
	if err := os.WriteFile(filePath, []byte("retained image"), 0o600); err != nil {
		t.Fatal(err)
	}
	document := config.DefaultStorage(t.TempDir())
	document, err := store.SaveStorageDocument(ctx, document)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.CreateAsset(ctx, storage.Asset{ID: "missing-image", Kind: config.AssetImage, PolicyVersion: document.Version, TargetID: "local", TargetVersion: document.Targets[0].Version, Key: "missing.jpg", Size: 10, ContentType: "image/jpeg", ContentHash: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, "image", "image")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: job.ID, FilePath: filePath, MediaURL: "https://pbs.twimg.com/legacy.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkDownloadAsset(ctx, record.ID, config.AssetImage, asset.ID); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newLoopbackRequest(http.MethodGet, fmt.Sprintf("/api/library/downloads/%d/preview", record.ID), nil))
	if response.Code != http.StatusOK || response.Body.String() != "retained image" {
		t.Fatalf("image preview fallback: %d %q", response.Code, response.Body.String())
	}
}

func TestTweetMediaRetryRejectsDuplicateActiveJobs(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	original, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindTweetLink, Input: "https://x.com/author/status/123", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveTweet(ctx, original.ID, parser.TweetData{ID: "123", URL: original.Input, Text: "body", Media: []parser.Media{{ID: "p", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/p.jpg"}}}, "pending"); err != nil {
		t.Fatal(err)
	}
	handler := authenticatedRoutes(t, NewServer(store, nil, jobs.NewManager(store, nil, nil), nil))
	for index, want := range []int{http.StatusConflict, http.StatusCreated, http.StatusConflict} {
		if index == 1 {
			if _, err := store.CancelJob(ctx, original.ID); err != nil {
				t.Fatal(err)
			}
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, newLoopbackRequest(http.MethodPost, "/api/archive/tweets/123/retry-media", nil))
		if response.Code != want {
			t.Fatalf("retry %d: %d %s, want %d", index, response.Code, response.Body.String(), want)
		}
	}
}

func TestS3AssetSupportsAuthenticatedHTTPRange(t *testing.T) {
	endpoint := os.Getenv("OPEN_XDOWNLOAD_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3 integration endpoint not configured")
	}
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "s3.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target := config.StorageTarget{ID: "s3", Type: "s3", Endpoint: endpoint, Region: "us-east-1", Bucket: "archive-integration", AccessKey: os.Getenv("OPEN_XDOWNLOAD_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("OPEN_XDOWNLOAD_TEST_S3_SECRET_KEY")}
	client, err := filestore.S3Client(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.MakeBucket(ctx, target.Bucket, minio.MakeBucketOptions{Region: target.Region}); err != nil {
		if exists, _ := client.BucketExists(ctx, target.Bucket); !exists {
			t.Fatal(err)
		}
	}
	document := config.DefaultStorage(t.TempDir())
	document.Enabled = true
	document.Targets = append(document.Targets, target)
	document, err = store.SaveStorageDocument(ctx, document)
	if err != nil {
		t.Fatal(err)
	}
	target = document.Targets[1]
	source := filepath.Join(t.TempDir(), "video.mp4")
	data := []byte("0123456789video")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := filestore.PutAsset(ctx, target, "range/"+filepath.Base(t.TempDir())+".mp4", source, "video/mp4", "http-range")
	if err != nil {
		t.Fatal(err)
	}
	defer client.RemoveObject(ctx, target.Bucket, key, minio.RemoveObjectOptions{})
	hash := sha256.Sum256(data)
	asset, err := store.CreateAsset(ctx, storage.Asset{ID: "s3-range", Kind: config.AssetVideo, PolicyVersion: document.Version, TargetID: target.ID, TargetVersion: target.Version, Key: key, Size: int64(len(data)), ContentType: "video/mp4", ContentHash: hex.EncodeToString(hash[:])})
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticatedRoutes(t, NewServer(store, nil, nil, nil))
	request := newLoopbackRequest(http.MethodGet, "/api/assets/"+asset.ID, nil)
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "2345" || response.Header().Get("Content-Range") != "bytes 2-5/15" {
		t.Fatalf("S3 HTTP Range: %d %q %+v", response.Code, response.Body.String(), response.Header())
	}
}
