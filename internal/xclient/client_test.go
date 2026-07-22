package xclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

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
