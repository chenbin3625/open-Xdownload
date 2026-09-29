package parser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractTweetURL(t *testing.T) {
	username, id, err := ExtractTweetURL("https://x.com/openai/status/1234567890?s=20")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if username != "openai" || id != "1234567890" {
		t.Fatalf("unexpected parsed URL: %s %s", username, id)
	}
}

func TestBestVariantPrefersHighestMP4Bitrate(t *testing.T) {
	got := BestVariant([]MediaVariant{
		{URL: "https://video.twimg.com/low.mp4", ContentType: "video/mp4", Bitrate: 832000},
		{URL: "https://video.twimg.com/playlist.m3u8", ContentType: "application/x-mpegURL", Bitrate: 0},
		{URL: "https://video.twimg.com/high.mp4", ContentType: "video/mp4", Bitrate: 2176000},
	})
	if got.URL != "https://video.twimg.com/high.mp4" {
		t.Fatalf("unexpected best URL: %s", got.URL)
	}
}

func TestParseTweetLinkUsesSyndicationMedia(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != "1349129669258448897" {
			t.Fatalf("unexpected id query: %s", got)
		}
		if got := r.URL.Query().Get("token"); got == "" {
			t.Fatal("expected syndication token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"__typename": "Tweet",
			"id_str": "1349129669258448897",
			"text": "hello media",
			"created_at": "2021-01-12T23:02:33.000Z",
			"user": {"id_str": "44196397", "name": "Elon Musk", "screen_name": "elonmusk"},
			"mediaDetails": [{
				"id_str": "1",
				"type": "photo",
				"media_url_https": "https://pbs.twimg.com/media/example.jpg"
			}]
		}`))
	}))
	defer server.Close()

	service := NewService()
	service.client = server.Client()
	service.syndicationURL = server.URL

	tweet, err := service.ParseTweetLink(context.Background(), "https://x.com/elonmusk/status/1349129669258448897")
	if err != nil {
		t.Fatalf("ParseTweetLink returned error: %v", err)
	}
	if tweet.Author.ScreenName != "elonmusk" {
		t.Fatalf("unexpected author: %#v", tweet.Author)
	}
	urls := tweet.BestMediaURLs()
	if len(urls) != 1 || urls[0] != "https://pbs.twimg.com/media/example.jpg" {
		t.Fatalf("unexpected media urls: %#v", urls)
	}
}

func TestTweetFromGraphQLResultParsesUnifiedCardVideo(t *testing.T) {
	unifiedCard := `{
		"media_entities": {
			"card-video": {
				"id_str": "card-video",
				"type": "video",
				"media_url_https": "https://pbs.twimg.com/media/card.jpg",
				"video_info": {
					"variants": [
						{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/ext_tw_video/card/playlist.m3u8"},
						{"bitrate": 832000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/card/640x360.mp4"},
						{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/card/1280x720.mp4"}
					]
				}
			}
		},
		"component_objects": {
			"component-1": {"data": {"media_id": "card-video"}}
		}
	}`
	result := gjson.Parse(`{
		"__typename": "Tweet",
		"rest_id": "100",
		"core": {"user_results": {"result": {"rest_id": "u1", "legacy": {"name": "OpenAI", "screen_name": "openai"}}}},
		"legacy": {"full_text": "card video", "created_at": "Tue Jan 12 23:02:33 +0000 2021"},
		"card": {"legacy": {"binding_values": [
			{"key": "unified_card", "value": {"string_value": ` + strconv.Quote(unifiedCard) + `}}
		]}}
	}`)

	tweet, err := TweetFromGraphQLResult("", "openai", "100", result)
	if err != nil {
		t.Fatalf("TweetFromGraphQLResult returned error: %v", err)
	}
	urls := tweet.BestMediaURLs()
	if len(urls) != 1 || urls[0] != "https://video.twimg.com/ext_tw_video/card/1280x720.mp4" {
		t.Fatalf("unexpected media urls: %#v", urls)
	}
}

func TestTweetFromGraphQLResultParsesUnifiedCardImageFallback(t *testing.T) {
	unifiedCard := `{
		"component_objects": {
			"component-1": {"data": {"image_url": "https://pbs.twimg.com/media/card-image.jpg?format=jpg&name=large"}}
		}
	}`
	result := gjson.Parse(`{
		"__typename": "Tweet",
		"rest_id": "101",
		"core": {"user_results": {"result": {"rest_id": "u1", "legacy": {"name": "OpenAI", "screen_name": "openai"}}}},
		"legacy": {"full_text": "card image", "created_at": "Tue Jan 12 23:02:33 +0000 2021"},
		"card": {"legacy": {"binding_values": [
			{"key": "unified_card", "value": {"string_value": ` + strconv.Quote(unifiedCard) + `}}
		]}}
	}`)

	tweet, err := TweetFromGraphQLResult("", "openai", "101", result)
	if err != nil {
		t.Fatalf("TweetFromGraphQLResult returned error: %v", err)
	}
	urls := tweet.BestMediaURLs()
	if len(urls) != 1 || urls[0] != "https://pbs.twimg.com/media/card-image.jpg?format=jpg&name=large" {
		t.Fatalf("unexpected media urls: %#v", urls)
	}
	if tweet.Media[0].Type != MediaPhoto {
		t.Fatalf("media type = %s, want %s", tweet.Media[0].Type, MediaPhoto)
	}
}

func TestTweetFromGraphQLResultNestedTweetMediaRequiresOption(t *testing.T) {
	result := gjson.Parse(`{
		"__typename": "Tweet",
		"rest_id": "200",
		"core": {"user_results": {"result": {"legacy": {"name": "Root", "screen_name": "root"}}}},
		"legacy": {"full_text": "quote", "created_at": "Tue Jan 12 23:02:33 +0000 2021"},
		"quoted_status_result": {"result": {
			"__typename": "Tweet",
			"rest_id": "201",
			"core": {"user_results": {"result": {"legacy": {"name": "Quoted", "screen_name": "quoted"}}}},
			"legacy": {
				"full_text": "nested media",
				"created_at": "Tue Jan 12 23:03:33 +0000 2021",
				"extended_entities": {"media": [{
					"id_str": "nested-photo",
					"type": "photo",
					"media_url_https": "https://pbs.twimg.com/media/nested.jpg"
				}]}
			}
		}}
	}`)

	defaultTweet, err := TweetFromGraphQLResultWithOptions("", "root", "200", result, ParseOptions{})
	if err != nil {
		t.Fatalf("default parse returned error: %v", err)
	}
	if len(defaultTweet.Media) != 0 {
		t.Fatalf("default parse should ignore nested media: %#v", defaultTweet.Media)
	}

	nestedTweet, err := TweetFromGraphQLResultWithOptions("", "root", "200", result, ParseOptions{IncludeNestedTweets: true})
	if err != nil {
		t.Fatalf("nested parse returned error: %v", err)
	}
	urls := nestedTweet.BestMediaURLs()
	if len(urls) != 1 || urls[0] != "https://pbs.twimg.com/media/nested.jpg" {
		t.Fatalf("unexpected nested media urls: %#v", urls)
	}
}

func TestTweetFromGraphQLResultMergesEquivalentMediaAcrossRetweet(t *testing.T) {
	// 推文自身与被转推推文重复列出同一张照片，且写法各异（裸 .jpg、?format=jpg、
	// ?name=small 缩略图）。这些都是同一张照片，必须合并成一份；不同编码（png）才是
	// 另一份媒体。
	result := gjson.Parse(`{
		"__typename": "Tweet", "rest_id": "200",
		"legacy": {
			"extended_entities": {"media": [
				{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/shared.jpg"},
				{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/shared?format=jpg&name=small"}
			]}
		},
		"retweeted_status_result": {"result": {
			"legacy": {"extended_entities": {"media": [
				{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/shared?format=jpg"},
				{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/shared?format=jpg&name=large"},
				{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/shared?format=png"}
			]}}
		}}
	}`)
	tweet, err := TweetFromGraphQLResultWithOptions("", "root", "200", result, ParseOptions{IncludeNestedTweets: true})
	if err != nil {
		t.Fatal(err)
	}
	urls := tweet.BestMediaURLs()
	want := []string{
		"https://pbs.twimg.com/media/shared.jpg",
		"https://pbs.twimg.com/media/shared?format=png",
	}
	if len(urls) != len(want) {
		t.Fatalf("media URLs = %#v, want %#v", urls, want)
	}
	for index, url := range want {
		if urls[index] != url {
			t.Fatalf("media URLs = %#v, want %#v", urls, want)
		}
	}
}

func TestSyndicationMediaMergesPhotoURLForms(t *testing.T) {
	result := gjson.Parse(`{
		"mediaDetails": [{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/shared.jpg"}],
		"photos": [{"url": "https://pbs.twimg.com/media/shared?format=jpg"}]
	}`)
	items := parseSyndicationMedia(result, ParseOptions{})
	if len(items) != 1 {
		t.Fatalf("media = %#v, want one photo", items)
	}
}

func TestMediaFromDetailRejectsNonTwimgURL(t *testing.T) {
	twimg := gjson.Parse(`{
		"id_str": "p1",
		"type": "photo",
		"media_url_https": "https://pbs.twimg.com/media/example.jpg"
	}`)
	parsed := mediaFromDetail(twimg)
	if parsed.URL != "https://pbs.twimg.com/media/example.jpg" || parsed.BestURL != "https://pbs.twimg.com/media/example.jpg" {
		t.Fatalf("twimg photo not preserved: %#v", parsed)
	}

	evil := gjson.Parse(`{
		"id_str": "p2",
		"type": "photo",
		"media_url_https": "https://attacker.example/example.jpg"
	}`)
	parsed = mediaFromDetail(evil)
	if parsed.URL != "" || parsed.BestURL != "" || parsed.PreviewURL != "" {
		t.Fatalf("non-twimg photo not rejected: %#v", parsed)
	}
}

func TestMediaFromDetailRejectsNonTwimgVideoVariant(t *testing.T) {
	item := gjson.Parse(`{
		"id_str": "v1",
		"type": "video",
		"media_url_https": "https://pbs.twimg.com/media/video-poster.jpg",
		"video_info": {"variants": [
			{"bitrate": 832000, "content_type": "video/mp4", "url": "https://pbs.twimg.com/media/low.mp4"},
			{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://attacker.example/high.mp4"}
		]}
	}`)
	parsed := mediaFromDetail(item)
	if parsed.BestURL != "https://pbs.twimg.com/media/low.mp4" {
		t.Fatalf("best URL should fall back to twimg variant, got %q (variants %#v)", parsed.BestURL, parsed.Variants)
	}
	for _, variant := range parsed.Variants {
		if !isTwimgMediaURL(variant.URL) {
			t.Fatalf("non-twimg variant URL leaked into Variants: %#v", parsed.Variants)
		}
	}

	allEvil := gjson.Parse(`{
		"id_str": "v2",
		"type": "video",
		"media_url_https": "https://attacker.example/poster.jpg",
		"video_info": {"variants": [
			{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://attacker.example/high.mp4"}
		]}
	}`)
	parsed = mediaFromDetail(allEvil)
	if parsed.BestURL != "" || parsed.URL != "" || len(parsed.Variants) != 0 {
		t.Fatalf("all-non-twimg video not fully rejected: %#v", parsed)
	}
}

func TestBestVariantSkipsNonTwimgHosts(t *testing.T) {
	got := BestVariant([]MediaVariant{
		{URL: "https://attacker.example/high.mp4", ContentType: "video/mp4", Bitrate: 99999999},
		{URL: "https://video.twimg.com/low.mp4", ContentType: "video/mp4", Bitrate: 832000},
	})
	if got.URL != "https://video.twimg.com/low.mp4" {
		t.Fatalf("unexpected best URL: %q", got.URL)
	}
	empty := BestVariant([]MediaVariant{
		{URL: "https://attacker.example/high.mp4", ContentType: "video/mp4", Bitrate: 99999999},
	})
	if empty.URL != "" {
		t.Fatalf("all-non-twimg variants should yield empty best, got %q", empty.URL)
	}
}

func TestParseSyndicationMediaRejectsNonTwimgPhotos(t *testing.T) {
	result := gjson.Parse(`{
		"photos": [
			{"url": "https://pbs.twimg.com/media/ok.jpg"},
			{"url": "https://attacker.example/evil.jpg"}
		]
	}`)
	media := parseSyndicationMedia(result, ParseOptions{})
	if len(media) != 1 || media[0].BestURL != "https://pbs.twimg.com/media/ok.jpg" {
		t.Fatalf("unexpected photos media: %#v", media)
	}
}

func TestParseTweetLinkWithClientUsesProvidedClient(t *testing.T) {
	// M1：单推解析应使用调用方注入的 client（httpapi 用它带配置代理）。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"__typename": "Tweet",
			"id_str": "101",
			"text": "via client",
			"user": {"id_str": "u1", "name": "A", "screen_name": "a"},
			"mediaDetails": [{"id_str": "m1", "type": "photo", "media_url_https": "https://pbs.twimg.com/media/x.jpg"}]
		}`))
	}))
	defer server.Close()

	service := NewService()
	service.syndicationURL = server.URL
	// 故意不设置 service.client（保持 nil→DefaultClient 路径）；传入的 client 必须被使用。
	tweet, err := service.ParseTweetLinkWithClient(context.Background(), "https://x.com/a/status/101", server.Client(), ParseOptions{})
	if err != nil {
		t.Fatalf("ParseTweetLinkWithClient: %v", err)
	}
	if tweet.ID != "101" {
		t.Fatalf("tweet id = %q, want 101", tweet.ID)
	}
}

func TestBestVariantNeverPicksHLSPlaylist(t *testing.T) {
	// 没有 mp4 时不能退回 m3u8：仓库里没有 HLS 处理，下载到的只是一个文本播放列表。
	got := BestVariant([]MediaVariant{
		{URL: "https://video.twimg.com/ext_tw_video/1/pl/playlist.m3u8", ContentType: "application/x-mpegURL"},
	})
	if got.URL != "" {
		t.Fatalf("BestVariant picked %q, want no variant when only HLS is available", got.URL)
	}
}

func TestVideoWithoutPlayableVariantHasNoDownloadURL(t *testing.T) {
	item := gjson.Parse(`{
		"id_str": "m1",
		"type": "video",
		"media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/1/pu/img/thumb.jpg",
		"video_info": {"variants": [
			{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/ext_tw_video/1/pl/playlist.m3u8"}
		]}
	}`)
	media := mediaFromDetail(item)
	// 缩略图只能作预览：拿它当视频下载，会把一张 jpg 当成视频存档并标记为已完成。
	if media.BestURL != "" {
		t.Fatalf("BestURL = %q, want empty for a video without mp4 variants", media.BestURL)
	}
	if media.PreviewURL == "" {
		t.Fatal("thumbnail should still be kept as the preview")
	}
	tweet := TweetData{Media: []Media{media}}
	if urls := tweet.BestMediaURLs(); len(urls) != 0 {
		t.Fatalf("BestMediaURLs = %v, want no download URL for an unplayable video", urls)
	}
	if got := MediaDownloadURL(media); got != "" {
		t.Fatalf("MediaDownloadURL = %q, want empty for an unplayable video", got)
	}
}

func TestMediaDownloadURLForPhotoAndVideo(t *testing.T) {
	photo := Media{Type: MediaPhoto, URL: "https://pbs.twimg.com/media/a.jpg"}
	if got := MediaDownloadURL(photo); got != photo.URL {
		t.Fatalf("photo download URL = %q", got)
	}
	video := Media{Type: MediaVideo, URL: "https://pbs.twimg.com/ext_tw_video_thumb/t.jpg", BestURL: "https://video.twimg.com/v.mp4"}
	if got := MediaDownloadURL(video); got != video.BestURL {
		t.Fatalf("video download URL = %q", got)
	}
	legacy := Media{Type: MediaVideo, Variants: []MediaVariant{
		{URL: "https://video.twimg.com/p.m3u8", ContentType: "application/x-mpegURL"},
		{URL: "https://video.twimg.com/v.mp4", ContentType: "video/mp4", Bitrate: 1},
	}}
	if got := MediaDownloadURL(legacy); got != "https://video.twimg.com/v.mp4" {
		t.Fatalf("legacy payload without BestURL: download URL = %q, want the mp4 variant", got)
	}
}

func TestLinkPreviewCardIsNotTweetMedia(t *testing.T) {
	// 普通链接卡片（summary_large_image）的配图不是推文媒体：纯链接推文不应解析出"照片"。
	result := gjson.Parse(`{
		"__typename": "Tweet",
		"rest_id": "102",
		"core": {"user_results": {"result": {"rest_id": "u1", "legacy": {"name": "OpenAI", "screen_name": "openai"}}}},
		"legacy": {"full_text": "read this https://t.co/x", "created_at": "Tue Jan 12 23:02:33 +0000 2021"},
		"card": {"legacy": {"name": "summary_large_image", "binding_values": [
			{"key": "thumbnail_image_large", "value": {"image_value": {"url": "https://pbs.twimg.com/card_img/1/abc?format=jpg&name=800x419", "width": 800, "height": 419}, "type": "IMAGE"}},
			{"key": "title", "value": {"string_value": "An article", "type": "STRING"}},
			{"key": "site", "value": {"user_value": {"id_str": "9"}, "type": "USER"}}
		]}}
	}`)
	tweet, err := TweetFromGraphQLResult("", "openai", "102", result)
	if err != nil {
		t.Fatalf("TweetFromGraphQLResult returned error: %v", err)
	}
	if urls := tweet.BestMediaURLs(); len(urls) != 0 {
		t.Fatalf("link preview card produced media %v, want none", urls)
	}
}

func TestUnifiedCardFallbackIgnoresProfileImages(t *testing.T) {
	unifiedCard := `{
		"component_objects": {
			"details": {"data": {"title": {"content": "App"}}},
			"media": {"data": {"image_url": "https://pbs.twimg.com/media/card-image.jpg?format=jpg&name=large"}}
		},
		"users": {"9": {"profile_image_url_https": "https://pbs.twimg.com/profile_images/1/avatar_normal.jpg", "profile_banner_url": "https://pbs.twimg.com/profile_banners/9/1"}}
	}`
	result := gjson.Parse(`{
		"__typename": "Tweet",
		"rest_id": "103",
		"core": {"user_results": {"result": {"rest_id": "u1", "legacy": {"name": "OpenAI", "screen_name": "openai"}}}},
		"legacy": {"full_text": "card", "created_at": "Tue Jan 12 23:02:33 +0000 2021"},
		"card": {"legacy": {"binding_values": [
			{"key": "unified_card", "value": {"string_value": ` + strconv.Quote(unifiedCard) + `}}
		]}}
	}`)
	tweet, err := TweetFromGraphQLResult("", "openai", "103", result)
	if err != nil {
		t.Fatalf("TweetFromGraphQLResult returned error: %v", err)
	}
	urls := tweet.BestMediaURLs()
	if len(urls) != 1 || urls[0] != "https://pbs.twimg.com/media/card-image.jpg?format=jpg&name=large" {
		t.Fatalf("media urls = %#v, want only the card media image (no avatar/banner)", urls)
	}
}

func TestExtractTweetURLAcceptsMobileHosts(t *testing.T) {
	for _, raw := range []string{
		"https://mobile.twitter.com/openai/status/123",
		"https://mobile.x.com/openai/status/123",
		"https://www.x.com/openai/status/123",
	} {
		user, id, err := ExtractTweetURL(raw)
		if err != nil || user != "openai" || id != "123" {
			t.Errorf("ExtractTweetURL(%q) = %q, %q, %v", raw, user, id, err)
		}
	}
	if _, _, err := ExtractTweetURL("https://evil.x.com.example/openai/status/123"); err == nil {
		t.Error("lookalike host must be rejected")
	}
}

func TestSyndicationTombstoneIsReportedAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"__typename":"TweetTombstone","tombstone":{"text":{"text":"This Post was deleted"}}}`))
	}))
	defer server.Close()
	service := NewService()
	service.syndicationURL = server.URL
	// 已删除推文不能被当成"一条没有媒体的推文"：用户需要看到推文不可用。
	_, err := service.ParseTweetLink(context.Background(), "https://x.com/openai/status/1349129669258448897")
	if err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("err = %v, want an unavailable-tweet error", err)
	}
}

func TestParseAuthorReadsCoreFields(t *testing.T) {
	// X 已把 screen_name / name 从 user_results.result.legacy 迁到 result.core；
	// 新旧两种结构都要能解析。
	result := gjson.Parse(`{"rest_id": "u1", "core": {"screen_name": "openai", "name": "OpenAI"}, "legacy": {}}`)
	author := parseAuthor(result, "fallback")
	if author.ScreenName != "openai" || author.Name != "OpenAI" {
		t.Fatalf("author = %+v, want core.screen_name/core.name", author)
	}
	legacy := gjson.Parse(`{"rest_id": "u1", "legacy": {"screen_name": "old", "name": "Old"}}`)
	if author := parseAuthor(legacy, "fallback"); author.ScreenName != "old" || author.Name != "Old" {
		t.Fatalf("legacy author = %+v", author)
	}
}
