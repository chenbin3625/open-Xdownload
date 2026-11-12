package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestLibraryDownloadsSortAndRanges(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, storage.JobKindMediaURL, "media", "media")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name string
		size int64
	}{{"large", 30}, {"small", 10}, {"medium", 20}, {"tie", 20}} {
		path := filepath.Join(root, item.name+".jpg")
		if err := os.WriteFile(path, []byte(strings.Repeat("x", int(item.size))), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateDownload(ctx, storage.DownloadRecord{JobID: job.ID, TweetID: item.name, MediaURL: "https://pbs.twimg.com/" + item.name + ".jpg", FilePath: path, Bytes: item.size}); err != nil {
			t.Fatal(err)
		}
	}
	orphan := filepath.Join(root, "orphan.jpg")
	if err := os.WriteFile(orphan, []byte(strings.Repeat("x", 15)), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2020, 6, 15, 23, 59, 59, 0, time.UTC)
	if err := os.Chtimes(orphan, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	handler := authenticatedRoutes(t, NewServer(store, nil, nil, nil))
	for _, tc := range []struct {
		query string
		want  []string
		total int
	}{
		{"sort=size_desc&pageSize=2", []string{"large.jpg", "tie.jpg"}, 5},
		{"sort=size_desc&pageSize=2&page=2", []string{"medium.jpg", "orphan.jpg"}, 5},
		{"sort=size_asc&pageSize=2", []string{"small.jpg", "orphan.jpg"}, 5},
		{"sort=date_asc&pageSize=1", []string{"orphan.jpg"}, 5},
		{"sort=size_desc&minBytes=15&maxBytes=20", []string{"tie.jpg", "medium.jpg", "orphan.jpg"}, 3},
		{"sort=size_asc&user=unknown&minBytes=15&maxBytes=20", []string{"orphan.jpg", "tie.jpg", "medium.jpg"}, 3},
		{"from=2020-06-15&to=2020-06-15", []string{"orphan.jpg"}, 1},
		{"to=2020-06-14", []string{}, 0},
		{"maxBytes=0", []string{}, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			query := tc.query
			if !strings.Contains(query, "page=") {
				query += "&page=1"
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, newLoopbackRequest(http.MethodGet, "/api/library/downloads?"+query, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var page storage.LibraryPage
			if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(page.Items))
			for _, item := range page.Items {
				got = append(got, filepath.Base(item.FilePath))
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") || page.Pagination.Total != tc.total {
				t.Fatalf("items=%v total=%d, want %v total=%d", got, page.Pagination.Total, tc.want, tc.total)
			}
			if page.Counts["all"] != 5 {
				t.Fatalf("global counts changed: %v", page.Counts)
			}
		})
	}
	for _, query := range []string{"sort=invalid", "from=2020-02-30", "to=bad", "from=2020-06-16&to=2020-06-15", "minBytes=-1", "maxBytes=1.5", "minBytes=21&maxBytes=20", "maxBytes=9223372036854775808"} {
		t.Run(query, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, newLoopbackRequest(http.MethodGet, "/api/library/downloads?page=1&"+query, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400: %s", response.Code, response.Body.String())
			}
		})
	}
}
