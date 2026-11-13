package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

func (m *Manager) archiveFullTimeline(ctx, saveCtx context.Context, job storage.Job, cfg config.AppConfig, pool *xclient.Pool, user xclient.User, entity storage.UserEntity, dir string, update func(string), shouldYield func() bool) (archiveStats, error) {
	stats := archiveStats{Users: 1}
	cp, _, err := m.loadArchiveCheckpoint(saveCtx, job.ID)
	if err != nil {
		return stats, err
	}
	if job.ArchiveMode == storage.ArchiveBoth && cp.Replay != nil && cp.Replay.UserID == user.ID {
		return m.replayArchivedTweetMedia(ctx, saveCtx, job, cfg, user, entity, dir, update, shouldYield, cp, stats, nil)
	}
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
	savePage := func(page xclient.TweetTimelinePage) error {
		if update != nil {
			update(fmt.Sprintf("全文归档 @%s：%d", user.ScreenName, state.Count+len(page.Tweets)))
		}
		tweets := make([]parser.TweetData, 0, len(page.Tweets))
		for _, tweet := range page.Tweets {
			if !collected[tweet.ID] {
				tweets = append(tweets, tweet)
			}
		}
		part, err := m.archiveTweetsWithSeen(ctx, saveCtx, job, cfg, user, entity, dir, tweets, update, "")
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
		if err := m.store.SaveTweetSync(saveCtx, state); err != nil {
			return err
		}
		if !page.Complete && shouldYield != nil && shouldYield() {
			return errYieldExecution
		}
		return nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		err = pool.WalkUserTweets(ctx, user, xclient.TweetTimelineOptions{Cursor: state.Cursor, StopID: state.LastID, HeadID: state.HeadID, Parse: parseOptions}, savePage)
		if attempt != 0 || state.Cursor == "" {
			break
		}
		// X 对失效的深翻页游标有时回 401 而非 400：Cookie 通过 /home 校验就当作游标失效。
		staleCursor := xclient.IsInvalidTimelineCursor(err) ||
			(xclient.IsUnauthorized(err) && pool.RecheckDisabledClients(ctx))
		if !staleCursor {
			break
		}
		state.Cursor, state.HeadID, state.Count = "", "", 0
		if err := m.store.SaveTweetSync(saveCtx, state); err != nil {
			return stats, err
		}
	}
	if err != nil {
		state.Status = "failed"
		if ctx.Err() != nil || errors.Is(err, errYieldExecution) {
			state.Status = "interrupted"
		}
		state.Error = err.Error()
		if saveErr := m.store.SaveTweetSync(saveCtx, state); saveErr != nil {
			return stats, saveErr
		}
		return stats, err
	}
	if job.ArchiveMode == storage.ArchiveBoth {
		cp.Replay = &tweetMediaReplay{UserID: user.ID, ArchivedBefore: time.Now().UTC()}
		return m.replayArchivedTweetMedia(ctx, saveCtx, job, cfg, user, entity, dir, update, shouldYield, cp, stats, collected)
	}
	return stats, nil
}

type tweetMediaReplay struct {
	UserID         string    `json:"userId"`
	AfterID        string    `json:"afterId"`
	ArchivedBefore time.Time `json:"archivedBefore"`
	Complete       bool      `json:"complete"`
}

func (m *Manager) replayArchivedTweetMedia(ctx, saveCtx context.Context, job storage.Job, cfg config.AppConfig, user xclient.User, entity storage.UserEntity, dir string, update func(string), shouldYield func() bool, cp archiveCheckpoint, stats archiveStats, collected map[string]bool) (archiveStats, error) {
	state := cp.Replay
	for !state.Complete {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		local, err := m.store.ListTweetReplay(saveCtx, user.ID, state.AfterID, state.ArchivedBefore, 100)
		if err != nil {
			return stats, err
		}
		for _, archived := range local {
			if len(archived.Media) == 0 || collected[archived.ID] {
				continue
			}
			part, err := m.archiveTweetsWithSeen(ctx, saveCtx, job, cfg, user, entity, dir, []parser.TweetData{archived}, update, "")
			if err != nil {
				return stats, err
			}
			stats.Downloaded += part.Downloaded
			stats.SkippedMedia += part.SkippedMedia
			stats.Failed += part.Failed
			stats.Issues = append(stats.Issues, part.Issues...)
		}
		if len(local) > 0 {
			state.AfterID = local[len(local)-1].ID
		}
		state.Complete = len(local) < 100
		payload, err := json.Marshal(cp)
		if err != nil {
			return stats, err
		}
		if err := m.store.SaveJobCheckpoint(saveCtx, job.ID, payload); err != nil {
			return stats, err
		}
		if !state.Complete && shouldYield != nil && shouldYield() {
			return stats, errYieldExecution
		}
	}
	return stats, nil
}
