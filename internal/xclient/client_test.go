package xclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/tidwall/gjson"
)

func TestTweetResultIDUnwrapsVisibilityResultForStopAt(t *testing.T) {
	result := gjson.Parse(`{
		"__typename": "TweetWithVisibilityResults",
		"tweet": {
			"rest_id": "12345",
			"legacy": {}
		}
	}`)
	if got := tweetResultID(result); got != "12345" {
		t.Fatalf("tweetResultID() = %q, want 12345", got)
	}
	if !shouldStopAt(tweetResultID(result), "12345", 0) {
		t.Fatal("wrapped stop tweet did not trigger exact-match early stop")
	}
}

func TestClientRetriesTransientFailuresUntilFifthAttempt(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if attempt < requestMaxAttempts {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	client := &Client{
		http:         server.Client(),
		baseURL:      server.URL,
		retryBackoff: func(int) time.Duration { return 0 },
	}
	payload, err := client.graphQL(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("graphQL: %v", err)
	}
	if requests.Load() != requestMaxAttempts {
		t.Fatalf("requests = %d, want %d", requests.Load(), requestMaxAttempts)
	}
	if string(payload) != `{"data":{}}` {
		t.Fatalf("payload = %q", payload)
	}
}

func TestPoolGetUserByInputFallsBackToAnotherCookie(t *testing.T) {
	var primaryRequests atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryRequests.Add(1)
		http.Error(w, "try again", http.StatusServiceUnavailable)
	}))
	defer primary.Close()

	var backupRequests atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupRequests.Add(1)
		_, _ = w.Write([]byte(`{"data":{"user":{"result":{"rest_id":"user-1","legacy":{"name":"Owner","screen_name":"owner"}}}}}`))
	}))
	defer backup.Close()

	pool := &Pool{clients: []*Client{
		{http: primary.Client(), baseURL: primary.URL, retryBackoff: func(int) time.Duration { return 0 }},
		{http: backup.Client(), baseURL: backup.URL, retryBackoff: func(int) time.Duration { return 0 }},
	}}
	user, err := pool.GetUserByInput(context.Background(), "owner")
	if err != nil {
		t.Fatalf("get user by input: %v", err)
	}
	if user.ID != "user-1" || user.ScreenName != "owner" {
		t.Fatalf("user = %+v, want backup response", user)
	}
	if primaryRequests.Load() != requestMaxAttempts || backupRequests.Load() != 1 {
		t.Fatalf("requests primary=%d backup=%d, want primary=%d backup=1", primaryRequests.Load(), backupRequests.Load(), requestMaxAttempts)
	}
}

func TestRequestRetryDelayUsesCappedExponentialBackoff(t *testing.T) {
	want := []time.Duration{750 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second, 6 * time.Second, 6 * time.Second}
	for attempt, expected := range want {
		if got := requestRetryDelay(attempt); got != expected {
			t.Fatalf("attempt %d delay = %s, want %s", attempt, got, expected)
		}
	}
}

func TestParseAdditionalCookiesAcceptsCommonFormats(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []Credentials
	}{
		{
			name: "json",
			raw:  `[{"auth_token":"auth-a","ct0":"csrf-a"},{"authToken":"auth-b","csrfToken":"csrf-b"}]`,
			want: []Credentials{
				{AuthToken: "auth-a", CSRFToken: "csrf-a"},
				{AuthToken: "auth-b", CSRFToken: "csrf-b"},
			},
		},
		{
			name: "cookie header",
			raw:  "auth_token=auth-a; ct0=csrf-a\nauth_token=auth-b; ct0=csrf-b",
			want: []Credentials{
				{AuthToken: "auth-a", CSRFToken: "csrf-a"},
				{AuthToken: "auth-b", CSRFToken: "csrf-b"},
			},
		},
		{
			name: "yaml",
			raw:  "- auth_token: auth-a\n  ct0: csrf-a\n- auth_token: auth-b\n  ct0: csrf-b",
			want: []Credentials{
				{AuthToken: "auth-a", CSRFToken: "csrf-a"},
				{AuthToken: "auth-b", CSRFToken: "csrf-b"},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAdditionalCookies(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d: %#v", len(got), len(tt.want), got)
			}
			for index := range tt.want {
				if got[index] != tt.want[index] {
					t.Fatalf("credential %d = %#v, want %#v", index, got[index], tt.want[index])
				}
			}
		})
	}
}

func TestLimitedErrorPayloadTruncatesLargeResponses(t *testing.T) {
	payload := []byte(strings.Repeat("a", maxErrorPayloadBytes+100))
	got := limitedErrorPayload(payload)
	if len(got) >= len(payload) {
		t.Fatalf("payload was not truncated: got length %d, original %d", len(got), len(payload))
	}
	if !strings.Contains(got, "truncated 100 bytes") {
		t.Fatalf("truncation marker missing from %q", got[len(got)-80:])
	}
}

func TestPoolSelectPrefersUnblockedClient(t *testing.T) {
	now := time.Now()
	blockedLimiter := newRateLimiter()
	blockedLimiter.limits["/x"] = rateLimitState{remaining: 0, limit: 20, reset: now.Add(time.Hour), ready: true}

	freeLimiter := newRateLimiter()
	pool := &Pool{clients: []*Client{
		{limiter: blockedLimiter, retryBackoff: func(int) time.Duration { return 0 }},
		{limiter: freeLimiter, retryBackoff: func(int) time.Duration { return 0 }},
	}}
	client, err := pool.Select(context.Background(), "/x")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if client.limiter != freeLimiter {
		t.Fatal("Select returned the blocked client; want the unblocked one")
	}
}

func TestRateLimiterBlockedResetReflectsSoonest(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter()
	limiter.limits["/a"] = rateLimitState{remaining: 0, limit: 20, reset: now.Add(10 * time.Second), ready: true}
	limiter.limits["/b"] = rateLimitState{remaining: 0, limit: 20, reset: now.Add(30 * time.Second), ready: true}
	limiter.limits["/unblocked"] = rateLimitState{remaining: 20, limit: 20, reset: now.Add(time.Minute), ready: true}

	reset, ok := limiter.blockedReset()
	if !ok {
		t.Fatal("blockedReset = not ok, want ok")
	}
	want := now.Add(15 * time.Second) // reset(10s) + 5s 裕量
	if delta := reset.Sub(want); delta < -time.Second || delta > time.Second {
		t.Fatalf("blockedReset = %s (delta %s), want ~+15s", reset.Format(time.RFC3339), delta)
	}
}

func TestPoolEarliestBlockedWaitUsesSoonestClient(t *testing.T) {
	now := time.Now()
	mk := func(offset time.Duration) *Client {
		limiter := newRateLimiter()
		limiter.limits["/x"] = rateLimitState{remaining: 0, limit: 20, reset: now.Add(offset), ready: true}
		return &Client{limiter: limiter, retryBackoff: func(int) time.Duration { return 0 }}
	}
	pool := &Pool{clients: []*Client{mk(20 * time.Second), mk(5 * time.Second)}}
	wait := pool.earliestBlockedWait()
	if wait <= 0 || wait > 11*time.Second {
		t.Fatalf("earliestBlockedWait = %s, want ~5s(+5s margin)", wait)
	}
}

func TestPoolSelectErrorsWhenAllClientsDisabled(t *testing.T) {
	pool := &Pool{clients: []*Client{
		{disabled: true},
		{disabled: true},
	}}
	if _, err := pool.Select(context.Background(), "/x"); err == nil {
		t.Fatal("Select with all disabled should error")
	}
}

// fakeTimelineRequester 依次返回预置的 timeline 页面，记录请求次数。
type fakeTimelineRequester struct {
	pages []string
	calls int
}

func (f *fakeTimelineRequester) graphQL(_ context.Context, _ string, _ url.Values) ([]byte, error) {
	if f.calls >= len(f.pages) {
		return []byte(`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`), nil
	}
	page := f.pages[f.calls]
	f.calls++
	return []byte(page), nil
}

// timelinePagePayload 构造一页 UserMedia timeline：按给定推文 ID 生成带图片媒体的条目，
// 可选附带 Bottom 游标。
func timelinePagePayload(tweetIDs []string, cursor string) string {
	entries := make([]string, 0, len(tweetIDs)+1)
	for _, id := range tweetIDs {
		entries = append(entries, fmt.Sprintf(
			`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":%q,"legacy":{"full_text":"tweet %s","extended_entities":{"media":[{"id_str":"m-%s","type":"photo","media_url_https":"https://pbs.twimg.com/media/%s.jpg"}]}}}}}}}`,
			id, id, id, id))
	}
	if cursor != "" {
		entries = append(entries, fmt.Sprintf(
			`{"content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":%q}}`, cursor))
	}
	return fmt.Sprintf(
		`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`,
		strings.Join(entries, ","))
}

func tweetIDs(tweets []parser.TweetData) []string {
	ids := make([]string, 0, len(tweets))
	for _, tweet := range tweets {
		ids = append(ids, tweet.ID)
	}
	return ids
}

func TestGetUserTimelineStopsAtExactMatchMidPage(t *testing.T) {
	// 增量归档（开关开启）：stopID 出现在首页中部，其后均为已归档的旧推文。
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload([]string{"300", "250", "200", "150"}, "cursor-2"),
	}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{StopAtTweetID: "200"})
	if err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	if got, want := strings.Join(tweetIDs(tweets), ","), "300,250"; got != want {
		t.Fatalf("tweet IDs = %q, want %q", got, want)
	}
	if requester.calls != 1 {
		t.Fatalf("graphQL calls = %d, want 1 (early stop must not fetch page 2)", requester.calls)
	}
}

func TestGetUserTimelinePinnedStopTweetKeepsNewerTweets(t *testing.T) {
	// 置顶回归：上次归档时置顶推文恰好是最新推文（游标=200=置顶）。本次首页为
	// [置顶200, 新推文300, 新推文250, 旧推文100]。若在 200 上立即返回，会永久漏掉
	// 300/250；正确行为是扫完本页、只保留比 200 新的推文，且不再翻页。
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload([]string{"200", "300", "250", "100"}, "cursor-2"),
	}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{StopAtTweetID: "200"})
	if err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	if got, want := strings.Join(tweetIDs(tweets), ","), "300,250"; got != want {
		t.Fatalf("tweet IDs = %q, want %q (newer tweets after pinned stop must be kept)", got, want)
	}
	if requester.calls != 1 {
		t.Fatalf("graphQL calls = %d, want 1 (reached stop must not fetch page 2)", requester.calls)
	}
}

func TestGetUserTimelinePinnedOldTweetDoesNotFalseStop(t *testing.T) {
	// 置顶较旧（100 < stopID）排在首页最前：首页不能因数值比较误停（page 0 仅精确
	// 匹配），置顶本身照旧包含（重复由下游去重），精确命中 200 后停止。
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload([]string{"100", "300", "250", "200", "150"}, "cursor-2"),
	}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{StopAtTweetID: "200"})
	if err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	if got, want := strings.Join(tweetIDs(tweets), ","), "100,300,250"; got != want {
		t.Fatalf("tweet IDs = %q, want %q", got, want)
	}
	if requester.calls != 1 {
		t.Fatalf("graphQL calls = %d, want 1", requester.calls)
	}
}

func TestGetUserTimelineWithoutStopCursorPaginates(t *testing.T) {
	// 全量归档（开关关闭，默认）：完整翻页取回时间线上所有带媒体的推文，
	// 已下载媒体由下游去重跳过。
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload([]string{"300", "250"}, "cursor-2"),
		timelinePagePayload([]string{"200", "100"}, ""),
	}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{})
	if err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	if got, want := strings.Join(tweetIDs(tweets), ","), "300,250,200,100"; got != want {
		t.Fatalf("tweet IDs = %q, want %q", got, want)
	}
	if requester.calls != 2 {
		t.Fatalf("graphQL calls = %d, want 2", requester.calls)
	}
}

func TestGetUserTimelineNumericStopOnLaterPage(t *testing.T) {
	// stopID 推文已删除：首页全为新推文，第二页出现数值更旧的推文时按数值比较停止。
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload([]string{"400", "350"}, "cursor-2"),
		timelinePagePayload([]string{"150", "100"}, ""),
	}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{StopAtTweetID: "200"})
	if err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	// 第二页首个条目 150 <= 200 触发停止；其后仅保留数值更新的项（无）。
	if got, want := strings.Join(tweetIDs(tweets), ","), "400,350"; got != want {
		t.Fatalf("tweet IDs = %q, want %q", got, want)
	}
	if requester.calls != 2 {
		t.Fatalf("graphQL calls = %d, want 2", requester.calls)
	}
}
