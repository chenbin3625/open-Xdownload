# 代码审查 · internal/jobs

审查范围：`internal/jobs` 全部 7 个 Go 文件（含全部测试）。逐行通读；只读代码，未使用 git 历史/diff，未阅读 README/docs。

## 0. 阅读清单

| 文件 | 行数 | 已读完 |
|---|---|---|
| `internal/jobs/manager.go` | 1947 | 是（1–1947，分 4 段） |
| `internal/jobs/events.go` | 110 | 是（1–110） |
| `internal/jobs/preview.go` | 139 | 是（1–139） |
| `internal/jobs/manager_test.go` | 1775 | 是（1–1775，分 4 段） |
| `internal/jobs/events_test.go` | 87 | 是（1–87） |
| `internal/jobs/preview_test.go` | 40 | 是（1–40） |
| `internal/jobs/safename_test.go` | 39 | 是（1–39） |

为核实跨模块契约，另**只读**了这些文件的片段（非审查对象，仅作证据）：`internal/config/config.go`、`internal/config/config_test.go`、`internal/storage/storage.go`、`internal/xclient/client.go`、`cmd/server/main.go`、`internal/httpapi/server.go`、`internal/downloader/downloader.go`、`internal/filestore/filestore.go`、`internal/httpx/fetch.go`、`apps/web/src/components/settings/ConfigForm.tsx`、`apps/web/src/lib/useDashboardEvents.ts`、`Dockerfile`。

本地复核（只读执行，未改动任何源码）：

```text
$ go vet ./internal/jobs/                 # 无告警
$ go test -race -count=1 ./internal/jobs/ # ok，8.198s
$ go test -cover ./internal/jobs/         # coverage: 58.2% of statements
```

## 1. P0 发现

**未发现 P0。** 已专项核查的安全/崩溃面均未发现可达的 P0 级问题，结论与证据如下（避免 Lead 重复核查）：

- 文件名/目录名穿越：`safeName` → `sanitizePathSegment` 已收敛 `.`/`..`/纯点号、结尾点与空格、Windows 保留设备名（`manager.go:1853-1902`），单测覆盖（`safename_test.go:8-38`）；`singleURLFilenameHint` 即使返回 `..`，也会被 `downloader.Filename` 的 `sanitizeFilename`（去分隔符）与「总是追加扩展名」（`downloader.go:354-374`）兜底，不构成 `filepath.Join` 逃逸。
- EventBus 无 send-on-closed-channel：`Publish`/`Unsubscribe`/`CloseAll` 全部在 `bus.mu` 下对 `subscribers` 做增删与关闭，`Publish` 仅在 map 中现存 channel 上非阻塞发送（`events.go:75-91`），`CloseAll` 后再 `Unsubscribe` 有二次 close 保护（`events.go:49-57`）；有测试（`events_test.go:59-87`）。
- 取消与完成的状态竞争已被存储层条件 UPDATE 兜住：`UpdateJob` 的 WHERE 带终态保护（`storage.go:979-991`），`CancelJob` 是单条条件 UPDATE（`storage.go:994-1018`），worker 的终态保存不会把 `canceled` 覆盖回 `completed`。副作用见 [P2-6]。
- 媒体锁/用户锁（`keyLock`）引用计数与令牌交接经逐场景推演未发现泄漏/死锁：holder 也计入 `waiters`，故持有期间不会被淘汰；`releaseKeyLock` 只在 `waiters>0` 时把令牌放回容量 1 的 channel，不会阻塞；等待方 ctx 取消走 `abortKeyLock` 递减、无人时淘汰；panic 时 `defer release()` 仍会执行（`manager.go:924-976`、`1154-1158`、`710-716`）。真正的风险是这套机制在当前串行槽位下基本不可达，见 [P2-8]。

## 2. P1 发现

### [P1-1] 批量归档中任一用户的目录/链接错误会中止整个任务，且已完成的统计被丢弃

- 位置：`internal/jobs/manager.go:1084-1090`（错误直接上抛）、`manager.go:1160-1166`、`manager.go:1169-1173`、`manager.go:1443-1448`、`manager.go:1485-1494`
- 证据：

```go
// manager.go:1084-1090  archiveUsers 对单用户错误没有降级处理
delta, err := m.archiveUser(ctx, saveCtx, job, cfg, pool, task.user, listEntity, func(message string) {
    saveProgress(storage.JobDownloading, message)
})
addStats(delta)
if err != nil {
    return stats, err
}

// manager.go:1160-1166  archiveUser 把"单用户本地 FS/DB 错误"当作任务级错误返回
if _, err := m.store.UpsertUser(saveCtx, storageUser(user)); err != nil {
    return stats, err
}
entity, dir, err := m.ensureUserEntity(saveCtx, cfg, user)
if err != nil {
    return stats, err
}

// manager.go:1443-1448  ensureUserEntity 把 MkdirAll / 链接刷新错误上抛
if err := target.MkdirAll(ctx, dir); err != nil {
    return storage.UserEntity{}, "", err
}
if err := m.refreshUserLinks(ctx, cfg, user.ID, entity.Name, dir); err != nil {
    return storage.UserEntity{}, "", err
}

// manager.go:1492-1494  refreshUserLinks 把单条 syncLink 错误上抛
if err := syncLink(target.Join(listDir, name), targetDir); err != nil {
    return err
}
```

- 影响：`processList` / `processFollowing` 的成员列表可能有成百上千个用户，只要其中**一个**用户的本地状态异常，整个任务在第一个异常用户处失败：已完成用户的 `stats` 不落库、后续用户不再处理、任务变 `failed`。可触发路径并非只靠 DB 故障：
  - `syncLink` 遇到链接路径已存在真实目录时返回错误（`manager.go:1946` 的 `removeLinkPlaceholder` 分支），测试 `manager_test.go:835-852` 明确断言这是预期错误；该错误经 `refreshUserLinks → ensureUserEntity → archiveUser → archiveUsers` 一路上抛。
  - 用户目录创建失败（`DownloadDir` 进入只读、磁盘满、路径长度等）同样中止整批任务。
  - 对比 `manager.go:1201-1206`：时间线读取失败只记该用户 `Failed/Issues` 并继续，说明作者对「单用户错误」本意是降级处理，但 FS/实体错误路径没有做到。
- 建议：`archiveUsers` 中把 `archiveUser` 返回的错误按类别处理——`context.Canceled`/`ErrAllClientsRateLimited`/DB 不可用（可判定）继续上抛；其余记录 `stats.Failed++ / Issues` 并 `continue`。同时把 `ensureUserLink` 的失败继续按"issue"记录（已有行为），避免一条坏链接中断整批。若担心 DB 层错误被吞，可对错误做 `errors.Is`/存储层哨兵值分类。

### [P1-2] 默认配置下，每个归档任务都会全量重跑全局失败推文队列，重试放大并阻塞唯一的调度槽位

- 位置：`internal/jobs/manager.go:542`、`manager.go:628`、`manager.go:677`（三个调用点）、`manager.go:1324-1337`；默认值来源 `internal/config/config.go:98-107`
- 证据：

```go
// manager.go:542 / 628 / 677：每个 user/list/following 任务收尾都会尝试自动重试
retried := m.retryFailedTweets(ctx, saveCtx, job, cfg, false)

// manager.go:1325  force=false 时只受配置开关控制
if !force && !cfg.AutoRetryFailed {
    return 0
}
// manager.go:1330-1337  只防并发，不防重复
if !m.retryMu.TryLock() {
    return 0
}
defer m.retryMu.Unlock()
items, err := m.store.ListFailedTweets(saveCtx, 200)

// config.go:101-102  Default() 中 AutoRetryFailed 默认为 true
MaxConcurrency:  1,
AutoRetryFailed: true,
```

- 影响：
  1. **重试放大**：失败队列是全局的（`ListFailedTweets` 不按用户过滤，`storage.go:2282-2288` 按 `updated_at ASC LIMIT 200`）。只要失败条目 ≤200（很常见），**每一个**新建的用户/列表/关注任务都会把全部失败条目重下一遍；持续失败（例如上游 5xx/超时）的条目只能靠 `TouchFailedTweet` 排到队尾，没有任何时间/次数退避（`manager.go:1388-1391`），失败多久就每任务重试多久（直到 180 天 `failedRecordRetention` 清理，`manager.go:58`）。
  2. **阻塞唯一槽位**：`availableSlots` 恒为 1（[P2-1]），自动重试发生在归档任务内部、完成保存之前；单个媒体下载超时可达 10 分钟（`downloader.New()`），200 条失败项 × 多个媒体会把当前任务无限拖长，期间进度冻结在 `0.94` 附近，队列中所有其他任务干等。
  3. 重试产生的下载记录归因到当前任务，见 [P2-5]。
- 建议：把"失败重试"收敛为独立的定时任务（使用 `JobKindFailedRetry`），并给 `failed_tweets` 增加 `next_retry_at`/`attempts` 做指数退避；或至少在自动路径上限制本次重试条数（如 20）、跳过 `updated_at` 在最近 N 分钟内的条目。`force=true` 仍保留手工全量重试语义。

## 3. P2 发现

### [P2-1] `MaxConcurrency` 是死配置：三处强制串行、manager 没有任何有效并发控制

- 位置：`internal/jobs/manager.go:313-318`、`internal/config/config.go:109-113`、`apps/web/src/components/settings/ConfigForm.tsx:312`；行为被测试锁定：`internal/jobs/manager_test.go:142-155`、`internal/config/config_test.go:103-111`
- 证据：

```go
// manager.go:313-318  参数被直接忽略，硬编码 1
func (m *Manager) availableSlots(_ int) int {
    const limit = 1
    m.mu.Lock()
    defer m.mu.Unlock()
    return limit - len(m.active)
}

// config.go:109-113  Normalized() 无条件覆盖
func (cfg AppConfig) Normalized() AppConfig {
    ...
    cfg.MaxConcurrency = 1

// manager_test.go:142-146  测试明确断言"忽略配置"
func TestAvailableSlotsIgnoresConfiguredConcurrency(t *testing.T) {
    manager := NewManager(nil, parser.NewService(), NewEventBus())
    if got := manager.availableSlots(8); got != 1 {
```

- 判断：**属于刻意串行**，不是漏改——config 归一化、manager 硬编码、Web 端 `normalizeConfig` 三处一致，且两处单测显式锁定。但它没有任何注释说明动机，`app_config.max_concurrency` 与 `AppConfig.MaxConcurrency`/API 字段仍在读写（`storage.go:761`、`storage.go:873`），属于「有意的死配置」：用户/调用方提交的并发值会被静默改成 1，仓库内也没有 UI 入口（`ConfigForm.tsx` 无该字段输入）。同时它使 `managerStopTimeout`、`lockMedia`、`lockUser`、并发测试（`TestDownloadMediaConcurrentSameTargetDownloadsOnce`）的目标语义变得可疑——生产上永远不会有两个任务并发。
- 建议：至少补注释说明"全局串行是产品决策"；或把字段从 `AppConfig`/API 移除、保留只读常量，避免误导；如果未来要放开并发，需要同时改三处（config/manager/web）并重新审查锁与 SQLite 写竞争。跨模块请 Lead 与 config/server reviewer 对齐。

### [P2-2] 调度循环 goroutine 没有 panic 恢复，`runOnce` 任一 panic 会终止整个进程

- 位置：`internal/jobs/manager.go:230-243`、`manager.go:245-264`；唯一的 recover 在 `manager.go:359-366`（只覆盖单个任务 goroutine）
- 证据：

```go
// manager.go:230-243  无 defer recover
func (m *Manager) loop(ctx context.Context) {
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            m.runOnce(ctx)   // runOnce 内 panic 将直接杀掉进程
        case <-m.wake:
            m.runOnce(ctx)
```

- 影响：`runOnce` 调用了 `enqueueDueArchiveSchedules`、`maybeMaintain`、`store.*`、`publish*`、`startJob` 等；任务 goroutine 有 recover（防止坏 payload 崩进程），但调度器本身没有。一旦调度路径出现 panic（存储层/JSON 编码/未来代码改动），进程崩溃、所有排队任务停摆（重启后 `RequeueInterruptedJobs` 可恢复，但已运行任务的进度丢失）。
- 建议：给 `runOnce` 套一层 `defer recover()` 记录 `debug.Stack()` 并继续循环（或把 panic 上报事件），保持"坏数据不自杀"的现有设计目标一致。

### [P2-3] `Stop` 超时后照常返回，调用方随后关闭数据库，而任务 goroutine 可能仍在写库

- 位置：`internal/jobs/manager.go:99-124`、`internal/jobs/manager.go:367`（`saveCtx` 恒为 `context.Background()`）、`cmd/server/main.go:144-147` + `main.go:62`
- 证据：

```go
// manager.go:99-124
func (m *Manager) Stop() {
    ...
    select {
    case <-done:
    case <-time.After(managerStopTimeout):
        log.Printf("manager.Stop: %d task(s) did not drain within %s: %v", ...)
    }
}
// manager.go:367  任务写库刻意不随取消中止
m.process(jobCtx, context.Background(), job)

// main.go:144-147
// 等待调度循环与活跃任务退出，再让 defer store.Close() 关闭数据库，
manager.Stop()
```

- 影响：`managerStopTimeout = 15s`（`manager.go:69`）到期即返回，`main` 继续执行 `defer store.Close()`；仍在运行的任务 goroutine 用 `context.Background()` 写库，之后会持续报 `database is closed` 类错误（只 log），并可能丢失终态/进度。`Stop()` 也不把"是否排空"返回给调用方。注释已承认这是"有界等待、避免 SIGTERM 卡死"的取舍，但缺少调用方可感知的结果。
- 建议：`Stop() error`（或返回 bool/未退出 id 列表），`main` 在超时后延迟 `store.Close()`（例如再等一小段或在进程退出前不关闭 DB），并评估把 `managerStopTimeout` 降到与容器停止宽限期匹配（见 [P2-7]）。

### [P2-4] 后台维护在调度 goroutine 内同步执行，启动后第一 tick 就可能阻塞任务领取

- 位置：`internal/jobs/manager.go:245-246`、`manager.go:322-341`、`manager.go:335`
- 证据：

```go
// manager.go:245-247  runOnce 先做维护，再领任务
m.maybeMaintain(ctx)
m.enqueueDueArchiveSchedules(ctx)
...
// manager.go:324-340
if !m.lastMaintenance.IsZero() && now.Sub(m.lastMaintenance) < maintenanceInterval {
    return
}
m.lastMaintenance = now
if pruned, err := m.store.PruneFailedRecords(...)
...
if removed, err := downloader.RemoveStaleTempFiles(ctx, cfg.DownloadDir, staleTempFileAge); err != nil ...
```

- 影响：`RemoveStaleTempFiles` 是 `filepath.WalkDir` 遍历整个下载根目录（`downloader.go` 内实现），在调度 goroutine 中同步执行；`lastMaintenance` 初值为零，因此**进程启动后第一个 tick 就会跑一次全盘扫描**。大型归档（几万~几十万文件、网络盘）时，任务领取被推迟数秒到数分钟；该函数虽然响应 ctx 取消，但正常运行时无法与调度并行。
- 建议：把维护放到独立 goroutine（用自己的 running 标记防止重入），或启动时延后/限流执行（例如只在无活跃任务时跑），避免与任务领取串行。

### [P2-5] 自动重试产生的下载记录被归因到"当前正在跑的归档任务"

- 位置：`internal/jobs/manager.go:1364`（重试复用当前 `job`）→ `manager.go:791-799`（`CreateDownload` 用 `job.ID`）
- 证据：

```go
// manager.go:1358-1364  retryFailedTweets 里拿到的 job 是触发重试的归档任务
for index, media := range tweet.Media {
    ...
    if _, err := m.downloadMedia(ctx, saveCtx, job, cfg, mediaURL, tweet.ID, dir, ...); err != nil {
...
// manager.go:791-799  downloadMedia 把下载记录写到该 job 名下
_, err = m.store.CreateDownload(saveCtx, storage.DownloadRecord{
    JobID:       job.ID,
    TweetID:     tweetID,
    MediaURL:    mediaURL,
    ...
```

- 影响：`processUser` 自动重试时，重试成功的媒体（可能属于**别的用户**）会记在正在跑的用户/列表任务下：`GET /api/jobs/{id}/files` 的"任务文件"列表会混入无关用户的文件，前端按 job 统计下载数也会失真（`completeArchive` 的 `Downloaded` 不含这些重试下载，只额外显示"重试成功 N"）。`failed_tweets` 行本身有 `job_id`（原始任务），而 `DownloadRecord` 丢弃了它。
- 建议：重试下载使用独立的重试任务 id（如专门的 `JobKindFailedRetry` 任务），或至少用 `item.JobID`/新增"来源任务"字段记录归因，避免污染当前任务。

### [P2-6] `save()` 不检查条件 UPDATE 是否命中，可能发布与数据库终态不一致的 `job.updated` 事件

- 位置：`internal/jobs/manager.go:1566-1575`、`internal/storage/storage.go:979-991`
- 证据：

```go
// storage.go:979-991  终态保护：0 行命中时仍返回 err == nil
UPDATE jobs SET ... WHERE id = :id
  AND (status NOT IN ('completed', 'completed_with_errors', 'failed', 'canceled') OR status = :status)

// manager.go:1566-1575  未检查影响行数，照样广播内存里的 job
func (m *Manager) save(ctx context.Context, job storage.Job) {
    if err := m.store.UpdateJob(ctx, job); err != nil {
        log.Printf("save job %d: %v", job.ID, err)
        return
    }
    job.UpdatedAt = time.Now().UTC()
    m.publishJob(ctx, "job.updated", job)
}
```

- 影响：典型窗口——用户 `CancelJob` 把 DB 置为 `canceled` 后，worker 的最后一次 `save(completed)` 被 WHERE 拦下（DB 仍是 canceled），但 `save` 仍然广播 `completed`（且 `UpdatedAt` 比 DB 里的更新）。前端 `isStaleJobUpdate`/`mergeNewerJobs`（`apps/web/src/lib/useDashboardEvents.ts:76-86`）会偏向这条"更新更晚"的事件；终态事件会触发全量刷新（同文件 `patchDashboardJobCaches` 的 `needsFullRefresh`），一般能自愈，但事件与数据库的契约被破坏，属于可观测性/一致性问题。
- 建议：改用 `ExecContext` + `RowsAffected()`，0 行时重新读取当前 job 再决定是否发布（或直接跳过发布），并把 DB 的 `updated_at` 作为事件时间戳。

### [P2-7] 关停总预算（10s server + 15s manager）超过容器默认 10s SIGTERM 宽限期

- 位置：`internal/jobs/manager.go:69`、`cmd/server/main.go:133-147`、`Dockerfile`（无 `STOPSIGNAL`，仓库内无 `stop_grace_period`）
- 证据：

```go
// manager.go:68-69
// managerStopTimeout 关停时等待调度循环与活跃任务退出的最长时间。
managerStopTimeout = 15 * time.Second

// main.go:134-147
shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
if err := server.Shutdown(shutdownCtx); err != nil { ... }
...
manager.Stop()
```

- 影响：Docker 默认 `docker stop` 的 SIGTERM→SIGKILL 宽限期是 10s。最坏情况下 `server.Shutdown` 用满 10s，`manager.Stop` 还没开始就被 SIGKILL，`RequeueInterruptedJobs` 之外的收尾（尤其 [P2-3] 中的 DB 关闭顺序）全部失效。正常路径任务会被根 ctx 立即取消、退得很快，但预算设计上不自洽。
- 建议：把两段预算收紧到合计 < 停止宽限期（如 server 3s + manager 5s），或在仓库内显式声明 `STOPSIGNAL`/`stop_grace_period`（compose/k8s 侧），并在部署文档中说明。跨模块请 Lead 与 ops/部署 reviewer 确认。

### [P2-8] 串行槽位下 `lockMedia`/`lockUser` 实际不可达，并发测试绕过了调度器

- 位置：`internal/jobs/manager.go:932-954`、`manager.go:1279-1307`、`manager_test.go:1341-1404`
- 证据：

```go
// manager.go:253-263  runOnce 每 tick 最多 claim 1 个任务（availableSlots 恒 ≤1）
available := m.availableSlots(cfg.MaxConcurrency)
if available <= 0 { return }
jobs, err := m.store.ClaimPendingJobs(ctx, available)
...
for _, job := range jobs { m.startJob(ctx, job) }
```

- 影响：调度侧严格串行（[P2-1]），因此"同一用户被两个任务同时归档"、"同一媒体被并发下载"在生产中不会发生；`lockUser` 的 `waited=true → stats.Skipped++`（`manager.go:1174-1177`）与 `mediaLocks` 冲突路径都属于防御性死代码。`TestDownloadMediaConcurrentSameTargetDownloadsOnce` 直接对 `downloadMedia` 起 6 个 goroutine，验证的是不可能出现的生产时序，容易给人"并发安全已被覆盖"的错觉。反过来说，这些锁在"未来放开并发"时是必要的，不算负资产，但当前没有端到端验证。
- 建议：要么在注释/测试名里明确"锁为未来并发预留，当前不可达"；要么新增一个测试专用入口真实驱动两个任务（例如显式 `MaxConcurrency>1` 的测试钩子），让 `lockUser` 的等待/跳过分支（目前 `lockUser` 覆盖率 71.4%、`abortKeyLock` 覆盖率 0%）真正被跑到。

## 4. P3 与优化建议

1. **`m.once` 使 Start/Stop 生命周期不可重入**：`Start` 用 `sync.Once`（`manager.go:85-97`），`Stop` 之后再次 `Start` 静默无效；`Stop` 先于 `Start` 调用时 `stopCancel == nil`，之后 `Start` 会正常起循环且再无人能停（`manager.go:101-107`）。建议加 `started`/`stopped` 状态位或注释说明仅支持一次性启动。
2. **任务完成后不 Notify**：`finishJob`（`manager.go:371-375`）只从 `active` 删除，不触发 `Notify()`；下一个排队任务要等 2s ticker（`manager.go:231`）。批量任务串行时每个任务固定多 0–2s 延迟，建议在 `finishJob` 后 `m.Notify()`。
3. **每 2s 空转 DB 查询**：即使无排队任务也执行 `GetConfig` + `ClaimPendingJobs`，并每 tick 调 `ListDueArchiveSchedules`（`manager.go:245-268`）。量级小，但可先查轻量计数或仅在 wake/到期时查询。
4. **`Manager.complete` 是死代码**：`manager.go:1535-1541` 从未被调用（`go tool cover` 显示 0.0%），与 `completeArchive` 语义重叠。建议删除。
5. **统计/完成文案语义混淆**：`stats.Skipped` 同时累加"输入用户重复数"（`manager.go:1033`）、"用户被其他任务归档而跳过"（`manager.go:1175`）和"媒体跳过"（`manager.go:1260-1264`）；`completeArchive` 的"跳过 N"无法区分（`manager.go:1722-1727`）。`stats.Users` 在 `waited`/受保护账号场景也计入（`manager.go:1167`）。建议拆成 `SkippedUsers`/`SkippedMedia`。
6. **blocked/muted 用户被静默剔除**：`manager.go:1110-1112` 直接 `continue`，既不计数也不进 Issues；列表成员数与应用显示的用户数会对不上。建议至少计入 `Skipped` 或在 Issues 里说明。
7. **`UpdateUserEntityMediaCount` 可能把计数写成 0**：`user.MediaCount` 来自 GraphQL 用户对象（`manager.go:1209`、`manager.go:1220`），若该字段缺失为 0 会覆盖库内正常值，影响 `archiveUserTasks` 的 `missingMedia` 排序（`manager.go:1130-1135`）。建议只在 `MediaCount > 0` 时更新。
8. **`renameEntityDir` 部分完成**：目录已 `Rename` 成功但 `RewriteDownloadPathPrefix` 失败时直接返回错误（`manager.go:1456-1467`），此刻磁盘已改名、DB 未改，媒体库短暂 404；下次运行靠 `filestore.Rename` 对不存在源返回 nil 自愈（`filestore.go:66-71`），但中间态没有回滚或告警。
9. **`ensureUserEntity` 对 legacy 空名不迁移**：`existing.Name != "" && existing.Name != name` 才改名（`manager.go:1433`），若历史实体 `name == ""`，会在新名下另建空目录，同一用户文件分裂到两个目录（待确认旧库是否可能存在空名）。
10. **`safeName` 未处理 C0 控制字符**：只替换 `\r`/`\n` 并用 `strings.Fields` 折叠空白（`manager.go:1853-1858`）；NUL 等字符会保留到路径段，`os.MkdirAll` 失败后触发 [P1-1] 的整批中止。另：截断 180 字节（`manager.go:1862-1869`）理论上可让两个超长显示名落到同一目录；`sanitizePathSegment` 的 `_` 前缀会让结果超过 180 字节（影响很小）。建议用 `strings.Map` 过滤 `unicode.IsControl`。
11. **`syncLink` 把 `os.IsExist` 当成功**：`manager.go:1922-1926`，若并发/外部在 `removeLinkPlaceholder` 与 `Symlink` 之间创建了任意对象，会静默接受错误目标。另 `removeLinkPlaceholder` 的错误文案说"不是符号链接或文件"，实际只接受符号链接（`manager.go:1946`），文案与实现矛盾；`.link` 回退文件是该机制的可视副作用（`manager.go:1928`）。
12. **`singleURLFilenameHint` 依赖下游兜底**：可返回 `..`、`CON`、超长段（`manager.go:480-487`），本身不穿越是因为 `downloader.Filename` 会拼扩展名并去分隔符；建议在 jobs 侧也做一次 `sanitizePathSegment` 风格收敛，减少跨模块隐式契约。
13. **`isPhotoURL` 可读性与匹配方式**：`manager.go:1687-1693` 的 `||`/`&&` 优先级不直观，且用 `strings.Contains(host, "pbs.twimg.com")` 子串匹配（`pbs.twimg.com.evil.com` 也会命中；当前 direct media URL 有 `httpapi` 的 twimg 白名单兜底，风险低）；`pbs.twimg.com` 分支未排除 `.mp4`。建议改成 `isAllowedTwimgHost` 式的后缀判断并加括号。
14. **`archiveIssueSummary` 依赖错误文案格式**：`manager.go:1766-1785` 通过首个 `": "`、`"@"`、中文"的"解析 action/target，任何错误文案调整会静默改变分组结果（已有较充分的单测，`manager_test.go:617-678`）。建议改为结构化 issue（action/target/reason 字段）。
15. **媒体文件名与目录名的 Windows 保留名处理不一致**：目录名有 `safeName` 处理 `CON/NUL/COM1...`（`manager.go:1887-1897`），媒体文件名走 `downloader.Filename`/`sanitizeFilename`（`downloader.go:352-374`）没有保留名处理；Windows 上 tweet 文本为 `CON` 时 `CON.mp4` 建文件失败。当前主部署是 Linux/Docker，若官方支持 Windows 建议补。跨模块。
16. **事件丢帧无任何可观测性**：`events.go:75-91` 缓冲满时静默丢弃，仅靠注释说明前端轮询兜底（前端确有 5s 轮询：`apps/web/src/App.tsx:91-107`）。建议加计数器/限频日志，便于排查"UI 不动"类问题。`EventBus` 单测未覆盖缓冲溢出的丢弃分支。
17. **`publishJob` 在 created/终态时额外查 `DashboardMeta`**（`manager.go:140-149`、`160-166`）：每次终态事件多一次 DB 聚合查询；高频批量任务下是写放大。可考虑节流或由前端按需拉取。
18. **事件时间戳略新于数据库**：`save()` 在 `UpdateJob` 之后才把内存 `UpdatedAt` 设为 `time.Now()`（`manager.go:1573`），比 DB 行内的 `updated_at` 更晚；前端 `isStaleJobUpdate` 会因此保留事件值。建议直接使用 DB 写回的时间。
19. **分页查找用 OFFSET**：`skipArchivedMedia`/`skipSameContentMedia` 用 `offset += 500` 翻页（`manager.go:827-859`、`868-906`），期间若有并发的 `CreateDownload` 插入，OFFSET 分页可能跳过/重复条目，导致明明存在的副本被漏判、多下一次。当前串行执行降低了概率；跨模块可评估 keyset 分页（按 id）。
20. **测试细节**：
    - `manager_test.go:30-40`、`manager_test.go:89-98` 在 `httptest` handler goroutine 里调用 `t.Fatal`——Go 官方要求 `Fatal` 只能在测试 goroutine 调用，否则可能让测试 hang/误报；应改为向 channel 发送错误再由测试主体断言。
    - `TestShutdownLeavesJobRequeueable`（`manager_test.go:87-140`）只断言"状态不是 canceled"，没有验证 `Stop` 是否真的等到了 goroutine 退出，也没有验证重启后 `RequeueInterruptedJobs` 能让任务恢复执行；Stop 超时/泄漏分支完全未覆盖（`Stop` 覆盖率 62.5%）。
    - `TestAvailableSlotsIgnoresConfiguredConcurrency` 把串行当成规范固化，若未来放开并发该测试会过期，建议改名/注释说明产品决策。
    - 未覆盖：调度器 panic、`lockUser` 等待/取消分支、`abortKeyLock`（0%）、`xPool`（0%）、`ensureListEntity`/`ensureUserLink`（0%）、`isTwimgPreviewHost`（0%）、`ensureVideoPoster` 大部分分支（39.1%）、`retryFailedTweets` 的 permanent-unavailable 分支、`processFailedRetry`/`processUser`/`processList`/`processFollowing`/`processTweetLink`（均 0%，见下条）。
21. **测试覆盖数据（实测 `go test -cover` 58.2%，`go tool cover -func`）**：整个 job 类型分发与状态机是最大盲区——`processTweetLink`、`processUser`、`processList`、`processFollowing`、`processFailedRetry` 均为 **0.0%**，`process` 33.3%，`archiveUser` 28.8%，`xPool` 0%；`cancel`（`manager.go:1577`，用户取消时唯一的终态落库路径）0.0%。也就是说：任务调度 → 分发 → 失败/取消收尾这条最关键的链路没有任何端到端测试，现有测试主要覆盖可单独调用的 helper 与 media 下载。`httpapi` 的测试用 `NewManager(db, nil, eventBus)` 且不启动调度（`internal/httpapi/server_test.go:76-78`），也不能补上这段覆盖。建议至少为 media URL / failed-retry 两类补"启动 Manager → 轮询 DB 终态"的端到端测试，并为 process* 抽取可注入的依赖（parser/pool）以便直接测试。
22. **`TestCancelJobStopsActiveDownload` 提前结束，取消收尾未被验证**：`manager_test.go:27-85` 在 `eventually` 里只等 `store.GetJob().Status == canceled`——这个状态是 `CancelJob` 自己写进去的，测试随即返回；此时 job goroutine 可能还在 `handleInterrupt` 之前。实测单跑该测试 `cancel` 覆盖率仍是 **0.0%**，证明 `handleInterrupt → m.cancel`（`manager.go:1589-1594`）从未被执行到。与此同时测试的 `defer store.Close()`（`manager_test.go:47`）可能与仍在运行的 goroutine 并发操作 DB（"database is closed" 类错误会被 `m.save` 静默 log 掉），属于潜在 flake 与掩盖真问题的组合。建议测试改为等待"goroutine 已退出"（例如 Manager 提供 `active` 计数/Stop 后断言），再关闭 store。
23. **`unavailable_media` 的"续期"注释与实际行为不符（跨模块）**：`downloadMedia` 在 `GetMediaDownloadState` 命中 unavailable 时直接返回（`manager.go:717-723`），不会再调用 `UpsertUnavailableMedia`；而 `PruneFailedRecords` 的注释声称"持续失败的媒体每次被记录都会续期"（`storage.go:2352-2358`）。实际效果是永久失效记录在首次标记后 180 天过期、随后被重试一次。行为本身可接受（自带自愈），但注释需修正。

## 5. 待确认/需 Lead 交叉验证

1. **全局串行是否是产品决策**：代码三处一致 + 两个单测锁定，倾向"刻意串行"；但 `maxConcurrency` 仍出现在 `AppConfig`/DB/API（无 UI 输入），文档不可读，需 Lead 结合 docs/产品确认；若是刻意，建议在 `config.Normalized` 处加注释（[P2-1]）。
2. **404/410 是否可安全视为"永久不可用"**：`isPermanentlyUnavailableMediaError`（`manager.go:1411-1420`）只认 404/410/DMCA-403，但命中后写入**全局** `unavailable_media` 并永久跳过（直到 180 天老化）。X 的签名 URL 是否可能瞬时返回 404/410（而非媒体真删除）无法从代码验证；若可能，会造成"一次误判 → 半年内不再下载"。请 Lead 与 xclient/产品侧确认，或考虑加"最少 N 天后重试一次"的兜底。
3. **旧版本是否会在 `lists/` 下留下真实目录**：这决定 [P1-1] 的触发概率（`syncLink` 对真实目录报错 → 整批任务失败）。不能查 git 历史，请 Lead 交叉 storage/迁移 reviewer 或历史版本行为。
4. **跨用户共享同一 `file_path` 的产品语义**：`skipArchivedMedia`/`skipSameContentMedia` 会让不同用户/推文的下载记录指向同一物理文件（测试显式要求，如 `manager_test.go:1104-1159`）。请确认媒体库删除、路径重写、导出等场景都按"多记录指向一份文件"处理（storage/httpapi reviewer）。
5. **`process*` 零覆盖是否有其他包的 e2e 补偿**：我实测 `internal/jobs` 覆盖率 58.2%，`httpapi` 测试不启动 manager；请 Lead 确认全仓 `go test -race ./...` 之外没有脚本级 e2e 覆盖这些 job 类型。
6. **Docker 停止宽限期**：仓库 Dockerfile 未设 `STOPSIGNAL`/`stop_grace_period`，部署编排（compose/k8s，可能在仓库外）是否显式放宽 10s 无法确认（[P2-7]）。
7. **list/following 任务绕过 Pool 轮换（跨模块契约，需 xclient reviewer 确认）**：`processList`/`processFollowing` 直接 `pool.Primary()` 后调用 `Client.GetListByID`/`GetListMembers`/`GetFollowing`/`FollowUser`（`manager.go:588-589`、`610`、`642`、`659`、`1180`），这些是 `Client.do` 直连，不走 `Pool.graphQL` 的故障转移/限流交还（`xclient/client.go:766-830`）。多 Cookie 配置下，主客户端被禁用/限流时，列表/关注任务会立即失败或阻塞到限流复位（`client.doOnce → limiter.before`），与用户时间线路径行为不一致。请 xclient reviewer 确认是否有意为之。

## 6. 设计上值得肯定之处（简短）

- **串行化目标贯彻得彻底**：config 归一化、manager 槽位、Web 端三处一致，避免了 SQLite 单写连接下的并发写放大与跨任务竞态；媒体/用户锁是面向未来的防御。
- **取消语义设计正确**：`isCancellation` 只信 job ctx、不信 error（`manager.go:1604-1610`），区分"用户取消 / 进程关停 / 网络超时"；`handleInterrupt`（`manager.go:1585-1594`）在优雅关停时保留 `Downloading/Resolving` 交给 `RequeueInterruptedJobs`，比标 canceled 更可恢复；`CancelJob` 先落库再 cancel ctx 的时序也正确。
- **media 去重/复用做得克制且安全**：`MediaIdentity` 规范化、媒体锁、按身份键复用已有文件、内容哈希二次去重、`ExistingFileOwner` 防覆盖其他推文文件，都有对应单测（`manager_test.go:869-1404`）；`skipSameContentMedia` 删除的是本次刚发布的新文件（`manager.go:885`），不误删他人文件。
- **文件系统加固到位**：`safeName` 的 `..`/Windows 保留名/长度处理与单测（`safename_test.go`），媒体目录 0700、文件 0600、硬链接发布、临时文件清理、`downloads` 隐藏权限加固，`syncLink` 拒绝删除真实目录（`manager_test.go:835-852`）。
- **EventBus 关停模型正确**：`CloseAll` 解决 `http.Server.Shutdown` 不取消处理中请求 ctx 导致 SSE 挂死的问题（`events.go:59-73`，`main.go:106-108`），二次关闭/重复 Unsubscribe 都有保护与测试；非阻塞投递保证慢消费者不拖住调度。
- **预览图回填有安全边界**：`savePosterImage` 限定 twimg.com 且限制重定向主机（`preview.go:85-93`、`136-139`），原子发布、限大小、失败只记日志不影响归档任务；测试覆盖超限不落盘（`preview_test.go:13-40`，虽然主机白名单本身缺测试）。
- **测试质量整体在平均线以上**：取消真实打断 HTTP 请求（`manager_test.go:27-85`）、网络超时不得当取消（`manager_test.go:1406-1484`）、关停可重排（`manager_test.go:87-140`）、目录改名重写路径（`manager_test.go:1486-1578`）、失败计划退避（`manager_test.go:1580-1623`）等都是有价值的回归测试，CI 以 `go test -race ./...` 运行（`.github/workflows/ci.yml:54-55`），本地 `-race` 通过。
