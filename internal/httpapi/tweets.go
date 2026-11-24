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

func parseTweetFilterFromRequest(r *http.Request) (storage.TweetFilter, error) {
	q := r.URL.Query()
	filter := storage.TweetFilter{
		Search:         q.Get("search"),
		Author:         q.Get("author"),
		MediaType:      q.Get("mediaType"),
		ID:             q.Get("id"),
		ConversationID: q.Get("conversationId"),
		Page:           parsePositiveInt(r, "page", 1, 1, 1000000),
		PageSize:       parsePositiveInt(r, "pageSize", 20, 1, 100),
	}
	filter.Sort = q.Get("sort")
	switch filter.Sort {
	case "", "desc", "asc", "likes_desc", "retweets_desc", "views_desc":
	default:
		return filter, storage.ErrValidation("无效的排序方式")
	}
	filter.Retweets = q.Get("retweets")
	if filter.Retweets != "" && filter.Retweets != "exclude" && filter.Retweets != "only" {
		return filter, storage.ErrValidation("无效的转推筛选")
	}
	var fromDate, toDate time.Time
	for key, target := range map[string]*time.Time{"from": &fromDate, "to": &toDate} {
		if value := q.Get(key); value != "" {
			date, err := time.Parse("2006-01-02", value)
			if err != nil {
				return filter, storage.ErrValidation("无效的日期")
			}
			*target = date
		}
	}
	if !fromDate.IsZero() && !toDate.IsZero() && fromDate.After(toDate) {
		return filter, storage.ErrValidation("开始日期不能晚于结束日期")
	}
	if !fromDate.IsZero() {
		filter.From = fromDate.Format(time.RFC3339)
	}
	if !toDate.IsZero() {
		filter.To = toDate.AddDate(0, 0, 1).Format(time.RFC3339)
	}
	if rawJobID := q.Get("jobId"); rawJobID != "" {
		jobID, err := strconv.ParseInt(rawJobID, 10, 64)
		if err != nil || jobID <= 0 {
			return filter, storage.ErrValidation("无效的任务 ID")
		}
		filter.JobID = jobID
	}
	if rawStarred := q.Get("starred"); rawStarred != "" {
		v := rawStarred == "1" || rawStarred == "true"
		filter.Starred = &v
	}
	if rawTag := q.Get("tagId"); rawTag != "" {
		if tagID, err := strconv.ParseInt(rawTag, 10, 64); err == nil && tagID > 0 {
			filter.TagID = tagID
		}
	}
	if rawCol := q.Get("collectionId"); rawCol != "" {
		if colID, err := strconv.ParseInt(rawCol, 10, 64); err == nil && colID > 0 {
			filter.CollectionID = colID
		}
	}
	return filter, nil
}

func (s *Server) listTweets(w http.ResponseWriter, r *http.Request) {
	filter, err := parseTweetFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	page, err := s.store.ListTweets(r.Context(), filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	s.enrichTweetsDownloads(r, page.Items)
	writeJSON(w, 200, page)
}

func (s *Server) listTweetAuthors(w http.ResponseWriter, r *http.Request) {
	authors, err := s.store.ListTweetAuthors(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, authors)
}

func (s *Server) enrichDownloads(r *http.Request, records []storage.DownloadRecord) {
	for i := range records {
		records[i].FileURL = "/api/library/downloads/" + strconv.FormatInt(records[i].ID, 10) + "/file"
	}
	s.enrichAssetURLs(r, records)
}

func (s *Server) enrichTweetsDownloads(r *http.Request, tweets []storage.ArchivedTweet) {
	var ids []int64
	for i := range tweets {
		for j := range tweets[i].Downloads {
			rec := &tweets[i].Downloads[j]
			rec.FileURL = "/api/library/downloads/" + strconv.FormatInt(rec.ID, 10) + "/file"
			if rec.ID > 0 {
				ids = append(ids, rec.ID)
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	assetsByID, err := s.store.DownloadAssetsBatch(r.Context(), ids)
	if err != nil {
		return
	}
	for i := range tweets {
		if len(tweets[i].Downloads) > 0 {
			applyAssetURLs(tweets[i].Downloads, assetsByID)
		}
	}
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
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, storage.ErrNotFound("推文未归档", err))
		} else {
			writeError(w, 500, err)
		}
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
