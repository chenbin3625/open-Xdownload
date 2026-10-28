# 代码审查 · internal/xclient + internal/parser

审查范围：`internal/xclient`（client.go、ratelimit.go、client_test.go、hardening_test.go）与 `internal/parser`（service.go、model.go、service_test.go）。
约定：仅阅读代码，未使用 git 历史 / git diff，未阅读 README.md 与 docs/（按用户要求）。为核对跨包调用契约，只读阅读了 `internal/jobs/manager.go`、`internal/httpapi/server.go`、`internal/httpx/httpx.go`、`internal/downloader/downloader.go`、`internal/config/config.go` 的相关片段（未修改任何源码/测试/配置）。
严重度：P0=安全漏洞/数据损坏/必然崩溃；P1=高概率功能错误/竞态/泄漏/鉴权边界缺失；P2=边界与错误处理/性能/可维护性；P3=风格/命名/重复/注释。

## 0. 阅读清单

| 文件 | 行数 | 已读完 |
|---|---|---|
| internal/xclient/client.go | 1166 | 是（1–1166，分 4 段） |
| internal/xclient/ratelimit.go | 203 | 是（1–203） |
| internal/xclient/client_test.go | 602 | 是（1–602，分 2 段） |
| internal/xclient/hardening_test.go | 48 | 是（1–48） |
| internal/parser/service.go | 846 | 是（1–846，分 3 段） |
| internal/parser/model.go | 68 | 是（1–68） |
| internal/parser/service_test.go | 492 | 是（1–492，分 2 段） |

补充验证（只读，不修改仓库）：
- `go vet ./internal/xclient ./internal/parser` 通过；`go test -count=1`、`go test -race -count=1` 两个包均通过。
- 为判断 `syndicationToken` 是否是线上可用的算法，用 Node 计算了 X 前端等价写法 `((Number(id)/1e15)*Math.PI).toString(36).replace(/(0+|\.)/g,'')`，并对 `cdn.syndication.twimg.com/tweet-result` 做了无凭据探测（结论见 P3-12 / 第 5 节，涉及外网实测已注明）。

## 1. P0 发现

**未发现 P0。**

说明：parser 对媒体 URL 的 twimg.com 白名单（service.go:626-677）与 xclient 的跨域重定向拒绝（client.go:233-241）把最可能成为 SSRF / 凭据外泄的路径都堵住了；未发现必然崩溃、数据损坏或未授权访问。P1-1 在"只配置了一个 Cookie"的部署里表现为列表/关注任务完全不可用，但属于可用性失效而非数据损坏，因此仍记为 P1。

## 2. P1 发现

### [P1-1] List / ListMembers / Following / Follow 只绑定 Primary Cookie，绕过了 Pool 的换 Cookie 与限流交还机制

- 位置：`internal/xclient/client.go:492`（GetListByID）、`:668`（GetListMembers）、`:705`（GetFollowing）、`:742`（FollowUser）；消费方 `internal/jobs/manager.go:588`、`:610`、`:642`、`:659`。
- 证据：

```go
// internal/jobs/manager.go:588-590（processList）
client := pool.Primary()
list, err := client.GetListByID(ctx, strings.TrimSpace(job.Input))
...
// internal/jobs/manager.go:610
members, err := client.GetListMembers(ctx, list)

// internal/jobs/manager.go:642 / 659（processFollowing）
client := pool.Primary()
owner, err := pool.GetUserByInput(ctx, job.Input)   // 这一步走 Pool，会故障切换
...
members, err := client.GetFollowing(ctx, owner)     // 这一步只走主 Cookie
```

上述 xclient 方法全部是 `*Client` 方法，内部直接 `c.graphQL(...)`（client.go:676、713），而"换 Cookie + 429 立即交还"只实现在 `Pool.graphQL` 里：

```go
// internal/xclient/client.go:784
payload, err := client.graphQL(withYieldOnRateLimit(ctx), path, values)
// internal/xclient/client.go:826-829：只有带 yield 标记的请求才会把 429 交还给 Pool
if yieldsOnRateLimit(ctx) && c.limiter != nil && c.limiter.wouldBlock(path) {
    return nil, err
}
```

- 影响：
  1. 主 Cookie 一旦被 401/403 标记禁用（client.go:819-821、`doOnce` 在 client.go:842-844 直接返回 `ErrClientDisabled`），列表/关注任务的 `GetListByID`/`GetListMembers`/`GetFollowing` 立即失败，**即使 AdditionalCookies 里的备用 Cookie 完全可用**；任务在 `processList`/`processFollowing` 直接 `m.fail`（manager.go:595、616、665）。
  2. 主 Cookie 遇到带 `X-Rate-Limit-*` 或 `Retry-After` 的 429 时，`do()` 的重试会进入 `limiter.before()` 阻塞到 `reset+5s`，上限 `maxRetryAfter = 15m`（ratelimit.go:27-55、:120）。请求不是"交还给 Pool 换 Cookie"，而是在主 Cookie 上最长卡 15 分钟；`processList`/`processFollowing` 因此可能长时间停在 resolving。
  3. 同一任务里 `pool.GetUserByInput`（会切换 Cookie）与 `client.GetFollowing`（不会）混用，诊断上会出现"解析用户成功、取关注列表却全部失败"的不一致。
- 建议：
  - 为 Pool 增加 `GetListByID`/`GetListMembers`/`GetFollowing`（以及可选的 `FollowUser`）包装：用 `p.Select(ctx, path)` + `p.graphQL(ctx, path, values)`，path 继续作为限流 key（与 UserMedia 的 per-query 粒度一致），jobs 改为调用 Pool 版本。
  - 至少让这些方法在 `ErrClientDisabled`/429 时返回可被 jobs 识别、可重试的信号，由 jobs 触发换池重试；而不是直接让整个 list/following 任务失败。
  - 补测试：primary 已禁用 / 返回 429，backup 正常时，`GetListMembers`/`GetFollowing` 应能从 backup 成功（现有 client_test.go 只覆盖了 `GetUserByInput` 的池级回退，client_test.go:64-93）。

## 3. P2 发现

### [P2-1] `apiError` 的"data 非空对象即成功"启发式会吞掉顶层错误码，破坏错误分类

- 位置：`internal/xclient/client.go:928-944`
- 证据：

```go
if data := gjson.GetBytes(payload, "data"); data.IsObject() && len(data.Map()) > 0 {
    return nil
}
code := -1
if raw := errorsResult.Get("0.code"); raw.Exists() { code = int(raw.Int()) }
return &APIError{Code: code, Raw: limitedErrorPayload(payload)}
```

判定只看 `data` 是否为非空对象，不检查其值是否全为 null。`{"data":{"user":null},"errors":[{"code":88,"message":"Rate limit exceeded"}]}` 会被判成功，随后 `parseUser(data.user)` 返回"响应中没有用户 legacy 数据"或 `timelineItems` 返回"无法解析 timeline instructions"。测试只覆盖了 `data:null` 与 `data:{}` 两种（client_test.go:551-576），没有非空对象 + null 值的用例。
- 影响：88（限流）/326（账号锁定）等错误码被伪装成解析错误；`Pool.graphQL` 认为这是非瞬时错误直接 `return nil, err`（client.go:792-794），不会切换到备用 Cookie，manager 的 `shouldAbortArchiveUsers`（依赖 `IsAllClientsRateLimited`，client.go:64-66）也不会触发中止，批量子任务会继续逐个撞墙。错误信息也不会说明真实原因。
- 建议：把"部分成功"的判定收紧为"data 中目标路径可解析"或至少"data 的对象值不全为 null"；更稳妥的是让调用方把顶层 errors 交回：`apiError` 返回 `(payload, apiErr)` 由调用方在 data 缺失时优先报 API 错误；补 `data:{"user":null}` 的用例。

### [P2-2] timeline 响应异常/逐条解析失败被静默当成"成功但没数据"，增量归档下会永久漏档

- 位置：`internal/xclient/client.go:568-574`、`:597-600`、`:1089-1137`
- 证据：

```go
items, next, err := timelineItems(payload, "data.user.result.timeline_v2.timeline.instructions")
if err != nil { return nil, err }
if len(items) == 0 { break }              // 形状不认识也走到这里
...
tweet, err := parser.TweetFromGraphQLResultWithOptions("", user.ScreenName, "", result, options)
if err != nil || len(tweet.Media) == 0 {  // 解析错误被静默丢弃，无日志/计数
    continue
}
```

`timelineItems` 只在 instructions 路径完全缺失时报错（client.go:1090-1097）；`instructions` 存在但为空数组/对象、或 `TimelineAddEntries` 的 entries 结构换了名字时，返回 `items=[]`、`next=""`、`err=nil`（client.go:1100-1136），`getUserTimeline` 把这一页当成"没有更多推文"正常结束。`len(tweets)==0` 时 manager 直接 `return stats, nil`（manager.go:1208-1211），任务显示完成。
- 影响：X 改字段或返回异常结构时，用户归档"成功"但零媒体、零失败记录；`IncrementalArchive` 开启时下一次从旧游标继续，但本次自认为已完成，缺失不会自愈。解析器单条失败（例如 `legacy` 位置变化）同样是静默 `continue`，没有任何告警。
- 建议：`timelineItems` 区分"识别到 TimelineTerminateTimeline / 合法空页"与"instructions 里完全没有已知 instruction 类型"，后者返回错误；对 `TweetFromGraphQLResultWithOptions` 的错误至少记日志/汇总计数（返回 `(tweets, skipped, err)` 或在 User 层日志）；对"0 推文 + 0 失败"增加诊断字段。

### [P2-3] 只有 Bottom cursor 的空页直接 break，翻页提前终止

- 位置：`internal/xclient/client.go:572-574`、`:1136`
- 证据：

```go
if len(items) == 0 {
    break            // 此时 next 可能非空，后面的 cursor 推进逻辑不会执行
}
...
return items, bottomCursor(entries), nil
```

- 影响：当某页 entries 仅含 cursor（X 端过滤掉整页内容、部分受限/风控裁剪、或 timeline module 结构变化）而 Bottom cursor 仍指向下一页时，直接停止翻页；`IncrementalArchive` 下还会把更旧的未归档推文永久跳过。
- 建议：`len(items)==0 && next!="" && page>0` 时继续翻页（仍受 `page<1000` 与 `seenCursor` 去重保护）；只有 `next==""` 或遇到终止 instruction 才结束。补一条"空页 + 非空 Bottom cursor"的测试。

### [P2-4] 长文 `note_tweet` 未解析，`Text` 只取 `legacy.full_text`

- 位置：`internal/parser/service.go:172-179`（GraphQL 路径的 `Text`）、`:333-340`（syndication 路径）；timelineFeatures 已开启 `longform_notetweets_*`（client.go:48）。
- 证据：

```go
return TweetData{
    ...
    Text: legacy.Get("full_text").String(),
    ...
}, nil
```

`note_tweet.note_tweet_results.result.text` 在整个包内没有被读取（grep 无匹配）。
- 影响：长推文的 `full_text` 是被截断并以 t.co 短链结尾的版本；`TweetData.Text` 会进入预览接口、`tweetFilename`（manager.go:1617-1635 用于文件名）与归档元数据，存档文本不完整。功能不崩溃，但属于可观的数据保真缺陷。
- 建议：优先 `note_tweet.note_tweet_results.result.text`，缺省再 `legacy.full_text`；syndication 侧确认其 `text` 是否已完整（实测 syndication 返回 `text`，但仍建议统一 helper）；加一条 note_tweet fixture 测试。

### [P2-5] 时间解析失败时静默用 `time.Now()`，下载文件的 mtime 被写成"下载当天"

- 位置：`internal/parser/service.go:166-171`、`:327-332`；消费方 `internal/jobs/manager.go:740`（`ModTime: modTime`）。
- 证据：

```go
createdAt := time.Now().UTC()
if raw := legacy.Get("created_at").String(); raw != "" {
    if parsed, err := time.Parse(time.RubyDate, raw); err == nil { createdAt = parsed }
}
```

- 影响：X 改 `created_at` 格式（RubyDate/RFC3339Nano 任一变化）时，所有推文时间被静默记成"当前时间"；下载侧把它用作文件 mtime，归档时间线失真且**无法通过重试修复**（媒体已落盘/去重跳过）。与"数据正确性"相关，但因为不崩溃且可人工发现，定 P2。
- 建议：解析失败时返回错误（或保留零值），至少记录并让上层对时间无效的媒体打标记；同时用 `legacy.created_at` + `note_tweet` 等多种格式尝试，补失败格式测试。

### [P2-6] syndication 只识别 TweetTombstone，其他 `__typename`（如 TweetUnavailable）会被当成"无媒体但成功"的推文

- 位置：`internal/parser/service.go:303-311`、`:314-341`
- 证据：

```go
result := gjson.ParseBytes(payload)
if !result.Get("id_str").Exists() && !result.Get("__typename").Exists() {
    return TweetData{}, errors.New("X 推文详情响应中没有推文数据")
}
if result.Get("__typename").String() == "TweetTombstone" {
    return TweetData{}, errors.New("推文不可用（已删除或受限）")
}
return tweetFromSyndication(rawURL, fallbackUsername, fallbackID, result, options)
```

`tweetFromSyndication` 在缺 id/text/media 时仍返回一个 `ID=fallbackID`、`Text=""`、`Media=nil` 的 `TweetData`（service.go:314-341）。`TweetUnavailable` 之类 typename 会通过上面的检查，返回"成功、无媒体"——单推任务随后在 manager 得到"链接已识别，但还没有解析到媒体 URL"（manager.go:421-428），**把"推文不可用"误报成"链接里没媒体"**，与 307-310 行注释的意图不一致。
- 影响：已删除/受限/不可用推文的用户提示错误，且 `httpapi` 预览接口会返回 200 + 空媒体而不是错误。
- 建议：对 `__typename` 做白名单（如 `Tweet` 才继续），把 `TweetTombstone`/`TweetUnavailable`/`TweetWithVisibilityResults` 等分别映射为明确错误；补一条 `TweetUnavailable` 测试。

### [P2-7] /home 登录自检存在误判：识别不到只记错误不禁用、HTTP 200 挑战页仍算 available；`enable()` 的"设置页重检可恢复"注释与 httpapi 实际行为不符

- 位置：`internal/xclient/client.go:401-439`、`:367-385`（CheckAll）、`:1000-1019`（`OK=false` 只看 disabled）；`:976-982`（enable 注释）；`internal/httpapi/server.go:255`（每次检测新建 Pool）；`internal/jobs/manager.go:1003-1011`（manager 缓存 Pool，key 只含 Cookie/代理）。
- 证据：

```go
// client.go:426-433
matches := screenNamePattern.FindSubmatch(payload)
if len(matches) < 2 {
    err := errors.New("无法从 X 首页识别登录账号")
    c.recordError(err)   // 只记录，不禁用、不影响 status.OK
    return "", err
}
```

```go
// httpapi/server.go:255
pool, err := xclient.NewPool(cfg)   // 一次性诊断池，不是 manager 缓存的那个池
...
diagnostics := pool.CheckAll(ctx)
```

- 影响：
  1. Cookie 已失效（/home 被重定向到登录页并 200）时，自检只记录 error，`ClientStatus.OK` 仍为 true、`Available` 仍计数（client.go:359-364、1014），设置页可能显示"可用"；真正的失效要等下一次归档请求 401 才被发现并禁用。
  2. `screenNamePattern` 对 4MB 首页做第一个 `"screen_name":"..."` 匹配（client.go:46、426），首页内嵌推荐/推文作者时可能取到别人的账号名（`ClientStatus.ScreenName` 仅诊断用，但会误导用户）；更严重的是若挑战页也含该字段会触发 `enable()`，把刚因 401 禁用的客户端重新启用。
  3. `enable()` 注释称"设置页重新检测即可恢复被误禁的 Cookie"，但 httpapi 的检测用的是临时 Pool（server.go:255），`enable()` 作用在临时 Client 上，manager 缓存池里的 `disabledUntil` 不会被清除（`xPoolConfigKey` 未变时 `cachedPool` 原样复用，manager.go:1003-1011）。用户即使检测成功，最长仍要再等 `clientDisableCooldown=30m`（client.go:62）。
- 建议：自检改用 `viewer` 类 GraphQL 查询（明确返回登录用户）而不是从 HTML 正则猜；识别不到账号/命中登录页时按"认证不可用"处理（可禁用或至少 OK=false）；设置页检测复用 manager 的 Pool，或在检测成功后让 manager 失效/刷新缓存池；补"登录页 200 不 enable""检测池与缓存池一致性"的测试。

### [P2-8] 同一 host 的重定向允许 https→http 降级与端口变化，ct0 仍有明文传输面

- 位置：`internal/xclient/client.go:233-241`、`:210-218`（sameHost 忽略端口/方案）
- 证据：

```go
CheckRedirect: func(req *http.Request, via []*http.Request) error {
    if len(via) >= 10 { return fmt.Errorf("重定向次数过多") }
    if len(via) > 0 && !sameHost(req.URL.Host, via[0].URL.Host) {
        return fmt.Errorf("拒绝跨域重定向: %s", req.URL.Host)
    }
    return nil
}
```

- 影响：只比较 host（且 `stripPort` 会忽略端口），`https://x.com/...` 被重定向到 `http://x.com/...` 或 `https://x.com:8443/...` 时仍会跟随，`X-Csrf-Token` 与 Cookie 会被发往明文/非预期端口。x.com 正常不会这样跳，但在被劫持/中间人场景下这是本可避免的凭据暴露面；与 230-232 行"防止 ct0 泄漏"的目标不完整。
- 建议：只允许 `https` 且 host+有效端口（默认 443）完全一致；拒绝 scheme 降级；测试真正的 `CheckRedirect` 行为（现有 hardening_test.go:41-47 只测了 sameHost 函数）。

### [P2-9] GraphQL queryId / 响应路径全部硬编码，无演进检测；404 被当普通永久错误

- 位置：`internal/xclient/client.go:51`、`:469`、`:485`、`:496`、`:676`、`:713`；错误分类 `:1022-1032`、`:792-794`
- 证据：

```go
userMediaPath = "/i/api/graphql/MOLbHrtk8Ovu7DUNOLcXiA/UserMedia"   // client.go:51
payload, err := c.graphQL(ctx, "/i/api/graphql/xmU6X_CKVnQ5lSrCbAmJsg/UserByScreenName", values) // :485
payload, err := c.graphQL(ctx, "/i/api/graphql/3dQPyRyAj6Lslp4e0ClXzg/ListMembers", values)      // :676
```

- 影响：X 轮换 queryId/路径或改响应层级（如 `timeline_v2` → `timeline`）时，所有相关请求 404/解析失败；404 不属于可重试错误，`Pool.graphQL` 直接返回（client.go:792-794），批量任务表现为"每个用户解析失败"而不会给出"X 接口已变更"的明确告警。没有任何启动自检/版本探测。
- 建议：queryId 改为配置项或集中常量表，并在失败时打日志/暴露 Prometheus 风格计数；识别 GraphQL 404 并给出"可能 queryId 失效"的专用错误；增加 `timeline_v2`/`timeline` 双路径回退（或用 feature 开关控制）。

### [P2-10] 403 一律禁用客户端 30 分钟，WAF 挑战与凭证失效不可区分

- 位置：`internal/xclient/client.go:819-821`、`:1022-1032`、`:967-974`、`:61-62`
- 证据：

```go
if isPermanentClientError(err) {
    c.disable(err)
    return nil, err
}
...
if errors.As(err, &httpErr) {
    return httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden
}
```

- 影响：Cloudflare/WAF 的 403 挑战会让一个有效 Cookie 被禁用 30 分钟（注释知道 403 可能来自 WAF，但仍选择禁用）。只有主 Cookie 的部署在这一窗口里列表/关注/受保护用户时间线全部不可用（与 P1-1 叠加）。429/5xx 已正确按瞬时不禁用，403 的区分缺失是残留。
- 建议：结合响应体特征（如 `cf-challenge`、HTML 挑战页、`x-rate-limit-*` 头）区分 WAF 与 401/403 凭证失败；WAF 走退避/代理切换而不是禁用 Cookie；或把"403 禁用"改成短冷却（如 1–2 分钟）并指数递增。

### [P2-11] `ExtractTweetURL` 拒绝 X 官方分享格式 `.../i/web/status/<id>`

- 位置：`internal/parser/service.go:24`、`:109-123`
- 证据：

```go
tweetURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.|mobile\.)?(?:x|twitter)\.com/([^/?#]+)/status/([0-9]+)`)
```

只允许 `<user>/status/<id>` 两段式；X Web/App 分享出的 `https://x.com/i/web/status/<id>`（多一段 `web`）会被判为"请输入 x.com 或 twitter.com 的推文链接"。`https://x.com/i/status/<id>` 恰好被当成 username=`i` 接受，行为不一致。
- 影响：用户粘贴官方分享链接直接报错；单推预览（httpapi/server.go:307）与单推任务（manager.go:412）的入口都被拒。
- 建议：正则增加 `(?:i/(?:web/)?status|([^/?#]+)/status)` 形式；或在解析后按路径段处理：`.../status/<id>` 前缀即接受 `i/web` 变体；补 `i/web/status` 用例（现有测试只覆盖 `mobile`/`www`，service_test.go:449-463）。

### [P2-12] 测试没有覆盖认证头、查询构造、重定向、游标防循环与真实 payload，关键回归会全部漏检

- 位置：`internal/xclient/client_test.go:47-51`、`:243-255`、`internal/xclient/hardening_test.go:41-47`、`internal/parser/service_test.go:35-74`
- 证据：

```go
client := &Client{
    http:         server.Client(),
    baseURL:      server.URL,
    retryBackoff: func(int) time.Duration { return 0 },
}
```

测试中的 Client 全部是手工构造、没有 `authToken/csrfToken/limiter`，也从未断言 `Cookie: auth_token/ct0`、`X-Csrf-Token`、`Authorization` 头；`fakeTimelineRequester`（client_test.go:243-255）直接吞掉 path/values，因此 UserMedia/UserByScreenName/ListMembers/Following 的 queryId、variables、features 写错时测试仍全绿。hardening_test.go 只测 `sameHost` 纯函数，没有用 httptest 验证真实重定向拒绝；也没有"重复 cursor 终止""refund""limiter 并发/预扣"用例。parser 侧 fixtures 全部是手工最小 JSON（结构恰好等于实现路径），没有一条真实 API 抓包 golden；`syndicationToken` 只断言非空（service_test.go:40-42），没有已知期望值。
- 影响：认证/查询/分页这类最易随 X 服务端变化的契约没有回归保护，测试更像"实现自证"。
- 建议：
  - 增加一个记录式 `RoundTripper`，断言 URL path、`variables/features/fieldToggles` 的 JSON（至少 screenName/id/cursor 三个变量）、Cookie 与 CSRF 头；
  - 用 httptest 断言跨域重定向被拒、https→http 被拒；
  - 补 `seenCursor` 重复 cursor 终止、limiter 预扣/refund、Pool 全限流分类用例；
  - parser 增加真实 payload 最小化 golden（含 video variant、note_tweet、TweetUnavailable、card、tombstone），并给 syndicationToken 一个已知 id→token 断言。

## 4. P3 与优化建议

- [P3-1] 退避无抖动：`requestRetryDelay`（client.go:915-921）固定 750ms/1.5s/3s/6s，多任务/多实例同时重试会形成同步脉冲；建议 `base*2^n + rand` 并有测试断言范围。
- [P3-2] 用 `fmt.Sprintf("%q")` 拼 JSON 变量（client.go:467、482、561、674、711）：`%q` 是 Go 字面量转义，不等于 JSON 转义（如 `\x` 形式会产生非法 JSON）；screen_name/id 目前是安全字符集才没出事。建议统一 `encoding/json` 或 `json.Marshal` 构造 variables。
- [P3-3] `firstBool` 把显式 `null` 当"存在且 false"（client.go:1080-1087），会阻断从新字段回退到 `legacy` 旧字段（例：`privacy.protected:null` + `legacy.protected:true` 会被判成非保护用户；`relationship_perspectives.following:null` + `legacy.following:true` 会让受保护用户被跳过）。建议只认 `True/False` 类型。
- [P3-4] 三层分页都未按 tweet/user ID 去重（client.go:540-615、668-703、705-740）：X 在翻页边界重复返回置顶/实时插入项时会产生重复条目，虽然下游有媒体锁兜底，但会多打请求、多计 stats；建议加 `seenItemIDs`。
- [P3-5] `before()` 预扣后，`url.Parse`/`NewRequestWithContext` 失败路径不 refund（client.go:845-866），只有 `http.Do` 失败才 refund（876-882）；极端情况下本地配额被永久少记，累积后提前阻塞。
- [P3-6] 响应体 `io.LimitReader(..., 32<<20)` 截断时静默（client.go:888）：超限响应会得到"半截 JSON → gjson 解析路径缺失 → 解析错误"，没有任何"响应过大"信号；建议超限直接报错。
- [P3-7] 死代码/未使用导出：`Pool.Next`（client.go:258-267）、`GetUserMedia`（513-515）、`GetUserByID`（461-463）、`GetUserByScreenName`（476-478）、`firstNonEmpty`（1150-1157）；parser 的 `ParseTweetJSON`/`ParseTweetJSONWithOptions`（service.go:82-99）与 `TweetFromGraphQLResult` 包装（101-107）仅测试使用；生产链路单推走 syndication，`ParseTweetJSON` 的 `instructions.0` 固定下标也很脆弱。建议删除或标注为测试 helper。
- [P3-8] `qualityFromURL`（service.go:827-839）对真实 URL 基本恒为空：路径段 `1280x720` 不含 `.`，`.mp4` 段不含 `x`，两个条件同时满足的段几乎不存在；`MediaVariant.Quality` 因此一直是空串。建议从 `video.twimg.com/.../vid/1280x720/...` 解析 `WxH`，或者删掉该字段以免误导。
- [P3-9] URL 匹配两个小坑：`ExtractTweetURL` 的 `status/([0-9]+)` 对 `status/123abc` 会静默截断为 `123`（service.go:24、118-122），可能下载到错误推文；`mediaURLPattern` 是小写敏感（service.go:28），大写主机的 twimg URL 在卡片兜底扫描里会被漏掉。
- [P3-10] `Media.ID` 语义三种混用：`mediaFromDetail` 用 `id_str`（service.go:407）、`mediaFromURL` 用完整 URL（service.go:644）、syndication photos 用 `photo-N`（service.go:372-378）；下游 jobs 目前未使用该字段（grep 仅 parser 内部），但若未来拿它作去重/存储键会出问题。建议统一为媒体 snowflake 或明确文档化"仅展示用"。
- [P3-11] `collectMediaURLs` 对每个 JSON 节点既用 `result.Raw` 正则扫描整棵子树又递归子节点（service.go:534-558），最坏 O(n²)；`parseUnifiedCardMedia` 对 `media_entities` 先 `.Array()` 再 `.ForEach` 双遍历（service.go:479-485，数组情况下重复解析，靠 seen 兜底）。数据来自 X 可信源，风险低，但可以顺手收敛。
- [P3-12] `syndicationToken`（service.go:789-825）与 X 前端算法不一致且没有 golden 测试：Node 对 `1349129669258448897` 计算 `toString(36)` 结果是 `39qeyy97t9x`，Go 实现（20 位小数展开）得到 `39qeyy97t9wsjr4724t2o6r`；目前实测 `cdn.syndication.twimg.com/tweet-result` 只要求 token 非空（空 token 返回 2 字节空 body，任意非空 token 均 200），所以暂不致命。但一旦 X 开始校验，单推解析会整体失效且没有任何测试能发现。建议改用与前端一致的"最短往返 base36 表示"并加已知 id→token 断言，或在注释中写明"服务端当前不校验"。
- [P3-13] `timelineItems` 的错误信息硬编码 `data.user.result.__typename`（client.go:1092-1096），ListMembers 路径（`data.list...`）出错时提示"用户不可用"，误导排障；建议按 instructionsPath 前缀拼接或由调用方提供上下文。
- [P3-14] 包级可变状态：`clientDisableCooldown`（client.go:62）、`maxPoolSelectWait`/`maxPoolSelectWaitStep`（271-274）被测试直接改写（client_test.go:212-220、415-429、461-464），生产语义上它们是常量；建议改为 struct 字段或 `-ldflags`，避免测试污染/未来并行测试的竞态。另 `Pool.Select` 的 `blocked==0` 分支（305-307）在现有循环里不可达，可删。
- [P3-15] 受保护用户分支只用 `p.Primary()`（client.go:517-526、532-534）：主 Cookie 被禁用时无法尝试其他"也关注了该用户"的 Cookie；虽然语义上多数部署只有主 Cookie 关注，但至少应在 `ErrClientDisabled` 时尝试 `p.Next()` 的可用客户端。
- [P3-16] `apiError` 在 errors 存在但没有 `code` 时给 `Code=-1`（client.go:939-943），`isTransientError` 的兜底会把这种非瞬时业务错误当瞬时可重试（client.go:1048），导致 5 次无意义重试；建议显式返回"未知 API 错误"并计入永久分类，或按 message/kind 判定。

## 5. 待确认 / 需 Lead 交叉验证

1. **GraphQL 端点与响应路径无法离线验证**：`UserMedia`(MOLbHrtk8Ovu7DUNOLcXiA)、`UserByScreenName`(xmU6X_CKVnQ5lSrCbAmJsg)、`ListByRestId`(ZMQOSpxDo0cP5Cdt8MgEVA)、`ListMembers`(3dQPyRyAj6Lslp4e0ClXzg)、`Following`(7FEKOPNAvxWASt6v9gfCXw) 以及 `timeline_v2`/`timeline` 的层级都只能靠真实 Cookie 冒烟确认。建议 Lead 让有凭据的维护者跑一次只读冒烟（每个方法拉 1 页），或加启动自检开关。尤其 `GetFollowing` 用的 `data.user.result.timeline.timeline.instructions` 与 `GetUserTimeline` 的 `timeline_v2` 不一致，需要真实响应确认。
2. **请求头是否足够**：代码只发 `Authorization: Bearer <公开 guest token>` + `Cookie: auth_token/ct0` + `X-Csrf-Token` + UA/Accept（client.go:866-873），没有 `x-client-transaction-id`、`x-twitter-auth-type`、`x-twitter-active-user`、`x-twitter-client-language`。目前无法离线判断 404/401 是否与缺失 `x-client-transaction-id` 有关（X 不同时期对此要求不同）；建议冒烟时逐一抓响应验证，若需要则补头并测试。
3. **分页 count 上限**：`ListMembers`/`Following` 使用 `count:200`（client.go:674、711），需确认 X 是否接受该上限（部分 GraphQL timeline 接口上限 100，超限可能 400）。若不确定，降到 100 并多翻页更稳。
4. **`enable()` 恢复契约**（跨模块）：xclient 注释（client.go:976-982）假设"设置页重新检测可恢复被误禁 Cookie"，但 `internal/httpapi/server.go:255` 每次 `xclient.NewPool` 建临时池，manager 的 `cachedPool`（`internal/jobs/manager.go:1003-1011`）不会刷新。需与 httpapi/jobs reviewer 对一下：检测成功后是否应让 manager 重建/复用池，否则该注释应删除。
5. **错误信息中的上游 payload 与前端渲染**：`HTTPError.Payload`/`APIError.Raw` 原样截取远端响应体（client.go:892-894、928-955），会经 `m.fail`/`CreateFailedMedia` 进入 DB → API → Web UI；CDN 403/429 返回 HTML/JS 时，若前端用 HTML 渲染会有 XSS 面。请前端/接口 reviewer 确认一律转义（本包侧只能保证截断与 UTF-8 安全）。
6. **`Media.ID` 语义**（与 storage/downloader 契约）：parser 三种写法混用（见 P3-10），下游未使用；请 storage/downloader reviewer 确认没有把它当去重键，否则需统一。
7. **syndication token 失配**（见 P3-12）：已外网无凭据验证当前服务端不校验 token；请确认是否保留这套与前端不一致的实现，或改为 golden 可测的确定性算法。
8. **多实例/持久化**：`rateLimiter` 是进程内内存（ratelimit.go:18-25），重启即清空、多副本不共享；`Pool.Select` 全限流时可等待 15 分钟（client.go:271-274）。若产品支持多实例部署，需确认限流与"卡 15 分钟"的可接受性。
9. **`ParseTweetJSONWithOptions` 的 TweetDetail 固定下标**（parser/service.go:91）：`instructions.0.entries.0.content.itemContent.tweet_results.result` 只适合"首条 instruction 首条 entry 就是焦点推文"的布局；当前生产链路不用它，但函数导出了，确认是否保留 / 是否需要改为遍历 entries。
10. **外溢观察（非本包，供 Lead 分派）**：`internal/jobs/manager.go:1687-1693` 的 `isPhotoURL` 运算符优先级是 `A || (B && C)`，pbs.twimg.com 命中时不再检查 `.mp4`；单媒体 URL 任务用它决定是否走图片大图改写（manager.go:693）。请 jobs reviewer 确认是否有意为之。
11. **`StopAtTweetID` 的数值早停依赖 timeline 严格倒序**（client.go:618-635）：`TimelineAddToModule` 混入的 conversation/module 项可能打乱顺序，依赖真实数据验证；若发现乱序需改成"整页扫描 + 数值比较取最大值"策略。

## 6. 设计上值得肯定之处

- **SSRF 纵深防御做得扎实**：parser 对 `media_url_https`、video variants、syndication photos、card 提取的每个入口都要求 twimg.com（service.go:626-677、403-405、613-615、366-368），`BestVariant` 二次过滤非 twimg 变体（125-151），`mediaURLPattern` 要求主机紧跟 `://` 防止 query 伪造（24-29 注释），并且在 service_test.go:235-318 有攻击者主机用例；下载器侧还有自己的 host 校验与重定向校验，属于少见的"解析侧就把不可信 URL 清掉"的正向设计。
- **重定向凭据泄漏防护**：自定义头 `X-Csrf-Token` 不会被标准库跨域剥离，代码用 `CheckRedirect` + `sameHost` 主动拒绝跨域跳转（client.go:230-241、209-218），并有 sameHost 单测；P2-8 只是把方案补完整，方向正确。
- **限流设计比常见实现细**：以响应头为权威增量预扣（ratelimit.go:27-55、57-97）、保留 2% 余量避免撞 429、reset+5s 裕量、`Retry-After`/reset 上限夹逼（116-141）、失败 refund（99-114）、`blockedReset` 供 Pool 有界等待、`snapshot` 暴露诊断；`Pool.graphQL` 还区分"交还换 Cookie"与"失败"（client.go:766-807），比固定 sleep 重试成熟。
- **HTTP 资源管理规范**：所有响应 `defer body.Close()`，读取一律 `io.LimitReader`（4MB/32MB），错误 payload 截断 8KB 且做了 UTF-8 边界回退（client.go:946-955）；httpx 复用带连接池和拨号防护的 Transport，超时分级合理（90s 总超时、30s 响应头超时）。
- **增量归档早停逻辑考虑周全**：区分"首页置顶精确命中"与"后续页数值落后"，命中后扫完整页只保留更新推文，并专门处理 stopID 推文已删除、置顶命中、置顶较旧等场景（client.go:544-616、618-647）；client_test.go:283-373 的四条用例覆盖了这些边界，质量高于平均。
- **媒体选择与去重语义清晰**："视频只用 mp4、绝不用 HLS/缩略图冒充视频"（service.go:125-151、model.go:55-68，测试 service_test.go:346-398）；同推文内跨 retweet/quote/card 的媒体用 `downloader.MediaIdentity` 归一化去重（service.go:741-765），把"下载侧按最大尺寸取回"的职责留给 downloader，避免两侧各改一次尺寸导致字节不一致；图片尺寸升级与去重语义在 downloader 侧有配套测试。
- **ID/时间精度处理正确**：推文 ID 全程字符串，只在早停比较时转 int64（雪花 ID 范围内安全），没有走 float；created_at 用 RubyDate/RFC3339 解析（P2-5 只是失败兜底问题）。
- **代码注释解释"为什么"而非"是什么"**：如 403 冷却期、reset 夹逼、HLS 不回退、置顶早停、partial errors 等，读代码即可理解取舍，便于后续维护。
