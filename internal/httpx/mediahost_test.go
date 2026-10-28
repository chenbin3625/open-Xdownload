package httpx

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestIsTwimgHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"pbs.twimg.com", true},
		{"video.twimg.com", true},
		{"twimg.com", true},
		{"TWIMG.COM", true},
		{"pbs.twimg.com.", true},
		{" pbs.twimg.com ", true},
		{"evil-twimg.com", false},
		{"twimg.com.evil.example", false},
		{"notwimg.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsTwimgHost(tc.host); got != tc.want {
			t.Fatalf("IsTwimgHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// 走代理时拨号层只能看到代理地址，因此 guard 必须在请求层拦掉"目标主机字面量是
// 链路本地 IP"的请求（云元数据 SSRF 最常见的形式）。
func TestGuardedRoundTripperRejectsLiteralLinkLocalTarget(t *testing.T) {
	called := false
	base := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected dial")
	})
	client := &http.Client{Transport: GuardedRoundTripper(base)}

	request, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = client.Do(request)
	if !IsBlockedTargetError(err) {
		t.Fatalf("err = %v, want blockedTargetError", err)
	}
	if called {
		t.Fatal("guarded round tripper must not delegate blocked targets")
	}

	// 普通目标仍然放行到下层 transport。
	request, err = http.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/health", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := client.Do(request); err == nil || !strings.Contains(err.Error(), "unexpected dial") {
		t.Fatalf("err = %v, want the delegated dial error", err)
	}
}
