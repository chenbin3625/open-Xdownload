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
	// ID 是媒体的稳定身份：优先 Snowflake ID（id_str/id）；没有时由归一化 URL 的
	// hash 生成（"url-<hex>"），不会随 photos 序号或 URL 写法变化。下游可当去重键。
	ID         string         `json:"id"`
	Type       MediaType      `json:"type"`
	URL        string         `json:"url"`
	PreviewURL string         `json:"previewUrl"`
	BestURL    string         `json:"bestUrl"`
	Variants   []MediaVariant `json:"variants"`
	AltText    string         `json:"altText,omitempty"`
	Width      int            `json:"width,omitempty"`
	Height     int            `json:"height,omitempty"`
}

type Author struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ScreenName string `json:"screenName"`
	AvatarURL  string `json:"avatarUrl,omitempty"`
}

// TweetArticle 保存 X Articles（长文章）的标题、纯文本、封面和插图。
type TweetArticle struct {
	ID            string  `json:"id,omitempty"`
	Title         string  `json:"title,omitempty"`
	PreviewText   string  `json:"previewText,omitempty"`
	PlainText     string  `json:"plainText,omitempty"`
	CoverImageURL string  `json:"coverImageUrl,omitempty"`
	Media         []Media `json:"media,omitempty"`
}

// PollChoice 表示推文投票中的单个选项及票数快照。
type PollChoice struct {
	Label string `json:"label"`
	Votes int64  `json:"votes"`
}

// TweetPoll 保存推文投票选项、截止时间与状态快照。
type TweetPoll struct {
	ID           string       `json:"id,omitempty"`
	EndDatetime  string       `json:"endDatetime,omitempty"`
	VotingStatus string       `json:"votingStatus,omitempty"`
	TotalVotes   int64        `json:"totalVotes,omitempty"`
	Choices      []PollChoice `json:"choices"`
}

// TweetMetrics 保存归档时采集到的推文互动指标快照。
type TweetMetrics struct {
	Likes     int64 `json:"likes,omitempty"`
	Retweets  int64 `json:"retweets,omitempty"`
	Replies   int64 `json:"replies,omitempty"`
	Bookmarks int64 `json:"bookmarks,omitempty"`
	Views     int64 `json:"views,omitempty"`
}

type TweetData struct {
	ID               string        `json:"id"`
	URL              string        `json:"url"`
	Text             string        `json:"text"`
	CreatedAt        time.Time     `json:"createdAt"`
	Author           Author        `json:"author"`
	Media            []Media       `json:"media"`
	ConversationID   string        `json:"conversationId,omitempty"`
	ReplyToID        string        `json:"replyToId,omitempty"`
	QuotedTweetID    string        `json:"quotedTweetId,omitempty"`
	RetweetedTweetID string        `json:"retweetedTweetId,omitempty"`
	Related          []TweetData   `json:"related,omitempty"`
	Article          *TweetArticle `json:"article,omitempty"`
	Poll             *TweetPoll    `json:"poll,omitempty"`
	Metrics          *TweetMetrics `json:"metrics,omitempty"`
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
