package xclient

import (
	"net/http"
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

func TestSameHostIgnoresPortAndCase(t *testing.T) {
	if !sameHost("X.com:443", "x.com") {
		t.Fatal("同主机（大小写/端口不同）应视为相同")
	}
	if sameHost("x.com", "evil.example") {
		t.Fatal("不同主机必须视为跨域，否则 ct0 会随重定向泄漏")
	}
}
