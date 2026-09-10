# open-Xdownload

open-Xdownload is a local-first X / Twitter media downloader with a built-in Web UI and background task queue. It is built for people who want to archive posts, user media timelines, lists, and followed accounts to their own disk — running it as a container on a home server or NAS, with your own cookies and your own storage.

![Workbench task list](https://raw.githubusercontent.com/chenbin3625/open-Xdownload/main/docs/screenshots/workbench.png)

## Quick Start

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

Then open `http://127.0.0.1:8787` and go to the **Configuration** page.

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

```bash
docker compose up -d      # start
docker compose pull       # upgrade
docker compose up -d
docker compose down       # stop
```

On a Linux host, set `PUID` / `PGID` to match your current user so the database and downloaded files inside the mounted directories don't become container-user files you can't read or write directly. Create a `.env` next to `compose.yml`:

```env
PUID=1000
PGID=1000
```

## Image Tags

| Tag | Description |
| --- | --- |
| `latest` | Latest build from the default branch (`main`). Recommended for normal use. |
| `main` | Branch build of `main`; same source as `latest`, rebuilt on every push. |
| `v*` | Release tags, e.g. `v1.2.3`, published from git tags. Pin one for a stable, reproducible deployment. |
| `sha-<short>` | Immutable build of a specific commit, e.g. `sha-1a2b3c4`. Useful for rollbacks. |

Images are multi-platform: `linux/amd64` and `linux/arm64`.

## Key Features

- Single-post parsing & download: paste an `x.com` or `twitter.com` post link to parse its text, author, and media list, then download the best available version of each image, video, or GIF.
- User media archiving: archive media from a user's media timeline by username, `@username`, or user ID.
- List archiving: enter an X list ID to automatically fetch its members and archive media posted by them.
- Followed-account archiving: enter an account to fetch the accounts it follows and archive media posted by those accounts.
- Batch tasks: users, lists, and followed accounts can be entered one per line — up to 200 tasks per batch, with automatic deduplication.
- Scheduled plans: save targets as automatic archiving plans with enable, disable, run now, and delete; intervals from 5 to 43200 minutes.
- Failure retry: retryable failed posts go into a failure queue that can be retried manually in a dedicated drawer, or automatically when a task finishes.
- Cookie pool: a primary cookie plus multiple backup cookie groups, rotated during batch archiving to work around API rate limits.
- Media library: browse downloaded media by image, video, or GIF, with counts per tab and search across file names, media URLs, and post IDs.
- Local-directory storage: the download directory can be browsed, typed in, or created in the configuration UI.

## Configuration

The container listens on port **8787** and sets `OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787` by default so the published port works. The binary itself binds to `127.0.0.1:8787` unless told otherwise.

| Environment variable | Default (container) | Description |
| --- | --- | --- |
| `OPEN_XDOWNLOAD_ADDR` | `0.0.0.0:8787` | HTTP listen address. |
| `OPEN_XDOWNLOAD_DATA_DIR` | `/data` | SQLite database directory; the database file is `open-xdownload.db`. |
| `OPEN_XDOWNLOAD_DOWNLOAD_DIR` | `/downloads` | Default download directory used when the config is first generated. |
| `OPEN_XDOWNLOAD_WEB_DIR` | `apps/web/dist` | Frontend static file directory; falls back to the built-in Web UI if it doesn't exist. |
| `TZ` | — | Container timezone, e.g. `Asia/Shanghai`. |
| `PUID` / `PGID` | `1000` / `1000` | UID/GID the service runs as inside the container. |
| `OPEN_XDOWNLOAD_FORCE_CHOWN` | `0` | Set to `1` to force a recursive `chown` of the data and download directories at startup. |

| Port | Purpose |
| --- | --- |
| `8787` | Web UI and HTTP API. Published as `127.0.0.1:8787:8787` in the examples. |

| Volume | Purpose |
| --- | --- |
| `/data` | SQLite database (config, tasks, download records, users, lists, scheduled plans, failure queue). |
| `/downloads` | Downloaded media files. |

**First-time setup** — open the Web UI and go to the "Configuration" page: set the download directory (`/downloads` for Docker), optionally set a proxy for X API requests and media downloads, adjust concurrency, filename pattern and max filename length, then fill in your X Cookie (`auth_token` and `ct0`) and click "Verify login". A cookie is required for user, list, and followed-account archiving; single-post parsing usually works without one.

The X Cookie and download directory are configured through the Web UI / database — they are not environment variables. Sensitive fields are shown as `********` when read, and leaving them empty or as `********` on a later save won't overwrite the stored values.

## Data Persistence

Both directories must be mounted as volumes, or everything is lost when the container is recreated:

```text
data/open-xdownload.db     # config, tasks, download records, users, lists, plans, failure queue
downloads/                 # the actual media files
```

Media is stored under `downloads/users/username-or-display-name/`; list archiving creates `downloads/lists/list-name(list-id)/` containing links to user directories, so the same user's media isn't copied multiple times.

At startup the entrypoint recursively fixes ownership of `/data`. `/downloads` is only auto-fixed when the top-level directory owner mismatches, so a large directory isn't fully scanned on every start; if old files or subdirectories inside it still have permission issues, start once with `OPEN_XDOWNLOAD_FORCE_CHOWN=1`.

## Security Notes

open-Xdownload targets personal, local archiving; **the service itself has no built-in user login or access control**. It binds to the loopback address `127.0.0.1:8787` by default. LAN access requires explicitly opting in with `-addr 0.0.0.0:8787` or `OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787`.

The Docker image sets `0.0.0.0` by default for port mapping — restrict the published port accordingly. The Compose example maps it as:

```yaml
ports:
  - "127.0.0.1:8787:8787"
```

This means only the host machine can reach it. To expose it to a LAN or the public internet, configure a reverse proxy, access control, and HTTPS first.

Please make sure you have the right to download and keep the content, and comply with X / Twitter's terms of service, the rules of the target sites, and applicable local laws.

## Links

- GitHub: https://github.com/chenbin3625/open-Xdownload
- Releases: https://github.com/chenbin3625/open-Xdownload/releases

---

# 中文

open-Xdownload 是一个本地优先的 X / Twitter 媒体下载器，内置 Web 界面与后台任务队列。它面向希望把推文、用户媒体时间线、列表和关注账号归档到自己磁盘上的用户，适合以容器方式运行在家庭服务器或 NAS 上，使用自己的 Cookie 和自己的存储。

![工作台任务列表](https://raw.githubusercontent.com/chenbin3625/open-Xdownload/main/docs/screenshots/workbench.png)

## 快速开始

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

然后打开 `http://127.0.0.1:8787`，进入「配置」页面。

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

```bash
docker compose up -d      # 启动
docker compose pull       # 升级镜像
docker compose up -d
docker compose down       # 停止服务
```

Linux 宿主机建议让 `PUID` / `PGID` 与当前用户一致，避免挂载目录里的数据库和下载文件变成无法直接读写的容器用户文件。可在 `compose.yml` 同目录新建 `.env`：

```env
PUID=1000
PGID=1000
```

## 镜像标签

| 标签 | 说明 |
| --- | --- |
| `latest` | 默认分支（`main`）的最新构建，日常使用推荐。 |
| `main` | `main` 分支构建，来源与 `latest` 相同，每次推送都会重新构建。 |
| `v*` | 发布标签，例如 `v1.2.3`，由 git tag 触发。需要稳定可复现的部署时可固定使用。 |
| `sha-<短哈希>` | 对应具体提交的不可变构建，例如 `sha-1a2b3c4`，便于回滚。 |

镜像为多平台构建：`linux/amd64` 与 `linux/arm64`。

## 功能概览

- 单条推文解析与下载：输入 `x.com` 或 `twitter.com` 推文链接，解析正文、作者和媒体列表，下载图片、视频、GIF 的最佳可用版本。
- 用户媒体归档：按用户名、`@用户名` 或用户 ID 归档该用户媒体时间线中的推文媒体。
- 列表归档：输入 X 列表 ID，自动获取列表成员并归档成员发布的媒体。
- 关注归档：输入某个账号，自动获取它关注的账号并归档这些账号发布的媒体。
- 批量任务：用户、列表、关注目标可以一次输入多行，单次最多创建 200 个任务，并会自动去重。
- 定时计划：可把目标保存为自动归档计划，支持启用、停用、立即运行和删除；执行间隔为 5 到 43200 分钟。
- 失败重试：批量归档中可重试的失败推文会进入失败队列，可在独立抽屉中手动重试，也可在任务结束时自动重试。
- Cookie 池：支持主 Cookie 和多组备用 Cookie，用于批量归档时轮换，规避 API 限流。
- 媒体归档库：按图片、视频、GIF 分类查看已下载媒体，顶部显示各分类数量，并支持文件名、媒体地址和推文号搜索。
- 本地目录存储：可在配置界面中浏览、直接输入或创建下载目录。

## 配置说明

容器监听端口 **8787**，并默认设置 `OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787` 以便端口映射生效。二进制本身在未指定时只监听 `127.0.0.1:8787`。

| 环境变量 | 容器内默认值 | 说明 |
| --- | --- | --- |
| `OPEN_XDOWNLOAD_ADDR` | `0.0.0.0:8787` | HTTP 监听地址。 |
| `OPEN_XDOWNLOAD_DATA_DIR` | `/data` | SQLite 数据库目录，数据库文件为 `open-xdownload.db`。 |
| `OPEN_XDOWNLOAD_DOWNLOAD_DIR` | `/downloads` | 首次生成配置时使用的默认下载目录。 |
| `OPEN_XDOWNLOAD_WEB_DIR` | `apps/web/dist` | 前端静态文件目录；目录不存在时使用内置 Web UI。 |
| `TZ` | — | 容器时区，例如 `Asia/Shanghai`。 |
| `PUID` / `PGID` | `1000` / `1000` | 容器内服务运行使用的 UID/GID。 |
| `OPEN_XDOWNLOAD_FORCE_CHOWN` | `0` | 设为 `1` 时在启动阶段强制递归修正数据目录和下载目录的归属。 |

| 端口 | 用途 |
| --- | --- |
| `8787` | Web UI 与 HTTP API，示例中映射为 `127.0.0.1:8787:8787`。 |

| 挂载点 | 用途 |
| --- | --- |
| `/data` | SQLite 数据库（配置、任务、下载记录、用户、列表、定时计划、失败队列）。 |
| `/downloads` | 下载的媒体文件。 |

**首次配置** —— 打开 Web UI 进入「配置」页面：设置下载目录（Docker 部署通常保持 `/downloads`），如需访问 X 或下载媒体要经过代理则填写代理地址，调整并发、文件名命名方式和最大文件名长度，然后填写 X Cookie（`auth_token` 和 `ct0`）并点击「校验登录」。用户、列表、关注归档需要 Cookie；单条推文解析通常不需要。

X Cookie 与下载目录通过 Web UI / 数据库配置，**不是环境变量**。敏感字段读取时会显示为 `********`，再次保存时留空或保持 `********` 不会覆盖已存的值。

## 数据持久化

两个目录都必须挂载为卷，否则容器重建后数据会全部丢失：

```text
data/open-xdownload.db     # 配置、任务、下载记录、用户、列表、定时计划、失败队列
downloads/                 # 实际媒体文件
```

媒体保存在 `downloads/users/用户名或昵称/`；列表归档会创建 `downloads/lists/列表名(列表ID)/`，其中包含指向用户目录的链接，避免同一用户媒体被复制多份。

启动时入口脚本会递归修正 `/data` 的归属。`/downloads` 只在顶层目录所有者不匹配时自动修正，避免大目录每次启动都被完整扫描；如果其中旧文件或子目录仍有权限问题，可临时加 `OPEN_XDOWNLOAD_FORCE_CHOWN=1` 启动一次。

## 安全说明

open-Xdownload 面向个人本地归档场景，**服务本身没有内置用户登录和访问鉴权**。默认只监听本机回环地址 `127.0.0.1:8787`；如需局域网访问，需通过 `-addr 0.0.0.0:8787` 或 `OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787` 显式放开。

Docker 镜像为便于端口映射默认设置了 `0.0.0.0`，发布端口时请自行限制。Compose 示例中的端口映射为：

```yaml
ports:
  - "127.0.0.1:8787:8787"
```

这表示只有宿主机本机可以访问。如需暴露到局域网或公网，请先配置反向代理、访问控制和 HTTPS。

请确认你有权下载和保存相关内容，并遵守 X / Twitter 的服务条款、目标站点规则以及所在地法律法规。

## 链接

- GitHub：https://github.com/chenbin3625/open-Xdownload
- Releases：https://github.com/chenbin3625/open-Xdownload/releases
