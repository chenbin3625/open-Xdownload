# V1 验证记录

验证日期：2026-11-11。本记录区分真实 X 会话、离线工具契约测试和容器运行验证。离线契约不会作为真实 X 获取成功的证据。

## 平台

- 开发主机：macOS / Apple Silicon ARM64。
- 本地服务：Rust/Axum，SQLite，内嵌前端；gallery-dl 1.32.19，yt-dlp 2025.10.14，FFmpeg/FFprobe 9.0.2。
- 容器环境：Colima / Docker Engine，Linux ARM64 原生运行，Linux AMD64 通过 QEMU 模拟运行。
- 容器内 gallery-dl 1.32.19、yt-dlp 2025.10.14、FFmpeg/FFprobe 5.1.9（Debian 12 软件包）。
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
| 前端 TypeScript 与 Vite 生产构建 | 通过；主 JS 包约 748 KB，存在超过 500 KB 的非阻断构建提醒 |
| `cargo fmt --all --check` | 通过 |
| `cargo clippy --locked --all-targets -- -D warnings` | 通过 |
| `cargo test --locked --tests` | 20 项通过：API 8、领域逻辑 4、提取契约 3、持久化/并发 5 |
| `cargo build --locked --release` | macOS ARM64 发布构建通过 |
| 桌面 Chromium 1440×1000 | 两项浏览器测试通过，覆盖全部页面和完整归档流程 |
| 移动 Chromium 390×844 | 两项浏览器测试通过，无页面横向溢出或脚本错误 |
| AMD64 / ARM64 镜像构建 | 两个平台均成功构建为同一个 OCI 镜像索引 |
| Linux ARM64 容器 | 原生运行通过：初始化、内嵌前端、鉴权、权限和重启持久化 |
| Linux AMD64 容器 | QEMU 模拟运行通过相同检查；未在 AMD64 实机运行 |
| Compose / Bake 配置 | Compose 配置校验和双平台 Bake 解析通过 |
| 本机正式服务 | `http://127.0.0.1:3000` 健康检查、真实工具版本检测、桌面/移动页面检查通过 |

浏览器流程使用真实 Rust API、SQLite、队列、图片处理、FFmpeg 和 HTTP 媒体响应；仅 X 端提取与媒体下载工具由离线契约替代。测试实际生成 PNG 与 H.264 MP4，断言图片加载、视频宽度及播放时间推进。覆盖 Cookie 上传、首次用户同步、重复同步/重复推文去重、媒体下载、PhotoSwipe 打开关闭、视频/GIF 播放、标签收藏、完整快照差异及部分快照禁止比较。

后端测试另覆盖 Cookie 非 JSON/空输出、成功退出但带认证错误、失败退出但错误仅在 JSON 中、访问限制、错误脱敏、HTTP Range、访问密码/来源校验、目录越界与符号链接保护、并发入队/下载、取消锁、取消排队下载后重试、文件数据库重开及任务状态恢复。测试 Cookie 和错误标记均为无效离线数据。

截图中媒体库和关系页面来自离线集成测试，设置页面来自本机正式服务：

- [桌面媒体库](screenshots/desktop-media.png)
- [移动媒体库](screenshots/mobile-media.png)
- [桌面部分快照](screenshots/desktop-relationships.png)
- [移动部分快照](screenshots/mobile-relationships.png)
- [本机正式服务桌面设置页](screenshots/live-desktop-settings.png)
- [本机正式服务移动设置页](screenshots/live-mobile-settings.png)

## 部署状态与限制

最终镜像已加载为 `x-archive:v1`，OCI 文件位于本机 `.local/x-archive-v1.oci.tar`（构建产物不纳入 Git）。镜像索引摘要为 `sha256:f8c80b60488c355dcf04d07317a059fa0669a1c05f198b8e59b660d0e14b07ca`。

执行 `node scripts/verify-container.mjs x-archive:v1`，在两个平台分别创建独立临时卷：检查空库自动迁移、前端静态资源、未登录拒绝访问、UID 10001 应用进程、Cookie/设置 `0600` 权限；写入测试媒体与设置后重启，确认用户、媒体、配置和 Cookie 保留，媒体 SHA-256 一致，旧 Web 登录会话失效，日志不包含离线 Cookie 或访问密码。测试容器和卷已清理。

本机正式服务使用 `target/release/x-archive` 和实际安装的下载工具，数据位于仓库 `data/`、配置位于 `config/`，仅监听回环地址。正式服务没有预置演示数据或测试 Cookie，下一步可在设置页导入用户自己的 Cookie。测试服务已经停止。

- 本目录最初为空，本次交付包含完整应用源码、迁移、Compose、多架构 Docker 配置及 CI/GHCR 工作流。
- 未配置 GitHub 远程仓库，因此未实际运行 GitHub Actions 或发布 GHCR 镜像；交付的是可执行工作流和本地镜像。
- 关系完整性采用前后用户 ID、计数和抓取成员数量的一致性判断。上游没有严格的分页完成凭证，真实平台计数延迟等边界仍需账号验收。
- 下载进度为处理阶段；目录变更不自动迁移旧媒体，异常残留文件不自动回收。详细行为见 README。
