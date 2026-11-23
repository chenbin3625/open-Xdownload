package xtid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGenerateFallsBackFromStaleSigningScript(t *testing.T) {
	for _, scenario := range []string{"not found", "missing key indices"} {
		t.Run(scenario, func(t *testing.T) {
			golden := loadGolden(t)
			fs := newFixtureServer(t)
			const stalePath = "/responsive-web/client-web/ondemand.s.abcdef1a.js"
			page := `<script src="https://abs.twimg.com` + stalePath + `"></script>` + string(loadFixture(t, "home.html"))
			fs.setRoute("/", []byte(page))
			if scenario == "missing key indices" {
				fs.setRoute(stalePath, []byte("export const decoy = true"))
			}
			gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))
			tc := golden.Cases[0]

			got, err := gen.Generate(context.Background(), tc.Method, tc.Path)

			if err != nil {
				t.Fatalf("stale signing script should fall back to dynamic imports: %v", err)
			}
			if got != tc.TransactionID {
				t.Fatalf("fallback transaction ID=%q, want %q", got, tc.TransactionID)
			}
			if fs.count(stalePath) == 0 {
				t.Error("fixture did not exercise the stale script")
			}
			if fs.count("/x-web/x-web/assets/DIQrwHty.js") == 0 {
				t.Error("fallback did not load the dynamic signing chunk")
			}
		})
	}
}

func TestScriptFetchRejectsUntrustedHostsBeforeRequest(t *testing.T) {
	fs := newFixtureServer(t)
	fs.setRoute("/untrusted.js", []byte("must not be requested"))
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, options{})
	for _, host := range []string{"evil.example", "x.com.evil.example", "abs.twimg.com.evil.example"} {
		t.Run(host, func(t *testing.T) {
			_, err := gen.get(context.Background(), "https://"+host+"/untrusted.js")
			if err == nil || !strings.Contains(err.Error(), "拒绝请求非 X 脚本主机") {
				t.Fatalf("untrusted script host must be rejected, got %v", err)
			}
			if got := fs.count("/untrusted.js"); got != 0 {
				t.Fatalf("untrusted script reached the transport %d times", got)
			}
		})
	}
}

func TestGeneratorGetRejectsDowngradeAndNonStandardPort(t *testing.T) {
	gen := New(nil, "https://x.com", testUserAgent)
	for _, raw := range []string{"http://x.com/app.js", "https://x.com:8443/app.js", "https://abs.twimg.com:8443/app.js"} {
		if _, err := gen.get(context.Background(), raw); err == nil {
			t.Fatalf("expected rejection for %q", raw)
		}
	}
}

func TestDefaultClientRedirectRejectsUntrustedHost(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/evil.js", http.StatusFound)
	}))
	defer src.Close()

	gen := New(nil, src.URL, testUserAgent)
	if _, err := gen.get(context.Background(), src.URL+"/start.js"); err == nil {
		t.Fatal("expected redirect to untrusted host to be rejected")
	}
}

func TestExternalClientRedirectRejectsUntrustedHost(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/evil.js", http.StatusFound)
	}))
	defer src.Close()

	extClient := &http.Client{Timeout: 5 * time.Second}
	gen := New(extClient, src.URL, testUserAgent)
	if _, err := gen.get(context.Background(), src.URL+"/start.js"); err == nil {
		t.Fatal("expected redirect to untrusted host to be rejected with external client")
	}
	if extClient.CheckRedirect != nil {
		t.Fatal("xtid.New must not mutate the caller's http.Client")
	}
}

func TestCrawlForIndicesBFSAndPriorityOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/seed1.js":
			// Has one priority dep and one normal dep.
			_, _ = w.Write([]byte(`import "./normal1.js"; import("./prio1.js").then(e => e.default());`))
		case "/prio1.js":
			_, _ = w.Write([]byte(`export const x = 1;`))
		case "/seed2.js":
			// Contains key byte indices so crawl terminates here if seed2 is visited before normal1.
			_, _ = w.Write([]byte(`function f(e){return parseInt(e[12],16)}`))
		default:
			_, _ = w.Write([]byte(`export const noop = 0;`))
		}
	}))
	defer srv.Close()

	gen := New(srv.Client(), srv.URL, testUserAgent)
	body, err := gen.crawlForIndices(context.Background(), []string{srv.URL + "/seed1.js", srv.URL + "/seed2.js"})
	if err != nil {
		t.Fatalf("crawlForIndices: %v", err)
	}
	if !matchesKeyByteIndices(body) {
		t.Fatalf("unexpected body: %q", string(body))
	}
	// Expected visit order: seed1 -> prio1 (prepended) -> seed2 (remaining seed before normal1).
	want := []string{"/seed1.js", "/prio1.js", "/seed2.js"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("crawl order = %v, want %v (normal deps must not starve remaining seeds)", order, want)
	}
}

func TestExtractScriptURLsLegacyMapScopedToScriptBlock(t *testing.T) {
	// Cross-script JSON/text noise with matching ID "3" must not override or leak into another script block.
	page := `<script>{3:"noise-from-other-script",4:"../traversal"}</script>` +
		`<script>{3:"ondemand.s",3:"abcdef1",4:"0123456789abcdef"}</script>`
	urls := extractScriptURLs(page)
	joined := strings.Join(urls, "\n")
	if !strings.Contains(joined, "ondemand.s.abcdef1a.js") {
		t.Fatalf("expected ondemand.s chunk, got:\n%s", joined)
	}
	if strings.Contains(joined, "noise-from-other-script") || strings.Contains(joined, "traversal") {
		t.Fatalf("unexpected cross-script or traversal name leaked:\n%s", joined)
	}
}

func TestLeaderCancellationDoesNotAbortWaiterInKeys(t *testing.T) {
	golden := loadGolden(t)
	fs := newFixtureServer(t)
	gate := make(chan struct{})
	origHome := loadFixture(t, "home.html")
	var gateOnce sync.Once
	fs.Server.Close()
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			gateOnce.Do(func() { <-gate })
			_, _ = w.Write(origHome)
			return
		}
		fs.mu.Lock()
		body, ok := fs.routes[r.URL.Path]
		fs.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := gen.Generate(leaderCtx, golden.Cases[0].Method, golden.Cases[0].Path)
		leaderErr <- err
	}()
	time.Sleep(30 * time.Millisecond)

	waiterRes := make(chan string, 1)
	waiterErr := make(chan error, 1)
	go func() {
		id, err := gen.Generate(context.Background(), golden.Cases[0].Method, golden.Cases[0].Path)
		waiterRes <- id
		waiterErr <- err
	}()
	time.Sleep(30 * time.Millisecond)

	cancelLeader()
	if err := <-leaderErr; err == nil {
		t.Fatal("expected leader cancellation error")
	}
	close(gate)
	if err := <-waiterErr; err != nil {
		t.Fatalf("waiter failed after leader canceled: %v", err)
	}
	if got := <-waiterRes; got != golden.Cases[0].TransactionID {
		t.Fatalf("waiter transaction ID = %q, want %q", got, golden.Cases[0].TransactionID)
	}
}
