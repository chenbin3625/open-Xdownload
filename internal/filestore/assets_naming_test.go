package filestore

import (
	"strings"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestTemplatePathDisplayNameAndDateNamingStrategies(t *testing.T) {
	created := time.Date(2026, 11, 11, 8, 30, 0, 0, time.UTC)
	tweet := parser.TweetData{
		ID:        "2001",
		Text:      "秋日街景摄影",
		CreatedAt: created,
		Author: parser.Author{
			ID:         "42",
			Name:       "张三 / 摄影",
			ScreenName: "zhangsan",
		},
	}
	media := parser.Media{ID: "m1", Type: "photo"}

	rule := config.AssetRule{
		TargetID:          "local",
		DirectoryTemplate: "{display_name} (@{username})",
		FilenameTemplate:  "[{date}]{text}-{index}.{ext}",
	}
	got, err := TemplatePath(rule, config.AssetImage, tweet, media, 0, "jpg")
	if err != nil {
		t.Fatalf("TemplatePath() error = %v", err)
	}
	want := "张三 _ 摄影 (@zhangsan)/[2026-11-11]秋日街景摄影-1.jpg"
	if got != want {
		t.Fatalf("TemplatePath() = %q, want %q", got, want)
	}

	gotSecond, err := TemplatePath(rule, config.AssetImage, tweet, media, 1, "jpg")
	if err != nil {
		t.Fatalf("TemplatePath(index=1) error = %v", err)
	}
	wantSecond := "张三 _ 摄影 (@zhangsan)/[2026-11-11]秋日街景摄影-2.jpg"
	if gotSecond != wantSecond {
		t.Fatalf("TemplatePath(index=1) = %q, want %q", gotSecond, wantSecond)
	}

	// Empty display name falls back to screenName.
	noNameTweet := tweet
	noNameTweet.Author.Name = "   "
	gotFallback, err := TemplatePath(config.AssetRule{
		TargetID:          "local",
		DirectoryTemplate: "{kind}/{display_name} (@{username})",
		FilenameTemplate:  "{date}_{text}_{index}.{ext}",
	}, config.AssetVideo, noNameTweet, media, 0, "mp4")
	if err != nil {
		t.Fatalf("TemplatePath(fallback display_name) error = %v", err)
	}
	wantFallback := "video/zhangsan (@zhangsan)/2026-11-11_秋日街景摄影_1.mp4"
	if gotFallback != wantFallback {
		t.Fatalf("TemplatePath(fallback) = %q, want %q", gotFallback, wantFallback)
	}

	// Long tweet text still preserves [YYYY-MM-DD] prefix, index, and extension.
	longTweet := tweet
	longTweet.Text = strings.Repeat("很长的正文", 80)
	gotLong, err := TemplatePath(rule, config.AssetImage, longTweet, media, 2, "png")
	if err != nil {
		t.Fatalf("TemplatePath(long text) error = %v", err)
	}
	if !strings.Contains(gotLong, "/[2026-11-11]") || !strings.HasSuffix(gotLong, "-3.png") {
		t.Fatalf("TemplatePath(long text) = %q, want [2026-11-11] prefix and -3.png suffix", gotLong)
	}
}
