# 修复复核 · 前端应用层（App / pages / lib / components{layout,modals,drawers,jobs,common}）

> 复核对象：`review/REPORT.md` 的 **P1-5** 与 **§3.5 前端（`apps/web`）** 中属于"应用层"的条目（`App.tsx` / `main.tsx` / `pages/*` / `lib/*` / `components/{layout,modals,drawers,jobs,common}`），以及 Lead 指定的关注清单。
> 复核方式：**修复复核，不是重新审查**。以当前工作区代码为准逐条核验；`git diff` 只用于确认"改了哪里"，结论一律引用**当前文件行号**。
> **基线快照**：本报告结论对应 **2026-10-28 12:37 的文件内容**。`apps/web/src` 自 10:57:49 之后无任何写入（`find apps/web/src -newermt "11:00"` 返回 0 个文件，12:16/12:20 两次确认），期间只有 `internal/webui/dist` 于 11:25 被他人重建。复核早期（10:45-10:57）工作区仍在他 reviewer 手中改动，那段时间的失败见 §5.3。
> 验证命令（均在 `apps/web` 下执行）：`npx tsc --noEmit -p tsconfig.json`（exit 0）；`npx vitest run`（23 文件 / 211 用例，共 8 次全量运行全绿，其中 3 次叠加 6 个 CPU 满载进程）；`npx vite build --outDir /tmp/oxd-build`（exit 0，2074 模块）。
> 约束遵守：仅新增本文件，未改动任何源码 / 测试 / 配置，未执行 npm install。

## 0. 状态总览

| # | REPORT §3.5 条目 | 状态 | 一句话结论 |
| --- | --- | --- | --- |
| a | 查询失败被渲染成"空状态" | ✅ | Gallery / FailedTweetQueue / Schedules / meta **4 处全部补了 isError 分支** |
| b | `api()` 无超时、无取消、2xx 非 JSON 抛原生 SyntaxError | ✅（含 3 个新风险） | 30s `AbortSignal.timeout` + `AbortSignal.any` + 空体/非 JSON 归一为 `ApiError`；但超时无 per-call 覆盖、运行时 API 无兜底、空体 `undefined as T`（见 §4 N-1/N-2/N-12） |
| c | 丢 SSE 事件后轮询兜底存在循环依赖 | 🟡 | meta 改成"活跃 5s / 空闲 30s **常驻**轮询"+ jobs 轮询改看**全局** `meta.stats.active`，循环已打断；残留"创建事件丢失且任务在下一次 meta 轮询前就结束"的窄窗口 |
| d | 媒体库全量 10 万条 + 每个终态全量失效 | 🟡 | 前端改服务端分页（60/页，含计数/用户列表/truncated），**响应体与主线程问题解决**；但后端仍按 10 万上限全量扫描/过滤，且筛选/搜索每个 keystroke 一次请求（见 §4 N-3/N-4/N-7） |
| e | `jobStatusPresentation[status]` 无未知值降级 + `isDashboardJobPayload` 弱校验 | 🟡 | 渲染崩溃已修（`unknownStatusPresentation` + `??` 兜底 + 测试）；`isDashboardJobPayload` **仍是只校验 id+status**，未收紧 |
| f | `jobErrors` 与后端聚合格式不符 | ✅ | `(共 N 次)`、`(@user)` 单目标、`另有 N 种其他异常` 三种新格式全部对齐，并有 3 条针对真实后端文案的测试 |
| g | `useTheme` 每实例独立 state | ✅ | 模块级 CustomEvent + `storage` 监听，Header 与设置页同步；`toggle` 改用 `resolveTheme(preference)`；2 条跨消费者测试 |
| h | ConfigForm 草稿同步"卡死" | ✅ | `pendingSavedConfigKey` 提升为 state + `draftRevision` 守卫 + 3s 兜底 + "重新加载配置"重置 dirty；4 条回归测试 |
| i | `BackupCookieInputs` 在 updater 内调 `onChange` | ✅ | updater 已改纯函数，`onChange` 移到 `setRows` 之后；行 id 用 `crypto.getRandomValues` |
| j | LocalDirectoryPicker 刷新无效 / 失败留旧树 / 越界无提示 | ✅ | effect 依赖改 `listing.data` 对象 + `isError`，刷新生效；失败清树 + 内联 Alert；"上级目录"在错误态禁用 |
| k | Cookie 单行粘贴污染 + 提示文案教用户这样粘 | ❌ | 全仓 `onPaste` 为 0 处；`cookieSettingsTips.backupCookie` 仍写"支持每行一个账号，格式为 `auth_token=xxx; ct0=yyy`"，而 UI 已是"每行两个独立输入框"，按提示粘贴仍会把整串塞进一个字段 |
| l | `NumberInput` 允许小数 → 后端 400 | ✅ | `parseInt(raw, 10)` + 失焦 clamp + `aria-valuemin/max/now` |
| m | Modal/Drawer 无 title 无关闭按钮 | ✅ | 两个分支都渲染关闭按钮 + sr-only `Dialog.Title` |
| n | Toast error 用 polite 且键盘无法暂停 | ✅ | error 项 `role="alert"`、容器常驻 `aria-live`、focus/blur 参与暂停，有 focus 暂停测试 |
| o | SearchSelect combobox 语义分裂 | ✅ | `role="combobox"` 从 Trigger 移到弹层 input，补 `aria-autocomplete`/`aria-activedescendant`/`aria-controls`；Trigger 改 `aria-haspopup="dialog"` |
| p | 对比度：`--fg-subtle` 与强调色 | 🟡 | `--fg-subtle` 亮色 2.6→**4.02:1**（仍 <4.5）、暗色 5.45:1 ✅；`--brand-contrast` 只接到 **Button primary**，Pagination 当前页 / Checkbox 勾 / 侧栏徽标 / 画廊播放按钮仍 `text-white` on `bg-brand-500`（深色钛金 1.10:1） |
| q | 构建配置死代码 / react-player | 🟡 | `react-player` 已从 `package.json` 移除且 `package-lock.json` 已重生成（0 处残留，依赖集合与 package.json 完全一致）；vite 的 player chunk / modulePreload 过滤已删、`build.target: es2020` 已加。残留：`preview` 仍无 `/api` 代理、dev/preview 仍 `--host 0.0.0.0`、无 sourcemap |
| r | 其他：`sseConnected` 无人消费 | ❌ | `useDashboardEvents.ts:278/377` 仍返回，`App.tsx:77` 直接丢弃 → 断线无任何 UI 提示 |
| s | 其他：重连补拉不含 jobFiles | ✅ | `invalidateAfterReconnect` 已含 `jobFilesQueryRoot`（`useDashboardEvents.ts:30-35`） |
| t | 其他：任务终态不失效失败队列 | ✅ | 终态 `job.updated` 现在同时失效 `libraryDownloadsQueryRoot` + `failedTweetQueryRoot`（`useDashboardEvents.ts:253-254`），有测试 |
| u | 其他：CreateJobModal scheme / 纯数字 ID | 🟡 | `normalizeTweetURL` 已补 scheme（手动输入与快捷入口共用，测试覆盖）；快捷入口 `detectQuickInputTab` 仍把 `/^\d{5,}$/` 判成 list |
| v | 其他：筛选状态未进 URL | ❌ | `GalleryPage` / `TaskCenterPage` 里 `useSearchParams` 均为 0 处 |
| w | 其他：`FailedTweet.payload` 幽灵字段 | ❌ | `api.ts:167` 仍声明，后端 `FailedTweetView` 仍无该字段 |
| x | 其他：`jobFilesResponse` omitempty 与前端类型不一致 | ✅ | 后端已去 `omitempty` 并加 `truncated`（`server.go:526-530`），前端 `JobFiles.truncated?: boolean` + 任务详情截断提示（`TaskCenterPage.tsx:906`） |
| y | 其他：CopyButton / VideoPoster 状态泄漏 | ✅ | CopyButton 定时器有清理；VideoPoster 通过 `key={`${item.id}:${previewUrl}`}` 重置，并有"源变化后重试海报"测试 |
| z | **P1-5** 轮询按旧快照重建列表 / 永久缺行 | ✅（含 2 个边界风险） | 保留窗口 + 排序 + 截断已实现且有 4 条测试；**但**保留判定依赖浏览器时钟，客户端时钟超前 >30s 时退化为原 bug（见 §1.3） |

> 净结论：§3.5 的 14 条应用层问题里 **11 条已真修复**、**4 条部分修复**、**4 条未动**（k、r、v、w）。
> **没有发现"修复引入的 P1 级新回归"**；新增问题集中在 `api()` 超时语义（N-1/N-2/N-12）与媒体库分页带来的请求/闪烁开销（N-3/N-4/N-5/N-7），均为 P2/P3。

## 1. P1-5 复核：✅ 主要场景已修复

### 1.1 保留窗口实现（✅）

- 位置：`apps/web/src/lib/useDashboardEvents.ts:92`（窗口常量）、`:94-125`（保留 + 排序 + 截断）
- 证据（关键片段）：

```ts
// :118-124 仅第一页；只保留"不在 fetched 内、创建于 30s 内、且比 fetched 首行更新"的行，然后按后端同序重排并截断
return !newestFetched || createdTime > newestCreatedTime ||
  (createdTime === newestCreatedTime && job.id > newestFetched.id);
...
items.sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt) || right.id - left.id);
items.splice(fetched.pageSize);
```

- 判定正确性：保留条件"比 fetched 首行更新（或同毫秒且 id 更大）"恰好等价于"这行本来应该出现在第 1 页首位"；排序键 `createdAt DESC, id DESC` 与后端 `ORDER BY created_at DESC, id DESC`（`internal/storage/storage.go:1060`）完全一致，因此不会把服务端顺序打乱；`splice(pageSize)` 保证页容量不变。
- 不会"复活已删任务"：后端**没有任何删除 jobs 的路径**（`grep "DELETE FROM jobs" internal/` 为 0 命中），因此保留行不可能对应一条已不存在的记录。
- 测试：`apps/web/src/lib/useDashboardEvents.test.ts` 新增 4 条（窗口内保留、>30s 丢弃、被挤出页丢弃、空第一页在途保留）。

### 1.2 轮询侧同步修复（✅，互补）

- `apps/web/src/App.tsx:90-94`：jobs 的 `refetchInterval` 改为读**全局** `dashboardMetaQueryRoot` 缓存的 `stats.active > 0`，不再只看"当前页有没有活跃任务"：

```ts
refetchInterval: () => {
  const metaData = queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot);
  return metaData && metaData.stats.active > 0 ? 5_000 : false;
},
```

- `App.tsx:104-107`：meta 从"空闲即停"改成"活跃 5s / **空闲 30s 常驻**"，这正好补上了 P1-5 描述里"快照里最后一个 active 任务结束后轮询全停"的洞——最坏情况下 30s 内一定能重新发现活跃任务并恢复 jobs 轮询（`refetchInterval` 是函数，App 因 meta 数据变化重渲染 → `setOptions` 重算 interval，机制已核对 TanStack v5 `QueryObserver#setOptions → #computeRefetchInterval`）。

### 1.3 残留风险（不阻塞，建议记 P2）

1. **时钟依赖**：`mergeNewerJobs` 用 `Date.now()` 与**服务端** `createdAt` 相减（`:113-114`）。若用户机器时钟比服务器快 >30s，`now - createdTime >= 30_000` 恒真 → 保留整体失效，P1-5 原样复现（表现为"新任务闪现后消失"，而非永久缺行——因为 meta 30s 兜底会把总数修回来）。查询响应本身带服务器时间可解，属可选优化。
2. **非法 createdAt 会打乱排序**：`items.sort` 的比较器在两行 `createdAt` 任一不可解析时返回 `NaN`，V8 会按实现相关顺序处理（等于放弃排序）；后端正常不会出现，但 `isDashboardJobPayload` 只校验 id/status（§0-e），畸形 payload 仍能进缓存。
3. **短暂跳行**：保留 k 行 → `splice(pageSize)` 会挤出 fetched 末尾 k 行；这 k 行在被下次轮询/翻页命中前不可见（服务端 offset 分页的固有代价，k 通常为 1-2，可接受）。

## 2. §3.5 逐条复核（应用层）

### 2.1 查询失败态：✅ 4/4 已修

| 位置 | 现状 | 证据 |
| --- | --- | --- |
| Gallery | 有 `isError` 分支，展示"媒体库加载失败: <msg>"（用的是 `Empty` 而非 `Alert`，且**没有重试按钮**） | `pages/GalleryPage.tsx:435-443` |
| FailedTweetQueue | 有独立 error 分支 | `components/jobs/FailedTweetQueue.tsx:136-138` |
| Schedules | 由 App 传 `error` 字符串，页面渲染 Alert（错误态下列表整体不显示） | `App.tsx:282`、`pages/SchedulesPage.tsx:105-107` |
| meta（右上统计） | `meta.isError` → Alert"统计信息加载失败" | `App.tsx:241-243` |

> 备注：Schedules 在"已有数据 + 后台刷新失败"时会把可用列表整体换成错误 Alert（`error ? ... : ...`），比"显示旧数据 + 顶部告警"差一档；Gallery 错误态无重试入口。两条都是 P3 体验项。

### 2.2 `api()` 超时 / 非 JSON：✅ 已修（新增风险见 §4）

- 位置：`apps/web/src/lib/api.ts:277-310`
- 证据：

```ts
const timeoutSignal = AbortSignal.timeout(30_000);
const signal = init.signal ? AbortSignal.any([init.signal, timeoutSignal]) : timeoutSignal;
...
const text = await response.text();
if (!text.trim()) return undefined as T;            // :303-305
try { return JSON.parse(text) as T; } catch { throw new ApiError("响应格式无效", response.status); }  // :306-309
```

- 已覆盖：挂起不再永久等待；HTML/纯文本 2xx → `ApiError("响应格式无效")`；空体 2xx → `undefined`；与调用方 `signal` 组合而非覆盖。测试：`lib/api.test.ts` 新增"2xx 非 JSON → ApiError"、"200 空体 → undefined"。

### 2.3 媒体库 10 万条 / 终态全量失效：🟡 客户端已解决，服务端成本仍在

- 前端：`pages/GalleryPage.tsx:236-243` 改为分页查询 `getLibraryDownloadsPage({page, pageSize: 60, kind, user, search})`；`getLibraryDownloadsPage` 定义在 `lib/api.ts:322-325`；计数/用户列表/截断标记全部取自响应（`GalleryPage.tsx:300-320/420`）。
- 终态失效仍然存在（每个终态事件一次），但现在只重拉**当前活跃的那一页**（`useDashboardEvents.ts:253`），不再拉 10 万条 → REPORT 描述的"主线程卡顿/内存峰值"问题实质解决。
- 未解决（服务端）：`internal/httpapi/server.go:776-780` 分页分支仍把 `limit` 强制为 `MaxLibraryDownloadsLimit`（10 万），并在 `:802-856` 对全部条目做内存过滤/计数/用户聚合。→ 每个分页请求都是 O(10 万)。配合本报告 N-3（每个 keystroke 一次请求）、N-7（每个终态事件一次失效），服务端成为新的热点。

### 2.4 未知 status 降级：✅（payload 校验仍弱）

- `pages/TaskCenterPage.tsx:95-103` 新增 `unknownStatusPresentation`（tone: neutral，文案"未知"），`:101-103` 的 `getJobStatusPresentation` 用 `?? unknownStatusPresentation` 兜底，`:244` 使用；测试：`TaskCenterPage.test.tsx` 新增"unknown status 渲染未知"。
- 后端 7 状态（pending/downloading/resolving/completed/failed/partial/canceled）与前端 7 键完全一一对应，已逐字核对。
- **未修**：`useDashboardEvents.ts:55-59` `isDashboardJobPayload` 仍只判断 `typeof id === "number" && typeof status === "string"`——白屏风险已消除，但畸形 payload（缺 createdAt/kind/input）仍会进缓存，见 §1.3-2。

### 2.5 jobErrors 与后端聚合格式：✅

后端真实文案（`internal/jobs/manager.go:1893-1926`）：
- 单目标：`%s (%s): %s` → `读取媒体时间线失败 (@alice): 连接超时`
- 多目标：`%s: %s (共 N 个账号: @a、@b)` / `%s (共 N 个账号: …)`
- 无目标：`%s: %s (共 N 次)` / `%s (共 N 次)`
- 尾部：`另有 N 种其他异常`

前端（`lib/jobErrors.ts`）：
- `:69-73` 聚合正则已支持 `(?:个(?:账号|目标|错误)?|次)` → `(共 7 次)`、`(共 31 个账号: …)` 都能识别；
- `:54-57` 新增 `distinctTrailer`（`/^另有\s*(\d+)\s*种其他异常$/`），`:148-150` 落为 `countUnit: "kinds"` 的独立组；
- `:115-121` 单目标行会把 `@alice` 与 `()`/`（）` 一并从 action 中剥离；
- 展示侧：`TaskCenterPage.tsx:653` 的 `{group.count} {group.countUnit ? "种异常" : "个失败"}`、`jobErrors.ts:221` 的 `部分失败 (N项，另有N种异常)`。
- 测试：`lib/jobErrors.test.ts` 新增 3 条，用的就是上述后端原文。
- 残留（P3）：若某条单行 reason 尾部长得像 `(限流 3 次)`，会被误判成"预聚合组"而截断 reason（`:71-73` 的正则先于单行分支）。

### 2.6 useTheme：✅

- `lib/useTheme.ts:112-135` 模块级 `THEME_CHANGE_EVENT` + `window.addEventListener("storage")`；`:156-166` `setPreference`/`setAccent` 写 localStorage 后派发事件；`:168-170` `toggle` 基于 `resolveTheme(preference)`。
- 效果：Header 与设置页两份实例同步；"跟随系统"监听器在 Header（常驻）实例上也会挂载，离开设置页后系统明暗切换不再失效。
- 测试：`lib/useTheme.test.ts` 2 条（同一 hook 里渲染两个消费者，改一个另一个跟随）。
- 残留（P3）：跨标签页依赖 `storage` 事件、同标签页依赖自定义事件，两条路径没有合并测试；`toggle` 在 `preference==="system"` 时会固化成显式 light/dark（旧行为，非回归）。

### 2.7 ConfigForm 草稿同步：✅

- `components/settings/ConfigForm.tsx:48-52`：`pendingSavedConfigKey` 改 useState + `pendingSavedTimer` ref + `draftRevision` ref。
- `:57-75` 同步 effect：pending 未匹配时 `return`（不覆盖用户草稿），匹配到则清 pending + 清定时器 + `setDraft`；无 pending 且 `!draftDirty` 才跟随 GET。三态冲突（dirty / pending / 刷新）都有明确优先级。
- `:101-118`：`onMutate: () => draftRevision.current` 快照；`onSuccess(updated, _submitted, submittedRevision)` 只在 `submittedRevision === draftRevision.current` 时 `setDraft`；否则保留用户新输入但仍失效 config 并 toast。
- `:112` 3s 兜底：env 覆盖部署下 PUT 响应与后续 GET 的 redacted 值不一致时，3s 后清 pending，让 effect 用最新 GET 值落草稿（不再"永久卡死"）。
- `:191-208`："重新加载配置"现在会重置 `draftRevision`/pending/timer/`draftDirty` 并回填 `normalizeConfig(config)`，修复了 REPORT 的"dirty 时静默无效"。
- 测试：`ConfigForm.test.tsx` 4 条（pending 期间编辑不被覆盖、env 覆盖 3s 后同步、dirty 时重新加载生效、常规保存同步）。
- 残留（P3）：`maxConcurrency: config.maxConcurrency || 1` 会把后端的 0 归一成 1（后端 `Normalized` 本就强制 1，无实际影响）。

### 2.8 `BackupCookieInputs` updater 副作用：✅

- `components/settings/CookieSettings.tsx:341-374`：`updateRow/addRow/removeRow` 先算出 `nextRows`/`nextValue`，再 `setRows(nextRows)` + `onChange(normalizeBackupCookieRows(nextRows))`；`setRows((currentRows) => normalize(currentRows) === nextValue ? currentRows : nextRows)` 保持纯函数。
- 全仓复查其余 updater：`Tree.tsx:75-85`、`SearchSelect.tsx:53`、`Table.tsx:49`、`CommonUI.tsx:118`、`CreateJobModal.tsx:279` 均为纯函数；未再发现"updater 内触发父级 setState"。
- 附带：行 id 用 `crypto.getRandomValues`（`:468-473`），`normalizeBackupCookieRows`（`:581-590`）输出带 id 的 JSON，与后端 `config.ParseCookiePairs` 的 JSON 分支格式一致（契约已核对 `internal/config/config.go:305-336`）。

### 2.9 LocalDirectoryPicker：✅

- `components/settings/LocalDirectoryPicker.tsx:64-85`：effect 依赖从字符串 `listing.data.path` 改为 `listing.data` 对象 + `listing.isError` → 刷新按钮 `listing.refetch()`（`:148`）现在会真正重建树；失败时清空 `treeData/expandedKeys/selectedPath` 并回退 `rootPath`。
- `:167`：`listing.isError` 时"上级目录"禁用；`:171-186` 目录读取失败改为内联 Alert（不再是 toast）；`:196-206` 子目录加载失败有行内错误；输入框有 `aria-label="下载目录"`（`:210`）。
- 测试：`LocalDirectoryPicker.test.tsx` 3 条（可靠名称、刷新后同路径显示新条目、失败清树）。
- 残留（P3）：每次成功 refetch 都会把 `expandedKeys` 重置为仅根 + `selectedPath` 拉回根（`:72-80`），即用户点了"刷新"后展开层级被折叠；属于既有行为，只是现在刷新真的生效后更容易被碰到。

### 2.10 Cookie 粘贴：❌ 未修

- 全仓 `grep -rn "onPaste" apps/web/src` = 0 处；主 Cookie 与备用 Cookie 都是普通受控输入（`CookieSettings.tsx:120-160`、`:230-300`），没有拆分逻辑。
- 提示文案仍在教用户按 `auth_token=xxx; ct0=yyy` 粘贴：`components/settings/CookieSettings.tsx:38-44`（`cookieSettingsTips.backupCookie`："支持每行一个账号，格式为 auth_token=xxx; ct0=yyy"）。但 UI 已改为"每行两个独立输入框"，`parseBackupCookieRows` 只用于解析**后端返回值**，没有任何入口能接受这种整行文本。
- 实际后果：用户按提示把整行粘进 `auth_token` 输入框 → 提交 `auth_token: "auth_token=abc; ct0=xyz"` → 该账号必然鉴权失败。
- 建议：要么给两个输入框加 `onPaste` 拆分（`splitCookieKeyValue` 已存在可直接复用），要么把提示改成"auth_token 只填值"。

### 2.11 NumberInput：✅

- `components/ui/Input.tsx:201-215`：`parseInt(raw, 10)`；非法/中间态（`"-"`、`""`）→ `null`；`:196` 失焦 clamp 到 `[min,max]`；`:100-108` `aria-valuemin/max/now`。
- 结论：`120.5` 不再透传到后端（会被截成 120），也就不再触发 Go `int` 解码 400。
- 残留（P3）：`parseInt("1e3") === 1`（少见），且缺"小数输入"的显式测试。

### 2.12 Modal / Drawer / Toast / SearchSelect / 对比度

| 子项 | 状态 | 证据 |
| --- | --- | --- |
| Modal 无 title 时有按钮 | ✅ | `components/ui/Overlay.tsx:54-61`（sr-only `Dialog.Title` + 关闭按钮）；`:50-51` 有 title 时同样有 |
| Drawer 无 title 时有按钮 | ✅ | `Overlay.tsx:160-166`；`:155-156` 有 title 分支 |
| Toast error live / 键盘暂停 | ✅ | `Toast.tsx:93` error 项 `role="alert"`；`:139-144` 容器常驻 `aria-live="polite"`；`:100-119` hover + **focus** 双暂停；测试"keeps a notification while its close button has keyboard focus" |
| SearchSelect 语义 | ✅ | `SearchSelect.tsx:74-78` Trigger 改 `aria-haspopup="dialog"`；`:105-112` 弹层 input `role="combobox"` + `aria-autocomplete="list"` + `aria-controls` + `aria-activedescendant`；`:136/145` listbox/option id 齐全 |
| `--fg-subtle` | 🟡 | `index.css:155` 亮色 `#718096`（白底 4.02:1 / canvas 3.84:1，仍 <4.5）；`:182/239` 暗色 `#85858f`（5.45/5.16:1 ✅） |
| 强调色对比 | 🟡 | `index.css:70/85/100/115/130` + 深色钛金覆盖 `:201` 已加 `--brand-contrast`，`Button.tsx:25` 改为 `text-brand-contrast`（深色钛金 18:1 ✅）；**仍硬编码 `text-white` on `bg-brand-500`**：`Pagination.tsx:100`（当前页数字，深色钛金 1.10:1 ≈ 不可见）、`Tag.tsx:106-110`（CountBadge，当前无使用方）、`Controls.tsx:157`（勾选对勾）、`AppSidebar.tsx:70`（𝕏 徽标）、`GalleryPage.tsx:117`（播放圆钮）。另 `text-brand-500` 作正文色在亮色 amber(2.05)/emerald(2.42)/x-blue(2.87) 仍不达 AA，见 `AppSidebar.tsx:93/97/127`、`AppHeader.tsx:122/135` |

> 对比度部分属于 rev-web-ui 的 `components/ui` 领地，这里只做交叉取证；**Pagination 当前页在深色+钛金下不可读**是本次新增的具体证据（`index.css:207`：`--color-brand-500: #f4f4f5`，叠加 `Pagination.tsx:100` 的 `text-white`）。

### 2.13 构建配置 / react-player：🟡

- `apps/web/package.json`：`react-player` 已从依赖移除；`package-lock.json` 已同步（用 node 比对 `dependencies` 键集合：missing/extra/mismatch 全为 0，`grep -c react-player package-lock.json` = 0）。
- `apps/web/vite.config.ts`：`manualChunks` 里 player 正则已删（现只有 react/query/icons/radix/vendor）；`modulePreload.resolveDependencies` 死过滤已删；新增 `build.target: "es2020"`。
- 残留：仍无 `build.sourcemap`；`preview` 段仍无 `/api` 代理（只有 `server.proxy`）→ `npm run preview` 必须同源由 Go 提供否则 API 404；`dev`/`preview` 脚本仍 `--host 0.0.0.0`（`package.json:7/9`，服务无鉴权，公开网络下等于开放管理端）。
- 旁证：早前出现在 `apps/web/` 的未跟踪 `pnpm-lock.yaml` 现已不存在，仓库只剩 `package-lock.json` 一份锁文件（与 npm 脚本一致）。

### 2.14 死代码 / 类型谎言（新增，P3）

- `lib/api.ts:259` `libraryDownloadsLimit`、`lib/api.ts:326-327` `getLibraryDownloads`：分页改造后**已无调用方**，成为新的死导出。
- `pages/GalleryPage.tsx:69/227` 声明 `jobs?: Job[]` 但函数体只解构 `downloads`；`App.tsx:287` 仍传 `jobs={jobsData}` → 画廊页白跑 jobs 查询（且该页 `isWorkbenchActive` 为真，活跃期每 5s 轮询一次）。
- `FailedTweetDrawer.tsx:34` 传 `items={[]}`，`FailedTweetQueue.tsx:33/55` 的 `items` 回退分支永远拿到空数组 → 死参数。

## 3. Lead 关注清单逐项（速查）

| 关注点 | 结论 |
| --- | --- |
| mergeNewerJobs 保留近期 SSE 行 | ✅ 窗口 30s + `createdAt/id` 双键排序 + `splice(pageSize)`，仅 page 1；测试 4 条；边界见 §1.3 |
| 窗口/排序/分页正确性 | ✅ 排序键与后端 SQL 一致；分页 total 由服务端给，不受保留行影响 |
| 是否仍可能长期缺行 | 常规场景不会；仅"客户端时钟快 >30s"或"创建事件丢失且任务 30s 内结束"两种窄场景仍可能（后者 ≤30s 由 meta 兜底，前者需要时钟修复） |
| Gallery 失败态 | ✅ `GalleryPage.tsx:435-443`（无重试按钮，P3） |
| FailedTweetQueue 失败态 | ✅ `FailedTweetQueue.tsx:136-138` |
| Schedules 失败态 | ✅ `SchedulesPage.tsx:105-107` + `App.tsx:282`（错误时列表整体替换，P3） |
| meta 失败态 | ✅ `App.tsx:241-243` |
| api() 超时/非 JSON | ✅ `api.ts:277-310`；风险见 §4 N-1/N-2/N-12 |
| SSE 丢事件兜底 | 🟡 meta 空闲 30s 常驻 + jobs 轮询看全局 active（`App.tsx:90-107`）；窄窗口见 §1.3 |
| 媒体库 10 万 / 终态全量失效 | 🟡 前端分页已落地；服务端每次请求仍 O(10 万)（`server.go:776-780/802-856`） |
| 未知 status 降级 / isDashboardJobPayload | 🟡 降级 ✅（`TaskCenterPage.tsx:95-103/244`）；payload 校验未收紧（`useDashboardEvents.ts:55-59`） |
| jobErrors 与新后端聚合格式 | ✅ 三种格式已对齐（`jobErrors.ts:54-57/69-73/115-121`） |
| ConfigForm 同步 | ✅（`ConfigForm.tsx:48-52/57-75/101-118/191-208`） |
| setState updater 副作用 | ✅ 已清（`CookieSettings.tsx:341-374`） |
| LocalDirectoryPicker | ✅（`LocalDirectoryPicker.tsx:64-85/167/171-186`） |
| Cookie 粘贴 | ❌ 无 `onPaste`；提示文案仍教旧格式（`CookieSettings.tsx:38-44`） |
| NumberInput | ✅ `parseInt`（`Input.tsx:201-215`） |
| Modal/Drawer | ✅（`Overlay.tsx:54-61/160-166`） |
| Toast | ✅（`Toast.tsx:93/100-119/139-144`） |
| SearchSelect | ✅（`SearchSelect.tsx:74-78/105-112`） |
| 对比度 | 🟡 `--fg-subtle` 亮色 4.02 仍 <AA；brand-contrast 只覆盖 Button，5 处 `text-white on brand-500` 未改 |
| 构建配置死代码 / react-player / lock | ✅ 依赖与锁已同步、vite 死规则已删、target 已加；🟡 preview 无代理、`--host 0.0.0.0`、无 sourcemap |
| sseConnected | ❌ 仍无人消费（`useDashboardEvents.ts:278/377`，`App.tsx:77` 丢弃） |
| 重连补拉 jobFiles | ✅（`useDashboardEvents.ts:30-35`） |
| FailedTweet.payload | ❌（`api.ts:167` vs 后端 View） |
| JobFiles omitempty | ✅（`server.go:526-530` 去 omitempty + `truncated`；`api.ts:236-240`；`TaskCenterPage.tsx:906`） |

## 4. 新增问题（本次修复引入或暴露）

### N-1【P2】`api()` 的 30s 硬超时对所有请求一视同仁，可能误杀合法长请求

- 位置：`lib/api.ts:280-283`（`AbortSignal.timeout(30_000)` 固定值，无任何 per-call 覆盖入口）
- 受影响的最慢请求：媒体库分页首屏（服务端 10 万行扫描 + 目录属性聚合，`server.go:802-856`）、`PUT /api/config` 改 `downloadDir` 触发的历史路径重写（`storage.go:1849-1880`）、`POST /api/auth/check`（后端自身 20s 超时，余量仅 10s）。
- 后果：客户端 abort 但**服务端继续执行**——配置保存这类写操作会变成"UI 报错但实际已成功"，用户重试可能叠加 effect；库页则是"加载失败"。
- 建议：给 `api()` 增加 `timeoutMs` 选项，至少让库分页 / 配置写入 / Cookie 检测走更长超时（或按方法区分）。

### N-2【P3】`AbortSignal.any` / `AbortSignal.timeout` 无兜底，`build.target: es2020` 也不会 polyfill 运行时 API

- 位置：`lib/api.ts:280-281`
- `AbortSignal.any` 需要 Chrome 116+/Firefox 124+/Safari 17.4+；`AbortSignal.timeout` 需要 Chrome 103+/Safari 16+。均不存在的浏览器上，**每个带 signal 的请求**都会 `TypeError: AbortSignal.any is not a function` → 整站数据层不可用。
- 建议：加一行 `typeof AbortSignal.any === "function"` 判断并退化为只用调用方 signal；或在 README/支持矩阵里写明最低浏览器版本。

### N-3【P2】媒体库搜索现在每个 keystroke 一次服务端全量扫描

- 位置：`pages/GalleryPage.tsx:236-243`（queryKey 含 `deferredSearchFilter`）+ `:234`（`useDeferredValue` 只延后渲染、**不是 debounce**）+ `server.go:776-780/802-856`（每请求 O(10 万)）
- 之前是"一次拉 10 万 + 客户端过滤"，现在是"每个字符一次请求 + 服务端全量过滤"。请求数从 1 变 N，单次响应变小。输入 5 个字符 ≈ 5 次全量扫描 + 5 份 60 条缓存（`gcTime` 30min）。
- 建议：搜索加 250-300ms debounce；或服务端对搜索做索引/预过滤。

### N-4【P2】Gallery 无 `placeholderData`，换页/换筛选时整块骨架闪烁且分页条消失

- 位置：`GalleryPage.tsx:236-243`（未设 `placeholderData: keepPreviousData`）、`:426-434`（`isLoading` 时渲染 12 张骨架）、`:462`（`totalEntries > pageSize` 才渲染分页）、`:300-320`（`counts`/`users` 在无 data 时回落到空结果）
- 现象：点页码/切筛选/打字 → 整页内容替换成骨架、分页条瞬时消失、分段控件计数变 0、"按用户筛选"触发器短暂显示 placeholder（即使筛选生效中）。
- 建议：`placeholderData: (previousData) => previousData`。

### N-5【P3】筛选切换会先发一次"新筛选 + 旧页码"的无效请求

- 位置：`GalleryPage.tsx:236-243`（queryKey 同一次渲染即用新 filter + 旧 `currentPage`）与 `:320-323`（`setCurrentPage(1)` 在 effect 里，晚一帧）
- 后果：每次切筛选多 1 个请求，且该请求可能命中空页（服务端会 clamp 页码）。

### N-6【P3】媒体库终态失效仍是"每个终态事件一次"

- 位置：`useDashboardEvents.ts:253`
- 批量任务收尾时 200 个终态事件 = 200 次 `/api/library/downloads` + 200 次服务端 10 万扫描（有 5min 缓存兜底，但过滤循环每请求都跑一遍）。
- 建议：与已有的 `scheduleRefresh`（800ms 去抖）合并，或对 libraryDownloads 失效也做去抖。

### N-7【P3】画廊灯箱左右切换被限制在"当前服务端分页 60 条"内

- 位置：`GalleryPage.tsx` 的 `filteredEntries` 在服务端模式下等于当前页条目；`previewIndex`/`stepPreview` 基于它。
- 行为变化：之前可以跨全库翻；现在到页边界就停。属于分页改造的必然结果，建议在灯箱底部加"下一页"或说明。

### N-8【P3】`api()` 空 200 体的 `undefined as T` 是类型谎言，下游可能 TypeError

- 位置：`lib/api.ts:303-305`
- 若反代/网关对某 GET 返回 200 空体，`getJobsPage` 会得到 `undefined`，`mergeNewerJobs(cached, undefined)` 在 `fetched.items` 处抛异常 → 任务页白屏（仍无 ErrorBoundary）。建议对"必须返回 JSON 的端点"把空体当错误。

### N-9【P3】jobs 轮询依赖 meta 缓存，首个 meta 请求就失败时永不轮询

- 位置：`App.tsx:90-94`（`getQueryData` 为 undefined 时返回 `false`）
- 场景：首屏 `/api/dashboard/meta` 500 且从未成功 → jobs 只靠 SSE；若此时 SSE 也不可用，列表不会自愈。建议 `metaData === undefined` 时退化为"按本页是否有 active 判断"的旧逻辑。

## 5. 验证与抖动结论

### 5.1 类型与测试

| 命令 | 结果 |
| --- | --- |
| `npx tsc --noEmit -p tsconfig.json` | exit 0，0 error（在 12:37 的稳定快照上重复执行 2 次） |
| `npx vitest run`（全量） | **23 文件 / 211 用例全绿**；共 8 次全量运行全部通过，其中 3 次在 6 个 CPU 满载进程下执行 |
| `npx vitest run src/pages/GalleryPage.test.tsx` | 15 次连跑，每次 8/8 通过 |
| `npx vitest run src/components/settings/ConfigForm.test.tsx` | 15 次单跑 + 6 路并行（叠加 CPU 负载）+ 8 次全量，全部 4/4 通过 |
| `npx vite build --outDir /tmp/oxd-build` | exit 0；2074 模块；index 143.46 kB；**无 react-player 相关 chunk/dep**；产物 hash 与 11:25 重建的 `internal/webui/dist` 一致 |

### 5.2 Lead 反馈的 `ConfigForm.test.tsx` 偶发失败：**未能复现，但定位到结构性风险点**

- 未复现：15 次单文件 + 6 路并行 + 8 次全量（含高负载）中 0 次失败。
- 结构性风险（建议修）：该用例依赖**真实 3s 定时器**与 **4s `waitFor` 上限**（`components/settings/ConfigForm.tsx:112` 的 `}, 3_000)` 对 `ConfigForm.test.tsx:79` 的 `{ timeout: 4_000 }`），余量只有 ~1s；我实测该文件在并发/负载下耗时 3.1s-4.7s 区间波动，因此 Lead 观察到的"并行跑时偶发超时"与这条**时序余量**高度吻合。
- 建议：改用 `vi.useFakeTimers()` + `advanceTimersByTime(3000)`，或把 3s 常量导出后在测试里按 2 倍余量等待；顺便把该用例耗时从 ~3s 压到毫秒级。

### 5.3 我复核早期观察到的一次失败（已定性，不是产品 flake）

- 10:50:20 的全量运行里 `GalleryPage.test.tsx > retries video posters when their source changes after a failed load` 失败（`Unable to find an element with the alt text: clip-a.mp4`）。
- 定性：**并发编辑造成的瞬态**——该测试文件写于 10:50:14，而它依赖的 `VideoPoster key={`${item.id}:${previewUrl}`}` 修复直到 10:54:24 才落进 `GalleryPage.tsx`（我 10:50:20 跑的时候组件还是旧版）。修复落地后该文件 15/15 通过、8 次全量无一失败。
- 结论：`apps/web/src` 自 10:57:49 起稳定（12:16、12:20 两次 `find -newermt "11:00"` 均返回 0 文件），本报告所有数字均取自该稳定快照。

### 5.4 新增测试质量评估

| 新增测试 | 是否断言真实行为 | 备注 |
| --- | --- | --- |
| `useDashboardEvents.test.ts` mergeNewerJobs 4 条 | ✅ | 直接断言保留/丢弃后的 id 序列，不是快照 |
| `GalleryPage.test.tsx` 服务端分页 / 页码回退 / 视频海报重试 3 条 | ✅ | 走真实 `fetch` 桩 + 新分页协议；**但大部分用例仍走 `downloads` prop（客户端过滤分支）**，服务端模式只有 3 条覆盖（生产路径覆盖偏薄） |
| `TaskCenterPage.test.tsx` 未知状态 | ✅ | 断言渲染"未知"而非 crash |
| `jobErrors.test.ts` 3 条 | ✅ | 直接粘贴后端真实格式 |
| `api.test.ts` 2 条 | ✅ | 但**没有超时/AbortSignal 组合的测试**（N-1/N-2 无回归网） |
| `ConfigForm.test.tsx` 4 条 | ✅ | 逻辑有效，第 2 条时序余量偏紧（§5.2） |
| `CookieSettings.test.tsx` 3 条 | ✅ | 覆盖清除哨兵、稳定 id、删中间行保 id |
| `LocalDirectoryPicker.test.tsx` 3 条 | ✅ | 覆盖刷新/失败清树/可靠名称 |
| `useTheme.test.ts` 2 条 | ✅ | 同标签页双消费者同步；跨标签页未覆盖 |

未覆盖（建议补）：`SchedulesPage` / `FailedTweetQueue` 的错误态、Gallery 服务端模式的 `isError` 分支、`App.tsx` 的 meta 兜底轮询逻辑、`api()` 超时与空体。

## 6. 交叉核对（给 Lead）

1. **后端契约已对齐**：`jobFilesResponse` 无 `omitempty` + `truncated`（`internal/httpapi/server.go:525-530`）、分页 `listDownloads`（`server.go:771-866`，返回 `items/pagination/counts/users/truncated`）、`ListJobsPage` 排序 `created_at DESC, id DESC`（`storage.go:1060`）、聚合文案（`manager.go:1893-1926`）、`ClearSentinel`/`ResolveSubmission`（`config.go:77/267-300`）均与前端新代码一致。
2. **jobs 永不删除**（全仓无 `DELETE FROM jobs`），所以 P1-5 的保留机制不存在"复活已删任务"风险——这点比报告担心的更安全。
3. **`internal/webui/dist` 状态**：11:25 被重建，当前是「16 个已跟踪文件被删 + 16 个新文件未跟踪」。若 CI 有 `git diff --exit-code -- internal/webui/dist` 之类的守卫，草稿态会失败，需要在提交时 `git add -A`（属 task-12 领地，仅提醒）。
4. **仓库杂物**（不在我范围，提醒）：根目录未跟踪的 `httpapi.test` 二进制、`cmd/server/lock_*.go`、`review/`。
5. **领地划分**：`components/ui`（Pagination/Tag/Checkbox 的 `text-white`、`--fg-subtle`、vite/tsconfig）与 `components/settings` 的 Cookie 清除/备用行 id 属 rev-web-ui（task-14）范围；我只做取证与契约核对，未与其结论重复计分。

## 附录 A · 关键行号索引（复核用）

| 主题 | 位置 |
| --- | --- |
| P1-5 保留窗口 | `apps/web/src/lib/useDashboardEvents.ts:89-128`（窗口常量 `:92`） |
| jobs/meta 轮询 | `apps/web/src/App.tsx:90-94`、`:104-107` |
| 重连补拉 / 终态失效 | `useDashboardEvents.ts:30-35`、`:253-254` |
| payload 校验 / sseConnected | `useDashboardEvents.ts:55-59`、`:278/377` |
| `api()` | `apps/web/src/lib/api.ts:277-310`（超时 `:280-281`，空体 `:303-305`） |
| 死导出 | `apps/web/src/lib/api.ts:259`、`:326-327` |
| 媒体库分页 | `apps/web/src/pages/GalleryPage.tsx:236-243`、`:300-320`、`:426`、`:435-443`、`:462` |
| 未知状态 | `apps/web/src/pages/TaskCenterPage.tsx:95-103`、`:244`、`:653`、`:906` |
| jobErrors | `apps/web/src/lib/jobErrors.ts:54-57`、`:69-73`、`:115-121`、`:208-221` |
| ConfigForm | `apps/web/src/components/settings/ConfigForm.tsx:48-52`、`:57-75`、`:101-118`、`:191-208` |
| Cookie | `apps/web/src/components/settings/CookieSettings.tsx:38-44`、`:101-115`、`:341-374`、`:468-473`、`:581-590` |
| LocalDirectoryPicker | `components/settings/LocalDirectoryPicker.tsx:64-85`、`:167`、`:171-186` |
| Overlay / Toast / SearchSelect | `Overlay.tsx:54-61/160-166`；`Toast.tsx:93/100-119/139-144`；`SearchSelect.tsx:74-78/105-112` |
| 对比度 | `index.css:70/85/100/115/130/155/182/201/207/239`；`Button.tsx:25`；`Pagination.tsx:100`；`Tag.tsx:106-110`；`Controls.tsx:157`；`AppSidebar.tsx:70`；`GalleryPage.tsx:117` |
| 构建 | `apps/web/vite.config.ts`（player chunk / modulePreload 已删，target es2020）；`apps/web/package.json:7/9` |
