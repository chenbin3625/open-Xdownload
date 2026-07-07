package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/filestore"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
)

type Server struct {
	store    *storage.Store
	parser   *parser.Service
	manager  *jobs.Manager
	eventBus *jobs.EventBus
}

func NewServer(store *storage.Store, parserService *parser.Service, manager *jobs.Manager, eventBus *jobs.EventBus) *Server {
	return &Server{store: store, parser: parserService, manager: manager, eventBus: eventBus}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:*", "http://127.0.0.1:*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
	}))

	r.Get("/api/health", s.health)
	r.Get("/api/config", s.getConfig)
	r.Put("/api/config", s.updateConfig)
	r.Post("/api/storage/test", s.testStorage)
	r.Get("/api/local-directories", s.listLocalDirectories)
	r.Post("/api/auth/check", s.checkAuth)
	r.Post("/api/parse/tweet-link", s.parseTweetLink)
	r.Post("/api/jobs", s.createJob)
	r.Post("/api/jobs/batch", s.createJobsBatch)
	r.Get("/api/archive-schedules", s.listArchiveSchedules)
	r.Post("/api/archive-schedules", s.createArchiveSchedule)
	r.Put("/api/archive-schedules/{id}", s.updateArchiveSchedule)
	r.Delete("/api/archive-schedules/{id}", s.deleteArchiveSchedule)
	r.Post("/api/archive-schedules/{id}/run", s.runArchiveSchedule)
	r.Get("/api/jobs", s.listJobs)
	r.Get("/api/jobs/{id}", s.getJob)
	r.Post("/api/jobs/{id}/cancel", s.cancelJob)
	r.Post("/api/jobs/{id}/retry", s.retryJob)
	r.Get("/api/events", s.events)
	r.Get("/api/library/downloads", s.listDownloads)
	r.Get("/api/logs", s.listFailedMedia)
	r.Get("/api/failed-tweets", s.listFailedTweets)
	r.Post("/api/failed-tweets/retry", s.retryFailedTweets)
	r.Delete("/api/failed-tweets/{id}", s.deleteFailedTweet)
	r.Delete("/api/failed-tweets", s.clearFailedTweets)
	r.Get("/api/dashboard", s.dashboard)
	return r
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "open-Xdownload"})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg.Redacted())
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.AppConfig
	if err := decodeJSON(w, r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := s.store.UpdateConfig(r.Context(), cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Redacted())
}

func (s *Server) testStorage(w http.ResponseWriter, r *http.Request) {
	var cfg config.AppConfig
	if err := decodeJSON(w, r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	cfg = mergeSecretPlaceholders(cfg, current).Normalized()
	// 阻止对链路本地地址（如云元数据 169.254.169.254）的测试请求，缓解 SSRF。
	if isBlockedStorageTarget(cfg.SMBHost) || isBlockedStorageTarget(urlHostname(cfg.WebDAVURL)) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("不允许的存储目标地址"))
		return
	}
	target, err := filestore.New(cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	probePath, err := target.TestWritable(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, storageTestResult{
		OK:      true,
		Type:    target.Type(),
		Root:    target.Root(),
		Message: "存储连接与写入测试通过",
		Path:    probePath,
	})
}

func (s *Server) listLocalDirectories(w http.ResponseWriter, r *http.Request) {
	targetPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if targetPath == "" {
		cfg, err := s.store.GetConfig(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		targetPath = cfg.DownloadDir
	}
	dir, err := localDirectoryPath(targetPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items := make([]localDirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		items = append(items, localDirectoryEntry{
			Name:        entry.Name(),
			Path:        child,
			HasChildren: hasChildDirectory(child),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	parent := filepath.Dir(dir)
	if parent == dir {
		parent = ""
	}
	writeJSON(w, http.StatusOK, localDirectoryListing{
		Path:    dir,
		Parent:  parent,
		Entries: items,
	})
}

func (s *Server) checkAuth(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	configured := cfg.AuthToken != "" && cfg.CSRFToken != ""
	if !configured {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"ok":         false,
			"message":    "请先配置 auth_token 和 ct0",
		})
		return
	}
	pool, err := xclient.NewPool(cfg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": configured,
			"ok":         false,
			"message":    err.Error(),
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	diagnostics := pool.CheckAll(ctx)
	screenName := ""
	for _, client := range diagnostics.Clients {
		if client.Primary {
			screenName = client.ScreenName
			break
		}
	}
	if diagnostics.Available == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured":  configured,
			"ok":          false,
			"message":     "所有 X Cookie 均不可用",
			"diagnostics": diagnostics,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":  configured,
		"ok":          true,
		"screenName":  screenName,
		"message":     "X 登录校验通过",
		"diagnostics": diagnostics,
	})
}

func (s *Server) parseTweetLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tweet, err := s.parser.ParseTweetLinkWithOptions(r.Context(), req.URL, parserOptionsFromConfig(cfg))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, tweet)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req jobRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	kind, input, title, err := s.prepareJob(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.store.CreateJob(r.Context(), kind, input, title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.manager.Notify()
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) createJobsBatch(w http.ResponseWriter, r *http.Request) {
	var req batchJobRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("任务列表不能为空"))
		return
	}
	if len(req.Items) > 200 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("一次最多创建 200 个任务"))
		return
	}

	prepared := make([]storage.JobDraft, 0, len(req.Items))
	seen := map[string]struct{}{}
	for _, item := range req.Items {
		kind, input, title, err := s.prepareJob(r.Context(), item)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		key := string(kind) + "\x00" + strings.ToLower(input)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		prepared = append(prepared, storage.JobDraft{Kind: kind, Input: input, Title: title})
	}
	if len(prepared) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("没有可创建的任务"))
		return
	}

	created, err := s.store.CreateJobs(r.Context(), prepared)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.manager.Notify()
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listArchiveSchedules(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListArchiveSchedules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createArchiveSchedule(w http.ResponseWriter, r *http.Request) {
	var req archiveScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	schedule, err := s.store.CreateArchiveSchedule(r.Context(), archiveScheduleFromRequest(req))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.eventBus.Publish(jobs.Event{Type: "archive_schedule.created", Payload: schedule})
	writeJSON(w, http.StatusCreated, schedule)
}

func (s *Server) updateArchiveSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req archiveScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	schedule := archiveScheduleFromRequest(req)
	schedule.ID = id
	updated, err := s.store.UpdateArchiveSchedule(r.Context(), schedule)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.eventBus.Publish(jobs.Event{Type: "archive_schedule.updated", Payload: updated})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteArchiveSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteArchiveSchedule(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.eventBus.Publish(jobs.Event{Type: "archive_schedule.deleted", Payload: map[string]any{"id": id}})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) runArchiveSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := s.manager.RunArchiveScheduleNow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, created)
}

type jobRequest struct {
	Kind  storage.JobKind `json:"kind"`
	Input string          `json:"input"`
	Title string          `json:"title"`
}

type batchJobRequest struct {
	Items []jobRequest `json:"items"`
}

type archiveScheduleRequest struct {
	Name            string                        `json:"name"`
	Enabled         bool                          `json:"enabled"`
	IntervalMinutes int                           `json:"intervalMinutes"`
	Items           []storage.ArchiveScheduleItem `json:"items"`
}

type storageTestResult struct {
	OK      bool               `json:"ok"`
	Type    config.StorageType `json:"type"`
	Root    string             `json:"root"`
	Message string             `json:"message"`
	Path    string             `json:"path"`
}

func archiveScheduleFromRequest(req archiveScheduleRequest) storage.ArchiveSchedule {
	return storage.ArchiveSchedule{
		Name:            strings.TrimSpace(req.Name),
		Enabled:         req.Enabled,
		IntervalMinutes: req.IntervalMinutes,
		Items:           req.Items,
	}
}

func mergeSecretPlaceholders(cfg config.AppConfig, current config.AppConfig) config.AppConfig {
	if cfg.AuthToken == "" || cfg.AuthToken == config.SecretPlaceholder {
		cfg.AuthToken = current.AuthToken
	}
	if cfg.CSRFToken == "" || cfg.CSRFToken == config.SecretPlaceholder {
		cfg.CSRFToken = current.CSRFToken
	}
	if cfg.AdditionalCookies == "" || cfg.AdditionalCookies == config.SecretPlaceholder {
		cfg.AdditionalCookies = current.AdditionalCookies
	}
	// 仅当目标主机未变时才继承已存的存储凭据，否则调用方可令服务器用管理员真实的
	// WebDAV/SMB 密码去认证一个由调用方提供的主机（凭据外泄）。主机变更时清掉占位符，
	// 避免把字面量 "********" 当作密码发送给新主机。
	cfg.SMBPassword = mergeStorageSecret(cfg.SMBPassword, current.SMBPassword, sameSMBTarget(cfg, current))
	cfg.WebDAVPassword = mergeStorageSecret(cfg.WebDAVPassword, current.WebDAVPassword, config.SameURLAuthority(cfg.WebDAVURL, current.WebDAVURL))
	// 还原 Redacted() 为展示而屏蔽的 URL 内嵌凭据。
	cfg.ProxyURL = config.RestoreURLUserinfo(cfg.ProxyURL, current.ProxyURL)
	cfg.WebDAVURL = config.RestoreURLUserinfo(cfg.WebDAVURL, current.WebDAVURL)
	return cfg
}

// mergeStorageSecret inherits the stored password when the submitted value is
// empty/placeholder and the target host is unchanged; otherwise it clears a
// leftover placeholder so it is never sent as a literal credential.
func mergeStorageSecret(submitted, stored string, hostUnchanged bool) string {
	if submitted == "" || submitted == config.SecretPlaceholder {
		if hostUnchanged {
			return stored
		}
		return ""
	}
	return submitted
}

func sameSMBTarget(a, b config.AppConfig) bool {
	a = a.Normalized()
	b = b.Normalized()
	return strings.EqualFold(strings.TrimSpace(a.SMBHost), strings.TrimSpace(b.SMBHost)) && a.SMBPort == b.SMBPort
}

// urlHostname returns the host (without port) of rawURL, or "" if unparseable.
func urlHostname(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// isBlockedStorageTarget reports whether host is a link-local IP literal
// (e.g. 169.254.169.254 cloud-metadata), which the storage test endpoint must
// not be allowed to reach. Hostnames are allowed (resolved by the dialer); a
// full egress allowlist is out of scope for the local no-auth service.
func isBlockedStorageTarget(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

type localDirectoryListing struct {
	Path    string                `json:"path"`
	Parent  string                `json:"parent"`
	Entries []localDirectoryEntry `json:"entries"`
}

type localDirectoryEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	HasChildren bool   `json:"hasChildren"`
}

func (s *Server) prepareJob(ctx context.Context, req jobRequest) (storage.JobKind, string, string, error) {
	if req.Kind == "" {
		req.Kind = storage.JobKindTweetLink
	}
	if strings.TrimSpace(req.Input) == "" {
		return "", "", "", fmt.Errorf("任务输入不能为空")
	}
	req.Input = strings.TrimSpace(req.Input)
	if req.Kind == storage.JobKindTweetLink {
		cfg, err := s.store.GetConfig(ctx)
		if err != nil {
			return "", "", "", err
		}
		tweet, err := s.parser.ParseTweetLinkWithOptions(ctx, req.Input, parserOptionsFromConfig(cfg))
		if err != nil {
			return "", "", "", err
		}
		if len(tweet.BestMediaURLs()) == 0 {
			return "", "", "", fmt.Errorf("这条推文没有可下载媒体")
		}
		if strings.TrimSpace(req.Title) == "" {
			req.Title = fmt.Sprintf("Tweet %s", tweet.ID)
		}
	}
	if req.Kind == storage.JobKindUser && strings.TrimSpace(req.Title) == "" {
		req.Title = fmt.Sprintf("用户 %s", displayUserInput(req.Input))
	}
	if req.Kind == storage.JobKindList && strings.TrimSpace(req.Title) == "" {
		req.Title = fmt.Sprintf("列表 %s", req.Input)
	}
	if req.Kind == storage.JobKindFollowing && strings.TrimSpace(req.Title) == "" {
		req.Title = fmt.Sprintf("关注 %s", displayUserInput(req.Input))
	}
	switch req.Kind {
	case storage.JobKindTweetLink, storage.JobKindUser, storage.JobKindList, storage.JobKindFollowing:
	default:
		return "", "", "", fmt.Errorf("不支持的任务类型: %s", req.Kind)
	}
	return req.Kind, req.Input, strings.TrimSpace(req.Title), nil
}

func parserOptionsFromConfig(cfg config.AppConfig) parser.ParseOptions {
	return parser.ParseOptions{IncludeNestedTweets: cfg.IncludeNestedTweetMedia}
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListJobs(r.Context(), parseLimit(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, fmt.Errorf("job not found"))
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.manager.CancelJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.store.RetryJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.manager.Notify()
	s.eventBus.Publish(jobs.Event{Type: "job.updated", JobID: job.ID, Payload: job})
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	channel, ok := s.eventBus.Subscribe()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("事件订阅连接数已达上限"))
		return
	}
	defer s.eventBus.Unsubscribe(channel)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// ResponseController 让我们对每次写入设置写超时，从而丢弃已断开或读取过慢的
	// 客户端，避免 goroutine 长期阻塞在 Flush 上；心跳保活并检测半开连接。
	rc := http.NewResponseController(w)
	write := func(data []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
		if _, err := w.Write(data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !write([]byte(": connected\n\n")) {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !write([]byte(": ping\n\n")) {
				return
			}
		case event := <-channel:
			if !write(event.MarshalSSE()) {
				return
			}
		}
	}
}

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDownloads(r.Context(), parseLimit(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) listFailedMedia(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListFailedMedia(r.Context(), parseLimit(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) listFailedTweets(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListFailedTweetViews(r.Context(), parseLimit(r, 200))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) retryFailedTweets(w http.ResponseWriter, r *http.Request) {
	job, err := s.manager.RetryFailedTweetsNow(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) deleteFailedTweet(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteFailedTweet(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) clearFailedTweets(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAllFailedTweets(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	page := parsePositiveInt(r, "page", 1, 1, 1000000)
	pageSize := parsePositiveInt(r, "pageSize", 20, 1, 100)
	stats, err := s.store.JobStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	totalPages := 0
	if stats.Total > 0 {
		totalPages = (stats.Total + pageSize - 1) / pageSize
		if page > totalPages {
			page = totalPages
		}
	} else {
		page = 1
	}
	offset := (page - 1) * pageSize
	jobItems, err := s.store.ListJobsPage(r.Context(), pageSize, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	jobIDs := make([]int64, 0, len(jobItems))
	for _, job := range jobItems {
		jobIDs = append(jobIDs, job.ID)
	}
	downloadItems, err := s.store.ListDownloadsForJobs(r.Context(), jobIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	failedItems, err := s.store.ListFailedMediaForJobs(r.Context(), jobIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	failedTweetItems, err := s.store.ListFailedTweetViews(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	failedTweetCount, err := s.store.CountFailedTweets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	archiveSchedules, err := s.store.ListArchiveSchedules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, storage.Dashboard{
		Config:           cfg.Redacted(),
		Jobs:             jobItems,
		Downloads:        downloadItems,
		Failed:           failedItems,
		FailedTweets:     failedTweetItems,
		FailedTweetCount: failedTweetCount,
		ArchiveSchedules: archiveSchedules,
		Pagination: storage.Pagination{
			Page:       page,
			PageSize:   pageSize,
			Total:      stats.Total,
			TotalPages: totalPages,
		},
		Stats: stats,
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	defer r.Body.Close()
	// 限制请求体大小，避免超大 body 占用内存（配合无鉴权的本地服务更稳妥）。
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	// 要求 Content-Type 为 application/json：浏览器对非简单请求会先发 CORS 预检，
	// 而预检可被 CORS 策略拒绝；若放行 text/plain 等"简单"类型，跨站页面可在无预检的
	// 情况下 POST JSON body（CSRF），命中各状态变更端点。即便本地部署也受此威胁。
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return fmt.Errorf("Content-Type 必须是 application/json")
	}
	return json.NewDecoder(r.Body).Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	// 5xx 多为内部错误（SQL schema/列名、DB 文件路径、OS 错误等），其原文不应回传
	// 客户端；完整错误记录到服务端日志以便排查。4xx 通常是面向用户的校验信息。
	log.Printf("httpapi %d: %v", status, err)
	if status >= http.StatusInternalServerError {
		writeJSON(w, status, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func parseID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

func parseLimit(r *http.Request, fallback int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return limit
}

func parsePositiveInt(r *http.Request, key string, fallback int, min int, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func displayUserInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return "用户"
	}
	if strings.HasPrefix(input, "@") {
		return input
	}
	if _, err := strconv.ParseUint(input, 10, 64); err == nil {
		return input
	}
	return "@" + input
}

func localDirectoryPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			value = home
		} else {
			value = "."
		}
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		// 不存在的路径直接返回错误，避免把家目录路径泄露给探测不存在路径的调用方。
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 不是目录", path)
	}
	return path, nil
}

func hasChildDirectory(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return true
		}
	}
	return false
}
