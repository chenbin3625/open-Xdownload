package storage

import (
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

type JobStatus string

const (
	JobPending     JobStatus = "pending"
	JobResolving   JobStatus = "resolving"
	JobDownloading JobStatus = "downloading"
	JobCompleted   JobStatus = "completed"
	JobFailed      JobStatus = "failed"
	JobCanceled    JobStatus = "canceled"
)

type JobKind string

const (
	JobKindTweetLink JobKind = "tweet_link"
	JobKindMediaURL  JobKind = "media_url"
	JobKindUser      JobKind = "user"
	JobKindList      JobKind = "list"
	JobKindFollowing JobKind = "following"
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

type Dashboard struct {
	Config          config.AppConfig  `json:"config"`
	Jobs            []Job             `json:"jobs"`
	Downloads       []DownloadRecord  `json:"downloads"`
	Failed          []FailedMedia     `json:"failed"`
	LastParsedTweet *parser.TweetData `json:"lastParsedTweet,omitempty"`
}
