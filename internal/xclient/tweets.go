package xclient

import (
	"context"
	"errors"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func tweetResultPath(queryID string) string { return xapi.GraphQLPath(queryID, "TweetResultByRestId") }

// fetchGuestTweet 是 legacy 的 guest/public 回退：新建一个不带 Cookie 的一次性客户端，
// 以未登录身份（loggedIn=false）直接请求，不经过 Pool。只有没有可用账号、
// NewSourceFromAccounts 退化为 guestSource 时才会走到这里；正常账号池路径使用
// Pool.FetchTweet。cfg 只提供全局代理，不读取已废弃的 Cookie 字段。
func fetchGuestTweet(ctx context.Context, cfg config.AppConfig, opts Options, id string, options parser.ParseOptions) (parser.TweetData, error) {
	client, err := newAccountClient(Account{}, cfg.ProxyURL, opts)
	if err != nil {
		return parser.TweetData{}, err
	}
	return xapi.FetchTweet(ctx, client, id, false, options)
}

// WalkUserTweets 逐页抓取全文时间线。全文有独立的 operation 和持久化页边界，
// 不能复用 UserMedia 的水位线。
func (p *Pool) WalkUserTweets(ctx context.Context, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error {
	var requester xapi.Requester = p
	if userMediaNeedsPrimaryClient(user) {
		client := p.Primary()
		if client == nil {
			return errors.New("没有可用 X 客户端")
		}
		requester = client
	}
	return xapi.WalkUserTweets(ctx, requester, user, options, save)
}

// walkUserTweets 是 xapi.WalkUserTweets 的包内适配版本，供既有测试使用。
func walkUserTweets(ctx context.Context, requester timelineRequester, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error {
	return xapi.WalkUserTweets(ctx, requesterAdapter{requester}, user, options, save)
}
