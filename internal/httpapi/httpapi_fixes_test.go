package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func openConfigStore(t *testing.T) *storage.Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func doRequest(t *testing.T, handler http.Handler, method, target string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := newLoopbackRequest(method, target, reader)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// PUT /api/config 的 body 是 JSON `null` 时，旧实现会解出 nil map、被 storage 当成
// "全量替换为零值配置"，一次请求就把 downloadDir / 备用 Cookie / 代理重置成默认值。
// 现在必须 400 且完全不改库。
func TestUpdateConfigNullBodyRejectedWithoutChanges(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	downloadDir := t.TempDir()
	cfg := config.Default()
	cfg.DownloadDir = downloadDir
	cfg.AdditionalCookies = `[{"id":"cookie-0","authToken":"E1","csrfToken":"E2"}]`
	cfg.ProxyURL = "http://user:pass@127.0.0.1:7890"
	cfg.IncrementalArchive = true
	cfg.MaxFilenameLength = 200
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	before, err := db.GetStoredConfig(ctx)
	if err != nil {
		t.Fatalf("stored config: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	response := doRequest(t, handler, http.MethodPut, "/api/config", "null", map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400", response.Code, response.Body.String())
	}
	after, err := db.GetStoredConfig(ctx)
	if err != nil {
		t.Fatalf("stored config after: %v", err)
	}
	if after.DownloadDir != before.DownloadDir ||
		after.AdditionalCookies != before.AdditionalCookies ||
		after.ProxyURL != before.ProxyURL ||
		after.MaxFilenameLength != before.MaxFilenameLength ||
		after.IncrementalArchive != before.IncrementalArchive {
		t.Fatalf("config mutated by null body:\nbefore=%+v\nafter =%+v", before, after)
	}
}

// 空对象 {} 是合法的"无改动 patch"，必须仍然是 200 且不改库。
func TestUpdateConfigEmptyObjectIsNoOp(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	cfg := config.Default()
	cfg.DownloadDir = t.TempDir()
	cfg.IncrementalArchive = true
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	before, _ := db.GetStoredConfig(ctx)

	handler := NewServer(db, nil, nil, nil).Routes()
	response := doRequest(t, handler, http.MethodPut, "/api/config", "{}", map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", response.Code, response.Body.String())
	}
	after, _ := db.GetStoredConfig(ctx)
	if after.DownloadDir != before.DownloadDir || after.IncrementalArchive != before.IncrementalArchive {
		t.Fatalf("empty patch mutated config: before=%+v after=%+v", before, after)
	}
}

// 解码错误不能把 Go 类型/结构体字段名回传客户端（旧行为：
// `json: cannot unmarshal string into Go struct field archiveScheduleRequest...`）。
func TestJSONDecodeErrorsAreSanitized(t *testing.T) {
	db := openConfigStore(t)
	handler := NewServer(db, nil, nil, nil).Routes()

	cases := []struct {
		name     string
		method   string
		target   string
		body     string
		wantCode int
	}{
		{"wrong field type", http.MethodPost, "/api/archive-schedules", `{"name":"x","intervalMinutes":"abc","items":[]}`, http.StatusBadRequest},
		{"invalid id", http.MethodGet, "/api/jobs/abc/files", "", http.StatusBadRequest},
		{"empty body", http.MethodPost, "/api/jobs", "", http.StatusBadRequest},
		{"malformed json", http.MethodPost, "/api/jobs", `{"kind":`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.body != "" || tc.method == http.MethodPost {
				headers["Content-Type"] = "application/json"
			}
			response := doRequest(t, handler, tc.method, tc.target, tc.body, headers)
			if response.Code != tc.wantCode {
				t.Fatalf("status = %d, body = %s, want %d", response.Code, response.Body.String(), tc.wantCode)
			}
			payload := response.Body.String()
			for _, leaked := range []string{"json:", "Go struct field", "storage.", "strconv.", "EOF"} {
				if strings.Contains(payload, leaked) {
					t.Fatalf("payload leaks internal detail %q: %s", leaked, payload)
				}
			}
		})
	}
}

// HEAD 必须与 GET 得到同一套响应头（此前 chi 只注册 GET，所有 HEAD 都是 405，
// 压缩中间件里的 finishHead 分支因此不可达、也从未生效）。
// 必须用真实 server：httptest.ResponseRecorder 不会像 net/http 那样丢弃 HEAD 的 body。
func TestHEADMatchesGETHeaders(t *testing.T) {
	db := openConfigStore(t)
	server := httptest.NewServer(NewServer(db, nil, nil, nil).Routes())
	defer server.Close()

	do := func(method string) *http.Response {
		request, err := http.NewRequest(method, server.URL+"/api/jobs?limit=1", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		request.Header.Set("Accept-Encoding", "gzip")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	get := do(http.MethodGet)
	getBody, err := io.ReadAll(get.Body)
	if err != nil {
		t.Fatalf("read GET body: %v", err)
	}
	head := do(http.MethodHead)
	headBody, err := io.ReadAll(head.Body)
	if err != nil {
		t.Fatalf("read HEAD body: %v", err)
	}
	if get.StatusCode != http.StatusOK || head.StatusCode != http.StatusOK {
		t.Fatalf("status GET=%d HEAD=%d", get.StatusCode, head.StatusCode)
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD body = %q, want empty", string(headBody))
	}
	if len(getBody) == 0 {
		t.Fatal("GET body is empty; test cannot compare headers meaningfully")
	}
	for _, key := range []string{"Content-Type", "Content-Encoding", "Content-Length"} {
		if get.Header.Get(key) != head.Header.Get(key) {
			t.Fatalf("%s mismatch: GET=%q HEAD=%q", key, get.Header.Get(key), head.Header.Get(key))
		}
	}
	if values := get.Header.Values("Vary"); len(values) != 1 {
		t.Fatalf("GET Vary = %v, want exactly one value", values)
	}
	if values := head.Header.Values("Vary"); len(values) != 1 {
		t.Fatalf("HEAD Vary = %v, want exactly one value", values)
	}
}

// 压缩响应只允许出现一个 Vary 头（此前中间件与 finish() 各 Add 一次）。
func TestCompressedResponseHasSingleVary(t *testing.T) {
	db := openConfigStore(t)
	handler := NewServer(db, nil, nil, nil).Routes()
	response := doRequest(t, handler, http.MethodGet, "/api/dashboard", "", map[string]string{"Accept-Encoding": "gzip"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if values := response.Header().Values("Vary"); len(values) != 1 {
		t.Fatalf("Vary = %v, want exactly one value", values)
	}
}

// 媒体读取端点必须直通：不缓冲、不压缩、不加 Vary（几百 MB 的视频不能先攒满缓冲区）。
func TestLibraryMediaEndpointStreamsThrough(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	downloadDir := t.TempDir()
	cfg := config.Default()
	cfg.DownloadDir = downloadDir
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	mediaPath := filepath.Join(downloadDir, "clip.jpg")
	payload := strings.Repeat("jpeg-bytes", 1024)
	if err := os.WriteFile(mediaPath, []byte(payload), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}

	handler := NewServer(db, nil, nil, nil).Routes()
	headers := map[string]string{"Accept-Encoding": "gzip"}
	get := doRequest(t, handler, http.MethodGet, "/api/library/file?path="+mediaPath, "", headers)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", get.Code, get.Body.String())
	}
	if get.Body.String() != payload {
		t.Fatalf("GET body was transformed/compressed: len=%d want=%d", get.Body.Len(), len(payload))
	}
	if get.Header().Get("Content-Encoding") != "" {
		t.Fatalf("media response compressed: Content-Encoding=%q", get.Header().Get("Content-Encoding"))
	}
	if values := get.Header().Values("Vary"); len(values) != 0 {
		t.Fatalf("media response should not vary on encoding: Vary=%v", values)
	}
	head := doRequest(t, handler, http.MethodHead, "/api/library/file?path="+mediaPath, "", headers)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body=%q", head.Code, head.Body.String())
	}
	if head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatalf("HEAD Content-Length=%q GET Content-Length=%q", head.Header().Get("Content-Length"), get.Header().Get("Content-Length"))
	}
}

// 上游 Content-Type 大小写不敏感（真实 CDN 会返回 "IMAGE/JPEG"）。
func TestFetchRemotePosterAcceptsMixedCaseContentType(t *testing.T) {
	body := []byte("poster-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "IMAGE/JPEG")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "x.preview.jpg")
	if err := fetchRemotePoster(context.Background(), server.Client(), make(countingSemaphore, 1), server.URL, target, maxPosterBytes); err != nil {
		t.Fatalf("fetchRemotePoster: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(body) {
		t.Fatalf("poster file = %q, err = %v", got, err)
	}
}

// 非图片 Content-Type 必须拒绝，且不落盘。
func TestFetchRemotePosterRejectsNonImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html></html>"))
	}))
	defer server.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "x.preview.jpg")
	err := fetchRemotePoster(context.Background(), server.Client(), make(countingSemaphore, 1), server.URL, target, maxPosterBytes)
	var statusErr *posterStatusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusUnsupportedMediaType {
		t.Fatalf("err = %v, want 415 posterStatusError", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("poster should not be published, stat err = %v", statErr)
	}
}

// single-flight：同一张海报的并发请求只回源一次。
func TestPosterFetchGroupDeduplicatesConcurrentFetches(t *testing.T) {
	group := &posterFetchSingleFlight{}
	var calls atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = group.Do("poster-key", func() error {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1 (single-flight)", got)
	}
}

// 负缓存：命中的失败在 TTL 内直接返回，过期后失效。
func TestPosterFailureCacheHonoursTTL(t *testing.T) {
	cache := &posterFailureCache{}
	key := "test-poster-failure-" + t.Name()
	cache.store(key, http.StatusNotFound, "视频预览图不可用")
	entry, ok := cache.lookup(key)
	if !ok || entry.status != http.StatusNotFound {
		t.Fatalf("lookup = %+v ok=%v, want cached 404", entry, ok)
	}
	cache.remove(key)
	if _, ok := cache.lookup(key); ok {
		t.Fatal("entry survived remove")
	}
	cache.entries.Store(key, posterFailureEntry{status: 502, message: "x", expiresAt: time.Now().Add(-time.Second)})
	if _, ok := cache.lookup(key); ok {
		t.Fatal("expired entry still reported as cached")
	}
}

// env 覆盖下载目录后：GET 返回 env 值，前端把该值原样 PUT 回来必须能保存成功
// （旧行为是 400 "下载目录不允许…请用 OPEN_XDOWNLOAD_DOWNLOAD_DIR"，而用户已经
// 在用该环境变量）。同时 env 值不得落库。
func TestUpdateConfigAcceptsEnvDownloadDirEcho(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	storedDir := filepath.Join(home, "stored-downloads")
	envDir := filepath.Join(home, "env-downloads")
	for _, dir := range []string{storedDir, envDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	db := openConfigStore(t)
	ctx := context.Background()
	cfg := config.Default()
	cfg.DownloadDir = storedDir
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv(config.EnvDownloadDir, envDir)

	handler := NewServer(db, nil, nil, nil).Routes()
	body := `{"downloadDir":"` + envDir + `","maxFilenameLength":150}`
	response := doRequest(t, handler, http.MethodPut, "/api/config", body, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", response.Code, response.Body.String())
	}
	after, err := db.GetStoredConfig(ctx)
	if err != nil {
		t.Fatalf("stored config: %v", err)
	}
	if filepath.Clean(after.DownloadDir) != filepath.Clean(storedDir) {
		t.Fatalf("env-echoed downloadDir was persisted: %q, want %q", after.DownloadDir, storedDir)
	}
	if after.MaxFilenameLength != 150 {
		t.Fatalf("other patch field lost: maxFilenameLength = %d", after.MaxFilenameLength)
	}
}

// Range 与条件请求仍由 http.ServeFile 处理（媒体直通路径不能把 Range 吃掉）。
func TestLibraryMediaEndpointSupportsRange(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	downloadDir := t.TempDir()
	cfg := config.Default()
	cfg.DownloadDir = downloadDir
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	mediaPath := filepath.Join(downloadDir, "clip.jpg")
	payload := "0123456789"
	if err := os.WriteFile(mediaPath, []byte(payload), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	handler := NewServer(db, nil, nil, nil).Routes()
	response := doRequest(t, handler, http.MethodGet, "/api/library/file?path="+mediaPath, "", map[string]string{"Range": "bytes=2-5"})
	if response.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", response.Code)
	}
	if response.Body.String() != payload[2:6] {
		t.Fatalf("range body = %q, want %q", response.Body.String(), payload[2:6])
	}
}

// 旧数组接口保留兼容，但默认/上限都是 100，超限用响应头告知。
func TestLegacyLibraryArrayMarksTruncated(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	downloadDir := t.TempDir()
	cfg := config.Default()
	cfg.DownloadDir = downloadDir
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	for _, name := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		path := filepath.Join(downloadDir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write media: %v", err)
		}
	}
	handler := NewServer(db, nil, nil, nil).Routes()
	response := doRequest(t, handler, http.MethodGet, "/api/library/downloads?limit=2", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("X-Result-Truncated") != "true" {
		t.Fatalf("X-Result-Truncated = %q, want true (header=%v)", response.Header().Get("X-Result-Truncated"), response.Header())
	}
	var items []map[string]any
	if err := jsonUnmarshal(response.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (limit is honoured)", len(items))
	}
}

func jsonUnmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

// 媒体库 paged 端点的契约：页大小、truncated 字段、counts/users 存在，
// 且过滤参数原样交给 storage（这里验证 HTTP 层行为，SQL 过滤由 storage 测试覆盖）。
func TestLibraryDownloadsPagedContract(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	downloadDir := t.TempDir()
	cfg := config.Default()
	cfg.DownloadDir = downloadDir
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	for _, name := range []string{"a.jpg", "b.mp4", "c.gif"} {
		path := filepath.Join(downloadDir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write media: %v", err)
		}
	}
	handler := NewServer(db, nil, nil, nil).Routes()
	response := doRequest(t, handler, http.MethodGet, "/api/library/downloads?page=1&pageSize=2", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Items      []storage.DownloadRecord `json:"items"`
		Pagination storage.Pagination       `json:"pagination"`
		Counts     map[string]int           `json:"counts"`
		Users      []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"users"`
		Truncated bool `json:"truncated"`
	}
	if err := jsonUnmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode paged body: %v", err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(payload.Items))
	}
	if payload.Pagination.PageSize != 2 || payload.Pagination.Total != 3 {
		t.Fatalf("pagination = %+v, want pageSize=2 total=3", payload.Pagination)
	}
	if payload.Counts["all"] != 3 {
		t.Fatalf("counts = %v, want all=3", payload.Counts)
	}

	filtered := doRequest(t, handler, http.MethodGet, "/api/library/downloads?page=1&pageSize=10&kind=images", "", nil)
	if filtered.Code != http.StatusOK {
		t.Fatalf("filtered status = %d", filtered.Code)
	}
	var filteredPayload struct {
		Items      []storage.DownloadRecord `json:"items"`
		Pagination storage.Pagination       `json:"pagination"`
	}
	if err := jsonUnmarshal(filtered.Body.Bytes(), &filteredPayload); err != nil {
		t.Fatalf("decode filtered body: %v", err)
	}
	if len(filteredPayload.Items) != 1 || filepath.Ext(filteredPayload.Items[0].FilePath) != ".jpg" {
		t.Fatalf("kind=images items = %+v, want only the .jpg", filteredPayload.Items)
	}
}

// /api/auth/check 必须用与 jobs 生产池隔离的临时池探测：CheckAll 会对 401/非 WAF 403
// 的客户端 disable（30m/5m），打在正在归档的生产池上会让任务半小时用不了主账号。
// 这里用"不可达代理"让探测快速失败（不依赖外网），再断言生产池诊断状态完全没变。
func TestCheckAuthProbesWithIsolatedPool(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	cfg := config.Default()
	cfg.DownloadDir = t.TempDir()
	cfg.AuthToken = "auth-token"
	cfg.CSRFToken = "csrf-token"
	cfg.ProxyURL = "http://127.0.0.1:1"
	if _, err := db.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	stored, err := db.GetStoredConfig(ctx)
	if err != nil {
		t.Fatalf("stored config: %v", err)
	}

	eventBus := jobs.NewEventBus()
	manager := jobs.NewManager(db, nil, eventBus)
	productionPool, err := manager.XClientPool(stored)
	if err != nil {
		t.Fatalf("production pool: %v", err)
	}
	before := productionPool.Diagnostics()

	handler := NewServer(db, nil, manager, eventBus).Routes()
	response := doRequest(t, handler, http.MethodPost, "/api/auth/check", "", map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Configured bool `json:"configured"`
		OK         bool `json:"ok"`
	}
	if err := jsonUnmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !payload.Configured {
		t.Fatalf("configured = false, body = %s", response.Body.String())
	}
	if payload.OK {
		t.Fatalf("probe against unreachable proxy reported ok, body = %s", response.Body.String())
	}

	afterPool, err := manager.XClientPool(stored)
	if err != nil {
		t.Fatalf("production pool after: %v", err)
	}
	if afterPool != productionPool {
		t.Fatal("auth check replaced the manager's cached pool")
	}
	after := productionPool.Diagnostics()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("production pool state changed by auth probe:\nbefore=%+v\nafter =%+v", before, after)
	}
}

// env 把下载目录设成 `/` 时，当前根不能让 record 端点对任意绝对路径放行。
func TestResolveDownloadPathSkipsUnsafeCurrentRoot(t *testing.T) {
	db := openConfigStore(t)
	ctx := context.Background()
	server := NewServer(db, nil, nil, nil)

	if path, _, err := server.resolveDownloadPath(ctx, "/", "/etc/hosts"); err == nil {
		t.Fatalf("unsafe current root accepted: path = %q", path)
	}

	// 合法当前根仍然照常工作。
	root := t.TempDir()
	target := filepath.Join(root, "clip.jpg")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	path, resolvedRoot, err := server.resolveDownloadPath(ctx, root, target)
	if err != nil {
		t.Fatalf("resolve within safe root: %v", err)
	}
	// macOS 上 /var 是指向 /private/var 的符号链接，解析结果是真实路径。
	expected, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if path != expected || resolvedRoot == "" {
		t.Fatalf("path = %q (want %q) resolvedRoot = %q", path, expected, resolvedRoot)
	}
}

// 下载目录是 `/` 时，目录选择器的允许根不能包含 `/`（否则可浏览/创建任意路径）。
func TestAllowedRootsSkipUnsafeDownloadDir(t *testing.T) {
	for _, unsafe := range []string{"/", strings.Repeat("/", 1)} {
		roots := localDirectoryAllowedRoots(config.AppConfig{DownloadDir: unsafe})
		for _, root := range roots {
			if filepath.Clean(root) == string(filepath.Separator) {
				t.Fatalf("allowed roots include filesystem root: %v", roots)
			}
		}
	}
}

// 家目录路径本身含符号链接时（macOS /tmp→/private/tmp 等），
// config.ValidateDownloadRoot 只解析候选路径、不解析 home，会漏判"家目录本身"。
// httpapi 的沙箱判断必须两侧都解析：这里用 symlink 形式的 HOME 锁死该边界。
func TestUnsafeDownloadRootDetectsSymlinkedHome(t *testing.T) {
	realHome := t.TempDir()
	linkParent := t.TempDir()
	linkHome := filepath.Join(linkParent, "home-link")
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	t.Setenv("HOME", linkHome)

	if !unsafeDownloadRoot(realHome) {
		t.Fatal("home directory (via symlinked HOME) must be rejected as download root")
	}
	safeDir := filepath.Join(realHome, "downloads")
	if err := os.MkdirAll(safeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if unsafeDownloadRoot(safeDir) {
		t.Fatal("subdirectory of home must be allowed as download root")
	}

	// record 端点也不能把"家目录"当当前根，否则任意已记录路径都会被放行。
	db := openConfigStore(t)
	server := NewServer(db, nil, nil, nil)
	target := filepath.Join(realHome, "clip.jpg")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if path, _, err := server.resolveDownloadPath(context.Background(), realHome, target); err == nil {
		t.Fatalf("symlinked home accepted as current download root: %q", path)
	}
}
