package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/andybalholm/brotli"
	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/go-chi/chi/v5"
)

func TestServeLibraryFileRejectsSymlinkToNonMedia(t *testing.T) {
	_, handler, root := newLibraryTestServer(t)
	secretPath := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top-secret-text"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "disguised.jpg")
	if err := os.Symlink(secretPath, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	resp := doRequest(t, handler, http.MethodGet, "/api/library/file?path="+linkPath, "", nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("symlink to non-media status = %d, want 403; body = %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "top-secret-text") {
		t.Fatal("response leaked non-media symlink target content")
	}
	if !strings.Contains(resp.Body.String(), "不允许读取非媒体文件") {
		t.Fatalf("unexpected error message: %s", resp.Body.String())
	}
}

func TestXAccountProbeMessageRedactsShortAndSpecialCharProxyPasswords(t *testing.T) {
	cases := []struct {
		name      string
		proxy     string
		errText   string
		forbidden []string
	}{
		{
			name:      "short password",
			proxy:     "http://usr:pw1@127.0.0.1:7890",
			errText:   "proxyconnect tcp: dial tcp http://usr:pw1@127.0.0.1:7890: password pw1 rejected for usr:pw1@",
			forbidden: []string{"pw1", "usr:pw1"},
		},
		{
			name:      "special characters raw and encoded",
			proxy:     "http://user1:p%40ss%3A1@127.0.0.1:7890",
			errText:   "dial failed via http://user1:p%40ss%3A1@127.0.0.1:7890 (raw=p@ss:1 enc=p%40ss%3A1 userinfo=user1:p%40ss%3A1@)",
			forbidden: []string{"p@ss:1", "p%40ss%3A1", "user1:p%40ss%3A1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creds := xapi.AccountCredentials{Proxy: tc.proxy}
			got := xAccountProbeMessage(errors.New(tc.errText), "fallback", creds)
			for _, secret := range tc.forbidden {
				if strings.Contains(got, secret) {
					t.Fatalf("probe message leaked %q: %q", secret, got)
				}
			}
			if !strings.Contains(got, "127.0.0.1:7890") {
				t.Fatalf("probe message lost proxy host context: %q", got)
			}
		})
	}
}

func TestUpdateConfigProxyRedactionAndDownloadDirPatch(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	outsideDir := t.TempDir()
	// Seed config with a directory outside HOME (e.g. from initial env/DB setup).
	cfg := config.Default()
	cfg.DownloadDir = outsideDir
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	handler := authenticatedRoutes(t, NewServer(db, nil, nil, nil))

	// Invalid proxy URL containing credentials must not leak password in 400 error.
	badProxy := `{"proxyURL":"http://user:secret-proxy-pw@[::1"}`
	resp := doRequest(t, handler, http.MethodPut, "/api/config", badProxy, map[string]string{"Content-Type": "application/json"})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid proxy status = %d, want 400: %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "secret-proxy-pw") || strings.Contains(resp.Body.String(), "parse") {
		t.Fatalf("proxy validation leaked raw URL/error: %s", resp.Body.String())
	}

	// Omitting downloadDir in PATCH must skip downloadDir validation even if stored DownloadDir is outside HOME.
	patch := `{"maxFilenameLength":160}`
	resp = doRequest(t, handler, http.MethodPut, "/api/config", patch, map[string]string{"Content-Type": "application/json"})
	if resp.Code != http.StatusOK {
		t.Fatalf("patch without downloadDir status = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	after, err := db.GetStoredConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.MaxFilenameLength != 160 || after.DownloadDir != outsideDir {
		t.Fatalf("unexpected config after patch: %+v", after)
	}

	// Explicit empty downloadDir must be rejected cleanly.
	resp = doRequest(t, handler, http.MethodPut, "/api/config", `{"downloadDir":""}`, map[string]string{"Content-Type": "application/json"})
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "下载目录不能为空") {
		t.Fatalf("empty downloadDir status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestCompressJSONHEADContentLengthAndPoolReset(t *testing.T) {
	largeHandler := compressJSON(http.HandlerFunc(jsonHandler))

	for _, encoding := range []string{"gzip", "br"} {
		t.Run("HEAD_"+encoding, func(t *testing.T) {
			srv := httptest.NewServer(largeHandler)
			defer srv.Close()

			do := func(method string) (*http.Response, []byte) {
				req, err := http.NewRequest(method, srv.URL+"/api/config", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Accept-Encoding", encoding)
				res, err := srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				b, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				return res, b
			}

			getRes, getBody := do(http.MethodGet)
			headRes, headBody := do(http.MethodHead)

			if getRes.Header.Get("Content-Encoding") != encoding || headRes.Header.Get("Content-Encoding") != encoding {
				t.Fatalf("Content-Encoding mismatch: GET=%q HEAD=%q", getRes.Header.Get("Content-Encoding"), headRes.Header.Get("Content-Encoding"))
			}
			if getRes.Header.Get("Content-Length") != strconv.Itoa(len(getBody)) {
				t.Fatalf("GET Content-Length=%q, body len=%d", getRes.Header.Get("Content-Length"), len(getBody))
			}
			if headRes.Header.Get("Content-Length") != getRes.Header.Get("Content-Length") {
				t.Fatalf("HEAD Content-Length=%q, want %q", headRes.Header.Get("Content-Length"), getRes.Header.Get("Content-Length"))
			}
			if len(headBody) != 0 {
				t.Fatalf("HEAD body len=%d, want 0", len(headBody))
			}
		})
	}

	t.Run("204_and_304_skip_compression_and_body", func(t *testing.T) {
		for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
			handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != status || rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 0 {
				t.Fatalf("status %d: code=%d encoding=%q body=%d", status, rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.Len())
			}
		}
	})

	t.Run("writeCompressed_resets_pooled_writer_on_error", func(t *testing.T) {
		for _, enc := range []string{httpxEncoding(t, true), httpxEncoding(t, false)} {
			failW := &failingWriter{failAfter: 0}
			if err := writeCompressed(failW, enc, bytes.Repeat([]byte("a"), 2048)); err == nil {
				t.Fatalf("expected error from failingWriter for %s", enc)
			}
			var okBuf bytes.Buffer
			if err := writeCompressed(&okBuf, enc, bytes.Repeat([]byte("b"), 2048)); err != nil {
				t.Fatalf("subsequent writeCompressed(%s) failed after pool return: %v", enc, err)
			}
			if failW.writesAfterError != 0 {
				t.Fatalf("pooled writer still pinned old failingWriter (%s)", enc)
			}
			decoded := decodeCompressedPayload(t, okBuf.Bytes(), enc)
			if decoded != strings.Repeat("b", 2048) {
				t.Fatalf("decoded payload corrupted after pool reuse (%s)", enc)
			}
		}
	})
}

func httpxEncoding(t *testing.T, br bool) string {
	t.Helper()
	if br {
		return "br"
	}
	return "gzip"
}

type failingWriter struct {
	failAfter        int
	writesAfterError int
	failed           bool
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.failed {
		f.writesAfterError++
		return 0, errors.New("still failing")
	}
	if f.failAfter <= 0 {
		f.failed = true
		return 0, errors.New("write failed")
	}
	f.failAfter -= len(p)
	return len(p), nil
}

func decodeCompressedPayload(t *testing.T, data []byte, encoding string) string {
	t.Helper()
	if encoding == "br" {
		b, err := io.ReadAll(brotli.NewReader(bytes.NewReader(data)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRelativeDownloadRootServesLibraryAndDownloadFiles(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)

	relRoot := "./downloads"
	if err := os.MkdirAll(relRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	mediaFile := filepath.Join(relRoot, "clip.jpg")
	if err := os.WriteFile(mediaFile, []byte("relative-root-jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := openConfigStore(t)
	ctx := context.Background()
	cfg := config.Default()
	cfg.DownloadDir = relRoot
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	job, err := db.CreateJob(ctx, storage.JobKindMediaURL, "https://pbs.twimg.com/clip.jpg", "clip")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := db.CreateDownload(ctx, storage.DownloadRecord{
		JobID:    job.ID,
		MediaURL: "https://pbs.twimg.com/clip.jpg",
		FilePath: mediaFile,
		Bytes:    18,
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := authenticatedRoutes(t, NewServer(db, nil, nil, nil))

	for _, target := range []string{
		"/api/library/file?path=" + mediaFile,
		fmt.Sprintf("/api/library/downloads/%d/file", rec.ID),
		fmt.Sprintf("/api/library/downloads/%d/preview", rec.ID),
	} {
		resp := doRequest(t, handler, http.MethodGet, target, "", nil)
		if resp.Code != http.StatusOK || resp.Body.String() != "relative-root-jpeg" {
			t.Fatalf("%s: status=%d body=%q, want 200 relative-root-jpeg", target, resp.Code, resp.Body.String())
		}
	}
}

func TestPosterFetchSingleFlightUnblocksWaitersOnPanic(t *testing.T) {
	group := &posterFetchSingleFlight{}
	entered := make(chan struct{})
	release := make(chan struct{})

	leaderPanic := make(chan any, 1)
	go func() {
		defer func() {
			leaderPanic <- recover()
		}()
		_ = group.Do(context.Background(), "panic-key", func() error {
			close(entered)
			<-release
			panic("boom")
		})
	}()

	<-entered
	waiterErr := make(chan error, 1)
	go func() {
		waiterErr <- group.Do(context.Background(), "panic-key", func() error {
			return nil
		})
	}()
	// Give waiter goroutine a brief moment to attach to call.done before panicking leader.
	time.Sleep(15 * time.Millisecond)
	close(release)

	if p := <-leaderPanic; p != "boom" {
		t.Fatalf("leader panic = %v, want boom", p)
	}
	select {
	case err := <-waiterErr:
		if err == nil {
			t.Fatal("waiter should receive fallback error after leader panic")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter deadlocked after leader panic")
	}

	// Subsequent call on same key must not be stuck or poisoned.
	ran := false
	if err := group.Do(context.Background(), "panic-key", func() error {
		ran = true
		return nil
	}); err != nil || !ran {
		t.Fatalf("post-panic Do: err=%v ran=%v", err, ran)
	}
}

func TestIntentional5xxMessagesPreserved(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	handler := authenticatedRoutes(t, NewServer(db, nil, nil, nil))

	// 1. /api/events with nil eventBus -> 503 "事件总线不可用"
	resp := doRequest(t, handler, http.MethodGet, "/api/events", "", nil)
	if resp.Code != http.StatusServiceUnavailable || !strings.Contains(resp.Body.String(), "事件总线不可用") {
		t.Fatalf("events nil bus: status=%d body=%s", resp.Code, resp.Body.String())
	}

	// 2. /api/assets/{assetID} when target fails to open -> 502 "asset unavailable"
	deadS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadS3.Close()
	doc := config.DefaultStorage(t.TempDir())
	doc.Enabled = true
	doc.Targets = append(doc.Targets, config.StorageTarget{
		ID: "s3", Name: "Dead S3", Type: "s3", Endpoint: deadS3.URL, Region: "us-east-1",
		Bucket: "bkt", AccessKey: "ak", SecretKey: "sk",
	})
	doc, err := db.SaveStorageDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("x"))
	asset, err := db.CreateAsset(ctx, storage.Asset{
		ID: "unavailable-asset", Kind: config.AssetImage, PolicyVersion: doc.Version,
		TargetID: doc.Targets[1].ID, TargetVersion: doc.Targets[1].Version,
		Key: "k.jpg", Size: 1, ContentType: "image/jpeg", ContentHash: hex.EncodeToString(hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp = doRequest(t, handler, http.MethodGet, "/api/assets/"+asset.ID, "", nil)
	if resp.Code != http.StatusBadGateway || !strings.Contains(resp.Body.String(), "asset unavailable") {
		t.Fatalf("asset unavailable: status=%d body=%s", resp.Code, resp.Body.String())
	}

	// 3. *posterStatusError with 502 preserves message through writeError
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadGateway, &posterStatusError{status: http.StatusBadGateway, message: "视频预览图回源暂时失败"})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "视频预览图回源暂时失败") {
		t.Fatalf("posterStatusError: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStreamingPathPanicAfterHeaderAbortsWithoutJSONCorruption(t *testing.T) {
	t.Run("after_header_repanics_ErrAbortHandler", func(t *testing.T) {
		handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(": connected\n\n"))
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Fatalf("ResponseController.Flush failed: %v", err)
			}
			panic("stream crashed")
		}))

		rec := httptest.NewRecorder()
		var recovered any
		func() {
			defer func() {
				recovered = recover()
			}()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
		}()

		if recovered != http.ErrAbortHandler {
			t.Fatalf("panic recovered = %v, want http.ErrAbortHandler", recovered)
		}
		if strings.Contains(rec.Body.String(), "handler panic") || strings.Contains(rec.Body.String(), "{") {
			t.Fatalf("stream body corrupted with JSON error: %q", rec.Body.String())
		}
	})

	t.Run("before_header_writes_clean_500_JSON", func(t *testing.T) {
		handler := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("before header")
		}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/library/file?path=x.jpg", nil))
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal error") {
			t.Fatalf("before-header panic: status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestBootstrapWorkbenchAlwaysIncludesEmptySchedules(t *testing.T) {
	db := openConfigStore(t)
	s := NewServer(db, nil, nil, nil)
	token := testSession(t, s)

	for _, tc := range []struct {
		path          string
		wantSchedules bool
	}{
		{"/", true},
		{"/overview", true},
		{"/settings", false},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		out := string(s.InjectIndexHTML(req, []byte("<html><head></head></html>")))
		hasSchedules := strings.Contains(out, `"schedules":[]`)
		if hasSchedules != tc.wantSchedules {
			t.Fatalf("path %s: hasSchedules=%v, want %v; html=%s", tc.path, hasSchedules, tc.wantSchedules, out)
		}
	}
}

func TestDecodeJSONOversizedTrailingBytesReturns413(t *testing.T) {
	db := openConfigStore(t)
	handler := authenticatedRoutes(t, NewServer(db, nil, nil, nil))
	oversized := "{}" + strings.Repeat(" ", 2<<20)
	resp := doRequest(t, handler, http.MethodPut, "/api/config", oversized, map[string]string{"Content-Type": "application/json"})
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized trailing body status = %d, want 413: %s", resp.Code, resp.Body.String())
	}
}

func TestListTweetsValidationAndBatchAssetEnrichment(t *testing.T) {
	store, handler, root := newLibraryTestServer(t)
	ctx := context.Background()

	// Validation: invalid jobId and from > to return 400
	for _, query := range []string{
		"jobId=abc",
		"jobId=0",
		"jobId=-3",
		"from=2026-11-12&to=2026-11-10",
	} {
		resp := doRequest(t, handler, http.MethodGet, "/api/archive/tweets?"+query, "", nil)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("query %q status = %d, want 400: %s", query, resp.Code, resp.Body.String())
		}
	}

	// Batch enrichment across multiple tweets
	doc := config.DefaultStorage(t.TempDir())
	doc.Enabled = true
	doc, err := store.SaveStorageDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "owner", ArchiveMode: storage.ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	for i, tid := range []string{"t1", "t2"} {
		mediaURL := fmt.Sprintf("https://pbs.twimg.com/%s.jpg", tid)
		tweet := parser.TweetData{
			ID:        tid,
			Text:      "tweet " + tid,
			Author:    parser.Author{ID: "1", ScreenName: "owner"},
			CreatedAt: time.Date(2026, 11, 10+i, 0, 0, 0, 0, time.UTC),
			Media:     []parser.Media{{ID: "m" + tid, Type: parser.MediaPhoto, URL: mediaURL}},
		}
		if err := store.ArchiveTweet(ctx, job.ID, tweet, "downloaded"); err != nil {
			t.Fatal(err)
		}
		rec, err := store.CreateDownload(ctx, storage.DownloadRecord{
			JobID:    job.ID,
			TweetID:  tid,
			MediaURL: mediaURL,
			FilePath: filepath.Join(root, tid+".jpg"),
			Bytes:    5,
		})
		if err != nil {
			t.Fatal(err)
		}
		asset, err := store.CreateAsset(ctx, storage.Asset{
			ID: "asset-" + tid, Kind: config.AssetImage, PolicyVersion: doc.Version,
			TargetID: doc.Targets[0].ID, TargetVersion: doc.Targets[0].Version,
			Key: tid + ".jpg", Size: 5, ContentType: "image/jpeg", ContentHash: "h" + tid,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.LinkDownloadAsset(ctx, rec.ID, config.AssetImage, asset.ID); err != nil {
			t.Fatal(err)
		}
	}

	resp := doRequest(t, handler, http.MethodGet, "/api/archive/tweets", "", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("list tweets status = %d: %s", resp.Code, resp.Body.String())
	}
	var page storage.TweetPage
	if err := json.Unmarshal(resp.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	for _, item := range page.Items {
		if len(item.Downloads) != 1 || item.Downloads[0].AssetURLs[config.AssetImage] == "" {
			t.Fatalf("tweet %s missing enriched AssetURLs: %+v", item.ID, item.Downloads)
		}
	}
}

func TestRetryTweetMediaReturns500OnDBError(t *testing.T) {
	db := openConfigStore(t)
	s := NewServer(db, nil, nil, nil)
	handler := authenticatedRoutes(t, s)

	// Missing tweet -> 404
	resp := doRequest(t, handler, http.MethodPost, "/api/archive/tweets/999/retry-media", "", nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("missing tweet status = %d, want 404", resp.Code)
	}

	// Non-ErrNoRows DB failure -> 500 (not masked as 404)
	r := chi.NewRouter()
	r.Post("/api/archive/tweets/{tweetID}/retry-media", s.retryTweetMedia)
	_ = db.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/archive/tweets/999/retry-media", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closed DB retryTweetMedia status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
}

func TestEventsConnectionHeaderOnlyOnHTTP1(t *testing.T) {
	db := openConfigStore(t)
	bus := jobs.NewEventBus()
	s := NewServer(db, nil, nil, bus)

	for _, protoMajor := range []int{1, 2} {
		t.Run(fmt.Sprintf("HTTP/%d", protoMajor), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
			req.ProtoMajor = protoMajor
			req = req.WithContext(context.WithValue(req.Context(), authContextKey{}, authenticatedSession{
				account: &storage.Account{Username: "owner"},
				revoked: make(chan struct{}),
			}))
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.events(rec, req)
			}()
			// Wait briefly for headers + initial comment, then cancel request.
			time.Sleep(20 * time.Millisecond)
			cancel()
			<-done
			conn := rec.Header().Get("Connection")
			if protoMajor == 1 && conn != "keep-alive" {
				t.Fatalf("HTTP/1 Connection = %q, want keep-alive", conn)
			}
			if protoMajor == 2 && conn != "" {
				t.Fatalf("HTTP/2 Connection = %q, want empty", conn)
			}
		})
	}
}

func TestLoginVsChangePasswordRaceAndXForwardedProto(t *testing.T) {
	t.Run("X-Forwarded-Proto_leftmost_token", func(t *testing.T) {
		for _, tc := range []struct {
			header string
			secure bool
		}{
			{"https, http", true},
			{" https , http ", true},
			{"http, https", false},
			{"", false},
		} {
			req := httptest.NewRequest(http.MethodGet, "http://backend.internal", nil)
			if tc.header != "" {
				req.Header.Set("X-Forwarded-Proto", tc.header)
			}
			rec := httptest.NewRecorder()
			setSessionCookie(rec, req, "tok")
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Secure != tc.secure {
				t.Fatalf("header=%q: secure=%v, want %v", tc.header, cookies[0].Secure, tc.secure)
			}
		}
	})

	t.Run("concurrent_login_and_changePassword_leaves_no_orphan_session", func(t *testing.T) {
		db := openConfigStore(t)
		s := NewServer(db, nil, nil, nil)
		handler := s.Routes()

		setup := authRequest(handler, http.MethodPost, "/api/auth/setup", `{"username":"owner","password":"old-password"}`, "")
		if setup.Code != http.StatusCreated {
			t.Fatalf("setup: %d %s", setup.Code, setup.Body.String())
		}
		adminToken := responseToken(t, setup)

		var wg sync.WaitGroup
		tokens := make(chan string, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodPost, "http://downloads.example/api/auth/login", strings.NewReader(`{"username":"owner","password":"old-password"}`))
				req.Header.Set("Content-Type", "application/json")
				req.RemoteAddr = fmt.Sprintf("198.51.100.%d:1234", idx+10)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code == http.StatusOK {
					var res struct{ Token string }
					if json.Unmarshal(rec.Body.Bytes(), &res) == nil && res.Token != "" {
						tokens <- res.Token
					}
				}
			}(i)
		}

		// Concurrently rotate password.
		changed := authRequest(handler, http.MethodPut, "/api/auth/password", `{"currentPassword":"old-password","newPassword":"new-password"}`, adminToken)
		if changed.Code != http.StatusOK {
			t.Fatalf("changePassword: %d %s", changed.Code, changed.Body.String())
		}
		wg.Wait()
		close(tokens)

		// No session minted with the old password may remain valid after changePassword succeeded.
		for tok := range tokens {
			if w := authRequest(handler, http.MethodGet, "/api/jobs", "", tok); w.Code != http.StatusUnauthorized {
				t.Fatalf("old-password login token survived concurrent changePassword: status=%d", w.Code)
			}
		}
	})

	t.Run("respondLogin_500_does_not_clear_rate_limit", func(t *testing.T) {
		db := openConfigStore(t)
		s := NewServer(db, nil, nil, nil)
		handler := s.Routes()
		setup := authRequest(handler, http.MethodPost, "/api/auth/setup", `{"username":"owner","password":"initial-password"}`, "")
		if setup.Code != http.StatusCreated {
			t.Fatalf("setup: %d", setup.Code)
		}
		account, err := db.GetAccount(context.Background())
		if err != nil || account == nil {
			t.Fatal(err)
		}
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil).WithContext(cancelledCtx)
		req.RemoteAddr = "203.0.113.55:4444"
		if !s.checkLoginRate(httptest.NewRecorder(), req) {
			t.Fatal("first rate check failed")
		}
		rec := httptest.NewRecorder()
		ok := s.respondLogin(rec, req, *account, http.StatusOK)
		if ok || rec.Code != http.StatusInternalServerError {
			t.Fatalf("respondLogin ok=%v code=%d, want false/500", ok, rec.Code)
		}
		s.loginMu.Lock()
		attempts := len(s.loginAttempts["203.0.113.55"])
		s.loginMu.Unlock()
		if attempts != 1 {
			t.Fatalf("loginAttempts = %d, want 1 (500 must not clear rate limit)", attempts)
		}
	})
}

func TestXAccountsInvalidateSourceOnCancelAfterPartialMutation(t *testing.T) {
	t.Run("importXAccounts_invalidates_on_cancel", func(t *testing.T) {
		db := openConfigStore(t)
		manager := jobs.NewManager(db, nil, nil)
		var factoryCalls atomic.Int32
		manager.SetXSourceFactory(func(context.Context, config.AppConfig) (xapi.Source, error) {
			factoryCalls.Add(1)
			return nil, errors.New("stub")
		})
		_, _ = manager.XSource(context.Background(), config.AppConfig{})
		if factoryCalls.Load() != 1 {
			t.Fatalf("initial factoryCalls = %d, want 1", factoryCalls.Load())
		}

		s := NewServer(db, nil, manager, nil)
		var lines []string
		for i := 0; i < 60; i++ {
			lines = append(lines, fmt.Sprintf("auth_token=imp-auth-%04d-token; ct0=imp-ct0-%04d-token", i, i))
		}
		body, _ := json.Marshal(map[string]string{"text": strings.Join(lines, "\n")})
		reqCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, "/api/x-accounts/import", bytes.NewReader(body)).WithContext(reqCtx)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			defer close(done)
			s.importXAccounts(rec, req)
		}()

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			accounts, err := db.ListXAccounts(context.Background())
			if err == nil && len(accounts) > 0 {
				cancel()
				break
			}
			time.Sleep(time.Millisecond)
		}
		<-done

		_, _ = manager.XSource(context.Background(), config.AppConfig{})
		if factoryCalls.Load() != 2 {
			t.Fatalf("factoryCalls after cancelled importXAccounts = %d, want 2 (cache invalidated)", factoryCalls.Load())
		}
	})

	t.Run("checkAllXAccounts_invalidates_on_cancel_after_write", func(t *testing.T) {
		db := openConfigStore(t)
		ctx := context.Background()
		a1, err := db.CreateXAccount(ctx, storage.XAccount{AuthToken: "t1-auth-token-1234", CSRFToken: "t1-ct0-1234"})
		if err != nil {
			t.Fatal(err)
		}
		a2, err := db.CreateXAccount(ctx, storage.XAccount{AuthToken: "t2-auth-token-5678", CSRFToken: "t2-ct0-5678"})
		if err != nil {
			t.Fatal(err)
		}
		_ = a1
		_ = a2

		manager := jobs.NewManager(db, nil, nil)
		var factoryCalls atomic.Int32
		manager.SetXSourceFactory(func(context.Context, config.AppConfig) (xapi.Source, error) {
			factoryCalls.Add(1)
			return nil, errors.New("stub")
		})
		_, _ = manager.XSource(ctx, config.AppConfig{})
		if factoryCalls.Load() != 1 {
			t.Fatalf("initial factoryCalls = %d, want 1", factoryCalls.Load())
		}

		s := NewServer(db, nil, manager, nil)
		reqCtx, cancel := context.WithCancel(context.Background())
		var probed atomic.Int32
		s.SetHomeProber(func(_ context.Context, creds xapi.AccountCredentials) xapi.HomeProbeResult {
			n := probed.Add(1)
			if n == 2 {
				// Let the 1st probe return & write ApplyXAccountCheck before cancelling.
				time.Sleep(30 * time.Millisecond)
				cancel()
			}
			return xapi.HomeProbeResult{ScreenName: "ok_" + creds.AuthToken[:2]}
		})

		req := httptest.NewRequest(http.MethodPost, "/api/x-accounts/check-all", nil).WithContext(reqCtx)
		rec := httptest.NewRecorder()
		s.checkAllXAccounts(rec, req)

		// Cache must have been invalidated by defer even though ctx was cancelled!
		_, _ = manager.XSource(context.Background(), config.AppConfig{})
		if factoryCalls.Load() != 2 {
			t.Fatalf("factoryCalls after cancelled checkAllXAccounts = %d, want 2 (cache invalidated)", factoryCalls.Load())
		}
	})
}

type unwindingXSource struct {
	xapi.Source
	started    chan struct{}
	once       sync.Once
	unwound    atomic.Bool
	unwindWait time.Duration
}

func (s *unwindingXSource) GetUserByInput(ctx context.Context, input string) (xapi.User, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	time.Sleep(s.unwindWait)
	s.unwound.Store(true)
	return xapi.User{}, ctx.Err()
}

func TestDeleteJobViaManagerWaitsForUnwindingWorker(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	cfg := config.Default()
	cfg.DownloadDir = t.TempDir()
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	src := &unwindingXSource{
		started:    make(chan struct{}),
		unwindWait: 80 * time.Millisecond,
	}
	manager := jobs.NewManager(db, nil, nil)
	manager.SetXSourceFactory(func(context.Context, config.AppConfig) (xapi.Source, error) {
		return src, nil
	})
	manager.Start(ctx)
	t.Cleanup(func() { manager.Stop() })

	handler := authenticatedRoutes(t, NewServer(db, nil, manager, nil))

	job, err := db.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	manager.Notify()

	select {
	case <-src.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}

	// Active job must return 409 Conflict.
	resp := doRequest(t, handler, http.MethodDelete, "/api/jobs/"+strconv.FormatInt(job.ID, 10), "", nil)
	if resp.Code != http.StatusConflict {
		t.Fatalf("delete active job status = %d, want 409: %s", resp.Code, resp.Body.String())
	}

	// Cancel marks the DB row terminal while the worker is still unwinding for 80ms.
	cancelResp := doRequest(t, handler, http.MethodPost, "/api/jobs/"+strconv.FormatInt(job.ID, 10)+"/cancel", "", nil)
	if cancelResp.Code != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", cancelResp.Code, cancelResp.Body.String())
	}

	// DeleteJob via manager must wait for activeDone[id] before deleting.
	delResp := doRequest(t, handler, http.MethodDelete, "/api/jobs/"+strconv.FormatInt(job.ID, 10), "", nil)
	if delResp.Code != http.StatusOK {
		t.Fatalf("delete canceled unwinding job status = %d, want 200: %s", delResp.Code, delResp.Body.String())
	}
	if !src.unwound.Load() {
		t.Fatal("deleteJob returned before worker goroutine finished unwinding")
	}

	// Deleting missing job via manager must still map sql.ErrNoRows to 404.
	missingResp := doRequest(t, handler, http.MethodDelete, "/api/jobs/999999", "", nil)
	if missingResp.Code != http.StatusNotFound {
		t.Fatalf("delete missing job status = %d, want 404: %s", missingResp.Code, missingResp.Body.String())
	}
}
