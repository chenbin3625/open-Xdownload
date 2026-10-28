# 代码审查 · 前端 UI 组件 / 设置页 / 样式 / 构建

> 审查范围：`apps/web/src/components/ui/*`（含全部测试）、`apps/web/src/components/settings/*`、`apps/web/src/index.css`、`apps/web/index.html`、`vite.config.ts`、`tsconfig*.json`、`package.json`、`src/test/*`。
> 未读（按分工）：`pages/`、`lib/`、`components/layout|modals|drawers|jobs|common`；未使用 git 历史/diff，未读 README/docs。
> 交叉对照（仅用于前后端契约）：`internal/config/config.go`、`internal/storage/storage.go`（UpdateConfig）、`internal/httpapi/server.go`（config/local-directories/auth/check 与路径校验）、`cmd/server/static.go`（CSP/静态服务）、`cmd/server/listen.go`。
> 验证命令：`npx tsc --noEmit -p tsconfig.json`（exit 0，无输出）；`npx vitest run src/components/ui`（7 文件 / 61 用例全绿）；`npx vite build`（exit 0，产物 hash 与仓库内 `dist`、`internal/webui/dist` 完全一致）。P0 的凭据错配结论另用 `/tmp` 内复制的 `internal/config/config.go` 做了可执行复现（见 P0-1）。

## 0. 阅读清单

| 文件 | 行数 | 已读完 |
| --- | --- | --- |
| apps/web/src/components/ui/Button.tsx | 110 | 是 |
| apps/web/src/components/ui/Card.tsx | 107 | 是 |
| apps/web/src/components/ui/Controls.tsx | 262 | 是 |
| apps/web/src/components/ui/Controls.test.tsx | 236 | 是 |
| apps/web/src/components/ui/Feedback.tsx | 176 | 是 |
| apps/web/src/components/ui/Input.tsx | 264 | 是 |
| apps/web/src/components/ui/Input.test.tsx | 133 | 是 |
| apps/web/src/components/ui/Overlay.tsx | 296 | 是 |
| apps/web/src/components/ui/Overlay.test.tsx | 117 | 是 |
| apps/web/src/components/ui/Pagination.tsx | 131 | 是 |
| apps/web/src/components/ui/Pagination.test.tsx | 92 | 是 |
| apps/web/src/components/ui/SearchSelect.tsx | 166 | 是 |
| apps/web/src/components/ui/Spinner.tsx | 59 | 是 |
| apps/web/src/components/ui/Table.tsx | 149 | 是 |
| apps/web/src/components/ui/Table.test.tsx | 95 | 是 |
| apps/web/src/components/ui/Tag.tsx | 145 | 是 |
| apps/web/src/components/ui/Toast.tsx | 147 | 是 |
| apps/web/src/components/ui/Toast.test.tsx | 94 | 是 |
| apps/web/src/components/ui/Tree.tsx | 205 | 是 |
| apps/web/src/components/ui/Tree.test.tsx | 123 | 是 |
| apps/web/src/components/ui/index.ts | 26 | 是 |
| apps/web/src/components/settings/ConfigForm.tsx | 330 | 是 |
| apps/web/src/components/settings/CookieSettings.tsx | 579 | 是 |
| apps/web/src/components/settings/DownloadSettings.tsx | 180 | 是 |
| apps/web/src/components/settings/LocalDirectoryPicker.tsx | 257 | 是 |
| apps/web/src/components/settings/StorageSettings.tsx | 25 | 是 |
| apps/web/src/index.css | 434 | 是 |
| apps/web/index.html | 16 | 是 |
| apps/web/vite.config.ts | 106 | 是 |
| apps/web/tsconfig.json | 20 | 是 |
| apps/web/tsconfig.node.json | 14 | 是 |
| apps/web/package.json | 36 | 是 |
| apps/web/src/test/setup.ts | 39 | 是 |
| apps/web/src/test/render.tsx | 11 | 是 |

合计 5180 行（`wc -l`），全部逐行读完。

## 1. P0 发现

### [P0-1] 前端丢弃后端下发的备用 Cookie 稳定 `id` 并以文本回传，删除行后会把「被删账号的凭据」写回数据库（凭据错配/丢失）

- 位置：
  - `apps/web/src/components/settings/CookieSettings.tsx:504-522`（`parseBackupCookieRowsFromJSON` 重建行时丢弃 `record.id`）
  - `apps/web/src/components/settings/CookieSettings.tsx:562-575`（`normalizeBackupCookieRows` 只输出 `auth_token=...; ct0=...` 文本，不带 `id`）
  - `apps/web/src/components/settings/CookieSettings.tsx:323-341`（`updateRow` 同样只写回文本）
- 证据（前端）：
```tsx
// CookieSettings.tsx:510-518 —— 后端下发的 id 被直接丢弃，重新生成 backup-cookie-N
return parsed.flatMap((item) => {
  ...
  const authToken = firstString(record.authToken, record.auth_token);
  const csrfToken = firstString(record.csrfToken, record.ct0);
  return authToken || csrfToken ? [{ ...emptyBackupCookieRow(), authToken, csrfToken }] : [];
});
```
```tsx
// CookieSettings.tsx:562-574 —— 提交格式永远是无 id 的文本行
return rows
  .map((row) => ({ authToken: row.authToken.trim(), csrfToken: row.csrfToken.trim() }))
  .filter((row) => Boolean(row.authToken || row.csrfToken))
  .map((row) => [row.authToken ? `auth_token=${row.authToken}` : "", row.csrfToken ? `ct0=${row.csrfToken}` : ""]...)
  .join("\n");
```
- 证据（后端契约，明确要求保留 id）：
```go
// internal/config/config.go:165-188
// RedactAdditionalCookies returns a JSON redacted version of additional cookies,
// preserving a stable id for each configured pair so row deletion cannot shift credentials.
...
redacted = append(redacted, AuthCookie{ID: cookie.ID, AuthToken: SecretPlaceholder, CSRFToken: SecretPlaceholder})
```
```go
// internal/config/config.go:204-217 —— 优先按 id 还原；id 缺失时按位置兜底
storedByID := make(map[string]AuthCookie, len(storedPairs))
...
storedPair, hasStored := storedByID[sub.ID]
if !hasStored && i < len(storedPairs) && sub.ID == fmt.Sprintf("cookie-%d", i) { ... }
```
```go
// internal/config/config_test.go:136-143 —— 后端单测假设前端按 id 提交
// Case 3: User removes the first row; the second row keeps its own credentials by ID.
editedPairs := []AuthCookie{redactedPairs[1]}
...
if pairs := ParseCookiePairs(restoredEdited); len(pairs) != 1 || pairs[0].AuthToken != "tok2" { ... }
```
- 可执行复现（把 `internal/config/config.go` 复制到 /tmp 后 `go run`，两个场景输入相同、只差是否保留 id）：
```
stored              = [{"id":"cookie-0","authToken":"AUTH_A",...},{"id":"cookie-1","authToken":"AUTH_B",...}]
# 用户在 UI 删除第 1 行（AUTH_A），只保留第 2 行：
frontend-submitted text  -> [{"id":"cookie-0","authToken":"AUTH_A","csrfToken":"CT0_A"}]
id-preserving submission -> [{"id":"cookie-1","authToken":"AUTH_B","csrfToken":"CT0_B"}]
```
- 影响：账号池 ≥2 条时，删除任意**非末行**并保存：被删除账号的真实 Cookie 会被当成保留行的 Cookie 重新落库，用户想保留的账号被真删。三类后果同时存在：
  1. 数据损坏：凭据与账号张冠李戴（最坏情况是 A 账号的 Cookie 被写进 B 行，后续请求用错账号）；
  2. 用户可见性极差：UI 全脱敏为 `********`，保存成功后返回的也是 `********`，用户无法从界面发现串号；
  3. 编辑/删除组合操作都会触发（`normalizeBackupCookieRows` 对所有提交都输出文本）。后端已给出解法（稳定 id），是前端单方面破坏了契约。
- 建议：
  1. `BackupCookieRow` 增加 `id`，`parseBackupCookieRowsFromJSON` 保留 `record.id`（新增行用本地唯一 id 如 `local-xxxx`）；
  2. `normalizeBackupCookieRows` 改为输出 JSON 数组 `[{id, authToken, csrfToken}]`（未改动字段写 `********` 或空串，后端 `RestoreAdditionalCookies` 两者都按“还原”处理），与后端 `AuthCookie` 结构对齐；
  3. 补一条“删除中间行/末行”的单元测试（纯函数级即可），并在前端直接把该契约写成注释，避免再次回退为文本格式。

## 2. P1 发现

### [P1-1] 无法从设置页清空已保存的主账号 `auth_token` / `ct0`：后端把空串语义定义为「保持原值」

- 位置：`apps/web/src/components/settings/CookieSettings.tsx:112-140`（唯一清除方式是把输入框删空）；`apps/web/src/components/settings/ConfigForm.tsx:188`（`mutation.mutate(draft)` 原样提交）
- 证据（前端只有“删空”这一种交互）：
```tsx
// CookieSettings.tsx:112-117
value={draft.authToken ?? ""}
onChange={(event) => onChange((current) => ({ ...current, authToken: event.target.value }))}
placeholder="输入 auth_token"
```
```go
// internal/storage/storage.go:841-846 —— 空串/占位符都被解释为“沿用当前值”
if cfg.AuthToken == "" || cfg.AuthToken == config.SecretPlaceholder {
    cfg.AuthToken = current.AuthToken
}
if cfg.CSRFToken == "" || cfg.CSRFToken == config.SecretPlaceholder {
    cfg.CSRFToken = current.CSRFToken
}
```
- 影响：Cookie 等同于账号凭据（代码注释里也这么写）。用户换号、吊销泄露 Cookie、或想停用主账号时**无法通过 UI 移除**：删空保存 → 被后端还原 → `GET /api/config` 又回显 `********`，`CookieTokenStatus` 继续显示“有效”，容易被误认为“已清空”。目前没有任何“清除 Cookie”入口，也没有 DELETE 语义。
- 建议（任选，需与后端 reviewer 对齐）：
  - 前端提供显式「清除 Cookie」按钮，提交一个不会与占位符混淆的哨兵或专用字段（例如 `clearAuthToken: true`），后端在 `UpdateConfig` 分支处理；
  - 或者把“清空字段 = 保留”的语义在 UI 上明确写成提示 + 提供独立清除动作；
  - 无论哪种，都需要前端测试覆盖“清空后保存 → 再次 GET 为空”。

### [P1-2] 暗色 + 「钛金」强调色下，所有 primary 按钮变成白底白字（文字不可见）

- 位置：`apps/web/src/index.css:193-206`（钛金暗色把 `--color-brand-500` 设为 `#f4f4f5`、`--color-brand-600` 设为 `#ffffff`）；`apps/web/src/components/ui/Button.tsx:25`（primary 固定 `text-white` + 从 brand-500→brand-600 渐变）
- 证据：
```css
/* index.css:193-206 */
.dark[data-accent="titanium"],
.dark [data-accent="titanium"] {
  --color-brand-500: #f4f4f5;
  --color-brand-600: #ffffff;
  ...
}
```
```tsx
// Button.tsx:24-25
primary:
  "bg-gradient-to-b from-brand-500 to-brand-600 text-white border border-brand-400/25 ...",
```
- 影响：主题是设置页的一等功能（`THEME_ACCENTS`，`ConfigForm.tsx:273-301` 可选择），用户一旦选择“深色 + 钛金”，所有 primary 按钮（保存配置、检测 Cookie、选择此目录、创建目录）的文字与背景几乎同色，无法阅读/无法确认可点。这是**选择即必现**的视觉回归，不是低概率边界。相关近似问题：`text-brand-500`/`text-brand-600` 被当作“强调文字色”广泛使用，在浅色 + amber 主题下 `#f59e0b` on white 对比度约 2.2:1（见 P2-12）。
- 建议：primary 前景色应由主题成对提供的“对比色 token”驱动（例如 `--color-brand-contrast`，在 5 套 accent × 明暗里都定义为可读色），不要在组件里硬编码 `text-white`；给 `THEME_ACCENTS` 增加暗色 smoke 检查或用 Storybook/截图测试固定 5×2 组合。

### [P1-3] 设置页关键表单控件普遍缺少 label 关联/可访问名称（`Field` 从未传 `htmlFor`，`Select` 未传 `ariaLabel`）

- 位置：
  - `apps/web/src/components/settings/DownloadSettings.tsx:59-70`（网络代理 `Input` 无 `id`/`aria-label`）
  - `apps/web/src/components/settings/DownloadSettings.tsx:74-86`（最大文件名长度 `NumberInput` 无 `id`/`aria-label`）
  - `apps/web/src/components/settings/DownloadSettings.tsx:90-104`（文件命名策略 `Select` 未传 `ariaLabel`，且不支持 `id`）
  - `apps/web/src/components/settings/LocalDirectoryPicker.tsx:127-133`（下载目录 `Input` 无 `aria-label`）
  - `apps/web/src/components/settings/ConfigForm.tsx:245,271`（用 `<label>` 包裹“明暗主题模式/主题强调色”，但下方是按钮组，label 未关联任何控件）
- 证据（组件自身已经写明正确用法，但调用方没照做）：
```tsx
// Input.tsx:229-233
// children 是一个受控表单控件时传 htmlFor，label 与控件显式关联；
// 若不传就渲染成普通 span —— 悬空的 htmlFor 比没有 label 更糟
```
```tsx
// Input.tsx:250-260 —— 没传 htmlFor 时只能渲染 span
{htmlFor ? <label htmlFor={htmlFor} ...>{label}{tooltip}</label>
         : <span className={labelClass}>{label}{tooltip}</span>}
```
```tsx
// DownloadSettings.tsx:74-86 —— 没有 htmlFor，Input/NumberInput 也没有 aria-label
<Field label="最大文件名长度" tooltip={...}>
  <NumberInput min={16} max={240} value={draft.maxFilenameLength} onChange={...} />
</Field>
```
```tsx
// ConfigForm.tsx:245-266 —— label 与下方的 button 组没有任何关联
<label className="text-xs font-semibold text-fg">明暗主题模式</label>
<div className="mt-2 flex flex-wrap gap-2">
  <button type="button" onClick={() => setPreference(mode.id as any)} ...>
```
- 影响：读屏用户听到的是“编辑框/数字编辑框”而没有名称，无法区分“网络代理”“最大文件名长度”“下载目录”等关键项；主题模式按钮既无 `aria-pressed` 也无 `radiogroup` 语义，无法播报当前选中。WCAG 1.3.1 / 2.5.3 / 4.1.2 不通过。这是**整个设置页的系统性问题**，不是单点。
- 建议：
  - 给 `Field` 增加受控约定（复制 children 注入 `id`/`aria-describedby`，或要求调用方传 `htmlFor` + 控件 `id`）；
  - `Select`/`Switch`/`Checkbox` 增加 `id`、`disabled` 透传（目前 `Select` 只支持 `ariaLabel`，`Checkbox` 无 `disabled`）；
  - 主题模式改用已有 `Segmented`（`Controls.tsx:171-216`）并配 `ariaLabel`，选中态交给 `data-state`；
  - `TipIcon`（`CookieSettings.tsx:48-56`、`DownloadSettings.tsx:35-43`）是 `span` + Tooltip，键盘不可达，应改为可聚焦 `button`（`aria-label`）。

## 3. P2 发现

### [P2-1] `BackupCookieInputs` 在 `setState` updater 内调用父组件 `onChange`（渲染期更新另一组件）

- 位置：`apps/web/src/components/settings/CookieSettings.tsx:323-341`、`343-349`、`351-358`
- 证据：
```tsx
function updateRow(index: number, field: keyof BackupCookieRow, nextValue: string) {
  setRows((currentRows) => {
    const nextRows = currentRows.map(...);
    onChange(normalizeBackupCookieRows(nextRows)); // ← updater 内部的副作用
    return nextRows;
  });
}
```
- 影响：updater 函数会在渲染阶段执行，在其中同步 `setDraft`（父组件 `ConfigForm` 的状态）属于“渲染期更新另一个组件”。开发环境会打 React 警告；`React.StrictMode`（`src/main.tsx` 已启用）下 updater 会被双调用以检测纯函数性，导致 `onChange` 每次编辑被调用两次、多一轮渲染。生产环境虽不崩，但这是纯函数契约破坏，容易在后续重构中变成真实 bug。
- 建议：先算出 `nextRows`（纯函数），在事件处理器里 `setRows(nextRows); onChange(normalizeBackupCookieRows(nextRows));`，不要在 updater 里做副作用。`addRow` / `removeRow` 同样改法。

### [P2-2] ConfigForm 的草稿同步存在“卡死”分支：dirty 后刷新无效；`pendingSavedConfigKey` 失配后永久不再回填

- 位置：`apps/web/src/components/settings/ConfigForm.tsx:43-53`、`55-68`、`84-96`、`319-321`
- 证据：
```tsx
// :55-68
useEffect(() => {
  const normalized = normalizeConfig(config);
  const configKey = configSyncKey(normalized);
  if (pendingSavedConfigKey.current) {
    if (pendingSavedConfigKey.current === configKey) { pendingSavedConfigKey.current = ""; setDraft(normalized); }
    return;                                  // ← 不匹配就 return，pending 永不清空
  }
  if (!draftDirty) setDraft(normalized);     // ← dirty 时外部配置变化被完全忽略
}, [config, draftDirty]);
```
- 触发条件与影响：
  1. **有未保存修改时点「重新加载配置」**：`onRefresh` → refetch → `config` 变化，但 `draftDirty` 为 true → 不更新草稿。按钮看起来“没反应”，实际是设计如此却没有提示，用户以为刷新失败。
  2. **env 覆盖部署**（`OPEN_XDOWNLOAD_AUTH_TOKEN` / `PROXY_URL` 等，`config.go:22-37`）：`GET /api/config` 返回经 `ApplyEnvOverrides` 再 `Redacted` 的值；`PUT` 的响应是库内值（`RevertEnvOnlyEcho` 保证不写 env 值，`storage.go:850-853`）。两者 `configSyncKey` 不相等 → `pendingSavedConfigKey` 一直保留 → 之后任何 `GET` 变化（包括用户点刷新）都会被 `return` 吞掉，草稿停在 PUT 响应上；同时 UI 会显示库内（空）Cookie 而服务端实际在用 env Cookie。
  3. refetch 失败（网络/500）同样触发 2 的卡死。
- 建议：把“正在等待保存结果”建模为显式状态而不是 ref 字符串比对；匹配失败时加超时/兜底清空；dirty 时给用户「放弃本地修改并重新加载」的确认流；env 覆盖字段在 UI 上标注“由环境变量提供，保存不会写入”。

### [P2-3] 设置页没有“未保存修改”离开拦截，草稿会静默丢失

- 位置：`apps/web/src/components/settings/ConfigForm.tsx:44-45`（`draft`/`draftDirty` 仅用于内部，无 `beforeunload`/路由拦截）；`ConfigForm.tsx:157-193`（保存是显式按钮）
- 证据：全文件没有 `beforeunload`、`useBlocker`、`history` 相关处理；`draftDirty` 只参与 `:65` 的回填判断。
- 影响：用户在设置页改完 Cookie/代理后切到别的 tab（`useRouteState` 路由）或刷新页面，改动全部丢失且无提示；考虑到 Cookie 是长字符串，误丢成本高。
- 建议：`draftDirty` 时注册 `beforeunload` 提示，并在应用内路由切换处拦截（需与页面 reviewer 协作）；顶栏显示“有未保存修改”标记。

### [P2-4] 备用 Cookie：把 `auth_token=xxx; ct0=yyy` 粘进单行输入框会污染值（提示文案正好教用户这样粘）

- 位置：`apps/web/src/components/settings/CookieSettings.tsx:36-46`（提示“支持每行一个账号，格式为 auth_token=xxx; ct0=yyy”）；`CookieSettings.tsx:398-429`（每行是单行 `PasswordInput`）；`CookieSettings.tsx:323-341` + `562-575`（粘入后经 normalize/reparse）
- 证据：
```tsx
// tips.backupCookie:42
<div>支持每行一个账号，格式为 auth_token=xxx; ct0=yyy。</div>
```
```tsx
// 用户把 "auth_token=abc; ct0=def" 粘进 auth_token 输入框后：
onChange(normalizeBackupCookieRows(nextRows))
// → "auth_token=auth_token=abc; ct0=def"
// 再次 parse（useEffect:315-321）→ token 拆分后 authToken 变成 "auth_token=abc"
```
- 影响：仓库里**没有**多行 textarea 入口（`ConfigForm` 只渲染 `CookieSettingsFields`），`parseBackupCookieRows` 的多行解析只对历史 DB 值/环境变量生效；按提示粘贴的常见操作会得到错误 token，且保存后才会在检测时暴露，排查成本高。
- 建议：提供专用“批量粘贴”textarea（一个账号一行/一列），或至少让单行输入在粘贴含 `auth_token=`/`ct0=` 前缀时自动解析；同时纠正提示文案，明确“每行账号请使用批量粘贴框”。

### [P2-5] LocalDirectoryPicker 的「刷新目录」按钮不会刷新树内容

- 位置：`apps/web/src/components/settings/LocalDirectoryPicker.tsx:70-83`、`178-187`
- 证据：
```tsx
const listingRootPath = listing.data?.path;
...
useEffect(() => {
  const current = listingRef.current;
  if (!current || listingRootPath === undefined) return;
  const rootNode = listingToDirectoryTreeRoot(current);
  setTreeData([rootNode]);                       // ← 只有 path 变化才会重建树
  ...
}, [listingRootPath]);                           // ← 依赖是字符串，refetch 同路径时不变
```
```tsx
<Button ... onClick={() => listing.refetch()} aria-label="刷新目录" />
```
- 影响：点刷新 → `refetch` 成功 → `listing.data` 换了新对象，但 `listing.data.path` 仍是同一字符串 → effect 不执行 → `treeData` 保持旧内容。按钮实际只刷新了路径栏与「上级目录」的可用性，磁盘上新建/删除的子目录不会出现，属于“看起来能用其实没用”的功能缺陷。
- 建议：effect 依赖改为完整的 `listing.data`（或 `dataUpdatedAt`），并用 `useMemo` 处理；或者直接在 `refetch().then` 里重建树。

### [P2-6] LocalDirectoryPicker 导航失败后保留旧树、工具栏显示失败路径；允许根边界外的「上级目录」仍可点 → 400

- 位置：`apps/web/src/components/settings/LocalDirectoryPicker.tsx:63`、`111-115`、`162-215`
- 证据：
```tsx
const resolvedPath = listing.data?.path ?? rootPath;   // 失败时展示失败路径
...
{listing.isLoading && treeData.length === 0 ? <Spinner/> :
 treeData.length > 0 ? <Tree .../> : <AppEmpty .../>}  // 失败时仍渲染旧 treeData
```
```tsx
<Button ... disabled={!listing.data?.parent || listing.isLoading}
        onClick={() => listing.data?.parent && setRootPath(listing.data.parent)}>上级目录</Button>
```
- 后端边界（`internal/httpapi/server.go:1235-1246`）：允许根 = 用户家目录 + 当前下载目录；`localDirectoryPath` 对根外路径直接 400（`:1306-1330`，错误文案在 `:1319`）。在 `~`（家目录）作为根时 `parent` 是 `/Users`，按钮可点 → 400，`Alert` 只提示“读取目录失败”，树仍是旧目录，用户点「选择此目录」会选中旧路径（与工具栏显示不一致）。
- 影响：可恢复但会误导；路径输入错误（不存在/无权限/越界）没有内联错误，只有一次性 Alert + 保留的旧树。
- 建议：请求失败时清空 `treeData` 并禁用选择；`上级目录` 除了 `parent` 非空还应判断是否在允许根内（后端可在 listing 里返回 `canGoUp`/`allowedRoots`）；本地对输入做绝对路径/存在性前置校验。

### [P2-7] 把本地选择的路径直接当下载目录提交，缺少与后端一致的校验（家目录本身/非目录/越界只在保存时才报错）

- 位置：`apps/web/src/components/settings/LocalDirectoryPicker.tsx:151-157`（选择此目录直接 `onSelect(selectedPath.trim())`）；`apps/web/src/components/settings/StorageSettings.tsx:19-22`
- 后端约束（`internal/httpapi/server.go:1334-1353`）：下载目录不能是文件系统根或家目录本身（`:1344-1347`），且必须位于允许根内；`validateDownloadDir` 只在 `PUT /api/config` 时执行（`server.go:128-139`）。
- 影响：用户可以在树里选到家目录本身（列表根就是家目录时最顺手一键）→ 保存时 400，错误只以 toast 出现（`notifyError("保存失败")`），没有字段级错误提示；越界/非目录同样。体验上像“保存按钮坏了”。
- 建议：选择前在本地对齐后端规则（禁止家目录本身，提示“请选择家目录下的子目录”），或在 UI 上给出保存前的行内校验。

### [P2-8] `NumberInput` 允许小数与中间态，直接保存会让后端 JSON 解码失败（400），且错误不可读

- 位置：`apps/web/src/components/ui/Input.tsx:196-207`；使用点 `apps/web/src/components/settings/DownloadSettings.tsx:78-85`
- 证据：
```tsx
// Input.tsx:196-207
onChange={(event) => {
  const raw = event.target.value;
  if (raw === "") { onChange(null); return; }
  const parsed = Number(raw);
  onChange(Number.isNaN(parsed) ? null : parsed);   // 允许 120.5
}}
onBlur={() => { if (value !== null) onChange(clamp(value)); }}  // clamp 不取整
```
```go
// internal/config/config.go:88 —— 后端是 int
MaxFilenameLength  int `json:"maxFilenameLength" db:"max_filename_length"`
```
- 影响：输入 `120.5` 后焦点仍在输入框时点保存（或被脚本/回车触发保存）→ 提交浮点数 → Go `json.Unmarshal` 报 `cannot unmarshal number 120.5 into Go struct field ... int` → 400，整个保存失败；错误信息是原始 JSON 错误，用户不知道是哪一项。同理 `maxFilenameLength` 在 16 以下/240 以上只有 blur 才收敛，保存路径依赖 blur 先后顺序。
- 建议：`NumberInput` 支持 `integer`/`precision` 选项并对 `Number(raw)` 取整；提交前在 `ConfigForm` 做一次集中校验（对齐后端 16–240），错误在对应 `Field` 上展示。

### [P2-9] Modal 的 `title` 可选但 Radix 要求 `Dialog.Title`；无 title 时既无标题也无关闭按钮，Drawer 无 title 时也没有关闭按钮

- 位置：`apps/web/src/components/ui/Overlay.tsx:40-54`（`title &&` 才渲染标题栏和关闭按钮）；`apps/web/src/components/ui/Overlay.tsx:143-152`（Drawer 只在 `title` 存在时渲染 header，否则只有 `sr-only` Title，没有关闭按钮）
- 证据：
```tsx
// Overlay.tsx:40-54
{title && (
  <div ...>
    <Dialog.Title ...>{title}</Dialog.Title>
    ...
    <Dialog.Close asChild><Button ... aria-label="关闭" /></Dialog.Close>
  </div>
)}
```
- 实际调用：移动端导航抽屉 `src/App.tsx:193-200` 只传 `side/width/open/onClose`，不传 `title` → 抽屉没有可见关闭按钮，只能点遮罩或按 ESC（触屏上等于“只能点遮罩”）。同时 `Dialog.Content` 内没有 `Dialog.Description` 且没有 `aria-describedby={undefined}`，Radix 开发环境会告警（Drawer/MediaModal 同样）。
- 建议：无 title 时仍渲染一个仅含 `Dialog.Close` 的 header（或圆角悬浮关闭按钮）；为 `Dialog.Content` 补 `aria-describedby={description ? undefined : undefined}` 的显式处理，避免每次打开刷警告；`Drawer` 的 `sr-only` 标题不要硬编码“导航菜单”（见 P3）。

### [P2-10] Toast：错误提示用 `aria-live="polite"`，不支持键盘/焦点暂停，自动消失可能读不完

- 位置：`apps/web/src/components/ui/Toast.tsx:79-87`（仅 hover 暂停）、`96-97`、`139-143`（统一 polite）
- 证据：
```tsx
onMouseEnter={() => setPaused(true)}
onMouseLeave={() => setPaused(false)}   // 键盘用户无法暂停
...
<div aria-live="polite" aria-atomic="false" ...>   // error 也走 polite
```
- 影响：4.5s 自动消失期间读屏可能尚未播报完成；error（如“保存失败”）不应用 polite 排队；键盘用户聚焦在 Toast 的关闭按钮上时计时不会暂停。
- 建议：error 单独用 `role="alert"`/`aria-live="assertive"`；`onFocusCapture/onBlurCapture` 与 hover 一起决定 `paused`；给容器加 `aria-relevant`。

### [P2-11] SearchSelect 的 combobox 语义分裂：`role="combobox"` 在触发器上，实际焦点在弹层里的普通 `input`

- 位置：`apps/web/src/components/ui/SearchSelect.tsx:74-91`（Trigger 上 `role="combobox"`/`aria-expanded`/`aria-controls`）、`108-132`（弹层 input 只有 `aria-controls`/`aria-activedescendant`，无 `role="combobox"`/`aria-expanded`/`aria-label`）
- 证据：
```tsx
<Popover.Trigger role="combobox" aria-expanded={open} aria-haspopup="listbox"
                 aria-controls={open ? listId : undefined} aria-label={ariaLabel} ...>
...
<input value={keyword} aria-controls={listId}
       aria-activedescendant={filtered[activeIndex] ? `${listId}-${activeIndex}` : undefined} ... />
```
- 影响：`onOpenAutoFocus` 把焦点放到弹层 input，此时获得焦点的是没有 combobox role 的文本框；读屏会把 `aria-expanded`/`aria-controls` 关联到已经失焦的 Trigger 上，`aria-activedescendant` 指向的 listbox 与当前焦点元素之间缺少 combobox 语义链。这是 ARIA combobox 模式的典型错误实现。测试只断言了 `aria-activedescendant` 指向真实元素（`Controls.test.tsx:127-131`），没有覆盖语义链。
- 建议：把 `role="combobox"` 移到弹层 input 上（Trigger 改为普通 button + `aria-haspopup="listbox"`），或采用 ARIA 1.2 的“select-only combobox + listbox popup”模式统一到一处；补 `aria-autocomplete="list"`、input 的 `aria-label`（可用 `searchPlaceholder` 兜底）；同时补 Escape 关闭并把焦点还给触发器的测试。

### [P2-12] 对比度不足：`--fg-subtle` 明暗两套都低于 AA，却大量用于 11px 辅助文字；强调色系也存在低对比组合

- 位置：`apps/web/src/index.css:144-149`（light `--fg-subtle:#94a3b8`）、`166-176`（dark `--fg-subtle:#5c5c66`）、`270-273`（`::selection` 用 brand-500 + 白字）；使用点如 `Feedback.tsx:99`、`Card.tsx:101`、`Tag.tsx` 等
- 证据/计算：
  - light：`#94a3b8` on `#ffffff` ≈ **2.57:1**（AA 需 4.5:1）；
  - dark：`#5c5c66` on `#111114` ≈ **2.85:1**；
  - `::selection` 背景 `var(--color-brand-500)` + `#fff`：amber 主题 `#f59e0b` on white ≈ 2.2:1。
- 影响：`text-fg-subtle` 用于占位符、时间戳、说明文字（含 `text-[11px]`），是弱视用户的高频信息；`::selection` 在 amber/浅色主题下选中文字难辨。
- 建议：把 `--fg-subtle` 提高到至少 4.5:1（light 约 `#64748b` 已由 `--fg-muted` 占据，可再深一档；dark 提到约 `#8e8e98`），`::selection` 改用 `--fg`/brand-700 组合；把 accent 对比度纳入 token 设计（见 P1-2）。

### [P2-13] 构建配置：分包/预加载策略与源码结构不一致（页面全部静态 import），相关配置是死代码；缺少 target/sourcemap；preview 无 API 代理；dev 暴露到 0.0.0.0

- 位置：`apps/web/vite.config.ts:69-76`、`77-90`、`93-98`；`apps/web/package.json:6-11`；`src/App.tsx:34-37`
- 证据：
```ts
// vite.config.ts:69-76 —— 假定存在 SettingsPage/player 页面 chunk
modulePreload: { polyfill: false, resolveDependencies(_filename, deps) {
  if (!_filename.includes("index")) return deps;
  return deps.filter((dep) => !/(?:SettingsPage|player)/.test(dep));
}},
```
```ts
// App.tsx:34-37 —— 页面全部静态 import，不存在按需加载的页面 chunk
import { TaskCenterPage } from "./pages/TaskCenterPage";
import { SchedulesPage } from "./pages/SchedulesPage";
import { GalleryPage } from "./pages/GalleryPage";
import { SettingsPage } from "./pages/SettingsPage";
```
- 实测 `npx vite build`（exit 0，191ms）产物只有 6 个 chunk：`index / react / radix / query / icons / vendor`（+ rolldown-runtime），**没有 SettingsPage / player chunk**；`manualChunks` 里 `player` 正则依赖的 `react-player` 在 `src/` 中 0 次引用（`grep -rn "react-player" src` 为空，但 `package.json:19` 保留依赖）。因此 `modulePreload.resolveDependencies` 的过滤永远不会命中，`_filename.includes("index")` 也会误匹配任意路径含 `index` 的模块。
- 其余：无 `build.target`/`sourcemap`（生产报错无法定位）；`preview` 没有 `/api` 代理，`npm run preview` 只能看到空壳（页面请求 404）；`dev`/`preview` 都是 `--host 0.0.0.0`，而该服务无鉴权（`cmd/server/static.go:99-100` 注释也说明“该服务无内置鉴权，被 iframe 嵌套没有正当用途”），局域网可访问开发服务器与代理后的 API。
- 建议：要么给页面加 `React.lazy` 让配置生效，要么删掉无效的 `modulePreload` 过滤与 player 规则并注释真实策略；补 `build.target`（产品不追求旧浏览器可显式写明）、按需开 sourcemap；`preview` 加同样 proxy；dev 默认仅 `127.0.0.1`，需要局域网时用 env 打开。

### [P2-14] 测试覆盖缺口：`components/settings/*` 0 测试；为测试而导出的纯函数无测试；多个 UI 组件未覆盖

- 位置：`apps/web/src/components/settings/`（仅 5 个 .tsx，无 .test）；`CookieSettings.tsx:451-579`、`LocalDirectoryPicker.tsx:222-257`、`ConfigForm.tsx:309-330` 中大量 `export` 的纯函数；`vite.config.ts:99-105`
- 证据：
  - `parseBackupCookieRows` / `parseBackupCookieRowsFromJSON` / `normalizeBackupCookieRows` / `splitCookieKeyValue` / `setBackupCookieValue` / `isRedactedBackupCookieRow` 全部 `export` 却无测试；
  - `listingToDirectoryTreeRoot` / `directoryEntryToTreeNode` / `updateDirectoryTreeChildren` 同样无测试；
  - `normalizeConfig` / `configSyncKey` / `cookieCheckKey` 无测试；
  - UI 侧未覆盖：`Select`（Radix Select 一条测试都没有）、`Tooltip`、`Alert/Skeleton/Progress/Empty/Descriptions/Divider`、`Tag/Avatar/CountBadge/StatusDot`、`Card/SectionCard`、`Toast` 的 hover 暂停/卸载清理、`Drawer` 内容与关闭按钮、`Table` 重复 rowKey、`Tree` 的 `loadChildren` 拒绝。
- 影响：P0-1 正好落在“无测试的纯函数”里；设置页是安全凭据的主要入口，回归成本最高却没有一行测试。P0-1 的修复（保留 id）本可以用 3 行纯函数测试锁死。
- 建议：至少为 Cookie 解析/序列化（含 redacted id 往返）、`normalizeConfig`、目录树纯函数补单测；再把 P0-1 的删除场景写成回归测试。

### [P2-15] 备用 Cookie 的脱敏聚合状态会重复显示到每一行；`clients` 与行号按位置对齐较脆弱

- 位置：`apps/web/src/components/settings/CookieSettings.tsx:378-390`、`198-287`
- 证据：
```tsx
const aggregateClients = isRedactedBackupCookieRow(row) && clients.length > 1 ? clients : undefined;
const client = aggregateClients ? undefined : clients[index];   // ← 行序 == clients 序的位置假设
```
- 影响：2 条以上脱敏行时每行都显示同一份“n/m 可用”聚合状态（同一信息重复 N 次）；非脱敏行用行下标取 `clients[index]`，一旦后端合并/去重（`xclient.NewPool` 会按 token 去重）导致 clients 数量与行数错位，就会把 A 账号的状态标到 B 行。与 P0-1 同源：缺少稳定 id。
- 建议：状态按 id 关联（与 P0-1 一起修）；脱敏行不显示逐行状态，改为区块级汇总。

### [P2-16] 正则/文件格式的隐式契约分散在前后端两处，且前端解析丢掉了 JSON 里的 `id` 之外还未处理“整体 ********”退化值

- 位置：`apps/web/src/components/settings/CookieSettings.tsx:458-502`（两种格式解析）、`463-465`（整体 `********` 特判）
- 证据：
```tsx
if (trimmed === "********") {
  return [{ ...emptyBackupCookieRow(), authToken: "********", csrfToken: "********" }];
}
```
- 影响：当后端库里存着“非空但解析不出任何 cookie 对”的历史值时，`RedactAdditionalCookies` 会返回整体 `"********"`（`config.go:176-178`）；前端把它展开成一行 `********`，用户再保存时提交 `auth_token=********; ct0=********`，后端 `RestoreAdditionalCookies` 在 stored 解析为空时不会还原，会把**字面量 `********` 当成真实 Cookie 落库**（已核对 `config.go:200-228` 的分支：`hasStored=false` 时原样保留）。这是与 P0-1 同族的“占位符泄漏为数据”风险，触发概率低但后果是凭据被 `********` 覆盖。
- 建议：前端遇到整体 `********` 时不要把占位符展开成可编辑行（显示“已保存（无法解析）”，保存时原样回传 `********`）；后端在 restore 后若仍有值为 `SecretPlaceholder` 应拒绝落库并报错。

## 4. P3 与优化建议

### UI 组件
1. `Button.tsx:26-28` `variant="default"` 与 `"secondary"` 完全同义（都指向 `defaultStyle`），`variant="ghost"` 与 `"text"` 也同义；建议删掉冗余语义或让 default 有独立视觉。`Button.tsx:88-90` asChild 分支把 `type` 留在 `rest` 里会透传给 `<a>`（`Button asChild type="submit"` 会产生 `<a type>`），建议从 rest 中剔除。
2. `Button.tsx:76-81` `loading` 替换图标、`disabled`/`aria-busy` 处理是好的；但 `asChild + loading` 仅 `aria-disabled` 而不阻止点击/导航，`Controls.test.tsx:226-235` 也只断言属性不测行为。建议 asChild 分支注入 `onClick` 拦截（或文档明确“asChild 不阻止交互”）。
3. `Card.tsx:55-62` `actions` 用数组下标做 key（`key={index}`），动态度低但仍是反模式；`Card.tsx:50` `children !== undefined` 使 `<Card>{null}</Card>` 也渲染出一个空 padding 区；`Card.tsx:100` `SectionCard` 固定 `<h3>`，在页面已有 `<h1>`（ConfigForm）时层级可用但在更深的卡片里会跳级。
4. `Controls.tsx:205` `Segmented` 内部用 `rounded-[0.375rem]` 而外层用 `rounded-control`（0.625rem），两套圆角来源不一致；`Checkbox`（`:126-162`）没有 `disabled`、不支持 `aria-label`；`Select`/`SearchSelect` 都没有 `disabled`/`id`；`Tabs`（`:224-262`）没有 `ariaLabel`，`RadixTabs.List` 无名称。
5. `Controls.tsx:36` `RadixSelect.Root value=""` 依赖 Radix 把空串视为 placeholder（实测内置 `data-placeholder` 生效，`:42` 的 `data-[placeholder]:text-fg-subtle` 有效）；但 `options` 为空时不渲染任何内容，没有“无数据”占位。
6. `Feedback.tsx:122` `Progress` 的 `Math.min(100, Math.max(0, percent))` 对 `NaN` 会得到 `NaN` → `width: NaN%`；建议 `Number.isFinite` 兜底。`Feedback.tsx:100` `Empty` 用 `<p>` 装 `ReactNode`，调用方传块级节点会产生非法嵌套（`Table.emptyText` 就是 `ReactNode`）。`Feedback.tsx:176` 重复导出 `Tone`（`index.ts:22` 已从 Tag 导出），可删。
7. `Spinner.tsx:11` 任意值动画 `animate-[spin_0.7s_linear_infinite]` 依赖 `@keyframes spin` 存在；当前能工作是因为别处有字面量 `animate-spin`（`ConfigForm.tsx:170` 等）让 Tailwind 生成 keyframes（已在构建产物中确认 `@keyframes spin` 存在），一旦这些调用点被删除，Spinner 会**静默停止转动**。建议改用 `--animate-spin: spin 0.7s linear infinite` 语义 token 或显式定义 keyframes。
8. `Input.tsx:37-42` 受控清空用“原生 setter + dispatch input 事件”绕过 React，工作但脆弱（React 未来对 value tracker 的改动会破坏它），且 `allowClear` 只对 `type value === "string"` 生效，`defaultValue`/非受控输入永远没有清除按钮。`Input.tsx:205-207` `NumberInput` 的 `onBlur` 会被 `{...rest}`（`:217`）覆盖，调用方传 `onBlur` 就丢失 clamp。
9. `Overlay.tsx:262-296` `SimplePopover` 被 `components/layout/AppHeader.tsx` 直接 import，但 `ui/index.ts` 没有导出它（导出面不一致）；`Overlay.tsx:224` `Popconfirm` 的 `disabled` 早返回在 hook 之后（合法但容易在后续加 hook 时踩坑），且 Popconfirm 无 loading 态、`Popover.Content` 无 `role="alertdialog"`/`aria-labelledby`。
10. `Overlay.tsx:87-92` `MediaModal` 只有宽度限制，没有 `max-h`/`overflow`，超出视口的高图/视频会溢出（实际调用方 `GalleryPage.tsx:453` 传了 className，建议在组件的默认约束里兜住）。`Overlay.tsx:151` Drawer 无标题时硬编码 `sr-only` 文案“导航菜单”，组件化后用于非导航场景会误导读屏。
11. `Table.tsx:22-23` 注释声称“粘性表头”，但 `<thead>`/`<th>` 上没有任何 `sticky` 类；`Table.tsx:24-44` 没有排序能力（无 `sortOrder`/`onSort`/`aria-sort`），排序完全外置；`<table>` 没有 `caption`/`aria-label`；`:131-137` 展开行 `<tr id>` 没有 `role="region"`/`aria-labelledby`；`:94` `String(rowKey())` 重复时不告警，展开状态会串行。
12. `Tag.tsx:16-17` `default` 与 `neutral` 完全相同；`Tag.tsx:112-124` `CountBadge` 的“99+”没有 `aria-label`（读屏读“九十九加”）；`Tag.tsx:66-88` `StatusDot` 既无 `aria-hidden` 也无文本替代；`Tag.tsx:140` `Avatar` 恒定 `aria-hidden`，如果单字母是唯一信息（如账号头像）会被读屏完全忽略。
13. `Toast.tsx:39-44` 模块级 store 无上限、无去重、无 key 去重策略；`Toast.tsx:75` 的 `duration` 语义是毫秒（`DEFAULT_DURATION = 4500`），与 antd notification 的“秒”心智模型不同，目前没有调用方传 duration 所以暂未踩坑；`Toast.tsx:120-127` 卸载后 store 残留，`Toaster` 重新挂载会把旧通知重新显示。
14. `Tree.tsx:71-85` `expand` 是 async 且 `try/finally` 无 `catch`，`toggle`/ArrowRight 里用 `void expand(node)`：一旦 `loadChildren` 拒绝，会产生未处理的 promise rejection（当前唯一调用方 `LocalDirectoryPicker.loadDirectoryNode` 内部自己 catch，所以现在不会触发；组件被复用到别处就会）。`Tree.tsx:143` `aria-label="目录树"` 硬编码；`:126-129` Enter 同时“选中 + 展开/收起”，行为叠加容易误操作；`:163-167` 单击只选中、要靠双击或 4px 小箭头展开，触屏可用性差；`:72` 展开多个节点时用闭包里的旧 `expandedKeys` 覆盖，快速连续展开不同节点会互相顶掉。
15. `Pagination.tsx:46-71` `total=0` 时仍渲染可点但无意义的“第 1 页”；`pageSize` 变化不重置 page（调用方责任，但组件没提供 `onPageSizeChange` 时自动回第一页的约定）；`pageSize=0` 时 `totalPages=Infinity`，`buildPageItems` 会渲染出页码 `Infinity`；每页按钮都是独立 tab 停靠点，页多时键盘成本高（可考虑 `aria-label` 里提示或用 roving tabindex）。
16. `SearchSelect.tsx:52-61` 在 `setActiveIndex` 的 updater 里调用 `scrollIntoView`（副作用进 updater，StrictMode 双调用）；`:41-49` 选中项不在 `options` 里时仍显示 placeholder，值已设置却看似未选；不支持远程搜索（无 loading/异步 options），若页面需要用大列表需自行实现防抖/竞态。
17. `Toast.test.tsx:9-18` 依赖 `afterEach` 点击 DOM 里的“关闭提示”来清空模块级 store，如果测试清理顺序（setup.ts 的 cleanup）变化就会被挂起的 store 污染；`:31-42` 用例名“按调用顺序排列”但只断言存在；缺少“卸载时定时器被清理”的断言。

### 样式（index.css）
18. `index.css:16-28` `@theme` 里 `--color-brand-50: var(--color-brand-50)` 等是**自引用**（构建产物中确实输出了 `--color-brand-50:var(--color-brand-50)`），靠 `@layer base` 后面的 `:root` 再赋字面量才成立（层叠后 base 覆盖 theme）。当前可运行，但这是脆弱写法：任何绕过 base 层的消费方都会拿到无效值。建议改用独立命名（如 `--brand-50` 在 base 定义，`@theme` 里 `--color-brand-50: var(--brand-50)`），或直接只在 `@theme` 定义字面量 + 在 `.dark`/`[data-accent]` 覆盖（v4 支持）。
19. `index.css:11-13` 注释“页面代码因此无需写任何 dark: 变体”与实现不符：`Tag.tsx:18,137`、`Card.tsx:95`、`Overlay.tsx:191,195`、`CookieSettings.tsx:172`、`ConfigForm.tsx:289` 都在手写 `dark:`。建议统一：要么全用语义 token，要么删掉这句注释。
20. `index.css:275-279` `:focus-visible` 里设置 `border-radius` 对绝大多数元素无效（utilities 层优先级高于 base 层，`rounded-*` 会覆盖它），属于误导性代码；真正需要的是给 `focus-visible` 补充“不改变布局”的 outline 与 offset 变量。
21. `index.css:219-243` 系统暗色兜底块只覆盖颜色 token，没有覆盖 `--shadow-card/--shadow-raised/--shadow-overlay`（它们在 `:root` 里是浅色值），JS 起来前暗色首屏阴影偏亮。`index.css:209-214` `color-scheme` 处理正确。
22. `index.css:287-303` 只处理了 `::-webkit-scrollbar`，Firefox 无滚动条样式（可用 `scrollbar-color`/`scrollbar-width`）；`:245-259` `background-attachment: fixed` + 大半径 radial-gradient 在移动端滚动时会持续重绘，建议用伪元素或 `background-attachment: scroll`。
23. 没有打印样式（设置页/列表打印会带深色背景与按钮）、没有 `@media (prefers-contrast)`；z-index 全部是散落的魔法数字（Modal 1200/1201、Select/Popover 1250、Tooltip/Toast 1300、ConfigForm sticky 10）且没有集中 token，Toast 与 Tooltip 同为 1300 时会按 DOM 顺序互相覆盖。

### 构建 / 配置 / 测试基础设施
24. `tsconfig.json:7,10` `skipLibCheck: true`（常见取舍，但依赖类型错误会被静默）；未开启 `noUncheckedIndexedAccess`/`noUnusedLocals`/`noUnusedParameters`/`verbatimModuleSyntax`。`exactOptionalPropertyTypes` 未开。建议至少加 `noUnusedLocals` 与 `noUncheckedIndexedAccess`（本项目大量 `arr[i]` 访问，如有真有收益）。
25. `tsconfig.node.json` 是死配置：`tsconfig.json` 已经 `include: ["vite.config.ts"]`，也没有 project references 引用它。建议删除，或在根 tsconfig 加 `references` 并用 `tsc -b`（顺带把 vite 配置的 `types:["node"]` 诉求表达清楚）。
26. `vite.config.ts:37-61` `precompress` 会给 `index.html` 也生成 `.br/.gz`，但 Go 侧 `serveInjectedHTML`（`cmd/server/static.go:151-180`）对注入 bootstrap 后的 HTML 会**重新压缩**，不会使用这两个 sidecar；`scripts/sync-webui.sh` 又把它们一起 embed，属于白占体积（P3）。`walkFiles` 同步阻塞 `closeBundle`（产物多时有感），`statSync` 对每个文件一次也可优化。
27. `package.json:19` `react-player` 依赖在 `src/` 中 0 引用（`vite.config.ts:82` 却为它写了 chunk 规则），建议移除（安装体积/供应链面）。`package.json` 无 `engines`（Vite 8 要求 Node ^20.19+ / >=22.12），`dev-all.sh` 也不校验 Node 版本。无 lint/format 脚本与 ESLint（`jsx-a11y` 插件缺失与 P1-3 的系统性 a11y 问题相关）。
28. `index.html:8-10` 从 Google Fonts 加载 Inter/JetBrains Mono：内网/离线部署时字体请求失败（有 `display=swap` 兜底但会闪动、首屏多一次跨域往返），且把访问行为泄露给第三方；CSP 为其专门放开 `style-src/font-src`（`static.go:89-90`）。建议自托管字体（构建期下载 woff2 + `@font-face`），并去掉 `preconnect`。`index.html` 还缺 `meta name="description"`、`theme-color`（暗色浏览器的地址栏颜色）、`<noscript>` 提示。
29. `src/test/setup.ts:14-38` 只补了 `ResizeObserver`/`DOMRect`；如果补 `Select` 的测试会立刻因为 jsdom 缺少 `Element.prototype.hasPointerCapture/releasePointerCapture/setPointerCapture`、`scrollIntoView` 而失败（Radix Select 依赖它们）。`afterEach` 里动态 `import("@testing-library/react")` 做 cleanup 可行但没必要（可直接静态 import）。`render.tsx:3` 的 `renderWithProviders` 在 `components/ui/*.test.tsx` 中完全没被使用（这些测试直接 import RTL 的 `render`），Tooltip 相关路径（设置页大量使用）在 UI 层没有 Provider 覆盖。
30. `src/components/settings/StorageSettings.tsx:6-8` `storageTargetLabel` 导出后无人使用（死代码）；`CookieSettings.tsx:21-46` 的 `cookieHelpSteps/cookieHelpNotes/cookieSettingsTips`、`ConfigForm.tsx:309-330` 的 `normalizeConfig/configSyncKey/cookieCheckKey` 也属于“导出但无调用方/无测试”，建议按需收窄可见性。
31. `ui/index.ts:15` 导出 `Drawer, MediaModal, Modal, Popconfirm, Tooltip, TooltipProvider`，漏了 `SimplePopover`（`Overlay.tsx:262`），导致外部按文件路径深层 import（`AppHeader.tsx:7`）；`index.ts:22` 与 `Feedback.tsx:176` 重复导出 `Tone`。建议统一 barrel 出口并加 `eslint(import/no-restricted-paths)` 之类约定。

## 5. 待确认 / 需 Lead 交叉验证

1. **`lib/api.ts` 的 `checkAuth` 是否总是携带请求体**：`server.go:227-245` 只有在 `r.ContentLength != 0` 时才用前端提交的 draft 合并占位符；若 api 层在某条路径下不发送 body，则“检测前未保存的修改”不会被检测到（会静默检测已保存的凭据）。我未读 `lib/api.ts`（按分工），请 Lead/对应 reviewer 确认。
2. **`pages/SettingsPage.tsx` 是否存在第二个 `additionalCookies` 输入入口（textarea/批量粘贴）**：我在 `components/settings/**` 内只看到逐行单行输入。若页面上另有批量粘贴 UI，P2-4 的严重度需下调。
3. **`useTheme` 的 class/data-accent 写入位置**：CSS 侧确认为 `html.dark` + `html[data-accent=...]`（构建 CSS 中存在 `.dark` 与 `[data-accent=x-blue]` 选择器，`index.css:7,66-136,166-206`），但 `THEME_ACCENTS` 的 `dotColor` 是否与 `data-accent` 值一一对应、`setPreference` 接受哪些字符串（`as any` 处）需要 lib reviewer 确认。
4. **env-only 覆盖部署下的回显差异**（P2-2 第 2 条）：需后端 reviewer 确认 `GetConfig` 的 `ApplyEnvOverrides` + `Redacted` 顺序（`storage.go:815-824` 与 `server.go:101-110`），以及 `PUT` 响应是否会与后续 `GET` 产生非占位符差异；这决定 P2-2 的触发面。
5. **Radix Dialog 的“缺少 Description”开发告警**：jsdom 下只验证了 `aria-labelledby` 存在（`Overlay.test.tsx:53-61`），未断言控制台 warning；Drawer/MediaModal 打开时是否每次都告警需在真实浏览器 dev 模式跑一次确认（P2-9）。
6. **`REDACT` 整体占位符落库路径**（P2-16）：依赖“库里存过非空但解析不出的 additionalCookies”这一边缘历史状态，建议后端 reviewer 确认 `RedactAdditionalCookies` 返回整体 `********` 的触发条件是否还需要额外保护。
7. **`ConfigForm` 保存时整包提交的字段完整性**：`AppConfig` 目前 13 个字段前端都有对应（已逐一核对 `config.go:74-90`），但若后端新增字段而前端 `normalizeConfig`/`draft` 未覆盖，会以零值写回（`storage.go` 的 UPDATE 是全列覆盖）。属未来风险，需后端 reviewer 知悉。
8. **`internal/webui/dist` 的发布流程**：我验证了 `npx vite build` 产物 hash 与仓库内 `apps/web/dist`、`internal/webui/dist` 完全一致（当前未过期），但 CI 是否强制 `npm run build:embedded`（`scripts/sync-webui.sh`）需要 Lead 确认，否则未来源码更新会再次出现“嵌入的是旧 UI”。

## 6. 设计上值得肯定之处（简短）

- 用 Radix 原语（Dialog/Popover/Tooltip/Select/ToggleGroup/Popover）承担焦点陷阱、ESC、滚动锁、`aria-modal`、portal，而不是自研，`Overlay.tsx:7-9` 的注释也把边界写清楚了；Drawer 复用 Dialog 行为只改定位，是正确取舍。
- `Field` 对“悬空 htmlFor 比没有 label 更糟”的处理（`Input.tsx:229-233`）说明作者理解 a11y 细节；`Button` 对 `asChild` 用 `Slot.Slottable` 同时保留图标（`Button.tsx:104-108`）也是正确实现（对照 `@radix-ui/react-slot` 源码确认）。
- `Input` 的受控清空、`NumberInput` 的 `null` 契约（空串 → null）、`buildPageItems` 导出可测（`Pagination.test.tsx` 覆盖 5 种边界）、`Toast` 悬停暂停用 `remainingRef` 精确计算剩余时间，这些细节都明显优于一般项目。
- 前后端占位符契约（`SecretPlaceholder`/`RestoreAdditionalCookies`/`RestoreURLUserinfo`/`RevertEnvOnlyEcho`）整体设计严谨（后端有专门单测），本报告 P0-1 是“前端没用好后端已提供的能力”，修复面很小。
- 暗色模式 class 驱动 + 首屏 `prefers-color-scheme` 兜底、`prefers-reduced-motion` 关装饰动画、Tailwind v4 `@theme` 语义 token + accent 变量化、`-webkit-font-smoothing`/`select-none`/`tabular-nums` 等细节到位；`@custom-variant dark (&:where(.dark, .dark *))` 写法正确。
- 构建侧 `precompress` 生成 `.br/.gz` sidecar 与 Go `NegotiateEncoding` 压缩协商（`static.go`）配套，冷启动不付 Brotli CPU，是有工程意识的优化；`manualChunks` 把 react/radix/query/icons 分开也合理。
- UI 单测 61 个全部通过，`Toast`/`Tree`/`Table`/`Pagination` 都覆盖了关键行为与 aria 属性（含 `aria-activedescendant` 指向真实元素、展开行 `aria-controls` 指向真实节点），测试意图清晰，属于“有效测试”而非快照式假阳性——主要问题只是覆盖范围没延伸到 `settings/`。
