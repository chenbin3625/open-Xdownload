package httpx

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCopyLimitedRejectsOversizedBody(t *testing.T) {
	var out bytes.Buffer
	// io.LimitReader 在上限处静默截断并返回 nil：截断的图片会被当成完整文件发布并永久缓存。
	_, err := CopyLimited(&out, bytes.NewReader(bytes.Repeat([]byte{'x'}, 11)), 10)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	out.Reset()
	n, err := CopyLimited(&out, bytes.NewReader(bytes.Repeat([]byte{'x'}, 10)), 10)
	if err != nil || n != 10 || out.Len() != 10 {
		t.Fatalf("exact-limit copy: n=%d err=%v len=%d", n, err, out.Len())
	}
}

func TestRestrictRedirectsRejectsForeignHosts(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("internal"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/secret", http.StatusFound)
	}))
	defer origin.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	RestrictRedirects(client, func(host string) bool { return false })
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL, nil)
	response, err := client.Do(request)
	if err == nil {
		response.Body.Close()
		t.Fatal("redirect to a non-allowed host must be refused")
	}
}

func TestCopyLimitedHandlesNegativeAndMaxInt64(t *testing.T) {
	var out bytes.Buffer
	if _, err := CopyLimited(&out, bytes.NewReader([]byte("a")), -1); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("limit=-1 err = %v, want ErrBodyTooLarge", err)
	}
	if _, err := CopyLimited(&out, bytes.NewReader(nil), math.MinInt64); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("limit=MinInt64 err = %v, want ErrBodyTooLarge", err)
	}
	out.Reset()
	n, err := CopyLimited(&out, bytes.NewReader([]byte("hello")), math.MaxInt64)
	if err != nil || n != 5 || out.String() != "hello" {
		t.Fatalf("limit=MaxInt64 got n=%d err=%v out=%q", n, err, out.String())
	}
}

func TestRestrictRedirectsRejectsIntermediateHTTPSDowngradeAndGuardsNil(t *testing.T) {
	RestrictRedirects(nil, nil)

	client := &http.Client{}
	RestrictRedirects(client, nil)
	req, _ := http.NewRequest(http.MethodGet, "https://pbs.twimg.com/a.jpg", nil)
	if err := client.CheckRedirect(req, nil); err == nil {
		t.Fatal("nil allowedHost must reject redirect")
	}

	RestrictRedirects(client, func(string) bool { return true })
	hop0, _ := http.NewRequest(http.MethodGet, "http://pbs.twimg.com/start", nil)
	hop1, _ := http.NewRequest(http.MethodGet, "https://pbs.twimg.com/secure", nil)
	hop2, _ := http.NewRequest(http.MethodGet, "http://pbs.twimg.com/downgrade", nil)
	if err := client.CheckRedirect(hop2, []*http.Request{hop0, hop1}); err == nil {
		t.Fatal("HTTPS->HTTP downgrade after initial HTTP hop must be rejected")
	}
}
