package storage

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

func TestPruneAutomaticRetryJobsRetainsDownloads(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.ReserveAutomaticRetryJob(ctx)
	if err != nil || job == nil {
		t.Fatalf("reserve: %v %v", job, err)
	}
	record, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: "123", MediaURL: "https://pbs.twimg.com/media/archive.jpg", FilePath: filepath.Join(t.TempDir(), "archive.jpg"), Bytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	job.Status = JobCompleted
	if err := store.UpdateJob(ctx, *job); err != nil {
		t.Fatal(err)
	}
	if pruned, err := store.PruneAutomaticRetryJobs(ctx, time.Now().Add(time.Hour)); err != nil || pruned != 0 {
		t.Fatalf("pruned task containing downloads: %d %v", pruned, err)
	}
	if got, err := store.GetDownload(ctx, record.ID); err != nil || got == nil {
		t.Fatalf("successful download lost: %v %v", got, err)
	}
	if reused, err := store.ReserveAutomaticRetryJob(ctx); err != nil || reused == nil || reused.ID != job.ID {
		t.Fatalf("retained task cannot be reused: %v %v", reused, err)
	}
}

func TestLibraryPageAttributesOrphansAndRefreshesUsers(t *testing.T) {
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
	writeLibraryTestFile(t, filepath.Join(root, "users", "Alice(alice)", "orphan.jpg"), "data")
	unknown, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Filter: LibraryFilter{User: "unknown"}})
	if err != nil || unknown.Pagination.Total != 1 {
		t.Fatalf("initial unknown page: %+v %v", unknown, err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(root, "users"), "Alice(alice)"); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Filter: LibraryFilter{User: "alice"}})
	if err != nil || page.Pagination.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("alice page: %+v %v", page, err)
	}
	if page.Items[0].UserScreenName != "alice" || page.Items[0].UserName != "Alice" {
		t.Fatalf("orphan attribution: %+v", page.Items[0])
	}
	unknown, err = store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Filter: LibraryFilter{User: "unknown"}})
	if err != nil || unknown.Pagination.Total != 0 {
		t.Fatalf("attributed file remains unknown: %+v %v", unknown, err)
	}
	for _, user := range unknown.Users {
		if user.Value == "unknown" {
			t.Fatal("unknown facet includes only attributed files")
		}
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice Updated"}); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Filter: LibraryFilter{User: "alice"}})
	if err != nil || len(page.Items) != 1 || page.Items[0].UserName != "Alice Updated" {
		t.Fatalf("user update left stale attribution: %+v %v", page, err)
	}
}

func TestLibraryPagePreservesDistinctNormalizedPaths(t *testing.T) {
	for _, tc := range []struct{ name, recorded, orphan string }{
		{"case", "a.jpg", "A.jpg"},
		{"separator", filepath.Join("nested", "a.jpg"), `nested\a.jpg`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "separator" && runtime.GOOS == "windows" {
				t.Skip("backslash is a path separator on Windows")
			}
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
			first, second := filepath.Join(root, tc.recorded), filepath.Join(root, tc.orphan)
			writeLibraryTestFile(t, first, "first")
			writeLibraryTestFile(t, second, "second")
			firstInfo, err := os.Stat(first)
			if err != nil {
				t.Fatal(err)
			}
			secondInfo, err := os.Stat(second)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if os.SameFile(firstInfo, secondInfo) {
				want = 1
			}
			job, err := store.CreateJob(ctx, JobKindUser, "alice", "archive")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, MediaURL: "https://pbs.twimg.com/media/a.jpg", FilePath: first}); err != nil {
				t.Fatal(err)
			}
			page, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{})
			if err != nil || page.Pagination.Total != want || len(page.Items) != want {
				t.Fatalf("distinct files lost: %+v %v; want %d", page, err, want)
			}
		})
	}
}
