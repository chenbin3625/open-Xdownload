package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

const SessionLifetime = 30 * 24 * time.Hour
const SessionIdleTimeout = 7 * 24 * time.Hour

// MaxActiveSessions caps the number of concurrent active sessions kept for the
// single app account; oldest/least-recently-seen sessions beyond this cap are
// evicted on CreateSession so app_sessions cannot grow without bound.
const MaxActiveSessions = 64

type Account struct {
	Username     string `db:"username"`
	PasswordHash []byte `db:"password_hash"`
	SessionKey   []byte `db:"session_key"`
}

func (s *Store) GetAccount(ctx context.Context) (*Account, error) {
	var account Account
	err := s.db.GetContext(ctx, &account, `SELECT username, password_hash, session_key FROM app_account WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &account, err
}

func (s *Store) CreateAccount(ctx context.Context, account Account) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO app_account (id, username, password_hash, session_key)
		VALUES (1, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, account.Username, account.PasswordHash, account.SessionKey)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return &UserFacingError{Status: http.StatusConflict, Message: "账号已创建，请登录"}
	}
	return nil
}

func (s *Store) UpdateAccountPassword(ctx context.Context, oldKey, passwordHash, sessionKey []byte) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE app_account SET password_hash = ?, session_key = ? WHERE id = 1 AND session_key = ?`, passwordHash, sessionKey, oldKey)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return &UserFacingError{Status: http.StatusUnauthorized, Message: "登录已失效，请重新登录"}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateSession(ctx context.Context, token string, now time.Time) error {
	hash := sha256.Sum256([]byte(token))
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_sessions WHERE expires_at<=? OR last_seen_at<=?`, now.UnixMilli(), now.Add(-SessionIdleTimeout).UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_sessions(token_hash,created_at,last_seen_at,expires_at) VALUES(?,?,?,?)`, hash[:], now.UnixMilli(), now.UnixMilli(), now.Add(SessionLifetime).UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_sessions WHERE rowid NOT IN (
		SELECT rowid FROM app_sessions ORDER BY last_seen_at DESC, created_at DESC, rowid DESC LIMIT ?
	)`, MaxActiveSessions); err != nil {
		return err
	}
	return tx.Commit()
}

// sessionTouchInterval 是刷新 last_seen_at 的最小间隔。每个已认证请求（包括每张媒体
// 图片、每条 SSE 事件与心跳）都落一次 UPDATE 会把读操作变成 SQLite 写事务，与下载
// worker 争用唯一写锁。空闲超时是 7 天粒度，10 分钟的刷新间隔完全够用。
const sessionTouchInterval = 10 * time.Minute

func (s *Store) SessionValid(ctx context.Context, token string, now time.Time) (bool, error) {
	hash := sha256.Sum256([]byte(token))
	nowMS := now.UnixMilli()
	idleCutoffMS := now.Add(-SessionIdleTimeout).UnixMilli()
	// 先做只读查询：只有 last_seen_at 已超过刷新间隔时才获取 SQLite 写锁刷新，
	// 避免每次已认证的读请求都触发一次写事务（即使 0 行匹配也会走写路径）。
	var lastSeenAt int64
	err := s.db.GetContext(ctx, &lastSeenAt, `SELECT last_seen_at FROM app_sessions
		WHERE token_hash=? AND created_at<=? AND expires_at>? AND last_seen_at>?`,
		hash[:], nowMS, nowMS, idleCutoffMS)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	touchCutoffMS := now.Add(-sessionTouchInterval).UnixMilli()
	if lastSeenAt > touchCutoffMS {
		return true, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE app_sessions SET last_seen_at=MAX(last_seen_at,?)
		WHERE token_hash=? AND created_at<=? AND expires_at>? AND last_seen_at>? AND last_seen_at<=?`,
		nowMS, hash[:], nowMS, nowMS, idleCutoffMS, touchCutoffMS)
	if err != nil {
		return false, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return false, err
	} else if count == 0 {
		return s.SessionValidReadOnly(ctx, token, now)
	}
	return true, nil
}

// SessionValidReadOnly 校验会话是否有效且未超期，但不刷新 last_seen_at。
// 供 SSE 心跳/推送等被动连接使用，避免后台保活无限延长空闲会话或造成 SQLite 写竞争。
func (s *Store) SessionValidReadOnly(ctx context.Context, token string, now time.Time) (bool, error) {
	hash := sha256.Sum256([]byte(token))
	var exists int
	err := s.db.GetContext(ctx, &exists, `SELECT 1 FROM app_sessions
		WHERE token_hash=? AND created_at<=? AND expires_at>? AND last_seen_at>?`,
		hash[:], now.UnixMilli(), now.UnixMilli(), now.Add(-SessionIdleTimeout).UnixMilli())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return exists == 1 && err == nil, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_sessions WHERE token_hash=?`, hash[:])
	return err
}
