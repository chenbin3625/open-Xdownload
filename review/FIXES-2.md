# 全部修复完成报告（FIXES-2）

- 日期：2026-10-28
- 输入：`review/REPORT.md`（全量审查 250 条发现）+ `review/VERIFY.md`（6 个新 P1 回归）+ 各区域 `recheck-*.md`
- 执行方式：Lead 统一编排，8 个区域 owner 并行实施（写域互不重叠），Lead 负责契约对齐、集成与最终验证
- 范围：仅代码/测试/构建配置；未提交、未推送

## 0. 结论

**报告与复核中列出的 P0/P1/P2 及可执行的 P3 已全部修复，新增回归全部消除。** 最终工作区通过全量 Go 测试（含 `-race -shuffle`）、前端类型检查/273 个单测/生产构建、双平台交叉编译、`gofmt`/`go vet`/`go mod tidy -diff` 干净；嵌入产物可复现且与源码构建逐字节一致。

规模：**139 个文件变更（+10,352/−2,245）**；Go 测试函数 436 个；前端测试 27 文件 / 273 用例。覆盖率较修复前提升：config 92.4%、parser 82.5%、downloader 80.7%、storage 74.9%、xclient 71.8%、jobs 63.8%、httpapi 64.0%、filestore 69.0%、httpx 45.1%。

## 1. 原报告 P0 / P1（12 条）全部闭环

| 编号 | 修复落地 |
|---|---|
| **P0-1** Cookie 稳定 id | 前端保留/回传 id（JSON）；后端删除按行号回填、逐字段脱敏、宽松还原；删除/重排/未知 id/半行均有回归测试 |
| **P1-1** 改下载目录后媒体 403 | `download_roots` 启动播种（库内 + env 生效值）、变更时登记旧/新根且两值都校验、上限 32 + 非法根清理；提交后立即失效 library 缓存；`resolveDownloadPath` 多根解析；env 路径下历史媒体实测 200 |
| **P1-2** 启动/h3 退出码 | `run() int` + `errCh`：启动/bind/TLS 失败 exit 1；h3 失败仅告警并撤 Alt-Svc；先 bind 后打日志；单实例文件锁 |
| **P1-3** 4xx 泄漏内部错误 | `UserFacingError`/`ErrNotFound`/`ErrValidation` + `writeError` 脱敏；`sql.ErrNoRows→404`；`decodeJSON` 统一文案（不泄漏类型名/EOF）；列表/计划/任务端点全部 typed |
| **P1-4** 占位符落库 | `ResolveSubmission` 恒不报错、占位符逐字段还原、还原不到置空、重复 id 取首条；`RedactAdditionalCookies` 逐字段脱敏；"整体 ********"路径消除 |
| **P1-5** 轮询抹掉 SSE 新任务 | 保留窗口改为纯服务器排序语义（`createdAt > fetched 首行`，同毫秒比 id），移除浏览器时钟依赖；jobs 轮询看全局 active，meta 空闲 30s 常驻 |
| **P1-6** 无法清除主 Cookie | `__CLEAR__` 全链路；env 生效时清除无效（保持 env）；哨兵不外发 |
| **P1-7** 单用户错误中止整批 | 仅 Path/Link 错误降级 continue；取消/限流/DB 错误仍上抛 |
| **P1-8** 自动重试全量重跑 | 独立 `JobKindFailedRetry`；`ReserveAutomaticRetryJob` 单行复用 + `next_retry_at` 指数退避 + 7 天清理；终态 `completed`；取消后 10min 暂停；检查频率 30s |
| **P1-9** List/Following 只走 Primary | Pool 新增 `GetListByID/GetListMembers/GetFollowing/FollowUser`；单次调用固定同一 Client 拉完分页；jobs 全部改用；故障转移测试 |
| **P1-10** 深色钛金白底白字 | `--{brand,success,warning,danger,info}-contrast` 5×2 全覆盖；Pagination/Tag/Checkbox/Switch/侧栏/画廊/`::selection` 全部改用配对色；对比度静态测试 |
| **P1-11** 设置页 label | `Field` Context 自动注入 id，Input/NumberInput/Textarea/Select/PasswordInput 认领；主题 `aria-pressed`；TipIcon 可聚焦；`getByRole(name)` 端到端断言 |

## 2. VERIFY 中的新回归（R-A ～ R-M）全部消除

- **R-A 配置保存 400 家族**：`ResolveSubmission` 改为宽容修复（半行/重复 id/未知 id 均 200）；env 接管字段（AUTH/CT0/COOKIES/PROXY/DOWNLOAD_DIR）保存时保留 stored、不落 env 回显；`AppConfig.EnvOverridden` 随 GET/PUT 下发供前端提示。探针：半行、env Cookie、env 下载目录三种场景均 200 且库内值/账号池不变。
- **R-B Windows DSN**：新增 `storage/dsn.go`，POSIX 用 `file:` URI（保留 `?`/`#` 百分号编码），Windows 盘符/UNC 用裸路径；table-driven 单测覆盖 6 种路径形态；`GOOS=windows` 构建通过。
- **R-C 旧库非法下载目录**：`Open` 不再失败，改为降级 `<dataDir>/downloads` + 写回 + 大声 warning；env 非法值运行时降级不写库；`/`、家目录永不登记为可信根（探针实测修复）。
  - **追加加固（复核期发现的边界）**：`config.ValidateDownloadRoot` 现在对候选路径与 `$HOME` **两侧都解析符号链接**（`resolveForCompare`/`samePath`，含 `os.SameFile` 兜底）；`internal/storage/download_root.go` 在 repair/seed/登记/读取四处再做一层加固。软链 HOME（macOS `/tmp`→`/private/tmp`、容器 `/home` 挂载）场景 E2E 验证：启动告警并降级写回、`download_roots` 不含家目录、家目录下文件端点 404、SIGTERM exit 0。
- **R-D `null` PUT 重置配置**：`decodeJSONObject` 对 nil map 返回 400，且库内配置零变化（探针实测）。
- **R-E env 下载目录历史 404 + 保存 400**：启动播种历史/生效根、允许根含 env 生效目录、env 回显不落库；独立实测历史媒体 200、PUT 200。
- **R-F legacy `/` 登记为可信根**：登记前双向校验，读侧过滤非法根；httpapi 对 `currentRoot`/目录浏览根同样过滤。
- **R-I 单条坏推文毒丸**：xclient 解析失败改 `skip+计数+日志`，新增 `MediaFetchStats.LastSeenTweetID/SkippedTweets`；jobs 用 `max(newest, LastSeenTweetID)` 推进游标且只增不减；不可解析媒体不入失败队列；`ErrUnknownMediaType` 判永久；`ErrQueryIDStale` 直接中止并提示升级。
- **R-J 静态 ETag/body 不一致**：ETag 按实际发送字节计算（按编码缓存），identity 更新即刷新；同一 ETag 不再对应两份内容；Vary 只加一次。
- **R-K HEALTHCHECK**：使用 `OPEN_XDOWNLOAD_ADDR` 端口 + `/api/health` + TLS 回退 + start-period。
- **R-L 停机语义**：`manager.Stop()` 超时改为 warning + exit 0（保留不关闭 SQLite 的安全语义），致命 serve 错误仍 exit 1。
- **R-M 运维杂项**：dist 闸门改 `git add -A -- internal/webui/dist && git diff --cached --exit-code`（堵住未跟踪新增盲区）；CI 加 `go mod tidy -diff`；go.mod `x/sys` 转直接依赖；`.gitignore`/`.dockerignore` 覆盖并删除 `httpapi.test`/`server` 两个大二进制；`go mod tidy -diff` 零 diff。

## 3. 其余 P2 / P3 修复清单（按区域）

### storage
Windows DSN、根目录播种/校验/清理、`claim_version`（schema v5）替代时间等值、状态集合单一来源（`status.go` 生成 SQL/触发器/对账）、`next_retry_at` 索引 + Go 计算退避、`PRAGMA optimize`、删除 `user_previous_names` 死表、迁移已应用快速跳过、`ListLibraryDownloadsPage`（SQL LIMIT/OFFSET+COUNT+kind/search/user 过滤+窗口去重+孤儿合并+facet）、`ReserveAutomaticRetryJob`/`PruneAutomaticRetryJobs`、`dropSMB` 迁移测试、并发分页/领取测试、弱断言修复。

### httpapi / httpx
paged 媒体库（不再全量 10 万）、旧数组接口 `X-Result-Truncated`、PUT/GET 配置一致（`ApplyEnvOverrides` 后 Redacted）、海报 `mime.ParseMediaType`+负缓存+single-flight+504/502 区分、压缩 `defer finish`+recover、头部一致（HEAD/GET）、媒体端点不缓冲、2MiB 缓冲上限、多根解析统一、Range 206、`httpx.IsTwimgHost`/`GuardedRoundTripper`（代理模式链路本地防护）、`/api/auth/check` 用临时池非破坏探测（并修真 bug：网络故障误报 ok）。

### xclient / parser
`apiError` 容忍非致命 partial error、`errors` 非数组返回错误、88/`ErrBodyTooLarge`/忙等非瞬时、Client-only 立即返回、连续空页上限 3 次、页间节流、`previousCursor` 修正、Pool 固定 Client 分页、`Pool.FollowUser`、queryId 集中表 + `ErrQueryIDStale`、401/403/WAF 分级、退避抖动、`json.Marshal` variables、按 ID 去重、syndicationToken 移植 V8 算法（黄金值+40 随机 id 对比）、qualityFromURL、Media.ID 统一、删除死代码、补认证头/query/重定向/cursor/故障转移测试。

### jobs
毒丸游标、结构化状态集合收敛、`safeName` 控制字符、MediaCount>0、blocked/muted 计入统计、Skipped 拆分、事件丢弃计数、`ErrUnknownMediaType`/`ErrQueryIDStale` 分类、`pool.FollowUser`、`ReserveAutomaticRetryJob` 接入、`UpdateJobAndGet` 行数校验、panic recover、维护移出调度、Stop 排空、内容哈希并发锁测试、e2e 媒体任务（覆盖率 0→86.4%）与 `cancel`（0→100%）。

### 前端
`api()` per-call timeout + 空响应/204 处理 + AbortSignal 降级；SSE 服务器排序保留 + `sseConnected` 消费（断线提示+重连）+ 终态失效去抖；媒体库真分页 + 搜索 debounce + keepPreviousData + 灯箱跨页；筛选/搜索进 URL；Cookie 批量粘贴 + 半行提示 + 稳定 id；删除幽灵字段/死 prop；CreateJobModal 纯数字 ID；失败态重试；主题配对色与 `--fg-subtle` ≥4.5 对比；Spinner keyframes；Tag/Table/Tree/Pagination/Card/Overlay/Popconfirm aria 与边界；env 覆盖字段标注；`tsconfig.node.json` 删除、preview 代理、dev/preview 回环、sourcemap 显式化、index.html meta。

## 4. 最终验证（Lead 集成）

```
go build ./...                                  ✅
go vet ./...                                    ✅
gofmt -l cmd internal                           ✅ 无输出
go test -race -shuffle=on -count=1 ./...        ✅ 10 包全绿
GOOS=windows GOARCH=amd64 go build ./...        ✅
GOOS=linux   GOARCH=arm64 go build ./...        ✅
go mod tidy -diff                               ✅ 零 diff
git diff --check                                ✅
cd apps/web && npx tsc --noEmit                  ✅
npx vitest run                                  ✅ 27 文件 / 273 用例
npx vite build                                  ✅
npm run build:embedded                          ✅ 可复现（连续两次 sha256 一致）
嵌入 sidecar 与 fresh build 逐字节比对            ✅ 21/21 一致
```

回归探针（/tmp 独立仓库副本，未在仓库落测试文件）：
- 半行 Cookie 保存 → **200**，库内值不变；
- env 注入 Cookie 保存 → **200**，库内账号池不被截断；
- env 下载目录保存 → **200**，env 值不落库（响应含 `envOverridden`）；
- `PUT /api/config` body `null` → **400**，配置零变化；
- 旧库 download_dir=家目录 → 启动成功 + 自动降级写回 + warning；
- Windows 盘符路径 DSN → 不再生成 `file://C:/...`；
- 单条坏推文 → 跳过且 `SkippedTweets=1`、`LastSeenTweetID` 参与游标。

## 5. 产品决策 / 已知残留（不阻塞本轮，需你拍板）

1. `indigo` 的 `--brand-contrast` 对比度上限 4.47（由主题色亮度决定），严格 AA 需调深 indigo `brand-500`——属改主题色，未擅自改。
2. Button primary 的 hover/active 瞬时态在 titanium-light/indigo 等仍低于 4.5（静息态已全部达标）。
3. 首次大库升级仍是单迁移事务（已分批读 + 已应用迁移快速跳过 + `PRAGMA optimize`，未加 SQLITE_BUSY 重试）。
4. `RequeueInterruptedJobs` 仍无 owner/心跳，由单实例文件锁兜底。
5. queryId 具体值仍需真实 Cookie 验证；`/home` 账号名准确性需真实账号端到端确认。
6. Docker 实机构建/HEALTHCHECK、Windows 真机运行、CI 上 `go mod tidy -diff` 的网络行为未在本机验证（本机无 Docker）。

## 6. 提交注意

- `internal/webui/dist` 已重新生成并 **`git add`**（16 A / 16 D / 3 M）；`apps/web` 有 23 个新增文件（测试与 `EnvOverrideNote.tsx`/`useDebouncedValue.ts`），提交时用 `git add -A` 一并入库，否则 CI 的 `npm test`/dist 闸门会红。
- `httpapi.test`、`server` 两个未跟踪大二进制已删除；`.gitignore` 已覆盖 `/httpapi.test`、`/server`。
- 未提交、未推送。
