package xclient

import (
	"context"
	"errors"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// errNoAccounts 是没有任何可用账号时，需要账号池的方法返回的错误。
var errNoAccounts = errors.New("没有可用的 X 账号，请先在「设置 → X 账号池」导入 Cookie")

// guestSource 是没有可用账号时的回退数据源。jobs 的"推文链接"任务历史上在未配置
// Cookie 时仍会以 guest/public 身份请求单条推文，这个行为保留；其余方法都需要账号池，
// 统一返回 err。
type guestSource struct {
	cfg  config.AppConfig
	opts Options
	err  error
}

func (s *guestSource) GetUserByInput(context.Context, string) (xapi.User, error) {
	return xapi.User{}, s.err
}

func (s *guestSource) GetListByID(context.Context, string) (xapi.List, error) {
	return xapi.List{}, s.err
}

func (s *guestSource) GetUserByIDAsPrimary(context.Context, string) (xapi.User, error) {
	return xapi.User{}, s.err
}

func (s *guestSource) FollowUserAsPrimary(context.Context, xapi.User) error { return s.err }

func (s *guestSource) FetchTweet(ctx context.Context, id string, options parser.ParseOptions) (parser.TweetData, error) {
	return fetchGuestTweet(ctx, s.cfg, s.opts, id, options)
}

func (s *guestSource) WalkUserMedia(context.Context, xapi.User, xapi.UserMediaTimelineOptions, func(xapi.UserMediaTimelinePage) error) (xapi.MediaFetchStats, error) {
	return xapi.MediaFetchStats{}, s.err
}

func (s *guestSource) WalkUserTweets(context.Context, xapi.User, xapi.TweetTimelineOptions, func(xapi.TweetTimelinePage) error) error {
	return s.err
}

func (s *guestSource) GetListMembersPage(context.Context, xapi.List, string, string) (xapi.UserPage, error) {
	return xapi.UserPage{}, s.err
}

func (s *guestSource) GetFollowingPage(context.Context, xapi.User, string, string) (xapi.UserPage, error) {
	return xapi.UserPage{}, s.err
}

func (s *guestSource) RecheckDisabledClients(context.Context) bool { return false }

func (s *guestSource) CheckAll(context.Context) xapi.PoolDiagnostics { return xapi.PoolDiagnostics{} }

func (s *guestSource) Diagnostics() xapi.PoolDiagnostics { return xapi.PoolDiagnostics{} }

func (s *guestSource) HasPrimary() bool { return false }

var (
	_ xapi.Source = (*Pool)(nil)
	_ xapi.Source = (*guestSource)(nil)
)
