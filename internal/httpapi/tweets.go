package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/go-chi/chi/v5"
)

func (s *Server) listTweets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := storage.TweetFilter{Search: q.Get("search"), Author: q.Get("author"), MediaType: q.Get("mediaType"), ID: q.Get("id"), Page: parsePositiveInt(r, "page", 1, 1, 1000000), PageSize: parsePositiveInt(r, "pageSize", 20, 1, 100)}
	for key, target := range map[string]*string{"from": &filter.From, "to": &filter.To} {
		if value := q.Get(key); value != "" {
			date, err := time.Parse("2006-01-02", value)
			if err != nil {
				writeError(w, 400, storage.ErrValidation("无效的日期"))
				return
			}
			if key == "to" {
				date = date.AddDate(0, 0, 1)
			}
			*target = date.Format(time.RFC3339)
		}
	}
	filter.JobID, _ = strconv.ParseInt(q.Get("jobId"), 10, 64)
	page, err := s.store.ListTweets(r.Context(), filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	for i := range page.Items {
		s.enrichDownloads(r, page.Items[i].Downloads)
	}
	writeJSON(w, 200, page)
}

func (s *Server) enrichDownloads(r *http.Request, records []storage.DownloadRecord) {
	for i := range records {
		records[i].FileURL = "/api/library/downloads/" + strconv.FormatInt(records[i].ID, 10) + "/file"
	}
	s.enrichAssetURLs(r, records)
}

func (s *Server) getTweet(w http.ResponseWriter, r *http.Request) {
	tweet, err := s.store.GetTweet(r.Context(), chi.URLParam(r, "tweetID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, storage.ErrNotFound("推文未归档", err))
		} else {
			writeError(w, 500, err)
		}
		return
	}
	s.enrichDownloads(r, tweet.Downloads)
	writeJSON(w, 200, tweet)
}

func (s *Server) tweetProgress(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListTweetSync(r.Context())
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) retryTweetMedia(w http.ResponseWriter, r *http.Request) {
	tweet, err := s.store.GetTweet(r.Context(), chi.URLParam(r, "tweetID"))
	if err != nil {
		writeError(w, 404, storage.ErrNotFound("推文未归档", err))
		return
	}
	job, err := s.store.RetryTweetMedia(r.Context(), tweet.ID)
	if err != nil {
		if errors.Is(err, storage.ErrJobRetryBusy) {
			writeError(w, http.StatusConflict, &storage.UserFacingError{Status: http.StatusConflict, Message: "媒体重试任务已在队列中"})
			return
		}
		writeError(w, 500, err)
		return
	}
	s.manager.Notify()
	s.publish(s.withMeta(r.Context(), jobs.Event{Type: "job.created", JobID: job.ID, Payload: job}))
	writeJSON(w, 201, job)
}
