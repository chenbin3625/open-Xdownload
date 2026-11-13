package xapi

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

const (
	APIErrDependency    = 0
	APIErrTimeout       = 29
	APIErrRateLimit     = 88
	APIErrOverCapacity  = 130
	APIErrAccountLocked = 326
)

const MaxErrorPayloadBytes = 8 << 10

type APIError struct {
	Code int
	Raw  string
}

func (e *APIError) Error() string {
	if e.Code == 0 {
		return e.Raw
	}
	return fmt.Sprintf("X API error %d: %s", e.Code, e.Raw)
}

type HTTPError struct {
	StatusCode int
	Payload    string
	// WAFChallenge 为 true 表示该 403 更像 WAF/挑战页（HTML/Cloudflare 等），
	// 不代表 Cookie 失效：调用方应短退避并尝试其他 Cookie，而不是 disable。
	WAFChallenge bool
}

func (e *HTTPError) Error() string {
	if e.Payload == "" {
		return fmt.Sprintf("X API 请求失败: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("X API 请求失败: HTTP %d %s", e.StatusCode, e.Payload)
}

// 限流/账号锁定的 API 错误码：即使 data 非空也必须上报，不能当成可容忍的 partial error。
var fatalAPIErrorCodes = map[int]struct{}{
	32:  {}, // Could not authenticate you
	64:  {}, // Account suspended
	88:  {}, // Rate limit exceeded
	89:  {}, // Invalid or expired token
	215: {}, // Bad authentication data
	326: {}, // Account locked
}

// IsFatalAPIErrorCode 报告 API 错误码是否属于限流/账号锁定/凭证类致命错误。
func IsFatalAPIErrorCode(code int) bool {
	_, ok := fatalAPIErrorCodes[code]
	return ok
}

// ParseAPIError 检查 GraphQL 响应体里的 errors，返回需要上报的第一个错误。
func ParseAPIError(payload []byte) error {
	errorsResult := gjson.GetBytes(payload, "errors")
	if !errorsResult.Exists() {
		return nil
	}
	raw := LimitedErrorPayload(payload)
	// errors 不是数组（对象/字符串）时不能静默当成功：X 正常都是数组，出现异常形态
	// 说明响应不符合预期，按未知 API 错误上报。
	if !errorsResult.IsArray() {
		code := -1
		if value := errorsResult.Get("code"); value.Exists() {
			code = int(value.Int())
		}
		return &APIError{Code: code, Raw: raw}
	}
	data := gjson.GetBytes(payload, "data")
	hasData := data.IsObject() && len(data.Map()) > 0
	for _, entry := range errorsResult.Array() {
		code := -1
		if value := entry.Get("code"); value.Exists() {
			code = int(value.Int())
		}
		err := &APIError{Code: code, Raw: raw}
		// GraphQL 允许部分成功：data 可用时，针对个别子项的缺失/无 kind 的
		// partial error 可以容忍（记日志后继续）。但限流/账号锁定/凭证类错误
		// （fatalAPIErrorCodes）与瞬时错误必须上报，供 Pool 换 Cookie 或让任务中止。
		if hasData && isTolerablePartialError(entry, err) {
			log.Printf("xclient: 忽略可容忍的 GraphQL 部分错误 code=%d kind=%q: %s", code, entry.Get("kind").String(), entry.Get("message").String())
			continue
		}
		return err
	}
	return nil
}

// isTolerablePartialError 判定 data 非空响应中的单条 GraphQL 错误能否忽略。
// 容忍范围：无 kind、kind=NonFatal、或没有 code（-1）的**非致命**错误；
// 限流/锁定/凭证错误与瞬时（0/29/130）错误一律不容忍。
func isTolerablePartialError(entry gjson.Result, err *APIError) bool {
	if IsFatalAPIErrorCode(err.Code) || IsPermanentClientError(err) || IsTransientError(err) {
		return false
	}
	kind := entry.Get("kind").String()
	if kind == "" || kind == "NonFatal" || err.Code == -1 {
		return true
	}
	// 子字段级别的校验错误（path 深于根字段，如 list.default_banner_media_results.result）
	// 只影响那个字段，主体数据仍完整；X 线上 ListByRestId 就会这样返回。
	return kind == "Validation" && len(entry.Get("path").Array()) > 1
}

func LimitedErrorPayload(payload []byte) string {
	if len(payload) <= MaxErrorPayloadBytes {
		return string(payload)
	}
	clipped := payload[:MaxErrorPayloadBytes]
	for len(clipped) > 0 && !utf8.Valid(clipped) {
		clipped = clipped[:len(clipped)-1]
	}
	return fmt.Sprintf("%s... [truncated %d bytes]", string(clipped), len(payload)-len(clipped))
}

// LooksLikeWAFChallenge 判断 4xx 响应是否更像 WAF/登录挑战页：Content-Type 为 HTML，
// 或正文包含常见挑战页标记（Cloudflare / HTML / challenge / security）。
// 真实 GraphQL 403 通常是 JSON 错误体，不会命中；命中时不能把 Cookie 判死。
func LooksLikeWAFChallenge(contentType string, payload []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	if len(payload) == 0 {
		return false
	}
	const maxScan = 8 << 10
	if len(payload) > maxScan {
		payload = payload[:maxScan]
	}
	body := strings.ToLower(string(payload))
	for _, marker := range []string{"<html", "<!doctype html", "challenge", "cf-chl", "cloudflare", "security check", "just a moment", "attention required"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func ParseUser(raw gjson.Result) (User, error) {
	if raw.Get("result").Exists() {
		raw = raw.Get("result")
	}
	if raw.Get("__typename").String() == "UserUnavailable" {
		return User{}, unavailableUserError(raw)
	}
	legacy := raw.Get("legacy")
	if !legacy.Exists() {
		return User{}, errors.New("响应中没有用户 legacy 数据")
	}
	// X 已把部分字段从 legacy 迁到 core / privacy / relationship_perspectives，不同接口
	// 返回的结构并不一致：新位置优先，旧位置兜底。
	relationship := raw.Get("relationship_perspectives")
	return User{
		ID:           raw.Get("rest_id").String(),
		Name:         FirstString(raw.Get("core.name"), legacy.Get("name")),
		ScreenName:   FirstString(raw.Get("core.screen_name"), legacy.Get("screen_name")),
		Protected:    FirstBool(raw.Get("privacy.protected"), legacy.Get("protected")),
		FriendsCount: int(legacy.Get("friends_count").Int()),
		MediaCount:   int(legacy.Get("media_count").Int()),
		Muting:       FirstBool(relationship.Get("muting"), legacy.Get("muting")),
		Blocking:     FirstBool(relationship.Get("blocking"), legacy.Get("blocking")),
		Following:    FirstBool(relationship.Get("following"), legacy.Get("following")),
		Requested:    FirstBool(relationship.Get("follow_request_sent"), legacy.Get("follow_request_sent")),
	}, nil
}

// unavailableUserError 把 X 的 UserUnavailable 响应翻译成可排查的中文错误。
//
// X 在 data.user.result 里只回一个 __typename，具体原因放在 reason/message
// （如 Suspended/Deactivated/NsfwLoggedOut）。旧实现直接返回"用户不可用"，
// 用户无法判断是账号问题、权限问题还是 Cookie 失效，任务一失败就只剩一句
// 无信息量的提示。这里保留原始原因，并针对已知原因给出下一步建议。
func unavailableUserError(raw gjson.Result) error {
	reason := strings.TrimSpace(raw.Get("reason").String())
	message := strings.TrimSpace(raw.Get("message").String())

	label := "用户不可用"
	hint := "提示: 账号可能已冻结/注销/受限；若多个不同账号都报此错误，多半是当前 Cookie 已失效，请重新导入 AuthToken 与 CSRFToken"
	// X 对受年龄限制的账号同样回 reason=Suspended，真实原因只写在 message 里
	// （User is age-restricted）。必须优先按 message 判断，否则会把"当前账号看不到
	// 成人内容"误报成"目标账号被冻结"，还会把用户引去重导 Cookie——而重导无效。
	if IsAgeRestrictedUnavailable(raw) {
		label = "用户受年龄限制，当前账号无法访问（age-restricted）"
		hint = "提示: Cookie 登录状态本身正常，重导 Cookie 无效；请在 X 中完成年龄验证并开启「显示可能含有敏感内容的媒体」，或改用已完成年龄验证的账号、更换代理出口地区"
	} else {
		switch strings.ToLower(reason) {
		case "suspended":
			label = "用户已被冻结（Suspended）"
		case "deactivated", "deleted":
			label = "用户已注销或被删除（" + reason + "）"
		case "nsfwloggedout", "nsfw":
			label = "用户被标记为敏感内容，当前 Cookie 未登录无法访问"
			hint = "提示: 请确认该账号 Cookie 仍处于登录状态；若其他接口正常，多为 X 对该内容额外要求登录态，必要时重新导入 AuthToken 与 CSRFToken"
		case "protected":
			label = "用户已受保护，需要关注后才能查看"
			hint = "提示: 请改用已关注该账号的 Cookie 重试"
		case "unavailable", "":
			// 保留默认措辞，下面用原因/消息补足信息。
		default:
			label = "用户不可用（" + reason + "）"
		}
	}

	// 用单行分隔符拼接：任务中心按换行拆分聚合错误，换行会被误判成多条独立
	// 失败记录，导致一次失败被显示成"部分失败 (N 项)"。
	parts := []string{label}
	if message != "" && !strings.Contains(label, message) {
		parts = append(parts, "X 返回: "+message)
	}
	if reason != "" && !strings.Contains(label, reason) {
		parts = append(parts, "原因: "+reason)
	}
	parts = append(parts, hint)
	return errors.New(strings.Join(parts, "；"))
}

// IsAgeRestrictedUnavailable 判断 UserUnavailable 响应是否属于"目标账号受年龄限制"。
// raw 可以是 data.user（含 result 包装）或 data.user.result。X 对此类账号的 reason
// 仍是 Suspended，因此以 reason/message 中的年龄限制标记为准。
func IsAgeRestrictedUnavailable(raw gjson.Result) bool {
	if raw.Get("result").Exists() {
		raw = raw.Get("result")
	}
	if raw.Get("__typename").String() != "UserUnavailable" {
		return false
	}
	reported := strings.ToLower(raw.Get("reason").String() + " " + raw.Get("message").String())
	for _, marker := range []string{"age-restricted", "age restricted", "agerestricted", "age_restricted"} {
		if strings.Contains(reported, marker) {
			return true
		}
	}
	return false
}

// AgeRestrictedPayload 在 GraphQL 响应体层面做同样的判断，供换 Cookie 重试使用。
func AgeRestrictedPayload(payload []byte) bool {
	return IsAgeRestrictedUnavailable(gjson.GetBytes(payload, "data.user"))
}

// FirstBool 返回第一个存在的布尔字段的值；都不存在时为 false。
func FirstBool(values ...gjson.Result) bool {
	for _, value := range values {
		if value.Type == gjson.True || value.Type == gjson.False {
			return value.Bool()
		}
	}
	return false
}

func FirstString(values ...gjson.Result) string {
	for _, value := range values {
		if raw := value.String(); raw != "" {
			return raw
		}
	}
	return ""
}

func TimelineItems(payload []byte, instructionsPath string) ([]gjson.Result, string, error) {
	instructions := gjson.GetBytes(payload, instructionsPath)
	if !instructions.Exists() {
		if strings.HasPrefix(instructionsPath, "data.user.") {
			result := gjson.GetBytes(payload, "data.user.result")
			typeName := result.Get("__typename").String()
			if typeName == "UserUnavailable" {
				return nil, "", unavailableUserError(result)
			}
			if typeName != "" && typeName != "User" {
				return nil, "", fmt.Errorf("用户不可用: %s", typeName)
			}
		}
		return nil, "", fmt.Errorf("无法解析 timeline instructions（%s）", instructionsPath)
	}
	// instructions 必须是数组；X 若改成对象/null/字符串，.Array() 为空但 .Exists() 为真，
	// 旧实现会把这当成"空时间线"静默成功——改为报错。
	if !instructions.IsArray() {
		return nil, "", fmt.Errorf("timeline instructions 不是数组（%s, type=%s）", instructionsPath, instructions.Type.String())
	}
	entries := gjson.Result{}
	// 累积所有 TimelineAddToModule 的条目，而不是只保留最后一个。
	allModuleItems := []gjson.Result{}
	recognized := false
	for _, instruction := range instructions.Array() {
		switch instruction.Get("type").String() {
		case "TimelineAddEntries":
			recognized = true
			entries = instruction.Get("entries")
		case "TimelineAddToModule":
			recognized = true
			for _, item := range instruction.Get("moduleItems").Array() {
				allModuleItems = append(allModuleItems, item)
			}
		case "TimelinePinEntry":
			recognized = true
			// 置顶推文以单条 entry 形式出现（非数组），追加到 entries 逻辑外单独处理。
			if entry := instruction.Get("entry"); entry.Exists() {
				content := entry.Get("content")
				if itemContent := content.Get("itemContent"); itemContent.Exists() {
					// 直接放入下方的 items（不通过 entries 数组，避免干扰 cursor 提取）。
					allModuleItems = append(allModuleItems, gjson.Parse(`{"item":{"itemContent":`+itemContent.Raw+`}}`))
				}
			}
		case "TimelineReplaceEntry":
			recognized = true
			// 替换条目同样可能携带媒体，按与 AddEntries 相同的方式提取。
			if entry := instruction.Get("entry"); entry.Exists() {
				content := entry.Get("content")
				if itemContent := content.Get("itemContent"); itemContent.Exists() {
					allModuleItems = append(allModuleItems, gjson.Parse(`{"item":{"itemContent":`+itemContent.Raw+`}}`))
				}
			}
		}
	}
	// instructions 非空但没有任何已知类型：说明 X 改了 timeline 结构。此时若仍返回空结果，
	// 上层会当成"该用户没有推文"静默成功（历史问题）；改为明确报错以便尽早发现接口变更。
	// 空 instructions 数组按"确实没有内容"处理（返回空结果由调用方判断是否结束）。
	if !recognized && len(instructions.Array()) > 0 {
		return nil, "", fmt.Errorf("无法识别 timeline 结构（instructions 无已知类型，path=%s）", instructionsPath)
	}
	items := []gjson.Result{}
	if entries.IsArray() {
		for _, entry := range entries.Array() {
			if entry.Get("content.entryType").String() == "TimelineTimelineCursor" {
				continue
			}
			content := entry.Get("content")
			switch content.Get("entryType").String() {
			case "TimelineTimelineModule":
				for _, item := range content.Get("items").Array() {
					if itemContent := item.Get("item.itemContent"); itemContent.Exists() {
						items = append(items, itemContent)
					}
				}
			case "TimelineTimelineItem":
				if itemContent := content.Get("itemContent"); itemContent.Exists() {
					items = append(items, itemContent)
				}
			}
		}
	}
	for _, item := range allModuleItems {
		if itemContent := item.Get("item.itemContent"); itemContent.Exists() {
			items = append(items, itemContent)
		}
	}
	return items, BottomCursor(entries), nil
}

func BottomCursor(entries gjson.Result) string {
	array := entries.Array()
	for i := len(array) - 1; i >= 0; i-- {
		if array[i].Get("content.entryType").String() == "TimelineTimelineCursor" &&
			array[i].Get("content.cursorType").String() == "Bottom" {
			return array[i].Get("content.value").String()
		}
	}
	return ""
}

// MaxTweetID 返回两个推文 ID 中数值较大的一个；任一无法解析为数字时退回字符串
// 比较（雪花 ID 等长时字符串比较等价于数值比较）。空串视为"无"。
func MaxTweetID(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	ai, err1 := strconv.ParseInt(a, 10, 64)
	bi, err2 := strconv.ParseInt(b, 10, 64)
	if err1 == nil && err2 == nil {
		if ai >= bi {
			return a
		}
		return b
	}
	if a > b {
		return a
	}
	return b
}

// ShouldStopAt 判断 timeline 翻页是否应在 tweetID 处停止。timeline 按时间倒序：
// 精确匹配 stopID 总是停止；page>0 时再按数值比较（tweetID <= stopID 表示已翻到不新于
// 上次归档的推文），以处理 stopID 推文被删除、精确匹配永不命中的情况。首页可能含置顶
// 推文（时间较旧但排在最前），故首页仅用精确匹配，避免误停而漏归档更新的推文。
func ShouldStopAt(tweetID, stopID string, page int) bool {
	if tweetID != "" && tweetID == stopID {
		return true
	}
	if page == 0 {
		return false
	}
	t, err1 := strconv.ParseInt(tweetID, 10, 64)
	s, err2 := strconv.ParseInt(stopID, 10, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	return t <= s
}

// TweetIDNewerThan 报告 tweetID 是否数值上大于 stopID。任一 ID 解析失败时返回 true：
// 保守保留该推文交给下游去重（downloads 表 / 媒体锁），避免 ID 异常时漏归档。
// X 的雪花 ID 均为数字，该分支正常不会触发。
func TweetIDNewerThan(tweetID, stopID string) bool {
	t, err1 := strconv.ParseInt(tweetID, 10, 64)
	s, err2 := strconv.ParseInt(stopID, 10, 64)
	if err1 != nil || err2 != nil {
		return true
	}
	return t > s
}

func TweetResultID(result gjson.Result) string {
	const maxDepth = 8
	for depth := 0; depth < maxDepth && result.Exists(); depth++ {
		if id := result.Get("rest_id").String(); id != "" {
			return id
		}
		if result.Get("__typename").String() == "TweetWithVisibilityResults" && result.Get("tweet").Exists() {
			result = result.Get("tweet")
			continue
		}
		if nested := result.Get("result"); nested.Exists() {
			result = nested
			continue
		}
		return ""
	}
	return ""
}
