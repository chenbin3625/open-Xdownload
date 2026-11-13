package xclient

import (
	"context"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// NewSource 是 legacy 的 xapi.Source 工厂。与 NewPool 的差别只有一处：凭据缺失
// 时不返回错误，而是返回一个只支持 FetchTweet 的 source。jobs 的"推文链接"任务
// 历史上在未配置 Cookie 时仍会用 guest/public 身份请求单条推文，这个回退行为必须
// 保持；需要账号池的方法会返回与 NewPool 完全相同的错误。
func NewSource(cfg config.AppConfig) (xapi.Source, error) {
	pool, err := NewPool(cfg)
	if err != nil {
		return &guestSource{cfg: cfg, err: err}, nil
	}
	return pool, nil
}

type guestSource struct {
	cfg config.AppConfig
	err error
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
	return FetchTweet(ctx, s.cfg, id, options)
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
