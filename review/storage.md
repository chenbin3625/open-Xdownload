# 代码审查 · internal/storage

审查方式：按硬性约束只读代码，未查看 git 历史 / diff，未阅读 README.md 与 docs/（文档非审查对象）。
所有引用均为 `文件:行号` 形式，代码片段逐字摘自工作区当前版本，可复核。
为验证跨包契约（返回值语义、调用方前提）做了**只读** grep；未修改任何源码/测试/配置，只写本文件。

基线验证：`GOFLAGS=-mod=readonly go test ./internal/storage/ -count=1` → `ok ... 0.857s`（全绿）。本文所有发现均建立在"当前测试全绿"之上，没有被测试暴露的问题。

---

## 0. 阅读清单

| 文件 | 行数 | 已读完 |
|---|---|---|
| `internal/storage/storage.go` | 2505 | 是（1–2505，分 7 段读取，无跳读） |
| `internal/storage/models.go` | 216 | 是（1–216） |
| `internal/storage/counters.go` | 128 | 是（1–128） |
| `internal/storage/storage_test.go` | 1270 | 是（1–1270） |
| `internal/storage/counters_test.go` | 82 | 是（1–82） |
| `internal/storage/migration_test.go` | 484 | 是（1–484） |
| `internal/storage/concurrency_test.go` | 136 | 是（1–136） |
| `internal/storage/resilience_test.go` | 262 | 是（1–262） |
| `internal/storage/media_state_test.go` | 136 | 是（1–136） |
| `internal/storage/archive_schedule_test.go` | 302 | 是（1–302） |

合计 5521 行，全部读毕。

---

## 1. P0 发现（安全漏洞 / 数据丢失 / 必然崩溃）

**本包未发现 P0 级问题。**

已逐项排查的 P0 候选与结论：

1. **SQL 注入面**：全包无用户输入拼进 SQL。唯一的动态 SQL 是
   `storage.go:705` `exec.Exec(`ALTER TABLE app_config DROP COLUMN ` + name)`（name 来自 696–697 行的硬编码白名单）与
   `storage.go:389` `fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)`（常量 int）；
   ORDER BY / IN 子句全部是字面量或 `sqlx.In` 占位符（`storage.go:1251`、`1783`、`1980`、`2086`），不存在注入面。
2. **文件误删**：本包**没有任何删除/移动文件的代码**。`collectOrphanLibraryFiles`（1469–1497）只读遍历并追加记录；`RewriteDownloadPathPrefix`（1654–1673）只改 DB 路径。
3. **必然崩溃**：所有切片/索引访问均有边界保护（`ListLibraryDownloads` 的 `cached[:limit]` 前有 `len(cached) > limit`，1412–1414；`videoThumbImagePath` 对无 `/` 的情况 `slash=-1` 仍安全，1622–1630）；`Rows` 均有 `defer Close()`（966、1071）。
4. **数据损坏**：唯一会 DROP 结构的是迁移，均在 `runMigrationOnce` 事务内（401–424），崩溃即回滚；索引的 DROP+CREATE 也在同一事务内（550–572，有专门回归测试）。

因此 P0 数量：0。

---

## 2. P1 发现（高概率 bug / 竞态 / 资源泄漏 / 逻辑错误）

### [P1-1] `UpdateConfig` 成功后不失效 `libraryDownloadsCache`：改下载目录后，媒体库最长 30 分钟仍返回旧目录记录，点开全部报错

- 位置：`internal/storage/storage.go:825-895`（UpdateConfig 写入路径）、`internal/storage/storage.go:44`（TTL）、`internal/storage/storage.go:1409-1418`（缓存命中）、`internal/storage/storage.go:1555-1563`（失效入口）
- 证据：

```go
// storage.go:890-894 —— 配置写回后只更新了 config 缓存，没有失效 library 缓存
	if err := tx.Commit(); err != nil {
		return config.AppConfig{}, err
	}
	s.rememberStoredConfig(cfg)
	return cfg, nil
```

```go
// storage.go:1409-1418 —— 命中缓存时不重新查库、不重新扫目录
	s.libraryCacheMu.RLock()
	if s.libraryCacheOK && time.Since(s.libraryCacheAt) < libraryDownloadsCacheTTL && s.libraryCacheLimit >= limit {
		cached := s.libraryCache
		...
		return items, nil
	}
```

全包 `invalidateLibraryDownloadsCache` 的调用点只有 4 处（`grep -n "invalidateLibraryDownloadsCache" internal/storage/*.go`）：

```
storage.go:1365  storage.go:1383   // CreateDownload
storage.go:1645                    // UpdateDownloadPreviewURL
storage.go:1670                    // RewriteDownloadPathPrefix
```

`UpdateConfig`（825）不在其中。

- 影响：用户把下载目录从 A 改成 B（或 local → 其他 storage_type）后，`ListLibraryDownloads` 在 30 分钟 TTL 内直接返回 A 目录的记录：
  - 画廊里显示的文件在新根目录之外，httpapi 的 `resolveLocalLibraryPath` 会把它判为越界 → 点击全部 403/404（`internal/httpapi/server.go:909-935` 已经证明了这一点）；
  - 孤儿文件扫描也不会针对新目录执行，新目录文件最长 30 分钟不出现。
  - 这是**单进程、用户可稳定复现**的功能错误，无 workaround（只能等 TTL 或重启）。
- 建议：`UpdateConfig` 提交成功后调用 `s.invalidateLibraryDownloadsCache()`；更彻底的做法是把影响列表内容的配置（`DownloadDir`、`StorageType`）纳入缓存 key，配置变更即天然换 key。

### [P1-2] 进程内 `cachedStored`（配置缓存）没有 TTL、没有跨进程失效：多实例共享同一个 SQLite 时，实例永远用旧配置

- 位置：`internal/storage/storage.go:29-30`（字段）、`internal/storage/storage.go:785-790`（写入缓存）、`internal/storage/storage.go:793-815`（读缓存，命中即返回）
- 证据：

```go
// storage.go:29-30
	configMu     sync.RWMutex
	cachedStored *config.AppConfig

// storage.go:793-800
func (s *Store) GetStoredConfig(ctx context.Context) (config.AppConfig, error) {
	s.configMu.RLock()
	if s.cachedStored != nil {
		cfg := *s.cachedStored
		s.configMu.RUnlock()
		return cfg, nil
	}
```

缓存只有两个写入点：`UpdateConfig`（storage.go:893，仅本进程）与 `Open`→`GetStoredConfig` 预热（storage.go:105-108）。**没有 TTL，也不监听 DB 变化**。

- 前提：多进程共用同一 DB 是"受支持场景"——存储层多处注释/测试显式声明：
  - `storage.go:117-120`（多进程同时启动切换 WAL）、`storage.go:426-428`（多进程同时 ALTER TABLE）、`storage.go:746-747`（多进程同时首启 INSERT OR IGNORE）、`counters.go:29-30`（多进程同时 DROP/CREATE trigger）；
  - `concurrency_test.go:51-78` `TestOpenSurvivesConcurrentOpens` 专测并发打开。
- 影响：实例 A 通过 UI 改了 `download_dir`/`proxy_url`/`auth_token`，实例 B 的内存配置**永久不变**（直到 B 重启或自己再保存一次），继续把新任务写到旧目录、走旧代理、用旧凭据——等于静默的配置漂移；配置里含凭据，排障时极难定位。
- 建议（按成本排序）：
  1. 给缓存加短 TTL（如 5s）或基于 `app_config.updated_at` 的版本校验（每次读缓存前比一次 `updated_at`，一次 PK 查询很便宜）；
  2. 若产品实际只支持单实例，则在 `main.go` 加单实例锁并在文档/注释里把"多进程安全"的表述降级，二者必须一致。
- 交叉验证：见 §5 第 1 条。

### [P1-3] `RequeueInterruptedJobs` 无条件把 `resolving/downloading` 重置为 `pending`：第二个实例启动会抢走第一个实例正在跑的任务

- 位置：`internal/storage/storage.go:1057-1082`；调用点 `cmd/server/main.go:89`
- 证据：

```go
// storage.go:1058-1067 —— 没有任何实例归属/心跳/启动时间过滤
	rows, err := s.db.QueryxContext(ctx, `
	UPDATE jobs SET
		status = ?,
		progress = 0,
		message = ?,
		error = '',
		updated_at = ?
	WHERE status IN (?, ?)
	RETURNING *`,
		JobPending, "上次运行中断，等待恢复", time.Now().UTC(), JobResolving, JobDownloading)
```

```go
// cmd/server/main.go:89 —— 每次进程启动都无条件执行
	if recovered, err := store.RequeueInterruptedJobs(ctx); err != nil {
```

jobs 表没有任何 `owner/instance_id/lease/heartbeat` 列（`storage.go:167-178` 的 DDL 可证），因此无法区分"自己上次崩溃残留"与"另一个活着的实例正在处理"。更糟的是 worker 的终态保存没有 owner 校验：

```go
// storage.go:989-990
WHERE id = :id
  AND (status NOT IN ('completed', 'completed_with_errors', 'failed', 'canceled') OR status = :status)
```

被 A 置回 pending、又被 B 重新 claim 成 resolving 的任务，A 仍然可以成功把它写成 completed。

- 影响（多实例同时启动，或"新进程先起、旧进程尚未退出"的重启重叠窗口——systemd/docker `restart`、双击启动两次都会命中）：
  - 同一任务被两个进程同时执行，媒体锁 `m.lockMedia` 是**进程内**的（`internal/jobs/manager.go:709-717`），跨进程不生效 → 并发重复下载、同目录出现 `photo(1).jpg` 副本、下载记录写入顺序不可控；
  - 进度回退（progress 归零），SSE 上的任务状态来回跳；
  - 归档计划的 `last_job_ids` 仍指向这些任务，`HasActiveJobs` 判定与实际状态不一致。
- 建议：给 jobs 增加 `owner_instance TEXT` / `started_at` / `heartbeat_at`，只 requeue「非本实例 且 心跳超时」的行；退一步的最低成本方案是 `main.go` 启动时加单实例文件锁，并在锁冲突时直接退出而不是继续跑。
- 交叉验证：见 §5 第 1 条。

---

## 3. P2 发现（边界 / 错误处理 / 性能 / 可维护性）

### [P2-1] 孤儿文件"已知集合"是用 **LIMIT 截断后的结果** 构建的：记录数超过 limit 时，已登记文件会被当成孤儿重复返回，top-N 计算错误且错误结果进缓存 30 分钟

- 位置：`internal/storage/storage.go:1424-1426`（SQL 已经 LIMIT）、`internal/storage/storage.go:1447-1453`（known 只由截断后的 items 构建）、`internal/storage/storage.go:1458-1462`（排序截断后缓存）
- 证据：

```go
// storage.go:1423-1429
	items := []DownloadRecord{}
	err := s.db.SelectContext(ctx, &items, `
SELECT d.* FROM downloads d
ORDER BY d.created_at DESC, d.id DESC LIMIT ?`, limit)
```

```go
// storage.go:1447-1453
	known := make(map[string]struct{}, len(items))
	for _, item := range items {
		if path, err := filepath.Abs(item.FilePath); err == nil {
			known[filepath.Clean(path)] = struct{}{}
		}
	}
	items, err = collectOrphanLibraryFiles(ctx, root, known, items)
```

- 触发条件（两条，均为真实场景）：
  1. `downloads` 记录数 > `MaxLibraryDownloadsLimit = 100000`（`storage.go:48`）。注释明确说这个上限正是"为超大规模归档约束响应与缓存体积"，即设计已预期有这种库；
  2. 任何 API 客户端传 `?limit=`（httpapi 会把它透传成 storage 的 limit，`internal/httpapi/server.go:736`），此时所有不在前 `limit` 条记录里的文件都会被当成孤儿。
- 影响：超出窗口的记录被替换成 `ID=0` 的孤儿记录（丢失 `UserScreenName/UserName/MediaURL/PreviewURL/JobID`），并且以文件 mtime 参与 `CreatedAt` 排序（1488），可能把真正该出现在 top-N 的记录挤出结果；该错误结果随后被写进缓存（1462），同一 limit 的请求 30 分钟内都错。
- 建议：`known` 用独立的窄查询构建，不受列表 limit 影响（只需 file_path 一列，必要时分批）：

```sql
SELECT file_path FROM downloads WHERE file_path <> ''
```

  （100 万行也只是几十 MB 的顺序扫描，且可以做"按目录前缀过滤"进一步收窄）；或把缓存改为"全量缓存 + 输出时截断"，让 limit 只影响输出而不影响已知集合。
- 交叉验证：前端固定传 100000（`apps/web/src/lib/api.ts:300`、`apps/web/src/lib/api.ts:250`、`apps/web/src/pages/GalleryPage.tsx:239`），所以普通用户只在 >10 万记录时触发；外部 API 客户端可主动触发。

### [P2-2] 孤儿扫描会把 `.preview-*.jpg` 残留临时文件当成媒体条目展示

- 位置：`internal/storage/storage.go:1477`；命名来源 `internal/downloader/downloader.go:606`、`internal/jobs/preview.go:115`；清理节奏 `internal/jobs/manager.go:59-65`、`internal/jobs/manager.go:335`
- 证据：

```go
// storage.go:1477 —— 只排除了字面量 ".preview.jpg" 后缀
		if !IsLibraryMediaPath(path) || strings.HasSuffix(strings.ToLower(path), ".preview.jpg") {
			return nil
		}
```

```go
// downloader.go:606 —— 真实临时文件命名是 ".preview-*.jpg"（通配符模式）
var staleTempFilePatterns = []string{".xdl-*.part", ".preview-*.jpg"}
// jobs/preview.go:115
	temporary, err := os.CreateTemp(filepath.Dir(destPath), ".preview-*.jpg")
```

`.preview-12345.jpg` 的扩展名是 `.jpg` → `IsLibraryMediaPath` 返回 true（`storage.go:1567-1574`），且不以 `.preview.jpg` 结尾 → 被当成媒体加入画廊。

- 影响：海报（preview）回填过程中进程被杀，会留下 `.preview-*.jpg`；`RemoveStaleTempFiles` 只在后台维护时跑（`maintenanceInterval = 12h`，manager.go:59-60），且要求文件年龄 >1h（manager.go:65）→ 该幽灵条目最长可显示约 13 小时；若期间被写入 library 缓存，再延长 30 分钟。表现为媒体库里出现无法解释、无归属、点开是缩略图的条目。
- 建议：与 downloader 共用同一套临时文件判定（导出 `IsStaleTempFile`/`IsTempArtifact`），或统一排除所有以 `.` 开头的文件名（下载器临时文件全部是隐藏文件）。

### [P2-3] `UpdateConfig` 的「提交 → 写缓存」顺序存在窄窗口竞态：并发保存可能让内存配置停留在较早写入者的值（且无 TTL，永久）

- 位置：`internal/storage/storage.go:890-894`
- 证据：

```go
// storage.go:889-894
	if err := tx.Commit(); err != nil {
		return config.AppConfig{}, err
	}
	s.rememberStoredConfig(cfg)   // ← 提交之后才写缓存，且没有和提交串行化
	return cfg, nil
```

两个并发 `UpdateConfig` 在 `BEGIN IMMEDIATE` 上串行：A 先提交，B 随后读到 A 的值并提交 B。但"A 的 `rememberStoredConfig`"与"B 的整个事务+提交+remember"谁后执行由调度器决定；若 A 的 goroutine 在 commit 返回后被抢占到 B 完成之后，最终 `cachedStored` 是 A 的旧值，而 DB 是 B 的新值。由于 P1-2 的缓存无 TTL，这个偏差会一直持续到下一次配置保存或重启。

- 影响：用户看到"保存成功"但 GET /api/config 与后续任务仍用上一次的值（下载目录、代理、并发等），排查困难。
- 建议：用一个 store 级互斥量把「事务提交 + 更新缓存」包起来；或提交后按 `app_config.updated_at` 条件回填（`UPDATE ... RETURNING` 已在事务里，可直接把事务内读到的最终值作为缓存，并配合 P1-2 的版本校验）。
- 概率说明：需要两个 `UpdateConfig` 真正并发且发生抢占，窗口很窄；但后果是持久化的配置不一致，故列 P2 而非 P1。

### [P2-4] 一次性迁移在单个 `BEGIN IMMEDIATE` 事务里做全表 SELECT + 逐行 UPDATE，并整表读入内存；另一个实例同时 Open 时只有 5s 的 busy_timeout

- 位置：`internal/storage/storage.go:344-349`（迁移登记）、`internal/storage/storage.go:579-598`、`601-620`、`626-648`（三个全表迁移）；`internal/storage/storage.go:54`（busy_timeout(5000)）
- 证据：

```go
// storage.go:601-610 —— rekey 迁移把全表读进内存，再逐行 UPDATE
	rows := []downloadRow{}
	if err := exec.Select(&rows, `SELECT id, media_url, media_key FROM downloads WHERE media_url <> ''`); err != nil {
		return err
	}
	for _, row := range rows {
		key := downloader.MediaIdentity(row.MediaURL)
		if key != row.MediaKey {
			if _, err := exec.Exec(`UPDATE downloads SET media_key = ? WHERE id = ?`, key, row.ID); err != nil {
```

```go
// storage.go:632-637 —— backfill 同样是"全表 SELECT"
	if err := exec.Select(&rows, `
SELECT id, media_url
FROM downloads
WHERE preview_url = '' AND media_url <> ''`); err != nil {
```

这些函数由 `runMigrationOnce`（401–424，`s.db.Beginx()` + `_txlock=immediate`，即事务一开始就持写锁）执行。

- 影响：
  - **内存**：一个 100 万行的库，`rows` 切片 + 每条 2~3 个 string ≈ 数百 MB，在启动阶段一次分配；
  - **持锁时长**：整个扫描 + 逐行 UPDATE 期间独占写锁；此时另一个实例 `Open` 的 `addMissingColumns`/`runMigrationOnce` 会等锁，超过 5s 直接 `SQLITE_BUSY` → `Open` 失败 → `cmd/server/main.go:60` `log.Fatalf("open storage: %v", err)` 退出（"多实例同时升级大库"这一场景会启动失败）；
  - 迁移只跑一次，登记后不再重复，所以这是**升级路径**的一次性风险，不是稳态问题。
- 建议：
  1. 迁移内部分批：`WHERE id > ? ORDER BY id LIMIT 1000`（id 是主键，天然可分页），每批一个事务提交（幂等，重跑安全）；
  2. 只 SELECT 需要的列（已是如此），并考虑用一条 SQL 完成能推到 SQL 侧的计算；
  3. `Open` 对 `SQLITE_BUSY` 做指数退避重试（当前只有 `ensureWALMode` 有重试，121-142）。

### [P2-5] 归档计划领取用 `next_run_at = ?` 的**精确时间等值**做乐观锁，且失败被上层当成"已被领走"静默 continue

- 位置：`internal/storage/storage.go:1278-1297`（claim 条件）、`internal/storage/storage.go:1308-1317`（更新）；上层 `internal/jobs/manager.go:286-289`
- 证据：

```go
// storage.go:1287-1297
	current := ArchiveSchedule{}
	query := `SELECT * FROM archive_schedules WHERE id = ? AND next_run_at = ?`
	if requireEnabled {
		query += ` AND enabled = 1`
	}
	if err := tx.GetContext(ctx, &current, query, snapshot.ID, claimNextRunAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrArchiveScheduleAlreadyClaimed
		}
```

`next_run_at` 是 `DATETIME`（SQLite 里实际以 TEXT 存储），查询是 TEXT 字面等值；而 `ListDueArchiveSchedules`（1216–1229）用的是 `next_run_at <= ?` 范围比较。也就是说"能不能被列出"和"能不能被领取"依赖两种不同的比较语义。

- 影响：只要同一个时间点被序列化成两种不同文本（驱动默认时间格式变化、某处写入了非 UTC location、精度截断差异），`<=` 仍然列出计划，但 `=` 永远不命中 → 每次 tick 都返回 `ErrArchiveScheduleAlreadyClaimed`；manager 对它的处理是**直接 continue 且不记日志**（manager.go:286-289），结果是定时归档静默永久停摆，日志里什么也看不到。
- 建议：不要用时间戳当乐观锁。加一个整数 `claim_version`（领取时 `WHERE version = ?` + `UPDATE ... SET version = version+1`），或用 `last_run_at` 与 `next_run_at` 组合判断；同时把 `ErrArchiveScheduleAlreadyClaimed` 与其他错误分开并至少 `log.Printf` 一次（也便于发现格式回归）。相关实现细节可参考 `claimArchiveScheduleJobs` 内部其实已具备"事务内重读最新行"的能力（1287–1305），可以只把条件换成版本列。

### [P2-6] `ListLibraryDownloads` 把 `GetConfig` 的错误吞掉，并把降级结果（没有孤儿文件）缓存 30 分钟

- 位置：`internal/storage/storage.go:1438-1446`
- 证据：

```go
// storage.go:1438-1446
	cfg, err := s.GetConfig(ctx)
	if err != nil || cfg.StorageType != config.StorageLocal || strings.TrimSpace(cfg.DownloadDir) == "" {
		s.cacheLibraryDownloads(items, limit, generation)
		return append([]DownloadRecord(nil), items...), nil
	}
	root, err := filepath.Abs(cfg.DownloadDir)
	if err != nil {
		return items, nil
	}
```

- 影响：
  - 一次瞬时的 DB/EIO 错误 → 本次结果缺少所有非 DB 文件，且被当作正常结果缓存 30 分钟（错误 500 反而更可取，客户端会重试）；
  - `filepath.Abs` 失败时返回未排序结果且不缓存（轻微不一致）；
  - 两种错误都没有任何日志。
- 建议：`err != nil` 时返回错误（让调用方 500 并重试）或至少不写缓存；`filepath.Abs` 失败同理。

### [P2-7] 孤儿去重是大小写敏感的，而用户归属匹配是大小写不敏感的：大小写不敏感文件系统上同一文件可能被列两次

- 位置：`internal/storage/storage.go:1447-1452`（known 用原始大小写） vs `internal/storage/storage.go:1523`、`1529-1536`（归属匹配 `strings.ToLower`）
- 证据：

```go
// storage.go:1449-1451
		if path, err := filepath.Abs(item.FilePath); err == nil {
			known[filepath.Clean(path)] = struct{}{}
		}
...
// storage.go:1523,1529-1531
		entities[index].Prefix = strings.ToLower(entities[index].Prefix)
...
		pathLower := strings.ToLower(strings.ReplaceAll(items[index].FilePath, `\`, `/`))
		for _, entity := range entities {
			if strings.HasPrefix(pathLower, entity.Prefix) {
```

同文件 1401–1404 的注释已经明确"匹配不区分大小写，以兼容 macOS/Windows 等大小写不敏感文件系统记录的路径"，说明作者预期 DB 里的路径大小写与磁盘实际条目可能不一致；但 known 集合没有做同样的归一化。`collectOrphanLibraryFiles` 用 `filepath.Clean(path)`（WalkDir 返回磁盘真实大小写）去查 known（DB 写入时的大小写）。

- 影响：在大小写不敏感文件系统上（macOS APFS 默认、Windows），若大小写发生漂移（外部改名、备份恢复、用户手工重命名目录的大小写），同一文件会出现两条：一条带 ID 的正常记录 + 一条 `ID=0` 的孤儿记录（无用户归属、FileURL 走 `/api/library/file`）。用户看到重复项，其中一条信息缺失。
- 建议：known 的 key 与归属匹配使用同一套归一化（`strings.ToLower(filepath.Clean(path))`），并在注释里说明这是刻意的平台兼容。

### [P2-8] `attributeLibraryUsers` 全量加载实体 + Go 侧 O(实体数 × 记录数) 前缀匹配；缓存每次下载写入都失效，归档期间会反复触发

- 位置：`internal/storage/storage.go:1501-1539`（匹配）、`internal/storage/storage.go:1365/1383`（每次 CreateDownload 都失效缓存）、`internal/storage/storage.go:1469-1497`（每次 miss 都全目录 WalkDir）
- 证据：

```go
// storage.go:1511-1515 —— 每次 cache miss 都拉全量实体
	err := s.db.SelectContext(ctx, &entities, `
SELECT COALESCE(u.screen_name, '') AS screen_name, COALESCE(u.name, '') AS user_name,
  REPLACE(ue.parent_dir, char(92), '/') || '/' || REPLACE(ue.name, char(92), '/') || '/' AS prefix
FROM user_entities ue JOIN users u ON u.id = ue.user_id
ORDER BY ue.id`)

// storage.go:1525-1537 —— 对每条记录线性扫实体，无匹配时走满全部实体
	for index := range items {
		if items[index].UserScreenName != "" {
			continue
		}
		pathLower := strings.ToLower(...)
		for _, entity := range entities {
			if strings.HasPrefix(pathLower, entity.Prefix) {
```

- 影响：实体 5000、画廊记录 10 万（其中大量孤儿无归属）时，最坏 ~5×10⁸ 次 `HasPrefix` + 字符串 ToLower，单次请求可达数秒到数十秒 CPU；更糟的是 `CreateDownload` 每写一条记录就 `invalidateLibraryDownloadsCache`（1365/1383），归档进行中画廊页面的每次刷新几乎必然 cache miss，于是「全量 SQL + 全量实体 + 全目录 WalkDir + 全量前缀匹配」被重复执行。前端还有一个会重试/刷新的画廊页（`apps/web/src/pages/GalleryPage.tsx:239`），会放大成 IO/CPU 抖动。
- 建议：
  1. 把实体前缀按目录层级组织（map[parentDir] → 前缀列表，或按前缀长度排序后二分；路径分量匹配而不是整串 HasPrefix）；
  2. `attributeLibraryUsers` 只对 `UserScreenName == ""` 的记录做匹配已经是现状，可进一步只对"路径确实落在某实体 parent_dir 下"的少量记录做；
  3. 孤儿 WalkDir 结果与实体前缀可以按目录缓存（目录 mtime 不变就不重扫）；
  4. 缓存失效做节流（例如归档期间最多 N 秒失效一次），或让缓存按 `created_at > cachedAt` 增量追加。

### [P2-9] `ListLibraryDownloads` 默认全量 10 万条，单次请求最多三份切片拷贝 + JSON 序列化

- 位置：`internal/storage/storage.go:1405-1464`（1423 查库、1415/1441/1463 三次拷贝）、`internal/httpapi/server.go:736-746`（序列化 + 拼 FileURL）
- 证据：

```go
// storage.go:1412-1415 —— 拷贝 1
		if len(cached) > limit {
			cached = cached[:limit]
		}
		items := append([]DownloadRecord(nil), cached...)
...
// storage.go:1548 —— 缓存本身再存一份
	s.libraryCache = append([]DownloadRecord(nil), items...)
...
// storage.go:1463 —— 返回再拷一份
	return append([]DownloadRecord(nil), items...), nil
```

前端固定请求 100000（`apps/web/src/lib/api.ts:250`、`300`）。

- 影响：`DownloadRecord` 含多个字符串字段，100k 条约 30–60MB/份；一次请求期间 DB 结果 + 缓存 + 返回值 + httpapi 的 JSON 缓冲叠加，峰值可达 100MB+；并发几个画廊请求就可能触发 OOM/GC 抖动。`cacheLibraryDownloads` 的拷贝在持 `libraryCacheMu` 写锁期间进行，还会阻塞其它读者的缓存命中路径。
- 建议：缓存持有只读切片、返回时不做拷贝（或返回 `[]DownloadRecord` 的只读契约并在文档注明）；更根本的是分页/游标接口；对画廊接口做并发限制。

### [P2-10] `JobFiles` 对每类明细静默截断 5000 行，响应里没有 truncated/total 标记

- 位置：`internal/storage/storage.go:1696`、`internal/storage/storage.go:1715-1739`
- 证据：

```go
// storage.go:1696
const MaxJobFilesPerKind = 5000
...
// storage.go:1715-1739 —— 两个分支各自 LIMIT，外层没有截断标记
	err := s.db.SelectContext(ctx, &rows, `
SELECT * FROM (
	SELECT 1 AS sort_order, 'download' AS record_kind, ...
	LIMIT ?
)
...
```

- 影响：>5000 个媒体的任务，前端按数组长度展示"文件数"（注释 1692–1695 自己说明了这一点），数字会偏小且无任何提示，用户会以为文件丢了；这个截断在 httpapi 层不可见（`jobFilesResponse{Downloads, Failed}` 没有计数）。
- 建议：响应增加 `total`/`truncated`（用一次 `COUNT(*)` 或 `COUNT(*) OVER()`），或在超限时至少打日志/置标志。

### [P2-11] 状态集合在 5 处独立硬编码，新增/调整 `JobStatus` 时计数器与终态保护会静默漂移

- 位置：`internal/storage/models.go:12-20`（常量）与
  `internal/storage/counters.go:50-52`、`55-67`、`69-77`、`103-107`（触发器 + 对账 SQL）、
  `internal/storage/storage.go:990`（UpdateJob 终态集合）、`1002`（CancelJob）、`1031`、`1036`（RetryJob）
- 证据（同一语义 5 种写法）：

```go
// models.go:12-20
	JobPending             JobStatus = "pending"
	JobResolving           JobStatus = "resolving"
	JobDownloading         JobStatus = "downloading"
	JobCompleted           JobStatus = "completed"
	JobCompletedWithErrors JobStatus = "completed_with_errors"
	JobFailed              JobStatus = "failed"
	JobCanceled            JobStatus = "canceled"

// counters.go:50 —— active 集合
		active = active + CASE WHEN NEW.status IN ('pending', 'resolving', 'downloading') THEN 1 ELSE 0 END,
// counters.go:52 —— failed 集合
		failed = failed + CASE WHEN NEW.status IN ('failed', 'completed_with_errors') THEN 1 ELSE 0 END
// storage.go:990 —— 终态集合
  AND (status NOT IN ('completed', 'completed_with_errors', 'failed', 'canceled') OR status = :status)
```

- 影响：新增一个状态（例如 `paused`）而漏改任一处时：DashboardMeta 的 total/active/completed 会算错（启动 Recount 会"自愈"成另一套口径，运行期仍错）；CancelJob/UpdateJob 的终态保护漏项会让终态可被覆盖。现有测试只断言当前行为，不能发现"漏改"。
- 建议：用 Go 常量生成 SQL 片段（`activeStatusesSQL()` / `terminalStatusesSQL()`，带单测断言集合），触发器仍手写但由同一份列表生成迁移文本；或至少加一个测试断言"models.go 的常量集合 == 计数器 SQL 中出现的字面量集合"。

### [P2-12] `hydrateArchiveSchedule` 单行坏 JSON 会让整个计划列表/到期列表失败；上层对 `ListDueArchiveSchedules` 的错误静默 return

- 位置：`internal/storage/storage.go:2451-2461`；上层 `internal/jobs/manager.go:266-272`
- 证据：

```go
// storage.go:2451-2456
func hydrateArchiveSchedule(schedule ArchiveSchedule) (ArchiveSchedule, error) {
	if strings.TrimSpace(schedule.ItemsJSON) != "" {
		if err := json.Unmarshal([]byte(schedule.ItemsJSON), &schedule.Items); err != nil {
			return ArchiveSchedule{}, err
		}
	}
```

```go
// manager.go:266-272
func (m *Manager) enqueueDueArchiveSchedules(ctx context.Context) {
	now := time.Now().UTC()
	schedules, err := m.store.ListDueArchiveSchedules(ctx, now, 10)
	if err != nil {
		return
	}
```

- 影响：一行历史/人工改坏的 `items_json` 会让 `ListArchiveSchedules`（API 500）与 `ListDueArchiveSchedules` 全部失败；manager 侧无声 return（没有日志）→ **所有**定时归档一起停摆，且无从排查。`last_job_ids` 损坏同理。
- 建议：storage 侧对单个坏行"跳过并记录"（返回已成功解析的行 + 聚合错误/日志钩子），或 manager 至少 `log.Printf` 该错误。

### [P2-13] 测试质量问题（可维护性）

以下问题会削弱回归保护，建议随修复一起补测试。

- **[P2-T1] library 缓存完全零覆盖**：`grep -rn "libraryCache\|invalidateLibrary\|cacheLibrary" internal/storage/*_test.go` 无任何结果。本包最复杂的并发组件（generation 原子性、TTL、limit 档位、失效时机）没有一行测试，而它正是 P1-1 / P2-1 / P2-3 / P2-6 的所在。
- **[P2-T2] 唯一会 `DROP COLUMN` 的破坏性迁移零覆盖**：`dropSMBWebDAVConfigColumns`（storage.go:693-711）在全仓库（含测试）中没有任何 `smb_password`/`webdav` 引用，无法验证"旧库带 SMB 列 → 升级后列消失、且可重入"。`schemaColumnsForVersion`（migration_test.go:437-454）只验证终态列集合，不覆盖"从旧 schema 升级"的输入。
- **[P2-T3] 并发编辑计划的断言过弱，测不出它声称的不变量**：`archive_schedule_test.go:200-210` 只断言 `next_run_at ∈ [now-1min, now+(interval+1)min]`。最终 `interval=109`、而 `next_run_at` 是按 `interval=10` 推算的值（now+10min）**同样能通过**（10 ≤ 110、且 > now-1min），即测试注释说的"必须与最终落库的 interval_minutes 自洽"并没有被验证。应改为断言 `abs(next_run_at - (now + interval)) < 容差`。
- **[P2-T4] 归档计划"并发领取只执行一次"的原子性无测试**：`storage_test.go:548-578` 只做顺序二次领取；真正要防的是两个调度器/两个进程同时 `CreateScheduledArchiveJobs`，当前没有任何并发用例覆盖 `claimArchiveScheduleJobs` 的事务边界。
- **[P3-T5] 墙钟断言易偶发失败**：`migration_test.go:219-221` `if elapsed := time.Since(before); elapsed > 100*time.Millisecond` —— 负载高的 CI 上会误报；该断言（fn 不被调用）已由 213 行的 `t.Fatal` 保证，计时是冗余的。
- **[P3-T6] 依赖 `time.Sleep(5ms)` 建立排序**：`storage_test.go:1180`、`1184` —— 时钟分辨率/极端负载下可能翻车；可用显式 backdate（测试其它地方已经在用 `UPDATE ... SET updated_at` 的做法）。
- **[P3-T7] 迁移测试直接对 `*sqlx.DB` 调用（自动提交），与生产路径（单事务）语义不同**：`migration_test.go:47-56`、`139`、`352` 直接传 `store.db`。生产路径的"索引不缺失"语义只由 `resilience_test.go:16-70` 覆盖；这些用例若失败会把测试库留在无索引状态（仅测试影响，但容易误导后续维护者）。
- **[P3-T8] 并发 Open 只测空库首启**：`concurrency_test.go:55-78` 用 4 个 goroutine 打开**全新空库**，没有覆盖"已有数据的库 + 并发 Open/迁移"（即 P2-4 的路径）。
- **[P3-T9] `TestUpdateConfigSurvivesConcurrentWriters` 只断言无错误**（`concurrency_test.go:44-48`），不断言最终配置等于某个写入者的值——对"锁升级不返回 BUSY"这个目标够用，但漏掉了 P2-3 那类缓存一致性问题。

---

## 4. P3 与优化建议

- **死表/未接线功能**：`user_previous_names` 有建表（storage.go:242-249）和触发器写入（storage.go:312-318），但**全仓库没有任何 SELECT / 读取接口**（`grep -rn "user_previous_names" --include=*.go .` 只有建表、触发器、测试列集合）。要么接上"历史用户名"功能，要么删掉触发器避免无意义写入。
- **死代码/多余契约面**（`grep "\.Method("` 在 storage 包外无调用，仅测试使用）：`HasDownload`（2247）、`ListDownloads`（1388）、`ListJobs`（913）、`GetUserLinks`（2221）、`GetList`（2159）、`GetListEntity`（2198）、`FindDownloadsByMediaKey`（1838）、`FindDownloadsByContentHash`（1866，非分页包装）。建议删除或标注为 API 预留，减少误用。
- **`GetMediaDownloadState` 的 bool 容易被误读**：`storage.go:1923-1947` 第二个返回值是 `unavailable`（不是 found）；当前实现（1929-1932 + 1933-1935）是唯一可证的。建议改成命名返回值或结构体 `{Record *DownloadRecord; Unavailable bool}`，减少调用方误判。
- **`CreateDownload` 两段近 40 行的 INSERT ... ON CONFLICT 复制粘贴**：`storage.go:1347-1386`，其中 1373-1379 的缩进已经和 1355-1361 不一致（`file_path`/`bytes`/`created_at` 对齐丢失）；两段必须与 715-720 的两个部分唯一索引严格同步，改一处忘一处就是运行期 SQL 错误。建议抽一个 SQL 构造函数。
- **`addMissingColumns` 用 map 迭代**（storage.go:436-455）：列添加顺序每次运行随机，失败时不可复现；改成有序 slice 更可预测（功能上安全，因为各列独立）。
- **`ListLibraryDownloads(limit<=0)` 语义与同包其它列表方法不一致**（storage.go:1406-1408：`<=0 → 100000`；对比 `ListJobs` 913-916 `<=0 → 100`、`ListFailedTweets` 2283-2285 `<=0 → 200`）。调用方传 0 拿到全量是个陷阱；建议统一为"<=0 用默认值，显式要全量用 Max"。
- **缺索引/全表扫描的维护查询**：`PruneFailedRecords`（storage.go:2364-2385）里 `DELETE FROM failed_media WHERE created_at < ?` 和 `DELETE FROM unavailable_media WHERE updated_at < ?` 都没有可用索引（`addMissingIndexes` 713-735 只建了 `(job_id, created_at)` 和 `failed_tweets(updated_at)`）；`ListFailedMedia`（1966-1973）按 `created_at DESC` 排序也走不到索引。这两张表通常不大，故列 P3；但如果失败媒体会大量堆积，建议补 `failed_media(created_at)`、`unavailable_media(updated_at)`。
- **统计信息**：库中大量复杂查询（`JobFiles` 1715-1739、`ListFailedTweetViewsPage` 2309-2324、`FindDownloadsBy*Page` 1851/1879）依赖 planner 估算；没有 `ANALYZE`/`PRAGMA optimize` 的调用点。建议 `Close()` 前或启动后执行一次 `PRAGMA optimize`（幂等且很便宜）。
- **`runMigrationOnce` 对已应用的迁移也开 `BEGIN IMMEDIATE`**（storage.go:401-424）：每次启动 9 个迁移各取一次写锁再立刻提交。可先无锁 `SELECT` 判断，仅未应用时进事务，减少启动期写锁竞争（对 P2-4 的并发 Open 也有帮助）。
- **时间比较依赖隐式不变式**：`next_run_at <= ?`（1223）、`updated_at ASC`（2287）、`next_run_at = ?`（1288）都建立在"所有写入点都用 `time.Now().UTC()` 且驱动把 time.Time 序列化成固定 TEXT 格式"之上。建议在 DSN 显式固定 `_time_format=sqlite`，并在 Store 里集中一个 `timeToDB()` helper + 注释，避免未来某个调用点传入本地时间让范围查询静默错乱。
- **`DashboardMeta` 在 `sql.ErrNoRows` 时返回零值而非错误**（counters.go:121-123）：计数器行被外部删除/旧库竞态时会静默显示全 0，建议至少记一条日志（启动 `ensureDashboardCounters` 会补行，但需要在运行期发现异常）。
- **`UpdateJob` 不检查 `RowsAffected`**（storage.go:979-992）：终态保护命中时是静默 no-op，调用方无法区分"已保存"与"被拒绝"。对 canceled 语义是预期的，建议返回 `(updated bool, err)` 或在注释中显式声明，避免未来把"任务行不存在"也吞掉。
- **DSN 拼接 `path + sqliteOpenOptions`**（storage.go:78-79）：路径里出现 `?`/`#` 会破坏 DSN（也会把路径切开导致在错误位置建库）。建议用 `url` 显式拼 query，或对路径做 `file:` URI 转义。
- **计数器相关重复**：`dashboard_counters` 的 `active/failed` 集合与 `RecountDashboardCounters`（counters.go:100-110）的集合必须与触发器逐字一致，目前靠人肉维护；见 P2-11。

---

## 5. 待确认 / 需 Lead 交叉验证

1. **多进程共用同一 SQLite 是否是受支持场景？**（决定 P1-2 / P1-3 的严重度，也影响 P2-4 的定级）
   证据：存储层在多处注释与测试中明确声明多进程安全（storage.go:117-120、426-428、746-747；counters.go:29-30；concurrency_test.go:51-78），但 `main.go` 没有单实例锁（全仓库 `grep -rn "flock\|LockFile\|single-instance" --include=*.go .` 无结果）。二者必须统一口径。
2. **P2-1 的触发规模**：前端固定 `limit=100000`（`apps/web/src/lib/api.ts:250/300`、`apps/web/src/pages/GalleryPage.tsx:239`），后端上限也是 100000（storage.go:48）。请确认 >10 万条记录的归档是否是目标场景；若是，P2-1 应升到 P1。
3. **`config.AppConfig.Normalized()` 无条件把 `MaxConcurrency` 压成 1**：`internal/config/config.go:113` `cfg.MaxConcurrency = 1`；配合 `internal/jobs/manager.go:313-322` `availableSlots` 硬编码 `limit = 1`，`max_concurrency` 实际永远为 1，且 `UpdateConfig` 会把它写回 1。请 config/并发方向的 reviewer 确认这是"串行归档"的有意设计还是遗留 bug；若是前者，UI 仍暴露该配置会误导用户。
4. **jobs 包的两处静默失败**（建议转给 jobs 负责人）：
   - `manager.go:266-272` 对 `ListDueArchiveSchedules` 错误直接 return、无日志（与 storage 的 P2-12 叠加时会让定时归档静默停摆）；
   - `manager.go:286-289` 对 `ErrArchiveScheduleAlreadyClaimed` 直接 continue、无日志（P2-5 的格式回归会在这里失去最后一道可观测性）。
5. **重命名流程的原子性**（jobs 包）：`manager.go:1445-1463` 先 `target.Rename` 再 `RewriteDownloadPathPrefix`；后者失败时目录已改名但 DB 仍指旧路径，且下一次 `LocateUserEntity` 返回旧名、`Rename` 失败后走"沿用旧名"分支（manager.go:1456-1462）→ 长期可能出现新旧两个目录并存、文件分裂。请 jobs 负责人评估；storage 侧 `RewriteDownloadPathPrefix` 本身是单条 UPDATE，原子性没有问题。
6. **`.preview` 命名约定跨包耦合**：storage 排除的是字面量 `.preview.jpg`（1477），downloader 的临时文件是 `.preview-*.jpg`（downloader.go:606），`jobs/preview.go:115` 用 `os.CreateTemp(..., ".preview-*.jpg")` 生成。建议把"临时文件/产物文件"的判定收敛到 downloader 包的一个导出函数，storage 直接复用（对应 P2-2）。
7. **`/api/library/file` 的暴露面**（httpapi）：storage 的孤儿扫描会把 `DownloadDir` 下**任意深度**的所有媒体扩展文件放进画廊（1469-1497），httpapi 的 `serveLibraryFile`（server.go:952-977）只靠 `IsLibraryMediaPath` + `resolveLocalLibraryPath` 沙箱。若用户把 DownloadDir 配到主目录（`validateDownloadDir` 是否允许取决于 httpapi 的 allowed-roots 规则），画廊就等于"媒体文件列举器"。请 httpapi reviewer 确认是否有意接受这个面。
8. **批量插入参数上限**：`insertJobs`（storage.go:1105-1129）每行 7 个绑定参数。当前两个调用方上限都是 200（httpapi:347 "一次最多创建 200 个任务"；archive schedule `MaxArchiveScheduleItems = 200`，storage.go:73），即 1400 参数，安全。若将来放宽批量上限会撞 SQLite 默认 `SQLITE_MAX_VARIABLE_NUMBER=32766`，请确认没有其它调用方。
9. **`unavailable_media` 的自愈窗口**：storage 侧只负责老化（2364-2385），写入判断在 jobs 包（`isPermanentlyUnavailableMediaError`）。保留期由 `failedRecordRetention = 180 天` 决定（manager.go:57-58）→ 一次误判会让媒体被跳过 180 天。是否可接受需产品确认（与 storage 的 Prune 设计相关，故列此）。

---

## 6. 设计上值得肯定之处

- **`BEGIN IMMEDIATE` 的选择有充分论证**：`storage.go:50-54` 的注释把"默认 DEFERRED 下锁升级不走 busy_timeout"这个 SQLite 细节讲清楚了，并且 `concurrency_test.go:15-49` 有对应回归测试——这是很多 Go+SQLite 项目踩过的坑，这里处理得比平均水平好。
- **迁移框架正确**：`runMigrationOnce`（401-424）把"数据变更 + 登记"放进同一事务，崩溃即整体回滚，下次启动可重试；迁移拆名（normalize / deduplicate / deduplicate_media_url / backfill_preview / rederive_preview / backfill_media_key / rekey_formats / drop_smb / drop_unique）粒度细、命名可读、各自幂等。`normalizeDownloadsMediaURL` 里"DROP 唯一索引必须在同一事务内重建"的推理（549-572）与专门的回归测试（resilience_test.go:16-70）非常到位。
- **schema 版本闸门**：`schemaVersion` + `ErrSchemaTooNew`（56-68、368-378）+ `migration_test.go:437-484` 用"列集合快照"倒逼"结构变更必须递增版本"，并配 `TestOpenRefusesNewerSchema`（resilience_test.go:124-160）。这是对"降级镜像打开新库导致 SELECT * 全炸"的正面防御，设计意图清楚。
- **部分唯一索引 + 字面量谓词**：1801-1804、1904-1921 明确解释了"绑定参数无法让 SQLite 证明部分索引谓词"，并用 `TestDownloadLookupsUseUniqueIndexes`（concurrency_test.go:84-136）钉住查询计划（连 `SCAN d` 别名都考虑到了）。既有正确性又有可维护性。
- **物化计数器 + 触发器 + 启动对账**：counters.go 的整体设计（DashboardMeta 只读单行 PK，触发器维护，启动 Recount 兜底自愈）兼顾了读性能与可恢复性，`TestOpenRepairsDriftedDashboardCounters`（resilience_test.go:74-120）覆盖了"篡改后重开自愈"。
- **孤儿文件同步**：`collectOrphanLibraryFiles`（1466-1497）把 DB 记录与磁盘文件合并展示，且遍历全程响应 `ctx` 取消（有 `TestCollectOrphanLibraryFilesStopsWhenCanceled` 覆盖），注释也说明了"取消的结果不能进缓存"（1454-1457）——考虑周到。
- **路径处理的细节**：用户归属从 SQL `LIKE` 改为 Go 侧前缀匹配（1400-1404 解释了 `_`/`%` 是通配符的问题）；`RewriteDownloadPathPrefix` 用 `substr` + `utf8.RuneCountInString` 处理中文/emoji 显示名（1650-1673），并有专门的多字节测试（storage_test.go:1076-1106）。
- **自愈型保留策略**：`PruneFailedRecords` 对 `unavailable_media` 按 `updated_at` 老化（2358-2364 注释 + resilience_test.go:211-262），让"误判永久不可用"有自动恢复路径，而不是一错到底。
- **注释质量**：大量注释解释"为什么这样做"（例如 50-54、116-120、326-328、399-400、1400-1404、1692-1695），对后续维护者非常友好；本报告中多条"待确认"都是由这些注释直接暴露出来的设计假设。
