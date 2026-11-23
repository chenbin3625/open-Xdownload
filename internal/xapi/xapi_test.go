package xapi

import (
	"context"
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/tidwall/gjson"
)

// fakeRequester 依次返回预置的页面，记录请求次数与最后一次请求参数。
type fakeRequester struct {
	pages  []string
	errs   []error
	calls  int
	path   string
	values url.Values
}

func (f *fakeRequester) GraphQL(_ context.Context, path string, values url.Values) ([]byte, error) {
	f.path, f.values = path, values
	if f.calls < len(f.errs) && f.errs[f.calls] != nil {
		err := f.errs[f.calls]
		f.calls++
		return nil, err
	}
	if f.calls >= len(f.pages) {
		return []byte(`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`), nil
	}
	page := f.pages[f.calls]
	f.calls++
	return []byte(page), nil
}

func mediaPage(tweetIDs []string, cursor string) string {
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

func TestParseUserReadsRelocatedFields(t *testing.T) {
	raw := gjson.Parse(`{"result": {
		"__typename": "User",
		"rest_id": "u1",
		"core": {"screen_name": "owner", "name": "Owner"},
		"avatar": {"image_url": "https://pbs.twimg.com/profile_images/1/a_normal.jpg"},
		"privacy": {"protected": true},
		"relationship_perspectives": {"following": true},
		"legacy": {"media_count": 7, "friends_count": 3}
	}}`)
	user, err := ParseUser(raw)
	if err != nil {
		t.Fatalf("ParseUser: %v", err)
	}
	if user.ScreenName != "owner" || user.Name != "Owner" || !user.Protected || !user.Following || user.MediaCount != 7 {
		t.Fatalf("user = %+v, want relocated fields parsed", user)
	}
	if user.AvatarURL != "https://pbs.twimg.com/profile_images/1/a_400x400.jpg" {
		t.Fatalf("AvatarURL = %q, want 400x400 avatar", user.AvatarURL)
	}
}

func TestParseUserUnavailableKeepsReason(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    []string
		notWant []string
	}{
		{
			name:    "suspended with message",
			payload: `{"result":{"__typename":"UserUnavailable","reason":"Suspended","message":"User has been suspended."}}`,
			want:    []string{"冻结", "Suspended", "User has been suspended.", "提示:"},
		},
		{
			name:    "age restricted masquerades as suspended",
			payload: `{"result":{"__typename":"UserUnavailable","reason":"Suspended","message":"User is age-restricted"}}`,
			want:    []string{"年龄限制", "age-restricted", "年龄验证", "重导 Cookie 无效"},
			notWant: []string{"冻结", "Cookie 已失效"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseUser(gjson.Parse(tc.payload))
			if err == nil {
				t.Fatal("ParseUser() = nil error, want unavailable error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q should contain %q", err.Error(), want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(err.Error(), notWant) {
					t.Fatalf("error %q should not contain %q", err.Error(), notWant)
				}
			}
		})
	}
	if !AgeRestrictedPayload([]byte(`{"data":{"user":{"result":{"__typename":"UserUnavailable","message":"age restricted"}}}}`)) {
		t.Fatal("AgeRestrictedPayload did not detect age-restricted response")
	}
}

func TestParseAPIErrorTolerance(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"partial without kind", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"message":"_Missing: No status found with that ID"}]}`, false},
		{"fatal kind without code", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"kind":"Fatal","message":"something failed"}]}`, true},
		{"authorization kind without code", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"kind":"Authorization","message":"denied"}]}`, true},
		{"top-level validation without code", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"kind":"Validation","message":"bad"}]}`, true},
		{"null data field without code", `{"data":{"user":null},"errors":[{"message":"_Missing: No status found with that ID"}]}`, true},
		{"empty data field object without code", `{"data":{"user":{}},"errors":[{"message":"_Missing: No status found with that ID"}]}`, true},
		{"rate limit with null user", `{"data":{"user":null},"errors":[{"code":88,"message":"Rate limit exceeded"}]}`, true},
		{"account locked with data", `{"data":{"user":{"result":{"rest_id":"u1"}}},"errors":[{"code":326,"message":"locked"}]}`, true},
		{"errors object not array", `{"errors":{"code":88}}`, true},
		{"clean data", `{"data":{"user":{}}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ParseAPIError([]byte(tc.payload)); (err != nil) != tc.wantErr {
				t.Fatalf("ParseAPIError() = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
	if !IsRateLimitError(&APIError{Code: APIErrRateLimit}) {
		t.Fatal("API 88 must classify as rate limit")
	}
	if IsTransientError(&APIError{Code: APIErrRateLimit}) {
		t.Fatal("API 88 must not be transient")
	}
}

func TestWalkUserMediaSavesPageAndResumes(t *testing.T) {
	first := &fakeRequester{pages: []string{mediaPage([]string{"300"}, "page-2")}}
	first.errs = []error{nil, &RateLimitedError{RetryAt: time.Now().Add(time.Minute), Cause: ErrAllClientsRateLimited}}
	var saved []UserMediaTimelinePage
	_, err := WalkUserMediaPages(context.Background(), first, User{ID: "u1"}, UserMediaTimelineOptions{}, func(page UserMediaTimelinePage) error {
		saved = append(saved, page)
		return nil
	})
	if err == nil {
		t.Fatal("walk should stop at the simulated rate limit")
	}
	if len(saved) != 1 || saved[0].Cursor != "page-2" || saved[0].Complete {
		t.Fatalf("saved pages = %+v", saved)
	}

	second := &fakeRequester{pages: []string{mediaPage([]string{"200"}, "")}}
	var resumed []UserMediaTimelinePage
	_, err = WalkUserMediaPages(context.Background(), second, User{ID: "u1"}, UserMediaTimelineOptions{Cursor: saved[0].Cursor}, func(page UserMediaTimelinePage) error {
		resumed = append(resumed, page)
		return nil
	})
	if err != nil || second.calls != 1 || len(resumed) != 1 || !resumed[0].Complete {
		t.Fatalf("resume: err=%v calls=%d pages=%+v", err, second.calls, resumed)
	}
}

func TestWalkUserMediaStopsAtIncrementalBoundary(t *testing.T) {
	requester := &fakeRequester{pages: []string{mediaPage([]string{"300", "250", "200", "150"}, "cursor-2")}}
	tweets, err := GetUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{StopAtTweetID: "200"})
	if err != nil {
		t.Fatalf("GetUserTimeline: %v", err)
	}
	ids := make([]string, 0, len(tweets))
	for _, tweet := range tweets {
		ids = append(ids, tweet.ID)
	}
	if got := strings.Join(ids, ","); got != "300,250" {
		t.Fatalf("tweet IDs = %q, want 300,250", got)
	}
	if requester.calls != 1 {
		t.Fatalf("calls = %d, want 1", requester.calls)
	}
}

func TestWalkUserMediaClassifiesStaleCursor(t *testing.T) {
	requester := &fakeRequester{errs: []error{
		&HTTPError{StatusCode: http.StatusBadRequest, Payload: "invalid cursor"},
	}}
	_, err := WalkUserMediaPages(context.Background(), requester, User{ID: "u1"}, UserMediaTimelineOptions{Cursor: "stale"}, func(UserMediaTimelinePage) error {
		return nil
	})
	if !IsInvalidTimelineCursor(err) {
		t.Fatalf("error = %v, want ErrInvalidTimelineCursor", err)
	}
}

func TestWalkUserMediaRejectsUnknownTimelineShape(t *testing.T) {
	page := `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineSomethingNew","entries":[]}]}}}}}}`
	requester := &fakeRequester{pages: []string{page}}
	_, err := GetUserTimeline(context.Background(), requester, User{ID: "u", ScreenName: "user"}, parser.ParseOptions{})
	if err == nil || !strings.Contains(err.Error(), "无法识别 timeline 结构") {
		t.Fatalf("error = %v, want unknown timeline shape error", err)
	}
}

func TestWalkUserMediaAllParseFailedGuardIgnoresUnavailableAndRunsBeforeSave(t *testing.T) {
	// Page has 1 unavailable tombstone + 1 broken tweet (missing legacy/id) -> all real candidates fail.
	page := `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
		`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"__typename":"TweetTombstone"}}}}},` +
		`{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"__typename":"Tweet","rest_id":"999"}}}}} ` +
		`]}]}}}}}}`
	requester := &fakeRequester{pages: []string{page}}
	saveCalled := false
	_, err := WalkUserMediaPages(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, UserMediaTimelineOptions{}, func(UserMediaTimelinePage) error {
		saveCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "全部解析失败") {
		t.Fatalf("err = %v, want all-parse-failed error", err)
	}
	if saveCalled {
		t.Fatal("save must not be called with Complete:true when all parse candidates failed")
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

// X 在全文时间线走到末尾时不返回空 cursor，也不发 TimelineTerminateTimeline：
// 它返回 0 条目 + 原样 Bottom cursor。这里必须正常结束并提交最后一页，否则每个
// 全量归档都会在最后一个用户上失败（"全文时间线游标未前进"），游标永远不推进。
func TestWalkUserTweetsEndOfTimelineRepeatingCursor(t *testing.T) {
	cursorPage := func(value string) string {
		return fullTweetPage(`[{"__typename":"TimelineAddEntries","entries":[{"content":{"__typename":"TimelineTimelineCursor","cursor_type":"Bottom","value":"` + value + `"}}]}]`)
	}
	cases := []struct {
		name  string
		pages []string
		want  int
	}{
		{
			name:  "empty page repeats the request cursor",
			pages: []string{cursorPage("page-2"), cursorPage("page-2")},
			want:  2,
		},
		{
			name:  "empty page falls back to an already seen cursor",
			pages: []string{cursorPage("page-3"), cursorPage("page-2")},
			want:  3,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			first := fullTweetPage(`[{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("103", "42") + `,{"content":{"__typename":"TimelineTimelineCursor","cursor_type":"Bottom","value":"page-2"}}]}]`)
			requester := &fakeRequester{pages: append([]string{first}, test.pages...)}
			var pages []TweetTimelinePage
			err := WalkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{}, func(page TweetTimelinePage) error {
				pages = append(pages, page)
				return nil
			})
			if err != nil {
				t.Fatalf("WalkUserTweets: %v", err)
			}
			if len(pages) != test.want || pages[0].Complete || !pages[len(pages)-1].Complete {
				t.Fatalf("pages = %+v, want %d pages with the last complete", pages, test.want)
			}
			if len(pages[0].Tweets) != 1 || pages[0].Tweets[0].ID != "103" {
				t.Fatalf("first page tweets = %+v", pages[0].Tweets)
			}
			if pages[len(pages)-1].HeadID != "103" {
				t.Fatalf("head = %q, want 103 preserved on the closing page", pages[len(pages)-1].HeadID)
			}
		})
	}
}

// 恢复正常：空 cursor 仍是结束信号，且只发一次请求。
func TestWalkUserTweetsEmptyCursorCompletes(t *testing.T) {
	requester := &fakeRequester{pages: []string{
		fullTweetPage(`[{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("103", "42") + `]}]`),
	}}
	var pages []TweetTimelinePage
	err := WalkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{}, func(page TweetTimelinePage) error {
		pages = append(pages, page)
		return nil
	})
	if err != nil || len(pages) != 1 || !pages[0].Complete || requester.calls != 1 {
		t.Fatalf("pages = %+v calls = %d err = %v", pages, requester.calls, err)
	}
}

func TestFullTimelineItemsSkipsUnavailableTweetPlaceholder(t *testing.T) {
	payload := fullTweetPage(`[{
		"__typename":"TimelineAddEntries",
		"entries":[
			{"content":{"__typename":"TimelineTimelineItem","content":{"__typename":"TimelineTweet","tweet_results":{"id":"VHdlZXRSZXN1bHRzOjE5ODkzNzEzNzU5ODgxOTUzNDU=","rest_id":"1989371375988195345"}}}},
			` + fullTweetEntry("103", "42") + `,
			{"content":{"__typename":"TimelineTimelineCursor","cursor_type":"Bottom","value":"page-2"}}
		]
	} ]`)
	items, next, err := FullTimelineItems([]byte(payload))
	if err != nil {
		t.Fatalf("FullTimelineItems: %v", err)
	}
	if next != "page-2" || len(items) != 1 || TweetResultID(items[0].Result) != "103" {
		t.Fatalf("items = %+v next = %q", items, next)
	}
}

func TestFullTimelineItemsTimelineV2DistinguishesPinnedAndBodyEntries(t *testing.T) {
	entry := func(id string) string {
		return `{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":` + fullTweetFixture(id, "42") + `}}}}`
	}
	payload := `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[` +
		`{"type":"TimelinePinEntry","entry":` + entry("200") + `},` +
		`{"type":"TimelineAddEntries","entries":[` + entry("200") + `,` + entry("150") + `]}` +
		`]}}}}}}`
	items, _, err := FullTimelineItems([]byte(payload))
	if err != nil {
		t.Fatalf("FullTimelineItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	// timelineItems emits TimelineAddEntries items first, then TimelinePinEntry items,
	// preserving distinct Pinned flags even when the same tweet ID appears in both.
	if items[0].Pinned || TweetResultID(items[0].Result) != "200" {
		t.Fatalf("items[0] = %+v, want non-pinned body 200", items[0])
	}
	if items[1].Pinned || TweetResultID(items[1].Result) != "150" {
		t.Fatalf("items[1] = %+v, want non-pinned body 150", items[1])
	}
	if !items[2].Pinned || TweetResultID(items[2].Result) != "200" {
		t.Fatalf("items[2] = %+v, want pinned 200", items[2])
	}
}

func TestWalkUserTweetsAdvancesHeadWhenNewestTweetIsPinned(t *testing.T) {
	t.Run("newest pinned also in body", func(t *testing.T) {
		requester := &fakeRequester{pages: []string{
			fullTweetPage(`[` +
				`{"__typename":"TimelinePinEntry","entry":` + fullTweetEntry("200", "42") + `},` +
				`{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("200", "42") + `,` + fullTweetEntry("150", "42") + `,` + fullTweetEntry("100", "42") + `]}` +
				`]`),
		}}
		var pages []TweetTimelinePage
		err := WalkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{StopID: "100"}, func(page TweetTimelinePage) error {
			pages = append(pages, page)
			return nil
		})
		if err != nil {
			t.Fatalf("WalkUserTweets: %v", err)
		}
		if len(pages) != 1 || !pages[0].Complete || pages[0].HeadID != "200" || len(pages[0].Tweets) != 2 {
			t.Fatalf("pages = %+v, want 1 complete page with HeadID=200 and 2 tweets", pages)
		}
	})

	t.Run("newest pinned only in pin slot", func(t *testing.T) {
		requester := &fakeRequester{pages: []string{
			fullTweetPage(`[` +
				`{"__typename":"TimelinePinEntry","entry":` + fullTweetEntry("200", "42") + `},` +
				`{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("150", "42") + `,` + fullTweetEntry("100", "42") + `]}` +
				`]`),
		}}
		var pages []TweetTimelinePage
		err := WalkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{StopID: "100"}, func(page TweetTimelinePage) error {
			pages = append(pages, page)
			return nil
		})
		if err != nil {
			t.Fatalf("WalkUserTweets: %v", err)
		}
		if len(pages) != 1 || !pages[0].Complete || pages[0].HeadID != "200" || len(pages[0].Tweets) != 2 {
			t.Fatalf("pages = %+v, want 1 complete page with HeadID=200 and 2 tweets", pages)
		}
	})
}

func TestWalkUserTweetsPinnedBoundaryAndHead(t *testing.T) {
	requester := &fakeRequester{pages: []string{
		fullTweetPage(`[{"__typename":"TimelinePinEntry","entry":` + fullTweetEntry("20", "42") + `},{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("103", "42") + `,` + fullTweetEntry("102", "42") + `,` + fullTweetEntry("104", "other") + `,{"content":{"__typename":"TimelineTimelineCursor","cursor_type":"Bottom","value":"page-2"}}]}]`),
		fullTweetPage(`[{"__typename":"TimelineAddEntries","entries":[` + fullTweetEntry("101", "42") + `,` + fullTweetEntry("100", "42") + `]}]`),
	}}
	var pages []TweetTimelinePage
	err := WalkUserTweets(context.Background(), requester, User{ID: "42", ScreenName: "author"}, TweetTimelineOptions{StopID: "100"}, func(page TweetTimelinePage) error {
		pages = append(pages, page)
		return nil
	})
	if err != nil || len(pages) != 2 || len(pages[0].Tweets) != 2 || pages[0].Cursor != "page-2" || pages[0].Complete || pages[1].HeadID != "103" || !pages[1].Complete || len(pages[1].Tweets) != 1 {
		t.Fatalf("timeline: %+v %v", pages, err)
	}
}

func TestFetchUserPageUsesRequesterAndParsesUsers(t *testing.T) {
	payload := `{"data":{"list":{"members_timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[{"content":{"entryType":"TimelineTimelineItem","itemContent":{"user_results":{"result":{"rest_id":"u1","legacy":{"screen_name":"u1","name":"U"}}}}}},{"content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"c2"}}]}]}}}}}`
	requester := &fakeRequester{pages: []string{payload}}
	page, err := FetchUserPage(context.Background(), requester, ListMembersPath, "data.list.members_timeline.timeline.instructions",
		map[string]any{"listId": "l1", "count": 200}, "c1")
	if err != nil {
		t.Fatalf("FetchUserPage: %v", err)
	}
	if page.Cursor != "c2" || len(page.Users) != 1 || page.Users[0].ID != "u1" {
		t.Fatalf("page = %+v", page)
	}
	if requester.path != ListMembersPath {
		t.Fatalf("path = %q", requester.path)
	}
}

func TestFetchUserPageClassifiesStaleCursor(t *testing.T) {
	requester := &fakeRequester{errs: []error{
		&HTTPError{StatusCode: http.StatusBadRequest, Payload: "invalid cursor"},
	}}
	_, err := FetchUserPage(context.Background(), requester, ListMembersPath, "data.list.members_timeline.timeline.instructions",
		map[string]any{"listId": "l1", "count": 200}, "stale-cursor")
	if !IsInvalidTimelineCursor(err) {
		t.Fatalf("error = %v, want ErrInvalidTimelineCursor", err)
	}

	// Empty cursor should NOT be wrapped as invalid timeline cursor.
	requesterEmpty := &fakeRequester{errs: []error{
		&HTTPError{StatusCode: http.StatusBadRequest, Payload: "invalid cursor"},
	}}
	_, err = FetchUserPage(context.Background(), requesterEmpty, ListMembersPath, "data.list.members_timeline.timeline.instructions",
		map[string]any{"listId": "l1", "count": 200}, "")
	if IsInvalidTimelineCursor(err) {
		t.Fatalf("empty cursor error = %v, should not wrap as ErrInvalidTimelineCursor", err)
	}
}

func TestLimitedErrorPayloadPreservesBodyWithEarlyInvalidByte(t *testing.T) {
	// Early invalid UTF-8 byte followed by valid text exceeding MaxErrorPayloadBytes, ending mid-rune.
	body := append([]byte("err:"), 0xff)
	body = append(body, []byte(strings.Repeat("a", MaxErrorPayloadBytes-len(body)-1))...)
	// Append a 3-byte UTF-8 rune ("中": E4 B8 AD) so truncation cuts off after the first byte.
	body = append(body, "中"...)
	got := LimitedErrorPayload(body)
	if len(got) < MaxErrorPayloadBytes-10 {
		t.Fatalf("len(got) = %d, payload was wiped instead of trimming trailing bytes", len(got))
	}
	if !strings.HasPrefix(got, "err:") || !strings.HasSuffix(got, "aaa... [truncated 3 bytes]") {
		t.Fatalf("unexpected clipped payload suffix: %q", got[max(0, len(got)-40):])
	}

	// Pure valid UTF-8 truncated mid-rune must stay valid UTF-8.
	valid := []byte(strings.Repeat("中", MaxErrorPayloadBytes))
	gotValid := LimitedErrorPayload(valid)
	if !utf8.ValidString(gotValid) || len(gotValid) == 0 {
		t.Fatalf("valid UTF-8 truncation produced invalid or empty string: len=%d", len(gotValid))
	}
}

func TestTimelineItemsReadsBottomCursorFromTimelineReplaceEntry(t *testing.T) {
	payload := `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[` +
		`{"type":"TimelineAddEntries","entries":[{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"101"}}}}}]},` +
		`{"type":"TimelineReplaceEntry","entry":{"content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"replaced-cursor"}}}` +
		`]}}}}}}`
	items, next, err := TimelineItems([]byte(payload), "data.user.result.timeline_v2.timeline.instructions")
	if err != nil {
		t.Fatalf("TimelineItems: %v", err)
	}
	if len(items) != 1 || next != "replaced-cursor" {
		t.Fatalf("len(items)=%d next=%q, want 1 and replaced-cursor", len(items), next)
	}
}

// 守卫：xapi 里所有指向 X API 的路径字面量都必须以 /i/api/ 开头。
func TestAllAPIPathLiteralsUseIAPIPrefix(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := goparser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		// strings.Contains/HasPrefix 等的参数是子串匹配，不是请求路径。
		matchers := map[*ast.BasicLit]bool{}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" {
					for _, arg := range call.Args {
						if lit, ok := arg.(*ast.BasicLit); ok {
							matchers[lit] = true
						}
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if ok && matchers[lit] {
				return true
			}
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.HasPrefix(value, "/") {
				return true
			}
			if strings.Contains(value, "graphql/") || strings.Contains(value, "/1.1/") || strings.Contains(value, "/2/") {
				checked++
				if !strings.HasPrefix(value, "/i/api/") {
					t.Errorf("%s: API path %q must start with /i/api/", fset.Position(lit.Pos()), value)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("guard found no API path literals; scanner is broken")
	}
}

func TestGraphQLPathsUseQueryIDTable(t *testing.T) {
	if !strings.HasPrefix(UserByScreenNamePath, "/i/api/graphql/") || !strings.HasSuffix(UserByScreenNamePath, "/UserByScreenName") {
		t.Fatalf("path = %q", UserByScreenNamePath)
	}
	values := UserByIDValues("u1")
	vars := gjson.Parse(values.Get("variables"))
	if vars.Get("userId").String() != "u1" || !gjson.Valid(values.Get("variables")) {
		t.Fatalf("variables = %s", values.Get("variables"))
	}
}

func mediaPageWithPin(pinnedID string, tweetIDs []string, cursor string) string {
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
	instructions := fmt.Sprintf(`{"type":"TimelineAddEntries","entries":[%s]}`, strings.Join(entries, ","))
	if pinnedID != "" {
		pin := fmt.Sprintf(
			`{"type":"TimelinePinEntry","entry":{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":%q,"legacy":{"full_text":"pinned %s","extended_entities":{"media":[{"id_str":"m-%s","type":"photo","media_url_https":"https://pbs.twimg.com/media/%s.jpg"}]}}}}}}}}`,
			pinnedID, pinnedID, pinnedID, pinnedID)
		instructions = pin + "," + instructions
	}
	return fmt.Sprintf(`{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[%s]}}}}}`, instructions)
}

func TestWalkUserMediaPinnedStopIDContinuesToNextPage(t *testing.T) {
	requester := &fakeRequester{pages: []string{
		mediaPageWithPin("200", []string{"400", "350"}, "cursor-2"),
		mediaPage([]string{"300", "250", "200", "150"}, "cursor-3"),
	}}
	tweets, err := GetUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{StopAtTweetID: "200"})
	if err != nil {
		t.Fatalf("GetUserTimeline: %v", err)
	}
	ids := make([]string, 0, len(tweets))
	for _, tweet := range tweets {
		ids = append(ids, tweet.ID)
	}
	if got := strings.Join(ids, ","); got != "400,350,300,250" {
		t.Fatalf("tweet IDs = %q, want 400,350,300,250", got)
	}
	if requester.calls != 2 {
		t.Fatalf("calls = %d, want 2", requester.calls)
	}
}

func TestWalkUserMediaPinnedStopIDDoesNotMaskPage1Stop(t *testing.T) {
	requester := &fakeRequester{pages: []string{
		mediaPageWithPin("200", []string{"400", "350"}, "cursor-2"),
		mediaPage([]string{"300", "200"}, "cursor-3"),
		mediaPage([]string{"150"}, ""),
	}}
	tweets, err := GetUserTimeline(context.Background(), requester, User{ID: "u1", ScreenName: "u"}, parser.ParseOptions{StopAtTweetID: "200"})
	if err != nil {
		t.Fatalf("GetUserTimeline: %v", err)
	}
	ids := make([]string, 0, len(tweets))
	for _, tweet := range tweets {
		ids = append(ids, tweet.ID)
	}
	if got := strings.Join(ids, ","); got != "400,350,300" {
		t.Fatalf("tweet IDs = %q, want 400,350,300", got)
	}
	if requester.calls != 2 {
		t.Fatalf("calls = %d, want 2 (should stop on page 1 at 200)", requester.calls)
	}
}
