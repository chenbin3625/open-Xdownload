// Package httpx 提供所有对外 HTTP 调用共享的、带 SSRF 拨号防护的 Transport/Client。
//
// 目的：
//   - 统一代理注入（M1）：单推解析、媒体下载、WebDAV 上传、X GraphQL 客户端共用同一套
//     代理解析逻辑，避免"timeline 走代理、单推解析/WebDAV 上传不走代理"的不一致。
//   - 下沉链路本地地址防护（S2）：任何拨号（SMB/WebDAV/下载/API）在建立连接前都会检查
//     目标 IP，拒绝 169.254.x.x 等链路本地地址（云元数据 SSRF 的主要目标），而不再局限于
//     /api/storage/test 一个端点。
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// blockedTargetError 是拨号被链路本地地址防护拦截时返回的错误，便于调用方统一识别。
type blockedTargetError struct {
	host string
}

// ValidateProxyURL 校验代理地址的格式。空值表示使用环境变量代理，不视为错误。
func ValidateProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("代理地址无效: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("代理协议不支持: %s", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return errors.New("代理地址缺少主机名")
	}
	return nil
}

func (e *blockedTargetError) Error() string {
	return fmt.Sprintf("拒绝访问链路本地地址: %s", e.host)
}

// IsBlockedTargetError 报告 err 是否由链路本地地址防护拦截。
func IsBlockedTargetError(err error) bool {
	var target *blockedTargetError
	return errors.As(err, &target)
}

// BlockedIP 报告 ip 是否为链路本地地址（169.254.0.0/16 等）。与 httpapi 存储目标
// 校验保持一致；环回地址与私网地址（NAS/SMB 常见于私网）不拦截。
func BlockedIP(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// lookupIPs 把 host 解析为 IP 列表；host 本身是 IP 字面量时直接返回。
func lookupIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, address.IP)
	}
	return ips, nil
}

// guardedDial 在拨号前校验目标 IP，拒绝链路本地地址。host:port 形式的 address 输入。
func guardedDial(ctx context.Context, network string, address string, timeout time.Duration) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := lookupIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	blocked := make([]net.IP, 0, len(ips))
	allowed := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if BlockedIP(ip) {
			blocked = append(blocked, ip)
		} else {
			allowed = append(allowed, ip)
		}
	}
	if len(allowed) == 0 {
		return nil, &blockedTargetError{host: host}
	}
	dialer := &net.Dialer{Timeout: timeout}
	var lastErr error
	for _, ip := range allowed {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		return nil, &blockedTargetError{host: host}
	}
	return nil, lastErr
}

// ResolveBlocked 用于不支持 http.Transport 的拨号路径（如 SMB 手动 dial），
// 在真正建立连接前校验 host 是否解析出链路本地地址。
func ResolveBlocked(ctx context.Context, host string) error {
	if strings.TrimSpace(host) == "" {
		return nil
	}
	ips, err := lookupIPs(ctx, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if BlockedIP(ip) {
			return &blockedTargetError{host: host}
		}
	}
	return nil
}

// DialGuarded 是给 SMB 等手动拨号路径使用的带防护拨号器。
func DialGuarded(ctx context.Context, network string, address string, timeout time.Duration) (net.Conn, error) {
	return guardedDial(ctx, network, address, timeout)
}

// guardDialContext 是 http.Transport.DialContext 的拦截实现（附加稳定超时与 KeepAlive）。
func guardDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return guardedDial(ctx, network, address, 30*time.Second)
}

// maxProxyTransports 限制缓存中的代理 Transport 数量，防止反复切换代理配置时连接无界累积。
const maxProxyTransports = 8

var (
	transportsMu sync.Mutex
	transports   = map[string]*http.Transport{}
)

// Transport 返回按 proxyURL 缓存的 Transport。
//   - proxyURL 非空：使用该代理（含内嵌凭据），经 http.ProxyURL。
//   - proxyURL 为空：使用 http.ProxyFromEnvironment（尊重环境变量代理，同 Go 默认行为）。
//
// 无论哪种配置，底层拨号都经过链路本地地址防护。
func Transport(proxyURL string) (*http.Transport, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if err := ValidateProxyURL(proxyURL); err != nil {
		return nil, err
	}
	key := "\x00" + proxyURL
	transportsMu.Lock()
	defer transportsMu.Unlock()
	if t, ok := transports[key]; ok {
		return t, nil
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           guardDialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	if len(transports) >= maxProxyTransports {
		for k, t := range transports {
			t.CloseIdleConnections()
			delete(transports, k)
			break
		}
	}
	transports[key] = transport
	return transport, nil
}

// Client 返回一个复用 Transport 的 http.Client，携带指定超时与（可选的）代理配置。
func Client(proxyURL string, timeout time.Duration) *http.Client {
	transport, err := Transport(proxyURL)
	if err != nil || transport == nil {
		return &http.Client{Timeout: timeout, Transport: errorRoundTripper{err: err}}
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

type errorRoundTripper struct {
	err error
}

func (t errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	if t.err != nil {
		return nil, t.err
	}
	return nil, errors.New("HTTP Transport 初始化失败")
}
