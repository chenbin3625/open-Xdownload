package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// TestGetMediaDownloadState 覆盖归档时每个媒体文件都要走一次的"跳过/重下"判定：
// 它同时回答"这条媒体是否已下载过"和"是否被标记为永久不可用"，判错会导致重复下载
// 或永久跳过本该下载的媒体。
func TestGetMediaDownloadState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	const (
		recordedURL    = "https://pbs.twimg.com/media/recorded.jpg"
		unavailableURL = "https://pbs.twimg.com/media/gone.jpg"
		freshURL       = "https://pbs.twimg.com/media/fresh.jpg"
		directURL      = "https://pbs.twimg.com/media/direct.jpg"
	)

	if _, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: "tweet-1", MediaURL: recordedURL,
		FilePath: "/tmp/recorded.jpg", Bytes: 111,
	}); err != nil {
		t.Fatalf("create download: %v", err)
	}
	// tweet_id 为空的直接媒体 URL 任务走另一个唯一索引，必须同样能被查到。
	if _, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, MediaURL: directURL, FilePath: "/tmp/direct.jpg", Bytes: 222,
	}); err != nil {
		t.Fatalf("create direct download: %v", err)
	}
	if err := store.UpsertUnavailableMedia(ctx, UnavailableMedia{
		MediaURL: unavailableURL, TweetID: "tweet-2", Error: "404",
	}); err != nil {
		t.Fatalf("upsert unavailable: %v", err)
	}

	t.Run("已下载", func(t *testing.T) {
		record, unavailable, err := store.GetMediaDownloadState(ctx, "tweet-1", recordedURL)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if unavailable {
			t.Fatal("unavailable = true, want false")
		}
		if record == nil {
			t.Fatal("record = nil, want the recorded download")
		}
		if record.FilePath != "/tmp/recorded.jpg" || record.Bytes != 111 || record.TweetID != "tweet-1" {
			t.Fatalf("record = %#v", record)
		}
	})

	t.Run("未下载", func(t *testing.T) {
		record, unavailable, err := store.GetMediaDownloadState(ctx, "tweet-9", freshURL)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if record != nil || unavailable {
			t.Fatalf("record = %#v, unavailable = %v; want nil, false", record, unavailable)
		}
	})

	t.Run("永久不可用", func(t *testing.T) {
		record, unavailable, err := store.GetMediaDownloadState(ctx, "tweet-2", unavailableURL)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if !unavailable {
			t.Fatal("unavailable = false, want true")
		}
		if record != nil {
			t.Fatalf("record = %#v, want nil", record)
		}
	})

	t.Run("永久不可用是全局的", func(t *testing.T) {
		// unavailable_media 只按 media_url 记录：同一媒体在另一条推文下也应判定为不可用。
		_, unavailable, err := store.GetMediaDownloadState(ctx, "tweet-other", unavailableURL)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if !unavailable {
			t.Fatal("unavailable = false, want true")
		}
	})

	t.Run("空 tweet_id 命中直接媒体记录", func(t *testing.T) {
		record, unavailable, err := store.GetMediaDownloadState(ctx, "", directURL)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if unavailable {
			t.Fatal("unavailable = true, want false")
		}
		if record == nil {
			t.Fatal("record = nil, want the direct-media download")
		}
		if record.Bytes != 222 || record.TweetID != "" {
			t.Fatalf("record = %#v", record)
		}
	})

	t.Run("空 tweet_id 不串到推文记录", func(t *testing.T) {
		// recordedURL 只以 tweet-1 存在，按空 tweet_id 查不应命中它。
		record, _, err := store.GetMediaDownloadState(ctx, "", recordedURL)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if record != nil {
			t.Fatalf("record = %#v, want nil", record)
		}
	})

	t.Run("推文 tweet_id 不串到直接媒体记录", func(t *testing.T) {
		record, _, err := store.GetMediaDownloadState(ctx, "tweet-1", directURL)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if record != nil {
			t.Fatalf("record = %#v, want nil", record)
		}
	})
}
