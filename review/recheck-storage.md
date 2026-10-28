# 修复复核 · internal/storage

复核对象：当前工作区（未提交改动）`internal/storage/**`，对照 `review/REPORT.md`（P1-1 存储部分、§3.1 十四条、§4 跨模块 4/5/6/7/8）与 `review/storage.md`（我上一轮的原始审查），并把 `review/FIXES.md` 的修复声明只当作"待验证的断言"。
复核方式：**结论全部基于当前工作区代码**；`git diff -- internal/storage/` 只用于快速定位改动，凡引用都给出当前文件的行号与逐字代码。未修改任何源码/测试/配置，只写本文件；临时探针放在 `/tmp`。

---

## 0. 复核方法与验证结果

规模变化（相对上一轮）：`storage.go` 2505→2756（+251），`models.go` 216→234，`storage_test.go` 1270→1553，`migration_test.go` 484→579，`resilience_test.go` 262→294，`concurrency_test.go` 136→151，`counters_test.go` 82→84；`archive_schedule_test.go`、`media_state_test.go`、`counters.go` 未改动。`git diff --stat -- internal/storage/` = 7 文件 +858/−179。

本轮实际执行的命令与结果：

```
go vet ./internal/storage/                       exit 0（无告警）
go test -count=1 ./internal/storage/             ok  github.com/chenbin3625/open-Xdownload/internal/storage  1.874s
go test -race -count=1 ./internal/storage/       ok  ...  11.528s
go build ./...                                   exit 0
gofmt -l internal/storage/                       无输出
```

本轮早前（同一工作区）另已执行：`go test -count=5` 命中新增的缓存/配置/领取/大小写/退避用例 → `ok 5.534s`；`go test -count=1 ./...` 10 个包全绿；`GOOS=windows`/`GOOS=linux` 交叉 `go build ./...` 通过；`go test -cover ./internal/storage/` = 70.6%。

关键探针（`/tmp` 下临时程序，使用仓库同版本 `modernc.org/sqlite v1.59.0` + `jmoiron/sqlx`）：

1. **Windows 盘符路径的 DSN 构造**：`(&url.URL{Scheme:"file", Path:"C:/Users/dev/AppData/Roaming/x/data.db"}).String()` → `file://C:/Users/dev/AppData/Roaming/x/data.db`；`db.Ping()` → `SQL logic error: invalid uri authority: C: (1)`。同一字符串在本机（非 Windows）也报同一错误，说明这是 SQLite `uri.c` 的解析规则（与 OS 无关），且 modernc 编译选项里没有 `SQLITE_ALLOW_URI_AUTHORITY`（`grep -rc ALLOW_URI_AUTHORITY ~/go/pkg/mod/modernc.org/sqlite@v1.59.0/lib/sqlite_linux_amd64.go` = 0）。对照组：HEAD 的 `path + "?..."` 写法（Windows 下是 `C:\...` 或 `C:/...`，不以 `file:` 开头）在 Windows 上是合法普通文件名，可正常打开。
2. **PATCH 语义**：`json.Unmarshal` 未知键静默忽略；字段值为 `null` 时对非指针字段是 no-op（保留原值）；类型不符才返回 `json: cannot unmarshal number into ...`。
3. **`next_retry_at` 混合时间格式**：`datetime()` 写入 `2026-10-28 04:49:58`（无小数、无时区），驱动绑定 `time.Time` 写入 `...+0000 UTC` 格式；`SELECT *` 扫描回 `*time.Time` 正常、`next_retry_at <= ?`（绑定 time.Time）比较结果正确（UTC 下前 19 字符定长且字典序等价）；但 `julianday(驱动格式)` 返回 0（不可解析）。

状态统计（P1-1 存储部分 1 条 + §3.1 十四条 + P2-13 测试质量 9 条 + §4 跨模块 5 条 = 29 条）：

| 状态 | 数量 |
|---|---:|
| ✅ 已修复 | 13 |
| 🟡 部分修复 | 7 |
| ❌ 未修复 | 9 |
| ⚪ 已移除 | 0 |

---

## 1. 总览

- P1-1（改下载目录后历史媒体全 403 + 缓存不失效）**已修复并被端到端测试覆盖**：`UpdateConfig` 提交后立刻失效 library 缓存（`storage.go:1013`），历史绝对路径通过新表 `download_roots` + `httpapi.resolveDownloadPath` 继续可解析（`storage.go:963-971`/`1019-1023`、`server.go:1089-1104`）。残留为"升级前已经变更过目录的库无法回溯"与"env 变量目录从不登记"（见 §3 R3）。
- §3.1 十四条：**9 条已修复、4 条部分修复、1 条未修复**。修复质量总体高（每一条都有对应代码结构变化，且新增了针对性测试），但"状态集合单一来源"（P2-11）完全没动。
- P2-13 测试质量：缓存零覆盖（T1）与并发缓存一致性（T9）已补；**破坏性迁移 `dropSMBWebDAVConfigColumns` 仍然零测试**，弱断言/墙钟断言/`time.Sleep` 断言原样保留。
- §4 跨模块：`filestore.Rename` 源缺失语义与目录改名回滚已修（7）；JSON 契约主体已修（6）；文件名长度常量、twimg 白名单/压缩/海报抓取去重、ContentHash Skipped 契约**未收敛**（4/5/8）。
- **最严重的新问题：`Open` 的 DSN 改造在 Windows 上必然失败**（`file://C:/...` → `invalid uri authority: C:`），release 矩阵包含 windows/amd64 与 windows/arm64，而 CI 只跑 ubuntu-latest，构建能过、运行起不来（R1）。
- 其余新问题集中在：library 缓存重建成本上升（R4）、HTTP 层内存分页与默认 limit 静默变化（R5）、损坏计划行每 2s 刷日志（R6）、`UserFacingError` 的 Cause 永不落日志（R7）、`configMu` 跨事务持有（R8）、`download_roots` 的生命周期（R3）、`next_retry_at` 无索引 + 混合时间格式（R12/R13）。

---

## 2. 逐条核验

### 2.1 [P1-1] 存储部分：`UpdateConfig` 是否失效 library 缓存；历史记录是否仍有可用解析根

**状态：✅ 已修复**（含两处残留，见 §3 R3）

- 缓存失效：`internal/storage/storage.go:1011-1013`

```go
	s.cachedStored = &cfg
	s.cachedStoredAt = time.Now()
	s.invalidateLibraryDownloadsCache()
```

  位于 `updateConfig` 末尾（提交之后、且整个函数持有 `configMu`），TTL 也从 30 分钟降到 5 分钟（`storage.go:71`）。有测试 `TestUpdateConfigInvalidatesLibraryDownloadsCache`。

- 历史记录可解析的语义选择：**不重写 `downloads.file_path`，改为持久化"可信历史根"**。
  - 表：`storage.go:248-250` `CREATE TABLE IF NOT EXISTS download_roots (path TEXT PRIMARY KEY);`（`schemaVersion` 同步 2→4，`storage.go:93`；`migration_test.go:500-516` 登记了 v4 列集合）。
  - 登记：`storage.go:963-971`，在配置写入的**同一事务**内 `INSERT OR IGNORE` 当前根与新根（回滚则一起撤销）。
  - 读取：`storage.go:1019-1023` `SELECT path FROM download_roots ORDER BY length(path) DESC`（长者优先，嵌套根取更具体者）。
  - 消费：`httpapi/server.go:1089-1104` `resolveDownloadPath` 遍历 roots + currentRoot，命中即返回；`serveDownloadFile` 与 `serveDownloadPreview` 都改用它（`server.go:1143-1170`、`868` 起）；越界/不存在映射为 404 + 可读文案（`writeResolveError`，`server.go:1077-1087`），不再 403。
  - 端到端测试 `TestChangingDownloadDirPreservesHistoricalMedia`（`internal/httpapi/server_test.go:755-801`）验证改目录后历史 `file`/`preview` 仍返回 200 且 **DB 路径未被重写**。
- `UpdateConfigPatch` 语义：`storage.go:919-925` → `updateConfig(ctx, zero, fields)`；`storage.go:943-952` 以事务内读到的 `current` 为基底，反序列化 PATCH 字段（省略字段保留原值），随后统一走 `config.ResolveSubmission` + `ValidateDownloadRoot`，并与 httpapi 的预校验使用**同一个函数**（跨模块旧问题"占位符合并三份实现"已收敛）。测试 `TestConfigPartialUpdatePreservesSettings`（`server_test.go:731-753`）。
- 持久化确认：roots 与配置同事务提交；`Open` 不 seed（当前根由 `resolveDownloadPath` 的 `currentRoot` 兜底，所以不需要）。

### 2.2 REPORT §3.1 十四条

| # | 条目 | 状态 | 当前证据（`storage.go` 除注明） |
|---|---|---|---|
| 1 | `known` 集合由 LIMIT 截断结果构建 | ✅ 已修复 | `:1617-1626` 独立全量 `SELECT file_path FROM downloads` 建 `known`；列表查询另用 `LIMIT ?`，以 `limit+1` 判截断（`:1589`、`:1633-1636`）；测试 `TestLibraryLimitDoesNotTurnRecordedFilesIntoOrphans` |
| 2 | 孤儿扫描把 `.preview-*` 当媒体 | ✅ 已修复 | `:1762-1766` `IsLibraryMediaPath` 排除 basename 前缀 `.preview-` 与后缀 `.preview.jpg`；`:1657` 的旧后缀判断变冗余（无害） |
| 3 | 归档领取用 `next_run_at = ?` 文本等值 | ✅ 已修复（换方案） | `:1435-1447` 事务内 `SELECT * WHERE id=?` 后 Go 侧 `!current.NextRunAt.Equal(claimNextRunAt)`；`_txlock=immediate`（`:82`）保证读-比较-写在单进程/单库上原子；`storage_test.go:1135-1158` 用 `+08:00` 文本验证格式无关 |
| 4 | 大库迁移单事务全表读入 | 🟡 部分修复 | 五个迁移改为 keyset 分批 `WHERE id > ? ... LIMIT 1000`（`:601`、`:657`、`:686`、`:719`、`:755`），内存问题解决且有 `TestDownloadMigrationsBoundReadBatches`（`migration_test.go:30-76`，执行器断言单批 ≤1000 行）；但**仍是 `runMigrationOnce` 单事务**（`:446-471`），写锁持有时间与 `busy_timeout=5s` 后 `Open` 直接失败的风险未变（也无 SQLITE_BUSY 退避） |
| 5 | 配置缓存无 TTL、无跨进程失效 | ✅ 已修复 | `:72` `storedConfigCacheTTL = 1 * time.Second`；`:880-908` 双重检查后过期重读；`TestStoredConfigRefreshesAcrossStoreInstances` 验证另一 Store 实例写入后 2s 内可见 |
| 6 | `RequeueInterruptedJobs` 无实例归属/单实例锁 | 🟡 部分修复 | `:1204-1228` **函数本身未变**（仍无条件重置 `resolving/downloading`，jobs 表无 owner/心跳列）；改为进程级单实例锁：`cmd/server/main.go:69-73` `acquireDataLock(dbPath+".lock")` 失败即 `return 1`，`lock_unix.go` flock / `lock_windows.go` LockFileEx |
| 7 | `UpdateConfig` 提交→写缓存竞态 | ✅ 已修复 | `:928-929` `configMu.Lock()` 覆盖事务 + 提交 + 写缓存；`:1011-1013` 在锁内；`concurrency_test.go:18-62` 8×25 并发写后断言 `cached.DownloadDir == stored.DownloadDir` |
| 8 | `ListLibraryDownloads` 吞 `GetConfig` 错误 | ✅ 已修复 | `:1601-1604` 直接返回错误；`:1613-1616` `filepath.Abs` 失败也返回错误（旧代码返回到一半的结果） |
| 9 | `attributeLibraryUsers` 复杂度 + 缓存失效节流 | 🟡 部分修复 | 复杂度已从 O(记录×实体) 降为 O(记录×路径深度)：`:1707-1713` 建 `prefixes map[小写前缀]index`，`:1714-1729` 从路径尾部逐级 `/` 上溯查表并取最小 index（保持"最小 id 优先"）；另加 `libraryBuildMu` 单飞（`:1565-1582`）。但**每条 `CreateDownload` 仍立即失效缓存**（`:1515`/`:1533`），归档期间画廊刷新仍会反复重建（节流未做） |
| 10 | `JobFiles` 静默截断、无标记 | ✅ 已修复 | `:1941` 每类 `LIMIT MaxJobFilesPerKind+1`，`:1957`/`:1972` 置 `truncated`；API `jobFilesResponse.Truncated`（`server.go:525-529`、`:674`）；前端 `api.ts:236-240` + `TaskCenterPage.tsx:906` 显示提示。无 `total` 字段（报告建议的 total 未做） |
| 11 | 状态集合五处独立硬编码 | ❌ 未修复 | 仍全部字面量：`counters.go:50-52`、`:59-66`、`:73-75`、`:104-106`（触发器 + Recount），`storage.go:1125`（`UpdateJob` 终态集合）。只有 `CancelJob`（`:1151`）改成绑定常量；没有共享 SQL 片段/集合断言测试 |
| 12 | 单行坏 JSON 让列表/到期列表全失败 | ✅ 已修复 | `:2689-2699` `hydrateArchiveSchedules` 逐行解析，坏行 `log.Printf` 后跳过，其余照常返回（`:1360`、`:1375` 使用）；单行接口 `GetArchiveSchedule`（`:1295`）/领取（`:1448`）仍会报错——这是合理行为；测试 `TestHydrateSchedulesKeepsHealthyRows` |
| 13 | `known` 大小写未归一化 | ✅ 已修复 | `:1645-1649` 按小写建 `caseVariants`，`:1668-1672` 对每个候选 `os.Stat(recorded)` + `os.SameFile(info, recordedInfo)`（比单纯 ToLower 更稳，能处理 APFS/NTFS 的大小写不敏感）；测试 `TestLibraryRecognizesRecordedPathWithDifferentCase`（但在大小写敏感的 Linux CI 上 `t.Skip`，CI 无覆盖） |
| 14 | P3：PRAGMA/DSN/死表/死代码/版本校验等 | 🟡 部分修复 | 见下表 |

**#14 逐项裁定**（只标"是否修复"）：

| P3 子项 | 状态 | 证据 |
|---|---|---|
| `PRAGMA optimize` / `ANALYZE` | ❌ | 全仓库 `grep -rn "PRAGMA optimize\|ANALYZE" --include=*.go internal/ cmd/` 无结果 |
| DSN 直接拼 `path + options`（含 `?`/`#` 会坏） | ✅（POSIX） | `:107-111` `filepath.Abs` + `filepath.ToSlash` + `url.URL{Scheme:"file"}` 编码；测试 `TestOpenDatabasePathWithURICharacters`（`archive?#.db`）；**Windows 上变成新回归**，见 R1 |
| 死表 `user_previous_names` 只写不读 | ❌ | `:284` 建表 + `:360` 触发器写入，仍无任何 SELECT |
| 仅测试使用的 storage 方法 | ❌ | `HasDownload:2458`、`ListDownloads:1538`、`ListJobs:1041`、`GetUserLinks:2432`、`GetList:2370`、`GetListEntity:2409`、`FindDownloadsByMediaKey:2049`、`FindDownloadsByContentHash:2077` 仍在，非测试引用数均为 0；本轮还新增一个死包装 `ListLibraryDownloads:1555-1558`（httpapi 已改用 `...WithStatus`） |
| `cachedStored` 无版本校验 | ✅ | 改为 1s TTL（`:72`、`:880-908`） |
| `GetMediaDownloadState` 第二返回值语义含糊 | ❌ | `:2134-2158` 仍是 `(*DownloadRecord, bool, error)`，bool 语义未命名 |
| `CreateDownload` 两段 ~40 行 INSERT 复制粘贴 | ❌ | `:1497-1536` 仍是两段近乎重复的 SQL（缩进已对齐，但复制未消除） |
| `addMissingColumns` map 迭代顺序随机 | ❌ | `:480-489` 仍是 `map[string]string` 迭代 |
| `ListLibraryDownloads(limit<=0)` 语义与同包其它方法不一致 | ❌ | `:1561-1563` 仍是 `<=0 → MaxLibraryDownloadsLimit` |
| 缺 `failed_media(created_at)` / `unavailable_media(updated_at)` 索引 | ❌ | `:807-829` 未新增（`PruneFailedRecords` `:2613-2634` 仍全表） |
| `runMigrationOnce` 对已应用迁移也开 `BEGIN IMMEDIATE` | ❌ | `:446-471` 未变（每次启动 9 个迁移各取一次写锁） |
| 时间比较依赖隐式格式不变式 | ❌（且变复杂） | DSN 无 `_time_format`（`:82`），无 `timeToDB()` helper；新增 `TouchFailedTweet` 用 `datetime()` 写"无时区秒级文本"（`:2492`），与同列其它写入格式并存的混合格式，见 R12 |
| `DashboardMeta` 对 `sql.ErrNoRows` 返回零值 | ❌ | `counters.go:121-123` 未变 |
| `UpdateJob` 不检查 `RowsAffected` | 🟡 | 新增 `UpdateJobAndGet`（`:1114-1140`，无行返回 `(nil,nil)`），`jobs.save` 用它并在 nil 时不广播；但 `UpdateJob`（`:1107-1110`）仍丢弃返回值，仅测试在用 |

### 2.3 [P2-13] 测试质量问题

| 编号 | 条目 | 状态 | 证据 |
|---|---|---|---|
| T1 | library 缓存零覆盖 | ✅ 已修复 | 新增 `TestUpdateConfigInvalidatesLibraryDownloadsCache`、`TestLibraryDownloadsReportsExactTruncation`（`resilience_test.go:13-37`，含恰好等于上限不误报）、`TestLibraryLimitDoesNotTurnRecordedFilesIntoOrphans` |
| T2 | `dropSMBWebDAVConfigColumns` 零覆盖 | ❌ 未修复 | 全仓库（含测试）仍无 `smb_password`/`webdav` 引用 |
| T3 | 并发编辑计划断言过弱（10 与 109 都能过） | ❌ 未修复 | `archive_schedule_test.go:204-210` 未改，仍只断言 `next_run_at` 落在 `[now-1min, now+(interval+1)min]` |
| T4 | 并发领取原子性零测试 | ❌ 未修复 | 仍只有顺序二次领取（`archive_schedule_test.go:234`、`:261`）；新增的两条（等价时间表示、领取时用库里最新 items）都不是并发用例 |
| T5 | 墙钟断言 `>100ms` 易偶发 | ❌ 未修复 | `migration_test.go:282` 原样 |
| T6 | `time.Sleep(5ms)` 建立排序 | ❌ 未修复 | `storage_test.go:1445`、`:1449` |
| T7 | 迁移测试直接对 `*sqlx.DB`（自动提交） | ❌ 未修复 | `migration_test.go:47-56` 等仍传 `store.db`（新加的 `TestDownloadMigrationsBoundReadBatches` 反而用了显式 tx，方向正确） |
| T8 | 并发 Open 只测空库 | 🟡 部分修复 | `concurrency_test.go:70-92` 改为 5 次重试，但仍是空库；"已有数据 + 并发 Open/迁移"未覆盖 |
| T9 | 并发写配置只断言无错误 | ✅ 已修复 | `concurrency_test.go:44-62` 增加 `cached.DownloadDir == stored.DownloadDir` 断言 |

### 2.4 REPORT §4 跨模块（4/5/6/7/8）

| # | 条目 | 状态 | 证据 |
|---|---|---|---|
| 4 | 文件名长度常量两套 | ❌ 未修复 | `config.go:88-90`（120/16/240）与 `downloader.go:590-600` `normalizedMaxFilenameLength`（120/16/240）仍各自硬编码，`downloader` 未 import `internal/config`；值当前一致，但改一处即漂移 |
| 5 | twimg 白名单 4 份 / 压缩 2 份 / 海报抓取 2 份 | ❌ 未修复 | 白名单仍 4 份：`downloader.go:346 isAllowedTwimgHost`、`parser/service.go:683 isTwimgHost`、`httpapi/server.go:1001 isAllowedPreviewHost`、`jobs/preview.go:136 isTwimgPreviewHost`；gzip 仍 2 份：`httpapi/compress.go:20`、`cmd/server/static.go:457`；海报抓取仍 2 份：`httpapi/server.go:960 fetchRemotePoster`、`jobs/preview.go:97 fetchPosterImage`（行为是否漂移需各自领域复核，本次未展开） |
| 6 | JSON 契约小偏差 | 🟡 部分修复 | `jobFilesResponse` 去掉 `omitempty` 并加 `truncated`（`server.go:525-529`、`:674`），前端类型 `JobFiles {downloads[]; failed[]; truncated?}`（`api.ts:236-240`）→ 已对齐；`jobErrors.ts` 的正则改为 `(?:个(?:账号\|目标\|错误)?\|次)` 并新增"另有 N 种其他异常"分支（本轮 diff `+18/−6`），与 `manager.go` 的 `(共 %d 个账号: …)`/`(共 %d 次)`/`另有 %d 种其他异常` 对齐；**残留** `FailedTweet.payload`：前端 `api.ts:167` 仍有 `payload?: string`，后端 `FailedTweetView`（`models.go:192-206`）与 `ListFailedTweetViewsPage` 的 SELECT（`storage.go:2552-2565`）都不产出该字段（可选字段，仅幽灵契约） |
| 7 | `filestore.Rename` 源缺失静默 nil + `RewriteDownloadPathPrefix` 后置 | ✅ 已修复 | `filestore.go:66-74` 源缺失返回 `os.ErrNotExist`（HEAD 是 `return nil`）；`jobs/manager.go:1538-1552` 目录改名后 DB 重写失败会 `Rename(newPath, oldPath)` 回滚并返回错误；`Rename` 失败时日志+沿用旧名。storage 侧 `RewriteDownloadPathPrefix`（`:1853-1875`）仍是单条原子 UPDATE |
| 8 | `Result.ContentHash` 在 Skipped 路径为空 | 🟡 部分修复（契约仍脆弱，需 jobs 侧确认） | `downloader.go:158-175` `existingFileState` 命中完整文件时返回 `Result{Path,Bytes,Skipped:true}`，**无 ContentHash**；`jobs/manager.go:803/830` 原样写 `result.ContentHash`（跳过路径为空串）；靠 `storage.go:1507` `content_hash = CASE WHEN excluded.content_hash <> '' THEN excluded.content_hash ELSE downloads.content_hash END` 保住旧 hash（未变）。storage 侧兜底有效，但"跳过分支依赖 upsert CASE"的隐式契约仍在；`skipArchivedMedia` 分支已改为传 `copy.ContentHash`（`manager.go:880`） |

---

## 3. 新发现 / 回归

> 按严重度排序。所有条目都是**本轮引入或本轮才变得可触发**的问题；行号对应当前工作区。

### [R1·高] Windows 上 `storage.Open` 必然失败：`file://C:/...` 被 SQLite 判为非法 authority

- 位置：`internal/storage/storage.go:107-111`

```go
	absPath, err := filepath.Abs(path)
	...
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String() + sqliteOpenOptions
```

- 机理：Windows 下 `filepath.Abs("data/open-xdownload.db")` = `C:\...\data\open-xdownload.db`，`ToSlash` → `C:/...`；`url.URL{Scheme:"file"}` 在 Path 不以 `/` 开头时 `String()` 会输出 `file://C:/...`（已实测）。SQLite 的 URI 解析把 `C:` 当 authority，在未编译 `SQLITE_ALLOW_URI_AUTHORITY` 时直接报 `SQL logic error: invalid uri authority: C: (1)`。
- 证据：
  - 探针输出：`dsn="file://C:/Users/dev/AppData/Roaming/x/data.db?_pragma=busy_timeout(5000)"` → `ping err: SQL logic error: invalid uri authority: C: (1)`；同一字符串在本机（darwin）同样报错，说明与平台无关（`uri.c` 逻辑）。
  - `grep -rc ALLOW_URI_AUTHORITY modernc.org/sqlite@v1.59.0/lib/sqlite_linux_amd64.go` = 0，编译选项未开。
  - HEAD 写法 `path + sqliteOpenOptions` 在 Windows 下不以 `file:` 开头，SQLite 按普通文件名处理 → 可用（探针用反斜杠原始路径 ping 成功）。
- 影响：`Open` 返回错误 → `cmd/server/main.go:76-79` 打 `open storage: ...` 并 `return 1`；**Windows 发行包（release 矩阵 windows/amd64、windows/arm64）完全无法启动**。CI 只 `runs-on: ubuntu-latest`，交叉编译发现不了；`TestOpenDatabasePathWithURICharacters` 在 Windows 上同样会失败（且其文件名含 `?`，Windows 本身不允许）。
- 建议：`p := filepath.ToSlash(absPath); if !strings.HasPrefix(p, "/") { p = "/" + p }` 再用 `url.URL{Scheme:"file", Path: p}`；或仅在非 Windows 用 `file:` URI、Windows 继续用裸路径 + 只对 `?`/`#` 做百分号转义。顺带在 CI 加一条 `GOOS=windows go test`/最小 smoke。

### [R2·中] 启动时新增的下载根校验可能让存量部署升级后无法启动，且没有自愈路径

- 位置：`storage.go:138-146`（`Open` 内 `config.ValidateDownloadRoot(cfg.DownloadDir)`）、`config.go:43-56`

```go
	cfg, err := store.GetConfig(context.Background())   // 已套用 env override
	...
	if err := config.ValidateDownloadRoot(cfg.DownloadDir); err != nil {
		_ = db.Close()
		return nil, err
	}
```

- 影响：`ValidateDownloadRoot` 拒绝"文件系统根"与"家目录本身"。此前这类值只会在 `PUT /api/config` 被拒；现在只要**库内已存值**或 `OPEN_XDOWNLOAD_DOWNLOAD_DIR`（本轮新生效，`config.go:20-27`）是 `/` 或 `$HOME`，服务直接启动失败。用户没有 UI 可改（服务起不来），只能改 env 或手工改库——这是一条新的"升级即不可用"路径（安全方向正确，但需要迁移/提示）。
- 建议：`Open` 失败前给出可操作提示（指明两个修复途径），或对"库内旧值"降级为告警 + 回退到默认下载目录并写一条 `log`。

### [R3·中] `download_roots` 只在"保存配置"时登记：升级前已换过目录的历史记录仍 404；env 目录永不登记；roots 无清理

- 位置：`storage.go:963-971`（唯一写入点）、`:1019-1023`（读取）
- 三个具体缺口：
  1. 存量库升级：`download_roots` 初始为空，`Open` 不 seed。如果用户在**升级前**已经改过下载目录（正是 P1-1 的受害者），旧目录从未被登记，历史记录仍解析不到（返回 404 而非 403）；只有"升级后再保存一次配置"才会把当时的旧/新根写进去。
  2. `OPEN_XDOWNLOAD_DOWNLOAD_DIR` 生效的目录永远不会进 `download_roots`（`updateConfig` 基于库内 `current` 与提交值，env 覆盖只作用于 `GetConfig`）。通过 env 切换目录后，旧 env 目录下的历史记录不可解析。
  3. roots 只增不减、读取时不重新校验：历史上任何被保存过的下载目录永久成为可信根（`/api/library/downloads/{id}/file` 只服务 DB 已记录的路径，暴露面可控，但语义上应明确"历史根永不过期"）。
- 建议：`Open` 时把当前 `DownloadDir` 幂等登记一次；提供从 `downloads.file_path` 的 `dirname` 批量回填的一次性迁移（或至少文档说明升级后需保存一次配置）；加一条按目录不存在就清理的维护逻辑。

### [R4·中] library 缓存重建成本显著上升：全表读路径 + 全量小写映射 + 全目录遍历，且每条下载写入就失效

- 位置：`storage.go:1617-1626`（`SELECT file_path FROM downloads` 无 WHERE、无分批）、`:1645-1649`（`caseVariants` 再复制一份全量路径）、`:1627`（WalkDir）、`:1515`/`:1533`（每次 CreateDownload 失效）
- 影响：100 万行库，单次重建要读全量 file_path（数十 MB）+ 再建一个等规模的 lowercase→[]path map（约 2× 路径内存）+ WalkDir；而归档期间每条媒体写入都会失效缓存，`libraryBuildMu`（`:1565-1582`）只把并发请求合并成一次重建，**没有把重建频率降下来**。表现为归档时画廊刷新延迟/内存峰值上升（修的是正确性，代价是这条路径的常数变大）。
- 建议：`known` 查询加 `WHERE file_path <> ''` 并按目录前缀收窄；`caseVariants` 只在"小写路径确实命中某个已记录路径"时才用到，可改为 `map[lower]string`（单值即可，因为同一小写只可能有一条记录）以省一半内存；失效做时间节流（如最多每 5s 一次）。

### [R5·中] "分页"只在 HTTP 层做内存切片；非分页默认 limit 从 10 万静默变 100

- 位置：`httpapi/server.go:771-789`、`:800-865`

```go
	limit := parseLimit(r, 100, storage.MaxLibraryDownloadsLimit)   // 非分页默认 100（HEAD 是 MaxLibraryDownloadsLimit）
	if paged { limit = storage.MaxLibraryDownloadsLimit }           // 分页仍取 10 万
```

- 影响：
  1. 带 `page/pageSize` 的请求仍让 storage 取 `limit+1 = 100001` 行、做全量 WalkDir/排序后只返回 60 条 → P2-9 的内存/CPU 峰值没有降低（HTTP 响应变小了）。
  2. 旧的外部客户端不传 `limit` 时从"最多 10 万条"变成"最多 100 条"，且非分页响应是裸数组，**没有任何 truncated 标记**，客户端无法察觉被截断（前端已改用分页接口，故内部无影响）。
- 建议：storage 提供真正的 keyset 分页（`created_at,id` 游标 + 只对当前页做 WalkDir/归属），或在非分页响应用 `X-Result-Truncated` 头/包装对象暴露截断。

### [R6·低中] 损坏的归档计划行会让 scheduler 每 2 秒打一条日志，且在 API 里彻底消失（无法删除）

- 位置：`storage.go:2689-2699`（每个坏行一次 `log.Printf`）、`:1375` 被 `manager.go:242` 的 2s ticker 每 tick 调用一次
- 影响：一行坏 `items_json`/`last_job_ids` 会产生约 4.3 万条/天日志；同时坏行被从 `ListArchiveSchedules` 过滤掉，UI 看不到、也删不掉（只能改库）。比"全部停摆"好，但仍需收敛。
- 建议：storage 层做去重日志（按 id 记一次）或返回"坏行 id 列表"由上层决定；API 返回坏行占位（只含 id/name + 错误标记）让用户能删。

### [R7·低] `UserFacingError` 的 `Cause` 永远不会出现在日志里（与注释矛盾）

- 位置：`storage.go:28-38`（注释称 "logging the full Cause server-side"）、`httpapi/server.go:1335-1341`（`log.Printf("httpapi %d: %v", uf.Status, err)`）
- `Error()` 返回 `Message`，`%v` 打的就是 Message，`Cause`（例如 `sql.ErrNoRows`、`os.PathError`）全部丢失；`ErrNotFound("计划不存在", err)` 这类包装后的内部原因无法排查。
- 建议：`log.Printf("httpapi %d: %v: %v", uf.Status, uf.Message, uf.Cause)`，或让 `Error()` 带 Cause 并单独取 Message 给客户端。

### [R8·低] `configMu` 被跨 DB 事务持有；缓存 miss 时还用写锁做一次 DB 读

- 位置：`storage.go:928`（`updateConfig` 全程 `Lock`，其中 `:932` 起是 `BEGIN IMMEDIATE`，最坏等 `busy_timeout(5s)`）、`:888-891`（`GetStoredConfig` 二次检查用 `Lock()` 而非 RLock 就为了做 DB 读）
- 影响：任何配置写（含 `PUT /api/config`）会让所有配置读（几乎每个请求 `GetConfig`）最长阻塞 5s；缓存 1s 过期后，读路径也会短暂串行化。功能正确，但把"低频写"变成了"全局读停顿"。
- 建议：把事务与缓存写入拆分（提交后再短暂持锁写缓存，用 `updated_at`/generation 校验），或配置写用独立连接 + 乐观重试。

### [R9·低] `UpdateConfigPatch` 对未知字段/`null` 静默无操作

- 位置：`storage.go:943-952`（`json.Unmarshal(payload, &cfg)`，`cfg` 基底是 `current`）
- 探针确认：未知键被忽略（200 但无效果）；`{"maxConcurrency": null}` 不报错也不修改。用户/脚本字段名写错时会"保存成功"却什么都没变。类型错误才 400。
- 建议：`DisallowUnknownFields`（用 `json.Decoder` 对 `fields` 逐字段解码），或对 `null` 明确语义（清除 or 400）。

### [R10·低] 单实例锁与 storage 层"多进程安全"的既有口径不一致；`RequeueInterruptedJobs` 本身仍未加固

- 位置：`cmd/server/main.go:69-73`（新增 flock/LockFileEx）vs `storage.go:116-120`、`:426-428`、`:832-841`、`counters.go:29-30` 的"多进程同时启动/迁移"注释，以及 `TestOpenSurvivesConcurrentOpens`
- 影响：`cmd/server` 已经不允许两个进程共用同一 data-dir，但 storage 包其它使用方（测试、将来的 CLI/工具）仍可绕过单实例锁拿到 `RequeueInterruptedJobs` 的无条件重置语义。注释与新策略矛盾会让后续维护者误判。
- 建议：要么在注释里把"多进程"降级为"多连接/多 Store 实例（同进程）"，要么给 jobs 加 `owner_instance`/`heartbeat_at` 后再宣称多进程。

### [R11·低] 魔法串 `automatic-failed-tweets` 跨包重复；自动重试任务无 DB 级唯一约束

- 位置：`storage.go:2509`/`:2512`（INSERT 与 NOT EXISTS 里写死）、`jobs/manager.go:587`（`job.Input != "automatic-failed-tweets"` 决定 `force`）
- 影响：改名即静默破坏"自动重试遵守退避"的语义（`force` 变 true，绕过 `next_retry_at`）。去重只靠两条 `NOT EXISTS`，没有部分唯一索引；当前依赖"单实例 + 单调度协程"，一旦引入并发调度就会重复建任务。
- 建议：`storage` 导出 `AutomaticFailedRetryInput` 常量供 jobs 引用；加 `CREATE UNIQUE INDEX ... ON jobs(kind) WHERE kind='failed_retry' AND input='automatic-failed-tweets' AND status IN (...)`（或用 `INSERT OR IGNORE` + 查询）。

### [R12·低] `next_retry_at` 引入混合时间文本格式，P3"隐式格式不变式"没有消除而是扩大

- 位置：`storage.go:2492` `datetime(?, '+' || CAST(...) || ' minutes')`，写入 `YYYY-MM-DD HH:MM:SS`（无小数、无时区）；同表其它 DATETIME 由驱动绑定 `time.Time`，写入 `...+0000 UTC` 带小数秒。
- 本轮探针结论：`SELECT *` 扫描回 `*time.Time` 正常；`next_retry_at <= ?`（绑定 time.Time）在 UTC 下结果正确（前 19 字符定长，字典序等价，仅同秒内可能提前 ≤1s 视为到期）；但 `julianday(驱动格式)` 返回 0（不可解析）——一旦有人对这类列写 SQL 时间函数就会静默错。
- 建议：统一用 `strftime('%Y-%m-%dT%H:%M:%fZ', ...)` 或改由 Go 计算时间后绑定参数（同时把退避公式移出 SQL，可单测）。

### [R13·低] `failed_tweets.next_retry_at` 无索引，自动重试开启后调度器每 2s 做一次 EXISTS 扫描

- 位置：`storage.go:2504-2522`（`INSERT ... SELECT ... WHERE EXISTS(SELECT 1 FROM failed_tweets WHERE next_retry_at IS NULL OR next_retry_at <= ?)`）、`:2524-2534`（同样的过滤 + `ORDER BY updated_at`）、`addMissingIndexes` 只有 `idx_failed_tweets_updated_at`（`:823-824`）
- 影响：`AutoRetryFailed` 默认开启（`app_config` 默认 1），每次 tick（2s）都会对 `failed_tweets` 做一次全表条件扫描并尝试一条 INSERT（无到期行时不产生写入，但仍是全表扫描）。失败队列通常不大，故低危；若 180 天内累积到万级则明显。
- 建议：`CREATE INDEX idx_failed_tweets_next_retry_at ON failed_tweets(next_retry_at)`（部分索引 `WHERE next_retry_at IS NOT NULL` 更好）。

### [R14·低] 两套路径规范化并存（登记用 `Clean(Abs)`，解析用 `EvalSymlinks`）

- 位置：`storage.go:963-971`（登记 `filepath.Clean(abs)`）vs `httpapi/server.go:1043-1069`（`EvalSymlinks(rootAbs)` 与 `EvalSymlinks(filePath)` 后 `Rel`）
- 影响：macOS `/tmp → /private/tmp`、Windows 8.3 短名、目录内有符号链接时，登记值与解析值不同；当前逻辑先做 `Abs+Rel`（未解析符号链接）再用 EvalSymlinks 复检，**功能上仍安全**，但"roots 里存的是哪种形态"没有文档化，后续加清理/去重逻辑容易出错。
- 建议：roots 统一存 `EvalSymlinks(Clean(Abs(path)))`（失败时退回 Clean(Abs)）并注释说明。

### [R15·低] `Result` 契约里 Skipped 分支仍可能无 ContentHash；`CreateFailedTweet` 的 upsert 不重置退避

- 前者见 §2.4-8。后者：`storage.go:2472-2481` `ON CONFLICT(entity_id, tweet_id) DO UPDATE` 不重置 `retry_count`/`next_retry_at`，因此同一条推文在冷却期内再次失败（甚至是新任务里的第一次失败）会**继承旧退避**，最长 24 小时内自动重试不会碰它（手动"重试失败推文"因 `force=true` 不受影响）。是否符合预期需 jobs/产品确认。

### [R16·低] `storage` 包开始 import `net/http`（持久层耦合传输层状态码）

- 位置：`storage.go:31-47`（`UserFacingError.Status int` 直接存 `http.StatusNotFound`/`StatusBadRequest`）
- 影响：分层倒置的坏味道；`storage` 的其它使用者（测试、工具）也会被拉进 `net/http`。`writeError` 的 `errors.As` 分支还额外带来一个行为变化：**任何** `*os.PathError`/`*url.Error`/`interface{ Code() int }` 现在强制 500（这是想要的），但如果将来有人把 `*UserFacingError` 包在 `*os.PathError` 外层，仍会走 UserFacing 分支返回 4xx。
- 建议：`storage` 定义自己的 `ErrorKind`（NotFound/Validation/Conflict），由 `httpapi` 映射到状态码。

---

## 4. 仍待处理（本轮未修）

按"修复收益/成本"排序，供 Lead 排期：

1. **§3.1-11 状态集合单一来源**（唯一一条完全没动的 §3.1 项）：`counters.go` 5 处 + `storage.go:1125`。建议生成 SQL 片段常量 + 单测断言集合一致。
2. **§3.1-4 迁移的锁时长/退避**：分批已解决内存，但仍是单事务 + 无 `SQLITE_BUSY` 退避；多实例同时升级大库仍可能启动失败（P2-4 的原始场景只解决了一半）。
3. **§3.1-6 `RequeueInterruptedJobs` 的 owner/心跳**：目前靠 `cmd/server` 单实例锁兜底，storage API 语义未变。
4. **§3.1-9 的缓存失效节流**与 **P2-9 的三份切片拷贝**（`cacheLibraryDownloads` 持 `libraryCacheMu` 写锁拷贝、返回值再拷贝）——两者叠加是归档期画廊卡顿的主因。
5. **§4-4 文件名长度常量**、**§4-5 twimg/压缩/海报抓取去重**、**§4-8 ContentHash Skipped 契约**（需 jobs 侧确认）——纯技术债/契约收敛。
6. **P3 遗留**：`PRAGMA optimize`/`ANALYZE`；死表 `user_previous_names`；仅测试使用的 8 个 storage 方法 + 新增死包装 `ListLibraryDownloads`；`addMissingColumns` map 迭代；`CreateDownload` 双段 SQL；`runMigrationOnce` 对已应用迁移取写锁；`failed_media(created_at)`/`unavailable_media(updated_at)`/`failed_tweets(next_retry_at)` 索引；`GetMediaDownloadState` bool 语义；`DashboardMeta` 零值；时间格式 helper。
7. **P2-13 测试缺口**：T2（`dropSMBWebDAVConfigColumns` 零测试）、T3（弱断言）、T4（并发领取原子性）、T5/T6（墙钟/`Sleep` 断言）、T7（迁移测试走自动提交）、T8（已有数据的并发 Open）；另：`TestLibraryRecognizesRecordedPathWithDifferentCase` 在 Linux CI 直接 Skip，等于该修复无 CI 覆盖。
8. **§4-6 残留**：前端 `FailedTweet.payload` 幽灵字段（后端不产出）——可删前端字段或后端补 `payload`。

---

## 5. 结论

- 存储区域的修复**方向正确、落地质量高**：P1-1 的两个根因（缓存不失效、历史路径无解析根）都被结构性解决，且各自有端到端/单元测试；§3.1 中 9/14 完全修复，剩余 4 条多为"内存修了、锁时长没修"这类部分完成；唯一完全未动的是状态集合收敛（纯可维护性风险，无当前功能 bug）。
- 需要优先处理的是 **R1（Windows 上无法启动）**：这是本轮 DSN 改造引入的、当前代码必然触发的平台级回归，且 CI 结构上发现不了。其次 R2（启动校验的升级兼容）与 R3（`download_roots` 的存量/环境变量缺口）决定了 P1-1 的修复在真实升级路径上能否兑现。
- 本轮新增的 12 个测试整体有效（覆盖了缓存失效/跨实例刷新/截断语义/损坏行隔离/大小写/退避任务去重/URI 路径），但"大小写"与"Windows 路径"两类用例在 Linux CI 上会被跳过或根本不存在，建议给 CI 补 `GOOS=windows` 的 smoke 与大小写敏感平台上的替代断言。
