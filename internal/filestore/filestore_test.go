package filestore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
)

func TestWebDAVSaveMediaSkipsConflictWithoutDeletingExistingFile(t *testing.T) {
	var mu sync.Mutex
	deletes := []string{}
	puts := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/media" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("media"))
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			if got := r.Header.Get("If-None-Match"); got != "*" {
				t.Errorf("If-None-Match = %q, want *", got)
			}
			mu.Lock()
			puts = append(puts, r.URL.Path)
			mu.Unlock()
			if strings.HasSuffix(r.URL.Path, "/media.mp4") {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			http.Error(w, "unexpected PUT path", http.StatusInternalServerError)
		case http.MethodDelete:
			mu.Lock()
			deletes = append(deletes, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	store := newWebDAVStore(config.AppConfig{WebDAVURL: server.URL, WebDAVPath: "remote"}.Normalized(), base)
	result, err := store.SaveMedia(context.Background(), downloader.New(), server.URL+"/media", store.Join(store.Root(), "downloads"), "media", downloader.Options{})
	if err != nil {
		t.Fatalf("save media: %v", err)
	}
	if !result.Skipped {
		t.Fatal("skipped = false, want true")
	}
	if !strings.HasSuffix(result.Path, "/media.mp4") {
		t.Fatalf("result path = %q, want original conflict path", result.Path)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(puts) != 1 {
		t.Fatalf("PUT paths = %#v, want one attempt", puts)
	}
	for _, path := range deletes {
		if strings.HasSuffix(path, "/media.mp4") {
			t.Fatalf("original conflict path was deleted: deletes=%#v", deletes)
		}
	}
}
