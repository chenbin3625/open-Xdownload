# open-Xdownload

<p align="center">
  <img src="docs/assets/icon.png" width="96" height="96" alt="open-Xdownload icon">
</p>

open-Xdownload 是一个本地优先的 X / Twitter 媒体下载器。内置 Web 界面与后台任务队列，支持单条推文、用户、列表、关注四种任务类型，提供批量任务、定时计划、增量归档、失败重试、Cookie 池与本地 / SMB / WebDAV 多存储后端。

## 功能概览

- 单条推文解析与下载：输入 `x.com` 或 `twitter.com` 推文链接，解析正文、作者和媒体列表，下载图片、视频、GIF 的最佳可用版本。
- 用户媒体归档：按用户名、`@用户名` 或用户 ID 归档该用户媒体时间线中的推文媒体。
- 列表归档：输入 X 列表 ID，自动获取列表成员并归档成员发布的媒体。
- 关注归档：输入某个账号，自动获取它关注的账号并归档这些账号发布的媒体。
- 批量任务：用户、列表、关注目标可以一次输入多行，单次最多创建 200 个任务，并会自动去重。
- 定时计划：可把用户、列表、关注目标保存为自动归档计划，支持启用、停用、立即运行和删除。
- 增量归档：首次归档扫描用户媒体时间线，后续从上次成功位置继续；已下载过且仍存在的媒体会自动跳过。
- 工作台任务列表：任务状态、进度、错误信息和下载记录直接在工作台展示，支持取消任务、重新执行、复制文件路径或失败媒体地址。
- 媒体归档库：按图片、视频、GIF 分类查看已下载媒体，顶部显示各分类数量，并支持文件名、媒体地址和推文号搜索。
- 用户分组筛选：归档媒体展示用户名和昵称，可按用户分组筛选；历史记录会根据归档目录自动识别用户。
- 轻量媒体预览：图片使用懒加载；视频默认只加载缩略图，点击后在放大预览中使用播放器组件播放，不在卡片中加载视频播放器。
- 预览快捷切换：打开媒体预览后可使用左右方向键或预览窗口按钮切换文件。
- 部分失败识别：归档任务中有媒体下载失败时会明确标记为“部分失败”，不会与完全成功的任务混在一起。
- 失败重试：批量归档中可重试的失败推文会进入失败队列，可在独立抽屉中手动重试，也可开启任务结束后的自动重试；重新执行任务会创建新任务并保留原任务记录。
- Cookie 池：支持主 Cookie 和多组备用 Cookie，用于列表、用户、关注归档以及 API 限流轮换。
- 多存储后端：支持本地目录、SMB 共享和 WebDAV；本地目录可在配置界面中浏览、直接输入或创建。

## 任务类型

| 类型 | 输入示例 | 是否需要 X Cookie | 说明 |
| --- | --- | --- | --- |
| 单条推文 | `https://x.com/user/status/1234567890` | 否 | 解析并下载单条推文中的媒体。 |
| 用户 | `openai`、`@openai`、`44196397` | 是 | 归档指定用户媒体时间线中的媒体。 |
| 列表 | `1234567890` | 是 | 获取列表成员，并归档成员发布的媒体。 |
| 关注 | `openai`、`@openai`、`44196397` | 是 | 获取目标账号关注的用户，并归档这些用户发布的媒体。 |

## 界面预览

以下截图使用演示数据生成，不包含真实账号、Cookie、下载路径或任务记录。

![工作台任务列表](docs/screenshots/workbench.png)

![配置页](docs/screenshots/batch-archive-drawer.png)

## Docker Compose 部署

推荐用 Docker Compose 运行。新建 `compose.yml`：

```yaml
services:
  open-xdownload:
    image: chenbin3625/open-xdownload:latest
    container_name: open-xdownload
    restart: unless-stopped
    ports:
      - "127.0.0.1:8787:8787"
    environment:
      OPEN_XDOWNLOAD_ADDR: 0.0.0.0:8787
      OPEN_XDOWNLOAD_DATA_DIR: /data
      OPEN_XDOWNLOAD_DOWNLOAD_DIR: /downloads
      TZ: Asia/Shanghai
      PUID: ${PUID:-1000}
      PGID: ${PGID:-1000}
    volumes:
      - ./data:/data
      - ./downloads:/downloads
```

启动：

```bash
docker compose up -d
```

访问：

```text
http://127.0.0.1:8787
```

升级镜像：

```bash
docker compose pull
docker compose up -d
```

停止服务：

```bash
docker compose down
```

## Docker 运行

也可以直接使用 `docker run`：

```bash
docker run -d \
  --name open-xdownload \
  --restart unless-stopped \
  -p 127.0.0.1:8787:8787 \
  -e OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787 \
  -e OPEN_XDOWNLOAD_DATA_DIR=/data \
  -e OPEN_XDOWNLOAD_DOWNLOAD_DIR=/downloads \
  -e TZ=Asia/Shanghai \
  -e PUID="$(id -u)" \
  -e PGID="$(id -g)" \
  -v "$PWD/data:/data" \
  -v "$PWD/downloads:/downloads" \
  chenbin3625/open-xdownload:latest
```

Linux 宿主机建议让 `PUID` / `PGID` 与当前用户一致，避免挂载目录里的数据库和下载文件变成无法直接读写的容器用户文件。Compose 示例默认使用 `1000:1000`；如果你的用户不是这个 UID/GID，可以在同目录新建 `.env`：

```env
PUID=1000
PGID=1000
```

如果历史版本已经生成了错误归属的数据库文件，新镜像启动时会自动递归修正 `/data`。`/downloads` 目录只在顶层目录所有者不匹配时自动修正，避免大目录每次启动都被完整扫描；如果下载目录内部旧文件或子目录仍有权限问题，可临时增加 `OPEN_XDOWNLOAD_FORCE_CHOWN=1` 启动一次。

## 二进制运行

从 Release 下载对应系统的 `open-xdownload` 后运行：

```bash
./open-xdownload
```

默认监听 `127.0.0.1:8787`（仅本机可访问），数据目录为当前目录下的 `data`，下载目录为当前目录下的 `downloads`。

如需指定路径：

```bash
OPEN_XDOWNLOAD_DATA_DIR=/path/to/data \
OPEN_XDOWNLOAD_DOWNLOAD_DIR=/path/to/downloads \
./open-xdownload -addr 0.0.0.0:8787
```

可用服务参数：

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-addr` | `OPEN_XDOWNLOAD_ADDR` | `127.0.0.1:8787` | HTTP 监听地址。服务无内置鉴权，默认只绑定本机；如需局域网访问可设为 `0.0.0.0:8787`，并建议前置带鉴权的反向代理。 |
| `-data-dir` | `OPEN_XDOWNLOAD_DATA_DIR` | `data` | SQLite 数据库目录，数据库文件为 `open-xdownload.db`。 |
| `-web-dir` | `OPEN_XDOWNLOAD_WEB_DIR` | `apps/web/dist` | 前端静态文件目录；目录不存在时使用二进制内置的 Web UI。 |
| 无 | `OPEN_XDOWNLOAD_DOWNLOAD_DIR` | 当前目录下的 `downloads` | 首次生成配置时使用的默认下载目录。 |

## 首次配置

启动服务并打开 Web UI 后，进入“配置”页面：

1. 选择存储方式。
2. 如果使用本地目录，可浏览、直接输入或创建下载目录；Docker 部署时通常保持 `/downloads`。
3. 如果使用 SMB，填写主机、端口、共享名、目录、域、用户名和密码。
4. 如果使用 WebDAV，填写服务地址、目录、用户名和密码。
5. 如访问 X 或下载媒体需要代理，填写代理地址，例如 `http://127.0.0.1:7890`。
6. 设置最大并发、文件命名方式和最大文件名长度。
7. 如需用户、列表、关注归档，填写 X Cookie：`auth_token` 和 `ct0`。
8. 如有多个账号 Cookie，在“备用 Cookie”中按组填写，用于批量归档时轮换。
9. 点击“保存配置”，再点击“校验登录”确认 Cookie 可用。

敏感字段读取时会显示为 `********`。再次保存配置时，留空或保持 `********` 不会覆盖已有密钥或密码。

## 使用教程

### 下载单条推文媒体

1. 打开“工作台”。
2. 在“单条解析”中粘贴推文链接。
3. 点击“解析”，确认识别出的媒体列表。
4. 点击“下载媒体”创建任务。
5. 在工作台的“任务列表”查看进度，完成后展开任务可复制文件路径。

单条推文解析通常不需要配置 X Cookie，但私密、删除、受限或无法公开访问的推文可能无法解析。

### 批量归档用户、列表或关注关系

1. 先在“配置”页面填写并校验 X Cookie。
2. 回到“工作台”，点击“批量归档”打开抽屉。
3. 在“用户”“列表”“关注”标签页中输入目标，每行一个。
4. 右侧确认任务预览。
5. 点击“批量下载”创建任务。

用户目标支持用户名、`@用户名` 和用户 ID；列表目标使用列表 ID；关注目标表示“归档这个账号关注的人发布的媒体”。

### 创建定时归档计划

1. 在“批量归档”抽屉中输入用户、列表或关注目标。
2. 填写计划名称。
3. 设置执行间隔，范围为 5 分钟到 43200 分钟。
4. 点击“保存计划”。
5. 在右侧“定时计划”中可启用、停用、立即运行或删除。

定时计划只支持用户、列表和关注目标，不支持单条推文链接。每个计划最多包含 200 个目标。如果上次计划创建的任务仍在运行，下一次执行会顺延。

### 处理失败任务

- 失败、取消、完成或部分失败的任务，可在工作台“任务列表”点击“重新执行”；系统会新建任务并保留原任务历史。
- 运行中的任务可点击“取消”。
- 归档任务只要存在下载失败项，就会显示为“部分失败”。
- 批量归档中可重试的失败推文会进入“失败推文队列”，可从任务列表上方的“查看失败项”打开。
- 可在失败队列抽屉中点击“全部重试”，也可删除单条失败记录或清空队列。
- 配置中的“失败重试”开启后，批量归档任务结束时会自动尝试重试失败队列。

### 浏览媒体归档库

1. 打开侧边栏中的“媒体归档库”。
2. 使用顶部分类标签按全部、图片、视频或 GIF 筛选；括号中的数字是当前归档数量。
3. 使用用户下拉框按用户名分组筛选，或搜索文件名、媒体地址和推文号。
4. 点击图片或视频缩略图打开大图预览；视频会在预览窗口中加载播放器并自动播放。
5. 在预览窗口中使用左右方向键切换相邻文件。

视频缩略图优先使用 X 返回的公开预览图；历史记录或没有缩略图的文件会自动尝试生成对应的 Twitter CDN 预览地址，失败时显示轻量占位图。

## 存储和文件目录

本地存储默认目录结构：

```text
downloads/
  users/
    用户名或昵称/
      媒体文件
  lists/
    列表名(列表ID)/
      指向用户目录的链接
```

说明：

- 用户归档和关注归档会把媒体保存到 `users/用户名或昵称/`。
- 列表归档会创建 `lists/列表名(列表ID)/`，其中包含指向用户目录的链接，避免同一用户媒体被复制多份。
- SMB 和 WebDAV 使用相同的逻辑路径，但不会创建本地符号链接。
- 文件名会清理系统不支持的字符，并受“最大文件名长度”限制。
- 图片会尽量下载大图版本，视频和 GIF 会选择最高码率 MP4。
- 视频预览图地址会随下载记录保存；升级到新版本后，历史 Twitter 视频记录会在媒体归档库接口中自动补齐可用缩略图。

## 配置项说明

| 配置项 | 说明 |
| --- | --- |
| 存储类型 | 可选本地目录、SMB、WebDAV。 |
| 下载目录 | 本地存储根目录，可在界面中浏览、输入或创建。Docker 部署时建议映射到宿主机目录。 |
| 代理 | 用于 X API 请求和媒体下载。留空则直连。 |
| 并发 | 后台任务最大并发数，范围 `1-64`。 |
| 文件名命名 | 可选“仅推文”或“用户名 + 用户 ID + 推文”。 |
| 最大文件名长度 | 范围 `16-240`。 |
| X Cookie | 主 `auth_token` 和 `ct0`，用于登录态接口。 |
| 备用 Cookie | 多组备用 `auth_token` / `ct0`，用于批量归档轮换。 |
| 失败重试 | 批量归档结束时自动重试失败推文队列。 |
| 保护账号自动关注 | 遇到未关注的保护账号时尝试自动关注。 |

## 数据备份

建议定期备份：

```text
data/open-xdownload.db
downloads/
```

其中 `open-xdownload.db` 保存配置、任务、下载记录、用户、列表、定时计划和失败队列；`downloads/` 保存实际媒体文件。使用 SMB 或 WebDAV 时，实际媒体文件位于对应远程存储中。

## 安全说明

open-Xdownload 面向个人本地归档场景，服务本身没有内置用户登录和访问鉴权。默认只监听本机回环地址 `127.0.0.1:8787`；如需局域网访问，需通过 `-addr 0.0.0.0:8787` 或 `OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787` 显式放开（Docker 镜像为便于端口映射默认设置了 `0.0.0.0`，发布端口时请自行限制），并把服务放在带鉴权的反向代理后面。

Docker Compose 示例中的端口映射为：

```yaml
ports:
  - "127.0.0.1:8787:8787"
```

这表示只有宿主机本机可以访问。如需暴露到局域网或公网，请先配置反向代理、访问控制和 HTTPS。

请确认你有权下载和保存相关内容，并遵守 X / Twitter 的服务条款、目标站点规则以及所在地法律法规。

---

# open-Xdownload

open-Xdownload is a local-first X / Twitter media downloader. It comes with a built-in Web UI and background task queue, supports four task types — single posts, users, lists, and followed accounts — and offers batch tasks, scheduled plans, incremental archiving, failure retry, a cookie pool, and local / SMB / WebDAV storage backends.

## Features

- Single-post parsing & download: Paste an `x.com` or `twitter.com` post link to parse its text, author, and media list, and download the best available version of each image, video, or GIF.
- User media archiving: Archive media from a user's media timeline by username, `@username`, or user ID.
- List archiving: Enter an X list ID to automatically fetch its members and archive media posted by them.
- Followed-account archiving: Enter an account to automatically fetch the accounts it follows and archive media posted by those accounts.
- Batch tasks: Users, lists, and followed accounts can be entered one per line in a single batch — up to 200 tasks at once, with automatic deduplication.
- Scheduled plans: Save users, lists, or followed accounts as automatic archiving plans, with support for enable, disable, run now, and delete.
- Incremental archiving: The first run scans a user's full media timeline; later runs resume from the last successful position. Media already downloaded and still present is skipped automatically.
- Workbench task list: Task status, progress, errors, and download records are shown directly in the workbench, with support for cancelling tasks, re-running them, and copying file paths or failed media URLs.
- Media library: Browse downloaded media by image, video, or GIF category, with counts on each tab and search across file names, media URLs, and post IDs.
- User grouping and filtering: Archived media shows the author's username and display name, supports filtering by user, and infers users from archive directories for older records.
- Lightweight previews: Images use lazy loading; video cards load only a poster by default and mount the player component only after opening the enlarged preview.
- Preview shortcuts: Use the left and right arrow keys, or the preview window buttons, to move between files.
- Partial-failure detection: An archiving task with any failed media download is clearly marked "partial failure" instead of being lumped in with fully successful tasks.
- Failure retry: Retryable failed posts from a batch archive go into a failure queue that can be retried manually in a dedicated drawer, or automatically when a task finishes; re-running a task creates a new task and keeps the original record.
- Cookie pool: Supports a primary cookie plus multiple backup cookie groups, used for list / user / followed-account archiving and API rate-limit rotation.
- Multiple storage backends: Supports local directories, SMB shares, and WebDAV; local directories can be browsed, typed in, or created in the configuration UI.

## Task Types

| Type | Example input | X Cookie required | Description |
| --- | --- | --- | --- |
| Single post | `https://x.com/user/status/1234567890` | No | Parses and downloads media from a single post. |
| User | `openai`, `@openai`, `44196397` | Yes | Archives media from the given user's media timeline. |
| List | `1234567890` | Yes | Fetches list members and archives media they post. |
| Followed | `openai`, `@openai`, `44196397` | Yes | Fetches the accounts the target follows and archives media posted by them. |

## Interface Preview

The screenshots below were generated with demo data and contain no real accounts, cookies, download paths, or task records.

![Workbench task list](docs/screenshots/workbench.png)

![Configuration page](docs/screenshots/batch-archive-drawer.png)

## Docker Compose Deployment

Docker Compose is the recommended way to run. Create a `compose.yml`:

```yaml
services:
  open-xdownload:
    image: chenbin3625/open-xdownload:latest
    container_name: open-xdownload
    restart: unless-stopped
    ports:
      - "127.0.0.1:8787:8787"
    environment:
      OPEN_XDOWNLOAD_ADDR: 0.0.0.0:8787
      OPEN_XDOWNLOAD_DATA_DIR: /data
      OPEN_XDOWNLOAD_DOWNLOAD_DIR: /downloads
      TZ: Asia/Shanghai
      PUID: ${PUID:-1000}
      PGID: ${PGID:-1000}
    volumes:
      - ./data:/data
      - ./downloads:/downloads
```

Start:

```bash
docker compose up -d
```

Access:

```text
http://127.0.0.1:8787
```

Upgrade the image:

```bash
docker compose pull
docker compose up -d
```

Stop the service:

```bash
docker compose down
```

## Docker Run

You can also run directly with `docker run`:

```bash
docker run -d \
  --name open-xdownload \
  --restart unless-stopped \
  -p 127.0.0.1:8787:8787 \
  -e OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787 \
  -e OPEN_XDOWNLOAD_DATA_DIR=/data \
  -e OPEN_XDOWNLOAD_DOWNLOAD_DIR=/downloads \
  -e TZ=Asia/Shanghai \
  -e PUID="$(id -u)" \
  -e PGID="$(id -g)" \
  -v "$PWD/data:/data" \
  -v "$PWD/downloads:/downloads" \
  chenbin3625/open-xdownload:latest
```

On a Linux host, set `PUID` / `PGID` to match your current user so the database and downloaded files inside the mounted directories don't become container-user files you can't read or write directly. The Compose example defaults to `1000:1000`; if your user isn't that UID/GID, create a `.env` in the same directory:

```env
PUID=1000
PGID=1000
```

If a previous version already created a database file with wrong ownership, the new image recursively fixes `/data` at startup. The `/downloads` directory is only auto-fixed when the top-level directory owner mismatches, so a large directory isn't fully scanned on every startup; if old files or subdirectories inside the download directory still have permission issues, start once with `OPEN_XDOWNLOAD_FORCE_CHOWN=1`.

## Binary Run

Download the `open-xdownload` binary for your platform from the Releases page and run:

```bash
./open-xdownload
```

It listens on `127.0.0.1:8787` (local-only) by default, with the data directory at `data` and the download directory at `downloads` in the current directory.

To specify paths:

```bash
OPEN_XDOWNLOAD_DATA_DIR=/path/to/data \
OPEN_XDOWNLOAD_DOWNLOAD_DIR=/path/to/downloads \
./open-xdownload -addr 0.0.0.0:8787
```

Available service flags:

| Flag | Environment variable | Default | Description |
| --- | --- | --- | --- |
| `-addr` | `OPEN_XDOWNLOAD_ADDR` | `127.0.0.1:8787` | HTTP listen address. The service has no built-in auth, so it binds to localhost by default; set `0.0.0.0:8787` for LAN access, preferably behind an authenticating reverse proxy. |
| `-data-dir` | `OPEN_XDOWNLOAD_DATA_DIR` | `data` | SQLite database directory; the database file is `open-xdownload.db`. |
| `-web-dir` | `OPEN_XDOWNLOAD_WEB_DIR` | `apps/web/dist` | Frontend static file directory; falls back to the built-in Web UI if the directory doesn't exist. |
| n/a | `OPEN_XDOWNLOAD_DOWNLOAD_DIR` | `downloads` in the current directory | Default download directory used when the config is first generated. |

## First-Time Setup

After starting the service and opening the Web UI, go to the "Configuration" page:

1. Choose a storage type.
2. For local storage, browse, type in, or create the download directory; for Docker deployments this is usually `/downloads`.
3. For SMB, enter host, port, share, directory, domain, username, and password.
4. For WebDAV, enter the service URL, directory, username, and password.
5. If you need a proxy to reach X or download media, set a proxy address such as `http://127.0.0.1:7890`.
6. Set the max concurrency, filename pattern, and max filename length.
7. For user / list / followed-account archiving, fill in your X Cookie: `auth_token` and `ct0`.
8. If you have cookies for multiple accounts, enter them as groups under "Backup cookies" for rotation during batch archiving.
9. Click "Save configuration", then "Verify login" to confirm the cookies work.

Sensitive fields are shown as `********` when read. Leaving them empty or as `********` on a later save won't overwrite the existing keys or passwords.

## Usage Guide

### Download media from a single post

1. Open the workbench.
2. Paste a post link into "Single post parsing".
3. Click "Parse" and confirm the recognized media list.
4. Click "Download media" to create a task.
5. Track progress in the "Task list" in the workbench; expand a finished task to copy its file path.

Parsing a single post usually doesn't require an X Cookie, but private, deleted, restricted, or otherwise non-publicly-accessible posts may fail to parse.

### Batch archive users, lists, or followed accounts

1. First fill in and verify your X Cookie on the "Configuration" page.
2. Back in the workbench, open the "Batch archive" drawer.
3. In the "Users", "Lists", or "Followed" tabs, enter your targets, one per line.
4. Review the task preview on the right.
5. Click "Batch download" to create the tasks.

User targets accept usernames, `@username`, and user IDs; list targets use list IDs; a followed target means "archive media posted by the accounts this account follows".

### Create a scheduled archiving plan

1. Enter user, list, or followed targets in the "Batch archive" drawer.
2. Fill in a plan name.
3. Set the execution interval, from 5 to 43200 minutes.
4. Click "Save plan".
5. Plans can be enabled, disabled, run now, or deleted from the "Scheduled plans" list on the right.

Scheduled plans only support user, list, and followed targets — not single post links. Each plan can contain up to 200 targets. If the task from the previous run is still running, the next execution is deferred.

### Handle failed tasks

- Failed, cancelled, completed, or partially failed tasks can be re-run from the "Task list" in the workbench; the system creates a new task and keeps the original history.
- Running tasks can be cancelled.
- An archiving task is shown as "partial failure" whenever it has any failed download.
- Retryable failed posts from a batch archive go into a "Failed post queue", opened via "View failures" above the task list.
- In the failure-queue drawer you can retry all entries, delete individual records, or clear the queue.
- With "Failure retry" enabled in configuration, batch archiving tasks automatically try to retry the failure queue when they finish.

### Browse the Media Library

1. Open **Media Library** from the sidebar.
2. Use the category tabs to filter all files, images, videos, or GIFs; the number in parentheses is the current archive count.
3. Use the user selector to filter by author, or search file names, media URLs, and post IDs.
4. Click an image or video poster to open the enlarged preview. Videos mount the player component in the preview window and start playing automatically.
5. Use the left and right arrow keys to move between adjacent files.

Video posters use the public preview URL returned by X when available. Older Twitter video records are automatically given a matching CDN thumbnail URL by the library API; records without a usable poster fall back to a lightweight placeholder.

## Storage & Directory Structure

Default local storage layout:

```text
downloads/
  users/
    username-or-display-name/
      media files
  lists/
    list-name(list-id)/
      link to the user directory
```

Notes:

- User and followed-account archiving save media to `users/username-or-display-name/`.
- List archiving creates `lists/list-name(list-id)/` containing links to user directories, so the same user's media isn't copied multiple times.
- SMB and WebDAV use the same logical paths but don't create local symlinks.
- Filenames are cleaned of characters the system doesn't support and are limited by the max filename length.
- Images are downloaded at the largest available size where possible; videos and GIFs pick the highest-bitrate MP4.
- Video poster URLs are stored with download records; after upgrading, historical Twitter video records are backfilled with usable CDN thumbnails by the media library API.

## Configuration Options

| Option | Description |
| --- | --- |
| Storage type | Local directory, SMB, or WebDAV. |
| Download directory | Root directory for local storage; can be browsed, entered, or created in the UI. For Docker, map it to a host directory. |
| Proxy | Used for X API requests and media downloads; leave empty for a direct connection. |
| Concurrency | Max concurrency for background tasks, range `1-64`. |
| Filename pattern | Either "post only" or "username + user ID + post". |
| Max filename length | Range `16-240`. |
| X Cookie | Primary `auth_token` and `ct0`, used for authenticated APIs. |
| Backup cookies | Additional `auth_token` / `ct0` groups, rotated during batch archiving. |
| Failure retry | Automatically retries the failed-post queue when batch archiving finishes. |
| Auto-follow protected accounts | Tries to auto-follow protected accounts it hasn't followed yet when encountered. |

## Data Backup

Back up periodically:

```text
data/open-xdownload.db
downloads/
```

`open-xdownload.db` stores config, tasks, download records, users, lists, scheduled plans, and the failure queue; `downloads/` holds the actual media files. With SMB or WebDAV, the actual media files live on the corresponding remote storage.

## Security Notes

open-Xdownload targets personal, local archiving; the service itself has no built-in user login or access control. It binds to the loopback address `127.0.0.1:8787` by default. LAN access requires explicitly opting in with `-addr 0.0.0.0:8787` or `OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787` (the Docker image sets `0.0.0.0` by default for port mapping — restrict the published port accordingly), and the service should then sit behind a reverse proxy with authentication.

The Docker Compose example maps the port as:

```yaml
ports:
  - "127.0.0.1:8787:8787"
```

This means only the host machine can reach it. To expose it to a LAN or the public internet, configure a reverse proxy, access control, and HTTPS first.

Please make sure you have the right to download and keep the content, and comply with X / Twitter's terms of service, the rules of the target sites, and applicable local laws.
