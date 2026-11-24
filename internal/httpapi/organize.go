package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/go-chi/chi/v5"
)

func (s *Server) getTweetThread(w http.ResponseWriter, r *http.Request) {
	tweetID := chi.URLParam(r, "tweetID")
	items, err := s.store.ListConversationTweets(r.Context(), tweetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, storage.ErrNotFound("推文未归档", err))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.enrichTweetsDownloads(r, items)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deleteDownload(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deleteFiles := r.URL.Query().Get("deleteFiles") == "1" || r.URL.Query().Get("deleteFiles") == "true"
	res, err := s.store.DeleteDownloads(r.Context(), []int64{id}, deleteFiles)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) batchDeleteDownloads(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs         []int64 `json:"ids"`
		DeleteFiles bool    `json:"deleteFiles"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 500 {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("一次可删除 1-500 条媒体记录"))
		return
	}
	res, err := s.store.DeleteDownloads(r.Context(), req.IDs, req.DeleteFiles)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) deleteTweet(w http.ResponseWriter, r *http.Request) {
	tweetID := chi.URLParam(r, "tweetID")
	deleteFiles := r.URL.Query().Get("deleteFiles") == "1" || r.URL.Query().Get("deleteFiles") == "true"
	res, err := s.store.DeleteTweets(r.Context(), []string{tweetID}, deleteFiles)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) batchDeleteTweets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs         []string `json:"ids"`
		DeleteFiles bool     `json:"deleteFiles"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 500 {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("一次可删除 1-500 条推文"))
		return
	}
	res, err := s.store.DeleteTweets(r.Context(), req.IDs, req.DeleteFiles)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) setTweetStar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Starred bool `json:"starred"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetTweetStarred(r.Context(), chi.URLParam(r, "tweetID"), req.Starred); err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"starred": req.Starred})
}

func (s *Server) setDownloadStar(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		Starred bool `json:"starred"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetDownloadStarred(r.Context(), id, req.Starred); err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"starred": req.Starred})
}

func (s *Server) listTags(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListTags(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createTag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := s.store.CreateTag(r.Context(), req.Name, req.Color)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateTag(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := s.store.UpdateTag(r.Context(), id, req.Name, req.Color)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteTag(r.Context(), id); err != nil {
		writeStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setTweetTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TagIDs []int64 `json:"tagIds"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tags, err := s.store.SetTweetTags(r.Context(), chi.URLParam(r, "tweetID"), req.TagIDs)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func (s *Server) setDownloadTags(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		TagIDs []int64 `json:"tagIds"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tags, err := s.store.SetDownloadTags(r.Context(), id, req.TagIDs)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func (s *Server) listCollections(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListCollections(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createCollection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := s.store.CreateCollection(r.Context(), req.Name, req.Description)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateCollection(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := s.store.UpdateCollection(r.Context(), id, req.Name, req.Description)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteCollection(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteCollection(r.Context(), id); err != nil {
		writeStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addCollectionItems(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		ItemType string   `json:"itemType"`
		ItemIDs  []string `json:"itemIds"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.AddCollectionItems(r.Context(), id, req.ItemType, req.ItemIDs); err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) removeCollectionItems(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		ItemType string   `json:"itemType"`
		ItemIDs  []string `json:"itemIds"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.RemoveCollectionItems(r.Context(), id, req.ItemType, req.ItemIDs); err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
