package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tag 是归档推文和媒体的本地自定义标签。
type Tag struct {
	ID        int64     `json:"id" db:"id"`
	Name      string    `json:"name" db:"name"`
	Color     string    `json:"color" db:"color"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// Collection 是用户自建的推文/媒体合集。
type Collection struct {
	ID            int64     `json:"id" db:"id"`
	Name          string    `json:"name" db:"name"`
	Description   string    `json:"description" db:"description"`
	TweetCount    int       `json:"tweetCount" db:"tweet_count"`
	DownloadCount int       `json:"downloadCount" db:"download_count"`
	CreatedAt     time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt     time.Time `json:"updatedAt" db:"updated_at"`
}

// DeleteResult 汇报删除记录与关联物理文件的结果。
type DeleteResult struct {
	DeletedRecords int `json:"deletedRecords"`
	DeletedFiles   int `json:"deletedFiles"`
	SkippedFiles   int `json:"skippedFiles"`
}

func (s *Store) migrateOrganize() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS tags (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  color TEXT NOT NULL DEFAULT 'blue',
  created_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS tweet_tags (
  tweet_id TEXT NOT NULL REFERENCES tweets(id) ON DELETE CASCADE,
  tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  created_at DATETIME NOT NULL,
  PRIMARY KEY(tweet_id, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_tweet_tags_tag ON tweet_tags(tag_id, tweet_id);
CREATE TABLE IF NOT EXISTS download_tags (
  download_id INTEGER NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
  tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  created_at DATETIME NOT NULL,
  PRIMARY KEY(download_id, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_download_tags_tag ON download_tags(tag_id, download_id);
CREATE TABLE IF NOT EXISTS collections (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS collection_items (
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  item_type TEXT NOT NULL,
  item_id TEXT NOT NULL,
  created_at DATETIME NOT NULL,
  PRIMARY KEY(collection_id, item_type, item_id)
);
CREATE INDEX IF NOT EXISTS idx_collection_items_lookup ON collection_items(item_type, item_id);
`)
	return err
}

func (s *Store) ListTags(ctx context.Context) ([]Tag, error) {
	items := []Tag{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM tags ORDER BY lower(name) ASC, id ASC`)
	return items, err
}

func (s *Store) CreateTag(ctx context.Context, name, color string) (Tag, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 48 {
		return Tag{}, ErrValidation("标签名称需为 1-48 个字符")
	}
	color = normalizeTagColor(color)
	var created Tag
	err := s.db.GetContext(ctx, &created, `INSERT INTO tags(name, color, created_at) VALUES(?, ?, ?) RETURNING *`,
		name, color, formatSortableTime(time.Now().UTC()))
	if err != nil {
		if isSQLiteUniqueViolation(err) {
			return Tag{}, ErrValidation("同名标签已存在")
		}
		return Tag{}, err
	}
	return created, nil
}

func (s *Store) UpdateTag(ctx context.Context, id int64, name, color string) (Tag, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 48 {
		return Tag{}, ErrValidation("标签名称需为 1-48 个字符")
	}
	color = normalizeTagColor(color)
	var updated Tag
	err := s.db.GetContext(ctx, &updated, `UPDATE tags SET name = ?, color = ? WHERE id = ? RETURNING *`, name, color, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, ErrNotFound("标签不存在", err)
	}
	if err != nil {
		if isSQLiteUniqueViolation(err) {
			return Tag{}, ErrValidation("同名标签已存在")
		}
		return Tag{}, err
	}
	return updated, nil
}

func (s *Store) DeleteTag(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound("标签不存在", sql.ErrNoRows)
	}
	return nil
}

func normalizeTagColor(color string) string {
	switch strings.ToLower(strings.TrimSpace(color)) {
	case "blue", "cyan", "green", "amber", "orange", "red", "pink", "purple", "indigo", "teal", "violet", "grey":
		return strings.ToLower(strings.TrimSpace(color))
	default:
		return "blue"
	}
}

func (s *Store) SetTweetStarred(ctx context.Context, tweetID string, starred bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tweets SET starred = ?, updated_at = ? WHERE id = ?`,
		starred, formatSortableTime(time.Now().UTC()), tweetID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound("推文未归档", sql.ErrNoRows)
	}
	return nil
}

func (s *Store) SetDownloadStarred(ctx context.Context, downloadID int64, starred bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE downloads SET starred = ? WHERE id = ?`, starred, downloadID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound("媒体记录不存在", sql.ErrNoRows)
	}
	s.markLibraryDownloadsCacheStale()
	return nil
}

func (s *Store) SetTweetTags(ctx context.Context, tweetID string, tagIDs []int64) ([]Tag, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.GetContext(ctx, &exists, `SELECT COUNT(*) FROM tweets WHERE id = ?`, tweetID); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound("推文未归档", sql.ErrNoRows)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tweet_tags WHERE tweet_id = ?`, tweetID); err != nil {
		return nil, err
	}
	now := formatSortableTime(time.Now().UTC())
	for _, tagID := range tagIDs {
		if tagID <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tweet_tags(tweet_id, tag_id, created_at) VALUES(?, ?, ?)`, tweetID, tagID, now); err != nil {
			return nil, err
		}
	}
	tags := []Tag{}
	if err := tx.SelectContext(ctx, &tags, `
SELECT t.* FROM tags t JOIN tweet_tags tt ON tt.tag_id = t.id WHERE tt.tweet_id = ? ORDER BY lower(t.name) ASC`, tweetID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return tags, nil
}

func (s *Store) SetDownloadTags(ctx context.Context, downloadID int64, tagIDs []int64) ([]Tag, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.GetContext(ctx, &exists, `SELECT COUNT(*) FROM downloads WHERE id = ?`, downloadID); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound("媒体记录不存在", sql.ErrNoRows)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM download_tags WHERE download_id = ?`, downloadID); err != nil {
		return nil, err
	}
	now := formatSortableTime(time.Now().UTC())
	for _, tagID := range tagIDs {
		if tagID <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO download_tags(download_id, tag_id, created_at) VALUES(?, ?, ?)`, downloadID, tagID, now); err != nil {
			return nil, err
		}
	}
	tags := []Tag{}
	if err := tx.SelectContext(ctx, &tags, `
SELECT t.* FROM tags t JOIN download_tags dt ON dt.tag_id = t.id WHERE dt.download_id = ? ORDER BY lower(t.name) ASC`, downloadID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.markLibraryDownloadsCacheStale()
	return tags, nil
}

func (s *Store) ListTagsForTweets(ctx context.Context, tweetIDs []string) (map[string][]Tag, error) {
	result := map[string][]Tag{}
	if len(tweetIDs) == 0 {
		return result, nil
	}
	args := make([]any, len(tweetIDs))
	for i, id := range tweetIDs {
		args[i] = id
	}
	type row struct {
		TweetID string `db:"tweet_id"`
		Tag
	}
	rows := []row{}
	if err := s.db.SelectContext(ctx, &rows, `
SELECT tt.tweet_id, t.id, t.name, t.color, t.created_at
FROM tweet_tags tt JOIN tags t ON t.id = tt.tag_id
WHERE tt.tweet_id IN (`+sqlPlaceholders(len(tweetIDs))+`)
ORDER BY lower(t.name) ASC`, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[r.TweetID] = append(result[r.TweetID], r.Tag)
	}
	return result, nil
}

func (s *Store) ListTagsForDownloads(ctx context.Context, downloadIDs []int64) (map[int64][]Tag, error) {
	result := map[int64][]Tag{}
	if len(downloadIDs) == 0 {
		return result, nil
	}
	args := make([]any, len(downloadIDs))
	for i, id := range downloadIDs {
		args[i] = id
	}
	type row struct {
		DownloadID int64 `db:"download_id"`
		Tag
	}
	rows := []row{}
	if err := s.db.SelectContext(ctx, &rows, `
SELECT dt.download_id, t.id, t.name, t.color, t.created_at
FROM download_tags dt JOIN tags t ON t.id = dt.tag_id
WHERE dt.download_id IN (`+sqlPlaceholders(len(downloadIDs))+`)
ORDER BY lower(t.name) ASC`, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[r.DownloadID] = append(result[r.DownloadID], r.Tag)
	}
	return result, nil
}

func (s *Store) ListCollections(ctx context.Context) ([]Collection, error) {
	items := []Collection{}
	err := s.db.SelectContext(ctx, &items, `
SELECT c.id, c.name, c.description, c.created_at, c.updated_at,
  COALESCE((SELECT COUNT(*) FROM collection_items ci WHERE ci.collection_id = c.id AND ci.item_type = 'tweet'), 0) AS tweet_count,
  COALESCE((SELECT COUNT(*) FROM collection_items ci WHERE ci.collection_id = c.id AND ci.item_type = 'download'), 0) AS download_count
FROM collections c
ORDER BY lower(c.name) ASC, c.id ASC`)
	return items, err
}

func (s *Store) CreateCollection(ctx context.Context, name, description string) (Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return Collection{}, ErrValidation("合集名称需为 1-80 个字符")
	}
	description = strings.TrimSpace(description)
	now := formatSortableTime(time.Now().UTC())
	var created Collection
	err := s.db.GetContext(ctx, &created, `
INSERT INTO collections(name, description, created_at, updated_at) VALUES(?, ?, ?, ?)
RETURNING id, name, description, created_at, updated_at, 0 AS tweet_count, 0 AS download_count`,
		name, description, now, now)
	if err != nil {
		if isSQLiteUniqueViolation(err) {
			return Collection{}, ErrValidation("同名合集已存在")
		}
		return Collection{}, err
	}
	return created, nil
}

func (s *Store) UpdateCollection(ctx context.Context, id int64, name, description string) (Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return Collection{}, ErrValidation("合集名称需为 1-80 个字符")
	}
	description = strings.TrimSpace(description)
	now := formatSortableTime(time.Now().UTC())
	res, err := s.db.ExecContext(ctx, `UPDATE collections SET name = ?, description = ?, updated_at = ? WHERE id = ?`, name, description, now, id)
	if err != nil {
		if isSQLiteUniqueViolation(err) {
			return Collection{}, ErrValidation("同名合集已存在")
		}
		return Collection{}, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Collection{}, ErrNotFound("合集不存在", sql.ErrNoRows)
	}
	items, err := s.ListCollections(ctx)
	if err != nil {
		return Collection{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return Collection{}, ErrNotFound("合集不存在", sql.ErrNoRows)
}

func (s *Store) DeleteCollection(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM collections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound("合集不存在", sql.ErrNoRows)
	}
	return nil
}

func (s *Store) AddCollectionItems(ctx context.Context, collectionID int64, itemType string, itemIDs []string) error {
	if itemType != "tweet" && itemType != "download" {
		return ErrValidation("无效的合集条目类型")
	}
	var exists int
	if err := s.db.GetContext(ctx, &exists, `SELECT COUNT(*) FROM collections WHERE id = ?`, collectionID); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound("合集不存在", sql.ErrNoRows)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatSortableTime(time.Now().UTC())
	for _, raw := range itemIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collection_items(collection_id, item_type, item_id, created_at) VALUES(?, ?, ?, ?)`,
			collectionID, itemType, id, now); err != nil {
			return err
		}
	}
	_, _ = tx.ExecContext(ctx, `UPDATE collections SET updated_at = ? WHERE id = ?`, now, collectionID)
	return tx.Commit()
}

func (s *Store) RemoveCollectionItems(ctx context.Context, collectionID int64, itemType string, itemIDs []string) error {
	if itemType != "tweet" && itemType != "download" {
		return ErrValidation("无效的合集条目类型")
	}
	if len(itemIDs) == 0 {
		return nil
	}
	args := make([]any, 0, len(itemIDs)+2)
	args = append(args, collectionID, itemType)
	for _, id := range itemIDs {
		args = append(args, strings.TrimSpace(id))
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM collection_items WHERE collection_id = ? AND item_type = ? AND item_id IN (`+sqlPlaceholders(len(itemIDs))+`)`, args...)
	return err
}

// DeleteDownloads 删除指定的下载记录，并在 deleteFiles=true 时安全清理不再被其他记录引用的本地文件。
func (s *Store) DeleteDownloads(ctx context.Context, ids []int64, deleteFiles bool) (DeleteResult, error) {
	result := DeleteResult{}
	if len(ids) == 0 {
		return result, nil
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	idArgs := make([]any, len(ids))
	for i, id := range ids {
		idArgs[i] = id
	}
	ph := sqlPlaceholders(len(ids))

	var paths []string
	if deleteFiles {
		if err := tx.SelectContext(ctx, &paths, `SELECT DISTINCT file_path FROM downloads WHERE id IN (`+ph+`) AND file_path <> ''`, idArgs...); err != nil {
			return result, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM download_assets WHERE download_id IN (`+ph+`)`, idArgs...); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM download_tags WHERE download_id IN (`+ph+`)`, idArgs...); err != nil {
		return result, err
	}
	strArgs := make([]any, len(ids))
	for i, id := range ids {
		strArgs[i] = formatInt64(id)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM collection_items WHERE item_type = 'download' AND item_id IN (`+ph+`)`, strArgs...); err != nil {
		return result, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM downloads WHERE id IN (`+ph+`)`, idArgs...)
	if err != nil {
		return result, err
	}
	if n, err := res.RowsAffected(); err == nil {
		result.DeletedRecords = int(n)
	}

	// 只删除已无任何其他下载记录引用的本地文件，避免误删跨推文去重共享的实体文件。
	safeToRemove := []string{}
	if deleteFiles {
		for _, p := range paths {
			var refs int
			if err := tx.GetContext(ctx, &refs, `SELECT COUNT(*) FROM downloads WHERE file_path = ?`, p); err != nil {
				return result, err
			}
			if refs == 0 {
				safeToRemove = append(safeToRemove, p)
			} else {
				result.SkippedFiles++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	s.invalidateLibraryDownloadsCache()

	if deleteFiles && len(safeToRemove) > 0 {
		roots, _ := s.DownloadRoots(ctx)
		for _, p := range safeToRemove {
			if removeFileWithinRoots(p, roots) {
				result.DeletedFiles++
				// 同步清理伴随 JSON sidecar（如果存在）
				_ = removeFileWithinRoots(p+".json", roots)
			} else {
				result.SkippedFiles++
			}
		}
	}
	return result, nil
}

// DeleteTweets 删除归档推文及其关联索引；当 deleteFiles=true 时同时删除其关联的下载记录和无共享引用的本地文件。
func (s *Store) DeleteTweets(ctx context.Context, tweetIDs []string, deleteFiles bool) (DeleteResult, error) {
	result := DeleteResult{}
	clean := make([]string, 0, len(tweetIDs))
	seen := map[string]bool{}
	for _, raw := range tweetIDs {
		id := strings.TrimSpace(raw)
		if id != "" && !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return result, nil
	}
	args := make([]any, len(clean))
	for i, id := range clean {
		args[i] = id
	}
	ph := sqlPlaceholders(len(clean))

	if deleteFiles {
		var downloadIDs []int64
		if err := s.db.SelectContext(ctx, &downloadIDs, `SELECT id FROM downloads WHERE tweet_id IN (`+ph+`) AND tweet_id <> ''`, args...); err != nil {
			return result, err
		}
		if len(downloadIDs) > 0 {
			dlRes, err := s.DeleteDownloads(ctx, downloadIDs, true)
			if err != nil {
				return result, err
			}
			result.DeletedFiles += dlRes.DeletedFiles
			result.SkippedFiles += dlRes.SkippedFiles
		}
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM tweet_relations WHERE tweet_id IN (` + ph + `)`,
		`DELETE FROM tweet_jobs WHERE tweet_id IN (` + ph + `)`,
		`DELETE FROM tweet_media WHERE tweet_id IN (` + ph + `)`,
		`DELETE FROM tweet_tags WHERE tweet_id IN (` + ph + `)`,
		`DELETE FROM collection_items WHERE item_type = 'tweet' AND item_id IN (` + ph + `)`,
		`DELETE FROM tweets_fts WHERE tweet_id IN (` + ph + `)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return result, err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM tweets WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return result, err
	}
	if n, err := res.RowsAffected(); err == nil {
		result.DeletedRecords = int(n)
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	s.invalidateLibraryDownloadsCache()
	return result, nil
}

func formatInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}

// removeFileWithinRoots 仅在路径严格位于可信下载根内时删除文件，防止路径穿越删除系统文件。
func removeFileWithinRoots(target string, roots []string) bool {
	if strings.TrimSpace(target) == "" || len(roots) == 0 {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	absTarget = filepath.Clean(absTarget)
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		absRoot = filepath.Clean(absRoot)
		rel, err := filepath.Rel(absRoot, absTarget)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		if err := os.Remove(absTarget); err == nil {
			return true
		}
		return false
	}
	return false
}
