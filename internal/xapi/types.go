// Package xapi 提供 legacy 与 twscrape 风格实现共享的类型、错误分类、GraphQL
// 端点定义，以及只依赖 Requester 的纯解析与分页逻辑。
//
// 账号选择、viewer 绑定、年龄限制换号等依赖账号池状态的策略不在这里，由各实现
// 自己处理；本包只接受一个最小的 Requester。
package xapi

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

// Requester 是纯解析/分页代码对底层请求层的唯一要求。
type Requester interface {
	GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error)
}

type User struct {
	ID           string
	Name         string
	ScreenName   string
	Protected    bool
	FriendsCount int
	MediaCount   int
	Muting       bool
	Blocking     bool
	Following    bool
	Requested    bool
}

func (u User) Title() string {
	if u.ScreenName == "" {
		return u.ID
	}
	if u.Name == "" {
		return u.ScreenName
	}
	return fmt.Sprintf("%s(%s)", u.Name, u.ScreenName)
}

func (u User) Visible() bool {
	return !u.Protected || u.Following
}

type List struct {
	ID          string
	Name        string
	MemberCount int
	Creator     User
	// ViewerKey 是读取该列表元信息的账号 auth_token 的 sha256 十六进制，用于把
	// 后续成员分页固定到同一个账号（私有列表只有特定账号视角可见）。
	//
	// 不参与 JSON 序列化：legacy List 的 viewer 是未导出字段，本来就不会出现在
	// JSON 里；检查点（jobs.memberCheckpoint）另外用独立的 viewerKey 字段持久化
	// 这个值，List 本身从不写入检查点。加 json:"-" 保持既有 JSON 形状完全不变。
	ViewerKey string `json:"-"`
}

type UserPage struct {
	Users     []User
	Cursor    string
	ViewerKey string
}

// UserMediaTimelineOptions 是 UserMedia 时间线的翻页参数。Cursor 用于从上次
// 已提交的页继续；StopID 是增量归档早停边界（为空表示全量扫描）。
type UserMediaTimelineOptions struct {
	Cursor, StopID string
	Parse          parser.ParseOptions
}

// UserMediaTimelinePage 是一页已解析的媒体时间线。回调返回后调用方应处理
// Tweets、提交失败队列，并保存 Cursor（Complete 为 true 时整轮结束）。
type UserMediaTimelinePage struct {
	Tweets   []parser.TweetData
	Cursor   string
	Complete bool
}

type TweetTimelineOptions struct {
	Cursor, StopID, HeadID string
	Parse                  parser.ParseOptions
}

type TweetTimelinePage struct {
	Tweets         []parser.TweetData
	Cursor, HeadID string
	Complete       bool
}

// MediaFetchStats 描述一次用户媒体时间线抓取的附加统计。既有 API
// （GetUserMedia/GetUserMediaWithOptions）保持原签名、不暴露统计；需要"越过毒丸"
// 推进增量游标的调用方使用 GetUserMediaWithOptionsStats。
type MediaFetchStats struct {
	// Requests 是本轮实际发起的 GraphQL 请求数（含失败/限流请求）。
	Requests int
	// Pages 是成功解析并交给回调的页数。
	Pages int
	// APITime 是 GraphQL 请求往返耗时总和，不含本地媒体下载与页间节流。
	APITime time.Duration
	// LastSeenTweetID 是本轮 timeline 中见到的所有条目（含被跳过/置顶/早停边界）里
	// 数值最大的推文 ID。空串表示本轮没有见到任何带 ID 的推文条目。
	LastSeenTweetID string
	// SkippedTweets 是因解析失败被跳过的推文数量（不含 TweetUnavailable）。
	SkippedTweets int
	// SkippedTweetIDs 是被跳过的推文 ID，便于上层写入任务 Issues / 日志。
	SkippedTweetIDs []string
}

type ClientStatus struct {
	Index        int                 `json:"index"`
	Primary      bool                `json:"primary"`
	ScreenName   string              `json:"screenName"`
	OK           bool                `json:"ok"`
	Disabled     bool                `json:"disabled"`
	Error        string              `json:"error,omitempty"`
	RequestCount int64               `json:"requestCount"`
	RateLimits   []RateLimitSnapshot `json:"rateLimits"`
}

type PoolDiagnostics struct {
	Total     int            `json:"total"`
	Available int            `json:"available"`
	Clients   []ClientStatus `json:"clients"`
}

type RateLimitSnapshot struct {
	Path      string    `json:"path"`
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
	Blocked   bool      `json:"blocked"`
}
