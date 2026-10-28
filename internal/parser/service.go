package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/tidwall/gjson"
)

var (
	tweetURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.|mobile\.)?(?:x|twitter)\.com/([^/?#]+|i/web)/status/([0-9]+)(?:[/?#]|$)`)
	// 仅匹配主机为 twimg.com（及其子域 pbs./video. 等）的 URL：主机部分必须紧跟 :// 之后，
	// 避免 `http://攻击者/?pbs.twimg.com=x.mp4` 这类把 twimg.com 放在 query 里的伪造 URL
	// 被提取并随后由 downloader 拉取（SSRF，如云元数据端点）。
	mediaURLPattern = regexp.MustCompile(`https?://(?:[a-z0-9-]+\.)*twimg\.com(?:/[^\s"'<>\\]*)?`)
	// qualityPattern 匹配 video.twimg.com 路径中的分辨率段，如 1280x720。
	qualityPattern = regexp.MustCompile(`(?i)^(\d{2,5})x(\d{2,5})$`)
)

const syndicationTweetResultURL = "https://cdn.syndication.twimg.com/tweet-result"

var ErrTweetUnavailable = errors.New("推文不可用（已删除或受限）")

type Service struct {
	client         *http.Client
	syndicationURL string
}

type ParseOptions struct {
	IncludeNestedTweets bool

	// StopAtTweetID 用于增量归档：getUserTimeline 翻页时遇到该推文即停止，
	// 因为 timeline 按时间倒序，遇到已归档的旧推文即可早停，避免全量拉取。
	// 仅在配置开启增量归档时由 manager 传入；为空表示全量扫描。
	StopAtTweetID string
}

func NewService() *Service {
	return &Service{
		client:         httpx.Client("", 20*time.Second),
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

// ParseTweetLinkWithClient 使用调用方提供的 http.Client 解析推文。httpapi 用它把配置的
// 代理注入本次请求（M1），使单推解析链路与 timeline/下载一致地走代理。
func (s *Service) ParseTweetLinkWithClient(ctx context.Context, rawURL string, client *http.Client, options ParseOptions) (TweetData, error) {
	if err := ctx.Err(); err != nil {
		return TweetData{}, err
	}
	username, tweetID, err := ExtractTweetURL(rawURL)
	if err != nil {
		return TweetData{}, err
	}
	return s.parseSyndicationTweetWithClient(ctx, rawURL, username, tweetID, options, client)
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
	// 只接受 mp4：没有 mp4 时不退回 HLS（m3u8）。项目里没有 HLS 下载实现，退回只会
	// 把一个文本播放列表当成视频存下来。
	for _, variant := range allowed {
		if isMP4Variant(variant) {
			return variant
		}
	}
	return MediaVariant{}
}

func tweetFromResult(rawURL string, fallbackUsername string, fallbackID string, result gjson.Result, options ParseOptions) (TweetData, error) {
	if result.Get("__typename").String() == "TweetWithVisibilityResults" {
		result = result.Get("tweet")
	}
	if kind := result.Get("__typename").String(); kind == "TweetTombstone" || kind == "TweetUnavailable" {
		return TweetData{}, ErrTweetUnavailable
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
	createdAt, err := tweetCreatedAt(legacy.Get("created_at").String(), time.RubyDate, id)
	if err != nil {
		return TweetData{}, err
	}
	text := result.Get("note_tweet.note_tweet_results.result.text").String()
	if text == "" {
		text = legacy.Get("full_text").String()
	}
	return TweetData{
		ID:        id,
		URL:       canonicalTweetURL(author.ScreenName, id),
		Text:      text,
		CreatedAt: createdAt,
		Author:    author,
		Media:     parseTweetResultMedia(result, options),
	}, nil
}

func parseAuthor(result gjson.Result, fallbackUsername string) Author {
	// X 已把 screen_name / name 从 legacy 迁到 core；新字段优先，旧结构兜底。
	legacy := result.Get("legacy")
	author := Author{
		ID:         result.Get("rest_id").String(),
		Name:       firstString(result.Get("core.name"), legacy.Get("name")),
		ScreenName: firstString(result.Get("core.screen_name"), legacy.Get("screen_name")),
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
	return s.parseSyndicationTweetWithClient(ctx, rawURL, fallbackUsername, fallbackID, options, s.client)
}

func (s *Service) parseSyndicationTweetWithClient(ctx context.Context, rawURL string, fallbackUsername string, fallbackID string, options ParseOptions, client *http.Client) (TweetData, error) {
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

	if client == nil {
		client = s.client
		if client == nil {
			client = httpx.Client("", 20*time.Second)
		}
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
	// 已删除/受限推文返回 TweetTombstone：不能当成"一条没有媒体的推文"静默处理。
	if kind := result.Get("__typename").String(); kind == "TweetTombstone" || kind == "TweetUnavailable" {
		return TweetData{}, ErrTweetUnavailable
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
	createdAt, err := tweetCreatedAt(result.Get("created_at").String(), time.RFC3339Nano, id)
	if err != nil {
		return TweetData{}, err
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

func tweetCreatedAt(raw, layout, id string) (time.Time, error) {
	if raw != "" {
		parsed, err := time.Parse(layout, raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("推文 %s 的 created_at 无效: %w", id, err)
		}
		return parsed, nil
	}
	// X snowflake IDs encode the creation time; missing metadata must not
	// turn an archived post into a post dated at the time of download.
	snowflake, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("推文 %s 缺少有效创建时间", id)
	}
	return time.UnixMilli(int64(snowflake>>22) + 1288834974657).UTC(), nil
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
	for _, item := range result.Get("photos").Array() {
		rawURL := item.Get("url").String()
		if rawURL == "" {
			continue
		}
		// 与 mediaFromURL 一致：仅接受 twimg.com 域下的媒体 URL，拒绝伪造 photos
		// 指向内网/任意主机（SSRF）。
		if !isTwimgMediaURL(rawURL) {
			continue
		}
		// 走 appendUniqueMedia 而不是自己比对 rawURL 字符串：syndication 的 photos 与
		// mediaDetails 常常是同一份图片的不同写法（`.jpg` 对 `?format=jpg`），按原始
		// 字符串去重会漏判，把同一张图片重复下载一次。
		appendUniqueMedia(items, seen, []Media{{
			ID:         stableMediaID(rawURL),
			Type:       MediaPhoto,
			URL:        rawURL,
			PreviewURL: rawURL,
			BestURL:    rawURL,
		}})
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
		// 无 Snowflake ID 时用归一化 URL 的稳定 hash，而不是把完整 URL/序号当 ID。
		media.ID = stableMediaID(firstString(item.Get("expanded_url"), item.Get("url"), item.Get("media_url_https")))
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
		// Media.ID 语义：优先 Snowflake ID（id_str/id）；缺失时由 stableMediaID 从
		// 归一化 URL 生成稳定 hash，下游可安全当去重/展示键使用。
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
		if media.ID == "" {
			media.ID = stableMediaID(mediaIdentityURL(media))
		}
		return media
	}
	if media.Type == "" {
		media.Type = MediaPhoto
	}
	media.BestURL = media.URL
	if media.ID == "" {
		media.ID = stableMediaID(mediaIdentityURL(media))
	}
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

	// 只有 unified_card 携带推文自身的媒体（图片/视频卡片）。其他 binding（普通链接卡片的
	// thumbnail_image_*、player_image 等）是链接预览配图或站点头像，不是推文媒体。
	return parseCardBindingMedia(bindings["unified_card"], mediaEntities)
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
	seen := map[string]struct{}{}
	for _, rawURL := range mediaURLPattern.FindAllString(binding, -1) {
		appendScannedMedia(&items, seen, mediaFromURL(rawURL))
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
			appendScannedMedia(items, seen, mediaFromURL(rawURL))
		}
		return
	}
	if result.Type != gjson.JSON {
		return
	}
	for _, rawURL := range mediaURLPattern.FindAllString(result.Raw, -1) {
		appendScannedMedia(items, seen, mediaFromURL(rawURL))
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
	if parsed.Scheme != "http" && parsed.Scheme != "https" || !isTwimgHost(parsed.Hostname()) {
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
		ID:         stableMediaID(rawURL),
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
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "twimg.com" || strings.HasSuffix(host, ".twimg.com")
}

// isTwimgMediaURL 报告 rawURL 是否可解析且主机为 twimg.com（或其子域），供从 JSON 中
// 提取媒体 URL 的路径复用，与 mediaFromURL 的拒绝规则保持一致。
func isTwimgMediaURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return isTwimgHost(parsed.Hostname())
}

// tweetPhotoPathPrefixes 是 pbs.twimg.com 上推文图片媒体（含视频/GIF 海报）的目录。
var tweetPhotoPathPrefixes = []string{
	"/media/",
	"/ext_tw_video_thumb/",
	"/amplify_video_thumb/",
	"/tweet_video_thumb/",
}

func isTweetPhotoPath(path string) bool {
	for _, prefix := range tweetPhotoPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isPhotoMediaURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// 图片媒体同样要求 twimg.com 主机，避免任意主机带图片扩展名的 URL 被当作媒体。
	if !isTwimgHost(parsed.Hostname()) {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "pbs.twimg.com" || strings.HasSuffix(host, ".pbs.twimg.com") {
		// pbs 上除了推文媒体还有头像（profile_images）、横幅（profile_banners）、链接卡片
		// 配图（card_img）等：卡片兜底扫描整段 JSON 时只能接受推文媒体目录。
		return isTweetPhotoPath(parsed.Path)
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

// appendScannedMedia 用于卡片兜底扫描：同一个视频的每个清晰度变体都会被扫到一次，
// 它们的身份键相同（媒体 ID）。appendUniqueMedia 保留先出现的那条，可能正好是最低
// 清晰度；这里改为保留路径里分辨率最高的那条。扫描结果都来自 mediaFromURL，替换时
// 不会丢掉海报地址或 Snowflake ID。
func appendScannedMedia(items *[]Media, seen map[string]struct{}, media Media) {
	key := mediaKey(media)
	if key == "" {
		return
	}
	if _, ok := seen[key]; !ok {
		seen[key] = struct{}{}
		*items = append(*items, media)
		return
	}
	for index, existing := range *items {
		if mediaKey(existing) == key {
			if resolutionPixels(media.BestURL) > resolutionPixels(existing.BestURL) {
				(*items)[index] = media
			}
			return
		}
	}
}

// resolutionPixels 返回 URL 路径里分辨率段（如 1280x720）的像素数，没有时返回 0。
func resolutionPixels(rawURL string) int64 {
	width, height, ok := strings.Cut(qualityFromURL(rawURL), "x")
	if !ok {
		return 0
	}
	w, _ := strconv.ParseInt(width, 10, 64)
	h, _ := strconv.ParseInt(height, 10, 64)
	return w * h
}

// mediaKey 返回用于同一条推文内去重的媒体身份键。键按 downloader.MediaIdentity 归一化，
// 使同一份媒体的不同 URL 写法（`.jpg` 与 `?format=jpg`、易变的 `?tag=`、大小写主机名）
// 收敛到同一个键：推文自身、被转推推文与引用推文的媒体列表常常重复列出同一份媒体，
// 未归一化时会被当成多份媒体各自下载一次。
//
// 图片的 `?name=` 尺寸变体（small/large/orig 等）同样收敛到同一个键：它们是同一张照片的
// 不同分辨率，下载侧按最大尺寸取回。
func mediaKey(media Media) string {
	return downloader.MediaIdentity(rawMediaKey(media))
}

func rawMediaKey(media Media) string {
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

// v8Base36 复刻 V8 Number.prototype.toString(radix) 的 DoubleToRadixCString 算法：
// 以 double 精度（delta = 0.5*ulp）逐位生成小数、四舍六入五成双，并在进位时回溯。
// 这是 X 前端 ((id/1e15)*Math.PI).toString(36) 的实际行为；仅用"最短往返"
// 会在小 ID（如 id=20）上少一位，与真实 token 不一致。
func v8Base36(value float64) (string, bool) {
	const radix = 36
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if value == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", false
	}
	negative := value < 0
	if negative {
		value = -value
	}
	integer := math.Floor(value)
	fraction := value - integer
	// 只计算到 double 精度：半个 ulp。
	delta := 0.5 * (math.Nextafter(value, math.Inf(1)) - value)
	if minDelta := math.Nextafter(0, 1); delta < minDelta {
		delta = minDelta
	}
	fracDigits := make([]byte, 0, 64)
	if fraction >= delta {
		for {
			fraction *= radix
			delta *= radix
			digit := int(fraction)
			if digit < 0 || digit >= radix {
				digit = 0
			}
			fracDigits = append(fracDigits, digits[digit])
			fraction -= float64(digit)
			// 四舍六入五成双；需要进位时回溯已写小数位。
			if fraction > 0.5 || (fraction == 0.5 && digit&1 == 1) {
				if fraction+delta > 1 {
					carried := false
					for len(fracDigits) > 0 {
						last := len(fracDigits) - 1
						previous := digitValue(fracDigits[last])
						fracDigits = fracDigits[:last]
						if previous+1 < radix {
							fracDigits = append(fracDigits, digits[previous+1])
							carried = true
							break
						}
					}
					if !carried {
						integer += 1
					}
					break
				}
			}
			if fraction < delta {
				break
			}
		}
	}
	text := strconv.FormatInt(int64(integer), 36)
	var builder strings.Builder
	if negative {
		builder.WriteByte('-')
	}
	builder.WriteString(text)
	if len(fracDigits) > 0 {
		builder.WriteByte('.')
		builder.Write(fracDigits)
	}
	return builder.String(), true
}

func digitValue(ch byte) int {
	switch {
	case ch >= '0' && ch <= '9':
		return int(ch - '0')
	case ch >= 'a' && ch <= 'z':
		return int(ch-'a') + 10
	default:
		return 0
	}
}

func syndicationToken(tweetID string) string {
	id, err := strconv.ParseFloat(tweetID, 64)
	if err != nil {
		return ""
	}
	value := id / 1e15 * math.Pi
	raw, ok := v8Base36(value)
	if !ok {
		return ""
	}
	// 与 X 前端 ((id/1e15)*Math.PI).toString(36).replace(/(0+|\.)/g,"") 一致：
	// 去掉所有 '0' 与小数点（不是只去尾部零）。
	return strings.Map(func(ch rune) rune {
		if ch == '0' || ch == '.' {
			return -1
		}
		return ch
	}, raw)
}

// qualityFromURL 从 video.twimg.com 路径里的 "1280x720" 这类分辨率段提取清晰度；
// 旧实现要求同一段同时含 'x' 和 '.'，实际 URL 永远不满足，导致 Quality 恒为空。
func qualityFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, part := range strings.Split(parsed.Path, "/") {
		if match := qualityPattern.FindStringSubmatch(part); match != nil {
			return match[1] + "x" + match[2]
		}
	}
	return ""
}

// stableMediaID 对没有 Snowflake ID（id_str/id）的媒体生成稳定身份 ID：对归一化后的
// 媒体 URL 取 FNV-64a。避免把完整 URL 当 ID（同一媒体不同写法会得到不同 ID），
// 也比 syndication 的 photo-N 序号稳定（序号会随 photos 数组顺序/内容变化）。
func stableMediaID(rawURL string) string {
	key := downloader.MediaIdentity(rawURL)
	if key == "" {
		return ""
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(key))
	return fmt.Sprintf("url-%016x", hash.Sum64())
}

// mediaIdentityURL 返回用于生成稳定 ID 的媒体 URL：优先下载地址，其次展示地址/变体。
func mediaIdentityURL(media Media) string {
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

func canonicalTweetURL(username string, tweetID string) string {
	if username == "" {
		username = "i"
	}
	return fmt.Sprintf("https://x.com/%s/status/%s", username, tweetID)
}
