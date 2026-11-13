package jobs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

func fullReviewTimelinePayload(id, cursor string) string {
	entries := fmt.Sprintf(`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":%q,"legacy":{"full_text":"body"},"core":{"user_results":{"result":{"rest_id":"42","core":{"screen_name":"author"}}}}}}}}}`, id)
	if cursor != "" {
		entries += fmt.Sprintf(`,{"content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":%q}}`, cursor)
	}
	return fmt.Sprintf(`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`, entries)
}

func TestFullTimelineYieldsAfterSavingPage(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	cursors := make(chan string, 4)
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		cursor := timelineCursor(t, r)
		cursors <- cursor
		if cursor == "" {
			fmt.Fprint(w, fullReviewTimelinePayload("105", "page-2"))
			return
		}
		fmt.Fprint(w, fullReviewTimelinePayload("104", ""))
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	user := xclient.User{ID: "42", ScreenName: "author"}
	_, err = manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, user, nil, nil, func() bool { return true })
	if !errors.Is(err, errYieldExecution) {
		t.Fatalf("page boundary did not yield: %v", err)
	}
	state, err := store.GetTweetSync(ctx, user.ID)
	if err != nil || state.Cursor != "page-2" || state.Status != "interrupted" || state.Count != 1 || len(cursors) != 1 {
		t.Fatalf("saved page = %+v, %v, requests=%d", state, err, len(cursors))
	}
	if cursor := <-cursors; cursor != "" {
		t.Fatalf("first cursor = %q", cursor)
	}
	_, err = manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, user, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.GetTweetSync(ctx, user.ID)
	if err != nil || state.LastID != "105" || state.Count != 2 || len(cursors) != 1 {
		t.Fatalf("resumed page = %+v, %v, requests=%d", state, err, len(cursors))
	}
	if cursor := <-cursors; cursor != "page-2" {
		t.Fatalf("resumed cursor = %q", cursor)
	}
}

func TestFullTimelineRecoversExpiredSavedCursor(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	cursors := make(chan string, 4)
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		cursor := timelineCursor(t, r)
		cursors <- cursor
		if cursor != "" {
			http.Error(w, "Invalid cursor", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, fullReviewTimelinePayload("105", ""))
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTweetSync(ctx, "42"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTweetSync(ctx, storage.TweetSync{UserID: "42", LastID: "100", HeadID: "110", Cursor: "expired", Count: 4, Status: "interrupted"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	_, err = manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, xclient.User{ID: "42", ScreenName: "author"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("saved cursor never recovered: %v", err)
	}
	state, err := store.GetTweetSync(ctx, "42")
	if err != nil || state.LastID != "105" || state.Count != 1 || state.Status != "completed" || len(cursors) != 2 {
		t.Fatalf("recovered state = %+v, %v, requests=%d", state, err, len(cursors))
	}
	if first, second := <-cursors, <-cursors; first != "expired" || second != "" {
		t.Fatalf("recovery cursors = %q, %q", first, second)
	}
}

func TestFullTimelineCursorRecoveryDoesNotRepeatSavedTweets(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	var started atomic.Bool
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		switch timelineCursor(t, r) {
		case "":
			cursor := "expired"
			if started.Swap(true) {
				cursor = "page-2"
			}
			fmt.Fprint(w, fullReviewTimelinePayload("105", cursor))
		case "expired":
			http.Error(w, "Invalid cursor", http.StatusBadRequest)
		case "page-2":
			fmt.Fprint(w, fullReviewTimelinePayload("104", ""))
		default:
			t.Errorf("unexpected timeline cursor: %q", timelineCursor(t, r))
		}
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	stats, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, xclient.User{ID: "42", ScreenName: "author"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("recover cursor after saving a page: %v", err)
	}
	if stats.Tweets != 2 {
		t.Fatalf("recovered tweet count = %d, want 2 unique tweets", stats.Tweets)
	}
	state, err := store.GetTweetSync(ctx, "42")
	if err != nil || state.LastID != "105" || state.Count != 2 || state.Status != "completed" {
		t.Fatalf("recovered state = %+v, %v", state, err)
	}
}

func TestArchiveUsersAbortsOnDiskFatalErrors(t *testing.T) {
	for _, cause := range []error{syscall.ENOSPC, syscall.EROFS, syscall.EACCES} {
		err := fmt.Errorf("archive failed: %w", &os.PathError{Op: "write", Path: "media.jpg", Err: cause})
		if !shouldAbortArchiveUsers(err) {
			t.Errorf("disk failure would continue to other users: %v", err)
		}
	}
}
