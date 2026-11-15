package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 早期归档的 payload 可能缺少 media 字段。反序列化后是 nil，如果直接序列化
// 给前端就会变成 "media": null，前端调用 .length 会抛异常并整页白屏。
func TestLegacyTweetPayloadSerializesArraysNotNull(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`
INSERT INTO tweets(id,author_id,screen_name,author_name,body,published_at,payload,archived_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?)`,
		"legacy", "42", "legacy_user", "Legacy", "legacy body", now.Format(time.RFC3339Nano), `{}`, now, now); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetTweet(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Media == nil {
		t.Fatal("GetTweet must return an empty media slice, not nil")
	}
	if got.ID != "legacy" || got.Text != "legacy body" || got.Author.ScreenName != "legacy_user" || got.CreatedAt.IsZero() {
		t.Fatalf("GetTweet must backfill base fields from columns: %+v", got)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"media":[]`) {
		t.Fatalf("GetTweet must serialize media as [], got %s", data)
	}

	page, err := store.ListTweets(ctx, TweetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Media == nil {
		t.Fatalf("ListTweets must return a non-nil media slice: %+v", page.Items)
	}
	data, err = json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"media":null`) {
		t.Fatalf("ListTweets must not serialize media as null: %s", data)
	}
}
