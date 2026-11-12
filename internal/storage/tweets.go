package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
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
}

type TweetFilter struct {
	Sort                                    string
	Search, Author, From, To, MediaType, ID string
	JobID                                   int64
	Page, PageSize                          int
}
type TweetPage struct {
	Items      []ArchivedTweet `json:"items"`
	Pagination Pagination      `json:"pagination"`
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
	for _, col := range []struct{ name, definition string }{{"archive_mode", "TEXT NOT NULL DEFAULT 'media'"}, {"backfill", "BOOLEAN NOT NULL DEFAULT 0"}} {
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
	return tx.Commit()
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
	if tweet.ID == "" {
		return ErrValidation("推文缺少 ID")
	}
	if tweet.Media == nil {
		tweet.Media = []parser.Media{}
	}
	for _, related := range tweet.Related {
		relatedStatus := "not_requested"
		if mediaStatus != "not_requested" {
			for _, item := range related.Media {
				for _, requested := range tweet.Media {
					if (item.ID != "" && item.ID == requested.ID) || (parser.MediaDownloadURL(item) != "" && downloader.MediaIdentity(parser.MediaDownloadURL(item)) == downloader.MediaIdentity(parser.MediaDownloadURL(requested))) {
						relatedStatus = mediaStatus
					}
				}
			}
		}
		if err := s.archiveTweet(ctx, jobID, related, bodyStatus, relatedStatus); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(tweet)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO tweets(id,author_id,screen_name,author_name,body,published_at,payload,archived_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET author_id=excluded.author_id,screen_name=excluded.screen_name,
author_name=excluded.author_name,body=excluded.body,published_at=excluded.published_at,payload=excluded.payload,updated_at=excluded.updated_at`,
		tweet.ID, tweet.Author.ID, tweet.Author.ScreenName, tweet.Author.Name, tweet.Text, tweet.CreatedAt.UTC().Format(time.RFC3339Nano), string(payload), now, now)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tweet_jobs(tweet_id,job_id,body_status,media_status) VALUES(?,?,?,?)
ON CONFLICT(tweet_id,job_id) DO NOTHING`, tweet.ID, jobID, bodyStatus, mediaStatus); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM tweet_relations WHERE tweet_id=?`, tweet.ID); err != nil {
		return err
	}
	for kind, related := range map[string]string{"reply": tweet.ReplyToID, "quote": tweet.QuotedTweetID, "retweet": tweet.RetweetedTweetID} {
		if related != "" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO tweet_relations VALUES(?,?,?)`, tweet.ID, kind, related); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tweet_media WHERE tweet_id=?`, tweet.ID); err != nil {
		return err
	}
	for i, media := range tweet.Media {
		if media.ID == "" {
			media.ID = media.URL
		}
		if media.ID == "" {
			continue
		}
		data, err := json.Marshal(media)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO archive_media VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,source_url=excluded.source_url`, media.ID, media.Type, parser.MediaDownloadURL(media), string(data)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tweet_media VALUES(?,?,?) ON CONFLICT(tweet_id,media_id) DO UPDATE SET position=excluded.position`, tweet.ID, media.ID, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetTweetMediaResult(ctx context.Context, jobID int64, tweetID, status, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tweet_jobs SET media_status=?,media_error=? WHERE tweet_id=? AND job_id=?`, status, message, tweetID, jobID)
	if err != nil {
		return err
	}
	var payload string
	if err := s.db.GetContext(ctx, &payload, `SELECT payload FROM tweets WHERE id=?`, tweetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var tweet parser.TweetData
	if err := json.Unmarshal([]byte(payload), &tweet); err != nil {
		return err
	}
	var updateRelated func(parser.TweetData) error
	updateRelated = func(related parser.TweetData) error {
		requested, available := 0, 0
		for _, item := range related.Media {
			for _, media := range tweet.Media {
				if (item.ID != "" && item.ID == media.ID) || (parser.MediaDownloadURL(item) != "" && downloader.MediaIdentity(parser.MediaDownloadURL(item)) == downloader.MediaIdentity(parser.MediaDownloadURL(media))) {
					requested++
					download, err := s.GetDownloadByTweetMedia(ctx, related.ID, downloader.NormalizeMediaURL(parser.MediaDownloadURL(item)))
					if err != nil {
						return err
					}
					if download != nil {
						available++
					}
					break
				}
			}
		}
		if requested > 0 {
			relatedStatus, relatedError := status, message
			if status != "pending" {
				relatedStatus = "failed"
				if available == requested {
					relatedStatus, relatedError = "completed", ""
				} else if available > 0 {
					relatedStatus = "partial"
				}
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE tweet_jobs SET media_status=?,media_error=? WHERE tweet_id=? AND job_id=?`, relatedStatus, relatedError, related.ID, jobID); err != nil {
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
	record, err := s.GetDownloadByTweetMedia(ctx, tweet.ID, mediaURL)
	if err != nil || record == nil {
		return err
	}
	assets, err := s.DownloadAssets(ctx, record.ID)
	if err != nil {
		return err
	}
	var link func(parser.TweetData) error
	link = func(related parser.TweetData) error {
		matches := false
		relatedMediaURL := mediaURL
		for _, item := range related.Media {
			if (media.ID != "" && item.ID == media.ID) || downloader.MediaIdentity(parser.MediaDownloadURL(item)) == downloader.MediaIdentity(mediaURL) {
				matches = true
				relatedMediaURL = downloader.NormalizeMediaURL(parser.MediaDownloadURL(item))
				break
			}
		}
		if matches && related.ID != tweet.ID {
			copy := *record
			copy.ID, copy.JobID, copy.TweetID = 0, jobID, related.ID
			copy.MediaURL = relatedMediaURL
			created, err := s.CreateDownload(ctx, copy)
			if err != nil {
				return err
			}
			for kind, assetID := range assets {
				if err := s.LinkDownloadAsset(ctx, created.ID, kind, assetID); err != nil {
					return err
				}
			}
			status := "completed"
			for _, item := range related.Media {
				download, err := s.GetDownloadByTweetMedia(ctx, related.ID, downloader.NormalizeMediaURL(parser.MediaDownloadURL(item)))
				if err != nil {
					return err
				}
				if download == nil {
					status = "partial"
				}
			}
			if err := s.SetTweetMediaResult(ctx, jobID, related.ID, status, ""); err != nil {
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
	return nil
}

func (s *Store) GetTweet(ctx context.Context, id string) (ArchivedTweet, error) {
	return s.getTweet(ctx, id, 0)
}

func (s *Store) getTweet(ctx context.Context, id string, jobID int64) (ArchivedTweet, error) {
	var row struct {
		Payload    string    `db:"payload"`
		ArchivedAt time.Time `db:"archived_at"`
	}
	if err := s.db.GetContext(ctx, &row, `SELECT payload,archived_at FROM tweets WHERE id=?`, id); err != nil {
		return ArchivedTweet{}, err
	}
	result := ArchivedTweet{ArchivedAt: row.ArchivedAt, JobIDs: []int64{}, Downloads: []DownloadRecord{}}
	if err := json.Unmarshal([]byte(row.Payload), &result.TweetData); err != nil {
		return result, err
	}
	if err := s.db.SelectContext(ctx, &result.JobIDs, `SELECT job_id FROM tweet_jobs WHERE tweet_id=? ORDER BY job_id DESC`, id); err != nil {
		return result, err
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT
  CASE WHEN EXISTS(SELECT 1 FROM tweet_jobs WHERE tweet_id=? AND body_status='archived') THEN 'archived' ELSE 'not_requested' END,
  COALESCE((SELECT media_status FROM tweet_jobs WHERE tweet_id=? AND (?=0 OR job_id=?) ORDER BY job_id DESC LIMIT 1), 'not_requested'),
  COALESCE((SELECT media_error FROM tweet_jobs WHERE tweet_id=? AND (?=0 OR job_id=?) ORDER BY job_id DESC LIMIT 1), '')`, id, id, jobID, jobID, id, jobID, jobID).Scan(&result.BodyStatus, &result.MediaStatus, &result.MediaError); err != nil {
		return result, err
	}
	if err := s.db.SelectContext(ctx, &result.Downloads, `SELECT d.*, '' AS user_screen_name, '' AS user_name FROM downloads d WHERE tweet_id=? ORDER BY id`, id); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) ListTweets(ctx context.Context, f TweetFilter) (TweetPage, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	where := []string{"1=1"}
	args := []any{}
	for _, condition := range []struct{ value, sql string }{
		{f.Search, `instr(lower(t.body),lower(?))>0`}, {strings.TrimPrefix(f.Author, "@"), `(t.author_id=? OR lower(t.screen_name)=lower(?))`},
		{f.From, `datetime(t.published_at)>=datetime(?)`}, {f.To, `datetime(t.published_at)<datetime(?)`}, {f.ID, `t.id=?`},
	} {
		if condition.value != "" {
			where = append(where, condition.sql)
			args = append(args, condition.value)
			if strings.Contains(condition.sql, " OR ") {
				args = append(args, condition.value)
			}
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
	clause := strings.Join(where, " AND ")
	result := TweetPage{Items: []ArchivedTweet{}, Pagination: Pagination{Page: f.Page, PageSize: f.PageSize}}
	if err := s.db.GetContext(ctx, &result.Pagination.Total, `SELECT COUNT(*) FROM tweets t WHERE `+clause, args...); err != nil {
		return result, err
	}
	result.Pagination.TotalPages = max(1, (result.Pagination.Total+f.PageSize-1)/f.PageSize)
	result.Pagination.Page = min(f.Page, result.Pagination.TotalPages)
	var ids []string
	args = append(args, f.PageSize, (result.Pagination.Page-1)*f.PageSize)
	order := "DESC"
	if f.Sort == "asc" {
		order = "ASC"
	}
	if err := s.db.SelectContext(ctx, &ids, `SELECT t.id FROM tweets t WHERE `+clause+` ORDER BY published_at `+order+`,id `+order+` LIMIT ? OFFSET ?`, args...); err != nil {
		return result, err
	}
	for _, id := range ids {
		tweet, err := s.getTweet(ctx, id, f.JobID)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, tweet)
	}
	return result, nil
}

func (s *Store) GetTweetSync(ctx context.Context, userID string) (TweetSync, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tweet_sync(user_id,updated_at) VALUES(?,?) ON CONFLICT(user_id) DO NOTHING`, userID, time.Now().UTC()); err != nil {
		return TweetSync{}, err
	}
	var state TweetSync
	err := s.db.GetContext(ctx, &state, `SELECT * FROM tweet_sync WHERE user_id=?`, userID)
	return state, err
}

func (s *Store) SaveTweetSync(ctx context.Context, state TweetSync) error {
	state.UpdatedAt = time.Now().UTC()
	_, err := s.db.NamedExecContext(ctx, `UPDATE tweet_sync SET last_id=:last_id,cursor=:cursor,head_id=:head_id,status=:status,error=:error,count=:count,updated_at=:updated_at WHERE user_id=:user_id`, state)
	return err
}

func (s *Store) ListTweetSync(ctx context.Context) ([]TweetSync, error) {
	items := []TweetSync{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM tweet_sync ORDER BY updated_at DESC LIMIT 200`)
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
	var inputs []string
	if err := tx.SelectContext(ctx, &inputs, `SELECT input FROM jobs WHERE kind=? AND status IN (`+activeJobStatusesSQL+`)`, JobKindTweetLink); err != nil {
		return Job{}, err
	}
	for _, input := range inputs {
		_, activeTweetID, err := parser.ExtractTweetURL(input)
		if input == tweet.URL || (err == nil && activeTweetID == tweetID) {
			return Job{}, ErrJobRetryBusy
		}
	}
	if err := checkJobQueueCapacity(ctx, tx, 1); err != nil {
		return Job{}, err
	}
	now := time.Now().UTC()
	var job Job
	if err := tx.GetContext(ctx, &job, `INSERT INTO jobs(kind,status,input,title,archive_mode,progress,message,created_at,updated_at,enqueued_at,enqueue_ns,source)
VALUES(?,?,?,?,?,0,?,?,?,?,?,?) RETURNING *`, JobKindTweetLink, JobPending, tweet.URL, "Retry media "+tweetID, ArchiveMedia, "排队中", now, now, now, now.UnixNano(), JobSourceManual); err != nil {
		return Job{}, err
	}
	return job, tx.Commit()
}
