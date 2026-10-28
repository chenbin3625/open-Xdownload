# 代码审查 · cmd/server + config + webui + 运维构建

审查范围：`cmd/server/*.go`（含全部测试）、`internal/config/*.go`（含测试）、`internal/webui`（embed + dist 产物清单）、Docker/脚本/CI/依赖与前端构建配置。
约束遵守：未查看 git 历史/diff，未阅读 `README.md` 与 `docs/`；只读代码与配置；本文件是唯一写产物。
时间：2026-10-28（Asia/Shanghai）；仓库：`/Volumes/SSD 1/code/open-Xdownload`。

## 0. 阅读清单

| 文件 | 行数 | 已读完 |
|---|---|---|
| `cmd/server/main.go` | 154 | ✅ 逐行 |
| `cmd/server/listen.go` | 68 | ✅ 逐行 |
| `cmd/server/tls.go` | 100 | ✅ 逐行 |
| `cmd/server/http3.go` | 28 | ✅ 逐行 |
| `cmd/server/static.go` | 447 | ✅ 逐行 |
| `cmd/server/listen_test.go` | 81 | ✅ 逐行 |
| `cmd/server/tls_test.go` | 73 | ✅ 逐行 |
| `cmd/server/static_test.go` | 420 | ✅ 逐行 |
| `cmd/server/streaming_test.go` | 143 | ✅ 逐行 |
| `internal/config/config.go` | 416 | ✅ 逐行 |
| `internal/config/config_test.go` | 199 | ✅ 逐行 |
| `internal/webui/embed.go` | 21 | ✅ 逐行 |
| `internal/webui/dist/index.html` | 23 | ✅ 逐行（`.br/.gz` 只做清单与解压校验，未读压缩内容） |
| `internal/webui/dist` 产物清单 | 21 个文件（10 对 br/gz + index.html） | ✅ `ls -laR` + 全部 sidecar 解压比对 |
| `Dockerfile` | 42 | ✅ 逐行 |
| `docker-entrypoint.sh` | 53 | ✅ 逐行 |
| `.dockerignore` | 15 | ✅ 逐行 |
| `.gitignore` | 22 | ✅ 逐行 |
| `scripts/sync-webui.sh` | 21 | ✅ 逐行 |
| `scripts/dev-all.sh` | 62 | ✅ 逐行 |
| `.github/workflows/ci.yml` | 58 | ✅ 逐行 |
| `.github/workflows/release.yml` | 174 | ✅ 逐行 |
| `.github/workflows/dockerhub.yml` | 139 | ✅ 逐行 |
| `.github/dependabot.yml` | 17 | ✅ 逐行 |
| `.github/pull_request_template.md` | 23 | ✅ 逐行 |
| `.github/ISSUE_TEMPLATE/bug_report.yml` / `config.yml` / `feature_request.yml` | 60 / 5 / 39 | ✅ 逐行（只看流程与安全相关处） |
| `go.mod` | 30 | ✅ 逐行 |
| `go.sum` | 90 | ✅ 逐行（来源域名全部为 github.com / golang.org/x / modernc.org / go.yaml.in / go.uber.org / filippo.io） |
| `package.json`（根） | 14 | ✅ 逐行 |
| `apps/web/package.json` | 36 | ✅ 逐行 |
| `apps/web/package-lock.json` | 5357 | ⚠️ 抽查：根依赖与 `package.json` 完全一致；20 个直接依赖的 locked 版本全部满足 range；registry 全部为 `registry.npmjs.org`；仅 `fsevents` 有 installScript |
| `apps/web/tsconfig.json` | 20 | ✅ 逐行 |
| `apps/web/tsconfig.node.json` | 14 | ✅ 逐行 |
| `apps/web/vite.config.ts` | 106 | ✅ 逐行 |
| `apps/web/index.html` | 16 | ✅ 逐行 |

**为交叉验证而额外只读的文件**（不在产出范围，仅用于核对契约）：`internal/storage/storage.go`（Open/UpdateConfig/GetConfig/GetStoredConfig、schemaVersion）、`internal/httpapi/server.go`（路由表、getConfig/updateConfig、mergeSecretPlaceholders）、`internal/httpapi/bootstrap.go`（InjectIndexHTML/injectBootstrapHTML）、`internal/httpapi/origin.go`、`internal/httpx/encoding.go`（NegotiateEncoding）、`internal/httpx/httpx.go`（ValidateProxyURL）、`internal/jobs/manager.go`（Start/Stop/availableSlots）、`internal/downloader/downloader.go`（composeFilename/truncateUTF8Bytes）。

**执行过的验证命令（均未改动仓库文件）**

```
go build -o /tmp/xdownload-review-build ./cmd/server   # 通过
go vet ./cmd/server ./internal/config                   # 通过
gofmt -l cmd/server internal/config internal/webui      # 无输出
go mod verify                                           # all modules verified
go test ./cmd/server ./internal/config                  # ok（cached）
node brotli/gzip 解压 internal/webui/dist 全部 10 个 sidecar 与 apps/web/dist 构建产物逐字节比对 → 10/10 一致
```

## 1. P0 发现

**无。** 为排除 P0，逐项做了针对性验证：

- **目录遍历**：`static.go:114` 先 `path.Clean("/"+r.URL.Path)`，再由 `http.Dir`/`http.FileServer` 二次防护；`%2e%2e%2f`、`%5c`、`//`、绝对路径在 Linux/Windows 两种 `http.Dir` 行为下都落不到 `webDir` 之外（详见 P2-6 的测试缺口，不是漏洞）。
- **XSS（index.html 注入）**：`injectBootstrapHTML` 用 `json.Encoder` + `SetEscapeHTML(true)`（`internal/httpapi/bootstrap.go:88-108`），`<`/`>`/`&` 全部转成 `\u003c` 等，无法用 `</script>` 逃逸；注入响应为 `no-store`。
- **静态资源越权**：`/api/` 前缀独占（路由表全部在 `/api` 下，`server.go:69-100`），静态层不会遮蔽后端；不存在目录列表（`fileExists` 对目录返回 false，`static.go:436-447`）。
- **凭据进 HTML**：`bootstrap.go:70` 用 `cfg.Redacted()`，并有测试断言明文 token 不出现（`bootstrap_test.go:88-90,103-105`）。
- **CI 脚本注入**：三个 workflow 都没有把 `${{ github.event.* }}` 直接拼进 `run:`，而是走 `env:`（`dockerhub.yml:40-43`、`release.yml:111-116`）。
- **Docker 密钥泄漏**：最终镜像只 COPY 二进制与入口脚本，`data/`、`*.db` 已被 `.dockerignore` 排除。

## 2. P1 发现

### [P1-1] HTTP/3 子服务启动/运行失败会拖垮整个 TCP 服务

- 位置：`cmd/server/main.go:124-131`（配套 `cmd/server/http3.go:16-24`）
- 证据：
```go
	if http3Server != nil {
		go func() {
			if err := http3Server.ListenAndServeTLS(listen.certFile, listen.keyFile); err != nil && !isHTTPServerClosed(err) {
				log.Printf("http3: %v", err)
				stop()
			}
		}()
	}
```
  `stop()` 是 `signal.NotifyContext` 的 cancel（`main.go:82`），取消后主 goroutine 从 `main.go:133 <-ctx.Done()` 继续走 Shutdown 并 **正常返回**，即整个进程退出。
- 影响：HTTP/3 是可选增强（同一 handler 已由 TCP 正常提供）。只要 UDP 8787 被别的进程占用、宿主内核/容器网络不支持 QUIC、或 CA 证书只对 TCP 有效等任一条触发，**唯一可用的 TCP 服务也被一起关掉**；且退出码为 0（见 P1-2），运维侧看不到失败。这是"次要组件故障 → 全站不可用"的可用性放大。
- 建议：h3 失败只 `log.Printf` 告警并继续服务 TCP（同时从后续响应中去掉 Alt-Svc，避免浏览器继续尝试 h3）；若要 fail-fast，则必须在启动阶段同步 bind 并返回非零退出码。将 h3 是否 fatal 做成显式开关。

### [P1-2] 监听失败/TLS 加载失败以退出码 0 结束，编排器无法感知

- 位置：`cmd/server/main.go:114-123`（另有 `124-131`）
- 证据：
```go
	go func() {
		log.Printf("open-Xdownload API listening on %s://%s", listen.scheme(), addr)
		...
		if err := serveHTTP(server, listen); err != nil && !isHTTPServerClosed(err) {
			log.Printf("serve: %v", err)
			stop()
		}
	}()
```
  `main` 末尾（`main.go:133-147`）没有把错误传出，`stop()` 之后是正常的 Shutdown/Stop 流程，`main` 返回 → 进程退出码 0。
- 影响：以下场景全部"成功退出"：`-addr` 写成 `8787`（缺少端口，`newHTTPServer` 的 Addr 非法）、端口被占用、`-tls-cert/-tls-key` 文件不存在/权限不足/内容损坏（`ListenAndServeTLS` 加载失败）、`tls-auto` 之外的自签名问题。后果：`docker run --restart=on-failure` 不会重启、systemd 判为成功、脚本/CI 误判部署成功；同时"listening on ..."日志在 bind 之前打印（`main.go:115`），日志本身也会误导。
- 建议：用 `errCh`（或 `errgroup`）收集两个 server 的 fatal error 与 `ctx.Done()`，`select` 后若非 `ErrServerClosed`，`log.Printf` 原因并 `os.Exit(1)`（或 `log.Fatalf`）；把 "listening" 日志移到 `net.Listen` 成功之后（当前 `ListenAndServe` 内部才 bind）。

### [P1-3] `-tls-auto` 自签名证书的 SAN 与可配置监听地址脱节，TLS 在本项目的默认部署形态下必然证书不匹配

- 位置：`cmd/server/tls.go:35-44`、`cmd/server/main.go:35,72-80`、`Dockerfile:32`
- 证据：
```go
	template := &x509.Certificate{
		...
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
```
```go
	flag.StringVar(&addr, "addr", envOrDefault("OPEN_XDOWNLOAD_ADDR", "127.0.0.1:8787"), "HTTP listen address")
	...
	if tlsAuto && !listen.tlsEnabled() {
		certFile, keyFile, err := ensureDevTLS(filepath.Join(dataDir, "tls"))
```
  Dockerfile 里 `ENV OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787`。
- 影响：`tls-auto` 的语义是 "localhost self-signed"，但代码允许它与任意 `-addr` 组合且不告警。Docker 用户（默认 0.0.0.0）或局域网用户启用 `OPEN_XDOWNLOAD_TLS_AUTO=1` 后，用 `http://<LAN-IP>:8787`、容器主机名、`127.0.0.1` 以外的任何地址访问都会证书名不匹配：浏览器出现拦截页，curl/HTTP 客户端（默认校验）直接失败；HTTP/3 共用同一证书，问题一样。
- 建议：`ensureDevTLS` 接收 `addr`，把非 `0.0.0.0/::` 的主机名/IP 加入 SAN（`0.0.0.0` 时枚举本机地址或直接拒绝并提示）；或在 `tlsAuto && addr 非 loopback` 时 `log.Printf` 明确告警"证书只对 localhost 有效"。

### [P1-4] `RestoreAdditionalCookies` 在 ID 不匹配时会把占位符 `********` 当成真实凭据写库

- 位置：`internal/config/config.go:190-235`（关键分支 `204-228`），调用链 `internal/storage/storage.go:840-846`、`internal/httpapi/server.go:496-507`
- 证据：
```go
	storedPairs := withCookieIDs(ParseCookiePairs(stored))
	storedByID := make(map[string]AuthCookie, len(storedPairs))
	for _, pair := range storedPairs {
		storedByID[pair.ID] = pair
	}
	...
		storedPair, hasStored := storedByID[sub.ID]
		if !hasStored && i < len(storedPairs) && sub.ID == fmt.Sprintf("cookie-%d", i) {
			storedPair = storedPairs[i]
			hasStored = true
		}
		if hasStored {
			if auth == SecretPlaceholder || auth == "" { auth = storedPair.AuthToken }
			...
		}
		if auth != "" || csrf != "" {
			merged = append(merged, AuthCookie{ID: sub.ID, AuthToken: auth, CSRFToken: csrf})
		}
```
  若 `hasStored == false`，`auth`/`csrf` 会保持提交上来的 `SecretPlaceholder`（非空），随后原样进入 `merged` 并被 JSON 序列化写库。
- 触发路径（可复核）：
  1. 标签页 A 打开设置页（拿到 `cookie-0...cookie-N` 的脱敏 JSON），标签页 B 删掉一行并保存；A 再保存 → A 提交的某个 ID 在库中不存在、索引 fallback 又不成立 → `additional_cookies` 里出现 `authToken: "********"`。
  2. `withCookieIDs`（`config.go:308-315`）生成的 ID 是**按索引**的 `cookie-%d`，任何既不是该格式、又不在库中的 ID（如前端 `CookieSettings.tsx:455` 的 `backup-cookie-N`、或未来改成随机 ID 后的旧数据）都会落入同一分支。
- 影响：真实 cookie 被字面量 `********` 覆盖（不可逆，只能重新登录 X 复制），且 `ParseCookiePairs` 之后会把 `********` 当真实值发给 X → 该账号全部请求鉴权失败；用户侧只看到"突然登录失效"，没有任何错误提示。另外 `auth == ""` 分支意味着**用户无法通过清空字段来删除某一对中的单项**。
- 建议：合并结束后对仍等于 `SecretPlaceholder` 的字段直接丢弃该对（或返回 400 要求重新输入，绝不能落库）；ID 改为在**首次脱敏时随机生成并持久化**（不要用索引），并给"未知 ID + 占位符"补一条回归测试。

### [P1-5] 代理 URL 脱敏在"缺少 scheme / 被解析为 opaque"时失效，明文凭据经 `/api/config` 与 `/settings` HTML 回显（服务无鉴权）

- 位置：`internal/config/config.go:341-373`（判空条件 `351-354`；query 拼回 `364-367`）
- 证据：
```go
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil || u.Scheme == "" {
		return rawURL
	}
```
  实测 `url.Parse` 行为（`/tmp` 下独立程序验证）：

| 输入 | Scheme / Host | `u.User` | 脱敏结果 |
|---|---|---|---|
| `http://alice:s3cret@proxy.local:3128` | http / proxy.local:3128 | alice/s3cret | `http://********@proxy.local:3128` ✅ |
| `alice:s3cret@proxy.local:3128` | alice / "" | nil | **原样返回（明文）** ❌ |
| `//alice:s3cret@proxy:3128` | "" / proxy:3128 | alice/s3cret | **原样返回（明文，因 `u.Scheme == ""`）** ❌ |

  回显链路：`storage.GetConfig`（`storage.go:817-822`）先 `ApplyEnvOverrides`，`httpapi.getConfig`（`server.go:109-114`）再 `cfg.Redacted()` 写 JSON；`/settings` 页的 `InjectIndexHTML` 把同一份 redacted config 注入 HTML（`bootstrap.go:67-70`、`injectBootstrapHTML`）。`ValidateProxyURL` 只在 `PUT /api/config` 里调用（`server.go:129`），**环境变量注入的 `OPEN_XDOWNLOAD_PROXY_URL` 完全不校验**；而环境变量注入正是项目为"不把凭据落库/经 HTTP 回传"提供的机制（`config.go:12-20`）。
- 影响：一个常见的漏写 scheme（`user:pass@host:port`）会让代理口令以明文出现在 `GET /api/config` 响应和 `/settings` 页面 HTML 里；本服务无内置鉴权、Docker 默认监听 `0.0.0.0`，任何能访问端口的人即可读到。此外 `?token=...` 形式的 query 凭据也被原样拼回（`364-367`）。这与 `Redacted()`/`SecretPlaceholder` 的设计目标直接冲突。
- 建议：兜底规则改为"原始字符串里出现 `@` 且能识别 userinfo 形态就掩码"，而不是依赖 `url.Parse` 成功；解析失败/opaque 时整个 URL 只输出 `********`；对 `RawQuery` 中的常见凭据键（token/secret/password/key）做掩码；`ApplyEnvOverrides` 后也应跑一次 `ValidateProxyURL` 并在失败时拒绝启用该代理。

## 3. P2 发现

### [P2-1] `Vary: Accept-Encoding` 只在压缩响应上设置，identity 响应缺失

- 位置：`cmd/server/static.go:180-183`（注入 HTML）、`205-208`（普通资源）
- 证据：
```go
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
		header.Add("Vary", "Accept-Encoding")
	}
```
- 影响：响应表示随 `Accept-Encoding` 变化，但 identity 变体不带 `Vary`。共享缓存/反向代理（项目推荐前置反代）可能把 identity 版本缓存后无差别回放；HTTP 语义上也不符合 RFC 9110 对 Vary 的要求。实测 `TestGzipSkippedWithoutAcceptEncoding`（`static_test.go:108-119`）只断言没有 `Content-Encoding`，没有断言 `Vary`，因此这个缺陷不会被测试发现。
- 建议：无论是否压缩都 `header.Add("Vary", "Accept-Encoding")`（可在 `serveAsset` 开头统一设置），并补 identity 分支的测试断言。

### [P2-2] 静态资源缓存永不失效、无容量上限（外部 `webDir` 重建后继续提供旧内容，直到进程重启）

- 位置：`cmd/server/static.go:302-344`（`assetCache.get` 只写不淘汰）、`63-76`（`externalWebApp` 只在启动时 `os.Stat`）
- 证据：
```go
	c.mu.Lock()
	if existing, ok := c.items[name]; ok {
		c.mu.Unlock()
		return existing, true
	}
	c.items[name] = item
	c.mu.Unlock()
```
- 影响：默认 `-web-dir` 是 `apps/web/dist`（源码树里存在）。若开发者在服务运行期间执行 `npm run build`，缓存的 `index.html` 与旧 hash 资源会一直被回放，而磁盘上的旧 hash 文件已被 `emptyOutDir` 删除 → 页面白屏，必须重启 Go 进程才能恢复。另一个角度：`assetCache` 无上限，把 `-web-dir` 指向大目录（误配）时按请求文件大小无限占用 RSS。
- 建议：对**外部** `webDir` 禁用缓存（或按 mtime/size 校验），嵌入 FS 才缓存；给缓存加条目/字节上限（LRU）；`externalWebApp` 失败时打日志说明回落到 embed。

### [P2-3] 静态层对任意 method 都按 GET 服务（无 405）

- 位置：`cmd/server/static.go:196-218`（`serveAsset` 只在 HEAD 时特判，其余一律写 body）
- 证据：
```go
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(body)
```
- 影响：`POST/PUT/DELETE /assets/x.js` 返回 200 + 文件内容（`http.FileServer` 那条 Range 分支反而会返回 405，两条分支行为不一致）。虽无直接安全后果，但语义错误、会被扫描器/代理误判，也让缓存行为混乱。
- 建议：非 GET/HEAD 直接 `w.Header().Set("Allow", "GET, HEAD")` + 405。

### [P2-4] SPA fallback 的"有扩展名即 404"规则会错误拦截带点号的前端路由；`/api`（无尾斜杠）被当 SPA 返回 200

- 位置：`cmd/server/static.go:124-136`、`108-111`
- 证据：
```go
		if !ok {
			if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
			item, ok = cache.get("index.html")
```
- 影响：前端路由若包含 `.`（例如 `/user/john.doe`、版本化的 `/v1.2/...`）会得到 404 而不是 SPA；`/api` 精确路径不满足 `HasPrefix(r.URL.Path, "/api/")`，落进静态层 → 返回 `index.html`（200 HTML）而不是后端 404，客户端/脚本会拿到 HTML 当 JSON 解析。
- 建议：把 SPA fallback 收敛到"没有扩展名 且 method 是 GET/HEAD"或使用白名单路由前缀；`/api` 精确路径也交给 API handler（改为 `strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api"`）。

### [P2-5] sidecar 解压失败时静默返回 200 空 body（构建产物损坏被吞掉）

- 位置：`cmd/server/static.go:251-283`、`389-414`
- 证据：
```go
func (a *cachedAsset) ensureIdentity() []byte {
	...
	if len(a.brotli) > 0 {
		a.identity = decompressBytes(a.brotli, httpx.EncodingBrotli)
		return a.identity
	}
```
  `decompressBytes` 出错返回 `nil`，`serveAsset` 随后 `header.Set("Content-Length", "0")` 并正常 200。
- 影响：`internal/webui/dist` 里 js/css 只有 `.br/.gz`（见阅读清单），identity 全靠解压生成。sidecar 截断/损坏时浏览器收到"200 + 0 字节 JS"，表现为白屏且没有任何服务端错误；排查成本高。
- 建议：解压失败返回 500 并 `log.Printf` 资源名；在构建期（CI）加一条"dist 里每个 `.js/.css` 至少有一个 sidecar 且能解压"的校验。

### [P2-6] 安全敏感的静态层缺少对抗性测试

- 位置：`cmd/server/static_test.go`（全文 420 行）
- 证据：现有用例覆盖 404/SPA fallback、缓存头、gzip/br 协商、q=0、Range 绕过、sidecar-only、注入绕过 ETag、benchmark；**没有**任何 `..`、`%2e%2e%2f`、`%5c`、`//`、绝对路径、NUL、HEAD、`/api` 委托、identity 的 `Vary`、非 GET 方法、`/assets/`（目录形态）的用例。
- 影响：这段代码是唯一直接面向"磁盘任意文件"的读取面（`-web-dir` 可配置），逻辑改动（尤其 `name` 归一化）缺少回归保护；P2-1/P2-3/P2-4 若补测试都能被钉住。
- 建议：补表驱动遍历用例（期望 404 或 200 index.html，绝不出现 webDir 外文件）、HEAD 用例、`/api/x` 直通用例、Vary 断言。

### [P2-7] DB 及其 `-wal/-shm` 的 0600 加固只做一次且是 best-effort

- 位置：`cmd/server/main.go:63-70`
- 证据：
```go
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(dbPath+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			log.Printf("chmod db%s: %v", suffix, err)
		}
	}
```
- 影响：`storage.Open`（`storage.go:78-109`）里 `PRAGMA journal_mode=WAL` + `migrate()` + `EnsureConfig` 都会写库，因此正常路径下 sidecar 在此刻已存在，加固生效——但这是**隐式依赖**：若 Open 之后才创建 `-wal/-shm`（例如 sidecar 被外部删除后重连、或多进程共享库）、或 chmod 失败（只有一行日志），文件会保持 SQLite/umask 决定的 0644，而库里存的是明文 `auth_token/ct0`。另外注释声称"Restrict the SQLite file and its -wal/-shm sidecars to the owner"，实际只对启动瞬间存在的文件成立。
- 建议：`OPEN_XDOWNLOAD_DATA_DIR` 已 0700，可进一步在 `storage.Open` 内对 DSN 使用 `mode=rwc` 后立即设置 umask（`syscall.Umask(0o077)`，进程级、可接受）或在 Open 之后重试 chmod；把 chmod 失败升级为警告以上（至少在启动日志里显式列出权限未收紧的文件）。

### [P2-8] 关停时序：`RegisterOnShutdown` 回调异步、10s+15s 预算超过 Docker 默认 10s stop grace

- 位置：`cmd/server/main.go:106-108,133-147`；`internal/jobs/manager.go:69,101-124`（`managerStopTimeout = 15s`）
- 证据：
```go
	server.RegisterOnShutdown(eventBus.CloseAll)
	...
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	...
	if err := server.Shutdown(shutdownCtx); err != nil { log.Printf("shutdown: %v", err) }
	if http3Server != nil { if err := http3Server.Close(); err != nil { ... } }
	manager.Stop()
```
- 影响：`http.Server.RegisterOnShutdown` 的回调在独立 goroutine 中执行且 `Shutdown` **不等待**它们完成，因此 `eventBus.CloseAll` 与后续 `manager.Stop()`/`defer store.Close()` 之间存在竞态窗口（SSE handler 可能仍在运行）；`http3Server.Close()`（quic-go）是立即关闭连接，不是优雅排空，进行中的媒体下载会被截断；总预算最坏 10s + 15s = 25s，Docker 默认只给 10s 就 SIGKILL，DB 无法干净关闭（WAL 可恢复，但下载临时文件残留）。另外 `Shutdown` 返回错误（超时）时没有任何升级动作。
- 建议：`select { case <-done: case <-ctx.Done(): }` 之类显式串行化；h3 改用 `Shutdown(ctx)` 并纳入同一 10s 预算；把 `managerStopTimeout` 计入总预算并在 Docker/Compose 文档给出 `stop_grace_period: 30s`；Shutdown 超时打 error 日志。

### [P2-9] `log.Fatalf` 绕过 `defer store.Close()`（tls-auto 失败路径）

- 位置：`cmd/server/main.go:58-62` 与 `72-80`
- 证据：
```go
	store, err := storage.Open(dbPath)
	...
	defer store.Close()
	...
	if tlsAuto && !listen.tlsEnabled() {
		certFile, keyFile, err := ensureDevTLS(filepath.Join(dataDir, "tls"))
		if err != nil {
			log.Fatalf("tls-auto: %v", err)   // os.Exit(1)，defer 不执行
		}
```
- 影响：`os.Exit` 跳过 defer，SQLite 未 Close（WAL 未 checkpoint），依赖下次启动的恢复；同理后续若在启动段新增 `log.Fatalf` 会踩同一坑。数据不会丢（WAL），但属于"有意的资源清理被静默跳过"。
- 建议：`ensureDevTLS` 失败改为 `log.Printf` + `return` 一个错误码（或用 `run() error` 包一层，`main` 里 `os.Exit(1)`），保证 defer 生效。

### [P2-10] `validDevTLS` 只检查有效期，不校验私钥配对/SAN/NotBefore

- 位置：`cmd/server/tls.go:62-79`
- 证据：
```go
	cert, err := x509.ParseCertificate(block.Bytes)
	...
	return time.Now().Before(cert.NotAfter.Add(-24 * time.Hour))
```
- 影响：`dev.key` 被替换/损坏、cert 与 key 不配对、或 cert 的 SAN 与当前 `-addr` 不匹配时，`ensureDevTLS` 会**复用**这对文件；错误被推迟到 TLS 握手期（表现为浏览器 "SSL_ERROR_NO_CYPHER_OVERLAP"/连接重置），启动日志干净。`NotBefore` 在未来（时钟回拨/证书来自快照）也不检测。
- 建议：`validDevTLS` 用 `tls.LoadX509KeyPair` 验证配对，检查 `NotBefore/NotAfter` 与 SAN 覆盖当前 host；不满足则重新生成。

### [P2-11] `listen.validate()` 不校验地址与证书文件；`listenPort` 解析失败静默回退 443

- 位置：`cmd/server/listen.go:16-21`、`cmd/server/tls.go:81-87`
- 证据：
```go
func (o listenOptions) validate() error {
	if (o.certFile == "") != (o.keyFile == "") {
		return fmt.Errorf("tls-cert and tls-key must both be set")
	}
	return nil
}
```
```go
func listenPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "443"
	}
```
- 影响：`-addr` 缺端口/格式错误、`-tls-cert` 路径不可读都不会在启动早期被指出；Alt-Svc 会把 `h3=":443"` 广告给客户端，而实际监听在别的端口（或根本没起来），浏览器会尝试错误的 UDP 端口；且 443 硬编码对非特权端口部署不友好。
- 建议：`validate` 里 `net.SplitHostPort` 校验 addr、`tls.LoadX509KeyPair` 校验证书可读（早失败、清晰报错）；`listenPort` 解析失败应返回错误而不是猜测 443（`withHTTP3AltSvc` 拿不到端口时干脆不发 Alt-Svc）。

### [P2-12] `MaxConcurrency` 是死配置，且 CI 注释仍宣称"上限 64"

- 位置：`internal/config/config.go:113`；对照 `.github/workflows/ci.yml:52-53`、`internal/jobs/manager.go:253`
- 证据：
```go
func (cfg AppConfig) Normalized() AppConfig {
	...
	cfg.MaxConcurrency = 1
```
```yaml
      # 竞态检测必须在 CI 跑：本服务的并发写者可达数百（MaxConcurrency 上限 64，
      # 每个归档任务再开多个 worker），普通 go test 无法暴露数据竞争。
```
- 影响：`AppConfig.MaxConcurrency` 仍带 `json:"maxConcurrency"`/`db:"max_concurrency"`，`PUT /api/config` 接受任意值（如 8）并返回 200，但落库/使用恒为 1（`manager.availableSlots(1)`）——API 契约说谎；CI 注释与代码事实相反，会误导后续调优（`vite.config`/README 之外也可能有同类描述）。前端 `ConfigForm.tsx:312` 也硬编码 `maxConcurrency: 1`。
- 建议：二选一——要么真正启用该配置（去掉强制 1，加范围校验 1..64 与 UI 控件），要么删掉字段/列并同步修正 CI 注释；无论哪种都不要再保留"静默改写"。

### [P2-13] `RevertEnvOnlyEcho` 会静默丢弃"值与 env 相同"的合法保存

- 位置：`internal/config/config.go:39-47`
- 证据：
```go
func RevertEnvOnlyEcho(submitted, stored, envKey string) string {
	envValue := strings.TrimSpace(os.Getenv(envKey))
	if envValue == "" || submitted != envValue {
		return submitted
	}
	return stored
}
```
- 影响：用户若想显式把当前代理/token 持久化到 DB（值恰好等于 env），保存会成功返回、值却被回退成旧值，UI 无任何提示；多点几次也无济于事。调试"为什么保存没生效"的成本高。
- 建议：保存时在响应里回显"该字段由环境变量覆盖，已忽略持久化"（或在 UI 上把 env 来源字段标灰），并在代码注释/日志中体现；不要静默改写。

### [P2-14] 占位符还原逻辑三处重复且已经分叉

- 位置：`internal/config/config.go:167-235,341-416`、`internal/httpapi/server.go:496-507`、`internal/storage/storage.go:840-847`
- 证据：
```go
// httpapi/server.go:496
func mergeSecretPlaceholders(cfg config.AppConfig, current config.AppConfig) config.AppConfig {
	if cfg.AuthToken == "" || cfg.AuthToken == config.SecretPlaceholder { cfg.AuthToken = current.AuthToken }
	...
	cfg.AdditionalCookies = config.RestoreAdditionalCookies(cfg.AdditionalCookies, current.AdditionalCookies)
	cfg.ProxyURL = config.RestoreURLUserinfo(cfg.ProxyURL, current.ProxyURL)
	return cfg
```
```go
// storage.go:840-847（同一份逻辑 + RevertEnvOnlyEcho，顺序也不同）
	cfg = cfg.Normalized()
	if cfg.AuthToken == "" || cfg.AuthToken == config.SecretPlaceholder { cfg.AuthToken = current.AuthToken }
	...
	cfg.AuthToken = config.RevertEnvOnlyEcho(cfg.AuthToken, current.AuthToken, config.EnvAuthToken)
```
- 影响：httpapi 版本只用于校验（`server.go:129`），storage 版本才真正落库，两者行为已不同（httpapi 不做 `RevertEnvOnlyEcho`），且 `updateConfig` 的校验基准与落库基准不是同一个函数——后续任何一处改动都可能造成"校验通过但落库结果不同"，这正是 P1-4/P1-5 这类问题容易复发的土壤。
- 建议：把"提交值 + 已存值 + env → 最终值"收敛为一个 `config.ResolveSubmission(submitted, stored)`，httpapi 校验与 storage 落库都调用它（校验时用同一函数产出的值）。

### [P2-15] Docker 镜像缺 HEALTHCHECK、默认以 root 启动，且未提示 stop grace

- 位置：`Dockerfile:26-42`
- 证据：
```dockerfile
FROM alpine:3.22
RUN apk add --no-cache ca-certificates su-exec tzdata && mkdir -p /data /downloads
...
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["open-xdownload"]
```
  无 `HEALTHCHECK`、无 `USER`、无 `STOPSIGNAL`。
- 影响：编排器（Compose/K8s/NAS UI）无法判断容器是否真的可用（进程活着但 DB 打不开/端口没起来也算 healthy）；容器以 root 启动，安全性完全依赖 entrypoint 的 `su-exec`（用户覆盖 entrypoint 或 PUID=0 时就是 root）；关停预算 25s 与 Docker 默认 10s 不匹配（见 P2-8）。后端已提供 `GET /api/health`（`internal/httpapi/server.go:69,104-106`），直接可用。
- 建议：加 `HEALTHCHECK --interval=30s --timeout=3s --start-period=20s CMD wget -qO- http://127.0.0.1:8787/api/health >/dev/null || exit 1`（busybox wget 可用）；文档给出 `stop_grace_period: 30s`；注释解释"必须以 root 启动才能 chown 卷，随后 su-exec 降权"。

### [P2-16] `.dockerignore` 未排除 `.env*`、密钥文件与 `review/`

- 位置：`.dockerignore:1-15`
- 证据（全文只有 `.git/.github/.reference/data/downloads/logs/node_modules/apps/web/node_modules/apps/web/dist/internal/webui/dist/*.db*/*.tsbuildinfo/.DS_Store`）：对比 `.gitignore:2-4` 明确忽略了 `.env`/`.env.*`。
- 影响：开发机若存在 `.env`、`data/tls/dev.key`（已排除）、根目录 `*.pem`/`*.key` 或本地笔记/审查产物，会进入 build context 与 builder cache（最终镜像只 COPY 二进制，故不会直接泄漏，但 build cache/context 上传面扩大，且 `COPY . .` 会把它们放进 Go 构建层）。
- 建议：补 `.env`、`.env.*`、`*.pem`、`*.key`、`review/`、`coverage/`；保持与 `.gitignore` 同步。

### [P2-17] CI 不校验提交的 `internal/webui/dist` 与前端源码是否同步，也没有 `docker build` 冒烟

- 位置：`.github/workflows/ci.yml:43-58`
- 证据：
```yaml
      - name: Build web
        run: npm run build
      - name: Sync embedded web assets
        run: npm run sync:web
```
  随后只有 vet/test/build，没有 `git diff --exit-code -- internal/webui/dist`，也没有 `docker build`（Dockerfile 只在发版时由 dockerhub.yml 验证）。
- 影响：`internal/webui/dist` 是**提交进 git 的**（`.gitignore:11,15-16` 用 `dist/` + `!internal/webui/dist/` 保留），且 `go build` 的默认路径依赖它（`Dockerfile` 会重建，但源码直编/`go install` 不会）。改了前端却忘记 `npm run sync:web` 时，CI 会自己重写 dist 而永远绿灯，可能导致源码构建产物里带着旧 UI。Dockerfile 的回归（.dockerignore/entrypoint/多架构）也只在 main 发布时才发现。
- 建议：CI 加 `git diff --exit-code -- internal/webui/dist`（在 sync 之后）作为"嵌入产物已更新"的闸门；加一条 `docker build -t test .`（或 buildx 单平台）冒烟。

### [P2-18] dockerhub 的 CI gate 预算（20min）小于 CI 自身超时（25min），且 300s 无 CI run 即放行

- 位置：`.github/workflows/dockerhub.yml:46,64-84`；对照 `.github/workflows/ci.yml:19`
- 证据：
```yaml
# ci.yml
    timeout-minutes: 25
# dockerhub.yml
          deadline=$(( SECONDS + 1200 ))
          ...
            elif [ "$SECONDS" -ge 300 ]; then
              ...
              echo "::warning::No CI run found for $SHA, publishing without a CI gate."
              exit 0
```
- 影响：CI 正常但较慢（race 测试 + 前端构建可能 >20min）时 gate 会 `Timed out waiting for CI` 报错，导致 main 上的镜像发布失败（需要手动重跑）；反之，若 CI run 因调度延迟 5min 仍未创建，main 分支会**跳过 gate 直接发布**镜像——两条路径都与"只有 CI 通过才发布"的设计意图冲突。
- 建议：deadline 至少 30min（> CI timeout）；"无 CI run"在 branch 上也应默认失败或延长等待（例如 10min），把 `skip_ci_check` 保留为唯一显式放行手段。

### [P2-19] 所有 GitHub Action 用可变 major tag 固定，第三方 action 拥有 `contents: write`

- 位置：`ci.yml:22,25,30`；`release.yml:28,31,47,94,106,143,161,170`；`dockerhub.yml:93,97,100,104,119,129`
- 证据：`uses: actions/checkout@v7`、`uses: softprops/action-gh-release@v3`（release job `permissions: contents: write`，`release.yml:157-171`）等，全部是 tag 而非 commit SHA。
- 影响：供应链风险——tag 可被上游移动/仓库被接管时，CI 会用仓库 token（release 甚至可写仓库）执行任意代码；`softprops/action-gh-release` 是第三方，是这条链上权限最高的一环。
- 建议：全部改为 commit SHA（dependabot 的 github-actions 生态会自动提 PR 更新 SHA，`dependabot.yml:13-17` 已配置）；第三方 action 优先换成官方 `gh release create`。

### [P2-20] 前端存在未使用依赖 + 死配置，且没有路由级代码分割

- 位置：`apps/web/package.json:19`、`apps/web/vite.config.ts:68-91`、`apps/web/src/App.tsx`（无 `lazy()`/动态 import，实测 grep 仅测试文件里有 `import()`）
- 证据：
```json
    "react-player": "^3.4.0",
```
```ts
          if (
            /\/react-player\/|\/(?:@mux\/mux-player-react|cloudflare-video-element|dash-video-element|hls-video-element|...)/.test(id)
          ) return "player";
```
```ts
      resolveDependencies(_filename, deps) {
        // 入口只预加载壳层；页面 chunk 按需加载。
        if (!_filename.includes("index")) return deps;
        return deps.filter((dep) => !/(?:SettingsPage|player)/.test(dep));
      },
```
- 影响：`react-player` 全仓库无人 import（`grep -rn "react-player" apps/web/src` 为空），`manualChunks` 的 `player` 规则永远不会命中（`apps/web/dist` 里也没有 player chunk）；`resolveDependencies` 过滤的 `SettingsPage` 没有任何动态 import，注释与事实不符。同时因为全部页面静态 import，`dist/assets/index-*.js`（38KB gz）一次性加载整站，路由切换没有懒加载收益。
- 建议：删除 `react-player` 与两条死配置；若要注释所说效果，给 `SettingsPage`/播放器改用 `React.lazy(() => import(...))`，再保留 `manualChunks` 规则。

## 4. P3 与优化建议

1. `cmd/server/main.go:19-23` — `version/commit/date` 三个变量只由 ldflags 写入、**全仓库无任何引用**（也无 `--version` 或 `/api/version`）。`Dockerfile:22-24`、`release.yml:121-123` 注入的元数据实际不可见，发版二进制无法自证版本。建议加 `-version` flag 或 `/api/health` 里带版本字段。
2. `cmd/server/static.go:221-227` — `setCacheControl` 对 `assets/` 前缀一律 `immutable`，隐含"文件名带内容哈希"。将来放任何非哈希文件（如 `assets/logo.png`）会被缓存一年。建议同时校验文件名含 hash 形态，或把 immutable 限制到 `assets/*-[A-Za-z0-9_-]{8}.{js,css}`。
3. `cmd/server/static.go:229-236` — `etagMatch` 逐段精确比较：不支持 `W/` 前缀、`*`、大小写无关的弱比较。建议用 `http.ServeContent` 或至少处理 `*`。
4. `cmd/server/static.go:114-122` — 请求 `/assets/`（`path.Clean` 后为 `assets`）不含 `/` 前缀且无扩展名 → 走 SPA fallback 返回 index.html；目录形态的静态路径语义混乱。建议目录请求一律 404。
5. `cmd/server/static.go:346-349` — 直接访问 `.br/.gz` 返回 404，但不带统一的错误体/Cache-Control；建议与其它 404 对齐并加 `Vary`/`no-store`。
6. `cmd/server/static.go:150-154` — Range 分支把"是否存在 identity 文件"当作开关，且不处理 `If-Range`、不当 Range 与压缩协商交互；对 sidecar-only 资源静默忽略 Range 返回 200（有测试固定此行为，但应在响应头说明 `Accept-Ranges: none`）。
7. `cmd/server/listen.go:55-61` — Alt-Svc 也发在 `/api/*`、404、以及 h3 自身的响应上；`ma=86400` 无 `persist` 选项，且没有按实际 scheme/端口计算。建议只在 HTML/静态响应上发，并考虑 `persist=1` 与更短的 `ma`（便于排障）。
8. `cmd/server/http3.go:16-24` — 未调任何 QUIC 参数（idle timeout、并发 stream、拥塞控制）；`Close()` 非优雅。建议 `http3.Server{IdleTimeout, MaxIncomingStreams, ...}` 显式化，并用 `Shutdown(ctx)`。
9. `cmd/server/main.go:82` — `signal.NotifyContext` 在 `store.Open`（含 WAL 切换重试、迁移）之后注册；迁移慢（大库）时收到 SIGTERM 会走默认处理直接杀进程。建议在 `main` 最开头注册。
10. `cmd/server/main.go:101` — `withWebApp` 在 `webDir` 不存在时静默回落 embed（`static.go:53-61`），无日志；默认 `apps/web/dist` 是相对 CWD 的路径，从别的目录启动会让"改了前端却看不到效果"难以定位。建议启动时打一行 "web assets: external <dir> | embedded"。
11. `cmd/server/tls.go:18-26` — `tls` 目录只在创建时 `0700`，已存在时不收紧（不像 `dataDir` 有 `os.Chmod`）；证书/私钥写文件非原子、无 `fsync`，中断会留下半个文件（下次因 `validDevTLS` 失败重新生成，尚可自愈）。建议与 dataDir 对齐。
12. `cmd/server/tls.go:89-96` — `envEnabled` 只认 `1/true/TRUE/yes/YES`，不认 `on/y/True/带空格`；建议 `strings.ToLower(strings.TrimSpace(...))` + `strconv.ParseBool` 风格。
13. `internal/config/config.go:147-156` — `defaultDownloadDir` 直接用 env/env 值，不做 `filepath.Clean`、不展开 `~`、不校验存在；该值只在**首次** `EnsureConfig` 时落库（`storage.go:737-780`），之后改 env 不再生效（用户容易误以为改 ENV 就能迁移下载目录）。
14. `internal/config/config.go:98-107` — `Default()` 只显式列了部分字段，`AutoFollowProtected/IncludeNestedTweetMedia/IncrementalArchive` 依赖零值；建议显式写 false 并在注释里说明语义。
15. `internal/config/config.go:238-306` — `ParseCookiePairs` 文本模式按空格/`;`/`,` 切分：值中含空格会截断，传入 JSON 对象（非数组）会静默走文本模式并返回 nil；错误全部吞掉（无日志）。建议解析失败时返回 error 供 API 提示。
16. `internal/config/config.go:308-315` — `withCookieIDs` 用索引生成 ID，是 P1-4 的根因之一；建议在第一次脱敏时生成稳定随机 ID 并持久化。
17. `internal/config/config.go:158-163` — `redact("") == ""`，UI 无法区分"未配置"和"已脱敏但值为空"，也无法区分"env 注入"。建议 UI 用独立布尔（`authTokenSet`）表达。
18. `Dockerfile:3,10,26` — 三个基础镜像都用浮动 tag（`node:22-alpine`/`golang:1.26-alpine`/`alpine:3.22`），未固定 digest；且 `go.mod:3` 要求 `go 1.26.6`，`golang:1.26-alpine` 需实际 patch ≥6 才能构建（当前成立，但 tag 漂移后会突然失败）。建议 `golang:1.26.6-alpine` 或 digest。
19. `Dockerfile:22-24` — 未用 BuildKit cache mount（`--mount=type=cache,target=/root/.cache/go-build`），每次源码改动全量重编；`-s -w` 后无法栈回溯（可接受），`-buildvcs` 未显式关闭（当前 context 无 `.git`，行为依赖构建方式）。
20. `Dockerfile:32-36,40` — `EXPOSE 8787` 只有 TCP；启用 TLS 时 HTTP/3 需要 `-p 8787:8787/udp`，镜像与注释都没有提示。
21. `docker-entrypoint.sh:31-45` — `mkdir -p "$dir"` 失败（只读 rootfs、卷未挂载）会因 `set -e` 直接退出且无友好提示；已有目录只比较顶层 `stat`，`chown -R` 在大下载目录上可能很慢；未设置 `umask 0077`（下载文件默认权限由进程 umask 决定，容器内为 0022）。
22. `docker-entrypoint.sh:17-29` — PUID/PGID 只校验"纯数字"，无范围检查；`PUID=0` 时静默以 root 运行，不打印警告（审计日志里看不到降权失败）。
23. `scripts/sync-webui.sh` 权限为 `0644`（`ls -l scripts`：`dev-all.sh` 是 `0755`），但带 shebang 且是可执行脚本；虽然 `package.json:9` 用 `sh scripts/sync-webui.sh` 规避了，直接 `./scripts/sync-webui.sh` 会 permission denied。另：脚本不校验 sidecar 是否齐全（`.br/.gz` 缺失时会删掉对应 identity，见 `sync-webui.sh:15-21` 的 `if [ -f "${file}.br" ] || [ -f "${file}.gz" ]`——该判断实际是安全的，但建议反过来断言"删除后每个压缩扩展名仍有 sidecar"）。
24. `scripts/dev-all.sh:44,52-61` — 打印 "http://0.0.0.0:8787"，但服务默认绑 `127.0.0.1:8787`（`main.go:35`），提示与实际不符；用 `go run` 作为子进程，`kill $backend_pid` 只作用于 go 工具进程，编译出的 server 子进程是否随之退出取决于 Go 版本的信号转发，可能残留占用 8787。
25. `package.json:5-13` — 无 `engines`/`packageManager`；`test` 串行执行 `go test`、前端测试、前端 build，没有 lint/format 入口。建议加 `lint`/`fmt` 脚本并在 CI 调用。
26. `apps/web/package.json` — 无 `engines`（CI/Docker 用 Node 22，开发机实测 Node 24）；`@types/node: "^26.5.1"` 与运行时 Node 22 不匹配，可能用到 Node 22 不存在的 API 类型。建议 `engines.node: ">=22 <25"` 并让 types 与运行时同主版本。
27. `apps/web/tsconfig.node.json`（14 行）是死配置：`apps/web/package.json:8` 只用 `tsc --noEmit -p tsconfig.json`，而 `tsconfig.json:19` 已把 `vite.config.ts` 纳入 include；`composite: true` 没有 project references 配合。建议删除或改成 references 结构。
28. `apps/web/tsconfig.json:7` — `skipLibCheck: true` 会掩盖依赖 d.ts 冲突；未开 `noUnusedLocals`/`noUnusedParameters`/`noUncheckedIndexedAccess`/`noFallthroughCasesInSwitch`。建议至少开后两项（后端项目尤其需要索引安全）。
29. `apps/web/vite.config.ts:63-67` — 未设 `base`，前端只能部署在站点根路径；`build.target` 未显式（tsconfig 是 ES2022，Vite 默认 target 是浏览器 baseline，两者可能不一致）；`precompress()` 的 `closeBundle` 在 `vite build --watch` 下不会清理旧 sidecar（`emptyOutDir` 由 Vite 负责，风险低）。
30. `apps/web/index.html:8-10` — 依赖 Google Fonts 三个外链（CSP 已放行）：离线/内网部署会看到字体回退，且每次首屏向第三方发请求（隐私/可用性）。建议自托管字体或用系统字体栈。
31. `.github/ISSUE_TEMPLATE/config.yml:3-5` — 联系链接指向个人主页 `https://github.com/chenbin3625` 而非文档/安全披露渠道；仓库无 `SECURITY.md`，与本项目"持有 X Cookie、无内置鉴权"的敏感度不匹配。建议加安全策略与私下披露邮箱。
32. `.github/dependabot.yml` — 缺 `package-ecosystem: docker`（Dockerfile 基础镜像）、无 `groups`、`open-pull-requests-limit: 1` 会让 gomod/npm/actions 的更新互相排队很久。
33. `.github/pull_request_template.md:21` — 勾选项要求"已阅读 README 贡献指引"，但仓库没有 `CONTRIBUTING` 文件；与"不要依赖文档"的现状容易漂移。
34. `.github/workflows/ci.yml` — 无 `gofmt -l`/`golangci-lint`/`govulncheck`/`npm audit`/CodeQL；`go test -race ./...` 没有 `-shuffle=on`。建议至少加 gofmt 与 govulncheck（本审查实测 `gofmt -l` 干净、`go mod verify` 通过，可作为基线）。
35. `.gitignore:11,15-16` — `dist/` + `!internal/webui/dist/` 的"排除再放行"组合可用但脆弱（要求放行项永远在排除项之后，且父目录未被排除）；建议改成显式 `/apps/web/dist/`、`/dist/`。
36. `cmd/server/streaming_test.go:50,125` — 用 `http.Get` 无 client 超时，端口/握手异常时测试可能挂到 go test 全局超时；建议 `&http.Client{Timeout: ...}` 或 `httptest.NewServer`。
37. `cmd/server/tls_test.go:10-45` — 只断言 `dev.key` 存在，未断言 `0600` 权限（安全属性无测试保护）；也未覆盖"证书过期/被替换后重新生成"。建议补 chmod 断言与过期再生用例。
38. `internal/config/config_test.go` — 缺 P1-4/P1-5 的回归用例：未知 ID + 占位符、`SameURLAuthority` 默认端口（`http://h` vs `http://h:80`）、`RestoreURLUserinfo` 保留 query、`RedactAdditionalCookies` 对不可解析输入的策略。
39. `internal/config/config.go:375-390` — `SameURLAuthority` 把 `http://h` 与 `http://h:80` 判为不同 authority（凭据被丢弃，方向安全但会造成"改个端口就丢密码"的困惑）；未做 IDNA/大小写端口的规范化。建议用 `url.URL.Hostname()+Port()` 并补默认端口。
40. `internal/downloader/downloader.go:550-561` 与 `internal/config/config.go:64-66` — `16/120/240` 三个文件名字长常量在两处各写一份；`config` 改上限时 downloader 不会跟随。建议 downloader 引用 `config` 常量（跨模块，交给 jobs/downloader reviewer 交叉确认）。

## 5. 待确认 / 需 Lead 交叉验证

1. **Docker 镜像未实际构建**：本机无 docker（`command -v docker` 为空）。Dockerfile/entrypoint 的可运行性（`busybox stat -c`、`su-exec` 路径、多架构 QEMU 下 `apk add`、`COPY --chmod`）只能静态审查；P2-15/P2-16 与可运行性结论建议在 CI 补一次 `docker build` 后复核。
2. **基础镜像与工具链版本存在性**：本地为 go1.27.1 / node24，而 CI 用 `go.mod` 的 `go 1.26.6`、`node-version: 22`，Docker 用 `golang:1.26-alpine`/`node:22-alpine`。离线无法确认这些 tag 当前解析到的 patch；若 `golang:1.26-alpine` 的 patch < 1.26.6，`go build` 会直接失败（`go.mod:3`）。建议 CI 增加 Dockerfile 构建或改成 `golang:1.26.6-alpine`。
3. **Action 版本真实性**：`checkout@v7`、`setup-go@v7`、`setup-node@v7`、`upload-artifact@v7`、`download-artifact@v8`、`docker/*@v4/v6/v7`、`softprops/action-gh-release@v3` 在本次时间线（2026-10）看起来合理，社区 PR 也显示 v7/v8 已存在（如 [actions/checkout v7 升级 PR](https://git.fiduswriter.org/fiduswriter/fiduswriter-gitrepo-export-plugin/pulls/5)、[artifact v7/v8 升级](https://lore.kernel.org/git/5d719b3729e39d63ec0a1a474b0c1ff57570133e.1777114720.git.gitgitgadget@gmail.com/)），但我未逐个核实可解析性——需要一次真实 CI 运行确认。
4. **提交的 `internal/webui/dist` 是否与源码同步**：我用 node 把 `internal/webui/dist` 的 10 个 sidecar（7 js + 1 css + icon.svg + index.html）全部解压，与 `apps/web/dist`（当前会话内另一进程于 08:23 产生）逐字节比对，**10/10 一致**，且 `index.html` `diff` 完全相同、文件集合除"被 sync 删除的 identity"外一致。因此结论是"当前是同步的"，但 `apps/web/dist` 的构建来源不在我可控范围内，建议 Lead 在干净环境执行 `npm ci && npm run build:embedded` 后 `git status/diff` 复核一次。
5. **`MaxConcurrency` 的历史语义**（P2-12）：`internal/jobs/manager.go:253` 拿到恒为 1 的值；若 jobs 侧原本支持并发、只是被 config 归一化关掉，则 CI 注释（`.github/workflows/ci.yml:52-53`）与可能的 README 描述都需要统一说法。请 jobs reviewer 确认。
6. **DB `-wal/-shm` 生命周期**（P2-7）：我的结论"启动 chmod 生效"依赖 `storage.Open` 的 `ensureWALMode`/`migrate`/`EnsureConfig` 在返回前确实创建了 sidecar。请 storage reviewer 确认是否存在"Open 返回后才创建 sidecar"的路径（尤其 `PRAGMA wal_checkpoint`/多连接/只读打开）。
7. **`RestoreAdditionalCookies` 的前端 ID 生命周期**（P1-4）：`CookieSettings.tsx:455` 会生成 `backup-cookie-N`；我确认了"未知 ID + 占位符 → 占位符落库"这条代码路径，但没完整读 `CookieSettings.tsx` 的保存/回填流程（属前端 reviewer 范围）。请前端 reviewer 确认是否还有别的"提交 ID 与库内 ID 不一致"的常规路径，以便定级（P1 或 P0）。
8. **`.dockerhub/README.md`（17KB，Docker Hub 镜像描述）未审**：按用户约束文档不算审查对象，但它是公开发布面的"配置说明"；若 Lead 认为需要核对镜像 ENV/PUID/端口说明与实际行为一致性，可另行指派（本次未读）。
9. **TLS/HTTP3 是否在文档中被承诺**：我不读 README，`tls-auto`/`tls-cert` 的对外承诺强度（"HTTPS+HTTP/2+HTTP/3 一键可用"？）会直接影响 P1-3 的定级；请 Lead 结合文档确认。
10. **`origin.go` 的 Host/Origin 策略与 Docker 部署**：`hostAllowed`（`internal/httpapi/origin.go:42`、定义在 `:94`）依赖 `OPEN_XDOWNLOAD_ALLOWED_HOSTS`，Dockerfile 未设置该 ENV；局域网/域名访问时的行为属 httpapi reviewer 范围，但与本审查的 Docker 配置有交叉，建议一并核对。

## 6. 设计上值得肯定之处

1. **无鉴权服务的安全默认值处理得当**：默认 `127.0.0.1:8787`（`main.go:32-35` 有明确注释解释为什么不默认 0.0.0.0），Docker 才用 ENV 显式放开，并在 Dockerfile/README 层面反复提示"前置带鉴权的反向代理"。
2. **静态服务的防遍历是双层的**：`path.Clean` 归一化 + `http.Dir` 内置校验 + 对目录不返回列表（`fileExists` 显式排除目录），而且 sidecar 直访 404、目录枚举不可达。
3. **预压缩 + 内存缓存 + 分层缓存头**的设计很完整：构建期 `vite.config.ts:37-61` 生成 br/gz（`sync-webui.sh` 进一步删掉 embed 里的 identity 减小二进制），运行期首次读取后缓存压缩体，`immutable/no-cache` 按路径分层，ETag 304 短路；`sidecar-only` 资源还能按需解压出 identity。`static_test.go` 对 sidecar-only、q=0、字节稳定性、注入绕过 ETag 都有覆盖。
4. **安全响应头齐全且有测试守护**：CSP（含 `frame-ancestors 'none'`、`object-src 'none'`、`script-src 'self'`）、`nosniff`、`Referrer-Policy`、`X-Frame-Options`；`streaming_test.go:72-111` 还专门断言 `script-src` 不得放开 `unsafe-*`。
5. **首屏 bootstrap 注入的 XSS/泄密防护正确**：`json.Encoder.SetEscapeHTML(true)` 阻断 `</script>` 逃逸，`Redacted()` 保证明文 token 不进 HTML，`InjectIndexHTML` 有"不得包含明文 token/不得串页"的测试，注入响应 `no-store`。
6. **配置脱敏/回填体系整体思路正确**：`SecretPlaceholder` 常量、`Redact/Restore`、`SameURLAuthority`（同 host 才回填、换 host 丢弃）、`RevertEnvOnlyEcho`（env 注入不落库）、`RestoreURLUserinfo` 的"换 authority 就丢弃凭据"——安全方向都是对的，且有较完整的单测（P1-4/P1-5 是边界未收敛，不是思路错误）。
7. **关停顺序考虑周到**：先 `eventBus.CloseAll` 断开 SSE 长连接、再 `manager.Stop()`、最后 defer `store.Close()`，并有 `streaming_test.go:114-143` 用真实 SSE 连接验证"Shutdown 不会被长连接拖满超时"；`manager.Stop()` 的 15s 有界等待也防止 SIGTERM 永久挂起。
8. **数据目录/数据库权限有明确意图**：`dataDir` 0700（含对已存在目录的收紧）、DB 与 sidecar 0600，注释直接点明"DB 里明文存 auth_token/ct0 且 modernc sqlite 默认 0644"——这种"把为什么写进注释"的习惯很好。
9. **Docker 构建工程质量高**：多阶段 + `--platform=$BUILDPLATFORM` 交叉编译 + `TARGETOS/TARGETARCH`、构建上下文最小化、entrypoint `exec` 保持 PID1 语义、chown 失败不杀容器（NFS root_squash 场景）、`COPY --chmod=0755`。CI 里 `permissions: contents: read` 最小权限、`concurrency` 取消过期 PR 构建、前端只在 release 的单独 job 构建一次并作为 artifact 分发、dockerhub 发布前有 CI gate、artifact 命名有意避开 `open-xdownload-*` 以免被当成 release 资产——这些都是踩过坑之后的正确设计。
10. **前端构建链严谨**：`tsc --noEmit` 前置到 `build`、`strict: true`、`isolatedModules`、`moduleResolution: Bundler`、`type: module`、`skipLibCheck` 有取舍；`npm ci` 在 CI/Docker 双处使用、lockfileVersion 3、依赖版本与 lock 完全一致、registry 单一且无异常 installScript（仅 `fsevents`，macOS 可选依赖）——依赖卫生状况良好（`go mod verify`、`gofmt -l`、`go vet`、本范围单测全绿）。
11. **测试对"运维契约"的覆盖**：`httpx.NegotiateEncoding` 的 q=0 拒绝、`TestShutdownDoesNotWaitForOpenEventStream`、`TestBootstrap not embed plaintext token`、`TestReadTimeoutDoesNotKillStreamingResponse`——这些测试保护的正是本次审查关注的运行时契约，而不是纯实现细节。
