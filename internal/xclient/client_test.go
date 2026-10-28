package xclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
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

func TestFirstBoolSkipsNullAndKeepsExplicitFalse(t *testing.T) {
	if !firstBool(gjson.Parse(`null`), gjson.Parse(`true`)) {
		t.Fatal("null suppressed the legacy true value")
	}
	if firstBool(gjson.Parse(`false`), gjson.Parse(`true`)) {
		t.Fatal("explicit false should override legacy true")
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

func TestPoolRateLimitErrorYieldsToAnotherCookie(t *testing.T) {
	var primaryRequests atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryRequests.Add(1)
		w.Header().Set("Retry-After", "60")
		_, _ = w.Write([]byte(`{"data":{"user":null},"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
	}))
	defer primary.Close()

	var backupRequests atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupRequests.Add(1)
		_, _ = w.Write([]byte(`{"data":{"user":{"result":{"rest_id":"user-2","legacy":{"name":"Backup","screen_name":"backup"}}}}}`))
	}))
	defer backup.Close()

	pool := &Pool{clients: []*Client{
		{http: primary.Client(), baseURL: primary.URL, retryBackoff: func(int) time.Duration { return 0 }, limiter: newRateLimiter()},
		{http: backup.Client(), baseURL: backup.URL, retryBackoff: func(int) time.Duration { return 0 }, limiter: newRateLimiter()},
	}}
	user, err := pool.GetUserByInput(context.Background(), "backup")
	if err != nil {
		t.Fatalf("get user by input: %v", err)
	}
	if user.ID != "user-2" {
		t.Fatalf("user id = %q, want backup response", user.ID)
	}
	if primaryRequests.Load() != 1 || backupRequests.Load() != 1 {
		t.Fatalf("requests primary=%d backup=%d, want 1 each", primaryRequests.Load(), backupRequests.Load())
	}
	if pool.clients[0].isDisabled() {
		t.Fatal("rate-limit response disabled a valid primary client")
	}
	if !pool.clients[0].limiter.wouldBlock("/i/api/graphql/xmU6X_CKVnQ5lSrCbAmJsg/UserByScreenName") {
		t.Fatal("rate-limit response did not block the primary client")
	}
}

func TestRequestRetryDelayUsesCappedExponentialBackoffWithJitter(t *testing.T) {
	base := []time.Duration{750 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second, 6 * time.Second, 6 * time.Second}
	for attempt, expected := range base {
		for i := 0; i < 50; i++ {
			got := requestRetryDelay(attempt)
			if got < expected || got > expected+expected/2 {
				t.Fatalf("attempt %d delay = %s, want within [%s, %s]", attempt, got, expected, expected+expected/2)
			}
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

func TestPoolSelectAllRateLimitedErrorIsClassified(t *testing.T) {
	oldWait := maxPoolSelectWait
	oldStep := maxPoolSelectWaitStep
	maxPoolSelectWait = 5 * time.Millisecond
	maxPoolSelectWaitStep = 5 * time.Millisecond
	t.Cleanup(func() {
		maxPoolSelectWait = oldWait
		maxPoolSelectWaitStep = oldStep
	})

	limiter := newRateLimiter()
	limiter.limits["/x"] = rateLimitState{remaining: 0, limit: 20, reset: time.Now().Add(time.Hour), ready: true}
	pool := &Pool{clients: []*Client{{limiter: limiter, retryBackoff: func(int) time.Duration { return 0 }}}}

	_, err := pool.Select(context.Background(), "/x")
	if !IsAllClientsRateLimited(err) {
		t.Fatalf("Select error = %v, want classified all-clients rate limit", err)
	}
}

func TestPoolSelectErrorsWhenAllClientsDisabled(t *testing.T) {
	pool := &Pool{clients: []*Client{
		{disabledUntil: time.Now().Add(time.Hour)},
		{disabledUntil: time.Now().Add(time.Hour)},
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

func TestUserTimelineContinuesAfterEmptyCursorPage(t *testing.T) {
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload(nil, "next-page"),
		timelinePagePayload([]string{"123"}, ""),
	}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u", ScreenName: "user"}, parser.ParseOptions{})
	if err != nil || len(tweets) != 1 || tweets[0].ID != "123" {
		t.Fatalf("tweets=%+v error=%v", tweets, err)
	}
}

func TestUserTimelineSkipsMalformedTweetWithoutFailing(t *testing.T) {
	// 毒丸修复：单条推文解析失败只计数跳过，不再让整个用户时间线失败；
	// 后续合法推文仍应被返回，且 stats.LastSeenTweetID 覆盖被跳过的条目。
	malformed := `{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"999","__typename":"Tweet"}}}}}`
	valid := `{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"123","legacy":{"full_text":"ok","extended_entities":{"media":[{"id_str":"m1","type":"photo","media_url_https":"https://pbs.twimg.com/media/a.jpg"}]}}}}}}}`
	page := fmt.Sprintf(`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s,%s]}]}}}}}}`, malformed, valid)
	requester := &fakeTimelineRequester{pages: []string{page}}
	tweets, stats, err := getUserTimelineWithStats(context.Background(), requester, User{ID: "u", ScreenName: "user"}, parser.ParseOptions{})
	if err != nil {
		t.Fatalf("getUserTimelineWithStats: %v", err)
	}
	if len(tweets) != 1 || tweets[0].ID != "123" {
		t.Fatalf("tweets = %+v, want the valid tweet only", tweets)
	}
	if stats.SkippedTweets != 1 || stats.LastSeenTweetID != "999" {
		t.Fatalf("stats = %+v, want 1 skipped and LastSeenTweetID=999", stats)
	}
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

func TestGetUserTimelinePinnedStopContinuesThroughNewerPages(t *testing.T) {
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload([]string{"200", "400", "350"}, "cursor-2"),
		timelinePagePayload([]string{"300", "250", "200", "150"}, "cursor-3"),
	}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{StopAtTweetID: "200"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tweetIDs(tweets), ","); got != "400,350,300,250" {
		t.Fatalf("tweet IDs = %q, want all newer tweets across both pages", got)
	}
	if requester.calls != 2 {
		t.Fatalf("graphQL calls = %d, want 2", requester.calls)
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

func TestSelfCheckPageChangeDoesNotDisableClient(t *testing.T) {
	// /home 改版、返回挑战页时 HTTP 200 但识别不到 screen_name：这不能证明 Cookie 失效，
	// 禁用会让主 Cookie 在不重启的情况下永远不可用。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>challenge</html>`))
	}))
	defer server.Close()
	client := &Client{http: server.Client(), baseURL: server.URL}
	if _, err := client.GetSelfScreenName(context.Background()); err == nil {
		t.Fatal("expected an error when screen_name is missing")
	}
	if client.isDisabled() {
		t.Fatal("unrecognized /home page must not disable the client")
	}
	if status := client.status(0, true); status.Error == "" {
		t.Fatal("the self-check error should still be recorded for diagnostics")
	}
}

func TestSuccessfulSelfCheckReenablesClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"screen_name":"owner"}`))
	}))
	defer server.Close()
	client := &Client{http: server.Client(), baseURL: server.URL}
	client.disable(&HTTPError{StatusCode: http.StatusForbidden})
	if !client.isDisabled() {
		t.Fatal("precondition: client should be disabled")
	}
	if _, err := client.GetSelfScreenName(context.Background()); err != nil {
		t.Fatalf("self check: %v", err)
	}
	if client.isDisabled() {
		t.Fatal("a successful login check must re-enable the client")
	}
	if status := client.status(0, true); !status.OK || status.Error != "" {
		t.Fatalf("status = %+v, want OK with cleared error", status)
	}
}

func TestDisabledClientRecoversAfterCooldown(t *testing.T) {
	previous := clientDisableCooldown
	clientDisableCooldown = 30 * time.Millisecond
	t.Cleanup(func() { clientDisableCooldown = previous })

	client := &Client{}
	client.disable(&HTTPError{StatusCode: http.StatusForbidden})
	if !client.isDisabled() {
		t.Fatal("client should be disabled right after disable()")
	}
	time.Sleep(60 * time.Millisecond)
	// 403 也可能来自 WAF 等瞬时拦截；冷却期后必须允许重新尝试，真正失效会被再次禁用。
	if client.isDisabled() {
		t.Fatal("disabled client should become selectable again after the cooldown")
	}
}

func TestDisabledClientErrorIsNotRetried(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()
	var backoffCalls atomic.Int64
	client := &Client{
		http:    server.Client(),
		baseURL: server.URL,
		retryBackoff: func(int) time.Duration {
			backoffCalls.Add(1)
			return 0
		},
	}
	client.disable(errors.New("forbidden"))
	_, err := client.graphQL(context.Background(), "/test", nil)
	if !errors.Is(err, ErrClientDisabled) {
		t.Fatalf("err = %v, want ErrClientDisabled", err)
	}
	if isTransientError(err) {
		t.Fatal("ErrClientDisabled must not be classified as transient")
	}
	if backoffCalls.Load() != 0 || requests.Load() != 0 {
		t.Fatalf("disabled client retried: backoff=%d requests=%d", backoffCalls.Load(), requests.Load())
	}
}

func TestPoolGraphQLKeepsAllClientsRateLimitedClassification(t *testing.T) {
	previousWait := maxPoolSelectWait
	maxPoolSelectWait = 0
	t.Cleanup(func() { maxPoolSelectWait = previousWait })

	reset := time.Now().Add(15 * time.Minute).Unix()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("X-Rate-Limit-Limit", "50")
		w.Header().Set("X-Rate-Limit-Remaining", "0")
		w.Header().Set("X-Rate-Limit-Reset", fmt.Sprint(reset))
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	newClient := func() *Client {
		return &Client{
			http:         server.Client(),
			baseURL:      server.URL,
			limiter:      newRateLimiter(),
			retryBackoff: func(int) time.Duration { return 0 },
		}
	}
	pool := &Pool{clients: []*Client{newClient(), newClient()}}
	_, err := pool.graphQL(context.Background(), "/i/api/graphql/x/UserMedia", nil)
	// 第一个客户端 429 后，Select 发现全部受限并返回 ErrAllClientsRateLimited。这个分类
	// 必须保留：manager 靠它中止批量归档，否则会继续对后面每个用户撞限流。
	if !IsAllClientsRateLimited(err) {
		t.Fatalf("err = %v, want ErrAllClientsRateLimited", err)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want the last 429 kept as context", err)
	}
}

func TestRateLimitResetHeaderIsClamped(t *testing.T) {
	limiter := newRateLimiter()
	header := http.Header{}
	header.Set("X-Rate-Limit-Limit", "50")
	header.Set("X-Rate-Limit-Remaining", "0")
	// 时钟偏差或异常值：reset 在一天之后。before() 会一直等到 reset，任务带着槽位卡死。
	header.Set("X-Rate-Limit-Reset", fmt.Sprint(time.Now().Add(24*time.Hour).Unix()))
	limiter.after("/p", header)
	reset, ok := limiter.blockedReset()
	if !ok {
		t.Fatal("path should be blocked")
	}
	if wait := time.Until(reset); wait > maxRetryAfter+time.Minute {
		t.Fatalf("blocked for %v, want at most ~%v", wait, maxRetryAfter)
	}
}

func TestPoolSwitchesCookieInsteadOfWaitingOutRateLimit(t *testing.T) {
	reset := time.Now().Add(15 * time.Minute).Unix()
	var limitedRequests atomic.Int64
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limitedRequests.Add(1)
		w.Header().Set("X-Rate-Limit-Limit", "50")
		w.Header().Set("X-Rate-Limit-Remaining", "0")
		w.Header().Set("X-Rate-Limit-Reset", fmt.Sprint(reset))
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer limited.Close()
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer free.Close()

	pool := &Pool{clients: []*Client{
		{http: limited.Client(), baseURL: limited.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }},
		{http: free.Client(), baseURL: free.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	payload, err := pool.graphQL(ctx, "/i/api/graphql/x/UserMedia", nil)
	if err != nil {
		t.Fatalf("graphQL: %v (a free cookie was available)", err)
	}
	if string(payload) != `{"data":{}}` {
		t.Fatalf("payload = %q", payload)
	}
	// 受限客户端收到 429 后应立即交还，而不是在 before() 里等 15 分钟复位。
	if elapsed := time.Since(started); elapsed > 2*time.Second || limitedRequests.Load() != 1 {
		t.Fatalf("elapsed %v, limited requests %d; want an immediate switch after one 429", elapsed, limitedRequests.Load())
	}
}

func TestAPIErrorIgnoresPartialErrorsAlongsideData(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{
			// X 常在 200 响应里同时返回完整 data 与针对个别子项（如不可用的引用推文）的错误。
			name:    "partial error with timeline data",
			payload: `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}},"errors":[{"message":"_Missing: No status found with that ID","kind":"NonFatal"}]}`,
			wantErr: false,
		},
		{name: "errors with null data", payload: `{"data":null,"errors":[{"code":88,"message":"Rate limit exceeded"}]}`, wantErr: true},
		{name: "rate limit with null user", payload: `{"data":{"user":null},"errors":[{"code":88,"message":"Rate limit exceeded"}]}`, wantErr: true},
		{name: "locked account with data", payload: `{"data":{"user":null},"errors":[{"code":326,"message":"locked"}]}`, wantErr: true},
		{name: "fatal error follows partial error", payload: `{"data":{"user":{}},"errors":[{"kind":"NonFatal"},{"code":88,"message":"Rate limit exceeded"}]}`, wantErr: true},
		{name: "rate limit marked nonfatal", payload: `{"data":{"user":{}},"errors":[{"kind":"NonFatal","code":88}]}`, wantErr: true},
		{name: "empty errors", payload: `{"data":{},"errors":[]}`, wantErr: false},
		{name: "errors with empty data", payload: `{"data":{},"errors":[{"code":29,"message":"Timeout"}]}`, wantErr: true},
		{name: "errors without data", payload: `{"errors":[{"code":326,"message":"locked"}]}`, wantErr: true},
		{name: "rest v1.1 error", payload: `{"errors":[{"code":34,"message":"Sorry, that page does not exist."}]}`, wantErr: true},
		{name: "clean data", payload: `{"data":{"user":{}}}`, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := apiError([]byte(tc.payload)); (err != nil) != tc.wantErr {
				t.Fatalf("apiError() = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestParseUserReadsRelocatedFields(t *testing.T) {
	// X 把部分字段从 legacy 迁到了 core / privacy / relationship_perspectives；读不到时
	// ScreenName 为空，私密账号的 following 也会被误判为 false（直接跳过归档）。
	raw := gjson.Parse(`{"result": {
		"__typename": "User",
		"rest_id": "u1",
		"core": {"screen_name": "owner", "name": "Owner"},
		"privacy": {"protected": true},
		"relationship_perspectives": {"following": true},
		"legacy": {"media_count": 7, "friends_count": 3}
	}}`)
	user, err := parseUser(raw)
	if err != nil {
		t.Fatalf("parseUser: %v", err)
	}
	if user.ScreenName != "owner" || user.Name != "Owner" || !user.Protected || !user.Following || user.MediaCount != 7 {
		t.Fatalf("user = %+v, want relocated fields parsed", user)
	}

	legacy := gjson.Parse(`{"result": {"rest_id": "u2", "legacy": {"screen_name": "old", "name": "Old", "protected": true, "following": true}}}`)
	user, err = parseUser(legacy)
	if err != nil || user.ScreenName != "old" || !user.Protected || !user.Following {
		t.Fatalf("legacy user = %+v, err %v", user, err)
	}
}

// —— 以下是本次修复新增的回归测试 ——

func newHeaderTestClient(server *httptest.Server) *Client {
	return &Client{
		http:         server.Client(),
		baseURL:      server.URL,
		authToken:    "auth-token-value",
		csrfToken:    "csrf-token-value",
		limiter:      newRateLimiter(),
		retryBackoff: func(int) time.Duration { return 0 },
	}
}

func TestClientSendsCookiesCSRFAndBearerHeaders(t *testing.T) {
	type captured struct {
		header  http.Header
		cookies map[string]string
	}
	requests := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookies := map[string]string{}
		for _, cookie := range r.Cookies() {
			cookies[cookie.Name] = cookie.Value
		}
		requests <- captured{header: r.Header.Clone(), cookies: cookies}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	client := newHeaderTestClient(server)
	if _, err := client.graphQL(context.Background(), "/i/api/graphql/test/Test", url.Values{"variables": {"{}"}}); err != nil {
		t.Fatalf("graphQL: %v", err)
	}
	got := <-requests
	if got.header.Get("Authorization") != "Bearer "+guestBearer {
		t.Fatalf("Authorization = %q", got.header.Get("Authorization"))
	}
	if got.header.Get("X-Csrf-Token") != "csrf-token-value" {
		t.Fatalf("X-Csrf-Token = %q", got.header.Get("X-Csrf-Token"))
	}
	if got.header.Get("User-Agent") != userAgent {
		t.Fatalf("User-Agent = %q", got.header.Get("User-Agent"))
	}
	if got.header.Get("Accept") != "application/json" {
		t.Fatalf("Accept = %q", got.header.Get("Accept"))
	}
	if got.cookies["auth_token"] != "auth-token-value" || got.cookies["ct0"] != "csrf-token-value" {
		t.Fatalf("cookies = %+v", got.cookies)
	}
}

type captureRequester struct {
	path    string
	values  url.Values
	payload string
	err     error
}

func (c *captureRequester) graphQL(_ context.Context, path string, values url.Values) ([]byte, error) {
	c.path, c.values = path, values
	return []byte(c.payload), c.err
}

func TestGraphQLPathsComeFromQueryIDTableAndVariablesAreJSON(t *testing.T) {
	requester := &captureRequester{payload: `{"data":{"user":{"result":{"rest_id":"u1","legacy":{"screen_name":"alice","name":"A"}}}}}`}
	if _, err := getUserByScreenName(context.Background(), requester, "alice"); err != nil {
		t.Fatalf("getUserByScreenName: %v", err)
	}
	if requester.path != userByScreenNamePath {
		t.Fatalf("path = %q, want %q", requester.path, userByScreenNamePath)
	}
	variables := gjson.Parse(requester.values.Get("variables"))
	if variables.Get("screen_name").String() != "alice" || variables.Get("withSafetyModeUserFields").Bool() != true {
		t.Fatalf("variables = %s", requester.values.Get("variables"))
	}
	if !gjson.Valid(requester.values.Get("variables")) || requester.values.Get("features") == "" {
		t.Fatal("variables/features not valid JSON")
	}

	mediaRequester := &captureRequester{payload: `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`}
	if _, err := getUserTimeline(context.Background(), mediaRequester, User{ID: "u1", ScreenName: "alice"}, parser.ParseOptions{}); err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	if mediaRequester.path != userMediaPath {
		t.Fatalf("user media path = %q, want %q", mediaRequester.path, userMediaPath)
	}
	mediaVars := gjson.Parse(mediaRequester.values.Get("variables"))
	if mediaVars.Get("userId").String() != "u1" || mediaVars.Get("count").Int() != 100 || !gjson.Valid(mediaRequester.values.Get("variables")) {
		t.Fatalf("user media variables = %s", mediaRequester.values.Get("variables"))
	}

	listRequester := &captureRequester{payload: `{"data":{"list":{"members_timeline":{"timeline":{"instructions":[]}}}}}`}
	if _, err := getListMembers(context.Background(), listRequester, List{ID: "list-1"}); err != nil {
		t.Fatalf("getListMembers: %v", err)
	}
	if listRequester.path != listMembersPath {
		t.Fatalf("list members path = %q, want %q", listRequester.path, listMembersPath)
	}
	if gjson.Get(listRequester.values.Get("variables"), "listId").String() != "list-1" {
		t.Fatalf("list members variables = %s", listRequester.values.Get("variables"))
	}

	followingRequester := &captureRequester{payload: `{"data":{"user":{"result":{"timeline":{"timeline":{"instructions":[]}}}}}}`}
	if _, err := getFollowing(context.Background(), followingRequester, User{ID: "u1"}); err != nil {
		t.Fatalf("getFollowing: %v", err)
	}
	if followingRequester.path != followingPath {
		t.Fatalf("following path = %q, want %q", followingRequester.path, followingPath)
	}
	if gjson.Get(followingRequester.values.Get("variables"), "userId").String() != "u1" {
		t.Fatalf("following variables = %s", followingRequester.values.Get("variables"))
	}
}

func TestClientRateLimitErrorsAreNotRetried(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "api code 88",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"data":{"user":null},"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
			},
		},
		{
			name: "http 429",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"errors":[{"code":88}]}`))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				tc.handler(w, r)
			}))
			defer server.Close()
			client := newHeaderTestClient(server)
			_, err := getUserByInput(context.Background(), client, "alice")
			if err == nil {
				t.Fatal("expected rate-limit error")
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1 (no retry on rate limit)", requests.Load())
			}
			if !isRateLimitError(err) {
				t.Fatalf("error %v not classified as rate limit", err)
			}
			if !client.limiter.wouldBlock(userByScreenNamePath) {
				t.Fatal("rate limit did not block the path for failover")
			}
		})
	}
}

func TestBodyTooLargeAndBusyWaitAreNotTransient(t *testing.T) {
	if isTransientError(httpx.ErrBodyTooLarge) {
		t.Fatal("ErrBodyTooLarge must not be retried")
	}
	if isTransientError(errRateLimitBusyWait) {
		t.Fatal("errRateLimitBusyWait must not be retried")
	}
	if !isRateLimitError(&APIError{Code: apiErrRateLimit}) || isTransientError(&APIError{Code: apiErrRateLimit}) {
		t.Fatal("API code 88 must be rate-limit and non-transient")
	}
}

func TestForbiddenWAFChallengeDoesNotDisableClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><head><title>Just a moment...</title></head><body>Checking your browser before accessing</body></html>`))
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	_, err := getUserByInput(context.Background(), client, "alice")
	if err == nil || !errors.Is(err, ErrWAFChallenge) || !isWAFChallengeError(err) {
		t.Fatalf("error = %v, want WAF challenge", err)
	}
	if client.isDisabled() {
		t.Fatal("WAF challenge 403 must not disable the client")
	}
	if !client.limiter.wouldBlock(userByScreenNamePath) {
		t.Fatal("WAF challenge should short-block the path for failover")
	}
}

func TestForbiddenNonWAFFailsWithShortCooldown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you"}]}`))
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	_, err := getUserByInput(context.Background(), client, "alice")
	if err == nil {
		t.Fatal("expected non-WAF 403 error")
	}
	if !client.isDisabled() {
		t.Fatal("non-WAF 403 should disable the client")
	}
	client.mu.Lock()
	remaining := time.Until(client.disabledUntil)
	client.mu.Unlock()
	if remaining < 4*time.Minute || remaining > 6*time.Minute {
		t.Fatalf("403 cooldown = %s, want about 5m", remaining)
	}
}

func TestUnauthorizedDisablesClientForLongCooldown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	if _, err := getUserByInput(context.Background(), client, "alice"); err == nil {
		t.Fatal("expected 401 error")
	}
	client.mu.Lock()
	remaining := time.Until(client.disabledUntil)
	client.mu.Unlock()
	if remaining < 25*time.Minute || remaining > 31*time.Minute {
		t.Fatalf("401 cooldown = %s, want about 30m", remaining)
	}
}

func TestSelfCheckWAF403DoesNotDisableClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html>challenge</html>`))
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	if _, err := client.GetSelfScreenName(context.Background()); err == nil || !errors.Is(err, ErrWAFChallenge) {
		t.Fatalf("error = %v, want WAF challenge", err)
	}
	if client.isDisabled() {
		t.Fatal("self-check WAF 403 must not disable the client")
	}
	if !client.limiter.wouldBlock(homeCheckPath) {
		t.Fatal("self-check WAF 403 should short-block /home")
	}
	// 冷却期内再次自检应立刻返回，不再请求 /home。
	if _, err := client.GetSelfScreenName(context.Background()); err == nil {
		t.Fatal("self-check during WAF cooldown should fail fast")
	}
}

func TestGraphQL404ReturnsQueryIDStale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`not found`))
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	_, err := getUserByInput(context.Background(), client, "alice")
	if err == nil || !errors.Is(err, ErrQueryIDStale) {
		t.Fatalf("error = %v, want ErrQueryIDStale", err)
	}
}

func TestAPIPartialErrorsAreToleratedOnlyWhenNonFatal(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"partial without kind", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"message":"_Missing: No status found with that ID"}]}`, false},
		{"partial with code -1", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"code":-1,"message":"missing child"}]}`, false},
		{"partial unexpected code", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"code":34,"message":"not found"}]}`, false},
		{"rate limit without kind", `{"data":{"user":null},"errors":[{"code":88,"message":"rate limited"}]}`, true},
		{"account locked with data", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"code":326,"message":"locked"}]}`, true},
		{"errors object not array", `{"errors":{"code":88}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := apiError([]byte(tc.payload))
			if (err != nil) != tc.wantErr {
				t.Fatalf("apiError() = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestPaginationStopsWhenCursorDoesNotAdvance(t *testing.T) {
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload(nil, "same-cursor"),
		timelinePagePayload(nil, "same-cursor"),
		timelinePagePayload([]string{"123"}, ""),
	}}
	if _, err := getUserTimeline(context.Background(), requester, User{ID: "u", ScreenName: "user"}, parser.ParseOptions{}); err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	if requester.calls != 2 {
		t.Fatalf("calls = %d, want 2 (stop when cursor does not advance)", requester.calls)
	}
}

func TestPaginationStopsAfterConsecutiveEmptyPages(t *testing.T) {
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload(nil, "c1"),
		timelinePagePayload(nil, "c2"),
		timelinePagePayload(nil, "c3"),
		timelinePagePayload(nil, "c4"),
		timelinePagePayload([]string{"123"}, ""),
	}}
	if _, err := getUserTimeline(context.Background(), requester, User{ID: "u", ScreenName: "user"}, parser.ParseOptions{}); err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	if requester.calls != maxConsecutiveEmptyPages {
		t.Fatalf("calls = %d, want %d", requester.calls, maxConsecutiveEmptyPages)
	}
}

func TestTimelineDeduplicatesTweetIDsAcrossPages(t *testing.T) {
	requester := &fakeTimelineRequester{pages: []string{
		timelinePagePayload([]string{"300", "250"}, "c2"),
		timelinePagePayload([]string{"250", "200"}, ""),
	}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u", ScreenName: "user"}, parser.ParseOptions{})
	if err != nil {
		t.Fatalf("getUserTimeline: %v", err)
	}
	if got, want := strings.Join(tweetIDs(tweets), ","), "300,250,200"; got != want {
		t.Fatalf("tweet IDs = %q, want %q", got, want)
	}
}

func listMembersPage(userIDs []string, cursor string) string {
	entries := make([]string, 0, len(userIDs)+1)
	for _, id := range userIDs {
		entries = append(entries, fmt.Sprintf(
			`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"user_results":{"result":{"rest_id":%q,"legacy":{"screen_name":"u-%s","name":"U"}}}}}}`, id, id))
	}
	if cursor != "" {
		entries = append(entries, fmt.Sprintf(`{"content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":%q}}`, cursor))
	}
	return fmt.Sprintf(`{"data":{"list":{"members_timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`, strings.Join(entries, ","))
}

func TestListMembersPoolUsesBackupWhenPrimaryDisabled(t *testing.T) {
	var primaryCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer primary.Close()
	var backupCalls atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		_, _ = w.Write([]byte(listMembersPage([]string{"u1"}, "")))
	}))
	defer backup.Close()

	primaryClient := &Client{http: primary.Client(), baseURL: primary.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }}
	primaryClient.disable(errors.New("expired"))
	backupClient := &Client{http: backup.Client(), baseURL: backup.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }}
	pool := &Pool{clients: []*Client{primaryClient, backupClient}}
	users, err := pool.GetListMembers(context.Background(), List{ID: "l1"})
	if err != nil {
		t.Fatalf("GetListMembers: %v", err)
	}
	if len(users) != 1 || users[0].ID != "u1" {
		t.Fatalf("users = %+v", users)
	}
	if primaryCalls.Load() != 0 || backupCalls.Load() != 1 {
		t.Fatalf("calls primary=%d backup=%d, want 0/1", primaryCalls.Load(), backupCalls.Load())
	}
}

func TestListMembersPoolKeepsOneClientForAllPages(t *testing.T) {
	var primaryCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := primaryCalls.Add(1)
		switch call {
		case 1:
			_, _ = w.Write([]byte(listMembersPage([]string{"u1"}, "c2")))
		case 2:
			_, _ = w.Write([]byte(listMembersPage([]string{"u2"}, "")))
		default:
			_, _ = w.Write([]byte(listMembersPage(nil, "")))
		}
	}))
	defer primary.Close()
	var backupCalls atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		_, _ = w.Write([]byte(listMembersPage([]string{"other"}, "")))
	}))
	defer backup.Close()

	primaryClient := &Client{http: primary.Client(), baseURL: primary.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }}
	backupClient := &Client{http: backup.Client(), baseURL: backup.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }}
	pool := &Pool{clients: []*Client{primaryClient, backupClient}}
	users, err := pool.GetListMembers(context.Background(), List{ID: "l1"})
	if err != nil {
		t.Fatalf("GetListMembers: %v", err)
	}
	if len(users) != 2 || users[0].ID != "u1" || users[1].ID != "u2" {
		t.Fatalf("users = %+v", users)
	}
	if primaryCalls.Load() != 2 || backupCalls.Load() != 0 {
		t.Fatalf("calls primary=%d backup=%d, want 2/0", primaryCalls.Load(), backupCalls.Load())
	}
}

func TestPoolFollowUserFallsBackOnRateLimit(t *testing.T) {
	var primaryCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer primary.Close()
	var backupCalls atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		_, _ = w.Write([]byte(`{"id":"user-1"}`))
	}))
	defer backup.Close()

	primaryClient := &Client{http: primary.Client(), baseURL: primary.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }}
	backupClient := &Client{http: backup.Client(), baseURL: backup.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }}
	pool := &Pool{clients: []*Client{primaryClient, backupClient}}
	if err := pool.FollowUser(context.Background(), User{ID: "user-1"}); err != nil {
		t.Fatalf("FollowUser: %v", err)
	}
	if primaryCalls.Load() != 1 || backupCalls.Load() != 1 {
		t.Fatalf("calls primary=%d backup=%d, want 1/1", primaryCalls.Load(), backupCalls.Load())
	}
}

func TestListMembersDeduplicatesUsers(t *testing.T) {
	requester := &fakeTimelineRequester{pages: []string{
		listMembersPage([]string{"u1", "u2"}, "c2"),
		listMembersPage([]string{"u2", "u3"}, ""),
	}}
	users, err := getListMembers(context.Background(), requester, List{ID: "l1"})
	if err != nil {
		t.Fatalf("getListMembers: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("users = %+v, want deduplicated 3", users)
	}
}

func TestTimelineParsingErrorsDoNotFailTheUser(t *testing.T) {
	// 毒丸场景：最新一条推文解析失败，但其他推文仍应返回、任务成功。
	malformed := `{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"999","__typename":"Tweet"}}}}}`
	valid := `{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"100","legacy":{"full_text":"ok","extended_entities":{"media":[{"id_str":"m1","type":"photo","media_url_https":"https://pbs.twimg.com/media/a.jpg"}]}}}}}}}`
	page := fmt.Sprintf(`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s,%s]}]}}}}}}`, malformed, valid)
	requester := &fakeTimelineRequester{pages: []string{page}}
	tweets, err := getUserTimeline(context.Background(), requester, User{ID: "u", ScreenName: "user"}, parser.ParseOptions{})
	if err != nil || len(tweets) != 1 || tweets[0].ID != "100" {
		t.Fatalf("tweets=%+v err=%v, want valid tweet with nil error", tweets, err)
	}
}

func followingPage(userIDs []string, cursor string) string {
	entries := make([]string, 0, len(userIDs)+1)
	for _, id := range userIDs {
		entries = append(entries, fmt.Sprintf(
			`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"user_results":{"result":{"rest_id":%q,"legacy":{"screen_name":"f-%s","name":"F"}}}}}}`, id, id))
	}
	if cursor != "" {
		entries = append(entries, fmt.Sprintf(`{"content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":%q}}`, cursor))
	}
	return fmt.Sprintf(`{"data":{"user":{"result":{"timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[%s]}]}}}}}}`, strings.Join(entries, ","))
}

func TestFollowingPoolUsesBackupWhenPrimaryDisabled(t *testing.T) {
	var primaryCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer primary.Close()
	var backupCalls atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		_, _ = w.Write([]byte(followingPage([]string{"u1"}, "")))
	}))
	defer backup.Close()

	primaryClient := &Client{http: primary.Client(), baseURL: primary.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }}
	primaryClient.disable(errors.New("expired"))
	backupClient := &Client{http: backup.Client(), baseURL: backup.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }}
	pool := &Pool{clients: []*Client{primaryClient, backupClient}}
	users, err := pool.GetFollowing(context.Background(), User{ID: "u1"})
	if err != nil {
		t.Fatalf("GetFollowing: %v", err)
	}
	if len(users) != 1 || users[0].ID != "u1" {
		t.Fatalf("users = %+v", users)
	}
	if primaryCalls.Load() != 0 || backupCalls.Load() != 1 {
		t.Fatalf("calls primary=%d backup=%d, want 0/1", primaryCalls.Load(), backupCalls.Load())
	}
}

func TestTimelineUnknownInstructionShapeReturnsError(t *testing.T) {
	// X 改 timeline 结构（instructions 类型全未知）时必须报错，而不是静默当成"没有推文"。
	page := `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineSomethingNew","entries":[]}]}}}}}}`
	requester := &fakeTimelineRequester{pages: []string{page}}
	_, err := getUserTimeline(context.Background(), requester, User{ID: "u", ScreenName: "user"}, parser.ParseOptions{})
	if err == nil || !strings.Contains(err.Error(), "无法识别 timeline 结构") {
		t.Fatalf("error = %v, want unknown timeline shape error", err)
	}
}
