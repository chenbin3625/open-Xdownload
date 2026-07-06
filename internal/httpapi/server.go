package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
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
		AllowedMethods:   []string{"GET", "POST", "PUT", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
	}))

	r.Get("/api/health", s.health)
	r.Get("/api/config", s.getConfig)
	r.Put("/api/config", s.updateConfig)
	r.Post("/api/auth/check", s.checkAuth)
	r.Post("/api/parse/tweet-link", s.parseTweetLink)
	r.Post("/api/download/media", s.createMediaDownload)
	r.Post("/api/jobs", s.createJob)
	r.Get("/api/jobs", s.listJobs)
	r.Get("/api/jobs/{id}", s.getJob)
	r.Post("/api/jobs/{id}/cancel", s.cancelJob)
	r.Post("/api/jobs/{id}/retry", s.retryJob)
	r.Get("/api/events", s.events)
	r.Get("/api/library/downloads", s.listDownloads)
	r.Get("/api/logs", s.listFailedMedia)
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
	if err := decodeJSON(r, &cfg); err != nil {
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

func (s *Server) checkAuth(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": cfg.AuthToken != "" && cfg.CSRFToken != "",
		"message":    "本地已保存 Cookie 字段；真实 X 登录校验将在 xclient 迁移后接入。",
	})
}

func (s *Server) parseTweetLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tweet, err := s.parser.ParseTweetLink(r.Context(), req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, tweet)
}

func (s *Server) createMediaDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if !jobs.IsHTTPURL(req.URL) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请输入有效 HTTP 下载地址"))
		return
	}
	job, err := s.store.CreateJob(r.Context(), storage.JobKindMediaURL, req.URL, jobs.MediaTitle(req.URL))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.manager.Notify()
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind  storage.JobKind `json:"kind"`
		Input string          `json:"input"`
		Title string          `json:"title"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Kind == "" {
		req.Kind = storage.JobKindTweetLink
	}
	if strings.TrimSpace(req.Input) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("任务输入不能为空"))
		return
	}
	job, err := s.store.CreateJob(r.Context(), req.Kind, strings.TrimSpace(req.Input), req.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.manager.Notify()
	writeJSON(w, http.StatusCreated, job)
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
		writeError(w, http.StatusNotFound, err)
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
	job, err := s.store.CancelJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.eventBus.Publish(jobs.Event{Type: "job.updated", JobID: job.ID, Payload: job})
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	channel := s.eventBus.Subscribe()
	defer s.eventBus.Unsubscribe(channel)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-channel:
			_, _ = w.Write(event.MarshalSSE())
			flusher.Flush()
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

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	jobItems, err := s.store.ListJobs(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	downloadItems, err := s.store.ListDownloads(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	failedItems, err := s.store.ListFailedMedia(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, storage.Dashboard{
		Config:    cfg.Redacted(),
		Jobs:      jobItems,
		Downloads: downloadItems,
		Failed:    failedItems,
	})
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
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
