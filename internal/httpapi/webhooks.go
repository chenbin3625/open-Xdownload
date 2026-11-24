package httpapi

import (
	"net/http"

	"github.com/chenbin3625/open-Xdownload/internal/notify"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

// SetNotifier 注入外部通知派发器（供测试发送及账号检测失败告警使用）。
func (s *Server) SetNotifier(dispatcher *notify.Dispatcher) {
	s.notifier = dispatcher
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListWebhooks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]storage.Webhook, len(items))
	for i, item := range items {
		out[i] = item.Redacted()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	var req storage.Webhook
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := s.store.CreateWebhook(r.Context(), req)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created.Redacted())
}

func (s *Server) updateWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req storage.Webhook
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := s.store.UpdateWebhook(r.Context(), id, req)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Redacted())
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteWebhook(r.Context(), id); err != nil {
		writeStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	wh, err := s.store.GetWebhook(r.Context(), id)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	dispatcher := s.notifier
	if dispatcher == nil {
		dispatcher = notify.NewDispatcher(s.store)
		defer dispatcher.Close()
	}
	if err := dispatcher.SendTest(r.Context(), wh); err != nil {
		writeError(w, http.StatusBadGateway, storage.ErrValidation("测试推送失败: "+err.Error()))
		return
	}
	updated, _ := s.store.GetWebhook(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "webhook": updated.Redacted()})
}
