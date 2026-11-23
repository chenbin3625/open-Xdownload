package storage

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestMigrateUserEntitiesToKindDirsPreservesTargetAndFailedTweets(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pre-v7.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root, StorageType: config.StorageLocal}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveStorageDocument(ctx, config.DefaultStorage(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	oldParent := filepath.Join(root, "users")
	oldEntity, err := store.EnsureUserEntity(ctx, "u1", oldParent, "Alice(alice)")
	if err != nil {
		t.Fatal(err)
	}
	imageParent := filepath.Join(root, "image")
	targetEntity, err := store.EnsureUserEntity(ctx, "u1", imageParent, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserEntityLastSeenTweet(ctx, targetEntity.ID, "999"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: oldEntity.ID, TweetID: "t-old-only", Payload: "{}", Error: "err"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: oldEntity.ID, TweetID: "t-dup", Payload: "{}", Error: "old-err"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: targetEntity.ID, TweetID: "t-dup", Payload: "{}", Error: "new-err"}); err != nil {
		t.Fatal(err)
	}

	// Simulate reopening an unmigrated database without user_entities_kind_dirs applied.
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE name = 'user_entities_kind_dirs'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	gotTarget, err := reopened.LocateUserEntity(ctx, "u1", imageParent)
	if err != nil || gotTarget == nil {
		t.Fatalf("target entity missing: %+v %v", gotTarget, err)
	}
	if gotTarget.ID != targetEntity.ID || gotTarget.LastSeenTweetID != "999" {
		t.Fatalf("target entity overwritten: got %+v, want id=%d lastSeen=999", gotTarget, targetEntity.ID)
	}
	if old, err := reopened.LocateUserEntity(ctx, "u1", oldParent); err != nil || old != nil {
		t.Fatalf("old entity still exists: %+v %v", old, err)
	}
	var failedTweets []FailedTweet
	if err := reopened.db.Select(&failedTweets, `SELECT * FROM failed_tweets WHERE entity_id = ? ORDER BY tweet_id`, targetEntity.ID); err != nil {
		t.Fatal(err)
	}
	if len(failedTweets) != 2 || failedTweets[0].TweetID != "t-dup" || failedTweets[0].Error != "new-err" || failedTweets[1].TweetID != "t-old-only" {
		t.Fatalf("failed tweets after migration = %+v", failedTweets)
	}
}

func TestMigratePreV7OrderCreatesStorageVersionsBeforeUserEntitiesKindDirs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "upgrade.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-v7 DB where storage_versions doesn't exist yet and migration hasn't run.
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS storage_versions`,
		`DELETE FROM schema_migrations WHERE name = 'user_entities_kind_dirs'`,
	} {
		if _, err := store.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open must run migrateAssets before user_entities_kind_dirs: %v", err)
	}
	defer reopened.Close()
}

func TestVideoThumbImagePathUnicodeDoesNotPanic(t *testing.T) {
	got := videoThumbImagePath("/ext_tw_video/123/İ/pu/VID/avc1/1280x720/abc.mp4", ".jpg")
	want := "/ext_tw_video/123/İ/pu/img/abc.jpg"
	if got != want {
		t.Fatalf("videoThumbImagePath = %q, want %q", got, want)
	}
}

func TestMarkLibraryDownloadsCacheStalePreventsStaleBackfill(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	genBefore := store.libraryCacheGen.Load()
	store.markLibraryDownloadsCacheStale()
	if store.libraryCacheGen.Load() == genBefore {
		t.Fatal("markLibraryDownloadsCacheStale did not increment libraryCacheGen")
	}

	// Attempting to backfill with the older generation must be rejected.
	store.cacheLibraryDownloads([]DownloadRecord{{ID: 1, FilePath: "/tmp/stale.jpg"}}, 100, false, genBefore)
	items, _, err := store.ListLibraryDownloadsWithStatus(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("stale cache backfill leaked items: %+v", items)
	}
}

func TestListLibraryUsersIncludesAttributedOrphansAndCaseInsensitiveFilter(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "orphans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "Alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(root, "users"), "Alice(Alice)"); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "users", "Alice(Alice)", "orphan.jpg")
	writeLibraryTestFile(t, orphan, "hello")

	users, err := store.ListLibraryUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].ScreenName != "Alice" || users[0].MediaCount != 1 || users[0].TotalBytes != 5 {
		t.Fatalf("ListLibraryUsers = %+v, want Alice with orphan counted", users)
	}

	page, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Filter: LibraryFilter{User: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].FilePath != orphan {
		t.Fatalf("case-insensitive orphan user filter = %+v", page.Items)
	}
}

func TestListJobsPageFilteredLargePageDoesNotOverflow(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.CreateJob(ctx, JobKindUser, "alice", "alice"); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListJobsPageFiltered(ctx, 20, math.MaxInt, JobListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Total != 1 {
		t.Fatalf("huge page returned items = %+v, total = %d; want empty items", page.Items, page.Total)
	}
}

func TestSeedDownloadRootsHandlesMissingFilesInRecoveredDir(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "seed.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(oldRoot, "b_existing.jpg")
	if err := os.WriteFile(existing, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	deleted := filepath.Join(oldRoot, "a_deleted.jpg")
	job, err := store.CreateJob(ctx, JobKindMediaURL, "m", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "1", MediaURL: "https://pbs.twimg.com/a.jpg", FilePath: deleted}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "2", MediaURL: "https://pbs.twimg.com/b.jpg", FilePath: existing}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM download_roots; DELETE FROM schema_migrations WHERE name = 'recover_recorded_download_roots'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	roots, err := reopened.DownloadRoots(ctx)
	if err != nil || !containsString(roots, oldRoot) {
		t.Fatalf("oldRoot not recovered: %v %v", roots, err)
	}
	var marked int
	if err := reopened.db.Get(&marked, `SELECT COUNT(*) FROM schema_migrations WHERE name = 'recover_recorded_download_roots'`); err != nil || marked != 1 {
		t.Fatalf("recovery migration not finalized despite recovered parent root: marked=%d err=%v", marked, err)
	}
}

func TestClaimPendingJobsReturnsExactPriorityOrder(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	sched, err := store.CreateJobFromSource(ctx, JobKindUser, "sched", "sched", JobSourceSchedule)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := store.CreateJobFromSource(ctx, JobKindUser, "m1", "m1", JobSourceManual)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := store.CreateJobFromSource(ctx, JobKindUser, "m2", "m2", JobSourceManual)
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimPendingJobs(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 3 || claimed[0].ID != m1.ID || claimed[1].ID != m2.ID || claimed[2].ID != sched.ID {
		t.Fatalf("claimed order = [%d, %d, %d], want [%d, %d, %d]", claimed[0].ID, claimed[1].ID, claimed[2].ID, m1.ID, m2.ID, sched.ID)
	}
}

func TestCancelJobAndZeroRequeueClearAvailableAt(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "requeue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	j1, err := store.CreateJob(ctx, JobKindUser, "a", "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequeueJobAt(ctx, j1.ID, time.Now().Add(time.Hour), "wait"); err != nil {
		t.Fatal(err)
	}
	canceled, err := store.CancelJob(ctx, j1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.AvailableAt != nil || canceled.AvailableNS != 0 {
		t.Fatalf("canceled job kept availableAt/NS: %+v", canceled)
	}

	j2, err := store.CreateJob(ctx, JobKindUser, "b", "b")
	if err != nil {
		t.Fatal(err)
	}
	requeued, err := store.RequeueJobAt(ctx, j2.ID, time.Time{}, "immediate")
	if err != nil {
		t.Fatal(err)
	}
	if requeued.AvailableAt != nil || requeued.AvailableNS != 0 {
		t.Fatalf("zero availableAt stored non-null/non-zero: %+v", requeued)
	}
}

func TestDeleteJobAndDeleteCheckpointCleanCheckpointTasks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	j1, err := store.CreateJob(ctx, JobKindList, "l1", "l1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobCheckpoint(ctx, j1.ID, []byte(`{"next":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobCheckpointTasks(ctx, j1.ID, []byte(`["a"]`)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJobCheckpoint(ctx, j1.ID); err != nil {
		t.Fatal(err)
	}
	var taskRows int
	if err := store.db.Get(&taskRows, `SELECT COUNT(*) FROM job_checkpoint_tasks WHERE job_id = ?`, j1.ID); err != nil || taskRows != 0 {
		t.Fatalf("checkpoint tasks remaining after DeleteJobCheckpoint: %d %v", taskRows, err)
	}

	j2, err := store.CreateJob(ctx, JobKindList, "l2", "l2")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobCheckpoint(ctx, j2.ID, []byte(`{"next":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobCheckpointTasks(ctx, j2.ID, []byte(`["b"]`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(ctx, j2.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJob(ctx, j2.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Get(&taskRows, `SELECT COUNT(*) FROM job_checkpoint_tasks WHERE job_id = ?`, j2.ID); err != nil || taskRows != 0 {
		t.Fatalf("checkpoint tasks remaining after DeleteJob: %d %v", taskRows, err)
	}
}

func TestClaimArchiveSchedulePreservesLastJobIDsWhenDeduplicated(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	schedule, err := store.CreateArchiveSchedule(ctx, newTestSchedule("keep-last", MinArchiveScheduleIntervalMinutes, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.CreateScheduledArchiveJobs(ctx, schedule, time.Now().UTC())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("first claim = %+v %v", jobs, err)
	}

	latest, err := store.GetArchiveSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateJobsForArchiveSchedule(ctx, latest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("expected deduplicated second claim to create 0 jobs, got %+v", second)
	}
	after, err := store.GetArchiveSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.LastJobIDs) != 1 || after.LastJobIDs[0] != jobs[0].ID || after.LastRunStatus != "running" {
		t.Fatalf("lastJobIDs wiped on dedupe: %+v", after)
	}
}

func TestDownloadLookupTrimsTweetID(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "trim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: " 123 ", MediaURL: "https://pbs.twimg.com/media/123.jpg", FilePath: "/tmp/123.jpg", Bytes: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if rec, err := store.GetDownloadByTweetMedia(ctx, " 123 ", "https://pbs.twimg.com/media/123.jpg"); err != nil || rec == nil {
		t.Fatalf("GetDownloadByTweetMedia untrimmed arg: %+v %v", rec, err)
	}
	if rec, _, err := store.GetMediaDownloadState(ctx, " 123 ", "https://pbs.twimg.com/media/123.jpg"); err != nil || rec == nil {
		t.Fatalf("GetMediaDownloadState untrimmed arg: %+v %v", rec, err)
	}
	if has, err := store.HasDownload(ctx, " 123 ", "https://pbs.twimg.com/media/123.jpg"); err != nil || !has {
		t.Fatalf("HasDownload untrimmed arg: %v %v", has, err)
	}
}

func TestSessionValidReadOnlyCheckAndUpdatePasswordClearsSessions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	sessionKey := []byte("session-key-1")
	if err := store.CreateAccount(ctx, Account{Username: "admin", PasswordHash: []byte("hash-1"), SessionKey: sessionKey}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.CreateSession(ctx, "tok-1", now); err != nil {
		t.Fatal(err)
	}
	ok, err := store.SessionValid(ctx, "tok-1", now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("SessionValid = %v %v", ok, err)
	}
	if err := store.UpdateAccountPassword(ctx, sessionKey, []byte("hash-2"), []byte("session-key-2")); err != nil {
		t.Fatal(err)
	}
	ok, err = store.SessionValid(ctx, "tok-1", now.Add(2*time.Minute))
	if err != nil || ok {
		t.Fatalf("old session survived password update: ok=%v err=%v", ok, err)
	}
}

func TestArchiveTweetAtomicAndCycleSafeAndListTweetsSubsecond(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "tweets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "alice", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	// Failure in a sibling related tweet must roll back earlier related tweets too.
	bad := parser.TweetData{
		ID: "parent", Text: "parent", Author: parser.Author{ID: "u1", ScreenName: "alice"},
		Related: []parser.TweetData{
			{ID: "good-related", Text: "good", Author: parser.Author{ID: "u2", ScreenName: "bob"}},
			{ID: "", Text: "broken"},
		},
	}
	if err := store.ArchiveTweet(ctx, job.ID, bad, "pending"); err == nil {
		t.Fatal("expected error for related tweet with empty ID")
	}
	var count int
	if err := store.db.Get(&count, `SELECT COUNT(*) FROM tweets`); err != nil || count != 0 {
		t.Fatalf("partial related tweet committed on error: count=%d err=%v", count, err)
	}

	// Self/mutual cycle in Related must not infinitely recurse.
	baseTime := time.Date(2026, 11, 10, 0, 0, 0, 200_000_000, time.UTC)
	cyclic := parser.TweetData{
		ID: "c1", Text: "c1", Author: parser.Author{ID: "u1", ScreenName: "alice_old", Name: "Alice"},
		CreatedAt: baseTime,
		Related: []parser.TweetData{{
			ID: "c2", Text: "c2", Author: parser.Author{ID: "u1", ScreenName: "alice_new", Name: "Alice New"},
			CreatedAt: baseTime.Add(400 * time.Millisecond),
			Related:   []parser.TweetData{{ID: "c1", Text: "c1"}},
		}},
	}
	if err := store.ArchiveTweet(ctx, job.ID, cyclic, "pending"); err != nil {
		t.Fatalf("cyclic ArchiveTweet: %v", err)
	}
	if err := store.SetTweetMediaResult(ctx, job.ID, "c1", "completed", ""); err != nil {
		t.Fatalf("cyclic SetTweetMediaResult: %v", err)
	}

	// Author screen_name change should yield a single entry in ListTweetAuthors.
	authors, err := store.ListTweetAuthors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].Value != "alice_new" {
		t.Fatalf("ListTweetAuthors = %+v, want single latest alice_new", authors)
	}

	// Sub-second From filter must distinguish .2s from .5s.
	page, err := store.ListTweets(ctx, TweetFilter{From: "2026-11-10T00:00:00.5Z"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "c2" {
		t.Fatalf("subsecond From filter = %+v, want only c2", page.Items)
	}
}

func TestGetMediaSyncIsReadOnlyAndXAccountUnauthorizedWithoutMessage(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "misc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	state, err := store.GetMediaSync(ctx, "missing-user")
	if err != nil || state.UserID != "missing-user" {
		t.Fatalf("GetMediaSync = %+v %v", state, err)
	}
	var rows int
	if err := store.db.Get(&rows, `SELECT COUNT(*) FROM media_sync`); err != nil || rows != 0 {
		t.Fatalf("GetMediaSync inserted row: count=%d err=%v", rows, err)
	}

	acc := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	checked, err := store.ApplyXAccountCheck(ctx, acc.ID, XAccountCheck{Unauthorized: true}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if checked.Active || checked.ErrorMsg == "" {
		t.Fatalf("unauthorized check with empty Err should deactivate account: %+v", checked)
	}
}

func TestSaveStorageDocumentValidationPruningAndFreezeError(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "assets-save.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// Invalid root must be rejected BEFORE inserting into storage_versions.
	badDoc := config.DefaultStorage("/")
	if _, err := store.SaveStorageDocument(ctx, badDoc); err == nil {
		t.Fatal("expected invalid root '/' to fail SaveStorageDocument")
	}
	var versions int
	if err := store.db.Get(&versions, `SELECT COUNT(*) FROM storage_versions`); err != nil || versions != 0 {
		t.Fatalf("storage_versions count = %d (err=%v), want 0 after validation failure", versions, err)
	}

	// Valid save must keep saved roots and prune excess stale roots down to MaxDownloadRoots.
	now := time.Now().UTC()
	for i := 0; i < MaxDownloadRoots+5; i++ {
		fake := filepath.Join(os.TempDir(), "xd-assets-prune-"+string(rune('a'+i)))
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, fake, now.Add(-time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	targetRoot := t.TempDir()
	goodDoc := config.DefaultStorage(targetRoot)
	if _, err := store.SaveStorageDocument(ctx, goodDoc); err != nil {
		t.Fatal(err)
	}
	roots, err := store.DownloadRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Protected roots (targetRoot + current app_config.download_dir) are always kept;
	// MaxDownloadRoots caps unreferenced historical roots only.
	if len(roots) > MaxDownloadRoots+2 || !containsString(roots, filepath.Clean(targetRoot)) {
		t.Fatalf("roots after SaveStorageDocument prune = %v", roots)
	}

	// FreezeJobStorage when SaveStorageDocument fails must wrap the non-nil saveErr.
	store2, err := Open(filepath.Join(t.TempDir(), "freeze-err.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, err := store2.db.Exec(`CREATE TRIGGER fail_sv_insert BEFORE INSERT ON storage_versions BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	job, err := store2.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store2.FreezeJobStorage(ctx, job.ID)
	if err == nil || !containsSubstring(err.Error(), "初始化存储策略失败") || containsSubstring(err.Error(), "%!w(<nil>)") {
		t.Fatalf("FreezeJobStorage err = %v, want real wrapped saveErr", err)
	}
}

func containsSubstring(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexString(s, sub) >= 0)
}

func indexString(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestLibrarySnapshotDedupsUsersCaseInsensitivelyAndPropagatesTruncated(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lib-trunc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "Alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u2", ScreenName: "alice", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(root, "users"), "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u2", filepath.Join(root, "users"), "alice"); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Users) != 1 {
		t.Fatalf("case-insensitive duplicate users in facet: %+v", page.Users)
	}

	// When snapshot entries are truncated (>MaxLibraryDownloadsLimit), ListLibraryDownloadsPage
	// falls back to SQL pagination/querying so counts and pages remain exact and untruncated.
	store.librarySnapMu.Lock()
	store.librarySnap = &librarySnapshot{
		gen:              store.libraryCacheGen.Load(),
		at:               time.Now(),
		entries:          []DownloadRecord{{ID: 1, FilePath: "/tmp/a.jpg", MediaURL: "https://pbs.twimg.com/a.jpg", CreatedAt: time.Now()}},
		entriesTruncated: true,
		prefixIndex:      map[string]int{},
	}
	store.librarySnapMu.Unlock()
	page, err = store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Truncated {
		t.Fatal("expected SQL fallback to return page.Truncated = false when snap.entriesTruncated = true")
	}
	if page.Pagination.Total != 0 || page.Counts["all"] != 0 {
		t.Fatalf("SQL fallback should query real DB (0 downloads), got total=%d counts=%v", page.Pagination.Total, page.Counts)
	}
}

func TestArchiveTweetGrandchildVariantFallbackPartialAndSyncUpsert(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "tweets-extra.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "alice", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	variantURL := "https://video.twimg.com/ext_tw_video/999/pu/vid/720x720/clip.mp4"
	rootTweet := parser.TweetData{
		ID: "root-1", Text: "root", Author: parser.Author{ID: "u1", ScreenName: "alice"},
		Media: []parser.Media{{
			Type:     parser.MediaVideo,
			Variants: []parser.MediaVariant{{URL: variantURL, Bitrate: 1000, ContentType: "video/mp4"}},
		}},
		Related: []parser.TweetData{{
			ID: "mid-1", Text: "mid", Author: parser.Author{ID: "u2", ScreenName: "bob"},
			Related: []parser.TweetData{{
				ID: "grand-1", Text: "grand", Author: parser.Author{ID: "u3", ScreenName: "carol"},
				Media: []parser.Media{
					{ID: "m-match", Type: parser.MediaVideo, Variants: []parser.MediaVariant{{URL: variantURL, Bitrate: 1000, ContentType: "video/mp4"}}},
					{ID: "m-other", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/other.jpg"},
				},
			}},
		}},
	}
	if err := store.ArchiveTweet(ctx, job.ID, rootTweet, "pending"); err != nil {
		t.Fatal(err)
	}
	grand, err := store.GetTweet(ctx, "grand-1")
	if err != nil {
		t.Fatal(err)
	}
	if grand.MediaStatus != "pending" {
		t.Fatalf("grandchild mediaStatus = %q, want pending", grand.MediaStatus)
	}
	// Verify variant-only root media was persisted to archive_media and can update media_type on conflict.
	var mediaType string
	if err := store.db.Get(&mediaType, `SELECT media_type FROM archive_media WHERE id = ?`, variantURL); err != nil || mediaType != "video" {
		t.Fatalf("archive_media variant fallback = %q %v, want video", mediaType, err)
	}
	rootTweet.Media[0].Type = parser.MediaGIF
	if err := store.ArchiveTweet(ctx, job.ID, rootTweet, "pending"); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Get(&mediaType, `SELECT media_type FROM archive_media WHERE id = ?`, variantURL); err != nil || mediaType != string(parser.MediaGIF) {
		t.Fatalf("archive_media ON CONFLICT did not update media_type = %q %v", mediaType, err)
	}

	// Partial download of grand-1 (1 of 2 media downloaded) must yield "partial", not "completed".
	if _, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: "grand-1", MediaURL: variantURL, FilePath: "/tmp/clip.mp4", Bytes: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTweetMediaResult(ctx, job.ID, "root-1", "completed", ""); err != nil {
		t.Fatal(err)
	}
	grand, err = store.GetTweet(ctx, "grand-1")
	if err != nil {
		t.Fatal(err)
	}
	if grand.MediaStatus != "partial" {
		t.Fatalf("grandchild partial mediaStatus = %q, want partial", grand.MediaStatus)
	}

	// LinkRelatedTweetMedia with empty media URL/ID must not match unrelated empty-URL media.
	if _, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: "root-1", MediaURL: "", FilePath: "/tmp/empty.bin", Bytes: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkRelatedTweetMedia(ctx, job.ID, parser.TweetData{
		ID: "root-1",
		Related: []parser.TweetData{{
			ID:    "grand-1",
			Media: []parser.Media{{ID: "", URL: ""}},
		}},
	}, parser.Media{}); err != nil {
		t.Fatal(err)
	}

	// SaveTweetSync must upsert a previously absent user_id.
	if err := store.SaveTweetSync(ctx, TweetSync{UserID: "new-sync-user", LastID: "555", Status: "ok", Count: 3}); err != nil {
		t.Fatal(err)
	}
	synced, err := store.GetTweetSync(ctx, "new-sync-user")
	if err != nil || synced.LastID != "555" || synced.Count != 3 {
		t.Fatalf("SaveTweetSync upsert = %+v %v", synced, err)
	}

	// RetryTweetMedia on a tweet without URL in payload must synthesize canonical web URL.
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{ID: "8888", Text: "no url"}, "failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryTweetMedia(ctx, "8888")
	if err != nil {
		t.Fatal(err)
	}
	if retried.Input != "https://x.com/i/web/status/8888" {
		t.Fatalf("RetryTweetMedia synthesized input = %q", retried.Input)
	}
}

func TestXAccountAdditionalFixesAndPermanentFailedMedia(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "xa-fixes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// UpdateXAccount must reject unsetting the last primary; simulate legacy primaries == 0 via SQL
	// to verify CreateXAccount still promotes when primaries == 0.
	a1 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-1", CSRFToken: "c1"})
	notPrimary := false
	if _, err := store.UpdateXAccount(ctx, a1.ID, XAccountPatch{IsPrimary: &notPrimary}); err == nil {
		t.Fatal("expected UpdateXAccount to reject unsetting the last primary account")
	}
	if _, err := store.db.Exec(`UPDATE x_accounts SET is_primary = 0 WHERE id = ?`, a1.ID); err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("UTC+8", 8*3600)
	nonUTC := time.Date(2026, 11, 20, 12, 0, 0, 0, loc)
	a2, err := store.CreateXAccount(ctx, XAccount{
		AuthToken:  "at-2",
		CSRFToken:  "c2",
		LastUsed:   sql.NullTime{Time: nonUTC, Valid: true},
		VerifiedAt: sql.NullTime{Time: nonUTC, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !a2.IsPrimary {
		t.Fatal("CreateXAccount must promote when primaries == 0")
	}
	if a2.LastUsed.Time.Location() != time.UTC || !a2.LastUsed.Time.Equal(nonUTC.UTC()) {
		t.Fatalf("LastUsed not UTC: %v", a2.LastUsed.Time)
	}

	// UpdateXAccount with unchanged credentials must NOT wipe verified_at or '*' lock.
	if err := store.LockXAccount(ctx, a2.ID, "*", time.Now().UTC().Add(time.Hour), "401"); err != nil {
		t.Fatal(err)
	}
	sameTok, sameCT0 := " at-2 ", " c2 "
	updated, err := store.UpdateXAccount(ctx, a2.ID, XAccountPatch{AuthToken: &sameTok, CSRFToken: &sameCT0})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.VerifiedAt.Valid {
		t.Fatal("unchanged credentials wiped verified_at")
	}
	locks, err := store.ListXAccountLocks(ctx, time.Now().UTC())
	if err != nil || len(locks) != 1 || locks[0].Queue != "*" {
		t.Fatalf("unchanged credentials wiped '*' lock: %+v %v", locks, err)
	}

	// upsertEnvXAccount transitioning ct0 or source must clear verified_at and '*' lock.
	if err := store.SyncEnvXAccounts(ctx, []EnvXAccount{{AuthToken: "at-2", CSRFToken: "c2", Primary: true}}); err != nil {
		t.Fatal(err)
	}
	envAcc, err := store.GetXAccount(ctx, a2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if envAcc.Source != "env" || envAcc.VerifiedAt.Valid {
		t.Fatalf("transition db->env should clear verified_at: %+v", envAcc)
	}
	locks, err = store.ListXAccountLocks(ctx, time.Now().UTC())
	if err != nil || len(locks) != 0 {
		t.Fatalf("transition db->env should clear '*' lock: %+v %v", locks, err)
	}

	// SetXAccountError must not overwrite manual deactivation sentinel (active=0, error_msg='').
	inactive := false
	if _, err := store.UpdateXAccount(ctx, a1.ID, XAccountPatch{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetXAccountError(ctx, a1.ID, "transient err"); err != nil {
		t.Fatal(err)
	}
	manualAcc, err := store.GetXAccount(ctx, a1.ID)
	if err != nil || manualAcc.Active || manualAcc.ErrorMsg != "" {
		t.Fatalf("SetXAccountError clobbered manual deactivation: %+v %v", manualAcc, err)
	}

	// HasPermanentFailedMediaForURL distinguishes permanent vs transient failed_media rows.
	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedMedia(ctx, FailedMedia{JobID: job.ID, MediaURL: "https://pbs.twimg.com/transient.jpg", Error: "HTTP 503 Service Unavailable"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedMedia(ctx, FailedMedia{JobID: job.ID, MediaURL: "https://pbs.twimg.com/forbidden.jpg", Error: "HTTP 403 Forbidden"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedMedia(ctx, FailedMedia{JobID: job.ID, MediaURL: "https://pbs.twimg.com/gone404.jpg", Error: "download failed: HTTP 404 Not Found"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedMedia(ctx, FailedMedia{JobID: job.ID, MediaURL: "https://pbs.twimg.com/dmca.jpg", Error: "HTTP 403 DMCA takedown"}); err != nil {
		t.Fatal(err)
	}
	for url, want := range map[string]bool{
		"https://pbs.twimg.com/missing.jpg":   false,
		"https://pbs.twimg.com/transient.jpg": false,
		"https://pbs.twimg.com/forbidden.jpg": false,
		"https://pbs.twimg.com/gone404.jpg":   true,
		"https://pbs.twimg.com/dmca.jpg":      true,
	} {
		got, err := store.HasPermanentFailedMediaForURL(ctx, url)
		if err != nil || got != want {
			t.Fatalf("HasPermanentFailedMediaForURL(%q) = %v, %v; want %v", url, got, err, want)
		}
	}
}
