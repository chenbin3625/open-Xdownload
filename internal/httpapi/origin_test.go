package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuardCrossOrigin(t *testing.T) {
	handler := guardCrossOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
		want    int
	}{
		{
			name:    "跨站表单提交被拒（CSRF 主场景）",
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "http://evil.example", "Content-Type": "application/x-www-form-urlencoded"},
			want:    http.StatusForbidden,
		},
		{
			name:    "跨站 Sec-Fetch-Site 被拒（即使没有 Origin）",
			method:  http.MethodPost,
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"},
			want:    http.StatusForbidden,
		},
		{
			name:    "SPA 同源 POST 放行",
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "http://127.0.0.1:8787", "Sec-Fetch-Site": "same-origin"},
			want:    http.StatusNoContent,
		},
		{
			name:    "curl / 脚本无 Origin 放行，保持自动化可用",
			method:  http.MethodPost,
			headers: nil,
			want:    http.StatusNoContent,
		},
		{
			name:    "跨源 GET 仍被拒（响应本就不可跨源读取，但保持一致）",
			method:  http.MethodGet,
			headers: map[string]string{"Origin": "http://evil.example"},
			want:    http.StatusForbidden,
		},
		{
			name:    "普通 GET 放行",
			method:  http.MethodGet,
			headers: nil,
			want:    http.StatusNoContent,
		},
		{
			// 沙箱 iframe / data: 页面发出的请求 Origin 为 "null"；旧浏览器不带
			// Sec-Fetch-Site 时只能靠这一条拦住无 body 的简单 POST。
			name:    "Origin: null 的变更请求被拒",
			method:  http.MethodPost,
			headers: map[string]string{"Origin": "null"},
			want:    http.StatusForbidden,
		},
		{
			name:    "Origin: null 的 GET 放行（响应本就不可跨源读取）",
			method:  http.MethodGet,
			headers: map[string]string{"Origin": "null"},
			want:    http.StatusNoContent,
		},
		{
			name:    "vite dev 代理（localhost:5173 -> 127.0.0.1:8787）放行",
			method:  http.MethodPost,
			target:  "http://127.0.0.1:8787/api/jobs",
			headers: map[string]string{"Origin": "http://localhost:5173"},
			want:    http.StatusNoContent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			if target == "" {
				target = "http://127.0.0.1:8787/api/jobs"
			}
			request := httptest.NewRequest(tc.method, target, nil)
			for key, value := range tc.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("got %d, want %d (body %s)", response.Code, tc.want, response.Body.String())
			}
		})
	}
}

func TestHostAllowedDefaultsToIPLiteralsAndLocalhost(t *testing.T) {
	t.Setenv("OPEN_XDOWNLOAD_ALLOWED_HOSTS", "")
	for _, host := range []string{
		"127.0.0.1:8787",
		"localhost:8787",
		"LOCALHOST",
		"[::1]:8787",
		"192.168.1.10:8787", // 局域网按 IP 访问 / Docker -p 映射
		"10.0.0.5",
	} {
		if !hostAllowed(host) {
			t.Errorf("未配置白名单时应放行 IP 字面量与 localhost: %q", host)
		}
	}
	// DNS rebinding 只能借助攻击者控制的域名发起：未配置白名单时一律拒绝域名 Host。
	for _, host := range []string{
		"rebind.evil.example:8787",
		"evil.example",
		"localhost.evil.example",
		"127.0.0.1.nip.io:8787",
		"",
	} {
		if hostAllowed(host) {
			t.Errorf("未配置白名单时应拒绝域名 Host（DNS rebinding）: %q", host)
		}
	}
}

func TestGuardCrossOriginRejectsDNSRebinding(t *testing.T) {
	t.Setenv("OPEN_XDOWNLOAD_ALLOWED_HOSTS", "")
	handler := guardCrossOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	// rebinding 场景：Origin 与 Host 同为攻击者域名，Sec-Fetch-Site 为 same-origin，
	// Origin 校验本身拦不住，只能靠 Host 校验。
	request := httptest.NewRequest(http.MethodPut, "http://rebind.evil.example:8787/api/config", nil)
	request.Header.Set("Origin", "http://rebind.evil.example:8787")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding request got %d, want %d", response.Code, http.StatusMisdirectedRequest)
	}
}

func TestHostAllowlistExplicitConfig(t *testing.T) {
	t.Setenv("OPEN_XDOWNLOAD_ALLOWED_HOSTS", "nas.local, 127.0.0.1")
	if !hostAllowed("nas.local:8787") {
		t.Fatal("白名单内的 Host 应放行（忽略端口）")
	}
	if hostAllowed("rebind.evil.example") {
		t.Fatal("白名单外的 Host 应拒绝（DNS rebinding 防御）")
	}
}
