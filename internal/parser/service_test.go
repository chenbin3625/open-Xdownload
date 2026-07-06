package parser

import "testing"

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
