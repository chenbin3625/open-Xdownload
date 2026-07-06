package parser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

var tweetURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.)?(?:x|twitter)\.com/([^/?#]+)/status/([0-9]+)`)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

func (s *Service) ParseTweetLink(ctx context.Context, rawURL string) (TweetData, error) {
	if err := ctx.Err(); err != nil {
		return TweetData{}, err
	}
	username, tweetID, err := ExtractTweetURL(rawURL)
	if err != nil {
		return TweetData{}, err
	}

	// X 的推文详情 GraphQL 接入点会在下一步从 tmd 迁入。当前先返回稳定的结构，
	// 让 Web 工作台和任务流能围绕同一个契约开发。
	return TweetData{
		ID:        tweetID,
		URL:       canonicalTweetURL(username, tweetID),
		Text:      "推文详情解析待接入 X GraphQL。当前已识别链接，可直接创建下载任务。",
		CreatedAt: time.Now().UTC(),
		Author: Author{
			ScreenName: username,
		},
		Media: []Media{},
	}, nil
}

func (s *Service) ParseTweetJSON(rawURL string, payload []byte) (TweetData, error) {
	username, tweetID, err := ExtractTweetURL(rawURL)
	if err != nil {
		return TweetData{}, err
	}
	result := gjson.GetBytes(payload, "data.threaded_conversation_with_injections_v2.instructions.0.entries.0.content.itemContent.tweet_results.result")
	if !result.Exists() {
		result = gjson.GetBytes(payload, "data.tweetResult.result")
	}
	if !result.Exists() {
		return TweetData{}, errors.New("payload 中没有找到 tweet result")
	}
	return tweetFromResult(rawURL, username, tweetID, result)
}

func ExtractTweetURL(rawURL string) (string, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", "", errors.New("链接不能为空")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("无效链接: %s", rawURL)
	}
	matches := tweetURLPattern.FindStringSubmatch(rawURL)
	if len(matches) != 3 {
		return "", "", errors.New("请输入 x.com 或 twitter.com 的推文链接")
	}
	return matches[1], matches[2], nil
}

func BestVariant(variants []MediaVariant) MediaVariant {
	if len(variants) == 0 {
		return MediaVariant{}
	}
	sort.SliceStable(variants, func(i, j int) bool {
		if variants[i].Bitrate == variants[j].Bitrate {
			return variants[i].ContentType > variants[j].ContentType
		}
		return variants[i].Bitrate > variants[j].Bitrate
	})
	for _, variant := range variants {
		if strings.Contains(variant.ContentType, "mp4") {
			return variant
		}
	}
	return variants[0]
}

func tweetFromResult(rawURL string, fallbackUsername string, fallbackID string, result gjson.Result) (TweetData, error) {
	if result.Get("__typename").String() == "TweetWithVisibilityResults" {
		result = result.Get("tweet")
	}
	legacy := result.Get("legacy")
	if !legacy.Exists() {
		return TweetData{}, errors.New("payload 中没有 legacy tweet")
	}
	id := result.Get("rest_id").String()
	if id == "" {
		id = fallbackID
	}
	author := parseAuthor(result.Get("core.user_results.result"), fallbackUsername)
	createdAt := time.Now().UTC()
	if raw := legacy.Get("created_at").String(); raw != "" {
		if parsed, err := time.Parse(time.RubyDate, raw); err == nil {
			createdAt = parsed
		}
	}
	return TweetData{
		ID:        id,
		URL:       canonicalTweetURL(author.ScreenName, id),
		Text:      legacy.Get("full_text").String(),
		CreatedAt: createdAt,
		Author:    author,
		Media:     parseMedia(legacy.Get("extended_entities.media")),
	}, nil
}

func parseAuthor(result gjson.Result, fallbackUsername string) Author {
	legacy := result.Get("legacy")
	author := Author{
		ID:         result.Get("rest_id").String(),
		Name:       legacy.Get("name").String(),
		ScreenName: legacy.Get("screen_name").String(),
	}
	if author.ScreenName == "" {
		author.ScreenName = fallbackUsername
	}
	return author
}

func parseMedia(media gjson.Result) []Media {
	if !media.Exists() || !media.IsArray() {
		return nil
	}
	items := make([]Media, 0, len(media.Array()))
	for _, item := range media.Array() {
		kind := MediaType(item.Get("type").String())
		parsed := Media{
			ID:         item.Get("id_str").String(),
			Type:       kind,
			URL:        item.Get("media_url_https").String(),
			PreviewURL: item.Get("media_url_https").String(),
		}
		if kind == MediaVideo || kind == MediaGIF {
			for _, variant := range item.Get("video_info.variants").Array() {
				u := variant.Get("url").String()
				if u == "" {
					continue
				}
				parsed.Variants = append(parsed.Variants, MediaVariant{
					URL:         u,
					ContentType: variant.Get("content_type").String(),
					Bitrate:     variant.Get("bitrate").Int(),
					Quality:     qualityFromURL(u),
				})
			}
			best := BestVariant(parsed.Variants)
			parsed.BestURL = best.URL
		} else {
			parsed.BestURL = parsed.URL
		}
		items = append(items, parsed)
	}
	return items
}

func qualityFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(parsed.Path, "/")
	for _, part := range parts {
		if strings.Contains(part, "x") && strings.Contains(part, ".") {
			return strings.TrimSuffix(part, ".mp4")
		}
	}
	return ""
}

func canonicalTweetURL(username string, tweetID string) string {
	if username == "" {
		username = "i"
	}
	return fmt.Sprintf("https://x.com/%s/status/%s", username, tweetID)
}
