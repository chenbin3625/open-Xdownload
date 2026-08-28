package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestRetryActiveJobReturnsBusinessError(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	job, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://example.com/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/jobs/"+strconv.FormatInt(job.ID, 10)+"/retry", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "internal error") || !strings.Contains(response.Body.String(), "不能重试") {
		t.Fatalf("body = %s, want business error", response.Body.String())
	}
}

func TestRetryTerminalJobCreatesNewJob(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	original, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://example.com/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	original.Status = storage.JobFailed
	original.Error = "boom"
	if err := db.UpdateJob(ctx, original); err != nil {
		t.Fatalf("mark job failed: %v", err)
	}

	eventBus := jobs.NewEventBus()
	manager := jobs.NewManager(db, nil, eventBus)
	handler := NewServer(db, nil, manager, eventBus).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/jobs/"+strconv.FormatInt(original.ID, 10)+"/retry", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var retried storage.Job
	if err := json.NewDecoder(response.Body).Decode(&retried); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if retried.ID == original.ID || retried.Status != storage.JobPending {
		t.Fatalf("retried job = %+v, want a new pending job", retried)
	}
	storedOriginal, err := db.GetJob(ctx, original.ID)
	if err != nil {
		t.Fatalf("get original job: %v", err)
	}
	if storedOriginal.Status != storage.JobFailed || storedOriginal.Error != "boom" {
		t.Fatalf("original job = %+v, want failed history unchanged", storedOriginal)
	}
}

func TestCancelNonexistentJobReturnsNotFound(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	eventBus := jobs.NewEventBus()
	manager := jobs.NewManager(db, nil, eventBus)
	handler := NewServer(db, nil, manager, eventBus).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/jobs/999999/cancel", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s, want 404", response.Code, response.Body.String())
	}
}

func TestRetryNonexistentJobReturnsNotFound(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/jobs/999999/retry", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s, want 404", response.Code, response.Body.String())
	}
}

func TestCreateTweetJobOnlyValidatesURLShape(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	eventBus := jobs.NewEventBus()
	manager := jobs.NewManager(db, nil, eventBus)
	handler := NewServer(db, nil, manager, eventBus).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(`{
		"kind": "tweet_link",
		"input": "https://x.com/openai/status/1234567890"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var created storage.Job
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.Title != "Tweet 1234567890" {
		t.Fatalf("title = %q, want Tweet 1234567890", created.Title)
	}
}

func TestCreateMediaURLRejectsNonTwimgHost(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	eventBus := jobs.NewEventBus()
	manager := jobs.NewManager(db, nil, eventBus)
	handler := NewServer(db, nil, manager, eventBus).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(`{
		"kind": "media_url",
		"input": "http://127.0.0.1/latest/meta-data"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "twimg.com") {
		t.Fatalf("body = %s, want twimg.com validation error", response.Body.String())
	}
}

func TestCreateLocalDirectoryCreatesAndReturnsListing(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// S5：目录创建/浏览被限制在"家目录 + 工作目录 + 配置下载目录"范围内。
	// 这里把下载目录指向 t.TempDir()，再在其下创建，符合允许范围。
	allowedRoot := t.TempDir()
	if _, err := db.UpdateConfig(context.Background(), config.AppConfig{DownloadDir: allowedRoot}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	target := filepath.Join(allowedRoot, "new", "downloads")
	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/local-directories", strings.NewReader(`{"path":`+strconv.Quote(target)+`}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("created dir stat = %+v, %v; want directory", info, err)
	}
	var listing localDirectoryListing
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if listing.Path != target {
		t.Fatalf("path = %q, want %q", listing.Path, target)
	}
}

func TestCreateLocalDirectoryRejectsPathOutsideAllowedRoots(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// 允许根 = 家目录/工作目录/t.TempDir 下载目录；/etc 显然不在其中。
	allowedRoot := t.TempDir()
	if _, err := db.UpdateConfig(context.Background(), config.AppConfig{DownloadDir: allowedRoot}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodPost, "/api/local-directories", strings.NewReader(`{"path":"/etc/open-xdownload-blocked"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400 for out-of-roots path", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "不允许") {
		t.Fatalf("body = %s, want allowlist error", response.Body.String())
	}
}

func TestLocalDirectoryRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	linkPath := filepath.Join(root, "escape")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if withinAllowedRoot(linkPath, []string{root}) {
		t.Fatal("symlink target outside root was accepted")
	}
	if _, err := localDirectoryPath(linkPath, []string{root}); err == nil {
		t.Fatal("localDirectoryPath accepted symlink escape")
	}
	if _, err := createLocalDirectoryPath(filepath.Join(linkPath, "created"), []string{root}); err == nil {
		t.Fatal("createLocalDirectoryPath accepted symlink escape")
	}
	if _, err := os.Stat(filepath.Join(outside, "created")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape created outside directory: %v", err)
	}
}

func TestWithinAllowedRootAcceptsFilesystemRootChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("filesystem root layout differs on Windows")
	}
	root := string(filepath.Separator)
	child := filepath.Join(root, "downloads")
	if !withinAllowedRoot(child, []string{root}) {
		t.Fatalf("%q should be allowed under root %q", child, root)
	}
}

func TestListFailedTweetsSupportsPagination(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	job, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://example.com/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := db.UpsertUser(ctx, storage.User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	entity, err := db.EnsureUserEntity(ctx, "u1", t.TempDir(), "Alice(alice)")
	if err != nil {
		t.Fatalf("ensure entity: %v", err)
	}
	for _, tweetID := range []string{"1", "2", "3"} {
		if _, err := db.CreateFailedTweet(ctx, storage.FailedTweet{
			JobID:    job.ID,
			EntityID: entity.ID,
			TweetID:  tweetID,
			Payload:  `{}`,
			Error:    "boom " + tweetID,
		}); err != nil {
			t.Fatalf("create failed tweet %s: %v", tweetID, err)
		}
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/failed-tweets?page=2&pageSize=2", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var page failedTweetPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page.Pagination.Total != 3 || page.Pagination.Page != 2 || page.Pagination.PageSize != 2 {
		t.Fatalf("pagination = %+v, want total 3 page 2 pageSize 2", page.Pagination)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items length = %d, want 1", len(page.Items))
	}
	raw, _ := json.Marshal(page.Items[0])
	if strings.Contains(string(raw), `"payload"`) {
		t.Fatalf("failed tweet view should omit payload, got %s", raw)
	}
}

func TestDashboardOmitsFailedTweetBodies(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	job, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://example.com/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := db.UpsertUser(ctx, storage.User{ID: "u1", ScreenName: "alice", Name: "Alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	entity, err := db.EnsureUserEntity(ctx, "u1", t.TempDir(), "Alice(alice)")
	if err != nil {
		t.Fatalf("ensure entity: %v", err)
	}
	if _, err := db.CreateFailedTweet(ctx, storage.FailedTweet{
		JobID:    job.ID,
		EntityID: entity.ID,
		TweetID:  "99",
		Payload:  `{"text":"` + strings.Repeat("x", 8000) + `"}`,
		Error:    "boom",
	}); err != nil {
		t.Fatalf("create failed tweet: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	raw := response.Body.Bytes()
	var dashboard storage.Dashboard
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("decode map: %v", err)
	}
	if dashboard.FailedTweetCount != 1 {
		t.Fatalf("failedTweetCount = %d, want 1", dashboard.FailedTweetCount)
	}
	if len(dashboard.FailedTweets) != 0 {
		t.Fatalf("dashboard should not embed failed tweet bodies, got %d", len(dashboard.FailedTweets))
	}
	if _, ok := rawMap["failedTweets"]; ok {
		t.Fatal("dashboard JSON should omit failedTweets key")
	}
	if _, ok := rawMap["config"]; ok {
		t.Fatal("dashboard JSON should omit config")
	}
	if _, ok := rawMap["downloads"]; ok {
		t.Fatal("dashboard JSON should omit downloads")
	}
	if _, ok := rawMap["failed"]; ok {
		t.Fatal("dashboard JSON should omit failed media list")
	}
	if _, ok := rawMap["archiveSchedules"]; ok {
		t.Fatal("dashboard JSON should omit archiveSchedules")
	}
}

func TestDashboardMetaReturnsStatsWithoutJobs(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://example.com/media.mp4", "media"); err != nil {
		t.Fatalf("create job: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/dashboard/meta", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	raw := response.Body.Bytes()
	var meta dashboardMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if meta.Stats.Total != 1 {
		t.Fatalf("stats.total = %d, want 1", meta.Stats.Total)
	}
	if strings.Contains(string(raw), `"jobs"`) {
		t.Fatal("meta must not include jobs")
	}
}

func TestListJobsPageOmitsStats(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://example.com/media.mp4", "media"); err != nil {
			t.Fatalf("create job: %v", err)
		}
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/jobs?page=1&pageSize=2", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var page jobListPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Page != 1 || page.PageSize != 2 {
		t.Fatalf("page=%d pageSize=%d", page.Page, page.PageSize)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	raw := response.Body.String()
	if strings.Contains(raw, `"stats"`) || strings.Contains(raw, `"failedTweetCount"`) {
		t.Fatal("jobs page must not include dashboard stats")
	}
}

func TestGetJobFilesReturnsDownloadsAndFailedMedia(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	job, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://example.com/media.mp4", "media")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := db.CreateDownload(ctx, storage.DownloadRecord{
		JobID:    job.ID,
		TweetID:  "t1",
		MediaURL: "https://example.com/a.jpg",
		FilePath: "/tmp/a.jpg",
		Bytes:    12,
	}); err != nil {
		t.Fatalf("create download: %v", err)
	}
	if _, err := db.CreateFailedMedia(ctx, storage.FailedMedia{
		JobID:    job.ID,
		MediaURL: "https://example.com/b.jpg",
		Error:    "404",
	}); err != nil {
		t.Fatalf("create failed media: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/jobs/"+strconv.FormatInt(job.ID, 10)+"/files", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var files jobFilesResponse
	if err := json.NewDecoder(response.Body).Decode(&files); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(files.Downloads) != 1 || files.Downloads[0].FilePath != "/tmp/a.jpg" {
		t.Fatalf("downloads = %+v", files.Downloads)
	}
	if len(files.Failed) != 1 || files.Failed[0].Error != "404" {
		t.Fatalf("failed = %+v", files.Failed)
	}

	missing := httptest.NewRequest(http.MethodGet, "/api/jobs/999999/files", nil)
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("missing job files status = %d, want 404", missingResponse.Code)
	}
}

func TestServeDownloadFileSupportsRangeAndKeepsPathContained(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "downloads")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir downloads: %v", err)
	}
	if _, err := db.UpdateConfig(ctx, config.AppConfig{
		DownloadDir: root,
		StorageType: config.StorageLocal,
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	filePath := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(filePath, []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	job, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://video.twimg.com/clip.mp4", "clip")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	record, err := db.CreateDownload(ctx, storage.DownloadRecord{
		JobID: job.ID, MediaURL: "https://video.twimg.com/clip.mp4", FilePath: filePath, Bytes: 10,
	})
	if err != nil {
		t.Fatalf("create download: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/library/downloads/"+strconv.FormatInt(record.ID, 10)+"/file", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "0123456789" {
		t.Fatalf("full response = %d %q, want 200 full media", response.Code, response.Body.String())
	}

	rangeRequest := httptest.NewRequest(http.MethodGet, "/api/library/downloads/"+strconv.FormatInt(record.ID, 10)+"/file", nil)
	rangeRequest.Header.Set("Range", "bytes=2-5")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "2345" {
		t.Fatalf("range response = %d %q, want 206 2345", rangeResponse.Code, rangeResponse.Body.String())
	}

	outside, err := db.CreateDownload(ctx, storage.DownloadRecord{
		JobID: job.ID, MediaURL: "https://video.twimg.com/outside.mp4", FilePath: filepath.Join(t.TempDir(), "outside.mp4"), Bytes: 1,
	})
	if err != nil {
		t.Fatalf("create outside download: %v", err)
	}
	outRequest := httptest.NewRequest(http.MethodGet, "/api/library/downloads/"+strconv.FormatInt(outside.ID, 10)+"/file", nil)
	outResponse := httptest.NewRecorder()
	handler.ServeHTTP(outResponse, outRequest)
	if outResponse.Code != http.StatusForbidden {
		t.Fatalf("outside response = %d, want 403", outResponse.Code)
	}
}

func TestEventsNilBusReturnsServiceUnavailable(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	handler := NewServer(db, nil, nil, nil).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", response.Code, response.Body.String())
	}
}

func TestEventsStreamsWithoutFlusherAssertion(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	eventBus := jobs.NewEventBus()
	server := httptest.NewServer(NewServer(db, nil, nil, eventBus).Routes())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	buf := make([]byte, 512)
	n, err := resp.Body.Read(buf)
	if n == 0 && err != nil {
		t.Fatalf("read connected: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, ": connected") {
		t.Fatalf("first chunk = %q, want connected comment", got)
	}

	eventBus.Publish(jobs.Event{Type: "job.created", JobID: 42})
	n, err = resp.Body.Read(buf)
	if n == 0 && err != nil {
		t.Fatalf("read event: %v", err)
	}
	got = string(buf[:n])
	if !strings.Contains(got, "job.created") {
		t.Fatalf("event chunk = %q", got)
	}
}

func newLibraryTestServer(t *testing.T) (*storage.Store, http.Handler, string) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "downloads")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir downloads: %v", err)
	}
	if _, err := db.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return db, NewServer(db, nil, nil, nil).Routes(), root
}

func TestServeLibraryFileWhitelistsMediaAndKeepsPathContained(t *testing.T) {
	_, handler, root := newLibraryTestServer(t)

	mediaPath := filepath.Join(root, "users", "john_smith", "clip.mp4")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o700); err != nil {
		t.Fatalf("mkdir media dir: %v", err)
	}
	if err := os.WriteFile(mediaPath, []byte("video-bytes"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	textPath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(textPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write text: %v", err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.jpg")
	if err := os.WriteFile(outsidePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	// 媒体文件正常返回。
	request := httptest.NewRequest(http.MethodGet, "/api/library/file?path="+strings.ReplaceAll(url.QueryEscape(mediaPath), "+", "%20"), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "video-bytes" {
		t.Fatalf("media response = %d %q, want 200 video-bytes", response.Code, response.Body.String())
	}

	// 非媒体扩展名被白名单拒绝。
	request = httptest.NewRequest(http.MethodGet, "/api/library/file?path="+url.QueryEscape(textPath), nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("text response = %d, want 403", response.Code)
	}

	// 下载目录之外拒绝。
	request = httptest.NewRequest(http.MethodGet, "/api/library/file?path="+url.QueryEscape(outsidePath), nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("outside response = %d, want 403", response.Code)
	}

	// 目录内的缺失媒体文件返回 404 而非 403。
	request = httptest.NewRequest(http.MethodGet, "/api/library/file?path="+url.QueryEscape(filepath.Join(root, "users", "john_smith", "gone.jpg")), nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing response = %d, want 404", response.Code)
	}
}

func TestServeDownloadFileUnknownIDsReturn404(t *testing.T) {
	_, handler, _ := newLibraryTestServer(t)

	for _, id := range []string{"0", "-1", "99999"} {
		request := httptest.NewRequest(http.MethodGet, "/api/library/downloads/"+id+"/file", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("id %s response = %d, want 404", id, response.Code)
		}
	}
}

func TestServeDownloadPreviewServesLocalPoster(t *testing.T) {
	db, handler, root := newLibraryTestServer(t)
	ctx := context.Background()

	videoPath := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0o600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	job, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://video.twimg.com/clip.mp4", "clip")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	record, err := db.CreateDownload(ctx, storage.DownloadRecord{
		JobID: job.ID, MediaURL: "https://video.twimg.com/clip.mp4", FilePath: videoPath, Bytes: 5,
	})
	if err != nil {
		t.Fatalf("create download: %v", err)
	}
	if err := os.WriteFile(videoPath+".preview.jpg", []byte("poster"), 0o600); err != nil {
		t.Fatalf("write poster: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/library/downloads/"+strconv.FormatInt(record.ID, 10)+"/preview", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "poster" {
		t.Fatalf("preview response = %d %q, want 200 poster", response.Code, response.Body.String())
	}
}

// TestResolveLocalLibraryPath 覆盖媒体库路径解析的边界：目录内文件、目录外拒绝、
// 缺失文件以 os.ErrNotExist 语义返回（供 HTTP 层映射 404）。
func TestResolveLocalLibraryPath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a.jpg")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// macOS 上 TempDir 位于 /var -> /private/var 符号链接下，期望值需同样解析。
	expected, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolveLocalLibraryPath(root, inside); err != nil || got != expected {
		t.Fatalf("inside = %q, %v; want %q", got, err, expected)
	}
	if _, err := resolveLocalLibraryPath(root, filepath.Join(t.TempDir(), "b.jpg")); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside err = %v, want non-ErrNotExist rejection", err)
	}
	if _, err := resolveLocalLibraryPath(root, filepath.Join(root, "missing.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing err = %v, want os.ErrNotExist", err)
	}
}
