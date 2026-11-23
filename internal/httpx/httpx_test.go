package httpx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
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
		{"fd00:ec2::254", true},   // AWS IPv6 IMDS
		{"100.100.100.200", true}, // 阿里云 IMDS
		{"0.0.0.0", true},         // 未指定 IPv4
		{"::", true},              // 未指定 IPv6
		{"127.0.0.1", false},      // 环回（测试服务器）不拦
		{"192.168.1.10", false},   // 私网地址不拦
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

func TestGuardedRoundTripperBlocksZonesUnmappedAndNonCanonicalIPv4(t *testing.T) {
	called := false
	base := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("delegated")
	})
	rt := GuardedRoundTripper(base)
	blockedURLs := []string{
		"http://[fe80::1%25eth0]/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/latest/meta-data/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://100.100.100.200/latest/meta-data/",
		"http://[::ffff:100.100.100.200]/latest/meta-data/",
		"http://0.0.0.0/",
		"http://[::]/",
		"http://169.254.169.254./latest/meta-data/",
		"http://2852039166/latest/meta-data/",
		"http://0xa9fea9fe/latest/meta-data/",
		"http://0251.0376.0251.0376/latest/meta-data/",
		"http://0x64.0x64.0x64.0xc8/latest/meta-data/",
		"http://0144.0144.0144.0310/latest/meta-data/",
		"http://169.254.43518/latest/meta-data/",
		"http://127.1/",
	}
	for _, raw := range blockedURLs {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q): %v", raw, err)
		}
		if _, err := rt.RoundTrip(req); !IsBlockedTargetError(err) {
			t.Fatalf("RoundTrip(%q) err = %v, want blockedTargetError", raw, err)
		}
		if called {
			t.Fatalf("RoundTrip(%q) delegated to base transport", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8080/ok", "http://0x1.example.com/ok"} {
		called = false
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if _, err := rt.RoundTrip(req); err == nil || err.Error() != "delegated" {
			t.Fatalf("RoundTrip(%q) err = %v, want delegated", raw, err)
		}
		if !called {
			t.Fatalf("RoundTrip(%q) did not delegate", raw)
		}
	}
}

func TestBlockedHostHandlesBracketsZonesAndNonCanonicalIPv4(t *testing.T) {
	for _, host := range []string{
		"fd00:ec2::254",
		"[fd00:ec2::254]",
		"fd00:ec2::254%eth0",
		"100.100.100.200",
		"::ffff:100.100.100.200",
		"0.0.0.0",
		"::",
		"0xa9fea9fe",
		"0251.0376.0251.0376",
		"0x64.0x64.0x64.0xc8",
	} {
		if !BlockedHost(host) {
			t.Errorf("BlockedHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"127.0.0.1", "::1", "[::1]", "s3.example.com"} {
		if BlockedHost(host) {
			t.Errorf("BlockedHost(%q) = true, want false", host)
		}
	}
}

func TestGuardedDialBlocksResolvedIMDSAndSocketControl(t *testing.T) {
	origLookup := lookupHostIPAddrs
	t.Cleanup(func() { lookupHostIPAddrs = origLookup })

	for _, ipStr := range []string{"fd00:ec2::254", "100.100.100.200", "0.0.0.0", "::", "169.254.169.254"} {
		ip := net.ParseIP(ipStr)
		lookupHostIPAddrs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: ip}}, nil
		}
		if _, err := guardedDial(context.Background(), "tcp", "rebind.example:80", time.Second); !IsBlockedTargetError(err) {
			t.Fatalf("guardedDial resolved %s err = %v, want blockedTargetError", ipStr, err)
		}
		if err := guardDialSocketControl("tcp", net.JoinHostPort(ipStr, "80"), nil); !IsBlockedTargetError(err) {
			t.Fatalf("guardDialSocketControl(%s) err = %v, want blockedTargetError", ipStr, err)
		}
	}
	if err := guardDialSocketControl("tcp", "127.0.0.1:80", nil); err != nil {
		t.Fatalf("guardDialSocketControl(127.0.0.1:80) err = %v, want nil", err)
	}
}

func TestGuardedRoundTripperNilBaseDefaultsToDefaultTransport(t *testing.T) {
	rt := GuardedRoundTripper(nil)
	req, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/", nil)
	if _, err := rt.RoundTrip(req); !IsBlockedTargetError(err) {
		t.Fatalf("err = %v, want blockedTargetError", err)
	}
}

func TestTransportSetsForceAttemptHTTP2(t *testing.T) {
	tr, err := Transport("")
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = false, want true")
	}
}

func TestGuardedDialHappyEyeballsAndZone(t *testing.T) {
	addrs, err := lookupIPAddrs(context.Background(), "fe80::1%eth0")
	if err != nil || len(addrs) != 1 || addrs[0].Zone != "eth0" {
		t.Fatalf("lookupIPAddrs zone = %+v, %v", addrs, err)
	}

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	origLookup := lookupHostIPAddrs
	t.Cleanup(func() { lookupHostIPAddrs = origLookup })
	// Return a non-routable/closed IPv6 first, then listening IPv4.
	lookupHostIPAddrs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("2001:db8::1")},
			{IP: net.ParseIP("127.0.0.1")},
		}, nil
	}

	start := time.Now()
	conn, err := guardedDial(context.Background(), "tcp", net.JoinHostPort("dualstack.example", port), 2*time.Second)
	if err != nil {
		t.Fatalf("guardedDial Happy Eyeballs failed: %v", err)
	}
	_ = conn.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Happy Eyeballs fallback took too long: %v", elapsed)
	}
}

func TestGuardedDialPrimarySuccessDoesNotTriggerFallbackDial(t *testing.T) {
	v4Ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp4: %v", err)
	}
	defer v4Ln.Close()
	_, port, _ := net.SplitHostPort(v4Ln.Addr().String())

	v6Ln, err := net.Listen("tcp6", net.JoinHostPort("::1", port))
	if err != nil {
		t.Skipf("ipv6 loopback on same port unavailable: %v", err)
	}
	defer v6Ln.Close()

	var v4Accepts, v6Accepts atomic.Int32
	go func() {
		for {
			c, err := v4Ln.Accept()
			if err != nil {
				return
			}
			v4Accepts.Add(1)
			_ = c.Close()
		}
	}()
	go func() {
		for {
			c, err := v6Ln.Accept()
			if err != nil {
				return
			}
			v6Accepts.Add(1)
			_ = c.Close()
		}
	}()

	origLookup := lookupHostIPAddrs
	t.Cleanup(func() { lookupHostIPAddrs = origLookup })
	lookupHostIPAddrs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("127.0.0.1")},
			{IP: net.ParseIP("::1")},
		}, nil
	}

	for i := 0; i < 5; i++ {
		conn, err := guardedDial(context.Background(), "tcp", net.JoinHostPort("dualstack.example", port), 2*time.Second)
		if err != nil {
			t.Fatalf("dial %d failed: %v", i, err)
		}
		_ = conn.Close()
	}
	// Give any stray fallback dial a brief moment to land if buggy.
	time.Sleep(50 * time.Millisecond)
	if v4Accepts.Load() != 5 {
		t.Fatalf("primary accepts = %d, want 5", v4Accepts.Load())
	}
	if v6Accepts.Load() != 0 {
		t.Fatalf("fallback accepts = %d, want 0 when primary succeeds", v6Accepts.Load())
	}
}
