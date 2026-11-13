package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

func TestBothArchiveResumesLocalMediaWithoutRepeatingTimeline(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	requests := make(chan struct{}, 4)
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		fmt.Fprint(w, `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`)
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveBoth, Backfill: true})
	if err != nil {
		t.Fatal(err)
	}
	user := xclient.User{ID: "42", ScreenName: "author"}
	for i := 1; i <= 101; i++ {
		tweet := parser.TweetData{
			ID: fmt.Sprintf("%03d", i), Author: parser.Author{ID: user.ID, ScreenName: user.ScreenName},
			Media: []parser.Media{{ID: fmt.Sprintf("media-%d", i), Type: parser.MediaPhoto}},
		}
		if err := store.ArchiveTweet(ctx, job.ID, tweet, "pending"); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	oldBudget := archiveSliceBudget
	archiveSliceBudget = 0
	defer func() { archiveSliceBudget = oldBudget }()
	stats, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, []xclient.User{user}, nil, 0, 1)
	if !errors.Is(err, errYieldExecution) || stats.Failed != 100 {
		t.Fatalf("local replay did not yield after one page: failed=%d, %v", stats.Failed, err)
	}
	cp, ok, err := manager.loadArchiveCheckpoint(ctx, job.ID)
	if err != nil || !ok || cp.Next != 0 || cp.Replay == nil || cp.Replay.AfterID != "100" {
		t.Fatalf("outer checkpoint lost local replay: %+v, %v", cp, err)
	}
	stats, err = manager.archiveUsers(ctx, ctx, job, cfg, pool, nil, nil, 0, 1)
	if err != nil || stats.Failed != 101 || stats.Users != 1 || len(requests) != 1 {
		t.Fatalf("local replay restarted or counted user twice: users=%d, failed=%d, %v, requests=%d", stats.Users, stats.Failed, err, len(requests))
	}
	if _, ok, err := store.LoadJobCheckpoint(ctx, job.ID); err != nil || ok {
		t.Fatalf("completed archive retained checkpoint: ok=%t, %v", ok, err)
	}
}

func TestManualRetryResumesFixedRange(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	job, err := store.CreateJob(ctx, storage.JobKindFailedRetry, "manual", "retry")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, storage.User{ID: "42", ScreenName: "author"}); err != nil {
		t.Fatal(err)
	}
	entity, err := store.EnsureUserEntity(ctx, "42", cfg.DownloadDir, "author")
	if err != nil {
		t.Fatal(err)
	}
	var items []storage.FailedTweet
	for i := 0; i < retryBatchLimit+3; i++ {
		item, err := store.CreateFailedTweet(ctx, storage.FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: fmt.Sprint(i), Payload: "invalid JSON"})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	payload, err := json.Marshal(retryCheckpoint{AfterID: items[0].ID, UpperID: items[len(items)-2].ID, Retried: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobCheckpoint(ctx, job.ID, payload); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	if retried, err := manager.retryFailedTweets(ctx, ctx, job, cfg, true); err != nil || retried != 7 {
		t.Fatalf("resumed retry = %d, %v", retried, err)
	}
	remaining, err := store.ListFailedTweetsForRetry(ctx, 500, true)
	if err != nil || len(remaining) != len(items) {
		t.Fatalf("remaining = %d, %v", len(remaining), err)
	}
	for _, item := range remaining {
		want := 1
		if item.ID == items[0].ID || item.ID == items[len(items)-1].ID {
			want = 0
		}
		if item.RetryCount != want {
			t.Fatalf("item %s tried %d times, want %d", item.TweetID, item.RetryCount, want)
		}
	}
	if _, ok, err := store.LoadJobCheckpoint(ctx, job.ID); err != nil || ok {
		t.Fatalf("finished retry retained checkpoint: ok=%t, %v", ok, err)
	}
}

func TestArchiveMembersResumeSavedViewerAndDeduplicate(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, storage.JobKindFollowing, "author", "following")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	calls := 0
	_, err = manager.collectArchiveMembers(ctx, ctx, job, func(cursor, viewer string) (xclient.UserPage, error) {
		calls++
		if calls == 1 {
			return xclient.UserPage{Users: []xclient.User{{ID: "42"}}, Cursor: "second", ViewerKey: "original"}, nil
		}
		return xclient.UserPage{}, errors.New("temporary request failure")
	})
	if err == nil || calls != 2 {
		t.Fatalf("first member pass = %v, calls=%d", err, calls)
	}
	members, err := manager.collectArchiveMembers(ctx, ctx, job, func(cursor, viewer string) (xclient.UserPage, error) {
		if cursor != "second" || viewer != "original" {
			t.Fatalf("resumed member page = %q, %q", cursor, viewer)
		}
		return xclient.UserPage{Users: []xclient.User{{ID: "42"}, {ID: "43"}}, ViewerKey: viewer}, nil
	})
	if err != nil || len(members) != 2 || members[1].ID != "43" {
		t.Fatalf("resumed members = %+v, %v", members, err)
	}
}

// emptyTailMemberPages 模拟 X 关注/成员分页翻到底后仍持续返回新的 Bottom cursor 但不再有用户。
func emptyTailMemberPages(users int, calls *int) func(string, string) (xclient.UserPage, error) {
	return func(cursor, viewer string) (xclient.UserPage, error) {
		*calls++
		page := xclient.UserPage{Cursor: fmt.Sprintf("0|%d", *calls), ViewerKey: "viewer"}
		if *calls <= users {
			page.Users = []xclient.User{{ID: fmt.Sprint(*calls)}}
		}
		return page, nil
	}
}

func newMemberTestManager(t *testing.T) (*Manager, storage.Job) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	job, err := store.CreateJob(context.Background(), storage.JobKindFollowing, "author", "following")
	if err != nil {
		t.Fatal(err)
	}
	oldDelay := memberPageDelay
	memberPageDelay = 0
	t.Cleanup(func() { memberPageDelay = oldDelay })
	return NewManager(store, parser.NewService(), NewEventBus()), job
}

func TestArchiveMembersStopsAfterConsecutiveEmptyPages(t *testing.T) {
	manager, job := newMemberTestManager(t)
	ctx := context.Background()
	calls := 0
	members, err := manager.collectArchiveMembers(ctx, ctx, job, emptyTailMemberPages(5, &calls))
	if err != nil || len(members) != 5 {
		t.Fatalf("members = %d, %v", len(members), err)
	}
	if want := 5 + xclient.MaxConsecutiveEmptyMemberPages; calls != want {
		t.Fatalf("calls = %d, want %d", calls, want)
	}
}

func TestArchiveMembersEmptyPageCountSurvivesYield(t *testing.T) {
	manager, job := newMemberTestManager(t)
	ctx := context.Background()
	oldBudget := archiveSliceBudget
	archiveSliceBudget = 0
	defer func() { archiveSliceBudget = oldBudget }()
	calls := 0
	fetch := emptyTailMemberPages(1, &calls)
	var members []xclient.User
	var err error
	for slice := 0; slice < 20; slice++ {
		members, err = manager.collectArchiveMembers(ctx, ctx, job, fetch)
		if !errors.Is(err, errYieldExecution) {
			break
		}
	}
	if err != nil || len(members) != 1 {
		t.Fatalf("members = %d, %v", len(members), err)
	}
	if want := 1 + xclient.MaxConsecutiveEmptyMemberPages; calls != want {
		t.Fatalf("calls = %d, want %d (empty page count reset across slices)", calls, want)
	}
}

func TestArchiveMembersEmptyPageCountResetsOnNewUsers(t *testing.T) {
	manager, job := newMemberTestManager(t)
	ctx := context.Background()
	calls := 0
	gap := xclient.MaxConsecutiveEmptyMemberPages - 1
	members, err := manager.collectArchiveMembers(ctx, ctx, job, func(cursor, viewer string) (xclient.UserPage, error) {
		calls++
		page := xclient.UserPage{Cursor: fmt.Sprint(calls)}
		switch {
		case calls == 1, calls == gap+2:
			page.Users = []xclient.User{{ID: fmt.Sprint(calls)}}
		case calls == gap+3:
			page.Cursor = ""
		}
		return page, nil
	})
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}
}
