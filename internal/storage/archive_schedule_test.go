package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestSchedule(name string, interval int, inputs ...string) ArchiveSchedule {
	items := make([]ArchiveScheduleItem, 0, len(inputs))
	for _, input := range inputs {
		items = append(items, ArchiveScheduleItem{Kind: JobKindUser, Input: input, Title: input})
	}
	return ArchiveSchedule{Name: name, Enabled: true, IntervalMinutes: interval, Items: items}
}

// TestArchiveScheduleCRUD 覆盖定时归档计划的增删改查：创建后能按 id / 列表读回，
// 更新会按开关与间隔的变化重算 next_run_at，Reschedule 只挪时间，Delete 之后读不到。
func TestArchiveScheduleCRUD(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	created, err := store.CreateArchiveSchedule(ctx, newTestSchedule("每日归档", 60, "alice", "bob"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 || len(created.Items) != 2 || !created.Enabled {
		t.Fatalf("created = %#v", created)
	}
	if created.NextRunAt.IsZero() {
		t.Fatal("next_run_at 未初始化")
	}

	got, err := store.GetArchiveSchedule(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "每日归档" || len(got.Items) != 2 || got.Items[0].Input != "alice" {
		t.Fatalf("get = %#v", got)
	}

	list, err := store.ListArchiveSchedules(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID || len(list[0].Items) != 2 {
		t.Fatalf("list = %#v", list)
	}

	// 间隔变化必须重算 next_run_at。
	changed := created
	changed.Name = "改名"
	changed.IntervalMinutes = 120
	updated, err := store.UpdateArchiveSchedule(ctx, changed)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "改名" || updated.IntervalMinutes != 120 {
		t.Fatalf("updated = %#v", updated)
	}
	if !updated.NextRunAt.After(created.NextRunAt) {
		t.Fatalf("next_run_at = %v, 应因间隔变化而重算到 %v 之后", updated.NextRunAt, created.NextRunAt)
	}

	// 仅改名字不应挪动 next_run_at。
	renamed := updated
	renamed.Name = "再改名"
	again, err := store.UpdateArchiveSchedule(ctx, renamed)
	if err != nil {
		t.Fatalf("update rename: %v", err)
	}
	if !again.NextRunAt.Equal(updated.NextRunAt) {
		t.Fatalf("next_run_at = %v, want 不变 %v", again.NextRunAt, updated.NextRunAt)
	}

	// Reschedule 只改 next_run_at。
	target := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)
	rescheduled, err := store.RescheduleArchiveSchedule(ctx, created.ID, target)
	if err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if !rescheduled.NextRunAt.Equal(target) {
		t.Fatalf("next_run_at = %v, want %v", rescheduled.NextRunAt, target)
	}
	if rescheduled.IntervalMinutes != 120 || rescheduled.Name != "再改名" {
		t.Fatalf("reschedule 改动了其他字段: %#v", rescheduled)
	}

	if err := store.DeleteArchiveSchedule(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetArchiveSchedule(ctx, created.ID); err == nil {
		t.Fatal("删除后仍能读到计划")
	}
	remaining, err := store.ListArchiveSchedules(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("list after delete = %#v, want empty", remaining)
	}
}

// TestListDueArchiveSchedules 覆盖到期筛选：只返回 enabled 且 next_run_at <= now 的计划，
// 并按 next_run_at 升序、受 limit 约束。
func TestListDueArchiveSchedules(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(name string, enabled bool, nextRunAt time.Time) int64 {
		schedule, err := store.CreateArchiveSchedule(ctx, newTestSchedule(name, 60, "alice"))
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := store.db.Exec(`UPDATE archive_schedules SET enabled = ?, next_run_at = ? WHERE id = ?`,
			enabled, nextRunAt, schedule.ID); err != nil {
			t.Fatalf("adjust %s: %v", name, err)
		}
		return schedule.ID
	}

	overdue := mk("很久前到期", true, now.Add(-2*time.Hour))
	dueSoon := mk("刚到期", true, now.Add(-time.Minute))
	mk("未到期", true, now.Add(time.Hour))
	mk("已停用但到期", false, now.Add(-time.Hour))

	due, err := store.ListDueArchiveSchedules(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("due count = %d (%#v), want 2", len(due), due)
	}
	if due[0].ID != overdue || due[1].ID != dueSoon {
		t.Fatalf("due order = %d,%d; want %d,%d", due[0].ID, due[1].ID, overdue, dueSoon)
	}
	if len(due[0].Items) != 1 {
		t.Fatalf("items 未 hydrate: %#v", due[0])
	}

	limited, err := store.ListDueArchiveSchedules(ctx, now, 1)
	if err != nil {
		t.Fatalf("list due limit: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != overdue {
		t.Fatalf("limited = %#v", limited)
	}
}

// TestUpdateArchiveScheduleConcurrentEditsStayConsistent 覆盖"读当前值→推算 next_run_at
// →写回"的读改写竞态：并发编辑不应互相基于过期的 enabled / interval_minutes 推算。
func TestUpdateArchiveScheduleConcurrentEditsStayConsistent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	created, err := store.CreateArchiveSchedule(ctx, newTestSchedule("并发", 60, "alice"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const writers = 8
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				edit := created
				edit.IntervalMinutes = 10 + (w*10+i)%100
				if _, err := store.UpdateArchiveSchedule(ctx, edit); err != nil && errs[w] == nil {
					errs[w] = err
				}
			}
		}(w)
	}
	wg.Wait()
	for w, err := range errs {
		if err != nil {
			t.Fatalf("并发编辑 writer %d 失败: %v", w, err)
		}
	}

	final, err := store.GetArchiveSchedule(ctx, created.ID)
	if err != nil {
		t.Fatalf("get final: %v", err)
	}
	// next_run_at 必须与最终落库的 interval_minutes 自洽，不能来自别人的旧值。
	if final.NextRunAt.Before(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("next_run_at = %v 已过期，说明按过期值推算", final.NextRunAt)
	}
	if final.NextRunAt.After(time.Now().UTC().Add(time.Duration(final.IntervalMinutes+1) * time.Minute)) {
		t.Fatalf("next_run_at = %v 超出 interval %d 分钟", final.NextRunAt, final.IntervalMinutes)
	}
}
