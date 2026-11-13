package xapi

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
)

const (
	// Host 是 X 的站点根地址。GraphQL 路径都相对于它构造。
	Host = "https://x.com"
	// UserAgent 是请求 X 时使用的浏览器 UA。
	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// TimelineFeatures 是时间线类接口共用的 features 参数。
const TimelineFeatures = `{"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_timeline_navigation_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"communities_web_enable_tweet_community_results_fetch":true,"c9s_tweet_anatomy_moderator_badge_enabled":true,"articles_preview_enabled":true,"tweetypie_unmention_optimization_enabled":true,"responsive_web_edit_tweet_api_enabled":true,"graphql_is_translatable_rweb_tweet_is_translatable_enabled":true,"view_counts_everywhere_api_enabled":true,"longform_notetweets_consumption_enabled":true,"responsive_web_twitter_article_tweet_consumption_enabled":true,"tweet_awards_web_tipping_enabled":false,"creator_subscriptions_quote_tweet_preview_enabled":false,"freedom_of_speech_not_reach_fetch_enabled":true,"standardized_nudges_misinfo":true,"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled":true,"rweb_video_timestamps_enabled":true,"longform_notetweets_rich_text_read_enabled":true,"longform_notetweets_inline_media_enabled":true,"responsive_web_enhance_cards_enabled":false}`

// GraphQLQueryIDs 集中管理 X GraphQL 的 queryId。X 轮换 queryId 时只需改这一处；
// 请求如果因 queryId 失效返回 404，会得到 ErrQueryIDStale（不自动猜测新 id）。
var GraphQLQueryIDs = struct {
	UserMedia        string
	UserByRestId     string
	UserByScreenName string
	ListByRestId     string
	ListMembers      string
	Following        string
}{
	UserMedia:        "MOLbHrtk8Ovu7DUNOLcXiA",
	UserByRestId:     "CO4_gU4G_MRREoqfiTh6Hg",
	UserByScreenName: "xmU6X_CKVnQ5lSrCbAmJsg",
	ListByRestId:     "ZMQOSpxDo0cP5Cdt8MgEVA",
	ListMembers:      "3dQPyRyAj6Lslp4e0ClXzg",
	Following:        "7FEKOPNAvxWASt6v9gfCXw",
}

// GraphQLPath 由 queryId + operation 构造 GraphQL 端点路径。
func GraphQLPath(queryID, operation string) string {
	return "/i/api/graphql/" + queryID + "/" + operation
}

var (
	UserMediaPath        = GraphQLPath(GraphQLQueryIDs.UserMedia, "UserMedia")
	UserByRestIDPath     = GraphQLPath(GraphQLQueryIDs.UserByRestId, "UserByRestId")
	UserByScreenNamePath = GraphQLPath(GraphQLQueryIDs.UserByScreenName, "UserByScreenName")
	ListByRestIDPath     = GraphQLPath(GraphQLQueryIDs.ListByRestId, "ListByRestId")
	ListMembersPath      = GraphQLPath(GraphQLQueryIDs.ListMembers, "ListMembers")
	FollowingPath        = GraphQLPath(GraphQLQueryIDs.Following, "Following")
)

// EncodeVariables 用 encoding/json 构造 GraphQL variables：%q 是 Go 字面量转义，
// 遇到非 JSON 字符（如 \x 形式）会产生非法 JSON，交给 json.Marshal 更稳妥。
func EncodeVariables(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// UserByIDValues 构造 UserByRestId 的查询参数。
func UserByIDValues(id string) url.Values {
	values := url.Values{}
	values.Set("variables", EncodeVariables(map[string]any{"userId": id, "withSafetyModeUserFields": true}))
	values.Set("features", `{"hidden_profile_likes_enabled":true,"hidden_profile_subscriptions_enabled":true,"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"highlights_tweets_tab_ui_enabled":true,"responsive_web_twitter_article_notes_tab_enabled":true,"subscriptions_feature_can_gift_premium":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	return values
}

// UserByScreenNameValues 构造 UserByScreenName 的查询参数。
func UserByScreenNameValues(screenName string) url.Values {
	values := url.Values{}
	values.Set("variables", EncodeVariables(map[string]any{"screen_name": screenName, "withSafetyModeUserFields": true}))
	values.Set("features", `{"hidden_profile_subscriptions_enabled":true,"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"subscriptions_verification_info_is_identity_verified_enabled":true,"subscriptions_verification_info_verified_since_enabled":true,"highlights_tweets_tab_ui_enabled":true,"responsive_web_twitter_article_notes_tab_enabled":true,"subscriptions_feature_can_gift_premium":false,"creator_subscriptions_tweet_preview_api_enabled":true,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	values.Set("fieldToggles", `{"withAuxiliaryUserLabels":false}`)
	return values
}

// ListByIDValues 构造 ListByRestId 的查询参数。
func ListByIDValues(id string) url.Values {
	values := url.Values{}
	values.Set("variables", EncodeVariables(map[string]any{"listId": id}))
	values.Set("features", `{"rweb_tipjar_consumption_enabled":true,"responsive_web_graphql_exclude_directive_enabled":true,"verified_phone_label_enabled":false,"responsive_web_graphql_skip_user_profile_image_extensions_enabled":false,"responsive_web_graphql_timeline_navigation_enabled":true}`)
	return values
}

// UserMediaValues 构造 UserMedia 时间线一页的查询参数。
func UserMediaValues(userID, cursor string) url.Values {
	values := url.Values{}
	values.Set("variables", EncodeVariables(map[string]any{
		"userId":                 userID,
		"count":                  100,
		"cursor":                 cursor,
		"includePromotedContent": false,
		"withClientEventToken":   false,
		"withBirdwatchNotes":     false,
		"withVoice":              true,
		"withV2Timeline":         true,
	}))
	values.Set("features", TimelineFeatures)
	values.Set("fieldToggles", `{"withArticlePlainText":false}`)
	return values
}

// ListMembersValues 构造 ListMembers 分页的查询参数（cursor 由调用方补入）。
func ListMembersValues(listID, cursor string) url.Values {
	values := url.Values{}
	values.Set("variables", EncodeVariables(map[string]any{
		"listId":                   listID,
		"count":                    200,
		"withSafetyModeUserFields": true,
		"cursor":                   cursor,
	}))
	values.Set("features", TimelineFeatures)
	return values
}

// FollowingValues 构造 Following 分页的查询参数（cursor 由调用方补入）。
func FollowingValues(userID, cursor string) url.Values {
	values := url.Values{}
	values.Set("variables", EncodeVariables(map[string]any{
		"userId":                 userID,
		"count":                  200,
		"includePromotedContent": false,
		"cursor":                 cursor,
	}))
	values.Set("features", TimelineFeatures)
	return values
}

// TweetVariables 返回 TweetResultByRestId 的 __relay_internal__pv__ 变量集合。
func TweetVariables(loggedIn bool) map[string]any {
	variables := map[string]any{}
	for key, value := range map[string]bool{"xclientsfeaturesappviewerisloggedinrelayprovider": loggedIn, "xclientsfeaturestranslationgroktranslatedpostenabledrelayprovider": true, "replydownvoteenabledrelayprovider": false, "postctafetchenabledrelayprovider": true, "xclientsfeaturestweetcomponentsgrokanalyzepostrelayprovider": true, "xclientsfeaturestranslationnoteautotranslationenabledrelayprovider": true, "liveeventpostcontextenabledrelayprovider": true, "sportspostcontextenabledrelayprovider": true} {
		variables["__relay_internal__pv__"+key] = value
	}
	return variables
}

// TweetResultPath 返回推文详情接口路径。queryId 可用环境变量
// OPEN_XDOWNLOAD_TWEET_RESULT_QUERY_ID 覆盖，无需改代码即可轮换。
func TweetResultPath() string {
	queryID := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_TWEET_RESULT_QUERY_ID"))
	if queryID == "" {
		queryID = "m6saLeawhcvnYmZi1Zzgmw"
	}
	return GraphQLPath(queryID, "TweetResultByRestId")
}

// UserTweetsPath 返回全文时间线接口路径。queryId 可用环境变量
// OPEN_XDOWNLOAD_USER_TWEETS_QUERY_ID 覆盖。
func UserTweetsPath() string {
	queryID := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_USER_TWEETS_QUERY_ID"))
	if queryID == "" {
		queryID = "jiF1DduqwzFSPmvRffg82g"
	}
	// X 已不再接受裸 /graphql/ 前缀（一律 401），必须与其他接口一样走 /i/api/graphql/。
	return GraphQLPath(queryID, "UserTweets")
}
