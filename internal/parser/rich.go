package parser

import (
	"html"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// parseTweetArticle 提取 X Articles（长文章）的标题、纯文本、封面图与内嵌图片。
func parseTweetArticle(result gjson.Result) *TweetArticle {
	article := firstResult(
		result.Get("article.article_results.result"),
		result.Get("article_results.result"),
		result.Get("article"),
	)
	if !article.Exists() || !article.IsObject() {
		return nil
	}
	title := html.UnescapeString(strings.TrimSpace(article.Get("title").String()))
	preview := html.UnescapeString(strings.TrimSpace(article.Get("preview_text").String()))
	plainText := html.UnescapeString(strings.TrimSpace(article.Get("plain_text").String()))
	if plainText == "" {
		// 从 content_state.blocks 拼装纯文本段落，适配 GraphQL 富文本结构。
		blocks := article.Get("content_state.blocks")
		if blocks.IsArray() {
			var paragraphs []string
			for _, block := range blocks.Array() {
				text := strings.TrimSpace(block.Get("text").String())
				if text != "" {
					paragraphs = append(paragraphs, html.UnescapeString(text))
				}
			}
			plainText = strings.Join(paragraphs, "\n\n")
		}
	}
	coverURL := firstString(
		article.Get("cover_media.media_info.original_img_url"),
		article.Get("cover_media.media_url_https"),
		article.Get("cover_image_url"),
	)
	if !isTwimgMediaURL(coverURL) {
		coverURL = ""
	}
	mediaItems := []Media{}
	seen := map[string]struct{}{}
	if coverURL != "" {
		coverID := strings.TrimSpace(article.Get("cover_media.media_id").String())
		if coverID == "" {
			coverID = stableMediaID(coverURL)
		}
		appendUniqueMedia(&mediaItems, seen, []Media{{
			ID:         coverID,
			Type:       MediaPhoto,
			URL:        coverURL,
			PreviewURL: coverURL,
			BestURL:    coverURL,
			Width:      int(article.Get("cover_media.media_info.original_img_width").Int()),
			Height:     int(article.Get("cover_media.media_info.original_img_height").Int()),
		}})
	}
	for _, entity := range article.Get("media_entities").Array() {
		rawURL := firstString(
			entity.Get("media_info.original_img_url"),
			entity.Get("media_url_https"),
			entity.Get("media_info.media_url_https"),
		)
		if !isTwimgMediaURL(rawURL) {
			continue
		}
		mediaID := strings.TrimSpace(entity.Get("media_id").String())
		if mediaID == "" {
			mediaID = stableMediaID(rawURL)
		}
		appendUniqueMedia(&mediaItems, seen, []Media{{
			ID:         mediaID,
			Type:       MediaPhoto,
			URL:        rawURL,
			PreviewURL: rawURL,
			BestURL:    rawURL,
			Width:      int(entity.Get("media_info.original_img_width").Int()),
			Height:     int(entity.Get("media_info.original_img_height").Int()),
		}})
	}
	if title == "" && preview == "" && plainText == "" && coverURL == "" && len(mediaItems) == 0 {
		return nil
	}
	return &TweetArticle{
		ID:            firstString(article.Get("rest_id"), article.Get("id")),
		Title:         title,
		PreviewText:   preview,
		PlainText:     plainText,
		CoverImageURL: coverURL,
		Media:         mediaItems,
	}
}

// parseTweetPoll 从 card.binding_values 解析推文投票选项、票数与状态快照。
func parseTweetPoll(card gjson.Result) *TweetPoll {
	if !card.Exists() {
		return nil
	}
	name := strings.ToLower(firstString(card.Get("legacy.name"), card.Get("name")))
	bindings := cardBindings(card)
	if !strings.Contains(name, "poll") && bindings["choice1_label"] == "" {
		return nil
	}
	choices := make([]PollChoice, 0, 4)
	var total int64
	for i := 1; i <= 4; i++ {
		label := html.UnescapeString(strings.TrimSpace(bindings["choice"+strconv.Itoa(i)+"_label"]))
		if label == "" {
			continue
		}
		votes, _ := strconv.ParseInt(strings.TrimSpace(bindings["choice"+strconv.Itoa(i)+"_count"]), 10, 64)
		if votes < 0 {
			votes = 0
		}
		total += votes
		choices = append(choices, PollChoice{Label: label, Votes: votes})
	}
	if len(choices) == 0 {
		return nil
	}
	return &TweetPoll{
		ID:           firstString(card.Get("rest_id"), card.Get("url"), card.Get("legacy.url")),
		EndDatetime:  strings.TrimSpace(bindings["end_datetime_utc"]),
		VotingStatus: strings.TrimSpace(bindings["counts_are_final"]),
		TotalVotes:   total,
		Choices:      choices,
	}
}

func parseTweetMetrics(result, legacy gjson.Result) *TweetMetrics {
	likes := legacy.Get("favorite_count").Int()
	retweets := legacy.Get("retweet_count").Int()
	replies := legacy.Get("reply_count").Int()
	bookmarks := legacy.Get("bookmark_count").Int()
	views := result.Get("views.count").Int()
	if views == 0 {
		if raw := strings.TrimSpace(result.Get("views.count").String()); raw != "" {
			views, _ = strconv.ParseInt(raw, 10, 64)
		}
	}
	if likes <= 0 && retweets <= 0 && replies <= 0 && bookmarks <= 0 && views <= 0 {
		return nil
	}
	return &TweetMetrics{
		Likes:     max(0, likes),
		Retweets:  max(0, retweets),
		Replies:   max(0, replies),
		Bookmarks: max(0, bookmarks),
		Views:     max(0, views),
	}
}

func parseSyndicationMetrics(result gjson.Result) *TweetMetrics {
	likes := result.Get("favorite_count").Int()
	retweets := firstInt(result.Get("retweet_count"), result.Get("conversation_count"))
	replies := result.Get("reply_count").Int()
	if likes <= 0 && retweets <= 0 && replies <= 0 {
		return nil
	}
	return &TweetMetrics{
		Likes:    max(0, likes),
		Retweets: max(0, retweets),
		Replies:  max(0, replies),
	}
}

func firstInt(values ...gjson.Result) int64 {
	for _, v := range values {
		if v.Exists() && v.Int() > 0 {
			return v.Int()
		}
	}
	return 0
}
