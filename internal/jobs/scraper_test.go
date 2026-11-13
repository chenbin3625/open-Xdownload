package jobs

import "testing"

func TestScraperMode(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"", ScraperLegacy},
		{"legacy", ScraperLegacy},
		{"xscrape", ScraperXScrape},
		{" XScrape ", ScraperXScrape},
		{"twscrape", ScraperLegacy},
	} {
		t.Setenv(EnvScraper, tc.env)
		if got := ScraperMode(); got != tc.want {
			t.Errorf("ScraperMode() with %q = %q, want %q", tc.env, got, tc.want)
		}
	}
}
