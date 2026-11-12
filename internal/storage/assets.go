package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/downloader"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

type Asset struct {
	ID            string           `json:"id" db:"id"`
	Kind          config.AssetKind `json:"kind" db:"kind"`
	PolicyVersion int64            `json:"policyVersion" db:"policy_version"`
	TargetID      string           `json:"targetId" db:"target_id"`
	TargetVersion int64            `json:"targetVersion" db:"target_version"`
	Key           string           `json:"key" db:"object_key"`
	ContentHash   string           `json:"contentHash" db:"content_hash"`
	Size          int64            `json:"size" db:"size"`
	ContentType   string           `json:"contentType" db:"content_type"`
	CreatedAt     time.Time        `json:"createdAt" db:"created_at"`
}

type MediaAsset struct {
	MediaKey  string           `json:"mediaKey" db:"media_key"`
	Kind      config.AssetKind `json:"kind" db:"kind"`
	AssetID   string           `json:"assetId" db:"asset_id"`
	Status    string           `json:"status" db:"status"`
	Error     string           `json:"error" db:"error"`
	JobID     int64            `json:"jobId" db:"job_id"`
	UpdatedAt time.Time        `json:"updatedAt" db:"updated_at"`
}

func (s *Store) migrateAssets() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS storage_versions(version INTEGER PRIMARY KEY AUTOINCREMENT, document TEXT NOT NULL, created_at DATETIME NOT NULL);
CREATE TABLE IF NOT EXISTS job_storage(job_id INTEGER PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE, policy_version INTEGER NOT NULL REFERENCES storage_versions(version));
CREATE TABLE IF NOT EXISTS assets(
 id TEXT PRIMARY KEY,kind TEXT NOT NULL,policy_version INTEGER NOT NULL REFERENCES storage_versions(version),
 target_id TEXT NOT NULL,target_version INTEGER NOT NULL,object_key TEXT NOT NULL,content_hash TEXT NOT NULL,
 size INTEGER NOT NULL,content_type TEXT NOT NULL,created_at DATETIME NOT NULL,
 UNIQUE(target_id,target_version,object_key)
);
CREATE INDEX IF NOT EXISTS idx_assets_hash ON assets(content_hash,kind,target_id,target_version);
CREATE TABLE IF NOT EXISTS media_assets(
 media_key TEXT NOT NULL,kind TEXT NOT NULL,asset_id TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',
 job_id INTEGER NOT NULL REFERENCES jobs(id),updated_at DATETIME NOT NULL,PRIMARY KEY(media_key,kind)
);
CREATE TABLE IF NOT EXISTS download_assets(download_id INTEGER NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,kind TEXT NOT NULL,asset_id TEXT NOT NULL REFERENCES assets(id),PRIMARY KEY(download_id,kind));
CREATE TABLE IF NOT EXISTS media_context(media_key TEXT PRIMARY KEY,tweet_id TEXT NOT NULL,payload TEXT NOT NULL,position INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS asset_migration_progress(
 job_id INTEGER PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,last_id INTEGER NOT NULL DEFAULT 0,
 max_id INTEGER NOT NULL,total INTEGER NOT NULL,processed INTEGER NOT NULL DEFAULT 0,failed INTEGER NOT NULL DEFAULT 0
);
`)
	return err
}

func (s *Store) GetStorageDocument(ctx context.Context) (config.StorageDocument, error) {
	var row struct {
		Version  int64  `db:"version"`
		Document string `db:"document"`
	}
	err := s.db.GetContext(ctx, &row, `SELECT version,document FROM storage_versions ORDER BY version DESC LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		cfg, err := s.GetConfig(ctx)
		return config.DefaultStorage(cfg.DownloadDir), err
	}
	if err != nil {
		return config.StorageDocument{}, err
	}
	var d config.StorageDocument
	err = json.Unmarshal([]byte(row.Document), &d)
	d.Version = row.Version
	return d, err
}

func (s *Store) GetStorageVersion(ctx context.Context, version int64) (config.StorageDocument, error) {
	var raw string
	if err := s.db.GetContext(ctx, &raw, `SELECT document FROM storage_versions WHERE version=?`, version); err != nil {
		return config.StorageDocument{}, err
	}
	var d config.StorageDocument
	err := json.Unmarshal([]byte(raw), &d)
	d.Version = version
	return d, err
}

func (s *Store) SaveStorageDocument(ctx context.Context, next config.StorageDocument) (config.StorageDocument, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return next, err
	}
	defer tx.Rollback()
	var raw string
	var version int64
	err = tx.QueryRowxContext(ctx, `SELECT version,document FROM storage_versions ORDER BY version DESC LIMIT 1`).Scan(&version, &raw)
	old := config.StorageDocument{}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return next, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &old); err != nil {
			return next, err
		}
	}
	if next.Version != version {
		return next, ErrValidation("存储配置已更新，请刷新后重试")
	}
	next, err = config.MergeStorageSecrets(next, old)
	if err != nil {
		return next, ErrValidation(err.Error())
	}
	for i := range next.Targets {
		next.Targets[i].Version = version + 1
		for _, prior := range old.Targets {
			candidate := next.Targets[i]
			candidate.Version = prior.Version
			if candidate == prior {
				next.Targets[i].Version = prior.Version
				break
			}
		}
	}
	next.Version = version + 1
	encoded, err := json.Marshal(next)
	if err != nil {
		return next, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO storage_versions(version,document,created_at) VALUES(?,?,?)`, next.Version, string(encoded), time.Now().UTC()); err != nil {
		return next, err
	}
	return next, tx.Commit()
}

// The first execution fixes the strategy. Recovery of the same job reuses it.
func (s *Store) FreezeJobStorage(ctx context.Context, jobID int64) (config.StorageDocument, error) {
	var version int64
	err := s.db.GetContext(ctx, &version, `SELECT policy_version FROM job_storage WHERE job_id=?`, jobID)
	if err == nil {
		return s.GetStorageVersion(ctx, version)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return config.StorageDocument{}, err
	}
	d, err := s.GetStorageDocument(ctx)
	if err != nil {
		return d, err
	}
	if d.Version == 0 {
		d, err = s.SaveStorageDocument(ctx, d)
		if err != nil {
			d, err = s.GetStorageDocument(ctx)
			if err != nil || d.Version == 0 {
				return d, fmt.Errorf("初始化存储策略失败: %w", err)
			}
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO job_storage VALUES(?,?) ON CONFLICT(job_id) DO NOTHING`, jobID, d.Version); err != nil {
		return d, err
	}
	if err := s.db.GetContext(ctx, &version, `SELECT policy_version FROM job_storage WHERE job_id=?`, jobID); err != nil {
		return d, err
	}
	return s.GetStorageVersion(ctx, version)
}

func (s *Store) CreateAsset(ctx context.Context, a Asset) (Asset, error) {
	a.CreatedAt = time.Now().UTC()
	_, err := s.db.NamedExecContext(ctx, `INSERT INTO assets(id,kind,policy_version,target_id,target_version,object_key,content_hash,size,content_type,created_at)
VALUES(:id,:kind,:policy_version,:target_id,:target_version,:object_key,:content_hash,:size,:content_type,:created_at)`, a)
	return a, err
}
func (s *Store) GetAsset(ctx context.Context, id string) (Asset, error) {
	var a Asset
	err := s.db.GetContext(ctx, &a, `SELECT * FROM assets WHERE id=?`, id)
	return a, err
}
func (s *Store) FindAssetHash(ctx context.Context, hash string, kind config.AssetKind, target config.StorageTarget, policyVersion int64) (*Asset, error) {
	var a Asset
	err := s.db.GetContext(ctx, &a, `SELECT * FROM assets WHERE content_hash=? AND kind=? AND target_id=? AND target_version=? AND policy_version=? ORDER BY created_at LIMIT 1`, hash, kind, target.ID, target.Version, policyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}
func (s *Store) GetMediaAsset(ctx context.Context, key string, kind config.AssetKind) (*Asset, error) {
	var a Asset
	err := s.db.GetContext(ctx, &a, `SELECT a.* FROM assets a JOIN media_assets m ON m.asset_id=a.id WHERE m.media_key=? AND m.kind=? AND m.status='ready'`, key, kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}
func (s *Store) SetMediaAsset(ctx context.Context, m MediaAsset) error {
	m.UpdatedAt = time.Now().UTC()
	_, err := s.db.NamedExecContext(ctx, `INSERT INTO media_assets(media_key,kind,asset_id,status,error,job_id,updated_at) VALUES(:media_key,:kind,:asset_id,:status,:error,:job_id,:updated_at)
ON CONFLICT(media_key,kind) DO UPDATE SET asset_id=excluded.asset_id,status=excluded.status,error=excluded.error,job_id=excluded.job_id,updated_at=excluded.updated_at`, m)
	return err
}
func (s *Store) LinkDownloadAsset(ctx context.Context, downloadID int64, kind config.AssetKind, assetID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO download_assets VALUES(?,?,?) ON CONFLICT(download_id,kind) DO UPDATE SET asset_id=excluded.asset_id`, downloadID, kind, assetID)
	return err
}
func (s *Store) DownloadAssets(ctx context.Context, id int64) (map[config.AssetKind]string, error) {
	rows, err := s.db.QueryxContext(ctx, `SELECT kind,asset_id FROM download_assets WHERE download_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[config.AssetKind]string{}
	for rows.Next() {
		var kind config.AssetKind
		var value string
		if err := rows.Scan(&kind, &value); err != nil {
			return nil, err
		}
		result[kind] = value
	}
	return result, rows.Err()
}
func (s *Store) RememberMediaContext(ctx context.Context, tweet parser.TweetData) error {
	data, err := json.Marshal(tweet)
	if err != nil {
		return err
	}
	for i, media := range tweet.Media {
		key := downloader.MediaIdentity(parser.MediaDownloadURL(media))
		if key == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO media_context VALUES(?,?,?,?) ON CONFLICT(media_key) DO UPDATE SET tweet_id=excluded.tweet_id,payload=excluded.payload,position=excluded.position`, key, tweet.ID, string(data), i); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) AssetContext(ctx context.Context, key string) (parser.TweetData, parser.Media, int, error) {
	var raw string
	var index int
	err := s.db.QueryRowxContext(ctx, `SELECT payload,position FROM media_context WHERE media_key=?`, key).Scan(&raw, &index)
	if errors.Is(err, sql.ErrNoRows) {
		return parser.TweetData{CreatedAt: time.Now().UTC()}, parser.Media{ID: key}, 0, nil
	}
	if err != nil {
		return parser.TweetData{}, parser.Media{}, 0, err
	}
	var tweet parser.TweetData
	if err := json.Unmarshal([]byte(raw), &tweet); err != nil {
		return tweet, parser.Media{}, 0, err
	}
	if index < 0 || index >= len(tweet.Media) {
		return tweet, parser.Media{}, 0, fmt.Errorf("媒体上下文序号无效")
	}
	return tweet, tweet.Media[index], index, nil
}

type AssetMigrationProgress struct {
	JobID     int64 `db:"job_id"`
	LastID    int64 `db:"last_id"`
	MaxID     int64 `db:"max_id"`
	Total     int   `db:"total"`
	Processed int   `db:"processed"`
	Failed    int   `db:"failed"`
}

func (s *Store) GetAssetMigrationProgress(ctx context.Context, jobID int64) (AssetMigrationProgress, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO asset_migration_progress(job_id,max_id,total) SELECT ?,COALESCE(MAX(id),0),COUNT(*) FROM downloads WHERE 1 ON CONFLICT(job_id) DO NOTHING`, jobID); err != nil {
		return AssetMigrationProgress{}, err
	}
	var progress AssetMigrationProgress
	err := s.db.GetContext(ctx, &progress, `SELECT * FROM asset_migration_progress WHERE job_id=?`, jobID)
	return progress, err
}

func (s *Store) SaveAssetMigrationProgress(ctx context.Context, progress AssetMigrationProgress) error {
	_, err := s.db.NamedExecContext(ctx, `UPDATE asset_migration_progress SET last_id=:last_id,processed=:processed,failed=:failed WHERE job_id=:job_id`, progress)
	return err
}

func (s *Store) AssetMigrationDownloads(ctx context.Context, after, maxID int64) ([]DownloadRecord, error) {
	items := []DownloadRecord{}
	err := s.db.SelectContext(ctx, &items, `SELECT * FROM downloads WHERE id>? AND id<=? ORDER BY id LIMIT 100`, after, maxID)
	return items, err
}
