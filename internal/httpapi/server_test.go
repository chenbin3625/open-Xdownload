package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestTestStorageUsesSubmittedWebDAVConfigAndSavedPassword(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	putBody := ""

	webdav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "alice" || password != "saved-secret" {
			t.Errorf("BasicAuth = %q/%q/%t, want alice/saved-secret/true", username, password, ok)
		}

		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read PUT body: %v", err)
			}
			putBody = string(body)
			if got := r.Header.Get("If-None-Match"); got != "*" {
				t.Errorf("If-None-Match = %q, want *", got)
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer webdav.Close()

	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.UpdateConfig(ctx, config.AppConfig{WebDAVURL: webdav.URL + "/dav", WebDAVPassword: "saved-secret"}); err != nil {
		t.Fatalf("save current config: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	requestBody := `{
		"storageType": "webdav",
		"webdavUrl": "` + webdav.URL + `/dav",
		"webdavPath": "remote",
		"webdavUsername": "alice",
		"webdavPassword": "********"
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/storage/test", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result storageTestResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !result.OK || result.Type != config.StorageWebDAV {
		t.Fatalf("result = %+v, want ok WebDAV result", result)
	}
	if result.Root != webdav.URL+"/dav/remote" {
		t.Fatalf("root = %q, want %q", result.Root, webdav.URL+"/dav/remote")
	}
	if !strings.HasPrefix(result.Path, result.Root+"/.open-xdownload-storage-test-") {
		t.Fatalf("path = %q, want probe path under %q", result.Path, result.Root)
	}
	if putBody != "open-Xdownload storage test\n" {
		t.Fatalf("PUT body = %q, want probe payload", putBody)
	}

	mu.Lock()
	defer mu.Unlock()
	wantPrefix := []string{
		"MKCOL /dav/remote",
		"PUT /dav/remote/.open-xdownload-storage-test-",
		"DELETE /dav/remote/.open-xdownload-storage-test-",
	}
	if len(paths) != len(wantPrefix) {
		t.Fatalf("paths = %#v, want %d requests", paths, len(wantPrefix))
	}
	for index, prefix := range wantPrefix {
		if !strings.HasPrefix(paths[index], prefix) {
			t.Fatalf("paths[%d] = %q, want prefix %q; all paths=%#v", index, paths[index], prefix, paths)
		}
	}
}

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
}
