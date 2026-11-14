package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTouchTweetSyncCreatesRowAndPreservesFields(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.TouchTweetSync(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	times, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if times["u1"].IsZero() {
		t.Fatalf("u1 updated_at not recorded: %+v", times)
	}

	if err := store.SaveTweetSync(ctx, TweetSync{
		UserID: "u1", LastID: "100", Status: "completed", Count: 7,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchTweetSync(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetTweetSync(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 7 || state.Status != "completed" || state.LastID != "100" {
		t.Fatalf("TouchTweetSync clobbered fields: %+v", state)
	}
}

func TestListTweetSyncUpdatedAtDoesNotInsertRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	times, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(times) != 0 {
		t.Fatalf("times = %+v, want empty", times)
	}
	rows, err := store.ListTweetSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListTweetSyncUpdatedAt inserted rows: %+v", rows)
	}
}
