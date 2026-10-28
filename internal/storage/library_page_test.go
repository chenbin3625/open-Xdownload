package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

func writeLibraryTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestListLibraryDownloadsPagePaginationAndFilters 覆盖服务端分页的核心语义：
// SQL LIMIT/OFFSET 分页 + 按 file_path 去重 + kind/user/search 过滤 + COUNT 总数 +
// 全局 counts/users facet + 孤儿文件并入排序。
func TestListLibraryDownloadsPagePaginationAndFilters(t *testing.T) {
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
	for _, user := range []User{
		{ID: "u1", ScreenName: "alice", Name: "Alice"},
		{ID: "u2", ScreenName: "bob", Name: "Bob"},
	} {
		if _, err := store.UpsertUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.EnsureUserEntity(ctx, "u1", filepath.Join(root, "users"), "Alice(alice)"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUserEntity(ctx, "u2", filepath.Join(root, "users"), "Bob(bob)"); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	type fixture struct {
		rel     string
		tweet   string
		media   string
		created time.Time
	}
	fixtures := []fixture{
		{"users/Alice(alice)/a.jpg", "t1", "https://pbs.twimg.com/media/a.jpg", now.Add(-time.Minute)},
		{"users/Alice(alice)/b.mp4", "t2", "https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/b.mp4", now.Add(-2 * time.Minute)},
		{"users/Alice(alice)/c.gif", "t3", "https://pbs.twimg.com/tweet_video/c.gif", now.Add(-3 * time.Minute)},
		{"users/Bob(bob)/d.webp", "t4", "https://pbs.twimg.com/media/d.webp", now.Add(-4 * time.Minute)},
	}
	for _, item := range fixtures {
		path := filepath.Join(root, filepath.FromSlash(item.rel))
		writeLibraryTestFile(t, path, "data")
		record, err := store.CreateDownload(ctx, DownloadRecord{JobID: job.ID, TweetID: item.tweet, MediaURL: item.media, FilePath: path, Bytes: 4})
		if err != nil {
			t.Fatalf("create download %s: %v", item.rel, err)
		}
		if _, err := store.db.Exec(`UPDATE downloads SET created_at = ? WHERE id = ?`, item.created, record.ID); err != nil {
			t.Fatal(err)
		}
	}
	// 同一 file_path 的第二条记录（转推/引用）：分页结果必须只出现一次。
	duplicatePath := filepath.Join(root, "users", "Alice(alice)", "a.jpg")
	duplicate, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: "t1-retweet", MediaURL: "https://pbs.twimg.com/media/a-retweet.jpg",
		FilePath: duplicatePath, Bytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE downloads SET created_at = ? WHERE id = ?`, now.Add(-30*time.Second), duplicate.ID); err != nil {
		t.Fatal(err)
	}
	// 孤儿文件（磁盘有、DB 无记录）参与同一排序；大小写不同的 .preview- 临时文件与非媒体文件必须被忽略。
	orphanPath := filepath.Join(root, "orphan.mp4")
	writeLibraryTestFile(t, orphanPath, "orphan")
	if err := os.Chtimes(orphanPath, now.Add(-5*time.Minute), now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	writeLibraryTestFile(t, filepath.Join(root, ".preview-123.jpg"), "temp")
	writeLibraryTestFile(t, filepath.Join(root, "notes.txt"), "text")

	all, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if all.Pagination.Total != 5 || all.Pagination.TotalPages != 3 || all.Pagination.Page != 1 || all.Pagination.PageSize != 2 {
		t.Fatalf("pagination = %+v, want total=5 totalPages=3 page=1", all.Pagination)
	}
	if all.Truncated {
		t.Fatal("truncated = true for small library")
	}
	pagePaths := func(page LibraryPage) []string {
		paths := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			paths = append(paths, filepath.ToSlash(item.FilePath))
		}
		return paths
	}
	wantFirstPage := []string{filepath.ToSlash(duplicatePath), filepath.ToSlash(filepath.Join(root, "users/Alice(alice)/b.mp4"))}
	if got := pagePaths(all); strings.Join(got, "|") != strings.Join(wantFirstPage, "|") {
		t.Fatalf("page 1 = %v, want %v", got, wantFirstPage)
	}
	if all.Counts["all"] != 5 || all.Counts["images"] != 2 || all.Counts["videos"] != 2 || all.Counts["gifs"] != 1 {
		t.Fatalf("counts = %v, want all=5 images=2 videos=2 gifs=1", all.Counts)
	}
	if len(all.Users) != 3 {
		t.Fatalf("users facet = %+v, want alice/bob/unknown", all.Users)
	}
	userLabels := map[string]string{}
	for _, user := range all.Users {
		userLabels[user.Value] = user.Label
	}
	if userLabels["alice"] != "Alice (@alice)" || userLabels["bob"] != "Bob (@bob)" {
		t.Fatalf("user labels = %v", userLabels)
	}
	if userLabels["unknown"] == "" {
		t.Fatalf("unknown user option missing: %v", userLabels)
	}
	// 归属用户已被填充。
	if all.Items[0].UserScreenName != "alice" {
		t.Fatalf("first item attribution = %q, want alice", all.Items[0].UserScreenName)
	}

	second, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	wantSecondPage := []string{
		filepath.ToSlash(filepath.Join(root, "users/Alice(alice)/c.gif")),
		filepath.ToSlash(filepath.Join(root, "users/Bob(bob)/d.webp")),
	}
	if got := pagePaths(second); strings.Join(got, "|") != strings.Join(wantSecondPage, "|") {
		t.Fatalf("page 2 = %v, want %v", got, wantSecondPage)
	}
	third, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := pagePaths(third); len(got) != 1 || got[0] != filepath.ToSlash(orphanPath) {
		t.Fatalf("page 3 = %v, want orphan %s", got, orphanPath)
	}

	// 过滤：kind。
	videos, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 10, Filter: LibraryFilter{Kind: "videos"}})
	if err != nil {
		t.Fatal(err)
	}
	if videos.Pagination.Total != 2 || len(videos.Items) != 2 {
		t.Fatalf("videos = %+v", videos.Pagination)
	}
	if !strings.HasSuffix(filepath.ToSlash(videos.Items[0].FilePath), "b.mp4") {
		t.Fatalf("videos order = %v", pagePaths(videos))
	}
	// counts 是不受过滤影响的全局 facet。
	if videos.Counts["all"] != 5 || videos.Counts["images"] != 2 {
		t.Fatalf("filtered counts changed: %v", videos.Counts)
	}
	// 过滤：user（具体用户 / unknown）。
	alice, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 10, Filter: LibraryFilter{User: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if alice.Pagination.Total != 3 {
		t.Fatalf("alice total = %d, want 3", alice.Pagination.Total)
	}
	unknown, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 10, Filter: LibraryFilter{User: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Pagination.Total != 1 || len(unknown.Items) != 1 || unknown.Items[0].FilePath != orphanPath {
		t.Fatalf("unknown filter = %+v, want only orphan", unknown.Items)
	}
	// 过滤：search（路径/媒体 URL/推文 ID，大小写不敏感）。
	search, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 10, Filter: LibraryFilter{Search: "users/bob"}})
	if err != nil {
		t.Fatal(err)
	}
	if search.Pagination.Total != 1 || !strings.HasSuffix(filepath.ToSlash(search.Items[0].FilePath), "d.webp") {
		t.Fatalf("search = %+v", pagePaths(search))
	}
	// 未知 kind：与旧 HTTP 语义一致——什么都不匹配，但 facet 仍正常。
	bogus, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 10, Filter: LibraryFilter{Kind: "documents"}})
	if err != nil {
		t.Fatal(err)
	}
	if bogus.Pagination.Total != 0 || len(bogus.Items) != 0 || bogus.Pagination.Page != 1 {
		t.Fatalf("unknown kind = %+v", bogus.Pagination)
	}
	// 页码越界被 clamp 到最后一页。
	clamped, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 99, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if clamped.Pagination.Page != 3 || len(clamped.Items) != 1 {
		t.Fatalf("clamped page = %+v items=%d, want page 3 with 1 item", clamped.Pagination, len(clamped.Items))
	}
}

// TestListLibraryDownloadsPageEmpty 验证空库返回非 nil 的空数组/空 facet（前端契约）。
func TestListLibraryDownloadsPageEmpty(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.ListLibraryDownloadsPage(context.Background(), LibraryPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || page.Users == nil {
		t.Fatalf("empty page must return non-nil arrays: %+v", page)
	}
	if page.Pagination.Total != 0 || page.Pagination.TotalPages != 1 || page.Pagination.Page != 1 || page.Pagination.PageSize != DefaultLibraryPageSize {
		t.Fatalf("empty pagination = %+v", page.Pagination)
	}
	if page.Counts["all"] != 0 {
		t.Fatalf("empty counts = %v", page.Counts)
	}
	// pageSize 上限被 clamp。
	clamped, err := store.ListLibraryDownloadsPage(context.Background(), LibraryPageOptions{Page: 1, PageSize: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if clamped.Pagination.PageSize != MaxLibraryPageSize {
		t.Fatalf("pageSize = %d, want %d", clamped.Pagination.PageSize, MaxLibraryPageSize)
	}
}

// TestListLibraryDownloadsPageDerivesPreviewURL 分页结果对缺失 preview_url 的视频记录
// 回退推导（与非分页接口一致）。
func TestListLibraryDownloadsPageDerivesPreviewURL(t *testing.T) {
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
	path := filepath.Join(root, "clip.mp4")
	writeLibraryTestFile(t, path, "video")
	if _, err := store.CreateDownload(ctx, DownloadRecord{
		JobID: job.ID, TweetID: "t1",
		MediaURL: "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/clip.mp4",
		FilePath: path, Bytes: 5,
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListLibraryDownloadsPage(ctx, LibraryPageOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %+v", page.Items)
	}
	if page.Items[0].PreviewURL == "" || !strings.Contains(page.Items[0].PreviewURL, "ext_tw_video_thumb") {
		t.Fatalf("derived preview url = %q", page.Items[0].PreviewURL)
	}
}
