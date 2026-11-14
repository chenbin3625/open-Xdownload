# xscrape：twscrape 风格账号池移植设计

本文件是各模块并行开发的契约。改动契约前先改这里。

## 目标与范围

- 用 twscrape 的模型替换 `internal/xclient` 底层：SQLite 账号表、持久化的按 queue 限流锁、`x-client-transaction-id`、queryId 自动发现、账号管理页。
- 账号只通过导入 Cookie 添加。**不做用户名密码登录。**
- jobs/httpapi 只依赖 `xapi.Source` 接口；`OPEN_XDOWNLOAD_SCRAPER=legacy|xscrape` 切换实现，本版默认 `legacy`。
- 不做：密码登录、TLS 指纹伪装、SearchTimeline。

## 包结构

```
internal/xapi       共享类型、错误、端点定义、纯解析与分页逻辑（不关心账号怎么选）
internal/xscrape    新实现：账号池、请求层、queryId、各接口
internal/xscrape/xtid  x-client-transaction-id 生成器
internal/xclient    旧实现：过渡期保留，凭据改从 x_accounts 读
internal/storage    x_accounts 表与查询
```

依赖方向：`xapi` 只依赖 `parser`/`config`/`httpx`；`xclient`、`xscrape` 依赖 `xapi`；`storage` 不依赖任何 x* 包；`jobs`/`httpapi` 依赖 `xapi` 和两个实现的构造函数。

## 兼容性硬约束

- `User`、`List`、`UserPage`、`TweetTimelinePage`、`UserMediaTimelinePage`、`MediaFetchStats` 的字段名不变：jobs 检查点按 JSON 持久化了它们。
- 成员分页的 `viewerKey` 继续是 `hex(sha256(auth_token))`，已保存的检查点要能续跑。
- 错误分类函数（`IsUnauthorized`、`IsAllClientsRateLimited`、`RateLimitRetryAt`、`IsInvalidTimelineCursor`、`ErrQueryIDStale`、`RateLimitedError`、`WithYieldToScheduler`）语义不变，移到 `xapi`，`xclient` 里保留同名别名。

## 账号表（storage）

```sql
CREATE TABLE IF NOT EXISTS x_accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  screen_name TEXT NOT NULL DEFAULT '',
  auth_token TEXT NOT NULL UNIQUE,
  ct0 TEXT NOT NULL,
  extra_cookies TEXT NOT NULL DEFAULT '',   -- JSON 对象 {name: value}，不含 auth_token/ct0
  user_agent TEXT NOT NULL DEFAULT '',      -- 空 = 全局默认
  proxy TEXT NOT NULL DEFAULT '',           -- 空 = 全局 ProxyURL
  is_primary INTEGER NOT NULL DEFAULT 0,    -- 至多一条为 1
  source TEXT NOT NULL DEFAULT 'db',        -- 'db' | 'env'
  active INTEGER NOT NULL DEFAULT 1,
  error_msg TEXT NOT NULL DEFAULT '',
  last_used DATETIME,
  verified_at DATETIME,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS x_account_locks (
  account_id INTEGER NOT NULL REFERENCES x_accounts(id) ON DELETE CASCADE,
  queue TEXT NOT NULL,          -- GraphQL operation 名；'*' 表示账号级冷却
  until DATETIME NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(account_id, queue)
);
CREATE TABLE IF NOT EXISTS x_account_stats (
  account_id INTEGER NOT NULL REFERENCES x_accounts(id) ON DELETE CASCADE,
  queue TEXT NOT NULL,
  day TEXT NOT NULL,            -- UTC 'YYYY-MM-DD'
  requests INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(account_id, queue, day)
);
```

只持久化**限流和冷却锁**。"正在使用"的租约放在内存里（沿用每账号单槽 + 3 秒间隔），否则两个并发任务撞同一 queue 时第二个会被当成限流、交还调度器 15 分钟。

### 一次性迁移

`runMigrationOnce("migrate_cookies_to_x_accounts")`：把 `app_config` 的 `auth_token`/`csrf_token` 写成 `is_primary=1` 账号，`additional_cookies` 用 `config.ParseCookiePairs` 逐条导入；重复 auth_token 跳过。旧列保留不删（下个版本删）。

### 环境变量账号

`OPEN_XDOWNLOAD_AUTH_TOKEN` + `OPEN_XDOWNLOAD_CT0` 存在时，启动时 `UpsertEnvXAccount` 写成 `source='env'`、`is_primary=1` 的账号；UI 中只读，不能删除、不能改 Cookie。

### Store API（Go）

```go
type XAccount struct {
    ID           int64        `db:"id"`
    ScreenName   string       `db:"screen_name"`
    AuthToken    string       `db:"auth_token"`
    CSRFToken    string       `db:"ct0"`
    ExtraCookies string       `db:"extra_cookies"`
    UserAgent    string       `db:"user_agent"`
    Proxy        string       `db:"proxy"`
    IsPrimary    bool         `db:"is_primary"`
    Source       string       `db:"source"`
    Active       bool         `db:"active"`
    ErrorMsg     string       `db:"error_msg"`
    LastUsed     sql.NullTime `db:"last_used"`
    VerifiedAt   sql.NullTime `db:"verified_at"`
    CreatedAt    time.Time    `db:"created_at"`
    UpdatedAt    time.Time    `db:"updated_at"`
}
type XAccountLock struct { AccountID int64; Queue, Reason string; Until time.Time }
type XAccountStat struct { AccountID int64; Queue, Day string; Requests int64 }
type XAccountPatch struct {
    Active    *bool
    IsPrimary *bool   // true 时清掉其他账号的 primary
    UserAgent *string
    Proxy     *string
    AuthToken, CSRFToken *string // env 账号拒绝
    ExtraCookies *string
}

ListXAccounts(ctx) ([]XAccount, error)                 // primary 在前，然后按 id
GetXAccount(ctx, id) (XAccount, error)                 // 不存在 -> ErrNotFound
CreateXAccount(ctx, XAccount) (XAccount, error)        // 重复 auth_token -> ErrValidation；首个账号自动 primary
UpdateXAccount(ctx, id, XAccountPatch) (XAccount, error)
DeleteXAccount(ctx, id) error                          // env 账号 -> ErrValidation；删 primary 后把最小 id 的 active 账号设为 primary
UpsertEnvXAccount(ctx, authToken, ct0 string) error
// PickXAccount 在一个写事务里选出 active、不在 exclude、queue 和 '*' 都没有生效锁的账号中
// last_used 最早（NULL 最先）的一个，并把 last_used 更新为 now。没有可选账号时 ok=false。
PickXAccount(ctx, queue string, exclude []int64, now time.Time) (acc XAccount, ok bool, err error)
// EarliestXAccountUnlock：在 active、不在 exclude 的账号里，queue 或 '*' 锁最早的解锁时间。
EarliestXAccountUnlock(ctx, queue string, exclude []int64, now time.Time) (time.Time, bool, error)
LockXAccount(ctx, id int64, queue string, until time.Time, reason string) error   // 已有锁取更晚者
UnlockXAccount(ctx, id int64, queue string) error      // queue == "" 解除全部
DeactivateXAccount(ctx, id int64, msg string) error
ActivateXAccount(ctx, id int64) error                  // 清 error_msg
MarkXAccountVerified(ctx, id int64, screenName string, at time.Time) error
SetXAccountError(ctx, id int64, msg string) error      // 只记录，不停用
IncrementXAccountStat(ctx, id int64, queue string, at time.Time) error
ListXAccountLocks(ctx, now time.Time) ([]XAccountLock, error)   // 只返回未过期
ListXAccountStats(ctx, day string) ([]XAccountStat, error)
PurgeExpiredXAccountLocks(ctx, now time.Time) error
```

## Cookie 输入解析（config）

`config.ParseCookieInput(text string) ([]CookieInput, []CookieInputIssue)`

```go
type CookieInput struct { AuthToken, CSRFToken string; Extra map[string]string }
type CookieInputIssue struct { Index int; Reason string }
```

接受：
1. 浏览器 Cookie 请求头字符串 `a=b; auth_token=..; ct0=..`（一行一个账号）
2. 两个裸值 `auth_token ct0` / `auth_token,ct0`（兼容旧备用 Cookie 格式，走 `ParseCookiePairs`）
3. JSON 对象 `{"auth_token":"..","ct0":"..",...}` 或其数组
4. 浏览器扩展导出 `[{"name":"auth_token","value":".."},...]`（一个数组 = 一个账号）

缺 auth_token 或 ct0 的条目进 issues。Extra 里丢掉 auth_token/ct0，只保留 `[A-Za-z0-9_\-.]+` 的名字和不含 `;`、换行的值。

## 共享接口（xapi）

```go
type Requester interface {
    GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error)
}
```

纯解析与分页函数接受 `Requester`。选账号相关的部分（主账号、viewer 绑定、年龄限制换号）由各实现自己处理。

`xapi.Source` 是 jobs/httpapi 唯一依赖的接口。方法集由 xapi 抽取时按 jobs/httpapi 的实际调用确定，并在本文件下方"Source 方法集"一节登记。

## xtid

```go
package xtid
func New(client *http.Client, baseURL, userAgent string) *Generator
func (g *Generator) Generate(ctx context.Context, method, path string) (string, error) // 懒初始化，TTL 1 小时
func (g *Generator) Invalidate()                                                       // 下次 Generate 重新抓取
```

`path` 是不带 query 的 URL path（如 `/i/api/graphql/xxx/UserMedia`），与 Python 版一致。

## HTTP API（/api/x-accounts）

都要求登录（沿用 `requireLogin`）。错误统一 `{"error": "..."}`。

```
GET    /api/x-accounts                 -> {scraper: "legacy"|"xscrape", accounts: XAccountView[]}
POST   /api/x-accounts                 {cookie, userAgent?, proxy?, isPrimary?} -> 201 XAccountView
POST   /api/x-accounts/import          {text} -> {created: XAccountView[], issues: [{index, reason}]}
PATCH  /api/x-accounts/{id}            {active?, isPrimary?, userAgent?, proxy?, cookie?} -> XAccountView
DELETE /api/x-accounts/{id}            -> 204
POST   /api/x-accounts/{id}/check      -> {ok, screenName?, error?, account: XAccountView}
POST   /api/x-accounts/{id}/unlock     -> XAccountView   （清除全部锁）
POST   /api/x-accounts/check-all       -> {results: [{id, ok, screenName?, error?}], accounts: XAccountView[]}
```

```ts
interface XAccountView {
  id: number;
  screenName: string;
  authToken: string;      // 打码："abcd…wxyz"
  csrfToken: string;      // 打码
  hasExtraCookies: boolean;
  userAgent: string;
  proxy: string;          // 打码掉 userinfo
  isPrimary: boolean;
  source: "db" | "env";
  active: boolean;
  errorMsg: string;
  lastUsed: string | null;    // RFC3339
  verifiedAt: string | null;
  createdAt: string;
  locks: { queue: string; until: string; reason: string }[];
  requestsToday: number;
  requestsByQueue: { queue: string; requests: number }[];
}
```

检测（check）用注入的 `xapi.HomeProber` 临时探测，不影响生产池：成功 -> `MarkXAccountVerified` + `ActivateXAccount` + 解除 `*` 锁；确认失效（`Unauthorized`）-> `DeactivateXAccount`；其他错误 -> `SetXAccountError`，不停用。探测端点见文末「2026-11-13 修订」。

`/api/config` 不再接受 `authToken`/`csrfToken`/`additionalCookies` 的写入（读取时也不再返回）；`/api/auth/check` 和"是否已配置 Cookie"改为看 x_accounts 是否有 active 账号。

## 错误处置（xscrape）

| 响应 | 处置 |
|---|---|
| 429 / API 88 | `LockXAccount(queue, x-rate-limit-reset)`，换号 |
| 401 | `/home` 确认失效 -> `DeactivateXAccount`；无法确认 -> 锁 `*` 5 分钟 |
| API 32/64/89/215/326 | `DeactivateXAccount` |
| 403 WAF 挑战页 | 锁 `*` 60 秒，换号 |
| 403 非 WAF | 锁 `*` 5 分钟 |
| 404 | `xtid.Invalidate()` + 重新发现 queryId，重试一次；仍 404 -> `ErrQueryIDStale` |

全部账号被锁：调度器上下文立即返回 `RateLimitedError{RetryAt: EarliestXAccountUnlock}`；否则最多等 `15m10s`。

## 账号管理 HTTP 层与 /home 探测（httpapi）

httpapi 不直接依赖 xscrape/xclient 的内部类型。账号检测通过一个探测函数注入：

```go
package xapi

// HomeProbeResult 是一次 /home 登录校验的结果。
type HomeProbeResult struct {
    ScreenName   string // 成功时为当前登录账号
    Unauthorized bool   // /home 明确返回 401：Cookie 确认失效
    Err          error  // 其他失败（网络、WAF、限流、页面改版）：不能证明失效
}

// AccountCredentials 是一次探测所需的凭据；Proxy 为空时使用全局代理。
type AccountCredentials struct {
    AuthToken, CSRFToken string
    ExtraCookies         map[string]string
    UserAgent, Proxy     string
}

type HomeProber func(ctx context.Context, creds AccountCredentials) HomeProbeResult
```

`Server.SetHomeProber(xapi.HomeProber)` 用于注入探测函数，由 `cmd/server` 负责接线；测试注入假函数。探测结果对应的存储操作见上面的「检测」说明。check-all 并发上限为 4，单个账号超时 20 秒。

`GET /api/x-accounts` 里的 `scraper` 字段取自 `jobs.ScraperMode()`，返回 `"legacy"` 或 `"xscrape"`，由环境变量 `OPEN_XDOWNLOAD_SCRAPER` 决定，默认 `legacy`。

改动账号的接口（POST/PATCH/DELETE/unlock，以及确认失效的 check）成功后，要调用 `manager.InvalidateXSource()`，让正在缓存的 Source 下次使用前重建或刷新。

代理字段用 `config.RestoreURLUserinfo(submitted, stored)` 还原打码值，用法与全局 ProxyURL 一致。前端编辑时会把打码后的代理原样带回来。

## Source 方法集

```go
type Source interface {
    GetUserByInput(ctx context.Context, input string) (User, error)
    GetListByID(ctx context.Context, id string) (List, error)
    GetUserByIDAsPrimary(ctx context.Context, id string) (User, error)
    FollowUserAsPrimary(ctx context.Context, user User) error
    FetchTweet(ctx context.Context, id string, options parser.ParseOptions) (parser.TweetData, error)
    WalkUserMedia(ctx context.Context, user User, options UserMediaTimelineOptions, save func(UserMediaTimelinePage) error) (MediaFetchStats, error)
    WalkUserTweets(ctx context.Context, user User, options TweetTimelineOptions, save func(TweetTimelinePage) error) error
    GetListMembersPage(ctx context.Context, list List, cursor, viewerKey string) (UserPage, error)
    GetFollowingPage(ctx context.Context, user User, cursor, viewerKey string) (UserPage, error)
    RecheckDisabledClients(ctx context.Context) bool
    CheckAll(ctx context.Context) PoolDiagnostics
    Diagnostics() PoolDiagnostics
    HasPrimary() bool
}
```

语义：

- `GetUserByInput`：按数字 ID 或 `@screen_name` 查询用户（对应 `UserByRestId` / `UserByScreenName`）。
- `GetListByID`：查询列表元信息；返回值的 `ViewerKey` 是读取该列表的账号 `hex(sha256(auth_token))`，供成员分页绑定视角。
- `GetUserByIDAsPrimary`：用主账号查询用户，受保护账号的 `following`/`muting` 等关系字段必须由主账号视角读取。
- `FollowUserAsPrimary`：用主账号关注用户，不经过账号轮换。
- `FetchTweet`：由账号池选中的账号抓取单条推文，受账号池限流与轮换约束；未配置 Cookie 时 legacy 仍以 guest/public 身份请求（无池可选的回退）。
- `WalkUserMedia`：逐页抓取 `UserMedia` 媒体时间线，每页解析/早停后回调 `save`，由调用方持久化游标。
- `WalkUserTweets`：逐页抓取全文时间线（独立 operation 与游标，不复用媒体时间线水位线）。
- `GetListMembersPage`：抓取列表成员一页；`viewerKey` 非空时固定使用该账号，保证私有列表视角一致。
- `GetFollowingPage`：抓取关注列表一页；受保护用户固定使用主账号视角。
- `RecheckDisabledClients`：立即对被禁用的 Cookie 做 `/home` 校验，返回此后是否至少有一个可用账号。
- `CheckAll`：并发校验全部账号并返回诊断快照（会 disable 失效账号）。
- `Diagnostics`：只读返回当前账号池诊断快照，不发起校验请求。
- `HasPrimary`：报告是否存在主账号，替代调用方直接访问 `Primary()`。

legacy 实现（`xclient.Pool`）直接实现该接口；其工厂 `xclient.NewSource` 在凭据缺失时返回一个仅支持 `FetchTweet` 的 source，其余方法返回与 `xclient.NewPool` 相同的错误。


## 2026-11-13 修订（实现前核对 x.com 现状）

- **账号校验探测改用 `GET /i/api/1.1/account/multi/list.json`。** X 现在对失效 Cookie 的 `/home` 返回 307 跳回首页并清掉 `auth_token`，不再返回 401，无法据此确认失效。2026-11-14 用真实 Cookie 实测：
  - `account/settings.json` 和 `account/verify_credentials.json`（x.com 与 api.x.com）即使 Cookie 有效也返回 404 + 错误码 34，不能用。
  - `multi/list.json`：有效 Cookie 返回 200 `{"users":[{"user_id","screen_name","name","is_auth_valid","is_suspended","is_protected",...}]}`；失效 Cookie 返回 401 + 错误码 32；ct0 与 auth_token 不匹配返回 403 `{"code":353,...}`（顶层对象，不是 errors 数组）。
  - 判定：200 时取 `is_auth_valid=true` 的条目（优先 `user_id` 与 `twid` Cookie 一致的那条），`is_suspended=true` 视为确认失效；401、错误码 32/64/89/215/326、403+353 视为确认失效；其余无法判断。
  - `xapi.HomeProber` 保留原名，判定规则写在 `internal/xapi/probe.go`。legacy 的 `/home` 自检不在本次范围内改动。
- **请求中遇到 HTTP 401 的处置顺序**：先用探测确认（2 分钟内确认过有效的账号不重复探测）。探测成功说明是目标/接口级 401，原样返回 `HTTPError`（`IsUnauthorized` 为真）；确认失效则 `DeactivateXAccount` 并换号；无法判断则锁 `*` 5 分钟并换号。错误码 32/64/89/215/326 出现在非 401 响应里时直接停用。
- **queryId 自动发现只是尽力而为。** 当前未登录首页已换成 x-web（Vite + Relay persisted query），不再引用 `responsive-web/client-web/main.*.js`，新 bundle 里也没有 `UserByScreenName`、`UserMedia` 等 operation 名。发现失败时使用内置表；内置 queryId 目前仍可用（2026-11-13 用 guest token 请求 `UserByScreenName` 返回 200）。优先级：环境变量覆盖 > 发现结果 > 内置表。
- **环境变量账号用 `Store.SyncEnvXAccounts` 同步**：`OPEN_XDOWNLOAD_AUTH_TOKEN`/`OPEN_XDOWNLOAD_CT0` 是主账号，`OPEN_XDOWNLOAD_ADDITIONAL_COOKIES` 按 `config.ParseCookiePairs` 逐条导入为非主账号。不再出现在环境变量里的 env 账号会被删除；同一组 Cookie 重启时保留 active/error_msg，换了 ct0 才重新启用。
- **爬虫开关**由 `jobs.ScraperMode()` 解析（`jobs.EnvScraper`、`jobs.ScraperLegacy`、`jobs.ScraperXScrape`），未识别的值按 legacy 处理。
- **legacy 账号池从 x_accounts 构建**：只使用 active=1 的账号，主账号（is_primary=1 且 active）排第一位。主账号停用或不存在时 legacy 池没有主账号（`HasPrimary()` 为 false，主账号专用操作返回错误），不会把备用账号当主账号用。账号级 proxy、user_agent、extra_cookies 在 legacy 里同样生效。没有任何 active 账号时退回 guest 推文抓取，且不再读取已废弃的 `cfg.AuthToken`。legacy 的禁用状态仍只在内存里，不写回 x_accounts。
- **账号检测的重新启用规则**：`PATCH active=false` 同时清空 error_msg；检测成功时只有被系统停用的账号（active=0 且 error_msg 非空）才重新启用，用户手动停用的账号保持停用。检测成功只解除 `*` 锁，不解除按 queue 的限流锁。
- **jobs 的 Source 缓存键只含全局代理**；账号变化统一通过 `InvalidateXSource` 触发重建。运行时不再读取 `AppConfig.AuthToken`/`CSRFToken`/`AdditionalCookies`，只有一次性迁移读取旧列。
- **`/api/config`**：GET/PUT 响应不再包含这三个字段；PUT 时空值和打码占位符静默忽略，真实值返回 400 并提示到账号池导入。
- **`/api/auth/check`** 保留为兼容接口：`configured` 表示存在 active 账号；对主账号（主账号不可用时取第一个 active 账号）做一次探测，不写库。
- **请求统计**只在请求成功时 +1（`IncrementXAccountStat`）。
- **xscrape 公共 API** 固定为 `internal/xscrape/engine.go` 里的 `Options`、`NewEngine(store, opts)`、`(*Engine).Source(ctx, cfg)`（签名与 `jobs.XSourceFactory` 一致）、`(*Engine).HomeProber()`。Engine 进程内只建一个，持有 xtid、queryId 表和每账号的内存执行槽；Source 重建时复用。cmd/server 无论哪种模式都创建 Engine，并把 `HomeProber()` 注入 httpapi；xscrape 模式下再把 `engine.Source` 设为 jobs 的工厂。
- **jobs 的 Source 工厂签名**改为 `jobs.XSourceFactory func(ctx, cfg) (xapi.Source, error)`；`Manager.XSource`、`Manager.NewXSource` 同样增加 ctx 参数。
- **`xapi.ViewerKey(authToken)`** 是 viewerKey 的唯一实现，两套实现共用。
- **没有 active 账号时，xscrape 的 FetchTweet 用 guest token 请求**（`POST {APIBaseURL}/1.1/guest/activate.json`，请求带 `x-guest-token`，loggedIn=false）。2026-11-13 实测：只带 bearer 请求 `TweetResultByRestId` 返回 403，带 guest token 返回 200。

## 2026-11-14 真实 Cookie 只读验收（xapi 层）

用户提供的账号（未写入仓库）对 x.com 的只读请求结果；请求参数由 `internal/xapi` 生成，响应交给 xapi/parser 的解析代码：

| 接口 | 结果 |
|---|---|
| UserByScreenName / UserByRestId（内置 queryId） | 200，`ParseUser` 正常 |
| UserMedia | 200，第一页 12 条媒体推文，Bottom 游标存在 |
| UserTweets | 200，`WalkUserTweets` 第一页 15 条本人推文 |
| Following | 200，第一页 50 个用户，有下一页游标 |
| ListByRestId / ListMembers（公开列表） | 200，元信息与成员正常；横幅子字段 code 214 部分错误按设计被容忍 |
| TweetResultByRestId（登录态） | 200，正文正常 |
| 不存在的列表 | 200，`data.list` 只有 `id`/`id_str`（没有 name），GetListByID 不报错 |
| legacy 未配置 Cookie 时的推文抓取 | 403（只带 bearer，没有 guest token） |

内置 queryId 目前全部有效。限流头 `x-rate-limit-limit/remaining/reset` 在 200 和 404 上都有，`reset` 为 unix 秒。
