package xapi

import (
	"context"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

// Source 是 jobs/httpapi 唯一依赖的 X 数据源接口。legacy（xclient.Pool）与
// twscrape 风格实现（xscrape）都实现它；接口里不出现任何具体实现类型。
type Source interface {
	// GetUserByInput 按数字 ID 或 @screen_name 查询用户。
	GetUserByInput(ctx context.Context, input string) (User, error)
	// GetListByID 查询列表元信息，返回的 List.ViewerKey 绑定读取该列表的账号。
	GetListByID(ctx context.Context, id string) (List, error)
	// GetUserByIDAsPrimary 用主账号查询用户；受保护账号的关系字段必须由主账号视角读取。
	GetUserByIDAsPrimary(ctx context.Context, id string) (User, error)
	// FollowUserAsPrimary 用主账号关注用户。
	FollowUserAsPrimary(ctx context.Context, user User) error
	// FetchTweet 用主 Cookie 直接抓取单条推文，不经过账号池轮换。
	FetchTweet(ctx context.Context, id string, options parser.ParseOptions) (parser.TweetData, error)
	// WalkUserMedia 逐页抓取用户媒体时间线，每页完成后调用 save。
	WalkUserMedia(ctx context.Context, user User, options UserMediaTimelineOptions, save func(UserMediaTimelinePage) error) (MediaFetchStats, error)
	// WalkUserTweets 逐页抓取全文时间线，每页完成后调用 save。
	WalkUserTweets(ctx context.Context, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error
	// GetListMembersPage 抓取列表成员的一页；viewerKey 非空时分页固定到同一账号。
	GetListMembersPage(ctx context.Context, list List, cursor, viewerKey string) (UserPage, error)
	// GetFollowingPage 抓取关注列表的一页；viewerKey 非空时分页固定到同一账号。
	GetFollowingPage(ctx context.Context, user User, cursor, viewerKey string) (UserPage, error)
	// RecheckDisabledClients 立即校验被禁用的 Cookie，返回此后是否至少有一个可用。
	RecheckDisabledClients(ctx context.Context) bool
	// CheckAll 并发校验全部账号并返回诊断快照。
	CheckAll(ctx context.Context) PoolDiagnostics
	// Diagnostics 返回当前账号池的只读诊断快照。
	Diagnostics() PoolDiagnostics
	// HasPrimary 报告是否存在主账号。
	HasPrimary() bool
}
