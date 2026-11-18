package xapi

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestUserMediaTimelineParseFailureAccounting(t *testing.T) {
	for _, tc := range []struct {
		name       string
		results    []string
		stopID     string
		wantError  bool
		skippedIDs []string
	}{
		{
			name:       "all malformed with IDs",
			results:    []string{`{"rest_id":"300"}`, `{"rest_id":"299"}`},
			wantError:  true,
			skippedIDs: []string{"300", "299"},
		},
		{
			name:       "all malformed without IDs",
			results:    []string{`{"__typename":"Tweet"}`, `{"__typename":"Tweet"}`},
			wantError:  true,
			skippedIDs: []string{"", ""},
		},
		{
			name:       "duplicate malformed tweet counts once",
			results:    []string{`{"rest_id":"300"}`, `{"rest_id":"300"}`},
			wantError:  true,
			skippedIDs: []string{"300"},
		},
		{
			name:       "incremental boundary is not a successful parse",
			results:    []string{`{"rest_id":"300"}`, `{"rest_id":"200","legacy":{"full_text":"already archived"}}`},
			stopID:     "200",
			wantError:  true,
			skippedIDs: []string{"300"},
		},
		{
			name:       "valid text without media is not a parse failure",
			results:    []string{`{"rest_id":"300"}`, `{"rest_id":"299","legacy":{"full_text":"text"}}`},
			skippedIDs: []string{"300"},
		},
		{
			name:       "unavailable tweet is not a malformed tweet",
			results:    []string{`{"rest_id":"300"}`, `{"rest_id":"299","__typename":"TweetUnavailable"}`},
			skippedIDs: []string{"300"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := make([]string, 0, len(tc.results))
			for _, result := range tc.results {
				entries = append(entries, fmt.Sprintf(`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":%s}}}}`, result))
			}
			payload := fmt.Sprintf(`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`, strings.Join(entries, ","))
			requester := &fakeRequester{pages: []string{payload}}

			tweets, stats, err := GetUserTimelineWithStats(context.Background(), requester,
				User{ID: "u1", ScreenName: "owner"}, parser.ParseOptions{StopAtTweetID: tc.stopID})

			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "全部解析失败") {
					t.Fatalf("all malformed candidates must fail, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("valid non-media or unavailable tweet must be tolerated: %v", err)
			}
			if len(tweets) != 0 {
				t.Fatalf("unexpected media tweets: %+v", tweets)
			}
			if stats.SkippedTweets != len(tc.skippedIDs) || !reflect.DeepEqual(stats.SkippedTweetIDs, tc.skippedIDs) {
				t.Errorf("incorrect skipped tweet accounting: %+v, want IDs %v", stats, tc.skippedIDs)
			}
			if requester.calls != 1 || stats.Requests != 1 || stats.Pages != 1 {
				t.Errorf("unexpected pagination: calls=%d stats=%+v", requester.calls, stats)
			}
		})
	}
}
