package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func TestUpdateConfigBlocksCaseInsensitiveDownloadDirBypassAndLegacyCookies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	db := openConfigStore(t)
	ctx := context.Background()
	initialDir := filepath.Join(home, "downloads")
	cfg := config.Default()
	cfg.DownloadDir = initialDir
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	handler := authenticatedRoutes(t, NewServer(db, nil, nil, nil))

	for _, body := range []string{
		`{"DownloadDir":"/etc"}`,
		`{"DOWNLOADDIR":"/etc"}`,
		`{"downloaddir":"/etc"}`,
		`{"DownloadDir":""}`,
		`{"downloadDir":` + strconv.Quote(filepath.Join(home, "ok")) + `,"DownloadDir":"/etc"}`,
		`{"proxyUrl":"","proxyurl":""}`,
	} {
		resp := doRequest(t, handler, http.MethodPut, "/api/config", body, map[string]string{"Content-Type": "application/json"})
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400 (body=%s)", body, resp.Code, resp.Body.String())
		}
	}

	stored, err := db.GetStoredConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(stored.DownloadDir) != filepath.Clean(initialDir) {
		t.Fatalf("stored DownloadDir mutated to %q, want %q", stored.DownloadDir, initialDir)
	}
	roots, err := db.DownloadRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if filepath.Clean(root) == "/etc" {
			t.Fatalf("/etc leaked into download_roots: %v", roots)
		}
	}

	// Case-insensitive (including Unicode simple fold) legacy cookie fields must be rejected for real values and ignored for placeholders.
	for _, body := range []string{
		`{"AuthToken":"real-auth-token"}`,
		`{"authToKen":"real-auth-token"}`,
		`{"CSRFTOKEN":"real-csrf-token"}`,
		`{"cſrfToken":"real-csrf-token"}`,
		`{"Ct0":"real-ct0-token"}`,
		`{"AdditionalCookies":"auth_token=a; ct0=b"}`,
		`{"authToken":"********","authToKen":"********"}`,
	} {
		resp := doRequest(t, handler, http.MethodPut, "/api/config", body, map[string]string{"Content-Type": "application/json"})
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("legacy cookie body %s: status = %d, want 400 (body=%s)", body, resp.Code, resp.Body.String())
		}
	}
	placeholderResp := doRequest(t, handler, http.MethodPut, "/api/config",
		`{"AuthToken":"********","Ct0":"","AdditionalCookies":null}`,
		map[string]string{"Content-Type": "application/json"})
	if placeholderResp.Code != http.StatusOK {
		t.Fatalf("legacy placeholder status = %d, want 200 (body=%s)", placeholderResp.Code, placeholderResp.Body.String())
	}
}

func TestUpdateStorageTrimsTargetRootWhitespace(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)

	store, err := storage.Open(filepath.Join(home, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	handler := authenticatedRoutes(t, NewServer(store, nil, nil, nil))

	// Outside root padded with whitespace must still be rejected.
	badDoc := config.DefaultStorage("  " + filepath.Join(outside, "escape") + "  ")
	badBody, _ := json.Marshal(badDoc)
	badReq := newLoopbackRequest(http.MethodPut, "/api/storage", bytes.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("X-XDownload-Request", "1")
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("padded outside root status = %d, want 400: %s", badRec.Code, badRec.Body.String())
	}

	// Valid local + nas roots padded with whitespace must all be trimmed before persisting.
	cleanRoot := filepath.Join(home, "trimmed-storage")
	cleanNAS := filepath.Join(home, "trimmed-nas")
	goodDoc := config.DefaultStorage("  " + cleanRoot + "  ")
	goodDoc.Targets = append(goodDoc.Targets, config.StorageTarget{
		ID:   "nas",
		Name: "NAS",
		Type: "nas",
		Root: "\t " + cleanNAS + " \n",
	})
	goodBody, _ := json.Marshal(goodDoc)
	goodReq := newLoopbackRequest(http.MethodPut, "/api/storage", bytes.NewReader(goodBody))
	goodReq.Header.Set("Content-Type", "application/json")
	goodReq.Header.Set("X-XDownload-Request", "1")
	goodRec := httptest.NewRecorder()
	handler.ServeHTTP(goodRec, goodReq)
	if goodRec.Code != http.StatusOK {
		t.Fatalf("padded valid root status = %d, want 200: %s", goodRec.Code, goodRec.Body.String())
	}

	saved, err := store.GetStorageDocument(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Targets) != 2 || saved.Targets[0].Root != cleanRoot || saved.Targets[1].Root != cleanNAS {
		t.Fatalf("saved roots = %+v, want %q and %q", saved.Targets, cleanRoot, cleanNAS)
	}
}

func TestAssetAndMediaSecurityHeadersAndEmptyMetadata(t *testing.T) {
	store, handler, root := newLibraryTestServer(t)
	ctx := context.Background()

	// 1. Asset with empty ContentType and empty ContentHash.
	doc := config.DefaultStorage(t.TempDir())
	doc.Enabled = true
	doc, err := store.SaveStorageDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	// Minimal JPEG header bytes so http.ServeContent can also sniff if needed.
	imgBytes := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	if err := os.WriteFile(filepath.Join(doc.Targets[0].Root, "photo.jpg"), imgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	asset, err := store.CreateAsset(ctx, storage.Asset{
		ID:            "empty-meta-asset",
		Kind:          config.AssetImage,
		PolicyVersion: doc.Version,
		TargetID:      "local",
		TargetVersion: doc.Targets[0].Version,
		Key:           "photo.jpg",
		Size:          int64(len(imgBytes)),
		ContentType:   "",
		ContentHash:   "",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newLoopbackRequest(http.MethodGet, "/api/assets/"+asset.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("Content-Type should be detected when Asset.ContentType is empty")
	}
	if _, hasETag := rec.Header()["Etag"]; hasETag {
		t.Fatalf("ETag header should be omitted when ContentHash is empty, got %q", rec.Header().Get("ETag"))
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != mediaContentSecurityPolicy {
		t.Fatalf("asset CSP = %q, want %q", got, mediaContentSecurityPolicy)
	}
	for _, directive := range []string{"default-src 'none'", "media-src 'self'", "img-src 'self'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), directive) {
			t.Fatalf("asset CSP missing %q: %q", directive, rec.Header().Get("Content-Security-Policy"))
		}
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("asset X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("asset X-Content-Type-Options = %q, want nosniff", got)
	}

	// 1b. Asset with valid non-empty ContentHash emits quoted ETag.
	hashedAsset, err := store.CreateAsset(ctx, storage.Asset{
		ID:            "hashed-asset",
		Kind:          config.AssetImage,
		PolicyVersion: doc.Version,
		TargetID:      "local",
		TargetVersion: doc.Targets[0].Version,
		Key:           "photo.jpg",
		Size:          int64(len(imgBytes)),
		ContentType:   "image/jpeg",
		ContentHash:   "abc123def",
	})
	if err != nil {
		t.Fatal(err)
	}
	hashedRec := httptest.NewRecorder()
	handler.ServeHTTP(hashedRec, newLoopbackRequest(http.MethodGet, "/api/assets/"+hashedAsset.ID, nil))
	if hashedRec.Code != http.StatusOK {
		t.Fatalf("hashed asset status = %d, want 200", hashedRec.Code)
	}
	if got := hashedRec.Header().Get("ETag"); got != `"abc123def"` {
		t.Fatalf("hashed asset ETag = %q, want %q", got, `"abc123def"`)
	}

	// 2. /api/library/file also emits strict media security headers.
	mediaPath := filepath.Join(root, "clip.jpg")
	if err := os.WriteFile(mediaPath, imgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	libRec := httptest.NewRecorder()
	handler.ServeHTTP(libRec, newLoopbackRequest(http.MethodGet, "/api/library/file?path="+mediaPath, nil))
	if libRec.Code != http.StatusOK {
		t.Fatalf("library file status = %d, want 200: %s", libRec.Code, libRec.Body.String())
	}
	if got := libRec.Header().Get("Content-Security-Policy"); got != mediaContentSecurityPolicy {
		t.Fatalf("library file CSP = %q, want %q", got, mediaContentSecurityPolicy)
	}
	if got := libRec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("library file X-Frame-Options = %q, want DENY", got)
	}
}

func TestLoginRateEvictsOldestEvenWhenAllBucketsLockedAndHandlesProxyKey(t *testing.T) {
	s := NewServer(nil, nil, nil, nil)
	now := time.Now()
	// Fill every bucket to maxLoginAttempts; "oldest" has the earliest last timestamp.
	for i := 0; i < maxLoginClients; i++ {
		ts := now.Add(-time.Second)
		key := strconv.Itoa(i)
		if i == 0 {
			ts = now.Add(-2 * time.Minute)
			key = "oldest"
		}
		attempts := make([]time.Time, maxLoginAttempts)
		for j := range attempts {
			attempts[j] = ts
		}
		s.loginAttempts[key] = attempts
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "203.0.113.99:5555"
	if !s.checkLoginRate(httptest.NewRecorder(), req) {
		t.Fatal("new client must not get 429 global lockout even when all buckets are locked")
	}
	if len(s.loginAttempts) != maxLoginClients {
		t.Fatalf("buckets = %d, want %d", len(s.loginAttempts), maxLoginClients)
	}
	if _, exists := s.loginAttempts["oldest"]; exists {
		t.Fatal("oldest bucket should have been evicted")
	}

	// X-Forwarded-For: trusted only from loopback, rightmost non-trusted valid IP wins (anti-spoofing).
	proxied := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	proxied.RemoteAddr = "127.0.0.1:43210"
	proxied.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.42")
	if got := loginClientKey(proxied); got != "203.0.113.42" {
		t.Fatalf("loopback proxied key = %q, want 203.0.113.42", got)
	}

	// Chained loopback proxy hops are skipped from the right.
	chained := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	chained.RemoteAddr = "[::1]:43210"
	chained.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.42, 127.0.0.1")
	if got := loginClientKey(chained); got != "203.0.113.42" {
		t.Fatalf("chained loopback proxied key = %q, want 203.0.113.42", got)
	}

	// X-Real-IP fallback when X-Forwarded-For is absent on loopback proxy.
	realIPReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	realIPReq.RemoteAddr = "127.0.0.1:43210"
	realIPReq.Header.Set("X-Real-IP", "203.0.113.55")
	if got := loginClientKey(realIPReq); got != "203.0.113.55" {
		t.Fatalf("loopback X-Real-IP key = %q, want 203.0.113.55", got)
	}

	direct := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	direct.RemoteAddr = "198.51.100.77:43210"
	direct.Header.Set("X-Forwarded-For", "203.0.113.42")
	if got := loginClientKey(direct); got != "198.51.100.77" {
		t.Fatalf("non-loopback key must ignore X-Forwarded-For: got %q, want 198.51.100.77", got)
	}

	// OPEN_XDOWNLOAD_TRUSTED_PROXIES trusts configured proxy CIDRs/IPs (e.g. Docker nginx).
	t.Setenv("OPEN_XDOWNLOAD_TRUSTED_PROXIES", "172.18.0.0/16, 10.0.0.5")
	dockerProxied := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	dockerProxied.RemoteAddr = "172.18.0.2:43210"
	dockerProxied.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.88, 10.0.0.5")
	if got := loginClientKey(dockerProxied); got != "203.0.113.88" {
		t.Fatalf("trusted proxy CIDR key = %q, want 203.0.113.88", got)
	}
}

func TestLoginRateBehindReverseProxyIsolatesAttackerAndAdmin(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "proxy-auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	server := NewServer(db, nil, nil, nil)
	handler := server.Routes()

	setupReq := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(`{"username":"owner","password":"initial-password"}`))
	setupReq.RemoteAddr = "127.0.0.1:40000"
	setupReq.Header.Set("Content-Type", "application/json")
	setupReq.Header.Set("X-Forwarded-For", "203.0.113.20")
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setupReq)
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d: %s", setupRec.Code, setupRec.Body.String())
	}

	postLogin := func(xff, password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"owner","password":%q}`, password)))
		req.RemoteAddr = "127.0.0.1:40001"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Attacker makes 9 failed login attempts behind the same loopback proxy.
	for i := 0; i < maxLoginAttempts-1; i++ {
		if rec := postLogin("198.51.100.10", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attacker attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}

	// Admin logs in successfully behind the same loopback proxy; this must NOT wipe the attacker's failures.
	if rec := postLogin("203.0.113.20", "initial-password"); rec.Code != http.StatusOK {
		t.Fatalf("admin login status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Attacker's 10th failure fills their bucket, and 11th is rate-limited (proving admin login didn't clear it).
	if rec := postLogin("198.51.100.10", "wrong-password"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("attacker 10th attempt: status = %d, want 401", rec.Code)
	}
	if rec := postLogin("198.51.100.10", "wrong-password"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attacker 11th attempt: status = %d, want 429", rec.Code)
	}

	// Admin is still NOT locked out despite attacker hitting 429.
	if rec := postLogin("203.0.113.20", "initial-password"); rec.Code != http.StatusOK {
		t.Fatalf("admin login after attacker lockout: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestXAccountsStaleProbeDoesNotDisableUpdatedCookie(t *testing.T) {
	server, httpServer := newXAccountsTestServer(t)
	created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
		"cookie": cookieLine("stale-old-auth-token-1111", "stale-old-ct0-1111"),
	}))

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	setHomeProber(t, server, func(_ context.Context, _ xapi.AccountCredentials) xapi.HomeProbeResult {
		close(probeStarted)
		<-releaseProbe
		return xapi.HomeProbeResult{Unauthorized: true}
	})

	type checkOutcome struct {
		resp *http.Response
		err  error
	}
	checkDone := make(chan checkOutcome, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/x-accounts/"+itoa(created.ID)+"/check", nil)
		if err != nil {
			checkDone <- checkOutcome{err: err}
			return
		}
		resp, err := httpServer.Client().Do(req)
		checkDone <- checkOutcome{resp: resp, err: err}
	}()

	<-probeStarted
	// Update cookie while slow probe for old cookie is in flight.
	patchResp := xAccountsRequest(t, httpServer, http.MethodPatch, "/api/x-accounts/"+itoa(created.ID), map[string]any{
		"cookie": cookieLine("fresh-new-auth-token-2222", "fresh-new-ct0-2222"),
	})
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d: %s", patchResp.StatusCode, readBodyString(t, patchResp))
	}
	close(releaseProbe)

	outcome := <-checkDone
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	defer outcome.resp.Body.Close()
	payload := mustXAccount[xAccountCheckResponse](t, outcome.resp)
	if payload.OK {
		t.Fatalf("stale probe should not report OK: %+v", payload)
	}
	latest, err := server.store.GetXAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Active || latest.ErrorMsg != "" || latest.AuthToken != "fresh-new-auth-token-2222" {
		t.Fatalf("stale probe corrupted updated account: active=%v err=%q token=%q", latest.Active, latest.ErrorMsg, latest.AuthToken)
	}
}

func TestXAccountsMaskedProxyConcurrentPatchPreservesCredentials(t *testing.T) {
	server, httpServer := newXAccountsTestServer(t)
	created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
		"cookie": cookieLine("proxy-race-auth-token-1111", "proxy-race-ct0-1111"),
		"proxy":  "http://olduser:oldpass@127.0.0.1:7890",
	}))

	newProxy := "http://newuser:newpass@127.0.0.1:7890"
	maskedProxy := "http://" + config.SecretPlaceholder + "@127.0.0.1:7890"

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	sendPatch := func(payload map[string]any) {
		defer wg.Done()
		<-start
		body, _ := json.Marshal(payload)
		req, err := http.NewRequest(http.MethodPatch, httpServer.URL+"/api/x-accounts/"+itoa(created.ID), bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpServer.Client().Do(req)
		if err != nil {
			errs <- err
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errs <- fmt.Errorf("status = %d", resp.StatusCode)
		}
	}

	wg.Add(2)
	go sendPatch(map[string]any{"proxy": newProxy})
	go sendPatch(map[string]any{"proxy": maskedProxy, "userAgent": "CustomUA/1.0"})
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	latest, err := server.store.GetXAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Proxy != newProxy {
		t.Fatalf("proxy lost update: got %q, want %q", latest.Proxy, newProxy)
	}
	if latest.UserAgent != "CustomUA/1.0" {
		t.Fatalf("userAgent = %q, want CustomUA/1.0", latest.UserAgent)
	}
}

func TestXAccountsImportLimitAndCheckAllCancellation(t *testing.T) {
	server, httpServer := newXAccountsTestServer(t)

	// 1. Batch import exceeding maxXAccountImportItems is rejected with 400.
	lines := make([]string, maxXAccountImportItems+1)
	for i := range lines {
		lines[i] = fmt.Sprintf("auth_token=limit-auth-%04d-token; ct0=limit-ct0-%04d-token", i, i)
	}
	resp := xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/import", map[string]any{
		"text": strings.Join(lines, "\n"),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-limit import status = %d, want 400: %s", resp.StatusCode, readBodyString(t, resp))
	}

	// 2. checkAllXAccounts stops enqueuing immediately on context cancellation.
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if _, err := server.store.CreateXAccount(ctx, storage.XAccount{
			AuthToken: fmt.Sprintf("cancel-auth-%04d-token", i),
			CSRFToken: fmt.Sprintf("cancel-ct0-%04d-token", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	reqCtx, cancel := context.WithCancel(ctx)
	var probed atomic.Int32
	var once sync.Once
	server.SetHomeProber(func(pctx context.Context, _ xapi.AccountCredentials) xapi.HomeProbeResult {
		probed.Add(1)
		once.Do(cancel)
		<-pctx.Done()
		return xapi.HomeProbeResult{Err: pctx.Err()}
	})
	req := httptest.NewRequest(http.MethodPost, "/api/x-accounts/check-all", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()
	server.checkAllXAccounts(rec, req)
	if got := probed.Load(); got >= 20 {
		t.Fatalf("checkAllXAccounts probed %d/20 accounts despite immediate cancellation", got)
	}
}

func TestCompressJSONAbortHandlerDoesNotPublishPartialResponse(t *testing.T) {
	for _, encoding := range []string{"", "gzip", "br"} {
		t.Run("encoding_"+encoding, func(t *testing.T) {
			handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Set-Cookie", "evil=1")
				w.Header().Set("ETag", `"partial-etag"`)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"partial":true,`))
				panic(http.ErrAbortHandler)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			if encoding != "" {
				req.Header.Set("Accept-Encoding", encoding)
			}
			rec := httptest.NewRecorder()
			var recovered any
			func() {
				defer func() {
					recovered = recover()
				}()
				handler.ServeHTTP(rec, req)
			}()
			if recovered != http.ErrAbortHandler {
				t.Fatalf("recovered = %v, want http.ErrAbortHandler", recovered)
			}
			if rec.Flushed || rec.Body.Len() != 0 {
				t.Fatalf("ErrAbortHandler published buffered response: code=%d body=%q", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("ETag") != "" {
				t.Fatalf("ErrAbortHandler left dirty headers: %v", rec.Header())
			}
		})
	}
}

func TestCompressJSONPanicRecoveryClearsHandlerSetHeaders(t *testing.T) {
	handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", "99999")
		w.Header().Set("ETag", `"stale-etag"`)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("Set-Cookie", "evil=1")
		w.Header().Add("Vary", "Cookie")
		w.Header().Set("X-Frame-Options", "ALLOWALL")
		w.Header().Set("X-Content-Type-Options", "bogus")
		_, _ = w.Write([]byte(`{"partial":`))
		panic("boom")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("stale Content-Encoding survived panic recovery: %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Header().Get("ETag") != "" {
		t.Fatalf("stale ETag survived panic recovery: %q", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("stale Set-Cookie survived panic recovery: %q", rec.Header().Get("Set-Cookie"))
	}
	if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
		t.Fatalf("Vary = %v, want [Accept-Encoding]", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("Content-Length = %q, want %d", rec.Header().Get("Content-Length"), rec.Body.Len())
	}
	if !strings.Contains(rec.Body.String(), "internal error") || strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("body = %q, want clean internal error JSON", rec.Body.String())
	}

	// Streaming path panic before WriteHeader also clears handler-set headers.
	streamHandler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "evil=1")
		w.Header().Set("ETag", `"stale"`)
		w.Header().Set("Cache-Control", "public, max-age=9999")
		panic("stream-boom")
	}))
	streamRec := httptest.NewRecorder()
	streamHandler.ServeHTTP(streamRec, httptest.NewRequest(http.MethodGet, "/api/assets/test-id", nil))
	if streamRec.Code != http.StatusInternalServerError {
		t.Fatalf("stream panic status = %d, want 500", streamRec.Code)
	}
	if streamRec.Header().Get("Set-Cookie") != "" || streamRec.Header().Get("ETag") != "" {
		t.Fatalf("stream panic left dirty headers: %v", streamRec.Header())
	}
	if streamRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("stream panic Cache-Control = %q, want no-store", streamRec.Header().Get("Cache-Control"))
	}
}
