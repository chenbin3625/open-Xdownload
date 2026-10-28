# 修复复核报告（对照 review/REPORT.md）

- 复核时间：2026-10-28 12:50（Asia/Shanghai）快照
- 复核对象：当前工作区相对 `HEAD`（`997de7a`）的 73 个已跟踪文件改动（+3,291/−578）+ 新增未跟踪文件；`review/FIXES.md` 的修复声明仅作为**待验证断言**
- 方法：逐条读当前代码 + 可执行探针（临时拷贝仓库到 `/tmp`，不在仓库落任何文件）+ 全量测试；已完成的区域复核：storage、xclient/parser、前端应用层、前端 UI/设置（详见 `review/recheck-*.md`）；http/运维两块由 Lead 直接核验（对应 agent 报告仍在收尾）
- **只读承诺**：本报告未修改任何源码/测试/配置。**但复核期间工作区仍在被并发修改**（见 §6），因此结论只对应上述快照。

> **后续进展（2026-10-28 14:05）**：本文件列出的 6 个 P1 回归与全部 P0/P1/P2 已在本轮全部修复并通过集成验证，详见 `review/FIXES-2.md`。

## 0. 一句话结论

**没有全部修复。** 报告中的 **1 个 P0 已修复**；**11 个 P1 中 10 个已修复、2 个只修了一部分**（P1-9、P1-10）；**P2 约修复 6 成、P3 大部分未动**。更关键的是：**这轮修复新引入了 6 个 P1 级回归**——配置保存被 400 阻塞/凭据截断（半行 Cookie、env 注入 Cookie、env 覆盖下载目录三种触发）、`PUT /api/config` body 为 `null` 时静默重置全部非密配置、env 覆盖下载目录后历史媒体永久 404、Windows 上数据库打不开、旧库启动失败、单条坏推文毒丸；另有若干 P2 风险（主题仍有 5 处不可读、API 30s 硬超时、legacy `/` 被登记为可信根等）。因此**现在还不能认为"报告已全部修复"**。

## 1. 验证基线（12:50 快照，均可复跑）

| 检查 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l cmd internal` | 通过 / 无告警 / 无输出 |
| `go test -count=1 ./...`（多轮） | 10 个包全部通过 |
| `go test -race -shuffle=on -count=1 ./...` | 全部通过（无数据竞争） |
| `GOOS=windows go build ./...`、`GOOS=linux go build ./...` | 通过（注意：能编译 ≠ 能运行，见 R-B） |
| `cd apps/web && npx tsc --noEmit` | 通过 |
| `npx vitest run` | 23 文件 / 211 用例通过（多轮、含并行负载） |
| `npx vite build` | 通过；产物 hash 与 `internal/webui/dist` 一致 |
| `storage` 覆盖率 | 70.6%（原 70.3%） |
| `npm ci`（干净目录） | 通过，`package-lock.json` 已与 `package.json` 同步（`react-player` 已移除） |

> 绿测不代表修好：下面的 4 个新回归都**在测试全绿的前提下存在**，且都已用可执行探针复现。

## 2. P0 / P1 逐条状态（12 条：10 ✅ / 2 🟡）

| 编号 | 状态 | 当前证据 | 备注 |
|---|---|---|---|
| **P0-1** 备用 Cookie 稳定 id | ✅ | 前端 `CookieSettings.tsx:521-541` 保留 `record.id`，`:581-586` 改为带 id 的 JSON；后端 `config.go:228-245` 删除位置兜底，`:273-283` 未知 id+占位符 400；新增回归测试 | 我用当前 `config.go` 实测：删首/中/末行、重排、旧文本兼容全部正确 |
| **P1-1** 改下载目录后历史媒体全 403 | ✅ | `storage.go:963-971/1013/1019-1023`（持久化 `download_roots` + 提交后失效缓存）+ `httpapi/server.go:1089-1104 resolveDownloadPath` + 越界 404 | 端到端测试 `TestChangingDownloadDirPreservesHistoricalMedia`；残留：升级前已换过目录的库无法回溯，**env 切换下载目录路径仍 404（见 R-E）** |
| **P1-2** 启动失败退出码 0 / h3 拖垮 TCP | ✅ | `main.go:29-31` `run() int` + `os.Exit`；`:157-172` `errCh`；h3 失败仅 `h3Available=false` 不再 `stop()`；先 `listenTCP` 后打日志；TLS 预检；新增单实例文件锁 | |
| **P1-3** 4xx 泄漏内部错误 | ✅ | `writeError`（`server.go:1320-1350`）识别 `UserFacingError`、`sql.ErrNoRows→404`、OS/URL/SQLite 错误→500 脱敏；各端点改用 `ErrValidation/ErrNotFound` | `runArchiveSchedule` 仍用 400，但内部错误已被 500 脱敏兜底 |
| **P1-4** 占位符被当真实凭据落库 | ✅ | `RestoreAdditionalCookies` 删除位置兜底、占位符无法还原时置空；`ResolveSubmission` 未知 id/重复 id/无法还原直接拒绝；`RedactAdditionalCookies` 零解析返回 `""` | **修复本身引入了新 P1 回归，见 R-A** |
| **P1-5** 轮询抹掉 SSE 新任务 | ✅ | `useDashboardEvents.ts:92-125` 30s 保留窗口 + `createdAt/id` 排序 + `splice(pageSize)`；jobs 轮询改看全局 `meta.stats.active`，meta 空闲也 30s 常驻 | 4 条测试；残留：依赖浏览器时钟（客户端快 >30s 退化） |
| **P1-6** 无法清除主 Cookie | ✅ | `ClearSentinel="__CLEAR__"` 全链路（`CookieSettings.tsx` 清除按钮 + `config.go:285-291` + storage 落库为 `""`），清除后 GET 为空、不误清 env | 有前端 + Go 测试 |
| **P1-7** 单用户错误中止整批 | ✅ | `manager.go` `archiveUsers` 对 `*os.PathError`/`*os.LinkError` 记 `Failed+Issue` 后 `continue`；取消/限流/DB 错误仍上抛 | |
| **P1-8** 自动重试全量重跑 + 阻塞 | ✅ | 改为独立 `JobKindFailedRetry` 任务：`storage.EnsureAutomaticRetryJob`（去重 + 10min 失败冷却 + `next_retry_at`）、`ListFailedTweetsForRetry(20)`、`TouchFailedTweet` 指数退避（10min×2^n，上限 24h）；user/list/following 不再内联重试 | 重试归因同步改为重试任务 |
| **P1-9** List/Following 只走 Primary | 🟡 | `xclient/client.go:507/708/758` 新增 `Pool.GetListByID/GetListMembers/GetFollowing`，jobs 已改用（`manager.go:618/639/686`），429 可交还 | `FollowUser` 仍 Primary（按 FIXES 声明为"保持操作身份"的设计决策）；3 个新 Pool 方法**零测试**；新增跨 Cookie 视角不一致风险（R-G3） |
| **P1-10** 深色钛金白底白字 | 🟡 | `--brand-contrast` 5×2 全覆盖、Button primary 已改用 | **另有 5 处同类未改**：`Pagination.tsx:100`、`Tag.tsx:106`、`Controls.tsx:157`（Checkbox 勾）、`AppSidebar.tsx:70`、Switch 滑块 + `::selection`，深色钛金 1.10:1 |
| **P1-11** 设置页 label 缺失 | ✅ | `Field` 改为 Context 自动注入 id（`Input.tsx:248-275`），Input/NumberInput/Select/PasswordInput 认领；`Select` 补 `ariaLabel`；主题按钮 `aria-pressed`；TipIcon 改 `<button>` | 我最初"代理/文件名长度仍无 label"的判断**不成立**，已更正（`ConfigForm.test.tsx` 有 `getByRole(name)` 断言） |

## 3. 新引入的回归（必须优先处理）

### [R-A][P1] 配置保存被 400 阻塞（3 种触发），并可能静默丢凭据

> 复现来源：Lead 独立探针（`/tmp` 仓库副本）+ `rev-web-ui` + `rev-http` 的一致结论；三种触发同一根因家族（"展示用 env/宽松值、校验与落库用 stored/严格值"）。

- 位置：`internal/config/config.go:203-206`（Redact 无条件把两字段都写 `********`）、`:252-254`（允许半行落库）、`:273-283`（占位符必须对上"非空"旧值）、`:300`（`RevertEnvOnlyEcho` 对 JSON 永不相等）；前端 `CookieSettings.tsx:581-586`
- **可执行复现**（当前代码，`/tmp` 探针）：
  1. **半行备用 Cookie**：新增备用行只填 `auth_token` 保存成功 → 之后**任何一次保存配置、任何一次"检测 Cookie"都 400**（`无法还原备用 Cookie…`），刷新/重启均不自愈，用户只能自己猜着删行。
  2. **env 注入 `OPEN_XDOWNLOAD_ADDITIONAL_COOKIES`**：DB 为空 → 保存/检测全 400；DB 非空且 id 不匹配 → 全 400；id 恰好匹配 → 保存会把库内其余备用账号**静默截断**成 env 那条（数据不可逆丢失）。
  3. **env 注入 `OPEN_XDOWNLOAD_DOWNLOAD_DIR` 与库内不同**：任何保存 400，错误文案却写着"请用 OPEN_XDOWNLOAD_DOWNLOAD_DIR 环境变量启动"。
- 影响：P0-1/P1-4 的收紧把"坏数据"从"静默写坏"变成了"整个设置页不可保存"，且 env 注入正是 README 推荐的 headless 部署方式。

### [R-B][P1] Windows 上 `storage.Open` 必然失败

- 位置：`internal/storage/storage.go:111`：`(&url.URL{Scheme:"file", Path: filepath.ToSlash(absPath)}).String()`。Windows 绝对路径 `C:/Users/...` 会生成 `file://C:/Users/...`。
- **实测**（modernc.org/sqlite v1.59.0，与仓库同版本）：`Ping()` → `SQL logic error: invalid uri authority: C: (1)`；`file:///C:/...` 才合法。
- 影响：release 矩阵包含 `windows/amd64`、`windows/arm64`（`.github/workflows/release.yml:81-89`），CI 只跑 ubuntu，**构建能过、用户运行必崩**。

### [R-C][P1] 旧库 `download_dir` 为家目录/`/` 时启动直接失败（升级不可用）

- 位置：`internal/storage/storage.go:141-146` 在 `Open()` 里新增 `config.ValidateDownloadRoot(cfg.DownloadDir)`，env override 也算。
- **实测**：把已有库的 `download_dir` 改成 `$HOME` 后重新 `Open` → 返回 `下载目录不能是文件系统根目录或家目录本身`，`main` 直接退出 1；用户没有任何 UI/CLI 途径修复（必须先手工改库或删库）。
- 影响：安全上合理（阻止媒体库变成全盘读取器），但缺少迁移/降级路径，属于"升级即砖"。

### [R-D][P1] `PUT /api/config` body 为 JSON `null` → 200 且静默重置全部非密配置

- 位置：`httpapi/server.go:122-159`（`decodeJSON` 到 `map[string]json.RawMessage` 得到 nil map）→ `storage.UpdateConfigPatch(ctx, nil)` → `storage.go:943` 的 `if fields != nil` 为 false → 走零值 `AppConfig` 分支。
- **可执行复现**（当前代码）：库内 `downloadDir=/x/downloads, proxyUrl=http://proxy:8080, additionalCookies=auth_token=A..., maxFilenameLength=200`；对 `/api/config` 发 `PUT` body `null` → 200，随后 `downloadDir` 回默认、`proxyUrl` 空、`additionalCookies` 清空、`maxFilenameLength=120`。基线 `HEAD` 同请求是 400 且不改库，**明确回归**。
- 影响：任何脚本/客户端（或误发空 body 的小工具）一次 `null` PUT 就清空代理与备用账号池（凭据不可逆丢失），前端设置页随后显示空值。
- 修复：`fields == nil` 时返回 400（或按 `{}` 处理），补一条"null body 不修改配置"的回归测试。

### [R-E][P1] env 覆盖 `OPEN_XDOWNLOAD_DOWNLOAD_DIR` 后：历史媒体永久 404 + 设置页保存全 400

- 位置：`config.go:24-27`（env 覆盖生效）+ `storage.go:962-973`（`download_roots` 只在 `updateConfig` 写入，启动/`EnsureConfig` 不播种、env 变更不登记旧值）+ `server.go:143-148`（`validateDownloadDir` 用 `GetStoredConfig` 的允许根，而前端提交的是 `GetConfig` 的 env 值）+ `api.ts:385-388`（全量回传）。
- **可执行复现**：env 指向 `.../envdir`、库内 `.../stored`；`storage.Open` 后 `DownloadRoots` 为 0 行；历史记录在旧目录 → `/api/library/downloads/{id}/file` 404；任意保存（哪怕只改 `maxFilenameLength`）→ 400 `下载目录不允许：仅限家目录或当前下载目录范围内；如需指向其他位置，请用 OPEN_XDOWNLOAD_DOWNLOAD_DIR 环境变量启动`（文案自身指向 env，形成循环）。
- 影响：这是 README/Docker 推荐的部署方式（`OPEN_XDOWNLOAD_DOWNLOAD_DIR=/downloads`），一旦从旧目录切换到 env 目录，历史媒体全部失效且**无法通过 UI 修复**。

### [R-F][P2] legacy 库 `download_dir="/"` 时 `/` 会被永久登记为可信根

- 位置：`storage.go:962-973`：登记 `current.DownloadDir` 与 `cfg.DownloadDir` 时只对新值做 `ValidateDownloadRoot`，不校验 `current`。
- 影响：若用 env 兜底启动一个 `download_dir="/"` 的旧库，任一次保存都会把 `/` 写进 `download_roots`；此后 `resolveDownloadPath` 对 `file_path=/etc/hosts` 之类记录也能解析（`rev-http` 实测 200）→ 媒体库退化为任意文件读取面。属安全回归，建议登记前对两个根都做校验，并清理历史根。

### [R-G][P2] xclient/parser 新风险（部分已修项的副作用）

1. `apiError` 容忍条件绑死 `kind=="NonFatal"`（`client.go:1013`）：无 `kind` 的 partial error 现在整页失败（需真实抓包确认，最坏 P1）。
2. 88 归为 transient + `markRateLimited`：Client-only 路径（受保护用户时间线、FollowUser）会 5 次重试并等待复位，最长 ~4×15min。
3. Pool 化 List/Following 后每页可能换 Cookie，受保护账号/私有列表"视角不一致"，可能空列表/少成员。
4. 设置页 `checkAuth` 复用生产 `XClientPool`（`server.go:284-300`），`/home` 403 会把正在归档的 Cookie 禁用 30 分钟；提交未保存配置还会临时替换 `cachedPool`。
5. `httpx.ErrBodyTooLarge`、`errRateLimitBusyWait` 被当瞬时可重试；持续"空页+新 cursor"最多翻 1000 页；`previousCursor` 守卫是死代码；`errors` 非数组时 `apiError` 静默成功。

### [R-H][P2] 前端新风险

1. `api()` 30s 硬超时无 per-call 覆盖（`api.ts:280`），可能误杀大媒体库首屏/改目录/`checkAuth`；`AbortSignal.any/timeout` 无运行时兜底（旧 Safari 全站数据层 TypeError）。
2. SSE 保留窗口依赖浏览器时钟；非法 `createdAt` 会让排序退化；保留行会挤出 fetched 末行。
3. 主题：除 P1-10 的 5 处外，`--fg-subtle` 亮色仅 **4.02:1**（<AA 4.5，暗色 5.45 达标）；`::selection` 白字 on brand。
4. 媒体库"分页"是 HTTP 层内存切片，服务端仍按 10 万上限全量扫描/过滤；搜索每个 keystroke 一次请求。

### [R-I][P1/P2] 单条坏推文变成"毒丸"，该用户永久无法增量归档

- 位置：`internal/xclient/client.go:620-626`（除 `ErrTweetUnavailable` 外解析错误直接让整页/整用户失败）；`internal/jobs/manager.go:1243-1253`（只记 `Failed++` 就返回，不进入 `archiveTweets`）。
- 影响：`archiveTweets` 里的 `UpdateUserEntityLastSeenTweet` 是游标唯一推进点；解析失败时游标不写，且 `created_at` 非法现在会报错（以前 fallback now），`IncrementalArchive` 下每次都在同一条推文处失败 → 该用户永久不再归档。新测试 `TestUserTimelineDoesNotSkipMalformedTweet` 反而锁死了该行为。

### [R-J][P2] 静态层：外部 webDir 下 sidecar 陈旧时 ETag 与 body 不一致

- 位置：`cmd/server/static.go:317-376`（assetCache 以 identity/`.gz`/`.br` 的 `(size, mtime)` 作为 key）+ 发送时按协商编码取 `item.body(encoding)`。
- 实测（`recheck-ops.md` §5.2）：只改写 identity → ETag 变、body 仍旧；再更新 `.gz` → **同一个 ETag 映射到两份不同字节**，带 `If-None-Match` 的客户端会永久拿 304 保留旧内容（`index.html` 同样受影响）。内嵌 embed 路径无 identity 文件，不受影响。建议 identity 请求直接用 identity 字节计算/发送，ETag 用实际发送字节计算，并补该场景测试。

### [R-K][P2] Docker HEALTHCHECK 两处缺陷

- 位置：`Dockerfile:42-43`。① 硬编码 `127.0.0.1:8787`：用户覆盖 `OPEN_XDOWNLOAD_ADDR`（镜像文档化能力）后容器永久 unhealthy；② 探的是 `/` 而非现成的 `/api/health`，没有验证 DB/服务就绪语义；③ TLS 分支用 `wget --no-check-certificate`，但 busybox wget 的该选项在部分发行版不可用。建议用 `OPEN_XDOWNLOAD_ADDR` 推导端口 + 探 `/api/health`，或提供独立 healthcheck 脚本。

### [R-L][P2] `manager.Stop()` 超时以 exit 1 退出，会和重启策略打架

- 位置：`cmd/server/main.go:189-193`。优雅停机超时被编码成"失败退出"，`systemd Restart=on-failure` / `docker --restart on-failure` 会把用户主动 `stop` 当成崩溃而自动重启；同时 `os.Exit` 跳过 `lock.Close()/store.Close()/ln.Close()`（注释声明为有意）。建议区分"主动停机 + 排空超时"（exit 0 + 警告日志）与"真正的致命错误"（exit 1）。

### [R-M][P3] 运维杂项

- CI dist 闸门的盲区：`git diff --exit-code` 看不到**未跟踪**的新 sidecar——实测只新增 `public/robots.txt` 重建后闸门仍 exit 0；建议改成 `git add -A -- internal/webui/dist && git diff --cached --exit-code`。
- `go.mod:25` `golang.org/x/sys` 被新锁文件直接 import 却仍标 `// indirect`（`go mod tidy` 会改），CI 无 `tidy -diff` 检查。
- 未跟踪的 20 MB `httpapi.test` 仍在仓库根，`.gitignore` 未排除；Docker 仍以 root 启动、未加 `USER`。

## 4. P2 / P3 修复面（不完整抽样，详见各 `recheck-*.md`）

| 区域 | 已修 | 部分 / 未修 |
|---|---|---|
| storage（§3.1 14 条） | 9 条：known 集合、`.preview-*`、计划领取、迁移分批、配置缓存 TTL、缓存失效、GetConfig 错误、JobFiles truncated、hydrate 坏行隔离 | 4 部分：迁移仍单事务、Requeue 仅靠单实例锁、缓存失效无节流、大小写处理；**1 未修：状态集合仍 5 处硬编码**（`counters.go:50-52/59-66/73-75/104-106` + `storage.go:1125`） |
| xclient/parser（29 条） | 9 条：apiError、空页+cursor、note_tweet（GraphQL）、created_at、TweetUnavailable、https 降级、i/web、firstBool、32MB 截断、预扣时机 | 6 部分（/home 自检、测试补强等）；**13 未修**：queryId/404 分类、403 一律禁用、退避抖动、`%q` 拼 JSON、分页 ID 去重、syndicationToken、qualityFromURL、Media.ID 等 |
| 前端应用层（§3.5） | 11 条：错误态、api 超时、jobErrors 新格式、useTheme 同步、ConfigForm、updater 副作用、目录选择器、NumberInput、Modal、Toast、SearchSelect、重连补拉、终态失效、JobFiles 契约 | 4 部分；**4 未动**：Cookie 粘贴、`sseConnected` 未消费、筛选未进 URL、`FailedTweet.payload` 幽灵字段 |
| 前端 UI/设置 | P0-1、P1-6、P1-11、ConfigForm、LocalDirectoryPicker、主题 aria、嵌入 dist | P1-10 部分；`--fg-subtle` 4.02；**未动**：Cookie 粘贴提示、Spinner keyframes、Card/Tag/Table/Tree P3、`tsconfig.node.json`、preview 无代理、dev `--host 0.0.0.0` |
| httpapi/httpx/downloader/filestore（`recheck-http.md`，10 条：1 ✅/8 🟡/1 ❌） | 仅 2xx、重定向 10 跳+降级、compress `defer finish`+recover、`MaxBytesReader` 解包装、海报 symlink/root 校验、`.avif` 白名单、目录 0700、媒体库分页契约+前端接入、PATCH 语义 | 残留：下载器初始 URL 主机未校验；海报 Content-Type 大小写/无负缓存；媒体库 paged 仍全量 10 万扫描；`.bin` 不在白名单；`decodeJSON` 仍回传内部类型名；可观测性（P2-10）完全未做 |
| cmd/server/config/静态/运维（`recheck-ops.md`，P1-2 与 §3.6 逐项验收） | 退出码非 0、h3 不拖垮 TCP 且 Alt-Svc clear、先 bind 后日志、TLS 预检、tls-auto SAN、单实例锁（unix/windows，已接入 `main.go:69-74`）、静态 405/带点路由/`/api` 404/Vary/损坏 sidecar 404、assetCache 按 (size,mtime) 失效 + 上限、DB+WAL 0600、tls 目录 0700、`log.Fatal` 清零、version 日志、CI gofmt/race-shuffle/action SHA、dockerhub 闸门 | 残留：Windows DSN 阻断（R-B）、HEALTHCHECK 两处缺陷、`manager.Stop()` 超时 exit 1 与重启策略冲突、dist 闸门盲区、go.mod 未 tidy、Docker 仍 root |

## 5. 仍完全未修的项（"是否全部修复"的直接答案）

- **storage**：状态集合单一来源、`PRAGMA optimize`/`ANALYZE`、`user_previous_names` 死表、计划领取仍按时间点比较（无 `claim_version`）、`MaxConcurrency` 死配置（仍硬编码 1 且无注释）、`download_roots` 只增不减、`next_retry_at` 无索引、`dropSMBWebDAVConfigColumns` 仍零测试。
- **跨模块**：文件名长度常量 16/120/240 仍两份；twimg 白名单 4 份、compress 2 份、海报抓取 2 份未收敛；`Result.ContentHash` 在 Skipped 路径仍为空。
- **xclient/parser**：上述 13 项（queryId/404、403 策略、退避抖动、ID 去重、syndicationToken、qualityFromURL、Media.ID 等）。
- **前端**：Cookie 粘贴、`sseConnected` 无人消费、筛选未进 URL、`FailedTweet.payload`、Spinner keyframes、Card/Tag/Table/Tree 的 P3、`tsconfig.node.json`、preview 代理、dev `0.0.0.0`。
- **仓库卫生**：`internal/webui/dist` 处于"16 个已跟踪文件被删 + 16 个新文件未跟踪 + 3 个修改"的状态——按现状 `git diff --exit-code -- internal/webui/dist`（CI 新闸门）会失败；若用 `git commit -am` 提交会漏掉新增资产，embed 后页面 404。另有未跟踪的 **20 MB `httpapi.test`** 与 **23 MB `server`** 二进制在仓库根目录。

## 6. 复核期间工作区仍在变化（结论有效期说明）

复核过程中检测到：
- `internal/webui/dist` 于 11:25 / 12:38 / 12:48 被重建；
- `internal/jobs/manager.go`、`manager_test.go` 于 **12:46** 仍有新写入（新增 content-hash 媒体锁与 `TestDownloadMediaConcurrentDifferentURLsReuseSameContent`），仓库根出现 23 MB `server` 二进制（12:46）；
- 因此：**"报告是否全部修复"目前没有稳定答案**——有人在复核同时继续改代码。我已中断仍在写文件的复核 agent，并保留现场未做回滚。
- 运维/构建区域（`recheck-ops.md`）随后已交付：P1-2 与 §3.6 逐项验收基本通过，**独立复现了 Windows DSN 阻断**，并新增 4 条 P2/P3（ETag/body 不一致、HEALTHCHECK 缺陷、`Stop` exit 1 语义、dist 闸门盲区/go.mod tidy）。

## 7. 建议（按顺序）

1. **先修 5 个 P1 回归**：R-A（半行/env 配置保存 400 + 账号池截断）、R-D（`null` PUT 重置配置）、R-E（env 下载目录历史 404 + 保存 400）、R-B（Windows DSN）、R-C（旧库家目录启动失败）。每条都要配可执行回归测试（`/tmp` 探针即可复现）。
2. **修毒丸（原 R-D→现 R-I）**：单条解析失败应"计数+跳过+记录"，而不是让整用户失败；或至少游标前进到失败推文之前，并在连续 N 条失败后再整体报错。
3. **补 P1-10 剩余 5 处 `text-white` on `brand-500`**，并加一条静态检查防回归。
4. **冻结工作区**（停掉并行修改）后，重跑本报告 §1 的全部命令 + `go test -race -shuffle=on ./...` + `npm test`，再判断"全部修复"。
5. P2/P3 按 §4/§5 清单排期；`httpapi.test`/`server` 二进制与 `internal/webui/dist` 的提交状态必须处理（`git add -A internal/webui/dist`，删除两个二进制）。
