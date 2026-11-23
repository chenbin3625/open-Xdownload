package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

// Item #1 (High #12): migrateUserEntitiesToKindDirs handles storage_versions ErrNoRows
// (falling back to config.DefaultStorage + OPEN_XDOWNLOAD_DOWNLOAD_DIR) and respects
// each kind rule's custom target.Root.
func TestMigrateUserEntitiesToKindDirsFallbackAndCustomTargetRoots(t *testing.T) {
	t.Run("storage_versions empty falls back to DefaultStorage and env download dir", func(t *testing.T) {
		envRoot := filepath.Join(t.TempDir(), "env-downloads")
		t.Setenv(config.EnvDownloadDir, envRoot)

		dbPath := filepath.Join(t.TempDir(), "legacy-env.db")
		store, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
			t.Fatal(err)
		}
		// Simulate pre-migration DB: remove migration marker, ensure storage_versions is empty,
		// and insert legacy user_entities row under envRoot.
		if _, err := store.db.Exec(`DELETE FROM storage_versions`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE name = 'user_entities_kind_dirs'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO user_entities(user_id, parent_dir, name, updated_at) VALUES ('u1', ?, 'alice', '2026-01-01T00:00:00Z')`, filepath.Join(envRoot, "users")); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}

		// Re-open triggers migrateUserEntitiesToKindDirs.
		reopened, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()

		var parents []string
		if err := reopened.db.Select(&parents, `SELECT parent_dir FROM user_entities WHERE user_id = 'u1' ORDER BY parent_dir`); err != nil {
			t.Fatal(err)
		}
		wantImages := filepath.Join(envRoot, "image")
		wantVideos := filepath.Join(envRoot, "video")
		if !containsString(parents, wantImages) || !containsString(parents, wantVideos) {
			t.Fatalf("migrated parent_dirs = %v, want %q and %q", parents, wantImages, wantVideos)
		}
	})

	t.Run("uses each kind rule target.Root from document.Rule(kind)", func(t *testing.T) {
		baseRoot := filepath.Join(t.TempDir(), "base")
		imgRoot := filepath.Join(t.TempDir(), "img-disk")
		vidRoot := filepath.Join(t.TempDir(), "vid-disk")
		for _, d := range []string{baseRoot, imgRoot, vidRoot} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}

		dbPath := filepath.Join(t.TempDir(), "multi-root.db")
		store, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: baseRoot}); err != nil {
			t.Fatal(err)
		}
		doc := config.DefaultStorage(baseRoot)
		doc.Targets = []config.StorageTarget{
			{ID: "local-img", Type: "local", Name: "Images", Root: imgRoot},
			{ID: "local-vid", Type: "local", Name: "Videos", Root: vidRoot},
		}
		doc.Default = config.AssetRule{
			TargetID:          "local-img",
			DirectoryTemplate: "{kind}/{username}",
			FilenameTemplate:  "{tweet_id}_{media_id}_{index}.{ext}",
		}
		doc.Rules = map[config.AssetKind]config.AssetRule{
			config.AssetImage: {
				TargetID:          "local-img",
				DirectoryTemplate: "photos/{kind}/{username}",
				FilenameTemplate:  "{tweet_id}_{media_id}_{index}.{ext}",
			},
			config.AssetVideo: {
				TargetID:          "local-vid",
				DirectoryTemplate: "clips/{kind}/{username}",
				FilenameTemplate:  "{tweet_id}_{media_id}_{index}.{ext}",
			},
		}
		if doc, err = store.SaveStorageDocument(ctx, doc); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertUser(ctx, User{ID: "u2", ScreenName: "bob", Name: "Bob"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE name = 'user_entities_kind_dirs'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO user_entities(user_id, parent_dir, name, updated_at) VALUES ('u2', ?, 'bob', '2026-01-01T00:00:00Z')`, filepath.Join(baseRoot, "users")); err != nil {
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

		var parents []string
		if err := reopened.db.Select(&parents, `SELECT parent_dir FROM user_entities WHERE user_id = 'u2' ORDER BY parent_dir`); err != nil {
			t.Fatal(err)
		}
		wantImgParent := filepath.Join(imgRoot, "photos", "image")
		wantVidParent := filepath.Join(vidRoot, "clips", "video")
		if !containsString(parents, wantImgParent) || !containsString(parents, wantVidParent) {
			t.Fatalf("migrated parent_dirs = %v, want %q and %q", parents, wantImgParent, wantVidParent)
		}
	})
}

// Item #2 (High #13): PruneDownloadRoots protects active storage_versions roots & env dir,
// normalizes relative paths with filepath.Abs, runs atomically, and caps unreferenced roots only.
func TestPruneDownloadRootsProtectsActiveStorageAndCapsUnreferencedOnly(t *testing.T) {
	envRoot := filepath.Join(t.TempDir(), "env-protected")
	t.Setenv(config.EnvDownloadDir, envRoot)

	store, err := Open(filepath.Join(t.TempDir(), "prune-high13.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	storageRoot := filepath.Join(t.TempDir(), "storage-target-root")
	doc := config.DefaultStorage(storageRoot)
	if doc, err = store.SaveStorageDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}

	// Register envRoot and a relative path that resolves to a protected root.
	now := time.Now().UTC()
	if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, envRoot, now.Add(-100*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Create MaxDownloadRoots + 3 referenced roots via downloads, so referencedCount > MaxDownloadRoots.
	job, err := store.CreateJob(ctx, JobKindUser, "https://x.com/a/status/1", "1")
	if err != nil {
		t.Fatal(err)
	}
	refRoots := make([]string, 0, MaxDownloadRoots+3)
	for i := 0; i < MaxDownloadRoots+3; i++ {
		r := filepath.Join(t.TempDir(), fmt.Sprintf("ref-root-%02d", i))
		refRoots = append(refRoots, r)
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, r, now.Add(-time.Duration(200+i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateDownload(ctx, DownloadRecord{
			JobID:    job.ID,
			TweetID:  fmt.Sprintf("tw-%d", i),
			MediaURL: fmt.Sprintf("https://pbs.twimg.com/media/%d.jpg", i),
			FilePath: filepath.Join(r, fmt.Sprintf("%d.jpg", i)),
			Bytes:    100,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Add MaxDownloadRoots + 5 unreferenced roots; the newest MaxDownloadRoots must survive
	// even though referenced/protected roots already exceed MaxDownloadRoots!
	unrefRoots := make([]string, 0, MaxDownloadRoots+5)
	for i := 0; i < MaxDownloadRoots+5; i++ {
		r := filepath.Join(t.TempDir(), fmt.Sprintf("unref-root-%02d", i))
		unrefRoots = append(unrefRoots, r)
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, r, now.Add(-time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	// Also insert a relative download_roots entry pointing to envRoot (if relative path computable).
	if wd, wdErr := os.Getwd(); wdErr == nil {
		if rel, relErr := filepath.Rel(wd, envRoot); relErr == nil && !filepath.IsAbs(rel) {
			if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, rel, now.Add(-500*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Call PruneDownloadRoots with nil keep: storageRoot, envRoot, and all refRoots must be auto-protected.
	if _, err := store.PruneDownloadRoots(ctx, nil); err != nil {
		t.Fatal(err)
	}
	roots, err := store.DownloadRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(roots, filepath.Clean(storageRoot)) {
		t.Fatalf("active storage_versions target root %q was pruned: %v", storageRoot, roots)
	}
	if !containsString(roots, filepath.Clean(envRoot)) {
		t.Fatalf("env download dir %q was pruned: %v", envRoot, roots)
	}
	for _, r := range refRoots {
		if !containsString(roots, filepath.Clean(r)) {
			t.Fatalf("referenced root %q was pruned", r)
		}
	}
	survivedUnref := 0
	for i, r := range unrefRoots {
		present := containsString(roots, filepath.Clean(r))
		if i < MaxDownloadRoots && !present {
			t.Fatalf("newest unreferenced root #%d (%q) was prematurely pruned when protected >= MaxDownloadRoots", i, r)
		}
		if i >= MaxDownloadRoots && present {
			t.Fatalf("oldest unreferenced root #%d (%q) should have been pruned", i, r)
		}
		if present {
			survivedUnref++
		}
	}
	if survivedUnref != MaxDownloadRoots {
		t.Fatalf("survived unreferenced roots = %d, want %d", survivedUnref, MaxDownloadRoots)
	}
}

// Item #3 (High #14): Fixed-width zero-padded UTC timestamps and mixed legacy timestamp sorting
// in ListTweets and ListLibraryDownloadsPage (including ROW_NUMBER dedup).
func TestSortableTimestampsAndLegacyMixedFormatOrdering(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "timestamps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "alice", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	// Archive a tweet with exact whole-second timestamp and verify stored published_at is fixed-width.
	wholeSec := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID:        "tw-whole",
		Text:      "whole second",
		CreatedAt: wholeSec,
		Author:    parser.Author{ID: "u1", ScreenName: "alice"},
	}, "completed"); err != nil {
		t.Fatal(err)
	}
	var rawPublished string
	if err := store.db.Get(&rawPublished, `SELECT 'raw:' || published_at FROM tweets WHERE id = 'tw-whole'`); err != nil {
		t.Fatal(err)
	}
	if rawPublished != "raw:2026-05-01T10:00:00.000000000Z" {
		t.Fatalf("stored published_at = %q, want fixed-width zero-padded UTC", rawPublished)
	}

	// Insert legacy mixed-format rows directly: '2026-05-01T10:00:00Z' vs '2026-05-01T10:00:00.5Z'
	// (Under raw BINARY sort, 'Z' (90) > '.' (46), so 00Z wrongly sorted AFTER 00.5Z!)
	if _, err := store.db.Exec(`INSERT INTO tweets(id, author_id, screen_name, author_name, body, published_at, payload, archived_at, updated_at)
		VALUES
		('legacy-00', 'u1', 'alice', 'Alice', 't0', '2026-05-01T10:00:00Z', '{}', '2026-05-01T10:00:00Z', '2026-05-01T10:00:00Z'),
		('legacy-50', 'u1', 'alice', 'Alice', 't5', '2026-05-01T10:00:00.5Z', '{}', '2026-05-01T10:00:00.5Z', '2026-05-01T10:00:00.5Z')`); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListTweets(ctx, TweetFilter{Sort: "published_desc", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 || list.Items[0].ID != "legacy-50" {
		ids := make([]string, len(list.Items))
		for i, it := range list.Items {
			ids[i] = it.ID
		}
		t.Fatalf("ListTweets published_desc with legacy mixed formats = %v, want legacy-50 first", ids)
	}

	// Also test library downloads with legacy mixed-format created_at (both sorting and ROW_NUMBER dedup).
	dupPath := filepath.Join(t.TempDir(), "dup.jpg")
	otherPath := filepath.Join(t.TempDir(), "other.jpg")
	if _, err := store.db.Exec(`INSERT INTO downloads(job_id, tweet_id, media_url, file_path, bytes, created_at)
		VALUES
		(?, 'legacy-00', 'https://pbs.twimg.com/old.jpg', ?, 100, '2026-05-01T10:00:00Z'),
		(?, 'legacy-50', 'https://pbs.twimg.com/new.jpg', ?, 200, '2026-05-01T10:00:00.5Z'),
		(?, 'legacy-00', 'https://pbs.twimg.com/other.jpg', ?, 150, '2026-05-01T10:00:00.25Z')`,
		job.ID, dupPath, job.ID, dupPath, job.ID, otherPath); err != nil {
		t.Fatal(err)
	}
	store.invalidateLibraryDownloadsCache()
	page, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Sort: "date_desc", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("library items = %d, want 2", len(page.Items))
	}
	if page.Items[0].FilePath != dupPath || page.Items[0].Bytes != 200 {
		t.Fatalf("ROW_NUMBER dedup picked wrong row for legacy mixed timestamps: %+v", page.Items[0])
	}
	if page.Items[1].FilePath != otherPath {
		t.Fatalf("second item = %+v, want %q", page.Items[1], otherPath)
	}
}

// Item #4 (High #15): Library SQL fallback when entries exceed librarySnapshotMaxEntries
// keeps user/kind/search/date/bytes filtering, date_asc/size sorting, orphan merging,
// Pagination.Total == Counts, and ListLibraryUsers 100% consistent.
func TestLibrarySQLFallbackConsistencyOverThreshold(t *testing.T) {
	origMax := librarySnapshotMaxEntries
	librarySnapshotMaxEntries = 2
	t.Cleanup(func() { librarySnapshotMaxEntries = origMax })

	root := t.TempDir()
	store, err := Open(filepath.Join(t.TempDir(), "lib-sql-fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpsertUser(ctx, User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "u2", ScreenName: "bob", Name: "Bob"}); err != nil {
		t.Fatal(err)
	}
	aliceDir := filepath.Join(root, "images", "alice")
	bobDir := filepath.Join(root, "videos", "bob")
	if err := os.MkdirAll(aliceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(root, "images"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u2", filepath.Join(root, "videos"), "bob"); err != nil {
		t.Fatal(err)
	}

	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	records := []struct {
		path string
		sz   int64
		at   time.Time
		url  string
	}{
		{filepath.Join(aliceDir, "a1.jpg"), 500, t0.Add(1 * time.Minute), "https://pbs.twimg.com/a1.jpg"},
		{filepath.Join(aliceDir, "中文图.png"), 100, t0.Add(2 * time.Minute), "https://pbs.twimg.com/a2.png"},
		{filepath.Join(bobDir, "b1.mp4"), 900, t0.Add(3 * time.Minute), "https://video.twimg.com/b1.mp4"},
		{filepath.Join(bobDir, "b2.gif"), 300, t0.Add(4 * time.Minute), "https://pbs.twimg.com/b2.gif"},
		{filepath.Join(root, "misc", "u1.jpg"), 200, t0.Add(5 * time.Minute), "https://pbs.twimg.com/u1.jpg"},
	}
	for i, r := range records {
		if _, err := store.db.Exec(
			`INSERT INTO downloads(job_id, tweet_id, media_url, file_path, bytes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			job.ID, fmt.Sprintf("tw-%d", i), r.url, r.path, r.sz, formatSortableTime(r.at),
		); err != nil {
			t.Fatal(err)
		}
	}
	// Add an orphan file on disk under aliceDir with intermediate size/time.
	orphanPath := filepath.Join(aliceDir, "orphan.jpg")
	if err := os.WriteFile(orphanPath, make([]byte, 400), 0o644); err != nil {
		t.Fatal(err)
	}
	orphanTime := t0.Add(2*time.Minute + 30*time.Second)
	if err := os.Chtimes(orphanPath, orphanTime, orphanTime); err != nil {
		t.Fatal(err)
	}
	store.invalidateLibraryDownloadsCache()

	// 1. All items, date_asc, pageSize=2 -> total=6 (5 DB + 1 orphan), Truncated=false, exact order.
	p1, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 2, Sort: "date_asc"})
	if err != nil {
		t.Fatal(err)
	}
	if p1.Truncated {
		t.Fatal("SQL fallback must set Truncated=false")
	}
	if p1.Pagination.Total != 6 || p1.Counts["all"] != 6 || p1.Counts["images"] != 4 || p1.Counts["videos"] != 1 || p1.Counts["gifs"] != 1 {
		t.Fatalf("counts/total mismatch: total=%d counts=%v", p1.Pagination.Total, p1.Counts)
	}
	if len(p1.Items) != 2 || filepath.Base(p1.Items[0].FilePath) != "a1.jpg" || filepath.Base(p1.Items[1].FilePath) != "中文图.png" {
		t.Fatalf("date_asc page 1 = %+v", p1.Items)
	}
	p2, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 2, PageSize: 2, Sort: "date_asc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 2 || filepath.Base(p2.Items[0].FilePath) != "orphan.jpg" || filepath.Base(p2.Items[1].FilePath) != "b1.mp4" {
		t.Fatalf("date_asc page 2 (orphan merge) = %+v", p2.Items)
	}

	// 2. size_asc across all 6 items (100, 200, 300, 400(orphan), 500, 900).
	var sizes []int64
	for pg := 1; pg <= 3; pg++ {
		res, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: pg, PageSize: 2, Sort: "size_asc"})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			sizes = append(sizes, it.Bytes)
		}
	}
	wantSizes := []int64{100, 200, 300, 400, 500, 900}
	if fmt.Sprint(sizes) != fmt.Sprint(wantSizes) {
		t.Fatalf("size_asc across pages = %v, want %v", sizes, wantSizes)
	}

	// 3. User filter (dir-attributed alice: a1.jpg, 中文图.png, orphan.jpg) + user=unknown (u1.jpg).
	alicePage, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{
		Page: 1, PageSize: 10, Sort: "size_desc", Filter: LibraryFilter{User: "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if alicePage.Pagination.Total != 3 || len(alicePage.Items) != 3 {
		t.Fatalf("user=alice total=%d items=%d, want 3", alicePage.Pagination.Total, len(alicePage.Items))
	}
	if alicePage.Items[0].Bytes != 500 || alicePage.Items[1].Bytes != 400 || alicePage.Items[2].Bytes != 100 {
		t.Fatalf("user=alice size_desc order = %+v", alicePage.Items)
	}

	unkPage, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{
		Page: 1, PageSize: 10, Filter: LibraryFilter{User: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if unkPage.Pagination.Total != 1 || filepath.Base(unkPage.Items[0].FilePath) != "u1.jpg" {
		t.Fatalf("user=unknown = %+v", unkPage)
	}

	// 4. Non-ASCII search + Kind filter.
	searchPage, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{
		Page: 1, PageSize: 10, Filter: LibraryFilter{Kind: "images", Search: "中文"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if searchPage.Pagination.Total != 1 || filepath.Base(searchPage.Items[0].FilePath) != "中文图.png" {
		t.Fatalf("unicode search = %+v", searchPage)
	}

	// 5. ListLibraryUsers aggregates ALL rows (not just the 2 cached snapshot entries).
	users, err := store.ListLibraryUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	countsByUser := map[string]int{}
	for _, u := range users {
		countsByUser[u.ScreenName] = u.MediaCount
	}
	if countsByUser["alice"] != 3 || countsByUser["bob"] != 2 {
		t.Fatalf("ListLibraryUsers over threshold = %+v, want alice=3 bob=2", countsByUser)
	}
}

// Item #5 (High #16): migrateCookiesToXAccounts auto-promotes first active account when
// app_config.auth_token is empty and only additional_cookies exist.
func TestMigrateCookiesToXAccountsPromotesPrimaryWhenMainTokenEmpty(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate-cookies.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	additional := `[{"auth_token":"extra-at-1","ct0":"extra-ct0-1","name":"first"},{"auth_token":"extra-at-2","ct0":"extra-ct0-2","name":"second"}]`
	if _, err := store.db.Exec(`UPDATE app_config SET auth_token = '', csrf_token = '', additional_cookies = ? WHERE id = 1`, additional); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM x_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE name = 'migrate_cookies_to_x_accounts'`); err != nil {
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

	accounts, err := reopened.ListXAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("migrated accounts = %d, want 2", len(accounts))
	}
	primaries := 0
	for _, a := range accounts {
		if a.IsPrimary {
			primaries++
			if a.AuthToken != "extra-at-1" {
				t.Fatalf("promoted primary auth_token = %q, want extra-at-1", a.AuthToken)
			}
		}
	}
	if primaries != 1 {
		t.Fatalf("primaries = %d, want 1", primaries)
	}
}

// Item #6 (High #17 storage part): CreateAsset handles policy_version upgrades and missing-file
// re-downloads on the same (target_id, target_version, object_key) without SQLITE_CONSTRAINT_UNIQUE,
// preserving existing asset ID for download_assets FKs, and FindAssetHash prefers matching policy_version.
func TestAssetsUpsertOnPolicyUpgradeAndFindAssetHash(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "assets-high17.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateJob(ctx, JobKindUser, "https://x.com/a/status/1", "1")
	if err != nil {
		t.Fatal(err)
	}
	dl, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: "1", MediaURL: "https://pbs.twimg.com/1.jpg", FilePath: "/tmp/1.jpg", Bytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	rootDir := t.TempDir()
	doc := config.DefaultStorage(rootDir)
	for v := 0; v < 3; v++ {
		if doc, err = store.SaveStorageDocument(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	target := doc.Targets[0]
	a1, err := store.CreateAsset(ctx, Asset{
		ID:            "asset-1",
		Kind:          config.AssetImage,
		TargetID:      target.ID,
		TargetVersion: 1,
		PolicyVersion: 1,
		Key:           "images/alice/1.jpg",
		ContentHash:   "hash-v1",
		Size:          100,
		ContentType:   "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkDownloadAsset(ctx, dl.ID, config.AssetImage, a1.ID); err != nil {
		t.Fatal(err)
	}

	// Policy version upgraded to 2, same (target_id, target_version, object_key): must upsert, preserve ID, and keep FK intact.
	a2, err := store.CreateAsset(ctx, Asset{
		ID:            "asset-2",
		Kind:          config.AssetImage,
		TargetID:      target.ID,
		TargetVersion: 1,
		PolicyVersion: 2,
		Key:           "images/alice/1.jpg",
		ContentHash:   "hash-v2",
		Size:          222,
		ContentType:   "image/webp",
	})
	if err != nil {
		t.Fatalf("CreateAsset on policy upgrade failed: %v", err)
	}
	if a2.ID != a1.ID {
		t.Fatalf("CreateAsset changed asset ID on upsert: got %q, want %q", a2.ID, a1.ID)
	}
	if a2.PolicyVersion != 2 || a2.ContentHash != "hash-v2" || a2.Size != 222 || a2.ContentType != "image/webp" {
		t.Fatalf("CreateAsset did not update metadata: %+v", a2)
	}
	linked, err := store.DownloadAssets(ctx, dl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if linked[config.AssetImage] != a1.ID {
		t.Fatalf("download_assets FK broken after upsert: %+v", linked)
	}

	// FindAssetHash prefers matching policy_version and falls back to another policy_version on same target.
	a3, err := store.CreateAsset(ctx, Asset{
		ID:            "asset-3",
		Kind:          config.AssetImage,
		TargetID:      target.ID,
		TargetVersion: 1,
		PolicyVersion: 3,
		Key:           "images/alice/1-pv3.jpg",
		ContentHash:   "hash-v2",
		Size:          222,
		ContentType:   "image/webp",
	})
	if err != nil {
		t.Fatal(err)
	}
	foundPV3, err := store.FindAssetHash(ctx, "hash-v2", config.AssetImage, target, 3)
	if err != nil || foundPV3 == nil || foundPV3.ID != a3.ID {
		t.Fatalf("FindAssetHash(pv=3) = %+v %v, want %q", foundPV3, err, a3.ID)
	}
	foundPV99, err := store.FindAssetHash(ctx, "hash-v2", config.AssetImage, target, 99)
	if err != nil || foundPV99 == nil {
		t.Fatalf("FindAssetHash fallback across policy_version = %+v %v", foundPV99, err)
	}
}

// Item #7 (Medium account.go): CreateSession enforces per-account active session cap.
func TestCreateSessionEvictsOldestBeyondCap(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	total := MaxActiveSessions + 5
	for i := 0; i < total; i++ {
		tok := fmt.Sprintf("tok-%03d", i)
		if err := store.CreateSession(ctx, tok, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := store.db.Get(&count, `SELECT COUNT(*) FROM app_sessions`); err != nil {
		t.Fatal(err)
	}
	if count != MaxActiveSessions {
		t.Fatalf("active sessions = %d, want %d", count, MaxActiveSessions)
	}

	checkAt := base.Add(time.Duration(total) * time.Second)
	for i := 0; i < 5; i++ {
		valid, err := store.SessionValidReadOnly(ctx, fmt.Sprintf("tok-%03d", i), checkAt)
		if err != nil || valid {
			t.Fatalf("oldest session tok-%03d should be evicted: valid=%v err=%v", i, valid, err)
		}
	}
	for i := 5; i < total; i++ {
		valid, err := store.SessionValidReadOnly(ctx, fmt.Sprintf("tok-%03d", i), checkAt)
		if err != nil || !valid {
			t.Fatalf("recent session tok-%03d should remain valid: valid=%v err=%v", i, valid, err)
		}
	}
}

// Item #8 (Medium storage/tweets.go):
// 1. Re-archiving nested Related stub tweet must not overwrite an already-archived main tweet/relations/media.
// 2. SetTweetMediaResult & LinkRelatedTweetMedia converge related tweets with empty-URL media to "completed".
func TestArchiveRelatedStubPreservesMainAndEmptyURLMediaConverges(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "tweets-med.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "alice", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}

	// Archive full main tweet "main-1" with relation and media.
	fullMain := parser.TweetData{
		ID:        "main-1",
		Text:      "Complete original tweet body",
		CreatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		Author:    parser.Author{ID: "u1", ScreenName: "alice", Name: "Alice Full"},
		Media: []parser.Media{
			{ID: "m1", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/m1.jpg"},
		},
		QuotedTweetID: "q-1",
		Related: []parser.TweetData{
			{ID: "q-1", Text: "quoted text", Author: parser.Author{ID: "u9", ScreenName: "zoe"}},
		},
	}
	if err := store.ArchiveTweet(ctx, job.ID, fullMain, "completed"); err != nil {
		t.Fatal(err)
	}

	// Now archive another tweet that embeds "main-1" as a sparse Related stub (different/empty text, no relations/media).
	outer := parser.TweetData{
		ID:     "outer-1",
		Text:   "Retweeting main-1",
		Author: parser.Author{ID: "u2", ScreenName: "bob"},
		Related: []parser.TweetData{
			{ID: "main-1", Text: "truncated stub", Author: parser.Author{ID: "u1", ScreenName: "alice"}},
		},
	}
	if err := store.ArchiveTweet(ctx, job.ID, outer, "pending"); err != nil {
		t.Fatal(err)
	}

	gotMain, err := store.GetTweet(ctx, "main-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMain.Text != "Complete original tweet body" || gotMain.MediaStatus != "completed" || len(gotMain.Media) != 1 {
		t.Fatalf("related stub overwrote complete main tweet: %+v", gotMain)
	}
	var relCount int
	if err := store.db.Get(&relCount, `SELECT COUNT(*) FROM tweet_relations WHERE tweet_id = 'main-1' AND related_id = 'q-1'`); err != nil || relCount != 1 {
		t.Fatalf("main-1 relations lost after related stub: count=%d err=%v", relCount, err)
	}

	// Related tweet with 1 real media + 1 empty-URL media must converge to "completed" (not stuck in "partial").
	parent := parser.TweetData{
		ID:     "parent-1",
		Text:   "parent",
		Author: parser.Author{ID: "u1", ScreenName: "alice"},
		Media:  []parser.Media{{ID: "pm1", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/shared.jpg"}},
		Related: []parser.TweetData{
			{
				ID:     "rel-empty-url",
				Text:   "related with one real media and one empty-URL media",
				Author: parser.Author{ID: "u3", ScreenName: "carol"},
				Media: []parser.Media{
					{ID: "rm1", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/shared.jpg"},
					{ID: "rm-empty", Type: parser.MediaPhoto, URL: ""},
				},
			},
		},
	}
	if err := store.ArchiveTweet(ctx, job.ID, parent, "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: "parent-1", MediaURL: "https://pbs.twimg.com/media/shared.jpg", FilePath: "/tmp/shared.jpg", Bytes: 55,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkRelatedTweetMedia(ctx, job.ID, parent, parent.Media[0]); err != nil {
		t.Fatal(err)
	}
	rel, err := store.GetTweet(ctx, "rel-empty-url")
	if err != nil {
		t.Fatal(err)
	}
	if rel.MediaStatus != "completed" {
		t.Fatalf("LinkRelatedTweetMedia status with empty-URL sibling = %q, want completed", rel.MediaStatus)
	}

	// Also verify SetTweetMediaResult converges to completed when only downloadable media is present.
	if _, err := store.db.Exec(`UPDATE tweet_jobs SET media_status = 'pending' WHERE tweet_id = 'rel-empty-url'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTweetMediaResult(ctx, job.ID, "parent-1", "completed", ""); err != nil {
		t.Fatal(err)
	}
	rel, err = store.GetTweet(ctx, "rel-empty-url")
	if err != nil {
		t.Fatal(err)
	}
	if rel.MediaStatus != "completed" {
		t.Fatalf("SetTweetMediaResult status with empty-URL sibling = %q, want completed", rel.MediaStatus)
	}
}

// Item #9 (Medium storage/xaccounts.go):
//   - UpdateXAccount rejects inactive primary, rejects unsetting last primary, resets screen_name & locks on auth_token change,
//     and supports atomic ProxyResolver inside tx.
//   - db -> env upgrade clears stale uneditable extra_cookies.
//   - ApplyXAccountCheckIfUnchanged rejects stale probe when credentials changed concurrently.
func TestXAccountsAuditFixes(t *testing.T) {
	store := newXAccountTestStore(t)
	ctx := context.Background()

	a1 := mustCreateXAccount(t, store, XAccount{
		AuthToken:    "at-1",
		CSRFToken:    "ct0-1",
		ExtraCookies: "k=v",
		Proxy:        "http://user:secret@127.0.0.1:7890",
	})
	a2 := mustCreateXAccount(t, store, XAccount{AuthToken: "at-2", CSRFToken: "ct0-2"})

	// Reject making an inactive account primary.
	inactive := false
	if _, err := store.UpdateXAccount(ctx, a2.ID, XAccountPatch{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	makePrimary := true
	if _, err := store.UpdateXAccount(ctx, a2.ID, XAccountPatch{IsPrimary: &makePrimary}); err == nil {
		t.Fatal("expected error when promoting inactive account to primary")
	}

	// Reject unsetting the last primary account.
	unsetPrimary := false
	if _, err := store.UpdateXAccount(ctx, a1.ID, XAccountPatch{IsPrimary: &unsetPrimary}); err == nil {
		t.Fatal("expected error when unsetting the last primary account")
	}

	// Atomic ProxyResolver runs inside transaction with current unmasked proxy.
	var seenCurrentProxy string
	updated, err := store.UpdateXAccount(ctx, a1.ID, XAccountPatch{
		ProxyResolver: func(current string) (string, error) {
			seenCurrentProxy = current
			return current, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenCurrentProxy != "http://user:secret@127.0.0.1:7890" || updated.Proxy != "http://user:secret@127.0.0.1:7890" {
		t.Fatalf("ProxyResolver saw %q, updated.Proxy = %q", seenCurrentProxy, updated.Proxy)
	}

	// ApplyXAccountCheckIfUnchanged: succeeds when unchanged, skips when credentials mutated concurrently.
	snapshotBefore := updated
	got, applied, err := store.ApplyXAccountCheckIfUnchanged(ctx, snapshotBefore, XAccountCheck{ScreenName: "alice"}, time.Now().UTC())
	if err != nil || !applied || got.ScreenName != "alice" {
		t.Fatalf("ApplyXAccountCheckIfUnchanged(unchanged) = %+v applied=%v err=%v", got, applied, err)
	}

	// Mutate auth_token (resets screen_name to "" and clears locks), then attempt stale probe write.
	newTok := "at-1-rotated"
	if _, err := store.UpdateXAccount(ctx, a1.ID, XAccountPatch{AuthToken: &newTok}); err != nil {
		t.Fatal(err)
	}
	got, applied, err = store.ApplyXAccountCheckIfUnchanged(ctx, snapshotBefore, XAccountCheck{ScreenName: "stale_alice"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("stale ApplyXAccountCheckIfUnchanged must not overwrite mutated account")
	}
	if got.ScreenName != "" || got.AuthToken != newTok {
		t.Fatalf("account after rejected stale probe = %+v", got)
	}

	// db -> env upgrade clears stale uneditable extra_cookies.
	if err := store.SyncEnvXAccounts(ctx, []EnvXAccount{{AuthToken: newTok, CSRFToken: "env-ct0", Primary: true}}); err != nil {
		t.Fatal(err)
	}
	afterEnv, err := store.GetXAccount(ctx, a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterEnv.Source != "env" || afterEnv.ExtraCookies != "" {
		t.Fatalf("db -> env upgrade left stale extra_cookies: source=%q extra=%q", afterEnv.Source, afterEnv.ExtraCookies)
	}
}

// Item #10 (Medium storage.go):
//   - ReserveAutomaticRetryJob checks ALL active retry jobs (not just highest ID) and clears
//     job_checkpoints + job_checkpoint_tasks when reusing a terminal row.
//   - ListFailedTweetViewsPage has deterministic ", id" pagination tie-breaker when updated_at is identical.
func TestReserveAutomaticRetryJobAllActiveAndCheckpointCleanupAndDeterministicFailedPagination(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "retry-and-failed-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// Create two failed_retry jobs: older (#1) is still active (running), newer (#2) is completed.
	j1, err := store.CreateJob(ctx, JobKindFailedRetry, "retry-1", "retry-1")
	if err != nil {
		t.Fatal(err)
	}
	j2, err := store.CreateJob(ctx, JobKindFailedRetry, AutomaticFailedRetryInput, "retry-2")
	if err != nil {
		t.Fatal(err)
	}
	j1.Status = JobDownloading
	if err := store.UpdateJob(ctx, j1); err != nil {
		t.Fatal(err)
	}
	j2.Status = JobCompleted
	if err := store.UpdateJob(ctx, j2); err != nil {
		t.Fatal(err)
	}

	// ReserveAutomaticRetryJob must detect j1 is still active and return nil (no duplicate active retry job).
	reserved, err := store.ReserveAutomaticRetryJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reserved != nil {
		t.Fatalf("ReserveAutomaticRetryJob allowed duplicate while older retry job #%d active: got #%d", j1.ID, reserved.ID)
	}

	// Finish j1, attach checkpoints to j2 (highest ID), then reserve again -> reuses j2 and clears checkpoints.
	j1.Status = JobCompleted
	if err := store.UpdateJob(ctx, j1); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobCheckpoint(ctx, j2.ID, []byte(`{"step":"old"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJobCheckpointTasks(ctx, j2.ID, []byte(`[{"id":"old"}]`)); err != nil {
		t.Fatal(err)
	}

	reserved, err = store.ReserveAutomaticRetryJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reserved == nil || reserved.ID != j2.ID {
		t.Fatalf("ReserveAutomaticRetryJob reused = %+v, want #%d", reserved, j2.ID)
	}
	if _, ok, err := store.LoadJobCheckpoint(ctx, j2.ID); err != nil || ok {
		t.Fatalf("job_checkpoints not cleared on reuse: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.LoadJobCheckpointTasks(ctx, j2.ID); err != nil || ok {
		t.Fatalf("job_checkpoint_tasks not cleared on reuse: ok=%v err=%v", ok, err)
	}

	// Deterministic pagination in ListFailedTweetViewsPage when updated_at is identical across rows.
	if _, err := store.UpsertUser(ctx, User{ID: "u-ft", ScreenName: "ftuser", Name: "FT User"}); err != nil {
		t.Fatal(err)
	}
	entity, err := store.EnsureUserEntity(ctx, "u-ft", t.TempDir(), "ftuser")
	if err != nil {
		t.Fatal(err)
	}
	sameTS := "2026-09-01T00:00:00.000000000Z"
	for i := 1; i <= 5; i++ {
		if _, err := store.db.Exec(
			`INSERT INTO failed_tweets(job_id, entity_id, tweet_id, payload, error, retry_count, created_at, updated_at)
			 VALUES (?, ?, ?, '{}', 'err', 1, ?, ?)`,
			j1.ID, entity.ID, fmt.Sprintf("ft-%d", i), sameTS, sameTS,
		); err != nil {
			t.Fatal(err)
		}
	}
	page1, total1, err := store.ListFailedTweetViewsPage(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	page2, total2, err := store.ListFailedTweetViewsPage(ctx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	page3, total3, err := store.ListFailedTweetViewsPage(ctx, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if total1 != 5 || total2 != 5 || total3 != 5 {
		t.Fatalf("totals = %d, %d, %d, want 5", total1, total2, total3)
	}
	gotIDs := []string{}
	for _, p := range [][]FailedTweetView{page1, page2, page3} {
		for _, item := range p {
			gotIDs = append(gotIDs, item.TweetID)
		}
	}
	wantIDs := []string{"ft-1", "ft-2", "ft-3", "ft-4", "ft-5"}
	if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
		t.Fatalf("ListFailedTweetViewsPage deterministic order = %v, want %v", gotIDs, wantIDs)
	}
}
