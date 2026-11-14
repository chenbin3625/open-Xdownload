package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newDeleteJobTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestDeleteJobRemovesTerminalJobAndRelatedRows(t *testing.T) {
	store := newDeleteJobTestStore(t)
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "someone", "用户 @someone")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	job.Status = JobFailed
	job.Progress = 1
	job.Error = "boom"
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	// SQLite 连接默认不开启 foreign_keys，级联删除要靠 DeleteJob 显式清理。
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at) VALUES (?, 't1', 'https://pbs.twimg.com/a.jpg', '/downloads/a.jpg', 1, ?)`,
		job.ID, time.Now().UTC()); err != nil {
		t.Fatalf("insert download: %v", err)
	}

	if err := store.DeleteJob(ctx, job.ID); err != nil {
		t.Fatalf("delete job: %v", err)
	}

	if _, err := store.GetJob(ctx, job.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetJob after delete = %v, want sql.ErrNoRows", err)
	}

	var downloads int
	if err := store.db.GetContext(ctx, &downloads, `SELECT COUNT(*) FROM downloads WHERE job_id = ?`, job.ID); err != nil {
		t.Fatalf("count downloads: %v", err)
	}
	if downloads != 0 {
		t.Fatalf("downloads left = %d, want 0", downloads)
	}
}

func TestDeleteJobRejectsActiveJob(t *testing.T) {
	store := newDeleteJobTestStore(t)
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "someone", "")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	err = store.DeleteJob(ctx, job.ID)
	if !errors.Is(err, ErrJobActive) {
		t.Fatalf("delete active job = %v, want ErrJobActive", err)
	}
	if _, err := store.GetJob(ctx, job.ID); err != nil {
		t.Fatalf("active job must survive delete: %v", err)
	}
}

func TestDeleteJobMissingReturnsNoRows(t *testing.T) {
	store := newDeleteJobTestStore(t)

	err := store.DeleteJob(context.Background(), 999999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("delete missing job = %v, want sql.ErrNoRows", err)
	}
}
