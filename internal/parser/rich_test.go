package parser

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestParseArticlePollMetricsAltTextAndAvatar(t *testing.T) {
	raw := `{
		"rest_id": "20001",
		"core": {
			"user_results": {
				"result": {
					"rest_id": "77",
					"core": {"name": "Alice", "screen_name": "alice"},
					"avatar": {"image_url": "https://pbs.twimg.com/profile_images/77/avatar_normal.jpg"}
				}
			}
		},
		"views": {"count": "9876"},
		"article": {
			"article_results": {
				"result": {
					"rest_id": "art1",
					"title": "Deep Dive into X",
					"preview_text": "Summary",
					"content_state": {"blocks": [{"text": "Para 1"}, {"text": "Para 2"}]},
					"cover_media": {"media_id": "cov1", "media_info": {"original_img_url": "https://pbs.twimg.com/media/cover.jpg", "original_img_width": 1600, "original_img_height": 900}}
				}
			}
		},
		"card": {
			"legacy": {
				"name": "poll2choice_text_only",
				"binding_values": [
					{"key": "choice1_label", "value": {"string_value": "Go"}},
					{"key": "choice1_count", "value": {"string_value": "120"}},
					{"key": "choice2_label", "value": {"string_value": "Rust"}},
					{"key": "choice2_count", "value": {"string_value": "80"}},
					{"key": "counts_are_final", "value": {"boolean_value": true}}
				]
			}
		},
		"legacy": {
			"full_text": "Check this out",
			"created_at": "Tue Nov 10 12:00:00 +0000 2026",
			"conversation_id_str": "20001",
			"favorite_count": 420,
			"retweet_count": 55,
			"reply_count": 12,
			"bookmark_count": 33,
			"extended_entities": {
				"media": [{
					"id_str": "m1",
					"type": "photo",
					"media_url_https": "https://pbs.twimg.com/media/photo1.jpg",
					"ext_alt_text": "Diagram of pipeline",
					"original_info": {"width": 1920, "height": 1080}
				}]
			}
		}
	}`
	tweet, err := TweetFromGraphQLResultWithOptions("", "alice", "20001", gjson.Parse(raw), ParseOptions{})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if tweet.Author.AvatarURL != "https://pbs.twimg.com/profile_images/77/avatar_normal.jpg" {
		t.Fatalf("avatar = %q", tweet.Author.AvatarURL)
	}
	if tweet.Metrics == nil || tweet.Metrics.Likes != 420 || tweet.Metrics.Views != 9876 || tweet.Metrics.Bookmarks != 33 {
		t.Fatalf("metrics = %+v", tweet.Metrics)
	}
	if tweet.Poll == nil || len(tweet.Poll.Choices) != 2 || tweet.Poll.TotalVotes != 200 || tweet.Poll.Choices[0].Label != "Go" {
		t.Fatalf("poll = %+v", tweet.Poll)
	}
	if tweet.Article == nil || tweet.Article.Title != "Deep Dive into X" || tweet.Article.PlainText != "Para 1\n\nPara 2" {
		t.Fatalf("article = %+v", tweet.Article)
	}
	if len(tweet.Media) != 2 || tweet.Media[0].AltText != "Diagram of pipeline" || tweet.Media[0].Width != 1920 {
		t.Fatalf("media = %+v", tweet.Media)
	}
}
