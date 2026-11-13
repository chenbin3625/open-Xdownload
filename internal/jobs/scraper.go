package jobs

import (
	"os"
	"strings"
)

// EnvScraper 选择 X 数据源实现：legacy（默认）或 xscrape。
const EnvScraper = "OPEN_XDOWNLOAD_SCRAPER"

const (
	// ScraperLegacy 是旧实现 internal/xclient。
	ScraperLegacy = "legacy"
	// ScraperXScrape 是 twscrape 风格的新实现 internal/xscrape。
	ScraperXScrape = "xscrape"
)

// ScraperMode 返回当前进程使用的 X 数据源实现。未设置或无法识别时返回 legacy。
func ScraperMode() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvScraper)), ScraperXScrape) {
		return ScraperXScrape
	}
	return ScraperLegacy
}
