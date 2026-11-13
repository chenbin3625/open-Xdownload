package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

func archiveTestPool(t *testing.T, cfg *config.AppConfig, handler http.HandlerFunc) *xclient.Pool {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	cfg.ProxyURL = server.URL
	cfg.AuthToken, cfg.CSRFToken = "primary", "csrf"
	cfg.AdditionalCookies = "auth_token=backup; ct0=csrf-backup"
	transport, err := httpx.Transport(cfg.ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	t.Cleanup(transport.CloseIdleConnections)
	pool, err := xclient.NewPool(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestArchiveProtectedUsersUsesPrimaryRelationships(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		following, muted, autoFollow     bool
		status, wantTimeline, wantFailed int
	}{
		{name: "primary follows despite backup mute", following: true, muted: true, wantTimeline: 1},
		{name: "only backup follows", following: false},
		{name: "primary auto-follow is rate limited", autoFollow: true},
		{name: "primary metadata failure", following: true, status: http.StatusForbidden, wantFailed: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
			cfg.AutoFollowProtected = tc.autoFollow
			var metadataCalls, timelineCalls, backupCalls, followCalls atomic.Int64
			pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
				cookie, _ := r.Cookie("auth_token")
				if cookie == nil || cookie.Value != "primary" {
					backupCalls.Add(1)
				}
				if r.Method == http.MethodPost {
					followCalls.Add(1)
					if cookie != nil && cookie.Value == "primary" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(http.StatusTooManyRequests)
					} else {
						fmt.Fprint(w, `{"id":"u1"}`)
					}
					return
				}
				if strings.HasSuffix(r.URL.Path, "/UserByRestId") {
					metadataCalls.Add(1)
					if tc.status != 0 {
						w.WriteHeader(tc.status)
						return
					}
					fmt.Fprintf(w, `{"data":{"user":{"result":{"rest_id":"u1","legacy":{"name":"Alice","screen_name":"alice","protected":true,"following":%t}}}}}`, tc.following)
					return
				}
				timelineCalls.Add(1)
				fmt.Fprint(w, `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`)
			})
			job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
			if err != nil {
				t.Fatal(err)
			}
			manager := NewManager(store, parser.NewService(), NewEventBus())
			original := xclient.User{ID: "u1", ScreenName: "alice", Protected: true, Following: !tc.following, Muting: tc.muted}
			users := []xclient.User{original}
			stats, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if metadataCalls.Load() != 1 || timelineCalls.Load() != int64(tc.wantTimeline) || backupCalls.Load() != 0 || stats.Failed != tc.wantFailed {
				t.Fatalf("metadata=%d timeline=%d backup=%d stats=%+v", metadataCalls.Load(), timelineCalls.Load(), backupCalls.Load(), stats)
			}
			if tc.wantFailed > 0 && len(stats.Issues) == 0 {
				t.Fatal("metadata failure was silent")
			}
			if tc.autoFollow && followCalls.Load() != 1 {
				t.Fatalf("automatic follow switched accounts: %d", followCalls.Load())
			}
			if users[0] != original {
				t.Fatal("caller user slice mutated")
			}
		})
	}
}

func TestArchiveUserPreservesUnresolvableMediaIssue(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[{"content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":{"rest_id":"300","legacy":{"full_text":"video","extended_entities":{"media":[{"id_str":"m1","type":"video","media_url_https":"https://pbs.twimg.com/thumb.jpg"}]}}}}}}}]}]}}}}}}`)
	})
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	stats, err := manager.archiveUser(ctx, ctx, job, cfg, pool, xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Tweets != 1 || stats.Failed != 1 || len(stats.Issues) != 1 || !strings.Contains(stats.Issues[0], "未解析到下载地址") {
		t.Fatalf("media issue not propagated: %+v", stats)
	}
}

func TestArchiveCursorAcrossSnowflakeDigitLengths(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	user := xclient.User{ID: "u1", ScreenName: "alice", Name: "Alice"}
	if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, parser.NewService(), NewEventBus())
	entity, dir, err := manager.ensureUserEntity(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	const newer = "2000000000000000000"
	const older = "999999999999999999"
	if err := store.UpdateUserEntityLastSeenTweet(ctx, entity.ID, newer); err != nil {
		t.Fatal(err)
	}
	tweets := []parser.TweetData{{ID: newer}, {ID: older}}
	if _, err := manager.archiveTweetsWithSeen(ctx, ctx, job, cfg, user, entity, dir, tweets, nil, newer); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUserEntity(ctx, entity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeenTweetID != newer {
		t.Fatalf("cursor regressed from %s to %s", newer, got.LastSeenTweetID)
	}
}

func TestFullTimelineBackfillAndCheckpointReplay(t *testing.T) {
	for _, scenario := range []struct {
		name, status, lastID, wantCursor string
		wantCount                        int
	}{
		{name: "completed starts historical scan", status: "completed", lastID: "100", wantCount: 1},
		{name: "failed incremental loses old boundary", status: "failed", lastID: "100", wantCount: 1},
		{name: "interrupted incremental loses old boundary", status: "interrupted", lastID: "100", wantCount: 1},
		{name: "interrupted historical scan replays saved page", status: "interrupted", wantCursor: "saved-page", wantCount: 5},
		{name: "failed historical scan replays saved page", status: "failed", wantCursor: "saved-page", wantCount: 5},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := storage.Open(filepath.Join(t.TempDir(), "timeline.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
			cursors := make(chan string, 1)
			pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
				var variables struct {
					Cursor string `json:"cursor"`
				}
				if err := json.Unmarshal([]byte(r.URL.Query().Get("variables")), &variables); err != nil {
					t.Error(err)
				}
				cursors <- variables.Cursor
				fmt.Fprint(w, `{"data":{"user_result_by_screen_name":{"result":{"profile_with_replies_timeline_v2":{"timeline":{"instructions":[{"__typename":"TimelineAddEntries","entries":[{"content":{"__typename":"TimelineTimelineItem","content":{"__typename":"TimelineTweet","tweet_results":{"result":{"__typename":"Tweet","rest_id":"90","legacy":{"full_text":"historical pure text","created_at":"Tue Nov 10 12:00:00 +0000 2026"},"core":{"user_results":{"result":{"rest_id":"42","core":{"screen_name":"author","name":"Author"}}}}}}}}}]}]}}}}}}`)
			})
			user := xclient.User{ID: "42", ScreenName: "author", Name: "Author"}
			if _, err := store.UpsertUser(ctx, storageUser(user)); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(store, parser.NewService(), NewEventBus())
			entity, dir, err := manager.ensureUserEntity(ctx, cfg, user)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateUserEntityLastSeenTweet(ctx, entity.ID, "999"); err != nil {
				t.Fatal(err)
			}
			job, err := store.CreateArchiveJob(ctx, storage.JobDraft{Kind: storage.JobKindUser, Input: "author", ArchiveMode: storage.ArchiveTweets, Backfill: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetTweetSync(ctx, user.ID); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveTweetSync(ctx, storage.TweetSync{UserID: user.ID, LastID: scenario.lastID, Cursor: "saved-page", HeadID: "110", Status: scenario.status, Count: 4}); err != nil {
				t.Fatal(err)
			}
			// Simulate a crash after the body commit but before the page checkpoint commit.
			if err := store.ArchiveTweet(ctx, job.ID, parser.TweetData{ID: "90", Text: "historical pure text", Author: parser.Author{ID: "42", ScreenName: "author"}}, "skipped"); err != nil {
				t.Fatal(err)
			}
			stats, err := manager.archiveFullTimeline(ctx, ctx, job, cfg, pool, user, entity, dir, nil, nil)
			if err != nil || stats.Tweets != 1 {
				t.Fatalf("historical scan: %+v %v", stats, err)
			}
			if cursor := <-cursors; cursor != scenario.wantCursor {
				t.Fatalf("cursor = %q, want %q", cursor, scenario.wantCursor)
			}
			state, err := store.GetTweetSync(ctx, user.ID)
			if err != nil || state.Status != "completed" || state.Count != scenario.wantCount || state.Cursor != "" || state.LastID == "" {
				t.Fatalf("checkpoint: %+v %v", state, err)
			}
			tweet, err := store.GetTweet(ctx, "90")
			if err != nil || tweet.Text != "historical pure text" || len(tweet.JobIDs) != 1 || tweet.JobIDs[0] != job.ID || len(tweet.Media) != 0 {
				t.Fatalf("idempotent body replay: %+v %v", tweet, err)
			}
		})
	}
}
