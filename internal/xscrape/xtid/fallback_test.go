package xtid

import (
	"context"
	"strings"
	"testing"
)

func TestGenerateFallsBackFromStaleSigningScript(t *testing.T) {
	for _, scenario := range []string{"not found", "missing key indices"} {
		t.Run(scenario, func(t *testing.T) {
			golden := loadGolden(t)
			fs := newFixtureServer(t)
			const stalePath = "/responsive-web/client-web/ondemand.s.abcdef1a.js"
			page := `<script src="https://abs.twimg.com` + stalePath + `"></script>` + string(loadFixture(t, "home.html"))
			fs.setRoute("/", []byte(page))
			if scenario == "missing key indices" {
				fs.setRoute(stalePath, []byte("export const decoy = true"))
			}
			gen := newWithOptions(fs.client(), fs.URL, testUserAgent, testOptions(golden.Timestamp, golden.RandomByte))
			tc := golden.Cases[0]

			got, err := gen.Generate(context.Background(), tc.Method, tc.Path)

			if err != nil {
				t.Fatalf("stale signing script should fall back to dynamic imports: %v", err)
			}
			if got != tc.TransactionID {
				t.Fatalf("fallback transaction ID=%q, want %q", got, tc.TransactionID)
			}
			if fs.count(stalePath) == 0 {
				t.Error("fixture did not exercise the stale script")
			}
			if fs.count("/x-web/x-web/assets/DIQrwHty.js") == 0 {
				t.Error("fallback did not load the dynamic signing chunk")
			}
		})
	}
}

func TestScriptFetchRejectsUntrustedHostsBeforeRequest(t *testing.T) {
	fs := newFixtureServer(t)
	fs.setRoute("/untrusted.js", []byte("must not be requested"))
	gen := newWithOptions(fs.client(), fs.URL, testUserAgent, options{})
	for _, host := range []string{"evil.example", "x.com.evil.example", "abs.twimg.com.evil.example"} {
		t.Run(host, func(t *testing.T) {
			_, err := gen.get(context.Background(), "https://"+host+"/untrusted.js")
			if err == nil || !strings.Contains(err.Error(), "拒绝请求非 X 脚本主机") {
				t.Fatalf("untrusted script host must be rejected, got %v", err)
			}
			if got := fs.count("/untrusted.js"); got != 0 {
				t.Fatalf("untrusted script reached the transport %d times", got)
			}
		})
	}
}
