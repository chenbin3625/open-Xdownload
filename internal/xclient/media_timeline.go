package xclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

// UserMediaTimelineOptions 是 UserMedia 时间线的翻页参数。Cursor 用于从上次
// 已提交的页继续；StopID 是增量归档早停边界（为空表示全量扫描）。
type UserMediaTimelineOptions struct {
	Cursor, StopID string
	Parse          parser.ParseOptions
}

// UserMediaTimelinePage 是一页已解析的媒体时间线。回调返回后调用方应处理
// Tweets、提交失败队列，并保存 Cursor（Complete 为 true 时整轮结束）。
type UserMediaTimelinePage struct {
	Tweets   []parser.TweetData
	Cursor   string
	Complete bool
}

// ErrInvalidTimelineCursor 表示恢复用的分页游标已失效（用户删推、令牌轮换等）。
// 上层应清空保存的游标并从第 0 页重翻；媒体去重会跳过已下载文件。
var ErrInvalidTimelineCursor = errors.New("时间线分页游标已失效")

// IsInvalidTimelineCursor 报告 err 是否由失效游标导致。
func IsInvalidTimelineCursor(err error) bool {
	return errors.Is(err, ErrInvalidTimelineCursor)
}

func isInvalidCursorError(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusBadRequest {
		return mentionsInvalidCursor(httpErr.Payload)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return mentionsInvalidCursor(apiErr.Raw)
	}
	return false
}

func mentionsInvalidCursor(payload string) bool {
	lower := strings.ToLower(payload)
	return strings.Contains(lower, "cursor") || strings.Contains(lower, "invalid")
}

// WalkUserMedia 逐页抓取用户媒体时间线，并在每页完成后调用 save。与
// GetUserMediaWithOptionsStats 不同，调用方无需把整条时间线攒在内存里；save 负责
// 下载本页媒体、提交失败队列并持久化游标。
func (p *Pool) WalkUserMedia(ctx context.Context, user User, options UserMediaTimelineOptions, save func(UserMediaTimelinePage) error) (MediaFetchStats, error) {
	if userMediaNeedsPrimaryClient(user) {
		client := p.Primary()
		if client == nil {
			return MediaFetchStats{}, fmt.Errorf("没有可用 X 客户端")
		}
		return walkUserMediaPages(ctx, client, user, options, save)
	}
	return walkUserMediaPages(ctx, p, user, options, save)
}
