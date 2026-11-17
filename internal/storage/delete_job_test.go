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

	// DeleteJob 显式清理以 job_id 关联的索引行，而不是只依赖外键级联。
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at) VALUES (?, 't1', 'https://pbs.twimg.com/a.jpg', '/downloads/a.jpg', 1, ?)`,
		job.ID, now); err != nil {
		t.Fatalf("insert download: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO users (id, screen_name, name, protected, friends_count, media_count, updated_at) VALUES ('u1', 'someone', 'Someone', 0, 0, 0, ?)`,
		now); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	entity, err := store.EnsureUserEntity(ctx, "u1", "", "Someone")
	if err != nil {
		t.Fatalf("ensure user entity: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO tweets (id, author_id, screen_name, author_name, body, published_at, payload, archived_at, updated_at) VALUES ('t2', 'u1', 'someone', 'Someone', '', ?, '{}', ?, ?)`,
		now, now, now); err != nil {
		t.Fatalf("insert tweet: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO failed_tweets (job_id, entity_id, tweet_id, payload, error, created_at, updated_at) VALUES (?, ?, 't2', '{}', 'boom', ?, ?)`,
		job.ID, entity.ID, now, now); err != nil {
		t.Fatalf("insert failed tweet: %v", err)
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

	var failedTweets int
	if err := store.db.GetContext(ctx, &failedTweets, `SELECT COUNT(*) FROM failed_tweets WHERE job_id = ?`, job.ID); err != nil {
		t.Fatalf("count failed tweets: %v", err)
	}
	if failedTweets != 0 {
		t.Fatalf("failed tweets left = %d, want 0", failedTweets)
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
