package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sqlx.DB
}

func Open(path string) (*Store, error) {
	db, err := sqlx.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
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
	auto_retry_failed BOOLEAN NOT NULL DEFAULT 1,
	keep_original_urls BOOLEAN NOT NULL DEFAULT 1,
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
`
	_, err := s.db.Exec(schema)
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
	auto_retry_failed, keep_original_urls, updated_at
) VALUES (
	1, :download_dir, :max_concurrency, :proxy_url, :auth_token, :csrf_token,
	:auto_retry_failed, :keep_original_urls, :updated_at
)`, map[string]any{
		"download_dir":       cfg.DownloadDir,
		"max_concurrency":    cfg.MaxConcurrency,
		"proxy_url":          cfg.ProxyURL,
		"auth_token":         cfg.AuthToken,
		"csrf_token":         cfg.CSRFToken,
		"auto_retry_failed":  cfg.AutoRetryFailed,
		"keep_original_urls": cfg.KeepOriginalURLs,
		"updated_at":         time.Now().UTC(),
	})
	return err
}

func (s *Store) GetConfig(ctx context.Context) (config.AppConfig, error) {
	cfg := config.AppConfig{}
	err := s.db.GetContext(ctx, &cfg, `SELECT download_dir, max_concurrency, proxy_url, auth_token, csrf_token, auto_retry_failed, keep_original_urls FROM app_config WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return config.Default(), nil
	}
	return cfg.Normalized(), err
}

func (s *Store) UpdateConfig(ctx context.Context, cfg config.AppConfig) (config.AppConfig, error) {
	current, err := s.GetConfig(ctx)
	if err != nil {
		return config.AppConfig{}, err
	}
	cfg = cfg.Normalized()
	if cfg.AuthToken == "" || cfg.AuthToken == "********" {
		cfg.AuthToken = current.AuthToken
	}
	if cfg.CSRFToken == "" || cfg.CSRFToken == "********" {
		cfg.CSRFToken = current.CSRFToken
	}
	_, err = s.db.NamedExecContext(ctx, `
UPDATE app_config SET
	download_dir = :download_dir,
	max_concurrency = :max_concurrency,
	proxy_url = :proxy_url,
	auth_token = :auth_token,
	csrf_token = :csrf_token,
	auto_retry_failed = :auto_retry_failed,
	keep_original_urls = :keep_original_urls,
	updated_at = :updated_at
WHERE id = 1`, map[string]any{
		"download_dir":       cfg.DownloadDir,
		"max_concurrency":    cfg.MaxConcurrency,
		"proxy_url":          cfg.ProxyURL,
		"auth_token":         cfg.AuthToken,
		"csrf_token":         cfg.CSRFToken,
		"auto_retry_failed":  cfg.AutoRetryFailed,
		"keep_original_urls": cfg.KeepOriginalURLs,
		"updated_at":         time.Now().UTC(),
	})
	return cfg, err
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

func (s *Store) ClaimPendingJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 1
	}
	jobs := []Job{}
	err := s.db.SelectContext(ctx, &jobs, `SELECT * FROM jobs WHERE status = ? ORDER BY created_at ASC LIMIT ?`, JobPending, limit)
	return jobs, err
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
WHERE id = :id`, job)
	return err
}

func (s *Store) CancelJob(ctx context.Context, id int64) (Job, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.Status == JobCompleted || job.Status == JobFailed || job.Status == JobCanceled {
		return job, nil
	}
	job.Status = JobCanceled
	job.Message = "已取消"
	job.Progress = 1
	if err := s.UpdateJob(ctx, job); err != nil {
		return Job{}, err
	}
	return s.GetJob(ctx, id)
}

func (s *Store) RetryJob(ctx context.Context, id int64) (Job, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	job.Status = JobPending
	job.Progress = 0
	job.Message = "等待重试"
	job.Error = ""
	if err := s.UpdateJob(ctx, job); err != nil {
		return Job{}, err
	}
	return s.GetJob(ctx, id)
}

func (s *Store) CreateDownload(ctx context.Context, record DownloadRecord) (DownloadRecord, error) {
	now := time.Now().UTC()
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
