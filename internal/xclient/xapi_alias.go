package xclient

import (
	"context"
	"net/url"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
	"github.com/tidwall/gjson"
)

// —— 共享类型别名 ——
//
// xapi 是两套实现共用的类型/错误/解析层；这里保留旧名字，让 xclient 的既有
// 调用方与测试无需改字段名即可继续使用。
type (
	User                     = xapi.User
	List                     = xapi.List
	UserPage                 = xapi.UserPage
	TweetTimelineOptions     = xapi.TweetTimelineOptions
	TweetTimelinePage        = xapi.TweetTimelinePage
	UserMediaTimelineOptions = xapi.UserMediaTimelineOptions
	UserMediaTimelinePage    = xapi.UserMediaTimelinePage
	MediaFetchStats          = xapi.MediaFetchStats
	APIError                 = xapi.APIError
	HTTPError                = xapi.HTTPError
	RateLimitedError         = xapi.RateLimitedError
	ClientStatus             = xapi.ClientStatus
	PoolDiagnostics          = xapi.PoolDiagnostics
	RateLimitSnapshot        = xapi.RateLimitSnapshot
)

// —— 哨兵错误别名 ——
var (
	ErrAllClientsRateLimited = xapi.ErrAllClientsRateLimited
	ErrClientDisabled        = xapi.ErrClientDisabled
	ErrQueryIDStale          = xapi.ErrQueryIDStale
	ErrWAFChallenge          = xapi.ErrWAFChallenge
	ErrInvalidTimelineCursor = xapi.ErrInvalidTimelineCursor
	errRateLimitBusyWait     = xapi.ErrRateLimitBusyWait
)

// —— 常量别名 ——

const (
	host                           = xapi.Host
	userAgent                      = xapi.UserAgent
	timelineFeatures               = xapi.TimelineFeatures
	maxConsecutiveEmptyPages       = xapi.MaxConsecutiveEmptyPages
	maxConsecutiveEmptyMemberPages = xapi.MaxConsecutiveEmptyPages
	// MaxConsecutiveEmptyMemberPages 供分段成员收集使用：X 翻到列表末尾后仍会持续
	// 返回新的 Bottom cursor，连续这么多页没有新增用户即视为已到末尾。
	MaxConsecutiveEmptyMemberPages = xapi.MaxConsecutiveEmptyPages
	// PaginationPageDelay 是分页之间的请求间隔，与其他时间线分页保持一致。
	PaginationPageDelay  = xapi.PaginationPageDelay
	paginationPageDelay  = xapi.PaginationPageDelay
	maxErrorPayloadBytes = xapi.MaxErrorPayloadBytes
	apiErrDependency     = xapi.APIErrDependency
	apiErrTimeout        = xapi.APIErrTimeout
	apiErrRateLimit      = xapi.APIErrRateLimit
	apiErrOverCapacity   = xapi.APIErrOverCapacity
	apiErrAccountLocked  = xapi.APIErrAccountLocked
)

var (
	userMediaPath        = xapi.UserMediaPath
	userByRestIDPath     = xapi.UserByRestIDPath
	userByScreenNamePath = xapi.UserByScreenNamePath
	listByRestIDPath     = xapi.ListByRestIDPath
	listMembersPath      = xapi.ListMembersPath
	followingPath        = xapi.FollowingPath
)

// graphQLPath 由 queryId + operation 构造 GraphQL 端点路径。
func graphQLPath(queryID, operation string) string {
	return xapi.GraphQLPath(queryID, operation)
}

// requesterAdapter 把包内 timelineRequester（未导出的 graphQL 方法）适配成
// xapi.Requester，便于既有测试的假实现继续工作。
type requesterAdapter struct {
	requester timelineRequester
}

func (a requesterAdapter) GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return a.requester.graphQL(ctx, path, values)
}

// —— 错误分类转发 ——

func IsUnauthorized(err error) bool          { return xapi.IsUnauthorized(err) }
func IsAllClientsRateLimited(err error) bool { return xapi.IsAllClientsRateLimited(err) }
func RateLimitRetryAt(err error) (time.Time, bool) {
	return xapi.RateLimitRetryAt(err)
}
func IsInvalidTimelineCursor(err error) bool { return xapi.IsInvalidTimelineCursor(err) }

// WithYieldToScheduler 让当前请求在账号池全部限流时立即交还给调度器。
func WithYieldToScheduler(ctx context.Context) context.Context {
	return xapi.WithYieldToScheduler(ctx)
}

func yieldsToScheduler(ctx context.Context) bool { return xapi.YieldsToScheduler(ctx) }

func isRateLimitError(err error) bool       { return xapi.IsRateLimitError(err) }
func isWAFChallengeError(err error) bool    { return xapi.IsWAFChallengeError(err) }
func isYieldableError(err error) bool       { return xapi.IsYieldableError(err) }
func isPermanentClientError(err error) bool { return xapi.IsPermanentClientError(err) }
func isTransientError(err error) bool       { return xapi.IsTransientError(err) }

// —— 纯解析函数转发（主要供既有测试与包内残留调用使用）——

func parseUser(raw gjson.Result) (User, error) { return xapi.ParseUser(raw) }
func apiError(payload []byte) error            { return xapi.ParseAPIError(payload) }
func firstBool(values ...gjson.Result) bool    { return xapi.FirstBool(values...) }
func tweetResultID(result gjson.Result) string { return xapi.TweetResultID(result) }
func shouldStopAt(tweetID, stopID string, page int) bool {
	return xapi.ShouldStopAt(tweetID, stopID, page)
}
func limitedErrorPayload(payload []byte) string { return xapi.LimitedErrorPayload(payload) }
func looksLikeWAFChallenge(contentType string, payload []byte) bool {
	return xapi.LooksLikeWAFChallenge(contentType, payload)
}
func encodeVariables(value any) string { return xapi.EncodeVariables(value) }
