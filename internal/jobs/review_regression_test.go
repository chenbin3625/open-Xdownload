package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

func TestAutomaticRetryOnlySchedulesDueFailures(t *testing.T) {
	for _, scenario := range []string{"empty", "cooling", "due"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			root := t.TempDir()
			if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root, AutoRetryFailed: true}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario != "empty" {
				job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.UpsertUser(ctx, storage.User{ID: "u1", ScreenName: "alice"}); err != nil {
					t.Fatal(err)
				}
				entity, err := store.EnsureUserEntity(ctx, "u1", root, "alice")
				if err != nil {
					t.Fatal(err)
				}
				failed, err := store.CreateFailedTweet(ctx, storage.FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "t1", Payload: `{}`})
				if err != nil {
					t.Fatal(err)
				}
				want = 1
				if scenario == "cooling" {
					if err := store.TouchFailedTweet(ctx, failed.ID, "retry later"); err != nil {
						t.Fatal(err)
					}
				} else {
					want++
				}
			}
			manager := NewManager(store, nil, NewEventBus())
			manager.active[-1] = func() {}
			manager.runOnce(ctx)
			jobs, err := store.ListJobs(ctx, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != want {
				t.Fatalf("jobs = %d, want %d for %s queue", len(jobs), want, scenario)
			}
		})
	}
}

func TestLongUserDisplayNamesKeepDistinctDirectoriesAndLinks(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	manager := NewManager(store, nil, NewEventBus())
	list, err := manager.ensureListEntity(ctx, cfg, "lists", "l1", "list(l1)")
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, screen := range []string{"alice", "bob"} {
		user := xclient.User{ID: "user-" + screen, ScreenName: screen, Name: strings.Repeat("\U0001f600", 50)}
		if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
			t.Fatal(err)
		}
		entity, dir, err := manager.ensureUserEntity(ctx, cfg, user)
		if err != nil {
			t.Fatal(err)
		}
		if paths[dir] || !strings.HasSuffix(entity.Name, "("+screen+")") || len(entity.Name) > 180 {
			t.Fatalf("directory lost account identity: %q", entity.Name)
		}
		paths[dir] = true
		if err := manager.ensureUserLink(ctx, cfg, list, user, dir); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(list.ParentDir, list.Name, entity.Name)
		if target, err := os.Readlink(link); err != nil || target != dir {
			t.Fatalf("user link = %q, %v, want %q", target, err, dir)
		}
	}
}

func TestEnsureUserEntityRejectsRenamingSharedLegacyDirectory(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	parent := filepath.Join(cfg.DownloadDir, "users")
	sharedName := safeName(strings.Repeat("\U0001f600", 50))
	sharedDir := filepath.Join(parent, sharedName)
	if err := os.MkdirAll(sharedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, screen := range []string{"alice", "bob"} {
		if _, err := store.UpsertUser(ctx, storage.User{ID: screen, ScreenName: screen}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnsureUserEntity(ctx, screen, parent, sharedName); err != nil {
			t.Fatal(err)
		}
	}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "bob", "bob")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sharedDir, "bob.jpg")
	if err := os.WriteFile(file, []byte("bob"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: job.ID, TweetID: "t1", MediaURL: "https://pbs.twimg.com/media/bob.jpg", FilePath: file})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, nil, NewEventBus())
	user := xclient.User{ID: "alice", ScreenName: "alice", Name: strings.Repeat("\U0001f600", 50)}
	if _, _, err := manager.ensureUserEntity(ctx, cfg, user); err == nil {
		t.Fatal("shared legacy directory accepted for rename or continued writes")
	}
	for _, screen := range []string{"alice", "bob"} {
		entity, err := store.LocateUserEntity(ctx, screen, parent)
		if err != nil || entity == nil || entity.Name != sharedName {
			t.Fatalf("shared entity changed: %+v, %v", entity, err)
		}
	}
	got, err := store.GetDownload(ctx, record.ID)
	if err != nil || got == nil || got.FilePath != file {
		t.Fatalf("other user's download moved: %+v, %v", got, err)
	}
	if body, err := os.ReadFile(file); err != nil || string(body) != "bob" {
		t.Fatalf("other user's file changed: %q, %v", body, err)
	}
}

func TestEnsureUserEntityRejectsDirectoryOwnedByAnotherUser(t *testing.T) {
	for _, scenario := range []string{"new", "unchanged", "renamed"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			cfg := config.AppConfig{DownloadDir: t.TempDir()}
			parent := filepath.Join(cfg.DownloadDir, "users")
			user := xclient.User{ID: "alice", ScreenName: "alice", Name: "Alice"}
			name := safeName(user.Title())
			for _, id := range []string{"alice", "bob"} {
				if _, err := store.UpsertUser(ctx, storage.User{ID: id, ScreenName: id}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.EnsureUserEntity(ctx, "bob", parent, name); err != nil {
				t.Fatal(err)
			}
			oldName := name
			if scenario == "renamed" {
				oldName = "Before(alice)"
			}
			if scenario != "new" {
				if _, err := store.EnsureUserEntity(ctx, "alice", parent, oldName); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(parent, oldName), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			manager := NewManager(store, nil, NewEventBus())
			if _, _, err := manager.ensureUserEntity(ctx, cfg, user); err == nil {
				t.Fatal("directory already owned by another user was accepted")
			}
			entity, err := store.LocateUserEntity(ctx, "alice", parent)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "new" {
				if entity != nil {
					t.Fatalf("conflicting entity was created: %+v", entity)
				}
			} else if entity == nil || entity.Name != oldName {
				t.Fatalf("entity changed on conflict: %+v", entity)
			}
		})
	}
}

func TestUserListLinkKeepsActualDirectoryNameWhenRenameFails(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir()}
	manager := NewManager(store, nil, NewEventBus())
	user := xclient.User{ID: "u1", ScreenName: "alice", Name: "Before"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	entity, oldDir, err := manager.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	list, err := manager.ensureListEntity(ctx, cfg, "lists", "l1", "list(l1)")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ensureUserLink(ctx, cfg, list, user, oldDir); err != nil {
		t.Fatal(err)
	}
	user.Name = "After"
	if err := os.MkdirAll(filepath.Join(entity.ParentDir, safeName(user.Title())), 0o700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, actualDir, err := manager.ensureUserEntity(ctx, cfg, user)
		if err != nil || actualDir != oldDir {
			t.Fatalf("rename fallback = %q, %v, want %q", actualDir, err, oldDir)
		}
		if err := manager.ensureUserLink(ctx, cfg, list, user, actualDir); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(list.ParentDir, list.Name))
	if err != nil || len(entries) != 1 || entries[0].Name() != entity.Name {
		t.Fatalf("list links = %v, %v, want only %q", entries, err, entity.Name)
	}
	links, err := store.GetUserLinkTargets(ctx, user.ID)
	if err != nil || len(links) != 1 || links[0].Name != entity.Name {
		t.Fatalf("stored links = %+v, %v, want actual directory name", links, err)
	}
}
