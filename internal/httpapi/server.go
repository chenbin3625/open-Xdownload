package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	store       *storage.Store
	parser      *parser.Service
	manager     *jobs.Manager
	eventBus    *jobs.EventBus
	authMu      sync.RWMutex
	authChanged chan struct{}
}

func NewServer(store *storage.Store, parserService *parser.Service, manager *jobs.Manager, eventBus *jobs.EventBus) *Server {
	return &Server{store: store, parser: parserService, manager: manager, eventBus: eventBus, authChanged: make(chan struct{})}
}

func (s *Server) publish(event jobs.Event) {
	if s == nil || s.eventBus == nil {
		return
	}
	s.eventBus.Publish(event)
}

func (s *Server) withMeta(ctx context.Context, event jobs.Event) jobs.Event {
	if s == nil || s.store == nil {
		return event
	}
	stats, count, err := s.store.DashboardMeta(ctx)
	if err != nil {
		return event
	}
	event.Meta = storage.DashboardMetaView{Stats: stats, FailedTweetCount: count}
	return event
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	// 防止浏览器对下载目录内文件做 MIME 嗅探（媒体库端点可能输出任意扩展名文件）。
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, req)
		})
	})
	r.Use(s.requireLogin)
	// read 同时注册 GET 与 HEAD：chi 的 Get 只匹配 GET，此前所有 HEAD 请求都在
	// 路由层拿到 405（压缩中间件里的 HEAD 分支因此是死代码）。HEAD 与 GET 共用
	// handler，由 net/http 负责丢弃 body 并按实际写入量生成 Content-Length。
	// /api/events 是长连接 SSE，不注册 HEAD。
	read := func(pattern string, handler http.HandlerFunc) {
		r.Get(pattern, handler)
		if pattern != "/api/events" {
			r.Head(pattern, handler)
		}
	}
	read("/api/health", s.health)
	read("/api/auth/session", s.sessionStatus)
	r.Post("/api/auth/setup", s.setupAccount)
	r.Post("/api/auth/login", s.login)
	r.Put("/api/auth/password", s.changePassword)
	read("/api/config", s.getConfig)
	r.Put("/api/config", s.updateConfig)
	read("/api/local-directories", s.listLocalDirectories)
	r.Post("/api/local-directories", s.createLocalDirectory)
	r.Post("/api/auth/check", s.checkAuth)
	r.Post("/api/parse/tweet-link", s.parseTweetLink)
	r.Post("/api/jobs", s.createJob)
	r.Post("/api/jobs/batch", s.createJobsBatch)
	read("/api/archive-schedules", s.listArchiveSchedules)
	r.Post("/api/archive-schedules", s.createArchiveSchedule)
	r.Put("/api/archive-schedules/{id}", s.updateArchiveSchedule)
	r.Delete("/api/archive-schedules/{id}", s.deleteArchiveSchedule)
	r.Post("/api/archive-schedules/{id}/run", s.runArchiveSchedule)
	read("/api/jobs", s.listJobs)
	read("/api/jobs/{id}", s.getJob)
	read("/api/jobs/{id}/files", s.getJobFiles)
	r.Post("/api/jobs/{id}/cancel", s.cancelJob)
	r.Post("/api/jobs/{id}/retry", s.retryJob)
	read("/api/events", s.events)
	read("/api/library/downloads", s.listDownloads)
	read("/api/library/downloads/{id}/file", s.serveDownloadFile)
	read("/api/library/downloads/{id}/preview", s.serveDownloadPreview)
	read("/api/library/file", s.serveLibraryFile)
	read("/api/logs", s.listFailedMedia)
	read("/api/failed-tweets", s.listFailedTweets)
	r.Post("/api/failed-tweets/retry", s.retryFailedTweets)
	r.Delete("/api/failed-tweets/{id}", s.deleteFailedTweet)
	r.Delete("/api/failed-tweets", s.clearFailedTweets)
	read("/api/dashboard", s.dashboard)
	read("/api/dashboard/meta", s.dashboardMeta)
	// JSON 按 Accept-Encoding 协商 br/gzip；SSE 与过小 payload 保持 identity。
	return compressJSON(r)
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
	stored, err := s.store.GetStoredConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// PATCH 语义：只处理请求里出现的字段。JSON `null` 会解成 nil map（不报错），
	// 若直接透传给 storage，nil fields 会被当成"全量替换为零值配置"，
	// 一次 null 请求就把 downloadDir/备用 Cookie/代理重置成默认值。这里明确拒绝。
	fields, err := decodeJSONObject(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// 环境变量接管的字段：请求里回显的值不能参与校验（见下方 ProxyURL/DownloadDir），
	// 落库值由 storage 的 ResolveSubmission 保证保留库内值，这里原样透传 patch。
	cfg := stored
	payload, err := json.Marshal(fields)
	if err != nil {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("配置格式无效"))
		return
	}
	if err := json.Unmarshal(payload, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("配置字段类型无效"))
		return
	}
	validationCfg, err := config.ResolveSubmission(cfg, stored)
	if err != nil {
		writeError(w, http.StatusBadRequest, storage.ErrValidation(err.Error()))
		return
	}
	// 校验用的代理地址也要取"生效值"：env 注入的代理（可能是 host:port 简写，
	// 或不含 scheme 的写法）只由部署方负责，不该让整份配置保存 400。
	if !config.IsEnvOverridden("ProxyURL") {
		if err := httpx.ValidateProxyURL(validationCfg.ProxyURL); err != nil {
			writeError(w, http.StatusBadRequest, storage.ErrValidation(err.Error()))
			return
		}
	}
	// 下载目录同时充当媒体库的沙箱根：若允许改成 "/"，任意媒体文件都会变成
	// "下载目录内"从而可被读取。允许根必须取自已存储的配置与环境变量生效值，
	// 用请求里的新值会让攻击者自证合法（"/" 的允许根就包含 "/"）。
	submittedDir := strings.TrimSpace(cfg.DownloadDir)
	effectiveDir := config.EffectiveDownloadDir(stored)
	envDirActive := config.IsEnvOverridden("DownloadDir")
	switch {
	case submittedDir == "":
		// 未提交 downloadDir：PATCH 语义，无需校验。
	case envDirActive && pathsEqual(submittedDir, effectiveDir):
		// env 生效值的回显：运行时不会变、也不会落库，跳过校验。
	case envDirActive && !pathsEqual(submittedDir, stored.DownloadDir):
		// env 接管时提交新目录不会生效。旧实现会走到 validateDownloadDir 报
		// "请用 OPEN_XDOWNLOAD_DOWNLOAD_DIR 环境变量启动"（用户其实已经在用），
		// 这里直接说明由环境变量接管，避免误导。
		writeError(w, http.StatusBadRequest, storage.ErrValidation(
			"下载目录由 OPEN_XDOWNLOAD_DOWNLOAD_DIR 环境变量接管，保存不会写入库；如需修改请调整该环境变量"))
		return
	default:
		if err := validateDownloadDir(validationCfg.DownloadDir, localDirectoryAllowedRoots(stored)); err != nil {
			writeError(w, http.StatusBadRequest, storage.ErrValidation(err.Error()))
			return
		}
	}
	updated, err := s.store.UpdateConfigPatch(r.Context(), fields)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// 响应也要套一层 env 覆盖：GET 返回的是 env 生效值，若 PUT 响应回库内值，
	// 前端"保存后比对"会永远不相等（这正是"env 覆盖部署下刷新配置被吞"的根因）。
	writeJSON(w, http.StatusOK, config.ApplyEnvOverrides(updated).Redacted())
}

// pathsEqual 比较两个目录是否指向同一位置，比较前统一 Abs + Clean，
// 避免 "./downloads" 与 "/abs/downloads" 这类写法差异。
func pathsEqual(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func (s *Server) listLocalDirectories(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	targetPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if targetPath == "" {
		targetPath = cfg.DownloadDir
	}
	dir, err := localDirectoryPath(targetPath, localDirectoryAllowedRoots(cfg))
	if err != nil {
		log.Printf("listLocalDirectories: path=%q err=%v", targetPath, err)
		writeError(w, http.StatusBadRequest, storage.ErrValidation("无法读取目录"))
		return
	}
	listing, err := localDirectoryListingForPath(dir)
	if err != nil {
		log.Printf("listLocalDirectories: dir=%q err=%v", dir, err)
		writeError(w, http.StatusBadRequest, storage.ErrValidation("无法读取目录"))
		return
	}
	if _, err := localDirectoryPath(listing.Parent, localDirectoryAllowedRoots(cfg)); err != nil {
		listing.Parent = ""
	}
	writeJSON(w, http.StatusOK, listing)
}

func localDirectoryListingForPath(dir string) (localDirectoryListing, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return localDirectoryListing{}, err
	}
	items := make([]localDirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		items = append(items, localDirectoryEntry{
			Name: entry.Name(),
			Path: child,
			// 子目录是否还有孙目录放到展开时再读，避免每个子项一次 ReadDir。
			HasChildren: true,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	parent := filepath.Dir(dir)
	if parent == dir {
		parent = ""
	}
	return localDirectoryListing{
		Path:    dir,
		Parent:  parent,
		Entries: items,
	}, nil
}

func (s *Server) createLocalDirectory(w http.ResponseWriter, r *http.Request) {
	var req localDirectoryRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dir, err := createLocalDirectoryPath(req.Path, localDirectoryAllowedRoots(cfg))
	if err != nil {
		log.Printf("createLocalDirectory: %v", err)
		writeError(w, http.StatusBadRequest, storage.ErrValidation("不允许创建此目录，请检查路径和访问权限"))
		return
	}
	listing, err := localDirectoryListingForPath(dir)
	if err != nil {
		log.Printf("createLocalDirectory listing: %v", err)
		writeError(w, http.StatusBadRequest, storage.ErrValidation("无法读取目录"))
		return
	}
	if _, err := localDirectoryPath(listing.Parent, localDirectoryAllowedRoots(cfg)); err != nil {
		listing.Parent = ""
	}
	writeJSON(w, http.StatusCreated, listing)
}

func (s *Server) checkAuth(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if r.ContentLength != 0 {
		var submitted config.AppConfig
		if err := decodeJSON(w, r, &submitted); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		stored, err := s.store.GetStoredConfig(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		resolved, err := config.ResolveSubmission(submitted, stored)
		if err != nil {
			writeError(w, http.StatusBadRequest, storage.ErrValidation(err.Error()))
			return
		}
		cfg = config.ApplyEnvOverrides(resolved)
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
	// 用临时池做"非破坏性探测"，不复用 jobs 的生产池：
	// CheckAll/GetSelfScreenName 在 401（及非 WAF 的 403）时会调用 disable（主账号
	// 30 分钟、备用 5 分钟）。如果探测打到正在归档的生产池上，用户点一次"检测 Cookie"
	// 就可能让归档任务半小时内用不了主账号。临时池与生产池共享底层
	// httpx.Transport（按代理缓存），连接仍复用，探测本身只在手动点击时发生。
	probePool, err := xclient.NewPool(cfg)
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
	diagnostics := probePool.CheckAll(ctx)
	// "校验通过"必须以真正拿到 screen name 为准：Diagnostics().Available 只表示
	// "客户端没有被 disable"，网络不通/代理错误时它仍然是 1，会被误报成通过。
	screenName := ""
	for _, client := range diagnostics.Clients {
		if client.OK && client.ScreenName != "" {
			screenName = client.ScreenName
			if client.Primary {
				break
			}
		}
	}
	if screenName == "" {
		message := "X 登录校验失败：未能通过认证（Cookie 可能已失效）"
		if diagnostics.Available == 0 {
			message = "所有 X Cookie 均不可用"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"configured":  configured,
			"ok":          false,
			"message":     message,
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
	// 单推解析请求把配置的代理注入本次请求用客户端（M1），使解析链路与 timeline/下载
	// 一致地走代理（httpx.Client 空代理时尊重环境变量）。
	tweet, err := s.parser.ParseTweetLinkWithClient(r.Context(), req.URL, httpx.Client(cfg.ProxyURL, 20*time.Second), parserOptionsFromConfig(cfg))
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
	s.publish(s.withMeta(r.Context(), jobs.Event{Type: "job.created", JobID: job.ID, Payload: job}))
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
	s.publish(s.withMeta(r.Context(), jobs.Event{Type: "jobs.created", Payload: created}))
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
		var uf *storage.UserFacingError
		if errors.As(err, &uf) {
			writeError(w, uf.Status, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	s.publish(jobs.Event{Type: "archive_schedule.created", Payload: schedule})
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
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, storage.ErrNotFound("计划不存在", err))
			return
		}
		var uf *storage.UserFacingError
		if errors.As(err, &uf) {
			writeError(w, uf.Status, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	s.publish(jobs.Event{Type: "archive_schedule.updated", Payload: updated})
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
	s.publish(jobs.Event{Type: "archive_schedule.deleted", Payload: map[string]any{"id": id}})
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
		// 业务拒绝（计划不存在/上次仍未结束）走 typed error 映射到 4xx；
		// 其它（DB/IO）一律 500，不能把内部错误原文当 400 回给客户端。
		var uf *storage.UserFacingError
		switch {
		case errors.As(err, &uf):
			writeError(w, uf.Status, err)
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, storage.ErrNotFound("记录不存在", err))
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
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

type failedTweetPage struct {
	Items      []storage.FailedTweetView `json:"items"`
	Pagination storage.Pagination        `json:"pagination"`
}

type jobFilesResponse struct {
	Downloads []storage.DownloadRecord `json:"downloads"`
	Failed    []storage.FailedMedia    `json:"failed"`
	Truncated bool                     `json:"truncated"`
}

type dashboardMeta struct {
	Stats            storage.JobStats `json:"stats"`
	FailedTweetCount int              `json:"failedTweetCount"`
}

func archiveScheduleFromRequest(req archiveScheduleRequest) storage.ArchiveSchedule {
	return storage.ArchiveSchedule{
		Name:            strings.TrimSpace(req.Name),
		Enabled:         req.Enabled,
		IntervalMinutes: req.IntervalMinutes,
		Items:           req.Items,
	}
}

func isAllowedDirectMediaURL(parsed *url.URL) bool {
	// 单一口径见 httpx.IsTwimgHost：媒体下载/海报回源/解析规范化共用同一规则。
	return httpx.IsTwimgHost(parsed.Hostname())
}

type localDirectoryListing struct {
	Path    string                `json:"path"`
	Parent  string                `json:"parent"`
	Entries []localDirectoryEntry `json:"entries"`
}

type localDirectoryRequest struct {
	Path string `json:"path"`
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
		_, tweetID, err := parser.ExtractTweetURL(req.Input)
		if err != nil {
			return "", "", "", err
		}
		if strings.TrimSpace(req.Title) == "" {
			req.Title = fmt.Sprintf("Tweet %s", tweetID)
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
	if req.Kind == storage.JobKindMediaURL {
		parsed, err := url.Parse(req.Input)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return "", "", "", fmt.Errorf("请输入有效的媒体 URL")
		}
		if !isAllowedDirectMediaURL(parsed) {
			return "", "", "", fmt.Errorf("媒体 URL 仅支持 twimg.com 域名")
		}
		if strings.TrimSpace(req.Title) == "" {
			req.Title = "媒体 URL"
		}
	}
	switch req.Kind {
	case storage.JobKindTweetLink, storage.JobKindMediaURL, storage.JobKindUser, storage.JobKindList, storage.JobKindFollowing:
	default:
		return "", "", "", fmt.Errorf("不支持的任务类型: %s", req.Kind)
	}
	return req.Kind, req.Input, strings.TrimSpace(req.Title), nil
}

func parserOptionsFromConfig(cfg config.AppConfig) parser.ParseOptions {
	return parser.ParseOptions{IncludeNestedTweets: cfg.IncludeNestedTweetMedia}
}

type jobListPage struct {
	Items    []storage.Job `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	page := parsePositiveInt(r, "page", 1, 1, 1000000)
	pageSize := parsePositiveInt(r, "pageSize", 10, 1, 100)
	offset := (page - 1) * pageSize
	items, err := s.store.ListJobsPage(r.Context(), pageSize, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if items == nil {
		items = []storage.Job{}
	}
	writeJSON(w, http.StatusOK, jobListPage{
		Items:    items,
		Page:     page,
		PageSize: pageSize,
	})
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
			writeError(w, http.StatusNotFound, storage.ErrNotFound("任务不存在", err))
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) getJobFiles(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	downloads, failed, truncated, err := s.store.JobFiles(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, storage.ErrNotFound("任务不存在", err))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, jobFilesResponse{Downloads: downloads, Failed: failed, Truncated: truncated})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.manager.CancelJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, storage.ErrNotFound("任务不存在", err))
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
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
		var uf *storage.UserFacingError
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, storage.ErrNotFound("任务不存在", err))
		case errors.Is(err, storage.ErrJobRetryBusy):
			// 业务冲突（原任务仍在运行）：保持 400 + 可读文案，前端直接展示该消息。
			writeError(w, http.StatusBadRequest, storage.ErrValidation(err.Error()))
		case errors.As(err, &uf):
			writeError(w, uf.Status, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	s.manager.Notify()
	s.publish(s.withMeta(r.Context(), jobs.Event{Type: "job.created", JobID: job.ID, Payload: job}))
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value(authContextKey{}).(authenticatedSession)
	if s.eventBus == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("事件总线不可用"))
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
	// 提示 nginx 等反向代理不要缓冲事件流，保证前端实时收到消息（M8）。
	w.Header().Set("X-Accel-Buffering", "no")

	// ResponseController 兼容 HTTP/1.1、HTTP/2 与 HTTP/3：不依赖 http.Flusher
	// 类型断言（quic-go / 中间件包装的 ResponseWriter 常常没有该接口）。
	rc := http.NewResponseController(w)
	write := func(data []byte) bool {
		select {
		case <-session.revoked:
			return false
		default:
		}
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
		if _, err := w.Write(data); err != nil {
			return false
		}
		if err := rc.Flush(); err != nil {
			return false
		}
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
		case <-session.revoked:
			return
		case <-ticker.C:
			if !write([]byte(": ping\n\n")) {
				return
			}
		case event, ok := <-channel:
			if !ok {
				return
			}
			if !write(event.MarshalSSE()) {
				return
			}
		}
	}
}

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	paged := r.URL.Query().Has("page") || r.URL.Query().Has("pageSize")
	if !paged {
		// 旧数组接口保留兼容，但明确收敛：默认 100、最多 100。它不再承担"取全量"
		// 的职责（前端已改走分页端点），超限通过响应头告知，避免客户端把截断当成
		// 完整结果。
		limit := parseLimit(r, 100, 100)
		items, truncated, err := s.store.ListLibraryDownloadsWithStatus(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for index := range items {
			if items[index].ID == 0 {
				items[index].FileURL = "/api/library/file?path=" + url.QueryEscape(items[index].FilePath)
			}
		}
		if truncated {
			w.Header().Set("X-Result-Truncated", "true")
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	// paged 模式交给 storage 的 SQL 分页（LIMIT/OFFSET + COUNT + 过滤），不再
	// 每次请求把最多 10 万条记录读进来在 Go 里过滤。
	page := parsePositiveInt(r, "page", 1, 1, 1000000)
	pageSize := parsePositiveInt(r, "pageSize", storage.DefaultLibraryPageSize, 1, storage.MaxLibraryPageSize)
	result, err := s.store.ListLibraryDownloadsPage(r.Context(), storage.LibraryPageOptions{
		Page:     page,
		PageSize: pageSize,
		Filter: storage.LibraryFilter{
			Kind:   strings.TrimSpace(r.URL.Query().Get("kind")),
			User:   strings.TrimSpace(r.URL.Query().Get("user")),
			Search: strings.TrimSpace(r.URL.Query().Get("search")),
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if result.Items == nil {
		result.Items = []storage.DownloadRecord{}
	}
	for index := range result.Items {
		if result.Items[index].ID == 0 {
			result.Items[index].FileURL = "/api/library/file?path=" + url.QueryEscape(result.Items[index].FilePath)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) serveDownloadPreview(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, err := s.store.GetDownload(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if record == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("download not found"))
		return
	}
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	videoPath, archiveRoot, err := s.resolveDownloadPath(r.Context(), cfg.DownloadDir, record.FilePath)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	if _, err := os.Stat(videoPath); err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("文件不存在"))
		return
	}
	posterPath := videoPath + ".preview.jpg"
	if !withinAllowedRoot(posterPath, []string{archiveRoot}) {
		writeError(w, http.StatusForbidden, fmt.Errorf("预览图路径不在允许目录内"))
		return
	}
	// Resolve symlinks on the poster path to prevent serving files outside
	// the download directory via a symlink escape.
	resolvedPoster, err := filepath.EvalSymlinks(posterPath)
	if err == nil && !withinAllowedRoot(resolvedPoster, []string{archiveRoot}) {
		writeError(w, http.StatusForbidden, fmt.Errorf("预览图路径不在允许目录内"))
		return
	}
	servePath := posterPath
	if err == nil {
		servePath = resolvedPoster
	}
	if info, stErr := os.Stat(servePath); stErr == nil && !info.IsDir() {
		http.ServeFile(w, r, servePath)
		return
	}
	posterURL := record.PreviewURL
	if strings.TrimSpace(posterURL) == "" || isVideoURL(posterURL) {
		posterURL = storage.VideoPreviewURL(record.MediaURL)
	}
	parsed, err := url.Parse(posterURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !isAllowedPreviewHost(parsed.Hostname()) {
		writeError(w, http.StatusNotFound, fmt.Errorf("没有可用的视频预览图"))
		return
	}
	// 缩略图回源必须与媒体下载共用同一套代理解析：浏览器无法直连 twimg 的部署里，
	// 走系统默认 transport 的回源请求会全部失败，这正是历史媒体看不到预览图的
	// 原因之一。重定向同样限定在 twimg.com。
	//
	// 回源前先查失败负缓存：画廊里几十张缺海报的卡片在首屏就会各发一次请求，
	// 上游持续 404/超时时如果不记负缓存，每次刷新都会重复回源（还会穿透到 X CDN）。
	if failure, ok := posterFailures.lookup(posterPath); ok {
		writeError(w, failure.status, &posterStatusError{status: failure.status, message: failure.message})
		return
	}
	client := httpx.Client(cfg.ProxyURL, 20*time.Second)
	httpx.RestrictRedirects(client, isAllowedPreviewHost)
	// 回源用与会话解绑的 ctx：single-flight 的 leader 如果因为浏览器取消而中断，
	// 其它等待同一张海报的请求不应该跟着失败。超时由 client.Timeout 兜底。
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)
	defer cancel()
	err = posterFetchGroup.Do(posterPath, func() error {
		return fetchRemotePoster(fetchCtx, client, posterFetchLimiter, posterURL, posterPath, maxPosterBytes)
	})
	if err != nil {
		status, message := posterFailureStatus(err)
		if r.Context().Err() != nil && status == http.StatusBadGateway {
			status, message = http.StatusServiceUnavailable, "预览图抓取已取消"
		}
		log.Printf("poster fetch %s: %v", posterURL, err)
		posterFailures.store(posterPath, status, message)
		writeError(w, status, &posterStatusError{status: status, message: message})
		return
	}
	posterFailures.remove(posterPath)
	http.ServeFile(w, r, posterPath)
}

// posterFailureStatus 把回源错误映射为 HTTP 状态：上游明确的 4xx/格式错误原样
// 透传；超时/网络错误区分 504 与 502，便于前端与运维区分"上游慢"和"上游坏"。
func posterFailureStatus(err error) (int, string) {
	var statusErr *posterStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status, statusErr.message
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return http.StatusGatewayTimeout, "视频预览图回源超时"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "视频预览图回源超时"
	}
	return http.StatusBadGateway, "下载视频预览图失败"
}

// posterFailureCache 记住短时间内的回源失败，避免同一张缺图在画廊每次刷新时
// 反复回源（无鉴权部署下也是向外网的放大面）。成功写入磁盘后会删除对应条目。
type posterFailureCache struct {
	entries sync.Map // map[string]posterFailureEntry
}

type posterFailureEntry struct {
	status    int
	message   string
	expiresAt time.Time
}

// posterFailureTTL 是负缓存有效期。上游 4xx/格式错误通常是"这张图就是没有"，
// 缓存久一点；5xx/超时属于瞬时故障，只缓存一小会儿，避免把临时抖动钉成 10 分钟。
const (
	posterFailureTTL          = 10 * time.Minute
	posterTransientFailureTTL = time.Minute
)

func (c *posterFailureCache) store(key string, status int, message string) {
	ttl := posterFailureTTL
	if status >= http.StatusInternalServerError {
		ttl = posterTransientFailureTTL
	}
	c.entries.Store(key, posterFailureEntry{status: status, message: message, expiresAt: time.Now().Add(ttl)})
}

func (c *posterFailureCache) lookup(key string) (posterFailureEntry, bool) {
	value, ok := c.entries.Load(key)
	if !ok {
		return posterFailureEntry{}, false
	}
	entry := value.(posterFailureEntry)
	if time.Now().After(entry.expiresAt) {
		c.entries.Delete(key)
		return posterFailureEntry{}, false
	}
	return entry, true
}

func (c *posterFailureCache) remove(key string) {
	c.entries.Delete(key)
}

var posterFailures = &posterFailureCache{}

// posterFetchSingleFlight 是海报回源的最小 single-flight：同一张海报的并发请求只让
// 一个真正回源，其余等待它的结果，避免首屏几十张卡片各自打一次上游。
type posterFetchSingleFlight struct {
	mu    sync.Mutex
	calls map[string]*posterFetchCall
}

type posterFetchCall struct {
	done chan struct{}
	err  error
}

func (g *posterFetchSingleFlight) Do(key string, fn func() error) error {
	g.mu.Lock()
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-call.done
		return call.err
	}
	call := &posterFetchCall{done: make(chan struct{})}
	if g.calls == nil {
		g.calls = map[string]*posterFetchCall{}
	}
	g.calls[key] = call
	g.mu.Unlock()

	call.err = fn()
	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	close(call.done)
	return call.err
}

var posterFetchGroup = &posterFetchSingleFlight{}

// maxPosterBytes 限制单张回源海报的大小；name=small 缩略图通常 <100KB。
const maxPosterBytes = 10 << 20

// posterStatusError 表示可以直接映射为 HTTP 状态码的回源失败（上游 4xx/5xx、非图片）。
type posterStatusError struct {
	status  int
	message string
}

func (e *posterStatusError) Error() string { return e.message }

// fetchRemotePoster 回源下载海报并原子发布到 posterPath。并发名额覆盖整个下载过程（含
// 读取 body），否则首屏几十张卡片的 body 仍会同时下载。超过 maxBytes 时报错且不发布：
// 截断的图片一旦落盘，之后会被当作有效缓存永久返回。
func fetchRemotePoster(ctx context.Context, client *http.Client, limiter countingSemaphore, posterURL string, posterPath string, maxBytes int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, posterURL, nil)
	if err != nil {
		return &posterStatusError{status: http.StatusNotFound, message: "预览图地址无效"}
	}
	if err := limiter.acquire(ctx); err != nil {
		return &posterStatusError{status: http.StatusServiceUnavailable, message: "预览图抓取繁忙"}
	}
	defer limiter.release()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &posterStatusError{status: http.StatusNotFound, message: "视频预览图不可用"}
	}
	// 上游 Content-Type 的大小写与参数都不可信（曾有 CDN 返回 "IMAGE/JPEG"）：
	// 用 mime.ParseMediaType 拆掉参数，再对主类型做大小写无关比较。
	contentType, _, contentTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentTypeErr != nil || !strings.EqualFold(strings.SplitN(contentType, "/", 2)[0], "image") {
		return &posterStatusError{status: http.StatusUnsupportedMediaType, message: "视频预览图格式无效"}
	}
	temporary, err := os.CreateTemp(filepath.Dir(posterPath), ".preview-*.jpg")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if _, err := httpx.CopyLimited(temporary, response.Body, maxBytes); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, posterPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func isAllowedPreviewHost(host string) bool {
	return httpx.IsTwimgHost(host)
}

// posterFetchLimiter bounds concurrent remote poster fetches: a gallery grid
// with many missing posters would otherwise open dozens of simultaneous
// proxied requests in one paint. Local disk hits return before acquiring it.
const maxConcurrentPosterFetches = 4

type countingSemaphore chan struct{}

var posterFetchLimiter = make(countingSemaphore, maxConcurrentPosterFetches)

func (s countingSemaphore) acquire(ctx context.Context) error {
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s countingSemaphore) release() {
	select {
	case <-s:
	default:
	}
}

func isVideoURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	return ext == ".mp4" || ext == ".mov" || ext == ".m4v" || ext == ".webm" || ext == ".ogv"
}

// resolveLocalLibraryPath resolves requested against the download directory,
// rejecting traversal and symlink escapes. Missing files return an error that
// satisfies errors.Is(err, os.ErrNotExist) so callers can map it to 404.
func resolveLocalLibraryPath(root, requested string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	filePath, err := filepath.Abs(requested)
	if err != nil {
		return "", fmt.Errorf("文件路径无效")
	}
	rel, err := filepath.Rel(rootAbs, filePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errFileOutsideDownloadDir
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("下载目录不存在: %w", os.ErrNotExist)
	}
	resolvedFile, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return "", fmt.Errorf("文件不存在: %w", os.ErrNotExist)
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedFile)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) || filepath.IsAbs(resolvedRel) {
		return "", errFileOutsideDownloadDir
	}
	return resolvedFile, nil
}

var errFileOutsideDownloadDir = errors.New("文件不在下载目录内")

// writeResolveError maps resolveLocalLibraryPath failures to HTTP errors.
// Both missing files and "outside download dir" yield 404 — the latter case
// usually means the download directory was changed and the path rewrite
// did not cover this record (e.g. the old directory was empty).
func writeResolveError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, storage.ErrNotFound("文件不存在", err))
		return
	}
	if errors.Is(err, errFileOutsideDownloadDir) {
		writeError(w, http.StatusNotFound, storage.ErrNotFound("文件不在当前下载目录中", err))
		return
	}
	// 其它解析失败（权限、路径异常）只回固定文案，原文进日志。
	log.Printf("resolve media path: %v", err)
	writeError(w, http.StatusForbidden, storage.ErrValidation("文件不可访问"))
}

func (s *Server) resolveDownloadPath(ctx context.Context, currentRoot, requested string) (string, string, error) {
	allRoots, err := s.store.DownloadRoots(ctx)
	if err != nil {
		return "", "", err
	}
	// 历史根来自 storage 的 download_roots 表。写侧已用 config.ValidateDownloadRoot
	// 过滤，但该函数对"家目录本身"存在符号链接漏判（见 unsafeDownloadRoot），
	// 因此读侧再过一遍 httpapi 自己的沙箱判断，绝不把家目录/文件系统根当媒体根。
	roots := make([]string, 0, len(allRoots)+1)
	for _, root := range allRoots {
		if unsafeDownloadRoot(root) {
			logUnsafeHistoricalDownloadRoot(root)
			continue
		}
		roots = append(roots, root)
	}
	// 当前根也要过同一套校验：库内值在启动时已被 repair，但
	// OPEN_XDOWNLOAD_DOWNLOAD_DIR 由部署方任意设置，`/` 或家目录本身一旦被
	// 当成当前根，record 端点就会对任意绝对路径放行、海报的 root 校验也会失效。
	// 非法值直接跳过（可信根表里本来也不会包含它们）。
	if strings.TrimSpace(currentRoot) != "" {
		if unsafeDownloadRoot(currentRoot) {
			logUnsafeCurrentDownloadRoot(currentRoot)
		} else {
			roots = append(roots, currentRoot)
		}
	}
	lastErr := errFileOutsideDownloadDir
	for _, root := range roots {
		path, err := resolveLocalLibraryPath(root, requested)
		if err == nil {
			resolvedRoot, err := filepath.EvalSymlinks(root)
			return path, resolvedRoot, err
		}
		if !errors.Is(err, errFileOutsideDownloadDir) {
			lastErr = err
		}
	}
	return "", "", lastErr
}

// serveLibraryFile exposes media files saved under the download directory
// (including unregistered/orphan files referenced by the gallery). The
// extension whitelist keeps the endpoint from turning into an arbitrary file
// reader for anything else that may live in the download directory.
func (s *Server) serveLibraryFile(w http.ResponseWriter, r *http.Request) {
	pathValue := strings.TrimSpace(r.URL.Query().Get("path"))
	if pathValue == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("缺少文件路径"))
		return
	}
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !storage.IsLibraryMediaPath(pathValue) {
		writeError(w, http.StatusForbidden, fmt.Errorf("仅支持媒体文件"))
		return
	}
	// 与 /api/library/downloads/{id}/file 保持同一套多根语义：改过下载目录之后，
	// 旧目录里的孤儿文件（画廊用 ?path= 打开的条目）也应该继续可读，否则同一份
	// 文件"有记录能播、没记录 404"。
	filePath, _, err := s.resolveDownloadPath(r.Context(), cfg.DownloadDir, pathValue)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, fmt.Errorf("文件不存在"))
		return
	}
	http.ServeFile(w, r, filePath)
}

// serveDownloadFile exposes only files recorded by the local filestore and
// keeps the resolved path inside the configured download directory.
func (s *Server) serveDownloadFile(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, err := s.store.GetDownload(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if record == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("download not found"))
		return
	}
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	filePath, _, err := s.resolveDownloadPath(r.Context(), cfg.DownloadDir, record.FilePath)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, fmt.Errorf("文件不存在"))
		return
	}
	http.ServeFile(w, r, filePath)
}

func (s *Server) listFailedMedia(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListFailedMedia(r.Context(), parseLimit(r, 100, 200))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) listFailedTweets(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("page") || r.URL.Query().Has("pageSize") {
		page := parsePositiveInt(r, "page", 1, 1, 1000000)
		pageSize := parsePositiveInt(r, "pageSize", 20, 1, 100)
		items, total, err := s.store.ListFailedTweetViewsPage(r.Context(), pageSize, (page-1)*pageSize)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		totalPages := 0
		if total > 0 {
			totalPages = (total + pageSize - 1) / pageSize
			if page > totalPages {
				page = totalPages
				items, total, err = s.store.ListFailedTweetViewsPage(r.Context(), pageSize, (page-1)*pageSize)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
			}
		} else {
			page = 1
		}
		writeJSON(w, http.StatusOK, failedTweetPage{
			Items: items,
			Pagination: storage.Pagination{
				Page:       page,
				PageSize:   pageSize,
				Total:      total,
				TotalPages: totalPages,
			},
		})
		return
	}
	items, err := s.store.ListFailedTweetViews(r.Context(), parseLimit(r, 200, 500))
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
	s.publish(s.withMeta(r.Context(), jobs.Event{Type: "failed_tweet.deleted", Payload: map[string]any{"id": id}}))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) clearFailedTweets(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAllFailedTweets(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.publish(s.withMeta(r.Context(), jobs.Event{Type: "failed_tweets.cleared"}))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	page := parsePositiveInt(r, "page", 1, 1, 1000000)
	pageSize := parsePositiveInt(r, "pageSize", 10, 1, 100)
	stats, failedTweetCount, err := s.store.DashboardMeta(r.Context())
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
	writeJSON(w, http.StatusOK, storage.Dashboard{
		Jobs:             jobItems,
		FailedTweetCount: failedTweetCount,
		Pagination: storage.Pagination{
			Page:       page,
			PageSize:   pageSize,
			Total:      stats.Total,
			TotalPages: totalPages,
		},
		Stats: stats,
	})
}

func (s *Server) dashboardMeta(w http.ResponseWriter, r *http.Request) {
	stats, failedTweetCount, err := s.store.DashboardMeta(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, dashboardMeta{
		Stats:            stats,
		FailedTweetCount: failedTweetCount,
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	defer r.Body.Close()
	// 限制请求体大小，避免超大 body 占用内存（配合无鉴权的本地服务更稳妥）。
	limitWriter := w
	for {
		wrapper, ok := limitWriter.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		limitWriter = wrapper.Unwrap()
	}
	r.Body = http.MaxBytesReader(limitWriter, r.Body, 2<<20)
	// 要求 Content-Type 为 application/json：浏览器对非简单请求会先发 CORS 预检，
	// 而预检可被 CORS 策略拒绝；若放行 text/plain 等"简单"类型，跨站页面可在无预检的
	// 情况下 POST JSON body（CSRF），命中各状态变更端点。即便本地部署也受此威胁。
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return storage.ErrValidation("Content-Type 必须是 application/json")
	}
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		// 解码错误原文包含 Go 类型/结构体字段名（如
		// "json: cannot unmarshal string into Go struct field
		// archiveScheduleRequest.intervalMinutes of type int"），既是内部实现
		// 细节，对调用方也没有可操作性。这里只把原文写进服务端日志，回给客户端
		// 的是统一文案；具体字段问题由端点自身的业务校验给出可读提示。
		log.Printf("httpapi decode %s %s: %v", r.Method, r.URL.Path, err)
		if errors.Is(err, io.EOF) {
			return storage.ErrValidation("请求体不能为空")
		}
		return storage.ErrValidation("请求格式无效")
	}
	return nil
}

// decodeJSONObject 在 decodeJSON 之上要求请求体是一个 JSON 对象（非 null）。
// JSON `null` 解进 map 会得到 nil map 且不报错；若继续走 PATCH 路径，
// storage 会把 nil fields 当成"全量替换为零值配置"，一次 null 请求就能把
// downloadDir/备用 Cookie/代理重置成默认值。这里直接拒绝。
func decodeJSONObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := decodeJSON(w, r, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, storage.ErrValidation("请求体必须是一个 JSON 对象")
	}
	return fields, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	var uf *storage.UserFacingError
	if errors.As(err, &uf) {
		log.Printf("httpapi %d: %v", uf.Status, err)
		writeJSON(w, uf.Status, map[string]string{"error": uf.Message})
		return
	}
	var pathError *os.PathError
	var urlError *url.Error
	var databaseError interface{ Code() int }
	if errors.As(err, &pathError) || errors.As(err, &urlError) || errors.As(err, &databaseError) {
		status = http.StatusInternalServerError
	}
	if errors.Is(err, sql.ErrNoRows) {
		log.Printf("httpapi 404: %v", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "记录不存在"})
		return
	}
	log.Printf("httpapi %d: %v", status, err)
	if status >= http.StatusInternalServerError {
		writeJSON(w, status, map[string]string{"error": "internal error"})
		return
	}
	// 4xx 多数来自端点内已构造好的面向用户消息（校验/格式/权限），直接返回。
	// 涉及内部错误的端点应先包装为 UserFacingError 或改用 5xx。
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func parseID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		// 不把 strconv 的原文回给客户端（"strconv.ParseInt: parsing ..."），
		// 对调用方来说只是 id 不合法。
		return 0, storage.ErrValidation("无效的 ID")
	}
	return id, nil
}

// parseLimit 解析 ?limit=，并就地收敛到 [1, max]。上限约束原本只存在于 storage
// 层，边界层把原始值（含负数、含 ?limit=-1 这种会被下游放大成上限的输入）直接
// 透传；由调用方显式声明自己的 max，让"limit 合法"这件事在入口就成立。
func parseLimit(r *http.Request, fallback int, max int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	if max > 0 && limit > max {
		return max
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

// localDirectoryAllowedRoots 返回本地目录浏览/创建允许的根集合：用户家目录与当前
// 配置的下载目录。无鉴权本地服务下，限制任意路径枚举/创建（S5）。刻意不含进程工作
// 目录：launchd、未配 WorkingDirectory 的 systemd 下 cwd 就是 "/"，会让沙箱整体失效。
// 默认下载目录 <cwd>/downloads 作为"当前下载目录"仍然可用。
// localDirectoryAllowedRoots 返回本地目录选择器/配置校验允许的根：
// 家目录 + 库内下载目录 + 环境变量生效的下载目录。
//
// 第三个根是必须的：GET /api/config 返回的是 env 生效目录，前端保存时会把该值
// 原样回传；如果只拿库内目录当允许根，env 覆盖部署下任何保存都会 400，而且报错
// 文案还在建议用户"用 OPEN_XDOWNLOAD_DOWNLOAD_DIR"——用户其实已经照做了。
func localDirectoryAllowedRoots(cfg config.AppConfig) []string {
	roots := []string{}
	seen := map[string]bool{}
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			return
		}
		seen[abs] = true
		roots = append(roots, abs)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		// 家目录作为"浏览根"始终允许（目录选择器默认就停在家目录）；它不会被当作
		// 下载/媒体沙箱根使用。
		add(home)
	}
	// 下载目录只有在能当沙箱用时才是允许根：env 把下载目录设成 `/` 时若也加进
	// 允许根，目录选择器就能浏览/创建任意路径，配置校验也会被"自证合法"绕过。
	addIfSafeDownloadRoot(add, cfg.DownloadDir)
	addIfSafeDownloadRoot(add, config.EffectiveDownloadDir(cfg))
	return roots
}

// addIfSafeDownloadRoot 只把能当沙箱根的目录加入允许根。
func addIfSafeDownloadRoot(add func(string), dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	if unsafeDownloadRoot(dir) {
		return
	}
	add(dir)
}

// unsafeDownloadRoot 报告 dir 是否不能作为下载/媒体沙箱根（文件系统根或家目录本身）。
//
// 比 config.ValidateDownloadRoot 更严格：后者只对候选路径做 EvalSymlinks、不解析
// home，于是当家目录路径本身含符号链接（macOS /tmp→/private/tmp、容器里
// /home→/mnt/home、或 HOME 指向符号链接）时，"家目录本身"会被漏判——那种情况下
// env 把下载目录设成家目录就能通过校验，媒体库沙箱根变成整个家目录。
// 这里调用 httpapi 自己两侧都解析的 isHomeDirectory，再叠加 config 的校验。
func unsafeDownloadRoot(dir string) bool {
	resolved, err := resolvePathForCheck(dir)
	if err != nil {
		return false
	}
	if isFilesystemRoot(resolved) || isHomeDirectory(resolved) {
		return true
	}
	return config.ValidateDownloadRoot(dir) != nil
}

// unsafeRootWarnings 让"下载目录不安全"的 warning 每个路径只打一次，
// 避免每次媒体请求都刷屏。
var unsafeRootWarnings sync.Map

// logUnsafeHistoricalDownloadRoot 对不安全的**历史**下载根只警告一次。
func logUnsafeHistoricalDownloadRoot(dir string) {
	if _, loaded := unsafeRootWarnings.LoadOrStore("history:"+dir, struct{}{}); loaded {
		return
	}
	log.Printf("[httpapi][WARNING] download_roots 中存在不可用作媒体沙箱的根（文件系统根或家目录本身）：%q；已忽略该根。", dir)
}

func logUnsafeCurrentDownloadRoot(dir string) {
	if _, loaded := unsafeRootWarnings.LoadOrStore(dir, struct{}{}); loaded {
		return
	}
	log.Printf("[httpapi][WARNING] 当前下载目录不可用作媒体库沙箱（文件系统根或家目录本身）：%q；已忽略该根，历史下载根仍然可用。请修正 OPEN_XDOWNLOAD_DOWNLOAD_DIR 或设置页中的下载目录。", dir)
}

// withinAllowedRoot 判断 path 的真实路径是否位于任一允许根之下（或等于该根）。
// 真实路径解析会跟随已有符号链接，避免通过允许目录中的 symlink 逃逸到根目录之外。
func withinAllowedRoot(path string, roots []string) bool {
	cleaned, err := resolvePathForCheck(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		rootAbs, err := resolvePathForCheck(root)
		if err != nil {
			continue
		}
		if cleaned == rootAbs {
			return true
		}
		rel, err := filepath.Rel(rootAbs, cleaned)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolvePathForCheck 解析 path 中已有部分的符号链接，并把尚不存在的尾部拼回去。
// 这样既能校验已有路径，也能在 MkdirAll 前校验待创建路径不会穿过指向允许根之外的 symlink。
func resolvePathForCheck(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	current := abs
	missing := make([]string, 0)
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("无法解析路径: %s", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func localDirectoryPath(value string, allowedRoots []string) (string, error) {
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
	if !withinAllowedRoot(path, allowedRoots) {
		return "", fmt.Errorf("不允许访问此目录（仅限家目录与下载目录范围）")
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

// validateDownloadDir 校验待保存的下载目录：必须是绝对可解析路径，且位于允许根
// （家目录 / 工作目录 / 当前下载目录）之下。不创建目录——下载流程会按需创建。
func validateDownloadDir(value string, allowedRoots []string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("下载目录不能为空")
	}
	path, err := filepath.Abs(trimmed)
	if err != nil {
		return fmt.Errorf("下载目录无效: %w", err)
	}
	// 下载目录同时是媒体库的沙箱根：设成文件系统根或家目录本身，其下所有图片/视频都会
	// 变成可读的"媒体库文件"。当前下载目录保持不变是允许的。
	if isFilesystemRoot(path) || isHomeDirectory(path) {
		return fmt.Errorf("下载目录不能是文件系统根目录或家目录本身，请选择其中的子目录")
	}
	if !withinAllowedRoot(path, allowedRoots) {
		return fmt.Errorf("下载目录不允许：仅限家目录或当前下载目录范围内；" +
			"如需指向其他位置（例如外置磁盘），请用 OPEN_XDOWNLOAD_DOWNLOAD_DIR 环境变量启动")
	}
	return nil
}

func isFilesystemRoot(path string) bool {
	cleaned := filepath.Clean(path)
	return filepath.Dir(cleaned) == cleaned
}

func isHomeDirectory(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	resolvedHome, err := resolvePathForCheck(home)
	if err != nil {
		return false
	}
	resolved, err := resolvePathForCheck(path)
	return err == nil && resolved == resolvedHome
}

func createLocalDirectoryPath(value string, allowedRoots []string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("目录路径不能为空")
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if !withinAllowedRoot(path, allowedRoots) {
		return "", fmt.Errorf("不允许创建此位置的目录（仅限家目录与下载目录范围）")
	}
	// Resolve symlinks on the existing prefix of the path before creating
	// directories, then re-check the resolved path. This prevents a symlink
	// inside the allowed root from redirecting MkdirAll to a location outside it.
	resolved, err := resolvePathForCheck(path)
	if err != nil {
		return "", err
	}
	if !withinAllowedRoot(resolved, allowedRoots) {
		return "", fmt.Errorf("不允许创建此位置的目录（仅限家目录与下载目录范围）")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 不是目录", path)
	}
	return path, nil
}
