package xclient

import (
	"context"
	"fmt"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// WalkUserMedia 逐页抓取用户媒体时间线，并在每页完成后调用 save。与
// GetUserMediaWithOptionsStats 不同，调用方无需把整条时间线攒在内存里；save 负责
// 下载本页媒体、提交失败队列并持久化游标。
func (p *Pool) WalkUserMedia(ctx context.Context, user User, options UserMediaTimelineOptions, save func(UserMediaTimelinePage) error) (MediaFetchStats, error) {
	if userMediaNeedsPrimaryClient(user) {
		client := p.Primary()
		if client == nil {
			return MediaFetchStats{}, fmt.Errorf("没有可用 X 客户端")
		}
		return xapi.WalkUserMediaPages(ctx, client, user, options, save)
	}
	return xapi.WalkUserMediaPages(ctx, p, user, options, save)
}
