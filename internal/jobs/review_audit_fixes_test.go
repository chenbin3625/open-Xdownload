package jobs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

func saveAuditTestStorageDoc(t *testing.T, store *storage.Store, root string, dirTemplate string) config.StorageDocument {
	t.Helper()
	current, err := store.GetStorageDocument(context.Background())
	if err != nil {
		t.Fatalf("GetStorageDocument: %v", err)
	}
	doc, err := store.SaveStorageDocument(context.Background(), config.StorageDocument{
		Version: current.Version,
		Enabled: true,
		Targets: []config.StorageTarget{{
			ID:   "local-1",
			Name: "Local",
			Type: "local",
			Root: root,
		}},
		Default: config.AssetRule{TargetID: "local-1", DirectoryTemplate: dirTemplate, FilenameTemplate: "{tweet_id}_{index}.{ext}"},
		Rules: map[config.AssetKind]config.AssetRule{
			config.AssetImage: {TargetID: "local-1", DirectoryTemplate: dirTemplate, FilenameTemplate: "{tweet_id}_{index}.{ext}"},
			config.AssetVideo: {TargetID: "local-1", DirectoryTemplate: dirTemplate, FilenameTemplate: "{tweet_id}_{index}.{ext}"},
		},
	})
	if err != nil {
		t.Fatalf("SaveStorageDocument: %v", err)
	}
	return doc
}

func writeAuditTempFile(t *testing.T, dir, name string, data []byte) (string, string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return p, hex.EncodeToString(sum[:])
}

// High #17: Policy version upgrade on the same target & template key reuses the
// existing physical file (no collision suffix) and preserves Asset.ID via upsert.
func TestSaveAssetPolicyUpgradeSameTargetReusesFileAndPreservesID(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assetRoot := t.TempDir()
	v1 := saveAuditTestStorageDoc(t, store, assetRoot, "{kind}/{username}")
	// Upgrade to v2 on the same target and same template (same desiredKey).
	v2 := saveAuditTestStorageDoc(t, store, assetRoot, "{kind}/{username}")
	if v2.Version <= v1.Version {
		t.Fatalf("expected policy version bump: v1=%d v2=%d", v1.Version, v2.Version)
	}

	png := archiveTestPNG(t)
	srcPath, hash := writeAuditTempFile(t, t.TempDir(), "photo.png", png)
	tweet := parser.TweetData{
		ID:        "1001",
		Author:    parser.Author{ID: "u1", ScreenName: "alice"},
		CreatedAt: time.Now().UTC(),
	}
	media := parser.Media{ID: "m1", Type: parser.MediaPhoto}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	first, err := manager.saveAsset(ctx, ctx, v1, config.AssetImage, tweet, media, 0, assetInput{path: srcPath, hash: hash, size: int64(len(png))})
	if err != nil {
		t.Fatalf("saveAsset v1: %v", err)
	}
	second, err := manager.saveAsset(ctx, ctx, v2, config.AssetImage, tweet, media, 0, assetInput{path: srcPath, hash: hash, size: int64(len(png))})
	if err != nil {
		t.Fatalf("saveAsset v2: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("policy upgrade changed Asset.ID: v1=%q v2=%q", first.ID, second.ID)
	}
	if second.Key != first.Key {
		t.Fatalf("policy upgrade created collision key: v1=%q v2=%q", first.Key, second.Key)
	}
	if second.PolicyVersion != v2.Version {
		t.Fatalf("PolicyVersion = %d, want %d", second.PolicyVersion, v2.Version)
	}
	entries, err := os.ReadDir(filepath.Join(assetRoot, "image", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file on disk without collision duplicates, got %d", len(entries))
	}
}

// High #17: Policy version upgrade when the physical file went missing restores
// the tracked key and preserves Asset.ID.
func TestSaveAssetPolicyUpgradeRestoresMissingFileAndPreservesID(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assetRoot := t.TempDir()
	v1 := saveAuditTestStorageDoc(t, store, assetRoot, "{kind}/{username}")
	v2 := saveAuditTestStorageDoc(t, store, assetRoot, "{kind}/{username}")

	png := archiveTestPNG(t)
	srcPath, hash := writeAuditTempFile(t, t.TempDir(), "photo.png", png)
	tweet := parser.TweetData{
		ID:        "1002",
		Author:    parser.Author{ID: "u1", ScreenName: "alice"},
		CreatedAt: time.Now().UTC(),
	}
	media := parser.Media{ID: "m2", Type: parser.MediaPhoto}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	first, err := manager.saveAsset(ctx, ctx, v1, config.AssetImage, tweet, media, 0, assetInput{path: srcPath, hash: hash, size: int64(len(png))})
	if err != nil {
		t.Fatalf("saveAsset v1: %v", err)
	}
	fullPath := filepath.Join(assetRoot, filepath.FromSlash(first.Key))
	if err := os.Remove(fullPath); err != nil {
		t.Fatal(err)
	}

	restored, err := manager.saveAsset(ctx, ctx, v2, config.AssetImage, tweet, media, 0, assetInput{path: srcPath, hash: hash, size: int64(len(png))})
	if err != nil {
		t.Fatalf("saveAsset v2 restore: %v", err)
	}
	if restored.ID != first.ID || restored.Key != first.Key || restored.PolicyVersion != v2.Version {
		t.Fatalf("restored = %+v, want ID=%q Key=%q Version=%d", restored, first.ID, first.Key, v2.Version)
	}
	if _, err := os.Stat(fullPath); err != nil {
		t.Fatalf("restored file missing at %s: %v", fullPath, err)
	}
}

// High #17: CreateAsset DB failure rolls back newly written untracked orphan files
// but never deletes an already-tracked physical file during policy version upgrade.
func TestSaveAssetRollsBackOrphanFileOnCreateAssetFailure(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assetRoot := t.TempDir()
	v1 := saveAuditTestStorageDoc(t, store, assetRoot, "{kind}/{username}")
	v2 := saveAuditTestStorageDoc(t, store, assetRoot, "{kind}/{username}")

	png := archiveTestPNG(t)
	srcPath1, hash1 := writeAuditTempFile(t, t.TempDir(), "existing.png", png)
	tweet1 := parser.TweetData{ID: "2001", Author: parser.Author{ID: "u1", ScreenName: "alice"}, CreatedAt: time.Now().UTC()}
	media1 := parser.Media{ID: "m1", Type: parser.MediaPhoto}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	tracked, err := manager.saveAsset(ctx, ctx, v1, config.AssetImage, tweet1, media1, 0, assetInput{path: srcPath1, hash: hash1, size: int64(len(png))})
	if err != nil {
		t.Fatalf("seed tracked asset: %v", err)
	}
	trackedPath := filepath.Join(assetRoot, filepath.FromSlash(tracked.Key))

	// Inject a SQLite trigger that fails all INSERT/UPDATE on assets after FindAssetHash runs.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	if _, err := rawDB.Exec(`CREATE TRIGGER fail_asset_insert BEFORE INSERT ON assets BEGIN SELECT RAISE(ABORT, 'injected db error'); END;`); err != nil {
		t.Fatal(err)
	}

	// Case 1: Brand new object -> CreateAsset fails -> orphan file MUST be removed.
	otherBytes := append(append([]byte(nil), png...), []byte("orphan")...)
	srcPath2, hash2 := writeAuditTempFile(t, t.TempDir(), "orphan.png", otherBytes)
	tweet2 := parser.TweetData{ID: "2002", Author: parser.Author{ID: "u1", ScreenName: "alice"}, CreatedAt: time.Now().UTC()}
	media2 := parser.Media{ID: "m2", Type: parser.MediaPhoto}
	if _, err := manager.saveAsset(ctx, ctx, v2, config.AssetImage, tweet2, media2, 0, assetInput{path: srcPath2, hash: hash2, size: int64(len(otherBytes))}); err == nil {
		t.Fatal("expected CreateAsset error from trigger")
	}
	orphanPath := filepath.Join(assetRoot, "image", "alice", "2002_1.png")
	if _, statErr := os.Stat(orphanPath); !os.IsNotExist(statErr) {
		t.Fatalf("orphan file was not rolled back after CreateAsset failure: %v", statErr)
	}

	// Case 2: Policy upgrade on already-tracked key -> CreateAsset fails -> existing tracked file MUST NOT be deleted.
	if _, err := manager.saveAsset(ctx, ctx, v2, config.AssetImage, tweet1, media1, 0, assetInput{path: srcPath1, hash: hash1, size: int64(len(png))}); err == nil {
		t.Fatal("expected CreateAsset error on policy upgrade")
	}
	if _, statErr := os.Stat(trackedPath); statErr != nil {
		t.Fatalf("already-tracked file was deleted on CreateAsset failure: %v", statErr)
	}
}

// High #18: archiveMediaTimeline must NOT advance user_entities.last_seen_tweet_id
// or pollute media_sync.LastSeenTweetID when a page fails to parse or fetch mid-scan.
func TestArchiveMediaTimelineDoesNotAdvanceWatermarkOnMidScanFailure(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var failPage atomic.Bool
	failPage.Store(true)

	// Poison page: entry has rest_id="500" (so WalkUserMedia records LastSeenTweetID="500")
	// with missing legacy fields, causing WalkUserMedia to fail the whole page.
	poisonPayload := `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"500"}}}}}]}]}}}}}}`

	png := archiveTestPNG(t)
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/media/") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
			return
		}
		if failPage.Load() {
			fmt.Fprint(w, poisonPayload)
			return
		}
		fmt.Fprint(w, mediaTimelinePayload([]string{"400"}, ""))
	})

	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	user := xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice", MediaCount: 5}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	// Run 1: page fails parse -> watermark must NOT be advanced or polluted with "500".
	stats1, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, user, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if stats1.Failed != 1 {
		t.Fatalf("expected 1 failed user on poison page, got stats=%+v", stats1)
	}
	entity, err := store.LocateUserEntity(ctx, user.ID, filepath.Join(cfg.DownloadDir, "users"))
	if err != nil || entity == nil {
		t.Fatalf("LocateUserEntity: %+v, %v", entity, err)
	}
	if entity.LastSeenTweetID != "" {
		t.Fatalf("user_entities.last_seen_tweet_id advanced on failed page: %q", entity.LastSeenTweetID)
	}
	syncState, err := store.GetMediaSync(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if syncState.LastSeenTweetID != "" {
		t.Fatalf("media_sync.LastSeenTweetID polluted by failed page: %q", syncState.LastSeenTweetID)
	}

	// Run 2: succeeds cleanly with tweet "400" -> watermark advances to "400" (not "500").
	failPage.Store(false)
	stats2, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, user, nil, nil, nil)
	if err != nil || stats2.Failed != 0 {
		t.Fatalf("clean run failed: stats=%+v err=%v", stats2, err)
	}
	entity, err = store.LocateUserEntity(ctx, user.ID, filepath.Join(cfg.DownloadDir, "users"))
	if err != nil || entity == nil {
		t.Fatalf("LocateUserEntity: %+v, %v", entity, err)
	}
	if entity.LastSeenTweetID != "400" {
		t.Fatalf("user_entities.last_seen_tweet_id = %q, want 400", entity.LastSeenTweetID)
	}
}

// High #19: Fresh member scan (startCursor == "") must NOT wipe progress back to page 0
// when page 2 fails with invalid cursor; resumed scan recovers cleanly when page 0 fails.
func TestCollectArchiveMembersFreshScanDoesNotWipeOnPage2InvalidCursor(t *testing.T) {
	prevDelay := memberPageDelay
	memberPageDelay = 0
	t.Cleanup(func() { memberPageDelay = prevDelay })

	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	job, err := store.CreateJob(ctx, storage.JobKindList, "list-1", "list-1")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())

	// Fresh scan: page 1 succeeds, page 2 fails with invalid cursor -> must NOT restart from "".
	var freshCalls []string
	_, err = manager.collectArchiveMembers(ctx, ctx, job, func(cursor, _ string) (xapi.UserPage, error) {
		freshCalls = append(freshCalls, cursor)
		if cursor == "" {
			return xapi.UserPage{
				Users:  []xapi.User{{ID: "u1", ScreenName: "alice"}},
				Cursor: "page-2",
			}, nil
		}
		return xapi.UserPage{}, fmt.Errorf("%w: bad cursor", xapi.ErrInvalidTimelineCursor)
	})
	if !xapi.IsInvalidTimelineCursor(err) {
		t.Fatalf("expected invalid cursor error on fresh scan page 2, got %v", err)
	}
	if len(freshCalls) != 2 || freshCalls[0] != "" || freshCalls[1] != "page-2" {
		t.Fatalf("fresh scan should not have reset to page 0: calls=%q", freshCalls)
	}
	cp, ok, err := manager.loadArchiveCheckpointProgress(ctx, job.ID)
	if err != nil || !ok || cp.Members == nil || cp.Members.Cursor != "page-2" || len(cp.Members.Users) != 1 {
		t.Fatalf("checkpoint wiped on fresh scan failure: ok=%v cp=%+v err=%v", ok, cp, err)
	}

	// Resumed scan (startCursor == "page-2"): first fetch fails -> resets to "" and completes.
	var resumeCalls []string
	users, err := manager.collectArchiveMembers(ctx, ctx, job, func(cursor, _ string) (xapi.UserPage, error) {
		resumeCalls = append(resumeCalls, cursor)
		if cursor == "page-2" {
			return xapi.UserPage{}, fmt.Errorf("%w: expired persisted cursor", xapi.ErrInvalidTimelineCursor)
		}
		return xapi.UserPage{
			Users: []xapi.User{{ID: "u1", ScreenName: "alice"}, {ID: "u2", ScreenName: "bob"}},
		}, nil
	})
	if err != nil {
		t.Fatalf("resumed scan failed to recover stale cursor: %v", err)
	}
	if len(resumeCalls) != 2 || resumeCalls[0] != "page-2" || resumeCalls[1] != "" {
		t.Fatalf("resume calls = %q, want [page-2 \"\"]", resumeCalls)
	}
	if len(users) != 2 {
		t.Fatalf("users = %d, want 2", len(users))
	}
}

// High #19: Resumed full timeline scan that already saved a page must NOT wipe progress
// if a deeper page fails with invalid cursor.
func TestFullTimelineResumedScanDoesNotWipeAfterSavingPage(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}

	var calls []string
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		cursor := timelineCursor(t, r)
		calls = append(calls, cursor)
		switch cursor {
		case "page-2":
			fmt.Fprint(w, fullReviewTimelinePayload("104", "page-3"))
		case "page-3":
			http.Error(w, "Invalid cursor", http.StatusBadRequest)
		default:
			t.Errorf("resumed scan wiped back to cursor %q", cursor)
			fmt.Fprint(w, fullReviewTimelinePayload("105", ""))
		}
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTweetSync(ctx, "42"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTweetSync(ctx, storage.TweetSync{UserID: "42", HeadID: "105", Cursor: "page-2", Count: 1, Status: "interrupted"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	stats, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, xclient.User{ID: "42", ScreenName: "author"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("expected failure on page-3, got stats=%+v", stats)
	}
	if len(calls) != 2 || calls[0] != "page-2" || calls[1] != "page-3" {
		t.Fatalf("calls = %q, want [page-2 page-3]", calls)
	}
	state, err := store.GetTweetSync(ctx, "42")
	if err != nil || state.Cursor != "page-3" || state.Count != 2 {
		t.Fatalf("state = %+v, %v (expected preserved cursor page-3, count 2)", state, err)
	}
}

// High #19: Fresh media timeline scan must NOT reset to page 0 when page 2 hits invalid cursor.
func TestArchiveMediaTimelineFreshScanDoesNotWipeOnPage2InvalidCursor(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var calls []string
	png := archiveTestPNG(t)
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/media/") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
			return
		}
		cursor := timelineCursor(t, r)
		calls = append(calls, cursor)
		if cursor == "" {
			fmt.Fprint(w, mediaTimelinePayload([]string{"300"}, "page-2"))
			return
		}
		http.Error(w, "Invalid cursor", http.StatusBadRequest)
	})
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	user := xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	stats, err := manager.archiveUserWithBudget(ctx, ctx, job, cfg, pool, user, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("expected failed=1, got %+v", stats)
	}
	if len(calls) != 2 || calls[0] != "" || calls[1] != "page-2" {
		t.Fatalf("calls = %q, want [\"\" page-2]", calls)
	}
	state, err := store.GetMediaSync(ctx, user.ID)
	if err != nil || state.Cursor != "page-2" || state.Count != 1 {
		t.Fatalf("media_sync state = %+v, %v (expected preserved page-2, count 1)", state, err)
	}
}

// High #20: selectArchiveBatchWithPrimary unifies filtering with execution-time
// primary relationship checks, and skipped protected/blocked/muted users call
// TouchTweetSync so they don't permanently pin batch slots.
func TestHigh20BatchFilteringAndTouchTweetSyncForSkippedUsers(t *testing.T) {
	// 1. selectArchiveBatchWithPrimary keeps Protected && Muting when hasPrimary=true.
	users := []xapi.User{
		{ID: "u1", ScreenName: "prot_muted", Protected: true, Muting: true},
		{ID: "u2", ScreenName: "pub_muted", Protected: false, Muting: true},
		{ID: "u3", ScreenName: "normal"},
	}
	withoutPrimary := selectArchiveBatchWithPrimary(users, nil, 10, false)
	if len(withoutPrimary) != 1 || withoutPrimary[0].ID != "u3" {
		t.Fatalf("withoutPrimary = %+v, want [u3]", withoutPrimary)
	}
	withPrimary := selectArchiveBatchWithPrimary(users, nil, 10, true)
	if len(withPrimary) != 2 || withPrimary[0].ID != "u1" || withPrimary[1].ID != "u3" {
		t.Fatalf("withPrimary = %+v, want [u1 u3]", withPrimary)
	}

	// 2. Execution-time primary refresh finds Blocking=true -> skips AND touches tweet_sync,
	// so the next batch advances to u3 instead of being starved by u1 forever.
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var archivedNormal atomic.Int64
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/UserByRestId") {
			fmt.Fprint(w, `{"data":{"user":{"result":{"rest_id":"u1","legacy":{"name":"Prot","screen_name":"prot_muted","protected":true,"blocking":true}}}}}`)
			return
		}
		archivedNormal.Add(1)
		fmt.Fprint(w, fullReviewTimelinePayload("501", ""))
	})
	manager := NewManager(store, parser.NewService(), NewEventBus())

	job1, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind:        storage.JobKindFollowing,
		Input:       "me",
		ArchiveMode: storage.ArchiveTweets,
		BatchSize:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stats1, err := manager.archiveUsers(ctx, ctx, job1, cfg, pool, users, nil, 0, 1)
	if err != nil {
		t.Fatalf("batch 1: %v", err)
	}
	if stats1.SkippedUsers < 2 {
		t.Fatalf("expected both u2 (pre-filter) and u1 (primary refresh) skipped, got %+v", stats1)
	}
	syncedAt, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if syncedAt["u1"].IsZero() || syncedAt["u2"].IsZero() {
		t.Fatalf("skipped users missing tweet_sync timestamp: %+v", syncedAt)
	}

	// Batch 2 (batch=1) must pick u3 (never synced) instead of re-picking u1.
	job2, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind:        storage.JobKindFollowing,
		Input:       "me",
		ArchiveMode: storage.ArchiveTweets,
		BatchSize:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.archiveUsers(ctx, ctx, job2, cfg, pool, users, nil, 0, 1); err != nil {
		t.Fatalf("batch 2: %v", err)
	}
	if archivedNormal.Load() != 1 {
		t.Fatalf("normal user u3 was starved by skipped protected/blocked user; timeline calls=%d", archivedNormal.Load())
	}
}

// Medium ①: When classified storage is disabled, retryFailedTweets must NOT fall back
// to writing a relative ./s3:<targetID>/... path into the process CWD.
func TestRetryFailedTweetsDoesNotWriteRelativeS3PathWhenClassifiedDisabled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rawParent string
	}{
		{name: "normalized_cwd_s3_parent", rawParent: ""},
		{name: "relative_s3_parent", rawParent: "s3:bucket-1/image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "test.db")
			store, err := storage.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			useLegacyStorage(t, store)

			// Isolate CWD so we can assert nothing is written to ./s3:...
			fakeCwd := t.TempDir()
			t.Chdir(fakeCwd)

			png := archiveTestPNG(t)
			mediaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(png)
			}))
			defer mediaServer.Close()

			downloadDir := t.TempDir()
			cfg := config.AppConfig{DownloadDir: downloadDir, StorageType: config.StorageLocal}
			if _, err := store.UpsertUser(ctx, storage.User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
				t.Fatal(err)
			}
			entity, err := store.EnsureUserEntity(ctx, "u1", "s3:bucket-1/image", "alice")
			if err != nil {
				t.Fatal(err)
			}
			if tc.rawParent != "" {
				rawDB, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := rawDB.Exec(`UPDATE user_entities SET parent_dir = ? WHERE id = ?`, tc.rawParent, entity.ID); err != nil {
					rawDB.Close()
					t.Fatal(err)
				}
				rawDB.Close()
			}
			job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
			if err != nil {
				t.Fatal(err)
			}
			tweet := parser.TweetData{
				ID:        "9001",
				Author:    parser.Author{ID: "u1", ScreenName: "alice", Name: "Alice"},
				CreatedAt: time.Now().UTC(),
				Media:     []parser.Media{{ID: "p1", Type: parser.MediaPhoto, URL: mediaServer.URL + "/photo.png"}},
			}
			payload, err := json.Marshal(tweet)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateFailedTweet(ctx, storage.FailedTweet{
				JobID:    job.ID,
				EntityID: entity.ID,
				TweetID:  tweet.ID,
				Payload:  string(payload),
				Error:    "temporary error",
			}); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(store, parser.NewService(), NewEventBus())
			useTestDownloader(t, manager)
			retried, err := manager.retryFailedTweets(ctx, ctx, job, cfg, true)
			if err != nil {
				t.Fatalf("retryFailedTweets: %v", err)
			}
			if retried != 1 {
				t.Fatalf("retried = %d, want 1", retried)
			}
			// CWD must remain completely empty (no ./s3:bucket-1 directory).
			cwdEntries, err := os.ReadDir(fakeCwd)
			if err != nil {
				t.Fatal(err)
			}
			if len(cwdEntries) != 0 {
				t.Fatalf("retryFailedTweets wrote into CWD: %v", cwdEntries)
			}
			downloads, err := store.ListDownloads(ctx, 10)
			if err != nil || len(downloads) != 1 {
				t.Fatalf("downloads = %+v, %v", downloads, err)
			}
			if !strings.HasPrefix(downloads[0].FilePath, downloadDir) {
				t.Fatalf("file written outside DownloadDir: %q (want under %q)", downloads[0].FilePath, downloadDir)
			}
			if _, err := os.Stat(downloads[0].FilePath); err != nil {
				t.Fatalf("downloaded file missing: %v", err)
			}
		})
	}
}

// Medium ②: skipSameContentMedia uses os.SameFile before os.Remove so symlinks /
// case-insensitive aliases to the same file never delete the only remaining copy.
func TestSkipSameContentMediaPreservesSameFileSymlink(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	dir := t.TempDir()
	cfg := config.AppConfig{DownloadDir: dir, StorageType: config.StorageLocal}
	target, err := filestore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}

	origPath, hash := writeAuditTempFile(t, dir, "orig.jpg", []byte("same-image-bytes"))
	aliasPath := filepath.Join(dir, "alias.jpg")
	if err := os.Symlink(origPath, aliasPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := store.CreateDownload(ctx, storage.DownloadRecord{
		JobID:       job.ID,
		TweetID:     "t1",
		MediaURL:    "https://pbs.twimg.com/media/one.jpg",
		ContentHash: hash,
		FilePath:    origPath,
		Bytes:       int64(len("same-image-bytes")),
	}); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(store, parser.NewService(), NewEventBus())
	skipped, err := manager.skipSameContentMedia(ctx, ctx, job, cfg, target, downloader.Result{
		Path:        aliasPath,
		ContentHash: hash,
		Bytes:       int64(len("same-image-bytes")),
	}, "https://pbs.twimg.com/media/two.jpg", "t2", "")
	if err != nil {
		t.Fatalf("skipSameContentMedia: %v", err)
	}
	if skipped {
		t.Fatal("expected skipped=false when candidate resolves to os.SameFile")
	}
	if _, err := os.Lstat(aliasPath); err != nil {
		t.Fatalf("alias symlink was removed: %v", err)
	}
	if _, err := os.Stat(origPath); err != nil {
		t.Fatalf("original file was removed: %v", err)
	}
}

// Medium ③: downloadAssetMedia cache hit for an existing (tweetID, mediaURL)
// must NOT redundantly call CreateDownload and refresh created_at, while still
// creating a download row for a different tweet sharing the same mediaKey.
func TestDownloadAssetMediaCacheHitDoesNotRefreshCreatedAt(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assetRoot := t.TempDir()
	doc := saveAuditTestStorageDoc(t, store, assetRoot, "{kind}/{username}")

	var hits atomic.Int64
	png := archiveTestPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer server.Close()

	mediaURL := server.URL + "/shared.png"
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	useTestDownloader(t, manager)
	published := time.Now().UTC().Truncate(time.Second)

	res1, err := manager.downloadAssetMedia(ctx, ctx, job, cfg, doc, mediaURL, "tweet-1", true, published)
	if err != nil || res1.skipped {
		t.Fatalf("first download: res=%+v err=%v", res1, err)
	}
	firstRecord, _, err := store.GetMediaDownloadState(ctx, "tweet-1", mediaURL)
	if err != nil || firstRecord == nil {
		t.Fatalf("GetMediaDownloadState(tweet-1): %+v, %v", firstRecord, err)
	}
	originalCreatedAt := firstRecord.CreatedAt

	time.Sleep(25 * time.Millisecond)

	// Second call for the SAME (tweet-1, mediaURL): cache hit must NOT refresh created_at.
	res2, err := manager.downloadAssetMedia(ctx, ctx, job, cfg, doc, mediaURL, "tweet-1", true, published)
	if err != nil || !res2.skipped {
		t.Fatalf("second download: res=%+v err=%v", res2, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("HTTP hits = %d, want 1", hits.Load())
	}
	afterRecord, _, err := store.GetMediaDownloadState(ctx, "tweet-1", mediaURL)
	if err != nil || afterRecord == nil {
		t.Fatalf("GetMediaDownloadState(tweet-1) after hit: %+v, %v", afterRecord, err)
	}
	if !afterRecord.CreatedAt.Equal(originalCreatedAt) {
		t.Fatalf("cache hit refreshed created_at: before=%v after=%v", originalCreatedAt, afterRecord.CreatedAt)
	}

	// Third call for a DIFFERENT tweet (tweet-2) sharing the same mediaURL:
	// reuses cached asset (no extra HTTP hit) AND creates a download row for tweet-2.
	res3, err := manager.downloadAssetMedia(ctx, ctx, job, cfg, doc, mediaURL, "tweet-2", true, published)
	if err != nil || !res3.skipped {
		t.Fatalf("third download (tweet-2): res=%+v err=%v", res3, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("HTTP hits = %d, want 1", hits.Load())
	}
	secondTweetRecord, _, err := store.GetMediaDownloadState(ctx, "tweet-2", mediaURL)
	if err != nil || secondTweetRecord == nil {
		t.Fatalf("expected download record for tweet-2, got %+v, %v", secondTweetRecord, err)
	}
	links, err := store.DownloadAssets(ctx, secondTweetRecord.ID)
	if err != nil || links[config.AssetImage] == "" {
		t.Fatalf("tweet-2 download not linked to asset: %+v, %v", links, err)
	}
}

// Medium ④: Default local mode downloadMedia calls ensureVideoPoster for videos
// (both fresh downloads and result.Skipped).
func TestDownloadMediaLocalModeCallsEnsureVideoPoster(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	var posterCalls atomic.Int64
	origSavePoster := savePosterImage
	savePosterImage = func(_ context.Context, _ string, _ string, destPath string) error {
		posterCalls.Add(1)
		return os.WriteFile(destPath, []byte("poster-jpg"), 0o600)
	}
	t.Cleanup(func() { savePosterImage = origSavePoster })

	videoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("fake-mp4-content"))
	}))
	defer videoServer.Close()

	dir := t.TempDir()
	cfg := config.AppConfig{DownloadDir: dir, StorageType: config.StorageLocal}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	useTestDownloader(t, manager)
	mediaURL := videoServer.URL + "/clip.mp4"
	previewURL := "https://pbs.twimg.com/ext_tw_video_thumb/123/pu/img/poster.jpg"

	// Fresh local video download must invoke ensureVideoPoster.
	res, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "v-tweet-1", dir, "clip", false, time.Now().UTC(), previewURL)
	if err != nil || res.skipped {
		t.Fatalf("fresh downloadMedia: res=%+v err=%v", res, err)
	}
	if posterCalls.Load() != 1 {
		t.Fatalf("posterCalls after fresh download = %d, want 1", posterCalls.Load())
	}
	rec, _, err := store.GetMediaDownloadState(ctx, "v-tweet-1", mediaURL)
	if err != nil || rec == nil {
		t.Fatalf("GetMediaDownloadState: %+v, %v", rec, err)
	}
	posterPath := rec.FilePath + posterSuffix
	if _, err := os.Stat(posterPath); err != nil {
		t.Fatalf("poster file not created at %s: %v", posterPath, err)
	}

	// Remove poster, delete download row? No, keep file on disk & test result.Skipped path:
	// Calling downloadMedia again with existing file & DB row hits skipArchivedMedia (which calls ensureVideoPoster).
	// To exercise the result.Skipped branch in downloadMedia specifically, point existing DB FilePath to the file
	// while removing the poster and making ExistingFileOwner return true via a fresh tweet/record check:
	if err := os.Remove(posterPath); err != nil {
		t.Fatal(err)
	}
	// Re-run via skipArchivedMedia verifies poster regeneration too; also verify direct result.Skipped
	// by removing only the poster and triggering downloadMedia again.
	res2, err := manager.downloadMedia(ctx, ctx, job, cfg, mediaURL, "v-tweet-1", dir, "clip", false, time.Now().UTC(), previewURL)
	if err != nil || !res2.skipped {
		t.Fatalf("second downloadMedia: res=%+v err=%v", res2, err)
	}
	if posterCalls.Load() != 2 {
		t.Fatalf("posterCalls after re-run = %d, want 2", posterCalls.Load())
	}
}

// Medium ⑤: saveArchiveCheckpoint must NOT re-read/unmarshal job_checkpoint_tasks
// on every user completion (avoiding O(N^2) read amplification).
func TestSaveArchiveCheckpointDoesNotReloadTasksTable(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	job, err := store.CreateJob(ctx, storage.JobKindFollowing, "me", "me")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	tasks := []archiveUserTask{
		{index: 0, order: 0, user: xapi.User{ID: "u1", ScreenName: "alice"}},
		{index: 1, order: 1, user: xapi.User{ID: "u2", ScreenName: "bob"}},
	}
	if err := manager.saveArchiveCheckpoint(ctx, job.ID, tasks, 0, archiveStats{}); err != nil {
		t.Fatalf("initial saveArchiveCheckpoint: %v", err)
	}

	// Corrupt job_checkpoint_tasks directly in SQLite: if saveArchiveCheckpoint
	// called loadArchiveCheckpoint (which reads & unmarshals tasks), it would treat
	// the checkpoint as corrupt and wipe it. Using loadArchiveCheckpointProgress must
	// ignore job_checkpoint_tasks completely and preserve progress cleanly.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	if _, err := rawDB.Exec(`UPDATE job_checkpoint_tasks SET payload = '{not-valid-json' WHERE job_id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}

	if err := manager.saveArchiveCheckpoint(ctx, job.ID, tasks, 1, archiveStats{Users: 1, Tweets: 5}); err != nil {
		t.Fatalf("saveArchiveCheckpoint failed or read tasks table: %v", err)
	}
	cp, ok, err := manager.loadArchiveCheckpointProgress(ctx, job.ID)
	if err != nil || !ok {
		t.Fatalf("loadArchiveCheckpointProgress: ok=%v err=%v", ok, err)
	}
	if cp.Next != 1 || cp.Stats.Users != 1 || cp.Stats.Tweets != 5 {
		t.Fatalf("progress not saved cleanly: %+v", cp)
	}
}

// Medium ⑥: Single-user directory conflict must NOT abort the entire batch archive;
// the error is isolated to that user and remaining users continue.
func TestArchiveUsersIsolatesSingleUserDirectoryConflict(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	useLegacyStorage(t, store)

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	parent := filepath.Join(cfg.DownloadDir, "users")
	alice := xclient.User{ID: "u-alice", ScreenName: "alice", Name: "Alice", MediaCount: 10}
	bob := xclient.User{ID: "u-bob", ScreenName: "bob", Name: "Bob", MediaCount: 1}

	// Pre-assign alice's target directory to another user to trigger directory conflict.
	if _, err := store.UpsertUser(ctx, storage.User{ID: "u-other", ScreenName: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u-other", parent, userDirectoryName(alice)); err != nil {
		t.Fatal(err)
	}

	var bobTimelineCalls atomic.Int64
	pool := archiveSingleClientPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		bobTimelineCalls.Add(1)
		fmt.Fprint(w, fullReviewTimelinePayload("777", ""))
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind:        storage.JobKindFollowing,
		Input:       "me",
		ArchiveMode: storage.ArchiveTweets,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	stats, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, []xclient.User{alice, bob}, nil, 0, 1)
	if err != nil {
		t.Fatalf("directory conflict aborted entire batch: %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats.Failed = %d, want 1 (stats=%+v)", stats.Failed, stats)
	}
	if len(stats.Issues) == 0 || !strings.Contains(stats.Issues[0], "归档目录") {
		t.Fatalf("expected directory conflict issue, got %v", stats.Issues)
	}
	if bobTimelineCalls.Load() != 1 {
		t.Fatalf("remaining user bob was not archived: timeline calls=%d", bobTimelineCalls.Load())
	}
	bobSync, err := store.GetTweetSync(ctx, bob.ID)
	if err != nil || bobSync.Status != "completed" {
		t.Fatalf("bob tweet sync = %+v, %v", bobSync, err)
	}
	// Conflicting user also got TouchTweetSync so it doesn't starve future batches.
	syncedAt, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil || syncedAt[alice.ID].IsZero() {
		t.Fatalf("conflicting user missing tweet_sync touch: %+v, %v", syncedAt, err)
	}
}
