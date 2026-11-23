package httpx

import (
	"net"
	"testing"
)

// BlockedIP 的边界是刻意划定的：环回与私网必须放行，否则用户在 127.0.0.1 或
// 局域网跑的代理会被自己的防护拦死。本测试把这个取舍固定下来，同时确认云元数据
// 端点（链路本地、AWS IPv6 IMDS、阿里云 IMDS）与未指定地址确实在拦截范围内。
func TestBlockedIPBoundary(t *testing.T) {
	blocked := []string{
		"169.254.169.254",        // 云元数据
		"::ffff:169.254.169.254", // IPv4-mapped 形式同样拦截
		"169.254.0.1",
		"fe80::1",
		"ff02::1",
		"fd00:ec2::254",          // AWS IPv6 IMDS
		"100.100.100.200",        // 阿里云 IMDS
		"::ffff:100.100.100.200", // IPv4-mapped 阿里云 IMDS
		"0.0.0.0",                // 未指定 IPv4
		"::",                     // 未指定 IPv6
		"::ffff:0.0.0.0",         // IPv4-mapped 未指定地址
	}
	for _, raw := range blocked {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("测试用例本身无效: %s", raw)
		}
		if !BlockedIP(ip) {
			t.Errorf("%s 应被拦截", raw)
		}
	}

	// 刻意放行：本地/内网代理是受支持的用法。
	allowed := []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "fd00::1"}
	for _, raw := range allowed {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("测试用例本身无效: %s", raw)
		}
		if BlockedIP(ip) {
			t.Errorf("%s 必须放行（本地/内网代理场景）", raw)
		}
	}
}
