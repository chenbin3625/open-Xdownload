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
	JobKindTweetLink   JobKind = "tweet_link"
	JobKindMediaURL    JobKind = "media_url"
	JobKindUser        JobKind = "user"
	JobKindList        JobKind = "list"
	JobKindFollowing   JobKind = "following"
	JobKindFailedRetry JobKind = "failed_retry"
)

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
}

type JobDraft struct {
	Kind  JobKind
	Input string
	Title string
}

type ArchiveScheduleItem struct {
	Kind  JobKind `json:"kind"`
	Input string  `json:"input"`
	Title string  `json:"title"`
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
}

type DownloadRecord struct {
	ID        int64     `json:"id" db:"id"`
	JobID     int64     `json:"jobId" db:"job_id"`
	TweetID   string    `json:"tweetId" db:"tweet_id"`
	MediaURL  string    `json:"mediaUrl" db:"media_url"`
	FilePath  string    `json:"filePath" db:"file_path"`
	Bytes     int64     `json:"bytes" db:"bytes"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
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
	UpdatedAt         time.Time     `json:"updatedAt" db:"updated_at"`
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
	ID        int64     `json:"id" db:"id"`
	JobID     int64     `json:"jobId" db:"job_id"`
	EntityID  int64     `json:"entityId" db:"entity_id"`
	TweetID   string    `json:"tweetId" db:"tweet_id"`
	Payload   string    `json:"payload" db:"payload"`
	Error     string    `json:"error" db:"error"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

type FailedTweetView struct {
	ID              int64     `json:"id" db:"id"`
	JobID           int64     `json:"jobId" db:"job_id"`
	EntityID        int64     `json:"entityId" db:"entity_id"`
	TweetID         string    `json:"tweetId" db:"tweet_id"`
	Payload         string    `json:"payload" db:"payload"`
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
	Config           config.AppConfig  `json:"config"`
	Jobs             []Job             `json:"jobs"`
	Downloads        []DownloadRecord  `json:"downloads"`
	Failed           []FailedMedia     `json:"failed"`
	FailedTweets     []FailedTweetView `json:"failedTweets"`
	FailedTweetCount int               `json:"failedTweetCount"`
	ArchiveSchedules []ArchiveSchedule `json:"archiveSchedules"`
	Pagination       Pagination        `json:"pagination"`
	Stats            JobStats          `json:"stats"`
}
