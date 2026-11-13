package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestFailedTweetRetryRangeKeepsOriginalUpperBound(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "author", "author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, User{ID: "42", ScreenName: "author"}); err != nil {
		t.Fatal(err)
	}
	entity, err := store.EnsureUserEntity(ctx, "42", t.TempDir(), "author")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	first, upper, err := store.ListFailedTweetsRetryRange(ctx, 2, 0, 0)
	if err != nil || len(first) != 2 {
		t.Fatalf("first range = %+v, %d, %v", first, upper, err)
	}
	if err := store.TouchFailedTweet(ctx, first[0].ID, "still unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFailedTweet(ctx, FailedTweet{JobID: job.ID, EntityID: entity.ID, TweetID: "new"}); err != nil {
		t.Fatal(err)
	}
	rest, resumedUpper, err := store.ListFailedTweetsRetryRange(ctx, 2, first[1].ID, upper)
	if err != nil || len(rest) != 1 || rest[0].TweetID != "2" || resumedUpper != upper {
		t.Fatalf("resumed range = %+v, %d, %v", rest, resumedUpper, err)
	}
}

func TestTweetReplayKeepsPositionAcrossUpdatesAndInsertions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "author", "author")
	if err != nil {
		t.Fatal(err)
	}
	tweet := parser.TweetData{Author: parser.Author{ID: "42", ScreenName: "author"}, CreatedAt: time.Now().UTC()}
	for _, id := range []string{"1", "2", "3"} {
		tweet.ID = id
		if err := store.ArchiveTweet(ctx, job.ID, tweet, "pending"); err != nil {
			t.Fatal(err)
		}
	}
	boundary := time.Now().UTC()
	first, err := store.ListTweetReplay(ctx, "42", "", boundary, 2)
	if err != nil || len(first) != 2 || first[1].ID != "2" {
		t.Fatalf("first replay = %+v, %v", first, err)
	}
	tweet.ID, tweet.Text, tweet.CreatedAt = "3", "updated", tweet.CreatedAt.Add(24*time.Hour)
	if err := store.ArchiveTweet(ctx, job.ID, tweet, "pending"); err != nil {
		t.Fatal(err)
	}
	tweet.ID = "4"
	if err := store.ArchiveTweet(ctx, job.ID, tweet, "pending"); err != nil {
		t.Fatal(err)
	}
	rest, err := store.ListTweetReplay(ctx, "42", first[1].ID, boundary, 2)
	if err != nil || len(rest) != 1 || rest[0].ID != "3" || rest[0].Text != "updated" {
		t.Fatalf("resumed replay = %+v, %v", rest, err)
	}
}
