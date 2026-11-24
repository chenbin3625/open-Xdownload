package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StorageStats 汇总磁盘容量、归档媒体分布与 Top 用户占用，用于存储统计面板。
type StorageStats struct {
	DownloadDir     string               `json:"downloadDir"`
	DiskTotalBytes  uint64               `json:"diskTotalBytes"`
	DiskFreeBytes   uint64               `json:"diskFreeBytes"`
	DiskAvailable   bool                 `json:"diskAvailable"`
	TotalTweets     int                  `json:"totalTweets"`
	TotalDownloads  int                  `json:"totalDownloads"`
	TotalBytes      int64                `json:"totalBytes"`
	ByKind          map[string]KindStat  `json:"byKind"`
	TopUsers        []LibraryUserSummary `json:"topUsers"`
	DuplicateGroups int                  `json:"duplicateGroups"`
	DuplicateBytes  int64                `json:"duplicateBytes"`
	DatabaseBytes   int64                `json:"databaseBytes"`
}

type KindStat struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// MaintenanceScanResult 汇报库维护扫描结果：断链记录、磁盘孤儿文件与内容重复组。
type MaintenanceScanResult struct {
	BrokenRecords   []BrokenDownloadItem `json:"brokenRecords"`
	OrphanFiles     []OrphanFileItem     `json:"orphanFiles"`
	DuplicateGroups []DuplicateHashGroup `json:"duplicateGroups"`
	BrokenCount     int                  `json:"brokenCount"`
	OrphanCount     int                  `json:"orphanCount"`
	DuplicateCount  int                  `json:"duplicateCount"`
	ReclaimableSize int64                `json:"reclaimableBytes"`
}

type BrokenDownloadItem struct {
	ID       int64  `json:"id"`
	TweetID  string `json:"tweetId"`
	FilePath string `json:"filePath"`
	Bytes    int64  `json:"bytes"`
}

type OrphanFileItem struct {
	FilePath string    `json:"filePath"`
	Bytes    int64     `json:"bytes"`
	ModTime  time.Time `json:"modTime"`
}

type DuplicateHashGroup struct {
	ContentHash string   `json:"contentHash"`
	FilePaths   []string `json:"filePaths"`
	Bytes       int64    `json:"bytes"`
}

type MaintenanceCleanupResult struct {
	Action           string `json:"action"`
	Affected         int    `json:"affected"`
	ReclaimedBytes   int64  `json:"reclaimedBytes"`
	Skipped          int    `json:"skipped"`
	CreatedBackupJob int64  `json:"createdJobId,omitempty"`
}

// GetStorageStats 计算当前归档库与磁盘空间统计。
func (s *Store) GetStorageStats(ctx context.Context) (StorageStats, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return StorageStats{}, err
	}
	stats := StorageStats{
		DownloadDir: cfg.DownloadDir,
		ByKind: map[string]KindStat{
			"images": {},
			"videos": {},
			"gifs":   {},
			"other":  {},
		},
		TopUsers: []LibraryUserSummary{},
	}
	if cfg.DownloadDir != "" {
		if total, free, ok := diskUsageForPath(cfg.DownloadDir); ok {
			stats.DiskTotalBytes = total
			stats.DiskFreeBytes = free
			stats.DiskAvailable = true
		}
	}
	if info, err := os.Stat(filepath.Join(s.dataDir, "open-xdownload.db")); err == nil {
		stats.DatabaseBytes = info.Size()
	}
	_ = s.db.GetContext(ctx, &stats.TotalTweets, `SELECT COUNT(*) FROM tweets`)
	type dlMeta struct {
		FilePath string `db:"file_path"`
		Bytes    int64  `db:"bytes"`
	}
	rows := []dlMeta{}
	if err := s.db.SelectContext(ctx, &rows, `
SELECT file_path, MAX(bytes) AS bytes FROM downloads WHERE file_path <> '' GROUP BY file_path`); err != nil {
		return stats, err
	}
	stats.TotalDownloads = len(rows)
	for _, r := range rows {
		stats.TotalBytes += r.Bytes
		kind := classifyLibraryKind(r.FilePath)
		ks := stats.ByKind[kind]
		ks.Count++
		ks.Bytes += r.Bytes
		stats.ByKind[kind] = ks
	}
	// 统计可合并的重复 content_hash（对应多个不同磁盘路径）
	type dupRow struct {
		Hash  string `db:"content_hash"`
		Paths int    `db:"paths"`
		Bytes int64  `db:"bytes"`
	}
	dups := []dupRow{}
	if err := s.db.SelectContext(ctx, &dups, `
SELECT content_hash, COUNT(DISTINCT file_path) AS paths, MAX(bytes) AS bytes
FROM downloads
WHERE content_hash <> '' AND file_path <> ''
GROUP BY content_hash
HAVING COUNT(DISTINCT file_path) > 1`); err == nil {
		stats.DuplicateGroups = len(dups)
		for _, d := range dups {
			stats.DuplicateBytes += d.Bytes * int64(d.Paths-1)
		}
	}
	if users, err := s.ListLibraryUsers(ctx); err == nil {
		sort.Slice(users, func(i, j int) bool {
			if users[i].TotalBytes != users[j].TotalBytes {
				return users[i].TotalBytes > users[j].TotalBytes
			}
			return users[i].MediaCount > users[j].MediaCount
		})
		if len(users) > 15 {
			users = users[:15]
		}
		stats.TopUsers = users
	}
	return stats, nil
}

func classifyLibraryKind(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	for _, e := range libraryImageExts {
		if ext == e {
			return "images"
		}
	}
	for _, e := range libraryVideoExts {
		if ext == e {
			return "videos"
		}
	}
	for _, e := range libraryGifExts {
		if ext == e {
			return "gifs"
		}
	}
	return "other"
}

// ScanMaintenance 扫描断链记录、磁盘孤儿文件和相同 content_hash 的多路径重复文件。
func (s *Store) ScanMaintenance(ctx context.Context) (MaintenanceScanResult, error) {
	res := MaintenanceScanResult{
		BrokenRecords:   []BrokenDownloadItem{},
		OrphanFiles:     []OrphanFileItem{},
		DuplicateGroups: []DuplicateHashGroup{},
	}
	type dlRow struct {
		ID          int64  `db:"id"`
		TweetID     string `db:"tweet_id"`
		FilePath    string `db:"file_path"`
		Bytes       int64  `db:"bytes"`
		ContentHash string `db:"content_hash"`
	}
	rows := []dlRow{}
	if err := s.db.SelectContext(ctx, &rows, `SELECT id, tweet_id, file_path, bytes, content_hash FROM downloads ORDER BY id DESC`); err != nil {
		return res, err
	}
	trackedPaths := make(map[string]bool, len(rows))
	hashPaths := map[string]map[string]int64{}
	for _, r := range rows {
		if r.FilePath == "" {
			continue
		}
		clean := filepath.Clean(r.FilePath)
		trackedPaths[clean] = true
		if _, err := os.Stat(clean); err != nil {
			if os.IsNotExist(err) {
				res.BrokenCount++
				if len(res.BrokenRecords) < 500 {
					res.BrokenRecords = append(res.BrokenRecords, BrokenDownloadItem{
						ID:       r.ID,
						TweetID:  r.TweetID,
						FilePath: clean,
						Bytes:    r.Bytes,
					})
				}
			}
			continue
		}
		if r.ContentHash != "" {
			if hashPaths[r.ContentHash] == nil {
				hashPaths[r.ContentHash] = map[string]int64{}
			}
			hashPaths[r.ContentHash][clean] = r.Bytes
		}
	}
	for hash, pathsMap := range hashPaths {
		if len(pathsMap) <= 1 {
			continue
		}
		paths := make([]string, 0, len(pathsMap))
		var bytes int64
		for p, b := range pathsMap {
			paths = append(paths, p)
			if b > bytes {
				bytes = b
			}
		}
		sort.Strings(paths)
		res.DuplicateCount++
		res.ReclaimableSize += bytes * int64(len(paths)-1)
		if len(res.DuplicateGroups) < 200 {
			res.DuplicateGroups = append(res.DuplicateGroups, DuplicateHashGroup{
				ContentHash: hash,
				FilePaths:   paths,
				Bytes:       bytes,
			})
		}
	}
	// 扫描下载目录中的孤儿媒体文件
	cfg, err := s.GetConfig(ctx)
	if err == nil && strings.TrimSpace(cfg.DownloadDir) != "" {
		root := filepath.Clean(cfg.DownloadDir)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || ctx.Err() != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if classifyLibraryKind(p) == "other" {
				return nil
			}
			clean := filepath.Clean(p)
			if trackedPaths[clean] {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			res.OrphanCount++
			if len(res.OrphanFiles) < 500 {
				res.OrphanFiles = append(res.OrphanFiles, OrphanFileItem{
					FilePath: clean,
					Bytes:    info.Size(),
					ModTime:  info.ModTime().UTC(),
				})
			}
			return nil
		})
	}
	return res, nil
}

// RunMaintenanceCleanup 执行指定维护清理动作：
// - prune_broken: 清理本地文件已不存在的断链 downloads 记录
// - delete_orphans: 删除未被数据库追踪的磁盘孤儿媒体文件
// - import_orphans: 将磁盘孤儿媒体文件登记进 downloads 表并计算 content_hash
// - dedup_hardlink: 对相同 content_hash 的不同磁盘副本执行原子硬链接替换以节省磁盘空间
func (s *Store) RunMaintenanceCleanup(ctx context.Context, action string) (MaintenanceCleanupResult, error) {
	res := MaintenanceCleanupResult{Action: action}
	scan, err := s.ScanMaintenance(ctx)
	if err != nil {
		return res, err
	}
	switch action {
	case "prune_broken":
		ids := make([]int64, 0, len(scan.BrokenRecords))
		for _, item := range scan.BrokenRecords {
			ids = append(ids, item.ID)
		}
		delRes, err := s.DeleteDownloads(ctx, ids, false)
		if err != nil {
			return res, err
		}
		res.Affected = delRes.DeletedRecords
		return res, nil
	case "delete_orphans":
		roots, _ := s.DownloadRoots(ctx)
		for _, orphan := range scan.OrphanFiles {
			if removeFileWithinRoots(orphan.FilePath, roots) {
				res.Affected++
				res.ReclaimedBytes += orphan.Bytes
			} else {
				res.Skipped++
			}
		}
		s.invalidateLibraryDownloadsCache()
		return res, nil
	case "import_orphans":
		if len(scan.OrphanFiles) == 0 {
			return res, nil
		}
		job, err := s.CreateArchiveJob(ctx, JobDraft{
			Kind:   JobKindMediaURL,
			Input:  "orphan-import",
			Title:  "孤儿媒体入库",
			Source: JobSourceMaintenance,
		})
		if err != nil {
			return res, err
		}
		res.CreatedBackupJob = job.ID
		for _, orphan := range scan.OrphanFiles {
			hash, _ := fileSHA256(orphan.FilePath)
			_, err := s.CreateDownload(ctx, DownloadRecord{
				JobID:       job.ID,
				MediaURL:    "file://" + filepath.ToSlash(orphan.FilePath),
				FilePath:    orphan.FilePath,
				Bytes:       orphan.Bytes,
				ContentHash: hash,
			})
			if err == nil {
				res.Affected++
			} else {
				res.Skipped++
			}
		}
		job.Status = JobCompleted
		job.Progress = 1
		job.Message = fmt.Sprintf("已登记 %d 个本地孤儿文件", res.Affected)
		_ = s.UpdateJob(ctx, job)
		s.invalidateLibraryDownloadsCache()
		return res, nil
	case "dedup_hardlink":
		roots, _ := s.DownloadRoots(ctx)
		for _, group := range scan.DuplicateGroups {
			if len(group.FilePaths) < 2 {
				continue
			}
			canonical := group.FilePaths[0]
			canonicalInfo, err := os.Stat(canonical)
			if err != nil {
				continue
			}
			for _, dup := range group.FilePaths[1:] {
				dupInfo, err := os.Stat(dup)
				if err != nil {
					res.Skipped++
					continue
				}
				if os.SameFile(canonicalInfo, dupInfo) {
					continue
				}
				if !pathWithinAnyRoot(dup, roots) || !pathWithinAnyRoot(canonical, roots) {
					res.Skipped++
					continue
				}
				tmp := dup + ".hl.tmp"
				_ = os.Remove(tmp)
				if err := os.Link(canonical, tmp); err != nil {
					res.Skipped++
					continue
				}
				if err := os.Rename(tmp, dup); err != nil {
					_ = os.Remove(tmp)
					res.Skipped++
					continue
				}
				res.Affected++
				res.ReclaimedBytes += dupInfo.Size()
			}
		}
		return res, nil
	default:
		return res, ErrValidation("不支持的维护操作: " + action)
	}
}

func pathWithinAnyRoot(target string, roots []string) bool {
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
		rel, err := filepath.Rel(filepath.Clean(absRoot), absTarget)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CreateOnlineBackup 使用 SQLite VACUUM INTO 生成一致的热备份快照文件（无需停服）。
func (s *Store) CreateOnlineBackup(ctx context.Context, destPath string) error {
	clean := filepath.Clean(destPath)
	_ = os.Remove(clean)
	// VACUUM INTO 不支持绑定参数；此处对单引号进行标准 SQL 转义，路径由服务端受控生成。
	escaped := strings.ReplaceAll(clean, "'", "''")
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`VACUUM INTO '%s'`, escaped))
	return err
}
