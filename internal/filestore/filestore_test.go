package filestore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
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

func TestWebDAVSaveMediaRetriesOn409MissingParent(t *testing.T) {
	var mu sync.Mutex
	var puts []string
	dirExists := false // 模拟服务端目录被删除后本地 webdavDirs 缓存仍认为存在

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("media"))
		case "MKCOL":
			mu.Lock()
			dirExists = true
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			mu.Lock()
			puts = append(puts, r.URL.Path)
			exists := dirExists
			mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusConflict) // 409: 父目录缺失
				return
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
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
	dir := store.Join(store.Root(), "downloads")
	// 预先污染 webdavDirs 缓存：MkdirAll 会因此跳过 MKCOL，首次 PUT 因目录缺失返回 409。
	webdavDirsMu.Lock()
	webdavDirs[store.webDAVDirKey(dir)] = struct{}{}
	webdavDirsMu.Unlock()
	defer func() {
		webdavDirsMu.Lock()
		delete(webdavDirs, store.webDAVDirKey(dir))
		webdavDirsMu.Unlock()
	}()

	result, err := store.SaveMedia(context.Background(), downloader.New(), server.URL+"/media", dir, "media", downloader.Options{})
	if err != nil {
		t.Fatalf("save media: %v", err)
	}
	if result.Skipped {
		t.Fatal("skipped = true, want false (409 must retry, not silently skip)")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(puts) != 2 {
		t.Fatalf("PUT attempts = %d, want 2 (initial 409 + retry)", len(puts))
	}
}

func TestWebDAVDirCacheKeySeparatesStores(t *testing.T) {
	// 两个不同 WebDAV 配置对同一目录字符串必须产生不同缓存键：若按目录字符串共享，
	// storeA 缓存命中后 storeB 会跳过 MKCOL，切换服务器后首次 PUT 必 409。
	var aMKCOL, bMKCOL int32
	newServer := func(counter *int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "MKCOL" {
				atomic.AddInt32(counter, 1)
			}
			w.WriteHeader(http.StatusCreated)
		}))
	}
	serverA := newServer(&aMKCOL)
	defer serverA.Close()
	serverB := newServer(&bMKCOL)
	defer serverB.Close()

	baseA, err := url.Parse(serverA.URL)
	if err != nil {
		t.Fatalf("parse server A URL: %v", err)
	}
	baseB, err := url.Parse(serverB.URL)
	if err != nil {
		t.Fatalf("parse server B URL: %v", err)
	}
	storeA := newWebDAVStore(config.AppConfig{WebDAVURL: serverA.URL, WebDAVPath: "remote"}.Normalized(), baseA)
	storeB := newWebDAVStore(config.AppConfig{WebDAVURL: serverB.URL, WebDAVPath: "remote"}.Normalized(), baseB)

	dir := "/downloads" // 两个存储传入相同的目录字符串
	if err := storeA.MkdirAll(context.Background(), dir); err != nil {
		t.Fatalf("mkdirall A: %v", err)
	}
	defer storeA.invalidateWebDAVDir(dir)
	if err := storeB.MkdirAll(context.Background(), dir); err != nil {
		t.Fatalf("mkdirall B: %v", err)
	}
	if atomic.LoadInt32(&aMKCOL) == 0 {
		t.Fatal("storeA sent no MKCOL")
	}
	if atomic.LoadInt32(&bMKCOL) == 0 {
		t.Fatal("storeB skipped MKCOL due to storeA's cached entry; cache key must include store identity")
	}
}

func TestSMBTempNameStaysUnderNameMax(t *testing.T) {
	// 最终文件名接近 NAME_MAX（240 字节）时，临时文件组件也必须远低于 255 字节。
	maxBase := strings.Repeat("a", 236) + ".mp4"
	tempRel := smbTempName(maxBase)
	base := path.Base(tempRel)
	if len(base) > 255 {
		t.Fatalf("temp file component length = %d, want <= 255: %q", len(base), base)
	}
	// 保留可识别的基名前缀（截断到 smbTempStemMax），方便排查残留临时文件。
	if !strings.HasPrefix(base, "a") {
		t.Fatalf("temp name = %q, want base-name prefix preserved", base)
	}
}

func TestWebDAVSaveMediaReplacesIncompleteExistingFile(t *testing.T) {
	var mu sync.Mutex
	deletes := []string{}
	puts := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write([]byte("0123456789"))
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case http.MethodHead:
			if strings.HasSuffix(r.URL.Path, "/media.mp4") {
				w.Header().Set("Content-Length", "4")
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			mu.Lock()
			puts = append(puts, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
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
	if result.Skipped {
		t.Fatal("skipped = true, want replacement download")
	}
	if result.Bytes != 10 {
		t.Fatalf("bytes = %d, want 10", result.Bytes)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deletes) != 1 || !strings.HasSuffix(deletes[0], "/media.mp4") {
		t.Fatalf("deletes = %#v, want one delete for stale media.mp4", deletes)
	}
	if len(puts) != 1 || !strings.HasSuffix(puts[0], "/media.mp4") {
		t.Fatalf("puts = %#v, want one replacement PUT for media.mp4", puts)
	}
}

// TestSMBReregisterLockedOwnership 验证 reregisterLocked 的返回语义（Bug #10 修复）：
// true 表示调用方持有的会话就是缓存中该键的持有者；false 表示同键已被另一条目占用，
// 调用方已成为孤儿，必须在操作结束后关闭自己重连出来的连接。纯内存测试，不涉及网络。
func TestSMBReregisterLockedOwnership(t *testing.T) {
	mkCfg := func(host string) smbStore {
		return smbStore{host: host, port: 445, share: "share", domain: "DOMAIN", username: "user", password: "pass"}
	}
	cfgA := mkCfg("host-a")
	cfgB := mkCfg("host-b")
	keyA := cfgA.signature()
	keyB := cfgB.signature()

	e1 := &smbSharedSession{cfg: cfgA}
	e2 := &smbSharedSession{cfg: cfgA} // 与 e1 同键
	e3 := &smbSharedSession{cfg: cfgB} // 异键

	smbSessionsMu.Lock()
	smbSessions[keyA] = e1
	smbSessionsMu.Unlock()
	defer func() {
		smbSessionsMu.Lock()
		delete(smbSessions, keyA)
		delete(smbSessions, keyB)
		smbSessionsMu.Unlock()
	}()

	// 同键已有条目且正是本会话：仍视为持有者。
	if !e1.reregisterLocked() {
		t.Fatal("e1 reregisterLocked = false, want true (existing entry is e1)")
	}
	// 同键已被另一条目占用：本会话是孤儿。
	if e2.reregisterLocked() {
		t.Fatal("e2 reregisterLocked = true, want false (key owned by e1)")
	}
	// 异键：插入新条目。
	if !e3.reregisterLocked() {
		t.Fatal("e3 reregisterLocked = false, want true (inserts new key)")
	}

	smbSessionsMu.Lock()
	if got := smbSessions[keyA]; got != e1 {
		smbSessionsMu.Unlock()
		t.Fatalf("map entry for keyA = %p, want e1 %p", got, e1)
	}
	if got := smbSessions[keyB]; got != e3 {
		smbSessionsMu.Unlock()
		t.Fatalf("map entry for keyB = %p, want e3 %p", got, e3)
	}
	smbSessionsMu.Unlock()

	// e1 被逐出缓存后，孤儿 e2 重试即可重新登记为持有者。
	smbSessionsMu.Lock()
	delete(smbSessions, keyA)
	smbSessionsMu.Unlock()
	if !e2.reregisterLocked() {
		t.Fatal("e2 reregisterLocked after eviction = false, want true (key now absent)")
	}
	smbSessionsMu.Lock()
	if got := smbSessions[keyA]; got != e2 {
		smbSessionsMu.Unlock()
		t.Fatalf("map entry for keyA after re-register = %p, want e2 %p", got, e2)
	}
	smbSessionsMu.Unlock()
}
