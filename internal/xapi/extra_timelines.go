package xapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/tidwall/gjson"
)

// ExtendedSource 扩展了 xapi.Source，提供书签、点赞、长串对话与关键词搜索能力。
// 通过独立扩展接口保持对既有单测 mock 的零破坏性兼容。
type ExtendedSource interface {
	Source
	WalkBookmarks(ctx context.Context, folderID string, options TweetTimelineOptions, save func(TweetTimelinePage) error) error
	WalkLikes(ctx context.Context, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error
	FetchTweetThread(ctx context.Context, tweetID string, includeReplies bool, options parser.ParseOptions) ([]parser.TweetData, error)
	WalkSearchTimeline(ctx context.Context, rawQuery, product string, options TweetTimelineOptions, save func(TweetTimelinePage) error) error
}

// extractGenericTimelineTweets 从常见 X GraphQL 时间线结构中提取推文条目和 Bottom 游标。
func extractGenericTimelineTweets(payload []byte, instructionPaths ...string) ([]gjson.Result, string, error) {
	root := gjson.ParseBytes(payload)
	for _, path := range instructionPaths {
		instructions := root.Get(path)
		if !instructions.IsArray() {
			continue
		}
		tweets := []gjson.Result{}
		next := ""
		for _, instruction := range instructions.Array() {
			kind := instruction.Get("__typename").String()
			switch kind {
			case "TimelineAddEntries", "TimelineReplaceEntry", "TimelinePinEntry":
				entries := instruction.Get("entries").Array()
				if e := instruction.Get("entry"); e.Exists() {
					entries = append(entries, e)
				}
				for _, entry := range entries {
					content := entry.Get("content")
					entryType := content.Get("__typename").String()
					switch entryType {
					case "TimelineTimelineCursor":
						if strings.EqualFold(content.Get("cursorType").String(), "bottom") ||
							strings.EqualFold(content.Get("cursor_type").String(), "bottom") {
							next = content.Get("value").String()
						}
					case "TimelineTimelineItem":
						for _, p := range []string{"itemContent.tweet_results.result", "content.tweet_results.result"} {
							if r := content.Get(p); r.Exists() {
								tweets = append(tweets, r)
								break
							}
						}
					case "TimelineTimelineModule":
						for _, item := range content.Get("items").Array() {
							for _, p := range []string{"item.itemContent.tweet_results.result", "item.content.tweet_results.result"} {
								if r := item.Get(p); r.Exists() {
									tweets = append(tweets, r)
									break
								}
							}
						}
					}
				}
			case "TimelineAddToModule":
				for _, item := range instruction.Get("moduleItems").Array() {
					for _, p := range []string{"item.itemContent.tweet_results.result", "item.content.tweet_results.result"} {
						if r := item.Get(p); r.Exists() {
							tweets = append(tweets, r)
							break
						}
					}
				}
				for _, item := range instruction.Get("module_items").Array() {
					for _, p := range []string{"item.itemContent.tweet_results.result", "item.content.tweet_results.result"} {
						if r := item.Get(p); r.Exists() {
							tweets = append(tweets, r)
							break
						}
					}
				}
			}
		}
		return tweets, next, nil
	}
	return nil, "", errors.New("未在 X 接口响应中找到有效时间线结构")
}

func walkGenericTweetTimeline(
	ctx context.Context,
	requester Requester,
	endpoint string,
	buildValues func(cursor string) url.Values,
	instructionPaths []string,
	options TweetTimelineOptions,
	maxPages int,
	save func(TweetTimelinePage) error,
) error {
	if maxPages <= 0 || maxPages > 500 {
		maxPages = 200
	}
	cursor, head := options.Cursor, options.HeadID
	seen := map[string]bool{}
	seenCursors := map[string]bool{}
	consecutiveEmpty := 0
	for page := 0; page < maxPages; page++ {
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
		payload, err := requester.GraphQL(ctx, endpoint, buildValues(cursor))
		if err != nil {
			if cursor != "" && isInvalidCursorError(err) {
				return fmt.Errorf("%w: %v", ErrInvalidTimelineCursor, err)
			}
			return err
		}
		rawTweets, next, err := extractGenericTimelineTweets(payload, instructionPaths...)
		if err != nil {
			return err
		}
		batch := TweetTimelinePage{Tweets: []parser.TweetData{}, HeadID: head}
		reachedStop := false
		for _, res := range rawTweets {
			tweet, err := parser.TweetFromGraphQLResultWithOptions("", "", "", res, options.Parse)
			if errors.Is(err, parser.ErrTweetUnavailable) {
				continue
			}
			if err != nil {
				log.Printf("xapi: 跳过无法解析的时间线推文 %s: %v", TweetResultID(res), err)
				continue
			}
			if tweet.ID == "" {
				continue
			}
			if options.StopID != "" && !TweetIDNewerThan(tweet.ID, options.StopID) {
				reachedStop = true
				continue
			}
			if seen[tweet.ID] {
				continue
			}
			seen[tweet.ID] = true
			head = MaxTweetID(head, tweet.ID)
			batch.Tweets = append(batch.Tweets, tweet)
		}
		batch.HeadID = head
		batch.Cursor = next
		if len(batch.Tweets) == 0 {
			consecutiveEmpty++
		} else {
			consecutiveEmpty = 0
		}
		batch.Complete = reachedStop || next == "" || next == cursor || seenCursors[next] || consecutiveEmpty >= MaxConsecutiveEmptyPages
		if err := save(batch); err != nil {
			return err
		}
		if batch.Complete {
			return nil
		}
		seenCursors[next] = true
		cursor = next
	}
	return nil
}

// WalkBookmarks 逐页抓取登录账号的书签（或指定书签文件夹）时间线。
func WalkBookmarks(ctx context.Context, requester Requester, folderID string, options TweetTimelineOptions, save func(TweetTimelinePage) error) error {
	folderID = strings.TrimSpace(folderID)
	if folderID != "" && !strings.EqualFold(folderID, "all") {
		return walkGenericTweetTimeline(
			ctx, requester, BookmarkFolderPath,
			func(cursor string) url.Values { return BookmarkFolderValues(folderID, cursor) },
			[]string{
				"data.bookmark_collection_timeline.timeline.instructions",
				"data.bookmark_timeline_v2.timeline.instructions",
			},
			options, 200, save,
		)
	}
	return walkGenericTweetTimeline(
		ctx, requester, BookmarksPath,
		BookmarksValues,
		[]string{
			"data.bookmark_timeline_v2.timeline.instructions",
			"data.bookmark_timeline.timeline.instructions",
		},
		options, 200, save,
	)
}

// WalkLikes 逐页抓取用户点赞时间线。
func WalkLikes(ctx context.Context, requester Requester, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error {
	return walkGenericTweetTimeline(
		ctx, requester, LikesPath,
		func(cursor string) url.Values { return LikesValues(user.ID, cursor) },
		[]string{
			"data.user.result.timeline_v2.timeline.instructions",
			"data.user.result.timeline.timeline.instructions",
		},
		options, 200, save,
	)
}

// WalkSearchTimeline 逐页抓取关键词或高级搜索时间线。
func WalkSearchTimeline(ctx context.Context, requester Requester, rawQuery, product string, options TweetTimelineOptions, save func(TweetTimelinePage) error) error {
	return walkGenericTweetTimeline(
		ctx, requester, SearchTimelinePath,
		func(cursor string) url.Values { return SearchTimelineValues(rawQuery, product, cursor) },
		[]string{
			"data.search_by_raw_query.search_timeline.timeline.instructions",
		},
		options, 100, save,
	)
}

// FetchTweetThread 使用 TweetDetail 抓取单条推文所属的完整对话长串（默认保留作者自回复长串，
// includeReplies=true 时保留同对话下的其他回复）。结果按发布时间升序排列。
func FetchTweetThread(ctx context.Context, requester Requester, tweetID string, includeReplies bool, options parser.ParseOptions) ([]parser.TweetData, error) {
	seen := map[string]parser.TweetData{}
	cursor := ""
	seenCursors := map[string]bool{}
	focalAuthorID := ""
	focalConvID := ""

	for page := 0; page < 20; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(PaginationPageDelay):
			}
		}
		payload, err := requester.GraphQL(ctx, TweetDetailPath, TweetDetailValues(tweetID, cursor))
		if err != nil {
			if page > 0 && len(seen) > 0 {
				break
			}
			return nil, err
		}
		rawTweets, next, err := extractGenericTimelineTweets(
			payload,
			"data.threaded_conversation_with_injections_v2.instructions",
			"data.threaded_conversation_with_injections.instructions",
		)
		if err != nil {
			if page > 0 && len(seen) > 0 {
				break
			}
			return nil, err
		}
		added := 0
		for _, res := range rawTweets {
			tweet, err := parser.TweetFromGraphQLResultWithOptions("", "", "", res, options)
			if err != nil || tweet.ID == "" {
				continue
			}
			if tweet.ID == tweetID {
				focalAuthorID = tweet.Author.ID
				if tweet.ConversationID != "" {
					focalConvID = tweet.ConversationID
				} else {
					focalConvID = tweet.ID
				}
			}
			if _, ok := seen[tweet.ID]; !ok {
				seen[tweet.ID] = tweet
				added++
			}
		}
		if next == "" || next == cursor || seenCursors[next] || added == 0 {
			break
		}
		seenCursors[next] = true
		cursor = next
	}
	if len(seen) == 0 {
		return nil, errors.New("未在长串响应中解析到推文")
	}
	result := make([]parser.TweetData, 0, len(seen))
	for _, tweet := range seen {
		if focalConvID != "" && tweet.ConversationID != "" && tweet.ConversationID != focalConvID && tweet.ID != tweetID {
			continue
		}
		if !includeReplies && focalAuthorID != "" && tweet.Author.ID != "" && tweet.Author.ID != focalAuthorID && tweet.ID != tweetID {
			continue
		}
		result = append(result, tweet)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}
