package xclient

import (
	"context"
	"errors"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

var (
	_ xapi.ExtendedSource = (*Pool)(nil)
	_ xapi.ExtendedSource = (*guestSource)(nil)
)

func (p *Pool) WalkBookmarks(ctx context.Context, folderID string, options xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
	primary := p.Primary()
	if primary == nil {
		return errors.New("没有可用 X 主账号")
	}
	return xapi.WalkBookmarks(ctx, primary, folderID, options, save)
}

func (p *Pool) WalkLikes(ctx context.Context, user xapi.User, options xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
	primary := p.Primary()
	if primary == nil {
		return errors.New("没有可用 X 主账号")
	}
	return xapi.WalkLikes(ctx, primary, user, options, save)
}

func (p *Pool) FetchTweetThread(ctx context.Context, tweetID string, includeReplies bool, options parser.ParseOptions) ([]parser.TweetData, error) {
	return xapi.FetchTweetThread(ctx, p, tweetID, includeReplies, options)
}

func (p *Pool) WalkSearchTimeline(ctx context.Context, rawQuery, product string, options xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
	return xapi.WalkSearchTimeline(ctx, p, rawQuery, product, options, save)
}

func (s *guestSource) WalkBookmarks(context.Context, string, xapi.TweetTimelineOptions, func(xapi.TweetTimelinePage) error) error {
	return s.err
}

func (s *guestSource) WalkLikes(context.Context, xapi.User, xapi.TweetTimelineOptions, func(xapi.TweetTimelinePage) error) error {
	return s.err
}

func (s *guestSource) FetchTweetThread(ctx context.Context, tweetID string, includeReplies bool, options parser.ParseOptions) ([]parser.TweetData, error) {
	client, err := newAccountClient(Account{}, s.cfg.ProxyURL, s.opts)
	if err != nil {
		return nil, err
	}
	return xapi.FetchTweetThread(ctx, client, tweetID, includeReplies, options)
}

func (s *guestSource) WalkSearchTimeline(context.Context, string, string, xapi.TweetTimelineOptions, func(xapi.TweetTimelinePage) error) error {
	return s.err
}
