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
	// MaxDownloadRoots caps unreferenced roots. Roots still used by downloads are retained.
	MaxDownloadRoots = 32
)

// librarySnapshotMaxEntries controls how many deduplicated download entries are
// cached in memory inside librarySnapshot. Libraries exceeding this threshold
// automatically fall back to SQL querying/pagination in ListLibraryDownloadsPage
// so filtering, sorting, and pagination totals stay 100% consistent.
// Defined as a package variable so unit tests can exercise the SQL fallback path
// without inserting 100,000+ rows.
var librarySnapshotMaxEntries = MaxLibraryDownloadsLimit

const (
	libraryKindAll    = "all"
	libraryKindImages = "images"
	libraryKindVideos = "videos"
	libraryKindGifs   = "gifs"
	// libraryUserUnknown 表示既无归档作者、也无法从 user_entities 目录推断归属的文件。
	libraryUserUnknown = "unknown"
)

// LibraryFilter 是媒体库分页的服务端过滤条件。Kind/User/Search 为空或 "all" 表示不过滤；
// User 的 "unknown" 表示既无归档作者、也无用户实体目录归属的文件。
type LibraryFilter struct {
	Kind               string
	User               string
	Search             string
	From, To           time.Time // UTC; To is exclusive.
	MinBytes, MaxBytes *int64
}

// LibraryPageOptions 是分页请求参数。
type LibraryPageOptions struct {
	Page     int
	PageSize int
	Filter   LibraryFilter
	Sort     string
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

type libraryUserAggregate struct {
	screenName  string
	displayName string
	mediaCount  int
	totalBytes  int64
	latestAt    time.Time
}

// librarySnapshot 是媒体库分页使用的进程内只读快照：孤儿文件、用户归属前缀、用户 facet
// 与全局分类计数。它随 libraryCacheGen 失效、最长 5 分钟刷新一次，因此分页请求不会每次
// 都重新扫描 downloads 全表与整个下载目录。
type librarySnapshot struct {
	gen              uint64
	at               time.Time
	entries          []DownloadRecord // 去重后的下载记录（已按目录前缀补全归属），最多 librarySnapshotMaxEntries 条
	entriesTruncated bool
	orphans          []DownloadRecord // 无下载记录的磁盘媒体文件，按 CreatedAt 降序
	prefixes         []libraryEntityPrefix
	prefixIndex      map[string]int // 实体前缀 -> prefixes 下标（取 id 最小者）
	users            []LibraryUserOption
	userAggregates   map[string]*libraryUserAggregate // 全量（含超过阈值与孤儿）按小写 screen_name 聚合
	unknownEntries   int                              // 无归属的 entries（含 file_path 为空的记录）
	counts           libraryCounts
}

// ListLibraryDownloadsPage 返回媒体库按 page/pageSize 分页、并已按 Kind/User/Search 过滤的
// 结果。支持时间、体积排序；同一 file_path 的多条下载记录只保留最新一条
// （与旧 HTTP 层实现的分组语义一致）；磁盘上无记录的文件（孤儿）并入同一排序与分页。
//
// Counts/Users 是不受过滤条件影响的全局 facet（与旧响应语义一致）。
// 当下载总数超过快照上限（entriesTruncated）时自动回退到全量 SQL 查询/分页，
// 避免按用户/类型筛选、date_asc/size 排序或总数统计在超大媒体库下失真。
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
	if snap.entriesTruncated {
		return s.listLibraryDownloadsPageSQL(ctx, snap, page, pageSize, filter, opts.Sort)
	}
	// 快照包含全量条目时直接在内存切片上过滤、排序、分页，避免每次翻页重跑全表 SQL。
	entries := filterLibraryEntries(snap.entries, filter, snap)
	sort.SliceStable(entries, func(i, j int) bool { return libraryEntryLess(entries[i], entries[j], opts.Sort) })
	orphans := filterLibraryOrphans(snap.orphans, filter)
	sort.SliceStable(orphans, func(i, j int) bool { return libraryEntryLess(orphans[i], orphans[j], opts.Sort) })
	dbItems, dbTotal := entries, len(entries)

	total := dbTotal + len(orphans)
	totalPages := max(1, (total+pageSize-1)/pageSize)
	if page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * pageSize
	need := min(offset+pageSize, total)

	merged := mergeLibraryEntries(dbItems, orphans, need, opts.Sort)
	if offset < len(merged) {
		merged = merged[offset:]
	} else {
		merged = nil
	}
	if len(merged) > pageSize {
		merged = merged[:pageSize]
	}
	return buildLibraryPageResponse(snap, merged, page, pageSize, total, totalPages, false), nil
}

func buildLibraryPageResponse(snap *librarySnapshot, items []DownloadRecord, page, pageSize, total, totalPages int, truncated bool) LibraryPage {
	if items == nil {
		items = []DownloadRecord{}
	}
	for index := range items {
		if items[index].PreviewURL == "" {
			items[index].PreviewURL = deriveVideoPreviewURL(items[index].MediaURL)
		}
		if items[index].UserScreenName == "" && items[index].FilePath != "" {
			if prefix, ok := snap.matchUser(items[index].FilePath); ok {
				items[index].UserScreenName = prefix.screen
				items[index].UserName = prefix.name
			}
		}
	}
	users := append([]LibraryUserOption(nil), snap.users...)
	if users == nil {
		users = []LibraryUserOption{}
	}
	return LibraryPage{
		Items: items,
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
	}
}

func (s *Store) listLibraryDownloadsPageSQL(ctx context.Context, snap *librarySnapshot, page, pageSize int, filter LibraryFilter, order string) (LibraryPage, error) {
	if _, ok := libraryKindExts(filter.Kind); !ok {
		return buildLibraryPageResponse(snap, nil, 1, pageSize, 0, 1, false), nil
	}
	orphans := filterLibraryOrphans(snap.orphans, filter)
	sort.SliceStable(orphans, func(i, j int) bool { return libraryEntryLess(orphans[i], orphans[j], order) })

	whereClause, args := buildLibraryEntriesWhereSQL(filter)
	orderBy := libraryEntriesOrderBySQL(order)
	baseCTE := libraryEntriesCTESQL()

	hasSubMillisBounds := (!filter.From.IsZero() && filter.From.Nanosecond()%1_000_000 != 0) ||
		(!filter.To.IsZero() && filter.To.Nanosecond()%1_000_000 != 0)
	needsGoFilter := filter.User != libraryKindAll || (filter.Search != "" && !isASCIIString(filter.Search)) || hasSubMillisBounds
	if !needsGoFilter {
		var dbTotal int
		countQuery := baseCTE + ` SELECT COUNT(*) FROM ranked WHERE rn = 1` + whereClause
		if err := s.db.GetContext(ctx, &dbTotal, countQuery, args...); err != nil {
			return LibraryPage{}, err
		}
		total := dbTotal + len(orphans)
		totalPages := max(1, (total+pageSize-1)/pageSize)
		if page > totalPages {
			page = totalPages
		}
		offset := (page - 1) * pageSize
		if dbTotal == 0 {
			var pageItems []DownloadRecord
			if offset < len(orphans) {
				end := min(offset+pageSize, len(orphans))
				pageItems = append([]DownloadRecord(nil), orphans[offset:end]...)
			}
			return buildLibraryPageResponse(snap, pageItems, page, pageSize, total, totalPages, false), nil
		}
		if len(orphans) == 0 {
			dbItems := []DownloadRecord{}
			selectQuery := baseCTE + libraryEntriesSelectColsSQL + whereClause + ` ORDER BY ` + orderBy + ` LIMIT ? OFFSET ?`
			selectArgs := append(append([]any{}, args...), pageSize, offset)
			if err := s.db.SelectContext(ctx, &dbItems, selectQuery, selectArgs...); err != nil {
				return LibraryPage{}, err
			}
			return buildLibraryPageResponse(snap, dbItems, page, pageSize, total, totalPages, false), nil
		}
		dbStart, dbLimit := librarySQLMergeWindow(offset, pageSize, dbTotal, len(orphans))
		dbItems := []DownloadRecord{}
		selectQuery := baseCTE + libraryEntriesSelectColsSQL + whereClause + ` ORDER BY ` + orderBy + ` LIMIT ? OFFSET ?`
		selectArgs := append(append([]any{}, args...), dbLimit, dbStart)
		if err := s.db.SelectContext(ctx, &dbItems, selectQuery, selectArgs...); err != nil {
			return LibraryPage{}, err
		}
		pageItems := mergeLibraryWindowPage(dbItems, dbStart, orphans, offset, pageSize, order)
		return buildLibraryPageResponse(snap, pageItems, page, pageSize, total, totalPages, false), nil
	}

	// 需要按 user_entities 前缀归属或非 ASCII 关键词过滤时，按目标 SQL 排序流式扫描，
	// 仅在内存中保留当前页合并窗口（O(pageSize + len(orphans)) 内存），零截断。
	streamPage := func(reqOffset int, forcedStart int) (int, []DownloadRecord, int, error) {
		dbStart := max(0, reqOffset-len(orphans))
		if forcedStart >= 0 {
			dbStart = forcedStart
		}
		dbLimit := pageSize + len(orphans)
		if reqOffset <= len(orphans) {
			dbStart = 0
			dbLimit = reqOffset + pageSize
		}
		selectQuery := baseCTE + libraryEntriesSelectColsSQL + whereClause + ` ORDER BY ` + orderBy
		rows, err := s.db.QueryxContext(ctx, selectQuery, args...)
		if err != nil {
			return 0, nil, 0, err
		}
		defer rows.Close()

		user := ""
		if filter.User != libraryKindAll && filter.User != libraryUserUnknown {
			user = strings.ToLower(strings.TrimPrefix(filter.User, "@"))
		}
		dbTotal := 0
		window := make([]DownloadRecord, 0, min(dbLimit, pageSize+len(orphans)))
		for rows.Next() {
			var item DownloadRecord
			if err := rows.StructScan(&item); err != nil {
				return 0, nil, 0, err
			}
			if item.UserScreenName == "" && item.FilePath != "" {
				if prefix, ok := snap.matchUser(item.FilePath); ok {
					item.UserScreenName = prefix.screen
					item.UserName = prefix.name
				}
			}
			if !matchesLibraryUserAndSearch(item, filter, user) {
				continue
			}
			if dbTotal >= dbStart && len(window) < dbLimit {
				window = append(window, item)
			}
			dbTotal++
		}
		if err := rows.Err(); err != nil {
			return 0, nil, 0, err
		}
		return dbTotal, window, dbStart, nil
	}

	reqOffset := (page - 1) * pageSize
	dbTotal, dbWindow, dbStart, err := streamPage(reqOffset, -1)
	if err != nil {
		return LibraryPage{}, err
	}
	total := dbTotal + len(orphans)
	totalPages := max(1, (total+pageSize-1)/pageSize)
	actualPage := min(page, totalPages)
	actualOffset := (actualPage - 1) * pageSize
	if dbTotal == 0 {
		var pageItems []DownloadRecord
		if actualOffset < len(orphans) {
			end := min(actualOffset+pageSize, len(orphans))
			pageItems = append([]DownloadRecord(nil), orphans[actualOffset:end]...)
		}
		return buildLibraryPageResponse(snap, pageItems, actualPage, pageSize, total, totalPages, false), nil
	}
	wantStart, _ := librarySQLMergeWindow(actualOffset, pageSize, dbTotal, len(orphans))
	if actualPage != page || wantStart != dbStart {
		dbTotal, dbWindow, dbStart, err = streamPage(actualOffset, wantStart)
		if err != nil {
			return LibraryPage{}, err
		}
	}
	pageItems := mergeLibraryWindowPage(dbWindow, dbStart, orphans, actualOffset, pageSize, order)
	return buildLibraryPageResponse(snap, pageItems, actualPage, pageSize, total, totalPages, false), nil
}

func librarySQLMergeWindow(offset, pageSize, dbTotal, orphanCount int) (dbStart, dbLimit int) {
	if offset <= orphanCount {
		return 0, min(dbTotal, offset+pageSize)
	}
	dbStart = min(offset-orphanCount, dbTotal-1)
	return dbStart, min(dbTotal-dbStart, orphanCount+pageSize)
}

func mergeLibraryWindowPage(dbItems []DownloadRecord, dbStart int, orphans []DownloadRecord, offset, pageSize int, order string) []DownloadRecord {
	if len(orphans) == 0 {
		if len(dbItems) > pageSize {
			return dbItems[:pageSize]
		}
		return dbItems
	}
	oBefore := 0
	if offset > len(orphans) && len(dbItems) > 0 {
		oBefore = sort.Search(len(orphans), func(j int) bool {
			return !libraryEntryLess(orphans[j], dbItems[0], order)
		})
	}
	skipped := dbStart + oBefore
	skipInWindow := offset - skipped
	if skipInWindow < 0 {
		skipInWindow = 0
	}
	need := skipInWindow + pageSize
	merged := mergeLibraryEntries(dbItems, orphans[oBefore:], need, order)
	if skipInWindow < len(merged) {
		merged = merged[skipInWindow:]
	} else {
		merged = nil
	}
	if len(merged) > pageSize {
		merged = merged[:pageSize]
	}
	return merged
}

func matchesLibraryUserAndSearch(item DownloadRecord, filter LibraryFilter, lowerUser string) bool {
	if (!filter.From.IsZero() && item.CreatedAt.Before(filter.From)) || (!filter.To.IsZero() && !item.CreatedAt.Before(filter.To)) {
		return false
	}
	if filter.Search != "" &&
		!strings.Contains(strings.ToLower(item.FilePath), filter.Search) &&
		!strings.Contains(strings.ToLower(item.MediaURL), filter.Search) &&
		!strings.Contains(strings.ToLower(item.TweetID), filter.Search) {
		return false
	}
	switch filter.User {
	case libraryKindAll:
		return true
	case libraryUserUnknown:
		return item.UserScreenName == ""
	default:
		return strings.EqualFold(item.UserScreenName, lowerUser)
	}
}

func buildLibraryEntriesWhereSQL(filter LibraryFilter) (string, []any) {
	conds := []string{}
	args := []any{}
	if exts, ok := libraryKindExts(filter.Kind); ok && len(exts) > 0 {
		extConds := make([]string, len(exts))
		for i, ext := range exts {
			extConds[i] = `lower(file_path) LIKE ?`
			args = append(args, "%"+ext)
		}
		conds = append(conds, "("+strings.Join(extConds, " OR ")+")")
	}
	if filter.Search != "" && isASCIIString(filter.Search) {
		conds = append(conds, `(instr(lower(file_path), ?) > 0 OR instr(lower(media_url), ?) > 0 OR instr(lower(tweet_id), ?) > 0)`)
		args = append(args, filter.Search, filter.Search, filter.Search)
	}
	switch filter.User {
	case libraryKindAll:
	case libraryUserUnknown:
		conds = append(conds, `user_screen_name = ''`)
	default:
		lowerUser := strings.ToLower(strings.TrimPrefix(filter.User, "@"))
		conds = append(conds, `(lower(user_screen_name) = ? OR user_screen_name = '')`)
		args = append(args, lowerUser)
	}
	normCreated := `COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', created_at), created_at)`
	if !filter.From.IsZero() {
		conds = append(conds, normCreated+` >= ?`)
		args = append(args, filter.From.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	if !filter.To.IsZero() {
		toBound := filter.To.UTC()
		if toBound.Nanosecond()%1_000_000 != 0 {
			// Round up by 1ms for the SQL pre-filter; streamPage applies exact Go comparison.
			toBound = toBound.Truncate(time.Millisecond).Add(time.Millisecond)
		}
		conds = append(conds, normCreated+` < ?`)
		args = append(args, toBound.Format("2006-01-02T15:04:05.000Z"))
	}
	if filter.MinBytes != nil {
		conds = append(conds, `bytes >= ?`)
		args = append(args, *filter.MinBytes)
	}
	if filter.MaxBytes != nil {
		conds = append(conds, `bytes <= ?`)
		args = append(args, *filter.MaxBytes)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " AND " + strings.Join(conds, " AND "), args
}

func libraryEntriesOrderBySQL(order string) string {
	const normCreated = `COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', created_at), created_at)`
	switch order {
	case "date_asc":
		return normCreated + ` ASC, created_at ASC, id DESC, file_path ASC`
	case "size_asc":
		return `bytes ASC, ` + normCreated + ` DESC, created_at DESC, id DESC, file_path ASC`
	case "size_desc":
		return `bytes DESC, ` + normCreated + ` DESC, created_at DESC, id DESC, file_path ASC`
	default:
		return normCreated + ` DESC, created_at DESC, id DESC, file_path ASC`
	}
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
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

const libraryEntriesSelectColsSQL = `
SELECT id, job_id, tweet_id, media_url, preview_url, file_path, bytes, created_at, media_key, content_hash,
	user_screen_name, user_name
FROM ranked
WHERE rn = 1`

func libraryEntriesCTESQL() string {
	return `
WITH filtered AS (
	SELECT d.id, d.job_id, d.tweet_id, d.media_url, d.preview_url, d.file_path, d.bytes,
		d.created_at, d.media_key, d.content_hash,
		COALESCE(t.screen_name, '') AS user_screen_name, COALESCE(t.author_name, '') AS user_name,
		CASE WHEN d.file_path = '' THEN 'record:' || d.id ELSE d.file_path END AS entry_key
	FROM downloads d
	LEFT JOIN tweets t ON t.id = d.tweet_id
), ranked AS (
	SELECT filtered.*, ROW_NUMBER() OVER (
		PARTITION BY entry_key
		ORDER BY COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', created_at), created_at) DESC, created_at DESC, id DESC
	) AS rn
	FROM filtered
)`
}

// libraryEntriesSQL 返回"每个 file_path 只保留最新一条下载记录"的去重查询。
func libraryEntriesSQL() string {
	return libraryEntriesCTESQL() + libraryEntriesSelectColsSQL
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

// filterLibraryEntries 在快照的去重条目上应用与旧 SQL 等价的筛选（分类、关键词、
// 作者、时间与体积）。归属已在快照构建时按目录前缀补全，因此 user=unknown 与具体
// 用户名都可以直接比较，不再需要在 Go 侧分块重跑全表查询。
func filterLibraryEntries(entries []DownloadRecord, filter LibraryFilter, snap *librarySnapshot) []DownloadRecord {
	if _, ok := libraryKindExts(filter.Kind); !ok {
		return nil
	}
	items := make([]DownloadRecord, 0, len(entries))
	user := ""
	if filter.User != libraryKindAll && filter.User != libraryUserUnknown {
		user = strings.ToLower(strings.TrimPrefix(filter.User, "@"))
	}
	for _, item := range entries {
		if !libraryKindMatches(item.FilePath, filter.Kind) {
			continue
		}
		if filter.Search != "" &&
			!strings.Contains(strings.ToLower(item.FilePath), filter.Search) &&
			!strings.Contains(strings.ToLower(item.MediaURL), filter.Search) &&
			!strings.Contains(strings.ToLower(item.TweetID), filter.Search) {
			continue
		}
		switch filter.User {
		case libraryKindAll:
		case libraryUserUnknown:
			if item.UserScreenName != "" {
				continue
			}
		default:
			if !strings.EqualFold(item.UserScreenName, user) {
				continue
			}
		}
		if (!filter.From.IsZero() && item.CreatedAt.Before(filter.From)) || (!filter.To.IsZero() && !item.CreatedAt.Before(filter.To)) {
			continue
		}
		if (filter.MinBytes != nil && item.Bytes < *filter.MinBytes) || (filter.MaxBytes != nil && item.Bytes > *filter.MaxBytes) {
			continue
		}
		items = append(items, item)
	}
	return items
}

func filterLibraryOrphans(orphans []DownloadRecord, filter LibraryFilter) []DownloadRecord {
	items := make([]DownloadRecord, 0, len(orphans))
	user := ""
	if filter.User != libraryKindAll && filter.User != libraryUserUnknown {
		user = strings.TrimPrefix(filter.User, "@")
	}
	for _, item := range orphans {
		if (!filter.From.IsZero() && item.CreatedAt.Before(filter.From)) || (!filter.To.IsZero() && !item.CreatedAt.Before(filter.To)) {
			continue
		}
		if (filter.MinBytes != nil && item.Bytes < *filter.MinBytes) || (filter.MaxBytes != nil && item.Bytes > *filter.MaxBytes) {
			continue
		}
		if filter.User == libraryUserUnknown && item.UserScreenName != "" {
			continue
		}
		if user != "" && !strings.EqualFold(item.UserScreenName, user) {
			continue
		}
		if !libraryKindMatches(item.FilePath, filter.Kind) {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(strings.ReplaceAll(item.FilePath, `\`, "/")), filter.Search) {
			continue
		}
		items = append(items, item)
	}
	return items
}

// mergeLibraryEntries 合并两个使用相同排序规则的列表；时间相同时 DB 记录优先
// （旧实现是 stable sort，先加入的 DB 记录在前）。合并时按条目键去重：快照里的孤儿列表
// 与当前 DB 结果之间存在一瞬的时间差（文件刚记录/刚写入），同一文件可能两边各出现一次；
// 去重时优先保留 DB 记录（ID != 0，信息更全）。
func mergeLibraryEntries(dbItems, orphans []DownloadRecord, need int, order string) []DownloadRecord {
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
			takeDB = !libraryEntryLess(orphans[j], dbItems[i], order)
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

func libraryEntryLess(a, b DownloadRecord, order string) bool {
	if (order == "size_asc" || order == "size_desc") && a.Bytes != b.Bytes {
		if order == "size_asc" {
			return a.Bytes < b.Bytes
		}
		return a.Bytes > b.Bytes
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		if order == "date_asc" {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.CreatedAt.After(b.CreatedAt)
	}
	if a.ID != b.ID {
		return a.ID > b.ID
	}
	return a.FilePath < b.FilePath
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
	s.invalidateStaleLibraryDownloadsCache()
	if snap := s.peekLibrarySnapshot(s.libraryCacheGen.Load()); snap != nil {
		return snap, nil
	}
	s.libraryBuildMu.Lock()
	defer s.libraryBuildMu.Unlock()
	s.invalidateStaleLibraryDownloadsCache()
	gen := s.libraryCacheGen.Load()
	if snap := s.peekLibrarySnapshot(gen); snap != nil {
		return snap, nil
	}
	snap, err := s.buildLibrarySnapshot(ctx, gen)
	if err != nil {
		return nil, err
	}
	if s.libraryCacheGen.Load() == gen && !s.libraryStale.Load() {
		s.librarySnapMu.Lock()
		s.librarySnap = snap
		s.librarySnapMu.Unlock()
	}
	return snap, nil
}

func (s *Store) peekLibrarySnapshot(gen uint64) *librarySnapshot {
	if s.libraryStale.Load() {
		return nil
	}
	s.librarySnapMu.RLock()
	defer s.librarySnapMu.RUnlock()
	snap := s.librarySnap
	if snap == nil || snap.gen != gen || time.Since(snap.at) >= libraryDownloadsCacheTTL {
		return nil
	}
	return snap
}

func (s *Store) buildLibrarySnapshot(ctx context.Context, gen uint64) (*librarySnapshot, error) {
	snap := &librarySnapshot{
		gen:            gen,
		at:             time.Now(),
		prefixIndex:    map[string]int{},
		userAggregates: map[string]*libraryUserAggregate{},
	}
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
		value := screen
		if value == libraryKindAll || value == libraryUserUnknown {
			value = "@" + value
		}
		key := strings.ToLower(value)
		if _, exists := userByScreen[key]; exists {
			return
		}
		label := name
		if label == "" {
			label = screen
		}
		userByScreen[key] = LibraryUserOption{Value: value, Label: label + " (@" + screen + ")"}
	}
	addUserAggregate := func(item DownloadRecord) {
		if item.UserScreenName == "" {
			return
		}
		key := strings.ToLower(item.UserScreenName)
		agg := snap.userAggregates[key]
		if agg == nil {
			agg = &libraryUserAggregate{screenName: item.UserScreenName}
			snap.userAggregates[key] = agg
		}
		agg.mediaCount++
		agg.totalBytes += item.Bytes
		if item.CreatedAt.After(agg.latestAt) {
			agg.latestAt = item.CreatedAt
		}
		if agg.displayName == "" {
			agg.displayName = item.UserName
		}
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

	// 流式扫描每个文件的最新下载记录，作者和去重规则与分页结果保持一致。
	known := make(map[string]struct{})
	pathVariants := make(map[string][]string)
	rows, err := s.db.QueryxContext(ctx, libraryEntriesSQL()+" ORDER BY "+libraryEntriesOrderBySQL("date_desc"))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item DownloadRecord
		if err := rows.StructScan(&item); err != nil {
			rows.Close()
			return nil, err
		}
		filePath := item.FilePath
		if filePath != "" {
			if abs, absErr := filepath.Abs(filePath); absErr == nil {
				cleaned := filepath.Clean(abs)
				if _, exists := known[cleaned]; !exists {
					key := libraryMatchPath(cleaned)
					pathVariants[key] = append(pathVariants[key], cleaned)
					known[cleaned] = struct{}{}
				}
			}
		}
		snap.counts.all++
		switch libraryCategory(filePath) {
		case libraryKindImages:
			snap.counts.images++
		case libraryKindVideos:
			snap.counts.videos++
		case libraryKindGifs:
			snap.counts.gifs++
		}
		if item.UserScreenName != "" {
			addUser(item.UserScreenName, item.UserName)
		} else if prefix, ok := snap.matchUser(filePath); ok {
			item.UserScreenName = prefix.screen
			item.UserName = prefix.name
			addUser(prefix.screen, prefix.name)
		} else {
			snap.unknownEntries++
		}
		addUserAggregate(item)
		// 仅在总数不超过阈值时缓存全部条目；超过阈值后分页查询自动回退 SQL，
		// 避免把超大媒体库的全部记录常驻内存，同时保证筛选/排序/统计 100% 一致。
		if len(snap.entries) < librarySnapshotMaxEntries {
			snap.entries = append(snap.entries, item)
		} else {
			snap.entriesTruncated = true
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
			if _, recorded := known[cleaned]; recorded {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil
			}
			for _, recorded := range pathVariants[libraryMatchPath(cleaned)] {
				if recordedInfo, err := os.Stat(recorded); err == nil && os.SameFile(info, recordedInfo) {
					return nil
				}
			}
			item := DownloadRecord{
				FilePath:  cleaned,
				Bytes:     info.Size(),
				CreatedAt: info.ModTime().UTC(),
			}
			if prefix, ok := snap.matchUser(cleaned); ok {
				item.UserScreenName = prefix.screen
				item.UserName = prefix.name
			} else {
				snap.unknownEntries++
			}
			addUserAggregate(item)
			snap.orphans = append(snap.orphans, item)
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
	if snap.unknownEntries > 0 {
		userByScreen[libraryUserUnknown] = LibraryUserOption{Value: libraryUserUnknown, Label: "未识别用户"}
	}
	snap.users = make([]LibraryUserOption, 0, len(userByScreen))
	for _, user := range userByScreen {
		snap.users = append(snap.users, user)
	}
	sort.Slice(snap.users, func(i, j int) bool { return snap.users[i].Label < snap.users[j].Label })
	return snap, nil
}
