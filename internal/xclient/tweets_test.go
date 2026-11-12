package xclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestPublicTweetRequestOmitsEmptyCookies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "Bearer "+guestBearer {
			t.Errorf("public request must omit empty cookies and retain bearer authentication")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	client.authToken, client.csrfToken = "", ""
	if _, err := client.graphQL(context.Background(), "/graphql/test/TweetResultByRestId", url.Values{"variables": {"{}"}}); err != nil {
		t.Fatal(err)
	}
}

func fullTweetFixture(id, author string) string {
	return fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"legacy":{"full_text":"pure text","created_at":"Tue Nov 10 12:00:00 +0000 2026"},"core":{"user_results":{"result":{"rest_id":%q,"core":{"screen_name":"author","name":"Author"}}}}}`, id, author)
}

func fullTweetEntry(id, author string) string {
	return `{"content":{"__typename":"TimelineTimelineItem","content":{"__typename":"TimelineTweet","tweet_results":{"result":` + fullTweetFixture(id, author) + `}}}}`
}

func fullTweetPage(instructions string) string {
	return `{"data":{"user_result_by_screen_name":{"result":{"profile_with_replies_timeline_v2":{"timeline":{"instructions":` + instructions + `}}}}}}`
}

func TestFullTweetTimelinePinsTextAndIndependentCheckpoint(t *testing.T) {
	requester := &fakeTimelineRequester{pages: []string{fullTweetPage(`[{"__typename":"TimelinePinEntry","entry":` + fullTweetEntry("20", "42") + `},{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("103", "42") + `,` + fullTweetEntry("102", "42") + `,` + fullTweetEntry("104", "other") + `,{"content":{"__typename":"TimelineTimelineCursor","cursor_type":"Bottom","value":"page-2"}}]}]`), fullTweetPage(`[{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("101", "42") + `,` + fullTweetEntry("100", "42") + `]}]`)}}
	var pages []TweetTimelinePage
	err := walkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{StopID: "100", Parse: parser.ParseOptions{RequireRelated: true}}, func(page TweetTimelinePage) error { pages = append(pages, page); return nil })
	if err != nil || len(pages) != 2 || len(pages[0].Tweets) != 2 || pages[0].Cursor != "page-2" || pages[0].Complete || pages[1].HeadID != "103" || !pages[1].Complete || len(pages[1].Tweets) != 1 {
		t.Fatalf("timeline: %+v %v", pages, err)
	}
	if len(pages[0].Tweets[0].Media) != 0 || pages[0].Tweets[0].Text != "pure text" {
		t.Fatal("text-only tweet was omitted")
	}
}

func TestFullTweetTimelineRejectsMissingSchemaAndSaveFailure(t *testing.T) {
	for _, payload := range []string{`{"data":{"user_result_by_screen_name":{"result":{}}}}`, fullTweetPage(`[{"__typename":"UnexpectedInstruction"}]`)} {
		requester := &fakeTimelineRequester{pages: []string{payload}}
		called := false
		err := walkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{}, func(TweetTimelinePage) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("missing schema advanced progress: %v", err)
		}
	}
	requester := &fakeTimelineRequester{pages: []string{fullTweetPage(`[{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("103", "42") + `,{"content":{"__typename":"TimelineTimelineCursor","cursor_type":"Bottom","value":"next"}}]}]`)}}
	err := walkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{Cursor: "resumed"}, func(TweetTimelinePage) error { return fmt.Errorf("database unavailable") })
	if err == nil || !strings.Contains(err.Error(), "database unavailable") || requester.calls != 1 {
		t.Fatalf("failed checkpoint continued: %v calls=%d", err, requester.calls)
	}
}
