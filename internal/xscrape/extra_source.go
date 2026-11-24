package xscrape

import (
	"context"
	"errors"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

var _ xapi.ExtendedSource = (*source)(nil)

func (s *source) WalkBookmarks(ctx context.Context, folderID string, options xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
	account, err := s.primaryAccount(ctx)
	if err != nil {
		return err
	}
	return xapi.WalkBookmarks(ctx, accountRequester{source: s, account: account}, folderID, options, save)
}

func (s *source) WalkLikes(ctx context.Context, user xapi.User, options xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
	account, err := s.primaryAccount(ctx)
	if err != nil {
		return err
	}
	if user.ID == "" && account.ScreenName != "" {
		resolved, err := s.GetUserByInput(ctx, account.ScreenName)
		if err != nil {
			return err
		}
		user = resolved
	}
	if user.ID == "" {
		return errors.New("无法确定点赞归档的目标用户 ID，请先检测主账号或指定用户名")
	}
	return xapi.WalkLikes(ctx, accountRequester{source: s, account: account}, user, options, save)
}

func (s *source) FetchTweetThread(ctx context.Context, tweetID string, includeReplies bool, options parser.ParseOptions) ([]parser.TweetData, error) {
	active, err := s.engine.activeAccountCount(ctx)
	if err != nil {
		return nil, err
	}
	if active == 0 {
		return xapi.FetchTweetThread(ctx, guestRequester{source: s}, tweetID, includeReplies, options)
	}
	return xapi.FetchTweetThread(ctx, s, tweetID, includeReplies, options)
}

func (s *source) WalkSearchTimeline(ctx context.Context, rawQuery, product string, options xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
	return xapi.WalkSearchTimeline(ctx, s, rawQuery, product, options, save)
}
