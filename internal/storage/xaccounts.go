package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

// XAccount 是 x_accounts 表的一行。账号只通过导入 Cookie 添加，不保存密码。
//
// 时间列沿用仓库统一约定：写入时绑定 UTC time.Time，驱动以 RFC3339Nano 文本落库
// （DSN 打开 _texttotime），因此 SQL 侧直接比较这些列是安全的。
type XAccount struct {
	ID           int64        `db:"id"`
	ScreenName   string       `db:"screen_name"`
	AuthToken    string       `db:"auth_token"`
	CSRFToken    string       `db:"ct0"`
	ExtraCookies string       `db:"extra_cookies"`
	UserAgent    string       `db:"user_agent"`
	Proxy        string       `db:"proxy"`
	IsPrimary    bool         `db:"is_primary"`
	Source       string       `db:"source"`
	Active       bool         `db:"active"`
	ErrorMsg     string       `db:"error_msg"`
	LastUsed     sql.NullTime `db:"last_used"`
	VerifiedAt   sql.NullTime `db:"verified_at"`
	CreatedAt    time.Time    `db:"created_at"`
	UpdatedAt    time.Time    `db:"updated_at"`
}

// XAccountLock 是某个账号在某个 queue 上的限流/冷却锁。
type XAccountLock struct {
	AccountID int64     `db:"account_id"`
	Queue     string    `db:"queue"`
	Reason    string    `db:"reason"`
	Until     time.Time `db:"until"`
}

// XAccountStat 是账号按 UTC 天、按 queue 聚合的请求计数。
type XAccountStat struct {
	AccountID int64  `db:"account_id"`
	Queue     string `db:"queue"`
	Day       string `db:"day"`
	Requests  int64  `db:"requests"`
}

// XAccountPatch 是 UpdateXAccount 的部分更新载荷；nil 表示不修改。
type XAccountPatch struct {
	Active    *bool
	IsPrimary *bool // true 时清掉其他账号的 primary
	UserAgent *string
	Proxy     *string
	// AuthToken / CSRFToken / ExtraCookies 对 source='env' 的账号拒绝修改。
	AuthToken    *string
	CSRFToken    *string
	ExtraCookies *string
}

const (
	// xAccountSourceDB 表示账号来自数据库（UI 导入）。
	xAccountSourceDB = "db"
	// xAccountSourceEnv 表示账号由环境变量接管，只读。
	xAccountSourceEnv = "env"
)

// migrateXAccounts 建账号池三张表。DDL 与 docs/xscrape-design.md 保持一致。
func (s *Store) migrateXAccounts() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS x_accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  screen_name TEXT NOT NULL DEFAULT '',
  auth_token TEXT NOT NULL UNIQUE,
  ct0 TEXT NOT NULL,
  extra_cookies TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  proxy TEXT NOT NULL DEFAULT '',
  is_primary INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'db',
  active INTEGER NOT NULL DEFAULT 1,
  error_msg TEXT NOT NULL DEFAULT '',
  last_used DATETIME,
  verified_at DATETIME,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS x_account_locks (
  account_id INTEGER NOT NULL REFERENCES x_accounts(id) ON DELETE CASCADE,
  queue TEXT NOT NULL,
  until DATETIME NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(account_id, queue)
);
CREATE TABLE IF NOT EXISTS x_account_stats (
  account_id INTEGER NOT NULL REFERENCES x_accounts(id) ON DELETE CASCADE,
  queue TEXT NOT NULL,
  day TEXT NOT NULL,
  requests INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(account_id, queue, day)
);
`)
	return err
}

// migrateCookiesToXAccounts 把 app_config 里的旧 Cookie 列导入账号表。直接读原始列，
// 不经过 GetConfig，避免环境变量覆盖改变导入值。旧列保留不删。
//
// 幂等：以 auth_token 判重；即使 schema_migrations 记录被清理后重跑，也不会重复插入。
func (s *Store) migrateCookiesToXAccounts(exec migrationExecutor) error {
	var legacy struct {
		AuthToken         string `db:"auth_token"`
		CSRFToken         string `db:"csrf_token"`
		AdditionalCookies string `db:"additional_cookies"`
	}
	if err := exec.Get(&legacy, `SELECT auth_token, csrf_token, additional_cookies FROM app_config WHERE id = 1`); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	now := time.Now().UTC()
	seen := make(map[string]struct{})
	primaryToken := strings.TrimSpace(legacy.AuthToken)
	if primaryToken != "" {
		seen[primaryToken] = struct{}{}
		exists, err := xAccountTokenExists(exec, primaryToken)
		if err != nil {
			return err
		}
		if !exists {
			// 迁移进来的主账号是唯一的 primary。
			if _, err := exec.Exec(`UPDATE x_accounts SET is_primary = 0 WHERE is_primary = 1`); err != nil {
				return err
			}
			if _, err := exec.Exec(`
INSERT INTO x_accounts (
	screen_name, auth_token, ct0, extra_cookies, user_agent, proxy,
	is_primary, source, active, error_msg, created_at, updated_at
) VALUES ('', ?, ?, '', '', '', 1, 'db', 1, '', ?, ?)`,
				primaryToken, strings.TrimSpace(legacy.CSRFToken), now, now); err != nil {
				return err
			}
		}
	}
	for _, pair := range config.ParseCookiePairs(legacy.AdditionalCookies) {
		token := strings.TrimSpace(pair.AuthToken)
		if token == "" {
			continue
		}
		if _, duplicate := seen[token]; duplicate {
			continue
		}
		seen[token] = struct{}{}
		exists, err := xAccountTokenExists(exec, token)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := exec.Exec(`
INSERT INTO x_accounts (
	screen_name, auth_token, ct0, extra_cookies, user_agent, proxy,
	is_primary, source, active, error_msg, created_at, updated_at
) VALUES ('', ?, ?, '', '', '', 0, 'db', 1, '', ?, ?)`,
			token, strings.TrimSpace(pair.CSRFToken), now, now); err != nil {
			return err
		}
	}
	return nil
}

func xAccountTokenExists(exec migrationExecutor, token string) (bool, error) {
	var count int
	if err := exec.Get(&count, `SELECT COUNT(*) FROM x_accounts WHERE auth_token = ?`, token); err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListXAccounts 返回全部账号，primary 在前，其余按 id 升序。
func (s *Store) ListXAccounts(ctx context.Context) ([]XAccount, error) {
	accounts := []XAccount{}
	err := s.db.SelectContext(ctx, &accounts, `SELECT * FROM x_accounts ORDER BY is_primary DESC, id ASC`)
	return accounts, err
}

// GetXAccount 按 id 读取账号；不存在返回 ErrNotFound。
func (s *Store) GetXAccount(ctx context.Context, id int64) (XAccount, error) {
	account := XAccount{}
	err := s.db.GetContext(ctx, &account, `SELECT * FROM x_accounts WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return XAccount{}, ErrNotFound("账号不存在", err)
	}
	return account, err
}

// CreateXAccount 新建账号。首个账号自动成为 primary；auth_token 重复返回 ErrValidation。
// 新账号默认启用（active=1）；需要停用请调用 DeactivateXAccount。
func (s *Store) CreateXAccount(ctx context.Context, account XAccount) (XAccount, error) {
	account.AuthToken = strings.TrimSpace(account.AuthToken)
	account.CSRFToken = strings.TrimSpace(account.CSRFToken)
	account.ScreenName = strings.TrimSpace(account.ScreenName)
	if account.AuthToken == "" {
		return XAccount{}, ErrValidation("auth_token 不能为空")
	}
	if account.Source == "" {
		account.Source = xAccountSourceDB
	}
	if account.Source != xAccountSourceDB && account.Source != xAccountSourceEnv {
		return XAccount{}, ErrValidation("账号来源无效")
	}
	now := time.Now().UTC()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return XAccount{}, err
	}
	defer tx.Rollback() // Commit 后为 no-op

	var duplicate int
	if err := tx.GetContext(ctx, &duplicate, `SELECT COUNT(*) FROM x_accounts WHERE auth_token = ?`, account.AuthToken); err != nil {
		return XAccount{}, err
	}
	if duplicate > 0 {
		return XAccount{}, ErrValidation("auth_token 已存在")
	}
	var total int
	if err := tx.GetContext(ctx, &total, `SELECT COUNT(*) FROM x_accounts`); err != nil {
		return XAccount{}, err
	}
	makePrimary := account.IsPrimary || total == 0
	if makePrimary {
		if _, err := tx.ExecContext(ctx, `UPDATE x_accounts SET is_primary = 0 WHERE is_primary = 1`); err != nil {
			return XAccount{}, err
		}
	}
	created := XAccount{}
	err = tx.GetContext(ctx, &created, `
INSERT INTO x_accounts (
	screen_name, auth_token, ct0, extra_cookies, user_agent, proxy,
	is_primary, source, active, error_msg, last_used, verified_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
RETURNING *`,
		account.ScreenName, account.AuthToken, account.CSRFToken, account.ExtraCookies,
		account.UserAgent, account.Proxy, makePrimary, account.Source, account.ErrorMsg,
		account.LastUsed, account.VerifiedAt, now, now)
	if err != nil {
		if isSQLiteUniqueViolation(err) {
			return XAccount{}, ErrValidation("auth_token 已存在")
		}
		return XAccount{}, err
	}
	if err := tx.Commit(); err != nil {
		return XAccount{}, err
	}
	return created, nil
}

// UpdateXAccount 部分更新账号。source='env' 的账号拒绝修改任何 Cookie 字段。
// IsPrimary=true 时在同一事务里清掉其他账号的 primary。
func (s *Store) UpdateXAccount(ctx context.Context, id int64, patch XAccountPatch) (XAccount, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return XAccount{}, err
	}
	defer tx.Rollback() // Commit 后为 no-op

	current := XAccount{}
	if err := tx.GetContext(ctx, &current, `SELECT * FROM x_accounts WHERE id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return XAccount{}, ErrNotFound("账号不存在", err)
		}
		return XAccount{}, err
	}
	if current.Source == xAccountSourceEnv &&
		(patch.AuthToken != nil || patch.CSRFToken != nil || patch.ExtraCookies != nil) {
		return XAccount{}, ErrValidation("环境变量账号不能修改 Cookie")
	}

	sets := make([]string, 0, 8)
	args := make([]any, 0, 9)
	if patch.Active != nil {
		sets = append(sets, "active = ?")
		args = append(args, *patch.Active)
	}
	if patch.IsPrimary != nil {
		if *patch.IsPrimary {
			if _, err := tx.ExecContext(ctx, `UPDATE x_accounts SET is_primary = 0 WHERE id <> ?`, id); err != nil {
				return XAccount{}, err
			}
		}
		sets = append(sets, "is_primary = ?")
		args = append(args, *patch.IsPrimary)
	}
	if patch.UserAgent != nil {
		sets = append(sets, "user_agent = ?")
		args = append(args, *patch.UserAgent)
	}
	if patch.Proxy != nil {
		sets = append(sets, "proxy = ?")
		args = append(args, *patch.Proxy)
	}
	if patch.AuthToken != nil {
		token := strings.TrimSpace(*patch.AuthToken)
		if token == "" {
			return XAccount{}, ErrValidation("auth_token 不能为空")
		}
		sets = append(sets, "auth_token = ?")
		args = append(args, token)
	}
	if patch.CSRFToken != nil {
		sets = append(sets, "ct0 = ?")
		args = append(args, strings.TrimSpace(*patch.CSRFToken))
	}
	if patch.ExtraCookies != nil {
		sets = append(sets, "extra_cookies = ?")
		args = append(args, *patch.ExtraCookies)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC())
	args = append(args, id)

	updated := XAccount{}
	err = tx.GetContext(ctx, &updated, `UPDATE x_accounts SET `+strings.Join(sets, ", ")+` WHERE id = ? RETURNING *`, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return XAccount{}, ErrNotFound("账号不存在", err)
	}
	if err != nil {
		if isSQLiteUniqueViolation(err) {
			return XAccount{}, ErrValidation("auth_token 已存在")
		}
		return XAccount{}, err
	}
	if err := tx.Commit(); err != nil {
		return XAccount{}, err
	}
	return updated, nil
}

// DeleteXAccount 删除账号。source='env' 的账号拒绝删除；删除 primary 后把 id 最小的
// active 账号提升为 primary，没有 active 账号时保持无 primary。
func (s *Store) DeleteXAccount(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Commit 后为 no-op

	current := XAccount{}
	if err := tx.GetContext(ctx, &current, `SELECT * FROM x_accounts WHERE id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound("账号不存在", err)
		}
		return err
	}
	if current.Source == xAccountSourceEnv {
		return ErrValidation("环境变量账号不能删除")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM x_accounts WHERE id = ?`, id); err != nil {
		return err
	}
	if current.IsPrimary {
		var nextID int64
		err := tx.GetContext(ctx, &nextID, `SELECT id FROM x_accounts WHERE active = 1 ORDER BY id ASC LIMIT 1`)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE x_accounts SET is_primary = 1, updated_at = ? WHERE id = ?`, time.Now().UTC(), nextID); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return tx.Commit()
}

// UpsertEnvXAccount 按 auth_token 写入/更新环境变量账号：source='env'、is_primary=1，
// 并清掉其他账号的 primary。由启动代码调用。
func (s *Store) UpsertEnvXAccount(ctx context.Context, authToken, ct0 string) error {
	authToken = strings.TrimSpace(authToken)
	ct0 = strings.TrimSpace(ct0)
	if authToken == "" || ct0 == "" {
		return ErrValidation("环境变量账号缺少 auth_token 或 ct0")
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Commit 后为 no-op

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO x_accounts (
	screen_name, auth_token, ct0, extra_cookies, user_agent, proxy,
	is_primary, source, active, error_msg, created_at, updated_at
) VALUES ('', ?, ?, '', '', '', 1, 'env', 1, '', ?, ?)
ON CONFLICT(auth_token) DO UPDATE SET
	ct0 = excluded.ct0,
	source = 'env',
	is_primary = 1,
	active = 1,
	error_msg = '',
	updated_at = excluded.updated_at`,
		authToken, ct0, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE x_accounts SET is_primary = 0 WHERE auth_token <> ?`, authToken); err != nil {
		return err
	}
	return tx.Commit()
}

// PickXAccount 原子地选出一个可用账号并把 last_used 更新为 now。
//
// 单条 UPDATE ... RETURNING 完成"选 + 标记"，不依赖 BEGIN IMMEDIATE：database/sql 的
// 连接池会让事务开始时的写锁不可靠，而单语句本身在 SQLite 内是原子的。候选条件为
// active、不在 exclude、且 queue 与 '*' 都没有仍未过期的锁；排序让从未使用过的账号
// 优先，其后按 last_used 最早、id 最小轮换。
func (s *Store) PickXAccount(ctx context.Context, queue string, exclude []int64, now time.Time) (XAccount, bool, error) {
	now = now.UTC()
	query := `
UPDATE x_accounts SET last_used = ?
WHERE id = (
	SELECT a.id FROM x_accounts a
	WHERE a.active = 1`
	args := make([]any, 0, len(exclude)+4)
	args = append(args, now)
	if len(exclude) > 0 {
		placeholders := make([]string, len(exclude))
		for index, id := range exclude {
			placeholders[index] = "?"
			args = append(args, id)
		}
		query += ` AND a.id NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += `
	  AND NOT EXISTS (
		SELECT 1 FROM x_account_locks l
		WHERE l.account_id = a.id
		  AND l.queue IN (?, ?)
		  AND l.until > ?
	  )
	ORDER BY a.last_used IS NOT NULL, a.last_used, a.id
	LIMIT 1
)
RETURNING *`
	args = append(args, queue, "*", now)

	account := XAccount{}
	err := s.db.GetContext(ctx, &account, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return XAccount{}, false, nil
	}
	if err != nil {
		return XAccount{}, false, err
	}
	return account, true, nil
}

// EarliestXAccountUnlock 返回 active、未排除账号中 queue 或 '*' 锁最早的未来解锁时间。
// 没有任何未过期锁时 ok=false。
func (s *Store) EarliestXAccountUnlock(ctx context.Context, queue string, exclude []int64, now time.Time) (time.Time, bool, error) {
	now = now.UTC()
	query := `
SELECT l.until FROM x_account_locks l
JOIN x_accounts a ON a.id = l.account_id
WHERE a.active = 1
  AND l.queue IN (?, ?)
  AND l.until > ?`
	args := make([]any, 0, len(exclude)+3)
	args = append(args, queue, "*", now)
	if len(exclude) > 0 {
		placeholders := make([]string, len(exclude))
		for index, id := range exclude {
			placeholders[index] = "?"
			args = append(args, id)
		}
		query += ` AND a.id NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += ` ORDER BY l.until ASC LIMIT 1`

	var until sql.NullTime
	err := s.db.GetContext(ctx, &until, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if !until.Valid {
		return time.Time{}, false, nil
	}
	return until.Time, true, nil
}

// LockXAccount 写入/延长锁。同一 (account_id, queue) 已有锁时保留更晚的 until。
func (s *Store) LockXAccount(ctx context.Context, id int64, queue string, until time.Time, reason string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO x_account_locks (account_id, queue, until, reason)
VALUES (?, ?, ?, ?)
ON CONFLICT(account_id, queue) DO UPDATE SET
	until = excluded.until,
	reason = excluded.reason
WHERE excluded.until > x_account_locks.until`,
		id, queue, until.UTC(), reason)
	return err
}

// UnlockXAccount 解除指定 queue 的锁；queue == "" 时解除全部锁。
func (s *Store) UnlockXAccount(ctx context.Context, id int64, queue string) error {
	if queue == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM x_account_locks WHERE account_id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM x_account_locks WHERE account_id = ? AND queue = ?`, id, queue)
	return err
}

// DeactivateXAccount 停用账号并记录原因。
func (s *Store) DeactivateXAccount(ctx context.Context, id int64, msg string) error {
	return s.updateXAccountState(ctx, `UPDATE x_accounts SET active = 0, error_msg = ?, updated_at = ? WHERE id = ?`, msg, time.Now().UTC(), id)
}

// ActivateXAccount 启用账号并清空 error_msg。
func (s *Store) ActivateXAccount(ctx context.Context, id int64) error {
	return s.updateXAccountState(ctx, `UPDATE x_accounts SET active = 1, error_msg = '', updated_at = ? WHERE id = ?`, time.Now().UTC(), id)
}

// MarkXAccountVerified 记录一次成功校验；screenName 为空时保留原值。
func (s *Store) MarkXAccountVerified(ctx context.Context, id int64, screenName string, at time.Time) error {
	screenName = strings.TrimSpace(screenName)
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.updateXAccountState(ctx, `
UPDATE x_accounts SET
	verified_at = ?,
	screen_name = CASE WHEN ? <> '' THEN ? ELSE screen_name END,
	updated_at = ?
WHERE id = ?`, at.UTC(), screenName, screenName, time.Now().UTC(), id)
}

// SetXAccountError 只记录错误信息，不停用账号。
func (s *Store) SetXAccountError(ctx context.Context, id int64, msg string) error {
	return s.updateXAccountState(ctx, `UPDATE x_accounts SET error_msg = ?, updated_at = ? WHERE id = ?`, msg, time.Now().UTC(), id)
}

func (s *Store) updateXAccountState(ctx context.Context, query string, args ...any) error {
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound("账号不存在", sql.ErrNoRows)
	}
	return nil
}

// IncrementXAccountStat 累加账号在 UTC 当天、指定 queue 上的请求次数。
func (s *Store) IncrementXAccountStat(ctx context.Context, id int64, queue string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	day := at.UTC().Format("2006-01-02")
	_, err := s.db.ExecContext(ctx, `
INSERT INTO x_account_stats (account_id, queue, day, requests)
VALUES (?, ?, ?, 1)
ON CONFLICT(account_id, queue, day) DO UPDATE SET requests = requests + 1`,
		id, queue, day)
	return err
}

// ListXAccountLocks 返回所有未过期的锁。
func (s *Store) ListXAccountLocks(ctx context.Context, now time.Time) ([]XAccountLock, error) {
	locks := []XAccountLock{}
	err := s.db.SelectContext(ctx, &locks, `
SELECT account_id, queue, reason, until FROM x_account_locks
WHERE until > ?
ORDER BY until ASC, account_id ASC, queue ASC`, now.UTC())
	return locks, err
}

// ListXAccountStats 返回指定 UTC 天（YYYY-MM-DD）的账号请求计数。
func (s *Store) ListXAccountStats(ctx context.Context, day string) ([]XAccountStat, error) {
	stats := []XAccountStat{}
	err := s.db.SelectContext(ctx, &stats, `
SELECT account_id, queue, day, requests FROM x_account_stats
WHERE day = ?
ORDER BY account_id ASC, queue ASC`, day)
	return stats, err
}

// PurgeExpiredXAccountLocks 删除 until 已到的锁。
func (s *Store) PurgeExpiredXAccountLocks(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM x_account_locks WHERE until <= ?`, now.UTC())
	return err
}

func isSQLiteUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
