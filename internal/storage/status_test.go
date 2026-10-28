package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestJobStatusSetsAreSingleSource 锁定"状态集合单一来源"（§3.1-11）：活动/终态集合必须
// 严密划分全部状态，生成的 SQL 片段与触发器必须覆盖相应状态，且 Go 判断与 SQL 口径一致。
func TestJobStatusSetsAreSingleSource(t *testing.T) {
	counts := make(map[JobStatus]int, len(AllJobStatuses))
	for _, status := range ActiveJobStatuses {
		counts[status]++
	}
	for _, status := range TerminalJobStatuses {
		counts[status]++
	}
	if len(counts) != len(AllJobStatuses) {
		t.Fatalf("active+terminal union = %d statuses, want %d", len(counts), len(AllJobStatuses))
	}
	for _, status := range AllJobStatuses {
		if counts[status] != 1 {
			t.Fatalf("status %q appears %d times in active/terminal sets, want exactly 1", status, counts[status])
		}
	}
	for _, status := range FailedJobStatuses {
		if !containsStatus(TerminalJobStatuses, status) {
			t.Fatalf("failed status %q is not terminal", status)
		}
	}
	for _, status := range CompletedJobStatuses {
		if !containsStatus(TerminalJobStatuses, status) {
			t.Fatalf("completed status %q is not terminal", status)
		}
	}
	for _, status := range InterruptedJobStatuses {
		if !containsStatus(ActiveJobStatuses, status) {
			t.Fatalf("interrupted status %q is not active", status)
		}
	}

	// 生成的 SQL 片段必须逐个包含集合成员。
	checkSQL := func(name, fragment string, statuses []JobStatus) {
		for _, status := range statuses {
			if !strings.Contains(fragment, "'"+string(status)+"'") {
				t.Fatalf("%s SQL missing status %q: %s", name, status, fragment)
			}
		}
	}
	checkSQL("active", activeJobStatusesSQL, ActiveJobStatuses)
	checkSQL("terminal", terminalStatusesSQL, TerminalJobStatuses)
	checkSQL("failed", failedJobStatusesSQL, FailedJobStatuses)
	checkSQL("completed", completedStatusesSQL, CompletedJobStatuses)

	triggers := dashboardCounterTriggers()
	for _, status := range ActiveJobStatuses {
		if !strings.Contains(triggers, "'"+string(status)+"'") {
			t.Fatalf("counter trigger SQL missing active status %q", status)
		}
	}
	for _, status := range append(append([]JobStatus{}, CompletedJobStatuses...), FailedJobStatuses...) {
		if !strings.Contains(triggers, "'"+string(status)+"'") {
			t.Fatalf("counter trigger SQL missing counted status %q", status)
		}
	}

	// Go 侧判断与集合一致，且活动/终态互斥。
	for _, status := range AllJobStatuses {
		if IsActiveJobStatus(status) == IsTerminalJobStatus(status) {
			t.Fatalf("status %q: active=%v terminal=%v, want exactly one", status, IsActiveJobStatus(status), IsTerminalJobStatus(status))
		}
	}
}

func containsStatus(items []JobStatus, want JobStatus) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// TestDashboardCountersMatchAllStatuses 用每一种状态各建一个任务，验证触发器计数与
// RecountDashboardCounters 的口径一致（状态集合只维护一份，两处结果必须相同）。
func TestDashboardCountersMatchAllStatuses(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	for _, status := range AllJobStatuses {
		job, err := store.CreateJob(ctx, JobKindUser, string(status), string(status))
		if err != nil {
			t.Fatalf("create job %q: %v", status, err)
		}
		job.Status = status
		if _, err := store.UpdateJobAndGet(ctx, job); err != nil {
			t.Fatalf("update job %q: %v", status, err)
		}
	}
	want := JobStats{Total: len(AllJobStatuses)}
	for range ActiveJobStatuses {
		want.Active++
	}
	for range CompletedJobStatuses {
		want.Completed++
	}
	for range FailedJobStatuses {
		want.Failed++
	}
	stats, failedTweets, err := store.DashboardMeta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats != want || failedTweets != 0 {
		t.Fatalf("trigger counters = %+v failed=%d, want %+v", stats, failedTweets, want)
	}
	if err := store.RecountDashboardCounters(ctx); err != nil {
		t.Fatal(err)
	}
	recounted, _, err := store.DashboardMeta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recounted != stats {
		t.Fatalf("recount %+v != trigger counters %+v (status sets drifted)", recounted, stats)
	}
}
