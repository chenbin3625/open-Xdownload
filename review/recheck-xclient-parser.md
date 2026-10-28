# 修复复核 · internal/xclient + internal/parser

复核对象：当前工作区（未提交改动）`internal/xclient`、`internal/parser`，对照 `review/REPORT.md` §3.3、P1-9、我上一轮 `review/xclient-parser.md` 的全部条目，以及 `review/FIXES.md` 中的修复声明（只读）。
复核方式：以当前代码为准（`git diff -- internal/xclient internal/parser` 仅作定位辅助，结论全部按当前文件行号与代码证据给出）；未修改任何源码/测试/配置，只写本文件。

## 0. 复核方法与验证结果

当前规模（相对上一轮）：`client.go` 1166→1241、`ratelimit.go` 203→253、`client_test.go` 602→689、`hardening_test.go` 48→56、`parser/service.go` 846→868、`parser/service_test.go` 492→514；`model.go` 未变。
`git diff --stat`：6 个文件 +330/−66（xclient/parser），另有 jobs/httpapi 等跨模块改动（P1-9 修复需要，仅只读核对）。

验证命令与结果：
- `go vet ./internal/xclient/ ./internal/parser/`：exit 0，无告警。
- `go test -count=1 -race ./internal/xclient/ ./internal/parser/`：两个包均 ok。
- 附加交叉验证（确认跨模块改动未破坏调用方）：`go test -count=1 ./internal/jobs/ ./internal/httpapi/ ./internal/httpx/ ./internal/downloader/` 全部 ok；`go build ./...` 通过。

状态统计（§3.3 的 12 条 + "其他" 拆成 9 条 + P1-9 + 我上轮报告不在 §3.3 的 7 条，共 29 条）：
✅ 已修复 9 · 🟡 部分修复 6 · ❌ 未修复 13 · ⚪ 复核后撤回 1。

## 1. 总体结论

- 上一轮最严重的 P1-9（列表/关注只走 Primary Cookie）**主体已修复**：`Pool` 新增了 `GetListByID/GetListMembers/GetFollowing`，jobs 三处调用已改为 Pool 版本，分页每页都经过 `p.Select + p.graphQL`（429 可交还、401/403 可换 Cookie）。残留：`FollowUser` 仍是 Client-only、jobs 仍 `pool.Primary()`；且新增了"跨 Cookie 视角不一致"的新风险（见 R3）。
- §3.3 的多数"静默数据问题"已修：apiError 判定收紧、空页+cursor、note_tweet（GraphQL 路径）、created_at、TweetUnavailable、https 降级/端口、i/web 链接、firstBool null、32MB 截断、before() 预扣失败、refund 时机。
- 仍未修复：queryId 硬编码/404 分类、403 一律禁用 30 分钟、退避无抖动、`%q` 拼 JSON、分页按 ID 去重、syndicationToken 失配、qualityFromURL、Media.ID 语义，以及几条仅测试/可维护性项。
- **新引入的回归风险**已单列在 §3，其中 R2（单条推文解析失败会终止整个用户的时间线且不推进增量游标）与 R4（88 变 transient 后 Client-only 路径可长时间阻塞重试）建议优先处理。

## 2. 逐条核验

### 2.1 [P1-9] List / ListMembers / Following / Follow 只走 Primary Cookie

**状态：🟡 部分修复**

- 已修复部分：
  - Pool 包装新增：`internal/xclient/client.go:507-509`（`Pool.GetListByID`）、`:708-710`（`Pool.GetListMembers`）、`:758-760`（`Pool.GetFollowing`）；三者与 Client 方法共用同一实现（`:511`/`:712`/`:762` 起），不存在两份逻辑漂移。
  - 内部走 `requester.graphQL`（`:515`、`:721`、`:771`）→ Pool 版本进入 `Pool.graphQL`（`:828-869`），带 `withYieldOnRateLimit`，429/API-88 可换 Cookie。
  - jobs 已改用：`internal/jobs/manager.go:618`（`pool.GetListByID`）、`:639`（`pool.GetListMembers`）、`:686`（`pool.GetFollowing`）；原 `client := pool.Primary()` + `client.Get*` 写法已删除。
- 残留：
  - `FollowUser` 仍只有 Client 方法（`client.go:804-809`），jobs 仍 `_ = pool.Primary().FollowUser(ctx, user)`（`internal/jobs/manager.go:1226-1227`）。`review/FIXES.md:18` 明确说明"自动关注仍使用主账号，以保持操作身份"，因此这更像有意保留的设计决策而非遗漏；但 REPORT 标题含 Follow，需 Lead 确认接受该裁定（主 Cookie 被禁用时自动关注仍然静默失败，错误本就被丢弃，影响有限）。
  - 受保护用户时间线仍固定 Primary（`client.go:536-545`），Primary 被限流/禁用时不会尝试其他 Cookie（与 P1-9 同构，属我上轮 P3-15，未修）。
  - 新增三个 Pool 方法**没有任何单元测试**（`grep GetListByID/GetListMembers/GetFollowing` 在 `*_test.go` 中无匹配）；P1-9 的故障转移行为无回归保护。

### 2.2 REPORT §3.3 条目

| # | 条目 | 状态 | 当前证据 |
|---|---|---|---|
| 1 | apiError "data 非空即成功" | ✅ 已修复 | `client.go:998-1019`；测试 `client_test.go:633-663` |
| 2 | timeline 异常形状/逐条解析失败静默 | 🟡 部分 | 逐条失败已报错 `client.go:620-626`；instruction 形态未知仍静默 `client.go:1164-1212` |
| 3 | 只有 Bottom cursor 的空页直接 break | ✅ 已修复 | `client.go:591-593`（ListMembers/Following 同 `:729`/`:779`）；测试 `client_test.go:322-331` |
| 4 | 长文 note_tweet 未解析 | 🟡 部分 | GraphQL 路径已修 `parser/service.go:175-178`；syndication 路径仍只取 `text`（`:341`） |
| 5 | created_at 失败静默用 now | ✅ 已修复 | `parser/service.go:171-174`/`334-337`/`348-363`；测试 `service_test.go:31-43` |
| 6 | 只识别 TweetTombstone | ✅ 已修复 | `parser/service.go:33`、`:159-161`、`:315-317`；xclient 跳过 `client.go:621-623` |
| 7 | /home 自检误判 + enable() 被临时池绕过 | 🟡 部分 | 临时池问题已修（`httpapi/server.go:284-289`、`jobs/manager.go:1030-1048`）；`/home` 判定本身未改（`client.go:412-451`、`:1089`、`:371`） |
| 8 | 同 host 重定向允许 https→http 降级/端口变化 | ✅ 已修复 | `client.go:208-226`（sameOrigin）+ `:241-252`（显式降级拒绝）；`hardening_test.go:42-56` |
| 9 | GraphQL queryId/响应路径硬编码、404 无分类 | ❌ 未修复 | `client.go:50`、`:480`、`:496`、`:515`、`:721`、`:771` 全部硬编码；错误分类 `:1097-1107` 无 404 分支（grep 无 `NotFound`） |
| 10 | 403 一律禁用 30 分钟 | ❌ 未修复 | `client.go:881-884`、`:1097-1107`（401/403 → permanent）、`:1042-1049`（disable）、`:60-61`（30m） |
| 11 | ExtractTweetURL 拒绝 i/web/status | ✅ 已修复 | `parser/service.go:24`（`([^/?#]+\|i/web)` + 结尾锚）；`:111-129`；测试 `service_test.go:24-29` |
| 12 | 测试不校验认证头/query/重定向/cursor 循环 | 🟡 部分 | 新增 9 个测试（见 §4），但 Cookie/CSRF/Authorization、GraphQL variables/features、真实重定向、cursor 不前进、Pool 新列表方法均无断言 |
| 13a | 退避无抖动 | ❌ 未修复 | `client.go:985-991` 仍固定 750ms/1.5s/3s/6s，测试 `client_test.go:142-149` 反向锁定该行为 |
| 13b | `fmt.Sprintf("%q")` 拼 JSON | ❌ 未修复 | `client.go:478`、`:493`、`:513`、`:580`、`:719`、`:769` |
| 13c | firstBool 把 null 当 false | ✅ 已修复 | `client.go:1154-1162`（仅认 `gjson.True/False`）；测试 `client_test.go:35-42` |
| 13d | 三层分页未按 ID 去重 | ❌ 未修复 | 仅 cursor 去重（`client.go:638-649`/`:738-749`/`:788-799`），无 seenTweetIDs/seenUserIDs |
| 13e | before() 预扣失败不 refund | ✅ 已修复 | `limiter.before` 移到请求构造之后（`client.go:907-922` → `:931-935`），构造失败不再预扣；Do 失败仍 refund（`:938-944`） |
| 13f | 32MB 截断静默 | ✅ 已修复 | `client.go:950-957` 返回 `httpx.ErrBodyTooLarge`（`httpx/fetch.go:11`）；但引入 R6 |
| 13g | syndicationToken 与前端算法不一致 | ❌ 未修复 | `parser/service.go:811-828` 未改；测试仍只断言 token 非空（`service_test.go:40-42`） |
| 13h | qualityFromURL 恒为空 | ❌ 未修复 | `parser/service.go:849-863` 未改 |
| 13i | Media.ID 三种语义混用 | ❌ 未修复 | `parser/service.go:395`（photo-N）、`:429`（id_str）、`:666`（完整 URL） |

#### 逐条细节（关键几条）

- **#2 timeline 异常形状/逐条解析失败**：逐条解析失败已从"静默 continue"改为报错——`client.go:620-626`（除 `ErrTweetUnavailable` 跳过）。但 `timelineItems`（`client.go:1164-1212`）仍只在 instructions 路径缺失时报错（`:1166-1172`），当 instructions 存在但类型/层级变化（真正的"X 改了结构"）时仍返回 `items=[]/next=""`，上层 `client.go:591-593` 直接 break，任务仍显示成功无数据。另外修复方向变成了"整页/整用户失败"（见 R2）。
- **#4 note_tweet**：GraphQL 时间线路径已改：`parser/service.go:175-178` 优先 `note_tweet.note_tweet_results.result.text`，回退 `legacy.full_text`；测试 `service_test.go:31-39`。单推 `tweetFromSyndication` 仍 `result.Get("text")`（`:341`），本次未动；`review/FIXES.md:41` 笼统声明"解析长文"，实现只覆盖了 GraphQL 一条路径，**声明与实现范围不一致**。我用真实 `cdn.syndication.twimg.com/tweet-result` 探测的长贴响应里**没有** `note_tweet` 字段，无法离线判定该端点是否对长文截断，列入待确认（见 §5）。
- **#5 created_at**：`tweetCreatedAt`（`parser/service.go:348-363`）对非法 `created_at` 返回错误；缺失时用雪花 ID 反推时间。修复正确，但错误会让整条时间线失败（R2）。
- **#6 TweetUnavailable**：两处入口都识别 `TweetTombstone/TweetUnavailable` 并返回哨兵 `ErrTweetUnavailable`（`parser/service.go:159-161`、`:315-317`）；xclient 时间线遇到该哨兵跳过（`client.go:621-623`），单推则作为错误上抛。未新增 `TweetUnavailable` 专门测试（现有 `service_test.go:487-500` 只测 Tombstone）。
- **#8 重定向**：`sameOrigin`（`client.go:208-226`）比较 scheme+hostname+有效端口；`CheckRedirect`（`:241-252`）额外显式拒绝 `https→http`，并保留 10 跳上限。测试 `hardening_test.go:42-56` 只测 `sameOrigin` 纯函数，没有用 httptest 验证客户端真实拒绝跨源/降级跳转（仍属 #12 测试缺口）。
- **#9/#10**：queryId 仍是常量；`isPermanentClientError` 仍把 HTTP 401/403 判为永久并 disable 30 分钟（`client.go:1097-1107`、`:1042-1049`），WAF 403 与凭证失效仍不可区分。两项均无"X 接口变更告警/404 特殊分类"。`review/FIXES.md:73` 已把它们列为**明确延期/风险接受**（"未使用真实 Cookie 做线上验证，不能依据推测自动更新 queryId、把所有 403 当挑战"），因此不是实现疏漏，但缺陷状态仍为未修复。
- **#11 i/web**：新正则 `.../([^/?#]+|i/web)/status/([0-9]+)(?:[/?#]|$)`（`parser/service.go:24`）同时修复了我上轮 P3-9 的"`status/123abc` 被静默截断为 123"；但 `mediaURLPattern`（`:28`）仍大小写敏感，大写主机的 twimg URL 在卡片兜底扫描里仍会漏。
- **#12 测试**：新增 `TestFirstBool...`（`client_test.go:35-42`）、`TestPoolRateLimitErrorYieldsToAnotherCookie`（`:104-140`）、`TestUserTimelineContinuesAfterEmptyCursorPage`（`:322-331`）、`TestUserTimelineDoesNotSkipMalformedTweet`（`:333-345`）、`TestGetUserTimelinePinnedStopContinuesThroughNewerPages`（`:402-417`）、apiError 新增 5 个用例（`:643-663`）、`TestSameOriginRequiresMatchingPortAndScheme`（`hardening_test.go:42-56`）、`TestExtractOfficialWebStatusURL`（`service_test.go:24-29`）、`TestTweetLongTextAndInvalidTimestamp`（`service_test.go:31-43`）。仍缺：请求头（Cookie/`X-Csrf-Token`/`Authorization`）与 GraphQL `variables/features/fieldToggles` 断言、真实 CheckRedirect 集成测试、cursor 不前进/重复 cursor 测试、Pool `GetListByID/GetListMembers/GetFollowing` 故障转移测试、`ErrBodyTooLarge`/`markRateLimited`/`prune`/`before` 等待预算测试；且 `hardening_test.go` 删掉了原来的"不同 host 必拒"纯函数断言，新测试只覆盖端口/降级。

### 2.3 我上轮报告其余 P3 条目（不在 §3.3 的）

| 条目 | 状态 | 当前证据 |
|---|---|---|
| P3-7 死代码/未使用导出 | ❌ 未修复 | `Pool.Next`（`client.go:269`）、`firstNonEmpty`（`:1225`）、`ParseTweetJSON*`（`parser/service.go:84-99`）仍在 |
| P3-9 URL 匹配 | 🟡 部分 | 短 ID 截断已修（`service.go:24` 尾锚）；`mediaURLPattern` 大小写敏感未修（`:28`） |
| P3-11 collectMediaURLs O(n²) / Array+ForEach 双遍历 | ❌ 未修复 | `parser/service.go:556` 起、`:498-523` |
| P3-13 timelineItems 错误信息硬编码 data.user 路径 | ❌ 未修复 | `client.go:1167` 仍固定 `data.user.result.__typename`（ListMembers/Following 出错时提示"用户不可用"） |
| P3-14 包级可变变量/不可达分支 | ❌ 未修复 | `client.go:60-61`、`:282-285` 仍被测试改写；`Select` 的 `blocked==0` 分支（`:316-318`）仍不可达 |
| P3-15 受保护用户时间线只用 Primary | ❌ 未修复 | `client.go:536-545`；`userMediaNeedsPrimaryClient`（`:551-553`）未变 |
| P3-16 未知 API code 被当瞬时重试 | ⚪ 复核后撤回 | 复核确认原判断有误：`isTransientError` 的 APIError 分支直接 `return`（`client.go:1115-1118`），code -1 不会落到兜底行（`:1123`），原本就不会被重试。不作为缺陷，保留说明 |

## 3. 新引入的回归/风险

### [R1] apiError 收紧过度：partial error 缺少 `kind:"NonFatal"` 时整页失败

- 位置：`internal/xclient/client.go:1003-1017`
- 证据：

```go
data := gjson.GetBytes(payload, "data")
hasData := data.IsObject() && len(data.Map()) > 0
for _, entry := range errorsResult.Array() {
    ...
    if hasData && entry.Get("kind").String() == "NonFatal" && !isPermanentClientError(err) && !isTransientError(err) {
        continue
    }
    return err
}
```

- 影响：修复"data 非空即成功"时，把容忍条件绑死为 `kind=="NonFatal"`。X 的 GraphQL errors 并不保证带 `kind`（常见形态只有 `message/locations/path/extensions`）。此前只要 data 非空就成功；现在这类"部分成功 + 无 kind 错误"会让整页请求失败，且 APIError code -1 非瞬时 → Pool 直接返回、不换 Cookie。测试只覆盖了带 `kind` 的 partial case（`client_test.go:643-663`）。需真实响应验证，按最坏情况是新 P1 级功能回归。
- 建议：`hasData` 时对无 `kind` 或 `code==-1` 的条目也按非致命处理（至少记日志），或按"data 目标路径可用"判定；补一条"data 非空 + kind 缺失"用例。

### [R2] 单条推文解析失败现在会终止整个用户的时间线，且增量游标不推进（毒丸）

- 位置：`internal/xclient/client.go:620-626`；消费方 `internal/jobs/manager.go:1243-1253`
- 证据：

```go
tweet, err := parser.TweetFromGraphQLResultWithOptions("", user.ScreenName, "", result, options)
if errors.Is(err, parser.ErrTweetUnavailable) { continue }
if err != nil {
    return nil, fmt.Errorf("解析推文 %s 失败: %w", id, err)   // 整页/整用户失败
}
```

- 影响：以前逐条解析失败是 `continue`（静默跳过）；现在除 `ErrTweetUnavailable` 外任何解析错误（缺 legacy、`created_at` 非法、未来未知 `__typename` 等）都会让该用户本轮 `GetUserMedia` 直接失败。jobs 侧只记录 `stats.Failed++` 与 Issue 后返回（`manager.go:1247-1253`），不进入 `archiveTweets` → **增量游标不写入**；若该异常推文持续存在，此用户每轮都在同一位置失败、永远无法归档（`IncrementalArchive` 下相当于永久毒丸）。新测试 `TestUserTimelineDoesNotSkipMalformedTweet`（`client_test.go:333-345`）固化了该行为。
- 建议：区分"不可用"（跳过）与"解析失败"（计数跳过 + 日志，超过阈值如连续 3 条才让整页失败），或返回已收集结果 + 警告；至少不要让单条坏数据阻断该用户全部后续归档。

### [R3] Pool 化列表/关注后，分页每页可能换 Cookie：受保护账号/私有列表的"视角"可能不一致

- 位置：`internal/xclient/client.go:712-802`（ListMembers/Following 内每页 `requester.graphQL`）；`client.go:828-869`（Pool.graphQL 每次 `p.Select`）；jobs `internal/jobs/manager.go:618/639/686`
- 证据：`getListMembers/getFollowing` 在分页循环内调用 `requester.graphQL`，Pool 版本每次都会重新 `Select`（`client.go:834`），可能用不同 Cookie 拉下一页。
- 影响：Following 列表、私有列表成员这类结果**依赖观看者身份**；主 Cookie 被限流/禁用后换到备用 Cookie，若备用账号没有关注该受保护账号或不在私有列表内，后续页会返回空/`UserUnavailable`，导致列表截断或整个任务失败。修复前固定 Primary 至少保证视角一致（但主 Cookie 失效时任务直接失败）。`GetUserMedia` 有 `userMediaNeedsPrimaryClient` 保护（`client.go:536-545`），List/Following 没有等价保护。
- 建议：为 `GetFollowing`/`GetListMembers` 增加 `primaryOnly`（受保护账号、私有列表）参数或失败回退 Primary；至少在同一任务内固定同一个 Client 拉完分页。

### [R4] 88 变 transient + markRateLimited：Client-only 路径会长时间阻塞重试

- 位置：`internal/xclient/client.go:961-967`（88 → `markRateLimited`）、`:1117`（88 归为 transient）、`:871-900`（do() 5 次重试）；`internal/xclient/ratelimit.go:66-73`（等待）、`:129-147`（markRateLimited 默认 1 分钟）、`:170`（上限 15 分钟）；Client-only 调用点 `client.go:536-545`（受保护用户时间线）、`:804-809`（FollowUser）
- 影响：Pool 路径没有问题（`yieldsOnRateLimit` 会立即交还）。但受保护用户时间线固定用 Primary Client、`FollowUser` 也是 Client 直调，没有 yield 标记：API-200+code 88 后 `markRateLimited` 让该 path 阻塞（默认 1 分钟，若带 `Retry-After` 最多 15 分钟），`do()` 仍会重试 5 次，每次进 `before()` 等复位 → 单次调用可能阻塞约 4×(1min) 起、最坏约 4×(15min)。修复前 88 是永久错误、立即 disable 返回。属于"收紧分类"的副作用。
- 建议：Client-only 路径收到 88 后立即返回（不重试），或只允许 1 次重试；或把这些路径也切到 Pool（受保护账号可保留 Primary 但走带 yield 的封装）。

### [R5] 共享 `XClientPool` 后，设置页"检测"会直接 disable/enable 生产池里的客户端

- 位置：`internal/httpapi/server.go:284-300`（`s.manager.XClientPool(cfg)` + `pool.CheckAll(ctx)`）；`internal/jobs/manager.go:1030-1048`（共享缓存池）；`internal/xclient/client.go:412-451`（`/home` 403/401 → `disable()`）
- 影响：修复 `enable()` 被临时池绕过的问题时，把诊断和运行任务合并到同一个 Pool。`/home` 是 HTML 页面，被 WAF/Cloudflare 403 的概率高于 GraphQL；用户在归档运行中点击"检测 Cookie"，可能把正在使用的主 Cookie 禁用 30 分钟（jobs 随即换 Cookie 或任务失败）。同时用**未保存**的提交配置调用 `XClientPool(cfg)` 会临时替换 manager 的 `cachedPool`（key 变化），造成缓存抖动。
- 建议：`CheckAll` 改为非破坏性探测（只记录诊断、不 `disable()`，或 disable 只作用于诊断副本），或让 checkAuth 使用独立池并显式把成功的 `enable()` 合并回缓存池；提交配置与已保存配置分离。

### [R6] `httpx.ErrBodyTooLarge` 被当成瞬时可重试

- 位置：`internal/xclient/client.go:950-957`（返回 `httpx.ErrBodyTooLarge`）、`:1109-1124`（不匹配 APIError/HTTPError → 兜底 `err != nil && !isPermanent` = true）
- 影响：响应超过 32MB 是确定性错误，但会被 `do()` 重试 5 次（每次重新下载 32MB+），Pool 层还会换 Cookie 再试；纯浪费带宽与时间。
- 建议：在 `isTransientError` 里对 `httpx.ErrBodyTooLarge` 返回 false（或统一"输入/响应不合规"分类）。

### [R7] `errRateLimitBusyWait` 也被当瞬时可重试，放大等待

- 位置：`internal/xclient/ratelimit.go:47`、`:71-73`（超过 3 轮等待返回该错误）；`internal/xclient/client.go:1109-1124`（兜底 transient）
- 影响：该错误的语义是"放弃本次请求"，但会被 `do()` 再重试 5 次，每次 `before()` 又最多等 3 轮，等待时间被放大；最终错误文本 "rate limiter: 连续等待次数超限" 也会直接进入任务失败原因，可读性差。
- 建议：为它单独归类（非瞬时），并在 Pool 层转换为"客户受限、换 Cookie"或可读错误。

### [R8] 空页 + 非空 cursor 会继续翻页：持续空页时最多翻 1000 页

- 位置：`internal/xclient/client.go:591-593`（`len(items)==0 && next==""` 才结束）、`:570-578`（每页 250ms 间隔）；ListMembers/Following 同逻辑（`:729-731`、`:779-781`，且无页间 sleep）
- 影响：修复"空页提前终止"的方向正确，但若服务端持续返回"空页 + 每页新 cursor"（或 cursor 编码每页递增），会翻满 1000 页：时间线约 4 分钟/用户，列表/关注无 sleep 会快速耗尽 API 配额。
- 建议：连续空页 N 次（如 3）即停；列表/关注同样加页间节流。

### [R9] `previousCursor` 守卫是死代码（比的是上上页 cursor）

- 位置：`internal/xclient/client.go:641-649`、`:738-749`、`:788-799`
- 证据：

```go
if next == previousCursor { break }
if _, ok := seenCursor[next]; ok { break }
seenCursor[next] = struct{}{}
previousCursor = cursor
cursor = next
```

- 影响：`previousCursor` 被赋值为"本页请求用的 cursor"，而下一轮比较的是"下一页 cursor == 上上页 cursor"；真正想防的"cursor 不前进"（`next == cursor`）已被 `seenCursor` 覆盖，因此该分支实际不可达，容易误导维护者。
- 建议：改成 `if next == cursor { break }`（保留 seenCursor 兜底）或删除该变量。

### [R10] `apiError` 对非数组 `errors` 会静默当成功

- 位置：`internal/xclient/client.go:1005`
- 影响：`errors` 为对象/字符串时 `errorsResult.Array()` 为空，循环不执行 → 返回 nil（成功）；旧实现会返回 APIError。X 正常都是数组，但该分支让异常响应更隐蔽。
- 建议：`if !errorsResult.IsArray() { return &APIError{Code: -1, Raw: limitedErrorPayload(payload)} }`。

## 4. 测试补强评估

已补（有价值）：apiError 的 5 个新用例（含 88+null user、326+data、NonFatal+88）、Pool 88 交还、空页+cursor 续翻、malformed tweet 报错、置顶 stop 跨页续翻、firstBool null、sameOrigin 端口/降级、i/web URL、note_tweet+非法时间戳。
仍缺（按重要性）：
1. **P1-9 回归保护**：`Pool.GetListByID/GetListMembers/GetFollowing` 在 primary disabled / 429 时应从 backup 成功——完全没有测试。
2. **认证与请求构造**：没有任何测试断言 `Cookie: auth_token/ct0`、`X-Csrf-Token`、`Authorization`，也没有断言 GraphQL `variables/features/fieldToggles`；queryId 写错/头丢失时测试仍全绿。
3. **重定向**：`hardening_test.go` 只测 `sameOrigin` 纯函数，没测 `Client.http.CheckRedirect` 真实拒绝跨源/端口/降级；且删除了原来的"不同 host 必拒"断言。
4. **cursor 终止**：没有"重复 cursor 不前进"测试（`seenCursor`/新增 `previousCursor` 均未覆盖）。
5. **新逻辑**：`ErrBodyTooLarge`、`markRateLimited`、`prune`、`before` 的 `maxWaitIterations`、`XClientPool` 共享池均无测试。
6. **fixture**：仍是手写最小 JSON，没有真实 API golden；note_tweet 的 syndication 路径、`TweetUnavailable` 在时间线中的跳过行为都没有用例。

## 5. 待确认 / 需 Lead 交叉验证

1. **R1 的真实影响**需要真实 GraphQL 抓包：X 的 partial errors 是否总带 `kind:"NonFatal"`。若普遍不带，R1 应升级为 P1 并尽快回退为"data 非空 + 无 kind 也容忍"。
2. **syndication 长文**（§3.3 #4 的残余）：代码未读 `note_tweet`；我实测 `cdn.syndication.twimg.com/tweet-result` 的响应里没有 `note_tweet` 字段，且无法离线确认其对 >280 字长文是否截断。若该端点确实返回截断 `text`，单推任务（`ParseTweetLinkWithClient`）的长文文本仍不完整。
3. **跨 Cookie 视角问题（R3）**需要产品确认：AdditionalCookies 是否一般是同一批账号（都能看私有列表/关注受保护账号），还是完全不同的账号。若不同，`GetFollowing/GetListMembers` 的 Primary-only 保护是必须的。
4. **R5 的行为变化**需要 httpapi/jobs reviewer 确认：`/api/auth/check` 是否有必要用共享池（这是为了修 `enable()` 恢复）；建议至少让 CheckAll 不改生产池的 disabled 状态。
5. **queryId/404（§3.3 #9）与 403 策略（#10）**本轮未动；`review/FIXES.md:73` 已声明为"依赖真实 Cookie 验证、暂不处理"。请 Lead 确认该延期裁定，并排期后续版本。
6. **P1-9 的 FollowUser 残余**：`review/FIXES.md:18` 称自动关注保留主账号以"保持操作身份"。这与 REPORT 的修复建议（标题含 Follow）存在范围差异，请 Lead 裁定是接受该设计还是要求补 Pool 包装（可保留"优先 Primary、失败再换 Cookie"语义）。
7. `Media.ID` 语义（#13i）在 downloader/storage 侧是否被当作去重键——当前 grep 下游未使用，但需 storage reviewer 确认无隐藏依赖。

## 6. 结论

本轮修复质量总体较高：P1-9 主路径、apiError 核心判定、空页 cursor、note_tweet（GraphQL）、created_at、TweetUnavailable、重定向同源/降级、i/web、firstBool、32MB 截断、refund 时机均已落地，且有针对性测试；`Pool` 新方法与 Client 方法共用实现，未出现逻辑漂移。

需要 Lead 重点关注两类问题：
- **回归风险**：R1（apiError 过度收紧）、R2（单条解析失败阻断整用户且游标不推进）、R4（88 在 Client-only 路径长时间阻塞重试）、R5（设置页检测影响生产池）。
- **仍未修复**：queryId/404、403 禁用策略、退避抖动、ID 去重、syndicationToken/qualityFromURL/Media.ID，以及 P1-9 的 FollowUser 与测试缺口。
