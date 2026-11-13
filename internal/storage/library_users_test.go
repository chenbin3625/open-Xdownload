package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newLibraryUsersTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestListLibraryUsersAggregatesByScreenName(t *testing.T) {
	store := newLibraryUsersTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	job, err := store.CreateJob(ctx, JobKindUser, "someone", "用户 @someone")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	job.Status = JobCompleted
	job.Progress = 1
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO users (id, screen_name, name, avatar_url, updated_at) VALUES ('u1', 'someone', '某人', 'https://pbs.twimg.com/a_400x400.jpg', ?)`,
		now); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// 下载记录本身不带作者，归属要靠 user_entities 的目录前缀推断。
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO user_entities (user_id, name, parent_dir, updated_at) VALUES ('u1', 'someone', '/downloads/users', ?)`,
		now); err != nil {
		t.Fatalf("insert user entity: %v", err)
	}
	for index, size := range []int64{100, 200} {
		path := "/downloads/users/someone/media_" + string(rune('a'+index)) + ".jpg"
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			job.ID, "t"+string(rune('1'+index)), "https://pbs.twimg.com/"+string(rune('a'+index))+".jpg", path, size, now); err != nil {
			t.Fatalf("insert download: %v", err)
		}
	}

	items, err := store.ListLibraryUsers(ctx)
	if err != nil {
		t.Fatalf("list library users: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("users = %d (%+v), want 1", len(items), items)
	}
	got := items[0]
	if got.ScreenName != "someone" {
		t.Errorf("screenName = %q, want someone", got.ScreenName)
	}
	if got.DisplayName != "某人" {
		t.Errorf("displayName = %q, want 某人", got.DisplayName)
	}
	if got.AvatarURL != "https://pbs.twimg.com/a_400x400.jpg" {
		t.Errorf("avatarUrl = %q", got.AvatarURL)
	}
	if got.MediaCount != 2 {
		t.Errorf("mediaCount = %d, want 2", got.MediaCount)
	}
	if got.TotalBytes != 300 {
		t.Errorf("totalBytes = %d, want 300", got.TotalBytes)
	}
	if !got.LatestAt.Equal(now) {
		t.Errorf("latestAt = %v, want %v", got.LatestAt, now)
	}
}

func TestListLibraryUsersSkipsUnattributedDownloads(t *testing.T) {
	store := newLibraryUsersTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	job, err := store.CreateJob(ctx, JobKindUser, "someone", "")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	job.Status = JobCompleted
	job.Progress = 1
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	// 既没有 tweets 关联，也不匹配任何 user_entities 前缀 → 不产生用户卡片。
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at) VALUES (?, 't1', 'https://pbs.twimg.com/x.jpg', '/downloads/misc/x.jpg', 10, ?)`,
		job.ID, now); err != nil {
		t.Fatalf("insert download: %v", err)
	}

	items, err := store.ListLibraryUsers(ctx)
	if err != nil {
		t.Fatalf("list library users: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("users = %+v, want none", items)
	}
}
