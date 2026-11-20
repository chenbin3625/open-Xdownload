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
	result, err := s.db.ExecContext(ctx, `UPDATE app_account SET password_hash = ?, session_key = ? WHERE id = 1 AND session_key = ?`, passwordHash, sessionKey, oldKey)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return &UserFacingError{Status: http.StatusUnauthorized, Message: "登录已失效，请重新登录"}
	}
	return nil
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
	return tx.Commit()
}

func (s *Store) SessionValid(ctx context.Context, token string, now time.Time) (bool, error) {
	hash := sha256.Sum256([]byte(token))
	result, err := s.db.ExecContext(ctx, `UPDATE app_sessions SET last_seen_at=MAX(last_seen_at,?)
		WHERE token_hash=? AND created_at<=? AND expires_at>? AND last_seen_at>?`,
		now.UnixMilli(), hash[:], now.UnixMilli(), now.UnixMilli(), now.Add(-SessionIdleTimeout).UnixMilli())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1 && err == nil, err
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
