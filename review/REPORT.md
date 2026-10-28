# open-Xdownload 全量代码审查报告

- 仓库：`/Volumes/SSD 1/code/open-Xdownload`（v0.8.6，Go 1.26 后端 + React 19/TS 前端）
- 审查方式：**全量逐行阅读源码**，不看 git 历史/diff，不读 README/docs
- 审查日期：2026-10-28
- 审查组织：7 个互不重叠的区域 reviewer 并行精读 + Lead 交叉复核与汇总；每个区域的详细报告见文末附录
- 审查结论：**无远程 RCE / SQL 注入 / 路径穿越 / XSS / SSRF 类 P0；发现 1 个凭据数据损坏级 P0、11 个 P1、约 55 个 P2、若干 P3**。整体工程质量明显高于同类个人项目（迁移/并发/路径沙箱/摘要压缩/SSE 生命周期等设计扎实），主要问题集中在**配置与凭据的边界语义、错误处理、多实例假设、以及前端几十处状态/可访问性细节**。

> 只读承诺：审查过程未在任何源码中保留修改（期间发现子代理误改了 5 个 Go 文件，已全部还原；当前 `git status` 仅新增 `review/` 报告目录，工作区其余部分与审查开始时一致）。

---

## 0. 审查范围与基线

### 0.1 覆盖范围（全量逐行）

| 部分 | 文件数 | 行数 | 说明 |
|---|---:|---:|---|
| Go 后端（`cmd/` + `internal/`） | 56 个 `.go`（含 26 个测试文件） | 20,664 | 全部逐行读完 |
| 前端 `apps/web/src` | 74 个 `.ts/.tsx/.css`（含 20 个测试文件） | 11,830 | 全部逐行读完 |
| 构建/运维/配置 | Dockerfile、entrypoint、2 个脚本、3 个 workflow、dependabot、go.mod/go.sum、package.json ×2、tsconfig ×2、vite.config、index.html、internal/webui/dist 清单 | ~1,300 | 全部逐行读完（压缩产物只做清单与解压比对） |

总计约 **3.4 万行**代码与配置被逐行阅读（不含 `package-lock.json`、`docs/`、README、二进制产物）。

### 0.2 工具基线（全部在还原后的干净工作区执行）

```
go build ./...                        通过
go vet ./...                          通过（无告警）
gofmt -l .                            无输出
go mod verify                         all modules verified
go test ./...                         全部 10 个包通过
go test -race ./...                   全部通过（无数据竞争）
tsc --noEmit -p tsconfig.json         通过
vitest run                            19 文件 / 179 用例全部通过
vite build                            通过，产物 hash 与仓库内 dist / internal/webui/dist 一致
npm ci                                与 package-lock.json 完全一致
```

覆盖率（`go test -cover`）：config 87.5% / parser 80.0% / downloader 78.3% / storage 70.3% / httpapi 58.6% / jobs 58.2% / xclient 58.2% / cmd 56.1% / filestore 48.3% / httpx 41.6%。

### 0.3 已专项排除、不再重复报告的 P0 面

为避免"没找到=没查"，以下高危面均已逐项验证并给出反证（详见各区域报告）：

- **SQL 注入**：全包参数化；唯一动态 SQL 是硬编码白名单 `DROP COLUMN` 与常量 PRAGMA。
- **路径穿越 / 符号链接逃逸**：`resolveLocalLibraryPath` 用 `filepath.Rel` + `EvalSymlinks` 双重校验；`local-directories` 用"已存在前缀解析"校验；静态层 `path.Clean` + `http.Dir` 双层防护；测试覆盖 `..`/symlink/前缀型（`/data` vs `/data-evil`）。
- **XSS**：前端无 `dangerouslySetInnerHTML`/`innerHTML`/`eval`；bootstrap 注入用 `json.Encoder` + `SetEscapeHTML(true)`，`</script>` 无法逃逸；`Redacted()` 保证明文凭据不进 HTML。
- **SSRF**：`httpx` 拨号层拒绝链路本地地址且按解析后 IP 拨号（防 DNS rebinding）；海报回源与媒体下载均有 twimg 主机白名单 + 重定向白名单 + 大小上限。
- **CSRF / DNS rebinding**：`guardCrossOrigin` 三层校验（Origin / Sec-Fetch-Site / Host 白名单）+ JSON Content-Type 强制。
- **EventBus/锁**：无 send-on-closed-channel、无死锁/泄漏可达路径。
- **任意文件写入**：下载器文件名清洗 + 发布路径受限；无 `..` 逃逸。

---

## 1. P0 发现（1 条）

### [P0-1] 前端丢弃备用 Cookie 的稳定 `id`，多账号删除行后保存会把**被删账号的凭据写回保留行**，静默丢失另一个账号的 Cookie

- **位置**：
  - 前端：`apps/web/src/components/settings/CookieSettings.tsx:504-522`（解析后端 JSON 时丢弃 `record.id`）、`:562-575`（提交时统一序列化成无 id 的文本行）、`:323-358`（编辑/增删都走同一序列化）
  - 后端：`internal/config/config.go:165-188`（`RedactAdditionalCookies` 明确"保留稳定 id，删除行不会导致凭据错位"）、`:190-235`（`RestoreAdditionalCookies` 优先按 id 还原，缺失时按**行号索引**兜底）
  - 调用链：`internal/storage/storage.go:840-847`（`UpdateConfig` 是真正落库的合并点）
- **证据**（前端把后端提供的 id 扔了）：

```tsx
// CookieSettings.tsx:510-518
return parsed.flatMap((item) => {
  const authToken = firstString(record.authToken, record.auth_token);
  const csrfToken = firstString(record.csrfToken, record.ct0);
  return authToken || csrfToken ? [{ ...emptyBackupCookieRow(), authToken, csrfToken }] : [];
});
```

```tsx
// CookieSettings.tsx:562-574 —— 提交永远是 "auth_token=...; ct0=..." 文本行，没有 id
return rows
  .map((row) => ({ authToken: row.authToken.trim(), csrfToken: row.csrfToken.trim() }))
  .map((row) => [`auth_token=${row.authToken}`, `ct0=${row.csrfToken}`].filter(Boolean).join("; "))
  .join("\n");
```

```go
// config.go:204-217 —— 没有 id 时按位置兜底：提交的第 0 行 = 库里的 cookie-0
storedByID := make(map[string]AuthCookie, len(storedPairs))
for _, pair := range storedPairs { storedByID[pair.ID] = pair }
...
storedPair, hasStored := storedByID[sub.ID]
if !hasStored && i < len(storedPairs) && sub.ID == fmt.Sprintf("cookie-%d", i) {
    storedPair = storedPairs[i]   // ← 位置匹配
    hasStored = true
}
```

- **可复现推演**（后端已有单测 `config_test.go:136-143` 证明"保留 id 时删除首行能正确保留 tok2"，但前端从不保留 id）：
  1. 库内：`[{"id":"cookie-0","authToken":"AUTH_A"},{"id":"cookie-1","authToken":"AUTH_B"}]`；GET `/api/config` 返回两者均为 `********` 且带 id。
  2. 前端渲染 2 行（id 被替换为 `backup-cookie-N`），用户删除第 1 行（想删 A、保留 B）并保存。
  3. 提交只剩 1 行文本 → `ParseCookiePairs` 得到 1 对、`withCookieIDs` 按索引赋 `cookie-0` → 后端按 id 命中 `cookie-0` → 还原出 **AUTH_A**。
  4. 结果：想保留的 **AUTH_B 被永久覆盖丢失**，库里留下的是用户想删除的 AUTH_A；界面全脱敏，用户无法察觉。
- **影响**：多账号 Cookie 池（项目主推功能）在"删除任意非末行 / 调整行序"后发生凭据错配与丢失。两个账号的请求会串号、被删账号的凭据反而留在系统里；凭据只能回浏览器重新复制，属于**不可逆的数据损坏**。
- **修复建议**：
  1. 前端 `BackupCookieRow` 增加 `id` 字段，解析时保留 `record.id`（新增行生成本地唯一 id）；
  2. `normalizeBackupCookieRows` 改为输出 JSON 数组 `[{id, authToken, csrfToken}]`（未改动字段提交 `********` 或空串，后端两者都按"还原"处理）；
  3. 后端 `RestoreAdditionalCookies` 对"提交值仍是 `********` 且没有匹配到已存 id"的情况**拒绝落库并返回 400**（见 P1-4），不再做位置兜底；
  4. 补"删除中间行/末行/重排序"的纯函数回归测试。

---

## 2. P1 发现（11 条）

### [P1-1] 修改"下载目录"后，全部历史归档媒体在 HTTP 层变成 403（媒体库整体不可用），且 library 缓存 30 分钟不失效

- **位置**：`internal/httpapi/server.go:117-146`（`updateConfig` 无任何路径迁移）、`:1002-1006`（`serveDownloadFile` 用**当前** `DownloadDir` 解析**历史绝对路径**）、`:909-946`（`resolveLocalLibraryPath` + 403 映射）；`internal/storage/storage.go:44`（缓存 TTL 30 分钟）、`:825-895`（`UpdateConfig` 成功后只更新配置缓存）、`:1409-1418`（缓存命中直接返回）、`:1555-1563`（失效入口，全文仅 4 个调用点且不含 `UpdateConfig`）。
- **证据**：`downloads.file_path` 写入的是下载时的绝对路径（`jobs/manager.go:791-798`），改配置不会重写；`resolveLocalLibraryPath(cfg.DownloadDir, record.FilePath)` 对旧目录文件返回 `errFileOutsideDownloadDir` → `writeResolveError` 映射 **403**。前端 Gallery 对 `id>0` 记录正是用该端点（`GalleryPage.tsx:51`）。
- **影响**：UI 明确提供"下载目录"修改（`DownloadSettings` + `LocalDirectoryPicker`）。用户只要改一次目录：媒体库所有历史记录裂图/无法播放且**永久** 403（不是 30 分钟，缓存只是加剧"新目录孤儿文件不出现"）；正在运行的归档还会横跨两个目录写记录。目录改名场景有 `RewriteDownloadPathPrefix` 迁移，下载目录变更这条路径漏掉了。
- **修复建议**：`updateConfig` 检测 `DownloadDir` 变化时在同一事务内复用 `RewriteDownloadPathPrefix` 重写旧前缀（或为记录保存"归档根"快照）；`UpdateConfig` 提交后调用 `invalidateLibraryDownloadsCache()`；对"记录存在但越界"返回 404/409 + 可读原因而不是静默 403。

### [P1-2] 监听端口/TLS 加载/HTTP3 任一失败都以退出码 0 结束；HTTP/3 故障还会把正常的 TCP 服务一起关掉

- **位置**：`cmd/server/main.go:114-131`（两个 goroutine 出错只 `log + stop()`）、`:133-147`（`main` 正常返回，从不 `os.Exit`）、`cmd/server/http3.go:16-24`。
- **证据**：`stop()` 只是取消 `signal.NotifyContext` 的根 ctx，主 goroutine 于是走正常 Shutdown 流程并返回 → 进程退出码 0。
- **影响**：
  1. `-addr` 写错（缺端口）、端口被占用、证书文件不存在/损坏等启动失败全部"成功退出"，`docker --restart=on-failure` 不会重启、systemd/脚本判成功；"listening on …"日志还打印在 bind 之前，进一步误导。
  2. HTTP/3 是可选增强（同一 handler 已在 TCP 上工作）。UDP 端口冲突、宿主网络不支持 QUIC 等会让整个服务（含 TCP）一起退出，且退出码仍为 0。
- **修复建议**：用 `errgroup`/`errCh` 汇合两个 server 的 fatal error 与 `ctx.Done()`，非 `ErrServerClosed` 时打日志并 `os.Exit(1)`；h3 失败默认只告警并撤下 Alt-Svc（或提供显式 fail-fast 开关）；把 "listening" 日志移到实际 bind 之后。

### [P1-3] 4xx 响应把存储/OS 层内部错误原文回传客户端，并把"不存在"误报为 400

- **位置**：`internal/httpapi/server.go:1166-1175`（`writeError` 对 4xx 原样 `err.Error()`）、`:405-425`（`updateArchiveSchedule` 把 storage 任何错误映射 400）、`:390-403`、`:441-453`、`:148-169`（列目录把 `os` 错误连绝对路径一起回传）。
- **证据**：`PUT /api/archive-schedules/{不存在id}` 返回 `400 {"error":"sql: no rows in result set"}`；SQLite 忙/锁错误会原样出现；列目录返回 `open /Users/xxx/...: permission denied`。
- **影响**：服务无鉴权，任何可访问端口的客户端都能借报错推断内部结构与绝对路径；同时"实体不存在"与"参数非法"无法区分（`getJob/cancelJob/retryJob` 都正确映射 404，这里不一致）。
- **修复建议**：存储层定义 typed error（`ErrValidation/ErrNotFound/ErrConflict`），httpapi 只对 Validation/Conflict 回传文案，NotFound→404，其余→500 脱敏；列目录 OS 错误改固定文案 + 服务端日志。

### [P1-4] 占位符 `********` 可能被当成真实凭据写库（两条独立路径）

- **位置 A**：`internal/config/config.go:204-228` —— 提交的 cookie 对带有"库中不存在的 id"时，`hasStored=false`，`auth/csrf` 保持提交值 `********`，随后因非空被 append 进 merged 并写库。
- **位置 B**：`internal/config/config.go:172-178`（库内 additionalCookies 是"非空但解析不出"时 `Redact` 返回整体 `********`）+ `apps/web/src/components/settings/CookieSettings.tsx:463-465`（前端把整体 `********` 展开成一行 `auth_token=********; ct0=********` 再提交）→ `RestoreAdditionalCookies` 找不到已存对 → 字面量落库。
- **影响**：真实凭据被 `********` 覆盖（不可逆），之后 `ParseCookiePairs` 把 `********` 当真实值发给 X → 该账号全部鉴权失败；用户只看到"突然登录失效"，没有任何错误提示。
- **修复建议**：合并结束后只要仍有字段等于 `SecretPlaceholder` 就丢弃该对或返回 400，**绝不落库**；同时配合 P0-1 改为稳定 id；给"未知 id + 占位符"和"整体占位符"各补一条回归测试。

### [P1-5] 轮询响应会按旧快照重建任务列表，抹掉 SSE 刚插入的新任务；特定时序下永久缺行

- **位置**：`apps/web/src/App.tsx:81-97`（`queryFn` 调用 `mergeNewerJobs`）、`apps/web/src/lib/useDashboardEvents.ts:89-101`（`mergeNewerJobs` 的结果只由 `fetched.items` 构成）、`:149-164`（SSE `jobs.created` 用 `prependJobsToCaches` 写入缓存）、`:219-239`（`patchDashboardJobCaches` 找不到行时只刷 meta）。
- **证据**：轮询请求在 T0 发出（快照不含新任务）→ T1 SSE 插入新任务 → T2 旧快照返回并整体替换 items，新行消失。若此时快照中最后一个 active 任务刚好结束，jobs 轮询（`App.tsx:91-96` 仅当本页有 active 才 5s 轮询）停止，后续 `job.updated` 因 `found=false` 不再重拉 jobs（`refetchOnWindowFocus:false`），该行**长期缺失**直到手动刷新/切页/SSE 重连。
- **影响**：用户可见"新任务闪现后消失"或"统计有、表格没有"；批量创建（定时归档触发）+ 批量完成时最容易命中。
- **修复建议**：`queryFn` 记录请求开始时间，merge 时保留缓存中"不在 fetched 内且 `updatedAt >= fetchStartedAt`"的行；或 `jobs.created` 后 debounce 一次 `invalidateQueries(jobsQueryRoot)` 自愈；轮询条件改为 `meta.stats.active > 0` 而非只看当前页。

### [P1-6] 无法从设置页清除已保存的主账号 Cookie（空串被后端解释为"保持原值"）

- **位置**：`apps/web/src/components/settings/CookieSettings.tsx:112-140`（唯一"清除"方式是把输入框删空）、`internal/storage/storage.go:841-846`（`auth_token/csrf_token` 为空串或占位符时恢复旧值）。
- **影响**：换号、吊销泄露 Cookie、停用主账号都无法通过 UI 完成；删空保存后 GET 又回显 `********`，状态标签仍显示"有效"，用户会误以为已清空。
- **修复建议**：前端提供显式"清除"动作（提交独立字段/哨兵，后端支持 DELETE 语义）；或在 UI 明示"空=保留"并配独立清除入口；补"清空后 GET 为空"的回归测试。

### [P1-7] 批量归档中任一用户的目录/链接错误会中止整个任务，已完成的统计被丢弃

- **位置**：`internal/jobs/manager.go:1084-1090`（`archiveUsers` 对单用户错误直接 `return stats, err`）、`:1160-1166`（`archiveUser` 把本地 FS/DB 错误当任务级错误）、`:1443-1448`（`ensureUserEntity` 上抛 `MkdirAll`/`refreshUserLinks` 错误）、`:1492-1494`（单条 `syncLink` 错误上抛）。
- **证据**：`syncLink` 遇到链接路径已是真实目录时返回错误（`manager_test.go:835-852` 明确断言这是预期行为），该错误一路冒泡；用户目录创建失败（只读/磁盘满/路径过长）同理。
- **影响**：list/following 任务可能有成百上千用户，第一个异常用户就让整个任务 `failed`，后续用户不再处理、已下载统计不落库。对比同文件时间线读取失败只记 Issue 继续（`manager.go:1201-1206`），说明"单用户错误降级"才是本意。
- **修复建议**：`archiveUsers` 按错误类别处理——取消/全限流/DB 不可用继续上抛，其余记 `Failed/Issues` 后 `continue`。

### [P1-8] 默认配置下每个 user/list/following 任务都会全量重跑全局失败队列：重试放大 + 阻塞唯一的串行槽位

- **位置**：`internal/jobs/manager.go:542/628/677`（三类任务收尾都调 `retryFailedTweets(..., false)`）、`:1324-1337`（只靠 `TryLock` 防并发、无时间/次数退避，`ListFailedTweets` 取全局最近 200 条）、`internal/config/config.go:98-107`（`AutoRetryFailed` 默认 true）。
- **影响**：失败条目 ≤200 时，**每一个**新归档任务都会把全部失败条目重下一遍；持续失败的条目没有退避，失败多久就每任务重试多久（直到 180 天清理）。由于槽位恒为 1（见 P2-1），自动重试发生在归档任务内部、完成保存之前，单个媒体超时可达 10 分钟，会把当前任务无限拖长、其他任务干等；重试成功的下载还会归因到当前任务（`DownloadRecord.JobID`，见 P2-5）。
- **修复建议**：把失败重试收敛为独立定时任务（`JobKindFailedRetry`），给 `failed_tweets` 增加 `next_retry_at/attempts` 指数退避；或在自动路径限制本次重试条数并跳过最近 N 分钟的条目。

### [P1-9] List / ListMembers / Following / Follow 只走 Primary Cookie，绕过 Pool 的故障转移与 429 交还

- **位置**：`internal/jobs/manager.go:588-589/610/642/659/1180`（`pool.Primary()` 后直接调 `*Client` 方法）、`internal/xclient/client.go:492/668/705/742`（这些方法内部直接 `c.graphQL`）、`:766-830`（换 Cookie 与"429 立即交还"只实现在 `Pool.graphQL`）。
- **影响**：主 Cookie 被 401/403 标记禁用后，列表/关注任务立即失败，即使备用 Cookie 完全可用；主 Cookie 遇到 429 时会在 `limiter.before()` 里阻塞到复位（上限 15 分钟）而不是换 Cookie。同一任务里"解析用户走 Pool、取关注列表走 Primary"的行为不一致，排障困难。
- **修复建议**：为 Pool 增加 `GetListByID/GetListMembers/GetFollowing` 包装（`p.Select` + `p.graphQL`），jobs 改调 Pool 版本；至少让这些方法在 `ErrClientDisabled`/429 时返回可重试信号。

### [P1-10] 深色 +「钛金」主题下所有 primary 按钮白底白字（选择即必现）

- **位置**：`apps/web/src/index.css:193-206`（`.dark[data-accent="titanium"]` 把 `--color-brand-500` 设为 `#f4f4f5`、`--color-brand-600` 设为 `#ffffff`）、`apps/web/src/components/ui/Button.tsx:25`（primary 固定 `text-white` + brand-500→600 渐变）。
- **影响**：主题是设置页的一等功能，5 套强调色之一"钛金"在暗色下把 primary 按钮变成白底白字：保存配置、检测 Cookie、选择目录、创建目录全部不可读/无法确认可点。`text-brand-500` 作为强调文字色在浅色 + amber 下对比度也只有约 2.2:1（P2-12）。
- **修复建议**：引入成对的 `--color-brand-contrast`，5×2 主题组合都定义为可读色，组件禁止硬编码 `text-white`；对主题矩阵加视觉回归/对比度检查。

### [P1-11] 设置页关键表单控件系统性缺少 label 关联与可访问名称

- **位置**：`apps/web/src/components/settings/DownloadSettings.tsx:59-105`（网络代理/最大文件名长度/命名策略）、`LocalDirectoryPicker.tsx:127-133`（下载目录）、`ConfigForm.tsx:245,271`（用未关联的 `<label>` 包按钮组，无 `aria-pressed`/`radiogroup`）。组件本身已写明正确用法（`Input.tsx:229-233`：不传 `htmlFor` 只能渲染 span），但所有调用方都没传。
- **影响**：读屏用户听到的是"编辑框/数字编辑框/组合框"而没有名称，无法区分关键设置项；主题选择按钮无法播报当前选中。WCAG 1.3.1 / 2.5.3 / 4.1.2 不通过。
- **修复建议**：`Field` 增加受控约定（注入 `id`/`aria-describedby`）或要求调用方传 `htmlFor` + 控件 `id`；`Select/Checkbox/Switch` 补 `id/aria-label/disabled`；主题改用 `Segmented` + `ariaLabel`；`TipIcon` 改为可聚焦 button。

---

## 3. P2 发现（按主题归纳，原始 84 条去重后约 55 条）

### 3.1 存储 / 迁移 / 缓存（`internal/storage`）

- **`ListLibraryDownloads` 的"已知集合"由 LIMIT 截断后的结果构建**（`storage.go:1424-1453`）：记录数超过 limit（上限 10 万）或 API 客户端传小 `limit` 时，已登记文件会被当孤儿重复返回、丢失用户/媒体字段，错误结果进缓存 30 分钟。建议用独立窄查询构建 `known`。
- **孤儿扫描把 `.preview-*.jpg` 残留临时文件当媒体**（`storage.go:1477` 只排除字面量 `.preview.jpg`；实际临时名是 `.preview-*.jpg`，`downloader.go:606`、`jobs/preview.go:115`）：进程被杀后画廊出现无法解释的幽灵条目（最长约 13 小时）。
- **定时归档计划领取用 `next_run_at = ?` 精确时间等值做乐观锁**（`storage.go:1288-1297`），而列出用 `<=` 范围比较；格式/时区/精度变化会导致"永远已领取"，上层 `manager.go:286-289` 静默 `continue` 不记日志 → 定时归档永久停摆且无从排查。建议改整数 `claim_version`。
- **大库迁移在单个 `BEGIN IMMEDIATE` 里全表 SELECT + 逐行 UPDATE**（`storage.go:579-648`），整表进内存，`busy_timeout` 只有 5s；多实例同时升级大库时 `main.go:60 log.Fatalf` 启动失败。
- **进程内配置缓存 `cachedStored` 无 TTL、无跨进程失效**（`storage.go:29-30/793-815/893`）：多实例共享同一 SQLite 时，实例 B 永久使用旧 `download_dir/proxy/token`。存储层多处注释/测试声明多进程安全，但 `main.go` 无单实例锁。
- **`RequeueInterruptedJobs` 无条件把 `resolving/downloading` 重置为 pending**（`storage.go:1057-1082`，`main.go:89` 每次启动调用）：jobs 表无 owner/心跳列，重启重叠窗口（新进程先起、旧进程未退）里第二个实例会抢走第一个实例在跑的任务，跨进程重复下载；`UpdateJob` 终态保护也无 owner 校验。建议加 owner 列或单实例文件锁。
- **`UpdateConfig` 提交后写缓存无串行化**（`storage.go:890-894`）：并发保存可能让内存配置停留在较早写入者的值（再叠加无 TTL，永久偏差）。
- **`ListLibraryDownloads` 吞 `GetConfig` 错误并缓存降级结果 30 分钟**（`storage.go:1438-1446`）。
- **`attributeLibraryUsers` 全量实体 × 全量记录 O(N×M) 前缀匹配**（`storage.go:1501-1539`），且每次 `CreateDownload` 都失效缓存（`:1365/1383`）→ 归档期间画廊请求反复执行"全表 SQL + 全目录 WalkDir + 海量前缀比较"。
- **`JobFiles` 每类静默截断 5000 行且无 truncated/total 标记**（`storage.go:1696`）；前端按数组长度展示"文件数"会偏小。
- **状态集合在 5 处独立硬编码**（models.go 常量、counters.go 触发器/对账、storage.go 终态/取消/重试 SQL）：新增状态漏改任一处会让统计/终态保护静默漂移。
- **单行坏 JSON 让整个计划列表/到期列表失败**（`storage.go:2451-2461`），叠加 manager 静默 return → 全部定时归档一起停摆。
- **大小写不敏感文件系统上 known 集合未归一化**（`storage.go:1449` vs `:1523/1529`）→ 同一文件可能出现"正常记录 + 无归属孤儿"两条。
- 其他：`PRAGMA optimize`/`ANALYZE` 缺失；`DSN` 直接拼 `path + options`（路径含 `?`/`#` 会坏）；死表 `user_previous_names` 只写不读；多个 storage 方法仅测试使用；`CachedStored` 无版本校验等。

### 3.2 任务调度 / 归档流程（`internal/jobs`）

- **`MaxConcurrency` 是死配置**：`config.Normalized()` 无条件置 1（`config.go:113`）、`availableSlots(_ int)` 硬编码 1（`manager.go:313-318`）、前端 `normalizeConfig` 也置 1，且两个单测锁定；但字段仍出现在 DB/API。建议删字段或加注释说明"产品决策=串行"，并修正 CI 里"上限 64"的注释。
- **调度循环无 panic recover**（`manager.go:230-243`；唯一 recover 在任务 goroutine）：`runOnce` 任何 panic 直接崩进程。建议加 `defer recover()` 记录栈。
- **`Stop` 超时(15s)后照常返回，`main` 随后 `defer store.Close()`**（`manager.go:99-124` + `main.go:144-147`）；任务用 `context.Background()` 继续写库 → "database is closed" 类错误，可能丢终态。`Stop` 不返回是否排空。关停预算 10s(Shutdown)+15s(Stop) 也超过 Docker 默认 10s SIGTERM 宽限。
- **后台维护在调度 goroutine 内同步执行**（`manager.go:245-246/322-341`）：启动后第一个 tick 就对整个下载目录 `WalkDir`，大归档时推迟任务领取。
- **自动重试的下载记录归因到当前任务**（`manager.go:1364` → `:791-799`）：`/api/jobs/{id}/files` 混入其他用户的媒体。
- **`save()` 不检查条件 UPDATE 的 `RowsAffected`**（`manager.go:1566-1575` + `storage.go:979-991`）：取消竞争时可能广播与 DB 终态不一致的 `job.updated`（前端终态刷新一般能自愈）。
- **串行槽位下 `lockMedia/lockUser` 生产不可达**，并发测试直接起 goroutine 绕过调度器，`abortKeyLock` 覆盖率 0%；注释/测试名应说明"为未来并发预留"。
- 其他：`Start/Stop` 生命周期不可重入；任务完成后不 `Notify`（每任务多等 0-2s）；`maybeMaintain` 每 2s 空转 DB；`m.complete` 死代码；`stats.Skipped` 语义混淆；blocked/muted 用户静默剔除；`UpdateUserEntityMediaCount` 可能写 0；`renameEntityDir` 目录已改名但 DB 重写失败时中间态无回滚；`safeName` 未过滤 C0 控制字符；`syncLink` 把 `os.IsExist` 当成功；错误文案格式耦合；404/410 写入全局 `unavailable_media` 后 180 天不再重试（需产品确认签名 URL 是否会瞬时 404）。

### 3.3 X API 客户端 / 解析（`internal/xclient` + `internal/parser`）

- **`apiError` 的"data 非空对象即成功"启发式**（`client.go:928-944`）：`{"data":{"user":null},"errors":[{"code":88}]}` 被判成功，限流/账号锁定码被伪装成解析错误，破坏 Pool 切换与批量中止契约。
- **timeline 异常形状/逐条解析失败被静默当"成功无数据"**（`client.go:568-574/597-600/1089-1137`；`len(items)==0` 直接 break）：X 改结构时增量归档永久漏档且任务显示完成。
- **只有 Bottom cursor 的空页直接 break**（`client.go:572-574/1136`）：翻页提前终止，早停游标场景永久跳过更旧推文。
- **长文 `note_tweet` 未解析**（`parser/service.go:172-179/333-340`）：`Text` 只取截断的 `legacy.full_text`，进入预览/文件名/元数据。
- **`created_at` 解析失败静默用 `time.Now()`**（`service.go:166-171/327-332`）：下载文件 mtime 变成"下载当天"且重试不可修复。
- **只识别 `TweetTombstone`**（`service.go:303-311`）：`TweetUnavailable` 等被当成功零媒体，用户提示错误。
- **`/home` 登录自检误判 + `enable()` 恢复被临时池绕过**（`client.go:401-439/1000-1019`；`httpapi/server.go:255` 每次新建诊断池，manager 缓存池的 `disabledUntil` 不会被清除）。
- **同 host 重定向允许 https→http 降级/端口变化**（`client.go:233-241/210-218`）：ct0 仍有明文传输面。
- **GraphQL queryId/响应路径全部硬编码，404 无特殊分类**（`client.go:51/469/485/496/676/713`）：X 轮换即全量失败且无明确告警。
- **403 一律禁用客户端 30 分钟**（`client.go:819-821/1022-1032`）：WAF 挑战与凭证失效不可区分。
- **`ExtractTweetURL` 拒绝官方 `i/web/status/<id>` 分享链接**（`service.go:24/109-123`）。
- **测试不校验认证头/query variables/重定向/cursor 循环，fixture 全是手工最小 JSON**（`client_test.go`、`hardening_test.go`）：最易随 X 变化的契约没有回归保护。
- 其他：退避无抖动；`fmt.Sprintf("%q")` 拼 JSON；`firstBool` 把 null 当 false 阻断 legacy 回退；三层分页未按 ID 去重；`before()` 预扣失败不 refund；32MB 截断静默；`syndicationToken` 与 X 前端算法不一致（实测服务端当前不校验）；`qualityFromURL` 恒为空；`Media.ID` 三种语义混用。

### 3.4 HTTP / 下载 / 文件存储（`internal/httpapi` + `httpx` + `downloader` + `filestore`）

- **下载器自建 `CheckRedirect` 丢失 net/http 默认的 10 次上限、不校验初始 URL 主机**（`downloader.go:303-318`）；配置代理后拨号层 SSRF 防护只检查代理地址（`httpx.go:147-155`，与 `:134-135` 注释矛盾）。
- **下载器把任意 `<400` 当成功**（`downloader.go:319-331`）：204/304/3xx 可发布 0 字节文件，之后因 `size == ContentLength` 被判"完整"永久跳过；`ExistingFileOwner` 的安全语义完全依赖调用方。
- **压缩中间件 `finish()` 未 `defer` + 全站无 recover**（`compress.go:42-57`）：handler panic 时 8MiB 已缓冲 JSON 全丢、连接重置；identity/HEAD 分支无 `Vary`，HEAD 与 GET 头不一致；每请求最多 8MiB 缓冲（无鉴权可放大）。实测 `MaxBytesReader` 的 `requestTooLarge` 私接口经 `sniffCompressWriter` 包装后失效，"请求体超限关闭连接"的保护丢失。
- **海报缓存路径不经 `resolveLocalLibraryPath`**（`server.go:769-782`）：只 `os.Stat` 后直接 `http.ServeFile`，下载目录内符号链接可被跟随；Content-Type 判断大小写敏感；无失败负缓存/在途去重。
- **`/api/library/downloads` 无分页，默认即上限 10 万条**（`server.go:735-747` + `storage` 侧全目录 WalkDir/全量匹配）：无鉴权部署下是低成本 DoS，归档期间前端每次刷新都可能触发。
- **`validateDownloadDir` 的报错文案建议用 `OPEN_XDOWNLOAD_DOWNLOAD_DIR`，但 `ApplyEnvOverrides` 不覆盖 DownloadDir**（`config.go:24-42/146-154`）：已有库时该建议无效；启动不经校验，env 设 `/` 时媒体库可读全盘媒体文件。
- **`PUT /api/config` 部分字段提交会静默重置其余非密字段**（`server.go:117-146` + `:496-507`，校验对象与实际落库对象不是同一个 `Normalized` 结果），可级联触发 P1-1。
- **扩展名白名单跨模块不一致**：downloader 可产出 `.avif/.bin`（`downloader.go:410-411/433`），`storage.IsLibraryMediaPath` 不含（`storage.go:1567-1575`）→ `/api/library/file` 403、孤儿 AVIF 不进画廊，但 `/api/library/downloads/{id}/file` 又能播。
- **`createLocalDirectoryPath` 用 0755**（`server.go:1373-1395`）：与全局 0700 隐私策略不一致，且校验与 `MkdirAll` 之间有 TOCTOU。
- 其他：`filestore.Rename` 源缺失静默成功；`TestWritable` 死接口；`Store` 接口反向依赖 downloader；`isPhotoURL` 运算符优先级/子串匹配；`Accept-Encoding` 解析对 `Q=`/附加参数不健壮；`etagMatch` 不认 `W/`/`*`；`sanitizeFilename` 未处理 NUL/Windows 保留名；`RemoveStaleTempFiles` 1 小时阈值会误删超长下载；twimg 白名单/压缩/海报抓取各有 2-4 份实现等。

### 3.5 前端（`apps/web`）

- **查询失败被渲染成"空状态"**：Gallery 无 `isError` 分支（`GalleryPage.tsx:411-424`）、FailedTweetQueue 顶部 total 与正文"暂无"自相矛盾、Schedules 显示"暂无计划 + 立即创建"可能诱导重复创建、`meta` 失败时统计静默归零（`App.tsx:100-109/164-170`）。
- **`api()` 无超时、无默认取消，2xx 非 JSON 抛原生 `SyntaxError`**（`api.ts:268-287`）：后端/反代挂起时请求永不结束；返回 HTML 时提示为 `Unexpected token '<'`。
- **后端明确会丢 SSE 事件，但前端轮询兜底以"已知有 active 任务"为前提**（后端 `events.go:31-35`；前端 `App.tsx:91-108`）：丢创建事件后两个轮询都停，形成循环依赖。
- **媒体库全量 10 万条 + 每个任务终态都全量失效重拉**（`GalleryPage.tsx:237-244` + `useDashboardEvents.ts:223-226`）：主线程卡顿/内存峰值高。
- **`jobStatusPresentation[status]` 无未知值降级 + `isDashboardJobPayload` 只校验 id/status**（`TaskCenterPage.tsx:233-239`、`useDashboardEvents.ts:54-58`）：后端加状态或响应异常时整页白屏（无 ErrorBoundary）。
- **`jobErrors` 解析规则与后端聚合格式不符**（`jobErrors.ts:53-69` vs `manager.go:1820/1836-1845`）："(共 N 次)"、"另有 N 种其他异常"、单目标格式都会被误解析 → 计数/文案失真。
- **`useTheme` 是每实例独立 state**（`useTheme.ts:110-157`；`AppHeader` 与设置页各一份）：设置页改主题后 Header 图标/强调色不同步；"跟随系统"监听器只挂在设置页实例，离开设置页后系统明暗切换失效。
- **ConfigForm 草稿同步可"卡死"**（`ConfigForm.tsx:55-68/84-96`）：dirty 时"重新加载配置"静默无效；env 覆盖部署下 PUT 响应与后续 GET 的 redacted 值不一致 → `pendingSavedConfigKey` 永不匹配，之后刷新被 `return` 吞掉。
- **`BackupCookieInputs` 在 `setState` updater 内调用父级 `onChange`**（`CookieSettings.tsx:323-358`）：渲染期更新另一组件，StrictMode 下双调用。
- **LocalDirectoryPicker「刷新目录」对树无效**（`LocalDirectoryPicker.tsx:70-83/178-187`：effect 依赖 `listing.data.path` 字符串）；导航失败保留旧树、工具栏显示失败路径，"上级目录"可越出允许根 → 400 无内联提示。
- **Cookie 单行输入粘贴 `auth_token=x; ct0=y` 会污染值，而提示文案正教用户这样粘**（`CookieSettings.tsx:36-46` + `:323-341/562-575`）。
- **`NumberInput` 允许小数/中间态**（`Input.tsx:196-207`）→ 提交 `120.5` 导致 Go `int` 解码失败 400，错误不可读。
- **Modal/Drawer 无 title 时没有关闭按钮**（`Overlay.tsx:40-54/143-152`，移动端导航抽屉实际命中）；Toast error 用 `aria-live=polite` 且键盘无法暂停；SearchSelect combobox 语义分裂（role 在 Trigger、焦点在弹层 input）；`--fg-subtle` 明暗两套对比度约 2.6-2.9:1；主题/强调色低对比。
- **构建配置与源码结构不符**（`vite.config.ts:69-90` + `App.tsx:34-37`）：页面全部静态 import，`modulePreload` 过滤与 `player` manualChunks 是死代码，`react-player` 零引用仍在依赖里；无 `build.target`/sourcemap；`preview` 无 `/api` 代理；dev/preview `--host 0.0.0.0`（服务无鉴权）。
- 其他：`sseConnected` 无人消费（断线无提示）；重连补拉不含 jobFiles；任务终态不失效失败队列；`CopyButton`/`VideoPoster` 计时器/状态小泄漏；CreateJobModal 手动输入不带 scheme 会被后端拒（快捷入口却补 https）、纯数字用户 ID 被快捷入口判成 list；筛选状态未进 URL；`FailedTweet.payload` 幽灵字段；`jobFilesResponse` 的 `omitempty` 与前端必填类型不一致；`tsconfig.node.json` 死配置等。

### 3.6 运维 / 部署 / 构建

- 静态层：`Vary: Accept-Encoding` 只在压缩响应设置；`assetCache` 永不失效且无上限（外部 `webDir` 重建后白屏到重启）；任意 HTTP method 都被当 GET 服务；SPA fallback 的"有扩展名即 404"会拦截带点号的路由，`/api` 精确路径返回 HTML；sidecar 解压失败静默返回 200 空 body。
- `-tls-auto` 自签名证书 SAN 只含 localhost/127.0.0.1/::1，与可配置 `-addr`（Docker 默认 0.0.0.0）脱节；`validDevTLS` 只查有效期，不校验私钥配对/SAN/NotBefore。
- `listen.validate()` 不校验 addr/证书可读；`listenPort` 解析失败静默回退 443（Alt-Svc 会广告错误端口）。
- DB 及 `-wal/-shm` 的 0600 只在启动瞬间 best-effort；`tls` 目录只在创建时 0700。
- Docker：无 `HEALTHCHECK`、默认以 root 启动（依赖 entrypoint `su-exec`）、无 `STOPSIGNAL`/stop grace 提示；基础镜像浮动 tag；`.dockerignore` 漏 `.env*`/密钥/`review/`；`COPY --from=web` 跳过 `sync-webui.sh` 导致镜像内 embed 与 release 二进制不一致。
- CI：不校验提交的 `internal/webui/dist` 是否与前端源码同步、无 `docker build` 冒烟；dockerhub gate 20min < CI 25min 且 300s 无 run 即放行；所有 action 用可变 major tag（含拥有 `contents: write` 的第三方 `softprops/action-gh-release`）；无 gofmt/golangci-lint/govulncheck/npm audit；race 测试无 `-shuffle=on`。
- 其他：`log.Fatalf` 绕过 `defer store.Close()`；`signal.NotifyContext` 在 DB Open 之后注册；`version/commit/date` 全仓库无引用（发版二进制无法自证版本）；`scripts/sync-webui.sh` 权限 0644；`dev-all.sh` 提示"0.0.0.0"但服务默认绑 127.0.0.1；`tsconfig.node.json` 死配置；`@types/node` 26 与 Node 22 运行时错配；Google Fonts 外链在离线/内网部署会回退。

### 3.7 安全边界（部署层面，需产品确认）

- **服务无内置鉴权**：默认绑 `127.0.0.1` 有缓解，但 Dockerfile 显式 `0.0.0.0:8787`。同网段任何人都能读脱敏配置（含 downloadDir 与代理地址）、创建/取消任务、浏览/下载下载目录内的媒体、通过 `/api/local-directories` 枚举家目录、让服务带 Cookie 请求 X。项目注释已说明"需前置带鉴权的反代"，仍建议至少提供可选的 Basic Auth/token 中间件，并在镜像里默认绑定回环或给出显式警告。

---

## 4. 跨模块契约问题（需要一次性对齐）

1. **占位符合并/回填有三份实现且已经分叉**：`internal/config`（`Restore*`/`RevertEnvOnlyEcho`）、`internal/httpapi/server.go:496-507`（`mergeSecretPlaceholders`，仅用于校验）、`internal/storage/storage.go:840-853`（真正落库，额外做 env 回退）。校验值与落库值走不同函数，是 P0-1/P1-4 类问题的土壤。建议收敛为唯一的 `config.ResolveSubmission(submitted, stored)`。
2. **`MaxConcurrency` 三处一致但仍是死配置**（config 归一化=1、manager 硬编码=1、前端=1，CI 注释却说"上限 64"）：要么启用并放开 UI，要么删字段并修正注释。
3. **媒体扩展名白名单两套**：downloader 会产出 `.avif/.bin`，storage 的 `IsLibraryMediaPath` 不认；`.ogv` 反向不一致。建议收敛到共享函数。
4. **文件名长度常量两套**：`config.Min/Max/DefaultMaxFilenameLength`（16/120/240）与 `downloader.normalizedMaxFilenameLength` 各一份。
5. **twimg 白名单 4 份、压缩实现 2 份、海报抓取 2 份**（httpapi 与 jobs），行为已出现漂移。
6. **JSON 契约小偏差**：`jobFilesResponse` 的 `omitempty`（空时 `{}`）vs 前端必填数组；`FailedTweet.payload` 前端有/后端 View 没有；`jobErrors` 解析规则与后端聚合文案格式耦合。
7. **`filestore.Rename` 源缺失静默 nil + `RewriteDownloadPathPrefix` 后置**：目录已改名但 DB 重写失败时中间态长期分裂。
8. **`Result.ContentHash` 在 Skipped 路径为空**：jobs 跳过分支仍写 `ContentHash: ""`，需确认 storage upsert 的 `CASE WHEN excluded.content_hash <> ''` 能保住既有 hash（能保住，但契约脆弱）。
9. **404/410 永久不可用黑名单（180 天）**：`isPermanentlyUnavailableMediaError` 写入全局 `unavailable_media`；若 X 签名 URL 会瞬时 404，一次误判会让媒体半年不重试。
10. **`isPhotoURL` 运算符优先级/子串匹配**（`manager.go:1687-1693`）：`pbs.twimg.com` 命中后不再检查 `.mp4`，且 `strings.Contains` 会匹配 `pbs.twimg.com.evil.com`（当前有上游白名单兜底，风险低）。

---

## 5. 测试与可观测性评估

- **覆盖率不均**：`httpx` 41.6%、`filestore` 48.3%、`cmd/server` 56.1%、`jobs` 58.2% 偏低；核心业务链路 `internal/jobs` 中 `processTweetLink/processUser/processList/processFollowing/processFailedRetry` 与用户取消唯一落库路径 `cancel()` 的覆盖率均为 **0.0%**，`archiveUser` 28.8%，`xPool` 0%。`httpapi` 测试不启动 manager，无法补位 → **"调度 → 分发 → 取消/失败收尾"这条最关键链路没有端到端测试**。
- **前端测试缺口**：`components/settings/*`、`SchedulesPage`、`FailedTweetQueue`、`CommonUI`、`useTheme`、`useMediaQuery`、`App` 均无测试；P0-1 恰好在"无测试的 Cookie 纯函数"里。`useRouteState.test.ts` 两个用例空断言；`TaskCenterPage.test.tsx` 用 `progress: 100` 与真实 0..1 契约不符；`jobErrors.test.ts` 测的是前端假设而非后端真实格式。
- **测试质量问题**：`manager_test.go:30-40/89-98` 在 httptest handler goroutine 里 `t.Fatal`；`TestCancelJobStopsActiveDownload` 只等 `CancelJob` 自己写的状态就返回（单跑 `cancel` 覆盖仍 0%），且 `defer store.Close()` 可能与仍在运行的 goroutine 竞争；归档计划并发测试断言过弱（interval 10 与 109 都能过）；`TestOpenSurvivesConcurrentOpens` 只测空库。
- **可观测性**：无 request id/访问日志/结构化日志、无 handler 级超时、无 recover 中间件；大量静默失败点（archive schedule 领取失败、`ListDueArchiveSchedules` 错误、事件丢弃、`GetConfig` 错误被吞）；CI 无 gofmt/lint/govulncheck/npm audit。

---

## 6. 值得肯定之处

1. **安全默认值与纵深防御**：默认只绑回环、数据目录 0700、DB 0600、下载文件 0600、目录 0700；路径沙箱用 `Rel + EvalSymlinks` 双重校验；SSRF 在拨号层按解析后 IP 拦截；CSRF 三层校验；CSP/`nosniff`/`X-Frame-Options` 齐全且有测试守护。
2. **SQLite 工程化到位**：`BEGIN IMMEDIATE` 的选择有论证与回归测试；迁移框架"数据变更+登记同一事务、崩溃可重试"、迁移拆分粒度细且幂等；schemaVersion 闸门 + 列集合快照测试；部分唯一索引用字面量谓词并有用例钉住查询计划；物化计数器 + 触发器 + 启动对账可自愈。
3. **下载发布协议健壮**：临时文件 → fsync → 硬链接（绝不覆盖）→ 不支持硬链接时 O_EXCL+rename 退化；`ExistingFileOwner` 防同名跨推文覆盖；媒体身份归一去重；exFAT/最大文件名/残留清理都有测试。
4. **压缩/静态服务细节**：预压缩 sidecar + 内存缓存 + 分层缓存头 + ETag 304；SSE 与流式响应强制 passthrough；bootstrap 注入用 JSON encoder 转义防 `</script>` 逃逸；CSP 测试禁止放开 `unsafe-*`。
5. **并发/取消语义**：串行化目标三处一致；`isCancellation` 只信 job ctx 不信 error；优雅关停保留任务为可重排队状态；`EventBus.CloseAll` 解决 SSE 拖死 `Shutdown` 的问题并有真实 SSE 测试。
6. **X API 侧**：限流器以响应头为权威、预扣/refund/夹逼/有界等待；增量归档早停对置顶/删除/旧推文边界考虑周全；解析侧就把不可信媒体 URL 清成 twimg 白名单；视频只取 mp4、绝不用 HLS/缩略图冒充。
7. **前端**：SSE 生命周期清理完整、断线退避重建；`updatedAt` 防抖与"迟到事件丢弃"；首屏 bootstrap 水合；危险操作均有 `Popconfirm`；无 XSS sink；大量纯函数 + 单测，`tsc` 与 179 个用例全绿。
8. **构建/发布**：多阶段交叉编译、最小构建上下文、entrypoint `exec` 保持 PID1、chown 失败不杀容器、CI 最小权限 + concurrency 取消、release 先构建一次前端再分发、dockerhub 发布前有 CI gate。

---

## 7. 建议的修复路线图

**第一批（P0/P1，建议 1-2 天，先堵数据损坏与可用性）**
1. 备用 Cookie 稳定 id 全链路（P0-1 + P1-4）：前端保留/回传 id，后端拒绝占位符落库，补删除/重排回归测试。
2. 配置变更语义（P1-1 + P2 部分字段 PUT）：改为 PATCH 语义或全量校验；下载目录变更时重写历史路径并失效 library 缓存；`UpdateConfig` 统一走 `ResolveSubmission`。
3. 进程启动/关停（P1-2 + P2）：错误退出码非 0，h3 失败不拖垮 TCP，Shutdown 预算 < Docker 宽限。
4. HTTP 错误边界（P1-3）：typed error 映射 4xx/404/500，禁止 4xx 回传 OS/DB 原文。
5. 任务链路（P1-7/P1-8/P1-9）：单用户错误降级 + 独立失败重试任务与退避 + list/following 走 Pool。
6. 前端可见缺陷（P1-5/P1-6/P1-10/P1-11）：轮询合并保留 SSE 新行、显式清除 Cookie、主题对比色 token、设置页 label 关联。

**第二批（P2 高价值，建议 3-5 天）**
- storage：library `known` 集合与 limit 解耦、`.preview-*` 临时文件过滤、归档计划 claim_version、迁移分批/退避、配置缓存 TTL、多实例单实例锁或文档统一口径。
- jobs：调度 panic recover、Stop 返回排空结果、维护移出调度协程、重试归因。
- http/downloader：重定向次数/初始主机/代理语义、`<200 || >=300` 判失败、压缩中间件 defer finish + recover、海报路径校验与负缓存、媒体库分页。
- xclient/parser：`apiError` 收紧、timeline 异常报错、空页+cursor 继续、note_tweet、时间解析失败报错、https 降级拒绝、queryId 集中与 404 分类。
- 前端：错误态与空态分离、`api()` 超时、未知状态降级、jobErrors 结构化、ConfigForm 同步状态机、目录选择器刷新/越界。
- 运维：HEALTHCHECK、stop grace、`.dockerignore`、CI 加 dist 同步闸门与 docker build、action 固定 SHA。

**第三批（P3/技术债）**：死代码/死配置清理（MaxConcurrency 字段、`user_previous_names`、`m.complete`、`react-player`、twimg 白名单/压缩/海报抓取去重）、注释与实现对齐、gofmt/lint/govulncheck 入 CI、测试补强（`process*`/`cancel`/settings 纯函数/真实 payload golden）。

---

## 附录 A：区域报告与覆盖清单

| 区域 | 报告 | 覆盖文件 | P0/P1/P2/P3 |
|---|---|---|---|
| storage | `review/storage.md` | 10 个文件 / 5,521 行 | 0 / 3 / 12(+4 测试) / 15 |
| jobs | `review/jobs.md` | 7 个文件 / 4,139 行 | 0 / 2 / 8 / 23 |
| xclient + parser | `review/xclient-parser.md` | 7 个文件 / 3,425 行 | 0 / 1 / 12 / 16 |
| httpapi + httpx + downloader + filestore | `review/httpapi-httpx-downloader-filestore.md` | 21 个文件 / 5,431 行 | 0 / 2 / 10 / 16 |
| cmd/server + config + webui + ops | `review/server-config-ops.md` | 33 个文件 / ~3,060 行（含测试与配置，不含 package-lock） | 0 / 5 / 20 / 40 |
| 前端应用层 | `review/web-app.md` | 39 个文件 / 6,900 行 | 0 / 2 / 6 / 8 |
| 前端 UI/设置/构建 | `review/web-ui-settings.md` | 34 个文件 / 5,180 行 | 1 / 3 / 16 / 29 |

7 份区域报告合计约 **250 条原始发现**（含同源重复）；本报告已合并去重，按严重度重新归纳。

## 附录 B：审查期间发现并按约束还原的意外改动

审查过程要求只读。收尾时发现子代理误改了 5 个 Go 源文件（`internal/downloader/downloader.go`、`internal/httpapi/server.go`、`internal/httpx/encoding.go`、`internal/xclient/client.go`、`internal/xclient/ratelimit.go`），内容恰好是对应审查发现的"顺手修复"。已用 `git checkout --` **全部还原**，还原后重新执行 `go build ./... && go vet ./... && go test ./...` 全部通过；当前工作区除 `review/` 报告目录外与审查开始时完全一致。这些改动未纳入本报告的结论，也未保留在代码中。
