package xapi

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

type stubRequester func(ctx context.Context, path string, values url.Values) ([]byte, error)

func (f stubRequester) GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return f(ctx, path, values)
}

func TestWalkBookmarksSearchLikesAndThread(t *testing.T) {
	ctx := context.Background()

	// Bookmarks
	bmReq := stubRequester(func(_ context.Context, path string, _ url.Values) ([]byte, error) {
		if !strings.Contains(path, "Bookmarks") {
			t.Fatalf("unexpected path %s", path)
		}
		return []byte(`{"data":{"bookmark_timeline_v2":{"timeline":{"instructions":[{"__typename":"TimelineAddEntries","entries":[
			{"content":{"__typename":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"501","core":{"user_results":{"result":{"rest_id":"u1","core":{"name":"Alice","screen_name":"alice"}}}},"legacy":{"full_text":"Bookmarked post","created_at":"Tue Nov 10 12:00:00 +0000 2026"}}}}}}
		]}]}}}}`), nil
	})
	var bmTweets []parser.TweetData
	if err := WalkBookmarks(ctx, bmReq, "all", TweetTimelineOptions{}, func(p TweetTimelinePage) error {
		bmTweets = append(bmTweets, p.Tweets...)
		return nil
	}); err != nil || len(bmTweets) != 1 || bmTweets[0].ID != "501" {
		t.Fatalf("WalkBookmarks failed: %+v err=%v", bmTweets, err)
	}

	// SearchTimeline
	searchReq := stubRequester(func(_ context.Context, path string, values url.Values) ([]byte, error) {
		if !strings.Contains(path, "SearchTimeline") || !strings.Contains(values.Get("variables"), "#golang") {
			t.Fatalf("unexpected search request: %s %s", path, values.Get("variables"))
		}
		return []byte(`{"data":{"search_by_raw_query":{"search_timeline":{"timeline":{"instructions":[{"__typename":"TimelineAddEntries","entries":[
			{"content":{"__typename":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"601","core":{"user_results":{"result":{"rest_id":"u2","core":{"name":"Bob","screen_name":"bob"}}}},"legacy":{"full_text":"Love #golang","created_at":"Tue Nov 10 13:00:00 +0000 2026"}}}}}}
		]}]}}}}}`), nil
	})
	var searchTweets []parser.TweetData
	if err := WalkSearchTimeline(ctx, searchReq, "#golang", "Latest", TweetTimelineOptions{}, func(p TweetTimelinePage) error {
		searchTweets = append(searchTweets, p.Tweets...)
		return nil
	}); err != nil || len(searchTweets) != 1 || searchTweets[0].ID != "601" {
		t.Fatalf("WalkSearchTimeline failed: %+v err=%v", searchTweets, err)
	}

	// TweetDetail thread (author self-reply filter vs includeReplies)
	threadReq := stubRequester(func(_ context.Context, path string, _ url.Values) ([]byte, error) {
		if !strings.Contains(path, "TweetDetail") {
			t.Fatalf("unexpected path %s", path)
		}
		return []byte(`{"data":{"threaded_conversation_with_injections_v2":{"instructions":[{"__typename":"TimelineAddEntries","entries":[
			{"content":{"__typename":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"701","core":{"user_results":{"result":{"rest_id":"u1","core":{"name":"Alice","screen_name":"alice"}}}},"legacy":{"full_text":"Thread 1/2","conversation_id_str":"701","created_at":"Tue Nov 10 12:00:00 +0000 2026"}}}}}},
			{"content":{"__typename":"TimelineTimelineModule","items":[
				{"item":{"itemContent":{"tweet_results":{"result":{"rest_id":"702","core":{"user_results":{"result":{"rest_id":"u1","core":{"name":"Alice","screen_name":"alice"}}}},"legacy":{"full_text":"Thread 2/2","conversation_id_str":"701","in_reply_to_status_id_str":"701","created_at":"Tue Nov 10 12:01:00 +0000 2026"}}}}}},
				{"item":{"itemContent":{"tweet_results":{"result":{"rest_id":"703","core":{"user_results":{"result":{"rest_id":"u2","core":{"name":"Bob","screen_name":"bob"}}}},"legacy":{"full_text":"Nice!","conversation_id_str":"701","in_reply_to_status_id_str":"701","created_at":"Tue Nov 10 12:02:00 +0000 2026"}}}}}}
			]}}
		]}]}}}`), nil
	})
	authorOnly, err := FetchTweetThread(ctx, threadReq, "701", false, parser.ParseOptions{})
	if err != nil || len(authorOnly) != 2 || authorOnly[0].ID != "701" || authorOnly[1].ID != "702" {
		t.Fatalf("author-only thread failed: %+v err=%v", authorOnly, err)
	}
	withReplies, err := FetchTweetThread(ctx, threadReq, "701", true, parser.ParseOptions{})
	if err != nil || len(withReplies) != 3 {
		t.Fatalf("withReplies thread failed: %+v err=%v", withReplies, err)
	}
}
