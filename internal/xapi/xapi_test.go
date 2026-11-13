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

func fullTweetFixture(id, author string) string {
	return fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"legacy":{"full_text":"pure text","created_at":"Tue Nov 10 12:00:00 +0000 2026"},"core":{"user_results":{"result":{"rest_id":%q,"core":{"screen_name":"author","name":"Author"}}}}}`, id, author)
}

func fullTweetEntry(id, author string) string {
	return `{"content":{"__typename":"TimelineTimelineItem","content":{"__typename":"TimelineTweet","tweet_results":{"result":` + fullTweetFixture(id, author) + `}}}}`
}

func fullTweetPage(instructions string) string {
	return `{"data":{"user_result_by_screen_name":{"result":{"profile_with_replies_timeline_v2":{"timeline":{"instructions":` + instructions + `}}}}}}`
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
