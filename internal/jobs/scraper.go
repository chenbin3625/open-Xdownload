package jobs

import (
	"log"
	"os"
	"strings"
	"sync"
)

// EnvScraper 选择 X 数据源实现：xscrape（默认）或 legacy。
const EnvScraper = "OPEN_XDOWNLOAD_SCRAPER"

const (
	// ScraperLegacy 是旧实现 internal/xclient，显式设置该值才会启用。
	ScraperLegacy = "legacy"
	// ScraperXScrape 是 twscrape 风格的新实现 internal/xscrape，也是默认实现。
	ScraperXScrape = "xscrape"
)

// warnUnknownScraper 只在第一次遇到无法识别的取值时告警：ScraperMode 会被
// /api/x-accounts 等请求路径反复调用，不能每次都写日志。
var warnUnknownScraper sync.Once

// ScraperMode 返回当前进程使用的 X 数据源实现。未设置时使用 xscrape；只有显式
// 设置为 legacy（大小写不敏感）才回退旧实现，其他取值按 xscrape 处理并告警一次。
func ScraperMode() string {
	raw := strings.TrimSpace(os.Getenv(EnvScraper))
	if strings.EqualFold(raw, ScraperLegacy) {
		return ScraperLegacy
	}
	if raw != "" && !strings.EqualFold(raw, ScraperXScrape) {
		warnUnknownScraper.Do(func() {
			log.Printf("x: 未识别的 %s=%q，使用 xscrape（需要旧实现请设置 legacy）", EnvScraper, raw)
		})
	}
	return ScraperXScrape
}
