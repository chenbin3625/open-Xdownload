package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestIncludeWorkbenchBootstrap(t *testing.T) {
	t.Parallel()
	if !includeWorkbenchBootstrap("/") || !includeWorkbenchBootstrap("/overview") {
		t.Fatal("workbench paths should inject bootstrap")
	}
	if includeWorkbenchBootstrap("/settings") {
		t.Fatal("settings should skip workbench bootstrap")
	}
}

func TestParseWorkbenchPage(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/overview?page=3&pageSize=50", nil)
	page, pageSize := parseWorkbenchPage(request)
	if page != 3 || pageSize != 50 {
		t.Fatalf("page=%d pageSize=%d, want 3/50", page, pageSize)
	}
	request = httptest.NewRequest(http.MethodGet, "/?pageSize=15", nil)
	_, pageSize = parseWorkbenchPage(request)
	if pageSize != 20 {
		t.Fatalf("invalid pageSize should fall back to 20, got %d", pageSize)
	}
}

func TestInjectBootstrapHTMLEscapes(t *testing.T) {
	t.Parallel()
	html := []byte("<!doctype html><html><head></head><body></body></html>")
	out := injectBootstrapHTML(html, appBootstrap{
		Jobs: &jobListPage{Items: []storage.Job{}, Page: 1, PageSize: 20},
		Meta: &dashboardMeta{FailedTweetCount: 0},
	})
	if !strings.Contains(string(out), `id="app-bootstrap"`) {
		t.Fatalf("missing bootstrap script: %s", out)
	}
	start := strings.Index(string(out), `id="app-bootstrap">`)
	end := strings.Index(string(out), "</script></head>")
	if start < 0 || end < 0 {
		t.Fatalf("bootstrap markers missing: %s", out)
	}
	raw := string(out)[start+len(`id="app-bootstrap">`) : end]
	var parsed appBootstrap
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("bootstrap json: %v", err)
	}
	if parsed.Jobs == nil || parsed.Jobs.Page != 1 || parsed.Jobs.PageSize != 20 {
		t.Fatalf("parsed jobs page = %+v", parsed.Jobs)
	}
}

func TestInjectIndexHTMLIncludesCreatedJob(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.UpdateConfig(ctx, config.AppConfig{
		DownloadDir: t.TempDir(),
		AuthToken:   "super-secret-token",
		CSRFToken:   "csrf-secret",
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if _, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://example.com/a.mp4", "media"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	api := NewServer(db, nil, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/overview", nil)
	html := api.InjectIndexHTML(request, []byte("<html><head></head></html>"))
	if !strings.Contains(string(html), "https://example.com/a.mp4") {
		t.Fatalf("expected job input in bootstrap html: %s", html)
	}
	if strings.Contains(string(html), "super-secret-token") {
		t.Fatal("workbench bootstrap must not embed plaintext auth token")
	}

	settings := httptest.NewRequest(http.MethodGet, "/settings", nil)
	settingsHTML := api.InjectIndexHTML(settings, []byte("<html><head></head></html>"))
	if !strings.Contains(string(settingsHTML), "app-bootstrap") {
		t.Fatalf("settings html should embed redacted config: %s", settingsHTML)
	}
	if !strings.Contains(string(settingsHTML), "downloadDir") {
		t.Fatalf("settings bootstrap missing downloadDir: %s", settingsHTML)
	}
	if !strings.Contains(string(settingsHTML), "********") {
		t.Fatalf("settings bootstrap missing redacted secret: %s", settingsHTML)
	}
	if strings.Contains(string(settingsHTML), "super-secret-token") {
		t.Fatal("settings bootstrap must not embed plaintext auth token")
	}
	if strings.Contains(string(settingsHTML), "https://example.com/a.mp4") {
		t.Fatalf("settings html should not embed workbench jobs: %s", settingsHTML)
	}
}
