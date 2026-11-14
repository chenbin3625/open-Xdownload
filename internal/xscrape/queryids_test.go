package xscrape

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func TestQueryIDRewritePrecedence(t *testing.T) {
	table := queryIDTable{ids: map[string]string{"UserMedia": "discoveredUserMedia1"}}
	if got, want := table.rewrite(xapi.UserMediaPath), xapi.GraphQLPath("discoveredUserMedia1", "UserMedia"); got != want {
		t.Fatalf("rewrite = %q, want %q", got, want)
	}
	if got := table.rewrite("/i/api/1.1/account/multi/list.json"); got != "/i/api/1.1/account/multi/list.json" {
		t.Fatalf("non-graphql path changed: %q", got)
	}

	t.Setenv("OPEN_XDOWNLOAD_TWEET_RESULT_QUERY_ID", "envTweetResultId1")
	table.ids["TweetResultByRestId"] = "discoveredTweetId1"
	path := xapi.TweetResultPath()
	if got := table.rewrite(path); got != path {
		t.Fatalf("env override was rewritten: %q -> %q", path, got)
	}

	table.ids["UserByRestId"] = ""
	if got := table.rewrite(xapi.UserByRestIDPath); got != xapi.UserByRestIDPath {
		t.Fatalf("builtin fallback changed: %q", got)
	}
}

func TestScanQueryIDsPairsNearestOperation(t *testing.T) {
	ids := map[string]string{}
	scanQueryIDs([]byte(`var a={operationName:"UserMedia",queryId:"mediaQueryId1",other:1};var b={queryId:"otherQueryId1",operationName:"UnknownOperation"};`), ids)
	if ids["UserMedia"] != "mediaQueryId1" {
		t.Fatalf("ids = %v", ids)
	}
	if _, ok := ids["UnknownOperation"]; ok {
		t.Fatalf("unrelated operation was recorded: %v", ids)
	}

	ids = map[string]string{}
	scanQueryIDs([]byte(`{queryId:"listingQueryId1",operationName:"ListMembers"}`), ids)
	if ids["ListMembers"] != "listingQueryId1" {
		t.Fatalf("reverse order ids = %v", ids)
	}

	ids = map[string]string{}
	scanQueryIDs([]byte(`{queryId:"short",operationName:"Following"}`), ids)
	if len(ids) != 0 {
		t.Fatalf("invalid query id length was accepted: %v", ids)
	}
}

func TestScriptURLsOnlyKeepsXHosts(t *testing.T) {
	html := `<script src="https://abs.twimg.com/responsive-web/client-web/main.abc.js"></script>
		<script src="https://evil.example/app.js"></script>
		<script src="/local.js"></script>
		<script src="https://x.com/app.js"></script>`
	scripts := scriptURLs([]byte(html), "https://x.com/")
	if len(scripts) != 3 {
		t.Fatalf("scripts = %v", scripts)
	}
	for _, script := range scripts {
		if strings.Contains(script, "evil.example") || !strings.HasSuffix(script, ".js") {
			t.Fatalf("unexpected script %q", script)
		}
	}
}

func TestDiscoverQueryIDsFromHomeAndScript(t *testing.T) {
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeJSON(w, http.StatusOK, `<html><script src="/static/app.js"></script></html>`)
		case "/static/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(`e.exports={queryId:"discoveredMedia1",operationName:"UserMedia"};`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ids, err := discoverQueryIDs(context.Background(), h.engine, "")
	if err != nil {
		t.Fatal(err)
	}
	if ids["UserMedia"] != "discoveredMedia1" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestRefreshFor404IsRateLimited(t *testing.T) {
	var homeRequests int
	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			homeRequests++
			writeJSON(w, http.StatusOK, `<html></html>`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	table := &queryIDTable{last404At: h.clock.Now()}
	if table.refreshFor404(context.Background(), h.engine, "") {
		t.Fatal("refresh within the 10 minute window should be skipped")
	}
	if homeRequests != 0 {
		t.Fatalf("home requests = %d, want 0", homeRequests)
	}

	h.clock.Advance(11 * 60 * time.Second)
	table.refreshFor404(context.Background(), h.engine, "")
	if homeRequests != 1 {
		t.Fatalf("home requests = %d, want 1 after the window", homeRequests)
	}
}

func TestEnvQueryIDOverride(t *testing.T) {
	t.Setenv("OPEN_XDOWNLOAD_TWEET_RESULT_QUERY_ID", "")
	t.Setenv("OPEN_XDOWNLOAD_USER_TWEETS_QUERY_ID", "")
	if envQueryIDOverride("TweetResultByRestId") || envQueryIDOverride("UserTweets") {
		t.Fatal("empty env values must not count as overrides")
	}
	t.Setenv("OPEN_XDOWNLOAD_USER_TWEETS_QUERY_ID", "abc")
	if !envQueryIDOverride("UserTweets") || envQueryIDOverride("UserMedia") {
		t.Fatal("unexpected env override classification")
	}
}
