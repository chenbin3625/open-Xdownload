package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	if _, err := db.UpdateConfig(ctx, config.AppConfig{WebDAVPassword: "saved-secret"}); err != nil {
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
