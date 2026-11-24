package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

func TestXAccountSortableSubSecondTimestampsAndPruneStats(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "xaccounts-opt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	acc1, err := store.CreateXAccount(ctx, XAccount{AuthToken: "tok-1", CSRFToken: "ct-1"})
	if err != nil {
		t.Fatal(err)
	}
	acc2, err := store.CreateXAccount(ctx, XAccount{AuthToken: "tok-2", CSRFToken: "ct-2"})
	if err != nil {
		t.Fatal(err)
	}

	// Whole-second timestamp vs fractional timestamp within the same second:
	// with fixed-width formatSortableTime, .000000000Z < .250000000Z < .500000000Z.
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	if err := store.LockXAccount(ctx, acc1.ID, "UserMedia", base, "rate limit"); err != nil {
		t.Fatal(err)
	}
	// At base + 250ms, acc1's lock at base (.000000000Z) MUST be expired!
	locks, err := store.ListXAccountLocks(ctx, base.Add(250*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 0 {
		t.Fatalf("whole-second lock should be expired at +250ms, got locks=%+v", locks)
	}

	// Sub-second LRU order: acc1 used at .000Z, acc2 used at .250Z -> PickXAccount picks acc1 at .500Z.
	if err := store.MarkXAccountUsed(ctx, acc1.ID, base); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkXAccountUsed(ctx, acc2.ID, base.Add(250*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	picked, ok, err := store.PickXAccount(ctx, "UserMedia", nil, base.Add(500*time.Millisecond))
	if err != nil || !ok || picked.ID != acc1.ID {
		t.Fatalf("picked = %d ok=%v err=%v, want acc1 (%d)", picked.ID, ok, err, acc1.ID)
	}

	// PruneXAccountStats deletes old days while preserving recent ones.
	if err := store.IncrementXAccountStat(ctx, acc1.ID, "UserMedia", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := store.IncrementXAccountStat(ctx, acc1.ID, "UserMedia", time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneXAccountStats(ctx, "2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	oldStats, err := store.ListXAccountStats(ctx, "2025-01-01")
	if err != nil || len(oldStats) != 0 {
		t.Fatalf("old stats still present: %+v, %v", oldStats, err)
	}
	newStats, err := store.ListXAccountStats(ctx, "2026-11-24")
	if err != nil || len(newStats) != 1 {
		t.Fatalf("recent stats missing: %+v, %v", newStats, err)
	}
}

func TestListTweetReplayPendingSkipsCompletedAndTextOnlyTweets(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "replay-pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	job, err := store.CreateArchiveJob(ctx, JobDraft{Kind: JobKindUser, Input: "alice", ArchiveMode: ArchiveBoth})
	if err != nil {
		t.Fatal(err)
	}
	makeTweet := func(id string, withMedia bool) parser.TweetData {
		tw := parser.TweetData{
			ID:        id,
			Text:      "post " + id,
			CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			Author:    parser.Author{ID: "42", ScreenName: "alice"},
		}
		if withMedia {
			tw.Media = []parser.Media{{ID: "m" + id, Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/" + id + ".jpg"}}
		}
		return tw
	}

	// 1: text-only -> must be skipped by ListTweetReplayPending
	if err := store.ArchiveTweet(ctx, job.ID, makeTweet("101", false), "not_requested"); err != nil {
		t.Fatal(err)
	}
	// 2: media already completed -> must be skipped by ListTweetReplayPending
	if err := store.ArchiveTweet(ctx, job.ID, makeTweet("102", true), "pending"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTweetMediaResult(ctx, job.ID, "102", "completed", ""); err != nil {
		t.Fatal(err)
	}
	// 3: media previously not_requested (e.g., ArchiveTweets-only) -> must be returned!
	if err := store.ArchiveTweet(ctx, job.ID, makeTweet("103", true), "not_requested"); err != nil {
		t.Fatal(err)
	}
	// 4: media pending -> must be returned!
	if err := store.ArchiveTweet(ctx, job.ID, makeTweet("104", true), "pending"); err != nil {
		t.Fatal(err)
	}

	pending, err := store.ListTweetReplayPending(ctx, "42", "", time.Now().UTC().Add(time.Minute), 100)
	if err != nil {
		t.Fatalf("ListTweetReplayPending: %v", err)
	}
	if len(pending) != 2 || pending[0].ID != "103" || pending[1].ID != "104" {
		t.Fatalf("pending = %+v, want [103, 104]", pending)
	}
}
