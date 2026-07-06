package parser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
