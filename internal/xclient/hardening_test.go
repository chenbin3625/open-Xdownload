package xclient

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseRetryAfterIsClamped(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		expect time.Duration
	}{
		{name: "正常秒数原样返回", value: "30", expect: 30 * time.Second},
		{name: "超大秒数被夹到上限", value: "86400", expect: maxRetryAfter},
		{name: "负数视为无", value: "-5", expect: 0},
		{name: "缺失视为无", value: "", expect: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			if tc.value != "" {
				header.Set("Retry-After", tc.value)
			}
			if got := parseRetryAfter(header); got != tc.expect {
				t.Fatalf("parseRetryAfter(%q) = %s, want %s", tc.value, got, tc.expect)
			}
		})
	}
}

func TestParseRetryAfterHTTPDateIsClamped(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat))
	if got := parseRetryAfter(header); got > maxRetryAfter {
		t.Fatalf("HTTP-date 分支未夹上限: %s > %s", got, maxRetryAfter)
	}
}

func TestSameOriginRequiresMatchingPortAndScheme(t *testing.T) {
	left, _ := url.Parse("https://X.com:443/path")
	right, _ := url.Parse("https://x.com/path")
	if !sameOrigin(left, right) {
		t.Fatal("显式 HTTPS 默认端口应视为同源")
	}
	otherPort, _ := url.Parse("https://x.com:8443/path")
	if sameOrigin(left, otherPort) {
		t.Fatal("不同端口必须视为跨源")
	}
	downgrade, _ := url.Parse("http://x.com/path")
	if sameOrigin(left, downgrade) {
		t.Fatal("HTTPS 降级到 HTTP 必须视为跨源")
	}
}

func TestCheckRedirectAllowsSameOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = checkRedirect
	response, err := client.Get(server.URL + "/start")
	if err != nil {
		t.Fatalf("same-origin redirect should be allowed: %v", err)
	}
	response.Body.Close()
}

func TestCheckRedirectRejectsCrossOrigin(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("target"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client := source.Client()
	client.CheckRedirect = checkRedirect
	response, err := client.Get(source.URL)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "跨域重定向") {
		t.Fatalf("cross-origin redirect error = %v, want 跨域重定向", err)
	}
}

func TestCheckRedirectRejectsHTTPSDowngrade(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("target"))
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client := source.Client()
	client.CheckRedirect = checkRedirect
	response, err := client.Get(source.URL)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "HTTPS 降级重定向") {
		t.Fatalf("https downgrade redirect error = %v, want HTTPS 降级重定向", err)
	}
}

func TestCheckRedirectRejectsTooManyHops(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = checkRedirect
	response, err := client.Get(server.URL + "/loop")
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "重定向次数过多") {
		t.Fatalf("redirect loop error = %v, want 重定向次数过多", err)
	}
}
