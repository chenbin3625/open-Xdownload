package xclient

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/tidwall/gjson"
)

type TweetTimelineOptions struct {
	Cursor, StopID, HeadID string
	Parse                  parser.ParseOptions
}
type TweetTimelinePage struct {
	Tweets         []parser.TweetData
	Cursor, HeadID string
	Complete       bool
}

func tweetVariables(loggedIn bool) map[string]any {
	variables := map[string]any{}
	for key, value := range map[string]bool{"xclientsfeaturesappviewerisloggedinrelayprovider": loggedIn, "xclientsfeaturestranslationgroktranslatedpostenabledrelayprovider": true, "replydownvoteenabledrelayprovider": false, "postctafetchenabledrelayprovider": true, "xclientsfeaturestweetcomponentsgrokanalyzepostrelayprovider": true, "xclientsfeaturestranslationnoteautotranslationenabledrelayprovider": true, "liveeventpostcontextenabledrelayprovider": true, "sportspostcontextenabledrelayprovider": true} {
		variables["__relay_internal__pv__"+key] = value
	}
	return variables
}

func FetchTweet(ctx context.Context, cfg config.AppConfig, id string, options parser.ParseOptions) (parser.TweetData, error) {
	client, err := NewClient(Credentials{AuthToken: cfg.AuthToken, CSRFToken: cfg.CSRFToken}, cfg.ProxyURL)
	if err != nil {
		return parser.TweetData{}, err
	}
	variables := tweetVariables(cfg.AuthToken != "")
	variables["restId"] = id
	variables["withFocalTweet"] = true
	queryID := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_TWEET_RESULT_QUERY_ID"))
	if queryID == "" {
		queryID = "m6saLeawhcvnYmZi1Zzgmw"
	}
	payload, err := client.graphQL(ctx, "/graphql/"+queryID+"/TweetResultByRestId", url.Values{"variables": {encodeVariables(variables)}})
	if err != nil {
		return parser.TweetData{}, err
	}
	result := gjson.GetBytes(payload, "data.tweet_result_by_rest_id.result")
	if !result.Exists() {
		return parser.TweetData{}, fmt.Errorf("推文详情接口没有返回正文")
	}
	return parser.TweetFromGraphQLResultWithOptions("", "", id, result, options)
}

// Full text has its own operation and durable page boundary. Never use UserMedia's watermark.
func (p *Pool) WalkUserTweets(ctx context.Context, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error {
	var requester timelineRequester = p
	if userMediaNeedsPrimaryClient(user) {
		client := p.Primary()
		if client == nil {
			return errors.New("没有可用 X 客户端")
		}
		requester = client
	}
	return walkUserTweets(ctx, requester, user, options, save)
}

func walkUserTweets(ctx context.Context, requester timelineRequester, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error {
	queryID := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_USER_TWEETS_QUERY_ID"))
	if queryID == "" {
		queryID = "jiF1DduqwzFSPmvRffg82g"
	}
	endpoint := "/graphql/" + queryID + "/UserTweets"
	cursor, head := options.Cursor, options.HeadID
	seen := map[string]bool{}
	seenCursors := map[string]bool{}
	for page := 0; page < 1000; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if page > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(paginationPageDelay):
			}
		}
		variables := map[string]any{"screenName": user.ScreenName, "count": 100, "cursor": cursor, "sortByMostLiked": false}
		for key, value := range map[string]bool{
			"xclientsfeaturesappviewerisloggedinrelayprovider":                  true,
			"xclientsfeaturestranslationgroktranslatedpostenabledrelayprovider": true,
			"replydownvoteenabledrelayprovider":                                 false, "postctafetchenabledrelayprovider": false,
			"xclientsfeaturestweetcomponentsgrokanalyzepostrelayprovider":        false,
			"xclientsfeaturestranslationnoteautotranslationenabledrelayprovider": false,
			"liveeventpostcontextenabledrelayprovider":                           false, "sportspostcontextenabledrelayprovider": false,
		} {
			variables["__relay_internal__pv__"+key] = value
		}
		payload, err := requester.graphQL(ctx, endpoint, url.Values{"variables": {encodeVariables(variables)}})
		if err != nil {
			return err
		}
		items, next, err := fullTimelineItems(payload)
		if err != nil {
			return err
		}
		batch := TweetTimelinePage{Tweets: []parser.TweetData{}, HeadID: head}
		reached := false
		lastOwnerID := ""
		for _, result := range items {
			tweet, err := parser.TweetFromGraphQLResultWithOptions("", user.ScreenName, "", result.Result, options.Parse)
			if errors.Is(err, parser.ErrTweetUnavailable) {
				continue
			}
			if err != nil {
				return fmt.Errorf("全文解析失败，保留上页进度: %w", err)
			}
			if tweet.Author.ID == "" {
				return fmt.Errorf("推文 %s 缺少作者 ID，未推进全文进度", tweet.ID)
			}
			if tweet.Author.ID != user.ID {
				continue
			}
			if seen[tweet.ID] {
				continue
			}
			seen[tweet.ID] = true
			if !result.Pinned {
				lastOwnerID = tweet.ID
				head = maxTweetID(head, tweet.ID)
			}
			if options.StopID != "" && !tweetIDNewerThan(tweet.ID, options.StopID) {
				if !result.Pinned {
					reached = true
				}
				continue
			}
			batch.Tweets = append(batch.Tweets, tweet)
		}
		// A pinned older post cannot terminate a page that still contains newer posts.
		if page == 0 && reached && tweetIDNewerThan(lastOwnerID, options.StopID) {
			reached = false
		}
		batch.HeadID = head
		batch.Cursor = next
		batch.Complete = reached || next == ""
		if !batch.Complete && (next == cursor || seenCursors[next]) {
			return errors.New("全文时间线游标未前进，保留进度以供重试")
		}
		if err := save(batch); err != nil {
			return err
		}
		if batch.Complete {
			return nil
		}
		seenCursors[next] = true
		cursor = next
	}
	return errors.New("全文时间线超过安全页数上限，保留进度以供继续补抓")
}

type fullTimelineTweet struct {
	Result gjson.Result
	Pinned bool
}

func fullTimelineItems(payload []byte) ([]fullTimelineTweet, string, error) {
	root := gjson.ParseBytes(payload)
	for _, path := range []string{"data.user_result_by_screen_name.result.profile_with_replies_timeline_v2.timeline.instructions", "data.user.result.timeline_v2.timeline.instructions"} {
		instructions := root.Get(path)
		if !instructions.IsArray() {
			continue
		}
		if path == "data.user.result.timeline_v2.timeline.instructions" {
			items, next, err := timelineItems(payload, path)
			if err != nil {
				return nil, "", err
			}
			results := []fullTimelineTweet{}
			for _, item := range items {
				if r := item.Get("tweet_results.result"); r.Exists() {
					results = append(results, fullTimelineTweet{Result: r})
				}
			}
			return results, next, nil
		}
		results := []fullTimelineTweet{}
		next := ""
		recognized := len(instructions.Array()) == 0
		for _, instruction := range instructions.Array() {
			kind := instruction.Get("__typename").String()
			switch kind {
			case "TimelineAddEntries", "TimelineReplaceEntry", "TimelinePinEntry":
				recognized = true
			case "TimelineAddToModule":
				recognized = true
				for _, item := range instruction.Get("module_items").Array() {
					var result gjson.Result
					for _, path := range []string{"item.content.tweet_results.result", "item.itemContent.tweet_results.result", "content.tweet_results.result"} {
						if candidate := item.Get(path); candidate.Exists() {
							result = candidate
							break
						}
					}
					if result.Exists() {
						results = append(results, fullTimelineTweet{Result: result})
					} else if item.Get("item.content.__typename").String() == "TimelineTweet" || item.Get("item.itemContent.__typename").String() == "TimelineTweet" {
						return nil, "", errors.New("全文模块缺少推文内容")
					}
				}
				continue
			case "TimelineTerminateTimeline", "TimelineClearCache":
				recognized = true
				continue
			default:
				return nil, "", fmt.Errorf("无法识别全文 instruction: %s", kind)
			}
			entries := instruction.Get("entries").Array()
			if entry := instruction.Get("entry"); entry.Exists() {
				entries = append(entries, entry)
			}
			for _, entry := range entries {
				content := entry.Get("content")
				switch content.Get("__typename").String() {
				case "TimelineTimelineCursor":
					if strings.EqualFold(content.Get("cursor_type").String(), "bottom") {
						next = content.Get("value").String()
					}
				case "TimelineTimelineItem":
					if result := content.Get("content.tweet_results.result"); result.Exists() {
						results = append(results, fullTimelineTweet{Result: result, Pinned: kind == "TimelinePinEntry"})
					} else if content.Get("content.__typename").String() == "TimelineTweet" {
						return nil, "", errors.New("全文条目缺少推文内容")
					}
				case "TimelineTimelineModule":
					for _, item := range content.Get("items").Array() {
						found := false
						for _, p := range []string{"item.content.tweet_results.result", "item.itemContent.tweet_results.result"} {
							if r := item.Get(p); r.Exists() {
								results = append(results, fullTimelineTweet{Result: r})
								found = true
								break
							}
						}
						if !found && item.Get("item.content.__typename").String() == "TimelineTweet" {
							return nil, "", errors.New("全文模块缺少推文内容")
						}
					}
				default:
					return nil, "", fmt.Errorf("无法识别全文 entry: %s", content.Get("__typename").String())
				}
			}
		}
		if !recognized {
			return nil, "", errors.New("无法识别全文时间线结构")
		}
		return results, next, nil
	}
	return nil, "", errors.New("全文接口没有返回 timeline；请检查 X Cookie 权限或更新接口，未推进归档进度")
}
