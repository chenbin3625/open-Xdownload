package jobs

import (
	"errors"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestFailureMessage(t *testing.T) {
	tests := []struct {
		name     string
		jobKind  storage.JobKind
		archMode storage.ArchiveMode
		err      error
		want     string
	}{
		{
			name:     "tweet link media",
			jobKind:  storage.JobKindTweetLink,
			archMode: storage.ArchiveMedia,
			err:      errors.New("download failed"),
			want:     "推文媒体下载失败",
		},
		{
			name:     "tweet link tweets",
			jobKind:  storage.JobKindTweetLink,
			archMode: storage.ArchiveTweets,
			err:      errors.New("archive failed"),
			want:     "推文归档失败",
		},
		{
			name:    "media url",
			jobKind: storage.JobKindMediaURL,
			err:     errors.New("download failed"),
			want:    "媒体下载失败",
		},
		{
			name:    "user archive",
			jobKind: storage.JobKindUser,
			err:     errors.New("user not found"),
			want:    "用户归档失败",
		},
		{
			name:    "list archive",
			jobKind: storage.JobKindList,
			err:     errors.New("list error"),
			want:    "列表归档失败",
		},
		{
			name:    "following archive",
			jobKind: storage.JobKindFollowing,
			err:     errors.New("following error"),
			want:    "关注归档失败",
		},
		{
			name:    "failed retry",
			jobKind: storage.JobKindFailedRetry,
			err:     errors.New("retry error"),
			want:    "失败重试任务失败",
		},
		{
			name:    "nil error",
			jobKind: storage.JobKindMediaURL,
			err:     nil,
			want:    "任务失败",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := storage.Job{
				Kind:        tt.jobKind,
				ArchiveMode: tt.archMode,
			}
			got := failureMessage(job, tt.err)
			if got != tt.want {
				t.Errorf("failureMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnhanceErrorContext(t *testing.T) {
	tests := []struct {
		name            string
		job             storage.Job
		mediaURL        string
		err             error
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:     "timeout error",
			job:      storage.Job{Kind: storage.JobKindMediaURL},
			mediaURL: "https://pbs.twimg.com/media/test.jpg",
			err:      errors.New("request timeout"),
			wantContains: []string{
				"request timeout",
				"媒体URL:",
				"提示: 网络超时",
			},
		},
		{
			name:     "403 forbidden",
			job:      storage.Job{Kind: storage.JobKindMediaURL},
			mediaURL: "",
			err:      errors.New("HTTP 403 Forbidden"),
			wantContains: []string{
				"403",
				"提示: 访问被拒绝",
				"刷新 X 账号凭据",
			},
		},
		{
			name:     "404 not found",
			job:      storage.Job{Kind: storage.JobKindTweetLink},
			mediaURL: "",
			err:      errors.New("HTTP 404 Not Found"),
			wantContains: []string{
				"404",
				"提示: 资源不存在",
			},
		},
		{
			name:     "401 unauthorized",
			job:      storage.Job{Kind: storage.JobKindUser},
			mediaURL: "",
			err:      errors.New("401 Unauthorized"),
			wantContains: []string{
				"401",
				"提示: 认证失败",
				"AuthToken",
				"CSRFToken",
			},
		},
		{
			name:     "proxy error",
			job:      storage.Job{Kind: storage.JobKindMediaURL},
			mediaURL: "",
			err:      errors.New("proxy connection failed"),
			wantContains: []string{
				"proxy",
				"提示: 代理连接问题",
			},
		},
		{
			name:     "disk space",
			job:      storage.Job{Kind: storage.JobKindMediaURL},
			mediaURL: "",
			err:      errors.New("no space left on device"),
			wantContains: []string{
				"no space",
				"提示: 磁盘空间不足",
			},
		},
		{
			name:     "permission denied",
			job:      storage.Job{Kind: storage.JobKindMediaURL},
			mediaURL: "",
			err:      errors.New("permission denied"),
			wantContains: []string{
				"permission denied",
				"提示: 权限不足",
			},
		},
		{
			name:     "user unavailable",
			job:      storage.Job{Kind: storage.JobKindUser, Input: "someone"},
			mediaURL: "",
			err:      errors.New("用户不可用"),
			wantContains: []string{
				"用户不可用",
				"目标: someone",
				"提示: 账号可能已冻结",
			},
		},
		{
			name:     "error already carries hint",
			job:      storage.Job{Kind: storage.JobKindUser, Input: "someone"},
			mediaURL: "",
			err:      errors.New("用户不可用\n提示: 请重新导入 AuthToken"),
			wantContains: []string{
				"用户不可用",
				"请重新导入 AuthToken",
			},
			wantNotContains: []string{
				"提示: 账号可能已冻结", // 不重复追加提示
			},
		},
		{
			name:     "long media url truncated",
			job:      storage.Job{Kind: storage.JobKindMediaURL},
			mediaURL: "https://video.twimg.com/amplify_video/1234567890123456789/pl/abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ.m3u8?tag=16&container=fmp4",
			err:      errors.New("download failed"),
			wantContains: []string{
				"媒体URL:",
				"...",
			},
			wantNotContains: []string{
				"container=fmp4", // Should be truncated
			},
		},
		{
			name:         "nil error",
			job:          storage.Job{Kind: storage.JobKindMediaURL},
			mediaURL:     "",
			err:          nil,
			wantContains: []string{},
		},
		{
			name:     "no enhancement needed",
			job:      storage.Job{Kind: storage.JobKindMediaURL},
			mediaURL: "",
			err:      errors.New("simple error"),
			wantContains: []string{
				"simple error",
			},
			wantNotContains: []string{
				"提示:",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enhanceErrorContext(tt.job, tt.mediaURL, tt.err)

			if tt.err == nil {
				if got != "" {
					t.Errorf("enhanceErrorContext() with nil error = %q, want empty string", got)
				}
				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("enhanceErrorContext() = %q, want to contain %q", got, want)
				}
			}

			for _, notWant := range tt.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("enhanceErrorContext() = %q, should not contain %q", got, notWant)
				}
			}
		})
	}
}

func TestEnhanceErrorContextURLLength(t *testing.T) {
	// Test that very long URLs are truncated to 100 characters
	longURL := "https://video.twimg.com/" + strings.Repeat("a", 150)
	job := storage.Job{Kind: storage.JobKindMediaURL}
	err := errors.New("download failed")

	got := enhanceErrorContext(job, longURL, err)

	// Check that the URL is present but truncated
	if !strings.Contains(got, "媒体URL:") {
		t.Error("Expected URL context to be present")
	}

	if !strings.Contains(got, "...") {
		t.Error("Expected long URL to be truncated with ellipsis")
	}

	// The full long URL should not appear
	if strings.Contains(got, strings.Repeat("a", 150)) {
		t.Error("Long URL should be truncated, not shown in full")
	}
}
