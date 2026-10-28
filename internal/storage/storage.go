package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// UserFacingError wraps an error with a safe, user-visible message and an HTTP
// status code. writeError checks for this type and returns Message to the
// client while logging the full Cause server-side.
type UserFacingError struct {
	Status  int
	Message string
	Cause   error
}

func (e *UserFacingError) Error() string { return e.Message }
func (e *UserFacingError) Unwrap() error { return e.Cause }

// ErrNotFound returns a 404 UserFacingError.
func ErrNotFound(msg string, cause error) *UserFacingError {
	return &UserFacingError{Status: http.StatusNotFound, Message: msg, Cause: cause}
}

// ErrValidation returns a 400 UserFacingError.
func ErrValidation(msg string) *UserFacingError {
	return &UserFacingError{Status: http.StatusBadRequest, Message: msg}
}

type Store struct {
	db *sqlx.DB

	// dataDir 是 SQLite 数据库文件所在目录；fallbackDownloadDir 是旧库 download_dir
	// 不可用（文件系统根/家目录）时的安全降级目录（<dataDir>/downloads）。
	dataDir             string
	fallbackDownloadDir string
	unsafeDirMu         sync.Mutex
	lastUnsafeDir       string

	configMu       sync.RWMutex
	cachedStored   *config.AppConfig
	cachedStoredAt time.Time

	// libraryCache avoids repeating the relatively expensive user-path joins and
	// download-directory walk whenever the gallery is revisited or refreshed.
	libraryCacheMu        sync.RWMutex
	libraryBuildMu        sync.Mutex
	libraryCacheAt        time.Time
	libraryCache          []DownloadRecord
	libraryCacheOK        bool
	libraryCacheLimit     int
	libraryCacheTruncated bool
	// libraryCacheGen 每次失效递增；回填缓存前校验代数未变，避免查询期间新写入触发
	// 的失效被随后到期的旧数据覆盖（脏缓存最长持续一个 TTL）。
	libraryCacheGen atomic.Uint64
	// librarySnap 是分页接口使用的快照（孤儿/归属/facet/计数），与 libraryCacheGen
	// 同步失效，见 library_page.go。
	librarySnapMu sync.RWMutex
	librarySnap   *librarySnapshot
}

const libraryDownloadsCacheTTL = 5 * time.Minute
const storedConfigCacheTTL = time.Second

// MaxLibraryDownloadsLimit 是媒体归档库单次返回的记录上限。前端在客户端做全部
// 筛选/计数/搜索，因此总是请求全量；该上限只为超大规模归档约束响应与缓存体积。
const MaxLibraryDownloadsLimit = 100000

// _txlock=immediate 让所有事务以 BEGIN IMMEDIATE 开始：事务里"先读后写"若用默认的
// BEGIN DEFERRED，事务先以读者身份启动，写第一条语句时才尝试升级为写者，而
// busy_timeout 对锁升级不生效，并发写者会立刻返回 SQLITE_BUSY(5)/SQLITE_BUSY_SNAPSHOT(517)
// 而不是等待。开头就取写锁后，冲突走 busy_timeout 排队。
const sqliteOpenOptions = "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-16000)&_pragma=mmap_size(268435456)"

// schemaVersion 是当前二进制认识的库结构版本，迁移完成后写入 PRAGMA user_version。
// 向前迁移是可加、幂等的，但没有版本闸门时，回滚镜像会让旧二进制打开新库并正常启动，
// 直到某个 SELECT * 撞上未知列才失败（ListDownloads/ListJobs 报
// "missing destination name ..."），任务列表和媒体库直接不可用且看不出原因。
// 新增列 / 新表这类结构变更时递增该值，并在 migration_test.go 的
// schemaColumnsForVersion 登记新版本的列集合（测试会拦截遗漏）。
//
// v2：downloads.content_hash（v0.8.3 引入时漏了递增，回滚到 v0.8.2 及更早的镜像会
// 通过闸门后在 SELECT * 上失败）。
// v3：failed_tweets.retry_count / next_retry_at（指数退避重试）。
// v4：download_roots 表（历史可信下载根，修复改目录后历史媒体 404）。
// v5：archive_schedules.claim_version（计划领取整数乐观锁）、download_roots.last_seen_at
// （可信根上限/最近使用清理）；同时 DROP 只写不读的 user_previous_names 及其触发器。
// v6: app_account stores the single local login and its persistent session key.
const schemaVersion = 6

// ErrSchemaTooNew 表示库由更新版本写入，当前二进制不认识。
var ErrSchemaTooNew = errors.New("数据库结构版本高于当前程序")

const (
	MinArchiveScheduleIntervalMinutes = 5
	MaxArchiveScheduleIntervalMinutes = 60 * 24 * 30
	MaxArchiveScheduleItems           = 200
)

var ErrArchiveScheduleAlreadyClaimed = errors.New("定时归档计划已被其他运行领取")

func Open(path string) (*Store, error) {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sqlx.Open("sqlite", dsn)
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
	if err := ensureWALMode(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	store := &Store{
		db:                  db,
		dataDir:             filepath.Dir(absPath),
		fallbackDownloadDir: filepath.Join(filepath.Dir(absPath), "downloads"),
	}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.EnsureConfig(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	// 旧库的 download_dir 可能是文件系统根 / 家目录（安全上不可用），也可能是空的。
	// 这里不返回致命错误（否则升级即不可用、用户没有 UI 可改）而是降级到
	// <dataDir>/downloads 并大声 warning；同时绝不把 `/`/家目录登记为可信下载根。
	if err := store.repairDownloadDir(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.seedDownloadRoots(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// repairDownloadDir 把库中不可信的 download_dir 修复为 <dataDir>/downloads。
// 只修复"库内已存值"：env 覆盖值不可信时不会写库（env 是部署方的决定），改由
// GetConfig 的运行时守卫降级到库内有效值。返回的错误只可能是数据库错误。
func (s *Store) repairDownloadDir(ctx context.Context) error {
	stored := config.AppConfig{}
	if err := s.db.GetContext(ctx, &stored, storedConfigQuery); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	stored = stored.Normalized()
	rootErr := validateTrustedDownloadRoot(stored.DownloadDir)
	if rootErr == nil {
		return nil
	}
	fallback := s.fallbackDownloadDir
	if strings.TrimSpace(fallback) == "" || validateTrustedDownloadRoot(fallback) != nil {
		s.warnUnsafeDownloadDir(stored.DownloadDir, rootErr)
		return nil
	}
	log.Printf("[storage][WARNING] 库内下载目录不可用作媒体库沙箱：%q（%v）；已自动降级为 %q 并写回数据库。请通过设置页或 OPEN_XDOWNLOAD_DOWNLOAD_DIR 重新指定。",
		stored.DownloadDir, rootErr, fallback)
	if _, err := s.db.ExecContext(ctx, `UPDATE app_config SET download_dir = ?, updated_at = ? WHERE id = 1`, fallback, time.Now().UTC()); err != nil {
		return err
	}
	s.configMu.Lock()
	s.cachedStored = nil
	s.cachedStoredAt = time.Time{}
	s.configMu.Unlock()
	s.invalidateLibraryDownloadsCache()
	return nil
}

// warnUnsafeDownloadDir 对同一个不可信目录只打印一次 warning，避免高频配置读刷屏。
func (s *Store) warnUnsafeDownloadDir(dir string, cause error) {
	s.unsafeDirMu.Lock()
	defer s.unsafeDirMu.Unlock()
	if s.lastUnsafeDir == dir {
		return
	}
	s.lastUnsafeDir = dir
	log.Printf("[storage][WARNING] 下载目录不可用作媒体库沙箱：%q（%v）；本次运行将使用安全兜底目录。请修正配置。", dir, cause)
}

func (s *Store) Close() error {
	// PRAGMA optimize 让 SQLite 按需要 ANALYZE 关键索引（幂等、开销很低）；它必须在
	// 关闭前执行。失败只记录日志，不影响 Close 的结果。
	if _, err := s.db.ExecContext(context.Background(), `PRAGMA optimize`); err != nil {
		log.Printf("storage: PRAGMA optimize: %v", err)
	}
	return s.db.Close()
}

// ensureWALMode 在启动时把库切到 WAL。journal_mode 是持久化的库属性，只需设置一次，
// 因此不放在每条连接的 DSN pragma 里：切换 journal_mode 需要短暂的排他锁，而 SQLite
// 在这一步不会走 busy handler（busy_timeout 无效），多个进程同时启动时建立连接阶段
// 就会直接返回 SQLITE_BUSY，导致 main.go 在 Open 上 fatal。这里改为启动时设置一次并
// 短暂重试：只要有任一方切换成功，其余方读到 wal 即视为成功。
func ensureWALMode(db *sqlx.DB) error {
	const attempts = 20
	var lastErr error
	for i := 0; i < attempts; i++ {
		var mode string
		if err := db.Get(&mode, `PRAGMA journal_mode`); err != nil {
			return err
		}
		if strings.EqualFold(mode, "wal") {
			return nil
		}
		if err := db.Get(&mode, `PRAGMA journal_mode = WAL`); err != nil {
			lastErr = err
		} else if strings.EqualFold(mode, "wal") {
			return nil
		} else {
			lastErr = fmt.Errorf("journal_mode 切换后仍为 %q", mode)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("无法把数据库切换到 WAL 模式: %w", lastErr)
}

func (s *Store) migrate() error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	schema := `
		CREATE TABLE IF NOT EXISTS app_account (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			username TEXT NOT NULL,
			password_hash BLOB NOT NULL,
			session_key BLOB NOT NULL
		);

		-- max_concurrency 字段保留只为兼容旧库/API：串行执行（=1）是产品决策，
		-- config.Normalized() 与 jobs.availableSlots() 都会强制为 1。不要根据该列
		-- 是否存在推断"可以放开并发"；若要改并发需要产品层面重新评估调度与限速。
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
				incremental_archive BOOLEAN NOT NULL DEFAULT 0,
				file_naming_mode TEXT NOT NULL DEFAULT 'tweet_text',
				max_filename_length INTEGER NOT NULL DEFAULT 120,
				storage_type TEXT NOT NULL DEFAULT 'local',
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
	claim_version INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_archive_schedules_enabled_next_run_at ON archive_schedules (enabled, next_run_at);

CREATE TABLE IF NOT EXISTS downloads (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL,
	tweet_id TEXT NOT NULL DEFAULT '',
	media_url TEXT NOT NULL,
	preview_url TEXT NOT NULL DEFAULT '',
	file_path TEXT NOT NULL,
	bytes INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL,
	FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS download_roots (
	path TEXT PRIMARY KEY,
	last_seen_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_downloads_created_at
	ON downloads (created_at DESC, id DESC);

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

	CREATE INDEX IF NOT EXISTS idx_users_screen_name ON users (screen_name);

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
		retry_count INTEGER NOT NULL DEFAULT 0,
		next_retry_at DATETIME,
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
	if err := s.runMigrationOnce("backfill_download_preview_urls", s.backfillDownloadPreviewURLs); err != nil {
		return err
	}
	if err := s.runMigrationOnce("rederive_download_preview_urls", s.rederiveDownloadPreviewURLs); err != nil {
		return err
	}
	if err := s.runMigrationOnce("backfill_downloads_media_key", s.backfillDownloadsMediaKey); err != nil {
		return err
	}
	if err := s.runMigrationOnce("rekey_downloads_media_formats", s.rekeyDownloadsMediaFormats); err != nil {
		return err
	}
	// 视频身份改为只按媒体 ID 识别后，历史记录的 media_key 需按新规则重算一次，
	// 否则旧库里已下载的视频遇到同一视频的其他变体 URL 时查不到副本而重复下载。
	if err := s.runMigrationOnce("rekey_downloads_video_media_id", s.rekeyDownloadsMediaFormats); err != nil {
		return err
	}
	if err := s.runMigrationOnce("drop_smb_webdav_config_columns", s.dropSMBWebDAVConfigColumns); err != nil {
		return err
	}
	if err := s.runMigrationOnce("drop_unique_users_screen_name", s.dropUniqueUsersScreenName); err != nil {
		return err
	}
	// user_previous_names 只被触发器写入、全仓库没有任何读取方（死表），删除以停止
	// 无意义写入；触发器必须先于表删除。
	if err := s.runMigrationOnce("drop_user_previous_names", s.dropUserPreviousNames); err != nil {
		return err
	}
	// 历史迁移都是"只增量"的，但 v5 给 archive_schedules/download_roots 加了列、并删除了
	// 旧表；列已由 addMissingColumns 补齐，表删除由上面的迁移承担。
	if err := s.ensureDashboardCounters(); err != nil {
		return err
	}
	if err := s.addMissingIndexes(); err != nil {
		return err
	}
	return s.setSchemaVersion()
}

// checkSchemaVersion 拒绝打开由更新版本写入的库。降级运行时旧二进制的结构体缺少新列，
// 约 20 处 SELECT * 会在扫描时失败，因此在迁移之前就明确拒绝，而不是让它带着不完整的
// 认知去改写结构。user_version 为 0 的旧库视为首次升级，正常向前迁移。
func (s *Store) checkSchemaVersion() error {
	var version int
	if err := s.db.Get(&version, `PRAGMA user_version`); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("%w：库为 v%d，当前程序只支持到 v%d。请升级到对应版本的程序，或从升级前的备份恢复该库",
			ErrSchemaTooNew, version, schemaVersion)
	}
	return nil
}

// setSchemaVersion 在迁移全部成功后写入版本号。PRAGMA 不支持绑定参数，故用常量拼接。
func (s *Store) setSchemaVersion() error {
	var version int
	if err := s.db.Get(&version, `PRAGMA user_version`); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	_, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

type migrationExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Get(dest any, query string, args ...any) error
	Select(dest any, query string, args ...any) error
}

// runMigrationOnce 在事务中执行一次性迁移并登记。进程如果在迁移中途崩溃，
// 数据变更和 schema_migrations 记录会一起回滚，避免下次启动误以为迁移已完成。
// 已应用的迁移先用一条无锁 SELECT 快速跳过：否则每次启动都要为 10+ 个迁移各开一个
// BEGIN IMMEDIATE 写事务（_txlock=immediate），与其它实例的启动/写入无谓竞争。
func (s *Store) runMigrationOnce(name string, fn func(migrationExecutor) error) error {
	var applied int
	if err := s.db.Get(&applied, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name); err != nil {
		return err
	}
	if applied > 0 {
		return nil
	}
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	// 事务内二次确认：上面的无锁检查与这里之间可能有其它实例完成了同一迁移。
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

// addMissingColumns 补齐历史库缺失的列。"检查列是否存在"与 ALTER TABLE 之间必须原子，
// 否则多个进程同时首次启动时会有一方撞上 "duplicate column name" 而启动失败，
// 故整体放进一个事务（DSN 已设 _txlock=immediate，事务开头即取写锁）。
func (s *Store) addMissingColumns() error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback() // Commit 后为 no-op

	// 使用有序切片而不是 map：ALTER 顺序固定，失败/日志可复现（功能上各列独立）。
	columns := []struct {
		name      string
		statement string
	}{
		{"additional_cookies", `ALTER TABLE app_config ADD COLUMN additional_cookies TEXT NOT NULL DEFAULT ''`},
		{"auto_follow_protected", `ALTER TABLE app_config ADD COLUMN auto_follow_protected BOOLEAN NOT NULL DEFAULT 0`},
		{"include_nested_tweet_media", `ALTER TABLE app_config ADD COLUMN include_nested_tweet_media BOOLEAN NOT NULL DEFAULT 0`},
		{"incremental_archive", `ALTER TABLE app_config ADD COLUMN incremental_archive BOOLEAN NOT NULL DEFAULT 0`},
		{"file_naming_mode", `ALTER TABLE app_config ADD COLUMN file_naming_mode TEXT NOT NULL DEFAULT 'tweet_text'`},
		{"max_filename_length", `ALTER TABLE app_config ADD COLUMN max_filename_length INTEGER NOT NULL DEFAULT 120`},
		{"storage_type", `ALTER TABLE app_config ADD COLUMN storage_type TEXT NOT NULL DEFAULT 'local'`},
	}
	for _, column := range columns {
		var exists int
		if err := tx.Get(&exists, `SELECT COUNT(*) FROM pragma_table_info('app_config') WHERE name = ?`, column.name); err != nil {
			return err
		}
		if exists == 0 {
			if _, err := tx.Exec(column.statement); err != nil {
				return err
			}
		}
	}
	// user_entities：增量归档早停游标（可配置开关）
	var hasLastSeen int
	if err := tx.Get(&hasLastSeen, `SELECT COUNT(*) FROM pragma_table_info('user_entities') WHERE name = 'last_seen_tweet_id'`); err != nil {
		return err
	}
	if hasLastSeen == 0 {
		if _, err := tx.Exec(`ALTER TABLE user_entities ADD COLUMN last_seen_tweet_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	var hasPreviewURL int
	if err := tx.Get(&hasPreviewURL, `SELECT COUNT(*) FROM pragma_table_info('downloads') WHERE name = 'preview_url'`); err != nil {
		return err
	}
	if hasPreviewURL == 0 {
		if _, err := tx.Exec(`ALTER TABLE downloads ADD COLUMN preview_url TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// downloads：媒体身份键（同一媒体内容的稳定键），用于跨推文/跨目录去重与复用。
	var hasMediaKey int
	if err := tx.Get(&hasMediaKey, `SELECT COUNT(*) FROM pragma_table_info('downloads') WHERE name = 'media_key'`); err != nil {
		return err
	}
	if hasMediaKey == 0 {
		if _, err := tx.Exec(`ALTER TABLE downloads ADD COLUMN media_key TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	var hasContentHash int
	if err := tx.Get(&hasContentHash, `SELECT COUNT(*) FROM pragma_table_info('downloads') WHERE name = 'content_hash'`); err != nil {
		return err
	}
	if hasContentHash == 0 {
		if _, err := tx.Exec(`ALTER TABLE downloads ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// failed_tweets：指数退避重试（P1-8）
	var hasRetryCount int
	if err := tx.Get(&hasRetryCount, `SELECT COUNT(*) FROM pragma_table_info('failed_tweets') WHERE name = 'retry_count'`); err != nil {
		return err
	}
	if hasRetryCount == 0 {
		if _, err := tx.Exec(`ALTER TABLE failed_tweets ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	var hasNextRetryAt int
	if err := tx.Get(&hasNextRetryAt, `SELECT COUNT(*) FROM pragma_table_info('failed_tweets') WHERE name = 'next_retry_at'`); err != nil {
		return err
	}
	if hasNextRetryAt == 0 {
		if _, err := tx.Exec(`ALTER TABLE failed_tweets ADD COLUMN next_retry_at DATETIME`); err != nil {
			return err
		}
	}
	// archive_schedules：计划领取的整数乐观锁（v5）。
	var hasClaimVersion int
	if err := tx.Get(&hasClaimVersion, `SELECT COUNT(*) FROM pragma_table_info('archive_schedules') WHERE name = 'claim_version'`); err != nil {
		return err
	}
	if hasClaimVersion == 0 {
		if _, err := tx.Exec(`ALTER TABLE archive_schedules ADD COLUMN claim_version INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	// download_roots：最近登记/使用时间，供 PruneDownloadRoots 排序（v5）。
	var hasRootSeenAt int
	if err := tx.Get(&hasRootSeenAt, `SELECT COUNT(*) FROM pragma_table_info('download_roots') WHERE name = 'last_seen_at'`); err != nil {
		return err
	}
	if hasRootSeenAt == 0 {
		if _, err := tx.Exec(`ALTER TABLE download_roots ADD COLUMN last_seen_at DATETIME`); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	changed := false
	var lastID int64
	for {
		var rows []downloadRow
		if err := exec.Select(&rows, `SELECT id, media_url FROM downloads WHERE id > ? AND tweet_id <> '' AND media_url <> '' AND media_url LIKE '%tag=%' ORDER BY id LIMIT 1000`, lastID); err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			normalized := downloader.NormalizeMediaURL(row.MediaURL)
			if normalized == row.MediaURL {
				continue
			}
			if !changed {
				// The index must stay absent until duplicates across all batches are removed.
				if _, err := exec.Exec(`DROP INDEX IF EXISTS idx_downloads_tweet_media_unique`); err != nil {
					return err
				}
				changed = true
			}
			if _, err := exec.Exec(`UPDATE downloads SET media_url = ? WHERE id = ?`, normalized, row.ID); err != nil {
				return err
			}
		}
		lastID = rows[len(rows)-1].ID
	}
	if !changed {
		return nil
	}
	// 去重 + 重建索引必须留在本事务内：索引一旦随事务提交而持久缺失，后续每个
	// runMigrationOnce 都会各自提交，在 addMissingIndexes 重建之前的这段窗口里，
	// CreateDownload 的 ON CONFLICT(tweet_id, media_url) 会因为找不到对应唯一约束而
	// 全部失败（SQL logic error ... does not match any PRIMARY KEY or UNIQUE constraint）。
	// 规范化只会在 tweet_id <> '' 分组里造成重复，故这里复用 deduplicateDownloads 的清理；
	// 它是幂等的，随后作为独立迁移再跑一次即为空操作。
	if err := s.deduplicateDownloads(exec); err != nil {
		return err
	}
	if _, err := exec.Exec(`
	CREATE UNIQUE INDEX IF NOT EXISTS idx_downloads_tweet_media_unique
	ON downloads (tweet_id, media_url)
	WHERE tweet_id <> ''`); err != nil {
		return err
	}
	return nil
}

// backfillDownloadsMediaKey 为历史 downloads 记录补齐媒体身份键（新增列默认为空）。
// 身份键在写入入口由 downloader.MediaIdentity 计算，新记录无需回填；一次性执行，
// 只为让旧记录也能参与"同一媒体已有可用文件即跳过"的判定。
func (s *Store) backfillDownloadsMediaKey(exec migrationExecutor) error {
	type downloadRow struct {
		ID       int64  `db:"id"`
		MediaURL string `db:"media_url"`
	}
	var lastID int64
	for {
		var rows []downloadRow
		if err := exec.Select(&rows, `SELECT id, media_url FROM downloads WHERE id > ? AND media_key = '' AND media_url <> '' ORDER BY id LIMIT 1000`, lastID); err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			key := downloader.MediaIdentity(row.MediaURL)
			if key == "" {
				continue
			}
			if _, err := exec.Exec(`UPDATE downloads SET media_key = ? WHERE id = ?`, key, row.ID); err != nil {
				return err
			}
		}
		lastID = rows[len(rows)-1].ID
	}
}

// rekeyDownloadsMediaFormats 按当前 downloader.MediaIdentity 重算全部 media_key。最初
// 用于 format 加入身份键后的重算；身份规则再变化时以新的迁移名重新登记它即可。
func (s *Store) rekeyDownloadsMediaFormats(exec migrationExecutor) error {
	type downloadRow struct {
		ID       int64  `db:"id"`
		MediaURL string `db:"media_url"`
		MediaKey string `db:"media_key"`
	}
	var lastID int64
	for {
		var rows []downloadRow
		if err := exec.Select(&rows, `SELECT id, media_url, media_key FROM downloads WHERE id > ? AND media_url <> '' ORDER BY id LIMIT 1000`, lastID); err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			key := downloader.MediaIdentity(row.MediaURL)
			if key != row.MediaKey {
				if _, err := exec.Exec(`UPDATE downloads SET media_key = ? WHERE id = ?`, key, row.ID); err != nil {
					return err
				}
			}
		}
		lastID = rows[len(rows)-1].ID
	}
}

// backfillDownloadPreviewURLs restores the CDN poster URL for historical
// Twitter video records created before preview_url was persisted. It is a
// one-time migration so opening the gallery no longer needs to derive this
// value across the whole downloads table.
func (s *Store) backfillDownloadPreviewURLs(exec migrationExecutor) error {
	type row struct {
		ID       int64  `db:"id"`
		MediaURL string `db:"media_url"`
	}
	var lastID int64
	for {
		var rows []row
		if err := exec.Select(&rows, `
SELECT id, media_url
FROM downloads
WHERE id > ? AND preview_url = '' AND media_url <> '' ORDER BY id LIMIT 1000`, lastID); err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, item := range rows {
			previewURL := deriveVideoPreviewURL(item.MediaURL)
			if previewURL == "" {
				continue
			}
			if _, err := exec.Exec(`UPDATE downloads SET preview_url = ? WHERE id = ? AND preview_url = ''`, previewURL, item.ID); err != nil {
				return err
			}
		}
		lastID = rows[len(rows)-1].ID
	}
}

// rederiveDownloadPreviewURLs re-derives stored poster URLs that were written
// by the earlier derivation. 旧版推导直接把 .mp4 换成 .jpg，得到 /vid/…jpg 形式的
// 死链（真实缩略图在 /img/ 下），历史媒体因此大面积 404。我们推导出的地址带有
// ?name=small 特征（API 解析到的 media_url_https 无查询串），据此挑出旧值并用
// 修正后的推导重写；解析得到真实海报地址的记录不受影响。
func (s *Store) rederiveDownloadPreviewURLs(exec migrationExecutor) error {
	type row struct {
		ID         int64  `db:"id"`
		MediaURL   string `db:"media_url"`
		PreviewURL string `db:"preview_url"`
	}
	var lastID int64
	for {
		var rows []row
		if err := exec.Select(&rows, `
SELECT id, media_url, preview_url
FROM downloads
WHERE id > ? AND media_url LIKE '%video.twimg.com%'
  AND (preview_url LIKE '%name=small%' OR preview_url LIKE '%/vid/%') ORDER BY id LIMIT 1000`, lastID); err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, item := range rows {
			previewURL := deriveVideoPreviewURL(item.MediaURL)
			if previewURL == "" || previewURL == item.PreviewURL {
				continue
			}
			if _, err := exec.Exec(`UPDATE downloads SET preview_url = ? WHERE id = ? AND preview_url = ?`, previewURL, item.ID, item.PreviewURL); err != nil {
				return err
			}
		}
		lastID = rows[len(rows)-1].ID
	}
}

// dropSMBWebDAVConfigColumns 删除历史版本 app_config 中遗留的 SMB/WebDAV 配置列，
// 连同其中的明文密码一并清除（SMB/WebDAV 存储后端已移除）。
// dropUniqueUsersScreenName 把 users.screen_name 的唯一索引换成普通索引。X 允许改名，
// 旧用户名也可能被他人注册：库里残留旧名时，新用户的 UpsertUser（只处理 id 冲突）
// 会撞上唯一约束，导致该用户的归档每次都失败。代码里没有依赖 screen_name 唯一性的查询。
func (s *Store) dropUniqueUsersScreenName(exec migrationExecutor) error {
	_, err := exec.Exec(`
DROP INDEX IF EXISTS idx_users_screen_name;
CREATE INDEX IF NOT EXISTS idx_users_screen_name ON users (screen_name);`)
	return err
}

func (s *Store) dropSMBWebDAVConfigColumns(exec migrationExecutor) error {
	columns := []string{
		"smb_host", "smb_port", "smb_share", "smb_path", "smb_domain",
		"smb_username", "smb_password", "webdav_url", "webdav_path",
		"webdav_username", "webdav_password",
	}
	for _, name := range columns {
		var exists int
		if err := exec.Get(&exists, `SELECT COUNT(*) FROM pragma_table_info('app_config') WHERE name = ?`, name); err != nil {
			return err
		}
		if exists > 0 {
			// 列名来自上面的硬编码白名单（不是用户输入），ALTER TABLE 不支持绑定列名。
			if _, err := exec.Exec(`ALTER TABLE app_config DROP COLUMN ` + name); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropUserPreviousNames 删除只写不读的 user_previous_names 表及其触发器（v5）。
// 触发器必须在表之前删除；DROP ... IF EXISTS 保证重复执行幂等。
func (s *Store) dropUserPreviousNames(exec migrationExecutor) error {
	_, err := exec.Exec(`
DROP TRIGGER IF EXISTS trg_users_name_history;
DROP TABLE IF EXISTS user_previous_names;`)
	return err
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
	CREATE INDEX IF NOT EXISTS idx_downloads_media_key
	ON downloads (media_key);
	CREATE INDEX IF NOT EXISTS idx_downloads_content_hash
	ON downloads (content_hash);
	CREATE INDEX IF NOT EXISTS idx_failed_media_job_created_at
	ON failed_media (job_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_failed_tweets_updated_at
	ON failed_tweets (updated_at);
	-- 自动重试（AutoRetryFailed 默认开启）的到期判断每 2s 触发一次，没有索引就是全表扫描。
	CREATE INDEX IF NOT EXISTS idx_failed_tweets_next_retry_at
	ON failed_tweets (next_retry_at);
	-- 保留策略 PruneFailedRecords 按时间删除；列表按时间倒序，均无可用前缀列。
	CREATE INDEX IF NOT EXISTS idx_failed_media_created_at
	ON failed_media (created_at);
	CREATE INDEX IF NOT EXISTS idx_unavailable_media_updated_at
	ON unavailable_media (updated_at);
	CREATE INDEX IF NOT EXISTS idx_user_entities_parent_user
	ON user_entities (parent_dir, user_id);
	-- 媒体库分页按 file_path 去重分组（ROW_NUMBER PARTITION BY + GROUP BY）。
	CREATE INDEX IF NOT EXISTS idx_downloads_file_path
	ON downloads (file_path);
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
	// max_concurrency 保留为兼容列/API 字段：串行执行（=1）是产品决策，
	// config.AppConfig.Normalized() 与 jobs.Manager.availableSlots() 都会强制为 1。
	// 这里不"修复"该值，也不要因为字段存在就尝试放开并发。
	cfg := config.Default()
	// OR IGNORE：上面的存在性检查与 INSERT 之间不是原子的，多个进程同时首次启动时
	// 会有一方撞上 app_config.id 唯一约束并让启动失败。行已存在即跳过，语义不变。
	_, err := s.db.NamedExecContext(ctx, `
		INSERT OR IGNORE INTO app_config (
			id, download_dir, max_concurrency, proxy_url, auth_token, csrf_token,
			additional_cookies, auto_retry_failed, auto_follow_protected,
			include_nested_tweet_media, incremental_archive,
			file_naming_mode, max_filename_length, storage_type, updated_at
		) VALUES (
			1, :download_dir, :max_concurrency, :proxy_url, :auth_token, :csrf_token,
			:additional_cookies, :auto_retry_failed, :auto_follow_protected,
			:include_nested_tweet_media, :incremental_archive,
			:file_naming_mode, :max_filename_length, :storage_type, :updated_at
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
		"incremental_archive":        cfg.IncrementalArchive,
		"file_naming_mode":           cfg.FileNamingMode,
		"max_filename_length":        cfg.MaxFilenameLength,
		"storage_type":               cfg.StorageType,
		"updated_at":                 time.Now().UTC(),
	})
	return err
}

// storedConfigQuery 显式列出列（不用 SELECT *）：新增"仅展示"字段（如 EnvOverridden）时
// 不会破坏扫描。max_concurrency 仍是串行产品决策的兼容字段，见 EnsureConfig 注释。
const storedConfigQuery = `
SELECT download_dir, max_concurrency, proxy_url, auth_token, csrf_token,
	additional_cookies, auto_retry_failed, auto_follow_protected,
	include_nested_tweet_media, incremental_archive,
	file_naming_mode, max_filename_length, storage_type
FROM app_config WHERE id = 1`

// GetStoredConfig 返回数据库中的配置，不套用环境变量。供占位符合并与 UpdateConfig 写库使用。
// 若库内 download_dir 不可信（文件系统根/家目录），返回值会降级为安全兜底目录（并 warning），
// 保证调用方拿到的永远是可用于媒体库沙箱的路径；Open 会把该兜底值持久化。
func (s *Store) GetStoredConfig(ctx context.Context) (config.AppConfig, error) {
	s.configMu.RLock()
	if s.cachedStored != nil && time.Since(s.cachedStoredAt) < storedConfigCacheTTL {
		cfg := *s.cachedStored
		s.configMu.RUnlock()
		return s.guardStoredDownloadDir(cfg), nil
	}
	s.configMu.RUnlock()

	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.cachedStored != nil && time.Since(s.cachedStoredAt) < storedConfigCacheTTL {
		return s.guardStoredDownloadDir(*s.cachedStored), nil
	}
	cfg := config.AppConfig{}
	err := s.db.GetContext(ctx, &cfg, storedConfigQuery)
	if errors.Is(err, sql.ErrNoRows) {
		cfg = config.Default()
		s.cachedStored = &cfg
		s.cachedStoredAt = time.Now()
		return s.guardStoredDownloadDir(cfg), nil
	}
	if err != nil {
		return config.AppConfig{}, err
	}
	cfg = cfg.Normalized()
	s.cachedStored = &cfg
	s.cachedStoredAt = time.Now()
	return s.guardStoredDownloadDir(cfg), nil
}

// guardStoredDownloadDir 把不可信的下载目录替换为安全兜底目录（<dataDir>/downloads）。
// 只影响返回值，不写库：写库由 Open 的 repairDownloadDir 负责，避免读路径产生副作用。
func (s *Store) guardStoredDownloadDir(cfg config.AppConfig) config.AppConfig {
	rootErr := validateTrustedDownloadRoot(cfg.DownloadDir)
	if rootErr == nil {
		return cfg
	}
	fallback := s.fallbackDownloadDir
	if strings.TrimSpace(fallback) == "" || validateTrustedDownloadRoot(fallback) != nil {
		s.warnUnsafeDownloadDir(cfg.DownloadDir, rootErr)
		return cfg
	}
	s.warnUnsafeDownloadDir(cfg.DownloadDir, rootErr)
	cfg.DownloadDir = fallback
	return cfg
}

func (s *Store) GetConfig(ctx context.Context) (config.AppConfig, error) {
	stored, err := s.GetStoredConfig(ctx)
	if err != nil {
		return config.AppConfig{}, err
	}
	cfg := config.ApplyEnvOverrides(stored)
	// env 覆盖的下载目录同样不能是 `/`/家目录：此时忽略该 env 值，退回库内（已守卫的）
	// 有效目录。不要写库——env 是部署方的输入，重启后还可能变化。
	if config.IsEnvOverridden("DownloadDir") {
		if rootErr := validateTrustedDownloadRoot(cfg.DownloadDir); rootErr != nil {
			s.warnUnsafeDownloadDir(cfg.DownloadDir, rootErr)
			cfg.DownloadDir = stored.DownloadDir
		}
	}
	return cfg, nil
}

func (s *Store) UpdateConfig(ctx context.Context, cfg config.AppConfig) (config.AppConfig, error) {
	return s.updateConfig(ctx, cfg, nil)
}

func (s *Store) UpdateConfigPatch(ctx context.Context, fields map[string]json.RawMessage) (config.AppConfig, error) {
	return s.updateConfig(ctx, config.AppConfig{}, fields)
}

func (s *Store) updateConfig(ctx context.Context, cfg config.AppConfig, fields map[string]json.RawMessage) (config.AppConfig, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
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
	if fields != nil {
		cfg = current
		payload, err := json.Marshal(fields)
		if err != nil {
			return config.AppConfig{}, ErrValidation("配置格式无效")
		}
		if err := json.Unmarshal(payload, &cfg); err != nil {
			return config.AppConfig{}, ErrValidation("配置字段类型无效")
		}
	}

	cfg, err = config.ResolveSubmission(cfg, current)
	if err != nil {
		return config.AppConfig{}, ErrValidation(err.Error())
	}
	if err := validateTrustedDownloadRoot(cfg.DownloadDir); err != nil {
		return config.AppConfig{}, ErrValidation(err.Error())
	}
	// Keep the trusted roots with the config change; existing files stay at
	// their original absolute paths, including downloads still in flight.
	// 旧根与新根都必须通过安全校验：历史遗留的 `/`/家目录会被跳过而不是登记成可信根，
	// 否则媒体库会退化为任意文件读取面（VERIFY R-F）。
	rootsToKeep := make([]string, 0, 2)
	for _, root := range []string{current.DownloadDir, cfg.DownloadDir} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if rootErr := validateTrustedDownloadRoot(root); rootErr != nil {
			log.Printf("[storage][WARNING] 跳过不可信的下载根 %q：%v", root, rootErr)
			continue
		}
		abs, absErr := filepath.Abs(root)
		if absErr != nil {
			continue
		}
		clean := filepath.Clean(abs)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO download_roots(path, last_seen_at) VALUES (?, ?)
ON CONFLICT(path) DO UPDATE SET last_seen_at = excluded.last_seen_at`, clean, time.Now().UTC()); err != nil {
			return config.AppConfig{}, err
		}
		rootsToKeep = append(rootsToKeep, clean)
	}

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
			incremental_archive = :incremental_archive,
			file_naming_mode = :file_naming_mode,
			max_filename_length = :max_filename_length,
			storage_type = :storage_type,
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
		"incremental_archive":        cfg.IncrementalArchive,
		"file_naming_mode":           cfg.FileNamingMode,
		"max_filename_length":        cfg.MaxFilenameLength,
		"storage_type":               cfg.StorageType,
		"updated_at":                 time.Now().UTC(),
	})
	if err != nil {
		return config.AppConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return config.AppConfig{}, err
	}
	s.cachedStored = &cfg
	s.cachedStoredAt = time.Now()
	s.invalidateLibraryDownloadsCache()
	// 可信根只增不减会永久扩大媒体库的解析面；按最近使用/最长前缀保留有限条数。
	// 清理失败不影响已提交的配置保存。
	if _, err := s.PruneDownloadRoots(ctx, rootsToKeep); err != nil {
		log.Printf("storage: prune download roots: %v", err)
	}
	return cfg, nil
}

// DownloadRoots returns roots registered by successful configuration changes.
// These are used only for recorded downloads, not arbitrary library paths.
//
// 返回值保证对调用方友好且安全：每个路径都是绝对路径 + filepath.Clean 过的形态
// （调用方会直接把它交给 httpapi 的路径沙箱解析，相对路径或尾斜杠差异会让"最长前缀
// 优先"的匹配顺序退化）、已去重、已过滤不可信根（`/`、家目录），并按路径长度降序
// 返回（更具体的根先匹配）。过滤只在读侧进行，写侧由 seed/updateConfig/Prune 负责清理。
func (s *Store) DownloadRoots(ctx context.Context) ([]string, error) {
	raw := []string{}
	if err := s.db.SelectContext(ctx, &raw, `SELECT path FROM download_roots`); err != nil {
		return nil, err
	}
	roots := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		abs, err := filepath.Abs(trimmed)
		if err != nil {
			continue
		}
		clean := filepath.Clean(abs)
		if rootErr := validateTrustedDownloadRoot(clean); rootErr != nil {
			continue
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		roots = append(roots, clean)
	}
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	return roots, nil
}

// seedDownloadRoots 在启动时把"库内下载目录"和"运行时生效目录（env 优先）"幂等登记为
// 可信根。存量库升级前可能已经改过下载目录，旧目录从未登记；不播种的话这些历史媒体
// 仍然解析不到（只有再保存一次配置才会登记）。
func (s *Store) seedDownloadRoots(ctx context.Context) error {
	stored := config.AppConfig{}
	if err := s.db.GetContext(ctx, &stored, storedConfigQuery); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	stored = stored.Normalized()
	effective := config.EffectiveDownloadDir(stored)
	keep := make([]string, 0, 2)
	for _, root := range []string{stored.DownloadDir, effective} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if rootErr := validateTrustedDownloadRoot(root); rootErr != nil {
			// `/`/家目录永远不登记为可信根（即便是 env 显式指定）。
			log.Printf("[storage][WARNING] 启动时跳过不可信的下载根 %q：%v", root, rootErr)
			continue
		}
		abs, absErr := filepath.Abs(root)
		if absErr != nil {
			continue
		}
		clean := filepath.Clean(abs)
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO download_roots(path, last_seen_at) VALUES (?, ?)
ON CONFLICT(path) DO UPDATE SET last_seen_at = excluded.last_seen_at`, clean, time.Now().UTC()); err != nil {
			return err
		}
		keep = append(keep, clean)
	}
	if _, err := s.PruneDownloadRoots(ctx, keep); err != nil {
		return err
	}
	return nil
}

// PruneDownloadRoots 清理可信下载根表：删除不可信根（`/`、家目录等历史遗留值），并把
// 数量限制在 MaxDownloadRoots。keep 中的根永远保留；其余按 last_seen_at 降序（最近使用），
// 同时间按路径长度降序（更长/更具体的根优先）。返回删除行数。
func (s *Store) PruneDownloadRoots(ctx context.Context, keep []string) (int, error) {
	type rootRow struct {
		Path     string       `db:"path"`
		LastSeen sql.NullTime `db:"last_seen_at"`
	}
	rows := []rootRow{}
	if err := s.db.SelectContext(ctx, &rows, `SELECT path, last_seen_at FROM download_roots`); err != nil {
		return 0, err
	}
	keepSet := make(map[string]struct{}, len(keep))
	for _, root := range keep {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if abs, err := filepath.Abs(root); err == nil {
			keepSet[filepath.Clean(abs)] = struct{}{}
		}
	}
	type candidate struct {
		path     string
		lastSeen time.Time
		kept     bool
	}
	candidates := make([]candidate, 0, len(rows))
	remove := make([]string, 0)
	for _, row := range rows {
		clean := filepath.Clean(row.Path)
		if _, ok := keepSet[clean]; ok {
			candidates = append(candidates, candidate{path: row.Path, lastSeen: row.LastSeen.Time, kept: true})
			continue
		}
		if rootErr := validateTrustedDownloadRoot(row.Path); rootErr != nil {
			remove = append(remove, row.Path)
			continue
		}
		candidates = append(candidates, candidate{path: row.Path, lastSeen: row.LastSeen.Time})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].kept != candidates[j].kept {
			return candidates[i].kept
		}
		if !candidates[i].lastSeen.Equal(candidates[j].lastSeen) {
			return candidates[i].lastSeen.After(candidates[j].lastSeen)
		}
		if len(candidates[i].path) != len(candidates[j].path) {
			return len(candidates[i].path) > len(candidates[j].path)
		}
		return candidates[i].path < candidates[j].path
	})
	limit := MaxDownloadRoots
	if len(keepSet) > limit {
		limit = len(keepSet)
	}
	for index := limit; index < len(candidates); index++ {
		remove = append(remove, candidates[index].path)
	}
	for _, path := range remove {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM download_roots WHERE path = ?`, path); err != nil {
			return 0, err
		}
	}
	return len(remove), nil
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
	_, err := s.UpdateJobAndGet(ctx, job)
	return err
}

// UpdateJobAndGet returns the stored row, or nil when a terminal state rejected
// the update. Event publishers must use this result rather than their stale copy.
func (s *Store) UpdateJobAndGet(ctx context.Context, job Job) (*Job, error) {
	job.UpdatedAt = time.Now().UTC()
	rows, err := s.db.NamedQueryContext(ctx, `
UPDATE jobs SET
	status = :status,
	title = :title,
	progress = :progress,
	message = :message,
	error = :error,
	updated_at = :updated_at
WHERE id = :id
  AND (status NOT IN (`+terminalStatusesSQL+`) OR status = :status)
RETURNING *`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var updated Job
	if err := rows.StructScan(&updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *Store) CancelJob(ctx context.Context, id int64) (Job, error) {
	// 单条条件 UPDATE：只把非终态（=活动状态集合）任务改为 canceled，避免 GetJob→UpdateJob
	// 两步之间与 worker 的终态保存竞争（worker 把任务标为 completed 后，这里不会再覆盖）。
	job := Job{}
	err := s.db.GetContext(ctx, &job, `
UPDATE jobs SET status = ?, message = ?, progress = 1, error = '', updated_at = ?
	WHERE id = ? AND status IN (`+activeJobStatusesSQL+`)
	RETURNING *`,
		JobCanceled, "已取消", time.Now().UTC(), id)
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, err
	}
	// 已是终态或任务不存在时，返回当前状态以保留原有语义。
	return s.GetJob(ctx, id)
}

// ErrJobRetryBusy 表示原任务或同一目标的任务仍在运行或排队中。
var ErrJobRetryBusy = errors.New("任务仍在运行或排队中，不能重试")

// RetryJob 为已结束的任务创建一个同目标的新任务。检查与插入放在同一个写事务里：先读后
// 写时，双击"重试"或两个标签页同时点会各自看到终态、各插入一条，同一目标被重复归档。
// 同一目标已有排队/运行中的任务时同样拒绝。
func (s *Store) RetryJob(ctx context.Context, id int64) (Job, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback() // Commit 后为 no-op

	job := Job{}
	if err := tx.GetContext(ctx, &job, `SELECT * FROM jobs WHERE id = ?`, id); err != nil {
		return Job{}, err
	}
	if IsActiveJobStatus(job.Status) {
		return Job{}, ErrJobRetryBusy
	}
	var active int
	if err := tx.GetContext(ctx, &active, `
SELECT COUNT(*) FROM jobs WHERE kind = ? AND input = ? AND status IN (`+activeJobStatusesSQL+`)`,
		job.Kind, job.Input); err != nil {
		return Job{}, err
	}
	if active > 0 {
		return Job{}, ErrJobRetryBusy
	}
	now := time.Now().UTC()
	retried := Job{}
	if err := tx.GetContext(ctx, &retried, `
INSERT INTO jobs (kind, status, input, title, progress, message, created_at, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?, ?)
RETURNING *`, job.Kind, JobPending, job.Input, job.Title, "排队中", now, now); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return retried, nil
}

func (s *Store) RequeueInterruptedJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryxContext(ctx, `
	UPDATE jobs SET
		status = ?,
		progress = 0,
		message = ?,
		error = '',
		updated_at = ?
	WHERE status IN (`+interruptedStatusesSQL+`)
	RETURNING *`,
		JobPending, "上次运行中断，等待恢复", time.Now().UTC())
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
	schedule, err := prepareArchiveScheduleForSave(schedule)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	// 与 UpdateConfig 同理：next_run_at 是从当前的 enabled / interval_minutes 推算出来的，
	// "读当前值 → 推算 → 写回"必须在一个事务里，否则两个并发编辑会各自读到旧值，
	// 后写入的一方按过期的开关/间隔重算 next_run_at。
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	defer tx.Rollback() // Commit 后为 no-op

	current := ArchiveSchedule{}
	if err := tx.GetContext(ctx, &current, `SELECT * FROM archive_schedules WHERE id = ?`, schedule.ID); err != nil {
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
	// claim_version 只在 next_run_at 真的变化时递增：编辑目标清单（间隔不变）应让调度器
	// 手里的快照继续有效，并按库里最新的清单执行（见 TestScheduleClaimUsesItemsFromClaimTransaction）；
	// 而改开关/间隔会移动 next_run_at，此时旧快照必须失效。
	claimVersion := current.ClaimVersion
	if !nextRunAt.Equal(current.NextRunAt) {
		claimVersion++
	}
	if err := tx.GetContext(ctx, &schedule, `
UPDATE archive_schedules SET
	name = ?,
	enabled = ?,
	interval_minutes = ?,
	items_json = ?,
	next_run_at = ?,
	claim_version = ?,
	updated_at = ?
WHERE id = ?
RETURNING *`,
		schedule.Name, schedule.Enabled, schedule.IntervalMinutes, schedule.ItemsJSON,
		nextRunAt, claimVersion, now, schedule.ID); err != nil {
		return ArchiveSchedule{}, err
	}
	if err := tx.Commit(); err != nil {
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
	// 移动 next_run_at 意味着"旧的领取条件"失效：同时递增 claim_version，让调度器手里的
	// 旧快照无法再领取（例如自动重试退避把计划推后 10 分钟时，不能按旧快照立即触发）。
	err := s.db.GetContext(ctx, &schedule, `
UPDATE archive_schedules SET next_run_at = ?, claim_version = claim_version + 1, updated_at = ? WHERE id = ?
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
WHERE id IN (?) AND status IN (`+activeJobStatusesSQL+`)`, ids)
	if err != nil {
		return false, err
	}
	var count int
	err = s.db.GetContext(ctx, &count, s.db.Rebind(query), args...)
	return count > 0, err
}

// CreateJobsForArchiveSchedule 手动"立即运行"：领取计划并按其目标创建任务。已暂停的
// 计划也允许手动运行。
func (s *Store) CreateJobsForArchiveSchedule(ctx context.Context, schedule ArchiveSchedule, runAt time.Time) ([]Job, error) {
	return s.claimArchiveScheduleJobs(ctx, schedule, runAt, false)
}

// CreateScheduledArchiveJobs 调度器到期触发：与手动运行相同，但只领取仍启用的计划。
// 调度器从 ListDueArchiveSchedules 读到快照后、领取前，用户可能刚停用了计划，而
// 停用会保留 next_run_at，只比对 next_run_at 会让已停用的计划再跑一次。
func (s *Store) CreateScheduledArchiveJobs(ctx context.Context, schedule ArchiveSchedule, runAt time.Time) ([]Job, error) {
	return s.claimArchiveScheduleJobs(ctx, schedule, runAt, true)
}

// claimArchiveScheduleJobs 以 claim_version 做乐观锁领取计划，并在同一事务里按库中最新的
// 目标清单与间隔创建任务。调用方手里的 schedule 只是快照：编辑目标清单（间隔不变、不移动
// next_run_at）不会改变 claim_version，因此仍会按库里最新的 Items 执行（不会被旧快照的
// 目标继续归档）；而停用/改间隔/重新排期会递增版本，旧快照的领取自然失败。
//
// 旧实现比较 next_run_at 时间是否相等：DATETIME 实际以 TEXT 存储，格式/精度/时区表示的
// 任何差异都会让领取永远失败（上层把 AlreadyClaimed 当成"已被领走"静默跳过），因此改为
// 与时间表示无关的整数版本号。
func (s *Store) claimArchiveScheduleJobs(ctx context.Context, snapshot ArchiveSchedule, runAt time.Time, requireEnabled bool) ([]Job, error) {
	runAt = runAt.UTC()
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current := ArchiveSchedule{}
	query := `SELECT * FROM archive_schedules WHERE id = ?`
	if requireEnabled {
		query += ` AND enabled = 1`
	}
	if err := tx.GetContext(ctx, &current, query, snapshot.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrArchiveScheduleAlreadyClaimed
		}
		return nil, err
	}
	if current.ClaimVersion != snapshot.ClaimVersion {
		return nil, ErrArchiveScheduleAlreadyClaimed
	}
	current, err = hydrateArchiveSchedule(current)
	if err != nil {
		return nil, err
	}
	schedule, err := prepareArchiveScheduleForSave(current)
	if err != nil {
		return nil, err
	}
	nextRunAt := nextArchiveScheduleRun(runAt, schedule.IntervalMinutes)
	// 事务开头已取写锁（_txlock=immediate），SELECT 与 UPDATE 之间不会有并发写入。
	if _, err := tx.ExecContext(ctx, `
UPDATE archive_schedules SET
	last_run_at = ?,
	next_run_at = ?,
	last_job_ids = '[]',
	claim_version = claim_version + 1,
	updated_at = ?
WHERE id = ?`,
		runAt, nextRunAt, runAt, schedule.ID); err != nil {
		return nil, err
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
	// 媒体身份键：同一媒体内容的稳定键，供跨推文/跨目录复用查找（见 FindDownloadsByMediaKey）。
	record.MediaKey = downloader.MediaIdentity(record.MediaURL)
	if strings.TrimSpace(record.TweetID) != "" {
		err := s.db.GetContext(ctx, &record, `
	INSERT INTO downloads (job_id, tweet_id, media_url, media_key, content_hash, preview_url, file_path, bytes, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(tweet_id, media_url) WHERE tweet_id <> '' DO UPDATE SET
		media_key = excluded.media_key,
		content_hash = CASE WHEN excluded.content_hash <> '' THEN excluded.content_hash ELSE downloads.content_hash END,
		preview_url = CASE WHEN excluded.preview_url <> '' THEN excluded.preview_url ELSE downloads.preview_url END,
		file_path = excluded.file_path,
		bytes = excluded.bytes,
		created_at = excluded.created_at
	RETURNING *`,
			record.JobID, record.TweetID, record.MediaURL, record.MediaKey, record.ContentHash, record.PreviewURL, record.FilePath, record.Bytes, now)
		if err == nil {
			s.invalidateLibraryDownloadsCache()
		}
		return record, err
	}
	// tweet_id 为空（直接媒体 URL 任务）：按 media_url 去重，避免同一 URL 重复跑产生重复行。
	err := s.db.GetContext(ctx, &record, `
INSERT INTO downloads (job_id, tweet_id, media_url, media_key, content_hash, preview_url, file_path, bytes, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(media_url) WHERE tweet_id = '' AND media_url <> '' DO UPDATE SET
		media_key = excluded.media_key,
		content_hash = CASE WHEN excluded.content_hash <> '' THEN excluded.content_hash ELSE downloads.content_hash END,
		preview_url = CASE WHEN excluded.preview_url <> '' THEN excluded.preview_url ELSE downloads.preview_url END,
		file_path = excluded.file_path,
	bytes = excluded.bytes,
	created_at = excluded.created_at
RETURNING *`,
		record.JobID, record.TweetID, record.MediaURL, record.MediaKey, record.ContentHash, record.PreviewURL, record.FilePath, record.Bytes, now)
	if err == nil {
		s.invalidateLibraryDownloadsCache()
	}
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

// ListLibraryDownloads enriches archive records with the owning user when the
// file was saved under a user entity directory. Older records remain valid and
// simply return empty user fields when no entity can be inferred.
//
// 用户归属在 Go 侧按路径前缀匹配完成（而非 SQL LIKE）：目录名来自用户显示名，
// 常含 `_`/`%` 等字符，LIKE 会把它们当通配符产生误归属。匹配不区分大小写，
// 以兼容 macOS/Windows 等大小写不敏感文件系统记录的路径。
// 注意：从磁盘删除的文件在下一次缓存失效（最长 5 分钟）前仍会出现在结果里。
func (s *Store) ListLibraryDownloads(ctx context.Context, limit int) ([]DownloadRecord, error) {
	items, _, err := s.ListLibraryDownloadsWithStatus(ctx, limit)
	return items, err
}

func (s *Store) ListLibraryDownloadsWithStatus(ctx context.Context, limit int) ([]DownloadRecord, bool, error) {
	if limit <= 0 || limit > MaxLibraryDownloadsLimit {
		limit = MaxLibraryDownloadsLimit
	}
	// Only cache misses wait for the builder; recheck after acquiring its lock.
	for attempt := 0; attempt < 2; attempt++ {
		s.libraryCacheMu.RLock()
		if s.libraryCacheOK && time.Since(s.libraryCacheAt) < libraryDownloadsCacheTTL && s.libraryCacheLimit >= limit {
			cached := s.libraryCache
			truncated := s.libraryCacheTruncated || len(cached) > limit
			if len(cached) > limit {
				cached = cached[:limit]
			}
			items := append([]DownloadRecord(nil), cached...)
			s.libraryCacheMu.RUnlock()
			return items, truncated, nil
		}
		s.libraryCacheMu.RUnlock()
		if attempt == 0 {
			s.libraryBuildMu.Lock()
			defer s.libraryBuildMu.Unlock()
		}
	}

	generation := s.libraryCacheGen.Load()

	items := []DownloadRecord{}
	err := s.db.SelectContext(ctx, &items, `
SELECT d.* FROM downloads d
ORDER BY d.created_at DESC, d.id DESC LIMIT ?`, limit+1)
	if err != nil {
		return nil, false, err
	}
	if err := s.attributeLibraryUsers(ctx, items); err != nil {
		return nil, false, err
	}
	for index := range items {
		if items[index].PreviewURL == "" {
			items[index].PreviewURL = deriveVideoPreviewURL(items[index].MediaURL)
		}
	}
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, false, err
	}
	if cfg.StorageType != config.StorageLocal || strings.TrimSpace(cfg.DownloadDir) == "" {
		truncated := len(items) > limit
		if truncated {
			items = items[:limit]
		}
		s.cacheLibraryDownloads(items, limit, truncated, generation)
		return append([]DownloadRecord(nil), items...), truncated, nil
	}
	root, err := filepath.Abs(cfg.DownloadDir)
	if err != nil {
		return nil, false, err
	}
	var recordedPaths []string
	if err := s.db.SelectContext(ctx, &recordedPaths, `SELECT file_path FROM downloads`); err != nil {
		return nil, false, err
	}
	known := make(map[string]struct{}, len(recordedPaths))
	for _, recordedPath := range recordedPaths {
		if path, err := filepath.Abs(recordedPath); err == nil {
			known[filepath.Clean(path)] = struct{}{}
		}
	}
	items, err = collectOrphanLibraryFiles(ctx, root, known, items)
	if err != nil {
		// 请求被取消：不完整的结果不能写进缓存。
		return nil, false, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	s.cacheLibraryDownloads(items, limit, truncated, generation)
	return append([]DownloadRecord(nil), items...), truncated, nil
}

// collectOrphanLibraryFiles 遍历下载目录，把不在 known 中的媒体文件（没有下载记录的
// 孤儿文件）追加到 items。遍历响应 ctx 取消：否则用户切走页面后，服务端仍会走完整个
// 下载目录。
func collectOrphanLibraryFiles(ctx context.Context, root string, known map[string]struct{}, items []DownloadRecord) ([]DownloadRecord, error) {
	caseVariants := make(map[string][]string, len(known))
	for recorded := range known {
		key := strings.ToLower(recorded)
		caseVariants[key] = append(caseVariants[key], recorded)
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		if !IsLibraryMediaPath(path) || strings.HasSuffix(strings.ToLower(path), ".preview.jpg") {
			return nil
		}
		cleaned := filepath.Clean(path)
		if _, exists := known[cleaned]; exists {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		for _, recorded := range caseVariants[strings.ToLower(cleaned)] {
			if recordedInfo, err := os.Stat(recorded); err == nil && os.SameFile(info, recordedInfo) {
				return nil
			}
		}
		items = append(items, DownloadRecord{FilePath: cleaned, Bytes: info.Size(), CreatedAt: info.ModTime().UTC()})
		known[cleaned] = struct{}{}
		return nil
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	_ = err // 其余遍历错误（根目录不存在等）沿用原行为：忽略，返回已收集的结果。
	return items, nil
}

// attributeLibraryUsers 按 file_path 前缀为下载记录补充归属用户。
// 多个实体同时命中时沿用原 SQL 行为：取 id 最小的实体。
func (s *Store) attributeLibraryUsers(ctx context.Context, items []DownloadRecord) error {
	if len(items) == 0 {
		return nil
	}
	type entityPrefix struct {
		ScreenName string `db:"screen_name"`
		UserName   string `db:"user_name"`
		Prefix     string `db:"prefix"` // 小写、正斜杠化的 "parent_dir/name/" 前缀
	}
	entities := []entityPrefix{}
	err := s.db.SelectContext(ctx, &entities, `
SELECT COALESCE(u.screen_name, '') AS screen_name, COALESCE(u.name, '') AS user_name,
  REPLACE(ue.parent_dir, char(92), '/') || '/' || REPLACE(ue.name, char(92), '/') || '/' AS prefix
FROM user_entities ue JOIN users u ON u.id = ue.user_id
ORDER BY ue.id`)
	if err != nil {
		return err
	}
	if len(entities) == 0 {
		return nil
	}
	prefixes := make(map[string]int, len(entities))
	for index, entity := range entities {
		prefix := strings.ToLower(entity.Prefix)
		if _, exists := prefixes[prefix]; !exists {
			prefixes[prefix] = index
		}
	}
	for index := range items {
		if items[index].UserScreenName != "" {
			continue
		}
		pathLower := strings.ToLower(strings.ReplaceAll(items[index].FilePath, `\`, `/`))
		match := len(entities)
		for slash := strings.LastIndexByte(pathLower, '/'); slash >= 0; slash = strings.LastIndexByte(pathLower[:slash], '/') {
			if candidate, ok := prefixes[pathLower[:slash+1]]; ok && candidate < match {
				match = candidate
			}
		}
		if match < len(entities) {
			items[index].UserScreenName = entities[match].ScreenName
			items[index].UserName = entities[match].UserName
		}
	}
	return nil
}

func (s *Store) cacheLibraryDownloads(items []DownloadRecord, limit int, truncated bool, generation uint64) {
	s.libraryCacheMu.Lock()
	if s.libraryCacheGen.Load() != generation {
		// 查询期间发生过失效：本结果可能缺少新记录，放弃回填，避免脏缓存。
		s.libraryCacheMu.Unlock()
		return
	}
	s.libraryCache = append([]DownloadRecord(nil), items...)
	s.libraryCacheAt = time.Now()
	s.libraryCacheOK = true
	s.libraryCacheLimit = limit
	s.libraryCacheTruncated = truncated
	s.libraryCacheMu.Unlock()
}

func (s *Store) invalidateLibraryDownloadsCache() {
	s.libraryCacheGen.Add(1)
	s.libraryCacheMu.Lock()
	s.libraryCache = nil
	s.libraryCacheAt = time.Time{}
	s.libraryCacheOK = false
	s.libraryCacheLimit = 0
	s.libraryCacheTruncated = false
	s.libraryCacheMu.Unlock()
}

// IsLibraryMediaPath reports whether path has a media extension that the
// gallery indexes and the library file endpoint may serve. Preview temp files
// (.preview-* and *.preview.jpg) are excluded.
func IsLibraryMediaPath(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".preview-") || strings.HasSuffix(strings.ToLower(base), ".preview.jpg") {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".mp4", ".mov", ".m4v", ".webm", ".ogv":
		return true
	default:
		return false
	}
}

// VideoPreviewURL returns the public Twitter thumbnail URL for a video CDN URL.
func VideoPreviewURL(mediaURL string) string { return deriveVideoPreviewURL(mediaURL) }

// deriveVideoPreviewURL maps Twitter's video CDN path to its public thumbnail
// path. It provides posters for records created before preview_url was stored.
//
// 现代 X 视频 CDN 路径形如 video.twimg.com/ext_tw_video/<id>/pu/vid/avc1/1280x720/<stem>.mp4，
// 对应缩略图为 pbs.twimg.com/ext_tw_video_thumb/<id>/pu/img/<stem>.jpg：目录段
// ext_tw_video→ext_tw_video_thumb、视频轨目录 /vid/<codec>/<分辨率>/→/img/、扩展名
// mp4→jpg。旧版直接把 .mp4 换成 .jpg，得到的 /vid/…jpg 地址并不存在，是历史媒体
// 缩略图大面积 404 的原因。GIF（tweet_video）缩略图约定为 .png。
func deriveVideoPreviewURL(mediaURL string) string {
	u, err := url.Parse(mediaURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "video.twimg.com" && !strings.HasSuffix(host, ".video.twimg.com") {
		return ""
	}
	lowerPath := strings.ToLower(u.Path)
	if !strings.HasSuffix(lowerPath, ".mp4") {
		return ""
	}
	thumbExt := ".jpg"
	switch {
	case strings.Contains(u.Path, "/ext_tw_video/"):
		u.Path = strings.Replace(u.Path, "/ext_tw_video/", "/ext_tw_video_thumb/", 1)
	case strings.Contains(u.Path, "/amplify_video/"):
		u.Path = strings.Replace(u.Path, "/amplify_video/", "/amplify_video_thumb/", 1)
	case strings.Contains(u.Path, "/tweet_video/"):
		u.Path = strings.Replace(u.Path, "/tweet_video/", "/tweet_video_thumb/", 1)
		thumbExt = ".png"
	default:
		return ""
	}
	u.Path = videoThumbImagePath(u.Path, thumbExt)
	u.RawQuery = "name=small"
	u.Host = "pbs.twimg.com"
	return u.String()
}

// videoThumbImagePath rewrites the video track directory (`/vid/<codec>/<res>/`
// on modern URLs) to the thumbnail directory `/img/`, keeping only the file
// stem. URLs without a `/vid/` segment keep their directory and just swap the
// extension (older amplify/tweet_video layouts).
func videoThumbImagePath(videoPath string, thumbExt string) string {
	slash := strings.LastIndex(videoPath, "/")
	dir, file := videoPath[:slash+1], videoPath[slash+1:]
	stem := strings.TrimSuffix(file, filepath.Ext(file))
	if index := strings.Index(strings.ToLower(dir), "/vid/"); index >= 0 {
		dir = dir[:index+1] + "img/"
	}
	return dir + stem + thumbExt
}

// UpdateDownloadPreviewURL backfills a stored preview URL for records created
// before preview_url was captured. It never reorders the record: only
// preview_url changes, so gallery 排序与创建时间保持不变。
func (s *Store) UpdateDownloadPreviewURL(ctx context.Context, id int64, previewURL string) error {
	if id <= 0 || strings.TrimSpace(previewURL) == "" {
		return nil
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET preview_url = ? WHERE id = ? AND preview_url <> ?`, previewURL, id, previewURL)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed > 0 {
		s.invalidateLibraryDownloadsCache()
	}
	return nil
}

// RewriteDownloadPathPrefix 在目录被重命名（如用户改显示名）后，把位于 oldDir 之下的
// 下载记录的 file_path 改写到 newDir 之下，返回改写行数。前缀用 substr 等值比较而
// 不是 LIKE：目录名里常见的 "_"、"%" 在 LIKE 中是通配符。SQLite 的 substr 对 TEXT
// 按字符计数，长度必须用 rune 数而不是字节数，否则含中文/emoji 的显示名永远匹配不上。
func (s *Store) RewriteDownloadPathPrefix(ctx context.Context, oldDir string, newDir string) (int64, error) {
	oldPrefix := strings.TrimRight(filepath.Clean(oldDir), `/\`) + string(filepath.Separator)
	newPrefix := strings.TrimRight(filepath.Clean(newDir), `/\`) + string(filepath.Separator)
	if oldPrefix == newPrefix {
		return 0, nil
	}
	prefixChars := utf8.RuneCountInString(oldPrefix)

	result, err := s.db.ExecContext(ctx, `
UPDATE downloads SET file_path = ? || substr(file_path, ?)
WHERE substr(file_path, 1, ?) = ?`, newPrefix, prefixChars+1, prefixChars, oldPrefix)
	if err != nil {
		return 0, err
	}
	total, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if total > 0 {
		s.invalidateLibraryDownloadsCache()
	}
	return total, nil
}

// GetDownload returns one archived media record by its stable database ID.
// Unknown and non-positive IDs both yield (nil, nil) so HTTP callers can map
// them uniformly to 404.
func (s *Store) GetDownload(ctx context.Context, id int64) (*DownloadRecord, error) {
	if id <= 0 {
		return nil, nil
	}
	var record DownloadRecord
	if err := s.db.GetContext(ctx, &record, `SELECT * FROM downloads WHERE id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &record, nil
}

// MaxJobFilesPerKind 是单个任务的文件列表里下载记录 / 失败记录各自返回的条数上限。
// JobFiles 的结果会被整体序列化进一个 JSON 响应，五万个媒体的归档任务不加限制会产生
// 极大的响应体。前端不分页、直接展示全部并按数组长度显示"文件数"，因此这里不取其他
// 列表方法的 100~500，而给一个足够覆盖真实任务规模的上限，只截断异常量级的任务。
const MaxJobFilesPerKind = 5000

func (s *Store) JobFiles(ctx context.Context, jobID int64) ([]DownloadRecord, []FailedMedia, bool, error) {
	type jobFileRow struct {
		SortOrder  int            `db:"sort_order"`
		RecordKind string         `db:"record_kind"`
		ID         int64          `db:"id"`
		JobID      int64          `db:"job_id"`
		TweetID    sql.NullString `db:"tweet_id"`
		MediaURL   sql.NullString `db:"media_url"`
		PreviewURL sql.NullString `db:"preview_url"`
		FilePath   sql.NullString `db:"file_path"`
		Bytes      sql.NullInt64  `db:"bytes"`
		Error      sql.NullString `db:"error"`
		CreatedAt  sql.NullTime   `db:"created_at"`
	}
	rows := []jobFileRow{}
	// 两个明细分支各自限流，而不是给整条 UNION ALL 加一个 LIMIT：后者在下载记录超额时
	// 会把 failed_media 整段挤掉（排序键 sort_order 让 downloads 永远排在 failed 之前）。
	err := s.db.SelectContext(ctx, &rows, `
SELECT * FROM (
	SELECT 1 AS sort_order, 'download' AS record_kind, id, job_id,
		tweet_id, media_url, preview_url, file_path, bytes, NULL AS error, created_at
	FROM downloads
	WHERE job_id = ?
	ORDER BY created_at DESC
	LIMIT ?
)
UNION ALL
SELECT * FROM (
	SELECT 2 AS sort_order, 'failed' AS record_kind, id, job_id,
		NULL AS tweet_id, media_url, NULL AS preview_url, NULL AS file_path, NULL AS bytes, error, created_at
	FROM failed_media
	WHERE job_id = ?
	ORDER BY created_at DESC
	LIMIT ?
)
UNION ALL
SELECT 0 AS sort_order, 'job' AS record_kind, id, id AS job_id,
	NULL AS tweet_id, NULL AS media_url, NULL AS preview_url, NULL AS file_path, NULL AS bytes,
	NULL AS error, NULL AS created_at
FROM jobs
WHERE id = ?
ORDER BY sort_order, created_at DESC`, jobID, MaxJobFilesPerKind+1, jobID, MaxJobFilesPerKind+1, jobID)
	if err != nil {
		return nil, nil, false, err
	}
	if len(rows) == 0 {
		return nil, nil, false, sql.ErrNoRows
	}

	downloads := make([]DownloadRecord, 0, len(rows))
	failed := make([]FailedMedia, 0, len(rows))
	truncated := false
	for _, row := range rows {
		switch row.RecordKind {
		case "job":
			continue
		case "download":
			if len(downloads) == MaxJobFilesPerKind {
				truncated = true
				continue
			}
			downloads = append(downloads, DownloadRecord{
				ID:         row.ID,
				JobID:      row.JobID,
				TweetID:    row.TweetID.String,
				MediaURL:   row.MediaURL.String,
				PreviewURL: row.PreviewURL.String,
				FilePath:   row.FilePath.String,
				Bytes:      row.Bytes.Int64,
				CreatedAt:  row.CreatedAt.Time,
			})
		case "failed":
			if len(failed) == MaxJobFilesPerKind {
				truncated = true
				continue
			}
			failed = append(failed, FailedMedia{
				ID:        row.ID,
				JobID:     row.JobID,
				MediaURL:  row.MediaURL.String,
				Error:     row.Error.String,
				CreatedAt: row.CreatedAt.Time,
			})
		default:
			return nil, nil, false, fmt.Errorf("unknown job file record kind %q", row.RecordKind)
		}
	}
	return downloads, failed, truncated, nil
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

// downloads 的去重键由两个部分唯一索引覆盖：tweet_id 非空走
// idx_downloads_tweet_media_unique，tweet_id 为空（直接媒体 URL 任务）走
// idx_downloads_media_url_unique。SQLite 只在查询能"证明"满足部分索引谓词时才会使用
// 该索引，而绑定参数证明不了，因此下面按 tweet_id 是否为空拆成两条查询，把索引谓词
// 写成字面量。否则每次查找都退化为 downloads 全表扫描（5 万行约 2.5ms，而归档时每个
// 媒体文件都要查一次）。tweet_id = ? 已隐含 tweet_id <> ”，语义不变。
const (
	downloadByTweetMediaQuery = `SELECT * FROM downloads WHERE tweet_id = ? AND media_url = ? AND tweet_id <> ''`
	downloadByMediaURLQuery   = `SELECT * FROM downloads WHERE tweet_id = '' AND media_url = ? AND media_url <> ''`
)

func (s *Store) GetDownloadByTweetMedia(ctx context.Context, tweetID string, mediaURL string) (*DownloadRecord, error) {
	record := DownloadRecord{}
	query, args := downloadByTweetMediaQuery, []any{tweetID, mediaURL}
	if tweetID == "" {
		query, args = downloadByMediaURLQuery, []any{mediaURL}
	}
	err := s.db.GetContext(ctx, &record, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// MaxMediaCopyLookup 是单页上限；调用方可继续翻页查找仍存在的副本。
const MaxMediaCopyLookup = 500

func normalizedMediaCopyLimit(limit int) int {
	if limit <= 0 || limit > MaxMediaCopyLookup {
		return MaxMediaCopyLookup
	}
	return limit
}

// FindDownloadsByMediaKey 按媒体身份键回查已归档的副本，用于跨推文复用同一份本地文件。
//
// 按 file_path 归并（每个路径取最小 id 的记录）：同一份媒体被转推/引用时会写入多条
// downloads 记录，但它们复用同一个 file_path。不归并时这些重复记录会挤满 LIMIT，
// 把真正还在磁盘上的那份文件挤出结果之外，导致调用方误判"没有副本"而重新下载一次。
// 记录里的文件未必仍然存在，调用方必须自行校验。
func (s *Store) FindDownloadsByMediaKey(ctx context.Context, mediaKey string, limit int) ([]DownloadRecord, error) {
	return s.FindDownloadsByMediaKeyPage(ctx, mediaKey, limit, 0)
}

func (s *Store) FindDownloadsByMediaKeyPage(ctx context.Context, mediaKey string, limit int, offset int) ([]DownloadRecord, error) {
	mediaKey = strings.TrimSpace(mediaKey)
	if mediaKey == "" {
		return nil, nil
	}
	if offset < 0 {
		offset = 0
	}
	items := []DownloadRecord{}
	err := s.db.SelectContext(ctx, &items, `
SELECT * FROM downloads
WHERE id IN (
	SELECT MIN(id) FROM downloads
	WHERE media_key = ? AND file_path <> ''
	GROUP BY file_path
)
ORDER BY id ASC
LIMIT ? OFFSET ?`, mediaKey, normalizedMediaCopyLimit(limit), offset)
	return items, err
}

// FindDownloadsByContentHash 按内容哈希回查已归档的副本，用于下载后发现同样字节的文件
// 已经存在时删掉本次多余的副本。与 FindDownloadsByMediaKey 同样按 file_path 归并，
// 避免重复记录把仍在磁盘上的那份文件挤出 LIMIT。
func (s *Store) FindDownloadsByContentHash(ctx context.Context, contentHash string, limit int) ([]DownloadRecord, error) {
	return s.FindDownloadsByContentHashPage(ctx, contentHash, limit, 0)
}

func (s *Store) FindDownloadsByContentHashPage(ctx context.Context, contentHash string, limit int, offset int) ([]DownloadRecord, error) {
	contentHash = strings.TrimSpace(contentHash)
	if contentHash == "" {
		return nil, nil
	}
	if offset < 0 {
		offset = 0
	}
	items := []DownloadRecord{}
	err := s.db.SelectContext(ctx, &items, `
SELECT * FROM downloads
WHERE id IN (
	SELECT MIN(id) FROM downloads
	WHERE content_hash = ? AND file_path <> ''
	GROUP BY file_path
)
ORDER BY id ASC
LIMIT ? OFFSET ?`, contentHash, normalizedMediaCopyLimit(limit), offset)
	return items, err
}

type mediaDownloadStateRow struct {
	Unavailable int            `db:"unavailable"`
	ID          sql.NullInt64  `db:"id"`
	JobID       sql.NullInt64  `db:"job_id"`
	TweetID     sql.NullString `db:"tweet_id"`
	MediaURL    sql.NullString `db:"media_url"`
	ContentHash sql.NullString `db:"content_hash"`
	PreviewURL  sql.NullString `db:"preview_url"`
	FilePath    sql.NullString `db:"file_path"`
	Bytes       sql.NullInt64  `db:"bytes"`
	CreatedAt   sql.NullTime   `db:"created_at"`
}

// 同 downloadByTweetMediaQuery：JOIN 条件里也必须带上部分索引自己的谓词字面量，
// 否则 downloads 侧是 SCAN 而不是 SEARCH。
const (
	mediaDownloadStateByTweetQuery = `
SELECT
	CASE WHEN um.media_url IS NOT NULL THEN 1 ELSE 0 END AS unavailable,
	d.id, d.job_id, d.tweet_id, d.media_url, d.content_hash, d.preview_url, d.file_path, d.bytes, d.created_at
FROM (SELECT ? AS tweet_id, ? AS media_url) request
LEFT JOIN unavailable_media um ON um.media_url = request.media_url
LEFT JOIN downloads d ON d.tweet_id = request.tweet_id AND d.media_url = request.media_url AND d.tweet_id <> ''`
	mediaDownloadStateByMediaURLQuery = `
SELECT
	CASE WHEN um.media_url IS NOT NULL THEN 1 ELSE 0 END AS unavailable,
	d.id, d.job_id, d.tweet_id, d.media_url, d.content_hash, d.preview_url, d.file_path, d.bytes, d.created_at
FROM (SELECT ? AS media_url) request
LEFT JOIN unavailable_media um ON um.media_url = request.media_url
LEFT JOIN downloads d ON d.tweet_id = '' AND d.media_url = request.media_url AND d.media_url <> ''`
)

func (s *Store) GetMediaDownloadState(ctx context.Context, tweetID string, mediaURL string) (*DownloadRecord, bool, error) {
	row := mediaDownloadStateRow{}
	query, args := mediaDownloadStateByTweetQuery, []any{tweetID, mediaURL}
	if tweetID == "" {
		query, args = mediaDownloadStateByMediaURLQuery, []any{mediaURL}
	}
	err := s.db.GetContext(ctx, &row, query, args...)
	if err != nil {
		return nil, false, err
	}
	if !row.ID.Valid {
		return nil, row.Unavailable != 0, nil
	}
	return &DownloadRecord{
		ID:          row.ID.Int64,
		JobID:       row.JobID.Int64,
		TweetID:     row.TweetID.String,
		MediaURL:    row.MediaURL.String,
		ContentHash: row.ContentHash.String,
		PreviewURL:  row.PreviewURL.String,
		FilePath:    row.FilePath.String,
		Bytes:       row.Bytes.Int64,
		CreatedAt:   row.CreatedAt.Time,
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

// UpdateUserEntityLastSeenTweet 记录增量归档（可配置开关）使用的早停游标。
// 无论开关是否开启，manager 在每轮归档后都会写入，保证切换开关后游标可用。
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

const (
	hasDownloadByTweetMediaQuery = `SELECT COUNT(*) FROM downloads WHERE tweet_id = ? AND media_url = ? AND tweet_id <> ''`
	hasDownloadByMediaURLQuery   = `SELECT COUNT(*) FROM downloads WHERE tweet_id = '' AND media_url = ? AND media_url <> ''`
)

func (s *Store) HasDownload(ctx context.Context, tweetID string, mediaURL string) (bool, error) {
	var count int
	// 谓词同 downloadByTweetMediaQuery，避免全表扫描。
	query, args := hasDownloadByTweetMediaQuery, []any{tweetID, mediaURL}
	if tweetID == "" {
		query, args = hasDownloadByMediaURLQuery, []any{mediaURL}
	}
	err := s.db.GetContext(ctx, &count, query, args...)
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

// TouchFailedTweet 记录一次重试失败：刷新 error 与 updated_at，递增 retry_count 并
// 按指数退避设置 next_retry_at（10min * 2^retry_count，上限 24h），让该条目在冷却期内
// 不再被 ListFailedTweets 选中，避免持续失败的条目占住每次重试的名额。
//
// 退避时间在 Go 侧计算后绑定参数（而不是 SQL 里的 datetime(...)）：同一列其它写入是
// 驱动绑定的 time.Time，混用两种文本格式会让任何 SQL 时间函数（julianday 等）对其中
// 一种返回 0，且难以排查。读取 retry_count 与写入放在同一事务里，避免并发重试时
// 两次都按旧计数计算。
func (s *Store) TouchFailedTweet(ctx context.Context, id int64, errMessage string) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var retryCount int
	if err := tx.GetContext(ctx, &retryCount, `SELECT retry_count FROM failed_tweets WHERE id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // 与旧实现的 UPDATE ... WHERE id = ? 语义一致：不存在视为无需处理
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE failed_tweets SET
	error = ?,
	retry_count = retry_count + 1,
	next_retry_at = ?,
	updated_at = ?
WHERE id = ?`, errMessage, now.Add(failedTweetRetryBackoff(retryCount)), now, id); err != nil {
		return err
	}
	return tx.Commit()
}

// failedTweetRetryBackoff 返回第 retryCount 次失败后的冷却时长：10min * 2^retryCount，
// 上限 24h（与旧 SQL 公式 10 * (1 << min(retry_count, 8))、cap 1440min 一致）。
func failedTweetRetryBackoff(retryCount int) time.Duration {
	const (
		base    = 10 * time.Minute
		maxWait = 24 * time.Hour
	)
	steps := retryCount
	if steps < 0 {
		steps = 0
	}
	if steps > 8 {
		steps = 8
	}
	wait := base << steps
	if wait > maxWait {
		wait = maxWait
	}
	return wait
}

func (s *Store) ListFailedTweets(ctx context.Context, limit int) ([]FailedTweet, error) {
	return s.ListFailedTweetsForRetry(ctx, limit, false)
}

// ReserveAutomaticRetryJob 复用同一条"自动重试失败推文"任务行，避免每轮重试都 INSERT 新行
// 导致 jobs 表膨胀：
//   - 没有该行：插入一条 pending 任务并返回；
//   - 已有 pending/resolving/downloading 行：返回 (nil, nil)，表示已排队/正在跑；
//   - 已有终态行：把该行重置为 pending（progress=0、清 error、刷新 message/updated_at）
//     并返回。
//
// "读取 + 重置/插入"在同一 BEGIN IMMEDIATE 事务里完成，并发调用只有一个能拿到任务。
func (s *Store) ReserveAutomaticRetryJob(ctx context.Context) (*Job, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current := Job{}
	err = tx.GetContext(ctx, &current, `
SELECT * FROM jobs WHERE kind = ? AND input = ? ORDER BY id DESC LIMIT 1`,
		JobKindFailedRetry, AutomaticFailedRetryInput)
	if errors.Is(err, sql.ErrNoRows) {
		inserted := Job{}
		if err := tx.GetContext(ctx, &inserted, `
INSERT INTO jobs(kind, status, input, title, progress, message, error, created_at, updated_at)
VALUES (?, ?, ?, '自动重试失败推文', 0, '排队中', '', ?, ?)
RETURNING *`, JobKindFailedRetry, JobPending, AutomaticFailedRetryInput, now, now); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &inserted, nil
	}
	if err != nil {
		return nil, err
	}
	if IsActiveJobStatus(current.Status) {
		// 已有排队/运行中的重试任务：调用方直接返回，不重复入队。
		return nil, tx.Commit()
	}
	reset := Job{}
	if err := tx.GetContext(ctx, &reset, `
UPDATE jobs SET status = ?, progress = 0, message = ?, error = '', updated_at = ?
WHERE id = ? AND status NOT IN (`+activeJobStatusesSQL+`)
RETURNING *`, JobPending, "排队中", now, current.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 事务是 immediate 的，正常不会发生；保守地当成"已被别人领取"。
			return nil, tx.Commit()
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &reset, nil
}

// PruneAutomaticRetryJobs 删除 updated_at 早于 olderThan 的自动重试任务行（只删该 input 的
// 终态行，避免误删正在跑的行）。由启动/定期维护调用，防止复用行之外的历史脏数据堆积。
func (s *Store) PruneAutomaticRetryJobs(ctx context.Context, olderThan time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
DELETE FROM jobs
WHERE kind = ? AND input = ? AND updated_at < ? AND status NOT IN (`+activeJobStatusesSQL+`)`,
		JobKindFailedRetry, AutomaticFailedRetryInput, olderThan.UTC())
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s *Store) ListFailedTweetsForRetry(ctx context.Context, limit int, force bool) ([]FailedTweet, error) {
	if limit <= 0 || limit > 500 {
		limit = 20
	}
	items := []FailedTweet{}
	now := time.Now().UTC()
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM failed_tweets WHERE (? OR next_retry_at IS NULL OR next_retry_at <= ?) ORDER BY updated_at ASC LIMIT ?`, force, now, limit)
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

func (s *Store) CountFailedTweetsForJob(ctx context.Context, jobID int64) (int, error) {
	var count int
	err := s.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM failed_tweets WHERE job_id = ?`, jobID)
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

// PruneFailedRecords 清理早于 olderThan 的失败推文 / 失败媒体 / 永久不可用媒体记录
// （M7 保留策略）。downloads 历史保留：任务文件列表依赖它，不做自动删除。
//
// unavailable_media 一并老化：命中该表的媒体会被无条件跳过且不再重试，一旦有记录被
// 误判为永久失败写进去，就永远不会自愈。按 updated_at 老化意味着持续失败的媒体每次
// 被记录都会续期，只有确实长时间没再出现的记录才会过期。
func (s *Store) PruneFailedRecords(ctx context.Context, olderThan time.Time) (int, error) {
	olderThan = olderThan.UTC()
	var pruned int64
	// 分语句执行：多语句 Exec 的 RowsAffected 只反映最后一条语句，要累计删除行数就必须
	// 逐条执行（modernc 驱动本身会执行全部语句，迁移代码依赖这一点）。
	for _, statement := range []string{
		`DELETE FROM failed_tweets WHERE updated_at < ?`,
		`DELETE FROM failed_media WHERE created_at < ?`,
		`DELETE FROM unavailable_media WHERE updated_at < ?`,
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
		return ArchiveSchedule{}, ErrValidation(fmt.Sprintf("定时任务间隔不能小于 %d 分钟", MinArchiveScheduleIntervalMinutes))
	}
	if schedule.IntervalMinutes > MaxArchiveScheduleIntervalMinutes {
		return ArchiveSchedule{}, ErrValidation(fmt.Sprintf("定时任务间隔不能超过 %d 分钟", MaxArchiveScheduleIntervalMinutes))
	}
	if len(schedule.Items) == 0 {
		return ArchiveSchedule{}, ErrValidation("定时任务目标不能为空")
	}
	if len(schedule.Items) > MaxArchiveScheduleItems {
		return ArchiveSchedule{}, ErrValidation(fmt.Sprintf("定时任务一次最多包含 %d 个目标", MaxArchiveScheduleItems))
	}

	items := make([]ArchiveScheduleItem, 0, len(schedule.Items))
	seen := map[string]struct{}{}
	for _, item := range schedule.Items {
		item.Input = strings.TrimSpace(item.Input)
		item.Title = strings.TrimSpace(item.Title)
		if item.Input == "" {
			return ArchiveSchedule{}, ErrValidation("定时任务目标不能为空")
		}
		switch item.Kind {
		case JobKindUser, JobKindList, JobKindFollowing:
		default:
			return ArchiveSchedule{}, ErrValidation(fmt.Sprintf("定时任务不支持的目标类型: %s", item.Kind))
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
		return ArchiveSchedule{}, ErrValidation("定时任务目标不能为空")
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
	healthy := make([]ArchiveSchedule, 0, len(items))
	for _, row := range items {
		item, err := hydrateArchiveSchedule(row)
		if err != nil {
			log.Printf("archive schedule %d: invalid saved data: %v", row.ID, err)
			continue
		}
		healthy = append(healthy, item)
	}
	return healthy, nil
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
