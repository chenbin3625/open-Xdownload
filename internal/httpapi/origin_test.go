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
			headers: map[string]string{"Origin": "http://example.test", "Sec-Fetch-Site": "same-origin"},
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
				target = "http://example.test/api/jobs"
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

func TestHostAllowlistOptInOnly(t *testing.T) {
	if !hostAllowed("anything.example") {
		t.Fatal("未设置 OPEN_XDOWNLOAD_ALLOWED_HOSTS 时必须放行任意 Host，避免破坏反向代理部署")
	}
	t.Setenv("OPEN_XDOWNLOAD_ALLOWED_HOSTS", "nas.local, 127.0.0.1")
	if !hostAllowed("nas.local:8787") {
		t.Fatal("白名单内的 Host 应放行（忽略端口）")
	}
	if hostAllowed("rebind.evil.example") {
		t.Fatal("白名单外的 Host 应拒绝（DNS rebinding 防御）")
	}
}
