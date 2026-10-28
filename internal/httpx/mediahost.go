package httpx

import "strings"

// IsTwimgHost 判断 host 是否属于 twimg.com（含子域）。
//
// 媒体下载、海报回源、URL 规范化散落着 5+ 份同样的判断，历史上已经出现过大小写/
// 尾点处理不一致的漂移。这里提供单一口径：大小写无关、忽略结尾的点（FQDN 写法），
// 只允许 twimg.com 本身与其子域，避免 `evil-twimg.com` 这类后缀混淆。
func IsTwimgHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "twimg.com" || strings.HasSuffix(host, ".twimg.com")
}
