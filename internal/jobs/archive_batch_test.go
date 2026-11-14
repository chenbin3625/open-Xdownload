package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func TestSelectArchiveBatchOrdersByLeastRecentSync(t *testing.T) {
	now := time.Now()
	users := []xapi.User{
		{ID: "u1", ScreenName: "recent"},
		{ID: "u2", ScreenName: "old"},
		{ID: "u3", ScreenName: "never-a"},
		{ID: "u2", ScreenName: "duplicate"},
		{ID: "", ScreenName: "empty"},
		{ID: "u4", ScreenName: "blocked", Blocking: true},
		{ID: "u5", ScreenName: "muted", Muting: true},
		{ID: "u6", ScreenName: "never-b"},
	}
	synced := map[string]time.Time{
		"u1": now,
		"u2": now.Add(-2 * time.Hour),
	}

	got := selectArchiveBatch(users, synced, 3)
	want := []string{"u3", "u6", "u2"}
	if len(got) != len(want) {
		t.Fatalf("selected %d users, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("selected[%d] = %s, want %s", i, got[i].ID, want[i])
		}
	}
}

const emptyTimelinePayload = `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`

func seedArchiveBatchJob(t *testing.T, store *storage.Store, mode storage.ArchiveMode) storage.Job {
	t.Helper()
	job, err := store.CreateArchiveJob(context.Background(), storage.JobDraft{
		Kind: storage.JobKindFollowing, Input: "me", ArchiveMode: mode, BatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestArchiveUsersAppliesBatchSizeToTweetModes(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	store.TouchTweetSync(ctx, "u1")
	store.TouchTweetSync(ctx, "u3")

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var names []string
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		variables := r.URL.Query().Get("variables")
		var vars struct {
			ScreenName string `json:"screenName"`
		}
		_ = json.Unmarshal([]byte(variables), &vars)
		names = append(names, vars.ScreenName)
		fmt.Fprint(w, emptyTimelinePayload)
	})
	job := seedArchiveBatchJob(t, store, storage.ArchiveTweets)
	users := []xapi.User{
		{ID: "u1", ScreenName: "synced-a"},
		{ID: "u2", ScreenName: "never"},
		{ID: "u3", ScreenName: "synced-b"},
	}
	if _, err := NewManager(store, parser.NewService(), NewEventBus()).archiveUsers(
		ctx, ctx, job, cfg, pool, users, nil, 0, 1,
	); err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "never" {
		t.Fatalf("requested users = %v, want only never-synced user", names)
	}
}

func TestArchiveUsersBatchSizeIgnoredForMediaMode(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	store.TouchTweetSync(ctx, "u1")
	store.TouchTweetSync(ctx, "u3")

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var mediaCalls int
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/UserMedia") {
			mediaCalls++
		}
		fmt.Fprint(w, mediaTimelinePayload(nil, ""))
	})
	job := seedArchiveBatchJob(t, store, storage.ArchiveMedia)
	users := []xapi.User{
		{ID: "u1", ScreenName: "a"},
		{ID: "u2", ScreenName: "b"},
		{ID: "u3", ScreenName: "c"},
	}
	if _, err := NewManager(store, parser.NewService(), NewEventBus()).archiveUsers(
		ctx, ctx, job, cfg, pool, users, nil, 0, 1,
	); err != nil {
		t.Fatal(err)
	}
	if mediaCalls != 3 {
		t.Fatalf("media calls = %d, want 3 (batchSize must not apply to media)", mediaCalls)
	}
}

func TestArchiveUsersBatchResumeKeepsOriginalSelection(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	store.TouchTweetSync(ctx, "u1") // 已归档，不应入选

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var names []string
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		var vars struct {
			ScreenName string `json:"screenName"`
		}
		_ = json.Unmarshal([]byte(r.URL.Query().Get("variables")), &vars)
		names = append(names, vars.ScreenName)
		fmt.Fprint(w, emptyTimelinePayload)
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind: storage.JobKindFollowing, Input: "me", ArchiveMode: storage.ArchiveTweets, BatchSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldBudget := archiveSliceBudget
	archiveSliceBudget = 0
	defer func() { archiveSliceBudget = oldBudget }()

	manager := NewManager(store, parser.NewService(), NewEventBus())
	users := []xapi.User{
		{ID: "u1", ScreenName: "synced"},
		{ID: "u2", ScreenName: "never-a"},
		{ID: "u3", ScreenName: "never-b"},
	}
	if _, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1); !errors.Is(err, errYieldExecution) {
		t.Fatalf("first slice err = %v, want yield", err)
	}
	if _, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "never-a" || names[1] != "never-b" {
		t.Fatalf("requested users = %v, want [never-a never-b] and no reselection", names)
	}
}

func TestArchiveUsersBatchTouchesProtectedNonFollowingUser(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind: storage.JobKindUser, Input: "locked", ArchiveMode: storage.ArchiveTweets, BatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	users := []xapi.User{{
		ID: "u1", ScreenName: "locked", Protected: true, Following: false,
	}}
	stats, err := NewManager(store, parser.NewService(), NewEventBus()).archiveUsers(
		ctx, ctx, job, cfg, nil, users, nil, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Users != 1 {
		t.Fatalf("stats.Users = %d, want 1", stats.Users)
	}
	times, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if times["u1"].IsZero() {
		t.Fatal("protected non-following user was not touched; it would pin a batch slot")
	}
}
