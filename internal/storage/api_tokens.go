package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// APITokenPrefix 用于明确区分长期 Personal API Token 与登录会话令牌。
const APITokenPrefix = "oxd_"

// APIToken 是 api_tokens 表的一行；数据库只存 SHA-256 哈希，绝不明文落库。
type APIToken struct {
	ID          int64        `json:"id" db:"id"`
	Name        string       `json:"name" db:"name"`
	TokenHash   string       `json:"-" db:"token_hash"`
	TokenPrefix string       `json:"tokenPrefix" db:"token_prefix"`
	Scopes      string       `json:"scopes" db:"scopes"`
	LastUsedAt  sql.NullTime `json:"-" db:"last_used_at"`
	ExpiresAt   sql.NullTime `json:"-" db:"expires_at"`
	CreatedAt   time.Time    `json:"createdAt" db:"created_at"`
	// 用于 JSON 输出的可空时间戳
	LastUsed *time.Time `json:"lastUsedAt,omitempty" db:"-"`
	Expires  *time.Time `json:"expiresAt,omitempty" db:"-"`
}

func hydrateAPIToken(t APIToken) APIToken {
	if t.LastUsedAt.Valid {
		v := t.LastUsedAt.Time.UTC()
		t.LastUsed = &v
	}
	if t.ExpiresAt.Valid {
		v := t.ExpiresAt.Time.UTC()
		t.Expires = &v
	}
	return t
}

func hashAPIToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateAPITokens() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS api_tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL,
  scopes TEXT NOT NULL DEFAULT 'all',
  last_used_at DATETIME,
  expires_at DATETIME,
  created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash);
`)
	return err
}

// CreateAPIToken 创建新的个人 API Token，并仅在此返回一次明文令牌。
func (s *Store) CreateAPIToken(ctx context.Context, name, scopes string, expiresAt *time.Time) (APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIToken{}, "", ErrValidation("令牌名称不能为空")
	}
	if len([]rune(name)) > 80 {
		return APIToken{}, "", ErrValidation("令牌名称不能超过 80 个字符")
	}
	scopes = strings.TrimSpace(scopes)
	if scopes == "" {
		scopes = "all"
	}
	var rawBytes [32]byte
	if _, err := rand.Read(rawBytes[:]); err != nil {
		return APIToken{}, "", err
	}
	plaintext := APITokenPrefix + base64.RawURLEncoding.EncodeToString(rawBytes[:])
	prefix := plaintext[:12] + "…"
	hash := hashAPIToken(plaintext)
	now := time.Now().UTC()
	var expArg any
	if expiresAt != nil && !expiresAt.IsZero() {
		expArg = formatSortableTime(expiresAt.UTC())
	}
	var created APIToken
	err := s.db.GetContext(ctx, &created, `
INSERT INTO api_tokens(name, token_hash, token_prefix, scopes, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?) RETURNING *`,
		name, hash, prefix, scopes, expArg, formatSortableTime(now))
	if err != nil {
		return APIToken{}, "", err
	}
	return hydrateAPIToken(created), plaintext, nil
}

// ListAPITokens 列出所有个人 API Token（不含明文与哈希）。
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows := []APIToken{}
	if err := s.db.SelectContext(ctx, &rows, `SELECT * FROM api_tokens ORDER BY id DESC`); err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i] = hydrateAPIToken(rows[i])
	}
	return rows, nil
}

// DeleteAPIToken 撤销一条个人 API Token。
func (s *Store) DeleteAPIToken(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound("API 令牌不存在", sql.ErrNoRows)
	}
	return nil
}

// ValidateAPIToken 校验个人 API Token 是否有效未过期，并在 touch=true 时节流更新 last_used_at。
func (s *Store) ValidateAPIToken(ctx context.Context, raw string, now time.Time, touch bool) (bool, error) {
	if !strings.HasPrefix(raw, APITokenPrefix) || len(raw) < len(APITokenPrefix)+16 {
		return false, nil
	}
	var token APIToken
	err := s.db.GetContext(ctx, &token, `SELECT * FROM api_tokens WHERE token_hash = ?`, hashAPIToken(raw))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	nowUTC := now.UTC()
	if token.ExpiresAt.Valid && nowUTC.After(token.ExpiresAt.Time) {
		return false, nil
	}
	if touch && (!token.LastUsedAt.Valid || nowUTC.Sub(token.LastUsedAt.Time) >= time.Minute) {
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, formatSortableTime(nowUTC), token.ID)
	}
	return true, nil
}
