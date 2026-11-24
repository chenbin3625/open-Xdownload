package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestV15HTTPAPIEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads")
	if err := os.MkdirAll(downloadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: downloadDir}); err != nil {
		t.Fatal(err)
	}
	bus := jobs.NewEventBus()
	mgr := jobs.NewManager(store, parser.NewService(), bus)
	srv := NewServer(store, parser.NewService(), mgr, bus)
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	// Setup local account & login
	reqSetup, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/setup", strings.NewReader(`{"username":"admin","password":"password123"}`))
	reqSetup.Header.Set("Content-Type", "application/json")
	reqSetup.Header.Set("X-XDownload-Request", "1")
	respSetup, err := http.DefaultClient.Do(reqSetup)
	if err != nil || respSetup.StatusCode != http.StatusCreated {
		t.Fatalf("setup failed: status=%v err=%v", respSetup, err)
	}
	cookies := respSetup.Cookies()
	respSetup.Body.Close()

	doReq := func(method, path, body, bearer string) *http.Response {
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		} else {
			for _, c := range cookies {
				req.AddCookie(c)
			}
			req.Header.Set("X-XDownload-Request", "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// 1. Create Personal API Token and authenticate with Bearer
	resTok := doReq(http.MethodPost, "/api/tokens", `{"name":"Userscript"}`, "")
	if resTok.StatusCode != http.StatusCreated {
		t.Fatalf("create token status = %d", resTok.StatusCode)
	}
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	_ = json.NewDecoder(resTok.Body).Decode(&tokResp)
	resTok.Body.Close()
	if !strings.HasPrefix(tokResp.Plaintext, storage.APITokenPrefix) {
		t.Fatalf("unexpected token: %q", tokResp.Plaintext)
	}

	// 2. Userscript endpoint works with API Token
	resUS := doReq(http.MethodGet, "/api/extensions/userscript.user.js", "", tokResp.Plaintext)
	if resUS.StatusCode != http.StatusOK {
		t.Fatalf("userscript status = %d", resUS.StatusCode)
	}
	resUS.Body.Close()

	// 3. Create new job kinds (bookmarks, likes, thread, search) + batch tweet_link
	resBatch := doReq(http.MethodPost, "/api/jobs/batch", `{"items":[
		{"kind":"bookmarks","input":"all","archiveMode":"both"},
		{"kind":"likes","input":"self","archiveMode":"both"},
		{"kind":"thread","input":"https://x.com/alice/status/1001","archiveMode":"both"},
		{"kind":"search","input":"#golang filter:media","archiveMode":"both"},
		{"kind":"tweet_link","input":"https://x.com/alice/status/1002","archiveMode":"both"},
		{"kind":"tweet_link","input":"https://x.com/alice/status/1003","archiveMode":"both"}
	]}`, tokResp.Plaintext)
	if resBatch.StatusCode != http.StatusCreated {
		t.Fatalf("batch create status = %d", resBatch.StatusCode)
	}
	resBatch.Body.Close()

	// 4. Create Cron Archive Schedule
	resSched := doReq(http.MethodPost, "/api/archive-schedules", `{"name":"Daily Bookmarks","enabled":true,"cronExpr":"0 3 * * *","timezone":"Asia/Shanghai","items":[{"kind":"bookmarks","input":"all","archiveMode":"both"}]}`, tokResp.Plaintext)
	if resSched.StatusCode != http.StatusCreated {
		t.Fatalf("create cron schedule status = %d", resSched.StatusCode)
	}
	resSched.Body.Close()

	// 5. Seed tweet & local JPEG download for star, tag, collection, thread, export ZIP/Markdown/HTML, RSS, thumbnail, backup
	imgPath := filepath.Join(downloadDir, "photo.jpg")
	f, _ := os.Create(imgPath)
	_ = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 800, 600)), nil)
	_ = f.Close()
	job, _ := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	_ = store.ArchiveTweet(ctx, job.ID, parser.TweetData{
		ID: "9001", URL: "https://x.com/alice/status/9001", Text: "Hello archive export",
		CreatedAt: time.Now().UTC(), ConversationID: "9001",
		Author:  parser.Author{ID: "u1", Name: "Alice", ScreenName: "alice"},
		Metrics: &parser.TweetMetrics{Likes: 42, Views: 999},
	}, "completed")
	dl, _ := store.CreateDownload(ctx, storage.DownloadRecord{
		JobID: job.ID, TweetID: "9001", MediaURL: "https://pbs.twimg.com/media/p.jpg",
		FilePath: imgPath, Bytes: 1024, ContentHash: "h1",
	})

	// Star & Tag & Collection
	resStar := doReq(http.MethodPost, "/api/archive/tweets/9001/star", `{"starred":true}`, tokResp.Plaintext)
	if resStar.StatusCode != http.StatusOK {
		t.Fatalf("star tweet status = %d", resStar.StatusCode)
	}
	resStar.Body.Close()

	resTag := doReq(http.MethodPost, "/api/tags", `{"name":"Inspo","color":"purple"}`, tokResp.Plaintext)
	if resTag.StatusCode != http.StatusCreated {
		t.Fatalf("create tag status = %d", resTag.StatusCode)
	}
	resTag.Body.Close()

	// Thread endpoint
	resThread := doReq(http.MethodGet, "/api/archive/tweets/9001/thread", "", tokResp.Plaintext)
	if resThread.StatusCode != http.StatusOK {
		t.Fatalf("thread status = %d", resThread.StatusCode)
	}
	resThread.Body.Close()

	// Export Markdown, HTML, JSON, ZIP
	for _, format := range []string{"markdown", "html", "json"} {
		resExp := doReq(http.MethodGet, "/api/export/tweets?format="+format, "", tokResp.Plaintext)
		if resExp.StatusCode != http.StatusOK {
			t.Fatalf("export %s status = %d", format, resExp.StatusCode)
		}
		resExp.Body.Close()
	}
	zipBody, _ := json.Marshal(map[string]any{"ids": []int64{dl.ID}})
	resZIP := doReq(http.MethodPost, "/api/export/media-zip", string(zipBody), tokResp.Plaintext)
	if resZIP.StatusCode != http.StatusOK {
		t.Fatalf("export zip status = %d", resZIP.StatusCode)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resZIP.Body)
	resZIP.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || len(zr.File) != 1 {
		t.Fatalf("invalid exported zip: err=%v files=%d", err, len(zr.File))
	}

	// RSS with query token
	resRSS, err := http.Get(ts.URL + "/api/rss/tweets?token=" + tokResp.Plaintext)
	if err != nil || resRSS.StatusCode != http.StatusOK {
		t.Fatalf("rss status = %v err=%v", resRSS, err)
	}
	resRSS.Body.Close()

	// Local thumbnail generation
	resThumb := doReq(http.MethodGet, "/api/library/downloads/1/thumbnail", "", tokResp.Plaintext)
	if resThumb.StatusCode != http.StatusOK || resThumb.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail status = %d ct=%s", resThumb.StatusCode, resThumb.Header.Get("Content-Type"))
	}
	resThumb.Body.Close()

	// Maintenance stats, scan, backup, config export
	for _, ep := range []string{"/api/maintenance/stats", "/api/maintenance/backup", "/api/maintenance/export-config"} {
		res := doReq(http.MethodGet, ep, "", tokResp.Plaintext)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", ep, res.StatusCode)
		}
		res.Body.Close()
	}

	// Webhook CRUD
	resWH := doReq(http.MethodPost, "/api/webhooks", `{"name":"Hook","kind":"generic","url":"https://example.com/hook","events":["job.failed"],"enabled":true}`, tokResp.Plaintext)
	if resWH.StatusCode != http.StatusCreated {
		t.Fatalf("create webhook status = %d", resWH.StatusCode)
	}
	resWH.Body.Close()
}
