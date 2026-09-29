package parser

import "time"

type MediaType string

const (
	MediaPhoto MediaType = "photo"
	MediaVideo MediaType = "video"
	MediaGIF   MediaType = "animated_gif"
	MediaFile  MediaType = "file"
)

type MediaVariant struct {
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Bitrate     int64  `json:"bitrate"`
	Quality     string `json:"quality"`
}

type Media struct {
	ID         string         `json:"id"`
	Type       MediaType      `json:"type"`
	URL        string         `json:"url"`
	PreviewURL string         `json:"previewUrl"`
	BestURL    string         `json:"bestUrl"`
	Variants   []MediaVariant `json:"variants"`
}

type Author struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ScreenName string `json:"screenName"`
}

type TweetData struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Author    Author    `json:"author"`
	Media     []Media   `json:"media"`
}

func (tweet TweetData) BestMediaURLs() []string {
	urls := make([]string, 0, len(tweet.Media))
	for _, media := range tweet.Media {
		if raw := MediaDownloadURL(media); raw != "" {
			urls = append(urls, raw)
		}
	}
	return urls
}

// MediaDownloadURL 返回应当下载的媒体地址；没有可下载的版本时返回空串。
//
// 视频/GIF 的 URL 字段是缩略图（media_url_https），只能用作预览：拿它兜底会把一张
// jpg 当成视频存档并标记完成。旧版本存下的 payload 可能没有 BestURL，此时从
// Variants 里重新挑选 mp4。
func MediaDownloadURL(media Media) string {
	if media.BestURL != "" {
		return media.BestURL
	}
	if media.Type == MediaVideo || media.Type == MediaGIF {
		return BestVariant(media.Variants).URL
	}
	return media.URL
}
