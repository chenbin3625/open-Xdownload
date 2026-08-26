package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sqlx.DB

	configMu     sync.RWMutex
	cachedStored *config.AppConfig
}

const sqliteOpenOptions = "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-16000)&_pragma=mmap_size(268435456)"

const (
	MinArchiveScheduleIntervalMinutes = 5
	MaxArchiveScheduleIntervalMinutes = 60 * 24 * 30
	MaxArchiveScheduleItems           = 200
)

var ErrArchiveScheduleAlreadyClaimed = errors.New("定时归档计划已被其他运行领取")

func Open(path string) (*Store, error) {
	db, err := sqlx.Open("sqlite", path+sqliteOpenOptions)
	if err != nil {
		return nil, err
	}
	// WAL 允许并发读者 + 单写者。保留少量连接，让 HTTP 读（任务列表/仪表盘）
	// 与 worker 进度写入重叠；busy_timeout(5000) 在写锁冲突时等待而不是 SQLITE_BUSY。
	// 连接数不宜过大：每条连接都会占一份 mmap 窗口。
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
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
	if _, err := store.GetStoredConfig(context.Background()); err != nil {
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

	CREATE TABLE IF NOT EXISTS unavailable_media (
		media_url TEXT PRIMARY KEY,
		tweet_id TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
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

CREATE TABLE IF NOT EXISTS schema_migrations (
	name TEXT PRIMARY KEY,
	applied_at DATETIME NOT NULL
);

CREATE TRIGGER IF NOT EXISTS trg_users_name_history
AFTER UPDATE OF screen_name, name ON users
WHEN OLD.screen_name <> NEW.screen_name OR OLD.name <> NEW.name
BEGIN
	INSERT INTO user_previous_names (user_id, screen_name, name, recorded_at)
	VALUES (OLD.id, OLD.screen_name, OLD.name, NEW.updated_at);
END;
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	if err := s.addMissingColumns(); err != nil {
		return err
	}
	// 一次性数据迁移：首次成功执行后记录到 schema_migrations，后续启动直接跳过，
	// 避免每次冷启动都对 downloads 全表扫描/分组。新写入的 media_url 已在入口规范化，
	// 不会再产生需要清理的重复，故这些迁移确为一次性。
	if err := s.runMigrationOnce("normalize_downloads_media_url", s.normalizeDownloadsMediaURL); err != nil {
		return err
	}
	if err := s.runMigrationOnce("deduplicate_downloads", s.deduplicateDownloads); err != nil {
		return err
	}
	if err := s.runMigrationOnce("deduplicate_downloads_media_url_only", s.deduplicateDownloadsMediaURLOnly); err != nil {
		return err
	}
	if err := s.ensureDashboardCounters(); err != nil {
		return err
	}
	return s.addMissingIndexes()
}

type migrationExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Get(dest any, query string, args ...any) error
	Select(dest any, query string, args ...any) error
}

// runMigrationOnce 在事务中执行一次性迁移并登记。进程如果在迁移中途崩溃，
// 数据变更和 schema_migrations 记录会一起回滚，避免下次启动误以为迁移已完成。
func (s *Store) runMigrationOnce(name string, fn func(migrationExecutor) error) error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	var count int
	if err := tx.Get(&count, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name); err != nil {
		return rollback(err)
	}
	if count > 0 {
		return tx.Commit()
	}
	if err := fn(tx); err != nil {
		return rollback(err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().UTC()); err != nil {
		return rollback(err)
	}
	return tx.Commit()
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

func (s *Store) deduplicateDownloads(exec migrationExecutor) error {
	_, err := exec.Exec(`
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

// deduplicateDownloadsMediaURLOnly 清理 tweet_id 为空（直接媒体 URL 任务）的历史重复行，
// 为随后 addMissingIndexes 创建的 (media_url) WHERE tweet_id = ” 唯一索引扫清障碍。
func (s *Store) deduplicateDownloadsMediaURLOnly(exec migrationExecutor) error {
	_, err := exec.Exec(`
	DELETE FROM downloads
	WHERE tweet_id = ''
	  AND media_url <> ''
	  AND id NOT IN (
		SELECT MIN(id)
		FROM downloads
		WHERE tweet_id = '' AND media_url <> ''
		GROUP BY media_url
	  )`)
	return err
}

// normalizeDownloadsMediaURL 规范化历史 downloads 记录中的 media_url（去掉 ?tag= 等易变参数），
// 使旧记录与规范化后的去重键一致，随后由 deduplicateDownloads 清理因规范化产生的重复行。
// 幂等：无待规范化的行时直接返回，不动索引。
func (s *Store) normalizeDownloadsMediaURL(exec migrationExecutor) error {
	type downloadRow struct {
		ID       int64  `db:"id"`
		MediaURL string `db:"media_url"`
	}
	// 仅取可能含 ?tag= 的候选行，避免把整个 downloads 表载入内存。规范化后的 URL 不再含
	// tag=，故干净库上该查询返回 0 行；Go 侧再用 NormalizeMediaURL 比对确认确需变更。
	var rows []downloadRow
	if err := exec.Select(&rows, `SELECT id, media_url FROM downloads WHERE tweet_id <> '' AND media_url <> '' AND media_url LIKE '%tag=%'`); err != nil {
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
	if _, err := exec.Exec(`DROP INDEX IF EXISTS idx_downloads_tweet_media_unique`); err != nil {
		return err
	}
	for _, r := range pending {
		if _, err := exec.Exec(`UPDATE downloads SET media_url = ? WHERE id = ?`, downloader.NormalizeMediaURL(r.MediaURL), r.ID); err != nil {
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
	CREATE UNIQUE INDEX IF NOT EXISTS idx_downloads_media_url_unique
	ON downloads (media_url)
	WHERE tweet_id = '' AND media_url <> '';
	CREATE INDEX IF NOT EXISTS idx_downloads_job_created_at
	ON downloads (job_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_failed_media_job_created_at
	ON failed_media (job_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_failed_tweets_updated_at
	ON failed_tweets (updated_at);
	CREATE INDEX IF NOT EXISTS idx_user_entities_parent_user
	ON user_entities (parent_dir, user_id);
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

const storedConfigQuery = `
SELECT download_dir, max_concurrency, proxy_url, auth_token, csrf_token,
	additional_cookies, auto_retry_failed, auto_follow_protected,
	include_nested_tweet_media,
	file_naming_mode, max_filename_length, storage_type,
	smb_host, smb_port, smb_share, smb_path, smb_domain, smb_username, smb_password,
	webdav_url, webdav_path, webdav_username, webdav_password
FROM app_config WHERE id = 1`

func (s *Store) rememberStoredConfig(cfg config.AppConfig) {
	copied := cfg
	s.configMu.Lock()
	s.cachedStored = &copied
	s.configMu.Unlock()
}

// GetStoredConfig 返回数据库中的配置，不套用环境变量。供占位符合并与 UpdateConfig 写库使用。
func (s *Store) GetStoredConfig(ctx context.Context) (config.AppConfig, error) {
	s.configMu.RLock()
	if s.cachedStored != nil {
		cfg := *s.cachedStored
		s.configMu.RUnlock()
		return cfg, nil
	}
	s.configMu.RUnlock()

	cfg := config.AppConfig{}
	err := s.db.GetContext(ctx, &cfg, storedConfigQuery)
	if errors.Is(err, sql.ErrNoRows) {
		cfg = config.Default()
		s.rememberStoredConfig(cfg)
		return cfg, nil
	}
	if err != nil {
		return config.AppConfig{}, err
	}
	cfg = cfg.Normalized()
	s.rememberStoredConfig(cfg)
	return cfg, nil
}

func (s *Store) GetConfig(ctx context.Context) (config.AppConfig, error) {
	cfg, err := s.GetStoredConfig(ctx)
	if err != nil {
		return config.AppConfig{}, err
	}
	return config.ApplyEnvOverrides(cfg), nil
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
	if err := tx.GetContext(ctx, &current, storedConfigQuery); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return config.AppConfig{}, err
	}
	current = current.Normalized()

	cfg = cfg.Normalized()
	if cfg.AuthToken == "" || cfg.AuthToken == config.SecretPlaceholder {
		cfg.AuthToken = current.AuthToken
	}
	if cfg.CSRFToken == "" || cfg.CSRFToken == config.SecretPlaceholder {
		cfg.CSRFToken = current.CSRFToken
	}
	cfg.AdditionalCookies = config.RestoreAdditionalCookies(cfg.AdditionalCookies, current.AdditionalCookies)
	cfg.SMBPassword = config.MergeStorageSecret(cfg.SMBPassword, current.SMBPassword, config.SameSMBTarget(cfg, current))
	cfg.WebDAVPassword = config.MergeStorageSecret(cfg.WebDAVPassword, current.WebDAVPassword, config.SameURLAuthority(cfg.WebDAVURL, current.WebDAVURL))
	// 还原 Redacted() 为展示而屏蔽的 URL 内嵌凭据，避免把占位符当真实代理/WebDAV 地址保存。
	cfg.ProxyURL = config.RestoreURLUserinfo(cfg.ProxyURL, current.ProxyURL)
	cfg.WebDAVURL = config.RestoreURLUserinfo(cfg.WebDAVURL, current.WebDAVURL)
	cfg.AuthToken = config.RevertEnvOnlyEcho(cfg.AuthToken, current.AuthToken, config.EnvAuthToken)
	cfg.CSRFToken = config.RevertEnvOnlyEcho(cfg.CSRFToken, current.CSRFToken, config.EnvCSRFToken)
	cfg.ProxyURL = config.RevertEnvOnlyEcho(cfg.ProxyURL, current.ProxyURL, config.EnvProxyURL)
	cfg.AdditionalCookies = config.RevertEnvOnlyEcho(cfg.AdditionalCookies, current.AdditionalCookies, config.EnvAdditionalCookies)
	cfg.SMBPassword = config.RevertEnvOnlyEcho(cfg.SMBPassword, current.SMBPassword, config.EnvSMBPassword)
	cfg.WebDAVPassword = config.RevertEnvOnlyEcho(cfg.WebDAVPassword, current.WebDAVPassword, config.EnvWebDAVPassword)

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
	s.rememberStoredConfig(cfg)
	return cfg, nil
}

func (s *Store) CreateJob(ctx context.Context, kind JobKind, input string, title string) (Job, error) {
	now := time.Now().UTC()
	job := Job{}
	err := s.db.GetContext(ctx, &job, `
INSERT INTO jobs (kind, status, input, title, progress, message, created_at, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?, ?)
RETURNING *`, kind, JobPending, input, title, "排队中", now, now)
	return job, err
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
	stats, _, err := s.DashboardMeta(ctx)
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
	job := Job{}
	err := s.db.GetContext(ctx, &job, `
UPDATE jobs SET status = ?, message = ?, progress = 1, error = '', updated_at = ?
	WHERE id = ? AND status NOT IN (?, ?, ?, ?)
	RETURNING *`,
		JobCanceled, "已取消", time.Now().UTC(), id, JobCompleted, JobCompletedWithErrors, JobFailed, JobCanceled)
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, err
	}
	// 已是终态或任务不存在时，返回当前状态以保留原有语义。
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

	jobs, err := s.insertJobs(ctx, tx, drafts, "排队中", now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) insertJobs(ctx context.Context, tx *sqlx.Tx, drafts []JobDraft, message string, now time.Time) ([]Job, error) {
	if len(drafts) == 0 {
		return []Job{}, nil
	}
	var query strings.Builder
	query.WriteString(`
INSERT INTO jobs (kind, status, input, title, progress, message, created_at, updated_at)
VALUES `)
	args := make([]any, 0, len(drafts)*7)
	for index, draft := range drafts {
		if index > 0 {
			query.WriteString(",")
		}
		query.WriteString("(?, ?, ?, ?, 0, ?, ?, ?)")
		args = append(args, draft.Kind, JobPending, draft.Input, draft.Title, message, now, now)
	}
	query.WriteString(" RETURNING *")

	jobs := make([]Job, 0, len(drafts))
	if err := tx.SelectContext(ctx, &jobs, query.String(), args...); err != nil {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	return jobs, nil
}

func (s *Store) CreateArchiveSchedule(ctx context.Context, schedule ArchiveSchedule) (ArchiveSchedule, error) {
	now := time.Now().UTC()
	schedule, err := prepareArchiveScheduleForSave(schedule)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	schedule.NextRunAt = nextArchiveScheduleRun(now, schedule.IntervalMinutes)
	err = s.db.GetContext(ctx, &schedule, `
INSERT INTO archive_schedules (
	name, enabled, interval_minutes, items_json, next_run_at, last_job_ids, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *`,
		schedule.Name, schedule.Enabled, schedule.IntervalMinutes, schedule.ItemsJSON,
		schedule.NextRunAt, "[]", now, now)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	return hydrateArchiveSchedule(schedule)
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
	err = s.db.GetContext(ctx, &schedule, `
UPDATE archive_schedules SET
	name = ?,
	enabled = ?,
	interval_minutes = ?,
	items_json = ?,
	next_run_at = ?,
	updated_at = ?
WHERE id = ?
RETURNING *`,
		schedule.Name, schedule.Enabled, schedule.IntervalMinutes, schedule.ItemsJSON,
		nextRunAt, now, schedule.ID)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	return hydrateArchiveSchedule(schedule)
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
	schedule := ArchiveSchedule{}
	err := s.db.GetContext(ctx, &schedule, `
UPDATE archive_schedules SET next_run_at = ?, updated_at = ? WHERE id = ?
RETURNING *`, nextRunAt.UTC(), time.Now().UTC(), id)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	return hydrateArchiveSchedule(schedule)
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

	drafts := make([]JobDraft, 0, len(schedule.Items))
	for _, item := range schedule.Items {
		drafts = append(drafts, JobDraft{Kind: item.Kind, Input: item.Input, Title: item.Title})
	}
	jobs, err := s.insertJobs(ctx, tx, drafts, "定时归档排队中", runAt)
	if err != nil {
		return nil, err
	}
	jobIDs := make([]int64, 0, len(schedule.Items))
	for _, job := range jobs {
		jobIDs = append(jobIDs, job.ID)
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
		file_path = excluded.file_path,
		bytes = excluded.bytes,
		created_at = excluded.created_at
	RETURNING *`,
			record.JobID, record.TweetID, record.MediaURL, record.FilePath, record.Bytes, now)
		return record, err
	}
	// tweet_id 为空（直接媒体 URL 任务）：按 media_url 去重，避免同一 URL 重复跑产生重复行。
	err := s.db.GetContext(ctx, &record, `
INSERT INTO downloads (job_id, tweet_id, media_url, file_path, bytes, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(media_url) WHERE tweet_id = '' AND media_url <> '' DO UPDATE SET
	file_path = excluded.file_path,
	bytes = excluded.bytes,
	created_at = excluded.created_at
RETURNING *`,
		record.JobID, record.TweetID, record.MediaURL, record.FilePath, record.Bytes, now)
	return record, err
}

func (s *Store) ListDownloads(ctx context.Context, limit int) ([]DownloadRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	items := []DownloadRecord{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM downloads ORDER BY created_at DESC LIMIT ?`, limit)
	return items, err
}

func (s *Store) JobFiles(ctx context.Context, jobID int64) ([]DownloadRecord, []FailedMedia, error) {
	type jobFileRow struct {
		SortOrder  int            `db:"sort_order"`
		RecordKind string         `db:"record_kind"`
		ID         int64          `db:"id"`
		JobID      int64          `db:"job_id"`
		TweetID    sql.NullString `db:"tweet_id"`
		MediaURL   sql.NullString `db:"media_url"`
		FilePath   sql.NullString `db:"file_path"`
		Bytes      sql.NullInt64  `db:"bytes"`
		Error      sql.NullString `db:"error"`
		CreatedAt  sql.NullTime   `db:"created_at"`
	}
	rows := []jobFileRow{}
	err := s.db.SelectContext(ctx, &rows, `
SELECT 1 AS sort_order, 'download' AS record_kind, id, job_id,
	tweet_id, media_url, file_path, bytes, NULL AS error, created_at
FROM downloads
WHERE job_id = ?
UNION ALL
SELECT 2 AS sort_order, 'failed' AS record_kind, id, job_id,
	NULL AS tweet_id, media_url, NULL AS file_path, NULL AS bytes, error, created_at
FROM failed_media
WHERE job_id = ?
UNION ALL
SELECT 0 AS sort_order, 'job' AS record_kind, id, id AS job_id,
	NULL AS tweet_id, NULL AS media_url, NULL AS file_path, NULL AS bytes,
	NULL AS error, NULL AS created_at
FROM jobs
WHERE id = ?
ORDER BY sort_order, created_at DESC`, jobID, jobID, jobID)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, sql.ErrNoRows
	}

	downloads := make([]DownloadRecord, 0, len(rows))
	failed := make([]FailedMedia, 0, len(rows))
	for _, row := range rows {
		switch row.RecordKind {
		case "job":
			continue
		case "download":
			downloads = append(downloads, DownloadRecord{
				ID:        row.ID,
				JobID:     row.JobID,
				TweetID:   row.TweetID.String,
				MediaURL:  row.MediaURL.String,
				FilePath:  row.FilePath.String,
				Bytes:     row.Bytes.Int64,
				CreatedAt: row.CreatedAt.Time,
			})
		case "failed":
			failed = append(failed, FailedMedia{
				ID:        row.ID,
				JobID:     row.JobID,
				MediaURL:  row.MediaURL.String,
				Error:     row.Error.String,
				CreatedAt: row.CreatedAt.Time,
			})
		default:
			return nil, nil, fmt.Errorf("unknown job file record kind %q", row.RecordKind)
		}
	}
	return downloads, failed, nil
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

type mediaDownloadStateRow struct {
	Unavailable int            `db:"unavailable"`
	ID          sql.NullInt64  `db:"id"`
	JobID       sql.NullInt64  `db:"job_id"`
	TweetID     sql.NullString `db:"tweet_id"`
	MediaURL    sql.NullString `db:"media_url"`
	FilePath    sql.NullString `db:"file_path"`
	Bytes       sql.NullInt64  `db:"bytes"`
	CreatedAt   sql.NullTime   `db:"created_at"`
}

func (s *Store) GetMediaDownloadState(ctx context.Context, tweetID string, mediaURL string) (*DownloadRecord, bool, error) {
	row := mediaDownloadStateRow{}
	err := s.db.GetContext(ctx, &row, `
SELECT
	CASE WHEN um.media_url IS NOT NULL THEN 1 ELSE 0 END AS unavailable,
	d.id, d.job_id, d.tweet_id, d.media_url, d.file_path, d.bytes, d.created_at
FROM (SELECT ? AS tweet_id, ? AS media_url) request
LEFT JOIN unavailable_media um ON um.media_url = request.media_url
LEFT JOIN downloads d ON d.tweet_id = request.tweet_id AND d.media_url = request.media_url`, tweetID, mediaURL)
	if err != nil {
		return nil, false, err
	}
	if !row.ID.Valid {
		return nil, row.Unavailable != 0, nil
	}
	return &DownloadRecord{
		ID:        row.ID.Int64,
		JobID:     row.JobID.Int64,
		TweetID:   row.TweetID.String,
		MediaURL:  row.MediaURL.String,
		FilePath:  row.FilePath.String,
		Bytes:     row.Bytes.Int64,
		CreatedAt: row.CreatedAt.Time,
	}, row.Unavailable != 0, nil
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

func (s *Store) GetUnavailableMedia(ctx context.Context, mediaURL string) (*UnavailableMedia, error) {
	item := UnavailableMedia{}
	err := s.db.GetContext(ctx, &item, `SELECT * FROM unavailable_media WHERE media_url = ?`, mediaURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) UpsertUnavailableMedia(ctx context.Context, item UnavailableMedia) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO unavailable_media (media_url, tweet_id, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(media_url) DO UPDATE SET
	tweet_id = CASE WHEN excluded.tweet_id <> '' THEN excluded.tweet_id ELSE unavailable_media.tweet_id END,
	error = excluded.error,
	updated_at = excluded.updated_at`,
		item.MediaURL, item.TweetID, item.Error, now, now)
	return err
}

func (s *Store) UpsertUser(ctx context.Context, user User) (User, error) {
	now := time.Now().UTC()
	err := s.db.GetContext(ctx, &user, `
INSERT INTO users (id, screen_name, name, protected, friends_count, media_count, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	screen_name = excluded.screen_name,
	name = excluded.name,
	protected = excluded.protected,
	friends_count = excluded.friends_count,
	media_count = excluded.media_count,
	updated_at = excluded.updated_at
RETURNING *`,
		user.ID, user.ScreenName, user.Name, user.Protected, user.FriendsCount, user.MediaCount, now)
	if err != nil {
		return User{}, err
	}
	return user, nil
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

// LocateUserEntities loads all existing entities for one archive root in a
// bounded number of queries. Archive jobs commonly process hundreds of users;
// doing one SELECT per user needlessly serializes SQLite round trips.
func (s *Store) LocateUserEntities(ctx context.Context, userIDs []string, parentDir string) (map[string]UserEntity, error) {
	parentDir, err := normalizeParentDir(parentDir)
	if err != nil {
		return nil, err
	}
	entities := make(map[string]UserEntity, len(userIDs))
	uniqueIDs := make([]string, 0, len(userIDs))
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		uniqueIDs = append(uniqueIDs, userID)
	}
	if len(uniqueIDs) == 0 {
		return entities, nil
	}

	const batchSize = 500
	for start := 0; start < len(uniqueIDs); start += batchSize {
		end := min(start+batchSize, len(uniqueIDs))
		query, args, err := sqlx.In(`
SELECT * FROM user_entities
WHERE parent_dir = ? AND user_id IN (?)`, parentDir, uniqueIDs[start:end])
		if err != nil {
			return nil, err
		}
		items := []UserEntity{}
		if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), args...); err != nil {
			return nil, err
		}
		for _, entity := range items {
			entities[entity.UserID] = entity
		}
	}
	return entities, nil
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
	err := s.db.GetContext(ctx, &list, `
INSERT INTO twitter_lists (id, name, owner_user_id, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	name = excluded.name,
	owner_user_id = excluded.owner_user_id,
	updated_at = excluded.updated_at
RETURNING *`,
		list.ID, list.Name, list.OwnerUserID, now)
	if err != nil {
		return List{}, err
	}
	return list, nil
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
	link := UserLink{}
	err := s.db.GetContext(ctx, &link, `
INSERT INTO user_links (user_id, name, list_entity_id, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(user_id, list_entity_id) DO UPDATE SET
	name = excluded.name,
	updated_at = excluded.updated_at
RETURNING *`,
		userID, name, listEntityID, now)
	if err != nil {
		return UserLink{}, err
	}
	return link, nil
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
	err := s.db.GetContext(ctx, &failed, `
INSERT INTO failed_tweets (job_id, entity_id, tweet_id, payload, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(entity_id, tweet_id) DO UPDATE SET
	job_id = excluded.job_id,
	payload = excluded.payload,
	error = excluded.error,
	updated_at = excluded.updated_at
RETURNING *`,
		failed.JobID, failed.EntityID, failed.TweetID, failed.Payload, failed.Error, now, now)
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
	items, _, err := s.ListFailedTweetViewsPage(ctx, limit, 0)
	return items, err
}

type failedTweetViewPageRow struct {
	FailedTweetView
	Total int `db:"total_count"`
}

func (s *Store) ListFailedTweetViewsPage(ctx context.Context, limit int, offset int) ([]FailedTweetView, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows := []failedTweetViewPageRow{}
	err := s.db.SelectContext(ctx, &rows, `
SELECT
	ft.id, ft.job_id, ft.entity_id, ft.tweet_id, ft.error, ft.created_at, ft.updated_at,
	COALESCE(j.title, '') AS job_title,
	COALESCE(ue.name, '') AS entity_name,
	COALESCE(ue.parent_dir, '') AS entity_parent_dir,
	COALESCE(u.id, '') AS user_id,
	COALESCE(u.screen_name, '') AS user_screen_name,
	COALESCE(u.name, '') AS user_name,
	COUNT(*) OVER() AS total_count
FROM failed_tweets ft
LEFT JOIN jobs j ON j.id = ft.job_id
LEFT JOIN user_entities ue ON ue.id = ft.entity_id
LEFT JOIN users u ON u.id = ue.user_id
ORDER BY ft.updated_at ASC
LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		total, err := s.CountFailedTweets(ctx)
		if err != nil {
			return nil, 0, err
		}
		return []FailedTweetView{}, total, nil
	}
	items := make([]FailedTweetView, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.FailedTweetView)
	}
	total := rows[0].Total
	return items, total, nil
}

func (s *Store) CountFailedTweets(ctx context.Context) (int, error) {
	_, count, err := s.DashboardMeta(ctx)
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

// PruneFailedRecords 清理早于 olderThan 的失败推文 / 失败媒体记录（M7 保留策略）。
// downloads 历史保留：任务文件列表依赖它，不做自动删除。
func (s *Store) PruneFailedRecords(ctx context.Context, olderThan time.Time) (int, error) {
	olderThan = olderThan.UTC()
	var pruned int64
	// 分语句执行：database/sql 的 Exec 对多语句 SQL 只执行第一条（SQLite 下 RowsAffected
	// 仅反映首个 DELETE），必须逐个执行。
	for _, statement := range []string{
		`DELETE FROM failed_tweets WHERE updated_at < ?`,
		`DELETE FROM failed_media WHERE created_at < ?`,
	} {
		result, err := s.db.ExecContext(ctx, statement, olderThan)
		if err != nil {
			return int(pruned), err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return int(pruned), err
		}
		pruned += affected
	}
	return int(pruned), nil
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
