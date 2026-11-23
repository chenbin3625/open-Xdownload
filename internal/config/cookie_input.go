package config

import (
	"encoding/json"
	"regexp"
	"strings"
)

// CookieInput 是一条解析成功的账号 Cookie。Index 是它在输入里的条目序号（从 0 开始，
// 与 CookieInputIssue.Index 的计数方式相同），调用方据此把后续失败（例如重复账号）
// 对应回原始条目。
type CookieInput struct {
	Index     int
	AuthToken string
	CSRFToken string
	Extra     map[string]string
}

// CookieInputIssue 描述第 Index 条输入无法使用的原因（Index 从 0 开始，按输入条目计数）。
type CookieInputIssue struct {
	Index  int
	Reason string
}

// validCookieName 限制额外 Cookie 的名字，避免把请求头/JSON 中的任意键带进 Cookie 头。
var validCookieName = regexp.MustCompile(`^[A-Za-z0-9_\-.]+$`)

// IsValidCookieName 报告 name 是否可以作为额外 Cookie 的名字（[A-Za-z0-9_\-.]+）。
// 发送请求前用它再过滤一次，库里被手工改坏的名字不会进入 Cookie 头。
func IsValidCookieName(name string) bool {
	return validCookieName.MatchString(name)
}

// ParseCookieInput 支持四种输入格式：
//  1. 浏览器 Cookie 请求头字符串，一行一个账号：a=b; auth_token=..; ct0=..
//  2. 两个裸值：auth_token ct0 或 auth_token,ct0（复用 ParseCookiePairs）
//  3. JSON 对象或对象数组：{"auth_token":"..","ct0":"..",...}
//  4. 浏览器扩展导出：[{"name":"auth_token","value":".."},...]（一个数组 = 一个账号）
//
// Extra 中丢弃 auth_token/ct0；名字只保留 [A-Za-z0-9_\-.]+，值不能含 ';' 或换行。
func ParseCookieInput(text string) ([]CookieInput, []CookieInputIssue) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if inputs, issues, ok := parseCookieInputJSON(trimmed); ok {
			return inputs, issues
		}
	}
	return parseCookieInputLines(trimmed)
}

func parseCookieInputJSON(raw string) ([]CookieInput, []CookieInputIssue, bool) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, nil, false
	}
	switch typed := value.(type) {
	case map[string]any:
		input, issue := cookieInputFromAccountObject(typed)
		if issue != "" {
			return nil, []CookieInputIssue{{Index: 0, Reason: issue}}, true
		}
		return []CookieInput{input}, nil, true
	case []any:
		if len(typed) == 0 {
			return nil, nil, true
		}
		if isCookieExtensionExport(typed) {
			// 一个数组 = 一个账号，条目序号固定为 0。
			input, issue := cookieInputFromExtension(typed)
			if issue != "" {
				return nil, []CookieInputIssue{{Index: 0, Reason: issue}}, true
			}
			return []CookieInput{input}, nil, true
		}
		inputs := make([]CookieInput, 0, len(typed))
		issues := make([]CookieInputIssue, 0)
		for index, item := range typed {
			object, ok := item.(map[string]any)
			if !ok {
				issues = append(issues, CookieInputIssue{Index: index, Reason: "条目不是 JSON 对象"})
				continue
			}
			input, issue := cookieInputFromAccountObject(object)
			if issue != "" {
				issues = append(issues, CookieInputIssue{Index: index, Reason: issue})
				continue
			}
			input.Index = index
			inputs = append(inputs, input)
		}
		return inputs, issues, true
	default:
		return nil, nil, false
	}
}

// isCookieExtensionExport 判断数组是否形如 [{name,value}, ...]。
func isCookieExtensionExport(items []any) bool {
	first, ok := items[0].(map[string]any)
	if !ok {
		return false
	}
	_, hasName := first["name"]
	_, hasValue := first["value"]
	return hasName && hasValue
}

var nonCookieAccountMetadataKeys = map[string]bool{
	"id":              true,
	"label":           true,
	"name":            true,
	"note":            true,
	"proxy":           true,
	"useragent":       true,
	"user_agent":      true,
	"source":          true,
	"active":          true,
	"isprimary":       true,
	"is_primary":      true,
	"errormsg":        true,
	"error_msg":       true,
	"locks":           true,
	"screenname":      true,
	"screen_name":     true,
	"lastused":        true,
	"last_used":       true,
	"verifiedat":      true,
	"verified_at":     true,
	"createdat":       true,
	"created_at":      true,
	"updatedat":       true,
	"updated_at":      true,
	"cookie":          true,
	"cookies":         true,
	"extracookies":    true,
	"extra_cookies":   true,
	"hasextracookies": true,
	"requeststoday":   true,
	"requestsbyqueue": true,
}

func cookieInputFromAccountObject(object map[string]any) (CookieInput, string) {
	authToken := firstString(object, "auth_token", "authToken")
	csrfToken := firstString(object, "ct0", "csrfToken")
	extra := make(map[string]string)
	for key, raw := range object {
		switch key {
		case "auth_token", "authToken", "ct0", "csrfToken":
			continue
		}
		if nonCookieAccountMetadataKeys[strings.ToLower(strings.TrimSpace(key))] {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			continue
		}
		addExtraCookie(extra, key, value)
	}
	return buildCookieInput(authToken, csrfToken, extra)
}

func cookieInputFromExtension(items []any) (CookieInput, string) {
	authToken, csrfToken := "", ""
	extra := make(map[string]string)
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if rawDomain, exists := object["domain"]; exists && rawDomain != nil {
			domain, ok := rawDomain.(string)
			if !ok || !isXCookieDomain(domain) {
				continue
			}
		}
		name, _ := object["name"].(string)
		value, _ := object["value"].(string)
		assignCookieValue(&authToken, &csrfToken, extra, name, value)
	}
	return buildCookieInput(authToken, csrfToken, extra)
}

func isXCookieDomain(domain string) bool {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimPrefix(d, ".")
	return d == "x.com" || strings.HasSuffix(d, ".x.com") ||
		d == "twitter.com" || strings.HasSuffix(d, ".twitter.com")
}

func parseCookieInputLines(raw string) ([]CookieInput, []CookieInputIssue) {
	inputs := make([]CookieInput, 0)
	issues := make([]CookieInputIssue, 0)
	index := 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		input, issue := parseCookieInputLine(line)
		if issue != "" {
			issues = append(issues, CookieInputIssue{Index: index, Reason: issue})
		} else {
			input.Index = index
			inputs = append(inputs, input)
		}
		index++
	}
	return inputs, issues
}

func parseCookieInputLine(line string) (CookieInput, string) {
	line = strings.TrimSpace(line)
	line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
	if len(line) >= len("cookie:") && strings.EqualFold(line[:len("cookie:")], "cookie:") {
		line = strings.TrimSpace(line[len("cookie:"):])
	}
	authToken, csrfToken := "", ""
	extra := make(map[string]string)

	// 格式 1：含 = 或 : 的结构化键值对行（Header / 逗号分隔 / YAML 行），不回落到裸值解析。
	if strings.ContainsAny(line, "=:") {
		var parts []string
		switch {
		case strings.Contains(line, ";"):
			parts = strings.Split(line, ";")
		case strings.Contains(line, ","):
			parts = strings.Split(line, ",")
		case strings.ContainsAny(line, " \t"):
			fields := strings.Fields(line)
			if countPairFields(fields) >= 2 {
				parts = fields
			} else {
				parts = []string{line}
			}
		default:
			parts = []string{line}
		}
		for _, part := range parts {
			key, value, ok := cutCookiePair(part)
			if !ok {
				continue
			}
			assignCookieValue(&authToken, &csrfToken, extra, key, value)
		}
		return buildCookieInput(authToken, csrfToken, extra)
	}

	// 格式 2：两个裸值，直接构造 CookieInput 校验，避免拼接后再经 ParseCookiePairs 被 ';' 截断。
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(fields) == 2 {
		return buildCookieInput(strings.Trim(fields[0], `"'`), strings.Trim(fields[1], `"'`), nil)
	}
	return buildCookieInput("", "", nil)
}

func buildCookieInput(authToken, csrfToken string, extra map[string]string) (CookieInput, string) {
	authToken = strings.TrimSpace(authToken)
	csrfToken = strings.TrimSpace(csrfToken)
	missing := make([]string, 0, 2)
	if authToken == "" {
		missing = append(missing, "auth_token")
	}
	if csrfToken == "" {
		missing = append(missing, "ct0")
	}
	if len(missing) > 0 {
		return CookieInput{}, "缺少 " + strings.Join(missing, " 和 ")
	}
	if hasInvalidCookieValueChar(authToken) || hasInvalidCookieValueChar(csrfToken) {
		return CookieInput{}, "Cookie 值包含非法字符"
	}
	input := CookieInput{AuthToken: authToken, CSRFToken: csrfToken}
	if len(extra) > 0 {
		input.Extra = extra
	}
	return input, ""
}

func hasInvalidCookieValueChar(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b == ';' || b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}

func assignCookieValue(authToken, csrfToken *string, extra map[string]string, name, value string) {
	name = strings.TrimSpace(name)
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	switch name {
	case "auth_token", "authToken":
		if value != "" {
			*authToken = value
		}
	case "ct0", "csrfToken":
		if value != "" {
			*csrfToken = value
		}
	default:
		addExtraCookie(extra, name, value)
	}
}

func addExtraCookie(extra map[string]string, name, value string) {
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if name == "" || !validCookieName.MatchString(name) {
		return
	}
	if hasInvalidCookieValueChar(value) {
		return
	}
	extra[name] = value
}

func cutCookiePair(raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	key, value, ok := cutEarliestCookieSep(raw)
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(key), value, true
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
