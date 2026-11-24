package jobs

import (
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestFilterTweetByRules(t *testing.T) {
	filter := &config.ArchiveFilter{
		MediaTypes:      []string{"photo"},
		ExcludeRetweets: true,
		ExcludeReplies:  true,
		MinWidth:        800,
		SinceDate:       "2026-01-01",
	}
	rt := parser.TweetData{ID: "1", RetweetedTweetID: "99", CreatedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	if _, ok := filterTweetByRules(rt, filter); ok {
		t.Fatal("expected retweet to be excluded")
	}
	reply := parser.TweetData{ID: "2", ReplyToID: "88", CreatedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	if _, ok := filterTweetByRules(reply, filter); ok {
		t.Fatal("expected reply to be excluded")
	}
	old := parser.TweetData{ID: "3", CreatedAt: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)}
	if _, ok := filterTweetByRules(old, filter); ok {
		t.Fatal("expected tweet before sinceDate to be excluded")
	}
	mixed := parser.TweetData{
		ID:        "4",
		CreatedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Media: []parser.Media{
			{ID: "small", Type: parser.MediaPhoto, Width: 400, Height: 300},
			{ID: "big", Type: parser.MediaPhoto, Width: 1200, Height: 900},
			{ID: "vid", Type: parser.MediaVideo, Width: 1920, Height: 1080},
		},
	}
	kept, ok := filterTweetByRules(mixed, filter)
	if !ok || len(kept.Media) != 1 || kept.Media[0].ID != "big" {
		t.Fatalf("unexpected kept media: ok=%v media=%+v", ok, kept.Media)
	}
}
