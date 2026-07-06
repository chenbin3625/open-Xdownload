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
		switch {
		case media.BestURL != "":
			urls = append(urls, media.BestURL)
		case media.URL != "":
			urls = append(urls, media.URL)
		}
	}
	return urls
}
