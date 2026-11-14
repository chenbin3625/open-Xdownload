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
	mu          sync.Mutex
	ids         map[string]string
	fetchedAt   time.Time
	last404At   time.Time
	discovering bool
	done        chan struct{}
	warned      bool
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

// ensureFresh 在首次使用和每小时到期时后台发现一次 queryId。发现失败不会影响
// 请求：始终回退到内置表。
func (t *queryIDTable) ensureFresh(ctx context.Context, e *Engine, proxy string) {
	t.mu.Lock()
	if t.discovering || (!t.fetchedAt.IsZero() && e.now().Sub(t.fetchedAt) < queryIDTTL) {
		t.mu.Unlock()
		return
	}
	t.discovering = true
	done := make(chan struct{})
	t.done = done
	t.mu.Unlock()

	go func() {
		defer func() {
			t.mu.Lock()
			t.discovering = false
			t.fetchedAt = e.now()
			t.done = nil
			close(done)
			t.mu.Unlock()
		}()
		discoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryDiscoveryTimeout)
		defer cancel()
		ids, err := discoverQueryIDs(discoveryCtx, e, proxy)
		if err != nil {
			e.logs.printf("queryids", time.Hour, "xscrape: 自动发现 queryId 失败，继续使用内置表: %v", err)
		} else if len(ids) == 0 {
			e.logs.printf("queryids", time.Hour, "xscrape: 首页/脚本中未发现 queryId，继续使用内置表")
		}
		t.merge(ids)
	}()
}

// refreshFor404 在 GraphQL 返回 404 后同步重新发现（单飞、最多每 10 分钟一次）。
// 返回是否真的拿到了新结果；无论返回值如何，调用方都会用现有表重试一次。
func (t *queryIDTable) refreshFor404(ctx context.Context, e *Engine, proxy string) bool {
	t.mu.Lock()
	if !t.last404At.IsZero() && e.now().Sub(t.last404At) < queryID404RefreshInterval {
		t.mu.Unlock()
		return false
	}
	if t.discovering {
		done := t.done
		t.mu.Unlock()
		select {
		case <-done:
			return true
		case <-ctx.Done():
			return false
		}
	}
	t.discovering = true
	t.last404At = e.now()
	done := make(chan struct{})
	t.done = done
	t.mu.Unlock()

	ids, err := discoverQueryIDs(ctx, e, proxy)
	t.merge(ids)
	t.mu.Lock()
	t.discovering = false
	t.fetchedAt = e.now()
	t.done = nil
	close(done)
	t.mu.Unlock()
	return err == nil && len(ids) > 0
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
	client := newHTTPClient(transport)
	homeURL := e.baseURL + "/"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, homeURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", xapi.UserAgent)
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	reply := doRequest(client, request)
	if reply.err != nil {
		return nil, reply.err
	}
	if reply.status >= 400 {
		return nil, fmt.Errorf("X 首页返回 HTTP %d", reply.status)
	}
	if len(reply.payload) > discoveryMaxBytes {
		return nil, fmt.Errorf("X 首页超过 %d 字节上限", discoveryMaxBytes)
	}

	ids := map[string]string{}
	scanQueryIDs(reply.payload, ids)

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
	}
	return ids, nil
}

// scriptURLs 提取首页引用的脚本，只保留 X/Twitter 静态资源主机上的 .js。
func scriptURLs(payload []byte, base string) []string {
	baseURL, _ := url.Parse(base)
	baseHost := ""
	if baseURL != nil {
		baseHost = strings.ToLower(baseURL.Hostname())
	}
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
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host != baseHost && host != "x.com" && host != "www.x.com" && host != "twitter.com" && host != "www.twitter.com" && !strings.HasSuffix(host, ".twimg.com") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(parsed.Path), ".js") {
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

func fetchScript(ctx context.Context, client *http.Client, scriptURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scriptURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", xapi.UserAgent)
	reply := doRequest(client, request)
	if reply.err != nil {
		return nil, reply.err
	}
	if reply.status >= 400 {
		return nil, fmt.Errorf("脚本返回 HTTP %d", reply.status)
	}
	if len(reply.payload) > discoveryMaxBytes {
		return nil, fmt.Errorf("脚本超过 %d 字节上限", discoveryMaxBytes)
	}
	return reply.payload, nil
}

// scanQueryIDs 在同一段脚本里把 queryId 与最近的 operationName 配对。压缩包中
// 两者相隔几个字段，取距离最近的已知 operation。
func scanQueryIDs(data []byte, ids map[string]string) {
	operations := operationPattern.FindAllSubmatchIndex(data, -1)
	if len(operations) == 0 {
		return
	}
	for _, query := range queryIDPattern.FindAllSubmatchIndex(data, -1) {
		queryID := string(data[query[2]:query[3]])
		// 先找最近的 operationName（不论是否已知）：紧邻的未知 operation 说明这个
		// queryId 属于它，不能拿来覆盖更远的已知 operation。
		bestOperation := ""
		bestDistance := -1
		for _, operation := range operations {
			distance := spanDistance(query[0], query[1], operation[0], operation[1])
			if bestDistance < 0 || distance < bestDistance {
				bestDistance = distance
				bestOperation = string(data[operation[2]:operation[3]])
			}
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
