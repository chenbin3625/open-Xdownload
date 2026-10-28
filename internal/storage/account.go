package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

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
