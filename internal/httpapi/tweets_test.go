package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestTweetRetweetFiltersApplyBeforePagination(t *testing.T) {
	store, handler, _ := newLibraryTestServer(t)
	ctx := context.Background()
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "owner", ArchiveMode: storage.ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	original := parser.TweetData{ID: "100", Text: "original body", Author: parser.Author{ID: "1", ScreenName: "source"}, CreatedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	for index, id := range []string{"100", "101", "102", "200", "201"} {
		tweet := parser.TweetData{ID: id, Text: "archived body", Author: parser.Author{ID: "2", ScreenName: "owner"}, CreatedAt: original.CreatedAt.Add(time.Duration(index) * time.Hour)}
		switch id {
		case "100":
			tweet = original
		case "101":
			tweet.QuotedTweetID, tweet.Related = original.ID, []parser.TweetData{original}
		case "102":
			tweet.Text = "RT @source: manually written body"
		default:
			tweet.RetweetedTweetID, tweet.Related = original.ID, []parser.TweetData{original}
		}
		if err := store.ArchiveTweet(ctx, job.ID, tweet, "not_requested"); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, query, id string
		total           int
	}{
		{"all", "", "201", 5},
		{"exclude", "retweets=exclude", "102", 3},
		{"exclude second page", "retweets=exclude&page=2", "101", 3},
		{"only", "retweets=only", "201", 2},
		{"only second page", "retweets=only&page=2", "200", 2},
		{"combined filters", "retweets=exclude&author=owner&search=archived&sort=asc", "101", 1},
		{"job filter", "retweets=exclude&jobId=" + strconv.FormatInt(job.ID, 10), "102", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, newLoopbackRequest(http.MethodGet, "/api/archive/tweets?pageSize=1&"+test.query, nil))
			var page storage.TweetPage
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil {
				t.Fatalf("list tweets: %d %s", response.Code, response.Body.String())
			}
			if page.Pagination.Total != test.total || len(page.Items) != 1 || page.Items[0].ID != test.id {
				t.Fatalf("filter %q: %+v", test.query, page)
			}
		})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newLoopbackRequest(http.MethodGet, "/api/archive/tweets?retweets=invalid", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid retweet filter: %d %s", response.Code, response.Body.String())
	}
}
