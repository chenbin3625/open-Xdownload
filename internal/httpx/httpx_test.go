package httpx

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
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
		{"192.168.1.10", false}, // 私网 NAS/SMB 不拦
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

func TestResolveBlockedRejectsLinkLocalLiteral(t *testing.T) {
	err := ResolveBlocked(context.Background(), "169.254.169.254")
	if !IsBlockedTargetError(err) {
		t.Fatalf("ResolveBlocked(link-local) err = %v, want blocked error", err)
	}
	if err := ResolveBlocked(context.Background(), "8.8.8.8"); err != nil {
		t.Fatalf("ResolveBlocked(public) err = %v", err)
	}
	if err := ResolveBlocked(context.Background(), ""); err != nil {
		t.Fatalf("ResolveBlocked(empty) err = %v", err)
	}
}

func TestDialGuardedRejectsLinkLocalBeforeConnecting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := DialGuarded(ctx, "tcp", net.JoinHostPort("169.254.169.254", strconv.Itoa(80)), 3*time.Second)
	if !IsBlockedTargetError(err) {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("DialGuarded(link-local) err = %v, want blocked error", err)
	}
	if conn != nil {
		_ = conn.Close()
	}
}

func TestBlockedTargetErrorMessageReadable(t *testing.T) {
	err := ResolveBlocked(context.Background(), "169.254.169.254")
	if err == nil || !strings.Contains(err.Error(), "链路本地") {
		t.Fatalf("error message = %v, want readable link-local message", err)
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
