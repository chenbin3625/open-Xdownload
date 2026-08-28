package httpx

import (
	"net"
	"testing"
)

func TestBlockedIPLinkLocal(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"169.254.169.254", true}, // 云元数据
		{"169.254.0.1", true},
		{"fe80::1", true},
		{"127.0.0.1", false},    // 环回（测试服务器）不拦
		{"192.168.1.10", false}, // 私网地址不拦
		{"10.0.0.5", false},
		{"8.8.8.8", false},
	}
	for _, tt := range cases {
		ip := net.ParseIP(tt.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", tt.ip)
		}
		if got := BlockedIP(ip); got != tt.blocked {
			t.Fatalf("BlockedIP(%s) = %v, want %v", tt.ip, got, tt.blocked)
		}
	}
}

func TestValidateProxyURL(t *testing.T) {
	valid := []string{"", "http://127.0.0.1:7890", "https://proxy.example:443", "socks5://127.0.0.1:1080", "socks5h://user:pass@proxy.example:1080"}
	for _, raw := range valid {
		if err := ValidateProxyURL(raw); err != nil {
			t.Errorf("ValidateProxyURL(%q) = %v, want nil", raw, err)
		}
	}
	invalid := []string{"http://", "ftp://proxy.example:21", "://bad", "http:///missing-host"}
	for _, raw := range invalid {
		if err := ValidateProxyURL(raw); err == nil {
			t.Errorf("ValidateProxyURL(%q) = nil, want error", raw)
		}
	}
}
