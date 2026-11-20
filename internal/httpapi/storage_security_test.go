package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestUpdateStorageRejectsRootOutsideAllowedRootsAndRegistersDownloadRoot(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)

	store, err := storage.Open(filepath.Join(home, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	server := NewServer(store, nil, nil, nil)
	handler := authenticatedRoutes(t, server)

	// Outside HOME/DownloadDir must be rejected with 400.
	badDoc := config.DefaultStorage(filepath.Join(outside, "escape"))
	badBody, _ := json.Marshal(badDoc)
	req := newLoopbackRequest(http.MethodPut, "/api/storage", bytes.NewReader(badBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-XDownload-Request", "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("outside root status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// Inside HOME must be accepted and immediately registered in download_roots.
	goodRoot := filepath.Join(home, "custom-storage")
	goodDoc := config.DefaultStorage(goodRoot)
	goodBody, _ := json.Marshal(goodDoc)
	req = newLoopbackRequest(http.MethodPut, "/api/storage", bytes.NewReader(goodBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-XDownload-Request", "1")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inside HOME status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	roots, err := store.DownloadRoots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range roots {
		if filepath.Clean(r) == filepath.Clean(goodRoot) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q to be registered in download_roots immediately, got %v", goodRoot, roots)
	}
}

func TestMigrateStorageDeduplicatesActiveJob(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := jobs.NewManager(store, nil, nil)
	server := NewServer(store, nil, manager, nil)
	handler := authenticatedRoutes(t, server)

	req1 := newLoopbackRequest(http.MethodPost, "/api/storage/migrate", nil)
	req1.Header.Set("X-XDownload-Request", "1")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first migrate status = %d, want 201: %s", rec1.Code, rec1.Body.String())
	}

	req2 := newLoopbackRequest(http.MethodPost, "/api/storage/migrate", nil)
	req2.Header.Set("X-XDownload-Request", "1")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second migrate status = %d, want 409: %s", rec2.Code, rec2.Body.String())
	}
}

func TestSessionValidReadOnlyDoesNotBumpLastSeen(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	start := time.Now().Add(-2 * time.Hour).UTC()
	if err := store.CreateSession(ctx, "tok-readonly", start); err != nil {
		t.Fatal(err)
	}
	ok, err := store.SessionValidReadOnly(ctx, "tok-readonly", start.Add(time.Hour))
	if err != nil || !ok {
		t.Fatalf("SessionValidReadOnly = %v, %v", ok, err)
	}
	// After SessionIdleTimeout from original start, read-only check must fail even though
	// we called SessionValidReadOnly halfway through.
	ok, err = store.SessionValidReadOnly(ctx, "tok-readonly", start.Add(storage.SessionIdleTimeout+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected idle session to expire without read-only calls bumping last_seen_at")
	}
}

func TestIsStreamingPathAllowsLibraryUsersCompression(t *testing.T) {
	if isStreamingPath("/api/library/users") {
		t.Fatal("/api/library/users should be compressible JSON, not streaming")
	}
	if !isStreamingPath("/api/assets/abc123") {
		t.Fatal("/api/assets/* should be treated as streaming")
	}
	if !isStreamingPath("/api/library/downloads/1/file") {
		t.Fatal("/api/library/downloads/1/file should be treated as streaming")
	}
}

func TestUnsafeDownloadRootFailsClosedOnUnresolvableSymlink(t *testing.T) {
	dir := t.TempDir()
	// Create a symlink loop so EvalSymlinks returns an error.
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if !unsafeDownloadRoot(a) {
		t.Fatal("expected unresolvable symlink loop to fail closed as unsafe")
	}
}
