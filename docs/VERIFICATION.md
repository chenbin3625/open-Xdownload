# 1.1.0 验证记录

验证日期：2026-11-10（UTC）。本记录只列本次实际执行的结果；GitHub Actions 配置、离线测试和真实 X 会话验证分别记录。

## 平台

- 本次环境：Linux x86-64，Rust 1.99.1、Node.js 22.23.4、Python 3.13.5、FFmpeg / FFprobe 7.1.5。
- CI 配置使用 Node.js 24；本次本地构建使用上述已安装版本。
- 发布目标：`x86_64-unknown-linux-musl`，musl-tools 1.2.5。
- 静态配置检查：actionlint 1.7.12、Docker Compose 5.7.0 独立二进制。
- 当前环境没有 Docker CLI / 守护进程，未在本次执行容器构建和运行验证。
- 浏览器：Playwright Chromium，1440×1000 桌面及 iPhone 13（390×844）移动视口；移动测试为 Chromium 模拟，不是 iOS Safari 实机。

## 待验证的真实 X 项目

本次没有提供有效个人 X Cookie，因此不将以下项目记为通过：

1. 实际账号的 Cookie 有效性检测和过期后的反馈。
2. 实际单条推文获取、媒体下载、媒体库查看、原图/视频播放的全流程。
3. 实际用户首次历史归档、增量同步和重复同步去重。
4. 实际 Following / Followers 抓取、平台分页与计数一致性、真实关系差异。
5. 受保护用户、账号权限变化、长期限流及 X 接口更新后的兼容性。

## 自动化及运行结果

| 检查 | 实际结果 |
| --- | --- |
| 前端 TypeScript 与 Vite 生产构建 | 通过；主 JS 包约 756 KB，存在超过 500 KB 的非阻断构建提醒 |
| `cargo fmt --all --check` | 通过 |
| `cargo clippy --locked --all-targets -- -D warnings` | 通过 |
| `cargo test --locked` | 31 项通过：API 10、领域逻辑 6、下载路径 1、提取契约 3、持久化/并发 5、任务分组 6 |
| `cargo build --locked --release --target x86_64-unknown-linux-musl` | 通过，`file` 确认为 stripped static-pie ELF |
| `scripts/smoke-binary.mjs` | 正式 musl 二进制在独立临时目录中通过启动、版本、内嵌前端资源、鉴权与 SQLite 初始化检查 |
| 桌面 Chromium 1440×1000 | 5 项通过 |
| 移动 Chromium 390×844 | 5 项通过 |
| GitHub Actions 配置 | 三份工作流通过 actionlint 静态检查，尚未在 GitHub 执行 |
| Compose 配置 | 默认镜像、源码构建覆盖、绑定目录示例均通过 `config`；覆盖结果确认为本地镜像、正确构建上下文和持久卷 |
| 其他二进制平台与双架构镜像 | 已配置工作流，尚未在相应 runner 上执行，不记为通过 |

浏览器流程使用真实 Rust API、SQLite、队列、图片处理、FFmpeg 和 HTTP 媒体响应；仅 X 端提取与媒体下载工具由离线契约替代。测试实际生成 PNG 与 H.264 MP4，断言图片加载、视频宽度及播放时间推进。覆盖 Cookie 分别输入、校验、更新、清空与删除，首次用户同步、重复同步/导入去重、推文媒体预览、详情切换、PhotoSwipe、视频/GIF 播放、标签收藏、完整快照差异及部分快照禁止比较。任务页另覆盖批次汇总、大小单位、历史结果显示和移动布局。

后端测试另覆盖工具认证错误与输出脱敏、HTTP Range、访问密码/来源校验、目录越界与符号链接保护、用户名存储目录、并发入队/下载、取消锁、重试与重启恢复，以及历史任务迁移、父子任务关联、聚合分页和取消/重试。测试 Cookie 和错误标记均为无效离线数据。

浏览器截图与失败追踪输出到 `frontend/test-results`，不纳入 Git。README 中的媒体库截图为离线测试数据，不能作为真实账号抓取证明。

## 部署状态与限制

远程仓库为 `https://github.com/chenbin3625/x-archive.git`。本次环境 `gh auth status` 显示未登录，Git 也无可用 HTTPS 凭据，因此未推送、未修改仓库可见性、未在线运行 Actions，未发布 GitHub Release 或 GHCR 镜像。

- `release.yml` 配置 Linux x86-64 / ARM64、macOS Intel / Apple Silicon、Windows x86-64 的生产构建与二进制冒烟检查，并在 CI 和镜像成功后公开 Release。
- `image.yml` 配置 AMD64 / ARM64 原生容器验证，通过后发布多架构镜像；PR 不推送，主分支使用 `edge`，稳定版使用 `latest`。
- GitHub 登录后需核对远程版本和分支，再执行公开仓库、推送版本及线上验收；GHCR 包的公开可见性需要单独确认。
- 只有 Linux x86-64 musl 二进制在本次完成了本地发布构建与运行验证，其他平台不能以配置存在代替实际执行结果。
- 关系完整性采用前后用户 ID、计数和抓取成员数量的一致性判断。上游没有严格的分页完成凭证，真实平台计数延迟等边界仍需账号验收。
- 下载进度为处理阶段；目录变更不自动迁移旧媒体，异常残留文件不自动回收。详细行为见 README。
