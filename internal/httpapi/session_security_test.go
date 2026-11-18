package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestLogoutRevokesOnlyCurrentSessionAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	handler := NewServer(db, nil, nil, nil).Routes()
	first := responseToken(t, authRequest(handler, "POST", "/api/auth/setup", `{"username":"owner","password":"initial-password"}`, ""))
	second := responseToken(t, authRequest(handler, "POST", "/api/auth/login", `{"username":"owner","password":"initial-password"}`, ""))
	logout := authRequest(handler, "POST", "/api/auth/logout", "", first)
	if logout.Code != http.StatusOK || len(logout.Result().Cookies()) != 1 || logout.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout: %d %s", logout.Code, logout.Body.String())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewServer(db, nil, nil, nil).Routes()
	if result := authRequest(handler, "GET", "/api/auth/session", "", first); !strings.Contains(result.Body.String(), `"authenticated":false`) {
		t.Fatalf("revoked session status: %s", result.Body.String())
	}
	if result := authRequest(handler, "GET", "/api/jobs", "", first); result.Code != http.StatusUnauthorized {
		t.Fatal("logout was not persisted")
	}
	if result := authRequest(handler, "GET", "/api/jobs", "", second); result.Code != http.StatusOK {
		t.Fatalf("other session was revoked: %d", result.Code)
	}
}

func TestBrowserLoginDoesNotExposeToken(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	request := httptest.NewRequest("POST", "/api/auth/setup", strings.NewReader(`{"username":"owner","password":"initial-password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-XDownload-Request", "1")
	response := httptest.NewRecorder()
	NewServer(db, nil, nil, nil).Routes().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), `"token"`) || len(response.Result().Cookies()) != 1 || !response.Result().Cookies()[0].HttpOnly {
		t.Fatalf("browser credentials: %d %s", response.Code, response.Body.String())
	}
}

func TestOpenedMediaSurvivesDirectoryReplacement(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "media"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "media", "photo.jpg")
	if err := os.WriteFile(path, []byte("safe media"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, file, _, err := NewServer(db, nil, nil, nil).openDownloadFile(context.Background(), root, path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	defer file.Close()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "photo.jpg"), []byte("private secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "media"), filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "media")); err != nil {
		t.Skip(err)
	}
	response := httptest.NewRecorder()
	serveOpenedMedia(response, httptest.NewRequest("GET", "/media", nil), file)
	if response.Body.String() != "safe media" {
		t.Fatalf("read replaced path: %q", response.Body.String())
	}
	if leaked, err := archive.Open("media/photo.jpg"); err == nil {
		defer leaked.Close()
		content, _ := io.ReadAll(leaked)
		t.Fatalf("root opened external content: %q", content)
	}
}
