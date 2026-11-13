package xclient

import (
	"context"
	"errors"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func tweetResultPath(queryID string) string { return xapi.GraphQLPath(queryID, "TweetResultByRestId") }

// FetchTweet 抓取单条推文详情。为保持既有语义，直接使用 cfg 里的主 Cookie
// 新建客户端发请求，不经过 Pool 轮换，也不受账号池选中逻辑影响。
func FetchTweet(ctx context.Context, cfg config.AppConfig, id string, options parser.ParseOptions) (parser.TweetData, error) {
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
