# 定时归档按用户分批（archive batch size）设计

日期：2026-11-14

## 意图

NAS 上单账号归档 326 个关注用户时，全文（`tweets`/`both`）每轮都要给每个用户发至少一次
`UserTweets` 请求。X 对单账号该接口的配额实测为 50 次/15 分钟；任务撞限流后会被调度器
按 reset 时间自动重排，但单个任务累计推迟超过 6 小时会被判失败。

用户决定把定时归档拆成两个计划：

- 日常：`archiveMode=media`，请求量小（媒体数未增长的用户零请求）。
- 全文：低频（如每周），并且**每轮只处理固定数量的用户，按最久未归档轮转**，让单轮
  请求量可控，避免 6 小时上限。

本设计实现第二部分：归档任务的“每轮用户上限”。

## 目标

- 定时归档计划的目标可以配置 `batchSize`（每轮最多处理多少个用户）。
- 对 `following` / `list` 这类多用户目标，每轮按**最久未做全文归档**优先挑选 N 个用户。
- 每轮结束后自然轮转，所有用户最终都能被覆盖；失败用户不会永久占据名额。
- `media` 模式行为不变；未设置 `batchSize`（0）时行为与现在完全一致。
- 手工任务、立即运行、重试任务都能保留/使用该上限。

## 非目标

- 不做“只归档重点博主”的筛选功能。
- 不改 X 请求节奏、限流判定或 6 小时上限本身。
- 不做定时计划的编辑 UI（当前产品也不支持编辑目标；删除后重建即可）。
- 不给单用户任务引入额外语义（`user` 目标只有 1 个用户，上限无影响）。

## 方案

### 数据模型

- `storage.ArchiveScheduleItem` 增加 `BatchSize int`（JSON `batchSize,omitempty`）。
  - `0` = 不限制，保持现有行为。
  - 有效范围 `0..1000`，超出返回校验错误。
- `jobs` 表新增 `batch_size INTEGER NOT NULL DEFAULT 0`，通过现有
  `migrateArchive()` 的幂等 `pragma_table_info` + `ALTER TABLE` 方式添加。
- `storage.Job` / `JobDraft` 增加 `BatchSize`；创建、重试、定时领取时透传。
- 前端 `Job` / `ArchiveScheduleItem` 类型同步增加 `batchSize`。

### 选择算法

在 `Manager.archiveUsers` 中，**从检查点恢复时不重选**；新建任务且
`job.BatchSize > 0 && job.ArchiveMode.IncludesTweets()` 时：

1. 过滤：空 ID、`Blocking`/`Muting`（与 `archiveUserTasks` 一致），按用户 ID 去重。
2. 读取所有 `tweet_sync.updated_at`（新增只读方法，不产生 `GetTweetSync` 的插入副作用）。
3. 按 `updated_at` 升序排序：**没有 `tweet_sync` 行（从未全文归档）的最先**，其余越久
   未归档越靠前；时间相同按成员列表原顺序稳定排序。
4. 取前 N 个，再进入现有的受保护用户解析与任务构建流程。

放在受保护用户解析之前，避免为不在本批次的用户发 `GetUserByIDAsPrimary` 请求。

### 轮转与公平性

- 用户被处理后 `tweet_sync.updated_at` 会前移，自然排到下一轮后面。
- 失败用户也会写 `tweet_sync`（状态 failed），因此不会卡住队列。
- `Protected && !Following` 的用户会在 `archiveUserWithBudget` 里提前返回、不写
  `tweet_sync`。为免其永久占据批次名额，在该分支（仅全文模式）调用一次
  `TouchTweetSync` 只更新 `updated_at`，让其回队尾。自动关注逻辑保持不变。

### 进度与暂停

- 任务的 `tasks` 数量是**本批次**的用户数，进度按本批次计算（符合“每轮”语义）。
- 限流重排复用同一任务行与检查点，恢复时继续同一批，不会重新选人。
- 手工重试创建新任务，会按最新 `tweet_sync` 重新选一批。

### UI

- `CreateJobModal` 在“保存为定时计划”且归档模式包含全文时显示
  “每轮最多用户数”数字输入（0 表示不限制，建议值 50）。
- 非定时任务、`media` 模式不显示该输入。
- 计划卡片展示目标时附带 `每轮 N 人`（可选，实现时一并做）。
- i18n zh/en 同步补 key。

## 已定的取舍

- 选择键用 `tweet_sync.updated_at`，而不是 `user_entities.updated_at`：后者在媒体归档
  时也会更新时间，无法代表“全文归档多久没跑”。
- 只对包含全文的模式生效：`media` 模式已有廉价跳过，无需再分批。
- 用 LRU 而不是持久化“上次处理到第几个用户”的游标：用户列表会变化，LRU 无需维护
  游标，且失败/新用户能自动获得优先级。

## 留给实现

- 具体 SQL、迁移落点与测试清单。
- `TouchTweetSync` 的幂等 UPSERT 写法。
- 前端数字输入的默认值/文案（0 = 不限制）。
- 计划卡片是否展示 `batchSize`。

## 风险

- 两个计划（media / tweets）指向同一关注目标时会串行执行（当前 `MaxConcurrency=1`），
  互不冲突，但全文计划会排在 media 计划之后。
- 现有 NAS 上的旧计划需要删除后按新方式重建，才能带上 `batchSize`。
- `batchSize` 只限制单轮用户数，单个超大用户的首次全量仍可能撞 6 小时上限；其进度
  按用户持久化，下一轮会从保存的游标继续。
