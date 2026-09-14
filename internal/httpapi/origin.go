package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// guardCrossOrigin 阻止跨站请求伪造：本服务无内置鉴权，任何页面都能向
// 127.0.0.1:8787 提交表单。decodeJSON 的 Content-Type 检查挡住了带 JSON body 的
// 接口，但无 body 的变更型 POST（取消/重试/回填/立即运行）不经过它，浏览器的
// 简单请求即可直接驱动。
//
// 策略：只在 Origin 存在且与 Host 不一致时拒绝，而不是"缺 Origin 就拒绝"。
// 浏览器对跨站非 GET 请求必定携带 Origin，因此这足以挡住 CSRF；而 curl、脚本
// 等非浏览器客户端不带 Origin，放行可保持自动化场景可用。
// Sec-Fetch-Site 作为补充：现代浏览器一定会带，能覆盖 Origin 被剥离的情况。
func guardCrossOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStateChanging(r.Method) {
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
				writeError(w, http.StatusForbidden, errors.New("拒绝跨站请求"))
				return
			}
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && origin != "null" {
			if !originMatchesHost(origin, r.Host) {
				writeError(w, http.StatusForbidden, errors.New("拒绝跨源请求"))
				return
			}
		}
		if !hostAllowed(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, errors.New("Host 不在允许列表内"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func originMatchesHost(origin string, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if strings.EqualFold(parsed.Host, host) || strings.EqualFold(hostOnly(parsed.Host), hostOnly(host)) {
		return true
	}
	// vite dev server（:5173）把浏览器请求代理到 :8787，Origin 仍是 localhost:5173
	// 而 Host 变成 127.0.0.1:8787，端口不同但同为回环。放行这种组合：攻击页面
	// 的 Origin 不可能是回环地址，除非攻击者已能在本机提供服务——那时 CSRF 已
	// 不是最需要担心的问题。
	return isLoopbackHost(parsed.Host) && isLoopbackHost(host)
}

func isLoopbackHost(hostport string) bool {
	host := hostOnly(hostport)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// hostAllowed 可选的 Host 白名单，用于防御 DNS rebinding（攻击者把自己的域名
// 解析到 127.0.0.1，从而绕过 Origin 之外的同源假设）。默认放行全部，避免破坏
// 既有的反向代理部署；设置 OPEN_XDOWNLOAD_ALLOWED_HOSTS 后才生效。
func hostAllowed(host string) bool {
	raw := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_ALLOWED_HOSTS"))
	if raw == "" {
		return true
	}
	candidate := hostOnly(host)
	for _, allowed := range strings.Split(raw, ",") {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if strings.EqualFold(candidate, hostOnly(allowed)) {
			return true
		}
	}
	return false
}
