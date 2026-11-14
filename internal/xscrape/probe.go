package xscrape

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/tidwall/gjson"
)

// probeVerifiedTTL 是"刚探测成功过"的有效期。接口 401 在这个窗口内不再重复探测，
// 直接视为目标/接口级 401。
const probeVerifiedTTL = 2 * time.Minute

// probePath 是账号登录校验端点。settings.json / verify_credentials.json 对有效
// Cookie 也返回 404，不能再作为探测入口。
const probePath = "/i/api/1.1/account/multi/list.json"

// probeRecords 记录内存中的"最近一次探测成功"时间；不落库、不保存 Cookie。
type probeRecords struct {
	mu       sync.Mutex
	verified map[int64]time.Time
}

func (p *probeRecords) mark(id int64, at time.Time) {
	p.mu.Lock()
	if p.verified == nil {
		p.verified = map[int64]time.Time{}
	}
	p.verified[id] = at
	p.mu.Unlock()
}

func (p *probeRecords) recent(id int64, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, ok := p.verified[id]
	return ok && now.Sub(at) < probeVerifiedTTL
}

func (p *probeRecords) forget(id int64) {
	p.mu.Lock()
	delete(p.verified, id)
	p.mu.Unlock()
}

// proxyFor 返回账号请求使用的代理：账号级代理优先，否则用最近一次 Source 的全局代理。
func (e *Engine) proxyFor(accountProxy string) string {
	if proxy := strings.TrimSpace(accountProxy); proxy != "" {
		return proxy
	}
	e.globalMu.Lock()
	defer e.globalMu.Unlock()
	return e.globalProxy
}

// probe 实现 xapi.HomeProber。它只发起探测请求，不改账号表；调用方根据结果
// 决定启用/停用/记录错误。响应判定规则见 xapi.HomeProbeResult 的注释。
func (e *Engine) probe(ctx context.Context, creds xapi.AccountCredentials) xapi.HomeProbeResult {
	authToken := strings.TrimSpace(creds.AuthToken)
	csrfToken := strings.TrimSpace(creds.CSRFToken)
	if authToken == "" || csrfToken == "" {
		return xapi.HomeProbeResult{Err: errors.New("缺少 auth_token 或 ct0")}
	}
	proxy := e.proxyFor(creds.Proxy)
	transport, err := e.transport(proxy)
	if err != nil {
		return xapi.HomeProbeResult{Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+probePath, nil)
	if err != nil {
		return xapi.HomeProbeResult{Err: err}
	}
	userAgent := strings.TrimSpace(creds.UserAgent)
	if userAgent == "" {
		userAgent = xapi.UserAgent
	}
	request.Header.Set("Authorization", "Bearer "+xapi.GuestBearer())
	request.Header.Set("X-Csrf-Token", csrfToken)
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
	request.Header.Set("X-Twitter-Active-User", "yes")
	request.Header.Set("X-Twitter-Client-Language", "en")
	addAccountCookies(request, authToken, csrfToken, creds.ExtraCookies)
	e.applyTransactionID(ctx, request, e.currentGenerator(), http.MethodGet, probePath)

	if err := e.paceProcess(ctx); err != nil {
		return xapi.HomeProbeResult{Err: err}
	}
	reply := doRequest(newHTTPClient(transport), request)
	if reply.err != nil {
		return xapi.HomeProbeResult{Err: reply.err}
	}
	return interpretProbe(reply, creds.ExtraCookies)
}

func interpretProbe(reply *response, extraCookies map[string]string) xapi.HomeProbeResult {
	code, message := probeErrorCode(reply.payload)
	switch reply.status {
	case http.StatusOK:
		// 少数接口会以 HTTP 200 携带错误码；凭证/封禁类错误码必须先于 users 解析。
		if xapi.IsFatalAPIErrorCode(code) {
			return xapi.HomeProbeResult{Unauthorized: true, Err: probeRejection(code, message, reply)}
		}
		if code == 353 {
			return xapi.HomeProbeResult{Unauthorized: true, Err: errors.New("ct0 与 auth_token 不匹配（code 353）")}
		}
		return interpretProbeUsers(reply.payload, extraCookies)
	case http.StatusUnauthorized:
		if code == 0 {
			return xapi.HomeProbeResult{Unauthorized: true, Err: errors.New("X 拒绝了该账号（HTTP 401）")}
		}
		return xapi.HomeProbeResult{Unauthorized: true, Err: probeRejection(code, message, reply)}
	}
	if code == 353 {
		return xapi.HomeProbeResult{Unauthorized: true, Err: errors.New("ct0 与 auth_token 不匹配（code 353）")}
	}
	if xapi.IsFatalAPIErrorCode(code) {
		return xapi.HomeProbeResult{Unauthorized: true, Err: probeRejection(code, message, reply)}
	}
	if xapi.LooksLikeWAFChallenge(reply.contentType(), reply.payload) {
		return xapi.HomeProbeResult{Err: fmt.Errorf("%w: HTTP %d", xapi.ErrWAFChallenge, reply.status)}
	}
	return xapi.HomeProbeResult{Err: fmt.Errorf("X 登录校验失败: HTTP %d", reply.status)}
}

func interpretProbeUsers(payload []byte, extraCookies map[string]string) xapi.HomeProbeResult {
	users := gjson.GetBytes(payload, "users")
	if !users.IsArray() {
		return xapi.HomeProbeResult{Err: errors.New("X 登录校验响应中没有 users 列表")}
	}
	twid := twidUserID(extraCookies)
	var chosen gjson.Result
	found := false
	for _, user := range users.Array() {
		if !user.Get("is_auth_valid").Bool() {
			continue
		}
		if !found {
			chosen = user
			found = true
		}
		if twid != "" && user.Get("user_id").String() == twid {
			chosen = user
			break
		}
	}
	if !found {
		return xapi.HomeProbeResult{Err: errors.New("X 登录校验响应中没有 is_auth_valid 为 true 的账号")}
	}
	if chosen.Get("is_suspended").Bool() {
		return xapi.HomeProbeResult{Unauthorized: true, Err: errors.New("账号已被封禁")}
	}
	screenName := strings.TrimSpace(chosen.Get("screen_name").String())
	if screenName == "" {
		return xapi.HomeProbeResult{Err: errors.New("X 登录校验响应中没有 screen_name")}
	}
	return xapi.HomeProbeResult{ScreenName: screenName}
}

// probeErrorCode 从响应体提取第一个 API 错误码。兼容 errors 数组和 403 的
// 顶层 {"code":353,...} 形状。
func probeErrorCode(payload []byte) (int, string) {
	if len(payload) == 0 {
		return 0, ""
	}
	root := gjson.ParseBytes(payload)
	if value := root.Get("code"); value.Exists() {
		return int(value.Int()), strings.TrimSpace(root.Get("message").String())
	}
	errs := root.Get("errors")
	if errs.IsArray() {
		for _, entry := range errs.Array() {
			if value := entry.Get("code"); value.Exists() {
				return int(value.Int()), strings.TrimSpace(entry.Get("message").String())
			}
		}
		return 0, ""
	}
	if value := errs.Get("code"); value.Exists() {
		return int(value.Int()), strings.TrimSpace(errs.Get("message").String())
	}
	return 0, ""
}

func probeRejection(code int, message string, reply *response) error {
	switch code {
	case xapi.APIErrAccountLocked:
		return fmt.Errorf("账号已锁定（code %d）", code)
	case 64:
		return errors.New("账号已被封禁")
	case 89:
		return fmt.Errorf("Cookie 已失效（code %d）", code)
	case 215:
		return fmt.Errorf("Cookie 无效（code %d）", code)
	}
	if code > 0 {
		if message != "" {
			return fmt.Errorf("X 拒绝了该账号（code %d: %s）", code, message)
		}
		return fmt.Errorf("X 拒绝了该账号（code %d）", code)
	}
	return fmt.Errorf("X 拒绝了该账号（HTTP %d）", reply.status)
}

// twidUserID 从 twid Cookie（形如 u%3D<id> 或 u=<id>）解析用户 ID。
func twidUserID(extraCookies map[string]string) string {
	raw := strings.TrimSpace(extraCookies["twid"])
	if raw == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(raw); err == nil {
		raw = decoded
	}
	if index := strings.Index(raw, "u="); index >= 0 {
		raw = raw[index+2:]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return raw
}

// addAccountCookies 把主 Cookie 与 extra_cookies 追加到请求。cookie 值只出现在
// 请求里，不会写入日志或数据库。extra 中重复的 auth_token/ct0 会被跳过。
func addAccountCookies(request *http.Request, authToken, csrfToken string, extra map[string]string) {
	if authToken != "" {
		request.AddCookie(&http.Cookie{Name: "auth_token", Value: authToken})
	}
	if csrfToken != "" {
		request.AddCookie(&http.Cookie{Name: "ct0", Value: csrfToken})
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		if name == "auth_token" || name == "ct0" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		request.AddCookie(&http.Cookie{Name: name, Value: extra[name]})
	}
}
