// Package httpx 提供所有对外 HTTP 调用共享的、带 SSRF 拨号防护的 Transport/Client。
//
// 目的：
//   - 统一代理注入（M1）：单推解析、媒体下载、X GraphQL 客户端共用同一套代理解析逻辑，
//     避免"timeline 走代理、单推解析不走代理"的不一致。
//   - 下沉链路本地地址防护（S2）：任何拨号（下载/API）在建立连接前都会检查目标 IP，
//     拒绝 169.254.x.x 等链路本地地址（云元数据 SSRF 的主要目标）。
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
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

var (
	awsIPv6IMDS     = netip.MustParseAddr("fd00:ec2::254")
	alibabaIMDSIPv4 = netip.MustParseAddr("100.100.100.200")
)

// BlockedIP 报告 ip 是否为禁止访问的目标地址：链路本地地址（169.254.0.0/16、fe80::/10）、
// 未指定地址（0.0.0.0、::）、以及云元数据专用地址（AWS IPv6 IMDS fd00:ec2::254、
// 阿里云 IMDS 100.100.100.200）。环回地址与普通私网地址不拦截。
func BlockedIP(ip net.IP) bool {
	if len(ip) == 0 {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if addr, ok := netip.AddrFromSlice(ip); ok {
		unmapped := addr.Unmap()
		if unmapped.IsUnspecified() || unmapped == awsIPv6IMDS || unmapped == alibabaIMDSIPv4 {
			return true
		}
	}
	return false
}

// BlockedHost 报告主机字面量是否为受禁 IP（含 IPv6 Zone、IPv4-mapped IPv6）或
// 非规范数值 IPv4（如十进制/十六进制/八进制简写），供 S3Client 等调用方复用一致策略。
func BlockedHost(host string) bool {
	return blockedLiteralOrNonCanonicalHost(host)
}

var lookupHostIPAddrs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// lookupIPAddrs 把 host 解析为带 Zone 的 IP 列表；host 本身是 IP 字面量时直接返回。
func lookupIPAddrs(ctx context.Context, host string) ([]net.IPAddr, error) {
	if addr, err := netip.ParseAddr(strings.TrimSuffix(host, ".")); err == nil {
		unmapped := addr.Unmap()
		return []net.IPAddr{{IP: net.IP(unmapped.AsSlice()), Zone: unmapped.Zone()}}, nil
	}
	return lookupHostIPAddrs(ctx, host)
}

func partitionIPAddrs(network string, addrs []net.IPAddr) ([]net.IPAddr, []net.IPAddr) {
	if len(addrs) <= 1 || strings.HasSuffix(network, "4") || strings.HasSuffix(network, "6") {
		return addrs, nil
	}
	firstIsV4 := addrs[0].IP.To4() != nil
	var primaries, fallbacks []net.IPAddr
	for _, addr := range addrs {
		if (addr.IP.To4() != nil) == firstIsV4 {
			primaries = append(primaries, addr)
		} else {
			fallbacks = append(fallbacks, addr)
		}
	}
	return primaries, fallbacks
}

func dialSerial(ctx context.Context, dialer *net.Dialer, network string, addrs []net.IPAddr, port string, totalBudget time.Duration) (net.Conn, error) {
	var lastErr error
	for i, addr := range addrs {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		attemptCtx := ctx
		var cancelAttempt context.CancelFunc
		remainingAddrs := len(addrs) - i
		if remainingAddrs > 1 {
			attemptTimeout := 10 * time.Second
			if totalBudget > 0 {
				if perAddr := totalBudget / time.Duration(len(addrs)); perAddr < attemptTimeout {
					attemptTimeout = perAddr
				}
			}
			if deadline, ok := ctx.Deadline(); ok {
				remaining := time.Until(deadline)
				if remaining <= 0 {
					if lastErr != nil {
						return nil, lastErr
					}
					return nil, context.DeadlineExceeded
				}
				if share := remaining / time.Duration(remainingAddrs); share < attemptTimeout {
					attemptTimeout = share
				}
				if attemptTimeout < 50*time.Millisecond && remaining > 50*time.Millisecond {
					attemptTimeout = 50 * time.Millisecond
				}
			} else if attemptTimeout < 50*time.Millisecond {
				attemptTimeout = 50 * time.Millisecond
			}
			if attemptTimeout > 0 {
				attemptCtx, cancelAttempt = context.WithTimeout(ctx, attemptTimeout)
			}
		}
		conn, err := dialer.DialContext(attemptCtx, network, net.JoinHostPort(addr.String(), port))
		if cancelAttempt != nil {
			cancelAttempt()
		}
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// guardDialSocketControl 在底层 socket connect 前做最终 IP 校验，作为 DNS 解析过滤之后的纵深兜底。
func guardDialSocketControl(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if BlockedIP(net.IP(addr.Unmap().AsSlice())) {
			return &blockedTargetError{host: host}
		}
	}
	return nil
}

// guardedDial 在拨号前校验目标 IP，拒绝链路本地地址。host:port 形式的 address 输入。
// 支持 IPv6 Zone 保留、单次尝试超时上限及 Happy Eyeballs 双栈回退。
func guardedDial(ctx context.Context, network string, address string, timeout time.Duration) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := lookupIPAddrs(ctx, host)
	if err != nil {
		return nil, err
	}
	allowed := make([]net.IPAddr, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IP != nil && !BlockedIP(addr.IP) {
			allowed = append(allowed, addr)
		}
	}
	if len(allowed) == 0 {
		return nil, &blockedTargetError{host: host}
	}
	dialCtx := ctx
	var cancelDial context.CancelFunc
	if timeout > 0 {
		dialCtx, cancelDial = context.WithTimeout(ctx, timeout)
		defer cancelDial()
	}
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control:   guardDialSocketControl,
	}
	primaries, fallbacks := partitionIPAddrs(network, allowed)
	if len(fallbacks) == 0 {
		conn, err := dialSerial(dialCtx, dialer, network, primaries, port, timeout)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}

	fallbackDelay := 300 * time.Millisecond
	if timeout > 0 && fallbackDelay > timeout/2 {
		fallbackDelay = timeout / 2
		if fallbackDelay < 10*time.Millisecond {
			fallbackDelay = 10 * time.Millisecond
		}
	}
	raceCtx, cancelRace := context.WithCancel(dialCtx)
	defer cancelRace()

	type dialResult struct {
		conn    net.Conn
		err     error
		primary bool
	}
	results := make(chan dialResult, 2)
	primaryFailed := make(chan struct{})

	go func() {
		conn, err := dialSerial(raceCtx, dialer, network, primaries, port, timeout)
		if conn != nil {
			cancelRace()
		} else {
			close(primaryFailed)
		}
		results <- dialResult{conn: conn, err: err, primary: true}
	}()

	go func() {
		timer := time.NewTimer(fallbackDelay)
		defer timer.Stop()
		select {
		case <-raceCtx.Done():
			results <- dialResult{err: raceCtx.Err(), primary: false}
			return
		case <-primaryFailed:
		case <-timer.C:
		}
		if raceCtx.Err() != nil {
			results <- dialResult{err: raceCtx.Err(), primary: false}
			return
		}
		conn, err := dialSerial(raceCtx, dialer, network, fallbacks, port, timeout)
		if conn != nil {
			cancelRace()
		}
		results <- dialResult{conn: conn, err: err, primary: false}
	}()

	var won net.Conn
	var primaryErr, fallbackErr error
	for range 2 {
		res := <-results
		if res.conn != nil {
			if won == nil {
				won = res.conn
				cancelRace()
			} else {
				_ = res.conn.Close()
			}
		} else if res.primary {
			primaryErr = res.err
		} else {
			fallbackErr = res.err
		}
	}
	if won != nil {
		return won, nil
	}
	if primaryErr != nil {
		return nil, primaryErr
	}
	return nil, fallbackErr
}

// guardDialContext 是 http.Transport.DialContext 的拦截实现（附加稳定超时与 KeepAlive）。
func guardDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return guardedDial(ctx, network, address, 30*time.Second)
}

// maxProxyTransports 限制缓存中的代理 Transport 数量，防止反复切换代理配置时连接无界累积。
const maxProxyTransports = 64

type transportEntry struct {
	transport *http.Transport
	lastUsed  time.Time
}

var (
	transportsMu sync.Mutex
	transports   = map[string]*transportEntry{}
)

// Transport 返回按 proxyURL 缓存的 Transport。
//   - proxyURL 非空：使用该代理（含内嵌凭据），经 http.ProxyURL。
//   - proxyURL 为空：使用 http.ProxyFromEnvironment（尊重环境变量代理，同 Go 默认行为）。
//
// 链路本地地址防护的边界（重要，别把注释当成"所有情况都安全"）：
//   - 直连：DialContext 会先解析目标 IP 再拨号，169.254.x.x 等链路本地地址被拒绝；
//   - 走代理：DialContext 拨的是**代理地址**，真实目标由代理解析并连接，拨号层
//     看不到它。此时代理请求的目标主机由 guardedRoundTripper 做字面量 IP 检查
//     （见 Client），域名解析后的链路本地地址无法在本地可靠拦截——这是部署方
//     选择代理时接受的信任边界。
func Transport(proxyURL string) (*http.Transport, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if err := ValidateProxyURL(proxyURL); err != nil {
		return nil, err
	}
	key := "\x00" + proxyURL
	transportsMu.Lock()
	defer transportsMu.Unlock()
	if entry, ok := transports[key]; ok {
		entry.lastUsed = time.Now()
		return entry.transport, nil
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           guardDialContext,
		ForceAttemptHTTP2:     true,
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
		var oldestKey string
		var oldestTime time.Time
		for k, entry := range transports {
			if oldestKey == "" || entry.lastUsed.Before(oldestTime) {
				oldestKey = k
				oldestTime = entry.lastUsed
			}
		}
		if oldestKey != "" {
			transports[oldestKey].transport.CloseIdleConnections()
			delete(transports, oldestKey)
		}
	}
	transports[key] = &transportEntry{transport: transport, lastUsed: time.Now()}
	return transport, nil
}

// Client 返回一个复用 Transport 的 http.Client，携带指定超时与（可选的）代理配置。
//
// 返回值外面再套一层 guardedRoundTripper：直连时拨号层已经拦截链路本地地址，
// 走代理时拨号层只能看到代理地址，这层补上"目标主机字面量是链路本地 IP"的拦截。
func Client(proxyURL string, timeout time.Duration) *http.Client {
	transport, err := Transport(proxyURL)
	if err != nil || transport == nil {
		return &http.Client{Timeout: timeout, Transport: errorRoundTripper{err: err}}
	}
	return &http.Client{Timeout: timeout, Transport: guardedRoundTripper{base: transport}}
}

// GuardedRoundTripper 给任意 RoundTripper 套上"目标主机字面量为链路本地 IP 时拒绝"
// 的防护。供不使用 Client() 的调用方（例如自带 Transport 的下载器）复用。
func GuardedRoundTripper(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return guardedRoundTripper{base: base}
}

type guardedRoundTripper struct {
	base http.RoundTripper
}

func (t guardedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request != nil && request.URL != nil {
		host := request.URL.Hostname()
		if blockedLiteralOrNonCanonicalHost(host) {
			return nil, &blockedTargetError{host: host}
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(request)
}

func blockedLiteralOrNonCanonicalHost(host string) bool {
	trimmed := strings.TrimSpace(host)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		trimmed = trimmed[1 : len(trimmed)-1]
	}
	trimmed = strings.TrimSuffix(trimmed, ".")
	if trimmed == "" {
		return false
	}
	if addr, err := netip.ParseAddr(trimmed); err == nil {
		unmapped := addr.Unmap()
		return BlockedIP(net.IP(unmapped.AsSlice()))
	}
	return isNonCanonicalNumericIPv4(trimmed)
}

func isNonCanonicalNumericIPv4(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if !isNumericIPv4Part(part) {
			return false
		}
	}
	return true
}

func isNumericIPv4Part(part string) bool {
	if part == "" {
		return false
	}
	if strings.HasPrefix(part, "0x") || strings.HasPrefix(part, "0X") {
		if len(part) <= 2 {
			return false
		}
		for i := 2; i < len(part); i++ {
			c := part[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(part); i++ {
		if part[i] < '0' || part[i] > '9' {
			return false
		}
	}
	return true
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
