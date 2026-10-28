package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

func authRequest(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://downloads.example"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func responseToken(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var result struct{ Token string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Token == "" {
		t.Fatalf("missing token: %s (%v)", w.Body.String(), err)
	}
	return result.Token
}

func TestSingleUserAuthenticationLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Existing jobs/config must survive account initialization on upgrade.
	if _, err := db.CreateJob(context.Background(), storage.JobKindUser, "alice", "archive"); err != nil {
		t.Fatal(err)
	}
	s := NewServer(db, nil, nil, nil)
	handler := s.Routes()
	status := authRequest(handler, "GET", "/api/auth/session", "", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"configured":false`) {
		t.Fatalf("new/upgrade setup status: %d %s", status.Code, status.Body.String())
	}
	for _, path := range []string{"/api/jobs", "/api/config", "/api/events", "/api/library/downloads/1/file", "/api/local-directories"} {
		w := authRequest(handler, "GET", path, "", "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unprotected %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	html := []byte("<html><head></head></html>")
	if got := s.InjectIndexHTML(httptest.NewRequest("GET", "/", nil), html); strings.Contains(string(got), "app-bootstrap") {
		t.Fatal("anonymous HTML leaked bootstrap data")
	}
	setup := authRequest(handler, "POST", "/api/auth/setup", `{"username":"owner","password":"initial-password"}`, "")
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", setup.Code, setup.Body.String())
	}
	token := responseToken(t, setup)
	account, err := db.GetAccount(context.Background())
	if err != nil || account == nil || bcrypt.CompareHashAndPassword(account.PasswordHash, []byte("initial-password")) != nil {
		t.Fatalf("password was not stored as bcrypt: %v", err)
	}
	cookies := setup.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].MaxAge <= 0 || cookies[0].Domain != "" {
		t.Fatalf("persistent host-only HttpOnly cookie: %+v", cookies)
	}
	if w := authRequest(handler, "POST", "/api/auth/setup", `{"username":"other","password":"another-password"}`, ""); w.Code != http.StatusConflict {
		t.Fatalf("second user setup: %d %s", w.Code, w.Body.String())
	}
	if w := authRequest(handler, "POST", "/api/auth/login", `{"username":"owner","password":"wrong-password"}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password accepted: %d", w.Code)
	}
	login := authRequest(handler, "POST", "/api/auth/login", `{"username":"owner","password":"initial-password"}`, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	secondToken := responseToken(t, login)
	if w := authRequest(handler, "GET", "/api/jobs", "", token+"x"); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token accepted: %d", w.Code)
	}
	if w := authRequest(handler, "PUT", "/api/auth/password", `{"currentPassword":"wrong","newPassword":"replacement-password"}`, token); w.Code != http.StatusBadRequest {
		t.Fatalf("wrong current password accepted: %d %s", w.Code, w.Body.String())
	}
	// There is no token expiry; persisted account keys keep tokens valid across restarts.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewServer(db, nil, nil, nil)
	handler = s.Routes()
	w := authRequest(handler, "GET", "/api/jobs", "", token)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "archive") {
		t.Fatalf("restart lost login or old data: %d %s", w.Code, w.Body.String())
	}
	request := httptest.NewRequest("GET", "/api/jobs", nil)
	request.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("cookie authentication: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest("POST", "/api/jobs/1/retry", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cookie-only simple mutation must require CSRF header: %d", response.Code)
	}
	request = httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookies[0])
	if got := s.InjectIndexHTML(request, html); !strings.Contains(string(got), "archive") {
		t.Fatal("authenticated HTML missing bootstrap")
	}
	changed := authRequest(handler, "PUT", "/api/auth/password", `{"currentPassword":"initial-password","newPassword":"replacement-password"}`, token)
	if changed.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", changed.Code, changed.Body.String())
	}
	for _, old := range []string{token, secondToken} {
		if w := authRequest(handler, "GET", "/api/jobs", "", old); w.Code != http.StatusUnauthorized {
			t.Fatalf("old token survived password change: %d", w.Code)
		}
	}
	if got := s.InjectIndexHTML(request, html); strings.Contains(string(got), "app-bootstrap") {
		t.Fatal("old cookie still accesses bootstrap")
	}
	if w := authRequest(handler, "POST", "/api/auth/login", `{"username":"owner","password":"initial-password"}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("old password survived change: %d", w.Code)
	}
	if w := authRequest(handler, "POST", "/api/auth/login", `{"username":"owner","password":"replacement-password"}`, ""); w.Code != http.StatusOK {
		t.Fatalf("new password rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestPasswordChangeClosesOldEventStreams(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := NewServer(db, nil, nil, jobs.NewEventBus()).Routes()
	created := authRequest(handler, "POST", "/api/auth/setup", `{"username":"owner","password":"initial-password"}`, "")
	token := responseToken(t, created)
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	openStream := func(token string) *http.Response {
		t.Helper()
		r, err := http.NewRequest("GET", server.URL+"/api/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("stream status: %d", response.StatusCode)
		}
		return response
	}
	stream := openStream(token)
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, ": connected") {
		t.Fatalf("stream not established: %q %v", line, err)
	}
	changed := authRequest(handler, "PUT", "/api/auth/password", `{"currentPassword":"initial-password","newPassword":"replacement-password"}`, token)
	if changed.Code != http.StatusOK {
		t.Fatalf("password: %d %s", changed.Code, changed.Body.String())
	}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("old stream did not close: %v", err)
	}
	login := authRequest(handler, "POST", "/api/auth/login", `{"username":"owner","password":"replacement-password"}`, "")
	newStream := openStream(responseToken(t, login))
	newStream.Body.Close()
}

func TestSetupIsAtomic(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := NewServer(db, nil, nil, nil).Routes()
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			codes <- authRequest(handler, "POST", "/api/auth/setup", `{"username":"owner","password":"initial-password"}`, "").Code
		})
	}
	wg.Wait()
	close(codes)
	created, conflict := 0, 0
	for code := range codes {
		if code == http.StatusCreated {
			created++
		}
		if code == http.StatusConflict {
			conflict++
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatalf("concurrent setup: created=%d conflict=%d", created, conflict)
	}
}

func TestAuthenticationValidationAndArbitraryHosts(t *testing.T) {
	t.Setenv("OPEN_XDOWNLOAD_ALLOWED_HOSTS", "unused.invalid")
	db, err := storage.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := NewServer(db, nil, nil, nil).Routes()
	for _, body := range []string{`null`, `{}`, `{"username":"  ","password":"initial-password"}`, `{"username":"owner","password":"short"}`, `{"username":"owner","password":"` + strings.Repeat("a", 73) + `"}`} {
		if w := authRequest(handler, "POST", "/api/auth/setup", body, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid setup %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	w := authRequest(handler, "POST", "/api/auth/setup", `{"username":"owner","password":"initial-password"}`, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("domain setup: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "http://another-domain.local/api/jobs", nil)
	r.Header.Set("Origin", "https://reverse-proxy.example")
	r.Header.Set("Authorization", "Bearer "+responseToken(t, w))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	if response.Code != http.StatusOK {
		t.Fatalf("host/origin restrictions remain: %d %s", response.Code, response.Body.String())
	}
}

func TestSessionCookieUsesHTTPSProxy(t *testing.T) {
	for _, target := range []string{"https://downloads.example", "http://backend.internal"} {
		r := httptest.NewRequest("GET", target, nil)
		if r.TLS == nil {
			r.Header.Set("X-Forwarded-Proto", "https")
		}
		w := httptest.NewRecorder()
		setSessionCookie(w, r, "session-token")
		if cookies := w.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
			t.Fatalf("HTTPS session cookie must be Secure: %s %+v", target, cookies)
		}
	}
}
