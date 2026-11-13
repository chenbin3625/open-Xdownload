package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/jmoiron/sqlx"
)

func TestOpenMigratesJobsBeforeCreatingQueueIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sqlx.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, status TEXT NOT NULL,
			input TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', progress REAL NOT NULL DEFAULT 0,
			message TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
		);
		INSERT INTO jobs(kind, status, input, created_at, updated_at)
		VALUES ('user', 'pending', 'alice', '2026-01-01 00:00:00', '2026-01-01 00:00:00');
		PRAGMA user_version = 7;
	`)
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy jobs schema: %v", err)
	}
	defer store.Close()
	jobs, err := store.ClaimPendingJobs(context.Background(), 1)
	if err != nil || len(jobs) != 1 || jobs[0].Input != "alice" {
		t.Fatalf("migrated queue = %+v, %v", jobs, err)
	}
}

func TestAutomaticRetryReuseResetsBudgetAndKeepsAssetReferences(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.ReserveAutomaticRetryJob(ctx)
	if err != nil || job == nil {
		t.Fatalf("reserve = %+v, %v", job, err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET status = ?, deferred_seconds = 86400 WHERE id = ?`, JobCompleted, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = store.ReserveAutomaticRetryJob(ctx)
	if err != nil || job == nil || job.DeferredSeconds != 0 {
		t.Fatalf("reused retry = %+v, %v", job, err)
	}
	if err := store.SetMediaAsset(ctx, MediaAsset{MediaKey: "media", Kind: config.AssetImage, Status: "failed", JobID: job.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`, JobCompleted, time.Now().Add(-48*time.Hour), job.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := store.PruneAutomaticRetryJobs(ctx, time.Now().Add(-24*time.Hour)); err != nil || count != 0 {
		t.Fatalf("prune referenced job = %d, %v", count, err)
	}
}

func TestLibraryDateFiltersAcceptStoredTimeFormats(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 12, 0, 0, 123456789, time.UTC)
	for i, stored := range []any{at, at.Format(time.RFC3339Nano), "2026-11-12 12:00:00"} {
		path := filepath.Join(root, string(rune('a'+i))+".jpg")
		writeLibraryTestFile(t, path, "image")
		record, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, MediaURL: "https://pbs.twimg.com/media/" + filepath.Base(path), FilePath: path, Bytes: 5})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE downloads SET created_at = ? WHERE id = ?`, stored, record.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, filter := range []LibraryFilter{{From: at.Add(-time.Hour)}, {To: at.Add(time.Hour)}, {From: at.Add(-time.Hour), To: at.Add(time.Hour)}} {
		page, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 10, Filter: filter})
		if err != nil || page.Pagination.Total != 3 {
			t.Fatalf("date filter %+v returned %d files, %v", filter, page.Pagination.Total, err)
		}
	}
}
