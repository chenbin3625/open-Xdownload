package jobs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// TestCollectArchiveMembersTreatsStalledCursorAsComplete 覆盖服务端在最后一页
// 返回原样 cursor 的情况：应正常结束，而不是让整个列表归档失败。
func TestCollectArchiveMembersTreatsStalledCursorAsComplete(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, storage.JobKindList, "1", "list")
	if err != nil {
		t.Fatal(err)
	}

	oldDelay := memberPageDelay
	memberPageDelay = 0
	defer func() { memberPageDelay = oldDelay }()

	pages := []xapi.UserPage{
		{Users: []xapi.User{{ID: "1"}}, Cursor: "same"},
		{Users: []xapi.User{{ID: "2"}}, Cursor: "same"},
	}
	calls := 0
	fetch := func(cursor, viewer string) (xapi.UserPage, error) {
		if calls >= len(pages) {
			t.Fatalf("unexpected extra fetch with cursor %q", cursor)
		}
		page := pages[calls]
		calls++
		return page, nil
	}

	manager := &Manager{store: store}
	users, err := manager.collectArchiveMembers(ctx, ctx, job, fetch)
	if err != nil {
		t.Fatalf("collectArchiveMembers: %v", err)
	}
	if len(users) != 2 || users[0].ID != "1" || users[1].ID != "2" {
		t.Fatalf("users = %+v, want both pages", users)
	}
}
