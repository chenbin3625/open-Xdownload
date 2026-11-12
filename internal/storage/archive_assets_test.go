package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestTweetArchiveDedupFiltersAndIndependentResults(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "author", ArchiveMode: ArchiveBoth, Backfill: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "author2", ArchiveMode: ArchiveTweets})
	if err != nil {
		t.Fatal(err)
	}
	text := parser.TweetData{ID: "100", Text: strings.Repeat("long text 正文 ", 300), Author: parser.Author{ID: "42", ScreenName: "author"}, CreatedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), ReplyToID: "99"}
	media := parser.TweetData{ID: "101", Text: "photo body", Author: text.Author, CreatedAt: text.CreatedAt.Add(23*time.Hour + 59*time.Minute), Media: []parser.Media{{ID: "p1", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/test.jpg"}}, QuotedTweetID: text.ID, Related: []parser.TweetData{text}}
	for _, tweet := range []parser.TweetData{text, media, media} {
		if err := store.ArchiveTweet(ctx, job.ID, tweet, "pending"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetTweetMediaResult(ctx, job.ID, media.ID, "failed", "CDN offline"); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveTweet(ctx, other.ID, media, "not_requested"); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListTweets(ctx, TweetFilter{From: "2026-11-10T00:00:00Z", To: "2026-11-11T00:00:00Z", Author: "@AUTHOR"})
	if err != nil || page.Pagination.Total != 2 {
		t.Fatalf("date/author filter: %+v %v", page, err)
	}
	if page.Items[0].ID != media.ID {
		t.Fatalf("default timeline must be newest first: %+v", page.Items)
	}
	page, err = store.ListTweets(ctx, TweetFilter{Sort: "asc", PageSize: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != text.ID || page.Pagination.Total != 2 {
		t.Fatalf("ascending timeline must sort before pagination: %+v %v", page, err)
	}
	page, err = store.ListTweets(ctx, TweetFilter{Search: "正文", MediaType: "text"})
	if err != nil || page.Pagination.Total != 1 || page.Items[0].Text != text.Text {
		t.Fatalf("text/long body: %+v %v", page, err)
	}
	page, err = store.ListTweets(ctx, TweetFilter{JobID: job.ID, MediaType: "photo"})
	if err != nil || len(page.Items) != 1 || page.Items[0].MediaStatus != "failed" || page.Items[0].MediaError != "CDN offline" {
		t.Fatalf("job-specific result: %+v %v", page, err)
	}
	got, err := store.GetTweet(ctx, media.ID)
	if err != nil || len(got.JobIDs) != 2 || len(got.Related) != 1 || got.QuotedTweetID != text.ID {
		t.Fatalf("dedup and relations: %+v %v", got, err)
	}
	media.Media = nil
	if err := store.ArchiveTweet(ctx, other.ID, media, "not_requested"); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListTweets(ctx, TweetFilter{MediaType: "photo"})
	if err != nil || page.Pagination.Total != 0 {
		t.Fatalf("stale media association: %+v %v", page, err)
	}
	job.Status = JobFailed
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	retry, err := store.RetryJob(ctx, job.ID)
	if err != nil || retry.ArchiveMode != ArchiveBoth || !retry.Backfill {
		t.Fatalf("retry archive options: %+v %v", retry, err)
	}
}

func TestTweetMediaRetryIsAtomicAcrossConcurrentRequests(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindTweetLink, Input: "https://x.com/author/status/123", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{ID: "123", URL: job.Input, Text: "body"}, "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	var created, busy atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < 8; index++ {
		wg.Go(func() {
			<-start
			if _, err := store.RetryTweetMedia(ctx, "123"); err == nil {
				created.Add(1)
			} else if errors.Is(err, ErrJobRetryBusy) {
				busy.Add(1)
			} else {
				t.Errorf("retry: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if created.Load() != 1 || busy.Load() != 7 {
		t.Fatalf("duplicate retries: created=%d busy=%d", created.Load(), busy.Load())
	}
}

func TestTweetMediaRetryRecognizesActiveURLAliases(t *testing.T) {
	for _, fixture := range []struct {
		name, input string
		status      JobStatus
		busy        bool
	}{
		{"canonical", "https://x.com/author/status/123", JobPending, true},
		{"twitter host", "https://twitter.com/author/status/123", JobPending, true},
		{"query", "https://x.com/author/status/123?s=20", JobDownloading, true},
		{"mobile host", "https://mobile.twitter.com/author/status/123", JobPending, true},
		{"different tweet", "https://x.com/author/status/1234", JobPending, false},
		{"completed alias", "https://twitter.com/author/status/123", JobCompleted, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "retry.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			original, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindTweetLink, Input: "https://x.com/author/status/123", ArchiveMode: ArchiveBoth})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ArchiveTweet(ctx, original.ID, parser.TweetData{ID: "123", URL: original.Input, Text: "body"}, "failed"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CancelJob(ctx, original.ID); err != nil {
				t.Fatal(err)
			}
			active, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindTweetLink, Input: fixture.input, ArchiveMode: ArchiveMedia})
			if err != nil {
				t.Fatal(err)
			}
			active.Status = fixture.status
			if err := store.UpdateJob(ctx, active); err != nil {
				t.Fatal(err)
			}
			_, err = store.RetryTweetMedia(ctx, "123")
			if fixture.busy && !errors.Is(err, ErrJobRetryBusy) {
				t.Fatalf("retry should reject active alias: %v", err)
			}
			if !fixture.busy && err != nil {
				t.Fatalf("unrelated or completed task blocked retry: %v", err)
			}
		})
	}
}

func TestNewJobFreezesEnabledMediaTypeThenUserStorage(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "author", "author")
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.FreezeJobStorage(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !document.Enabled || document.Version == 0 || document.Default.DirectoryTemplate != "{kind}/{username}" {
		t.Fatalf("new job did not freeze the default category/user policy: %+v", document)
	}
	resumed, err := store.FreezeJobStorage(ctx, job.ID)
	if err != nil || resumed.Version != document.Version || resumed.Default != document.Default {
		t.Fatalf("job storage snapshot changed: %+v, %v", resumed, err)
	}
}

func TestStorageVersionsSecretsAndInterruptedJobSnapshot(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	document := config.DefaultStorage(t.TempDir())
	document.Enabled = true
	document.Targets = append(document.Targets, config.StorageTarget{ID: "s3", Type: "s3", Endpoint: "http://s3.example", Bucket: "archive", AccessKey: "access", SecretKey: "secret"})
	document, err = store.SaveStorageDocument(ctx, document)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, JobKindMediaURL, "one", "one")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := store.FreezeJobStorage(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := document.Redacted()
	draft.Default.Prefix = "new-prefix"
	draft.Targets[1].AccessKey = ""
	next, err := store.SaveStorageDocument(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if next.Targets[1].SecretKey != "secret" || next.Targets[1].AccessKey != "access" {
		t.Fatal("blank/redacted credentials did not preserve secrets")
	}
	if next.Targets[0].Version != document.Targets[0].Version {
		t.Fatal("unchanged target version should remain stable")
	}
	if _, err := store.SaveStorageDocument(ctx, draft); err == nil {
		t.Fatal("stale version was accepted")
	}
	job.Status = JobDownloading
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequeueInterruptedJobs(ctx); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.FreezeJobStorage(ctx, job.ID)
	if err != nil || resumed.Version != frozen.Version || resumed.Default.Prefix != "" {
		t.Fatalf("snapshot changed: %+v %v", resumed, err)
	}
	draft = next.Redacted()
	draft.Targets[1].SecretKey = "__CLEAR__"
	next, err = store.SaveStorageDocument(ctx, draft)
	if err != nil || next.Targets[1].SecretKey != "" {
		t.Fatalf("explicit clear failed: %+v %v", next.Redacted(), err)
	}
	old, err := store.GetStorageVersion(ctx, frozen.Version)
	if err != nil || old.Targets[1].SecretKey != "secret" {
		t.Fatal("historical target credentials were changed")
	}
}
