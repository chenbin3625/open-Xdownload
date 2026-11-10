# X Archive

使用自己的 X 登录会话，将有权访问的推文、原始图片、视频、GIF 和关注关系归档到本地。React 19 界面和 API 由同一个 Rust 服务提供；Docker 镜像包含 Python 下载工具及 FFmpeg，不依赖其他服务。

> 当前交付状态和实际验证结果见 [docs/VERIFICATION.md](docs/VERIFICATION.md)。离线测试数据只用于自动化测试。真实 X 会话的验收需要用户提供有效 Cookie，并受账号权限、X 返回范围、网络及平台限流影响。

## Docker 启动

需要 Docker Engine 和 Compose。首次启动前，在项目目录创建 `.env`，配置自己的访问密码（至少 12 位）：

```dotenv
X_ARCHIVE_PASSWORD=replace-with-your-own-long-password
```

```sh
docker compose up -d --build
docker compose logs -f x-archive
```

打开 <http://127.0.0.1:3000>，使用访问密码登录。在设置中导入 Cookie 后即可添加用户、导入推文、管理任务和创建关系快照。

默认只将端口映射到本机；远程部署可通过 HTTPS 反向代理访问服务，并保留访问密码。服务对外监听时强制要求至少 12 位密码。访问密码通过环境变量传入，不写入 SQLite。登录会话在服务重启后失效，需要重新登录。

### 多架构镜像

```sh
docker buildx bake -f docker-bake.hcl --set x-archive.output=type=oci,dest=x-archive.oci.tar
docker load -i x-archive.oci.tar
```

`docker-bake.hcl` 同时构建 `linux/amd64` 和 `linux/arm64`。Rust 在构建主机原生执行交叉编译，运行镜像为目标平台的 Debian Bookworm Slim。前端产物嵌入 Rust 二进制，运行镜像无需挂载前端目录。

`.github/workflows/image.yml` 在推送 `main` 或 `v*` 标签时构建并发布 `ghcr.io/<仓库所有者>/<仓库名>`，PR 只构建、不发布。需要将本目录推送到自己的 GitHub 仓库，并允许工作流写入 Packages。这里交付工作流，不代表已经在 GitHub 执行或发布了镜像。

## 本地开发

需要稳定版 Rust、Node.js 24、Python 3.9 或更新版本，以及 PATH 中的 FFmpeg/FFprobe。

```sh
python3 -m venv .tools
.tools/bin/pip install -r requirements-tools.txt
npm --prefix frontend ci
npm --prefix frontend run build
PATH="$PWD/.tools/bin:$PATH" cargo run --locked
```

默认监听 <http://127.0.0.1:3000>。本机模式可以不配置访问密码，且只接受回环 Host。开发前端热更新可运行 `npm --prefix frontend run dev`，API 代理到端口 3000；日常使用直接访问 Rust 服务。

### 环境变量

| 变量 | 默认值 | 作用 |
| --- | --- | --- |
| `X_ARCHIVE_BIND` | `127.0.0.1:3000` | 监听地址；镜像内为 `0.0.0.0:3000` |
| `X_ARCHIVE_PASSWORD` | 无 | Web 访问密码；对外监听时必填，至少 12 位 |
| `X_ARCHIVE_DATA` | `data` | 数据库、媒体、预览图和下载临时目录；镜像内 `/data` |
| `X_ARCHIVE_CONFIG` | `config` | 设置与 Cookie；镜像内 `/config` |
| `GALLERY_DL_BIN` | `gallery-dl` | gallery-dl 可执行文件路径 |
| `YT_DLP_BIN` | `yt-dlp` | yt-dlp 可执行文件路径 |
| `RUST_LOG` | `x_archive=info,tower_http=warn` | 服务日志级别；不要开启第三方工具的流量调试 |

## Cookie 导入与会话

1. 在浏览器中登录自己的 X 账号，并导出 **x.com 的 Netscape 格式 cookies.txt**。文件应包含未过期的 `auth_token` 和 `ct0`；JSON、HTTP Cookie 字符串及浏览器数据库不属于该格式。
2. 进入“设置 → X 登录 Cookie”，上传文件。上传只保留 X/Twitter 域的 Cookie，最大 1 MB，保存为 `/config/cookies.txt`。
3. 检测任务使用需要登录的书签读取验证会话。界面区分未导入、待检测、有效、已失效和访问受限；上传通过格式校验不等于已经验证登录有效。
4. 会话失效时重新导出并更新，再检测、重试失败任务。删除 Cookie 会取消未结束任务，已归档数据保留。

配置目录权限为 `0700`，Cookie 和设置文件为 `0600`。每次工具执行使用 `/config/.sessions` 中的私有 Cookie 副本，避免工具修改用户上传的原文件；结束后删除，异常关机残留在下一次启动时清理。Cookie 内容不进入接口响应、数据库、日志或镜像构建上下文。不要将 `cookies.txt` 提交到 Git。

应用使用 gallery-dl 和 yt-dlp 的现有提取能力，不使用 X 付费 API，也不提供越权抓取、解除访问限制或绕过限流的功能。

## 归档流程

### 单条推文

“导入推文”接受 `https://x.com/<用户>/status/<数字 ID>`、Twitter 同类链接或 `https://x.com/i/web/status/<ID>`。任务获取作者、正文、发布时间与媒体关联。重复导入不会重复创建推文或媒体，也不会清除收藏和标签。文字推文同样入库。

启用自动下载时，媒体会进入独立下载队列；图片使用 gallery-dl，视频和视频格式 GIF 使用 yt-dlp。下载完成后生成图片缩略图或视频封面，通过 FFprobe 读取视频信息、FFmpeg 处理为可在线播放的 MP4。媒体库支持原图缩放、HTML5 视频播放、GIF、用户/日期/类型/收藏/标签筛选以及原推文链接。媒体详情提供标签、收藏及重新下载操作。

### 用户归档

添加用户名、`@用户名` 或主页链接后开始首次归档。同步读取用户资料及当前会话可访问的本人推文与回复，不递归抓取其他账号或转推。X 时间线能返回的历史可能有限，任务成功表示工具完成了当前可返回范围，不保证 X 已删除、不可访问或未返回的历史也被保存。

增量同步从最新已存推文向前重叠两天读取，再按推文 ID 与媒体序号去重。置顶推文不作为增量时间线起点。可以设置全局或按用户的同步间隔、暂停/启用、立即同步。重新添加已存在的用户会启用归档并排队同步。移除用户会停止同步并删除其关系快照，推文和媒体保留。

### 任务与重启

下载并发数为 1–8；X 元数据抓取串行执行以控制请求频率。进度显示准备、提取/下载、处理/保存等阶段，并非精确的网络字节百分比。临时错误最多自动尝试 3 次，普通重试间隔 30/60 秒，限流错误等待 15 分钟；Cookie/权限错误需要用户处理后手动重试。

活动任务通过 SQLite 唯一约束去重。取消执行中的任务先进入“取消中”，工具退出后才释放同目标锁；手动重试创建新任务并保留历史。重启时正在执行的任务重新排队，取消中的任务明确结束，未完成的媒体下载恢复为待处理或失败。下载使用临时文件，成功处理后才原子替换公开文件。一个数据目录只允许一个应用实例。

抓取失败时，已解析并入库的部分推文会保留，任务明确报告未完整结束；重试不会重复入库。Cookie 或工具异常不会被标记为成功。

## 关注关系快照

“关注关系”支持 Following / Followers 的手动快照、历史浏览和差异比较；“设置”可启用定时双向快照及配置间隔。暂停用户不进行定时或手动抓取。

每次抓取记录以下状态：

| 状态 | 判断与用途 |
| --- | --- |
| 完整 | 前后用户资料 ID 和关系计数一致、成员 ID 有效且去重数量相符、工具无错误或警告，且未使用范围/日期截断 |
| 部分 | 已有可读成员，但计数不一致、未知、抓取期间变化或存在截断/错误；仅用于查看已抓到的成员 |
| 失败 | 没有获得可用结果，保留明确原因 |

只有同一用户、同一方向、时间顺序正确的两个**完整快照**才能判断新增或取消；成员以稳定用户 ID 比较，改名不会被误判为取关。不完整快照不会得出“取消关注”。

gallery-dl 没有提供严格的分页完成凭证，完整状态采用前后计数等保守一致性校验；仍受 X 端计数延迟、可见性和接口变化影响。计数不一致时宁可显示部分，也不推断取关。

## 持久化和存储设置

Compose 默认使用 `archive-data` 和 `archive-config` 命名卷：

```text
/data/archive.sqlite       SQLite 数据及任务、快照、收藏、标签
/data/images/             默认图片和原生 GIF
/data/videos/             默认视频及 MP4 GIF
/data/thumbnails/         预览图及视频封面
/data/.staging/           未完成下载
/config/settings.json     Web 设置
/config/cookies.txt       私有会话文件
/config/.sessions/        临时私有会话副本
```

Web 保存路径为 `/data` 内的相对路径，不接受 `..`、绝对路径或符号链接跳转。修改目录只影响后续下载，不自动迁移旧文件。重新下载后旧目录文件、异常中断后的临时/未引用文件可能残留，V1 不含垃圾回收。

“归档媒体”显示数据库记录的媒体文件字节数，不包括缩略图、暂存文件或遗留文件；磁盘总量/可用量为数据目录所在文件系统的实际数值。可用空间不足 128 MB 时暂停新的媒体下载。

镜像应用进程使用 UID/GID `10001`，入口程序初始化卷顶层权限后降权运行。使用自己准备的绑定目录时，已有内容也需要由 UID/GID `10001` 可读写。

## 更新下载工具

工具版本固定在 `requirements-tools.txt` 和 Dockerfile 构建参数。X 接口变化导致提取失败时，可以选择明确版本后重建：

```sh
docker compose build --build-arg GALLERY_DL_VERSION=1.32.19 --build-arg YT_DLP_VERSION=2025.10.14
docker compose up -d
```

本地环境可通过 `.tools/bin/pip install --upgrade gallery-dl yt-dlp` 更新，再检查设置页中的工具版本。更新后需要重新验证实际账号流程；固定版本不保证未来 X 接口兼容。

## 验证

```sh
npm --prefix frontend ci
npm --prefix frontend run build
cargo fmt --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked
cargo build --locked --release
cd frontend
npx playwright install chromium
npm run test:e2e
```

浏览器测试会启动独立的临时服务（端口 3101），使用 `tests/fixtures` 中明确标识的离线工具契约和实际生成的 PNG/H.264 媒体，测试真实 API、SQLite、任务执行、FFmpeg、预览、标签和关系差异。该服务和测试专用接口不会编入正式二进制，不使用用户数据或真实 Cookie。截图/失败追踪位于 `frontend/test-results`。

构建并加载多架构镜像后可执行 `node scripts/verify-container.mjs x-archive:local`，用可销毁卷验证两个平台的启动、权限、鉴权和重启持久化。AMD64 在 ARM 主机上是模拟运行，不代表在 AMD64 实机验证。

### 常见问题

| 现象 | 处理 |
| --- | --- |
| Cookie 文件格式被拒绝 | 导出 x.com 的 Netscape 文本，确认包含未过期的 `auth_token` 和 `ct0` |
| Cookie 失效或内容无权访问 | 在浏览器确认账号仍能查看目标，更新 Cookie 后检测；账号权限不足的内容不能归档 |
| X 频率限制 | 等待任务标示的重试时间，延长同步间隔，降低并发 |
| 下载工具启动失败 | 设置页检查 gallery-dl、yt-dlp、FFmpeg 和 FFprobe；检查 PATH 或可执行文件变量 |
| 提取或媒体处理失败 | 检查网络、工具版本、存储空间；任务页查看安全摘要并重试 |
| 关系快照一直为部分 | 检查抓取数与预期计数，账号可见性或接口可能截断；部分结果不用于取关判断 |
| 重启后要求重新登录 | Web 登录会话按进程生成；Cookie 文件、归档和设置仍持久保存 |
| 无法写入挂载目录 | 检查 UID/GID `10001` 的读写权限，避免将同一数据卷交给多个实例 |

V1 不包含数据导出、备份功能、高级统计和关系图。
