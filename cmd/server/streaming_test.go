package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 确认 ReadTimeout 不会掐断长连接流式响应（SSE）。
func TestReadTimeoutDoesNotKillStreamingResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter 不支持 Flush")
			return
		}
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: tick-%d\n\n", i)
			flusher.Flush()
			time.Sleep(150 * time.Millisecond)
		}
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := newHTTPServer(listener.Addr().String(), handler)
	if server.ReadTimeout <= 0 {
		t.Fatal("本测试的前提是 ReadTimeout 已设置")
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	response, err := http.Get("http://" + listener.Addr().String() + "/api/events")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()

	reader := bufio.NewReader(response.Body)
	for i := 0; i < 3; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("第 %d 条事件读取失败（流被提前关闭？）: %v", i, err)
		}
		want := fmt.Sprintf("data: tick-%d\n", i)
		if line != want {
			t.Fatalf("got %q, want %q", line, want)
		}
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatalf("读取空行失败: %v", err)
		}
	}
}

func TestWebAppSecurityHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	setWebAppSecurityHeaders(recorder)
	header := recorder.Header()

	for key, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	} {
		if got := header.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	csp := header.Get("Content-Security-Policy")
	// antd 的 CSS-in-JS 必须能注入 <style>，Google Fonts 必须可达，否则页面会明显崩坏。
	for _, required := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"'unsafe-inline'",
		"https://fonts.googleapis.com",
		"https://fonts.gstatic.com",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, required) {
			t.Errorf("CSP 缺少 %q: %s", required, csp)
		}
	}
	// 可执行脚本不得放开 unsafe-inline / unsafe-eval。
	scriptDirective := ""
	for _, part := range strings.Split(csp, ";") {
		if strings.Contains(part, "script-src") {
			scriptDirective = part
		}
	}
	if strings.Contains(scriptDirective, "unsafe-inline") || strings.Contains(scriptDirective, "unsafe-eval") {
		t.Errorf("script-src 不应放开 unsafe-*: %s", scriptDirective)
	}
}
