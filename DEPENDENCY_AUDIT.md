# 依赖审计（2026-11-18 UTC）

覆盖 Go 模块、Go 标准库及 `apps/web` 的 npm 依赖。审计期间工作区已有并行进行的业务代码修改，依赖兼容性另用已提交源码做了隔离验证。

## 已应用

| 依赖 | 原版本 | 新版本 |
| --- | --- | --- |
| Go / Docker 构建镜像 | 1.26.6 | 1.26.11 |
| brotli | 1.2.4 | 1.3.0 |
| chi/v5 | 5.3.2 | 5.5.0 |
| quic-go | 0.62.0 | 0.65.0 |
| gjson | 1.19.0 | 1.20.4 |
| x/crypto、x/sys、x/net | 0.57.0、0.48.0、0.60.0 | 0.59.0、0.50.0、0.63.0 |
| sqlite、compress | 1.59.0、1.19.2 | 1.62.0、1.20.4 |
| React / React DOM | 19.3.0 | 19.3.1 |
| Vite、Vitest | 8.3.5、5.0.5 | 8.3.8、5.1.0 |
| Tailwind / Vite 插件 | 4.3.3 | 4.3.5 |
| Playwright | 1.65.0 | 1.65.1 |

其余兼容范围更新包括 React Query、i18next 26、react-i18next 17、Lucide 1、jsdom 30、Testing Library user-event 和类型定义。已更新 `apps/web/package-lock.json`、`go.mod`、`go.sum`，并执行 `go mod tidy`。SQLite 的 libc 保持其要求的 `1.80.1`，没有独立提升到其它版本。

QUIC 升级经过 0.63–0.65 发布说明和项目调用点核对；项目使用的 `http3.Server`、ALPN、TLS 启动及关闭接口保持兼容，未使用已变更的流接口或 qlog。

## 安全结果

原 Brotli 解压路径存在可达内存耗尽漏洞 [GO-2026-6888](https://pkg.go.dev/vuln/GO-2026-6888)。同时提升有公告的 compress、x/net、x/crypto 及 Go 补丁版本。升级后的 `govulncheck -show=verbose ./...` 未发现漏洞。

npm 最终报告 9 条 high 受影响包记录，均来自 [picomatch ReDoS / GHSA-fc5q-39xc-vjpf](https://github.com/advisories/GHSA-fc5q-39xc-vjpf)。最初为 6 条，后续并行加入的 patch-package/micromatch 链增加了 3 条记录。查询时最新 picomatch 4.0.7 仍在受影响范围，没有可用修补版；没有采用审计建议的跨主版本工具链降级。

已保留并行新增的补丁和安装检查，并补齐 `micromatch++picomatch+2.3.2.patch`。`npm run test:security` 的 2 份安装样例全部通过，但相同 helper 在 chenbinPT 中对 `+(a+)b`、`+([[]*)b` 的 40 字符输入仍超过 1000ms 隔离子进程限制。补丁仅缓解部分输入，并会收紧部分 glob 匹配语义，不能宣称该公告已完整修复。

## 实际验证

- 升级后的 `go test -mod=readonly ./... -count=1` 在工作区和最终隔离检出均全部通过；最终检出包含当前最新 Go 传递依赖。
- `CGO_ENABLED=0 go build -mod=readonly -o /tmp/openx-audit-binary ./cmd/server` 通过。
- 最终版本组合的 `npm run build` 通过。
- 临时检出提交 `d7a420d`，复制最新前端清单/锁文件、补丁和安全脚本，独立 `npm ci` 通过；执行 `npm test -- --cache=false --maxWorkers=1`：33 文件、359/359 测试通过，最终构建也通过。
- 原工作区早期同一命令得到 357/359；两项 `App.auth.test.tsx` 要求 localStorage 保存 token，而当时并行修改后的 `session.ts`/`AuthGate.tsx` 已移除此行为。后续追加测试又受到安装目录重建影响，因此采用独立检出作为依赖兼容性证据。本次没有修改这些业务源码或测试。

## 撤回或暂缓

- Semi UI 2.105：实际引入 Pagination DOM/无障碍属性和 Escape 传播回归。恢复 2.104 后相关 25/25 测试通过，最终将范围限制为 `~2.104.0`。后续需先适配包装组件再升级。
- i18next 27 / react-i18next 18：需要成对迁移，当前缺少完整行为验证。
- jsdom 31：提高 Node 最低版本，尚未核验所有运行环境和行为变化。
- 未运行需要外部服务和认证配置的 Playwright E2E、真实 HTTP/3 UDP/TLS 联调或 Docker 镜像构建；未同步受版本控制的 embedded dist。
