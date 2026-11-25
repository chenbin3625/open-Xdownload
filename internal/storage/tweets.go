package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/jmoiron/sqlx"
)

type ArchiveMode string

const (
	ArchiveMedia  ArchiveMode = "media"
	ArchiveTweets ArchiveMode = "tweets"
	ArchiveBoth   ArchiveMode = "both"
)

func (m ArchiveMode) Valid() bool {
	return m == "" || m == ArchiveMedia || m == ArchiveTweets || m == ArchiveBoth
}
func (m ArchiveMode) Normalized() ArchiveMode {
	if m == "" {
		return ArchiveMedia
	}
	return m
}
func (m ArchiveMode) IncludesTweets() bool { return m == ArchiveTweets || m == ArchiveBoth }

type ArchivedTweet struct {
	parser.TweetData
	JobIDs      []int64          `json:"jobIds"`
	BodyStatus  string           `json:"bodyStatus"`
	MediaStatus string           `json:"mediaStatus"`
	MediaError  string           `json:"mediaError,omitempty"`
	Downloads   []DownloadRecord `json:"downloads"`
	ArchivedAt  time.Time        `json:"archivedAt"`
	Starred     bool             `json:"starred"`
	Tags        []Tag            `json:"tags,omitempty"`
}

type TweetFilter struct {
	Sort, Retweets                                          string
	Search, Author, From, To, MediaType, ID, ConversationID string
	JobID                                                   int64
	Starred                                                 *bool
	TagID                                                   int64
	CollectionID                                            int64
	Page, PageSize                                          int
}
type TweetPage struct {
	Items      []ArchivedTweet `json:"items"`
	Pagination Pagination      `json:"pagination"`
}
type TweetAuthor struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

const (
	legacyTweetPublishedAtPredicate = `published_at <> '' AND (length(published_at) <> 30 OR substr(published_at, 11, 1) <> 'T' OR substr(published_at, 30, 1) <> 'Z')`
	legacyTweetUpdatedAtPredicate   = `updated_at <> '' AND (length(updated_at) <> 30 OR substr(updated_at, 11, 1) <> 'T' OR substr(updated_at, 30, 1) <> 'Z')`
)

func (s *Store) invalidateTweetAuthorsCache() {
	s.tweetAuthorsGen.Add(1)
	s.tweetAuthorsMu.Lock()
	s.tweetAuthorsCache = nil
	s.tweetAuthorsIndex = nil
	s.tweetAuthorsOK = false
	s.tweetAuthorsGenBuilt = 0
	s.tweetAuthorsMaxRowID = 0
	s.tweetAuthorsMu.Unlock()
}

func (s *Store) hasLegacyTweetPublishedAt(ctx context.Context) bool {
	var exists bool
	err := s.db.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM tweets WHERE `+legacyTweetPublishedAtPredicate+`)`)
	return err != nil || exists
}

func (s *Store) hasLegacyTweetTimestamps(ctx context.Context) bool {
	if s.hasLegacyTweetPublishedAt(ctx) {
		return true
	}
	var exists bool
	err := s.db.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM tweets WHERE `+legacyTweetUpdatedAtPredicate+`)`)
	return err != nil || exists
}

type archivedTweetAuthor struct {
	authorID   string
	screenName string
	authorName string
}

type archivedTweetSummary struct {
	ids     []string
	authors []archivedTweetAuthor
}

func (s *Store) noteArchivedTweetAuthors(ctx context.Context, authors []archivedTweetAuthor) {
	if len(authors) == 0 {
		return
	}
	s.tweetAuthorsMu.Lock()
	if !s.tweetAuthorsOK {
		s.tweetAuthorsMu.Unlock()
		return
	}
	for _, a := range authors {
		if a.screenName == "" {
			continue
		}
		key := a.authorID
		if key == "" {
			key = "sn:" + strings.ToLower(a.screenName)
		}
		existing, ok := s.tweetAuthorsIndex[key]
		if !ok || existing.Value != a.screenName || existing.Label != a.authorName {
			s.tweetAuthorsGen.Add(1)
			s.tweetAuthorsCache = nil
			s.tweetAuthorsIndex = nil
			s.tweetAuthorsOK = false
			s.tweetAuthorsGenBuilt = 0
			s.tweetAuthorsMaxRowID = 0
			s.tweetAuthorsMu.Unlock()
			return
		}
	}
	gen := s.tweetAuthorsGen.Load()
	s.tweetAuthorsMu.Unlock()

	var maxRowID int64
	if err := s.db.GetContext(ctx, &maxRowID, `SELECT COALESCE(MAX(rowid), 0) FROM tweets`); err != nil {
		return
	}
	s.tweetAuthorsMu.Lock()
	if s.tweetAuthorsOK && s.tweetAuthorsGen.Load() == gen && maxRowID > s.tweetAuthorsMaxRowID {
		s.tweetAuthorsMaxRowID = maxRowID
	}
	s.tweetAuthorsMu.Unlock()
}

func (s *Store) anyTweetHasDownload(ctx context.Context, tweetIDs []string) bool {
	if len(tweetIDs) == 0 {
		return false
	}
	query, args, err := sqlx.In(`SELECT EXISTS(SELECT 1 FROM downloads WHERE tweet_id IN (?) AND tweet_id <> '')`, tweetIDs)
	if err != nil {
		return true
	}
	var exists bool
	if err := s.db.GetContext(ctx, &exists, s.db.Rebind(query), args...); err != nil {
		return true
	}
	return exists
}

func (s *Store) ListTweetAuthors(ctx context.Context) ([]TweetAuthor, error) {
	var maxRowID int64
	if err := s.db.GetContext(ctx, &maxRowID, `SELECT COALESCE(MAX(rowid), 0) FROM tweets`); err != nil {
		return nil, err
	}
	gen := s.tweetAuthorsGen.Load()
	s.tweetAuthorsMu.RLock()
	if s.tweetAuthorsOK && s.tweetAuthorsGenBuilt == gen && s.tweetAuthorsMaxRowID == maxRowID {
		cached := append([]TweetAuthor(nil), s.tweetAuthorsCache...)
		s.tweetAuthorsMu.RUnlock()
		return cached, nil
	}
	s.tweetAuthorsMu.RUnlock()

	type authorRow struct {
		AuthorKey string `db:"author_key"`
		Value     string `db:"value"`
		Label     string `db:"label"`
	}
	rows := []authorRow{}
	query := `
SELECT author_key, value, label FROM (
	SELECT author_id AS author_key, screen_name AS value, author_name AS label
	FROM (
		SELECT author_id, screen_name, author_name,
			ROW_NUMBER() OVER (
				PARTITION BY author_id
				ORDER BY published_at DESC, updated_at DESC, id DESC
			) AS rn
		FROM tweets
		WHERE screen_name <> '' AND author_id <> ''
	)
	WHERE rn = 1
	UNION ALL
	SELECT 'sn:' || lower(screen_name) AS author_key, screen_name AS value, author_name AS label
	FROM (
		SELECT screen_name, author_name,
			ROW_NUMBER() OVER (
				PARTITION BY lower(screen_name)
				ORDER BY published_at DESC, updated_at DESC, id DESC
			) AS rn
		FROM tweets
		WHERE screen_name <> '' AND author_id = ''
	)
	WHERE rn = 1
)
ORDER BY lower(value), value`
	if s.hasLegacyTweetTimestamps(ctx) {
		query = `
SELECT author_key, screen_name AS value, author_name AS label
FROM (
	SELECT CASE WHEN author_id <> '' THEN author_id ELSE 'sn:' || lower(screen_name) END AS author_key,
		screen_name, author_name,
		ROW_NUMBER() OVER (
			PARTITION BY CASE WHEN author_id <> '' THEN author_id ELSE lower(screen_name) END
			ORDER BY COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', published_at), published_at) DESC,
				published_at DESC,
				COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', updated_at), updated_at) DESC,
				updated_at DESC,
				id DESC
		) AS rn
	FROM tweets
	WHERE screen_name <> ''
)
WHERE rn = 1
ORDER BY lower(screen_name), screen_name`
	}
	if err := s.db.SelectContext(ctx, &rows, query); err != nil {
		return nil, err
	}
	authors := make([]TweetAuthor, len(rows))
	index := make(map[string]TweetAuthor, len(rows))
	for i, row := range rows {
		item := TweetAuthor{Value: row.Value, Label: row.Label}
		authors[i] = item
		index[row.AuthorKey] = item
	}

	var afterMaxRowID int64
	if err := s.db.GetContext(ctx, &afterMaxRowID, `SELECT COALESCE(MAX(rowid), 0) FROM tweets`); err == nil && afterMaxRowID == maxRowID && s.tweetAuthorsGen.Load() == gen {
		s.tweetAuthorsMu.Lock()
		if s.tweetAuthorsGen.Load() == gen {
			s.tweetAuthorsCache = append([]TweetAuthor(nil), authors...)
			s.tweetAuthorsIndex = index
			s.tweetAuthorsOK = true
			s.tweetAuthorsGenBuilt = gen
			s.tweetAuthorsMaxRowID = maxRowID
		}
		s.tweetAuthorsMu.Unlock()
	}
	return authors, nil
}

type TweetSync struct {
	UserID    string    `json:"userId" db:"user_id"`
	LastID    string    `json:"lastId" db:"last_id"`
	Cursor    string    `json:"cursor" db:"cursor"`
	HeadID    string    `json:"headId" db:"head_id"`
	Status    string    `json:"status" db:"status"`
	Error     string    `json:"error" db:"error"`
	Count     int       `json:"count" db:"count"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

func (s *Store) migrateArchive() error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, col := range []struct{ name, definition string }{{"archive_mode", "TEXT NOT NULL DEFAULT 'media'"}, {"backfill", "BOOLEAN NOT NULL DEFAULT 0"}, {"batch_size", "INTEGER NOT NULL DEFAULT 0"}} {
		var count int
		if err := tx.Get(&count, `SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name=?`, col.name); err != nil {
			return err
		}
		if count == 0 {
			if _, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN ` + col.name + ` ` + col.definition); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS tweets (
 id TEXT PRIMARY KEY, author_id TEXT NOT NULL, screen_name TEXT NOT NULL,
 author_name TEXT NOT NULL, body TEXT NOT NULL, published_at DATETIME NOT NULL,
 payload TEXT NOT NULL, archived_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tweets_date ON tweets(published_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_tweets_author ON tweets(author_id, published_at DESC);
-- 全文归档重放按 author_id 过滤、按 id 排序翻页；idx_tweets_author 的
-- 第二列是 published_at，无法避免临时排序。索引必须建在 tweets 表之后
-- （addMissingIndexes 早于本迁移执行）。
CREATE INDEX IF NOT EXISTS idx_tweets_author_id ON tweets(author_id, id);
CREATE TABLE IF NOT EXISTS tweet_relations (
 tweet_id TEXT NOT NULL REFERENCES tweets(id), relation TEXT NOT NULL, related_id TEXT NOT NULL,
 PRIMARY KEY(tweet_id,relation,related_id)
);
CREATE TABLE IF NOT EXISTS tweet_jobs (
 tweet_id TEXT NOT NULL REFERENCES tweets(id), job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 body_status TEXT NOT NULL DEFAULT 'archived', media_status TEXT NOT NULL DEFAULT 'pending',
 media_error TEXT NOT NULL DEFAULT '', PRIMARY KEY(tweet_id,job_id)
);
CREATE INDEX IF NOT EXISTS idx_tweet_jobs_job ON tweet_jobs(job_id);
CREATE TABLE IF NOT EXISTS archive_media (
 id TEXT PRIMARY KEY, media_type TEXT NOT NULL, source_url TEXT NOT NULL, payload TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tweet_media (
 tweet_id TEXT NOT NULL REFERENCES tweets(id), media_id TEXT NOT NULL REFERENCES archive_media(id),
 position INTEGER NOT NULL, PRIMARY KEY(tweet_id,media_id)
);
CREATE TABLE IF NOT EXISTS tweet_sync (
 user_id TEXT PRIMARY KEY, last_id TEXT NOT NULL DEFAULT '', cursor TEXT NOT NULL DEFAULT '',
 head_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'idle', error TEXT NOT NULL DEFAULT '',
 count INTEGER NOT NULL DEFAULT 0, updated_at DATETIME NOT NULL
);
`)
	if err != nil {
		return err
	}
	for _, col := range []struct{ name, definition string }{
		{"like_count", "INTEGER NOT NULL DEFAULT 0"},
		{"retweet_count", "INTEGER NOT NULL DEFAULT 0"},
		{"reply_count", "INTEGER NOT NULL DEFAULT 0"},
		{"bookmark_count", "INTEGER NOT NULL DEFAULT 0"},
		{"view_count", "INTEGER NOT NULL DEFAULT 0"},
		{"starred", "BOOLEAN NOT NULL DEFAULT 0"},
		{"conversation_id", "TEXT NOT NULL DEFAULT ''"},
		{"alt_texts", "TEXT NOT NULL DEFAULT ''"},
	} {
		var count int
		if err := tx.Get(&count, `SELECT COUNT(*) FROM pragma_table_info('tweets') WHERE name=?`, col.name); err != nil {
			return err
		}
		if count == 0 {
			if _, err := tx.Exec(`ALTER TABLE tweets ADD COLUMN ` + col.name + ` ` + col.definition); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`
CREATE INDEX IF NOT EXISTS idx_tweets_conversation ON tweets(conversation_id, published_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_tweets_starred ON tweets(starred, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_tweets_likes ON tweets(like_count DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_tweets_legacy_published_at ON tweets(id)
	WHERE published_at <> '' AND (length(published_at) <> 30 OR substr(published_at, 11, 1) <> 'T' OR substr(published_at, 30, 1) <> 'Z');
CREATE INDEX IF NOT EXISTS idx_tweets_legacy_updated_at ON tweets(id)
	WHERE updated_at <> '' AND (length(updated_at) <> 30 OR substr(updated_at, 11, 1) <> 'T' OR substr(updated_at, 30, 1) <> 'Z');
CREATE INDEX IF NOT EXISTS idx_tweets_screen_name_lower ON tweets(lower(screen_name), published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_tweets_id_author ON tweets(id, screen_name, author_name);
CREATE INDEX IF NOT EXISTS idx_tweets_likes_date ON tweets(like_count DESC, published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_tweets_retweets_date ON tweets(retweet_count DESC, published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_tweets_views_date ON tweets(view_count DESC, published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_tweets_authors_cover ON tweets(author_id, published_at DESC, updated_at DESC, id DESC, screen_name, author_name) WHERE screen_name <> '';
CREATE INDEX IF NOT EXISTS idx_tweet_relations_rel_tweet ON tweet_relations(relation, tweet_id);
CREATE INDEX IF NOT EXISTS idx_archive_media_type_id ON archive_media(media_type, id);
CREATE INDEX IF NOT EXISTS idx_tweet_media_media_tweet ON tweet_media(media_id, tweet_id);
CREATE INDEX IF NOT EXISTS idx_tweet_jobs_job_tweet ON tweet_jobs(job_id, tweet_id);
CREATE VIRTUAL TABLE IF NOT EXISTS tweets_fts USING fts5(tweet_id UNINDEXED, screen_name, author_name, body, alt_texts, tokenize='unicode61');
`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) backfillTweetsFTS(exec migrationExecutor) error {
	if _, err := exec.Exec(`DELETE FROM tweets_fts`); err != nil {
		return err
	}
	_, err := exec.Exec(`
INSERT INTO tweets_fts(tweet_id, screen_name, author_name, body, alt_texts)
SELECT id, screen_name, author_name, body, alt_texts FROM tweets`)
	return err
}

func collectTweetAltTexts(tweet parser.TweetData) string {
	parts := make([]string, 0, len(tweet.Media))
	for _, m := range tweet.Media {
		if alt := strings.TrimSpace(m.AltText); alt != "" {
			parts = append(parts, alt)
		}
	}
	if tweet.Article != nil {
		if t := strings.TrimSpace(tweet.Article.Title); t != "" {
			parts = append(parts, t)
		}
		if p := strings.TrimSpace(tweet.Article.PlainText); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n")
}

// Each body commits before any media I/O. A later media failure cannot roll it back.
func (s *Store) ArchiveTweet(ctx context.Context, jobID int64, tweet parser.TweetData, mediaStatus string) error {
	return s.archiveTweet(ctx, jobID, tweet, "archived", mediaStatus)
}

// ArchiveTweetMediaOnly records the tweet metadata and media result without
// claiming that the body was requested. This keeps media-only jobs visible in
// the archive and gives their media a durable retry/status row.
func (s *Store) ArchiveTweetMediaOnly(ctx context.Context, jobID int64, tweet parser.TweetData, mediaStatus string) error {
	return s.archiveTweet(ctx, jobID, tweet, "not_requested", mediaStatus)
}

func (s *Store) archiveTweet(ctx context.Context, jobID int64, tweet parser.TweetData, bodyStatus, mediaStatus string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	summary := &archivedTweetSummary{}
	if err := archiveTweetTx(ctx, tx, jobID, tweet, bodyStatus, mediaStatus, mediaStatus, tweet.Media, map[string]struct{}{}, false, summary); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.noteArchivedTweetAuthors(ctx, summary.authors)
	// 只有本次归档的推文已关联 downloads 记录（可能更新作者展示名）时才标记媒体库缓存过期，
	// 避免纯文本归档或尚未落盘媒体的推文在后台持续写入时反复打穿媒体库快照。
	if len(summary.ids) > 0 && s.anyTweetHasDownload(ctx, summary.ids) {
		s.markLibraryDownloadsCacheStale()
	}
	return nil
}

func archiveTweetTx(ctx context.Context, tx *sqlx.Tx, jobID int64, tweet parser.TweetData, bodyStatus, mediaStatus, rootMediaStatus string, requestedMedia []parser.Media, ancestors map[string]struct{}, isRelated bool, summary *archivedTweetSummary) error {
	if tweet.ID == "" {
		return ErrValidation("推文缺少 ID")
	}
	if _, inCycle := ancestors[tweet.ID]; inCycle {
		return nil
	}
	ancestors[tweet.ID] = struct{}{}
	defer delete(ancestors, tweet.ID)

	if tweet.Media == nil {
		tweet.Media = []parser.Media{}
	}
	for _, related := range tweet.Related {
		relatedStatus := "not_requested"
		if rootMediaStatus != "not_requested" {
			for _, item := range related.Media {
				itemIdentity := downloader.MediaIdentity(parser.MediaDownloadURL(item))
				for _, requested := range requestedMedia {
					if (item.ID != "" && item.ID == requested.ID) || (itemIdentity != "" && itemIdentity == downloader.MediaIdentity(parser.MediaDownloadURL(requested))) {
						relatedStatus = rootMediaStatus
						break
					}
				}
				if relatedStatus != "not_requested" {
					break
				}
			}
		}
		if err := archiveTweetTx(ctx, tx, jobID, related, bodyStatus, relatedStatus, rootMediaStatus, requestedMedia, ancestors, true, summary); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(tweet)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	publishedAt := formatSortableTime(tweet.CreatedAt)
	updatedAt := formatSortableTime(now)

	preserveExisting := false
	if isRelated {
		var existingCount int
		if err := tx.GetContext(ctx, &existingCount, `SELECT COUNT(*) FROM tweets WHERE id=?`, tweet.ID); err != nil {
			return err
		}
		preserveExisting = existingCount > 0
	}

	if !preserveExisting {
		var likeCount, retweetCount, replyCount, bookmarkCount, viewCount int64
		if tweet.Metrics != nil {
			likeCount = tweet.Metrics.Likes
			retweetCount = tweet.Metrics.Retweets
			replyCount = tweet.Metrics.Replies
			bookmarkCount = tweet.Metrics.Bookmarks
			viewCount = tweet.Metrics.Views
		}
		altTexts := collectTweetAltTexts(tweet)
		convID := strings.TrimSpace(tweet.ConversationID)
		_, err = tx.ExecContext(ctx, `INSERT INTO tweets(id,author_id,screen_name,author_name,body,published_at,payload,archived_at,updated_at,like_count,retweet_count,reply_count,bookmark_count,view_count,conversation_id,alt_texts)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET author_id=excluded.author_id,screen_name=excluded.screen_name,
author_name=excluded.author_name,body=excluded.body,published_at=excluded.published_at,payload=excluded.payload,updated_at=excluded.updated_at,
like_count=CASE WHEN excluded.like_count>0 THEN excluded.like_count ELSE tweets.like_count END,
retweet_count=CASE WHEN excluded.retweet_count>0 THEN excluded.retweet_count ELSE tweets.retweet_count END,
reply_count=CASE WHEN excluded.reply_count>0 THEN excluded.reply_count ELSE tweets.reply_count END,
bookmark_count=CASE WHEN excluded.bookmark_count>0 THEN excluded.bookmark_count ELSE tweets.bookmark_count END,
view_count=CASE WHEN excluded.view_count>0 THEN excluded.view_count ELSE tweets.view_count END,
conversation_id=CASE WHEN excluded.conversation_id<>'' THEN excluded.conversation_id ELSE tweets.conversation_id END,
alt_texts=CASE WHEN excluded.alt_texts<>'' THEN excluded.alt_texts ELSE tweets.alt_texts END`,
			tweet.ID, tweet.Author.ID, tweet.Author.ScreenName, tweet.Author.Name, tweet.Text, publishedAt, string(payload), now, updatedAt,
			likeCount, retweetCount, replyCount, bookmarkCount, viewCount, convID, altTexts)
		if err != nil {
			return err
		}
		if summary != nil {
			summary.ids = append(summary.ids, tweet.ID)
			summary.authors = append(summary.authors, archivedTweetAuthor{
				authorID:   tweet.Author.ID,
				screenName: tweet.Author.ScreenName,
				authorName: tweet.Author.Name,
			})
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM tweets_fts WHERE tweet_id=?`, tweet.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO tweets_fts(tweet_id,screen_name,author_name,body,alt_texts) VALUES(?,?,?,?,?)`,
			tweet.ID, tweet.Author.ScreenName, tweet.Author.Name, tweet.Text, altTexts); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tweet_jobs(tweet_id,job_id,body_status,media_status) VALUES(?,?,?,?)
ON CONFLICT(tweet_id,job_id) DO UPDATE SET
	body_status = CASE WHEN tweet_jobs.body_status = 'not_requested' THEN excluded.body_status ELSE tweet_jobs.body_status END,
	media_status = CASE WHEN tweet_jobs.media_status = 'not_requested' THEN excluded.media_status ELSE tweet_jobs.media_status END`, tweet.ID, jobID, bodyStatus, mediaStatus); err != nil {
		return err
	}
	if !preserveExisting {
		if _, err = tx.ExecContext(ctx, `DELETE FROM tweet_relations WHERE tweet_id=?`, tweet.ID); err != nil {
			return err
		}
	}
	for kind, related := range map[string]string{"reply": tweet.ReplyToID, "quote": tweet.QuotedTweetID, "retweet": tweet.RetweetedTweetID} {
		if related != "" {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO tweet_relations VALUES(?,?,?)`, tweet.ID, kind, related); err != nil {
				return err
			}
		}
	}
	writeMedia := !preserveExisting
	if preserveExisting && len(tweet.Media) > 0 {
		var existingMedia int
		if err := tx.GetContext(ctx, &existingMedia, `SELECT COUNT(*) FROM tweet_media WHERE tweet_id=?`, tweet.ID); err != nil {
			return err
		}
		writeMedia = existingMedia == 0
	}
	if writeMedia {
		if !preserveExisting {
			if _, err := tx.ExecContext(ctx, `DELETE FROM tweet_media WHERE tweet_id=?`, tweet.ID); err != nil {
				return err
			}
		}
		for i, media := range tweet.Media {
			if media.ID == "" {
				media.ID = media.URL
			}
			if media.ID == "" {
				media.ID = parser.MediaDownloadURL(media)
			}
			if media.ID == "" {
				continue
			}
			data, err := json.Marshal(media)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO archive_media VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET media_type=excluded.media_type,payload=excluded.payload,source_url=excluded.source_url`, media.ID, media.Type, parser.MediaDownloadURL(media), string(data)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO tweet_media VALUES(?,?,?) ON CONFLICT(tweet_id,media_id) DO UPDATE SET position=excluded.position`, tweet.ID, media.ID, i); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) SetTweetMediaResult(ctx context.Context, jobID int64, tweetID, status, message string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTweetMediaResultTx(ctx, tx, jobID, tweetID, status, message); err != nil {
		return err
	}
	return tx.Commit()
}

func setTweetMediaResultTx(ctx context.Context, tx *sqlx.Tx, jobID int64, tweetID, status, message string) error {
	_, err := tx.ExecContext(ctx, `UPDATE tweet_jobs SET media_status=?,media_error=? WHERE tweet_id=? AND job_id=?`, status, message, tweetID, jobID)
	if err != nil {
		return err
	}
	var payload string
	if err := tx.GetContext(ctx, &payload, `SELECT payload FROM tweets WHERE id=?`, tweetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var tweet parser.TweetData
	if err := json.Unmarshal([]byte(payload), &tweet); err != nil {
		return err
	}
	visited := map[string]struct{}{tweetID: {}}
	var updateRelated func(parser.TweetData) error
	updateRelated = func(related parser.TweetData) error {
		if related.ID == "" {
			return nil
		}
		if _, seen := visited[related.ID]; seen {
			return nil
		}
		visited[related.ID] = struct{}{}
		requested, downloadable, available := 0, 0, 0
		for _, item := range related.Media {
			itemURL := downloader.NormalizeMediaURL(parser.MediaDownloadURL(item))
			itemIdentity := downloader.MediaIdentity(parser.MediaDownloadURL(item))
			for _, media := range tweet.Media {
				if (item.ID != "" && item.ID == media.ID) || (itemIdentity != "" && itemIdentity == downloader.MediaIdentity(parser.MediaDownloadURL(media))) {
					requested++
					break
				}
			}
			if itemURL == "" {
				continue
			}
			downloadable++
			download, err := getDownloadByTweetMediaTx(ctx, tx, related.ID, itemURL)
			if err != nil {
				return err
			}
			if download != nil {
				available++
			}
		}
		if requested > 0 {
			relatedStatus, relatedError := status, message
			if status != "pending" {
				if downloadable == 0 {
					if status == "completed" {
						relatedError = ""
					}
				} else if available == downloadable {
					relatedStatus, relatedError = "completed", ""
				} else if available > 0 {
					relatedStatus = "partial"
				} else {
					relatedStatus = "failed"
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tweet_jobs SET media_status=?,media_error=? WHERE tweet_id=? AND job_id=?`, relatedStatus, relatedError, related.ID, jobID); err != nil {
				return err
			}
		}
		for _, nested := range related.Related {
			if err := updateRelated(nested); err != nil {
				return err
			}
		}
		return nil
	}
	for _, related := range tweet.Related {
		if err := updateRelated(related); err != nil {
			return err
		}
	}
	return nil
}

// LinkRelatedTweetMedia preserves every tweet association while sharing the
// downloaded file and its versioned assets. Nested media is flattened for the
// download loop, but each related tweet must still work when opened on its own.
func (s *Store) LinkRelatedTweetMedia(ctx context.Context, jobID int64, tweet parser.TweetData, media parser.Media) error {
	mediaURL := downloader.NormalizeMediaURL(parser.MediaDownloadURL(media))
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	record, err := getDownloadByTweetMediaTx(ctx, tx, tweet.ID, mediaURL)
	if err != nil || record == nil {
		return err
	}
	assets, err := downloadAssetsTx(ctx, tx, record.ID)
	if err != nil {
		return err
	}
	mutatedDownloads := false
	visited := map[string]struct{}{tweet.ID: {}}
	var link func(parser.TweetData) error
	link = func(related parser.TweetData) error {
		if related.ID == "" {
			return nil
		}
		if _, seen := visited[related.ID]; seen {
			return nil
		}
		visited[related.ID] = struct{}{}
		matches := false
		relatedMediaURL := mediaURL
		id := downloader.MediaIdentity(mediaURL)
		for _, item := range related.Media {
			if (media.ID != "" && item.ID == media.ID) || (id != "" && downloader.MediaIdentity(parser.MediaDownloadURL(item)) == id) {
				matches = true
				relatedMediaURL = downloader.NormalizeMediaURL(parser.MediaDownloadURL(item))
				break
			}
		}
		if matches && related.ID != tweet.ID {
			copy := *record
			copy.ID, copy.JobID, copy.TweetID = 0, jobID, related.ID
			copy.MediaURL = relatedMediaURL
			created, err := createDownloadTx(ctx, tx, copy)
			if err != nil {
				return err
			}
			mutatedDownloads = true
			for kind, assetID := range assets {
				if err := linkDownloadAssetTx(ctx, tx, created.ID, kind, assetID); err != nil {
					return err
				}
			}
			status := "completed"
			for _, item := range related.Media {
				itemURL := downloader.NormalizeMediaURL(parser.MediaDownloadURL(item))
				if itemURL == "" {
					continue
				}
				download, err := getDownloadByTweetMediaTx(ctx, tx, related.ID, itemURL)
				if err != nil {
					return err
				}
				if download == nil {
					status = "partial"
				}
			}
			if err := setTweetMediaResultTx(ctx, tx, jobID, related.ID, status, ""); err != nil {
				return err
			}
		}
		for _, nested := range related.Related {
			if err := link(nested); err != nil {
				return err
			}
		}
		return nil
	}
	for _, related := range tweet.Related {
		if err := link(related); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if mutatedDownloads {
		s.markLibraryDownloadsCacheStale()
	}
	return nil
}

func downloadAssetsTx(ctx context.Context, tx *sqlx.Tx, id int64) (map[config.AssetKind]string, error) {
	rows, err := tx.QueryxContext(ctx, `SELECT kind,asset_id FROM download_assets WHERE download_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[config.AssetKind]string{}
	for rows.Next() {
		var kind config.AssetKind
		var value string
		if err := rows.Scan(&kind, &value); err != nil {
			return nil, err
		}
		result[kind] = value
	}
	return result, rows.Err()
}

func linkDownloadAssetTx(ctx context.Context, tx *sqlx.Tx, downloadID int64, kind config.AssetKind, assetID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO download_assets VALUES(?,?,?) ON CONFLICT(download_id,kind) DO UPDATE SET asset_id=excluded.asset_id`, downloadID, kind, assetID)
	return err
}

func (s *Store) GetTweet(ctx context.Context, id string) (ArchivedTweet, error) {
	return s.getTweet(ctx, id, 0)
}

// normalizeTweetData 保证数组字段在 JSON 中始终是 []，而不是 null。
// 早期归档或外部导入的数据可能缺少这些字段。
func normalizeTweetData(tweet *parser.TweetData) {
	if tweet.Media == nil {
		tweet.Media = []parser.Media{}
	}
	for index := range tweet.Media {
		if tweet.Media[index].Variants == nil {
			tweet.Media[index].Variants = []parser.MediaVariant{}
		}
	}
	for index := range tweet.Related {
		normalizeTweetData(&tweet.Related[index])
	}
}

type tweetRow struct {
	ID          string    `db:"id"`
	AuthorID    string    `db:"author_id"`
	ScreenName  string    `db:"screen_name"`
	AuthorName  string    `db:"author_name"`
	Body        string    `db:"body"`
	PublishedAt time.Time `db:"published_at"`
	Payload     string    `db:"payload"`
	ArchivedAt  time.Time `db:"archived_at"`
	Starred     bool      `db:"starred"`
}

func hydrateArchivedTweetRow(row tweetRow, jobIDs []int64, bodyStatus, mediaStatus, mediaError string, downloads []DownloadRecord, tags []Tag) (ArchivedTweet, error) {
	if jobIDs == nil {
		jobIDs = []int64{}
	}
	if downloads == nil {
		downloads = []DownloadRecord{}
	}
	if bodyStatus == "" {
		bodyStatus = "not_requested"
	}
	if mediaStatus == "" {
		mediaStatus = "not_requested"
	}
	result := ArchivedTweet{
		ArchivedAt:  row.ArchivedAt,
		JobIDs:      jobIDs,
		BodyStatus:  bodyStatus,
		MediaStatus: mediaStatus,
		MediaError:  mediaError,
		Downloads:   downloads,
		Starred:     row.Starred,
		Tags:        tags,
	}
	if err := json.Unmarshal([]byte(row.Payload), &result.TweetData); err != nil {
		return result, err
	}
	// 旧版本/外部导入的 payload 可能不完整。列里存的是归档时同一份基础字段，
	// 用它们补全空值，避免列表显示空记录。
	if result.ID == "" {
		result.ID = row.ID
	}
	if result.Author.ID == "" {
		result.Author.ID = row.AuthorID
	}
	if result.Author.ScreenName == "" {
		result.Author.ScreenName = row.ScreenName
	}
	if result.Author.Name == "" {
		result.Author.Name = row.AuthorName
	}
	if result.Text == "" {
		result.Text = row.Body
	}
	if result.CreatedAt.IsZero() {
		result.CreatedAt = row.PublishedAt
	}
	// 旧版本/不完整的 payload 可能没有 media 字段，反序列化后是 nil，
	// 序列化出去会变成 "media": null。API 契约是数组，这里统一归一化，
	// 避免前端对 null 调 .length 导致整页崩溃。
	normalizeTweetData(&result.TweetData)
	return result, nil
}

func (s *Store) getTweet(ctx context.Context, id string, jobID int64) (ArchivedTweet, error) {
	var row tweetRow
	if err := s.db.GetContext(ctx, &row, `SELECT id,author_id,screen_name,author_name,body,published_at,payload,archived_at,starred FROM tweets WHERE id=?`, id); err != nil {
		return ArchivedTweet{}, err
	}
	var jobIDs []int64
	if err := s.db.SelectContext(ctx, &jobIDs, `SELECT job_id FROM tweet_jobs WHERE tweet_id=? ORDER BY job_id DESC`, id); err != nil {
		return ArchivedTweet{}, err
	}
	var bodyStatus, mediaStatus, mediaError string
	if err := s.db.QueryRowContext(ctx, `
SELECT
  CASE WHEN EXISTS(SELECT 1 FROM tweet_jobs WHERE tweet_id=? AND body_status='archived') THEN 'archived' ELSE 'not_requested' END,
  COALESCE((SELECT media_status FROM tweet_jobs WHERE tweet_id=? AND (?=0 OR job_id=?) ORDER BY job_id DESC LIMIT 1), 'not_requested'),
  COALESCE((SELECT media_error FROM tweet_jobs WHERE tweet_id=? AND (?=0 OR job_id=?) ORDER BY job_id DESC LIMIT 1), '')`, id, id, jobID, jobID, id, jobID, jobID).Scan(&bodyStatus, &mediaStatus, &mediaError); err != nil {
		return ArchivedTweet{}, err
	}
	// 必须写出部分索引谓词 tweet_id <> ''：idx_downloads_tweet_media_unique 是
	// `WHERE tweet_id <> ''` 的部分索引，只写 tweet_id=? 时 SQLite 无法证明谓词成立，
	// 会退化为整表扫描（10 万行约 8ms/次，而 ListTweets 与资产下载路径都会逐条调用）。
	var downloads []DownloadRecord
	if err := s.db.SelectContext(ctx, &downloads, `SELECT d.*, '' AS user_screen_name, '' AS user_name FROM downloads d WHERE tweet_id=? AND tweet_id<>'' ORDER BY id`, id); err != nil {
		return ArchivedTweet{}, err
	}
	tagsByTweet, _ := s.ListTagsForTweets(ctx, []string{id})
	return hydrateArchivedTweetRow(row, jobIDs, bodyStatus, mediaStatus, mediaError, downloads, tagsByTweet[id])
}

// buildFTS5Query 将用户输入转为安全的 FTS5 MATCH 表达式：每词用双引号包裹并启用前缀匹配，
// 避免 FTS5 特殊操作符（如 OR/NOT/:/-/"）造成语法错误。
func buildFTS5Query(search string) string {
	fields := strings.Fields(strings.TrimSpace(search))
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		clean := strings.ReplaceAll(field, `"`, `""`)
		if clean == "" {
			continue
		}
		parts = append(parts, `"`+clean+`"*`)
	}
	return strings.Join(parts, " AND ")
}

// ListConversationTweets 返回与指定 tweetID 属于同一对话串（conversation_id）的所有已归档推文，
// 按发布时间升序排列，供前端 Thread 串联视图使用。
func (s *Store) ListConversationTweets(ctx context.Context, tweetID string) ([]ArchivedTweet, error) {
	anchor, err := s.GetTweet(ctx, tweetID)
	if err != nil {
		return nil, err
	}
	convID := strings.TrimSpace(anchor.ConversationID)
	if convID == "" {
		convID = anchor.ID
	}
	page, err := s.ListTweets(ctx, TweetFilter{
		ConversationID: convID,
		Sort:           "asc",
		Page:           1,
		PageSize:       100,
	})
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 {
		return []ArchivedTweet{anchor}, nil
	}
	found := false
	for _, item := range page.Items {
		if item.ID == anchor.ID {
			found = true
			break
		}
	}
	if !found {
		page.Items = append([]ArchivedTweet{anchor}, page.Items...)
	}
	return page.Items, nil
}

func normalizeTweetFilterTime(raw string, legacy bool) (string, bool) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			if legacy {
				return parsed.UTC().Format("2006-01-02T15:04:05.000Z"), true
			}
			return formatSortableTime(parsed), true
		}
	}
	return raw, false
}

func (s *Store) ListTweets(ctx context.Context, f TweetFilter) (TweetPage, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	legacyTS := s.hasLegacyTweetPublishedAt(ctx)
	where := []string{"1=1"}
	args := []any{}
	if search := strings.TrimSpace(f.Search); search != "" {
		ftsQuery := buildFTS5Query(search)
		if ftsQuery != "" {
			where = append(where, `(t.id IN (SELECT tweet_id FROM tweets_fts WHERE tweets_fts MATCH ?) OR instr(lower(t.body),lower(?))>0 OR instr(lower(t.alt_texts),lower(?))>0)`)
			args = append(args, ftsQuery, search, search)
		} else {
			where = append(where, `(instr(lower(t.body),lower(?))>0 OR instr(lower(t.alt_texts),lower(?))>0)`)
			args = append(args, search, search)
		}
	}
	for _, condition := range []struct{ value, sql string }{
		{strings.TrimPrefix(f.Author, "@"), `(t.author_id=? OR lower(t.screen_name)=lower(?))`},
		{f.ID, `t.id=?`},
		{f.ConversationID, `(t.conversation_id=? OR t.id=?)`},
	} {
		if condition.value != "" {
			where = append(where, condition.sql)
			args = append(args, condition.value)
			if strings.Contains(condition.sql, " OR ") {
				args = append(args, condition.value)
			}
		}
	}
	if f.Starred != nil {
		if *f.Starred {
			where = append(where, `t.starred=1`)
		} else {
			where = append(where, `t.starred=0`)
		}
	}
	if f.TagID > 0 {
		where = append(where, `EXISTS(SELECT 1 FROM tweet_tags tt WHERE tt.tweet_id=t.id AND tt.tag_id=?)`)
		args = append(args, f.TagID)
	}
	if f.CollectionID > 0 {
		where = append(where, `EXISTS(SELECT 1 FROM collection_items ci WHERE ci.collection_id=? AND ci.item_type='tweet' AND ci.item_id=t.id)`)
		args = append(args, f.CollectionID)
	}
	if f.From != "" {
		if norm, ok := normalizeTweetFilterTime(f.From, legacyTS); ok {
			if legacyTS {
				where = append(where, `COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', t.published_at), t.published_at)>=?`)
			} else {
				where = append(where, `t.published_at>=?`)
			}
			args = append(args, norm)
		} else {
			where = append(where, `datetime(t.published_at)>=datetime(?)`)
			args = append(args, f.From)
		}
	}
	if f.To != "" {
		if norm, ok := normalizeTweetFilterTime(f.To, legacyTS); ok {
			if legacyTS {
				where = append(where, `COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', t.published_at), t.published_at)<?`)
			} else {
				where = append(where, `t.published_at<?`)
			}
			args = append(args, norm)
		} else {
			where = append(where, `datetime(t.published_at)<datetime(?)`)
			args = append(args, f.To)
		}
	}
	if f.MediaType != "" {
		if f.MediaType == "text" {
			where = append(where, `NOT EXISTS(SELECT 1 FROM tweet_media tm WHERE tm.tweet_id=t.id)`)
		} else {
			where = append(where, `EXISTS(SELECT 1 FROM tweet_media tm JOIN archive_media m ON m.id=tm.media_id WHERE tm.tweet_id=t.id AND m.media_type=?)`)
			args = append(args, f.MediaType)
		}
	}
	if f.JobID > 0 {
		where = append(where, `EXISTS(SELECT 1 FROM tweet_jobs tj WHERE tj.tweet_id=t.id AND tj.job_id=?)`)
		args = append(args, f.JobID)
	}
	switch f.Retweets {
	case "exclude":
		where = append(where, `NOT EXISTS(SELECT 1 FROM tweet_relations tr WHERE tr.tweet_id=t.id AND tr.relation='retweet')`)
	case "only":
		where = append(where, `EXISTS(SELECT 1 FROM tweet_relations tr WHERE tr.tweet_id=t.id AND tr.relation='retweet')`)
	}
	clause := strings.Join(where, " AND ")
	result := TweetPage{Items: []ArchivedTweet{}, Pagination: Pagination{Page: f.Page, PageSize: f.PageSize}}
	if err := s.db.GetContext(ctx, &result.Pagination.Total, `SELECT COUNT(*) FROM tweets t WHERE `+clause, args...); err != nil {
		return result, err
	}
	result.Pagination.TotalPages = max(1, (result.Pagination.Total+f.PageSize-1)/f.PageSize)
	result.Pagination.Page = min(f.Page, result.Pagination.TotalPages)
	args = append(args, f.PageSize, (result.Pagination.Page-1)*f.PageSize)
	var orderClause string
	if legacyTS {
		orderClause = `COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', t.published_at), t.published_at) DESC, t.published_at DESC, t.id DESC`
		switch f.Sort {
		case "asc":
			orderClause = `COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', t.published_at), t.published_at) ASC, t.published_at ASC, t.id ASC`
		case "likes_desc":
			orderClause = `t.like_count DESC, COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', t.published_at), t.published_at) DESC, t.id DESC`
		case "retweets_desc":
			orderClause = `t.retweet_count DESC, COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', t.published_at), t.published_at) DESC, t.id DESC`
		case "views_desc":
			orderClause = `t.view_count DESC, COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', t.published_at), t.published_at) DESC, t.id DESC`
		}
	} else {
		orderClause = `t.published_at DESC, t.id DESC`
		switch f.Sort {
		case "asc":
			orderClause = `t.published_at ASC, t.id ASC`
		case "likes_desc":
			orderClause = `t.like_count DESC, t.published_at DESC, t.id DESC`
		case "retweets_desc":
			orderClause = `t.retweet_count DESC, t.published_at DESC, t.id DESC`
		case "views_desc":
			orderClause = `t.view_count DESC, t.published_at DESC, t.id DESC`
		}
	}
	var rows []tweetRow
	if err := s.db.SelectContext(ctx, &rows, `SELECT t.id,t.author_id,t.screen_name,t.author_name,t.body,t.published_at,t.payload,t.archived_at,t.starred FROM tweets t WHERE `+clause+
		` ORDER BY `+orderClause+` LIMIT ? OFFSET ?`, args...); err != nil {
		return result, err
	}
	if len(rows) == 0 {
		return result, nil
	}

	idArgs := make([]any, len(rows))
	for i, row := range rows {
		idArgs[i] = row.ID
	}
	placeholders := sqlPlaceholders(len(rows))

	type tweetJobRow struct {
		TweetID     string `db:"tweet_id"`
		JobID       int64  `db:"job_id"`
		BodyStatus  string `db:"body_status"`
		MediaStatus string `db:"media_status"`
		MediaError  string `db:"media_error"`
	}
	var jobRows []tweetJobRow
	if err := s.db.SelectContext(ctx, &jobRows, `SELECT tweet_id, job_id, body_status, media_status, media_error FROM tweet_jobs WHERE tweet_id IN (`+placeholders+`) ORDER BY tweet_id ASC, job_id DESC`, idArgs...); err != nil {
		return result, err
	}
	type tweetJobAgg struct {
		jobIDs      []int64
		bodyStatus  string
		mediaStatus string
		mediaError  string
		mediaPicked bool
	}
	jobsByTweet := make(map[string]*tweetJobAgg, len(rows))
	for _, jr := range jobRows {
		agg := jobsByTweet[jr.TweetID]
		if agg == nil {
			agg = &tweetJobAgg{bodyStatus: "not_requested", mediaStatus: "not_requested"}
			jobsByTweet[jr.TweetID] = agg
		}
		agg.jobIDs = append(agg.jobIDs, jr.JobID)
		if jr.BodyStatus == "archived" {
			agg.bodyStatus = "archived"
		}
		if !agg.mediaPicked && (f.JobID == 0 || jr.JobID == f.JobID) {
			agg.mediaStatus = jr.MediaStatus
			agg.mediaError = jr.MediaError
			agg.mediaPicked = true
		}
	}

	// 保持部分索引谓词 d.tweet_id <> ''，避免 downloads 整表扫描。
	var downloadRows []DownloadRecord
	if err := s.db.SelectContext(ctx, &downloadRows, `SELECT d.*, '' AS user_screen_name, '' AS user_name FROM downloads d WHERE d.tweet_id IN (`+placeholders+`) AND d.tweet_id<>'' ORDER BY d.id`, idArgs...); err != nil {
		return result, err
	}
	downloadsByTweet := make(map[string][]DownloadRecord, len(rows))
	for _, d := range downloadRows {
		downloadsByTweet[d.TweetID] = append(downloadsByTweet[d.TweetID], d)
	}

	tweetIDs := make([]string, len(rows))
	for i, row := range rows {
		tweetIDs[i] = row.ID
	}
	tagsByTweet, _ := s.ListTagsForTweets(ctx, tweetIDs)

	result.Items = make([]ArchivedTweet, 0, len(rows))
	for _, row := range rows {
		var jobIDs []int64
		bodyStatus, mediaStatus, mediaError := "not_requested", "not_requested", ""
		if agg := jobsByTweet[row.ID]; agg != nil {
			jobIDs = agg.jobIDs
			bodyStatus = agg.bodyStatus
			mediaStatus = agg.mediaStatus
			mediaError = agg.mediaError
		}
		tweet, err := hydrateArchivedTweetRow(row, jobIDs, bodyStatus, mediaStatus, mediaError, downloadsByTweet[row.ID], tagsByTweet[row.ID])
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, tweet)
	}
	return result, nil
}

// Replay uses immutable IDs and the original archive time so updates and newly
// archived tweets cannot shift a saved page boundary.
func (s *Store) ListTweetReplay(ctx context.Context, authorID, afterID string, archivedBefore time.Time, limit int) ([]parser.TweetData, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var payloads []string
	if err := s.db.SelectContext(ctx, &payloads, `SELECT payload FROM tweets WHERE author_id=? AND archived_at<=? AND id>? ORDER BY id LIMIT ?`, authorID, archivedBefore.UTC(), afterID, limit); err != nil {
		return nil, err
	}
	items := make([]parser.TweetData, 0, len(payloads))
	for _, payload := range payloads {
		var tweet parser.TweetData
		if err := json.Unmarshal([]byte(payload), &tweet); err != nil {
			return nil, err
		}
		items = append(items, tweet)
	}
	return items, nil
}

// ListTweetReplayPending 仅返回有媒体且尚未有任一任务将媒体标记为 completed 的历史推文，
// 避免非回溯的 ArchiveBoth 每次都全量重扫/重写已完成的历史推文。
func (s *Store) ListTweetReplayPending(ctx context.Context, authorID, afterID string, archivedBefore time.Time, limit int) ([]parser.TweetData, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var payloads []string
	if err := s.db.SelectContext(ctx, &payloads, `
SELECT t.payload FROM tweets t
WHERE t.author_id = ?
  AND t.archived_at <= ?
  AND t.id > ?
  AND EXISTS (SELECT 1 FROM tweet_media tm WHERE tm.tweet_id = t.id)
  AND NOT EXISTS (
    SELECT 1 FROM tweet_jobs tj
    WHERE tj.tweet_id = t.id AND tj.media_status = 'completed'
  )
ORDER BY t.id
LIMIT ?`, authorID, archivedBefore.UTC(), afterID, limit); err != nil {
		return nil, err
	}
	items := make([]parser.TweetData, 0, len(payloads))
	for _, payload := range payloads {
		var tweet parser.TweetData
		if err := json.Unmarshal([]byte(payload), &tweet); err != nil {
			return nil, err
		}
		items = append(items, tweet)
	}
	return items, nil
}

func (s *Store) GetTweetSync(ctx context.Context, userID string) (TweetSync, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tweet_sync(user_id,updated_at) VALUES(?,?) ON CONFLICT(user_id) DO NOTHING`, userID, time.Now().UTC()); err != nil {
		return TweetSync{}, err
	}
	var state TweetSync
	err := s.db.GetContext(ctx, &state, `SELECT * FROM tweet_sync WHERE user_id=?`, userID)
	return state, err
}

// TouchTweetSync 只推进 updated_at，不清空全文归档的进度字段。用于让没有产生
// tweet_sync 的受保护用户也能按“最久未处理”轮转到队尾。
func (s *Store) TouchTweetSync(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tweet_sync(user_id, updated_at) VALUES(?, ?)
ON CONFLICT(user_id) DO UPDATE SET updated_at=excluded.updated_at`,
		userID, time.Now().UTC())
	return err
}

// ListTweetSyncUpdatedAt 返回每个用户最近一次全文归档时间。只读，不像
// GetTweetSync 那样为不存在的用户插入空行。
func (s *Store) ListTweetSyncUpdatedAt(ctx context.Context) (map[string]time.Time, error) {
	rows := []struct {
		UserID    string    `db:"user_id"`
		UpdatedAt time.Time `db:"updated_at"`
	}{}
	if err := s.db.SelectContext(ctx, &rows, `SELECT user_id, updated_at FROM tweet_sync`); err != nil {
		return nil, err
	}
	result := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		result[row.UserID] = row.UpdatedAt
	}
	return result, nil
}

func (s *Store) SaveTweetSync(ctx context.Context, state TweetSync) error {
	state.UpdatedAt = time.Now().UTC()
	_, err := s.db.NamedExecContext(ctx, `
INSERT INTO tweet_sync (user_id, last_id, cursor, head_id, status, error, count, updated_at)
VALUES (:user_id, :last_id, :cursor, :head_id, :status, :error, :count, :updated_at)
ON CONFLICT(user_id) DO UPDATE SET
	last_id = excluded.last_id,
	cursor = excluded.cursor,
	head_id = excluded.head_id,
	status = excluded.status,
	error = excluded.error,
	count = excluded.count,
	updated_at = excluded.updated_at`, state)
	return err
}

func (s *Store) ListTweetSync(ctx context.Context) ([]TweetSync, error) {
	items := []TweetSync{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM tweet_sync ORDER BY COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', updated_at), updated_at) DESC, updated_at DESC, user_id ASC LIMIT 200`)
	return items, err
}

func (s *Store) CreateArchiveJob(ctx context.Context, draft JobDraft) (Job, error) {
	// 单个归档任务由明确的用户操作创建（重试单条推文/立即归档），不做批量入队去重：
	// 去重只用于批量创建与定时计划，避免内部调用方拿不到刚创建的任务行。
	items, err := s.createJobs(ctx, []JobDraft{draft}, false)
	if err != nil {
		return Job{}, err
	}
	if len(items) != 1 {
		return Job{}, fmt.Errorf("job creation returned no row")
	}
	return items[0], nil
}

// RetryTweetMedia checks and enqueues in one immediate transaction so repeated
// requests cannot create parallel retries for the same archived tweet.
func (s *Store) RetryTweetMedia(ctx context.Context, tweetID string) (Job, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var payload string
	if err := tx.GetContext(ctx, &payload, `SELECT payload FROM tweets WHERE id=?`, tweetID); err != nil {
		return Job{}, err
	}
	var tweet parser.TweetData
	if err := json.Unmarshal([]byte(payload), &tweet); err != nil {
		return Job{}, err
	}
	var active int
	if err := tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM jobs j WHERE status IN (`+activeJobStatusesSQL+`) AND
EXISTS(SELECT 1 FROM tweet_jobs tj WHERE tj.job_id=j.id AND tj.tweet_id=?)`, tweetID); err != nil {
		return Job{}, err
	}
	if active > 0 {
		return Job{}, ErrJobRetryBusy
	}
	tweetURL := strings.TrimSpace(tweet.URL)
	if tweetURL == "" {
		tweetURL = "https://x.com/i/web/status/" + tweetID
	}
	var inputs []string
	if err := tx.SelectContext(ctx, &inputs, `SELECT input FROM jobs WHERE kind=? AND status IN (`+activeJobStatusesSQL+`)`, JobKindTweetLink); err != nil {
		return Job{}, err
	}
	for _, input := range inputs {
		_, activeTweetID, err := parser.ExtractTweetURL(input)
		if input == tweetURL || (err == nil && activeTweetID == tweetID) {
			return Job{}, ErrJobRetryBusy
		}
	}
	if err := checkJobQueueCapacity(ctx, tx, 1); err != nil {
		return Job{}, err
	}
	now := time.Now().UTC()
	var job Job
	if err := tx.GetContext(ctx, &job, `INSERT INTO jobs(kind,status,input,title,archive_mode,progress,message,created_at,updated_at,enqueued_at,enqueue_ns,source)
VALUES(?,?,?,?,?,0,?,?,?,?,?,?) RETURNING *`, JobKindTweetLink, JobPending, tweetURL, "Retry media "+tweetID, ArchiveMedia, "排队中", now, now, now, now.UnixNano(), JobSourceManual); err != nil {
		return Job{}, err
	}
	return job, tx.Commit()
}
