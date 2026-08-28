package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

var errFakePosterFetch = errors.New("fake poster fetch failure")

// stubPosterFetch 把 savePosterImage 替换为不触网的内存实现，返回记录调用地址的
// 切片指针。failFor 中的地址会让抓取失败。
func stubPosterFetch(t *testing.T, failFor map[string]bool) *[]string {
	t.Helper()
	fetched := &[]string{}
	original := savePosterImage
	savePosterImage = func(ctx context.Context, proxyURL string, rawURL string, destPath string) error {
		if failFor[rawURL] {
			return errFakePosterFetch
		}
		*fetched = append(*fetched, rawURL)
		return os.WriteFile(destPath, []byte("poster"), 0o600)
	}
	t.Cleanup(func() { savePosterImage = original })
	return fetched
}

func newPosterTestManager(t *testing.T) (*Manager, *storage.Store, config.AppConfig, filestore.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	root := t.TempDir()
	cfg := config.AppConfig{DownloadDir: root}
	target, err := filestore.New(cfg)
	if err != nil {
		t.Fatalf("init filestore: %v", err)
	}
	return NewManager(store, parser.NewService(), NewEventBus()), store, cfg, target
}

func seedVideoDownload(t *testing.T, store *storage.Store, tweetID string, mediaURL string, filePath string, previewURL string) storage.DownloadRecord {
	t.Helper()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, mediaURL, "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	record, err := store.CreateDownload(ctx, storage.DownloadRecord{
		JobID:      job.ID,
		TweetID:    tweetID,
		MediaURL:   mediaURL,
		PreviewURL: previewURL,
		FilePath:   filePath,
		Bytes:      10,
	})
	if err != nil {
		t.Fatalf("seed download: %v", err)
	}
	return record
}

const derivedPosterURL = "https://pbs.twimg.com/ext_tw_video_thumb/123/pu/img/abc.jpg?name=small"
const videoMediaURL = "https://video.twimg.com/ext_tw_video/123/pu/vid/avc1/1280x720/abc.mp4"
const parsedPosterURL = "https://pbs.twimg.com/ext_tw_video_thumb/123/pu/img/abc.jpg"

func TestEnsureVideoPosterFetchesAndBackfillsDerivedURL(t *testing.T) {
	manager, store, cfg, target := newPosterTestManager(t)
	fetched := stubPosterFetch(t, nil)
	record := seedVideoDownload(t, store, "tweet-1", videoMediaURL, filepath.Join(t.TempDir(), "abc.mp4"), "")

	gotFetched, gotSkipped := manager.ensureVideoPoster(context.Background(), cfg, target, &record, "")
	if !gotFetched || gotSkipped {
		t.Fatalf("fetched/skipped = %v/%v, want true/false", gotFetched, gotSkipped)
	}
	if len(*fetched) != 1 || (*fetched)[0] != derivedPosterURL {
		t.Fatalf("fetched URLs = %v, want [%s]", *fetched, derivedPosterURL)
	}
	updated, err := store.GetDownload(context.Background(), record.ID)
	if err != nil || updated == nil {
		t.Fatalf("get download: %v", err)
	}
	if updated.PreviewURL != derivedPosterURL {
		t.Fatalf("preview_url = %q, want derived %q", updated.PreviewURL, derivedPosterURL)
	}
}

func TestEnsureVideoPosterKeepsParsedPosterURL(t *testing.T) {
	manager, store, cfg, target := newPosterTestManager(t)
	fetched := stubPosterFetch(t, nil)
	record := seedVideoDownload(t, store, "tweet-1", videoMediaURL, filepath.Join(t.TempDir(), "abc.mp4"), parsedPosterURL)

	if _, skipped := manager.ensureVideoPoster(context.Background(), cfg, target, &record, ""); skipped {
		t.Fatal("skipped = true, want false (poster file missing)")
	}
	if len(*fetched) != 1 || (*fetched)[0] != parsedPosterURL {
		t.Fatalf("fetched URLs = %v, want parsed poster %s", *fetched, parsedPosterURL)
	}
	updated, err := store.GetDownload(context.Background(), record.ID)
	if err != nil || updated == nil {
		t.Fatalf("get download: %v", err)
	}
	if updated.PreviewURL != parsedPosterURL {
		t.Fatalf("preview_url = %q, want parsed poster kept %q", updated.PreviewURL, parsedPosterURL)
	}
}

func TestEnsureVideoPosterSkipsExistingPosterAndPhotos(t *testing.T) {
	manager, store, cfg, target := newPosterTestManager(t)
	fetched := stubPosterFetch(t, nil)
	ctx := context.Background()

	// 海报文件已存在的视频：直接跳过。
	record := seedVideoDownload(t, store, "tweet-1", videoMediaURL, filepath.Join(t.TempDir(), "abc.mp4"), "")
	if err := os.WriteFile(record.FilePath+".preview.jpg", []byte("poster"), 0o600); err != nil {
		t.Fatalf("seed poster: %v", err)
	}
	if gotFetched, _ := manager.ensureVideoPoster(context.Background(), cfg, target, &record, ""); gotFetched {
		t.Fatal("fetched = true, want false (poster file already exists)")
	}
	if len(*fetched) != 0 {
		t.Fatalf("fetched URLs = %v, want none", *fetched)
	}

	// 照片记录：预览就是文件本身，跳过。
	photo := seedVideoDownload(t, store, "tweet-2", "https://pbs.twimg.com/media/x.jpg", filepath.Join(t.TempDir(), "x.jpg"), "")
	if fetchedFlag, skipped := manager.ensureVideoPoster(ctx, cfg, target, &photo, ""); fetchedFlag || !skipped {
		t.Fatalf("photo: fetched/skipped = %v/%v, want false/true", fetchedFlag, skipped)
	}

	// 照片记录不应出现在回填扫描结果里。
	items, err := store.ListVideoDownloadsForPosterBackfill(ctx)
	if err != nil {
		t.Fatalf("list video downloads: %v", err)
	}
	if len(items) != 1 || items[0].ID != record.ID {
		t.Fatalf("scan items = %+v, want only the video record %d", items, record.ID)
	}
}

func TestEnsureVideoPosterFailureIsCountedNotSkipped(t *testing.T) {
	manager, store, cfg, target := newPosterTestManager(t)
	stubPosterFetch(t, map[string]bool{derivedPosterURL: true})
	record := seedVideoDownload(t, store, "tweet-1", videoMediaURL, filepath.Join(t.TempDir(), "abc.mp4"), "")

	fetched, skipped := manager.ensureVideoPoster(context.Background(), cfg, target, &record, "")
	if fetched || skipped {
		t.Fatalf("fetched/skipped = %v/%v, want false/false on fetch failure", fetched, skipped)
	}
}

func TestStartPosterBackfillRunsOnceAndCompletes(t *testing.T) {
	manager, store, _, _ := newPosterTestManager(t)
	release := make(chan struct{})
	original := savePosterImage
	savePosterImage = func(ctx context.Context, proxyURL string, rawURL string, destPath string) error {
		<-release
		return os.WriteFile(destPath, []byte("poster"), 0o600)
	}
	t.Cleanup(func() { savePosterImage = original })

	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		tweetID := fmt.Sprintf("tweet-%d", i)
		record := seedVideoDownload(t, store, tweetID, videoMediaURL, filepath.Join(t.TempDir(), tweetID+".mp4"), "")
		if err := os.WriteFile(record.FilePath+".preview.jpg", []byte("poster"), 0o600); err != nil {
			t.Fatalf("seed poster: %v", err)
		}
	}
	seedVideoDownload(t, store, "tweet-3", videoMediaURL+"?tag=12", filepath.Join(t.TempDir(), "abc3.mp4"), "")

	if _, err := manager.StartPosterBackfill(ctx); err != nil {
		t.Fatalf("start backfill: %v", err)
	}
	if _, err := manager.StartPosterBackfill(ctx); err == nil {
		t.Fatal("second start while running should be rejected")
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		status := manager.PosterBackfillStatus()
		if !status.Running {
			if status.Total != 3 || status.Done != 3 || status.Fetched != 1 || status.Skipped != 2 || status.Failed != 0 {
				t.Fatalf("final status = %+v, want total/done=3, fetched=1, skipped=2, failed=0", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backfill did not finish in time: %+v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
