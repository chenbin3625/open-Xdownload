package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

var tweetURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.)?(?:x|twitter)\.com/([^/?#]+)/status/([0-9]+)`)

const syndicationTweetResultURL = "https://cdn.syndication.twimg.com/tweet-result"

type Service struct {
	client         *http.Client
	syndicationURL string
}

func NewService() *Service {
	return &Service{
		client:         &http.Client{Timeout: 20 * time.Second},
		syndicationURL: syndicationTweetResultURL,
	}
}

func (s *Service) ParseTweetLink(ctx context.Context, rawURL string) (TweetData, error) {
	if err := ctx.Err(); err != nil {
		return TweetData{}, err
	}
	username, tweetID, err := ExtractTweetURL(rawURL)
	if err != nil {
		return TweetData{}, err
	}
	return s.parseSyndicationTweet(ctx, rawURL, username, tweetID)
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

func TweetFromGraphQLResult(rawURL string, fallbackUsername string, fallbackID string, result gjson.Result) (TweetData, error) {
	return tweetFromResult(rawURL, fallbackUsername, fallbackID, result)
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

func (s *Service) parseSyndicationTweet(ctx context.Context, rawURL string, fallbackUsername string, fallbackID string) (TweetData, error) {
	endpoint, err := url.Parse(s.syndicationURL)
	if err != nil {
		return TweetData{}, err
	}
	query := endpoint.Query()
	query.Set("id", fallbackID)
	query.Set("lang", "zh")
	query.Set("token", syndicationToken(fallbackID))
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return TweetData{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 open-Xdownload/0.1")

	client := s.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return TweetData{}, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return TweetData{}, fmt.Errorf("X 推文详情请求失败: HTTP %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return TweetData{}, err
	}
	if !json.Valid(payload) {
		return TweetData{}, errors.New("X 推文详情返回了非 JSON 内容")
	}
	result := gjson.ParseBytes(payload)
	if !result.Get("id_str").Exists() && !result.Get("__typename").Exists() {
		return TweetData{}, errors.New("X 推文详情响应中没有推文数据")
	}
	return tweetFromSyndication(rawURL, fallbackUsername, fallbackID, result)
}

func tweetFromSyndication(rawURL string, fallbackUsername string, fallbackID string, result gjson.Result) (TweetData, error) {
	id := result.Get("id_str").String()
	if id == "" {
		id = fallbackID
	}
	author := Author{
		ID:         result.Get("user.id_str").String(),
		Name:       result.Get("user.name").String(),
		ScreenName: result.Get("user.screen_name").String(),
	}
	if author.ScreenName == "" {
		author.ScreenName = fallbackUsername
	}
	createdAt := time.Now().UTC()
	if raw := result.Get("created_at").String(); raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			createdAt = parsed
		}
	}
	return TweetData{
		ID:        id,
		URL:       canonicalTweetURL(author.ScreenName, id),
		Text:      result.Get("text").String(),
		CreatedAt: createdAt,
		Author:    author,
		Media:     parseSyndicationMedia(result),
	}, nil
}

func parseSyndicationMedia(result gjson.Result) []Media {
	seen := map[string]struct{}{}
	items := []Media{}
	for _, item := range result.Get("mediaDetails").Array() {
		media := mediaFromSyndicationDetail(item)
		key := media.BestURL
		if key == "" {
			key = media.URL
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, media)
	}
	if len(items) > 0 {
		return items
	}
	for index, item := range result.Get("photos").Array() {
		rawURL := item.Get("url").String()
		if rawURL == "" {
			continue
		}
		if _, ok := seen[rawURL]; ok {
			continue
		}
		seen[rawURL] = struct{}{}
		items = append(items, Media{
			ID:         fmt.Sprintf("photo-%d", index+1),
			Type:       MediaPhoto,
			URL:        rawURL,
			PreviewURL: rawURL,
			BestURL:    rawURL,
		})
	}
	return items
}

func mediaFromSyndicationDetail(item gjson.Result) Media {
	kind := MediaType(item.Get("type").String())
	mediaURL := firstString(item.Get("media_url_https"), item.Get("media_url"))
	media := Media{
		ID:         firstString(item.Get("id_str"), item.Get("id")),
		Type:       kind,
		URL:        mediaURL,
		PreviewURL: mediaURL,
	}
	if media.ID == "" {
		media.ID = firstString(item.Get("expanded_url"), item.Get("url"), item.Get("media_url_https"))
	}
	if kind == MediaVideo || kind == MediaGIF {
		for _, variant := range firstResult(item.Get("video_info.variants"), item.Get("videoInfo.variants")).Array() {
			rawURL := variant.Get("url").String()
			if rawURL == "" {
				continue
			}
			media.Variants = append(media.Variants, MediaVariant{
				URL:         rawURL,
				ContentType: firstString(variant.Get("content_type"), variant.Get("contentType")),
				Bitrate:     variant.Get("bitrate").Int(),
				Quality:     qualityFromURL(rawURL),
			})
		}
		best := BestVariant(media.Variants)
		media.BestURL = best.URL
	} else {
		media.Type = MediaPhoto
		media.BestURL = media.URL
	}
	return media
}

func firstResult(values ...gjson.Result) gjson.Result {
	for _, value := range values {
		if value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func firstString(values ...gjson.Result) string {
	for _, value := range values {
		if raw := value.String(); raw != "" {
			return raw
		}
	}
	return ""
}

func syndicationToken(tweetID string) string {
	id, err := strconv.ParseFloat(tweetID, 64)
	if err != nil {
		return ""
	}
	value := id / 1e15 * math.Pi
	raw := formatBase36Float(value)
	return strings.Map(func(ch rune) rune {
		if ch == '0' || ch == '.' {
			return -1
		}
		return ch
	}, raw)
}

func formatBase36Float(value float64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	integer := math.Floor(value)
	raw := strconv.FormatInt(int64(integer), 36)
	fraction := value - integer
	if fraction == 0 {
		return raw
	}
	var builder strings.Builder
	builder.WriteString(raw)
	builder.WriteByte('.')
	for i := 0; i < 20 && fraction > 0; i++ {
		fraction *= 36
		digit := int(fraction)
		if digit >= 36 {
			digit = 35
		}
		builder.WriteByte(digits[digit])
		fraction -= float64(digit)
	}
	return builder.String()
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
