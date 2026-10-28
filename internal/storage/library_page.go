package storage

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

// 媒体库服务端分页（§3.1 / R5）：旧实现把最多 10 万条记录连同全目录 WalkDir 一次性读入
// 内存，再在 HTTP 层做内存切片分页。本文件提供真正走 SQL LIMIT/OFFSET + COUNT + 过滤的
// 分页接口，供 GET /api/library/downloads?page=... 使用；旧的
// ListLibraryDownloadsWithStatus 保留给非分页数组接口，行为不变。
const (
	// DefaultLibraryPageSize 是分页请求未指定 pageSize 时的默认值。
	DefaultLibraryPageSize = 60
	// MaxLibraryPageSize 是分页请求 pageSize 的上限。
	MaxLibraryPageSize = 200
	// MaxDownloadRoots 是 download_roots 表中可信下载根的保留上限（见 PruneDownloadRoots）。
	MaxDownloadRoots = 32
)

const (
	libraryKindAll    = "all"
	libraryKindImages = "images"
	libraryKindVideos = "videos"
	libraryKindGifs   = "gifs"
	// libraryUserUnknown 表示"没有归属用户"的过滤值（文件不在任何 user_entities 目录下）。
	libraryUserUnknown = "unknown"
)

// LibraryFilter 是媒体库分页的服务端过滤条件。Kind/User/Search 为空或 "all" 表示不过滤；
// User 的 "unknown" 表示没有归属用户（不在任何 user_entities 目录下）的文件。
type LibraryFilter struct {
	Kind   string
	User   string
	Search string
}

// LibraryPageOptions 是分页请求参数。
type LibraryPageOptions struct {
	Page     int
	PageSize int
	Filter   LibraryFilter
}

// LibraryUserOption 是媒体库用户筛选下拉项，与前端 {value,label} 契约一致。
type LibraryUserOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// LibraryPage 是分页响应，可直接 JSON 序列化给前端。
type LibraryPage struct {
	Items      []DownloadRecord    `json:"items"`
	Pagination Pagination          `json:"pagination"`
	Counts     map[string]int      `json:"counts"`
	Users      []LibraryUserOption `json:"users"`
	Truncated  bool                `json:"truncated"`
}

var (
	libraryImageExts = []string{".jpg", ".jpeg", ".png", ".webp", ".avif"}
	libraryVideoExts = []string{".mp4", ".mov", ".m4v", ".webm", ".ogv"}
	libraryGifExts   = []string{".gif"}
)

// libraryEntityPrefix 是一个 user_entities 目录前缀（小写、正斜杠、以 "/" 结尾）。
type libraryEntityPrefix struct {
	prefix string
	screen string
	name   string
}

type libraryCounts struct {
	all    int
	images int
	videos int
	gifs   int
}

// librarySnapshot 是媒体库分页使用的进程内只读快照：孤儿文件、用户归属前缀、用户 facet
// 与全局分类计数。它随 libraryCacheGen 失效、最长 5 分钟刷新一次，因此分页请求不会每次
// 都重新扫描 downloads 全表与整个下载目录。
type librarySnapshot struct {
	gen            uint64
	at             time.Time
	orphans        []DownloadRecord // 无下载记录的磁盘媒体文件，按 CreatedAt 降序
	prefixes       []libraryEntityPrefix
	prefixIndex    map[string]int // 实体前缀 -> prefixes 下标（取 id 最小者）
	users          []LibraryUserOption
	unknownEntries int // 无归属的 entries（含 file_path 为空的记录）
	counts         libraryCounts
}

// ListLibraryDownloadsPage 返回媒体库按 page/pageSize 分页、并已按 Kind/User/Search 过滤的
// 结果。排序为 created_at DESC, id DESC；同一 file_path 的多条下载记录只保留最新一条
// （与旧 HTTP 层实现的分组语义一致）；磁盘上无记录的文件（孤儿）并入同一排序与分页。
//
// Counts/Users 是不受过滤条件影响的全局 facet（与旧响应语义一致）。
func (s *Store) ListLibraryDownloadsPage(ctx context.Context, opts LibraryPageOptions) (LibraryPage, error) {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = DefaultLibraryPageSize
	}
	if pageSize > MaxLibraryPageSize {
		pageSize = MaxLibraryPageSize
	}
	page := opts.Page
	if page < 1 {
		page = 1
	}
	filter := normalizeLibraryFilter(opts.Filter)
	snap, err := s.librarySnapshot(ctx)
	if err != nil {
		return LibraryPage{}, err
	}
	query := buildLibraryQuery(filter, snap)
	orphans := filterLibraryOrphans(snap.orphans, filter)

	var dbTotal int
	var dbItems []DownloadRecord
	need := 0
	if !query.noMatch {
		// 为与孤儿文件合并后再取窗口，DB 侧至少取到 offset+pageSize。
		need = (page-1)*pageSize + pageSize
		if need > MaxLibraryDownloadsLimit {
			need = MaxLibraryDownloadsLimit
		}
	}
	switch {
	case query.noMatch:
		// 条件不可能匹配任何记录（未知 kind、或用户没有任何实体目录）。
	case query.userUnknown:
		// 归属只能在 Go 侧逐条判定；扫描时顺带统计满足条件的无归属总数。
		dbItems, dbTotal, err = s.scanUnknownLibraryEntries(ctx, query, snap, need)
		if err != nil {
			return LibraryPage{}, err
		}
	default:
		if dbTotal, err = s.countLibraryEntries(ctx, query); err != nil {
			return LibraryPage{}, err
		}
		if need > 0 {
			if dbItems, err = s.queryLibraryEntries(ctx, query, need, 0); err != nil {
				return LibraryPage{}, err
			}
		}
	}

	total := dbTotal + len(orphans)
	truncated := total > MaxLibraryDownloadsLimit
	if truncated {
		total = MaxLibraryDownloadsLimit
	}
	totalPages := max(1, (total+pageSize-1)/pageSize)
	if page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * pageSize

	merged := mergeLibraryEntries(dbItems, orphans, offset+pageSize)
	if offset < len(merged) {
		merged = merged[offset:]
	} else {
		merged = nil
	}
	if len(merged) > pageSize {
		merged = merged[:pageSize]
	}
	if merged == nil {
		merged = []DownloadRecord{}
	}
	for index := range merged {
		if merged[index].PreviewURL == "" {
			merged[index].PreviewURL = deriveVideoPreviewURL(merged[index].MediaURL)
		}
		if merged[index].UserScreenName == "" && merged[index].FilePath != "" {
			if prefix, ok := snap.matchUser(merged[index].FilePath); ok {
				merged[index].UserScreenName = prefix.screen
				merged[index].UserName = prefix.name
			}
		}
	}
	users := append([]LibraryUserOption(nil), snap.users...)
	if users == nil {
		users = []LibraryUserOption{}
	}
	return LibraryPage{
		Items: merged,
		Pagination: Pagination{
			Page:       page,
			PageSize:   pageSize,
			Total:      total,
			TotalPages: totalPages,
		},
		Counts: map[string]int{
			libraryKindAll:    snap.counts.all,
			libraryKindImages: snap.counts.images,
			libraryKindVideos: snap.counts.videos,
			libraryKindGifs:   snap.counts.gifs,
		},
		Users:     users,
		Truncated: truncated,
	}, nil
}

func normalizeLibraryFilter(filter LibraryFilter) LibraryFilter {
	filter.Kind = strings.ToLower(strings.TrimSpace(filter.Kind))
	if filter.Kind == "" {
		filter.Kind = libraryKindAll
	}
	filter.User = strings.TrimSpace(filter.User)
	if filter.User == "" {
		filter.User = libraryKindAll
	}
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	return filter
}

// libraryQuery 是记录过滤条件在 SQL 侧的表示。
type libraryQuery struct {
	where       string // "" 或 "WHERE ..."
	args        []any
	userUnknown bool // User=="unknown"：归属判定必须在 Go 侧做，SQL 不含用户条件
	noMatch     bool // 条件不可能匹配任何记录
}

func buildLibraryQuery(filter LibraryFilter, snap *librarySnapshot) libraryQuery {
	conds := make([]string, 0, 3)
	args := make([]any, 0, 6)
	if kindSQL, kindArgs, kindOK := libraryKindSQL("d.file_path", filter.Kind); !kindOK {
		return libraryQuery{noMatch: true}
	} else if kindSQL != "" {
		conds = append(conds, kindSQL)
		args = append(args, kindArgs...)
	}
	if filter.Search != "" {
		conds = append(conds, "(instr(lower(d.file_path), ?) > 0 OR instr(lower(d.media_url), ?) > 0 OR instr(lower(d.tweet_id), ?) > 0)")
		args = append(args, filter.Search, filter.Search, filter.Search)
	}
	result := libraryQuery{}
	if filter.User != libraryKindAll {
		if filter.User == libraryUserUnknown {
			// 归属由 user_entities 目录前缀决定，SQLite 无法在表达式深度/参数上限内表达
			// "不在任意实体目录下"，因此这里的用户条件留到 Go 侧逐条判定。
			result.userUnknown = true
		} else {
			prefixes := snap.prefixesForUser(filter.User)
			if len(prefixes) == 0 {
				return libraryQuery{noMatch: true}
			}
			parts := make([]string, 0, len(prefixes))
			for _, prefix := range prefixes {
				parts = append(parts, "instr(lower(replace(d.file_path, char(92), '/')), ?) = 1")
				args = append(args, prefix)
			}
			conds = append(conds, "("+strings.Join(parts, " OR ")+")")
		}
	}
	if len(conds) > 0 {
		result.where = "WHERE " + strings.Join(conds, " AND ")
	}
	result.args = args
	return result
}

func libraryKindExts(kind string) ([]string, bool) {
	switch kind {
	case libraryKindAll:
		return nil, true
	case libraryKindImages:
		return libraryImageExts, true
	case libraryKindVideos:
		return libraryVideoExts, true
	case libraryKindGifs:
		return libraryGifExts, true
	default:
		return nil, false
	}
}

// libraryKindSQL 返回对单个路径列的分类过滤 SQL；第三个返回值告诉调用方 kind 是否合法。
func libraryKindSQL(column, kind string) (string, []any, bool) {
	exts, ok := libraryKindExts(kind)
	if !ok {
		return "", nil, false
	}
	if exts == nil {
		return "", nil, true
	}
	parts := make([]string, 0, len(exts))
	args := make([]any, 0, len(exts))
	for _, ext := range exts {
		parts = append(parts, "lower("+column+") LIKE ?")
		args = append(args, "%"+ext)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args, true
}

// libraryKindMatches 报告路径是否属于 kind 分类；未知 kind 返回 false（与旧 HTTP 层
// "category != kind 全部过滤掉" 的语义一致）。
func libraryKindMatches(path, kind string) bool {
	exts, ok := libraryKindExts(kind)
	if !ok {
		return false
	}
	if exts == nil {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, candidate := range exts {
		if ext == candidate {
			return true
		}
	}
	return false
}

func libraryCategory(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case containsString(libraryImageExts, ext):
		return libraryKindImages
	case containsString(libraryVideoExts, ext):
		return libraryKindVideos
	case containsString(libraryGifExts, ext):
		return libraryKindGifs
	default:
		return "other"
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func libraryEntriesSQL(where string) string {
	return `
WITH filtered AS (
	SELECT d.id, d.job_id, d.tweet_id, d.media_url, d.preview_url, d.file_path, d.bytes,
		d.created_at, d.media_key, d.content_hash,
		CASE WHEN d.file_path = '' THEN 'record:' || d.id ELSE d.file_path END AS entry_key
	FROM downloads d
	` + where + `
), ranked AS (
	SELECT filtered.*, ROW_NUMBER() OVER (PARTITION BY entry_key ORDER BY created_at DESC, id DESC) AS rn
	FROM filtered
)
SELECT id, job_id, tweet_id, media_url, preview_url, file_path, bytes, created_at, media_key, content_hash
FROM ranked
WHERE rn = 1
ORDER BY created_at DESC, id DESC`
}

func (s *Store) queryLibraryEntries(ctx context.Context, query libraryQuery, limit, offset int) ([]DownloadRecord, error) {
	if query.noMatch || limit <= 0 {
		return nil, nil
	}
	items := []DownloadRecord{}
	args := append(append([]any{}, query.args...), limit, offset)
	err := s.db.SelectContext(ctx, &items, libraryEntriesSQL(query.where)+" LIMIT ? OFFSET ?", args...)
	return items, err
}

func (s *Store) countLibraryEntries(ctx context.Context, query libraryQuery) (int, error) {
	if query.noMatch {
		return 0, nil
	}
	var total int
	sqlText := `SELECT COUNT(*) FROM (
	SELECT 1 FROM downloads d ` + query.where + `
	GROUP BY CASE WHEN d.file_path = '' THEN 'record:' || d.id ELSE d.file_path END)`
	err := s.db.GetContext(ctx, &total, sqlText, query.args...)
	return total, err
}

// scanUnknownLibraryEntries 在 SQL 侧只应用 kind/search，归属在 Go 侧按实体前缀判定：
// 返回前 need 条无归属记录，并统计满足过滤条件的无归属总数。仅用于 user=="unknown"
// （该过滤值在正常前端里很少被选中，扫描成本可接受）。
func (s *Store) scanUnknownLibraryEntries(ctx context.Context, query libraryQuery, snap *librarySnapshot, need int) ([]DownloadRecord, int, error) {
	if query.noMatch {
		return nil, 0, nil
	}
	const chunkSize = 512
	items := make([]DownloadRecord, 0, need)
	total := 0
	scanOffset := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		batch, err := s.queryLibraryEntries(ctx, query, chunkSize, scanOffset)
		if err != nil {
			return nil, 0, err
		}
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			if _, ok := snap.matchUser(item.FilePath); ok {
				continue
			}
			total++
			if len(items) < need {
				items = append(items, item)
			}
		}
		scanOffset += len(batch)
		if len(batch) < chunkSize {
			break
		}
	}
	return items, total, nil
}

// matchUser 按"目录前缀逐级上溯、取 id 最小的实体"判定路径归属（与
// attributeLibraryUsers 语义一致）。
func (snap *librarySnapshot) matchUser(path string) (libraryEntityPrefix, bool) {
	if path == "" {
		return libraryEntityPrefix{}, false
	}
	lower := libraryMatchPath(path)
	best := -1
	for slash := strings.LastIndexByte(lower, '/'); slash >= 0; slash = strings.LastIndexByte(lower[:slash], '/') {
		if index, ok := snap.prefixIndex[lower[:slash+1]]; ok && (best < 0 || index < best) {
			best = index
		}
	}
	if best < 0 {
		return libraryEntityPrefix{}, false
	}
	return snap.prefixes[best], true
}

func (snap *librarySnapshot) prefixesForUser(user string) []string {
	prefixes := make([]string, 0, 2)
	for _, prefix := range snap.prefixes {
		if prefix.screen == user {
			prefixes = append(prefixes, prefix.prefix)
		}
	}
	return prefixes
}

func filterLibraryOrphans(orphans []DownloadRecord, filter LibraryFilter) []DownloadRecord {
	if filter.User != libraryKindAll && filter.User != libraryUserUnknown {
		// 孤儿文件没有归属目录，因此任何具体用户的过滤都不匹配。
		return nil
	}
	items := make([]DownloadRecord, 0, len(orphans))
	for _, item := range orphans {
		if !libraryKindMatches(item.FilePath, filter.Kind) {
			continue
		}
		if filter.Search != "" && !strings.Contains(libraryMatchPath(item.FilePath), filter.Search) {
			continue
		}
		items = append(items, item)
	}
	return items
}

// mergeLibraryEntries 合并两个各自按 CreatedAt 降序的列表；时间相同时 DB 记录优先
// （旧实现是 stable sort，先加入的 DB 记录在前）。合并时按条目键去重：快照里的孤儿列表
// 与当前 DB 结果之间存在一瞬的时间差（文件刚记录/刚写入），同一文件可能两边各出现一次；
// 去重时优先保留 DB 记录（ID != 0，信息更全）。
func mergeLibraryEntries(dbItems, orphans []DownloadRecord, need int) []DownloadRecord {
	if need <= 0 {
		return nil
	}
	merged := make([]DownloadRecord, 0, min(need, len(dbItems)+len(orphans)))
	positions := make(map[string]int, len(dbItems)+len(orphans))
	add := func(item DownloadRecord) {
		key := item.FilePath
		if key == "" {
			key = "record:" + strconv.FormatInt(item.ID, 10)
		}
		if index, exists := positions[key]; exists {
			if item.ID != 0 && merged[index].ID == 0 {
				merged[index] = item
			}
			return
		}
		positions[key] = len(merged)
		merged = append(merged, item)
	}
	i, j := 0, 0
	for len(merged) < need && (i < len(dbItems) || j < len(orphans)) {
		takeDB := false
		switch {
		case j >= len(orphans):
			takeDB = true
		case i < len(dbItems):
			takeDB = !dbItems[i].CreatedAt.Before(orphans[j].CreatedAt)
		}
		if takeDB {
			add(dbItems[i])
			i++
		} else {
			add(orphans[j])
			j++
		}
	}
	return merged
}

// libraryMatchPath 把路径规范成"小写、正斜杠"的形式用于前缀/大小写不敏感比较。
// 只对 ASCII 做小写（SQLite 内置 lower() 同样只处理 ASCII），保证 Go 侧前缀与 SQL 侧
// 过滤条件一致；非 ASCII 目录名按原样比较。
func libraryMatchPath(path string) string {
	path = strings.ReplaceAll(path, `\`, "/")
	var builder strings.Builder
	builder.Grow(len(path))
	for i := 0; i < len(path); i++ {
		b := path[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		builder.WriteByte(b)
	}
	return builder.String()
}

func libraryEntityPrefixPath(parentDir, name string) string {
	parent := strings.TrimRight(libraryMatchPath(parentDir), "/")
	namePart := strings.Trim(libraryMatchPath(name), "/")
	if namePart == "" {
		return ""
	}
	if parent == "" {
		return namePart + "/"
	}
	return parent + "/" + namePart + "/"
}

// librarySnapshot 返回当前快照；失效（代数变化）或超过 TTL 时重建。重建由
// libraryBuildMu 串行化，避免画廊并发请求同时扫描全表/全目录。
func (s *Store) librarySnapshot(ctx context.Context) (*librarySnapshot, error) {
	if snap := s.peekLibrarySnapshot(s.libraryCacheGen.Load()); snap != nil {
		return snap, nil
	}
	s.libraryBuildMu.Lock()
	defer s.libraryBuildMu.Unlock()
	gen := s.libraryCacheGen.Load()
	if snap := s.peekLibrarySnapshot(gen); snap != nil {
		return snap, nil
	}
	snap, err := s.buildLibrarySnapshot(ctx, gen)
	if err != nil {
		return nil, err
	}
	s.librarySnapMu.Lock()
	s.librarySnap = snap
	s.librarySnapMu.Unlock()
	return snap, nil
}

func (s *Store) peekLibrarySnapshot(gen uint64) *librarySnapshot {
	s.librarySnapMu.RLock()
	defer s.librarySnapMu.RUnlock()
	snap := s.librarySnap
	if snap == nil || snap.gen != gen || time.Since(snap.at) >= libraryDownloadsCacheTTL {
		return nil
	}
	return snap
}

func (s *Store) buildLibrarySnapshot(ctx context.Context, gen uint64) (*librarySnapshot, error) {
	snap := &librarySnapshot{gen: gen, at: time.Now(), prefixIndex: map[string]int{}}
	type entityRow struct {
		ScreenName string `db:"screen_name"`
		UserName   string `db:"user_name"`
		ParentDir  string `db:"parent_dir"`
		EntityName string `db:"entity_name"`
	}
	entities := []entityRow{}
	if err := s.db.SelectContext(ctx, &entities, `
SELECT COALESCE(u.screen_name, '') AS screen_name, COALESCE(u.name, '') AS user_name,
	ue.parent_dir AS parent_dir, ue.name AS entity_name
FROM user_entities ue JOIN users u ON u.id = ue.user_id
ORDER BY ue.id`); err != nil {
		return nil, err
	}
	userByScreen := make(map[string]LibraryUserOption, len(entities))
	addUser := func(screen, name string) {
		if screen == "" {
			return
		}
		if _, exists := userByScreen[screen]; exists {
			return
		}
		label := name
		if label == "" {
			label = screen
		}
		userByScreen[screen] = LibraryUserOption{Value: screen, Label: label + " (@" + screen + ")"}
	}
	for _, entity := range entities {
		prefix := libraryEntityPrefix{
			prefix: libraryEntityPrefixPath(entity.ParentDir, entity.EntityName),
			screen: entity.ScreenName,
			name:   entity.UserName,
		}
		if prefix.prefix == "" || prefix.screen == "" {
			continue
		}
		if _, exists := snap.prefixIndex[prefix.prefix]; !exists {
			snap.prefixIndex[prefix.prefix] = len(snap.prefixes)
			snap.prefixes = append(snap.prefixes, prefix)
		}
		addUser(entity.ScreenName, entity.UserName)
	}

	// 单次流式扫描 downloads：建立"已记录路径"小写集合（孤儿排除用）、去重后的分类
	// 计数、用户 facet 与无归属数量。不在 Go 侧保留全部记录，内存与行数无关。
	known := make(map[string]struct{})
	seenEntries := make(map[string]struct{})
	rows, err := s.db.QueryxContext(ctx, `SELECT id, file_path FROM downloads`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var filePath string
		if err := rows.Scan(&id, &filePath); err != nil {
			rows.Close()
			return nil, err
		}
		entryKey := filePath
		if filePath == "" {
			entryKey = "record:" + strconv.FormatInt(id, 10)
		} else if abs, absErr := filepath.Abs(filePath); absErr == nil {
			known[libraryMatchPath(filepath.Clean(abs))] = struct{}{}
		}
		if _, seen := seenEntries[entryKey]; seen {
			continue
		}
		seenEntries[entryKey] = struct{}{}
		snap.counts.all++
		switch libraryCategory(filePath) {
		case libraryKindImages:
			snap.counts.images++
		case libraryKindVideos:
			snap.counts.videos++
		case libraryKindGifs:
			snap.counts.gifs++
		}
		if prefix, ok := snap.matchUser(filePath); ok {
			addUser(prefix.screen, prefix.name)
		} else {
			snap.unknownEntries++
		}
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return nil, rowsErr
	}

	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.StorageType == config.StorageLocal && strings.TrimSpace(cfg.DownloadDir) != "" {
		root, absErr := filepath.Abs(cfg.DownloadDir)
		if absErr != nil {
			return nil, absErr
		}
		walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			if !IsLibraryMediaPath(path) {
				return nil
			}
			cleaned := filepath.Clean(path)
			if _, recorded := known[libraryMatchPath(cleaned)]; recorded {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil
			}
			snap.orphans = append(snap.orphans, DownloadRecord{
				FilePath:  cleaned,
				Bytes:     info.Size(),
				CreatedAt: info.ModTime().UTC(),
			})
			snap.counts.all++
			switch libraryCategory(cleaned) {
			case libraryKindImages:
				snap.counts.images++
			case libraryKindVideos:
				snap.counts.videos++
			case libraryKindGifs:
				snap.counts.gifs++
			}
			return nil
		})
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		_ = walkErr // 根目录不存在/不可读沿用旧行为：忽略，返回已收集结果。
	}
	sort.SliceStable(snap.orphans, func(i, j int) bool {
		return snap.orphans[i].CreatedAt.After(snap.orphans[j].CreatedAt)
	})
	if snap.unknownEntries > 0 || len(snap.orphans) > 0 {
		userByScreen[libraryUserUnknown] = LibraryUserOption{Value: libraryUserUnknown, Label: "未识别用户"}
	}
	snap.users = make([]LibraryUserOption, 0, len(userByScreen))
	for _, user := range userByScreen {
		snap.users = append(snap.users, user)
	}
	sort.Slice(snap.users, func(i, j int) bool { return snap.users[i].Label < snap.users[j].Label })
	return snap, nil
}
