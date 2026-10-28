# 修复复核：运维 / 部署 / 配置 / 构建（REPORT §3.6、§3.7、P1-2）

复核人：rev-server-ops　时间：2026-10-28 12:50（UTC+8）
对象：当前工作区（未提交）。`git status` 快照：121 项（80 M / 16 D / 25 ??），`git diff --shortstat` = 96 files, +3763, -1403。
方法：逐文件读当前代码 + 运行期起进程验证（不依赖 FIXES.md 的自述）；`git diff`/`git show HEAD:` 仅作对照。**复核期间发现其他成员仍在并发改动**（如 `apps/web/labelcheck.tmp.mjs` 出现后消失、`apps/web/pnpm-lock.yaml` 被删除、`internal/webui/dist` 11:27 重建），本文件结论以 12:50 的磁盘状态为准；如后续再改动需重新核对。

状态口径：✅ 已修复并经证据确认　🟡 部分修复/仍有残留　❌ 未修复　⚪ 判定为产品决策或有意保留　⛔ **新回归**

---

## 0. 本轮必答的四项

| # | 问题 | 结论 | 证据 |
| --- | --- | --- | --- |
| a | Windows 上 sqlite DSN `file://C:/...` 是否会让 release 的 windows 二进制启动失败？ | **⛔ 是，会直接启动失败（P1，新增回归）** | 见 §1.1 |
| b | `internal/webui/dist` 16 删除 + 16 新增未跟踪，CI `git diff --exit-code` 闸门在草稿态会失败 | **⛔ 确认会失败（exit 1）；且该闸门对"纯新增"存在盲区** | 见 §1.2 |
| c | 未跟踪 `httpapi.test` 20MB 二进制 | **⛔ 仍存在，且未被 `.gitignore` 覆盖（`.dockerignore` 已覆盖）** | 见 §1.3 |
| d | `package-lock.json` 是否与 `package.json` 同步 | **✅ 已同步**（独立复核，与 Lead 结论一致） | 见 §1.4 |

### 1.1 ⛔（P1）Windows sqlite DSN 变成 `file://C:/...`，Windows 二进制必然启动失败

当前代码 `internal/storage/storage.go:111`：

```go
dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String() + sqliteOpenOptions
```

HEAD 是 `sqlx.Open("sqlite", path+sqliteOpenOptions)`（`git show HEAD:internal/storage/storage.go` 的 `func Open`），即**纯路径 DSN**，Windows 可用；本分支改成了 `file:` URI。

Windows 上 `filepath.Abs("data/open-xdownload.db")` = `C:\...\data\open-xdownload.db`，`ToSlash` → `C:/.../data/open-xdownload.db`，而 `url.URL{Scheme:"file", Path:"C:/..."}` 的 `String()` **不补第三个斜杠**，产出 `file://C:/...`，把 `C:` 当成了 URI 的 authority。

我用仓库同版本驱动（`modernc.org/sqlite v1.59.0`，取自本模块缓存）实测四种写法：

```
dsn=file://C:/Users/alice/data/open-xdownload.db?...      PING ERR: SQL logic error: invalid uri authority: C: (1)
dsn=file:///C:/Users/alice/data/open-xdownload.db?...    （/C:/ 不存在于本机，仅验证解析通过）
dsn=file:C:/Users/alice/data/open-xdownload.db?...       PING ERR: unable to open database file (14)
plain path C:/Users/alice/data/x.db?...                  PING ERR: unable to open database file (14) ← 本机无此目录，非解析错误
```

关键点是第一行：**错误发生在 URI 解析阶段（`invalid uri authority: C:`），与文件是否存在无关**，所以在 Windows 上同样报错。调用链：

1. `storage.Open` → `db.Ping()` 返回 `invalid uri authority: C:` → `return nil, err`（storage.go:112-121）；
2. `main.go:75-79`：`log.Printf("open storage: %v", err); return 1` → 进程退出码 1。

即 **Windows amd64/arm64 的 release 二进制（`.github/workflows/release.yml:85-88` 的 matrix）一启动就退出**，数据目录都建不起来。这条不会被告警捕获：`GOOS=windows go build ./...`、`GOOS=windows go vet ./...`、`go test -c` 全部通过（已实测），只有真机运行才炸；本机无 Windows，故以「同一驱动 + 同一 DSN 构造」的方式复现解析错误，结论确定。

修复建议（任一）：
- 回到 HEAD 的纯路径写法 `sqlx.Open("sqlite", absPath+sqliteOpenOptions)`（modernc 支持裸路径 + `?` 参数）；
- 或构造 DSN 时保证路径部分以 `/` 开头：`u := url.URL{Scheme:"file", Path:"/"+filepath.ToSlash(absPath)}`，Windows 下得到 `file:///C:/...`（可用），POSIX 下 `//tmp/...` 需注意别重复斜杠；
- 并加一个 `go test` 单测（构造 Windows 路径字符串断言 DSN 不含 `file://C:`），CI 的 linux runner 就能拦住这类平台差异。

### 1.2 ⛔ CI `git diff --exit-code -- internal/webui/dist` 闸门：草稿态失败 + 有盲区

新闸门在 `.github/workflows/ci.yml:46-50`（`npm run sync:web` 后 `git diff --exit-code -- internal/webui/dist`）。

当前工作区实测：

```
$ git diff --exit-code -- internal/webui/dist ; echo $?
16 D（删除）/ 3 M（修改），exit=1
$ git status --porcelain -- internal/webui/dist | grep -c '^??'
16   ← 新哈希 sidecar 全部是未跟踪
```

- **草稿态必红**：这 16 个删除是**已跟踪**文件被 `sync-webui.sh` 的 `rm -rf` 移除，`git diff` 看得到 → exit 1。提交时必须 `git add -A internal/webui/dist`（含 16 个未跟踪新文件）才能让闸门变绿，否则 CI 直接红。
- 复核"提交后是否会绿"：把当前工作区整体（rsync，排除 node_modules）复制到 /tmp 并 `git init && git add -A && commit`，再跑 `vite build` + `sync-webui.sh` → **gate exit=0**，说明产物与源码一致、闸门设计本身有效。
- **盲区（P2）**：`git diff` **看不到未跟踪文件**。实测在已提交状态下只往 `apps/web/public/` 新增 `robots.txt`、重建 + 同步后，闸门仍 **exit=0**，而 `internal/webui/dist/robots.txt{,.br,.gz}` 是 `??` 未提交状态 —— 即"只新增、不删除"的产物漏检。建议改成 `git add -A -- internal/webui/dist && git diff --cached --exit-code`，或 `test -z "$(git status --porcelain -- internal/webui/dist)"`。
- 附带风险：`vite build` 的可复现性依赖 `apps/web/dist` 被 `.gitignore` 忽略（Tailwind v4 会自动扫描非忽略目录，`dist` 不被忽略时会把自身产物当源码扫描，CSS 从 64276 涨到 81251 字节、哈希改变）。实测在"有 .git + `.gitignore` 含 `dist/`"的仓库里连续 3 次构建哈希稳定（`index-9Q6AC5w8.css` / `index-BnW3_LU8.js`，与提交产物一致）；但**本地工作区存在 `apps/web/dist` 时**会污染构建结果。CI 是干净 checkout 不受影响，开发者本地二次构建会得到不同哈希 → 提交后 CI 红。属 P3 维护陷阱。

### 1.3 ⛔ `httpapi.test` 20MB 二进制仍在仓库根

`ls -la httpapi.test` → 20,375,202 字节，Mach-O arm64 可执行，mtime 10-28 09:03，`git status` 显示 `?? httpapi.test`。

- `.dockerignore:24` 已显式排除（构建上下文安全，✅）；
- 但 `.gitignore`（22 行）没有 `*.test`，`git add -A` 会把它提交进仓库 → P2 仓库卫生问题。
- 建议：删除该文件 + `.gitignore` 增加 `/httpapi.test`、`/*.test`。

### 1.4 ✅ package-lock 与 package.json 已同步（独立复核）

- `apps/web/package.json:12-20` 已无 `react-player`；`apps/web/package-lock.json`（167,308 字节，mtime 10:46）中 `react-player` 出现次数 = 0；
- lock 根依赖集合与 `package.json` 依赖集合逐项相等；
- `npm ci --dry-run` 成功（退出 0，无 lock/package 不一致报错）；
- `npx tsc --noEmit` 退出 0；`npm run build` 在临时副本中成功（产物哈希与已提交 dist 一致）。
- 复核过程中该 lock 曾被替换（10:41 还残留 react-player，10:46 更新为现在的版本），说明是并发修复的产物；**结论以当前状态为准：同步，正确**。
- `apps/web/pnpm-lock.yaml`（147KB，且解析出的版本与 npm lock 不同：react 19.3.1 vs 19.3.0 等）**在复核期间已消失**——若再现，会与 `npm ci` 工作流冲突，建议 `.gitignore` 加 `pnpm-lock.yaml`（P3）。

---

## 2. P1-2（REPORT.md:118）逐项验收

| 子项 | 状态 | 当前证据 |
| --- | --- | --- |
| 监听/地址/TLS 失败时退出码非 0 | ✅ | `main.go:52-55`（validate 失败 return 1）、`122-126`（bind 失败 return 1）、`101-105`（TLS 预检失败 return 1）、`69-73`（锁冲突 return 1）。运行验证：`-addr 127.0.0.1:18808`（端口占用）→ exit 1；`-addr 8787`（缺端口）→ exit 1 + `invalid listen address`；`-cert` 指向不存在文件 → exit 1 + `load TLS: open ...: no such file`；同数据目录第二实例 → exit 1 + `data directory is already in use` |
| HTTP/3 失败不得拖垮 TCP | ✅ | `main.go:166-173`：h3 goroutine 仅 `h3Available.Store(false)` + 日志，不再 `stop()`。运行验证：先占用 UDP 端口再启动 → 进程存活、`GET /api/health` = 200、日志 `http3: ... address already in use (TCP server continues)`、SIGTERM 退出码 0 |
| h3 失败撤回 Alt-Svc | ✅ | `listen.go:62-72` 在 `available[0].Load()==false` 时 `Set("Alt-Svc","clear")`。运行验证：h3 绑定失败后响应头 `alt-svc: clear`；正常时 `alt-svc: h3=":18830"; ma=86400` |
| errCh 收集致命 serve 错误 | ✅ | `main.go:157-165`（buf 2，非 `ErrServerClosed` 才上报并 `stop()`）、`195-203`（关闭后 `select` 取错误 → return 1）。h3 错误刻意不入 errCh（正确） |
| 先 bind 再打印监听地址 | ✅ | `main.go:122-128` 先 `listenTCP` 再取 `ln.Addr()`；日志在 `149-153`，使用 `ln.Addr()` 实际地址。运行验证：端口占用时只有 `bind ...` 错误、**没有** listening 行 |
| TLS 证书启动前预检 | ✅ | `main.go:101-106` `tls.LoadX509KeyPair`，坏证书 → exit 1 |
| tls-auto 的 SAN 跟随 `-addr` | 🟡 部分修复 | `tls.go:49-56` 现在会把 `-addr` 的 host 加进 SAN；运行验证 `-addr 10.10.11.106:18792` 时 SAN 含该 IP、`curl --cacert dev.crt` 通过。但**未指定地址（0.0.0.0/::，Docker 默认）时 `IsUnspecified()` 被跳过**，运行验证 `-addr 0.0.0.0:18830` 的 SAN 仍只有 `DNS:localhost, IP 127.0.0.1, IP ::1`，从局域网 IP 访问仍证书不匹配。可接受（self-signed 语义即 localhost），但建议至少打一条 warning |
| validDevTLS 校验私钥配对/NotBefore | ✅ | `tls.go:75-118`：解析证书与 PKCS8 私钥后比对 ECDSA 公钥 X/Y、检查 `NotBefore` 与到期前 24h、逐 host `VerifyHostname`；测试 `tls_test.go:51,73` 覆盖私钥不匹配与未来证书 |
| listen.validate 校验 addr / 证书可读 | ✅（证书可读由 main 预检承担） | `listen.go:18-28` 用 `net.ResolveTCPAddr` 校验 addr、cert/key 必须成对；证书文件可读性由 `main.go:101-105` 完成 |
| listenPort 解析失败回退 443 | 🟡 残留但基本不可达 | `tls.go:120-126` 仍 `return "443"`；现在调用点只传 `boundAddr`（`main.go:134`），而 `boundAddr` 必然带端口，故实际不可达。属死回退，建议改为返回 `(string,bool)` |

---

## 3. REPORT §3.6 静态层

| 子项（REPORT 原文摘要） | 状态 | 当前证据 |
| --- | --- | --- |
| `Vary` 只在压缩响应设置 | ✅ | `static.go:143-145`：可压缩扩展名**总是**加 `Vary`（与是否编码无关）。运行验证：identity 请求头含 `Vary: Accept-Encoding` |
| assetCache 永不失效、无上限 | ✅ 基本修复 / 🟡 细节 | `static.go:317-376`：以 `name`/`.gz`/`.br` 三者 `(size, mtime)` 作为失效键、`>=128` 时 `clear`；`static_test.go:432` 覆盖重建刷新。运行验证：外部 webDir 改写 identity 后 identity 侧内容刷新。**残留见 §5.2（sidecar 陈旧时 ETag/body 不一致）** |
| 任意 method 当 GET 服务 | ✅ | `static.go:114-118` 非 GET/HEAD → 405 + `Allow: GET, HEAD`。运行验证 `POST /` = 405 |
| SPA fallback 拦截带点路由、`/api` 精确路径返回 HTML | ✅ | 判定改为 `strings.HasPrefix(name,"assets/") \|\| contentTypeFor(name)!=""`（`static.go:132`）而非 `path.Ext`；`/api` 与 `/api/` 先委派给 API（`static.go:109-112`）。运行验证：`/users/example.name` = 200、`/api` = 404（不再返回 HTML）、`/api/health` = 200 |
| sidecar 解压失败静默 200 空 body | ✅ | `static.go:344-349` 解压校验失败即丢弃该 sidecar；测试 `static_test.go:451` 断言非 200。运行验证：仅损坏 `.gz` 时 `GET` 返回 404（不是 200 空） |
| DB 及 `-wal/-shm` 0600 | 🟡 语义未改但实测有效 | `main.go:84-88` 仍是"启动后 chmod 一次"。运行验证（进程存活时）：`data/` 0700、`open-xdownload.db` / `.db-wal` / `.db-shm` 全部 `-rw-------`。残留：若运行中 WAL 被 SQLite 删除重建，理论上会回到默认权限（数据目录 0700 兜底，实际暴露面很低） |
| `tls` 目录 0700 | ✅ | `tls.go:22-25` 现在每次都 `os.Chmod(dir, 0o700)`（不再只在创建时） |
| `log.Fatalf` 绕过 defer | ✅ | `grep -rn "log.Fatal" --include=*.go` 全仓库 0 处；`main.go` 全部 `log.Printf + return 1` |
| `signal.NotifyContext` 在 DB Open 之后注册 | 🟡 未改 | `main.go:108-109` 仍在 `storage.Open`(75) / 锁(69) / TLS(90-106) 之后。窗口内收到 SIGTERM 会走默认处理器，无法执行 `store.Close()`；WAL 崩溃可恢复，风险低，但 REPORT 条目未闭环 |
| `version/commit/date` 无引用 | ✅ | `main.go:149` 启动日志输出 `version=%s commit=%s date=%s`；`Dockerfile:23-25` 与 `release.yml:121-123` 均注入 ldflags |
| `scripts/sync-webui.sh` 权限 0644 | ⚪ 已非问题 | 文件仍是 0644，但所有调用点都走 `sh`（`package.json:9`、`ci.yml:47`、`Dockerfile:17`），不依赖执行位 |
| `dev-all.sh` 提示 0.0.0.0 但默认回环 | ✅ | `scripts/dev-all.sh:44` 改为 `http://${OPEN_XDOWNLOAD_ADDR:-127.0.0.1:8787}` |
| `tsconfig.node.json` 死配置 | ❌ 未改 | 无任何引用（`grep -rn "tsconfig.node"` = 0）；`apps/web/tsconfig.json` 直接 `include: ["src","vite.config.ts"]` |
| `@types/node` 26 / Node 22 错配 | ❌ 未改 | `apps/web/package.json:25` `"@types/node": "^26.5.1"`，CI/Docker 均 Node 22 |
| Google Fonts 外链 | ❌ 未改（⚪ 可接受） | `apps/web/index.html:8-10` 仍外链 `fonts.googleapis.com`；`static.go:90-91` CSP 仍放行。离线/内网部署会回退字体 |
| （新增）`gofmt` 闸门 | ✅ | `ci.yml:52-53` `test -z "$(gofmt -l cmd internal)"`；本地实测 `gofmt -l cmd internal` 为空 |
| （新增）单实例数据目录锁 | ✅ | 见 §4 |

---

## 4. 新增单实例锁（cmd/server/lock_*.go）

| 检查点 | 状态 | 证据 |
| --- | --- | --- |
| 是否接入 main | ✅ | `main.go:69-74`：`acquireDataLock(dbPath+".lock")`，位置在 `storage.Open`(75) **之前**、`MkdirAll`(57) **之后**；失败 `return 1` |
| defer 顺序 | ✅ | LIFO：`lock.Close`(74) 先注册 → `store.Close`(80) 后注册 → 实际先关 DB 再放锁，顺序正确 |
| Unix 实现 | ✅ | `lock_unix.go:12-22` `unix.Flock(LOCK_EX\|LOCK_NB)`，失败关闭 fd 并返回 `data directory is already in use: %w`；锁文件 0600（实测 `-rw-------`） |
| Windows 实现 | ✅ 编译/静态通过，运行时**未复核** | `lock_windows.go:10-21` `LockFileEx(EXCLUSIVE\|FAIL_IMMEDIATELY, 0,1,0)`；`GOOS=windows go build ./...` / `go vet` / `go test -c` 均通过。**注意：Windows 上即使本项 OK，也会先撞上 §1.1 的 DSN 问题，二进制根本起不来** |
| 跨平台测试 | ✅（但 Windows 运行时未验） | `lock_test.go:8-28` 两实例互斥 + Close 后可重取。本机 darwin 通过；`lock_test.go` 无 build tag，Windows 上是否同样报错（Windows 锁是 per-handle）**未复核** |
| 残留文件 | 🟡 观察项 | 正常退出后 `open-xdownload.db.lock`（0 字节）**保留**在数据目录（实测）。flock 语义下无害；但 `.gitignore` 未覆盖 `*.db.lock`（默认 `data/` 已忽略，自定义 data-dir 时会成为未跟踪文件） |
| 与 `os.Exit` 的关系 | 🟡 观察项 | `main.go:189-193` `manager.Stop()` 未排空时 `os.Exit(1)`：跳过 `lock.Close()/store.Close()/ln.Close()`（注释声明为有意，避免与 worker 抢写）。锁由进程退出释放，功能无损；但同一路径把"优雅停机超时"编码成 exit 1，`systemd Restart=on-failure` / `docker --restart on-failure` 会把主动停机当失败重启 → P2 运维语义问题 |
| NFS | ⚪ 未复核 | flock 在 NFS 上可靠性依实现而定，未在生产文件系统上验证 |

---

## 5. 新回归 / 新发现（本轮重点）

### 5.1 ⛔ P1 Windows DSN（见 §1.1）——**唯一确定的发布阻断项**

### 5.2 ⛔ P2 外部 webDir 下 sidecar 陈旧时，ETag 与实际 body 不一致（缓存正确性）

`assetCache.get` 用 **identity 文件**算哈希（`static.go:346-349`），但 `serveAsset` 返回的 identity body 来自 `ensureIdentity()`：当 sidecar 存在时 `item.identity` **不保留**（`static.go:362-365`），于是 body 是**解压旧 sidecar** 的结果。

实测（外部 `-web-dir`，`assets/s.js` 有旧 `.gz`）：

```
改写 identity 前：  ETag "2a88200cebc577cd"  body AAAAAAAA
只改写 identity 后： ETag "c0612aa559f49f25"  body AAAAAAAA   ← ETag 变了、body 没变
再更新 .gz 后：      ETag "c0612aa559f49f25"  body BBBBBBBBBBBBBBBB ← 同一个 ETag 对应两份不同内容
```

即**同一 ETag 映射到两份不同字节**：已按新 ETag 缓存的客户端带 `If-None-Match` 请求会拿到 304，从而永久保留旧内容；对 `index.html` 同样成立（改写 identity 后仍返回旧 HTML）。触发条件：`-web-dir` 指向外部目录且构建只更新了一部分文件（`npm run build` 的 `closeBundle` 之前有毫秒级窗口；rsync/手工改文件时无窗口保护）。内嵌 embed 路径没有 identity 文件，不受影响。建议：identity 请求的 body 一律直接用 `identity` 字节（不要从 sidecar 反解），或 ETag 改用"实际要发送的字节"计算。新测试 `static_test.go:432` 只覆盖无 sidecar 的文件，未覆盖本场景。

### 5.3 🟡 P3 注入 HTML 的 `Vary` 重复

`static.go:144` 与 `serveInjectedHTML` 内的 `static.go:189` 各 Add 一次。实测响应头出现两行 `Vary: Accept-Encoding`。语义等价（可合并），但属疏漏。

### 5.4 🟡 P2 Docker HEALTHCHECK 有两处实际缺陷

`Dockerfile:45-46`：

```
HEALTHCHECK ... CMD wget --spider --quiet http://127.0.0.1:8787/ || wget --spider --quiet --no-check-certificate https://127.0.0.1:8787/ || exit 1
```

1. **端口/地址硬编码**：镜像文档化的 `OPEN_XDOWNLOAD_ADDR` 可被覆盖，一旦用户改成别的端口，HEALTHCHECK 永远失败 → 容器永久 unhealthy（重启策略若配 `--restart` 会反复重启）。
2. 探测的是 `/`（SPA index），只证明"静态层能出 HTML"，不证明 DB/API 可用；DB 打不开时 `/` 仍可能是 200。建议改探 `/api/health`，并允许用 `$OPEN_XDOWNLOAD_ADDR` 拼 URL。
   另：Alpine 的 busybox `--spider` 实际发 **GET**（源码 `SENDFMT(sfp,"%s /%s HTTP/1.1", ...)` 只有 POST/GET 两态），且 `--no-check-certificate` 在未启用 `FEATURE_WGET_OPENSSL` 时会被内部 TLS 分支忽略（`spawn_ssl_client` 强制 NO_CHECK_CERT）→ HTTPS 分支可用但没有证书校验。功能可用，仅记录。

### 5.5 🟡 P2 环境变量下载目录与"保存配置"冲突（新引入的用户可见问题）

`ApplyEnvOverrides` 现在会覆盖 `DownloadDir`（`internal/config/config.go:24-27`），但保存时的允许根仍取自**库内**配置（`internal/httpapi/server.go:150` + `localDirectoryAllowedRoots` 1421-1432 = `$HOME` + 库内 downloadDir）。

实测：库内 dir 与 env dir 不同且 env dir 在 `$HOME` 之外时，UI 式整份 PUT（前端 `updateConfig` 发送整个 AppConfig，`apps/web/src/lib/api.ts:385-388`）返回 **400**：

```
{"error":"下载目录不允许：仅限家目录或当前下载目录范围内；如需指向其他位置（例如外置磁盘），请用 OPEN_XDOWNLOAD_DOWNLOAD_DIR 环境变量启动"}
```

提示让用户"用 OPEN_XDOWNLOAD_DOWNLOAD_DIR"——而用户正是在用它。此时设置页**任何**字段都保存不了（只能发 PATCH 局部字段才 200）。Docker 首次启动不受影响（`EnsureConfig` 用 `config.Default()` 落库时 `defaultDownloadDir()` 已读 env，`config.go:171-180`），受影响的是"先跑过、后加/改 env"的升级场景。建议把 env 覆盖后的目录也计入允许根。

### 5.6 🟡 P3 go.mod 未 tidy：`golang.org/x/sys` 直接依赖却标 `// indirect`

`cmd/server/lock_unix.go:9`、`lock_windows.go:7` 直接 import `golang.org/x/sys/{unix,windows}`，但 `go.mod:25` 仍是 `golang.org/x/sys v0.48.0 // indirect`。`go build`/`go vet`/`go test` 均正常（CI 也没有 tidy 检查），但下次 `go mod tidy` 会改 go.mod（去掉 indirect 注释）。建议在 CI 加 `go mod tidy -diff`（需网络）或手工修正注释。

### 5.7 🟡 P3 CI 其它观察

- 新增的 `docker build`（`ci.yml:65-66`）在同一个 25 分钟 timeout 的 job 里，会重跑一遍 `npm ci` + 前端构建 + Go 构建；本地无法验证真实耗时，**未复核**是否可能顶到 25min。无缓存(`--cache-from`)也无 buildx。
- `release.yml:94` 用 `download-artifact@37930b1…# v7`，`release.yml:161` 用 `download-artifact@9000827…# v8`：同一仓库混用 v7/v8 大版本。两个 SHA 均可解析（codeload 200），但**跨大版本下载是否兼容未复核**（无法运行 Actions）。
- 所有 action 均已钉到 40 位 SHA（12 处，逐个用 codeload 校验存在性并通过 package.json 版本号核对注释）；`softprops/action-gh-release@5113cdc` 的 commit 页面返回 404 但 codeload 200 + `package.json version 3.0.3` 与注释 `# v3` 一致，判定为 GitHub 页面渲染问题，SHA 有效。
- `dockerhub.yml`：`timeout-minutes: 35` > `deadline=1800s`（30min）✅；无 CI run 时 300s 后**对 push 与 tag 一律 exit 1** ✅（原报告要求的"不得放行"已落实）；同时移除了 `conclusion != "skipped"` 豁免，`cancelled/skipped` 现在都算失败（更严格）。`cancelled` 若由并发取消产生会误伤，属边缘。
- `ci.yml` 与 dockerhub gate 的时间关系：CI 25min < gate 等待 30min，一致 ✅。
- `govulncheck`、`golangci-lint`、`npm audit` 仍未进 CI（原建议，非阻断）。

### 5.8 🟡 P3 Docker 仍以 root 启动、未加 `USER`

`Dockerfile` 无 `USER`；`docker-entrypoint.sh:47-50` 在 root 下 `chown` 后 `exec su-exec $PUID:$PGID`，实际工作进程是非 root。**属既有设计**（需要 root 才能 chown 卷），FIXES.md 未声称修复；记录为 ⚪/🟡，不视为回归。`STOPSIGNAL SIGTERM` 已加（`Dockerfile:43`），停机预算 3s(Shutdown)+5s(manager.Stop)=8s < Docker 默认 10s ✅。

### 5.9 ✅ 其它已确认修复（附录）

- `MAX_CONCURRENCY` 仍强制 1（`config.go:137`），FIXES.md 声明为产品约定保留，CI 里那条错误注释已改（`ci.yml:58`）→ ⚪ 接受。
- `.dockerignore` 已覆盖 `.env*`/证书私钥/`review`/`docs`/`httpapi.test`/测试文件/`*.tsbuildinfo`（33 行）✅；`Dockerfile:16-17` 先 `COPY --from=web` 再 `sh scripts/sync-webui.sh`，镜像内嵌产物与 release 一致 ✅。
- `scripts/sync-webui.sh:9` 支持 `rm -rf internal/webui/dist` 后重建，`internal/webui/dist` 与 `apps/web/dist` 逐字节一致（21/21 文件；identity js/css/svg 按设计被移除）✅。
- `internal/webui/dist` 已跟踪文件 vs 当前产物：内容一致（在临时 git 副本中重建后 `git diff` 干净）✅。
- `go build ./...`、`go vet ./...`、`GOOS=windows go build ./...`、`GOOS=windows go vet ./...`、`go test -count=1 ./cmd/server/ ./internal/config/`、`go test -race -shuffle=on ./...`（10 包）、`cd apps/web && npx tsc --noEmit`、`gofmt -l cmd internal` 全部通过（本机 go1.27.1 darwin/arm64，`go.mod` go 1.26.6）。
- 运行期补充验证：`-web-dir` 不存在 → 自动回退内嵌资源（`GET /` = 200）；`-tls-auto` 生成证书 0600、tls 目录 0700；SIGTERM 正常退出码 0。

---

## 6. 未复核 / 待确认

1. **Windows 运行时行为**（§1.1 的结论来自同驱动同 DSN 的解析错误，非 Windows 真机；`lock_windows.go` 的 `LockFileEx` 语义、`lock_test.go` 在 Windows 上是否通过也未验）。
2. `docker build` 实测（本机无 Docker）——镜像能否构建、HEALTHCHECK 在真实镜像里的行为、CI 是否顶到 25min。
3. Actions 的 v7→v8 `download-artifact` 混用兼容性（无法运行工作流）。
4. `go mod tidy -diff`（离线不可跑；结论基于 import 关系推断）。
5. npm 依赖审计（`npm audit` 需联网）。
6. NFS/网络文件系统上 flock 的可靠性。

---

## 7. 结论与建议处置（按优先级）

1. **P1 必须修**：Windows sqlite DSN（§1.1）——现分支的 Windows release 二进制无法启动，且现有 CI 全绿也拦不住。改回纯路径或 `file:///C:/...`，并加平台相关的单测。
2. **P2 提交前必做**：`git add -A internal/webui/dist`（含 16 个新 sidecar），否则 CI 的 dist 闸门必红（§1.2）；同时把闸门改成能发现未跟踪新增（`git status --porcelain` / `git add` 后 `git diff --cached`）。
3. **P2 清理**：删除 `httpapi.test` 20MB 二进制并在 `.gitignore` 补规则（§1.3）。
4. **P2 修**：外部 webDir 下 ETag/body 不一致（§5.2）；env 下载目录导致整份保存 400（§5.5）；HEALTHCHECK 端口硬编码 + 探针路径（§5.4）。
5. **P2 决策**：`manager.Stop()` 失败路径 `os.Exit(1)` 与 systemd/docker 重启策略的语义（§4）。
6. **P3**：`Vary` 重复、go.mod `x/sys` indirect 注释、`listenPort` 死回退、`signal.NotifyContext` 注册时机、tsconfig.node 死配置、@types/node 版本、Google Fonts、`.gitignore` 缺 `*.test`/`pnpm-lock.yaml`。
