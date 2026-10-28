# 代码审查 · internal/httpapi + httpx + downloader + filestore

审查范围：`internal/httpapi`（server/bootstrap/compress/origin 及全部测试）、`internal/httpx`（httpx/fetch/encoding 及全部测试）、`internal/downloader`（downloader 及全部测试）、`internal/filestore`（filestore 及测试）。

约束遵守说明：仅阅读源码，未查看 git 历史/diff，未阅读 `README.md` 与 `docs/`。为核对跨模块契约，额外只读了 `internal/jobs/manager.go`、`internal/jobs/preview.go`、`internal/jobs/events.go`、`internal/storage/storage.go`、`internal/config/config.go`、`internal/storage/models.go`、`cmd/server/{main,listen,static}.go`、`Dockerfile`/`docker-entrypoint.sh` 中与本范围直接相关的代码段（均在文中标注“跨模块”）。本次只新增了本报告文件，未修改任何源码/测试/配置。

基线验证：`go vet ./internal/{httpapi,httpx,downloader,filestore}/...` 无输出；`go test -count=1 ./internal/{httpapi,httpx,downloader,filestore}/...` 全部通过。

## 0. 阅读清单

| 文件 | 行数 | 已读完 |
|---|---|---|
| internal/httpapi/server.go | 1395 | ✅ 1–1395（分 4 段：1–360 / 361–720 / 721–1080 / 1081–1395） |
| internal/httpapi/bootstrap.go | 122 | ✅ 1–122 |
| internal/httpapi/compress.go | 199 | ✅ 1–199 |
| internal/httpapi/origin.go | 111 | ✅ 1–111 |
| internal/httpapi/server_test.go | 937 | ✅ 1–937（分 2 段：1–480 / 481–937） |
| internal/httpapi/bootstrap_test.go | 109 | ✅ 1–109 |
| internal/httpapi/compress_test.go | 231 | ✅ 1–231 |
| internal/httpapi/origin_test.go | 152 | ✅ 1–152 |
| internal/httpx/httpx.go | 192 | ✅ 1–192 |
| internal/httpx/fetch.go | 43 | ✅ 1–43 |
| internal/httpx/encoding.go | 72 | ✅ 1–72 |
| internal/httpx/httpx_test.go | 45 | ✅ 1–45 |
| internal/httpx/fetch_test.go | 45 | ✅ 1–45 |
| internal/httpx/encoding_test.go | 41 | ✅ 1–41 |
| internal/httpx/blocked_test.go | 39 | ✅ 1–39 |
| internal/downloader/downloader.go | 649 | ✅ 1–649（分 2 段：1–330 / 331–649） |
| internal/downloader/downloader_test.go | 632 | ✅ 1–632 |
| internal/downloader/identity_test.go | 188 | ✅ 1–188 |
| internal/downloader/normalize_test.go | 72 | ✅ 1–72 |
| internal/filestore/filestore.go | 95 | ✅ 1–95 |
| internal/filestore/filestore_test.go | 62 | ✅ 1–62 |
| **合计** | **5431** | **21/21 文件逐行读完** |

## 1. P0 发现

**本范围内未发现可远程触发、且不依赖本地写权限的 P0（安全漏洞/数据损坏/必然崩溃）。**

为避免“没找到 = 没查”，下面列出按 P0 标准逐项验证后判定不成立的候选，并给出复核要点：

| 候选 P0 | 结论 | 关键证据 |
|---|---|---|
| 媒体库路径穿越（`..`、前缀匹配 `/data` vs `/data-evil`） | 不成立 | `server.go:909-935` 先用 `filepath.Rel` 判 `..` 前缀（不是字符串前缀比较），再用 `EvalSymlinks` 对 root/file 二次 Rel 校验；URL 编码的 `..%2f` 解码后同样被 Clean+Rel 拦截；有测试 `server_test.go:779-800` |
| 允许目录 symlink 逃逸（local-directories） | 不成立 | `server.go:1250-1272` 用 `resolvePathForCheck`（已存在前缀 EvalSymlinks + 缺失尾部拼回）比较；`createLocalDirectoryPath` 在 MkdirAll 前校验；有测试 `server_test.go:265-285` |
| 海报回源 SSRF / 开放重定向 | 不成立（白名单内） | `server.go:787-791` 校验初始 URL 主机；`server.go:795-796` `RestrictRedirects(isAllowedPreviewHost)`；`server.go:840-855` 仅接受 2xx + `image/` 且 `CopyLimited` 超限不发布 |
| 任意路径写入（下载发布） | 不成立（无 `..`/分隔符逃逸） | `downloader.go:542-548` 去掉 `/ \ : * ? " < > \|` 与 CR/LF；`downloader.go:110-140` 临时文件写完后经 `os.Link`（不覆盖）或 O_EXCL 占位 + rename 发布 |
| 未捕获 panic 导致进程崩溃 | 不成立 | 无 recover 中间件，但 `net/http` 按连接 recover；本范围内 handler 未发现可由请求输入触发的 nil 解引用（`publish`/`withMeta` 均有 nil 守卫）。可观测性问题见 P2-10 |
| CSRF 状态变更 | 不成立（浏览器场景） | `origin.go:21-48` Origin/Sec-Fetch-Site/Host 三重校验；无 body 的 POST 也被 `Sec-Fetch-Site`/`Origin: null` 覆盖；有测试 `origin_test.go:9-97,127-142` |

真正的风险集中在 P1/P2（配置一致性、变更后媒体库断链、错误信息泄漏、下载器重定向/状态码边界、压缩中间件健壮性）。

## 2. P1 发现

### [P1-1] 修改“下载目录”后，全部历史归档媒体在 HTTP 层变成 403，媒体库整体不可用（无迁移、无回退、无提示）

- 位置：`internal/httpapi/server.go:1002-1006`（`serveDownloadFile`）、`internal/httpapi/server.go:769-773`（`serveDownloadPreview`）、`internal/httpapi/server.go:958-971`（`serveLibraryFile`）、`internal/httpapi/server.go:940-946`（`writeResolveError`）
- 证据：

```go
// server.go:1002
filePath, err := resolveLocalLibraryPath(cfg.DownloadDir, record.FilePath)
if err != nil {
    writeResolveError(w, err)
    return
}
...
// server.go:940
func writeResolveError(w http.ResponseWriter, err error) {
    if errors.Is(err, os.ErrNotExist) {
        writeError(w, http.StatusNotFound, err)
        return
    }
    writeError(w, http.StatusForbidden, err)   // ← 旧目录中的历史文件落到这里：403
}
```

```go
// server.go:918-933（resolveLocalLibraryPath 用“当前”下载目录做根）
rel, err := filepath.Rel(rootAbs, filePath)
...
resolvedRel, err := filepath.Rel(resolvedRoot, resolvedFile)
if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) || filepath.IsAbs(resolvedRel) {
    return "", errFileOutsideDownloadDir
}
```

```go
// server.go:117-146 updateConfig：校验并写库，但整个 handler 没有任何路径迁移/重写钩子
updated, err := s.store.UpdateConfig(r.Context(), cfg)
```

- 影响：
  - `downloads.file_path` 是下载时的绝对路径（跨模块 `jobs/manager.go:791-798` `FilePath: result.Path`，`filestore.localStore.Root()` 为 `filepath.Abs(cfg.DownloadDir)`），改配置不会改写它。
  - 用户只要在设置页把下载目录改到别处（这是 UI 明确提供的功能），下一次打开媒体库：`/api/library/downloads/{id}/file`、`/api/library/downloads/{id}/preview`、`/api/library/file?path=` 全部对旧记录返回 **403 `文件不在下载目录内`**。前端 `apps/web/src/pages/GalleryPage.tsx:51` 对 `id>0` 的记录正是用 `/api/library/downloads/{id}/file`，表现为整馆裂图/无法播放。
  - 正在运行的归档任务会在改配置前后分别写入两个目录，记录路径混杂，问题持续存在。
  - 对比：目录改名场景有专门迁移（跨模块 `jobs/manager.go:1459-1467` 调 `RewriteDownloadPathPrefix`），说明项目内已有该模式，但下载目录变更这条路径漏掉了。
- 建议（任选其一，建议前两项）：
  1. `updateConfig` 检测 `DownloadDir` 变化时，在同一事务里把 `downloads.file_path` 的旧前缀重写为新前缀（复用 `RewriteDownloadPathPrefix`），或
  2. 在记录中保存“归档根”快照（如 `downloads.storage_root`），`resolveLocalLibraryPath` 用“该记录的根 + 当前根（兼容）”解析，避免旧记录因全局根变化而失效；
  3. 最低限度：在 `writeResolveError` 对“记录存在但越界”返回 404/409 并附可读原因，前端提示“下载目录已变更，旧文件不在当前目录下”，禁止静默 403。

### [P1-2] 4xx 响应把存储/OS 层内部错误原文回传客户端，且少量端点把“不存在”当 400（信息泄漏 + 状态码语义错误）

- 位置：`internal/httpapi/server.go:1166-1175`（`writeError`）；触发点：`server.go:405-425`（`updateArchiveSchedule`）、`server.go:390-403`（`createArchiveSchedule`）、`server.go:441-453`（`runArchiveSchedule`）、`server.go:148-169`（`listLocalDirectories`）
- 证据：

```go
// server.go:1166
func writeError(w http.ResponseWriter, status int, err error) {
    log.Printf("httpapi %d: %v", status, err)
    if status >= http.StatusInternalServerError {
        writeJSON(w, status, map[string]string{"error": "internal error"})
        return
    }
    writeJSON(w, status, map[string]string{"error": err.Error()})   // ← 4xx 原文回传
}
```

```go
// server.go:418-421：存储层任何错误（含纯 DB 错误）都被映射为 400 并原文回传
updated, err := s.store.UpdateArchiveSchedule(r.Context(), schedule)
if err != nil {
    writeError(w, http.StatusBadRequest, err)
    return
}
```

```go
// 跨模块 storage.go:1166-1168：id 不存在时返回 sql.ErrNoRows 原文
if err := tx.GetContext(ctx, &current, `SELECT * FROM archive_schedules WHERE id = ?`, schedule.ID); err != nil {
    return ArchiveSchedule{}, err
}
```

```go
// server.go:163-167：列目录失败时把 os 错误（含绝对路径）作为 400 回传
listing, err := localDirectoryListingForPath(dir)
if err != nil {
    writeError(w, http.StatusBadRequest, err)
```

- 影响：
  - `PUT /api/archive-schedules/{不存在的id}` 会返回 `400 {"error":"sql: no rows in result set"}`；计划相关写库失败会回传 `database is locked (5) (SQLITE_BUSY)` 之类的 SQLite 原文；`GET /api/local-directories?path=不可读路径` 会回传 `open /Users/xxx/...: permission denied`（完整绝对路径）。
  - 本服务无鉴权（见 §5-1），任何能访问端口的人都能用这些响应推断内部结构；同时“不存在”被误报为 400，前端无法区分业务校验失败与实体不存在（`getJob`/`cancelJob`/`retryJob` 都有正确 404 映射，这里不一致）。
- 建议：
  - 在 storage/jobs 层定义 typed error（如 `ErrValidation`/`ErrNotFound`/`ErrConflict`/其他），httpapi 只对 `ErrValidation`/`ErrConflict` 回传 `err.Error()`，`ErrNotFound` → 404，其余 → 500 脱敏；
  - `updateArchiveSchedule`/`runArchiveSchedule` 对 `sql.ErrNoRows` 显式映射 404（可参照 `server.go:611-620` 的写法）；
  - `listLocalDirectories` 的 OS 错误改为固定文案 + 服务端日志。

## 3. P2 发现

### [P2-1] 下载器重定向策略三处缺陷：无次数上限、不校验初始 URL 主机、代理模式下拨号层 SSRF 防护不覆盖真实目标

- 位置：`internal/downloader/downloader.go:303-318`（自建 `CheckRedirect`）、`internal/httpx/httpx.go:136-172`（Transport）、`internal/httpx/httpx.go:147-155`（Proxy+DialContext 组合）、`internal/httpx/httpx.go:81-116`（guardedDial）
- 证据：

```go
// downloader.go:306-317
CheckRedirect: func(req *http.Request, via []*http.Request) error {
    if len(via) == 0 {          // ← 该分支不可达（via 至少 1 项），且全程没有 len(via) >= N 的上限
        return nil
    }
    if req.URL.Scheme != "http" && req.URL.Scheme != "https" { ... }
    if !isAllowedTwimgHost(req.URL.Hostname()) { ... }
    return nil
},
```

```go
// httpx.go:147-155
transport := &http.Transport{
    Proxy:                 http.ProxyFromEnvironment,
    DialContext:           guardDialContext,
    ...
}
if proxyURL != "" {
    transport.Proxy = http.ProxyURL(parsed)
}
```

```go
// httpx.go:134-135 的注释声称：
// "无论哪种配置，底层拨号都经过链路本地地址防护。"
```

- 影响：
  - 提供 `CheckRedirect` 后 `net/http` 的默认“最多 10 次重定向”策略不再生效，`Open` 也没有自行补上限：上游（或本地代理/中间人）返回 302 环时，一次下载会把连接挂到 `http.Client.Timeout`（`New()` 默认 10 分钟）为止，并被 jobs 反复重试。
  - `Downloader.Open` 只校验“重定向目标”主机，不校验 `rawURL` 初始主机；而 `mediaURL` 的 twimg 白名单校验只存在于 `prepareJob` 的 `media_url` 任务（`server.go:556-563`），归档路径的媒体 URL 直接来自 X API/解析结果（跨模块 `jobs/manager.go:757` 调 `SaveMedia`）。任何未来把非 X 来源 URL 传进下载器的调用点都不再有主机约束。
  - 配置代理后，`DialContext` 连接的是**代理地址**，链路本地地址防护只检查代理 IP，真实目标由代理解析连接 —— “无论哪种配置都经防护”的注释与实现不符；云上“本机代理 + 169.254.169.254 元数据”场景下防护实际失效。
- 建议：
  - `Open` 中加 `if len(via) >= 5 { return errors.New("重定向次数过多") }`，或直接复用 `httpx.RestrictRedirects` 并补次数上限；
  - 下载入口校验初始 URL 主机在 twimg 白名单内（与重定向校验对称）；
  - 修正 httpx 注释，明确“代理模式下不校验目标”；如需强防护，在调用方对目标 URL 做主机/IP 白名单。

### [P2-2] 下载器把任意 <400 状态当成功：3xx/204/304 可发布 0 字节文件，且之后因“大小相等”被永久跳过；无 Content-Length 时的替换语义依赖调用方回调，API 默认不安全

- 位置：`internal/downloader/downloader.go:319-331`（状态判断）、`downloader.go:87-105`（跳过/替换决策）、`downloader.go:154-176`（`existingFileState`）
- 证据：

```go
// downloader.go:323-330
if response.StatusCode >= 400 {          // ← 3xx/204/304/1xx 全部按成功处理
    defer response.Body.Close()
    payload, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
    return nil, &HTTPStatusError{...}
}
return response, nil
```

```go
// downloader.go:169-175
if contentLength < 0 {
    return Result{}, false, true, nil     // ← 未知长度 → 允许替换已有文件
}
...
return Result{Path: path, Bytes: info.Size(), Skipped: true}, true, false, nil
// 完整判定只看 info.Size() == contentLength，不校验内容
```

- 影响：
  - 若上游返回 `204 No Content`/`304 Not Modified`/3xx（Go 客户端仅在 `ErrUseLastResponse` 时才会把 3xx 交回，但 304/204/300 可直达），`io.Copy` 得到 0 字节 → 发布 0 字节文件 → `jobs` 写入 `bytes: 0` 的记录；后续重试 `existingFileState` 判定 `size == ContentLength` 为“完整”并 `Skipped`，0 字节文件被永久沿用。
  - `contentLength < 0`（常见于分块传输或 Go 自动解压 gzip 后）时 `existingFileState` 一律返回 `replaceExisting=true`；当前唯一生产调用点（跨模块 `jobs/manager.go:745-756`）设置了 `ExistingFileOwner` 才不会误删他推文同尺寸文件。下载器 API 的默认 `Options{}` 语义是“宁可复用小尺寸相同文件、乃至删除未知长度文件”，这个安全边界完全由调用方维持，属于契约脆弱点（`Options.ExistingFileOwner` 文档已说明，但没有强制/测试兜底）。
- 建议：
  - 状态码改为 `if status < 200 || status >= 300` 报错（至少 `status == 204 || status == 304` 直接报错）；发布前校验 `bytes > 0`（或加 `MinMediaBytes`）；
  - `ExistingFileOwner == nil` 时禁止 replace 语义（把 nil 当作“无主文件，冲突即后缀”），让安全默认成立；
  - 补测试：204/304、无 Content-Length + 已有完整文件、0 字节 body。

### [P2-3] 压缩中间件健壮性：`finish()` 未 defer、全站无 recover、8MiB/请求缓冲放大、`Vary`/HEAD 处理不一致

- 位置：`internal/httpapi/compress.go:42-57`（入口）、`compress.go:161-182`（`finish`）、`compress.go:170-181`（压缩分支头）、`server.go:59-102`（`Routes` 无 recover）
- 证据：

```go
// compress.go:53-56
writer := newSniffCompressWriter(w, encoding)
next.ServeHTTP(writer, r)   // ← panic 时直接向上传播
writer.finish()             // ← 不是 defer：handler panic 则永远不会执行
```

```go
// compress.go:170-181：压缩分支才 Set Vary / Del Content-Length
if g.encoding == "" || len(body) < minCompressJSON || !isJSONContentType(g.contentType) {
    g.emitRaw()
    _, _ = g.ResponseWriter.Write(body)      // ← identity 分支没有 Vary
    return
}
header.Set("Content-Encoding", g.encoding)
header.Add("Vary", "Accept-Encoding")
header.Del("Content-Length")
```

```go
// server.go:44：HEAD 直接绕过整个协商
if r.Method == http.MethodHead || strings.HasPrefix(r.URL.Path, "/api/events") {
    next.ServeHTTP(w, r)
    return
}
```

- 影响：
  - `Routes()` 未挂任何 recover 中间件（全仓库 grep 无 `middleware.Recoverer`/`recover()`，仅跨模块 `jobs/manager.go:362` 在任务 goroutine 内有 recover）。一旦 handler panic：`net/http` 只负责记录日志并断开该连接，而压缩包装器此前已把整个 JSON 响应缓存在 `bytes.Buffer` 中且尚未写出任何 header → 客户端拿到空响应/连接重置，没有任何 500 JSON；日志里也没有 request id/路径关联。
  - 每个带 `Accept-Encoding: br/gzip` 的 JSON 请求最多常驻 8 MiB 缓冲（`maxBufferedJSON`）；这个服务无鉴权，并发请求即内存放大手段（尤其 `/api/library/downloads` 默认返回 10 万条）。
  - `HEAD` 与 `GET` 的头不一致：`GET /api/config` 压缩后无 `Content-Length`、带 `Content-Encoding: br`，而 `HEAD` 完全不压缩（有原始长度、无编码），不符合 RFC 9110 §9.3.2 “HEAD 头应与 GET 相同”的期望；`Vary` 只在压缩分支添加，媒体/identity 分支缺失（缓存方向无实际危害，但语义不完整）。
  - `WriteHeader` 不处理 1xx（`WriteHeader(103)` 会被当成最终状态）；`Write` 缓冲溢出分支返回的 n 不含已冲刷的缓冲字节（`compress.go:115-123`），与 `io.Writer` 契约不符。
- 建议：
  - `defer writer.finish()` + 增加统一 recover 中间件（写 500 JSON、记录 method/path/stack；若已写 header 则只能断开）；
  - 缓冲上限按路由下调（如 1 MiB）或对流式大响应（library list）直接 passthrough；
  - `Vary: Accept-Encoding` 在协商成功后无条件设置（包括 identity 分支和 HEAD），HEAD 走协商逻辑只是不写 body；
  - `finish` 覆盖 1xx 防护（`code < 200` 时不当作最终响应）。

### [P2-4] 海报回源路径一致性：缓存文件路径不走 `resolveLocalLibraryPath`（可跟随符号链接）、Content-Type 判断大小写敏感、无失败负缓存/无效并发去重、丢弃具体错误

- 位置：`internal/httpapi/server.go:769-810`（`serveDownloadPreview`）、`server.go:826-865`（`fetchRemotePoster`）、`server.go:841-844`（Content-Type）
- 证据：

```go
// server.go:769-782
videoPath, err := resolveLocalLibraryPath(cfg.DownloadDir, record.FilePath)   // 视频路径有校验
...
posterPath := videoPath + ".preview.jpg"
if info, err := os.Stat(posterPath); err == nil && !info.IsDir() {
    http.ServeFile(w, r, posterPath)      // ← 只 os.Stat（跟随符号链接）后直接 ServeFile，未校验 posterPath 仍在 root 内
    return
}
```

```go
// server.go:843
if !strings.HasPrefix(response.Header.Get("Content-Type"), "image/") {
    return &posterStatusError{status: http.StatusUnsupportedMediaType, message: "视频预览图格式无效"}
}
```

- 影响：
  - 与视频路径的加固不一致：若下载目录内存在 `.preview.jpg` 符号链接（本地进程/共享目录场景），`http.ServeFile` 会跟随链接读取任意可读文件并以 `image/jpeg` 返回；同类 TOCTOU 也存在于 `serveLibraryFile`/`serveDownloadFile`（校验后 `ServeFile` 重新打开）。
  - `Content-Type: IMAGE/JPEG` 会被判为非法（415），上游大小写异常时预览图无法缓存。
  - 抓取失败没有负缓存/在途去重：画廊里几十个缺失海报每次刷新都会重新回源（受 4 并发信号量限制，单个请求最长 20s），X CDN 抖动时首屏持续卡顿并放大出站流量。
  - 所有非状态类失败统一映射成 502 `下载视频预览图失败`，丢掉了 `ErrBodyTooLarge`/超时等可诊断信息（服务端也没有日志）。
- 建议：
  - `posterPath` 也走 `resolveLocalLibraryPath`（文件不存在时的 EvalSymlinks 会失败 → 先校验父目录 + 用 `os.OpenFile(O_NOFOLLOW)` 写/读），或对海报单独做 `Lstat` 拒绝符号链接；
  - Content-Type 用 `mime.ParseMediaType` + `EqualFold`；
  - 加进程内短期负缓存（如 1 分钟）与 single-flight；
  - 抓取失败记录 `log.Printf` 并区分 502/504。

### [P2-5] 媒体库与列表端点缺少规模/并发约束：`/api/library/downloads` 单请求可触发全目录 WalkDir + 最多 10 万条 JSON，且无速率限制

- 位置：`internal/httpapi/server.go:735-747`（`listDownloads`）、`server.go:1184-1197`（`parseLimit`）、跨模块 `storage.go:48`（`MaxLibraryDownloadsLimit = 100000`）、`storage.go:1400-1445`（`ListLibraryDownloads` + `collectOrphanLibraryFiles`）
- 证据：

```go
// server.go:736：fallback 与 max 都是 100000 → 不带 limit 参数就是全量
items, err := s.store.ListLibraryDownloads(r.Context(), parseLimit(r, storage.MaxLibraryDownloadsLimit, storage.MaxLibraryDownloadsLimit))
```

```go
// server.go:1193-1195：limit 只会被“收敛到上限”，不存在“默认分页”
if max > 0 && limit > max {
    return max
}
```

```go
// 跨模块 storage.go:1466-1495：缓存未命中时 WalkDir 整个下载目录收集孤儿文件
err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
```

- 影响：
  - 10 万条记录一次性序列化（每条含 mediaUrl/previewUrl/filePath 等），响应可达数十 MB；配合 P2-3 的 8 MiB 缓冲，会先缓冲 8 MiB 再 passthrough。
  - 归档任务运行期间 `libraryCacheGen` 频繁失效，画廊轮询会反复触发“SELECT 10 万行 + 全目录 WalkDir + attributeLibraryUsers 全表扫描”，CPU/IO 放大；无鉴权部署下是现成的低成本 DoS 面。
  - 同类：`/api/jobs`、`/api/failed-tweets`、`/api/dashboard` 都允许 `page=1000000`（`server.go:587-589,1026-1028,1099-1100`），`OFFSET` 上限约 1e8（`(page-1)*pageSize`），SQLite 对超大 OFFSET 需要顺序跳过。
- 建议：给 `/api/library/downloads` 增加 `page/pageSize`（默认如 200）并在前端改为分页/虚拟滚动；对重端点（parse/checkAuth/preview/library）加简单令牌桶或并发上限；`page` 上限下调到与数据量匹配。

### [P2-6] `OPEN_XDOWNLOAD_DOWNLOAD_DIR` 的提示与实际行为不一致；启动时不校验下载目录；下载目录可被配成家目录内的任意目录（含 dotdir）

- 位置：`internal/httpapi/server.go:1334-1353`（`validateDownloadDir` 的错误文案）、`server.go:1235-1246`（`localDirectoryAllowedRoots`）、`server.go:1360-1371`（`isHomeDirectory`）、跨模块 `config.go:24-42`、`config.go:146-154`
- 证据：

```go
// server.go:1348-1351
if !withinAllowedRoot(path, allowedRoots) {
    return fmt.Errorf("下载目录不允许：仅限家目录或当前下载目录范围内；" +
        "如需指向其他位置（例如外置磁盘），请用 OPEN_XDOWNLOAD_DOWNLOAD_DIR 环境变量启动")
}
```

```go
// 跨模块 config.go:24-42 ApplyEnvOverrides：只覆盖 4 个敏感字段，完全不读 DOWNLOAD_DIR
func ApplyEnvOverrides(cfg AppConfig) AppConfig {
    if value := os.Getenv(EnvAuthToken); value != "" { ... }
    ...
}
// config.go:146-154：DOWNLOAD_DIR 只在“默认值”里读一次
func defaultDownloadDir() string {
    if value := os.Getenv("OPEN_XDOWNLOAD_DOWNLOAD_DIR"); value != "" { return value }
    ...
}
```

- 影响：
  - 已有数据库（DB 里 `download_dir` 非空）之后，`OPEN_XDOWNLOAD_DOWNLOAD_DIR` 不再生效（`Normalized()` 只在空值时用默认值，`ApplyEnvOverrides` 不碰该字段）。用户按错误提示设置环境变量重启后行为不变，随后仍无法通过 UI 把目录改到 home 之外 —— 提示文案与实现不符。
  - 启动路径不经过 `validateDownloadDir`：首次启动时若 `OPEN_XDOWNLOAD_DOWNLOAD_DIR=/`（或后续手工改库/迁移数据），媒体库沙箱根就是 `/`，`/api/library/file` 变成“全盘媒体文件读取”；`localDirectoryAllowedRoots` 也会把 `/` 列为允许根，目录浏览器可枚举全盘。
  - `validateDownloadDir` 只排除“文件系统根”和“家目录本身”，`~/.ssh`、`~/.config`、`~/Library` 等 dotdir/系统目录都可被设为下载目录（下载器随后 0700 MkdirAll 并向里写媒体）。
- 建议：把 `DownloadDir` 加入 `ApplyEnvOverrides`（或至少让错误文案改为“首次启动时设置”）；启动时复用 `validateDownloadDir` 做一次硬校验（拒绝 `/`、家目录、常见隐藏目录）；下载目录建议要求“非 dotdir 且非家目录一级子目录以外”的显式白名单策略。

### [P2-7] `PUT /api/config` 部分字段提交会静默重置其余非密字段（包括 downloadDir），且校验对象与实际持久化对象不同

- 位置：`internal/httpapi/server.go:117-146`、`server.go:496-507`（只合并密文字段）、跨模块 `storage.go:825-855`（`UpdateConfig` 同样只合并 4 个敏感字段）
- 证据：

```go
// server.go:117-145
var cfg config.AppConfig
if err := decodeJSON(w, r, &cfg); err != nil { ... }
stored, err := s.store.GetStoredConfig(r.Context())
...
validationCfg := mergeSecretPlaceholders(cfg, stored).Normalized()   // 只合并 auth/ct0/cookies/proxy
...
updated, err := s.store.UpdateConfig(r.Context(), cfg)               // ← 其余字段以零值（再 Normalized）落库
```

```go
// server.go:496-506：合并范围仅限密文
if cfg.AuthToken == "" || cfg.AuthToken == config.SecretPlaceholder { ... }
if cfg.CSRFToken == "" || cfg.CSRFToken == config.SecretPlaceholder { ... }
cfg.AdditionalCookies = config.RestoreAdditionalCookies(...)
cfg.ProxyURL = config.RestoreURLUserinfo(...)
```

- 影响：任何直接调用 API 的客户端（脚本/curl）用部分 body 做“局部更新”，都会把未提交的 `downloadDir`、`fileNamingMode`、`maxFilenameLength`、`includeNestedTweetMedia`、`autoRetryFailed`、`autoFollowProtected`、`incrementalArchive` 静默重置为默认值：
  - `downloadDir` 被重置为 `defaultDownloadDir()`（通常是 `<cwd>/downloads`）。若该默认目录恰好落在允许根内，校验通过 → **下载目录被静默改掉**，随即触发 P1-1 的“历史媒体全部 403”。
  - 前端（`apps/web/src/lib/api.ts:358-362` 提交完整对象）不会触发；但 API 没有部分更新声明，也不返回“哪些字段被重置”。
- 建议：显式区分“PATCH 部分更新（先取 stored 再覆盖提交字段）”与“PUT 全量替换（要求所有字段）”；或在 decode 后用指针字段判断“字段是否出现”，未出现则沿用 stored；同时把校验与持久化统一到同一个 `Normalized` 结果上。

### [P2-8] `.avif`/`.bin` 等下载器可产出的扩展名与媒体库白名单不一致（跨模块契约）

- 位置：`internal/downloader/downloader.go:400-437`（`extensionFromContentType`/`normalizeMediaExtension`） vs 跨模块 `internal/storage/storage.go:1567-1575`（`IsLibraryMediaPath`）、`storage.go:1477`（孤儿文件收集）
- 证据：

```go
// downloader.go:410-411
case "image/avif":
    return ".avif"
// downloader.go:433
case ".jpg", ".png", ".gif", ".webp", ".avif", ".mp4", ".mov", ".m4v", ".webm":
```

```go
// 跨模块 storage.go:1567-1575
switch strings.ToLower(filepath.Ext(path)) {
case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".mp4", ".mov", ".m4v", ".webm", ".ogv":
    return true
```

- 影响：
  - 下载器可能把 `image/avif` 保存为 `*.avif`（`Filename` 优先用 Content-Type 决定扩展名，`downloader.go:354-373`），但 `IsLibraryMediaPath` 不含 `.avif`：
    - `/api/library/file?path=xxx.avif` 一律 403 `仅支持媒体文件`（`server.go:963-966`）；
    - 孤儿 AVIF（无 downloads 记录）不会被 `collectOrphanLibraryFiles` 收录，画廊里直接消失；
    - 有记录的 AVIF 仍能通过 `/api/library/downloads/{id}/file` 播放（该端点不查扩展名），造成两套端点行为不一致。
  - 反向不一致：`storage` 接受 `.ogv`，但下载器不会产出 `.ogv`（`extensionFromContentType` 无 `video/ogg`）；`mediaExtension` 兜底 `.bin` 同样不在库白名单里。
  - 现有测试 `downloader_test.go` 完全没覆盖 `.avif`，`server_test.go` 只测 `.mp4/.jpg/.txt`。
- 建议：把媒体扩展名白名单收敛到 `downloader`/共享包一处（如 `downloader.IsMediaExtension`），storage/httpapi 引用同一来源；为 avif 增加端到端测试（下载 → 记录 → 列表 → 播放）。

### [P2-9] 目录创建权限 0755，与全局 0700 隐私策略不一致；clean 检查与 MkdirAll 之间存在 TOCTOU

- 位置：`internal/httpapi/server.go:1373-1395`（`createLocalDirectoryPath`）；对比 `server.go:1384`、跨模块 `filestore.go:60-64`、`downloader.go:74-79`
- 证据：

```go
// server.go:1381-1390
if !withinAllowedRoot(path, allowedRoots) {
    return "", fmt.Errorf("不允许创建此位置的目录（仅限家目录与下载目录范围）")
}
if err := os.MkdirAll(path, 0o755); err != nil {   // ← 0700 策略之外的唯一 0755
    return "", err
}
```

- 影响：
  - 数据目录、下载目录、下载器写入目录均刻意使用 0700（“私有账号下载的媒体不应被其他本地用户读取”），但用户在目录选择器里新建的目录是 0755（可被其他本地用户遍历）；若该目录正是下载目录，则后续媒体文件本身 0600 仍安全，但目录结构/文件名可被枚举。
  - `withinAllowedRoot` 校验 → `MkdirAll` 之间路径组件可能被替换为指向允许根之外的 symlink（需本地同用户写权限，风险低，但与项目其它地方的加固口径不一致）。
- 建议：统一用 0700；创建前再次校验（或用 `os.OpenFile` + `O_DIRECTORY` 逐级创建）并校验 `MkdirAll` 后的真实路径仍在允许根内。

### [P2-10] 可观测性与运维面：无 request id/结构化日志、无 handler 超时、`writeError` 日志缺请求上下文

- 位置：`internal/httpapi/server.go:59-102`（`Routes` 全部中间件只有 nosniff + guardCrossOrigin）、`server.go:1166-1175`（`log.Printf("httpapi %d: %v", ...)`）、`compress.go:42-57`
- 证据：

```go
// server.go:68-101：整条链只有两个中间件，无 RequestID/Recoverer/超时/访问日志
r.Use(func(next http.Handler) http.Handler { ... "X-Content-Type-Options" ... })
r.Use(guardCrossOrigin)
r.Get("/api/health", s.health)
...
return compressJSON(r)
```

- 影响：
  - 日志无法关联到具体请求（无 method/path/request-id），排障只能靠时间戳猜；P1-2 里的错误原文只进日志，但日志本身缺上下文。
  - 处理型端点没有超时约束：`checkAuth` 20s（`server.go:264`）、`parseTweetLink` 20s（`server.go:307`）、海报回源 20s，均依赖客户端断开才取消；`listDownloads`/`directory` 的 FS 操作没有 ctx 可取消路径（`filestore` 忽略 ctx，见 P3-6）。
  - 服务端 `cmd/server/listen.go:34-53` 已有 `ReadHeaderTimeout/ReadTimeout/IdleTimeout` 且刻意不设 WriteTimeout（SSE/媒体长连接，合理），所以 handler 级超时更应由中间件/ctx 承担。
- 建议：加一个轻量 middleware：request id + 访问日志（method/path/status/duration）+ panic recover；对同步阻塞端点用 `http.TimeoutHandler` 或 handler 内 `context.WithTimeout`（SSE 除外）。

## 4. P3 与优化建议

1. **`Accept-Encoding` 解析边界**（`internal/httpx/encoding.go:41-72`）：`parseQuality` 只认小写 `q=` 前缀且不允许附加参数 —— `br;Q=0`、`gzip;q=0;level=1`、`gzip; q = 0` 都会被当成“q=1 接受”，客户端明确拒绝的编码仍会被下发（浏览器实际不会这样发，但属于协商函数正确性缺陷）。`NegotiateEncoding` 按服务器优先级而非客户端 q 值排序（测试 `encoding_test.go:17` 已把“q=0.9 的 br 胜过 q=1.0 的 gzip 视为期望”固化，属 RFC 允许的服务器偏好，但建议注释说明是有意为之）。
2. **`sanitizeFilename` 边界**（`downloader.go:542-548`）：未处理 `\x00`（含 NUL 的文件名会让 `os.Link` 失败，任务报错而非降级）、其他控制字符（除 CR/LF）、Windows 保留名（`CON`/`NUL`/`AUX`）、结尾点号/空格；跨平台部署时下载会失败。建议替换为显式白名单/正则并补测试。
3. **`publishFile` 的 `bytes` 参数未使用**（`downloader.go:223`），`publishTempFile` 的 `contentLength` 只在 replace 分支使用（`downloader.go:183-212`）；`downloader.New()` 创建的 `d.client` 只被取 `Timeout`（`downloader.go:59-68,303-305`），实际请求每次新建 client —— 死参数/易误导，建议清理。
4. **`RemoveStaleTempFiles` 的 1 小时阈值会误删超长下载的临时文件**（`downloader.go:610-640` + 跨模块 `jobs/manager.go:64-65` `staleTempFileAge = time.Hour`，调用点 `manager.go:335`）：慢速代理下 >1h 的视频下载，临时 `.xdl-*.part` 被后台清理 → 发布 `os.Link` 报 ENOENT → 下载失败重试。建议按“至少 2 倍单次下载超时”或跳过近期有写入的 inode；另外该清理每次都对整个下载目录 `WalkDir`，大归档下成本可观。
5. **`httpx.Transport` 缓存策略**（`httpx.go:136-172`）：超过 8 个代理配置时任意 `delete` 一个条目并 `CloseIdleConnections`，被逐出的 transport 仍可能被在途 client 使用（不会出错，但连接池不再复用，且缓存 key 含代理凭据，凭据以明文长驻内存 map）；同时 `Transport` 设置了 `DialContext` 但未设 `ForceAttemptHTTP2`，Go 默认在自定义 DialContext 时禁用 HTTP/2（X GraphQL/下载全走 HTTP/1.1，属性能损失）。
6. **`filestore` 语义细节**（`filestore.go:17-95`）：`Rename` 在源不存在时静默返回 nil（`filestore.go:66-71`），调用方 `jobs.renameEntityDir` 随后会 `RewriteDownloadPathPrefix`，可能出现“DB 路径指向不存在文件”；`Exists` 返回 `(err == nil, err)` 且不区分目录，调用方只判断 bool 时会把“无权限 stat”当成“不存在”；`TestWritable` 在生产代码中没有任何调用点（死接口）；`Store` 接口反向依赖 `downloader.Downloader`，分层耦合；rename 后没有对父目录 fsync（崩溃可能丢目录项）。
7. **压缩池的错误处理**（`compress.go:184-199`）：`gzip/brotli Writer.Close()` 与 `Write` 的错误全部丢弃，压缩失败时客户端只会看到“……截断的 200 + Content-Encoding”，无法感知；`Vary` 用 `Add` 可能与上游已有值重复；两块压缩实现（`httpapi/compress.go` 与 `cmd/server/static.go`）重复，建议下沉共享包。
8. **`writeJSON` 忽略编码错误**（`server.go:1160-1164`）：`json.Encoder.Encode` 失败（如 payload 内 channel/函数）时响应已写入 header，返回截断 JSON；建议至少记录日志；`writeError` 对所有 4xx 也打日志（含 `parseID` 的常见错误）会造成日志噪声，可只记录 5xx。
9. **`decodeJSON` 细节**（`server.go:1146-1158`）：不接受未知字段（`DisallowUnknownFields` 未启用，拼错 `downloadDir` 的请求会静默成功并触发 P2-7 的重置）；超大 body 命中 `MaxBytesReader` 时映射为 400 而非 413；`defer r.Body.Close()` 关闭的是原始 body，包装后的 `MaxBytesReader` 未关闭/未 drain；并且 `MaxBytesReader` 的 `requestTooLarge` 快路径依赖 ResponseWriter 实现私有接口，经过 `sniffCompressWriter` 包装后失效（我用最小复现验证：未包装时响应带 `Close=true`，包装后 `Close=false`），即“请求体超限后关闭连接”的保护在压缩路径下丢失。
10. **重复的 twimg 白名单实现三份**：`downloader.go:340-343`、`httpapi/server.go:509-512`、`httpapi/server.go:867-870`，且 `jobs/preview.go:136-139` 还有第四份；大小写/尾点处理一致但后续容易漂移，建议收敛到 `httpx` 或独立 `mediahost` 包。
11. **`localDirectoryEntry.HasChildren` 恒为 true**（`server.go:182-187`）：注释说“展开时再读”，但前端据此显示可展开箭头会全部显示；小性能/UX 问题。
12. **`serveDownloadPreview` 在视频文件不存在时直接 404，即使海报缓存存在**（`server.go:774-777`）；`serveDownloadFile` 对“记录存在但目录已变更”返回 403（见 P1-1），建议统一 404 语义。
13. **`parsePositiveInt` 的 pageSize 白名单逻辑冗余**（`bootstrap.go:28-37`）：先 `parsePositiveInt` 再 switch 回退 20，与 `listJobs`（只 clamp 不白名单）不一致；无功能问题。
14. **`origin.go` 的 loopback 例外在状态变更请求上实际不可达**（`origin.go:25-28` 先拒绝 `same-site`，而 localhost:5173 → 127.0.0.1:8787 在浏览器里是 same-site）：例外只对“无 Sec-Fetch-Site 的旧浏览器”生效，建议注释说明，避免后人误以为现代浏览器也会走到该分支。
15. **`httpapi` 对 `GET /api/...` 未设置 `Cache-Control: no-store`**：`/api/config`（脱敏后）、`/api/jobs` 等可被启发式缓存；媒体库端点则希望被缓存（`http.ServeFile` 只给 Last-Modified，无 ETag）。建议对非媒体 API 统一 no-store，媒体加 ETag。
16. **测试缺口（本范围）**：
    - 压缩：HEAD、`Vary`（identity/媒体分支）、>8MiB 溢出回退、304/204、panic 时 `finish` 未执行、`Write` 溢出返回值、错误 JSON 的压缩；
    - httpapi：`decodeJSON` 未知字段/超限/错误 Content-Type/空 body、`parseID/parseLimit/parsePositiveInt` 边界（`-1`/`0`/超大）、batch 200 上限、归档计划参数边界在 HTTP 层的映射、`isAllowedPreviewHost` 无任何测试、`serveLibraryFile` 的 symlink 逃逸（只测了目录函数与普通越界）、`HEAD`/Range 在压缩包装下的组合；
    - httpx：`guardedDial` 只通过 downloader 集成测试覆盖，无 Transport 缓存/驱逐测试、无 `RestrictRedirects` 的次数/scheme/端口测试、无 `Client` 初始化失败路径、无代理模式行为测试；
    - downloader：204/304、无 Content-Length + 已有文件、ctx 取消清理、`maxFilenameConflicts` 耗尽、`sanitizeFilename` 的 NUL/Windows 名、临时文件清理与进行中下载的竞争、`.avif` 文件名；
    - filestore：`Rename`（源缺失/目标存在/跨设备）、`Exists`（目录/权限错误）、`New` 相对路径、`SaveMedia` 端到端。

## 5. 待确认 / 需 Lead 交叉验证

1. **无内置鉴权 + Docker 默认 `0.0.0.0:8787` 的定级**：`Routes()`（`server.go:59-102`）没有任何认证/授权中间件，`guardCrossOrigin` 只是 CSRF 防护；跨模块 `cmd/server/main.go:33-36` 默认绑定 `127.0.0.1:8787`（缓解），但 `Dockerfile:32` 显式 `OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787`，注释要求部署方用 `-p 127.0.0.1` 或前置反代。按“鉴权边界缺失”可判 P1，但这是项目已声明的部署取舍 —— 我未读 README/docs，请 Lead 结合项目对外表述定级（若 Docker 路径被视为默认用法，建议至少提供可选的 Basic Auth/token 中间件）。
2. **`Result.ContentHash` 在 `Skipped` 路径为空**（`downloader.go:95-100,175`）：跨模块 `jobs/manager.go:772-784` 跳过分支仍 `CreateDownload(ContentHash: "")`。请 storage/ jobs reviewer 确认 upsert 是否真的保住了既有 content_hash（`storage.go:1353-1382` 的 `CASE WHEN excluded.content_hash <> ''`），以及“跳过”路径是否会丢失后续按内容去重的能力。
3. **`filestore.Rename` 源缺失静默成功 + `RewriteDownloadPathPrefix`**（`filestore.go:66-71` + 跨模块 `jobs/manager.go:1454-1468`）：请 jobs reviewer 确认“目录不存在但 DB 有记录”的组合是否会导致记录指向不存在路径。
4. **P1-1 的修复归属**：媒体库路径解析在 httpapi（`resolveLocalLibraryPath`），记录路径由 jobs/filestore 写入，迁移逻辑在 storage（`RewriteDownloadPathPrefix`）。需要 Lead 决定修复放在哪一层（建议 storage/httpapi 协同）。
5. **海报抓取两套实现漂移**：httpapi `fetchRemotePoster`（`server.go:826-865`）与 jobs `savePosterImage`（跨模块 `jobs/preview.go:85-134`）行为差异（前者无独立 ctx 超时/无负缓存，后者有 `context.WithTimeout`）。建议统一到一处，请 jobs reviewer 确认职责归属。
6. **`.avif`/`.ogv`/`.bin` 是否真实出现**（P2-8）：需要 parser/xclient reviewer 确认 X 是否会返回 `format=avif` / `image/avif` / 非媒体 Content-Type 的媒体，以判断该不一致是线上问题还是潜在问题。
7. **`MaxBytesReader` + ResponseWriter 包装的结论**：我已用最小复现验证（未包装 `resp.Close=true`，包装后 `Close=false`，即 `requestTooLarge` 私有接口失效），但这是 Go 标准库实现细节，请 Lead 在目标 Go 版本（go.mod:3 `go 1.26.6`）上再确认一次。
8. **CSP 与 bootstrap 数据块**：`InjectIndexHTML` 注入的是 `<script type="application/json">`，页面 CSP `script-src 'self'`（跨模块 `cmd/server/static.go:78-93`）注释称数据块不受限。请前端/安全 reviewer 确认真实浏览器行为（若受限，生产环境首屏 bootstrap 会失效）。
9. **代理模式下的 SSRF 语义**（P2-1）：需要安全 owner 确认“配置代理后链路本地防护失效”是否被接受；若不可接受，应在 `httpx` 层对 CONNECT/绝对 URI 目标做校验，而不只是拨号检查。
10. **`downloader.Options.ExistingFileOwner` 的 API 契约**（P2-2）：目前只有跨模块 `jobs/manager.go:749-756` 设置；文档注释已说明语义，但默认零值不安全。建议 reviewer 群确认是否改为必填/新增 `Options.RequireOwner()`。

### 与其他 reviewer 报告的交叉关系（供 Lead 去重/合并）

> 这三条是在汇总时读了同目录下其它 reviewer 报告后补的，仅用于说明根因关系，不改变本报告的定级。

- **与 `review/storage.md` P1-1（`UpdateConfig` 不失效 library 缓存）**：不是同一个 bug，需分别修。
  - storage.md P1-1 根因：缓存 30 分钟陈旧；
  - 本报告 P1-1 根因：**即使缓存已刷新**，`downloads.file_path` 仍是旧目录绝对路径 → `resolveLocalLibraryPath` 永久判越界 → 403。只修缓存失效不能解决本报告 P1-1；需同时做路径迁移/根快照。
- **与 `review/server-config-ops.md` P1-4（`RestoreAdditionalCookies` 未知 ID + 占位符落库）**：同一条调用链在本包有两处入口 —— `updateConfig`（`server.go:496-507` → storage 落库，真正风险）与 `checkAuth`（`server.go:239-245`，仅用于本次 X 校验、不落库，影响是“用 `********` 当 cookie 去请求 X 导致校验失败”）。修复应放在 `internal/config.RestoreAdditionalCookies` 内部，本包两处调用随之受益。
- **与 `review/server-config-ops.md` 第 400 行（httpapi 版 merge 与 storage 版 merge 行为已分叉）**：与本报告 P2-7 同源（`server.go:128` 校验对象 = `mergeSecretPlaceholders(...).Normalized()`，落库对象 = `cfg.Normalized()`，且 storage 内还有第三次合并 + `RevertEnvOnlyEcho`）。建议 Lead 把“配置校验/持久化统一到同一个归一化结果”作为一条跨模块修复项，而不是按三份报告分别打补丁。

## 6. 设计上值得肯定之处（简短）

1. **媒体库路径校验是本次审查里质量最高的部分**：`filepath.Rel` + `EvalSymlinks` 双重校验（`server.go:909-935`），并用 `resolvePathForCheck` 处理“尚不存在尾部 + 已有 symlink 前缀”（`server.go:1276-1304`），正确地避免了字符串前缀比较（`/data` vs `/data-evil`）与 symlink 逃逸；`localDirectoryAllowedRoots` 刻意不含进程 CWD（`server.go:1231-1246`），测试固化了这些边界（`server_test.go:265-296,864-896`）。
2. **下载发布协议抵御了“崩溃残留被当完整文件”**：临时文件 → `Sync` → 硬链接（绝不覆盖）→ 不支持硬链接时 O_EXCL 占位 + rename 退化（`downloader.go:106-140,217-265`），临时名与最终名解耦避免 ENAMETOOLONG，权限 0600 通过硬链接传递；`ExistingFileOwner` 解决同名跨推文冲突；测试覆盖了并发同名、exFAT 退化、最大文件名、残留清理（`downloader_test.go:150-248,451-567`）。
3. **海报回源的 SSRF 防护闭环**：初始 URL 主机白名单 + 重定向白名单 + 2xx/image 校验 + `CopyLimited`“超限即失败、绝不发布截断图” + 并发信号量覆盖整个 body 读取 + ctx 取消（`server.go:787-865`），并有测试验证信号量在 body 读取期间确实被占用（`server_test.go:898-937`）；`httpx.CopyLimited` 明确避免 `io.LimitReader` 静默截断（`fetch.go:13-25`），注释解释了“截断图会被永久缓存”的真实教训。
4. **SSRF 拨号层防 DNS rebinding**：先解析再按 IP 字面量拨号（`httpx.go:81-116`），metadata 端点 169.254/16 与 IPv4-mapped 形式均被拦（`blocked_test.go`），且把“环回/私网放行”的取舍用测试固定下来；`http.Client` 统一复用 Transport（`httpx.go:136-181`）。
5. **Bootstrap 注入安全**：`json.Encoder` + `SetEscapeHTML(true)` 使配置/任务文本无法闭合 `</script>`（`bootstrap.go:88-108`），测试断言明文 secret 不落 HTML 且 `/settings` 与工作台注入内容隔离（`bootstrap_test.go:65-108`）。`jobs.Event.MarshalSSE` 用 `json.Marshal` 加 `data: ` 前缀，换行被转义，SSE 帧注入不成立。
6. **压缩协商的流式安全**：非 JSON / 超大 / 显式 Flush 一律 raw passthrough，SSE 除外（`compress.go:42-57,97-157`），测试验证了“首个 body 字节在 handler 写 64KiB 时就到达客户端”（`compress_test.go:189-215`），说明设计者明确考虑过“缓冲导致流式端点退化”。
7. **CSRF/DNS-rebinding 双层防护**：Origin/Sec-Fetch-Site/`Origin: null` 与 Host 白名单（`origin.go:21-111`）覆盖了带 body 与无 body 两类状态变更；`decodeJSON` 强制 `application/json`（`server.go:1150-1156`）挡住浏览器可无预检发送的简单类型；测试覆盖 rebinding 场景（`origin_test.go:127-142`）。
8. **测试风格可维护**：注释直接记录“为什么这样取舍”（如 `blocked_test.go:8-10`、`compress.go:27-41`、`downloader_test.go:104-108`），下载器把 `os.Link` 抽成可注入变量以便模拟 exFAT（`downloader.go:214-215`），这类设计让边界行为可回归。
