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

var (
	tweetURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.)?(?:x|twitter)\.com/([^/?#]+)/status/([0-9]+)`)
	// 仅匹配主机为 twimg.com（及其子域 pbs./video. 等）的 URL：主机部分必须紧跟 :// 之后，
	// 避免 `http://攻击者/?pbs.twimg.com=x.mp4` 这类把 twimg.com 放在 query 里的伪造 URL
	// 被提取并随后由 downloader 拉取（SSRF，如云元数据端点）。
	mediaURLPattern = regexp.MustCompile(`https?://(?:[a-z0-9-]+\.)*twimg\.com(?:/[^\s"'<>\\]*)?`)
)

const syndicationTweetResultURL = "https://cdn.syndication.twimg.com/tweet-result"

type Service struct {
	client         *http.Client
	syndicationURL string
}

type ParseOptions struct {
	IncludeNestedTweets bool

	// StopAtTweetID 用于增量归档：getUserTimeline 翻页时遇到该推文即停止，
	// 因为 timeline 按时间倒序，遇到已归档的旧推文即可早停，避免全量拉取。
	StopAtTweetID string
}

func NewService() *Service {
	return &Service{
		client:         &http.Client{Timeout: 20 * time.Second},
		syndicationURL: syndicationTweetResultURL,
	}
}

func (s *Service) ParseTweetLink(ctx context.Context, rawURL string) (TweetData, error) {
	return s.ParseTweetLinkWithOptions(ctx, rawURL, ParseOptions{})
}

func (s *Service) ParseTweetLinkWithOptions(ctx context.Context, rawURL string, options ParseOptions) (TweetData, error) {
	if err := ctx.Err(); err != nil {
		return TweetData{}, err
	}
	username, tweetID, err := ExtractTweetURL(rawURL)
	if err != nil {
		return TweetData{}, err
	}
	return s.parseSyndicationTweet(ctx, rawURL, username, tweetID, options)
}

func (s *Service) ParseTweetJSON(rawURL string, payload []byte) (TweetData, error) {
	return s.ParseTweetJSONWithOptions(rawURL, payload, ParseOptions{})
}

func (s *Service) ParseTweetJSONWithOptions(rawURL string, payload []byte, options ParseOptions) (TweetData, error) {
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
	return tweetFromResult(rawURL, username, tweetID, result, options)
}

func TweetFromGraphQLResult(rawURL string, fallbackUsername string, fallbackID string, result gjson.Result) (TweetData, error) {
	return TweetFromGraphQLResultWithOptions(rawURL, fallbackUsername, fallbackID, result, ParseOptions{})
}

func TweetFromGraphQLResultWithOptions(rawURL string, fallbackUsername string, fallbackID string, result gjson.Result, options ParseOptions) (TweetData, error) {
	return tweetFromResult(rawURL, fallbackUsername, fallbackID, result, options)
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
	// 仅从 twimg.com 主机的变体中挑选最佳版本，拒绝伪造 video_info.variants 注入的
	// 任意主机 URL（SSRF），与 mediaFromURL 的 isTwimgHost 校验保持一致。
	allowed := make([]MediaVariant, 0, len(variants))
	for _, variant := range variants {
		if isTwimgMediaURL(variant.URL) {
			allowed = append(allowed, variant)
		}
	}
	if len(allowed) == 0 {
		return MediaVariant{}
	}
	sort.SliceStable(allowed, func(i, j int) bool {
		if allowed[i].Bitrate == allowed[j].Bitrate {
			return allowed[i].ContentType > allowed[j].ContentType
		}
		return allowed[i].Bitrate > allowed[j].Bitrate
	})
	for _, variant := range allowed {
		if isMP4Variant(variant) {
			return variant
		}
	}
	return allowed[0]
}

func tweetFromResult(rawURL string, fallbackUsername string, fallbackID string, result gjson.Result, options ParseOptions) (TweetData, error) {
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
		Media:     parseTweetResultMedia(result, options),
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
		parsed := mediaFromDetail(item)
		items = append(items, parsed)
	}
	return items
}

func parseTweetResultMedia(result gjson.Result, options ParseOptions) []Media {
	items := []Media{}
	seen := map[string]struct{}{}
	collectTweetResultMedia(result, &items, seen, options, 0)
	return items
}

func collectTweetResultMedia(result gjson.Result, items *[]Media, seen map[string]struct{}, options ParseOptions, depth int) {
	if !result.Exists() || depth > 2 {
		return
	}
	result = unwrapTweetResult(result)
	legacy := result.Get("legacy")
	if legacy.Exists() {
		appendUniqueMedia(items, seen, parseMedia(legacy.Get("extended_entities.media")))
		appendUniqueMedia(items, seen, parseMedia(legacy.Get("entities.media")))
	}
	appendUniqueMedia(items, seen, parseCardMedia(result.Get("card")))

	if !options.IncludeNestedTweets {
		return
	}
	for _, path := range []string{
		"legacy.retweeted_status_result.result",
		"retweeted_status_result.result",
		"quoted_status_result.result",
		"legacy.quoted_status_result.result",
	} {
		if nested := result.Get(path); nested.Exists() {
			collectTweetResultMedia(nested, items, seen, options, depth+1)
		}
	}
}

func unwrapTweetResult(result gjson.Result) gjson.Result {
	// 限制下钻深度，防止构造的深层嵌套 {"result":{"result":...}} payload 让循环 O(depth)、
	// 每轮 Get 再 O(depth)（O(depth^2)）而挂起解析器。与兄弟递归 helper 的深度上限一致。
	const maxDepth = 8
	for depth := 0; depth < maxDepth; depth++ {
		if result.Get("__typename").String() == "TweetWithVisibilityResults" && result.Get("tweet").Exists() {
			result = result.Get("tweet")
			continue
		}
		if result.Get("result").Exists() && result.Get("legacy").Exists() == false {
			result = result.Get("result")
			continue
		}
		return result
	}
	return result
}

func (s *Service) parseSyndicationTweet(ctx context.Context, rawURL string, fallbackUsername string, fallbackID string, options ParseOptions) (TweetData, error) {
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
	return tweetFromSyndication(rawURL, fallbackUsername, fallbackID, result, options)
}

func tweetFromSyndication(rawURL string, fallbackUsername string, fallbackID string, result gjson.Result, options ParseOptions) (TweetData, error) {
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
		Media:     parseSyndicationMedia(result, options),
	}, nil
}

func parseSyndicationMedia(result gjson.Result, options ParseOptions) []Media {
	seen := map[string]struct{}{}
	items := []Media{}
	collectSyndicationMedia(result, &items, seen, options, 0)
	return items
}

func collectSyndicationMedia(result gjson.Result, items *[]Media, seen map[string]struct{}, options ParseOptions, depth int) {
	if !result.Exists() || depth > 2 {
		return
	}
	for _, item := range result.Get("mediaDetails").Array() {
		media := mediaFromSyndicationDetail(item)
		appendUniqueMedia(items, seen, []Media{media})
	}
	appendUniqueMedia(items, seen, parseCardMedia(result.Get("card")))
	for index, item := range result.Get("photos").Array() {
		rawURL := item.Get("url").String()
		if rawURL == "" {
			continue
		}
		// 与 mediaFromURL 一致：仅接受 twimg.com 域下的媒体 URL，拒绝伪造 photos
		// 指向内网/任意主机（SSRF）。
		if !isTwimgMediaURL(rawURL) {
			continue
		}
		if _, ok := seen[rawURL]; ok {
			continue
		}
		seen[rawURL] = struct{}{}
		*items = append(*items, Media{
			ID:         fmt.Sprintf("photo-%d", index+1),
			Type:       MediaPhoto,
			URL:        rawURL,
			PreviewURL: rawURL,
			BestURL:    rawURL,
		})
	}
	if !options.IncludeNestedTweets {
		return
	}
	for _, path := range []string{"quoted_tweet", "retweeted_tweet", "quoted_status", "retweeted_status"} {
		if nested := result.Get(path); nested.Exists() {
			collectSyndicationMedia(nested, items, seen, options, depth+1)
		}
	}
}

func mediaFromSyndicationDetail(item gjson.Result) Media {
	media := mediaFromDetail(item)
	if media.ID == "" {
		media.ID = firstString(item.Get("expanded_url"), item.Get("url"), item.Get("media_url_https"))
	}
	return media
}

func mediaFromDetail(item gjson.Result) Media {
	kind := MediaType(item.Get("type").String())
	mediaURL := firstString(item.Get("media_url_https"), item.Get("media_url"), item.Get("url"))
	// 与 mediaFromURL 一致：仅接受 twimg.com 域下的媒体 URL，拒绝伪造 media entity
	// 指向内网/任意主机（SSRF，如云元数据 169.254.169.254）。
	if !isTwimgMediaURL(mediaURL) {
		mediaURL = ""
	}
	media := Media{
		ID:         firstString(item.Get("id_str"), item.Get("id")),
		Type:       kind,
		URL:        mediaURL,
		PreviewURL: mediaURL,
	}
	if kind == MediaVideo || kind == MediaGIF {
		media.Variants = parseVariantList(firstResult(item.Get("video_info.variants"), item.Get("videoInfo.variants")))
		best := BestVariant(media.Variants)
		media.BestURL = best.URL
		if media.PreviewURL == "" {
			media.PreviewURL = media.URL
		}
		return media
	}
	if media.Type == "" {
		media.Type = MediaPhoto
	}
	media.BestURL = media.URL
	return media
}

func parseCardMedia(card gjson.Result) []Media {
	if !card.Exists() {
		return nil
	}
	bindings := cardBindings(card)
	mediaEntities := map[string]gjson.Result{}
	for _, path := range []string{
		"legacy.media_entities",
		"media_entities",
		"legacy.mediaEntities",
		"mediaEntities",
	} {
		raw := card.Get(path)
		if !raw.Exists() || !raw.IsObject() {
			continue
		}
		raw.ForEach(func(key, value gjson.Result) bool {
			mediaEntities[key.String()] = value
			return true
		})
	}

	items := []Media{}
	for _, binding := range bindings {
		for _, media := range parseCardBindingMedia(binding, mediaEntities) {
			items = append(items, media)
		}
	}
	return items
}

func parseCardBindingMedia(binding string, mediaEntities map[string]gjson.Result) []Media {
	binding = strings.TrimSpace(binding)
	if binding == "" {
		return nil
	}
	items := []Media{}
	if json.Valid([]byte(binding)) {
		result := gjson.Parse(binding)
		appendUniqueMedia(&items, map[string]struct{}{}, parseUnifiedCardMedia(result, mediaEntities))
		if len(items) > 0 {
			return items
		}
	}
	for _, rawURL := range mediaURLPattern.FindAllString(binding, -1) {
		if media := mediaFromURL(rawURL); mediaKey(media) != "" {
			items = append(items, media)
		}
	}
	return items
}

func parseUnifiedCardMedia(result gjson.Result, mediaEntities map[string]gjson.Result) []Media {
	items := []Media{}
	seen := map[string]struct{}{}
	for _, media := range result.Get("media_entities").Array() {
		appendUniqueMedia(&items, seen, []Media{mediaFromDetail(media)})
	}
	result.Get("media_entities").ForEach(func(_, media gjson.Result) bool {
		appendUniqueMedia(&items, seen, []Media{mediaFromDetail(media)})
		return true
	})
	for _, mediaID := range result.Get("component_objects.#.data.media_id").Array() {
		if entity, ok := mediaEntities[mediaID.String()]; ok {
			appendUniqueMedia(&items, seen, []Media{mediaFromDetail(entity)})
		}
	}
	for _, mediaID := range result.Get("destination_objects.#.data.media_id").Array() {
		if entity, ok := mediaEntities[mediaID.String()]; ok {
			appendUniqueMedia(&items, seen, []Media{mediaFromDetail(entity)})
		}
	}
	for _, mediaID := range collectMediaEntityIDs(result) {
		if entity, ok := mediaEntities[mediaID]; ok {
			appendUniqueMedia(&items, seen, []Media{mediaFromDetail(entity)})
		}
	}
	if len(items) == 0 {
		collectMediaURLs(result, &items, seen, 0)
	}
	return items
}

func collectMediaEntityIDs(result gjson.Result) []string {
	seen := map[string]struct{}{}
	items := []string{}
	collectMediaEntityIDsInto(result, seen, &items, 0)
	return items
}

func collectMediaEntityIDsInto(result gjson.Result, seen map[string]struct{}, items *[]string, depth int) {
	if !result.Exists() || result.Type != gjson.JSON || depth > 8 {
		return
	}
	result.ForEach(func(key, value gjson.Result) bool {
		name := strings.ToLower(key.String())
		if value.Type == gjson.String && (name == "media_id" || name == "media_key" || name == "id") {
			raw := value.String()
			if raw != "" {
				if _, ok := seen[raw]; !ok {
					seen[raw] = struct{}{}
					*items = append(*items, raw)
				}
			}
		}
		collectMediaEntityIDsInto(value, seen, items, depth+1)
		return true
	})
}

func collectMediaURLs(result gjson.Result, items *[]Media, seen map[string]struct{}, depth int) {
	if !result.Exists() || depth > 8 {
		return
	}
	if result.Type == gjson.String {
		for _, rawURL := range mediaURLPattern.FindAllString(result.String(), -1) {
			if media := mediaFromURL(rawURL); mediaKey(media) != "" {
				appendUniqueMedia(items, seen, []Media{media})
			}
		}
		return
	}
	if result.Type != gjson.JSON {
		return
	}
	for _, rawURL := range mediaURLPattern.FindAllString(result.Raw, -1) {
		if media := mediaFromURL(rawURL); mediaKey(media) != "" {
			appendUniqueMedia(items, seen, []Media{media})
		}
	}
	result.ForEach(func(_, value gjson.Result) bool {
		collectMediaURLs(value, items, seen, depth+1)
		return true
	})
}

func cardBindings(card gjson.Result) map[string]string {
	bindings := map[string]string{}
	for _, path := range []string{"legacy.binding_values", "binding_values", "legacy.bindingValues", "bindingValues"} {
		raw := card.Get(path)
		if !raw.Exists() {
			continue
		}
		if raw.IsArray() {
			for _, item := range raw.Array() {
				key := item.Get("key").String()
				value := cardBindingValue(item.Get("value"))
				if key != "" && value != "" {
					bindings[key] = value
				}
			}
			continue
		}
		if raw.IsObject() {
			raw.ForEach(func(key, value gjson.Result) bool {
				if binding := cardBindingValue(value); binding != "" {
					bindings[key.String()] = binding
				}
				return true
			})
		}
	}
	return bindings
}

func cardBindingValue(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	for _, path := range []string{"string_value", "stringValue", "scribe_key", "url", "value"} {
		if raw := value.Get(path).String(); raw != "" {
			return raw
		}
	}
	return value.String()
}

func parseVariantList(variants gjson.Result) []MediaVariant {
	if !variants.Exists() || !variants.IsArray() {
		return nil
	}
	items := []MediaVariant{}
	for _, variant := range variants.Array() {
		rawURL := variant.Get("url").String()
		if rawURL == "" {
			continue
		}
		// 仅接受 twimg.com 主机的变体 URL，拒绝伪造 video_info.variants 注入的任意主机
		// URL（SSRF），与 mediaFromURL 的 isTwimgHost 校验保持一致。
		if !isTwimgMediaURL(rawURL) {
			continue
		}
		items = append(items, MediaVariant{
			URL:         rawURL,
			ContentType: firstString(variant.Get("content_type"), variant.Get("contentType")),
			Bitrate:     variant.Get("bitrate").Int(),
			Quality:     qualityFromURL(rawURL),
		})
	}
	return items
}

func mediaFromURL(rawURL string) Media {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Media{}
	}
	// 仅接受 twimg.com 域下的媒体 URL，避免经伪造推文 card 内容注入的内网/任意主机 URL
	// 被提取并由 downloader 拉取（SSRF，如云元数据 169.254.169.254）。
	if !isTwimgHost(parsed.Host) {
		return Media{}
	}
	lower := strings.ToLower(rawURL)
	kind := MediaPhoto
	if strings.Contains(lower, ".mp4") {
		kind = MediaVideo
	} else if !isPhotoMediaURL(rawURL) {
		return Media{}
	}
	media := Media{
		ID:         rawURL,
		Type:       kind,
		URL:        rawURL,
		PreviewURL: rawURL,
		BestURL:    rawURL,
	}
	if kind == MediaVideo {
		media.Variants = []MediaVariant{{
			URL:         rawURL,
			ContentType: "video/mp4",
			Quality:     qualityFromURL(rawURL),
		}}
	}
	return media
}

// isTwimgHost 报告 host 是否为 twimg.com 或其子域（pbs.twimg.com / video.twimg.com 等）。
func isTwimgHost(host string) bool {
	host = strings.ToLower(host)
	return host == "twimg.com" || strings.HasSuffix(host, ".twimg.com")
}

// isTwimgMediaURL 报告 rawURL 是否可解析且主机为 twimg.com（或其子域），供从 JSON 中
// 提取媒体 URL 的路径复用，与 mediaFromURL 的拒绝规则保持一致。
func isTwimgMediaURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return isTwimgHost(parsed.Host)
}

func isPhotoMediaURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// 图片媒体同样要求 twimg.com 主机，避免任意主机带图片扩展名的 URL 被当作媒体。
	if !isTwimgHost(parsed.Host) {
		return false
	}
	if strings.Contains(strings.ToLower(parsed.Host), "pbs.twimg.com") {
		return true
	}
	switch strings.ToLower(pathExtension(parsed.Path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif":
		return true
	default:
		return false
	}
}

func pathExtension(value string) string {
	index := strings.LastIndex(value, ".")
	if index < 0 {
		return ""
	}
	return value[index:]
}

func appendUniqueMedia(items *[]Media, seen map[string]struct{}, mediaItems []Media) {
	for _, media := range mediaItems {
		key := mediaKey(media)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		*items = append(*items, media)
	}
}

func mediaKey(media Media) string {
	if media.BestURL != "" {
		return media.BestURL
	}
	if media.URL != "" {
		return media.URL
	}
	for _, variant := range media.Variants {
		if variant.URL != "" {
			return variant.URL
		}
	}
	return ""
}

func isMP4Variant(variant MediaVariant) bool {
	return strings.Contains(strings.ToLower(variant.ContentType), "mp4") || strings.Contains(strings.ToLower(variant.URL), ".mp4")
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
