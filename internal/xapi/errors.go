package xapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

var ErrAllClientsRateLimited = errors.New("X 客户端暂时全部限流，请稍后重试")

// ErrClientDisabled 表示该 Cookie 近期因认证失败被标记为不可用。它不是瞬时错误：
// 同一客户端重试只会立刻再次命中，应交给 Pool 换用其他 Cookie。
var ErrClientDisabled = errors.New("X Cookie 已被标记为不可用")

// ErrQueryIDStale 表示 GraphQL 端点返回 404，通常意味着 X 轮换了 queryId/接口路径。
// jobs 可识别该错误并提示用户升级程序；程序不会自动猜测新 queryId。
var ErrQueryIDStale = errors.New("X GraphQL queryId 可能已失效（X 接口可能已变更，请升级程序）")

// ErrWAFChallenge 表示 403 响应更像 WAF/登录挑战页（HTML、Cloudflare 等），
// 不能据此判定 Cookie 失效：只做短退避，不 disable 客户端。
var ErrWAFChallenge = errors.New("X 返回 WAF/挑战页（HTTP 403），已短退避且未禁用 Cookie")

// ErrInvalidTimelineCursor 表示恢复用的分页游标已失效（用户删推、令牌轮换等）。
// 上层应清空保存的游标并从第 0 页重翻；媒体去重会跳过已下载文件。
var ErrInvalidTimelineCursor = errors.New("时间线分页游标已失效")

// ErrRateLimitBusyWait 表示本地限流器等待超限主动放弃本次请求。它属于"应换号/
// 交还调度器"的错误，不是瞬时错误，不能原样重试。
var ErrRateLimitBusyWait = errors.New("rate limiter: 连续等待次数超限，放弃本次请求")

// RateLimitedError 在账号池暂时无可用客户端时携带最早可恢复时间，供调度器把任务
// 重新置为 pending 并设置 available_at，而不是让唯一执行槽空等限流窗口。
type RateLimitedError struct {
	Path    string
	RetryAt time.Time
	Cause   error
}

func (e *RateLimitedError) Error() string {
	if e == nil {
		return ErrAllClientsRateLimited.Error()
	}
	if e.RetryAt.IsZero() {
		return ErrAllClientsRateLimited.Error()
	}
	return fmt.Sprintf("%s（最早 %s 恢复）", ErrAllClientsRateLimited.Error(), e.RetryAt.Format(time.RFC3339))
}

func (e *RateLimitedError) Unwrap() error { return e.Cause }

// Is 保持原有分类：调用方仍可用 errors.Is(err, ErrAllClientsRateLimited) 判断
// “全部账号限流”，从而中止逐个用户的批量归档。
func (e *RateLimitedError) Is(target error) bool { return target == ErrAllClientsRateLimited }

// IsAllClientsRateLimited 报告 err 是否属于"账号池全部限流"。
func IsAllClientsRateLimited(err error) bool {
	return errors.Is(err, ErrAllClientsRateLimited)
}

// RateLimitRetryAt 返回限流错误的最早恢复时间；不是限流错误时返回 false。
// 没有结构化时间的旧错误按 1 分钟保守退避。
func RateLimitRetryAt(err error) (time.Time, bool) {
	var limited *RateLimitedError
	if errors.As(err, &limited) && !limited.RetryAt.IsZero() {
		return limited.RetryAt, true
	}
	if errors.Is(err, ErrAllClientsRateLimited) {
		return time.Now().Add(time.Minute), true
	}
	if IsRateLimitError(err) {
		return time.Now().Add(time.Minute), true
	}
	return time.Time{}, false
}

// IsUnauthorized 报告错误是否为 X 返回的 HTTP 401。
func IsUnauthorized(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

// IsInvalidTimelineCursor 报告 err 是否由失效游标导致。
func IsInvalidTimelineCursor(err error) bool {
	return errors.Is(err, ErrInvalidTimelineCursor)
}

// IsRateLimitError 报告该错误是否代表"账号/路径被限流"。API-88 与 HTTP 429 都算；
// 这类错误不再做客户端内重试（88 以前被当 transient，会让 Client-only 路径等 15 分钟）。
func IsRateLimitError(err error) bool {
	if errors.Is(err, ErrRateLimitBusyWait) {
		return true
	}
	// 单账号路径等待较长时也会返回带恢复时间的 RateLimitedError；
	// Pool 仍应把它当成可交还错误，尝试其他 Cookie。
	var limited *RateLimitedError
	if errors.As(err, &limited) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == APIErrRateLimit
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// IsWAFChallengeError 报告 403 是否是 WAF/挑战页（不能据此 disable Cookie）。
func IsWAFChallengeError(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.WAFChallenge
}

// IsYieldableError 报告该错误应"立即交还 Pool 换 Cookie / 立即返回"而不是原地重试。
func IsYieldableError(err error) bool {
	return IsRateLimitError(err) || IsWAFChallengeError(err) || errors.Is(err, ErrClientDisabled)
}

// IsPermanentClientError 报告错误是否说明当前 Cookie 不应继续使用。
func IsPermanentClientError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return IsFatalAPIErrorCode(apiErr.Code) && apiErr.Code != APIErrRateLimit
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == http.StatusUnauthorized {
			return true
		}
		// WAF/挑战页 403 不代表凭证失效：交给 do()/Pool 短退避与换 Cookie。
		return httpErr.StatusCode == http.StatusForbidden && !httpErr.WAFChallenge
	}
	return false
}

// IsTransientError 报告错误是否值得在同一请求上重试。
func IsTransientError(err error) bool {
	// 上下文取消/超时不是"可重试的瞬时错误"：重试只会再失败。把它判为非瞬时，
	// 让 do()/Pool.graphQL 立即返回，避免取消时遍历重试所有客户端并白白消耗速率预算。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrClientDisabled) {
		return false
	}
	// 响应体超限、限流器连续等待失败、限流、WAF 挑战都不适合原地重试：
	// 前两者是确定性失败，后两者应交给 Pool 换 Cookie / 调用方短退避。
	if errors.Is(err, httpx.ErrBodyTooLarge) || errors.Is(err, ErrRateLimitBusyWait) {
		return false
	}
	if IsRateLimitError(err) || IsWAFChallengeError(err) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == APIErrDependency || apiErr.Code == APIErrTimeout || apiErr.Code == APIErrOverCapacity
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode >= 500 {
			return true
		}
		// 408 Request Timeout / 425 Too Early 可重试；其余 4xx（400/404/405/409/422 等）
		// 是确定性客户端错误，重试结果不变，白消耗配额和退避时间。
		return httpErr.StatusCode == http.StatusRequestTimeout || httpErr.StatusCode == 425
	}
	// 非 HTTP/非 API 错误（DNS、连接重置等网络错误）按瞬时处理。
	return err != nil && !IsPermanentClientError(err)
}

// schedulerYieldKey 标记请求来自调度器：账号池全部限流时立即返回结构化错误，
// 而不是在任务内部等待最长 maxPoolSelectWait（约 15 分钟）占用执行槽。
type schedulerYieldKey struct{}

// WithYieldToScheduler 让当前请求在账号池全部限流时立即交还给调度器。
func WithYieldToScheduler(ctx context.Context) context.Context {
	return context.WithValue(ctx, schedulerYieldKey{}, true)
}

// YieldsToScheduler 报告当前上下文是否要求限流时立即交还调度器。
func YieldsToScheduler(ctx context.Context) bool {
	yield, _ := ctx.Value(schedulerYieldKey{}).(bool)
	return yield
}
