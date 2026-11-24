package xapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestFullTimelineItemsToleratesUnknownInstructionsAndEntries(t *testing.T) {
	payload := fullTweetPage(`[
		{"__typename":"TimelineShowAlert","alertType":"NewTweets"},
		{"__typename":"TimelineFutureExperimentalInstruction"},
		{"__typename":"TimelineAddEntries","entries":[
			{"content":{"__typename":"TimelineTimelinePrompt","header":"Try Grok"}},
			` + fullTweetEntry("205", "42") + `,
			{"content":{"__typename":"TimelineTimelineCursor","cursor_type":"Bottom","value":"next-cur"}}
		]}
	]`)
	items, next, err := FullTimelineItems([]byte(payload))
	if err != nil {
		t.Fatalf("FullTimelineItems should tolerate unknown instructions/entries: %v", err)
	}
	if next != "next-cur" || len(items) != 1 || TweetResultID(items[0].Result) != "205" {
		t.Fatalf("items=%+v next=%q", items, next)
	}

	// Completely unrecognized instruction set must still fail.
	broken := fullTweetPage(`[{"__typename":"TimelineCompletelyUnknown"}]`)
	if _, _, err := FullTimelineItems([]byte(broken)); err == nil {
		t.Fatal("expected error when no instruction is recognized")
	}
}

func TestWalkUserTweetsSkipsSinglePoisonPillAndFailsWhenAllBroken(t *testing.T) {
	t.Run("skips single malformed tweet and continues", func(t *testing.T) {
		brokenEntry := `{"content":{"__typename":"TimelineTimelineItem","content":{"__typename":"TimelineTweet","tweet_results":{"result":{"rest_id":"999"}}}}}`
		payload := fullTweetPage(`[{"__typename":"TimelineAddEntries","entries":[` +
			brokenEntry + `,` + fullTweetEntry("205", "42") +
			`]}]`)
		requester := &fakeRequester{pages: []string{payload}}
		var got []parser.TweetData
		err := WalkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{}, func(page TweetTimelinePage) error {
			got = append(got, page.Tweets...)
			return nil
		})
		if err != nil {
			t.Fatalf("WalkUserTweets aborted on single malformed tweet: %v", err)
		}
		if len(got) != 1 || got[0].ID != "205" {
			t.Fatalf("tweets = %+v, want [205]", got)
		}
	})

	t.Run("fails when every tweet fails to parse without advancing progress", func(t *testing.T) {
		brokenEntry := `{"content":{"__typename":"TimelineTimelineItem","content":{"__typename":"TimelineTweet","tweet_results":{"result":{"rest_id":"999"}}}}}`
		payload := fullTweetPage(`[{"__typename":"TimelineAddEntries","entries":[` + brokenEntry + `]}]`)
		requester := &fakeRequester{pages: []string{payload}}
		saved := false
		err := WalkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{}, func(page TweetTimelinePage) error {
			saved = true
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "全部解析失败") {
			t.Fatalf("err = %v, want all-failed parse error", err)
		}
		if saved {
			t.Fatal("save callback must not run when all tweets failed to parse")
		}
	})
}

func TestWalkUserMediaPagesPageZeroDeletedStopIDAndUntaggedPinned(t *testing.T) {
	makeEntry := func(id string) string {
		return fmt.Sprintf(`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":%q,"core":{"user_results":{"result":{"rest_id":"u1","legacy":{"screen_name":"alice","name":"Alice"}}}},"legacy":{"id_str":%q,"full_text":"hi","extended_entities":{"media":[{"id_str":"m%s","type":"photo","media_url_https":"https://pbs.twimg.com/media/%s.jpg"}]}}}}}}}`, id, id, id, id)
	}
	makePage := func(entries []string, next string) string {
		all := append([]string(nil), entries...)
		if next != "" {
			all = append(all, fmt.Sprintf(`{"content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":%q}}`, next))
		}
		return fmt.Sprintf(`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`, strings.Join(all, ","))
	}

	t.Run("stops on page 0 when StopID was deleted", func(t *testing.T) {
		req := &fakeRequester{pages: []string{
			makePage([]string{makeEntry("120"), makeEntry("90"), makeEntry("80")}, "cursor-page-2"),
			makePage([]string{makeEntry("70")}, ""),
		}}
		tweets, stats, err := GetUserTimelineWithStats(context.Background(), req, User{ID: "u1", ScreenName: "alice"}, parser.ParseOptions{StopAtTweetID: "100"})
		if err != nil {
			t.Fatalf("GetUserTimelineWithStats: %v", err)
		}
		if req.calls != 1 || stats.Pages != 1 {
			t.Fatalf("expected early stop on page 0 without fetching page 1, calls=%d pages=%d", req.calls, stats.Pages)
		}
		if len(tweets) != 1 || tweets[0].ID != "120" {
			t.Fatalf("tweets = %+v, want only [120]", tweets)
		}
	})

	t.Run("continues to page 1 when untagged pinned old tweet precedes newer tweets", func(t *testing.T) {
		req := &fakeRequester{pages: []string{
			makePage([]string{makeEntry("50"), makeEntry("200"), makeEntry("150")}, "cursor-page-2"),
			makePage([]string{makeEntry("120"), makeEntry("90")}, ""),
		}}
		tweets, stats, err := GetUserTimelineWithStats(context.Background(), req, User{ID: "u1", ScreenName: "alice"}, parser.ParseOptions{StopAtTweetID: "100"})
		if err != nil {
			t.Fatalf("GetUserTimelineWithStats: %v", err)
		}
		if req.calls != 2 || stats.Pages != 2 {
			t.Fatalf("expected 2 pages when untagged pinned old tweet precedes newer posts, calls=%d pages=%d", req.calls, stats.Pages)
		}
		if len(tweets) < 3 {
			t.Fatalf("tweets = %+v, want newer tweets 200, 150, 120 kept", tweets)
		}
		gotIDs := map[string]bool{}
		for _, tw := range tweets {
			gotIDs[tw.ID] = true
		}
		for _, wantID := range []string{"200", "150", "120"} {
			if !gotIDs[wantID] {
				t.Fatalf("missing newer tweet %s in %+v", wantID, tweets)
			}
		}
	})
}
