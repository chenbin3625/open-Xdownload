# 修复复核 · 前端 UI 组件 / 设置页 / 样式 / 构建

> 复核对象：`review/REPORT.md` 的 P0-1、P1-6、P1-10、P1-11，以及 §3.5 中属于"UI / 设置页 / 样式 / 构建"的条目（其余 §3.5 条目标注为「他人在范围」）。
> 复核方式：以当前工作区代码为准逐条核验；`git diff` 仅用于确认"改了哪里"，结论一律引用**当前文件行号**。
> **基线快照**：复核期间工作区仍在被其他 reviewer 修改（观察到 `ConfigForm.tsx` 10:54、`SearchSelect.tsx`/`Toast.tsx` 10:57、`package-lock.json` 10:46、`internal/webui/dist` 11:25 重建；期间还短暂出现过未跟踪的 `apps/web/pnpm-lock.yaml`，随后消失（当前不存在））。本报告结论对应当前工作区内容（复核区间 11:20–12:47，关键文件 md5 在此期间未变），文件指纹与行数列于附录 A；若后续仍有改动，以附录 A 的 hash 判断是否已过期。
> 验证命令：`npx tsc --noEmit`（exit 0）；`npx vitest run src/components/settings src/components/ui src/lib/useTheme.test.ts`（11 文件 / 75 用例，连跑 3 次全绿）；`npx vite build --outDir /tmp/dsh-webdist`（exit 0，产物 hash 与 `internal/webui/dist` 完全一致）。
> P0-1 / P1-6 的后端语义用 `/tmp` 内复制的 `internal/config/config.go` 做了**可执行验证**（不在仓库内落任何文件）。

## 0. 状态总览

| # | 条目 | 状态 | 一句话结论 |
| --- | --- | --- | --- |
| P0-1 | 备用 Cookie 稳定 id 全链路 | ✅（核心）/ 🟡（连带） | 前端保留 id + 输出带 id 的 JSON；后端删掉位置兜底、未知 id+占位符直接 400。删除首/中/末行实测全部正确。**但新增 2 个 P1 回归**（见 §6：半行死锁、纯 env 部署不可配） |
| P1-6 | 清除主 Cookie（ClearSentinel） | ✅（含 1 处 P2 UX） | `__CLEAR__` 全链路生效、清空后 GET 为空、不会误清 env；但 env 注入场景下"清除后仍显示 ********"无解释 |
| P1-10 | 钛金下 primary 按钮白底白字 | 🟡 | `--brand-contrast` 已覆盖 5×2 且 Button 已改用；但 **Pagination 当前页 / CountBadge / Checkbox / 侧栏徽标 / Switch 滑块 5 处 + `::selection` 未改**，深色钛金下仍是 1.10:1 |
| P1-11 | 设置页 label 关联 | ✅ | `Field` 改为 Context 自动注入 id，Input/NumberInput/Select 已消费。**经实测 DownloadSettings 的"网络代理""最大文件名长度"已有关联名称**（Lead 初查结论需更正） |
| 3.5-a | ConfigForm 草稿同步卡死 | ✅ | pending key 改 state + 3s 超时 + revision 守卫 + 重新加载按钮重置 dirty；有 3 条回归测试 |
| 3.5-b | Cookie 单行粘贴污染 | ❌ | 交互与提示文案均未改：单行输入框粘贴 `auth_token=x; ct0=y` 会整串存进 `authToken` 字段（文本解析分支不会被走到） |
| 3.5-c | LocalDirectoryPicker | ✅ | 刷新对树生效（依赖改为 `listing.data`）、失败清树+内联错误、上级目录在越界时禁用（后端也补了 parent 清洗） |
| 3.5-d | NumberInput 小数 | ✅ | 改 `parseInt(raw, 10)` |
| 3.5-e | Modal/Drawer 无 title 无关闭 | ✅ | 两个分支都渲染关闭按钮 + sr-only Title |
| 3.5-f | Toast error live / 键盘暂停 | ✅ | error 加 `role="alert"`、focus/blur 参与暂停，并有 fake-timer 测试 |
| 3.5-g | SearchSelect combobox 语义 | ✅ | role 从 Trigger 移到弹层 input，补 `aria-autocomplete`/`aria-expanded`；Trigger 改 `aria-haspopup="dialog"` |
| 3.5-h | `--fg-subtle` 对比度 | 🟡 | 暗色已达标（5.16）；**亮色 #718096 仅 4.02:1，`on surface-muted` 3.67:1，仍低于 AA 4.5** |
| 3.5-i | Spinner 依赖外部 keyframes | ❌ | 仍是 `animate-[spin_0.7s_linear_infinite]`，无自身 keyframes |
| 3.5-j | vite/tsconfig 死配置 | 🟡 | 已加 `target`、删掉 modulePreload 过滤与 player chunk、移除 `react-player` 并重生成 lock；但 `tsconfig.node.json` 仍是死配置，preview 无代理、dev 仍 `--host 0.0.0.0`、无 sourcemap |
| 3.5-l | `BackupCookieInputs` 渲染期调用父级 `onChange` | ✅ | updater 已改纯函数，`onChange` 移到之后；`useEffect` 也做了「值相同保留 currentRows」 |
| 3.5-m | `useTheme` 每实例独立 state | ✅ | 新增模块级事件 + `storage` 监听，Header 与设置页同步；系统偏好监听只在 `system` 时挂载 |
| 3.5-k | Card/Tag/Table/Tree/Pagination 既有 P3 | ❌ | 除 Pagination 加 `pageSizeOptions` 外基本未动（粘性表头注释、Tree async catch、Tag default==neutral 等） |
| — | 嵌入产物 `internal/webui/dist` | ✅ | 11:25 重建后与全新构建**逐字节一致**（sha256 比对通过） |
| — | 新增测试 | ✅ | `settings/` 从 0 测试变为 10 个用例（ConfigForm 4 / CookieSettings 3 / LocalDirectoryPicker 3）+ `useTheme.test.ts` 2 个 |

> 净结论：P0-1、P1-6、P1-11 已真正修复且可验证；P1-10 **只修了主按钮**，同类问题在另外 5 处 + `::selection` 仍在线。
>
> 但 P0-1 的收紧带来 **2 个新的 P1 回归**（都只在"备用 Cookie 处于非成对/env 来源"这类边界状态触发，常规"库内成对值"路径无影响）：
> 1. **半行死锁**：历史库里只要存在"只填了 auth_token 或只填了 ct0"的备用行，此后**每一次保存配置、每一次"检测 Cookie"都 400**，而页面提示"请重新加载配置"却无法自愈（刷新只会重现同一状态）。用户必须自己猜到"删掉那一行"。
> 2. **纯 env 部署不可配**：`OPEN_XDOWNLOAD_ADDITIONAL_COOKIES` 提供备用 Cookie 而 DB 为空时，同样"保存/检测"全部 400；若 DB 非空则反过来——任意一次保存都会把库内其它备用账号**静默删除**。
>
> 两条建议在合入前修掉（修法见 §6），并各补一条回归测试。

## 1. P0-1 复核：✅ 核心链路已修复（含全链路实测）

> 结论先行：REPORT P0-1 描述的「按行号回填导致删行错配」确实已经彻底修好，且旧数据兼容。但紧缩校验引入 2 个新回归（半行死锁 / env 部署），见 §6 —— 那两条属于本次改动带来的新问题，不是 P0-1 未修完。

### 1.1 前端保留 id（✅）

- 位置：`apps/web/src/components/settings/CookieSettings.tsx:521-541`、`:581-586`、`:58-62`、`:468-473`

```tsx
// :58-62 行类型已带 id
export type BackupCookieRow = { id: string; authToken: string; csrfToken: string };

// :527-537 解析后端 JSON 时保留 record.id，缺失才本地生成
return parsed.flatMap((item) => {
  ...
  return authToken || csrfToken
    ? [{ id: firstString(record.id) || emptyBackupCookieRow().id, authToken, csrfToken }]
    : [];
});

// :581-586 提交改为带 id 的 JSON 数组（原先是 "auth_token=..; ct0=.." 文本）
export function normalizeBackupCookieRows(rows: BackupCookieRow[]) {
  const populatedRows = rows
    .map((row) => ({ id: row.id, authToken: row.authToken.trim(), csrfToken: row.csrfToken.trim() }))
    .filter((row) => Boolean(row.authToken || row.csrfToken));
  return populatedRows.length > 0 ? JSON.stringify(populatedRows) : "";
}
```

本地 id 用 `crypto.getRandomValues` 生成（`:468-473`），避免"新增行 id 与已保存行 id 相同"（`CookieSettings.test.tsx` 专门覆盖了 `vi.resetModules()` 重载后不撞 id）。

### 1.2 后端去掉位置兜底 + 拒绝未知 id（✅）

- 位置：`internal/config/config.go:216-264`（`RestoreAdditionalCookies`）、`:266-302`（`ResolveSubmission`）

```go
// :228-245 只按 id 命中，原先的 "i < len(storedPairs) && sub.ID == cookie-i" 位置兜底已删除
storedPairs := withCookieIDs(ParseCookiePairs(stored))
storedByID := make(map[string]AuthCookie, len(storedPairs))
...
for _, sub := range subPairs {
    storedPair, hasStored := storedByID[sub.ID]
    ...
}

// :273-283 未知 id + 占位符、或占位符对不上非空旧值 -> 直接拒绝
if (pair.AuthToken == SecretPlaceholder && (!exists || old.AuthToken == "" || old.AuthToken == SecretPlaceholder)) ||
   (pair.CSRFToken == SecretPlaceholder && (!exists || old.CSRFToken == "" || old.CSRFToken == SecretPlaceholder)) {
    return AppConfig{}, fmt.Errorf("无法还原备用 Cookie，请重新加载配置或输入完整凭据")
}
```

落库唯一入口已改为 `ResolveSubmission`：`internal/storage/storage.go:954-957`（`cfg, err = config.ResolveSubmission(cfg, current)`），`internal/httpapi/server.go:138-141` 校验分支也走同一函数 —— REPORT §4 的"三份合并实现分叉"已收敛为一份。`mergeSecretPlaceholders` 函数本身已不存在，但 `config.go:70` 的注释仍在提它（"Callers that round-trip a redacted config back through the API (mergeSecretPlaceholders / RestoreURLUserinfo)…"）—— 陈旧注释 P3。

### 1.3 可执行验证（删除中间/末行/重排/兼容）

从库内 3 条账号出发，用当前 `config.go` 精确复现（输入/输出均为真实调用）：

```
1) GET redacted:            [{"id":"cookie-0","authToken":"********"...},{"id":"cookie-1",...},{"id":"cookie-2",...}]
2) 删除首行(cookie-0):       保留 cookie-1/cookie-2 + AUTH_B/AUTH_C   ✅ 不再错配
3) 删除中间行(cookie-1):     保留 cookie-0/cookie-2 + AUTH_A/AUTH_C   ✅
4) 删除末行(cookie-2):       保留 cookie-0/cookie-1 + AUTH_A/AUTH_B   ✅
5) 未知 id+占位符:           err=无法还原备用 Cookie...              ✅ 拒绝落库
6) 旧文本格式提交:           [{"id":"cookie-0","authToken":"AUTH_A",...}] err=<nil>  ✅ 向后兼容
7) 旧文本库 GET:            [{"id":"cookie-0","authToken":"********",...}]          ✅ 自动补 id
8) 旧文本库原样回传:          [{"id":"cookie-0","authToken":"AUTH_OLD",...}]         ✅ 往返无损
9) 编辑单行 auth 保留 csrf:   csrf 自动还原 C_A                                      ✅
10) 新增行:                  id 保留 backup-cookie-abc + 真实值                       ✅
11) 删除全部行:              ""  err=<nil>                                          ✅ 可清空
12) 重复 id:                 err=备用 Cookie ID 重复，请重新加载配置                  ✅
13) 占位符落库检查:          结果中不含 ********                                     ✅
14) 行顺序整体倒置(API 层):  cookie-2→C / cookie-1→B / cookie-0→A 各归其位            ✅ id 与凭据始终绑定
```
注：UI 没有「上移/下移」操作，第 14 条是在 API 层直接构造乱序 JSON 验证的（前端 normalize 保序，正常路径不会触发）。

### 1.4 "整体 `********`" 路径（🟡 新数据不再产生；历史毒化值仍会回显但已 fail-closed）

`RedactAdditionalCookies` 在"非空但解析不出任何 cookie 对"时改为返回 `""`（`config.go:199-202`），没有再退化成整体 `********`：

```go
cookies := withCookieIDs(ParseCookiePairs(raw))
if len(cookies) == 0 {
    return ""     // 原先是 return SecretPlaceholder
}
```

但 `RedactAdditionalCookies` **仍保留了** `if raw == SecretPlaceholder { return SecretPlaceholder }`（`config.go:196-198`），所以"整体 `********`"的回显路径并没有被删掉——只是它现在只会来自"历史库里已经存了字面量 `********`"这一种情况（新写入不可能再产生），失败模式也从"静默写坏"变成了"显式 400"。

前端 `parseBackupCookieRows` 里的整体占位符分支（`CookieSettings.tsx:480-481`）现在只可能被这种历史值触发；即使触发，回传也会被 `ResolveSubmission` 以 400 拦住（实测 `毒化库+前端展开回传 → err=无法还原备用 Cookie`），而"删掉该行→提交空"可以把库清成 `""`（实测 err=nil），所以旧毒化数据**可恢复且不会再扩大**。建议后续加一次性迁移把 `additional_cookies='********'` 清空（见 §9 待确认 2）。

副作用（P3，实测）：库里若存了**不可解析**的值（例如外部工具写坏成 `{"foo":"bar"}`），`Redact` 现在返回 `""`；用户之后任意一次保存（前端整包提交 `additionalCookies: ""`）都会把该值**静默清空**（`err=<nil>`）。因为这种值本来也无法使用，清掉不算数据损失，但属于无提示变更，可在 `Redact` 时区分「空」与「不可解析」并给前后端一个显式信号（或启动时记一条 warn 日志）。

## 2. P1-6 复核：✅ 已修复（ClearSentinel 全链路）

- 后端常量与语义：`internal/config/config.go:74-77`、`:285-294`

```go
// :74-77
// ClearSentinel is submitted by the frontend to explicitly clear a saved secret field.
const ClearSentinel = "__CLEAR__"

// :285-294 清除优先于"空串=保留"
if submitted.AuthToken == ClearSentinel {
    submitted.AuthToken = ""
} else if submitted.AuthToken == "" || submitted.AuthToken == SecretPlaceholder {
    submitted.AuthToken = stored.AuthToken
}
```

- 前端入口：`apps/web/src/components/settings/CookieSettings.tsx:102-117`（"清除"按钮，仅在已有值时出现），并在输入框里隐藏哨兵：`:135`、`:158`（`value={isClearSentinel(...) ? "" : ...}`），`primaryComplete`/`hasValue` 也排除哨兵（`:84-87`、`:131`、`:154`），按钮仅在 `primaryConfigured` 为真时渲染（`:102`）。测试：`CookieSettings.test.tsx:8-15`。
- 落库与读回：实测 `ClearSentinel → auth="" csrf="" err=<nil>`；`storage_test.go:430-458`（`TestUpdateConfigClearSentinelRemovesPrimaryCookies`）断言 `GetStoredConfig` 为空，对应"保存后 GET 为空"。
- 不会误清 env：`RevertEnvOnlyEcho` 在哨兵转换**之后**执行（`config.go:297-300`），提交值已是 `""`，与 env 值不相等 → 直接返回 `""`（写库空值）；而读路径 `ApplyEnvOverrides` 仍让 env 值生效。实测：`清除 + env 存在 → DB auth="" (env 仍为 ENV_AUTH)`。
- 哨兵不会外发给 X：`checkAuth` 先 `ResolveSubmission` 再 `ApplyEnvOverrides`（`server.go:268-273`），若清除后无凭据则 `configured=false` 走 `"请先配置 auth_token 和 ct0"` 提前返回（`server.go:276-283`），不会把 `__CLEAR__` 当 Cookie 送出去。实测 `configured=false`。

残留 P2（不阻塞）：env 注入场景下"清除后界面仍显示 `********`"（因为 `GetConfig` 会被 env 覆盖），用户会以为清除没生效；建议在设置页标注"该项由环境变量提供，保存不会写入库"。另外哨兵是明文魔法串，若某天 X token 恰为 `__CLEAR__` 会被误解为清除（现实不可能，P3）。

## 3. P1-10 复核：🟡 部分修复（主按钮已修，5 处同类问题仍在）

### 3.1 已修复部分（✅）

- `--brand-contrast` 在 5 套 accent 的 base 层全部定义，且有默认值（`:root` 与 `[data-accent="x-blue"]` 同组，`index.css:68-70`）；暗色钛金单独覆写为深色（`:199-201`），语义正确（近白底 → 深色字）。

```css
/* index.css:68-70 / 84-85 / 99-100 / 114-115 / 129-130 / 199-201 */
:root, [data-accent="x-blue"] { --brand-contrast: #09090b; ... }
[data-accent="titanium"]      { --brand-contrast: #ffffff; ... }
[data-accent="indigo"]        { --brand-contrast: #ffffff; ... }
[data-accent="emerald"]       { --brand-contrast: #09090b; ... }
[data-accent="amber"]         { --brand-contrast: #09090b; ... }
.dark[data-accent="titanium"], .dark [data-accent="titanium"] { --brand-contrast: #09090b; ... }
```

```css
/* index.css:29 —— 注册为 Tailwind 颜色 */
--color-brand-contrast: var(--brand-contrast);
```
```tsx
// Button.tsx:25 —— primary 改用它
"bg-gradient-to-b from-brand-500 to-brand-600 text-brand-contrast border border-brand-400/25 ...",
```

构建产物已确认生成 `.text-brand-contrast{color:var(--color-brand-contrast)}` 与 6 处 `--brand-contrast` 定义（含 dark titanium 覆写）。

实测对比度（Button primary 文字 = brand-contrast，叠在渐变两端）：

| accent | base(brand-500) | base(brand-600) | hover(brand-400) | active(brand-700) |
| --- | --- | --- | --- | --- |
| x-blue | 6.63 | 5.18 | 9.29 | 3.35 |
| titanium-light | 4.83 | 7.73 | **2.56** | 10.44 |
| titanium-dark | 18.10 | 19.90 | 7.76 | 19.90 |
| indigo | 4.47 | 6.29 | **2.98** | 7.90 |
| emerald | 7.84 | 5.28 | 10.35 | 3.63 |
| amber | 9.26 | 6.24 | 11.92 | 3.96 |

→ 静息态全部 ≥4.47（P1 主诉已解决）。仍有 3 个 hover/active 中间态低于 4.5（titanium-light hover 2.56、indigo hover 2.98、x-blue/emerald/amber active 3.35/3.63/3.96），但都是短暂过渡态，降级为 **P2**。

### 3.2 未修复部分（❌ 同类问题仍在 5 处 + `::selection`，深色钛金下仍不可读）

同一"白字 + brand-500 背景"模式未被替换：

| 位置 | 代码 | 深色钛金对比度 |
| --- | --- | --- |
| `apps/web/src/components/ui/Pagination.tsx:100` | `"border-brand-500 bg-brand-500 text-white"`（当前页码） | **1.10** |
| `apps/web/src/components/ui/Tag.tsx:106` | `brand: "bg-brand-500 text-white"`（CountBadge） | **1.10** |
| `apps/web/src/components/ui/Controls.tsx:157` | `<Check className="size-3 text-white" />`（Checkbox 勾） | **1.10** |
| `apps/web/src/components/layout/AppSidebar.tsx:70` | `from-brand-500 to-brand-600 ... text-white`（Logo 徽标） | **1.10** |
| `apps/web/src/components/ui/Controls.tsx:112/120` | Switch 轨道 `data-[state=checked]:bg-brand-500` + 滑块 `bg-white`（深色钛金下轨道近白，滑块边界消失） | **1.10** |

Pagination 的分页条在 `TaskCenterPage.tsx:541` 与 `GalleryPage.tsx:464` 都有渲染，即"深色+钛金"用户在每个列表页都会看到**当前页码消失**；Checkbox 勾选后在四个开关主题下勾号不可见。这正是 P1-10 的原症状，只是从主按钮挪到了其他组件。其他主题下同一处也不达标：amber 2.15、emerald 2.54、x-blue 3.00（`text-white` on `#f59e0b`/`#10b981`/`#1d9bf0`）。

另外 `index.css:277-280` 的 `::selection` 仍固定 `background: var(--color-brand-500); color: #fff`：amber 亮色 2.15:1、深色钛金 1.10:1 —— 选中文字在这些主题下同样不可辨。也属 P1-10 的同一类问题，建议一并改成 `color: var(--brand-contrast)`。

**建议**：把这 5 处统一换成 `text-brand-contrast`（Checkbox 勾号可用 `text-brand-contrast` 或改成 `stroke="currentColor"` 跟随），并在 CI 里加一条"`grep -r 'text-white' components/ui` 白名单"式的静态检查，避免下次只修一处。

## 4. P1-11 复核：✅ 已修复（并更正 Lead 的初查结论）

### 4.1 机制（✅）

`Field` 不再"不传 htmlFor 就退化成 span"，而是始终渲染 `<label htmlFor>`，并通过 Context 让子控件认领同一个 id：

```tsx
// Input.tsx:241-275（Field 主体 :248-275）
const FieldIdContext = React.createContext<string | undefined>(undefined);
export function useFieldId() { return React.useContext(FieldIdContext); }

export function Field({ label, tooltip, htmlFor, children, className }) {
  const generatedId = useId();
  const fieldId = htmlFor ?? generatedId;
  return (
    <FieldIdContext.Provider value={fieldId}>
      <div ...>
        <label htmlFor={fieldId} className={labelClass}>{label}{tooltip}</label>
        {children}
      </div>
    </FieldIdContext.Provider>
  );
}
```
```tsx
// Input.tsx:61-62 / :173-174 / Controls.tsx:38-42
const fieldId = useFieldId();
const resolvedId = id ?? fieldId;   // Input / NumberInput
<RadixSelect.Trigger id={id ?? fieldId} ...>   // Select
```

### 4.2 关于 Lead 的初查："DownloadSettings 的网络代理/最大文件名长度仍无 htmlFor/aria-label" → 需更正

这两处**确实没有显式 `htmlFor`/`aria-label`**（`DownloadSettings.tsx:59-70`、`:74-86`），但**不需要**：它们通过上面的 Context 自动拿到了 id 与 `<label for>`。可执行证据：

```tsx
// ConfigForm.test.tsx:55-58（真实渲染整个设置页后按可访问名称查询）
expect(screen.getByRole("textbox",    { name: "网络代理 (Proxy)" })).not.toBeNull();
expect(screen.getByRole("spinbutton", { name: "最大文件名长度" })).not.toBeNull();
expect(screen.getByRole("combobox",   { name: "文件命名策略" })).not.toBeNull();
```
我本地实跑该文件 3 次均通过（4 用例 / ~4.7s）。另外 `Controls.test.tsx:60-77` 单测了"Select 用 Field 的 id 关联 label"。

### 4.3 其余 a11y 点

- 主题模式 / 强调色：`ConfigForm.tsx:279`、`:306` 改为 `role="group"` + `aria-label`，按钮加 `aria-pressed`（`:280`、`:305`）✅。
- 下载目录输入框：`LocalDirectoryPicker.tsx:128` 补 `aria-label="下载目录"` ✅（有对应测试）。
- Tooltip 图标键盘可达：`DownloadSettings.tsx:35-43` 已改为 `<button type="button" aria-label="查看说明">` ✅；但 **`CookieSettings.tsx:48-56` 的 TipIcon 仍是不可聚焦的 `<span>`** ❌（不一致，P2）。
- 新引入的语义问题（P3/待确认）：`Field` 把 `tooltip` 放进 `<label>` 内部，而 tooltip 现在是 `<button>`（`DownloadSettings.tsx:59-62` → `Field label + tooltip`）。`label` 的内容模型不允许交互内容/其它 labelable 元素，属于不规范标记；jsdom 实测点击该按钮不会把焦点转给输入框（`document.activeElement` 为 BODY），但真实浏览器行为需人工确认。同时原测试 `Input.test.tsx:124-133` 被改写成了「一定存在 label、且 htmlFor 为真」，**不再验证目标元素是否存在**：我用临时 jsdom 用例（跑完即删）实测 `<Field label="仅说明"><input aria-label="内部输入" /></Field>` 渲染出的 `label.htmlFor` 指向的 id 在 DOM 里**不存在**（`getElementById` 返回 null）——即「悬空 label」已回归，只是当前 5 处真实调用（`CookieSettings.tsx:120`/`:143`、`DownloadSettings.tsx:59`/`:74`/`:90`）子控件都会消费 `useFieldId`，所以尚未在页面上显形。建议 `Field` 保留「子控件不认领 id 时降级为 `<span>`」的能力，或至少把测试改回断言「htmlFor 目标存在」（P3）。

## 5. §3.5 逐条复核

### 5.1 ConfigForm 草稿同步（✅ 已修复）

- `apps/web/src/components/settings/ConfigForm.tsx:48-121`：`pendingSavedConfigKey` 由 ref 改为 state；`onMutate` 记录 `draftRevision`，`onSuccess` 只在"保存期间没有新编辑"时才覆盖草稿（`:104`）；等待 GET 匹配加了 3 秒超时（`:108-112`）；`updateDraft` 会清 pending + 清定时器（`:80-89`）；卸载清理（`:76-78`）。
- "重新加载配置"按钮现在显式重置 dirty 并回灌最新 config（`:191-210`）✅ 原"刷新静默无效"解决。
- 测试：`ConfigForm.test.tsx:19-42`（保存期间继续编辑不被覆盖）、`:44-59`（label 关联，断言在 `:55-58`）、`:61-80`（PUT 响应无法确认时 3s 后跟随 GET）、`:82-99`（reload 丢弃草稿）。

残留（P2）：
1. "重新加载配置"会**无提示丢弃**未保存修改（`:204-205` 直接 `setDraftDirty(false)`；测试 `:97-98` 断言的正是"输入被清空"）。建议加确认气泡（`Popconfirm` 已有）。
2. 3 秒超时是时间启发式：慢网络下 PUT 后 GET 若 >3s 才返回，界面会先回落到旧 GET 值再被新值覆盖（视觉抖动）。测试 `ConfigForm.test.tsx:79` 用 `{ timeout: 4_000 }` 等待这个 3s 定时器，是整个套件里最慢的用例（~3.1s）——建议改用 `vi.useFakeTimers()` 或把超时时长做成可注入参数。
3. 保存成功但"保存期间又编辑过"时仍提示"配置已保存"（`:118` 在 if 之外），用户可能误以为新编辑也存了（P3）。
4. 仍然没有 `beforeunload`/路由拦截，"未保存就切页"依旧静默丢失（我上一轮 P2-3，未修，非本次要求项）。

### 5.2 Cookie 单行粘贴污染（❌ 未修复）

- 提示文案未改：`CookieSettings.tsx:42` 仍写"支持每行一个账号，格式为 auth_token=xxx; ct0=yyy"，但仓库里依然没有批量粘贴入口（`grep -n "Textarea\|onPaste\|批量" CookieSettings.tsx` 无结果）。
- 行状态更新仍无键名剥离：`CookieSettings.tsx:346-361` 的 `updateRow` 直接 `{...row, [field]: nextValue}`。粘贴后 `normalizeBackupCookieRows` 产出 JSON，`useEffect`（`:338-344`）再 parse 时 **JSON 分支优先命中**（`:484-487`），所以**在这条粘贴路径上文本分支 `:498-518` 不会被走到**（只有历史库里存的是纯文本格式时才会走到），整串会原样留在 `authToken` 字段里。
- 我用 `CookieSettings.tsx` 里的纯函数原样复制成 Node 脚本跑了一遍（不动仓库文件）：
```
输入(updateRow 后): rows[0].authToken = "auth_token=x; ct0=y"
提交值:  [{"id":"row-…","authToken":"auth_token=x; ct0=y","csrfToken":""}]   ← 没有被拆解
对照: 若 value 整段是文本 "auth_token=ALICE; ct0=CT0A"，文本分支能正确拆成 {ALICE, CT0A}
```
也就是说：文本解析能力存在，但**单行输入框的粘贴路径永远走不到它**，提示文案与实际可用的输入形态不匹配。
- 端到端实测（当前 `config.go` + 前端 normalize 的结果）：
```
前端提交: [{"id":"backup-cookie-abc","authToken":"auth_token=x; ct0=y","csrfToken":""}]
后端 ParseCookiePairs: [{ID:backup-cookie-abc AuthToken:"auth_token=x; ct0=y" CSRFToken:""}]
ResolveSubmission 落库: 同上一字不差（err=<nil>）   ← 整串被当 auth_token 存下
```
- 影响与上轮一致：按提示操作即产出错误凭据，保存后要等"检测 Cookie"才暴露。
- 建议：加一个 textarea 批量粘贴（按 `;`/换行分区），或在 `updateRow` 里对 `auth_token=`/`ct0=` 前缀做剥离。

### 5.3 LocalDirectoryPicker（✅ 已修复）

- 刷新按钮对树生效：effect 依赖改为 `[listing.data, listing.isError]`（`LocalDirectoryPicker.tsx:71-84`），refetch 同路径也会重建树；有测试 `LocalDirectoryPicker.test.tsx:13-30`。
- 失败不保留旧树：`:73-78` 清空 tree/expanded；`resolvedPath` 失败时回落到 `rootPath`（`:64`）；渲染分支 `:208-212` 在 `isError` 时不渲染 Tree。
- 内联错误：`:199-205`（子目录加载失败走 `Alert type="warning"`）、`:191-197`（列表失败 `Alert type="error"`，文案已由后端脱敏为"无法读取目录"）。
- 越界父目录：后端 `server.go:184-186` 用 `localDirectoryPath` 校验 `listing.Parent`，越界直接置空；前端 `:168` 在 `isError` 时也禁用"上级目录" ✅。
- 残留（P2）：仍可把"家目录本身"选为下载目录，`validateDownloadDir`（`server.go:1520-1539`）只在保存时拒绝，前端没有行内提示（我上轮 P2-7 未修）。

### 5.4 NumberInput 取整（✅）

```tsx
// Input.tsx:203-211
const parsed = parseInt(raw, 10);
onChange(Number.isNaN(parsed) ? null : parsed);
```
不再提交 `120.5` 触发 Go `int` 解码 400。仍存的小问题（P3）：`onBlur` 在 `{...rest}` 之前（`:212-224`），调用方传 `onBlur` 会覆盖掉 clamp。

### 5.5 Overlay 无 title 关闭（✅）

- Modal：`Overlay.tsx:40` 三元 — 有 title 走完整 header（`:41-53`），无 title 走 `:55-62`（sr-only `<Dialog.Title>对话框</Dialog.Title>` + 右对齐关闭按钮，容器 `px-5 pt-3`）。
- Drawer：`Overlay.tsx:152` 同样三元，无 title 走 `:160-167`（sr-only「导航菜单」+ 关闭按钮）。
- 实际命中点（移动端导航抽屉 `App.tsx:193-200` 不传 title）现在有可见关闭按钮与可访问名称 ✅。
- 残留（P3）：无 title 分支下的 sr-only 文案是硬编码（"对话框"/"导航菜单"），组件化后用于非导航场景会误导读屏；`MediaModal`（`:79-115`）仍无 `max-h`/`overflow`，高媒体会溢出视口。

### 5.6 Toast（✅）

```tsx
// Toast.tsx:93 —— 错误改为 assertive 语义
role={record.variant === "error" ? "alert" : undefined}
// :101-102 —— 键盘焦点也暂停计时
onFocus={() => setFocused(true)}
onBlur={(event) => setFocused(event.currentTarget.contains(event.relatedTarget))}
```
测试 `Toast.test.tsx:21-33` 用 fake timers 断言"关闭按钮持有焦点时不消失 → blur 后消失"，并断言 `role="alert"` 存在。容器仍是常驻 `aria-live="polite"`（`:144`）——`role="alert"` 嵌在 polite 区域里，播报优先级由读屏实现决定（可接受，P3）。

### 5.7 SearchSelect（✅）

- Trigger：`aria-haspopup="dialog"` + `aria-expanded`（`SearchSelect.tsx:74-77`），不再谎称 combobox。
- 弹层 input：`role="combobox"` + `aria-label` + `aria-expanded` + `aria-autocomplete="list"`（`:106-111`），并挂 `aria-controls`/`aria-activedescendant`（`:129-132`）。
- 测试已断言"焦点在弹层 input 上，且它就是 combobox"（`Controls.test.tsx:139-147`）。
- 我另写了一个临时 jsdom 用例验证 Escape 行为（跑完即删，未在仓库留文件）：`Escape` 由 Radix `DismissableLayer` 关闭弹层，`FocusScope` 卸载后把焦点还给 Trigger —— **通过**。所以这块无需自己实现，但**仓库里没有对应回归测试**（P3：建议把该用例补进 `Controls.test.tsx`）。
- 残留（P3）：无远程搜索（loading/竞态由调用方负责）。

### 5.7b `BackupCookieInputs` 渲染期副作用（✅ 已修复）

上一轮指出的"在 `setState` updater 内调用父级 `onChange`"已消除：`updateRow`/`addRow`/`removeRow` 全部改成"先算 nextRows，再 `setRows` + `onChange`"，updater 保持纯函数。

```tsx
// CookieSettings.tsx:346-361
function updateRow(index, field, nextValue) {
  const nextRows = rows.map((row, rowIndex) => { ... });
  setRows(nextRows);
  onChange(normalizeBackupCookieRows(nextRows));   // 已移到 updater 之外
}
// :364-369 addRow / :371-377 removeRow 同样改法
```
另外 `useEffect` 同步也改成"值相同就保留 currentRows"（`:338-344`），避免编辑中被父级回写覆盖、丢失输入焦点。

### 5.7c `useTheme` 跨实例不同步（✅ 已修复）

`useTheme.ts:110-157` 新增模块级事件 + `storage` 监听，任一实例 `setPreference/setAccent` 会 dispatch `THEME_CHANGE_EVENT`，其余实例与其它标签页同步；系统偏好监听只在 `preference === "system"` 时挂载（`:146-157`），用户显式选择后不会被系统切换覆盖。`useTheme.test.ts` 两个用例断言"两个消费者同步"。

### 5.8 `--fg-subtle` 对比度（🟡 部分修复）

| 组合 | 现在 | AA(4.5) |
| --- | --- | --- |
| light `#718096` on `#ffffff` | **4.02** | ❌ |
| light on `--canvas #f8fafc` | **3.84** | ❌ |
| light on `--surface-muted #f1f5f9` | **3.67** | ❌ |
| dark `#85858f` on `#111114` | 5.16 | ✅ |
| dark on `#09090b` | 5.45 | ✅ |

`index.css:155`（light）、`:182`/`:239`（dark 与 prefers 兜底）。暗色已达标，**亮色仍差一档**（`#718096` → 建议 `#5f6b7a`：surface 5.43 / canvas 5.19 / surface-muted 4.95，三档全过；退一步 `#64748b` 在 surface-muted 上只有 4.34，仍不够）。该变量用于占位符、时间戳、`text-[11px]` 说明文字，仍是高频弱视信息。

### 5.9 Spinner keyframes（❌ 未修复）

```tsx
// Spinner.tsx:11
className={cn("size-4 shrink-0 animate-[spin_0.7s_linear_infinite]", className)}
```
依旧依赖"别处存在字面量 `animate-spin`"才让 Tailwind 产出 `@keyframes spin`（当前 `ConfigForm.tsx:195`、`LocalDirectoryPicker.tsx:184`、`CookieSettings.tsx:255`、`TaskCenterPage.tsx:83-84` 提供）。一旦这些调用点被删/被 tree-shake，Spinner 会静默停转。建议在 `@theme` 定义 `--animate-spin: spin .7s linear infinite` 或显式写 keyframes。

### 5.10 vite / tsconfig / 依赖（🟡 部分修复）

已修（✅）：
- `vite.config.ts:69` 加 `build.target: "es2020"`。
- `vite.config.ts:70-72` 现有 `modulePreload: { polyfill: false }`（原先那段永不命中的 `resolveDependencies` 过滤已删）；`:75-83` 的 `manualChunks` 里不再有 `player` 分支。
- `package.json` 移除 `react-player`（原 `dependencies` 项已不存在），`package-lock.json` 重新生成后 `react-player` 出现次数为 **0**，`lock.packages[""].dependencies` 与 `package.json.dependencies` 完全一致，`devDependencies` 亦一致（CI 用 `npm ci --prefix apps/web` 后 `npm run build`，见 `.github/workflows/ci.yml:37`、`:44`，两边自洽）。

未修（❌）：
- `apps/web/tsconfig.node.json` 仍存在且**无人引用**（`tsconfig.json` 无 `references`，`package.json` 无 `tsc -b`），`tsc -p tsconfig.node.json` 能跑但没有意义 —— 死配置。
- `vite.config.ts:86-91` 只有 `server.proxy`（dev 用），**没有 `preview.proxy`** → `npm run preview` 看不到 API；`package.json:6-11` 的 `dev`/`preview` 仍是 `--host 0.0.0.0`（该服务本身无鉴权）。
- 仍无 `build.sourcemap`。
- 未新增 lint/format 脚本（`jsx-a11y` 缺失与 P1-11 这类问题相关）。

### 5.11 Card/Tag/Table/Tree/Pagination 既有 P3（❌ 基本未动）

`git diff --stat` 显示这批组件里**只有 `Pagination.tsx` 有 3 行改动**（且只是把每页条数选项外置），其余零改动：

- `Table.tsx:22` 注释仍称"粘性表头"，但 `<thead>`（`:67`）与 `<th>`（`:69`/`:71`）无任何 `sticky` 类。
- `Tree.tsx:71-89` `expand` 有 `try/finally`（清理 loadingKeys）但**没有 `catch`**，且调用点 `:94`/`:112` 是 `void expand(node)` → `loadChildren` 拒绝时仍会产出未处理 rejection（当前唯一调用方 `LocalDirectoryPicker.tsx:92-109` 自己 catch 并转成内联错误，所以线上暂无影响，属潜在问题）。
- `Tag.tsx:16-17` `default` 与 `neutral` 仍完全相同；`CountBadge`（`:91-124`）仍无 `aria-label`，且 `brand` tone（`:106`）是 §3.2 那处 `text-white`。
- `Pagination.tsx:48` `totalPages = Math.max(1, Math.ceil(total / pageSize))`：`pageSize=0` 时 `totalPages=Infinity`，`buildPageItems` 会渲染出 `Infinity` 页码（调用方目前不会传 0，属防御缺失）。本次新增的 `pageSizeOptions` 参数（`:30`、`:40`、`:123`）只把选项外置，没解决越界。
- `Card.tsx:55-57` `actions` 仍用数组下标做 key（`key={index}`），actions 重排会串状态。

### 5.12 构建产物一致性（✅）

复核期间 `internal/webui/dist` 被重建（11:25）。验证：把 `internal/webui/dist/assets/index-BnW3_LU8.js.br` 解压后与全新 `vite build` 的同一文件做 sha256 比对 → **完全一致**；`index.html` 的 asset 清单与新建产物清单一致；CSS 亦一致。CI 已加 `git diff --exit-code -- internal/webui/dist` 守卫（`.github/workflows/ci.yml:50`），这条链路现在是闭环的。

## 6. 新发现 / 新回归

### [新-P1] 只填一半的备用 Cookie 行会让**此后所有配置保存**都 400（不可自查的死锁）

- 位置：`internal/config/config.go:203-206`（Redact 无条件把两个字段都写成 `********`）+ `:279-282`（占位符必须能对上"非空"的旧值）+ `:252-254`（允许只存半对的行走进库）+ 前端 `CookieSettings.tsx:581-586`（不拦半行）+ `:396`（`pairComplete` 只影响标签不影响保存）
- 可执行复现（当前 config.go）：
```
stored（用户上次保存：只填了 auth_token）: [{"id":"cookie-0","authToken":"AUTH_PARTIAL","csrfToken":""}]
1) 下次 GET 脱敏:  [{"id":"cookie-0","authToken":"********","csrfToken":"********"}]   ← csrf 本来为空也被涂成 ********
2) 用户什么都没改，只点"保存配置": err=无法还原备用 Cookie，请重新加载配置或输入完整凭据  ← 整个 PUT 400
3) 用户只补 csrf 再保存:        [{"id":"cookie-0","authToken":"AUTH_PARTIAL","csrfToken":"NEW_CT0"}] err=<nil>
4) 对称情形（只填 csrf）同样死锁
```
- **触发路径（普通用户可达，无需旧数据）**：新增一行备用账号 → 只填 auth_token（不填 ct0）→ 点"保存配置"（**成功**，`ResolveSubmission` 只看占位符，不看是否成对；`CookieSettings.tsx:396` 的 `pairComplete` 只渲染"待补全"标签）→ 之后再点任何一次保存都 400。实测：
```
半行首次保存: err=<nil> -> DB: [{"id":"backup-cookie-x","authToken":"ONLY_AUTH","csrfToken":""}]
随后 GET:   [{"id":"backup-cookie-x","authToken":"********","csrfToken":"********"}]
保存其它字段（前端整包提交）: err=无法还原备用 Cookie，请重新加载配置或输入完整凭据
```
- 额外影响（实测）：`POST /api/config/check` 也走同一个 `ResolveSubmission`（`internal/httpapi/server.go:268-272`），所以半行状态下点"检测 Cookie"同样得到 400 —— 用户连自检都做不了。实测输出：
```
B) 半行场景点『检测 Cookie』 -> 400: 无法还原备用 Cookie，请重新加载配置或输入完整凭据
```
- 影响：`RestoreAdditionalCookies` 允许半行落库（`auth != "" || csrf != ""`），`xclient` 又会跳过不完整凭据（`xclient/client.go` 的 `ParseAdditionalCookies` 过滤 `AuthToken == "" || CSRFToken == ""`），所以这半行本身**毫无用处**，却把"保存配置"这个全局操作锁死：用户想改文件名长度、改代理、改主题之外的任何设置都会收到与当前操作无关的 400。这属于本次 P0-1 修复顺带引入的回归（旧实现是位置兜底 + 静默写 `********`，坏但不阻塞）。
- 建议（二选一）：
  1. 让 Redact 逐字段复用 `redact()`：空值保持 `""`（与主 Cookie 一致），这样占位符只出现在真正有值的字段上，`ResolveSubmission` 的"必须能还原"才有意义；
  2. 或在 `ResolveSubmission` 阶段直接拒绝/裁掉半行（`if auth == "" || csrf == ""` → 丢弃或报"请成对填写"），并让前端在 `pairComplete=false` 时禁用保存。
  建议两条都做，并补一条"半行 → 再保存成功"的回归测试。
- 佐证：现有测试没覆盖"stored 本身就是半行"这一状态（`config_test.go:114-176` 的用例都从成对值出发；`CookieSettings.test.tsx` 也只覆盖成对行），所以 211 个前端用例 + `go test ./...` 全绿也发现不了它。
- 顺带发现的测试质量问题（P3）：`config_test.go:178-187` 的 `TestRedactAdditionalCookiesEmptyParsedReturnsEmpty` 断言是 `if len(pairs) == 0 && got == SecretPlaceholder { t.Fatal(...) }` —— 只要返回的不是占位符就通过，**`"invalid-not-a-cookie"` 原样返回也能过**，没有真正锁住 `want ""`。建议改成 `if got != "" { t.Fatalf(...) }`。

### [新-P1] env 注入备用 Cookie 时：保存要么 400，要么把库内账号池截断成 env 里的那一条

- 位置：`internal/config/config.go:37-39`（`ApplyEnvOverrides` 覆盖 `AdditionalCookies`）+ `:300`（`RevertEnvOnlyEcho` 恢复不了，因为比对的是"还原后的 JSON" vs "env 原文"）+ `:191-199`（Redact 用的是 env 值）
- 复现（库内 3 条，env 里 1 条）：
```
1) GET 显示 additionalCookies: [{"id":"cookie-0","authToken":"********","csrfToken":"********"}]     ← env 只有 1 条
2) 用户只改了别的字段、原样保存: DB 将写成 [{"id":"cookie-0","authToken":"DB_A","csrfToken":"DB_CT_A"}]  ← 另外 2 条永久丢失
```
- 影响：`OPEN_XDOWNLOAD_ADDITIONAL_COOKIES` 部署下，任何人点一次"保存配置"都会静默删除库内其余备用账号（凭据不可逆丢失，用户要从浏览器重新复制）。
- 原因：`RevertEnvOnlyEcho(submitted, stored, EnvAdditionalCookies)`（`config.go:60-66`）的比较对象是 `submitted`（已按 id 还原成 JSON），永远不等于 env 的原始文本（实测 `envRes == os.Getenv(ENV)` → **false**），因此永远不会回退到 stored。
- 同一场景下"检测 Cookie"也直接 400（实测 `A) 仅 env 场景点『检测 Cookie』 -> 400: 无法还原备用 Cookie...`）——纯 env 部署（DB 为空）等于**新增/修改任何设置都不可用**，且刷新页面不会好转。这一条比账号池被截断更严重，建议与上一条一起修。
- 建议：对 `AdditionalCookies` 在"提交值源自 env 脱敏结果"时直接沿用 stored（例如持有 env 原文做一次 `RestoreAdditionalCookies(submitted, envRaw)` 后再比较，或在 `ApplyEnvOverrides` 时记录哪些字段被 env 覆盖、保存时跳过写入）。

### [新-✅] `crypto.getRandomValues` 在 jsdom / 非安全上下文的可用性（已核实，非问题）

`CookieSettings.tsx:469` 用 `crypto.getRandomValues(new Uint32Array(4))` 生成行 id。核实结论：该 API **不要求安全上下文**（只有 `crypto.subtle`/`randomUUID` 等要求 https），Node 24 与 jsdom 30 均内置；`CookieSettings.test.tsx` 3 个用例在 jsdom 下通过，`vitest` 全量 75 用例连跑 3 次无失败。仅提示：老版本 Safari（<15）以外都可用，本项目 `target: es2020` 与之匹配，无需降级方案。

### [新-P3] 其他观察

1. `CookieSettings.tsx:48-56` 的 TipIcon 仍是 `<span>`（同文件的下载设置版已改 `<button>`），键盘用户读不到这两个 Cookie 说明。
2. `ConfigForm.test.tsx:17` mock 了 `./StorageSettings`（整个目录选择器被替换为 `null`），意味着"设置页完整渲染"的 a11y 断言没覆盖 LocalDirectoryPicker 与 Cookie 区；`LocalDirectoryPicker.test.tsx` 单独补齐了目录输入框，但 Cookie 区的 label 关联没有端到端断言（Input/NumberInput/Select 已覆盖，PasswordInput 未覆盖）。
3. `CookieSettings.test.tsx:8-15` 只断言"点了清除按钮 → 得到 `__CLEAR__`"的前端半边，**没有**断言"保存后 GET 为空"的端到端链路（后端有 Go 测试，但前端没有串起来）；建议补一条 mock `updateConfig` 的 ConfigForm 级测试。
4. 复核期间观察到 `apps/web/pnpm-lock.yaml` 一度出现在未跟踪列表（`??`）随后又消失；当前目录下已不存在，与 CI 的 `npm ci` 一致。若这是误操作残留，注意别把 pnpm 锁文件提交进仓库（两套锁文件会导致依赖漂移）。
5. `tsconfig.json` 仍 `skipLibCheck: true` 且未开 `noUncheckedIndexedAccess`/`noUnusedLocals`（沿用上轮 P3，非本次修复项）。

## 7. 测试与工具验证记录

| 命令 | 结果 |
| --- | --- |
| `npx tsc --noEmit`（`apps/web`） | exit 0，无输出 |
| `npx vitest run src/components/settings src/components/ui src/lib/useTheme.test.ts` | 11 文件 / **75 用例全通过**；连跑 3 次结果一致，无 flaky |
| `npm test`（全量 `vitest run`） | 23 文件 / **211 用例全通过**（5.2s），与 FIXES.md 的声称一致 |
| `npx vitest run src/components/settings/ConfigForm.test.tsx` ×3 | 均通过（单文件 ~4.0-4.7s，其中 `syncs the latest GET config...` ~3.1s，因等待 3s 超时定时器） |
| `npx vite build --outDir /tmp/dsh-webdist` | exit 0；asset 清单与 `apps/web/dist`、`internal/webui/dist` 一致；`.br` 解压后 sha256 与新建产物一致 |
| `go test ./internal/config/ ./internal/storage/ -run 'Cookie|Clear|Redact|Resolve|Config|Sentinel'` | 两个包均 `ok`（未修改任何 Go 文件；用 /tmp 副本做语义复现） |
| `go test -count=1 ./...` | 10 个包全部 `ok` |
| `go test ./...`（首次，包并行执行） | **1 次失败**：`internal/jobs` 的 `TestDownloadMediaConcurrentDifferentURLsReuseSameContent`（`manager_test.go:1452` 归档路径未按内容复用）。该测试是 **12:46 由其他 agent 刚加进 `internal/jobs` 的新用例**（`git diff` 显示为 `+func`，同一时间 `manager.go` 也在改），随后 `-count=1 ./...` 与单独跑该包 4 次全部通过 → 属**并发/新用例不稳定**，与本次前端修复无关；但我随后用 `-race -count=1` 单独连跑 5 次均通过，只在 `go test ./...` 的多包并行负载下翻红过一次，属**负载敏感的时序用例**，建议 jobs 责任人用 `go test -count=3 ./...` 复跑确认（仅记录并转告 Lead，不在我的范围） |
| 对比度计算 | 脚本计算 5 accent × 明暗 × hover/active 组合、`fg-subtle`、`text-white` on brand（见 §3.1 / §5.8） |

观察到的告警：`npx vitest` 输出中没有 React `act(...)` 警告、没有 Radix "Missing Description" 警告（Radix 用 `descriptionPresent` 判断，未提供 Description 时不会生成悬空 `aria-describedby`，`@radix-ui/react-dialog` 内部已确认）。

## 8. 结论与建议优先级

**可以放行（已核实修复）**：P0-1 核心链路、P1-6、P1-11 的全部诉求，以及 §3.5 中 ConfigForm 草稿同步、LocalDirectoryPicker、BackupCookieInputs 副作用、useTheme 同步、NumberInput 取整、无标题弹层关闭、Toast alert/键盘暂停、SearchSelect combobox 语义、react-player 清理与 `internal/webui/dist` 同步。

**合入前建议处理（按优先级）**：

1. **P1｜半行 Cookie 死锁**（§6 新-P1 之一）：库内存在不成对备用行时，保存与检测全部 400 且无法自愈。修 Redact 逐字段（空值保持 `""`）+ `ResolveSubmission` 拒绝半行 + 前端 `pairComplete=false` 时禁用保存，并补回归测试。
2. **P1｜纯 env 部署不可配 / 账号池被截断**（§6 新-P1 之二）：`OPEN_XDOWNLOAD_ADDITIONAL_COOKIES` 场景下 DB 为空则保存/检测全 400，DB 非空则保存会静默删掉其它账号。需要"env 覆盖字段在保存时沿用 stored"的显式处理。
3. **P1｜P1-10 收尾**（§3.2）：Pagination 当前页（`Pagination.tsx:100`）、CountBadge（`Tag.tsx:106`）、Checkbox 勾（`Controls.tsx:157`）、侧栏徽标（`AppSidebar.tsx:70`）、Switch 滑块（`Controls.tsx:112`/`:120`）5 处 + `::selection`（`index.css:277-280`）——深色钛金下 1.10:1 完全不可见。可加静态检查防回归。
4. **P2**：Button hover/active 中间态对比度（§3.1 表）；`--fg-subtle` 亮色 4.02:1（§5.8）；Spinner 自带 keyframes（§5.9）；Cookie 批量粘贴（§5.2，当前提示文案会教用户产出错误凭据）；ConfigForm 3 秒超时启发式与 reload 静默丢草稿（§5.1）；Cookie 区 TipIcon 键盘不可达（§6 P3-1）。
5. **P3**：`tsconfig.node.json` 死配置、preview 无代理、dev `--host 0.0.0.0`、无 sourcemap（§5.10）；Card/Tag/Table/Tree/Pagination 既有问题（§5.11）。

**范围外但值得 Lead 统筹**：FIXES.md 声称"Cookie 粘贴规范化"已完成，但当前代码里既无 `onPaste` 也无文本格式的显式入口（`CookieSettings.tsx:42` 文案仍在教 `auth_token=xxx; ct0=yyy`），实测单行粘贴仍会污染——建议与该项的负责人确认是否有意为之（`parseBackupCookieRows` 的文本分支只在 value 整段是文本时生效，单行输入场景不会走到）。

## 9. 待确认（需人工/产品判断，我不下结论）

1. **`Field` 把可交互 tooltip 放进 `<label>`**（`Input.tsx:262-270` 渲染 `{tooltip}` 于 label 内；调用方 `DownloadSettings.tsx:59-62` 传的是 `<button>`）：HTML 规范不允许 `label` 内含其它 labelable 元素。jsdom 里点击该按钮不会把焦点转给输入框（`document.activeElement` 为 BODY），但真实浏览器（尤其 Safari 的 label 激活算法）行为我无法在此环境验证 —— 需要人工点一次确认是否会误触输入框聚焦。
2. **历史毒化数据是否需要迁移**：若线上库曾存过字面量 `additional_cookies='********'`，当前代码会让该用户每次保存都 400（前端会展开成一行 `********`）。用户可自行删行恢复（实测可行），是否需要服务启动时一次性清空，属产品决策。
3. **`OPEN_XDOWNLOAD_ADDITIONAL_COOKIES` 的预期语义**：是"仅作为兜底默认值"还是"强制覆盖库配置"？当前实现是后者（`ApplyEnvOverrides` 无条件覆盖），但保存路径又无法表达"我没有改这个字段"，导致 §6 的两个 P1。正确修法取决于这个语义决定。
4. **Button hover/active 中间态**（§3.1 表中 <4.5 的 6 个组合）是否按 WCAG AA 严格要求：这些是 `:hover`/`:active` 瞬时态，AA 对文字对比度的要求同样适用于状态样式，但实践中多数项目只保证静息态。是否修由团队判定。
5. **`FIXES.md` 与代码的差异**：FIXES.md「前端与构建」段写"Cookie 粘贴规范化…已核验"，但当前 `CookieSettings.tsx` 无任何 `onPaste`/输入规范化（§5.2 实测仍污染），且提示文案 `:42` 仍在教 `auth_token=xxx; ct0=yyy`。需与该项负责人确认是"文档超前"还是"实现遗漏"。
6. **`preview` 无 `/api` 代理、`dev --host 0.0.0.0`**：是否为刻意（便于局域网调试）——若是则属文档缺口（应在 README 标注"仅限可信网络"），不是代码缺陷。

## 附录 A：复核基线的文件指纹（12:47 复核）

| 文件 | 行数 | md5 |
| --- | --- | --- |
| apps/web/src/components/settings/CookieSettings.tsx | 590 | 62d8f7073db2acb8636fca24b0b4e06a |
| apps/web/src/components/settings/ConfigForm.tsx | 367 | 477b7e7672e85a319cc146c89e0917b9 |
| apps/web/src/components/settings/DownloadSettings.tsx | 181 | a5c21cd9bca41f80e2ecbee3adf9170a |
| apps/web/src/components/settings/LocalDirectoryPicker.tsx | 266 | 072eb34eb6e25932f0f214dbb693308f |
| apps/web/src/components/ui/Input.tsx | 275 | f26c6302359c662a310a0bb101d418f2 |
| apps/web/src/components/ui/Controls.tsx | 267 | 0e9a39d42678966d74a907af8ab9cb33 |
| apps/web/src/components/ui/Overlay.tsx | 312 | 4483a0cbf1382fba8be61011b59a3556 |
| apps/web/src/components/ui/Pagination.tsx | 133 | f183448746b3bfe6de9a00b504575d88 |
| apps/web/src/components/ui/Button.tsx | 110 | b5175c665b3e37095d62b1e9dc481082 |
| apps/web/src/components/ui/SearchSelect.tsx | 168 | 0ec94ac02c9b624249198de55af07944 |
| apps/web/src/components/ui/Toast.tsx | 152 | 63d13c5699d3d6d76bdf04b835949a64 |
| apps/web/src/index.css | 441 | 47f0bc384324156c6cba5dc8feae1ff7 |
| apps/web/vite.config.ts | 99 | 8ec2cd424969d9f8390e53c3516484f0 |
| apps/web/package.json | 35 | eadc0633eabef789cc4df32a3fab0da1 |
| internal/config/config.go | 483 | d82953949045ee15869487f92c2163b1 |
| internal/storage/storage.go | 2756 | 15f6a2fb17f68a77e1068b797e1bd3b6 |
| internal/httpapi/server.go | 1591 | 15660de4163ec331b1b1ba32b571bc9c |

## 附录 B：未由我负责、仅顺带抽查的 §3.5 条目

以下属于其他 reviewer 的范围，我只做了"是否已改"的粗查，未做逐行复核，仅供 Lead 交叉时参考：查询失败展示（`GalleryPage.tsx:435`、`TaskCenterPage.tsx:916`、`App.tsx:228/241/245/282` 均已有 `isError` 分支）、`api()` 超时（`api.ts:280-283` 用 `AbortSignal.timeout(30_000)` + `AbortSignal.any`）、`jobStatus` 未知值降级（`TaskCenterPage.tsx:102` `?? unknownStatusPresentation`）。`ErrorBoundary` 仍不存在（首屏异常仍会白屏）。
