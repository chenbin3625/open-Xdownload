# X Archive

将 X / Twitter 推文、原始图片、视频、GIF 和关注关系保存在自己的设备上。通过浏览器管理归档，使用自己的 X 登录会话，无需申请 X API Key。

[下载二进制](https://github.com/chenbin3625/x-archive/releases) · [Docker 镜像](https://github.com/chenbin3625/x-archive/pkgs/container/x-archive) · [更新记录](CHANGELOG.md) · [MIT License](LICENSE)

## 功能

- **用户归档**：添加用户名或主页链接，保存可访问的本人推文与回复；支持定时增量同步、立即同步、暂停和去重。
- **单条导入**：粘贴 X / Twitter 推文链接，保存正文和媒体；纯文字推文同样入库。
- **本地媒体库**：原图缩放、视频和 GIF 播放，按用户、日期、类型、收藏和标签筛选，随时返回原推文。
- **分类存储**：原始媒体、缩略图和视频封面按作者用户名分目录，图片与视频根目录可在设置中调整。
- **关注关系**：手动或定时保存 Following / Followers 快照，浏览历史，比较完整快照的新增和取消关系。
- **任务管理**：按导入或用户同步汇总媒体下载数量、大小与失败原因，支持取消、重试、下载并发和重启后继续处理。
- **独立部署**：一个 Rust 服务提供 Web 界面和 API，SQLite 存储；Docker 镜像包含全部下载工具，无需外部数据库或 Redis。

![媒体库桌面界面，使用离线测试数据](docs/screenshots/desktop-media.png)

归档范围取决于当前账号权限和 X 实际返回的内容，不保证获取全部历史。部分关系快照可查看成员，但不用于推断取关。

## Docker Compose 部署

推荐使用 Docker Engine 和 Compose，支持 `linux/amd64`、`linux/arm64`。无需安装 Rust、Node.js、Python 或 FFmpeg。

```sh
mkdir x-archive
cd x-archive
curl -fsSLO https://raw.githubusercontent.com/chenbin3625/x-archive/main/compose.yaml
curl -fsSL https://raw.githubusercontent.com/chenbin3625/x-archive/main/.env.example -o .env
```

编辑 `.env`，将 `X_ARCHIVE_PASSWORD` 改为自己的访问密码，至少 12 位。然后启动：

```sh
docker compose pull
docker compose up -d
docker compose logs -f x-archive
```

打开 <http://127.0.0.1:3000>，使用访问密码登录。默认只发布到本机回环地址，远程访问可配置 [HTTPS 反向代理或 SSH 隧道](docs/OPERATIONS.md#远程访问)。

默认 [compose.yaml](compose.yaml) 使用两个命名卷保存数据和配置。需要直接挂载宿主机目录时，参考 [绑定目录示例](examples/compose.bind.yaml)；已有目录和内容需允许 UID/GID `10001` 读写。

镜像标签：`latest` 为最近稳定版，`1.1.0` 为指定版本，`1.1` / `1` 跟随对应版本系列，`edge` 跟随 `main`。可在 `.env` 中固定版本：

```dotenv
X_ARCHIVE_IMAGE=ghcr.io/chenbin3625/x-archive:1.1.0
```

更新时，先备份数据和配置；若固定版本，先修改镜像标签，再运行：

```sh
docker compose pull
docker compose up -d
```

`docker compose down` 保留数据；**`docker compose down -v` 会删除数据卷**。

### 从源码构建镜像

在仓库根目录配置 `.env` 后运行：

```sh
docker compose -f compose.yaml -f compose.build.yaml up -d --build
```

[compose.build.yaml](compose.build.yaml) 使用本地源码构建，不需要先拉取发布镜像。双架构构建见 [开发文档](docs/DEVELOPMENT.md#docker-构建)。

## 二进制部署

在 [Releases](https://github.com/chenbin3625/x-archive/releases) 下载对应平台的压缩包，并用同页的 `SHA256SUMS` 校验。包名为 `x-archive-v<版本>-<目标平台>.tar.gz`，Windows 使用 `.zip`。

| 平台 | 目标平台 |
| --- | --- |
| Linux x86-64 | `x86_64-unknown-linux-musl` |
| Linux ARM64 | `aarch64-unknown-linux-musl` |
| macOS Intel | `x86_64-apple-darwin` |
| macOS Apple Silicon | `aarch64-apple-darwin` |
| Windows x86-64 | `x86_64-pc-windows-msvc` |

前端已嵌入二进制，运行无需 Rust 或 Node.js。下载功能需要 **Python 3.9+、gallery-dl、yt-dlp、FFmpeg 和 FFprobe**；FFmpeg/FFprobe 需安装到 PATH，Python 工具版本见包内 `requirements-tools.txt`。

### Linux / macOS

安装 Python 和 FFmpeg，解压并进入发布包目录后：

```sh
python3 -m venv .tools
.tools/bin/pip install -r requirements-tools.txt
chmod +x x-archive
PATH="$PWD/.tools/bin:$PATH" ./x-archive
```

### Windows（PowerShell）

安装 Python 和 FFmpeg，解压并进入发布包目录后：

```powershell
py -3 -m venv .tools
.\.tools\Scripts\python.exe -m pip install -r requirements-tools.txt
$env:PATH = "$PWD\.tools\Scripts;$env:PATH"
.\x-archive.exe
```

两种方式默认均在 <http://127.0.0.1:3000> 提供服务，数据和配置保存在当前工作目录的 `data/`、`config/`。直接启动二进制**不会自动读取 `.env`**，需通过 shell、服务管理器或系统配置传入环境变量。长期运行示例见 [运维文档](docs/OPERATIONS.md#长期运行二进制)。

## 首次使用

1. **设置 X Cookie**：在浏览器登录自己的 X 账号。开发者工具中打开 Chrome / Edge 的 **Application → Storage → Cookies → https://x.com**，或 Firefox 的 **Storage → Cookies → https://x.com**，复制同一会话中 `auth_token` 和 `ct0` 的 **Value**。
2. **保存并检测**：进入“设置 → X 登录 Cookie”，分别粘贴到同名输入框，只填值，无需名称前缀或整个 Cookie 请求头。点击“保存并检测”，等待任务确认会话有效。保存后输入框清空，已存值不会回显。
3. **开始归档**：在“用户归档”添加用户名、`@用户名` 或主页链接；也可以使用“导入推文”粘贴单条链接。开启自动下载后，媒体会进入下载队列。
4. **浏览媒体**：在“媒体库”筛选用户、日期和类型，查看原图或播放视频，添加收藏与标签；失败任务可在任务页查看原因并重试。
5. **保存关系快照**：在“关注关系”选择用户和 Following / Followers，手动抓取或在设置中启用定时快照。只有同一用户、同一方向的两份完整快照可比较。

Cookie 格式通过校验不等于登录会话有效。过期时重新复制、保存并检测；删除 Cookie 会取消未结束任务，已归档内容保留。只归档自己有权访问的内容。

## 数据与配置

```text
data/
  archive.sqlite          推文、媒体索引、任务、快照、收藏与标签
  images/<username>/      图片与原生 GIF
  videos/<username>/      视频与 MP4 GIF
  thumbnails/<username>/  缩略图与视频封面
config/
  settings.json           Web 设置
  cookies.txt             私有 X 会话
```

Docker 中对应 `/data`、`/config`。新下载文件按小写用户名分类；修改保存目录只影响后续下载，不迁移旧文件。每个数据目录只能运行一个实例。备份时先停止服务，再备份完整数据目录和配置目录。

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `X_ARCHIVE_BIND` | `127.0.0.1:3000` | 监听地址，镜像内为 `0.0.0.0:3000` |
| `X_ARCHIVE_PASSWORD` | 无 | Web 访问密码；对外监听时必填且至少 12 位 |
| `X_ARCHIVE_DATA` | `data` | 数据目录，镜像内为 `/data` |
| `X_ARCHIVE_CONFIG` | `config` | 配置目录，镜像内为 `/config` |
| `GALLERY_DL_BIN` | `gallery-dl` | gallery-dl 可执行文件路径 |
| `YT_DLP_BIN` | `yt-dlp` | yt-dlp 可执行文件路径 |
| `RUST_LOG` | `x_archive=info,tower_http=warn` | 日志级别 |

Compose 另支持在 `.env` 中配置 `X_ARCHIVE_IMAGE`、`X_ARCHIVE_HOST` 和 `X_ARCHIVE_PORT`，默认地址和端口为 `127.0.0.1:3000`。

## 常见问题

| 问题 | 处理 |
| --- | --- |
| Cookie 无效或内容无权访问 | 浏览器确认能访问目标，更新同一会话的两个 Cookie 后重新检测 |
| 遇到 X 限流 | 等待任务中的重试时间，延长同步间隔 |
| 下载或媒体处理失败 | 设置页检查四项工具、网络和存储空间；X 接口变化时可能需要更新下载工具 |
| 关系快照为“部分” | X 返回数量可能不完整或抓取期间发生变化；部分结果不用于判断取关 |
| 重启后需要登录 | Web 登录会话随进程重置；X Cookie、设置和归档仍持久保存 |

更多说明：[会话、归档边界与运维](docs/OPERATIONS.md) · [源码开发、测试与发布](docs/DEVELOPMENT.md) · [验证记录](docs/VERIFICATION.md)。
