# 修复复核（第 2 轮）：HTTP / 下载器 / 海报 / 媒体库

- 复核人：`rev-http`（第 1 轮报告 `review/httpapi-httpx-downloader-filestore.md` 的作者）
- 复核对象：当前未提交工作区（`git diff --stat` 已跟踪文件 96 个、+3763/−1403；另有未跟踪测试/产物）
- 复核方式：**以当前代码为唯一事实来源**（逐行读改动后的 `internal/httpapi`、`internal/httpx`、`internal/downloader`、`internal/filestore`，并对照 `internal/storage`、`internal/config` 的契约）；`git diff` 仅用于判断"是否为本轮新引入"。另**用 HEAD（`git archive HEAD` 构建基线二进制 `/tmp/openx-head`）做行为差分**，凡标"回归"者均有前后对照。
- 基线：`go build ./...` / `go vet ./...` / `gofmt -l` 均干净；`go test -count=1 ./...` 10 个包全通过；`go test -race` 在 `httpapi`、`downloader`、`httpx`、`filestore`、`storage`、`jobs` 上通过。
- 状态图例：✅ 已修复（含证据）｜🟡 部分修复/仍有余留｜❌ 未修复｜⚪ 不适用或行为本就如此

---

## 0. 结论摘要

| 复核对象 | 状态 | 一句话 |
|---|---|---|
| REPORT §3.4 十条 | 1 ✅ / 8 🟡 / 1 ❌ | 主要方向都动了，但 4/10 只修了表层，另有 1 条（`P2-10`）完全未动 |
| P1-1（改下载目录 → 历史媒体不可用 + 缓存不失效） | 🟡 | **API 改目录路径修好并实测可用**；但 env 改目录（官方推荐 + Docker 默认）仍 404，且根表启动不播种、不校验、不清理 |
| P1-3（4xx 回传存储/OS 内部错误） | 🟡 | OS/DB 泄漏与 `ErrNoRows→400` 已修；残留 JSON 解码错误原文（泄漏内部类型名） |
| compress 高风险点 | 🟡 | `defer finish` + `recover` 正确且测试覆盖；但 `Vary` 重复、HEAD 分支实际不可达、identity 客户端新增缓冲 |
| 海报高风险点 | 🟡 | 符号链接/root 校验已修且有测试；Content-Type 大小写、负缓存/在途去重仍未做，`stat→ServeFile` TOCTOU 收窄未消除 |
| 下载器高风险点 | 🟡 | 重定向与状态码两项修好且有测试；**初始 URL 主机仍未校验**，代理模式拨号防护的注释仍与实现矛盾 |
| 媒体库分页高风险点 | 🟡 | 服务端分页契约已实现且前端已接入；paged 模式仍固定加载 10 万条 + 每次请求内存过滤，服务端成本未降 |
| **新增回归** | **P1×3 / P2×1 / P3×5** | 最严重三个：`PUT /api/config` body `null` 静默重置配置；env 覆盖下载目录后设置页保存全 400；env 改下载目录历史媒体仍 404 |

**新增 P0：0 条。** 新增 P1：`N1`、`N2`、`N3`。

---

## 1. 逐条状态表（§3.4 十条 + 我的 P1/P2/P3）

### 1.1 REPORT §3.4（HTTP / 下载 / 文件存储）

| # | 第 1 轮结论 | 现状 | 关键证据（当前行号） |
|---|---|---|---|
| 1 | 下载器去掉重定向上限、不校验初始主机、代理模式 SSRF 注释矛盾 | 🟡 | 上限 ✅ `downloader.go:306-309`；scheme ✅ `:311-314`；https→http ✅ `:316-318`；twimg ✅ `:319-321`；**初始 URL 主机 ❌**（`Open` `:289-334` 全程无 host 校验）；**注释仍错 ❌** `httpx.go:140` |
| 2 | 任意 `<400` 当成功 | ✅ | `downloader.go:329` `if response.StatusCode != http.StatusOK`；`TestOpenRejectsResponsesWithoutCompleteMedia` 覆盖 204/304/206 |
| 3 | 压缩 `finish` 未 defer、无 recover、Vary/HEAD 不一致、8MiB 缓冲、`MaxBytesReader` 包装失效 | 🟡 | defer ✅ `compress.go:56-62`；recover ✅ `:67-89`；Vary ✅ 但**重复**（`:48` + `:210`）；HEAD 分支**不可达**（chi 只注册 GET，`HEAD /api/jobs` 实测 405）；8MiB 保留 `:34`；`MaxBytesReader` ✅ `server.go:1310-1318` + `TestCompressedRequestBodyLimitClosesConnection` |
| 4 | 海报缓存路径不校验、Content-Type 大小写、无负缓存/去重、丢弃错误 | 🟡 | root 校验 ✅ `server.go:898-909` + `TestServeDownloadPreviewRejectsPosterSymlinkOutsideDownloadRoot`；Content-Type 仍 `strings.HasPrefix(..., "image/")` ❌ `:977`；无负缓存/在途去重 ❌（只有 4 并发信号量 `:1006-1013`）；错误仍统一 502 且无日志 ❌ `:934-942` |
| 5 | `/api/library/downloads` 无分页、默认 10 万条 | 🟡 | 分页 ✅ `server.go:771-801`、`:846-866`（`items/pagination/counts/users/truncated`，`pageSize≤200`）；前端已接 `GalleryPage.tsx` + `api.ts:322-325`；但 paged 仍 `limit = MaxLibraryDownloadsLimit`（`:774-776`）→ 每请求 10 万行 + `WalkDir` + 内存过滤，成本未降 |
| 6 | env 提示与实现不一致、启动不校验、可配成 `/` | 🟡 | `ApplyEnvOverrides` 覆盖 ✅ `config.go:24-27`；启动校验 ✅ `storage.go:139-146`；文案现已与实际一致 ✅；**但引入 N1/N2 两个新回归** |
| 7 | `PUT /api/config` 部分字段重置 | 🟡 | PATCH 语义 ✅ `server.go:117-159` + `storage.go:923-951`（`{}` no-op、单字段保留已实测）；**`null` body 全量重置 → N3**；未知字段静默忽略（JSON 大小写不敏感匹配，`downloaddir` 会被当成 `downloadDir`） |
| 8 | 扩展名白名单不一致（`.avif/.bin`） | 🟡 | `.avif` ✅ `storage.go:1768`、`server.go:815`、前端 `IMAGE_EXTENSIONS`；`.bin` ❌ 下载器仍可产出（`downloader.go:362-364`）但白名单不含 → `/api/library/file` 403、孤儿不进画廊 |
| 9 | `createLocalDirectoryPath` 0755 + TOCTOU | ✅ | `0700` ✅ `server.go:1580`；`resolvePathForCheck` 二次校验 ✅ `:1573-1579`；`MkdirAll` 仍在校验之后（窗口收窄，本地场景可接受） |
| 10 | 其他 P3 清单 | 🟡 | `filestore.Rename` ✅ `filestore.go:71-73`（+ jobs 侧回滚 `manager.go:1536-1541`）；`isPhotoURL` ✅ `manager.go:1773-1780`；`parseQuality` ✅ `encoding.go:62-75`；`etagMatch` ✅ `static.go:237-244`；`sanitizeFilename` ✅ `downloader.go:548-570`；**`TestWritable` 仍死接口 ❌ `filestore.go:25/80`；`Store` 仍反向依赖 downloader ❌；twimg 白名单仍 5 份 ❌（`downloader.go:355`、`server.go:545`、`server.go:1001`、`manager.go:1779`、`parser/service.go:728`）；压缩 2 份 ❌；海报抓取 2 份 ❌；`RemoveStaleTempFiles` 阈值仍 1h 🟡（`manager.go:61`，但下载超时 10min，1h 已安全）** |

### 1.2 P1-1（我报告原 P1-1 = REPORT P1-1）：改下载目录后历史媒体 403 + 缓存不失效

**结论：🟡 大部分修复，但最关键的部署路径仍坏；且修复自身引入新回归。**

已修好（有实测）：

1. 新增 `download_roots` 表（`storage.go:248`），成功配置变更时把 `current.DownloadDir` 与 `cfg.DownloadDir` 一起登记（`storage.go:962-973`）。
2. `resolveDownloadPath` 依次尝试历史根 + 当前根（`server.go:1089-1110`），`serveDownloadFile`/`serveDownloadPreview` 改用它（`:1165`、`:890`）。
3. 越界/不存在改为 404 且文案中性（`server.go:1077-1086`，实测 404 `文件不在当前下载目录中`）。
4. 配置提交后立即失效 library 缓存（`storage.go:1013`），TTL 从 30 分钟降到 5 分钟（`:71`），并补了 `TestUpdateConfigInvalidatesLibraryDownloadsCache`。

实测（`HOME=/tmp/rcl-home-home`，d1→d2 经 `PUT /api/config`）：

```
roots before: (空)
PUT {"downloadDir":"…/d2"} → 200
roots after : …/downloads  …/d2
GET /api/library/downloads/1/file → 200 "JPEGDATA"
```

仍未修好：

| 缺口 | 证据 |
|---|---|
| **env 改 `OPEN_XDOWNLOAD_DOWNLOAD_DIR` 完全不登记旧根** → 历史媒体永久 404（而错误文案恰恰推荐用 env 改目录、Dockerfile:35 默认就用 env） | 实测：d1 下已有记录 → 用 env 切到 d2 重启 → `download_roots` 仍为 0 行；`/api/library/downloads/1/file` → `404 {"error":"文件不在当前下载目录中"}`；library 列表仍列出该记录（可点但打不开） |
| `download_roots` 启动不播种：全新库/升级库首次启动根表为空，只有"保存过一次配置"才有内容 | `grep download_roots` 全仓仅 3 处：建表 `:248`、写入 `:968`、读取 `:1021`；无 `EnsureConfig`/`Open` 播种。实测首启 `select count(*) → 0` |
| 登记时只校验"新值"，不校验历史值 `current.DownloadDir`（`storage.go:962-973` 用 `for _, root := range []string{current.DownloadDir, cfg.DownloadDir}`） | 见 N4：legacy 的 `/` 会被永久写成可信根 |
| `/api/library/file?path=`（孤儿/ID==0 入口）仍只用当前根 | `server.go:1127` 仍是 `resolveLocalLibraryPath(cfg.DownloadDir, …)`；实测改目录后老根文件 404，而 record 端点 200 |
| gallery 孤儿扫描只遍历当前根 | `storage.go:1615-1619`（`root, _ := filepath.Abs(cfg.DownloadDir)`），改目录后老根孤儿从画廊消失（文件还在） |

### 1.3 P1-3（原 P1-2 = REPORT P1-3）：4xx 泄漏内部错误 / "不存在"误报 400

**结论：🟡 主要风险已修，残留 JSON 解码类错误原文。**

已修好：

- 新增 typed error `storage.UserFacingError{Status,Message,Cause}` + `ErrValidation/ErrNotFound`（`storage.go:28-48`），`writeError` 优先识别（`server.go:1336-1341`）。
- `sql.ErrNoRows` 全局映射 404 `记录不存在`（`server.go:1348-1352`）；`*os.PathError`/`*url.Error`/`interface{Code() int}` 一律改成 500 + `internal error`（`:1342-1347`、`:1354-1357`）。
- 列目录/建目录改为固定文案 + 服务端日志（`server.go:172-187`、`:225-247`）。
- 归档计划："不存在"→404（`server.go:447-449`、`:497` 起经 writeError 映射）。

实测：

```
POST /api/archive-schedules/999999/run → 404 {"error":"记录不存在"}
PUT  /api/archive-schedules/999999    → 404 {"error":"计划不存在"}（此前 400 sql: no rows）
GET  /api/local-directories?path=/nonexistent-dir-xyz → 400 {"error":"无法读取目录"}
GET  /api/local-directories?path=/etc               → 400 {"error":"无法读取目录"}
```

仍未修好（P3 级，信息量有限但仍是内部细节）：

```
POST /api/archive-schedules {"intervalMinutes":"abc"}
 → 400 {"error":"json: cannot unmarshal string into Go struct field archiveScheduleRequest.intervalMinutes of type int"}
POST /api/jobs {"kind":123,"input":"alice"}
 → 400 {"error":"json: cannot unmarshal number into Go struct field jobRequest.kind of type storage.JobKind"}
GET  /api/jobs/abc/files → 400 {"error":"strconv.ParseInt: parsing \"abc\": invalid syntax"}
POST /api/jobs 空 body   → 400 {"error":"EOF"}
```

根因：`decodeJSON` 的错误仍走 `writeError(400, err)` 的 4xx 兜底分支原样回传（`server.go:1358-1360`，注释自认"涉及内部错误的端点应先包装为 UserFacingError"）。建议 `decodeJSON` 统一包成 `ErrValidation("请求体格式无效：" + 友好文案)`，或对 `*json.SyntaxError/UnmarshalTypeError` 单独脱敏。

---

## 2. 四个高风险点结论

### 2.1 压缩中间件（`internal/httpapi/compress.go`）

- ✅ `finish()` 已 `defer`，且 panic 路径不会二次 finish；`serveWithRecovery` recover 后重置 `sniffCompressWriter` 状态并写入 500（`:71-88`），`TestCompressJSONRecoversWithoutPublishingPartialJSON` 覆盖"不泄露半截 JSON"。
- ✅ `MaxBytesReader` 包装失效已修：`decodeJSON` 沿 `Unwrap()` 找到底层 writer（`server.go:1310-1317`），`TestCompressedRequestBodyLimitClosesConnection` 断言连接被关闭。
- 🟡 **`Vary: Accept-Encoding` 被重复添加**：中间件无条件 `Add`（`:48`），`finish()` 压缩时又 `Add` 一次（`:210`）。实测压缩响应带 2 个 `Vary: Accept-Encoding`（RFC 上等价于去重合并，但属重复头，建议 `finish` 改 `Set` 或判重）。
- 🟡 **HEAD 分支实际不可达**：路由全部用 chi `r.Get` 注册，`HEAD /api/jobs` 实测 `405 Allow: GET, POST`（基线 HEAD 也是 405，非回归）。因此 `finishHead()`（`:216-226`）只会在 405/错误响应上跑，"HEAD 与 GET 头一致"的目标并未达成 → 要么补 HEAD 路由，要么删掉这段死代码。
- 🟡 8 MiB 缓冲仍在（`:34`），且 `compressJSON` 不再跳过 `encoding==""` 的请求（旧实现直接 passthrough），identity 客户端（curl/脚本）现在也会被缓冲最多 8MiB 后才输出首字节——无鉴权场景下这是新的一点点放大面（见 N7）。

### 2.2 海报（`serveDownloadPreview` / `fetchRemotePoster`）

- ✅ 符号链接/root 逃逸已修：`posterPath` 先 `withinAllowedRoot(posterPath, []string{archiveRoot})`（`:898`），再 `EvalSymlinks` 并二次校验（`:904-909`），越界 403；有独立测试 `TestServeDownloadPreviewRejectsPosterSymlinkOutsideDownloadRoot`。
- ✅ `archiveRoot` 现在来自 `resolveDownloadPath` 命中的那个根（`:890-894`），历史根的海报也能正确定位。
- 🟡 `stat → ServeFile` 仍是两次打开（TOCTOU 窗口收窄但未消除）；`serveDownloadFile`/`serveLibraryFile` 同型。
- ❌ Content-Type 判断仍大小写敏感（`:977` `strings.HasPrefix(..., "image/")`），`IMAGE/JPEG` 会 415。
- ❌ 无失败负缓存、无 single-flight：同一张缺失海报并发请求仍各自回源（只有 4 个并发名额 `:1006-1013`）。
- ❌ 回源失败仍统一 502 `下载视频预览图失败` 且**不写日志**，丢掉了超时/过大等可诊断信息（`server.go` 全文 `log.Printf` 只有 6 处，均与海报无关）。

### 2.3 下载器（`internal/downloader/downloader.go`）

- ✅ 重定向策略 4 项里补齐 3 项：10 次上限（`:307`）、仅 http/https（`:311-314`）、https→http 降级（`:316-318`）、twimg 白名单（`:319-321`），有 `TestOpenStopsTwimgRedirectLoop`。
- ✅ 状态码严格 200（`:329`），204/304/206 全部拒绝，有 `TestOpenRejectsResponsesWithoutCompleteMedia`。
- ❌ **初始 URL 主机仍未校验**：`Open`（`:289-334`）对 `requestURL` 不做 `isAllowedTwimgHost` 检查，isAllowedTwimgHost 只出现在 `MediaIdentity/NormalizeMediaURL`（`:483`、`:536`）和重定向回调。调用侧（`httpapi.prepareJob:592-599`）只覆盖 API 创建任务，内部任务/解析产出的 URL 不再有第二道防线。
- ❌ `httpx.go:140` 注释仍写"无论哪种配置，底层拨号都经过链路本地地址防护"——配置代理后 `DialContext` 拨的是代理地址，真实目标不受 `BlockedIP` 检查（`:82-100` 只校验拨号目标）。实现未变、注释未改，属于"知道但没修"。

### 2.4 媒体库分页（`listDownloads` / `ListLibraryDownloadsWithStatus`）

- ✅ 契约实现完整：`?page&pageSize` → `{items, pagination, counts, users, truncated}`（`server.go:846-866`），`pageSize` 上限 200，`page` 超界夹到末页；`kind/user/search` 服务端过滤并按 `FilePath` 去重（`:799-853`）；前端 `GalleryPage` 已改用 `getLibraryDownloadsPage`（pageSize 60）并展示 `truncated` 告警。
- ✅ 顺带修掉一个真 bug：旧实现用"已被 limit 截断的 items"当 `known` 集合，超过上限时已记录文件会被误判成孤儿（新实现 `storage.go:1618-1624` 用全量 `SELECT file_path FROM downloads`，并有 `TestLibraryLimitDoesNotTurnRecordedFilesIntoOrphans`）。
- 🟡 **服务端成本没降**：paged 分支把 limit 硬设为 100000（`server.go:774-776`），仍然一次查询 10 万行 + 全目录 `WalkDir` + 每次请求 O(N) 内存过滤/计数；只是响应体变小。缓存（5 分钟 TTL + `libraryBuildMu` 单飞，`storage.go:1560-1642`）能挡住重复请求，但归档期间缓存被频繁失效时仍会反复做全量。
- 🟡 非分页路径（`?limit=`）默认值从 10 万变成 100（`server.go:773`），且空结果仍是 `null` 而非 `[]`——实测基线 HEAD 同样返回 `null`，**不是回归**，且前端已不再调用该路径（`getLibraryDownloads` 无调用点）。

---

## 3. 新发现（回归）——按严重度

### [N1][P1] env 覆盖 `OPEN_XDOWNLOAD_DOWNLOAD_DIR` 后，历史媒体永久 404（P1-1 的修复没覆盖官方推荐路径）

- 位置：`internal/config/config.go:24-27`（本轮新增 env → DownloadDir 覆盖）、`internal/storage/storage.go:962-973`（只在 `updateConfig` 里登记根）、`internal/httpapi/server.go:1089-1110`（只信任 `download_roots` + 当前根）。
- 证据（实测，d1 里已有记录，用 env 重启指向 d2）：

```
download_roots: 0 行
GET /api/library/downloads/1/file → 404 {"error":"文件不在当前下载目录中"}
GET /api/library/file?path=<d1>/archive/alice/x.jpg → 404 同一文案
GET /api/library/downloads（列表）→ 仍返回该记录（用户可点、打不开）
```

- 影响：`OPEN_XDOWNLOAD_DOWNLOAD_DIR` 是项目自己推荐的"指向其他位置（例如外置磁盘）"手段（`server.go:1536` 文案），也是 Dockerfile:35 的默认配置方式。按此建议迁移磁盘 → 整个媒体库历史打不开，比修复前的 403 更难排查（404 文案还暗示"文件不在当前目录"）。
- 建议：启动时把 `GetConfig().DownloadDir` 播种进 `download_roots`；env 生效值与库值不一致时，把库内旧值也登记（env 变化无法回溯历史 env 值，至少在文档/启动日志提示）。

### [N2][P1] env 覆盖下载目录后，设置页保存一律 400（httpapi 侧 `validateDownloadDir` 参与，已确认为校验链路问题）

- 位置：`internal/httpapi/server.go:143-148`（`validateDownloadDir(validationCfg.DownloadDir, localDirectoryAllowedRoots(stored))`）、`:1421-1432`（allowedRoots = 家目录 + **stored** 的 downloadDir）、`internal/storage/storage.go:880-917`（`GetStoredConfig` 不含 env，`GetConfig` 才含）。
- 证据（实测；`GET` 显示 env 值 `…/b`，stored 为 `…/a`）：

```
GET  /api/config                        → {"downloadDir":"/tmp/rcj-b", …}（env 值）
PUT  /api/config  （前端全量对象，含 /tmp/rcj-b）
     → 400 {"error":"下载目录不允许：仅限家目录或当前下载目录范围内；如需指向其他位置（例如外置磁盘），请用 OPEN_XDOWNLOAD_DOWNLOAD_DIR 环境变量启动"}
PUT  /api/config {"maxFilenameLength":150}   → 200，但响应里 downloadDir 是 stored 值 /tmp/rcj-a（与 GET 不一致）
```

- 与 Lead 提供的跨模块回归的一致性：**是**。httpapi 的 `validateDownloadDir` 明确参与（校验快照取 `GetStoredConfig`，展示快照取 `GetConfig`）；前端 `updateConfig(config)` 是 `JSON.stringify(整个 AppConfig)`（`api.ts:385-388`），必然把 env 值原样回传 → 400。`OPEN_XDOWNLOAD_ADDITIONAL_COOKIES` 那条同属"展示用 env 值、校验/落库用 stored 快照"的家族，但涉及 Cookie 占位符链路（`config.ResolveSubmission`），我未逐条复核。
- 基线对照：HEAD 版 `ApplyEnvOverrides` 不覆盖 DownloadDir，GET 返回 stored 值，PUT 200 —— 所以这是本轮新增的回归。
- 建议：校验允许根时把 env 生效值也算进去（`localDirectoryAllowedRoots(GetConfig())` + 仍以 stored 作"缩小校验"），或对 `downloadDir == 当前生效值` 的情况直接跳过校验；同时让 `UpdateConfigPatch` 的响应也走 `GetConfig()`，消除 GET/PUT 不一致（前端 `pendingSavedConfigKey` 现在只能靠 3s 超时兜底，`ConfigForm.tsx:106` 有相关注释）。

### [N3][P1] `PUT /api/config` body 为 JSON `null` → **200 且静默重置全部非密配置**

- 位置：`internal/httpapi/server.go:124`（`fields := map[string]json.RawMessage{}`）→ `decodeJSON`（`:1307-1327`）对 `null` 解出 **nil map** → `:129-136` 用 `json.Marshal(nil)`（`"null"`）反解进 `cfg` 时为 no-op、校验通过（校验对象 = stored）→ `:154` `UpdateConfigPatch(ctx, nil)` → `internal/storage/storage.go:943` `if fields != nil` 为 false → **走 `cfg = config.AppConfig{}` 零值分支** → `ResolveSubmission(零值, current)` 归一化出一组默认值写入。
- 证据（实测，DB 前置：`archive | AUTH_A | [{"id":"cookie-0","E1","E2"}] | http://user:pass@… | incremental=1 | max=200`）：

```
PUT /api/config  -d 'null'  → 200
DB after: /tmp/rcn-cwd/downloads | AUTH_A | (空) | (空) | 0 | 120
```

- 影响：一次 `null` body 就把 **downloadDir 重置为默认目录、清空 additionalCookies（备用账号凭据丢失）、清空 proxyUrl、重置 incrementalArchive/maxFilenameLength**；主 auth/ct0 因 `ResolveSubmission` 从 stored 还原而幸存。服务无鉴权，本机任意脚本/误用客户端即可触发；且旧版会把 `null` 判成 400 且不改配置（基线实测 400 + DB 未变）→ 明确回归，属 P2-7"部分字段重置"家族里最坏的一条。
- 建议：`decodeJSON` 到 `map` 后若为 nil 或空则直接 400（或按空 patch 处理）；storage 侧把 `fields == nil` 与"零值全量替换"拆开（例如用显式 `fullReplace` 布尔，或让 `UpdateConfigPatch` 拒绝 nil）。

### [N4][P2] legacy 库 `download_dir="/"`：新版本直接拒绝启动；若用 env 兜底启动，则任意配置保存会把 `/` 永久登记为可信根

- 证据 A（启动）：DB `download_dir='/'`、无 env → `storage.Open` 调 `config.ValidateDownloadRoot`（`storage.go:143`、`config.go:43-56`）→ 进程退出码 1，日志 `open storage: 下载目录不能是文件系统根目录或家目录本身`。基线 HEAD 可正常启动（实测 `GET /api/health → 200`）。
- 证据 B（登记未校验历史根）：DB `download_dir='/'` + env 指向合法目录 → 启动成功（env 覆盖生效）；执行任意配置保存 → `storage.go:962-973` 把 `current.DownloadDir`（`/`）和 `cfg.DownloadDir` 一起 `INSERT`, 实测 `download_roots = /, /tmp/rcx-dl/newsub`；随后对一条 `file_path=/etc/hosts` 的下载记录请求 `/api/library/downloads/1/file` → **200 并返回 /etc/hosts 内容**（构造记录后）。
- 影响：旧版允许 `OPEN_XDOWNLOAD_DOWNLOAD_DIR=/` 首次启动把 `/` 写进库（`defaultDownloadDir()` 老代码就读 env，`config.go:166-176`），升级后这类库或拒绝启动、或把 `/` 固化成"历史下载根"，使 record 端点对任意记录的绝对路径都放行（也同时让海报的 root 校验对 `/` 失效）。利用需要 downloads 表里存在记录（正常只由下载器写入），所以定 P2 而非 P1；但"可信根永不过期、无清理、登记时不做 `ValidateDownloadRoot`"是实打实的加固退化。
- 建议：登记前对每个 root 调 `config.ValidateDownloadRoot`（非法则跳过并记日志）；启动时若生效目录合法但 stored 目录非法，主动把 stored 值修正/清空而不是静默保留。

### [N5][P3] 压缩响应带两个 `Vary: Accept-Encoding`

`:48`（中间件无条件 Add）+ `:210`（`finish()` 压缩时 Add）。实测：

```
Content-Encoding: gzip
Vary: Accept-Encoding
Vary: Accept-Encoding
```

建议 `finish()` 改 `Set`（或中间件已加则跳过）。

### [N6][P3] `finishHead()` 是死代码（HEAD 在路由层已 405）

chi 的 `r.Get` 不注册 HEAD，实测 `HEAD /api/jobs → 405 Allow: GET, POST`（基线同样 405，所以"HEAD 头不一致"并非回归，但本轮的 `finishHead` 修复无法生效）。要么给只读端点补 HEAD，要么删除该分支以免误以为已支持。

### [N7][P3] identity 客户端现在也被缓冲最多 8 MiB

旧 `compressJSON` 在 `encoding == ""` 时直接 `next.ServeHTTP`（HEAD 版 `compress.go:42-52`）；新实现一律构造 `sniffCompressWriter`（`:53-63`）以便统一 `Vary`/HEAD。副作用：不带 `Accept-Encoding` 的客户端（curl、脚本、部分内网反代）的 JSON 响应在 8MiB 以内被整体缓冲后才开始输出，首字节延迟与无鉴权内存放大面都略微变差。

### [N8][P3] `download_roots` 只增不减、无校验、无清理

每次配置保存 `INSERT OR IGNORE` 两条（`storage.go:962-973`），没有任何删除入口；同时 `resolveDownloadPath` 对每个请求遍历所有根并对每个根 `EvalSymlinks`（`server.go:1093-1107`），历史根越多，媒体请求的 syscall 越多。配合 N4 会长期保留无效根。

### [N9][P3] `/api/library/file` 与 record 端点多根语义不一致

`serveLibraryFile`（`server.go:1113-1141`）仍只按 `cfg.DownloadDir` 解析（`:1127`），改目录后"孤儿文件"（`ID==0`、前端用 `fileUrl`）老根路径 404，而同一文件有记录时走 `/api/library/downloads/{id}/file` 是 200；同时 gallery 孤儿扫描只看当前根（`storage.go:1615-1619`）→ 老根孤儿直接从画廊消失。不影响有记录的媒体，但会让"历史目录里的文件"看起来像丢了。

---

## 4. 复核未覆盖 / 明确未复核

- `P2-10`（可观测性：request id、handler 超时、结构化日志、`writeJSON` 编码错误）**仍未做**，我只做了 `grep` 确认（`httpapi` 中无 timeout/request-id 中间件，`writeJSON:1329-1333` 仍 `_ = Encode`）→ ❌。
- 前端 `GalleryPage`/`CookieSettings`/`ConfigForm` 的联动行为只核到与 HTTP 契约相关的部分（分页参数、错误分支、`__CLEAR__`），未系统性复核前端测试。
- `OPEN_XDOWNLOAD_ADDITIONAL_COOKIES` 与库值不一致导致的 400：仅确认属同一"展示用 env、校验用 stored"家族（`ApplyEnvOverrides` `config.go:24-40` vs `GetStoredConfig` `storage.go:880-917`），未逐条跑通 Cookie 保存链路（属 cookie/前端 owner）。
- `httpx.Transport` 的凭据明文驻留内存、`ForceAttemptHTTP2` 缺失、`filestore.Exists` 语义、`origin.go` loopback 例外：均确认代码未变，归入既有 P3，未再展开。

---

## 5. 建议 Lead 的处理顺序（按"回归优先"）

1. **N3**（`PUT null` 配置重置）：1-3 行改动 + 回归测试，收益最大（防凭据丢失）。
2. **N2 + N1**（env 覆盖链路）：allowedRoots 纳入生效值；启动播种 `download_roots` + env 变化时登记旧值。这是"官方推荐操作会弄坏媒体库/设置页"的组合，用户可见度最高。
3. **N4**（legacy `/` 库）：登记根前 `ValidateDownloadRoot`；启动失败时给出可执行的修复提示（或自动回落到 env/默认目录并改库）。
4. §3.4 剩余项：`decodeJSON` 错误脱敏（1.3 残留）、海报 Content-Type `EqualFold`、`.bin` 白名单对齐、初始 URL 主机校验（1.1）、`Vary` 去重、删/补 HEAD。
5. 结构性去重（twimg 白名单 5 份、压缩 2 份、海报抓取 2 份）可单独排期，不急。
