package xapi

import (
	"context"
	"fmt"
	"net/url"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/tidwall/gjson"
)

// FetchTweet 抓取单条推文详情。loggedIn 决定 __relay_internal__pv__ 中的登录态
// 变量；legacy 实现按主 Cookie 是否存在传入，xscrape 通常为 true。
func FetchTweet(ctx context.Context, requester Requester, id string, loggedIn bool, options parser.ParseOptions) (parser.TweetData, error) {
	variables := TweetVariables(loggedIn)
	variables["restId"] = id
	variables["withFocalTweet"] = true
	payload, err := requester.GraphQL(ctx, TweetResultPath(), url.Values{"variables": {EncodeVariables(variables)}})
	if err != nil {
		return parser.TweetData{}, err
	}
	result := gjson.GetBytes(payload, "data.tweet_result_by_rest_id.result")
	if !result.Exists() {
		return parser.TweetData{}, fmt.Errorf("推文详情接口没有返回正文")
	}
	return parser.TweetFromGraphQLResultWithOptions("", "", id, result, options)
}
