package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sqlx.DB
}

const (
	MinArchiveScheduleIntervalMinutes = 5
	MaxArchiveScheduleIntervalMinutes = 60 * 24 * 30
	MaxArchiveScheduleItems           = 200
)

var ErrArchiveScheduleAlreadyClaimed = errors.New("定时归档计划已被其他运行领取")

func Open(path string) (*Store, error) {
	db, err := sqlx.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// SQLite 只允许单个写事务。限制为单连接可让 database/sql 在 Go 层串行化
	// 读写，彻底避免高并发下 "database is locked"；配合 WAL 让写入更快且不阻塞读快照。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.EnsureConfig(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS app_config (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		download_dir TEXT NOT NULL,
		max_concurrency INTEGER NOT NULL,
		proxy_url TEXT NOT NULL DEFAULT '',
		auth_token TEXT NOT NULL DEFAULT '',
			csrf_token TEXT NOT NULL DEFAULT '',
			additional_cookies TEXT NOT NULL DEFAULT '',
			auto_retry_failed BOOLEAN NOT NULL DEFAULT 1,
			auto_follow_protected BOOLEAN NOT NULL DEFAULT 0,
			include_nested_tweet_media BOOLEAN NOT NULL DEFAULT 0,
			file_naming_mode TEXT NOT NULL DEFAULT 'tweet_text',
			max_filename_length INTEGER NOT NULL DEFAULT 120,
			storage_type TEXT NOT NULL DEFAULT 'local',
			smb_host TEXT NOT NULL DEFAULT '',
			smb_port INTEGER NOT NULL DEFAULT 445,
			smb_share TEXT NOT NULL DEFAULT '',
			smb_path TEXT NOT NULL DEFAULT '',
			smb_domain TEXT NOT NULL DEFAULT '',
			smb_username TEXT NOT NULL DEFAULT '',
			smb_password TEXT NOT NULL DEFAULT '',
			webdav_url TEXT NOT NULL DEFAULT '',
			webdav_path TEXT NOT NULL DEFAULT '',
			webdav_username TEXT NOT NULL DEFAULT '',
			webdav_password TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL
		);

CREATE TABLE IF NOT EXISTS jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	status TEXT NOT NULL,
	input TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	progress REAL NOT NULL DEFAULT 0,
	message TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_jobs_status_created_at ON jobs (status, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_created_at_id ON jobs (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS archive_schedules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT 1,
	interval_minutes INTEGER NOT NULL,
	items_json TEXT NOT NULL,
	last_run_at DATETIME,
	next_run_at DATETIME NOT NULL,
	last_job_ids TEXT NOT NULL DEFAULT '[]',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_archive_schedules_enabled_next_run_at ON archive_schedules (enabled, next_run_at);

CREATE TABLE IF NOT EXISTS downloads (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL,
	tweet_id TEXT NOT NULL DEFAULT '',
	media_url TEXT NOT NULL,
	file_path TEXT NOT NULL,
	bytes INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL,
	FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE
);

	CREATE TABLE IF NOT EXISTS failed_media (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id INTEGER NOT NULL,
		media_url TEXT NOT NULL,
		error TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		screen_name TEXT NOT NULL,
		name TEXT NOT NULL,
		protected BOOLEAN NOT NULL DEFAULT 0,
		friends_count INTEGER NOT NULL DEFAULT 0,
		media_count INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME NOT NULL
	);

	CREATE UNIQUE INDEX IF NOT EXISTS idx_users_screen_name ON users (screen_name);

	CREATE TABLE IF NOT EXISTS user_previous_names (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL,
		screen_name TEXT NOT NULL,
		name TEXT NOT NULL,
		recorded_at DATETIME NOT NULL,
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS twitter_lists (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		owner_user_id TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS user_entities (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL,
		parent_dir TEXT NOT NULL COLLATE NOCASE,
		latest_release_time DATETIME,
		media_count INTEGER,
		last_seen_tweet_id TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL,
		UNIQUE(user_id, parent_dir),
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS list_entities (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		list_id TEXT NOT NULL,
		name TEXT NOT NULL,
		parent_dir TEXT NOT NULL COLLATE NOCASE,
		updated_at DATETIME NOT NULL,
		UNIQUE(list_id, parent_dir)
	);

	CREATE TABLE IF NOT EXISTS user_links (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL,
		list_entity_id INTEGER NOT NULL,
		updated_at DATETIME NOT NULL,
		UNIQUE(user_id, list_entity_id),
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
		FOREIGN KEY(list_entity_id) REFERENCES list_entities(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_user_links_user_id ON user_links (user_id);

	CREATE TABLE IF NOT EXISTS failed_tweets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id INTEGER NOT NULL,
		entity_id INTEGER NOT NULL,
		tweet_id TEXT NOT NULL,
		payload TEXT NOT NULL,
		error TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		UNIQUE(entity_id, tweet_id),
		FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE,
		FOREIGN KEY(entity_id) REFERENCES user_entities(id) ON DELETE CASCADE
	);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	if err := s.addMissingColumns(); err != nil {
		return err
	}
	if err := s.normalizeDownloadsMediaURL(); err != nil {
		return err
	}
	if err := s.deduplicateDownloads(); err != nil {
		return err
	}
	return s.addMissingIndexes()
}

func (s *Store) addMissingColumns() error {
	columns := map[string]string{
		"additional_cookies":         `ALTER TABLE app_config ADD COLUMN additional_cookies TEXT NOT NULL DEFAULT ''`,
		"auto_follow_protected":      `ALTER TABLE app_config ADD COLUMN auto_follow_protected BOOLEAN NOT NULL DEFAULT 0`,
		"include_nested_tweet_media": `ALTER TABLE app_config ADD COLUMN include_nested_tweet_media BOOLEAN NOT NULL DEFAULT 0`,
		"file_naming_mode":           `ALTER TABLE app_config ADD COLUMN file_naming_mode TEXT NOT NULL DEFAULT 'tweet_text'`,
		"max_filename_length":        `ALTER TABLE app_config ADD COLUMN max_filename_length INTEGER NOT NULL DEFAULT 120`,
		"storage_type":               `ALTER TABLE app_config ADD COLUMN storage_type TEXT NOT NULL DEFAULT 'local'`,
		"smb_host":                   `ALTER TABLE app_config ADD COLUMN smb_host TEXT NOT NULL DEFAULT ''`,
		"smb_port":                   `ALTER TABLE app_config ADD COLUMN smb_port INTEGER NOT NULL DEFAULT 445`,
		"smb_share":                  `ALTER TABLE app_config ADD COLUMN smb_share TEXT NOT NULL DEFAULT ''`,
		"smb_path":                   `ALTER TABLE app_config ADD COLUMN smb_path TEXT NOT NULL DEFAULT ''`,
		"smb_domain":                 `ALTER TABLE app_config ADD COLUMN smb_domain TEXT NOT NULL DEFAULT ''`,
		"smb_username":               `ALTER TABLE app_config ADD COLUMN smb_username TEXT NOT NULL DEFAULT ''`,
		"smb_password":               `ALTER TABLE app_config ADD COLUMN smb_password TEXT NOT NULL DEFAULT ''`,
		"webdav_url":                 `ALTER TABLE app_config ADD COLUMN webdav_url TEXT NOT NULL DEFAULT ''`,
		"webdav_path":                `ALTER TABLE app_config ADD COLUMN webdav_path TEXT NOT NULL DEFAULT ''`,
		"webdav_username":            `ALTER TABLE app_config ADD COLUMN webdav_username TEXT NOT NULL DEFAULT ''`,
		"webdav_password":            `ALTER TABLE app_config ADD COLUMN webdav_password TEXT NOT NULL DEFAULT ''`,
	}
	for name, statement := range columns {
		var exists int
		if err := s.db.Get(&exists, `SELECT COUNT(*) FROM pragma_table_info('app_config') WHERE name = ?`, name); err != nil {
			return err
		}
		if exists == 0 {
			if _, err := s.db.Exec(statement); err != nil {
				return err
			}
		}
	}
	// user_entities：增量归档早停所需的 last_seen_tweet_id
	var hasLastSeen int
	if err := s.db.Get(&hasLastSeen, `SELECT COUNT(*) FROM pragma_table_info('user_entities') WHERE name = 'last_seen_tweet_id'`); err != nil {
		return err
	}
	if hasLastSeen == 0 {
		if _, err := s.db.Exec(`ALTER TABLE user_entities ADD COLUMN last_seen_tweet_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) deduplicateDownloads() error {
	_, err := s.db.Exec(`
	DELETE FROM downloads
	WHERE tweet_id <> ''
	  AND id NOT IN (
		SELECT MIN(id)
		FROM downloads
		WHERE tweet_id <> ''
		GROUP BY tweet_id, media_url
	  )`)
	return err
}

// normalizeDownloadsMediaURL 规范化历史 downloads 记录中的 media_url（去掉 ?tag= 等易变参数），
// 使旧记录与规范化后的去重键一致，随后由 deduplicateDownloads 清理因规范化产生的重复行。
// 幂等：无待规范化的行时直接返回，不动索引。
func (s *Store) normalizeDownloadsMediaURL() error {
	type downloadRow struct {
		ID       int64  `db:"id"`
		MediaURL string `db:"media_url"`
	}
	var rows []downloadRow
	if err := s.db.Select(&rows, `SELECT id, media_url FROM downloads WHERE tweet_id <> '' AND media_url <> ''`); err != nil {
		return err
	}
	pending := make([]downloadRow, 0)
	for _, r := range rows {
		if downloader.NormalizeMediaURL(r.MediaURL) != r.MediaURL {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	// 临时移除唯一索引，避免 UPDATE 触发 (tweet_id, media_url) 约束冲突；
	// 随后 deduplicateDownloads 清理重复行，addMissingIndexes 重建索引。
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_downloads_tweet_media_unique`); err != nil {
		return err
	}
	for _, r := range pending {
		if _, err := s.db.Exec(`UPDATE downloads SET media_url = ? WHERE id = ?`, downloader.NormalizeMediaURL(r.MediaURL), r.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) addMissingIndexes() error {
	_, err := s.db.Exec(`
	CREATE UNIQUE INDEX IF NOT EXISTS idx_downloads_tweet_media_unique
	ON downloads (tweet_id, media_url)
	WHERE tweet_id <> '';
	CREATE INDEX IF NOT EXISTS idx_downloads_job_created_at
	ON downloads (job_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_failed_media_job_created_at
	ON failed_media (job_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_failed_tweets_updated_at
	ON failed_tweets (updated_at);
	`)
	return err
}

func (s *Store) EnsureConfig(ctx context.Context) error {
	var count int
	if err := s.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM app_config WHERE id = 1`); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	cfg := config.Default()
	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO app_config (
			id, download_dir, max_concurrency, proxy_url, auth_token, csrf_token,
			additional_cookies, auto_retry_failed, auto_follow_protected,
			include_nested_tweet_media,
			file_naming_mode, max_filename_length, storage_type,
			smb_host, smb_port, smb_share, smb_path, smb_domain, smb_username, smb_password,
			webdav_url, webdav_path, webdav_username, webdav_password, updated_at
		) VALUES (
			1, :download_dir, :max_concurrency, :proxy_url, :auth_token, :csrf_token,
			:additional_cookies, :auto_retry_failed, :auto_follow_protected,
			:include_nested_tweet_media,
			:file_naming_mode, :max_filename_length, :storage_type,
			:smb_host, :smb_port, :smb_share, :smb_path, :smb_domain, :smb_username, :smb_password,
			:webdav_url, :webdav_path, :webdav_username, :webdav_password, :updated_at
		)`, map[string]any{
		"download_dir":               cfg.DownloadDir,
		"max_concurrency":            cfg.MaxConcurrency,
		"proxy_url":                  cfg.ProxyURL,
		"auth_token":                 cfg.AuthToken,
		"csrf_token":                 cfg.CSRFToken,
		"additional_cookies":         cfg.AdditionalCookies,
		"auto_retry_failed":          cfg.AutoRetryFailed,
		"auto_follow_protected":      cfg.AutoFollowProtected,
		"include_nested_tweet_media": cfg.IncludeNestedTweetMedia,
		"file_naming_mode":           cfg.FileNamingMode,
		"max_filename_length":        cfg.MaxFilenameLength,
		"storage_type":               cfg.StorageType,
		"smb_host":                   cfg.SMBHost,
		"smb_port":                   cfg.SMBPort,
		"smb_share":                  cfg.SMBShare,
		"smb_path":                   cfg.SMBPath,
		"smb_domain":                 cfg.SMBDomain,
		"smb_username":               cfg.SMBUsername,
		"smb_password":               cfg.SMBPassword,
		"webdav_url":                 cfg.WebDAVURL,
		"webdav_path":                cfg.WebDAVPath,
		"webdav_username":            cfg.WebDAVUsername,
		"webdav_password":            cfg.WebDAVPassword,
		"updated_at":                 time.Now().UTC(),
	})
	return err
}

func (s *Store) GetConfig(ctx context.Context) (config.AppConfig, error) {
	cfg := config.AppConfig{}
	err := s.db.GetContext(ctx, &cfg, `
SELECT download_dir, max_concurrency, proxy_url, auth_token, csrf_token,
	additional_cookies, auto_retry_failed, auto_follow_protected,
	include_nested_tweet_media,
	file_naming_mode, max_filename_length, storage_type,
	smb_host, smb_port, smb_share, smb_path, smb_domain, smb_username, smb_password,
	webdav_url, webdav_path, webdav_username, webdav_password
FROM app_config WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return config.Default(), nil
	}
	return cfg.Normalized(), err
}

func (s *Store) UpdateConfig(ctx context.Context, cfg config.AppConfig) (config.AppConfig, error) {
	// 单连接 + 事务：把"读取当前值 → 合并占位符/还原 URL 凭据 → 写回"封装在一个事务里，
	// 避免两个并发 PUT /api/config 各自读到旧值再先后写回，导致其中一方的新凭据被静默覆盖。
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return config.AppConfig{}, err
	}
	defer tx.Rollback() // Commit 后为 no-op

	current := config.AppConfig{}
	if err := tx.GetContext(ctx, &current, `
SELECT download_dir, max_concurrency, proxy_url, auth_token, csrf_token,
	additional_cookies, auto_retry_failed, auto_follow_protected,
	include_nested_tweet_media,
	file_naming_mode, max_filename_length, storage_type,
	smb_host, smb_port, smb_share, smb_path, smb_domain, smb_username, smb_password,
	webdav_url, webdav_path, webdav_username, webdav_password
FROM app_config WHERE id = 1`); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return config.AppConfig{}, err
	}

	cfg = cfg.Normalized()
	if cfg.AuthToken == "" || cfg.AuthToken == config.SecretPlaceholder {
		cfg.AuthToken = current.AuthToken
	}
	if cfg.CSRFToken == "" || cfg.CSRFToken == config.SecretPlaceholder {
		cfg.CSRFToken = current.CSRFToken
	}
	if cfg.AdditionalCookies == "" || cfg.AdditionalCookies == config.SecretPlaceholder {
		cfg.AdditionalCookies = current.AdditionalCookies
	}
	cfg.SMBPassword = mergeStorageSecret(cfg.SMBPassword, current.SMBPassword, sameSMBTarget(cfg, current))
	cfg.WebDAVPassword = mergeStorageSecret(cfg.WebDAVPassword, current.WebDAVPassword, config.SameURLAuthority(cfg.WebDAVURL, current.WebDAVURL))
	// 还原 Redacted() 为展示而屏蔽的 URL 内嵌凭据，避免把占位符当真实代理/WebDAV 地址保存。
	cfg.ProxyURL = config.RestoreURLUserinfo(cfg.ProxyURL, current.ProxyURL)
	cfg.WebDAVURL = config.RestoreURLUserinfo(cfg.WebDAVURL, current.WebDAVURL)

	_, err = tx.NamedExecContext(ctx, `
	UPDATE app_config SET
		download_dir = :download_dir,
		max_concurrency = :max_concurrency,
		proxy_url = :proxy_url,
		auth_token = :auth_token,
			csrf_token = :csrf_token,
			additional_cookies = :additional_cookies,
			auto_retry_failed = :auto_retry_failed,
			auto_follow_protected = :auto_follow_protected,
			include_nested_tweet_media = :include_nested_tweet_media,
			file_naming_mode = :file_naming_mode,
			max_filename_length = :max_filename_length,
			storage_type = :storage_type,
			smb_host = :smb_host,
			smb_port = :smb_port,
			smb_share = :smb_share,
			smb_path = :smb_path,
			smb_domain = :smb_domain,
			smb_username = :smb_username,
			smb_password = :smb_password,
			webdav_url = :webdav_url,
			webdav_path = :webdav_path,
			webdav_username = :webdav_username,
			webdav_password = :webdav_password,
			updated_at = :updated_at
		WHERE id = 1`, map[string]any{
		"download_dir":               cfg.DownloadDir,
		"max_concurrency":            cfg.MaxConcurrency,
		"proxy_url":                  cfg.ProxyURL,
		"auth_token":                 cfg.AuthToken,
		"csrf_token":                 cfg.CSRFToken,
		"additional_cookies":         cfg.AdditionalCookies,
		"auto_retry_failed":          cfg.AutoRetryFailed,
		"auto_follow_protected":      cfg.AutoFollowProtected,
		"include_nested_tweet_media": cfg.IncludeNestedTweetMedia,
		"file_naming_mode":           cfg.FileNamingMode,
		"max_filename_length":        cfg.MaxFilenameLength,
		"storage_type":               cfg.StorageType,
		"smb_host":                   cfg.SMBHost,
		"smb_port":                   cfg.SMBPort,
		"smb_share":                  cfg.SMBShare,
		"smb_path":                   cfg.SMBPath,
		"smb_domain":                 cfg.SMBDomain,
		"smb_username":               cfg.SMBUsername,
		"smb_password":               cfg.SMBPassword,
		"webdav_url":                 cfg.WebDAVURL,
		"webdav_path":                cfg.WebDAVPath,
		"webdav_username":            cfg.WebDAVUsername,
		"webdav_password":            cfg.WebDAVPassword,
		"updated_at":                 time.Now().UTC(),
	})
	if err != nil {
		return config.AppConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return config.AppConfig{}, err
	}
	return cfg, nil
}

func mergeStorageSecret(submitted, stored string, targetUnchanged bool) string {
	if submitted == "" || submitted == config.SecretPlaceholder {
		if targetUnchanged {
			return stored
		}
		return ""
	}
	return submitted
}

func sameSMBTarget(a, b config.AppConfig) bool {
	return strings.EqualFold(strings.TrimSpace(a.SMBHost), strings.TrimSpace(b.SMBHost)) && a.SMBPort == b.SMBPort
}

func (s *Store) CreateJob(ctx context.Context, kind JobKind, input string, title string) (Job, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
INSERT INTO jobs (kind, status, input, title, progress, message, created_at, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?, ?)`, kind, JobPending, input, title, "排队中", now, now)
	if err != nil {
		return Job{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Job{}, err
	}
	return s.GetJob(ctx, id)
}

func (s *Store) GetJob(ctx context.Context, id int64) (Job, error) {
	job := Job{}
	err := s.db.GetContext(ctx, &job, `SELECT * FROM jobs WHERE id = ?`, id)
	return job, err
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	jobs := []Job{}
	err := s.db.SelectContext(ctx, &jobs, `SELECT * FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	return jobs, err
}

func (s *Store) ListJobsPage(ctx context.Context, limit int, offset int) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	jobs := []Job{}
	err := s.db.SelectContext(ctx, &jobs, `
SELECT * FROM jobs
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?`, limit, offset)
	return jobs, err
}

func (s *Store) JobStats(ctx context.Context) (JobStats, error) {
	stats := JobStats{}
	err := s.db.GetContext(ctx, &stats, `
SELECT
	COUNT(*) AS total,
	COALESCE(SUM(CASE WHEN status IN (?, ?, ?) THEN 1 ELSE 0 END), 0) AS active,
	COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS completed,
	COALESCE(SUM(CASE WHEN status IN (?, ?) THEN 1 ELSE 0 END), 0) AS failed
FROM jobs`,
		JobPending, JobResolving, JobDownloading, JobCompleted, JobFailed, JobCompletedWithErrors)
	return stats, err
}

func (s *Store) ClaimPendingJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > 64 {
		limit = 64
	}
	rows, err := s.db.QueryxContext(ctx, `
	UPDATE jobs SET
		status = ?,
		progress = ?,
		message = ?,
		error = '',
		updated_at = ?
	WHERE id IN (
		SELECT id FROM jobs
		WHERE status = ?
		ORDER BY created_at ASC
		LIMIT ?
	)
	RETURNING *`, JobResolving, 0.1, "正在解析", time.Now().UTC(), JobPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []Job{}
	for rows.Next() {
		var job Job
		if err := rows.StructScan(&job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) UpdateJob(ctx context.Context, job Job) error {
	job.UpdatedAt = time.Now().UTC()
	_, err := s.db.NamedExecContext(ctx, `
UPDATE jobs SET
	status = :status,
	title = :title,
	progress = :progress,
	message = :message,
	error = :error,
	updated_at = :updated_at
WHERE id = :id
  AND (status NOT IN ('completed', 'completed_with_errors', 'failed', 'canceled') OR status = :status)`, job)
	return err
}

func (s *Store) CancelJob(ctx context.Context, id int64) (Job, error) {
	// 单条条件 UPDATE：只把非终态任务改为 canceled，避免 GetJob→UpdateJob 两步之间与
	// worker 的终态保存竞争（worker 把任务标为 completed 后，这里不会再覆盖为 canceled）。
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs SET status = ?, message = ?, progress = 1, error = '', updated_at = ?
WHERE id = ? AND status NOT IN (?, ?, ?, ?)`,
		JobCanceled, "已取消", time.Now().UTC(), id, JobCompleted, JobCompletedWithErrors, JobFailed, JobCanceled)
	if err != nil {
		return Job{}, err
	}
	// 无论是否更新到行（已是终态或不存在），都返回当前状态。
	return s.GetJob(ctx, id)
}

func (s *Store) RetryJob(ctx context.Context, id int64) (Job, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	switch job.Status {
	case JobPending, JobResolving, JobDownloading:
		return Job{}, fmt.Errorf("任务仍在运行或排队中，不能重试")
	}
	return s.CreateJob(ctx, job.Kind, job.Input, job.Title)
}

func (s *Store) RequeueInterruptedJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryxContext(ctx, `
	UPDATE jobs SET
		status = ?,
		progress = 0,
		message = ?,
		error = '',
		updated_at = ?
	WHERE status IN (?, ?)
	RETURNING *`,
		JobPending, "上次运行中断，等待恢复", time.Now().UTC(), JobResolving, JobDownloading)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []Job{}
	for rows.Next() {
		var job Job
		if err := rows.StructScan(&job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) CreateJobs(ctx context.Context, drafts []JobDraft) ([]Job, error) {
	if len(drafts) == 0 {
		return []Job{}, nil
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	jobs := make([]Job, 0, len(drafts))
	for _, draft := range drafts {
		result, err := tx.ExecContext(ctx, `
	INSERT INTO jobs (kind, status, input, title, progress, message, created_at, updated_at)
	VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
			draft.Kind, JobPending, draft.Input, draft.Title, "排队中", now, now)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, Job{
			ID:        id,
			Kind:      draft.Kind,
			Status:    JobPending,
			Input:     draft.Input,
			Title:     draft.Title,
			Message:   "排队中",
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) CreateArchiveSchedule(ctx context.Context, schedule ArchiveSchedule) (ArchiveSchedule, error) {
	now := time.Now().UTC()
	schedule, err := prepareArchiveScheduleForSave(schedule)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	schedule.NextRunAt = nextArchiveScheduleRun(now, schedule.IntervalMinutes)
	result, err := s.db.ExecContext(ctx, `
INSERT INTO archive_schedules (
	name, enabled, interval_minutes, items_json, next_run_at, last_job_ids, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		schedule.Name, schedule.Enabled, schedule.IntervalMinutes, schedule.ItemsJSON,
		schedule.NextRunAt, "[]", now, now)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return ArchiveSchedule{}, err
	}
	return s.GetArchiveSchedule(ctx, id)
}

func (s *Store) UpdateArchiveSchedule(ctx context.Context, schedule ArchiveSchedule) (ArchiveSchedule, error) {
	current, err := s.GetArchiveSchedule(ctx, schedule.ID)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	schedule, err = prepareArchiveScheduleForSave(schedule)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	now := time.Now().UTC()
	nextRunAt := current.NextRunAt
	if schedule.Enabled && (!current.Enabled || current.IntervalMinutes != schedule.IntervalMinutes) {
		nextRunAt = nextArchiveScheduleRun(now, schedule.IntervalMinutes)
	}
	if nextRunAt.IsZero() {
		nextRunAt = nextArchiveScheduleRun(now, schedule.IntervalMinutes)
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE archive_schedules SET
	name = ?,
	enabled = ?,
	interval_minutes = ?,
	items_json = ?,
	next_run_at = ?,
	updated_at = ?
WHERE id = ?`,
		schedule.Name, schedule.Enabled, schedule.IntervalMinutes, schedule.ItemsJSON,
		nextRunAt, now, schedule.ID)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	return s.GetArchiveSchedule(ctx, schedule.ID)
}

func (s *Store) GetArchiveSchedule(ctx context.Context, id int64) (ArchiveSchedule, error) {
	schedule := ArchiveSchedule{}
	err := s.db.GetContext(ctx, &schedule, `SELECT * FROM archive_schedules WHERE id = ?`, id)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	return hydrateArchiveSchedule(schedule)
}

func (s *Store) ListArchiveSchedules(ctx context.Context) ([]ArchiveSchedule, error) {
	items := []ArchiveSchedule{}
	if err := s.db.SelectContext(ctx, &items, `
SELECT * FROM archive_schedules
ORDER BY enabled DESC, next_run_at ASC, updated_at DESC`); err != nil {
		return nil, err
	}
	return hydrateArchiveSchedules(items)
}

func (s *Store) ListDueArchiveSchedules(ctx context.Context, now time.Time, limit int) ([]ArchiveSchedule, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	items := []ArchiveSchedule{}
	if err := s.db.SelectContext(ctx, &items, `
SELECT * FROM archive_schedules
WHERE enabled = 1 AND next_run_at <= ?
ORDER BY next_run_at ASC
LIMIT ?`, now.UTC(), limit); err != nil {
		return nil, err
	}
	return hydrateArchiveSchedules(items)
}

func (s *Store) DeleteArchiveSchedule(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM archive_schedules WHERE id = ?`, id)
	return err
}

func (s *Store) RescheduleArchiveSchedule(ctx context.Context, id int64, nextRunAt time.Time) (ArchiveSchedule, error) {
	_, err := s.db.ExecContext(ctx, `
UPDATE archive_schedules SET next_run_at = ?, updated_at = ? WHERE id = ?`,
		nextRunAt.UTC(), time.Now().UTC(), id)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	return s.GetArchiveSchedule(ctx, id)
}

func (s *Store) HasActiveJobs(ctx context.Context, ids []int64) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	query, args, err := sqlx.In(`
SELECT COUNT(*) FROM jobs
WHERE id IN (?) AND status IN (?, ?, ?)`, ids, JobPending, JobResolving, JobDownloading)
	if err != nil {
		return false, err
	}
	var count int
	err = s.db.GetContext(ctx, &count, s.db.Rebind(query), args...)
	return count > 0, err
}

func (s *Store) CreateJobsForArchiveSchedule(ctx context.Context, schedule ArchiveSchedule, runAt time.Time) ([]Job, error) {
	schedule, err := prepareArchiveScheduleForSave(schedule)
	if err != nil {
		return nil, err
	}
	runAt = runAt.UTC()
	claimNextRunAt := schedule.NextRunAt.UTC()
	nextRunAt := nextArchiveScheduleRun(runAt, schedule.IntervalMinutes)
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	claimResult, err := tx.ExecContext(ctx, `
UPDATE archive_schedules SET
	last_run_at = ?,
	next_run_at = ?,
	last_job_ids = '[]',
	updated_at = ?
WHERE id = ? AND next_run_at = ?`,
		runAt, nextRunAt, runAt, schedule.ID, claimNextRunAt)
	if err != nil {
		return nil, err
	}
	claimed, err := claimResult.RowsAffected()
	if err != nil {
		return nil, err
	}
	if claimed == 0 {
		return nil, ErrArchiveScheduleAlreadyClaimed
	}

	jobs := make([]Job, 0, len(schedule.Items))
	jobIDs := make([]int64, 0, len(schedule.Items))
	for _, item := range schedule.Items {
		result, err := tx.ExecContext(ctx, `
INSERT INTO jobs (kind, status, input, title, progress, message, created_at, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
			item.Kind, JobPending, item.Input, item.Title, "定时归档排队中", runAt, runAt)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		jobIDs = append(jobIDs, id)
		jobs = append(jobs, Job{
			ID:        id,
			Kind:      item.Kind,
			Status:    JobPending,
			Input:     item.Input,
			Title:     item.Title,
			Message:   "定时归档排队中",
			CreatedAt: runAt,
			UpdatedAt: runAt,
		})
	}
	lastJobIDs, err := json.Marshal(jobIDs)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE archive_schedules SET last_job_ids = ?, updated_at = ? WHERE id = ?`,
		string(lastJobIDs), runAt, schedule.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) CreateDownload(ctx context.Context, record DownloadRecord) (DownloadRecord, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(record.TweetID) != "" {
		err := s.db.GetContext(ctx, &record, `
	INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(tweet_id, media_url) WHERE tweet_id <> '' DO UPDATE SET
		job_id = excluded.job_id,
		file_path = excluded.file_path,
		bytes = excluded.bytes,
		created_at = excluded.created_at
	RETURNING *`,
			record.JobID, record.TweetID, record.MediaURL, record.FilePath, record.Bytes, now)
		return record, err
	}
	result, err := s.db.ExecContext(ctx, `
	INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at)
	VALUES (?, ?, ?, ?, ?, ?)`, record.JobID, record.TweetID, record.MediaURL, record.FilePath, record.Bytes, now)
	if err != nil {
		return DownloadRecord{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return DownloadRecord{}, err
	}
	record.ID = id
	record.CreatedAt = now
	return record, nil
}

func (s *Store) ListDownloads(ctx context.Context, limit int) ([]DownloadRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	items := []DownloadRecord{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM downloads ORDER BY created_at DESC LIMIT ?`, limit)
	return items, err
}

func (s *Store) ListDownloadsForJobs(ctx context.Context, jobIDs []int64) ([]DownloadRecord, error) {
	if len(jobIDs) == 0 {
		return []DownloadRecord{}, nil
	}
	query, args, err := sqlx.In(`
SELECT * FROM downloads
WHERE job_id IN (?)
ORDER BY job_id DESC, created_at DESC`, jobIDs)
	if err != nil {
		return nil, err
	}
	items := []DownloadRecord{}
	err = s.db.SelectContext(ctx, &items, s.db.Rebind(query), args...)
	return items, err
}

func (s *Store) GetDownloadByTweetMedia(ctx context.Context, tweetID string, mediaURL string) (*DownloadRecord, error) {
	record := DownloadRecord{}
	err := s.db.GetContext(ctx, &record, `SELECT * FROM downloads WHERE tweet_id = ? AND media_url = ?`, tweetID, mediaURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *Store) CreateFailedMedia(ctx context.Context, failed FailedMedia) (FailedMedia, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
INSERT INTO failed_media (job_id, media_url, error, created_at)
VALUES (?, ?, ?, ?)`, failed.JobID, failed.MediaURL, failed.Error, now)
	if err != nil {
		return FailedMedia{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return FailedMedia{}, err
	}
	failed.ID = id
	failed.CreatedAt = now
	return failed, nil
}

func (s *Store) ListFailedMedia(ctx context.Context, limit int) ([]FailedMedia, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	items := []FailedMedia{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM failed_media ORDER BY created_at DESC LIMIT ?`, limit)
	return items, err
}

func (s *Store) ListFailedMediaForJobs(ctx context.Context, jobIDs []int64) ([]FailedMedia, error) {
	if len(jobIDs) == 0 {
		return []FailedMedia{}, nil
	}
	query, args, err := sqlx.In(`
SELECT * FROM failed_media
WHERE job_id IN (?)
ORDER BY job_id DESC, created_at DESC`, jobIDs)
	if err != nil {
		return nil, err
	}
	items := []FailedMedia{}
	err = s.db.SelectContext(ctx, &items, s.db.Rebind(query), args...)
	return items, err
}

func (s *Store) UpsertUser(ctx context.Context, user User) (User, error) {
	now := time.Now().UTC()
	var existing User
	err := s.db.GetContext(ctx, &existing, `SELECT * FROM users WHERE id = ?`, user.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	// 仅当已存在的用户改名时才记录"旧"用户名。新用户没有旧名可记；且 INSERT 必须在
	// users UPSERT 之后，否则 user_id 的外键尚未存在（foreign_keys=ON）会触发约束违例。
	nameChanged := !errors.Is(err, sql.ErrNoRows) && (existing.Name != user.Name || existing.ScreenName != user.ScreenName)

	_, err = s.db.ExecContext(ctx, `
INSERT INTO users (id, screen_name, name, protected, friends_count, media_count, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	screen_name = excluded.screen_name,
	name = excluded.name,
	protected = excluded.protected,
	friends_count = excluded.friends_count,
	media_count = excluded.media_count,
	updated_at = excluded.updated_at`,
		user.ID, user.ScreenName, user.Name, user.Protected, user.FriendsCount, user.MediaCount, now)
	if err != nil {
		return User{}, err
	}
	if nameChanged {
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO user_previous_names (user_id, screen_name, name, recorded_at)
VALUES (?, ?, ?, ?)`, user.ID, existing.ScreenName, existing.Name, now); err != nil {
			return User{}, err
		}
	}
	return s.GetUser(ctx, user.ID)
}

func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	user := User{}
	err := s.db.GetContext(ctx, &user, `SELECT * FROM users WHERE id = ?`, id)
	return user, err
}

func (s *Store) LocateUserEntity(ctx context.Context, userID string, parentDir string) (*UserEntity, error) {
	parentDir, err := normalizeParentDir(parentDir)
	if err != nil {
		return nil, err
	}
	entity := UserEntity{}
	err = s.db.GetContext(ctx, &entity, `SELECT * FROM user_entities WHERE user_id = ? AND parent_dir = ?`, userID, parentDir)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entity, nil
}

func (s *Store) EnsureUserEntity(ctx context.Context, userID string, parentDir string, name string) (UserEntity, error) {
	parentDir, err := normalizeParentDir(parentDir)
	if err != nil {
		return UserEntity{}, err
	}
	now := time.Now().UTC()
	entity := UserEntity{}
	err = s.db.GetContext(ctx, &entity, `
INSERT INTO user_entities (user_id, name, parent_dir, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(user_id, parent_dir) DO UPDATE SET
	name = excluded.name,
	updated_at = excluded.updated_at
RETURNING *`, userID, name, parentDir, now)
	return entity, err
}

func (s *Store) GetUserEntity(ctx context.Context, id int64) (UserEntity, error) {
	entity := UserEntity{}
	err := s.db.GetContext(ctx, &entity, `SELECT * FROM user_entities WHERE id = ?`, id)
	return entity, err
}

func (s *Store) UpdateUserEntityMediaCount(ctx context.Context, id int64, mediaCount int) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE user_entities SET media_count = ?, updated_at = ? WHERE id = ?`,
		mediaCount, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateUserEntityLastSeenTweet(ctx context.Context, id int64, tweetID string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE user_entities SET last_seen_tweet_id = ?, updated_at = ? WHERE id = ?`,
		tweetID, time.Now().UTC(), id)
	return err
}

func (s *Store) UpsertList(ctx context.Context, list List) (List, error) {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO twitter_lists (id, name, owner_user_id, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	name = excluded.name,
	owner_user_id = excluded.owner_user_id,
	updated_at = excluded.updated_at`,
		list.ID, list.Name, list.OwnerUserID, now)
	if err != nil {
		return List{}, err
	}
	return s.GetList(ctx, list.ID)
}

func (s *Store) GetList(ctx context.Context, id string) (List, error) {
	list := List{}
	err := s.db.GetContext(ctx, &list, `SELECT * FROM twitter_lists WHERE id = ?`, id)
	return list, err
}

func (s *Store) LocateListEntity(ctx context.Context, listID string, parentDir string) (*ListEntity, error) {
	parentDir, err := normalizeParentDir(parentDir)
	if err != nil {
		return nil, err
	}
	entity := ListEntity{}
	err = s.db.GetContext(ctx, &entity, `SELECT * FROM list_entities WHERE list_id = ? AND parent_dir = ?`, listID, parentDir)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entity, nil
}

func (s *Store) EnsureListEntity(ctx context.Context, listID string, parentDir string, name string) (ListEntity, error) {
	parentDir, err := normalizeParentDir(parentDir)
	if err != nil {
		return ListEntity{}, err
	}
	now := time.Now().UTC()
	entity := ListEntity{}
	err = s.db.GetContext(ctx, &entity, `
INSERT INTO list_entities (list_id, name, parent_dir, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(list_id, parent_dir) DO UPDATE SET
	name = excluded.name,
	updated_at = excluded.updated_at
RETURNING *`, listID, name, parentDir, now)
	return entity, err
}

func (s *Store) GetListEntity(ctx context.Context, id int64) (ListEntity, error) {
	entity := ListEntity{}
	err := s.db.GetContext(ctx, &entity, `SELECT * FROM list_entities WHERE id = ?`, id)
	return entity, err
}

func (s *Store) EnsureUserLink(ctx context.Context, userID string, listEntityID int64, name string) (UserLink, error) {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO user_links (user_id, name, list_entity_id, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(user_id, list_entity_id) DO UPDATE SET
	name = excluded.name,
	updated_at = excluded.updated_at`,
		userID, name, listEntityID, now)
	if err != nil {
		return UserLink{}, err
	}
	link := UserLink{}
	err = s.db.GetContext(ctx, &link, `SELECT * FROM user_links WHERE user_id = ? AND list_entity_id = ?`, userID, listEntityID)
	return link, err
}

func (s *Store) GetUserLinks(ctx context.Context, userID string) ([]UserLink, error) {
	items := []UserLink{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM user_links WHERE user_id = ?`, userID)
	return items, err
}

func (s *Store) GetUserLinkTargets(ctx context.Context, userID string) ([]UserLinkTarget, error) {
	items := []UserLinkTarget{}
	err := s.db.SelectContext(ctx, &items, `
SELECT
	ul.id, ul.user_id, ul.name, ul.list_entity_id, ul.updated_at,
	le.list_id AS list_id,
	le.name AS list_name,
	le.parent_dir AS list_parent_dir
FROM user_links ul
JOIN list_entities le ON le.id = ul.list_entity_id
WHERE ul.user_id = ?
ORDER BY le.parent_dir, le.name, ul.name`, userID)
	return items, err
}

func (s *Store) HasDownload(ctx context.Context, tweetID string, mediaURL string) (bool, error) {
	var count int
	err := s.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM downloads WHERE tweet_id = ? AND media_url = ?`, tweetID, mediaURL)
	return count > 0, err
}

func (s *Store) CreateFailedTweet(ctx context.Context, failed FailedTweet) (FailedTweet, error) {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO failed_tweets (job_id, entity_id, tweet_id, payload, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(entity_id, tweet_id) DO UPDATE SET
	job_id = excluded.job_id,
	payload = excluded.payload,
	error = excluded.error,
	updated_at = excluded.updated_at`,
		failed.JobID, failed.EntityID, failed.TweetID, failed.Payload, failed.Error, now, now)
	if err != nil {
		return FailedTweet{}, err
	}
	err = s.db.GetContext(ctx, &failed, `
SELECT * FROM failed_tweets WHERE entity_id = ? AND tweet_id = ?`, failed.EntityID, failed.TweetID)
	return failed, err
}

func (s *Store) ListFailedTweets(ctx context.Context, limit int) ([]FailedTweet, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	items := []FailedTweet{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM failed_tweets ORDER BY updated_at ASC LIMIT ?`, limit)
	return items, err
}

func (s *Store) ListFailedTweetViews(ctx context.Context, limit int) ([]FailedTweetView, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	return s.ListFailedTweetViewsPage(ctx, limit, 0)
}

func (s *Store) ListFailedTweetViewsPage(ctx context.Context, limit int, offset int) ([]FailedTweetView, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	items := []FailedTweetView{}
	err := s.db.SelectContext(ctx, &items, `
SELECT
	ft.id, ft.job_id, ft.entity_id, ft.tweet_id, ft.payload, ft.error, ft.created_at, ft.updated_at,
	COALESCE(j.title, '') AS job_title,
	COALESCE(ue.name, '') AS entity_name,
	COALESCE(ue.parent_dir, '') AS entity_parent_dir,
	COALESCE(u.id, '') AS user_id,
	COALESCE(u.screen_name, '') AS user_screen_name,
	COALESCE(u.name, '') AS user_name
FROM failed_tweets ft
LEFT JOIN jobs j ON j.id = ft.job_id
LEFT JOIN user_entities ue ON ue.id = ft.entity_id
LEFT JOIN users u ON u.id = ue.user_id
ORDER BY ft.updated_at ASC
LIMIT ? OFFSET ?`, limit, offset)
	return items, err
}

func (s *Store) CountFailedTweets(ctx context.Context) (int, error) {
	var count int
	err := s.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM failed_tweets`)
	return count, err
}

func (s *Store) DeleteFailedTweet(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM failed_tweets WHERE id = ?`, id)
	return err
}

func (s *Store) DeleteAllFailedTweets(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM failed_tweets`)
	return err
}

func prepareArchiveScheduleForSave(schedule ArchiveSchedule) (ArchiveSchedule, error) {
	schedule.Name = strings.TrimSpace(schedule.Name)
	if schedule.Name == "" {
		schedule.Name = "批量归档计划"
	}
	if schedule.IntervalMinutes < MinArchiveScheduleIntervalMinutes {
		return ArchiveSchedule{}, fmt.Errorf("定时任务间隔不能小于 %d 分钟", MinArchiveScheduleIntervalMinutes)
	}
	if schedule.IntervalMinutes > MaxArchiveScheduleIntervalMinutes {
		return ArchiveSchedule{}, fmt.Errorf("定时任务间隔不能超过 %d 分钟", MaxArchiveScheduleIntervalMinutes)
	}
	if len(schedule.Items) == 0 {
		return ArchiveSchedule{}, fmt.Errorf("定时任务目标不能为空")
	}
	if len(schedule.Items) > MaxArchiveScheduleItems {
		return ArchiveSchedule{}, fmt.Errorf("定时任务一次最多包含 %d 个目标", MaxArchiveScheduleItems)
	}

	items := make([]ArchiveScheduleItem, 0, len(schedule.Items))
	seen := map[string]struct{}{}
	for _, item := range schedule.Items {
		item.Input = strings.TrimSpace(item.Input)
		item.Title = strings.TrimSpace(item.Title)
		if item.Input == "" {
			return ArchiveSchedule{}, fmt.Errorf("定时任务目标不能为空")
		}
		switch item.Kind {
		case JobKindUser, JobKindList, JobKindFollowing:
		default:
			return ArchiveSchedule{}, fmt.Errorf("定时任务不支持的目标类型: %s", item.Kind)
		}
		key := string(item.Kind) + "\x00" + strings.ToLower(item.Input)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if item.Title == "" {
			item.Title = defaultArchiveScheduleItemTitle(item)
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return ArchiveSchedule{}, fmt.Errorf("定时任务目标不能为空")
	}
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	schedule.Items = items
	schedule.ItemsJSON = string(itemsJSON)
	return schedule, nil
}

func hydrateArchiveSchedules(items []ArchiveSchedule) ([]ArchiveSchedule, error) {
	for index := range items {
		item, err := hydrateArchiveSchedule(items[index])
		if err != nil {
			return nil, err
		}
		items[index] = item
	}
	return items, nil
}

func hydrateArchiveSchedule(schedule ArchiveSchedule) (ArchiveSchedule, error) {
	if strings.TrimSpace(schedule.ItemsJSON) != "" {
		if err := json.Unmarshal([]byte(schedule.ItemsJSON), &schedule.Items); err != nil {
			return ArchiveSchedule{}, err
		}
	}
	if strings.TrimSpace(schedule.LastJobIDsJSON) != "" {
		if err := json.Unmarshal([]byte(schedule.LastJobIDsJSON), &schedule.LastJobIDs); err != nil {
			return ArchiveSchedule{}, err
		}
	}
	if schedule.Items == nil {
		schedule.Items = []ArchiveScheduleItem{}
	}
	if schedule.LastJobIDs == nil {
		schedule.LastJobIDs = []int64{}
	}
	return schedule, nil
}

func defaultArchiveScheduleItemTitle(item ArchiveScheduleItem) string {
	switch item.Kind {
	case JobKindUser:
		return fmt.Sprintf("用户 %s", displayArchiveScheduleUserInput(item.Input))
	case JobKindList:
		return fmt.Sprintf("列表 %s", item.Input)
	case JobKindFollowing:
		return fmt.Sprintf("关注 %s", displayArchiveScheduleUserInput(item.Input))
	default:
		return item.Input
	}
}

func displayArchiveScheduleUserInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "" || strings.HasPrefix(input, "@") {
		return input
	}
	if _, err := strconv.ParseUint(input, 10, 64); err == nil {
		return input
	}
	return "@" + input
}

func nextArchiveScheduleRun(now time.Time, intervalMinutes int) time.Time {
	return now.UTC().Add(time.Duration(intervalMinutes) * time.Minute)
}

func normalizeParentDir(parentDir string) (string, error) {
	parentDir = strings.TrimSpace(parentDir)
	if strings.Contains(parentDir, "://") {
		return strings.TrimRight(parentDir, "/"), nil
	}
	return filepath.Abs(parentDir)
}
