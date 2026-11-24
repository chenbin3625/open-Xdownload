package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/notify"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// SetNotifier 注入外部通知派发器（Webhook / Telegram / Discord 等）。
func (m *Manager) SetNotifier(notifier *notify.Dispatcher) {
	m.mu.Lock()
	m.notifier = notifier
	m.mu.Unlock()
}

func (m *Manager) emitNotification(ev notify.Event) {
	m.mu.Lock()
	notifier := m.notifier
	m.mu.Unlock()
	if notifier != nil {
		notifier.Emit(ev)
	}
}

// effectiveFilter 合并全局过滤规则和任务级过滤规则。
func effectiveFilter(cfg config.AppConfig, job storage.Job) *config.ArchiveFilter {
	jobFilter := job.Filter
	if jobFilter == nil && strings.TrimSpace(job.FilterJSON) != "" {
		if parsed, err := config.ParseArchiveFilterJSON(job.FilterJSON); err == nil {
			jobFilter = parsed
		}
	}
	return config.EffectiveArchiveFilter(cfg.ArchiveFilter, jobFilter)
}

// filterTweetByRules 按归档过滤规则过滤推文与媒体；返回 (保留后的推文, 是否整条保留)。
func filterTweetByRules(tweet parser.TweetData, filter *config.ArchiveFilter) (parser.TweetData, bool) {
	if filter == nil || filter.IsEmpty() {
		return tweet, true
	}
	if filter.ExcludeRetweets && tweet.RetweetedTweetID != "" {
		return tweet, false
	}
	if filter.ExcludeReplies && tweet.ReplyToID != "" {
		return tweet, false
	}
	if !filter.AllowsPublishedAt(tweet.CreatedAt) {
		return tweet, false
	}
	if len(tweet.Media) > 0 && (len(filter.MediaTypes) > 0 || filter.MinWidth > 0 || filter.MinHeight > 0) {
		kept := make([]parser.Media, 0, len(tweet.Media))
		for _, item := range tweet.Media {
			if !filter.AllowsMediaType(string(item.Type)) {
				continue
			}
			if !filter.AllowsDimensions(item.Width, item.Height) {
				continue
			}
			kept = append(kept, item)
		}
		tweet.Media = kept
	}
	return tweet, true
}

// cacheAuthorAvatar 将作者头像下载缓存到 dataDir/avatars，失败不阻断归档流程。
func (m *Manager) cacheAuthorAvatar(ctx context.Context, cfg config.AppConfig, userID, screenName, avatarURL string) {
	screenName = strings.TrimPrefix(strings.TrimSpace(screenName), "@")
	avatarURL = strings.TrimSpace(avatarURL)
	if screenName == "" || avatarURL == "" || m.store == nil {
		return
	}
	if existing, err := m.store.GetAvatarCache(ctx, screenName); err == nil && existing != nil {
		if existing.SourceURL == avatarURL && existing.FilePath != "" {
			if _, statErr := os.Stat(existing.FilePath); statErr == nil && time.Since(existing.UpdatedAt) < 7*24*time.Hour {
				return
			}
		}
	}
	avatarDir := m.store.AvatarDir()
	if err := os.MkdirAll(avatarDir, 0o700); err != nil {
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, avatarURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", xapi.UserAgent)
	client := httpx.Client(cfg.ProxyURL, 10*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || len(data) == 0 {
		return
	}
	ct := resp.Header.Get("Content-Type")
	ext := ".jpg"
	if strings.Contains(ct, "png") {
		ext = ".png"
	} else if strings.Contains(ct, "webp") {
		ext = ".webp"
	} else if ct == "" {
		ct = "image/jpeg"
	}
	safeName := strings.ToLower(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, screenName))
	destPath := filepath.Join(avatarDir, safeName+ext)
	tmp := destPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = m.store.UpsertAvatarCache(ctx, storage.AvatarCache{
		UserID:      userID,
		ScreenName:  screenName,
		SourceURL:   avatarURL,
		FilePath:    destPath,
		ContentType: ct,
	})
}

func (m *Manager) processBookmarks(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	pool, err := m.XSource(ctx, cfg)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	extPool, ok := pool.(xapi.ExtendedSource)
	if !ok {
		m.fail(saveCtx, job, "", errors.New("当前数据源不支持书签归档"))
		return
	}
	folderID := strings.TrimSpace(job.Input)
	folderID = strings.TrimPrefix(folderID, "folder:")
	syncKey := "bookmarks:all"
	if folderID != "" && !strings.EqualFold(folderID, "all") {
		syncKey = "bookmarks:" + folderID
	}
	m.runTimelineStreamJob(ctx, saveCtx, job, cfg, syncKey, "书签归档", func(opts xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
		return extPool.WalkBookmarks(ctx, folderID, opts, save)
	})
}

func (m *Manager) processLikes(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	pool, err := m.XSource(ctx, cfg)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	extPool, ok := pool.(xapi.ExtendedSource)
	if !ok {
		m.fail(saveCtx, job, "", errors.New("当前数据源不支持点赞归档"))
		return
	}
	input := strings.TrimSpace(job.Input)
	var target xapi.User
	if input != "" && !strings.EqualFold(input, "self") {
		resolved, err := pool.GetUserByInput(ctx, input)
		if err != nil {
			m.fail(saveCtx, job, "", err)
			return
		}
		target = resolved
	}
	syncKey := "likes:self"
	if target.ID != "" {
		syncKey = "likes:" + target.ID
	}
	m.runTimelineStreamJob(ctx, saveCtx, job, cfg, syncKey, "点赞归档", func(opts xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
		return extPool.WalkLikes(ctx, target, opts, save)
	})
}

func (m *Manager) processSearch(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	pool, err := m.XSource(ctx, cfg)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	extPool, ok := pool.(xapi.ExtendedSource)
	if !ok {
		m.fail(saveCtx, job, "", errors.New("当前数据源不支持关键词搜索归档"))
		return
	}
	rawQuery := strings.TrimSpace(job.Input)
	product := "Latest"
	for _, prefix := range []struct{ tag, prod string }{
		{"top:", "Top"},
		{"latest:", "Latest"},
		{"media:", "Media"},
	} {
		if strings.HasPrefix(strings.ToLower(rawQuery), prefix.tag) {
			product = prefix.prod
			rawQuery = strings.TrimSpace(rawQuery[len(prefix.tag):])
			break
		}
	}
	if rawQuery == "" {
		m.fail(saveCtx, job, "", errors.New("搜索关键词不能为空"))
		return
	}
	syncKey := "search:" + strings.ToLower(product) + ":" + rawQuery
	m.runTimelineStreamJob(ctx, saveCtx, job, cfg, syncKey, "搜索归档", func(opts xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
		return extPool.WalkSearchTimeline(ctx, rawQuery, product, opts, save)
	})
}

func (m *Manager) processThread(ctx context.Context, saveCtx context.Context, job storage.Job) {
	cfg, err := m.store.GetConfig(saveCtx)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	input := strings.TrimSpace(job.Input)
	includeReplies := false
	if strings.HasPrefix(strings.ToLower(input), "replies:") {
		includeReplies = true
		input = strings.TrimSpace(input[len("replies:"):])
	}
	_, tweetID, err := parser.ExtractTweetURL(input)
	if err != nil {
		// 也支持直接输入纯数字推文 ID
		if isDigits(input) {
			tweetID = input
		} else {
			m.fail(saveCtx, job, "", err)
			return
		}
	}
	pool, err := m.XSource(ctx, cfg)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	extPool, ok := pool.(xapi.ExtendedSource)
	if !ok {
		m.fail(saveCtx, job, "", errors.New("当前数据源不支持长串归档"))
		return
	}
	parseOpts := parserOptionsFromConfig(cfg)
	parseOpts.RequireRelated = true
	job.Status = storage.JobResolving
	job.Message = fmt.Sprintf("正在解析长串 %s", tweetID)
	job.Progress = 0.2
	m.save(saveCtx, job)

	tweets, err := extPool.FetchTweetThread(ctx, tweetID, includeReplies, parseOpts)
	if err != nil {
		if m.jobCanceled(ctx, saveCtx, job.ID) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	if job.ArchiveMode == "" {
		job.ArchiveMode = storage.ArchiveBoth
	}
	job.Status = storage.JobDownloading
	job.Message = fmt.Sprintf("正在归档长串（%d 条推文）", len(tweets))
	job.Progress = 0.5
	m.save(saveCtx, job)

	baseDir := filepath.Clean(cfg.DownloadDir)
	user := xapi.User{}
	if len(tweets) > 0 {
		user = xapi.User{ID: tweets[0].Author.ID, ScreenName: tweets[0].Author.ScreenName, Name: tweets[0].Author.Name}
	}
	stats, err := m.archiveTweetsBatch(ctx, saveCtx, job, cfg, user, storage.UserEntity{}, baseDir, tweets, func(msg string) {
		job.Message = msg
		m.save(saveCtx, job)
	})
	if err != nil {
		if m.jobCanceled(ctx, saveCtx, job.ID) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", err)
		return
	}
	m.completeArchive(saveCtx, job, stats, 0)
}

func (m *Manager) runTimelineStreamJob(
	ctx, saveCtx context.Context,
	job storage.Job,
	cfg config.AppConfig,
	syncKey, label string,
	walk func(opts xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error,
) {
	if job.ArchiveMode == "" {
		job.ArchiveMode = storage.ArchiveBoth
	}
	state, err := m.store.GetTweetSync(saveCtx, syncKey)
	if err != nil {
		m.fail(saveCtx, job, "", err)
		return
	}
	if job.Backfill {
		state.LastID = ""
		state.Cursor = ""
		state.HeadID = ""
		state.Count = 0
	}
	stopID := ""
	if cfg.IncrementalArchive && !job.Backfill {
		stopID = state.LastID
	}
	state.Status = "running"
	state.Error = ""
	_ = m.store.SaveTweetSync(saveCtx, state)

	parseOpts := parserOptionsFromConfig(cfg)
	parseOpts.RequireRelated = true
	baseDir := filepath.Clean(cfg.DownloadDir)
	stats := archiveStats{}
	pages := 0

	job.Status = storage.JobDownloading
	job.Message = fmt.Sprintf("正在执行%s", label)
	job.Progress = 0.15
	m.save(saveCtx, job)

	walkErr := walk(xapi.TweetTimelineOptions{
		Cursor: state.Cursor,
		StopID: stopID,
		HeadID: state.HeadID,
		Parse:  parseOpts,
	}, func(page xapi.TweetTimelinePage) error {
		pages++
		if m.jobCanceled(ctx, saveCtx, job.ID) {
			return context.Canceled
		}
		if len(page.Tweets) > 0 {
			part, err := m.archiveTweetsBatch(ctx, saveCtx, job, cfg, xapi.User{ScreenName: label}, storage.UserEntity{}, baseDir, page.Tweets, func(msg string) {
				job.Message = msg
				m.save(saveCtx, job)
			})
			if err != nil {
				return err
			}
			stats.Tweets += part.Tweets
			stats.Downloaded += part.Downloaded
			stats.SkippedMedia += part.SkippedMedia
			stats.Failed += part.Failed
			stats.Issues = append(stats.Issues, part.Issues...)
		}
		state.Count += len(page.Tweets)
		state.Cursor = page.Cursor
		state.HeadID = page.HeadID
		if page.Complete {
			if newerTweetID(page.HeadID, state.LastID) {
				state.LastID = page.HeadID
			}
			state.Cursor = ""
			state.HeadID = ""
			state.Status = "completed"
		}
		if err := m.store.SaveTweetSync(saveCtx, state); err != nil {
			log.Printf("save tweet sync %s: %v", syncKey, err)
		}
		job.Progress = min(0.95, 0.15+float64(pages)*0.05)
		job.Message = fmt.Sprintf("%s：已处理 %d 条推文，下载 %d 个媒体", label, stats.Tweets, stats.Downloaded)
		m.save(saveCtx, job)
		return nil
	})
	if walkErr != nil {
		state.Status = "failed"
		state.Error = walkErr.Error()
		_ = m.store.SaveTweetSync(saveCtx, state)
		if m.jobCanceled(ctx, saveCtx, job.ID) {
			m.handleInterrupt(ctx, saveCtx, job)
			return
		}
		m.fail(saveCtx, job, "", walkErr)
		return
	}
	m.completeArchive(saveCtx, job, stats, 0)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
