package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

const bootstrapScriptID = "app-bootstrap"

type appBootstrap struct {
	Jobs      *jobListPage              `json:"jobs,omitempty"`
	Meta      *dashboardMeta            `json:"meta,omitempty"`
	Schedules []storage.ArchiveSchedule `json:"schedules,omitempty"`
	Config    *config.AppConfig         `json:"config,omitempty"`
}

func includeWorkbenchBootstrap(path string) bool {
	return strings.TrimSuffix(path, "/") != "/settings"
}

func parseWorkbenchPage(r *http.Request) (page int, pageSize int) {
	page = parsePositiveInt(r, "page", 1, 1, 1000000)
	pageSize = parsePositiveInt(r, "pageSize", 20, 1, 100)
	switch pageSize {
	case 10, 20, 50, 100:
	default:
		pageSize = 20
	}
	return page, pageSize
}

func (s *Server) loadWorkbenchBootstrap(ctx context.Context, page, pageSize int) (jobListPage, dashboardMeta, []storage.ArchiveSchedule, error) {
	items, err := s.store.ListJobsPage(ctx, pageSize, (page-1)*pageSize)
	if err != nil {
		return jobListPage{}, dashboardMeta{}, nil, err
	}
	if items == nil {
		items = []storage.Job{}
	}
	stats, failedTweetCount, err := s.store.DashboardMeta(ctx)
	if err != nil {
		return jobListPage{}, dashboardMeta{}, nil, err
	}
	schedules, err := s.store.ListArchiveSchedules(ctx)
	if err != nil {
		return jobListPage{}, dashboardMeta{}, nil, err
	}
	if schedules == nil {
		schedules = []storage.ArchiveSchedule{}
	}
	return jobListPage{Items: items, Page: page, PageSize: pageSize},
		dashboardMeta{Stats: stats, FailedTweetCount: failedTweetCount},
		schedules,
		nil
}

func (s *Server) loadAppBootstrap(ctx context.Context, path string, page, pageSize int) (appBootstrap, bool) {
	payload := appBootstrap{}
	loaded := false
	if cfg, err := s.store.GetConfig(ctx); err != nil {
		log.Printf("bootstrap config: %v", err)
	} else {
		redacted := cfg.Redacted()
		payload.Config = &redacted
		loaded = true
	}
	if includeWorkbenchBootstrap(path) {
		jobs, meta, schedules, err := s.loadWorkbenchBootstrap(ctx, page, pageSize)
		if err != nil {
			log.Printf("bootstrap workbench: %v", err)
		} else {
			payload.Jobs = &jobs
			payload.Meta = &meta
			payload.Schedules = schedules
			loaded = true
		}
	}
	return payload, loaded
}

func injectBootstrapHTML(html []byte, payload any) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(payload); err != nil {
		return html
	}
	jsonBytes := bytes.TrimSpace(buf.Bytes())
	snippet := make([]byte, 0, 64+len(jsonBytes))
	snippet = append(snippet, `<script type="application/json" id="`+bootstrapScriptID+`">`...)
	snippet = append(snippet, jsonBytes...)
	snippet = append(snippet, "</script>"...)
	if index := bytes.Index(html, []byte("</head>")); index >= 0 {
		out := make([]byte, 0, len(html)+len(snippet))
		out = append(out, html[:index]...)
		out = append(out, snippet...)
		out = append(out, html[index:]...)
		return out
	}
	return append(snippet, html...)
}

// InjectIndexHTML embeds first-paint JSON into index.html so the SPA can
// hydrate without waiting on /api/jobs, /api/dashboard/meta, or /api/config.
func (s *Server) InjectIndexHTML(r *http.Request, html []byte) []byte {
	if s == nil || s.store == nil {
		return html
	}
	page, pageSize := parseWorkbenchPage(r)
	payload, ok := s.loadAppBootstrap(r.Context(), r.URL.Path, page, pageSize)
	if !ok {
		return html
	}
	return injectBootstrapHTML(html, payload)
}
