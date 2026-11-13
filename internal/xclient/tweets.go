package xclient

import (
	"context"
	"errors"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func tweetResultPath(queryID string) string { return xapi.GraphQLPath(queryID, "TweetResultByRestId") }

// fetchGuestTweet 是 legacy 的 guest/public 回退：用 cfg 里的（可能为空的）Cookie
// 新建一次性客户端直接请求，不经过 Pool。只有凭据缺失、NewSource 退化为 guestSource
// 时才会走到这里；正常账号池路径使用 Pool.FetchTweet。
func fetchGuestTweet(ctx context.Context, cfg config.AppConfig, id string, options parser.ParseOptions) (parser.TweetData, error) {
	client, err := NewClient(Credentials{AuthToken: cfg.AuthToken, CSRFToken: cfg.CSRFToken}, cfg.ProxyURL)
	if err != nil {
		return parser.TweetData{}, err
	}
	return xapi.FetchTweet(ctx, client, id, cfg.AuthToken != "", options)
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
