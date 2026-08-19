package parser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
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
