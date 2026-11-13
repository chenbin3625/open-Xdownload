package xapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/tidwall/gjson"
)

// MaxConsecutiveEmptyPages 是空页（items 为空但 Bottom cursor 仍在前进）连续出现
// 多少次后停止翻页，防止异常响应让单个用户翻满 1000 页、空耗 API 配额。
const MaxConsecutiveEmptyPages = 3

// PaginationPageDelay 是列表/关注/时间线分页之间的请求间隔，避免连续高频请求触发
// 短时限流/WAF。
const PaginationPageDelay = 250 * time.Millisecond

// UserMediaNeedsPrimaryClient 报告该用户的时间线是否必须由主账号视角读取。
// 受保护且已关注的用户，其媒体时间线只有关注者账号可见。
func UserMediaNeedsPrimaryClient(user User) bool {
	return user.Protected && user.Following
}

// GetUserTimeline 保留旧的两返回值形状。
func GetUserTimeline(ctx context.Context, requester Requester, user User, options parser.ParseOptions) ([]parser.TweetData, error) {
	tweets, _, err := GetUserTimelineWithStats(ctx, requester, user, options)
	return tweets, err
}

func GetUserTimelineWithStats(ctx context.Context, requester Requester, user User, options parser.ParseOptions) ([]parser.TweetData, MediaFetchStats, error) {
	if !user.Visible() {
		return nil, MediaFetchStats{}, nil
	}
	tweets := []parser.TweetData{}
	stats, err := WalkUserMediaPages(ctx, requester, user, UserMediaTimelineOptions{
		StopID: options.StopAtTweetID,
		Parse:  options,
	}, func(page UserMediaTimelinePage) error {
		tweets = append(tweets, page.Tweets...)
		return nil
	})
	return tweets, stats, err
}

// WalkUserMediaPages 逐页抓取 UserMedia 时间线，并在每页解析/早停判断完成后调用 save。
// 调用方可以在 save 里处理本页媒体并持久化 page.Cursor；与 GetUserTimelineWithStats
// 的"先攒完整条时间线再返回"不同，这里的内存占用只与本页大小相关。
//
// 早停、置顶、空页、cursor 不前进等语义与旧实现完全一致，便于既有测试与行为保持。
func WalkUserMediaPages(ctx context.Context, requester Requester, user User, options UserMediaTimelineOptions, save func(UserMediaTimelinePage) error) (MediaFetchStats, error) {
	stats := MediaFetchStats{}
	if !user.Visible() {
		return stats, nil
	}
	cursor := options.Cursor
	seenCursor := map[string]struct{}{}
	seenTweetIDs := map[string]struct{}{}
	parsedTweets := 0
	emptyPages := 0
	// 首页命中的 stopID 可能是置顶项；页尾仍比 stopID 新时必须继续翻页。
	// 为空时全程翻页（全量扫描）。
	reachedStop := false
	for page := 0; page < 1000; page++ {
		if page > 0 {
			// 翻页间微小间隔，防止连续高频请求触发 X 接口短时限流与 WAF
			select {
			case <-ctx.Done():
				return stats, ctx.Err()
			case <-time.After(PaginationPageDelay):
			}
		}
		values := UserMediaValues(user.ID, cursor)
		requestStarted := time.Now()
		payload, err := requester.GraphQL(ctx, UserMediaPath, values)
		stats.Requests++
		stats.APITime += time.Since(requestStarted)
		if err != nil {
			// 恢复用的游标可能已过期（用户删推/服务端分页令牌轮换）。显式分类后由上层
			// 清空游标、退回第 0 页重翻；已下载媒体会被去重跳过。
			if cursor != "" && isInvalidCursorError(err) {
				return stats, fmt.Errorf("%w: %v", ErrInvalidTimelineCursor, err)
			}
			return stats, err
		}
		items, next, err := TimelineItems(payload, "data.user.result.timeline_v2.timeline.instructions")
		if err != nil {
			return stats, err
		}
		stats.Pages++
		if len(items) == 0 {
			if next == "" {
				// 没有更多条目：本页即结束。
			} else {
				// 空页但 cursor 仍在前进：连续多次才停，避免单页被服务端过滤就提前结束；
				// 同时防止异常响应让单个用户翻满 1000 页。
				emptyPages++
				if emptyPages >= MaxConsecutiveEmptyPages {
					log.Printf("xclient: 时间线连续 %d 页为空但 cursor 仍在前进，停止翻页 (user=%s)", emptyPages, user.ID)
					next = ""
				}
			}
		} else {
			emptyPages = 0
		}
		lastTweetID := ""
		pageTweets := []parser.TweetData{}
		for _, item := range items {
			result := item.Get("tweet_results.result")
			if !result.Exists() {
				continue
			}
			// 先判断早停（在 media/parse 跳过之前）：直接从 rest_id 取 ID，使无媒体或解析
			// 失败的停止推文仍能触发早停，避免切换 IncludeNestedTweetMedia 或停止推文被删
			// 时全量重翻页、空耗 X API 配额。
			id := TweetResultID(result)
			if id != "" {
				lastTweetID = id
				stats.LastSeenTweetID = MaxTweetID(stats.LastSeenTweetID, id)
				if _, ok := seenTweetIDs[id]; ok {
					// 翻页边界/置顶重复返回同一条推文：只保留一份。
					continue
				}
				seenTweetIDs[id] = struct{}{}
			}
			if options.StopID != "" && !reachedStop && ShouldStopAt(id, options.StopID, page) {
				// 不能立即 return：page 0 的精确命中可能落在置顶推文上——上次归档时它恰为
				// 最新推文（newestTweetID 游标就是它），而其后仍排着本次新增的推文。继续
				// 扫完本页，只保留数值更新的项，再根据页尾判断是否继续翻页。
				reachedStop = true
			}
			if reachedStop {
				// 已到上次归档位置：数值不新于 stopID 的推文均已归档（含 stopID 本身），跳过；
				// 仅保留比 stopID 新的（置顶命中场景）。
				if !TweetIDNewerThan(id, options.StopID) {
					continue
				}
			}
			tweet, err := parser.TweetFromGraphQLResultWithOptions("", user.ScreenName, "", result, options.Parse)
			if errors.Is(err, parser.ErrTweetUnavailable) {
				continue
			}
			if err != nil {
				// 单条推文解析失败只跳过并计数：不再让整个用户的时间线归档失败
				// （否则一条无法解析的推文会让增量游标永远无法推进）。
				stats.SkippedTweets++
				stats.SkippedTweetIDs = append(stats.SkippedTweetIDs, id)
				log.Printf("xclient: 跳过无法解析的推文 %s (user=%s): %v", id, user.ID, err)
				continue
			}
			if len(tweet.Media) == 0 {
				continue
			}
			pageTweets = append(pageTweets, tweet)
		}
		if page == 0 && reachedStop && TweetIDNewerThan(lastTweetID, options.StopID) {
			reachedStop = false
		}
		complete := reachedStop || next == ""
		if !complete {
			if next == cursor {
				// 服务端没有推进 cursor：立即结束，避免死循环。
				complete = true
			} else if _, seen := seenCursor[next]; seen {
				// 回退到已见过的游标：同样结束，避免循环。
				complete = true
			}
		}
		batch := UserMediaTimelinePage{Tweets: pageTweets, Cursor: next, Complete: complete}
		if err := save(batch); err != nil {
			return stats, err
		}
		parsedTweets += len(pageTweets)
		if complete {
			// 安全检查：timeline 返回了推文条目但全部被解析失败跳过。这通常意味着 X 改了
			// 推文结构或 legacy 字段缺失，而非"该用户没有媒体推文"。若当成正常空结果返回，
			// 上层会推进增量游标，导致这些推文的媒体被永久跳过。
			if parsedTweets == 0 && stats.SkippedTweets > 0 && len(seenTweetIDs) == stats.SkippedTweets {
				return stats, fmt.Errorf("时间线返回了 %d 条推文但全部解析失败（user=%s，跳过的推文: %s）",
					stats.SkippedTweets, user.ID, strings.Join(stats.SkippedTweetIDs, ", "))
			}
			return stats, nil
		}
		seenCursor[next] = struct{}{}
		cursor = next
	}
	return stats, errors.New("媒体时间线超过安全页数上限，保留进度以供继续")
}

// WalkUserTweets 逐页抓取全文时间线。全文有独立的 operation 和持久化页边界，
// 不能复用 UserMedia 的水位线。
func WalkUserTweets(ctx context.Context, requester Requester, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error {
	endpoint := UserTweetsPath()
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
			case <-time.After(PaginationPageDelay):
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
		payload, err := requester.GraphQL(ctx, endpoint, url.Values{"variables": {EncodeVariables(variables)}})
		if err != nil {
			if cursor != "" && isInvalidCursorError(err) {
				return fmt.Errorf("%w: %v", ErrInvalidTimelineCursor, err)
			}
			return err
		}
		items, next, err := FullTimelineItems(payload)
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
				head = MaxTweetID(head, tweet.ID)
			}
			if options.StopID != "" && !TweetIDNewerThan(tweet.ID, options.StopID) {
				if !result.Pinned {
					reached = true
				}
				continue
			}
			batch.Tweets = append(batch.Tweets, tweet)
		}
		// A pinned older post cannot terminate a page that still contains newer posts.
		if page == 0 && reached && TweetIDNewerThan(lastOwnerID, options.StopID) {
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

type FullTimelineTweet struct {
	Result gjson.Result
	Pinned bool
}

func FullTimelineItems(payload []byte) ([]FullTimelineTweet, string, error) {
	root := gjson.ParseBytes(payload)
	for _, path := range []string{"data.user_result_by_screen_name.result.profile_with_replies_timeline_v2.timeline.instructions", "data.user.result.timeline_v2.timeline.instructions"} {
		instructions := root.Get(path)
		if !instructions.IsArray() {
			continue
		}
		if path == "data.user.result.timeline_v2.timeline.instructions" {
			items, next, err := TimelineItems(payload, path)
			if err != nil {
				return nil, "", err
			}
			results := []FullTimelineTweet{}
			pinned := map[string]bool{}
			for _, instruction := range instructions.Array() {
				if instruction.Get("type").String() == "TimelinePinEntry" {
					if id := TweetResultID(instruction.Get("entry.content.itemContent.tweet_results.result")); id != "" {
						pinned[id] = true
					}
				}
			}
			for _, item := range items {
				if r := item.Get("tweet_results.result"); r.Exists() {
					results = append(results, FullTimelineTweet{Result: r, Pinned: pinned[TweetResultID(r)]})
				}
			}
			return results, next, nil
		}
		results := []FullTimelineTweet{}
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
						results = append(results, FullTimelineTweet{Result: result})
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
						results = append(results, FullTimelineTweet{Result: result, Pinned: kind == "TimelinePinEntry"})
					} else if content.Get("content.__typename").String() == "TimelineTweet" {
						return nil, "", errors.New("全文条目缺少推文内容")
					}
				case "TimelineTimelineModule":
					for _, item := range content.Get("items").Array() {
						found := false
						for _, p := range []string{"item.content.tweet_results.result", "item.itemContent.tweet_results.result"} {
							if r := item.Get(p); r.Exists() {
								results = append(results, FullTimelineTweet{Result: r})
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

// GetListMembers 拉取列表全部成员。调用方需保证所有分页使用同一个账号视角
// （私有列表只有成员可见），这里只负责分页与去重。
func GetListMembers(ctx context.Context, requester Requester, list List) ([]User, error) {
	cursor := ""
	seenCursor := map[string]struct{}{}
	seenUserIDs := map[string]struct{}{}
	users := []User{}
	emptyPages := 0
	for page := 0; page < 1000; page++ {
		if page > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(PaginationPageDelay):
			}
		}
		payload, err := requester.GraphQL(ctx, ListMembersPath, ListMembersValues(list.ID, cursor))
		if err != nil {
			return nil, err
		}
		items, next, err := TimelineItems(payload, "data.list.members_timeline.timeline.instructions")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			if next == "" {
				break
			}
			emptyPages++
			if emptyPages >= MaxConsecutiveEmptyPages {
				log.Printf("xclient: 列表成员连续 %d 页为空但 cursor 仍在前进，停止翻页 (list=%s)", emptyPages, list.ID)
				break
			}
		} else {
			emptyPages = 0
		}
		for _, item := range items {
			user, err := ParseUser(item.Get("user_results"))
			if err != nil || user.ID == "" {
				continue
			}
			if _, ok := seenUserIDs[user.ID]; ok {
				continue
			}
			seenUserIDs[user.ID] = struct{}{}
			users = append(users, user)
		}
		if next == "" {
			break
		}
		if next == cursor {
			break
		}
		if _, ok := seenCursor[next]; ok {
			break
		}
		seenCursor[next] = struct{}{}
		cursor = next
	}
	return users, nil
}

// GetFollowing 拉取关注列表。调用方需保证受保护用户由主账号视角读取。
func GetFollowing(ctx context.Context, requester Requester, user User) ([]User, error) {
	cursor := ""
	seenCursor := map[string]struct{}{}
	seenUserIDs := map[string]struct{}{}
	users := []User{}
	emptyPages := 0
	for page := 0; page < 1000; page++ {
		if page > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(PaginationPageDelay):
			}
		}
		payload, err := requester.GraphQL(ctx, FollowingPath, FollowingValues(user.ID, cursor))
		if err != nil {
			return nil, err
		}
		items, next, err := TimelineItems(payload, "data.user.result.timeline.timeline.instructions")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			if next == "" {
				break
			}
			emptyPages++
			if emptyPages >= MaxConsecutiveEmptyPages {
				log.Printf("xclient: 关注列表连续 %d 页为空但 cursor 仍在前进，停止翻页 (user=%s)", emptyPages, user.ID)
				break
			}
		} else {
			emptyPages = 0
		}
		for _, item := range items {
			parsed, err := ParseUser(item.Get("user_results"))
			if err != nil || parsed.ID == "" {
				continue
			}
			if _, ok := seenUserIDs[parsed.ID]; ok {
				continue
			}
			seenUserIDs[parsed.ID] = struct{}{}
			users = append(users, parsed)
		}
		if next == "" {
			break
		}
		if next == cursor {
			break
		}
		if _, ok := seenCursor[next]; ok {
			break
		}
		seenCursor[next] = struct{}{}
		cursor = next
	}
	return users, nil
}

// FetchUserPage 抓取成员/关注分页的一页。调用方负责选定账号并补上 ViewerKey。
func FetchUserPage(ctx context.Context, requester Requester, path, instructions string, variables map[string]any, cursor string) (UserPage, error) {
	variables["cursor"] = cursor
	values := url.Values{"variables": {EncodeVariables(variables)}, "features": {TimelineFeatures}}
	payload, err := requester.GraphQL(ctx, path, values)
	if err != nil {
		return UserPage{}, err
	}
	items, next, err := TimelineItems(payload, instructions)
	if err != nil {
		return UserPage{}, err
	}
	page := UserPage{Cursor: next}
	for _, item := range items {
		user, err := ParseUser(item.Get("user_results"))
		if err == nil && user.ID != "" {
			page.Users = append(page.Users, user)
		}
	}
	return page, nil
}

func isInvalidCursorError(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusBadRequest {
		return mentionsInvalidCursor(httpErr.Payload)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return mentionsInvalidCursor(apiErr.Raw)
	}
	return false
}

func mentionsInvalidCursor(payload string) bool {
	lower := strings.ToLower(payload)
	return strings.Contains(lower, "cursor") || strings.Contains(lower, "invalid")
}
