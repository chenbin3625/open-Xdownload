package storage

import (
	"database/sql"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

type JobStatus string

const (
	JobPending             JobStatus = "pending"
	JobResolving           JobStatus = "resolving"
	JobDownloading         JobStatus = "downloading"
	JobCompleted           JobStatus = "completed"
	JobCompletedWithErrors JobStatus = "completed_with_errors"
	JobFailed              JobStatus = "failed"
	JobCanceled            JobStatus = "canceled"
)

type JobKind string

const (
	JobKindTweetLink      JobKind = "tweet_link"
	JobKindMediaURL       JobKind = "media_url"
	JobKindUser           JobKind = "user"
	JobKindList           JobKind = "list"
	JobKindFollowing      JobKind = "following"
	JobKindFailedRetry    JobKind = "failed_retry"
	JobKindAssetMigration JobKind = "asset_migration"
)

// JobSource 标记任务的来源，用于排队公平性：手动任务更快响应，后台任务仍通过
// enqueued_at 的老化获得固定执行份额（见 ClaimPendingJobs）。
type JobSource string

const (
	JobSourceManual      JobSource = "manual"
	JobSourceSchedule    JobSource = "schedule"
	JobSourceAutoRetry   JobSource = "auto_retry"
	JobSourceMaintenance JobSource = "maintenance"
)

// Normalized 把未知/空来源收敛为 manual，避免历史数据或调用方拼写差异导致
// 排序 CASE 落空（按 0 处理会让后台任务获得手动优先级）。
func (s JobSource) Normalized() JobSource {
	switch s {
	case JobSourceSchedule, JobSourceAutoRetry, JobSourceMaintenance:
		return s
	default:
		return JobSourceManual
	}
}

type Job struct {
	ID        int64     `json:"id" db:"id"`
	Kind      JobKind   `json:"kind" db:"kind"`
	Status    JobStatus `json:"status" db:"status"`
	Input     string    `json:"input" db:"input"`
	Title     string    `json:"title" db:"title"`
	Progress  float64   `json:"progress" db:"progress"`
	Message   string    `json:"message" db:"message"`
	Error     string    `json:"error,omitempty" db:"error"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
	// EnqueuedAt 是最近一次进入等待队列的时间。它与 CreatedAt 分离：自动重试会复用
	// 旧任务行，若仍按 created_at 排序就会凭首次创建时间插到新建手动任务前面。
	EnqueuedAt time.Time `json:"enqueuedAt" db:"enqueued_at"`
	// EnqueueNS 是 EnqueuedAt 的 UnixNano 整数排序键。SQLite 的日期函数不解析 Go
	// 驱动写入的 RFC3339Nano（小数秒 9 位），用整数键保证排序与比较无格式依赖。
	EnqueueNS int64 `json:"-" db:"enqueue_ns"`
	// AvailableAt 非空且未到期时任务不会被领取：限流冷却或分段让出时，调度器把
	// 任务重新置为 pending 并在此时间之后才允许再次执行，从而释放唯一执行槽。
	// AvailableNS 是 0（可执行）或该时间的 UnixNano 整数比较键。
	AvailableAt *time.Time `json:"availableAt,omitempty" db:"available_at"`
	AvailableNS int64      `json:"-" db:"available_ns"`
	Source      JobSource  `json:"source" db:"source"`
	// DeferredSeconds 是任务因限流被重新排队所累计的推迟时长。超过上限后任务不再
	// 无限等待，而是明确失败并写明原因。
	DeferredSeconds int64       `json:"-" db:"deferred_seconds"`
	ArchiveMode     ArchiveMode `json:"archiveMode" db:"archive_mode"`
	Backfill        bool        `json:"backfill" db:"backfill"`
	// BatchSize 是全文归档每轮最多处理的用户数；0 表示不限制。
	BatchSize int `json:"batchSize,omitempty" db:"batch_size"`
}

type JobDraft struct {
	Kind        JobKind
	Input       string
	Title       string
	ArchiveMode ArchiveMode
	Backfill    bool
	// BatchSize 是全文归档每轮最多处理的用户数；0 表示不限制。
	BatchSize int
	// Source 为空时按 manual 处理；定时计划由调度器显式设为 schedule。
	Source JobSource
}

type ArchiveScheduleItem struct {
	Kind        JobKind     `json:"kind"`
	Input       string      `json:"input"`
	Title       string      `json:"title"`
	ArchiveMode ArchiveMode `json:"archiveMode,omitempty"`
	Backfill    bool        `json:"backfill,omitempty"`
	BatchSize   int         `json:"batchSize,omitempty"`
}

type ArchiveSchedule struct {
	ID              int64                 `json:"id" db:"id"`
	Name            string                `json:"name" db:"name"`
	Enabled         bool                  `json:"enabled" db:"enabled"`
	IntervalMinutes int                   `json:"intervalMinutes" db:"interval_minutes"`
	Items           []ArchiveScheduleItem `json:"items" db:"-"`
	ItemsJSON       string                `json:"-" db:"items_json"`
	LastRunAt       *time.Time            `json:"lastRunAt,omitempty" db:"last_run_at"`
	NextRunAt       time.Time             `json:"nextRunAt" db:"next_run_at"`
	LastJobIDs      []int64               `json:"lastJobIds" db:"-"`
	LastJobIDsJSON  string                `json:"-" db:"last_job_ids"`
	CreatedAt       time.Time             `json:"createdAt" db:"created_at"`
	UpdatedAt       time.Time             `json:"updatedAt" db:"updated_at"`
	// ClaimVersion 是计划领取的乐观锁版本号，不对外暴露。领取（claimArchiveScheduleJobs）、
	// 重新排期与任何改变 next_run_at 的编辑都会使其递增，从而让"读到快照 → 领取"之间的
	// 并发/过期快照自然失效。用整数而不是比较 next_run_at 时间：时间以 TEXT 存储，格式、
	// 精度或时区表示的任何差异都会导致比较失败且难以排查。
	ClaimVersion int64 `json:"-" db:"claim_version"`
}

type DownloadRecord struct {
	ID             int64                       `json:"id" db:"id"`
	JobID          int64                       `json:"jobId" db:"job_id"`
	TweetID        string                      `json:"tweetId" db:"tweet_id"`
	MediaURL       string                      `json:"mediaUrl" db:"media_url"`
	PreviewURL     string                      `json:"previewUrl,omitempty" db:"preview_url"`
	FilePath       string                      `json:"filePath" db:"file_path"`
	Bytes          int64                       `json:"bytes" db:"bytes"`
	CreatedAt      time.Time                   `json:"createdAt" db:"created_at"`
	UserScreenName string                      `json:"userScreenName,omitempty" db:"user_screen_name"`
	UserName       string                      `json:"userName,omitempty" db:"user_name"`
	FileURL        string                      `json:"fileUrl,omitempty" db:"-"`
	AssetURLs      map[config.AssetKind]string `json:"assetUrls,omitempty" db:"-"`
	// MediaKey 是 media_url 的媒体身份键（downloader.MediaIdentity），同一份媒体内容的
	// 不同 URL 写法共用同一个键，用于跨推文判断"已有可用文件"并跳过；不对外暴露。
	MediaKey    string `json:"-" db:"media_key"`
	ContentHash string `json:"-" db:"content_hash"`
}

type FailedMedia struct {
	ID        int64     `json:"id" db:"id"`
	JobID     int64     `json:"jobId" db:"job_id"`
	MediaURL  string    `json:"mediaUrl" db:"media_url"`
	Error     string    `json:"error" db:"error"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

type UnavailableMedia struct {
	MediaURL  string    `db:"media_url"`
	TweetID   string    `db:"tweet_id"`
	Error     string    `db:"error"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

type JobStats struct {
	Total     int `json:"total" db:"total"`
	Active    int `json:"active" db:"active"`
	Completed int `json:"completed" db:"completed"`
	Failed    int `json:"failed" db:"failed"`
}

// DashboardMetaView is the compact stats payload piggybacked on SSE and
// returned by GET /api/dashboard/meta. Keep JSON keys stable with the SPA.
type DashboardMetaView struct {
	Stats            JobStats `json:"stats"`
	FailedTweetCount int      `json:"failedTweetCount"`
}

type Pagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

type User struct {
	ID           string    `json:"id" db:"id"`
	ScreenName   string    `json:"screenName" db:"screen_name"`
	Name         string    `json:"name" db:"name"`
	Protected    bool      `json:"protected" db:"protected"`
	FriendsCount int       `json:"friendsCount" db:"friends_count"`
	MediaCount   int       `json:"mediaCount" db:"media_count"`
	UpdatedAt    time.Time `json:"updatedAt" db:"updated_at"`
}

type UserEntity struct {
	ID                int64         `json:"id" db:"id"`
	UserID            string        `json:"userId" db:"user_id"`
	Name              string        `json:"name" db:"name"`
	ParentDir         string        `json:"parentDir" db:"parent_dir"`
	LatestReleaseTime sql.NullTime  `json:"-" db:"latest_release_time"`
	MediaCount        sql.NullInt64 `json:"-" db:"media_count"`
	LastSeenTweetID   string        `json:"-" db:"last_seen_tweet_id"`
	// LastFullScanAt 是最近一次成功完成全量媒体扫描的时间；定时任务据此每隔
	// 7 天为每个用户补一次全量（媒体数量不变时也能发现删除后重发、补预览图等）。
	LastFullScanAt sql.NullTime `json:"-" db:"last_full_scan_at"`
	UpdatedAt      time.Time    `json:"updatedAt" db:"updated_at"`
}

type List struct {
	ID          string    `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	OwnerUserID string    `json:"ownerUserId" db:"owner_user_id"`
	UpdatedAt   time.Time `json:"updatedAt" db:"updated_at"`
}

type ListEntity struct {
	ID        int64     `json:"id" db:"id"`
	ListID    string    `json:"listId" db:"list_id"`
	Name      string    `json:"name" db:"name"`
	ParentDir string    `json:"parentDir" db:"parent_dir"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

type UserLink struct {
	ID           int64     `json:"id" db:"id"`
	UserID       string    `json:"userId" db:"user_id"`
	Name         string    `json:"name" db:"name"`
	ListEntityID int64     `json:"listEntityId" db:"list_entity_id"`
	UpdatedAt    time.Time `json:"updatedAt" db:"updated_at"`
}

type UserLinkTarget struct {
	UserLink
	ListID        string `json:"listId" db:"list_id"`
	ListName      string `json:"listName" db:"list_name"`
	ListParentDir string `json:"listParentDir" db:"list_parent_dir"`
}

type FailedTweet struct {
	ID          int64      `json:"id" db:"id"`
	JobID       int64      `json:"jobId" db:"job_id"`
	EntityID    int64      `json:"entityId" db:"entity_id"`
	TweetID     string     `json:"tweetId" db:"tweet_id"`
	Payload     string     `json:"payload" db:"payload"`
	Error       string     `json:"error" db:"error"`
	RetryCount  int        `json:"retryCount" db:"retry_count"`
	NextRetryAt *time.Time `json:"nextRetryAt,omitempty" db:"next_retry_at"`
	CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt   time.Time  `json:"updatedAt" db:"updated_at"`
}

type FailedTweetView struct {
	ID              int64     `json:"id" db:"id"`
	JobID           int64     `json:"jobId" db:"job_id"`
	EntityID        int64     `json:"entityId" db:"entity_id"`
	TweetID         string    `json:"tweetId" db:"tweet_id"`
	Error           string    `json:"error" db:"error"`
	CreatedAt       time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt       time.Time `json:"updatedAt" db:"updated_at"`
	JobTitle        string    `json:"jobTitle" db:"job_title"`
	EntityName      string    `json:"entityName" db:"entity_name"`
	EntityParentDir string    `json:"entityParentDir" db:"entity_parent_dir"`
	UserID          string    `json:"userId" db:"user_id"`
	UserScreenName  string    `json:"userScreenName" db:"user_screen_name"`
	UserName        string    `json:"userName" db:"user_name"`
}

type Dashboard struct {
	Config           *config.AppConfig `json:"config,omitempty"`
	Jobs             []Job             `json:"jobs"`
	Downloads        []DownloadRecord  `json:"downloads,omitempty"`
	Failed           []FailedMedia     `json:"failed,omitempty"`
	FailedTweets     []FailedTweetView `json:"failedTweets,omitempty"`
	FailedTweetCount int               `json:"failedTweetCount"`
	ArchiveSchedules []ArchiveSchedule `json:"archiveSchedules,omitempty"`
	Pagination       Pagination        `json:"pagination"`
	Stats            JobStats          `json:"stats"`
}
