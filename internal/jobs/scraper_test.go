package jobs

import "testing"

func TestScraperMode(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"", ScraperXScrape},
		{"xscrape", ScraperXScrape},
		{" XScrape ", ScraperXScrape},
		{"legacy", ScraperLegacy},
		{" LEGACY ", ScraperLegacy},
		// 无法识别的取值按默认实现处理，不静默回退旧实现。
		{"twscrape", ScraperXScrape},
		{"bogus", ScraperXScrape},
	} {
		t.Setenv(EnvScraper, tc.env)
		if got := ScraperMode(); got != tc.want {
			t.Errorf("ScraperMode() with %q = %q, want %q", tc.env, got, tc.want)
		}
	}
}
