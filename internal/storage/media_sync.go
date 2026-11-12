package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MediaSync 是单个用户的媒体时间线翻页游标。与 tweet_sync（全文归档）独立：
// UserMedia 与 UserTweets 是两个接口、两套分页结构，不能共用水位。
//
// 每处理完一页就覆盖写入 Cursor；整轮成功结束后 Status=completed 且 Cursor 清空。
// StopID 记录本轮增量早停边界：恢复时只有边界一致才复用 Cursor，否则从第 0 页重翻
// （已下载媒体由 downloads 去重跳过）。
type MediaSync struct {
	UserID          string    `json:"userId" db:"user_id"`
	Cursor          string    `json:"cursor" db:"cursor"`
	StopID          string    `json:"stopId" db:"stop_id"`
	LastSeenTweetID string    `json:"lastSeenTweetId" db:"last_seen_tweet_id"`
	Count           int       `json:"count" db:"count"`
	Status          string    `json:"status" db:"status"`
	Error           string    `json:"error" db:"error"`
	UpdatedAt       time.Time `json:"updatedAt" db:"updated_at"`
}

// GetMediaSync 读取用户媒体时间线游标；不存在时自动创建空行。
func (s *Store) GetMediaSync(ctx context.Context, userID string) (MediaSync, error) {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO media_sync(user_id, updated_at) VALUES(?, ?)
ON CONFLICT(user_id) DO NOTHING`, userID, time.Now().UTC()); err != nil {
		return MediaSync{}, err
	}
	state := MediaSync{}
	err := s.db.GetContext(ctx, &state, `SELECT * FROM media_sync WHERE user_id = ?`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return MediaSync{UserID: userID}, nil
	}
	return state, err
}

// SaveMediaSync 覆盖写入用户的媒体时间线游标。
func (s *Store) SaveMediaSync(ctx context.Context, state MediaSync) error {
	state.UpdatedAt = time.Now().UTC()
	_, err := s.db.NamedExecContext(ctx, `
INSERT INTO media_sync (user_id, cursor, stop_id, last_seen_tweet_id, count, status, error, updated_at)
VALUES (:user_id, :cursor, :stop_id, :last_seen_tweet_id, :count, :status, :error, :updated_at)
ON CONFLICT(user_id) DO UPDATE SET
	cursor = excluded.cursor,
	stop_id = excluded.stop_id,
	last_seen_tweet_id = excluded.last_seen_tweet_id,
	count = excluded.count,
	status = excluded.status,
	error = excluded.error,
	updated_at = excluded.updated_at`, state)
	return err
}
