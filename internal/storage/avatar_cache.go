package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// AvatarCache 记录已缓存到本地的 X 作者头像信息。
type AvatarCache struct {
	UserID      string    `db:"user_id"`
	ScreenName  string    `db:"screen_name"`
	SourceURL   string    `db:"source_url"`
	FilePath    string    `db:"file_path"`
	ContentType string    `db:"content_type"`
	UpdatedAt   time.Time `db:"updated_at"`
}

func (s *Store) migrateAvatarCache() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS avatar_cache (
  user_id TEXT NOT NULL DEFAULT '',
  screen_name TEXT PRIMARY KEY COLLATE NOCASE,
  source_url TEXT NOT NULL DEFAULT '',
  file_path TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT 'image/jpeg',
  updated_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_avatar_cache_user_id ON avatar_cache(user_id);
`)
	return err
}

// AvatarDir 返回本地头像缓存目录（位于 dataDir/avatars）。
func (s *Store) AvatarDir() string {
	return filepath.Join(s.dataDir, "avatars")
}

// ThumbnailDir 返回本地离线缩略图缓存目录（位于 dataDir/thumbnails）。
func (s *Store) ThumbnailDir() string {
	return filepath.Join(s.dataDir, "thumbnails")
}

// DataDir 返回 SQLite 数据目录路径。
func (s *Store) DataDir() string {
	return s.dataDir
}

func (s *Store) UpsertAvatarCache(ctx context.Context, entry AvatarCache) error {
	entry.ScreenName = strings.TrimPrefix(strings.TrimSpace(entry.ScreenName), "@")
	if entry.ScreenName == "" {
		return nil
	}
	if entry.ContentType == "" {
		entry.ContentType = "image/jpeg"
	}
	now := formatSortableTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `
INSERT INTO avatar_cache(user_id, screen_name, source_url, file_path, content_type, updated_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(screen_name) DO UPDATE SET
  user_id = CASE WHEN excluded.user_id <> '' THEN excluded.user_id ELSE avatar_cache.user_id END,
  source_url = excluded.source_url,
  file_path = excluded.file_path,
  content_type = excluded.content_type,
  updated_at = excluded.updated_at`,
		entry.UserID, entry.ScreenName, entry.SourceURL, entry.FilePath, entry.ContentType, now)
	return err
}

func (s *Store) GetAvatarCache(ctx context.Context, screenName string) (*AvatarCache, error) {
	screenName = strings.TrimPrefix(strings.TrimSpace(screenName), "@")
	if screenName == "" {
		return nil, nil
	}
	var entry AvatarCache
	err := s.db.GetContext(ctx, &entry, `SELECT * FROM avatar_cache WHERE lower(screen_name) = lower(?)`, screenName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entry, nil
}
