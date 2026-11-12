package jobs

import (
	"context"
	"fmt"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

// Refresh only the media the endpoint returned. A partial public response must
// not remove previously requested media or discard its last usable URL.
func mergeRefreshedMedia(original, fresh []parser.Media) []parser.Media {
	result := append([]parser.Media(nil), original...)
	for _, media := range fresh {
		if parser.MediaDownloadURL(media) == "" {
			continue
		}
		found := false
		for index, prior := range result {
			if (media.ID != "" && prior.ID == media.ID) || downloader.MediaIdentity(parser.MediaDownloadURL(prior)) == downloader.MediaIdentity(parser.MediaDownloadURL(media)) {
				if media.PreviewURL == "" {
					media.PreviewURL = prior.PreviewURL
				}
				result[index], found = media, true
				break
			}
		}
		if !found {
			result = append(result, media)
		}
	}
	return result
}

func (m *Manager) archiveFullTimeline(ctx, saveCtx context.Context, job storage.Job, cfg config.AppConfig, pool *xclient.Pool, user xclient.User, entity storage.UserEntity, dir string, update func(string)) (archiveStats, error) {
	stats := archiveStats{Users: 1}
	state, err := m.store.GetTweetSync(saveCtx, user.ID)
	if err != nil {
		return stats, err
	}
	// Resume an unfinished unbounded scan, but never reuse an incremental stop boundary for backfill.
	if job.Backfill && (state.Status == "completed" || state.LastID != "") {
		state.LastID = ""
		state.Cursor = ""
		state.HeadID = ""
		state.Count = 0
	}
	state.Status = "running"
	state.Error = ""
	if err := m.store.SaveTweetSync(saveCtx, state); err != nil {
		return stats, err
	}
	parseOptions := parserOptionsFromConfig(cfg)
	parseOptions.RequireRelated = true
	collected := map[string]bool{}
	err = pool.WalkUserTweets(ctx, user, xclient.TweetTimelineOptions{Cursor: state.Cursor, StopID: state.LastID, HeadID: state.HeadID, Parse: parseOptions}, func(page xclient.TweetTimelinePage) error {
		if update != nil {
			update(fmt.Sprintf("全文归档 @%s：%d", user.ScreenName, state.Count+len(page.Tweets)))
		}
		part, err := m.archiveTweetsWithSeen(ctx, saveCtx, job, cfg, user, entity, dir, page.Tweets, update, "")
		if err != nil {
			return err
		}
		for _, tweet := range page.Tweets {
			collected[tweet.ID] = true
		}
		stats.Tweets += part.Tweets
		stats.Downloaded += part.Downloaded
		stats.SkippedMedia += part.SkippedMedia
		stats.Failed += part.Failed
		stats.Issues = append(stats.Issues, part.Issues...)
		state.Count += len(page.Tweets)
		state.Cursor = page.Cursor
		state.HeadID = page.HeadID
		if page.Complete {
			state.LastID = page.HeadID
			state.Cursor = ""
			state.HeadID = ""
			state.Status = "completed"
		}
		return m.store.SaveTweetSync(saveCtx, state)
	})
	if err != nil {
		state.Status = "failed"
		if ctx.Err() != nil {
			state.Status = "interrupted"
		}
		state.Error = err.Error()
		if saveErr := m.store.SaveTweetSync(saveCtx, state); saveErr != nil {
			return stats, saveErr
		}
		return stats, err
	}
	if job.ArchiveMode == storage.ArchiveBoth {
		for page := 1; ; page++ {
			local, err := m.store.ListTweets(saveCtx, storage.TweetFilter{Author: user.ID, Page: page, PageSize: 100})
			if err != nil {
				return stats, err
			}
			for _, archived := range local.Items {
				if len(archived.Media) == 0 || collected[archived.ID] {
					continue
				}
				part, err := m.archiveTweetsWithSeen(ctx, saveCtx, job, cfg, user, entity, dir, []parser.TweetData{archived.TweetData}, update, "")
				if err != nil {
					return stats, err
				}
				stats.Downloaded += part.Downloaded
				stats.SkippedMedia += part.SkippedMedia
				stats.Failed += part.Failed
				stats.Issues = append(stats.Issues, part.Issues...)
			}
			if page >= local.Pagination.TotalPages {
				break
			}
		}
	}
	return stats, nil
}
