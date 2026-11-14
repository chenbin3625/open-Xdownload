# 定时归档按用户分批（batchSize + LRU）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给定时归档目标增加“每轮最多用户数”（`batchSize`），全文模式下每轮按最久未归档优先挑选 N 个用户，降低单轮 `UserTweets` 请求量。

**Architecture:** 在 `jobs` 表持久化 `batch_size`，由 `Manager.archiveUsers` 在建立任务清单前（且非检查点恢复时）按 `tweet_sync.updated_at` 做 LRU 选择；`media` 模式与 `batchSize=0` 完全走原路径。前端只在“定时计划 + 非 media 模式”暴露该输入。

**Tech Stack:** Go 1.26、SQLite（sqlx）、React 19 + TypeScript + Semi UI、i18next、Vitest。

**Spec:** `docs/superpowers/specs/2026-11-14-archive-batch-design.md`

## Global Constraints

- `batchSize` 有效范围 `0..1000`，`0` 表示不限制；越界必须返回 `storage.ErrValidation`（HTTP 400）。
- 分批只在 `job.BatchSize > 0 && job.ArchiveMode.IncludesTweets()` 时生效；`media` 模式永不因 `batchSize` 减少用户。
- 从检查点恢复（限流重排、分段让出、重启续跑）时不得重新选人，继续原批次。
- 旧数据库与旧计划 JSON 没有 `batch_size` 时按 `0` 处理，行为与现在一致。
- 迁移遵循现有 `migrateArchive()` 风格：`pragma_table_info` 检查 + 幂等 `ALTER TABLE`。
- 前端默认值 `0`，提示“0 表示不限制，建议 50”；中英文 i18n 同步。
- 提交信息结尾统一加：`Co-Authored-By: Claude Code <noreply@anthropic.com>`。

## Review Focus

- `media` 模式计划误设 `batchSize`：仍应处理全部用户，而不是被截断。
- 限流重排或分段让出后恢复：必须继续同一批用户，不能按最新 `tweet_sync` 重新选人导致部分用户被跳过。
- `Protected && !Following` 用户：不写 `tweet_sync` 就会永久占据批次名额，必须更新时间戳让它轮转。
- 从未全文归档（无 `tweet_sync` 行）的用户：必须排在已归档用户之前，否则可能永远轮不到。
- 旧库/旧计划没有该字段：迁移后旧任务仍需可读，`batchSize` 为 0，行为不变。

---

### Task 1: 持久化 batchSize（模型、迁移、校验、透传）

**Files:**
- Modify: `internal/storage/models.go`（`Job`、`JobDraft`、`ArchiveScheduleItem`）
- Modify: `internal/storage/tweets.go`（`migrateArchive`）
- Modify: `internal/storage/storage.go`（`MaxArchiveBatchSize`、`insertJobs`、`RetryJob`、`prepareArchiveScheduleForSave`、`claimArchiveScheduleJobs`）
- Test: `internal/storage/archive_batch_test.go`（新建）

**Interfaces:**
- Produces: `const storage.MaxArchiveBatchSize = 1000`
- Produces: `storage.Job.BatchSize int`、`storage.JobDraft.BatchSize int`、`storage.ArchiveScheduleItem.BatchSize int`（JSON `batchSize,omitempty`，DB `batch_size`）
- Produces: 所有创建/重试/定时领取路径都保留该字段。

- [ ] **Step 1: 写失败测试**

新建 `internal/storage/archive_batch_test.go`：

```go
package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveScheduleBatchSizeRoundTripAndValidation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	schedule, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name: "weekly", Enabled: true, IntervalMinutes: 10080,
		Items: []ArchiveScheduleItem{{
			Kind: JobKindFollowing, Input: "me", ArchiveMode: ArchiveTweets, BatchSize: 25,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetArchiveSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].BatchSize != 25 {
		t.Fatalf("items = %+v, want batchSize 25", got.Items)
	}

	legacy, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name: "daily", Enabled: true, IntervalMinutes: 60,
		Items: []ArchiveScheduleItem{{Kind: JobKindUser, Input: "bob"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Items[0].BatchSize != 0 {
		t.Fatalf("missing batchSize decoded as %d, want 0", legacy.Items[0].BatchSize)
	}

	for _, bad := range []int{-1, MaxArchiveBatchSize + 1} {
		_, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
			Name: "bad", Enabled: true, IntervalMinutes: 60,
			Items: []ArchiveScheduleItem{{Kind: JobKindUser, Input: "alice", BatchSize: bad}},
		})
		var uf *UserFacingError
		if !errors.As(err, &uf) || uf.Status != 400 {
			t.Fatalf("batchSize %d: err = %v, want 400 validation", bad, err)
		}
	}
}

func TestJobBatchSizePersistsThroughCreateRetryAndScheduleClaim(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, JobDraft{
		Kind: JobKindFollowing, Input: "me", ArchiveMode: ArchiveTweets, BatchSize: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.BatchSize != 25 {
		t.Fatalf("created batchSize = %d, want 25", job.BatchSize)
	}
	// RetryJob 只为终态任务创建新任务；先把原任务标记为失败。
	job.Status = JobFailed
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.BatchSize != 25 {
		t.Fatalf("retried batchSize = %d, want 25", retried.BatchSize)
	}

	schedule, err := store.CreateArchiveSchedule(ctx, ArchiveSchedule{
		Name: "weekly", Enabled: true, IntervalMinutes: 10080,
		Items: []ArchiveScheduleItem{{
			Kind: JobKindFollowing, Input: "you", ArchiveMode: ArchiveTweets, BatchSize: 50,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.CreateScheduledArchiveJobs(ctx, schedule, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].BatchSize != 50 {
		t.Fatalf("scheduled jobs = %+v, want batchSize 50", jobs)
	}
}

func TestMigrateArchiveAddsBatchSizeColumn(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.CreateJob(ctx, JobKindUser, "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE jobs DROP COLUMN batch_size`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateArchive(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.Get(&count, `SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name='batch_size'`); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("batch_size column count = %d, want 1", count)
	}
	reloaded, err := store.GetJob(ctx, job.ID)
	if err != nil || reloaded.BatchSize != 0 {
		t.Fatalf("legacy job = %+v, err = %v; want batchSize 0", reloaded, err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/storage -run 'TestArchiveScheduleBatchSize|TestJobBatchSize|TestMigrateArchiveAddsBatchSize' -v`
Expected: FAIL，编译错误提示 `BatchSize`/`MaxArchiveBatchSize` 未定义或列不存在。

- [ ] **Step 3: 实现存储字段与迁移**

- `models.go`：
  - `Job` 增加 `BatchSize int \`json:"batchSize,omitempty" db:"batch_size"\``（放在 `Backfill` 后）。
  - `JobDraft` 增加 `BatchSize int`。
  - `ArchiveScheduleItem` 增加 `BatchSize int \`json:"batchSize,omitempty"\``。
- `tweets.go` 的 `migrateArchive` 列清单追加 `{"batch_size", "INTEGER NOT NULL DEFAULT 0"}`。
- `storage.go` 常量区增加 `const MaxArchiveBatchSize = 1000`。
- `insertJobs`：INSERT 列清单加 `batch_size`，占位符加一个，参数追加 `draft.BatchSize`；循环内校验：
  ```go
  if draft.BatchSize < 0 || draft.BatchSize > MaxArchiveBatchSize {
      return nil, ErrValidation(fmt.Sprintf("每轮用户数必须在 0 到 %d 之间", MaxArchiveBatchSize))
  }
  ```
- `RetryJob`：`UPDATE jobs SET archive_mode=?, backfill=?, batch_size=? WHERE id=?`，并同步 `retried.BatchSize = job.BatchSize`。
- `claimArchiveScheduleJobs` 的 `JobDraft` 追加 `BatchSize: item.BatchSize`。
- `prepareArchiveScheduleForSave` 在 item 去重前校验同一范围，返回同样的 `ErrValidation`。

- [ ] **Step 4: 运行测试确认通过**

Run: `gofmt -w internal/storage/models.go internal/storage/tweets.go internal/storage/storage.go internal/storage/archive_batch_test.go && go test ./internal/storage -run 'TestArchiveScheduleBatchSize|TestJobBatchSize|TestMigrateArchiveAddsBatchSize' -v`
Expected: PASS（3 个测试）。

- [ ] **Step 5: 提交**

```bash
git add internal/storage/models.go internal/storage/tweets.go internal/storage/storage.go internal/storage/archive_batch_test.go
git commit -m "feat(storage): persist archive batch size" -m "Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: 读取与更新 tweet_sync 时间戳

**Files:**
- Modify: `internal/storage/tweets.go`
- Test: `internal/storage/tweets_sync_test.go`（新建）

**Interfaces:**
- Produces: `func (s *Store) ListTweetSyncUpdatedAt(ctx context.Context) (map[string]time.Time, error)` — 只读，不创建行。
- Produces: `func (s *Store) TouchTweetSync(ctx context.Context, userID string) error` — UPSERT，只更新 `updated_at`，不清空 `last_id/cursor/status/count`。

- [ ] **Step 1: 写失败测试**

新建 `internal/storage/tweets_sync_test.go`：

```go
package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTouchTweetSyncCreatesRowAndPreservesFields(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.TouchTweetSync(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	times, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if times["u1"].IsZero() {
		t.Fatalf("u1 updated_at not recorded: %+v", times)
	}

	if err := store.SaveTweetSync(ctx, TweetSync{
		UserID: "u1", LastID: "100", Status: "completed", Count: 7,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchTweetSync(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetTweetSync(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 7 || state.Status != "completed" || state.LastID != "100" {
		t.Fatalf("TouchTweetSync clobbered fields: %+v", state)
	}
}

func TestListTweetSyncUpdatedAtDoesNotInsertRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	times, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(times) != 0 {
		t.Fatalf("times = %+v, want empty", times)
	}
	rows, err := store.ListTweetSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListTweetSyncUpdatedAt inserted rows: %+v", rows)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/storage -run 'TestTouchTweetSync|TestListTweetSyncUpdatedAt' -v`
Expected: FAIL，`TouchTweetSync` / `ListTweetSyncUpdatedAt` 未定义。

- [ ] **Step 3: 实现两个方法**

在 `GetTweetSync` 附近新增：

```go
func (s *Store) TouchTweetSync(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tweet_sync(user_id, updated_at) VALUES(?, ?)
ON CONFLICT(user_id) DO UPDATE SET updated_at=excluded.updated_at`,
		userID, time.Now().UTC())
	return err
}

func (s *Store) ListTweetSyncUpdatedAt(ctx context.Context) (map[string]time.Time, error) {
	rows := []struct {
		UserID    string    `db:"user_id"`
		UpdatedAt time.Time `db:"updated_at"`
	}{}
	if err := s.db.SelectContext(ctx, &rows, `SELECT user_id, updated_at FROM tweet_sync`); err != nil {
		return nil, err
	}
	result := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		result[row.UserID] = row.UpdatedAt
	}
	return result, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `gofmt -w internal/storage/tweets.go internal/storage/tweets_sync_test.go && go test ./internal/storage -run 'TestTouchTweetSync|TestListTweetSyncUpdatedAt' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/storage/tweets.go internal/storage/tweets_sync_test.go
git commit -m "feat(storage): add tweet sync LRU timestamps" -m "Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: 在 archiveUsers 中应用 LRU 批次

**Files:**
- Create: `internal/jobs/archive_batch.go`
- Modify: `internal/jobs/manager.go`（`archiveUsers`、`archiveUserWithBudget` 的受保护用户分支）
- Test: `internal/jobs/archive_batch_test.go`（新建）

**Interfaces:**
- Consumes: `storage.ListTweetSyncUpdatedAt`、`storage.TouchTweetSync`（Task 2）。
- Produces: `func selectArchiveBatch(users []xapi.User, syncedAt map[string]time.Time, limit int) []xapi.User`（同包内使用）。

- [ ] **Step 1: 写选择算法失败测试**

新建 `internal/jobs/archive_batch_test.go`：

```go
package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

func TestSelectArchiveBatchOrdersByLeastRecentSync(t *testing.T) {
	now := time.Now()
	users := []xapi.User{
		{ID: "u1", ScreenName: "recent"},
		{ID: "u2", ScreenName: "old"},
		{ID: "u3", ScreenName: "never-a"},
		{ID: "u2", ScreenName: "duplicate"},
		{ID: "", ScreenName: "empty"},
		{ID: "u4", ScreenName: "blocked", Blocking: true},
		{ID: "u5", ScreenName: "muted", Muting: true},
		{ID: "u6", ScreenName: "never-b"},
	}
	synced := map[string]time.Time{
		"u1": now,
		"u2": now.Add(-2 * time.Hour),
	}

	got := selectArchiveBatch(users, synced, 3)
	want := []string{"u3", "u6", "u2"}
	if len(got) != len(want) {
		t.Fatalf("selected %d users, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("selected[%d] = %s, want %s", i, got[i].ID, want[i])
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/jobs -run TestSelectArchiveBatch -v`
Expected: FAIL，`selectArchiveBatch` 未定义。

- [ ] **Step 3: 实现 selectArchiveBatch**

新建 `internal/jobs/archive_batch.go`：

```go
package jobs

import (
	"sort"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// selectArchiveBatch 按“最久未做全文归档”优先挑选最多 limit 个用户。
// 没有 tweet_sync 行的用户视为从未归档，排在最前；时间相同保持入参顺序。
func selectArchiveBatch(users []xapi.User, syncedAt map[string]time.Time, limit int) []xapi.User {
	if limit <= 0 || len(users) == 0 {
		return users
	}
	candidates := make([]xapi.User, 0, len(users))
	seen := make(map[string]struct{}, len(users))
	for _, user := range users {
		if user.ID == "" || user.Blocking || user.Muting {
			continue
		}
		if _, ok := seen[user.ID]; ok {
			continue
		}
		seen[user.ID] = struct{}{}
		candidates = append(candidates, user)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, leftOK := syncedAt[candidates[i].ID]
		right, rightOK := syncedAt[candidates[j].ID]
		leftMissing := !leftOK || left.IsZero()
		rightMissing := !rightOK || right.IsZero()
		if leftMissing != rightMissing {
			return leftMissing
		}
		if leftMissing {
			return false
		}
		return left.Before(right)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}
```

- [ ] **Step 4: 写接线测试（应失败）**

追加到 `internal/jobs/archive_batch_test.go`：

```go
const emptyTimelinePayload = `{"data":{"user":{"result":{"timeline_v2":{"timeline":{"instructions":[]}}}}}}`

func seedArchiveBatchJob(t *testing.T, store *storage.Store, mode storage.ArchiveMode) storage.Job {
	t.Helper()
	job, err := store.CreateArchiveJob(context.Background(), storage.JobDraft{
		Kind: storage.JobKindFollowing, Input: "me", ArchiveMode: mode, BatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestArchiveUsersAppliesBatchSizeToTweetModes(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	store.TouchTweetSync(ctx, "u1")
	store.TouchTweetSync(ctx, "u3")

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var names []string
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		variables := r.URL.Query().Get("variables")
		var vars struct {
			ScreenName string `json:"screenName"`
		}
		_ = json.Unmarshal([]byte(variables), &vars)
		names = append(names, vars.ScreenName)
		fmt.Fprint(w, emptyTimelinePayload)
	})
	job := seedArchiveBatchJob(t, store, storage.ArchiveTweets)
	users := []xapi.User{
		{ID: "u1", ScreenName: "synced-a"},
		{ID: "u2", ScreenName: "never"},
		{ID: "u3", ScreenName: "synced-b"},
	}
	if _, err := NewManager(store, parser.NewService(), NewEventBus()).archiveUsers(
		ctx, ctx, job, cfg, pool, users, nil, 0, 1,
	); err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "never" {
		t.Fatalf("requested users = %v, want only never-synced user", names)
	}
}

func TestArchiveUsersBatchSizeIgnoredForMediaMode(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	store.TouchTweetSync(ctx, "u1")
	store.TouchTweetSync(ctx, "u3")

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var mediaCalls int
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/UserMedia") {
			mediaCalls++
		}
		fmt.Fprint(w, mediaTimelinePayload(nil, ""))
	})
	job := seedArchiveBatchJob(t, store, storage.ArchiveMedia)
	users := []xapi.User{
		{ID: "u1", ScreenName: "a"},
		{ID: "u2", ScreenName: "b"},
		{ID: "u3", ScreenName: "c"},
	}
	if _, err := NewManager(store, parser.NewService(), NewEventBus()).archiveUsers(
		ctx, ctx, job, cfg, pool, users, nil, 0, 1,
	); err != nil {
		t.Fatal(err)
	}
	if mediaCalls != 3 {
		t.Fatalf("media calls = %d, want 3 (batchSize must not apply to media)", mediaCalls)
	}
}

func TestArchiveUsersBatchResumeKeepsOriginalSelection(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	store.TouchTweetSync(ctx, "u1") // 已归档，不应入选

	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	var names []string
	pool := archiveTestPool(t, &cfg, func(w http.ResponseWriter, r *http.Request) {
		var vars struct {
			ScreenName string `json:"screenName"`
		}
		_ = json.Unmarshal([]byte(r.URL.Query().Get("variables")), &vars)
		names = append(names, vars.ScreenName)
		fmt.Fprint(w, emptyTimelinePayload)
	})
	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind: storage.JobKindFollowing, Input: "me", ArchiveMode: storage.ArchiveTweets, BatchSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldBudget := archiveSliceBudget
	archiveSliceBudget = 0
	defer func() { archiveSliceBudget = oldBudget }()

	manager := NewManager(store, parser.NewService(), NewEventBus())
	users := []xapi.User{
		{ID: "u1", ScreenName: "synced"},
		{ID: "u2", ScreenName: "never-a"},
		{ID: "u3", ScreenName: "never-b"},
	}
	if _, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1); !errors.Is(err, errYieldExecution) {
		t.Fatalf("first slice err = %v, want yield", err)
	}
	if _, err := manager.archiveUsers(ctx, ctx, job, cfg, pool, users, nil, 0, 1); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "never-a" || names[1] != "never-b" {
		t.Fatalf("requested users = %v, want [never-a never-b] and no reselection", names)
	}
}

func TestArchiveUsersBatchTouchesProtectedNonFollowingUser(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	job, err := store.CreateArchiveJob(ctx, storage.JobDraft{
		Kind: storage.JobKindUser, Input: "locked", ArchiveMode: storage.ArchiveTweets, BatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.AppConfig{DownloadDir: t.TempDir(), StorageType: config.StorageLocal}
	users := []xapi.User{{
		ID: "u1", ScreenName: "locked", Protected: true, Following: false,
	}}
	stats, err := NewManager(store, parser.NewService(), NewEventBus()).archiveUsers(
		ctx, ctx, job, cfg, nil, users, nil, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Users != 1 {
		t.Fatalf("stats.Users = %d, want 1", stats.Users)
	}
	times, err := store.ListTweetSyncUpdatedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if times["u1"].IsZero() {
		t.Fatal("protected non-following user was not touched; it would pin a batch slot")
	}
}
```

补齐 imports：`encoding/json`、`fmt`、`net/http`、`strings`、`parser`。

- [ ] **Step 5: 运行接线测试确认失败**

Run: `go test ./internal/jobs -run 'TestArchiveUsersAppliesBatchSize|TestArchiveUsersBatchSizeIgnored|TestArchiveUsersBatchResume|TestArchiveUsersBatchTouchesProtected' -v`
Expected: FAIL；全文用例请求了全部 3 个用户、重选后请求了 `synced`，或受保护用户没有 `tweet_sync` 行。

- [ ] **Step 6: 接线 manager**

- `archiveUsers` 的 `if !resumed {` 内、`len(users) == 0` 检查之后：
  ```go
  if job.BatchSize > 0 && job.ArchiveMode.IncludesTweets() {
      syncedAt, err := m.store.ListTweetSyncUpdatedAt(saveCtx)
      if err != nil {
          return stats, err
      }
      users = selectArchiveBatch(users, syncedAt, job.BatchSize)
      if len(users) == 0 {
          return stats, nil
      }
  }
  ```
  必须在解析 `Protected` 用户与 `archiveUserTasks` 之前执行，避免给本批次外用户发 `GetUserByIDAsPrimary`。
- `archiveUserWithBudget` 的 `if user.Protected && !user.Following {` 分支，在自动关注逻辑之后、`return stats, nil` 之前：
  ```go
  if job.ArchiveMode.IncludesTweets() {
      if err := m.store.TouchTweetSync(saveCtx, user.ID); err != nil {
          log.Printf("touch tweet sync for @%s: %v", fallbackUserName(user), err)
      }
  }
  ```

- [ ] **Step 7: 运行测试确认通过**

Run: `gofmt -w internal/jobs/archive_batch.go internal/jobs/archive_batch_test.go internal/jobs/manager.go && go test ./internal/jobs -run 'TestSelectArchiveBatch|TestArchiveUsersAppliesBatchSize|TestArchiveUsersBatchSizeIgnored|TestArchiveUsersBatchResume|TestArchiveUsersBatchTouchesProtected' -v`
Expected: PASS（5 个测试）。

- [ ] **Step 8: 跑 jobs 与 storage 回归**

Run: `go test ./internal/jobs ./internal/storage`
Expected: PASS，无既有归档/检查点测试回归。

- [ ] **Step 9: 提交**

```bash
git add internal/jobs/archive_batch.go internal/jobs/archive_batch_test.go internal/jobs/manager.go
git commit -m "feat(jobs): rotate archive users by least recent sync" -m "Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: HTTP API 透传与校验

**Files:**
- Modify: `internal/httpapi/server.go`（`jobRequest`、`prepareJob`、`createJob`、`createJobsBatch`）
- Test: `internal/httpapi/archive_batch_test.go`（新建）

**Interfaces:**
- Consumes: `storage.MaxArchiveBatchSize`、`storage.JobDraft.BatchSize`（Task 1）。
- Produces: `jobRequest.BatchSize int \`json:"batchSize"\``；单任务/批量任务 JSON 均接受并回显 `batchSize`；计划接口通过 `storage.ArchiveScheduleItem` 自动解析该字段。

- [ ] **Step 1: 写失败测试**

新建 `internal/httpapi/archive_batch_test.go`：

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestCreateJobPersistsBatchSize(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := jobs.NewManager(db, nil, nil)
	handler := authenticatedRoutes(t, NewServer(db, nil, manager, nil))

	request := newLoopbackRequest(http.MethodPost, "/api/jobs",
		strings.NewReader(`{"kind":"following","input":"me","archiveMode":"tweets","batchSize":25}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var job storage.Job
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.BatchSize != 25 {
		t.Fatalf("batchSize = %d, want 25", job.BatchSize)
	}
}

func TestCreateJobRejectsBatchSizeOverLimit(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := jobs.NewManager(db, nil, nil)
	handler := authenticatedRoutes(t, NewServer(db, nil, manager, nil))

	request := newLoopbackRequest(http.MethodPost, "/api/jobs",
		strings.NewReader(`{"kind":"following","input":"me","archiveMode":"tweets","batchSize":1001}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "每轮用户数") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateArchiveSchedulePersistsBatchSize(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := jobs.NewManager(db, nil, nil)
	handler := authenticatedRoutes(t, NewServer(db, nil, manager, nil))

	body := `{"name":"weekly","enabled":true,"intervalMinutes":10080,"items":[{"kind":"following","input":"me","archiveMode":"tweets","batchSize":50}]}`
	request := newLoopbackRequest(http.MethodPost, "/api/archive-schedules", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var schedule storage.ArchiveSchedule
	if err := json.Unmarshal(response.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	if len(schedule.Items) != 1 || schedule.Items[0].BatchSize != 50 {
		t.Fatalf("items = %+v, want batchSize 50", schedule.Items)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/httpapi -run 'BatchSize' -v`
Expected: FAIL；返回的 `batchSize` 为 0，越界请求返回 201。

- [ ] **Step 3: 实现 API 透传与校验**

- `jobRequest` 增加 `BatchSize int \`json:"batchSize"\``。
- `prepareJob` 开头（归档模式校验后）：
  ```go
  if req.BatchSize < 0 || req.BatchSize > storage.MaxArchiveBatchSize {
      return "", "", "", storage.ErrValidation(fmt.Sprintf("每轮用户数必须在 0 到 %d 之间", storage.MaxArchiveBatchSize))
  }
  ```
- `createJob` 的 `JobDraft` 增加 `BatchSize: req.BatchSize`。
- `createJobsBatch` 的 `JobDraft` 增加 `BatchSize: item.BatchSize`。

- [ ] **Step 4: 运行测试确认通过**

Run: `gofmt -w internal/httpapi/server.go internal/httpapi/archive_batch_test.go && go test ./internal/httpapi -run 'BatchSize' -v`
Expected: PASS（3 个测试）。

- [ ] **Step 5: 提交**

```bash
git add internal/httpapi/server.go internal/httpapi/archive_batch_test.go
git commit -m "feat(api): accept archive batch size" -m "Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: 前端输入、类型、i18n 与计划卡片

**Files:**
- Modify: `apps/web/src/lib/api.ts`
- Modify: `apps/web/src/components/modals/CreateJobModal.tsx`
- Modify: `apps/web/src/pages/SchedulesPage.tsx`
- Modify: `apps/web/src/locales/zh-CN.json`、`apps/web/src/locales/en.json`
- Modify: `README.md`
- Test: `apps/web/src/components/modals/CreateJobModal.test.tsx`

**Interfaces:**
- Consumes: API `batchSize`（Task 4）。
- Produces: `Job.batchSize?: number`、`JobRequest.batchSize?: number`、`ArchiveScheduleItem.batchSize?: number`；定时计划提交的 item 携带 `batchSize`。

- [ ] **Step 1: 写失败测试**

在 `CreateJobModal.test.tsx` 的 `describe("CreateJobModal")` 中追加：

```tsx
it("submits batchSize for tweet-mode schedules", async () => {
  const user = userEvent.setup();
  vi.mocked(api.createArchiveSchedule).mockResolvedValue({
    id: 1, name: "weekly", enabled: true, intervalMinutes: 360,
    items: [], nextRunAt: "", lastJobIds: [], createdAt: "", updatedAt: "",
  });
  renderModal({ initialKind: "schedule" });

  await user.type(screen.getByPlaceholderText(/elonmusk/), "alice");
  const input = screen.getByRole("spinbutton", { name: "每轮最多用户数" });
  await user.clear(input);
  await user.type(input, "50");
  await user.click(screen.getByRole("button", { name: /保存定时计划/ }));

  await waitFor(() => expect(api.createArchiveSchedule).toHaveBeenCalledTimes(1));
  expect(api.createArchiveSchedule).toHaveBeenCalledWith(expect.objectContaining({
    items: [expect.objectContaining({ input: "alice", batchSize: 50 })],
  }));
});
```

- [ ] **Step 2: 运行测试确认失败**

Run: `npm --prefix apps/web run test -- src/components/modals/CreateJobModal.test.tsx`
Expected: FAIL；找不到名为“每轮最多用户数”的 spinbutton。

- [ ] **Step 3: 实现类型、状态、输入与提交**

- `api.ts`：三个接口类型各加 `batchSize?: number`。
- `CreateJobModal.tsx`：
  - 状态：`const [batchSize, setBatchSize] = useState(0);`
  - 定时计划 payload 的 item 映射改为：
    ```ts
    items: currentBatchItems.map(item => ({
      ...item, archiveMode, backfill,
      batchSize: archiveMode === "media" ? 0 : batchSize,
    })),
    ```
  - 定时计划设置网格（计划名称 / 调度频率）内追加，仅非 media 模式显示：
    ```tsx
    {archiveMode !== "media" && (
      <Field label={t("每轮最多用户数")}>
        <NumberInput
          min={0}
          max={1000}
          value={batchSize}
          onChange={(val) => setBatchSize(val ?? 0)}
          addonAfter={t("人")}
        />
        <p className="mt-1 text-[11px] text-fg-subtle">{t("0 表示不限制，建议 50")}</p>
      </Field>
    )}
    ```
- `SchedulesPage.tsx`：在“目标清单”行右侧统计中追加：
  ```tsx
  const batchSize = Math.max(0, ...schedule.items.map(item => item.batchSize ?? 0));
  ...
  <span>
    {schedule.items.length} {t(" 个目标")}
    {batchSize > 0 ? ` · ${t("每轮 {{value0}} 人", { value0: batchSize })}` : ""}
  </span>
  ```
- i18n（两个文件同步）：
  - `"每轮最多用户数"`: zh `每轮最多用户数` / en `Max users per run`
  - `"0 表示不限制，建议 50"`: zh `0 表示不限制，建议 50` / en `0 means unlimited; 50 recommended`
  - `"人"`: zh `人` / en `users`
  - `"每轮 {{value0}} 人"`: zh `每轮 {{value0}} 人` / en `{{value0}} users per run`
- `README.md` 的“创建定时归档计划”小节追加一句：目标可设置每轮最多用户数（`0` 表示不限制），全文归档按最久未归档优先轮转。

- [ ] **Step 4: 运行前端测试与类型检查**

Run: `npm --prefix apps/web run test -- src/components/modals/CreateJobModal.test.tsx`
Expected: PASS。
Run: `npx --prefix apps/web tsc --noEmit`
Expected: 无输出（通过）。

- [ ] **Step 5: 提交**

```bash
git add apps/web/src/lib/api.ts apps/web/src/components/modals/CreateJobModal.tsx apps/web/src/pages/SchedulesPage.tsx apps/web/src/locales/zh-CN.json apps/web/src/locales/en.json apps/web/src/components/modals/CreateJobModal.test.tsx README.md
git commit -m "feat(web): configure archive batch size" -m "Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: 全量验证

**Files:** 无新增。

- [ ] **Step 1: 跑 Go 全量测试**

Run: `go test ./...`
Expected: PASS。

- [ ] **Step 2: 跑前端全量测试与构建**

Run: `npm --prefix apps/web run test && npm --prefix apps/web run build`
Expected: PASS。

- [ ] **Step 3: 格式检查**

Run: `gofmt -l .`
Expected: 无输出。

- [ ] **Step 4: 提交设计与计划文档**

```bash
git add docs/superpowers
git commit -m "docs: add archive batch design and plan" -m "Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

- [ ] **Step 5: 确认工作区**

Run: `git status --short`
Expected: 无输出（干净）。
