package httpx

import (
	"net"
	"testing"
)

// BlockedIP 的边界是刻意划定的：环回与私网必须放行，否则用户在 127.0.0.1 或
// 局域网跑的代理会被自己的防护拦死。本测试把这个取舍固定下来，同时确认云元数据
// 端点（SSRF 最高价值目标）确实在拦截范围内——它属于链路本地，已被覆盖。
func TestBlockedIPBoundary(t *testing.T) {
	blocked := []string{
		"169.254.169.254",        // 云元数据
		"::ffff:169.254.169.254", // IPv4-mapped 形式同样拦截
		"169.254.0.1",
		"fe80::1",
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
