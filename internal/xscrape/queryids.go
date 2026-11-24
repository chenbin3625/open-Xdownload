package xscrape

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

const (
	// queryIDTTL 是内置/发现 queryId 的刷新周期。
	queryIDTTL = time.Hour
	// queryIDEmptyTTL 是首页/脚本未发现任何 queryId（例如 X 切换纯客户端分包）后的退避周期，
	// 避免每小时空抓 24 个前端脚本。
	queryIDEmptyTTL = 6 * time.Hour
	// queryIDErrorTTL 是发现失败或超时后的退避周期，避免每个请求重复触发发现。
	queryIDErrorTTL = 30 * time.Minute
	// queryID404RefreshInterval 限制 404 触发的同步重新发现频率，避免故障时每个
	// 请求都抓一遍首页。
	queryID404RefreshInterval = 10 * time.Minute
	// queryDiscoveryTimeout 是单次后台发现的超时。
	queryDiscoveryTimeout = 45 * time.Second
	// discoveryMaxScripts / discoveryMaxBytes 限制发现过程抓取的数据量。
	discoveryMaxScripts = 24
	discoveryMaxBytes   = 16 << 20
	// discoveryPairDistance 是 queryId 与 operationName 在压缩代码里的最大字符距离。
	discoveryPairDistance = 600
)

// builtinQueryIDs 是内置表。环境变量覆盖优先于发现结果，发现结果优先于这里；
// 环境变量只覆盖 TweetResultByRestId 和 UserTweets（由 xapi 的 Path 函数处理）。
var builtinQueryIDs = map[string]string{
	"UserMedia":           xapi.GraphQLQueryIDs.UserMedia,
	"UserByRestId":        xapi.GraphQLQueryIDs.UserByRestId,
	"UserByScreenName":    xapi.GraphQLQueryIDs.UserByScreenName,
	"ListByRestId":        xapi.GraphQLQueryIDs.ListByRestId,
	"ListMembers":         xapi.GraphQLQueryIDs.ListMembers,
	"Following":           xapi.GraphQLQueryIDs.Following,
	"UserTweets":          "jiF1DduqwzFSPmvRffg82g",
	"TweetResultByRestId": "m6saLeawhcvnYmZi1Zzgmw",
}

// queryIDTable 保存自动发现的 queryId。读路径只加锁查表；发现是后台/单飞任务。
type queryIDTable struct {
	mu            sync.Mutex
	ids           map[string]string
	fetchedAt     time.Time
	nextRefreshAt time.Time
	last404At     time.Time
	discovering   bool
	lastSucceeded bool
	done          chan struct{}
}

func queryIDRefreshCooldown(ids map[string]string, err error) time.Duration {
	if err != nil {
		return queryIDErrorTTL
	}
	if len(ids) == 0 {
		return queryIDEmptyTTL
	}
	return queryIDTTL
}

// rewrite 把 xapi 构造的 GraphQL 路径中的 queryId 换成当前值。环境变量覆盖过的
// 操作保持原样：调用方显式配置的优先级最高。
func (t *queryIDTable) rewrite(path string) string {
	queryID, operation, ok := splitGraphQLPath(path)
	if !ok || envQueryIDOverride(operation) {
		return path
	}
	current := t.operationID(operation)
	if current == "" || current == queryID {
		return path
	}
	return xapi.GraphQLPath(current, operation)
}

func (t *queryIDTable) operationID(operation string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id := t.ids[operation]; id != "" {
		return id
	}
	return builtinQueryIDs[operation]
}

// ensureFresh 在首次使用和冷却到期时后台发现一次 queryId。发现失败不会影响
// 请求：始终回退到内置表。
func (t *queryIDTable) ensureFresh(ctx context.Context, e *Engine, proxy string) {
	if ctx.Err() != nil {
		return
	}
	t.mu.Lock()
	now := e.now()
	if t.discovering {
		t.mu.Unlock()
		return
	}
	if !t.nextRefreshAt.IsZero() && now.Before(t.nextRefreshAt) {
		t.mu.Unlock()
		return
	}
	if t.nextRefreshAt.IsZero() && !t.fetchedAt.IsZero() && now.Sub(t.fetchedAt) < queryIDTTL {
		t.mu.Unlock()
		return
	}
	t.discovering = true
	t.lastSucceeded = false
	done := make(chan struct{})
	t.done = done
	t.mu.Unlock()

	// 后台发现由多个并发请求及 refreshFor404 等待者共享：与发起方取消解耦，
	// 避免单个调用取消中断共享发现或阻止冷却时间戳更新。
	discoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryDiscoveryTimeout)
	go func() {
		defer cancel()
		var ids map[string]string
		var err error
		defer func() {
			t.merge(ids)
			finishedAt := e.now()
			effectiveErr := err
			if effectiveErr == nil && discoveryCtx.Err() != nil {
				effectiveErr = discoveryCtx.Err()
			}
			cooldown := queryIDRefreshCooldown(ids, effectiveErr)
			t.mu.Lock()
			t.discovering = false
			t.fetchedAt = finishedAt
			t.nextRefreshAt = finishedAt.Add(cooldown)
			t.lastSucceeded = effectiveErr == nil && len(ids) > 0
			t.done = nil
			close(done)
			t.mu.Unlock()
		}()
		ids, err = discoverQueryIDs(discoveryCtx, e, proxy)
		if err != nil {
			if discoveryCtx.Err() == nil {
				e.logs.printf("queryids", time.Hour, "xscrape: 自动发现 queryId 失败，继续使用内置表: %v", err)
			}
		} else if len(ids) == 0 {
			e.logs.printf("queryids", time.Hour, "xscrape: 首页/脚本中未发现 queryId，继续使用内置表")
		}
	}()
}

// refreshFor404 在 GraphQL 返回 404 后同步重新发现（单飞、最多每 10 分钟一次）。
// 返回是否真的拿到了新结果。
func (t *queryIDTable) refreshFor404(ctx context.Context, e *Engine, proxy string) bool {
	if ctx.Err() != nil {
		return false
	}
	t.mu.Lock()
	if t.discovering {
		done := t.done
		t.mu.Unlock()
		select {
		case <-done:
			t.mu.Lock()
			ok := t.lastSucceeded
			t.mu.Unlock()
			return ok
		case <-ctx.Done():
			return false
		}
	}
	if !t.last404At.IsZero() && e.now().Sub(t.last404At) < queryID404RefreshInterval {
		t.mu.Unlock()
		return false
	}
	t.discovering = true
	t.last404At = e.now()
	t.lastSucceeded = false
	done := make(chan struct{})
	t.done = done
	t.mu.Unlock()

	e.assets.clear()

	// 发现过程会抓首页和最多 24 个脚本；使用与 leader 取消解耦的超时上下文，
	// 避免 leader 取消中断等待者或污染冷却时间戳。
	discoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryDiscoveryTimeout)
	go func() {
		defer cancel()
		var ids map[string]string
		var err error
		defer func() {
			t.merge(ids)
			finishedAt := e.now()
			effectiveErr := err
			if effectiveErr == nil && discoveryCtx.Err() != nil {
				effectiveErr = discoveryCtx.Err()
			}
			cooldown := queryIDRefreshCooldown(ids, effectiveErr)
			t.mu.Lock()
			t.discovering = false
			t.fetchedAt = finishedAt
			t.nextRefreshAt = finishedAt.Add(cooldown)
			t.lastSucceeded = effectiveErr == nil && len(ids) > 0
			t.done = nil
			close(done)
			t.mu.Unlock()
		}()
		ids, err = discoverQueryIDs(discoveryCtx, e, proxy)
	}()

	select {
	case <-done:
		t.mu.Lock()
		ok := t.lastSucceeded
		t.mu.Unlock()
		return ok
	case <-ctx.Done():
		return false
	}
}

func (t *queryIDTable) merge(ids map[string]string) {
	if len(ids) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ids == nil {
		t.ids = make(map[string]string, len(ids))
	}
	for operation, id := range ids {
		t.ids[operation] = id
	}
}

// splitGraphQLPath 拆分 /i/api/graphql/<queryId>/<operation>。
func splitGraphQLPath(path string) (queryID, operation string, ok bool) {
	const prefix = "/i/api/graphql/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// envQueryIDOverride 报告该 operation 的 queryId 是否由环境变量显式指定。
func envQueryIDOverride(operation string) bool {
	switch operation {
	case "TweetResultByRestId":
		return strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_TWEET_RESULT_QUERY_ID")) != ""
	case "UserTweets":
		return strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_USER_TWEETS_QUERY_ID")) != ""
	default:
		return false
	}
}

var (
	scriptSrcPattern = regexp.MustCompile(`(?i)<script[^>]*\bsrc\s*=\s*["']([^"']+)["']`)
	queryIDPattern   = regexp.MustCompile(`"?queryId"?\s*:\s*"([A-Za-z0-9_-]{10,40})"`)
	operationPattern = regexp.MustCompile(`"?operationName"?\s*:\s*"([A-Za-z0-9_]+)"`)
)

// discoverQueryIDs 抓取 x.com 首页及其引用的 JS，尽力解析 queryId/operationName。
// 抓到的 HTML/JS 都是不可信数据：只做正则解析，绝不执行。
func discoverQueryIDs(ctx context.Context, e *Engine, proxy string) (map[string]string, error) {
	transport, err := e.transport(proxy)
	if err != nil {
		return nil, err
	}
	client := newHTTPClient(e.assetRoundTripper(proxy, transport))
	homeURL := e.baseURL + "/"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, homeURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", xapi.UserAgent)
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	reply := doRequestLimited(client, request, discoveryMaxBytes)
	if reply.err != nil {
		return nil, reply.err
	}
	if reply.status >= 400 {
		return nil, fmt.Errorf("X 首页返回 HTTP %d", reply.status)
	}

	ids := map[string]string{}
	scanQueryIDs(reply.payload, ids)
	// 首页内联脚本通常已包含全部 operation；此时跳过后续脚本抓取（每个最多 16MiB）。
	if allQueryIDsFound(ids) {
		return ids, nil
	}

	scripts := scriptURLs(reply.payload, homeURL)
	for index, script := range scripts {
		if index >= discoveryMaxScripts {
			break
		}
		if err := ctx.Err(); err != nil {
			return ids, err
		}
		body, err := fetchScript(ctx, client, script)
		if err != nil {
			continue
		}
		scanQueryIDs(body, ids)
		if allQueryIDsFound(ids) {
			break
		}
	}
	return ids, nil
}

// allQueryIDsFound 报告内置表里的每个 operation 是否都已找到 queryId。
func allQueryIDsFound(ids map[string]string) bool {
	for operation := range builtinQueryIDs {
		if ids[operation] == "" {
			return false
		}
	}
	return true
}

// scriptURLs 提取首页引用的脚本，只保留 X/Twitter 静态资源主机上的 .js，
// 并跳过明显的语言包/polyfill/vendor 块以减少无效下载。
func scriptURLs(payload []byte, base string) []string {
	baseURL, _ := url.Parse(base)
	seen := map[string]bool{}
	scripts := []string{}
	for _, match := range scriptSrcPattern.FindAllSubmatch(payload, -1) {
		reference := strings.TrimSpace(string(match[1]))
		if reference == "" {
			continue
		}
		parsed, err := url.Parse(reference)
		if err != nil {
			continue
		}
		if baseURL != nil {
			parsed = baseURL.ResolveReference(parsed)
		}
		if !allowedScriptURL(parsed, baseURL) {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(parsed.Path), ".js") {
			continue
		}
		if isNonGraphQLScriptPath(parsed.Path) {
			continue
		}
		value := parsed.String()
		if seen[value] {
			continue
		}
		seen[value] = true
		scripts = append(scripts, value)
	}
	return scripts
}

func isNonGraphQLScriptPath(scriptPath string) bool {
	lower := strings.ToLower(scriptPath)
	return strings.Contains(lower, "/i18n/") ||
		strings.Contains(lower, "polyfill") ||
		strings.Contains(lower, "emoji") ||
		strings.Contains(lower, "/vendor.") ||
		strings.Contains(lower, "/vendor-")
}

func allowedScriptURL(parsed, baseURL *url.URL) bool {
	if parsed == nil || parsed.User != nil {
		return false
	}
	if baseURL != nil && sameOrigin(parsed, baseURL) {
		return true
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	return host == "x.com" || host == "www.x.com" || host == "twitter.com" || host == "www.twitter.com" || strings.HasSuffix(host, ".twimg.com")
}

func fetchScript(ctx context.Context, client *http.Client, scriptURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scriptURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", xapi.UserAgent)
	reply := doRequestLimited(client, request, discoveryMaxBytes)
	if reply.err != nil {
		return nil, reply.err
	}
	if reply.status >= 400 {
		return nil, fmt.Errorf("脚本返回 HTTP %d", reply.status)
	}
	return reply.payload, nil
}

// scanQueryIDs 在同一段脚本里把 queryId 与最近的 operationName 配对。压缩包中
// 两者相隔几个字段，取距离最近的已知 operation；operations 与 queries 均按偏移升序，
// 用双指针在线性时间内找到每个 queryId 左右最近的候选。
func scanQueryIDs(data []byte, ids map[string]string) {
	operations := operationPattern.FindAllSubmatchIndex(data, -1)
	if len(operations) == 0 {
		return
	}
	opIdx := 0
	for _, query := range queryIDPattern.FindAllSubmatchIndex(data, -1) {
		queryID := string(data[query[2]:query[3]])
		for opIdx < len(operations) && operations[opIdx][1] <= query[0] {
			opIdx++
		}
		// 先找最近的 operationName（不论是否已知）：紧邻的未知 operation 说明这个
		// queryId 属于它，不能拿来覆盖更远的已知 operation。
		bestOperation := ""
		bestDistance := -1
		consider := func(operation []int) {
			distance := spanDistance(query[0], query[1], operation[0], operation[1])
			if bestDistance < 0 || distance < bestDistance {
				bestDistance = distance
				bestOperation = string(data[operation[2]:operation[3]])
			}
		}
		if opIdx > 0 {
			consider(operations[opIdx-1])
		}
		if opIdx < len(operations) {
			consider(operations[opIdx])
		}
		if _, known := builtinQueryIDs[bestOperation]; !known {
			continue
		}
		if bestDistance <= discoveryPairDistance {
			ids[bestOperation] = queryID
		}
	}
}

func spanDistance(aStart, aEnd, bStart, bEnd int) int {
	if aEnd <= bStart {
		return bStart - aEnd
	}
	if bEnd <= aStart {
		return aStart - bEnd
	}
	return 0
}

// discoveredOperations 返回已发现的 operation 名（按字母序），仅用于日志/测试。
func (t *queryIDTable) discoveredOperations() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.ids))
	for name := range t.ids {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
