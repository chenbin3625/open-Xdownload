# 运维与归档行为

[返回 README](../README.md) · [开发与发布](DEVELOPMENT.md)

本页记录会话、归档边界、持久化和长期部署的具体行为。快速启动见 README。

## Cookie 设置与会话

1. 在电脑浏览器中登录自己的 X 账号，右键页面选择“检查”。Chrome / Edge 进入开发者工具的 **Application（应用）→ Storage → Cookies → https://x.com**；Firefox 进入 **Storage（存储）→ Cookies → https://x.com**。
2. 在 Name（名称）列找到 `auth_token` 和 `ct0`，分别复制同一会话中对应的 **Value（值）**。进入“设置 → X 登录 Cookie”，粘贴到两个同名输入框，只填值，无需包含 `auth_token=`、`ct0=` 或整个 Cookie 请求头。点击“保存并检测”后自动排队检测；保存成功后输入框清空，已保存的值不会回显。设置页的“如何获取 Cookie”也提供了这些步骤。
3. 检测任务使用需要登录的书签读取验证会话。界面区分未设置、待检测、有效、已失效和访问受限；保存通过格式校验不等于已经验证登录有效。
4. 会话失效时重新复制并更新，再检测、重试失败任务。删除 Cookie 会取消未结束任务，已归档数据保留。

服务将填写的 Cookie 转换为下载工具需要的 Netscape 格式，内部保存为 `/config/cookies.txt`。Linux/macOS 和容器中的配置目录权限为 `0700`，Cookie 和设置文件为 `0600`；Windows 使用所在目录继承的 ACL，应限制该目录的访问权限。每次工具执行使用 `/config/.sessions` 中的私有 Cookie 副本，避免工具修改已保存的会话；结束后删除，异常关机残留在下一次启动时清理。Cookie 内容不进入接口响应、数据库、日志或镜像构建上下文。不要将 `cookies.txt` 提交到 Git。

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
/data/archive.sqlite          SQLite 数据及任务、快照、收藏、标签
/data/images/<username>/      默认图片和原生 GIF，按用户名分目录
/data/videos/<username>/      默认视频及 MP4 GIF，按用户名分目录
/data/thumbnails/<username>/  预览图及视频封面，按用户名分目录
/data/.staging/               未完成下载
/config/settings.json         Web 设置
/config/cookies.txt           私有会话文件
/config/.sessions/            临时私有会话副本
```

所有新下载的媒体和预览图均按推文作者的用户名分目录，用户名统一为小写；原文件名为 `<推文 ID>_<媒体序号>.<扩展名>`。例如，`alice` 和 `bob` 的图片分别保存在 `/data/images/alice/`、`/data/images/bob/`，视频分别保存在 `/data/videos/alice/`、`/data/videos/bob/`。单条推文导入与用户同步使用相同规则。

Web 保存路径为 `/data` 内的图片或视频根目录，其下自动创建用户名子目录。例如图片路径设为 `photos/originals` 时，实际保存到 `/data/photos/originals/<username>/`。不接受 `..`、绝对路径或符号链接跳转。修改目录只影响后续下载，不自动迁移旧文件；旧媒体及预览图仍按数据库记录的原路径读取。重新下载后旧目录文件、异常中断后的临时/未引用文件可能残留，当前版本不含垃圾回收。

“归档媒体”显示数据库记录的媒体文件字节数，不包括缩略图、暂存文件或遗留文件；磁盘总量/可用量为数据目录所在文件系统的实际数值。可用空间不足 128 MB 时暂停新的媒体下载。

镜像应用进程使用 UID/GID `10001`，入口程序初始化卷顶层权限后降权运行。使用自己准备的绑定目录时，已有内容也需要由 UID/GID `10001` 可读写。

## 更新下载工具

工具版本固定在 `requirements-tools.txt` 和 Dockerfile 构建参数。X 接口变化导致提取失败时，可以选择明确版本后重建：

```sh
docker compose -f compose.yaml -f compose.build.yaml build --build-arg GALLERY_DL_VERSION=1.32.19 --build-arg YT_DLP_VERSION=2025.10.14
docker compose -f compose.yaml -f compose.build.yaml up -d --no-build
```

本地环境可通过 `.tools/bin/pip install --upgrade gallery-dl yt-dlp` 更新，再检查设置页中的工具版本。更新后需要重新验证实际账号流程；固定版本不保证未来 X 接口兼容。

## 远程访问

Compose 默认只发布到 `127.0.0.1:3000`。可通过 HTTPS 反向代理访问，保留原始 `Host`，让浏览器的 Origin 与 Host 一致。例如 Caddy：

```caddyfile
archive.example.com {
    reverse_proxy 127.0.0.1:3000
}
```

反向代理和应用在同一台主机时保留默认端口映射即可；代理运行在容器中时应通过同一 Docker 网络连接 `x-archive:3000`。访问密码通过环境变量传入，不写入 SQLite。Web 登录会话在应用重启后失效，X Cookie 和归档仍保留。

也可以通过 SSH 隧道访问远程服务器的回环端口：

```sh
ssh -L 3000:127.0.0.1:3000 user@server
```

## 长期运行二进制

Linux 可使用 systemd。以下示例假定已经创建 `x-archive` 系统用户，将二进制安装到 `/usr/local/bin/x-archive`，将下载工具安装在 `/opt/x-archive-tools/bin`，并将 `/var/lib/x-archive` 的所有权交给该用户。

```ini
[Unit]
Description=X Archive
After=network-online.target
Wants=network-online.target

[Service]
User=x-archive
Group=x-archive
WorkingDirectory=/var/lib/x-archive
Environment=PATH=/opt/x-archive-tools/bin:/usr/local/bin:/usr/bin:/bin
Environment=X_ARCHIVE_BIND=127.0.0.1:3000
EnvironmentFile=/etc/x-archive.env
ExecStart=/usr/local/bin/x-archive
Restart=on-failure
RestartSec=5
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

将访问密码放入仅 root 可读的 `/etc/x-archive.env`：`X_ARCHIVE_PASSWORD=自己的长密码`。systemd 读取该文件后传入环境；直接启动二进制不会自动读取 `.env`。macOS 可用 launchd，Windows 可使用计划任务或服务管理工具，同样需要固定工作目录和工具 PATH。

## 升级与备份

升级前停止服务，备份整个数据目录和配置目录，包括 SQLite、媒体、设置与 Cookie。不要只备份仍在运行的 `archive.sqlite`：SQLite 使用 WAL，还可能有尚未合并的数据。Compose 命名卷的实际名称带项目名前缀，可用 `docker volume ls` 查询。

使用镜像部署时运行 `docker compose pull` 和 `docker compose up -d`；若 `.env` 固定了镜像版本，先修改 `X_ARCHIVE_IMAGE`。二进制部署先停止旧进程，再替换可执行文件。迁移在启动时自动执行；降级前应恢复与旧版本对应的备份。`docker compose down` 保留数据卷，`docker compose down -v` 会删除数据卷。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| Cookie 格式被拒绝 | 从 x.com 的 Cookies 列表复制 `auth_token`、`ct0` 的 Value，分别填入同名输入框；只填值，不含名称前缀或整个请求头 |
| Cookie 失效或内容无权访问 | 在浏览器确认账号仍能查看目标，更新 Cookie 后检测；账号权限不足的内容不能归档 |
| X 频率限制 | 等待任务标示的重试时间，延长同步间隔，降低并发 |
| 下载工具启动失败 | 设置页检查 gallery-dl、yt-dlp、FFmpeg 和 FFprobe；检查 PATH 或可执行文件变量 |
| 提取或媒体处理失败 | 检查网络、工具版本、存储空间；任务页查看安全摘要并重试 |
| 关系快照一直为部分 | 检查抓取数与预期计数，账号可见性或接口可能截断；部分结果不用于取关判断 |
| 重启后要求重新登录 | Web 登录会话按进程生成；Cookie 文件、归档和设置仍持久保存 |
| 无法写入挂载目录 | 检查 UID/GID `10001` 的读写权限，避免将同一数据卷交给多个实例 |

当前版本不包含内置数据导出、备份功能、高级统计和关系图。
