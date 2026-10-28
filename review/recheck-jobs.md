# jobs 复核（task-9）：REPORT §3.2 + P1-7/P1-8 修复状态

- 复核对象：`internal/jobs/**`（`manager.go` 12:46:41 版本 `43179e48…`、`manager_test.go` 12:46:19 版本 `38513e03…`）。
- 方法：逐条读当前工作区代码；在 `/tmp/xdlprobe`（仓库副本，不改仓库）写探针复现行为；`git diff` 只作辅助、不采信注释。
- 命令：`go test -count=5 ./internal/jobs/` ✅、`go test -race -count=1 ./internal/jobs/` ✅、`go test -race -count=1 ./...` ✅、`gofmt -l` 空、`go vet` ✅；两处 Lead 报红的测试另跑 `-count=200` / `-count=100`(GOMAXPROCS=1) / `-race -count=10` 全绿。
- 注意：复核期间工作区**被并发编辑**（12:46 有他人写入 content-hash 锁 + 两个新测试），下结论均基于上述哈希快照。

## 1. 判定总览

| 条目 | 状态 | 关键证据（当前行号） |
|---|---|---|
| P1-7 单用户目录/链接错误中止整批 | ✅ 已修复 | `manager.go:1128-1142`：仅 `*os.PathError`/`*os.LinkError` 降级为 `stats.Failed++`+Issue+`continue`，DB 错误（`Code() int`）与取消/限流仍上抛；探针 4/5：坏目录、真实目录占位链接 → `err=nil`、其余用户继续、`Failed:1` |
| P1-8 每任务全量重跑失败队列 | ✅ 已修复 | 归档任务不再调用重试（仅 `processFailedRetry:587` 调用 `retryFailedTweets:1388`）；`EnsureAutomaticRetryJob(storage.go:2504-2522)` + `TouchFailedTweet` 指数退避（10min→24h，`storage.go:2487-2498`）+ `ListFailedTweetsForRetry` 只取到期项 + 每批 20；探针 2/6/8：Touch 后 `due_now=0`，45 条 → 3 个任务（20+20+5）后终止，无死循环 |
| §3.2 调度 panic recover | ✅ | `manager.go:257-262` defer recover + stack；`TestSchedulerRecoversFromPanic:27`。残留：维护 goroutine 无 recover |
| §3.2 Stop 排空结果 + 关停预算 | ✅ | `Stop() bool`（`manager.go:110-135`）；`managerStopTimeout=5s`（:65）+ `Shutdown 3s` = 8s < 10s；`main.go:189-193` 未排空才 `os.Exit(1)`；Dockerfile:43 `STOPSIGNAL SIGTERM` |
| §3.2 维护移出调度 goroutine | ✅ | `manager.go:91-104` 独立 goroutine + 12h ticker，`runOnce` 不再调 `maybeMaintain`；启动仍会做一次全盘 WalkDir（已不在任务领取路径上） |
| §3.2 自动重试归因 | ✅ | 重试只发生在独立 `JobKindFailedRetry` 任务内（`processFailedRetry:576`），归档任务的下载记录不再混入重试媒体 |
| §3.2 save 不校验 RowsAffected | ✅ | `save:1653-1661` 用 `UpdateJobAndGet`（`storage.go:1114-1137`，`RETURNING *`），终态保护拒绝时返回 nil → 不广播；`TestSaveDoesNotPublishAnUpdateRejectedAfterCancellation:135` |
| §3.2 MaxConcurrency 死配置 | ❌ 未修复 | `config.go:137` 仍强制 1、`availableSlots(_ int)` 仍硬编码 1（`manager.go:337-341`）、`config_test.go` 仍锁定；无注释说明（本轮修复补注释） |
| §3.2 串行槽位下 lockMedia/lockUser 不可达、`abortKeyLock` 0% | ❌ 未修复 | 槽位仍恒 1；`abortKeyLock` 仍 0% 覆盖；注释未说明"为未来并发预留" |
| §3.2 `m.complete` 死代码 | ❌ 未修复 | `manager.go:1621-1627` 仍无调用者、0% 覆盖 |
| §3.2 `stats.Skipped` 语义混淆 | ❌ 未修复 | `1075`(跳过的用户)/`1232`(链接去重)/`1271/1325`(媒体) 仍混入一个计数器 |
| §3.2 blocked/muted 静默剔除 | ❌ 未修复 | `archiveUserTasks:1162-1164` `continue`，不计数、不记 Issue |
| §3.2 `UpdateUserEntityMediaCount` 可能写 0 | ❌ 未修复 | `manager.go:1265/1275` 无条件写 `user.MediaCount` |
| §3.2 Start/Stop 不可重入 | ❌ 未修复 | `once.Do`（:82）；Stop 后再 Start 静默无效 |
| §3.2 任务结束不 Notify | ✅ | `finishJob:391-396` 调 `m.Notify()` |
| §3.2 maybeMaintain 每 2s 空转 DB | ✅ | 已移出 `runOnce`；但每 tick 新增 `EnsureAutomaticRetryJob` 写事务（见回归 R4） |
| §3.2 renameEntityDir 中间态无回滚 | ✅ | `manager.go:1539-1552` DB 重写失败时目录回滚 + `TestRenameEntityDirRollsBackWhenDatabaseRewriteFails:49`；但 `filestore.Rename` 改语义引出回归 R3 |
| §3.2 safeName 未过滤 C0 | 🟡 部分 | `manager.go:1953-1961` 已去 C0+DEL，但未用 `unicode.IsControl`（C1 0x80-0x9F 仍在），本轮修复 |
| §3.2 syncLink 把 os.IsExist 当成功 | ✅ | `syncLink:1998-2031` 校验链接目标；`removeLinkPlaceholder:2033-2046` 返回 `*os.PathError(ErrExist)`，被 `archiveUsers` 识别降级 |
| §3.2 错误文案格式耦合 | ❌ 未修复 | `archiveIssueSummary:1826-1863` 仍用 `": "`/`"@"`/`"共 N 次"` 反解 |
| §3.2 404/410 全局 unavailable_media 180 天 | ❌ 未修复 | `downloadMedia:786-796` 仍写全局黑名单；`storage.PruneFailedRecords` 仍在 180 天后删记录（黑名单本身无到期） |
| 我报告 P2：重试归因已完成 | ✅ 同 P1-8 |
| 我报告 P2：批量任务统计被丢弃 | ✅ 同 P1-7（`addStats` 在错误前已累加 + 降级路径保留 stats） |
| 我报告 P2：`ListFailedTweets` 语义 | 🟡 变为"只取到期项"（`storage.go:2500-2502` 委托 `ListFailedTweetsForRetry(force=false)`），当前无生产调用方，仅测试受影响 |

## 2. 新引入的回归（本轮修复引入）

- **R1（P1）未解析媒体永久堵死增量游标 + 僵尸重试条目**：`archiveTweets:1294-1303` 对 `bestMediaURL==""` 的媒体 `stats.Failed++`、`cursorBlocked=true`、`rememberFailedTweet`；而 `retryFailedTweets` 对同一 payload 永远 `failed=true` → `TouchFailedTweet`（退避顶到 24h）→ 条目永不删除。探针 12：混合时间线（1 条正常 + 1 条无 mp4 变体视频）→ 正常推文已下载但 `last_seen=""`，第 2 轮仍 `failed=1`，5 次强制重试后 `remaining=1`。后果：开 `IncrementalArchive` 的用户永久全量重扫（正是游标要避免的成本）、任务永远 `completed_with_errors`、`failed_tweets` 永不清理；`TestArchiveTweetsBlocksCursorWhenMediaHasNoDownloadURL:2045` 把该行为固化为期望。（本轮修复：不再入队、游标推进）
- **R2（P2）取消自动重试任务无效、任务行白涨**：`EnsureAutomaticRetryJob` 只排除 pending/resolving/downloading 与"10 分钟内 failed"；`canceled` 不在其列 → 取消后 ≤2s 内重建。探针 1：Cancel 后 5 次 Ensure 重建 1 个、`jobs` 自动行=2。
- **R3（P2）崩溃中间态自愈丢失**：`filestore.Rename` 源缺失从"返回 nil"改为 `os.ErrNotExist`（`filestore.go:66-74`），`renameEntityDir:1542-1545` 对任何 Rename 错误都"沿用旧名"→ 进程在 `os.Rename` 与 DB 重写之间被杀后，磁盘=新名、DB=旧名，下次运行保留旧名并新建空旧目录。探针 7：`users/` 同时存在 `Alice(alice)`/`Alice2(alice)`，`download.file_path` 指向不存在的旧路径（`exists=false`）→ 媒体库 404 + 重复下载。修复前该状态靠 Rename nil + 前缀重写自愈。
- **R4（P2）调度器每 2s 一次空写事务**：`runOnce:269-275` 在 `AutoRetryFailed`（默认 true）下每 tick 调 `EnsureAutomaticRetryJob`，该 `INSERT…SELECT` 即使 0 行也要写锁。探针 13：另一连接持写锁时调用阻塞 >1.5s（证明需要写锁）。与任务进度写入争抢 SQLite 单写者。
- **R5（P2）自动重试任务行/失败计数无界增长**：每次到期批次新建一个 jobs 行（探针 14：200 条坏条目一次 drain 产生 10 行；`jobs` 表无任何清理逻辑）；且 `processFailedRetry:600-605` 用**全局** `CountFailedTweets` 判 `completed_with_errors`（含仍在退避期的条目）→ 每个自动重试任务都记成"失败"，`dashboard_counters.failed`（`counters.go:106` 计入 `completed_with_errors`）单调增长。探针 6：3 个批次全部 `completed_with_errors`。
- **R6（P3）`os.Exit(1)` 语义**：`main.go:189-193` 主动停机超时也 exit 1（跳过 defer 的 `store.Close/lock.Close`），systemd/docker 会当成崩溃；注释"leave SQLite open for workers still unwinding"与实际行为不符。
- **R7（P3）** `runOnce` recover 只记日志；若 panic 发生在 `ClaimPendingJobs` 之后、`startJob` 之前，已领取任务停在 `resolving` 直到进程重启（无进程内回收）。维护 goroutine 无 recover，panic 仍崩进程。
- **R8（P3）** 手动"全部重试"每任务只处理 20 条（`retryFailedTweets:1395`，HEAD 为 200），关闭自动重试时需反复点击。
- **R9（P3）** `CreateFailedTweet` ON CONFLICT 不重置 `retry_count/next_retry_at`（`storage.go:2472-2482`），重新失败的推文继承最长 24h 退避。
- **R10（P3）** 魔法串 `"automatic-failed-tweets"` 在 `storage.go:2509`、`manager.go:587`、测试三处重复，改动会静默翻转 force 语义。

## 3. 测试有效性 / flaky 判定

- Lead 提到的 `TestPersistenceErrorFailsArchive` **在当前工作区与 HEAD 都不存在**（HEAD 42 个 Test，当前 55 个）；最近似的是 `TestArchiveTweetsPropagatesPersistenceErrors`（queue/cursor 两个子用例）与 `TestArchiveUsersAbortsOnDatabaseWriteError`。`TestAutomaticRetryBacksOffWhenQueueWritesFail` 存在（UPDATE/DELETE 子用例）。→ 名字不匹配，疑为旧快照/记忆中笔误。
- 复现尝试：目标两测 `-count=200` ✅、`GOMAXPROCS=1 -count=100` ✅、`-race -count=10` ✅、6 进程 × `-count=50` ✅、3 轮 4 进程 × `go test ./...`（120 个 ok）✅，另跑 `-shuffle=on -count=20`。结论：**当前快照不可复现**。
- 关键旁证：复核期间**同一工作区正被并发编辑**。13:46-13:47 我亲历一次瞬时失败：`TestDownloadMediaPersistsCompletedDownloadAfterCancellation` 连续 50/50 失败（`completed download returned before being recorded: context canceled`），随后 `manager.go` 于 12:46:41 落盘 content-hash 锁修复后即 3/3 通过。说明"并行全量里偶发红、单跑绿"的机制是**测试跑在他人半写完的快照上**，不是产品竞态。
- 可加固项（非阻塞）：三个新测试用裸 `sql.Open("sqlite", dbPath)` 建触发器（`manager_test.go:949/989/1046`），无 `busy_timeout`；探针 11 证明该连接在别人持写锁时 240µs 内 `SQLITE_BUSY`。同文件 `:1965` 已有正确写法 `?_pragma=busy_timeout(5000)`，建议三处对齐（本轮修复顺手处理）。

## 4. 未修复清单（交付 Lead）

MaxConcurrency 死配置/无注释；lockMedia/lockUser 不可达且无预留说明、`abortKeyLock` 0%；`m.complete` 死代码；`stats.Skipped` 语义混淆；blocked/muted 静默剔除；`UpdateUserEntityMediaCount` 可写 0；Start/Stop 不可重入；错误文案反解耦合；404/410 全局黑名单无到期；`process*`/`cancel()` 覆盖仍低（`processTweetLink/processUser/processList/processFollowing` 0%、`cancel` 0%）；新回归 R1-R10。

## 5. 值得肯定

单用户降级、独立自动重试+退避+去重、调度 recover、Stop 返回排空、维护移出、save/UpdateJobAndGet、归因、关停预算、safeName/syncLink/isPhotoURL/renameEntityDir 回滚均落地且有针对性测试（新增 11 个测试）。
