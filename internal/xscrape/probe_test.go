package xscrape

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func TestProbeRules(t *testing.T) {
	type state struct {
		status      int
		contentType string
		body        string
	}
	var mu sync.Mutex
	current := state{status: http.StatusOK, contentType: "application/json", body: `{"users":[]}`}
	var lastRequest *http.Request
	var lastCookies map[string]string

	h := newHarness(t, func(h *testHarness, w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		response := current
		lastRequest = r.Clone(context.Background())
		lastCookies = map[string]string{}
		for _, cookie := range r.Cookies() {
			lastCookies[cookie.Name] = cookie.Value
		}
		mu.Unlock()
		if response.contentType != "" {
			w.Header().Set("Content-Type", response.contentType)
		}
		w.WriteHeader(response.status)
		_, _ = w.Write([]byte(response.body))
	})

	run := func(next state) xapi.HomeProbeResult {
		mu.Lock()
		current = next
		mu.Unlock()
		return h.engine.probe(context.Background(), xapi.AccountCredentials{
			AuthToken:    "auth-token-value",
			CSRFToken:    "csrf-token-value",
			ExtraCookies: map[string]string{"twid": "u%3D1", "lang": "en"},
			UserAgent:    "Custom UA",
		})
	}

	t.Run("valid prefers twid entry", func(t *testing.T) {
		result := run(state{status: http.StatusOK, contentType: "application/json", body: `{"users":[
			{"user_id":"2","screen_name":"other","is_auth_valid":true,"is_suspended":false},
			{"user_id":"1","screen_name":"wanted","is_auth_valid":true,"is_suspended":false}]}`})
		if result.Err != nil || result.Unauthorized || result.ScreenName != "wanted" {
			t.Fatalf("result = %+v", result)
		}
		mu.Lock()
		defer mu.Unlock()
		if lastCookies["auth_token"] != "auth-token-value" || lastCookies["ct0"] != "csrf-token-value" || lastCookies["twid"] != "u%3D1" || lastCookies["lang"] != "en" {
			t.Fatalf("cookies = %v", lastCookies)
		}
		if lastRequest.Header.Get("Authorization") != "Bearer "+xapi.GuestBearer() {
			t.Fatalf("authorization = %q", lastRequest.Header.Get("Authorization"))
		}
		if lastRequest.Header.Get("X-Csrf-Token") != "csrf-token-value" || lastRequest.Header.Get("User-Agent") != "Custom UA" {
			t.Fatalf("headers = %v", lastRequest.Header)
		}
		if lastRequest.Header.Get("X-Twitter-Auth-Type") != "OAuth2Session" || lastRequest.Header.Get("X-Twitter-Active-User") != "yes" {
			t.Fatalf("missing twitter session headers: %v", lastRequest.Header)
		}
	})

	t.Run("suspended entry", func(t *testing.T) {
		result := run(state{status: http.StatusOK, contentType: "application/json", body: `{"users":[{"user_id":"1","screen_name":"wanted","is_auth_valid":true,"is_suspended":true}]}`})
		if !result.Unauthorized || result.Err == nil || !strings.Contains(result.Err.Error(), "封禁") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("no valid entry is undecided", func(t *testing.T) {
		result := run(state{status: http.StatusOK, contentType: "application/json", body: `{"users":[{"user_id":"1","screen_name":"wanted","is_auth_valid":false}]}`})
		if result.Unauthorized || result.Err == nil {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("missing screen name is undecided", func(t *testing.T) {
		result := run(state{status: http.StatusOK, contentType: "application/json", body: `{"users":[{"user_id":"1","is_auth_valid":true}]}`})
		if result.Unauthorized || result.Err == nil {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("401 is unauthorized", func(t *testing.T) {
		result := run(state{status: http.StatusUnauthorized, contentType: "application/json", body: `{"errors":[{"message":"Could not authenticate you","code":32}]}`})
		if !result.Unauthorized || result.Err == nil || !strings.Contains(result.Err.Error(), "32") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("403 with code 32 is unauthorized", func(t *testing.T) {
		result := run(state{status: http.StatusForbidden, contentType: "application/json", body: `{"errors":[{"code":32}]}`})
		if !result.Unauthorized {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("403 with code 353 is csrf mismatch", func(t *testing.T) {
		result := run(state{status: http.StatusForbidden, contentType: "application/json", body: `{"code":353,"message":"This request requires a matching csrf cookie and header."}`})
		if !result.Unauthorized || result.Err == nil || !strings.Contains(result.Err.Error(), "353") || !strings.Contains(result.Err.Error(), "ct0") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("429 is undecided", func(t *testing.T) {
		result := run(state{status: http.StatusTooManyRequests, contentType: "application/json", body: `{}`})
		if result.Unauthorized || result.Err == nil {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("404 is undecided", func(t *testing.T) {
		result := run(state{status: http.StatusNotFound, contentType: "application/json", body: `{"errors":[{"code":34}]}`})
		if result.Unauthorized || result.Err == nil {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("500 is undecided", func(t *testing.T) {
		result := run(state{status: http.StatusInternalServerError, contentType: "application/json", body: `{"error":"boom"}`})
		if result.Unauthorized || result.Err == nil {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("network error is undecided", func(t *testing.T) {
		mu.Lock()
		current = state{status: http.StatusOK, contentType: "application/json", body: `{}`}
		mu.Unlock()
		h.server.Close()
		result := h.engine.probe(context.Background(), xapi.AccountCredentials{AuthToken: "a", CSRFToken: "c"})
		if result.Unauthorized || result.Err == nil {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestTwidUserID(t *testing.T) {
	for _, testCase := range []struct {
		raw  string
		want string
	}{
		{"u%3D12345", "12345"},
		{"u=12345", "12345"},
		{"v=1&u%3D678", "678"},
		{"garbage", ""},
		{"", ""},
	} {
		if got := twidUserID(map[string]string{"twid": testCase.raw}); got != testCase.want {
			t.Errorf("twidUserID(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
}

func TestProbeRejectionMessages(t *testing.T) {
	cases := map[int]string{
		32:  "32",
		64:  "封禁",
		89:  "失效",
		215: "无效",
		326: "锁定",
	}
	for code, want := range cases {
		err := probeRejection(code, "", &response{status: http.StatusUnauthorized})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("probeRejection(%d) = %v, want %q", code, err, want)
		}
	}
	if err := probeRejection(0, "", &response{status: http.StatusUnauthorized}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("probeRejection(0) = %v", err)
	}
	if got := fmt.Sprint(probeRejection(64, "Account suspended", &response{status: http.StatusUnauthorized})); got == "" {
		t.Error("empty message")
	}
}
