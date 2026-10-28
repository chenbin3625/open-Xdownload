# 代码审查 · 前端应用层（pages / layout / modals / drawers / jobs / common / lib）

审查范围：`apps/web/src` 应用层（App/main、4 个 pages、layout/modals/drawers/jobs/common、lib 全部含测试、src/test）。
未审查（按分工）：`components/ui`、`components/settings`、`index.css`、`vite.config.ts`、`tsconfig*`。
约束遵守：未查看 git 历史/diff，未读 README、docs；只读代码。

辅助验证（已实际运行）：

- `cd apps/web && npx tsc --noEmit` → 无输出（通过）。
- `npx vitest run src/lib src/pages src/components/layout src/components/modals` → **12 个文件 / 118 个用例全部通过**（约 2s）。

> 范围说明：为核对调用契约，额外只读查看了 `components/ui/Pagination.tsx`、`ui/Table.tsx`、`ui/Overlay.tsx`（Drawer 部分）与 `components/settings/ConfigForm.tsx` 中 `useTheme` 调用点的少量片段；这些文件不作为本报告的独立审查对象，其完整审查归 task-7。

---

## 0. 阅读清单

| 文件 | 行数 | 已读完 |
| --- | ---: | :---: |
| apps/web/src/App.tsx | 318 | 是 |
| apps/web/src/main.tsx | 48 | 是 |
| apps/web/src/vite-env.d.ts | 3 | 是 |
| apps/web/src/pages/TaskCenterPage.tsx | 976 | 是 |
| apps/web/src/pages/GalleryPage.tsx | 544 | 是 |
| apps/web/src/pages/SchedulesPage.tsx | 245 | 是 |
| apps/web/src/pages/SettingsPage.tsx | 23 | 是 |
| apps/web/src/pages/TaskCenterPage.test.tsx | 414 | 是 |
| apps/web/src/pages/GalleryPage.test.tsx | 123 | 是 |
| apps/web/src/components/layout/AppHeader.tsx | 166 | 是 |
| apps/web/src/components/layout/AppSidebar.tsx | 142 | 是 |
| apps/web/src/components/layout/AppHeader.test.tsx | 87 | 是 |
| apps/web/src/components/layout/AppSidebar.test.tsx | 87 | 是 |
| apps/web/src/components/modals/CreateJobModal.tsx | 442 | 是 |
| apps/web/src/components/modals/CreateJobModal.test.tsx | 129 | 是 |
| apps/web/src/components/drawers/FailedTweetDrawer.tsx | 41 | 是 |
| apps/web/src/components/jobs/FailedTweetQueue.tsx | 205 | 是 |
| apps/web/src/components/common/CommonUI.tsx | 397 | 是 |
| apps/web/src/components/common/GlobalLoadingBar.tsx | 42 | 是 |
| apps/web/src/lib/api.ts | 396 | 是 |
| apps/web/src/lib/api.test.ts | 68 | 是 |
| apps/web/src/lib/bootstrap.ts | 89 | 是 |
| apps/web/src/lib/bootstrap.test.ts | 66 | 是 |
| apps/web/src/lib/jobStatus.ts | 41 | 是 |
| apps/web/src/lib/jobStatus.test.ts | 82 | 是 |
| apps/web/src/lib/jobErrors.ts | 229 | 是 |
| apps/web/src/lib/jobErrors.test.ts | 166 | 是 |
| apps/web/src/lib/useDashboardEvents.ts | 349 | 是 |
| apps/web/src/lib/useDashboardEvents.test.ts | 347 | 是 |
| apps/web/src/lib/useDashboardEvents.hook.test.ts | 119 | 是 |
| apps/web/src/lib/useRouteState.ts | 189 | 是 |
| apps/web/src/lib/useRouteState.test.ts | 66 | 是 |
| apps/web/src/lib/pagination.ts | 3 | 是 |
| apps/web/src/lib/useTheme.ts | 158 | 是 |
| apps/web/src/lib/useMediaQuery.ts | 33 | 是 |
| apps/web/src/lib/cn.ts | 9 | 是 |
| apps/web/src/test/setup.ts | 39 | 是 |
| apps/web/src/test/render.tsx | 11 | 是 |

合计 39 个文件、约 6.9k 行（含测试）。

---

## 1. P0 发现

**未发现 P0 级问题。** 具体核对过：

- 全范围内无 `dangerouslySetInnerHTML`、无 `target="_blank"`、无 `href=` 拼接用户数据（grep 结果为空）；图片/视频 `src` 只来自同源 `/api/...` 或后端 DB 中的 `previewUrl/mediaUrl`（`img/video src` 不可执行脚本），外链预览图额外加了 `referrerPolicy="no-referrer"`（`GalleryPage.tsx:112`）。
- 无 `eval`/`new Function`/`innerHTML`；无 token 写入 `localStorage` 之外的位置（主题配置只存 light/dark/system 与 accent，`useTheme.ts:7-8`）。
- 未发现必然崩溃路径（`main.tsx` 的 `getElementById("root")!` 依赖 `index.html` 有 `#root`，由 task-7 覆盖）。

---

## 2. P1 发现

### [P1-1] 轮询/刷新响应会把 SSE 刚插入的新任务从列表缓存中"抹掉"，且不会自愈

- 位置：`apps/web/src/App.tsx:81-97`（jobs query），`apps/web/src/lib/useDashboardEvents.ts:89-101`（`mergeNewerJobs`）、`:149-164`（`prependJobsToCaches`）
- 证据：

  ```ts
  // App.tsx:83-87 —— 每次轮询/手动/失效重拉都会走这里
  const page = await getJobsPage({ page: jobPage, pageSize: jobPageSize, signal });
  // 请求期间可能已有更新的 SSE 补丁落库，逐条比较 updatedAt，别把进度写回旧值。
  return mergeNewerJobs(queryClient.getQueryData<JobsPage>(queryKey), page);
  ```

  ```ts
  // useDashboardEvents.ts:89-101 —— 结果只由 fetched.items 构成
  export function mergeNewerJobs(cached: JobsPage | undefined, fetched: JobsPage): JobsPage {
    if (!cached) return fetched;
    ...
    const items = fetched.items.map((job) => { ... });   // 缓存里有、fetched 里没有的 job 直接消失
    return changed ? { ...fetched, items } : fetched;
  }
  ```

  ```ts
  // App.tsx:91-96 —— 列表轮询的前提是"当前页有 active 任务"
  refetchInterval: (query) => {
    const page = query.state.data as JobsPage | undefined;
    return page?.items.some((job) => jobStatusBucket(job.status) === "active") ? 5_000 : false;
  },
  ```

  触发链：当前页有活跃任务 → 每 5s 一次轮询；轮询请求在 T0 发出（服务端快照不含新任务）→ 后台/另一标签页/归档计划在 T1 创建任务，SSE `jobs.created` 走 `prependJobsToCaches` 把新任务写入第 1 页缓存 → 轮询响应在 T2 返回，`mergeNewerJobs` 以旧快照重建 items，新任务被丢弃。

- 影响：被丢弃的新任务在列表里"闪现后消失"。
  - 若该次轮询返回的快照里仍有活跃任务（常见），下一次 5s 轮询会重新拉到它（自愈，抖动 ≤5s）；
  - 若快照里最后一个活跃任务恰好已结束，jobs 轮询随即停止；后续该任务的 `job.updated` 因 `patchDashboardJobCaches` 找不到它（`found=false`），带 meta 时只返回 `"handled"`、不带 meta 时只做 `"refresh-meta"`（`useDashboardEvents.ts:219-239`），**都不会重新拉起 jobs 查询**；`refetchOnWindowFocus:false`（`main.tsx:22`）又关闭了兜底 → 统计卡片计数正确、表格里却长期缺这一行，直到用户手动刷新/切页/SSE 重连。批量创建（归档计划自动触发）+ 批量完成同时发生时最容易命中。
  - 注：这是竞态窗口问题（poll 在途 + 同时创建），不是每次创建必现，但一旦命中后一种情况没有任何自愈路径。
- 建议：二选一（或都做）——
  1. `queryFn` 记录 fetch 起始时间，merge 时保留缓存中「不在 fetched 内且 `updatedAt >= fetchStartedAt`」的 job（或在 `jobs.created` 处理后做一次 debounce 的 `invalidateQueries(jobsQueryRoot)` 让服务端快照自愈）；
  2. jobs 轮询条件不要只看当前页 items，改为 `meta.stats.active > 0 || 存在未落定的 prepend`（配合 App 现有的 meta 5s 轮询）。

### [P1-2] `useTheme` 是"每实例独立状态"，设置页改主题后两个实例状态分裂；"跟随系统"在离开设置页后失效

- 位置：`apps/web/src/lib/useTheme.ts:110-157`，调用点 `apps/web/src/components/layout/AppHeader.tsx:26`、`apps/web/src/components/settings/ConfigForm.tsx:235`（后者仅作调用点引用）
- 证据：

  ```ts
  // useTheme.ts:110-146 —— 每个调用点各持一份 state / 监听器
  export function useTheme() {
    const [preference, setPreference] = useState<ThemePreference>(readThemePreference);
    const [resolved, setResolved] = useState<ResolvedTheme>(() => resolveTheme(readThemePreference()));
    ...
    // 仅在跟随系统时监听系统切换；用户显式选择后系统变化不应覆盖其选择。
    useEffect(() => {
      if (preference !== "system" || !window.matchMedia) return;
      const query = window.matchMedia("(prefers-color-scheme: dark)");
      const handleChange = (event: MediaQueryListEvent) => { ... };
      query.addEventListener("change", handleChange);
      return () => query.removeEventListener("change", handleChange);
    }, [preference]);
  ```

  AppHeader 在应用生命周期内常驻（`App.tsx:217-223`），它的 `useTheme` 实例在启动时读取一次偏好（假设为 `"dark"`），之后**不会再被别处的修改同步**；设置页里的 `ThemeSettingsSection`（`ConfigForm.tsx:235`）是另一个实例。

- 影响：
  1. 用户在设置页选择「跟随系统」后，只有 ConfigForm 这个实例的 `preference === "system"`，系统主题监听器挂在它身上；**离开设置页（`SettingsPage` 随 section 卸载）监听器被移除**，此后系统明暗切换不再生效；Header 的 `preference` 仍是旧值、不会补挂监听。只有刷新页面后才恢复。
  2. 用户在设置页改主题/强调色后，Header 的太阳/月亮图标与强调色圆点仍是旧值（`AppHeader.tsx:143-152`、`:89-107`），直到 Header 自己再切一次或刷新；反之亦然（设置页的单选按钮状态不会跟 Header 的切换同步）。
- 建议：把主题改为单一数据源——根节点 `ThemeProvider` + Context（或 `useSyncExternalStore` 外部 store，监听 `storage` 事件），`useTheme` 只订阅、不各持一份；这样监听器与派生状态只有一份。

---

## 3. P2 发现

### [P2-1] 查询失败在多个页面被渲染成"空状态"，会误导用户以为数据丢失/引导重复创建

- 位置：
  - `apps/web/src/pages/GalleryPage.tsx:411-424`（无 `libraryQuery.isError` 分支）
  - `apps/web/src/components/jobs/FailedTweetQueue.tsx:42-55,135-138`
  - `apps/web/src/pages/SchedulesPage.tsx:103-138` + `apps/web/src/App.tsx:275-281`
  - `apps/web/src/App.tsx:100-109,164-170`（meta 失败时统计与分页静默归零）
- 证据：

  ```tsx
  // GalleryPage.tsx:411-424
  {!downloads && libraryQuery.isLoading ? (
    ...skeleton...
  ) : filteredEntries.length === 0 ? (
    <Card className="text-center py-12">
      <Empty description="暂无归档文件记录。完成下载任务后，媒体文件将自动在此呈现。" />
    </Card>
  ) : ( ... )}
  ```

  ```tsx
  // FailedTweetQueue.tsx:54,135-138
  const pageItems = failedTweetsQuery.data?.items ?? (page === 1 ? items.slice(0, pageSize) : []);
  ...
  ) : pageItems.length === 0 ? (
    <AppEmpty description="暂无失败推文" />
  ```

  ```tsx
  // App.tsx:275-281 —— schedules 的 isError 完全没有被使用
  {activeSection === "schedules" && (
    <SchedulesPage schedules={schedules.data ?? []} loading={schedules.isLoading} ... />
  )}
  ```

- 影响：接口 500/断网时：媒体库显示"暂无归档文件"（像归档丢失）；失败队列顶部徽标显示 `total>0`、正文却"暂无失败推文"（自相矛盾，且用户可能重复点击"全部重试"）；归档计划页显示"暂无定时归档计划 + 立即创建第一个计划"，可能诱导用户重复创建计划；`/api/dashboard/meta` 失败时任务总数恒为 0（`currentStats` 默认值 + `pagination.total=0`），表格却可能有数据。
- 建议：三个页面补 `isError` 分支（Alert + 重试按钮，参考 `TaskCenterPage.tsx:901-911` 的 `filesQuery.isError` 写法）；App 层对 `meta.isError` 也给出提示；错误态与空态必须区分。

### [P2-2] `api()` 无超时、无默认取消；2xx 非 JSON 会抛出非 `ApiError` 的解析异常

- 位置：`apps/web/src/lib/api.ts:268-287`
- 证据：

  ```ts
  export async function api<T>(path: string, init?: RequestInit): Promise<T> {
    const method = (init?.method ?? "GET").toUpperCase();
    const response = await fetch(path, { ...init, headers: {...} });
    if (!response.ok) {
      const payload: { error?: string } | null = await response.json().catch(() => null);
      ...
      throw new ApiError(message, response.status);
    }
    return response.json() as Promise<T>;   // 2xx 但非 JSON / 空 body → SyntaxError
  }
  ```

  全局只对部分 query 传了 `signal`（如 `getJobsPage/getConfig/getJobFiles`），所有 mutation（`createJob/retryJob/cancelJob/updateConfig/parseTweetLink/...`）都没有 `signal`，也没有 `AbortSignal.timeout`。
- 影响：后端 TCP 卡住/反向代理挂起时请求永不结束，按钮一直 loading、`GlobalLoadingBar` 一直显示，且无法取消；另一端返回 200 的 HTML（代理错误页、SPA fallback）时，调用方拿到的是英文 `Unexpected token '<'` 而不是后端错误信息；5xx 统一为 `"internal error"`（`internal/httpapi/server.go:1166-1175`），前端未做更友好提示。
- 建议：`api()` 内加默认 `AbortSignal.timeout(30_000)`（或用 `AbortSignal.any` 合并调用方 signal）；2xx 解析失败时抛出 `ApiError("响应不是合法 JSON", status)`；mutation 允许传 signal。

### [P2-3] 后端明确会"丢事件"，但前端的轮询兜底并不完整

- 位置：`apps/web/src/App.tsx:91-108`；后端约定 `internal/jobs/events.go:31-35`
- 证据：

  ```go
  // events.go:31-35
  // subscriberBuffer 是每个订阅者的 SSE 事件缓冲深度。... 缓冲溢出时 Publish 丢弃事件（非阻塞发送），
  // 前端依赖轮询对账兜底。
  ```

  ```ts
  // App.tsx:91-96 / 105-108
  refetchInterval: (query) => page?.items.some((job) => jobStatusBucket(job.status) === "active") ? 5_000 : false,
  refetchInterval: (query) => data && data.stats.active > 0 ? 5_000 : false,
  ```

- 影响：一旦创建事件被丢弃（慢消费者/瞬时断流），前端 meta 仍是 `active=0`，jobs 页也无 active 项 → 两个轮询都停，服务端新任务/状态变化不会再被对账（另一种表现即 P1-1 的长期陈旧）。后端把"轮询对账"当作兜底，但前端兜底依赖的正是"已知有活跃任务"这一前提，形成循环依赖。
- 建议：给 jobs/meta 增加低频兜底轮询（如 meta 常驻 30s，或在 SSE 断开时启用 30s 轮询）；或者后端为 SSE 丢事件计数并下发，前端据此主动 invalidate。

### [P2-4] 媒体库全量拉 10 万条，且每个任务终态事件都会触发一次全量重拉

- 位置：`apps/web/src/pages/GalleryPage.tsx:237-244`、`apps/web/src/lib/useDashboardEvents.ts:223-226`、`apps/web/src/lib/api.ts:248-250`
- 证据：

  ```ts
  // GalleryPage.tsx:237-244
  const libraryQuery = useQuery({
    queryKey: libraryDownloadsQueryRoot,
    queryFn: ({ signal }) => getLibraryDownloads(libraryDownloadsLimit, signal), // 100000
    staleTime: 10 * 60_000,
    ...
  });
  ```

  ```ts
  // useDashboardEvents.ts:223-226
  // 任务结束意味着可能落了新文件；媒体库 staleTime 很长，需主动失效（只会重拉活跃查询）。
  if (isJobTerminal(event.payload.status)) {
    void queryClient.invalidateQueries({ queryKey: libraryDownloadsQueryRoot });
  }
  ```

- 影响：后端 `storage.MaxLibraryDownloadsLimit = 100000`（`internal/storage/storage.go:48`），单次响应可能是几十 MB；归档进行中每完成一个任务就失效一次，媒体库保持打开时会反复全量下载 + `JSON.parse` + 10 万条 `indexedDownloads` 分组（`GalleryPage.tsx:249-271`）→ 主线程卡顿、内存峰值高。批量归档（数百任务）时尤其明显。
- 建议：后端提供分页/增量接口（`?since=`/`?afterId=`），或按 job 只增量插入新记录到缓存；至少对失效做合并去抖（例如 2s 内多次终态只重拉一次），并把 `libraryDownloadsLimit` 降为分页大小。

### [P2-5] 状态映射对未知 `status` 无降级，SSE payload 校验过松，可能整表崩溃

- 位置：`apps/web/src/pages/TaskCenterPage.tsx:233-239`；`apps/web/src/lib/useDashboardEvents.ts:54-62`
- 证据：

  ```tsx
  // TaskCenterPage.tsx:233-239
  render: (record) => {
    const meta = jobStatusPresentation[record.status];
    return (
      <Tag tone={meta.tone} icon={meta.icon}>   // status 未知时 meta 为 undefined → TypeError
        {meta.label}
      </Tag>
    );
  },
  ```

  ```ts
  // useDashboardEvents.ts:54-58 —— 只校验 id + status，其余字段缺失也会进缓存并被渲染
  export function isDashboardJobPayload(payload: unknown): payload is Job {
    if (!payload || typeof payload !== "object") return false;
    const job = payload as Partial<Job>;
    return typeof job.id === "number" && typeof job.status === "string";
  }
  ```

- 影响：后端未来新增状态（或旧服务端/新前端混跑、事件被中间层篡改）时，SSE `job.created` 会把该 job 直接 prepend 进缓存，首屏渲染 `meta.tone` 抛异常；应用没有 ErrorBoundary，整页白屏。即使不白屏，进度/统计也会错桶（`jobStatusBucket` 对未知值返回 `"idle"`）。本项目 7 个 status 目前与 `internal/storage/models.go:12-20` 完全一致（已核对），属于接口演进风险而非当前缺陷。
- 建议：`jobStatusPresentation` 改用 `Record<string, ...>` + `?? fallback`（label "未知状态"、tone default、progressStatus normal）；`isDashboardJobPayload` 补齐 `kind/input/progress/createdAt/updatedAt` 类型校验。

### [P2-6] `jobErrors` 的解析规则与实际后端聚合格式存在偏差，计数/文案会不准

- 位置：`apps/web/src/lib/jobErrors.ts:53-69`；后端格式 `internal/jobs/manager.go:1820,1836-1838,1845`
- 证据：

  ```ts
  // jobErrors.ts:53-69
  const trailerMatch = chunk.match(/^另有\s*(\d+)\s*个错误$/);
  ...
  const backendGroupMatch = chunk.match(
    /^(?:([^:：]+)[:：]\s*)?(.*?)\s*\((?:共\s*)?(\d+)\s*个(?:账号|目标|错误|次)?(?::|：)?\s*(.*?)\)$/,
  );
  ```

  ```go
  // manager.go:1820 / 1836-1838 / 1845
  fmt.Sprintf("%s (%s): %s", g.action, g.targets[0], g.reason)       // 单目标：action (@u1): reason
  fmt.Sprintf("%s: %s (共 %d 次)", g.action, g.reason, g.count)     // 无目标聚合：没有"个"字
  fmt.Sprintf("%s (共 %d 次)", g.reason, g.count)
  fmt.Sprintf("另有 %d 种其他异常", len(groups)-limit)               // 不是 "个错误"
  ```

- 影响：
  1. `"(共 N 次)"` 不匹配前端正则（正则要求 `(\d+)\s*个`），会被当成单条错误，`count` 记 1。多条此类错误聚合时，进度列显示"部分失败 (3项)"而真实是 9 项，`JobErrorAlert` 的"X 个失败"也会少算。
  2. 单目标格式 `读取媒体时间线失败 (@u1): 限流` 走单项分支后，`cleanPrefix` 只删 `@handle` 与"的"，括号残留 → `action` 变成 `"读取媒体时间线失败()"`（`jobErrors.ts:105-113`），作为 Tag 展示。
  3. `"另有 N 种其他异常"` 不匹配 trailer 规则，被当作一条独立的"错误原因"展示。
  后端聚合格式是该功能的主要输入（`archiveIssueSummary`），这种偏差会让"结构化错误分组"在真实数据上部分失真。
- 建议：与后端 owner 对齐 format 契约（最好由后端输出结构化 JSON：`[{action,reason,count,targets}]`，前端不再正则解析）；过渡期补 `(共\s*N\s*次)` 与 `另有\s*N\s*种其他异常` 两条规则，并修 `cleanPrefix` 的括号清理；把后端真实样例加入 `jobErrors.test.ts`。

---

## 4. P3 与优化建议

### [P3-1] 死代码 / 无效 prop / 未消费的状态

- `App.tsx:310-315` 给 `FailedTweetDrawer items={[]}`，而 `FailedTweetQueue` 自己请求数据（`FailedTweetQueue.tsx:42-45`），`items` 只在"查询尚无数据且 page===1"时作 fallback（`:54`）——该 prop 实际永久为空，属于死参数。
- `App.tsx:283` `<GalleryPage jobs={jobsData} />`，但 `GalleryPage` 解构只取 `downloads`，`jobs` 声明了却从未使用（`GalleryPage.tsx:69-72,228`）；同时 gallery 页仍启用 jobs 查询与可能的 5s 轮询（`App.tsx:72-78,81-97`），纯属浪费请求。
- `useDashboardEvents` 返回 `sseConnected`（`useDashboardEvents.ts:249,348`），但 `App.tsx:78` 丢弃返回值，全仓库无消费点 → 用户对"实时更新已断开（如服务端 503 订阅上限）"毫无感知，只能等下一次刷新；同时该 state 每次连接/断开会让 App 重渲染。`AppHeader.test.tsx:56-63` 还断言了"不展示运行状态标签"，说明这可能是产品上刻意移除的展示，但 `sseConnected` 没有一并删除。
- `lib/api.ts:162-177` `FailedTweet.payload` 前端类型有、后端 `FailedTweetView`（`internal/storage/models.go:190-204`）没有，且前端从未读取；属于契约残留。

### [P3-2] SSE 相关的小遗漏

- 重连补拉集合不含任务文件清单：`useDashboardEvents.ts:30-35` 只失效 jobs/meta/library/schedules/failed-tweets；断线期间某任务落盘的新文件，不会让已展开行的 `[...jobFilesQueryRoot, id]` 失效（该失效只在实时 `job.updated` 终态时发生，`:227-231`）。建议重连时一并失效 `jobFilesQueryRoot`。
- 任务终态不失效失败队列列表：`:219-239` 只失效 `libraryDownloadsQueryRoot`/`jobFiles`，不带 `failedTweetQueryRoot`；而归档任务执行中会新增 failed_tweets（`internal/jobs/manager.go:1309-1320`，无对应 SSE 事件），抽屉若保持打开，列表不会更新（计数由 meta 更新，会出现"徽标 N、列表空/旧"）。
- `reconnectPendingRef` 在 `enabled=false` 时不清空（`:252-259`），设置页往返后会多做一次全量补拉（无害但多余）。
- `applyDashboardEvent` 对无 `type`/未知 `type` 事件一律 `"refresh-workbench"`（`:240`），未知事件类型会退化成整组失效；可加白名单日志。

### [P3-3] 计时器/状态小问题

- `CommonUI.tsx:291-305` `CopyButton` 的 `window.setTimeout(() => setCopied(false), 1200)` 没有保存句柄、不在卸载时清理（组件在列表/抽屉里会被频繁卸载）。
- `GalleryPage.tsx:93-116` `VideoPoster` 的 `sourceIndex` 不随 `item` 变化重置：同一张卡片（key 相同）复用组件时，上一次已递增到 `sources.length`，新 item 会直接不渲染封面。
- `TaskCenterPage.tsx:718-728` `ExpandedJobDetails` 的 effect 在 `downloads/failed` 长度变化时强制切 tab，会覆盖用户手动选中的 tab（轮询刷新也会触发）。
- `GalleryPage.tsx:316-323,440-450`：`currentPage` 在过滤/数据收缩后不收敛；`Pagination` 内部会把页码 clamp 到 `totalPages`，但 `visibleEntries` 仍按未收敛的 `currentPage` 切片 → 可能出现"分页器显示第 1 页、网格为空"。建议像 `CommonUI.useClientPagination` 那样在 `maxPage` 变化时 `setPage(min)`。
- `TaskCenterPage.tsx:530-537`：筛选激活时把 `page` 强制为 1、`total` 换成"本页匹配数"，用户无法翻页找匹配项（虽是"仅当前页筛选"的刻意设计，但 +"共 0 条"会让页大小下拉旁出现 1 页的错觉）；建议筛选激活时隐藏页码、只保留"清除筛选"。

### [P3-4] 表单与输入校验不一致

- `CreateJobModal.tsx:142-160` 只对**快捷输入**的推文链接补 `https://`；用户直接在"单条推文"输入框手输 `x.com/u/status/1` 会原样提交，而后端要求带 scheme（`internal/parser/service.go:109-116` 对 `parsed.Scheme == ""` 直接报"无效链接"）。同一输入从两个入口进入结果不同。
- `CreateJobModal.tsx:88-94` `detectQuickInputTab` 把 `/^\d{5,}$/` 一律判为列表；而同一个弹窗的"用户归档"提示写"支持用户名、@screen_name 或数字 ID"（`:54`），纯数字用户 ID 经顶部快捷框粘贴会被错误路由到列表任务（`CreateJobModal.test.tsx:50` 用 `["1492019283","list"]` 固化了该行为）。建议在快捷入口把纯数字归为用户、列表只认 `lists/` 链接，或在 UI 上明确提示。
- `CreateJobModal.tsx:96-122` 无批量条数上限预校验；后端 jobs 批量上限 200（`internal/httpapi/server.go:346-350`）、计划目标上限 200（`internal/storage/storage.go:73`），超限只能在提交后看 toast 报错。
- `SchedulesPage.tsx:159-163` 开关直接 `!schedule.enabled`：请求未返回前连点两次，两次都基于旧 prop 计算为同值（都设为启用），UI 与最终状态可能不一致。建议 pending 时禁用开关或做本地乐观状态。
- `CreateJobModal.tsx:323` 直接展示后端 ISO 时间 `parsedTweet.createdAt`（未走 `formatDateTime`），零值时会显示 `0001-01-01T00:00:00Z`。

### [P3-5] 路由与可恢复性

- 任务中心的状态/搜索/类型筛选（`TaskCenterPage.tsx:106-108`）与媒体库筛选（`GalleryPage.tsx:229-231`）都是组件内 state，未进 URL：切页/刷新即丢失，无法分享"筛选后的视图"；分页却进了 URL（`useRouteState.ts:57-74`），语义不一致。
- `useRouteState.ts:139-149` 的 `popstate` 只回填状态，不修正非法/非规范 URL（`shouldReplace` 只在初次挂载用一次 `:133-137`）；例如 history 回到 `?pageSize=30` 时地址栏保留非法参数，直到下一次导航才被规范化。
- `useRouteState.ts:127-137` 的 `initialRoute` 用 `useMemo(() => readRouteState(), [])` 冻结首次取值，StrictMode 下无副作用问题；但 `syncServerPage` 的 `replaceState`（`:171-178`）会静默改地址栏，用户按浏览器"后退"时可能感觉丢了一步（当前位置被 replace 掉了）。

### [P3-6] 性能细节

- `TaskCenterPage.tsx:197-334` 的 `columns` 数组每次渲染重建，`Table` 在大列表下每 SM 事件（进度 ~400ms/次）都会重渲染整表；50~100 行时可接受，但可对 `columns` 做 `useMemo`/行组件 `React.memo`。
- `CommonUI.tsx:229-231` `PaginatedList` 用 `index` 作为 key，翻页/过滤时 DOM 复用可能错位（行内无 state，当前影响小）。
- `GalleryPage.tsx:283-295` `userOptions` 每次数据变化都对全量记录建 Map 并 `localeCompare` 排序，10 万条时开销大；可缓存或改服务端 distinct。
- `CommonUI.tsx:335-350` `formatDateTime` 模块级 formatter 做得好；但固定中文 locale、依赖浏览器本地时区且不含秒。任务监控场景下"更新时间"精确到分钟、且未标注时区，跨时区/快速连续更新时信息不足。

### [P3-7] 可访问性

- `CreateJobModal.tsx:407-410` 的 `Checkbox`、`:264-272` 的 `Textarea`、`:286-295` 的推文 URL `Input`、`:426-433` 的 `NumberInput` 都没有 label/`aria-label`/`htmlFor` 关联（只有 placeholder 或相邻文本），读屏用户无法获知控件用途。
- `TaskCenterPage.tsx:310-318` 的"取消"直接执行、无二次确认；`FailedTweetQueue` 的"全部重试"也无确认（会创建后台任务）。清空/删除类危险操作都有 `Popconfirm`（`FailedTweetQueue.tsx:114-131`、`SchedulesPage.tsx:212-226`），这一点做得对。
- `GlobalLoadingBar.tsx:25-27` 用 `aria-hidden` 隐藏加载条是正确的（避免读屏噪音），但没有任何 `aria-busy`/live region 表达"正在请求"。

### [P3-8] 测试质量与覆盖缺口

- 完全没有测试的文件：`SchedulesPage.tsx`（245 行，含 3 个 mutation 与开关/删除/立即执行）、`FailedTweetQueue.tsx`（205 行）、`FailedTweetDrawer.tsx`、`CommonUI.tsx`（`writeClipboard` 降级、`formatDateTime`、`formatBytes` 边界）、`useTheme.ts`（localStorage/系统监听）、`useMediaQuery.ts`、`App.tsx`（分页边界 effect、SSE 装配、路由 section 分发）。
- `useRouteState.test.ts:48-65` 两个用例在 node 环境下"直接 return / 只断言 not.toThrow"，实际未覆盖 `history.pushState/replaceState/popstate`；`useRouteState.ts` 的核心逻辑（规范化、页面尺寸白名单、`syncServerPage`）没有 DOM 级测试。
- `useDashboardEvents.hook.test.ts:73-76` 的 `afterEach` 没有断言"卸载时 `EventSource.close()` 被调用、计时器被清理"；`hook.test` 也未覆盖 800ms 去抖刷新、meta 补拉、`AbortController` 取消。
- `TaskCenterPage.test.tsx:44-80` 的 mock 任务 `progress: 100` 与真实契约（后端 0~1，见 `internal/jobs/manager.go:442`、`clampPercent` 的 `*100`）不一致，测试里所有进度都会 clamp 到 100%，无法发现 `clampPercent` 回归；也没有断言百分比文本。
- `jobErrors.test.ts` 用的聚合样例与后端真实输出（P2-6 的三种格式）不完全一致，属于"测了前端假设的格式，没测后端实际格式"。
- `api.test.ts` 未覆盖成功路径（JSON 解析、header 构造）、未覆盖 `signal` 透传、未覆盖 2xx 非 JSON（正是 P2-2 的缺陷点）。
- `GalleryPage.test.tsx:37-39` 对 `useDeferredValue` 的异步更新直接用同步断言，虽然当前通过，但依赖 React 内部刷新时机，脆弱；建议 `await waitFor`。
- 测试基础设施：`test/render.tsx` 只包 `TooltipProvider`，而页面测试各自手写 `QueryClientProvider` 包装（`TaskCenterPage.test.tsx:31-42`、`GalleryPage.test.tsx:53-58`、`CreateJobModal.test.tsx:61-68`），重复且容易漏 provider；建议提供统一的 `renderWithProviders`（含 QueryClient/Tooltip）。

---

## 5. 待确认 / 需 Lead 交叉验证

1. **`__APP_VERSION__` 注入**：`AppHeader.tsx:85` 直接引用全局 `__APP_VERSION__`，`vite-env.d.ts` 只是类型声明；若 `vite.config.ts` 的 `define` 缺失，运行时是 `ReferenceError` 并导致整个 Header/应用崩溃。归 task-7，请其确认 define 存在。
2. **`schedules`/`meta` 的 SSE 依赖**：`archive_schedule.updated` 由后台调度器在每次 `nextRunAt` 变化时发布（`internal/jobs/manager.go:282,297`），前端每条都 `invalidateQueries` 计划列表（`useDashboardEvents.ts:201-204`）；高频调度时的请求量需结合实际调度频率评估（task-2 可确认）。
3. **`/api/library/downloads` 体量**：P2-4 的性能结论依赖"10 万条"的实际 JSON 大小/解析耗时；建议用大库实测或由 task-4 确认响应 `Content-Length` 量级。
4. **`FailedTweet.payload`**：前端类型有（`api.ts:167`）、`FailedTweetView` 无（`models.go:190-204`）、列表接口 `GET /api/failed-tweets?page=` 返回 View。需确认是否有未合并的分支计划返回 payload，否则应删掉该字段。
5. **`JobFiles` 可选性**：`jobFilesResponse` 的 `downloads/failed` 带 `omitempty`（`internal/httpapi/server.go:477-480`），空时返回 `{}`，而前端 `JobFiles` 声明为必填数组（`api.ts:236-239`）。运行时用 `?? []`（`TaskCenterPage.tsx:711-712`）兜住，建议后端去掉 `omitempty` 或前端改可选类型。
6. **SSE 并发订阅上限**：后端 `maxEventSubscribers = 64`（`internal/jobs/events.go:31`），前端 StrictMode/HMR 每次挂载新建一个 `EventSource`；单标签页正常，多标签页 + 反复刷新是否可能打满上限（打满后前端 3s→30s 退避重连），需 task-2/task-4 联调确认。
7. **错误聚合格式契约**（P2-6）：建议 Lead 组织前端与 jobs 后端 owner 确认是否改为结构化字段，避免两侧正则/格式继续漂移。
8. **`parseAggregatedErrors` 正则 ReDoS**：`/(?:([^:：]+)[:：]\s*)?(.*?)\s*\(...\)$/` 对超长且含大量括号的 `job.error` 有潜在回溯开销，未实测；若后端可能写入 >10KB 错误文本，建议限长或改线性解析。

---

## 6. 设计上值得肯定之处

- **SSE 生命周期管理完整**：`useDashboardEvents.ts:254-346` 的 effect 清理覆盖 `EventSource.close()`、三个 timer、`AbortController.abort()`、`disposed` 守卫，未发现卸载后 setState / 重复订阅；对"非 200 导致 EventSource 永久 CLOSED"做了显式重建与 3s→30s 退避（`:243-315`），并区分了"自动重连"和"换实例重连"两种路径。
- **anti-flapping 设计到位**：`mergeNewerJobs`/`isStaleJobUpdate`（`:79-101`）用 `updatedAt` 防止迟到的分页响应覆盖新的 SSE 进度；终态事件后按需失效 library/jobFiles，事件带 meta 时直接落库统计，避免多余请求。
- **首屏数据水合**：`bootstrap.ts:39-89` + `main.tsx:27-35` 在首次 render 前完成主题/强调色与 query 缓存水合，并有字段校验与失败降级；`takeAppBootstrap` 会移除 script 节点避免重复水合。
- **组件级清理与可访问性细节**：`TaskCenterPage.tsx:551-556` 复制计时器卸载清理；`Table` 展开按钮带 `aria-expanded/aria-controls`、`Pagination` 带 `aria-current`、`AppSidebar` 用 `aria-current="page"`；`GalleryPage.tsx:58-67` 灯箱方向键会主动让渡给 `<video>`/输入框/contenteditable，测试覆盖了三种场景。
- **危险操作确认**：清空失败队列、删除计划、删除失败记录、删除单条均使用 `Popconfirm`；媒体库/任务表格只读且不做破坏性内联操作。
- **安全基线良好**：全范围内无 `dangerouslySetInnerHTML`/`href` 注入/`target=_blank`；下载文件统一走同源 `/api/library/...`（后端 `http.ServeFile` 支持 Range），外部预览图带 `no-referrer`；`copyToClipboard` 对明文 HTTP 下 `navigator.clipboard` 不存在做了 `execCommand` 降级（`CommonUI.tsx:248-273`）。
- **纯函数 + 单测**：`jobStatus.ts`、`jobErrors.ts`、`bootstrap.ts`、`useDashboardEvents` 的事件应用逻辑、`useRouteState` 的路径解析都是无副作用纯函数并有针对性测试，118 个用例全绿、`tsc --noEmit` 通过，整体可维护性不错。
- **前后端契约大体一致（已核对）**：7 个 `JobStatus`/6 个 `JobKind` 枚举一致；`pageSize` 白名单 [10,20,50,100] 与后端 clamp + bootstrap 白名单一致；`libraryDownloadsLimit=100000` 与 `storage.MaxLibraryDownloadsLimit` 一致；计划间隔 5~43200 与后端 `Min/MaxArchiveScheduleIntervalMinutes` 一致；`retryableStatuses` 与 `storage.RetryJob` 允许集合一致；`progress` 为 0~1 与 `clampPercent` 的 `*100` 一致。
