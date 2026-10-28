package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestUpgradeRequiresAccountSetupAndPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "alice", "existing archive")
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the previous version's database without the local account table.
	if _, err := store.db.Exec(`DROP TABLE app_account`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, err := store.GetAccount(ctx)
	if err != nil || account != nil {
		t.Fatalf("upgrade should require setup: %+v %v", account, err)
	}
	existing, err := store.GetJob(ctx, job.ID)
	if err != nil || existing.Title != job.Title {
		t.Fatalf("upgrade lost existing job: %+v %v", existing, err)
	}
	var version int
	if err := store.db.Get(&version, `PRAGMA user_version`); err != nil || version != 6 {
		t.Fatalf("schema version = %d: %v", version, err)
	}
}
