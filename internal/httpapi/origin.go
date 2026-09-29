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
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if isStateChanging(r.Method) {
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
				writeError(w, http.StatusForbidden, errors.New("拒绝跨站请求"))
				return
			}
			// 沙箱 iframe、data: 页面等不透明来源的 Origin 是 "null"。SPA 自身永远不会发出
			// 这种请求；不带 Sec-Fetch-Site 的旧浏览器上，这是拦住无 body 简单 POST 的唯一一环。
			if origin == "null" {
				writeError(w, http.StatusForbidden, errors.New("拒绝不透明来源的请求"))
				return
			}
		}
		if origin != "" && origin != "null" {
			if !originMatchesHost(origin, r.Host) {
				writeError(w, http.StatusForbidden, errors.New("拒绝跨源请求"))
				return
			}
		}
		if !hostAllowed(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, errors.New("Host 不在允许列表内：通过域名访问时请把该域名加入 OPEN_XDOWNLOAD_ALLOWED_HOSTS"))
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

// hostAllowed 防御 DNS rebinding：攻击者把自己的域名解析到 127.0.0.1 后，页面发出的
// 请求 Origin 与 Host 一致，Origin 校验拦不住，只能校验 Host。rebinding 只能借助域名
// 发起，因此未设置 OPEN_XDOWNLOAD_ALLOWED_HOSTS 时只放行 IP 字面量与 localhost：局域网
// 按 IP 访问、Docker 端口映射不受影响；通过域名访问（反向代理、mDNS 主机名）需把域名
// 加入白名单。设置白名单后只放行列表内的 Host。
func hostAllowed(host string) bool {
	raw := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_ALLOWED_HOSTS"))
	if raw == "" {
		name := strings.Trim(hostOnly(host), "[]")
		return strings.EqualFold(name, "localhost") || net.ParseIP(name) != nil
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
