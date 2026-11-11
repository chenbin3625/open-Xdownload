# 运维与归档行为

[返回 README](../README.md) · [开发与发布](DEVELOPMENT.md)

本页记录会话、归档边界、持久化和长期部署的具体行为。快速启动见 README。

## Cookie 设置与会话

1. 在电脑浏览器中登录自己的 X 账号，右键页面选择“检查”。Chrome / Edge 进入开发者工具的 **Application（应用）→ Storage → Cookies → https://x.com**；Firefox 进入 **Storage（存储）→ Cookies → https://x.com**。
2. 在 Name（名称）列找到 `auth_token` 和 `ct0`，分别复制同一会话中对应的 **Value（值）**。进入“设置 → X 登录 Cookie”，粘贴到两个同名输入框，只填值，无需包含 `auth_token=`、`ct0=` 或整个 Cookie 请求头。点击“保存并检测”后自动排队检测；保存成功后输入框清空，已保存的值不会回显。设置页的“如何获取 Cookie”也提供了这些步骤。
3. 检测任务使用需要登录的书签读取验证会话。界面区分未设置、待检测、有效、已失效和访问受限；保存通过格式校验不等于已经验证登录有效。
4. 会话失效时重新复制并更新，再检测、重试失败任务。删除 Cookie 会取消未结束任务，已归档数据保留。

服务将填写的 Cookie 转换为下载工具需要的 Netscape 格式，内部保存为配置目录下的 `cookies.txt`。Linux/macOS 和容器中的配置目录权限为 `0700`，Cookie 和设置文件为 `0600`；Windows 使用所在目录继承的 ACL，应限制该目录的访问权限。每次工具执行使用配置目录下 `.sessions` 中的私有 Cookie 副本，避免工具修改已保存的会话；结束后删除，异常关机残留在下一次启动时清理。Cookie 内容不进入接口响应、数据库、日志或镜像构建上下文。不要将 `cookies.txt` 提交到 Git。

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

Compose 默认使用 `archive-data` 和 `archive-config` 命名卷，示例分别挂载到 `/app/data` 和 `/app/config`。目录可通过 `command` 中的 `--data-dir`、`--config-dir` 配置，修改时需同步调整卷挂载的容器路径：

```text
<配置目录>/archive.sqlite          SQLite 数据及任务、快照、收藏、标签
<配置目录>/.instance.lock          实例锁
<图片根目录>/<模板>                默认 <数据目录>/images/<username>/<推文 ID>_<序号>.<扩展名>
<视频根目录>/<模板>                默认 <数据目录>/videos/<username>/<推文 ID>_<序号>.<扩展名>
<缩略图根目录>/<模板>              默认 <数据目录>/thumbnails/<username>/<媒体 ID>.<扩展名>
<数据目录>/.staging/               未完成下载
<配置目录>/settings.json           Web 设置
<配置目录>/cookies.txt             私有会话文件
<配置目录>/.sessions/              临时私有会话副本
```

旧版本的数据库在数据目录中。升级前停止旧实例并备份数据与配置目录；新版本首次启动会将旧数据库连同 WAL 中已提交的数据复制到配置目录，成功后删除旧数据库及其 WAL/SHM 文件，不改变媒体路径。配置目录应有足够空间保存完整数据库副本。若两个目录均存在 `archive.sqlite`，启动会报错，请备份并确认要保留的数据库后移除另一份，再启动服务；迁移完成后不要直接回退到旧版本。

图片、视频和缩略图分别配置根目录与路径模板。根目录相对数据目录解析，也可填写绝对路径或使用 `{data}` 前缀（例如 `{data}/media/images`），从而保存到数据目录之外。模板支持以下占位符：

| 占位符 | 含义 |
| --- | --- |
| `{data}` | 数据目录的绝对路径 |
| `{kind}` | 媒体类型（`image`、`gif` 或 `video`） |
| `{author}` | 推文作者的小写用户名 |
| `{tweet_id}` | 推文 ID |
| `{ordinal}` | 推文内媒体序号 |
| `{extension}` | 下载后的文件扩展名 |
| `{media_id}` | 媒体记录 ID |

模板为相对路径时与根目录拼接；模板为绝对路径或以 `{data}` 开头时忽略根目录。默认模板保持原有布局：图片和视频为 `{author}/{tweet_id}_{ordinal}.{extension}`，缩略图为 `{author}/{media_id}.{extension}`。例如把图片根目录设为 `photos/originals`、模板保持默认时，实际保存到 `<数据目录>/photos/originals/<username>/<推文 ID>_<序号>.<扩展名>`；把模板改为 `{kind}/{author}/{tweet_id}_{ordinal}.{extension}` 可加入媒体类型目录。模板必须包含 `{extension}`；缩略图模板还必须包含 `{media_id}`，图片和视频模板必须包含 `{media_id}` 或同时包含 `{tweet_id}` 与 `{ordinal}`，以免文件互相覆盖。

根目录和模板不接受 `..`、控制字符或 `.staging`、`.sessions`、`.instance.lock`、`archive.sqlite` 等保留名称。修改配置只影响后续下载，不自动迁移旧文件；旧媒体及预览图仍按数据库记录的原路径读取。数据目录内的文件以相对路径记录，外部绝对路径按原值记录。重新下载后旧目录文件、异常中断后的临时/未引用文件可能残留，当前版本不含垃圾回收。自定义媒体根目录位于其他文件系统时，下载完成后会使用复制方式移动文件。

“归档媒体”显示数据库记录的媒体文件字节数，不包括缩略图、暂存文件或遗留文件；磁盘总量/可用量默认为数据目录所在文件系统的实际数值。图片、视频或缩略图保存到其他文件系统时，下载前会检查对应根目录的可用空间，但设置页仍只显示数据目录所在文件系统；请自行确认外部磁盘容量。可用空间不足 128 MB 时暂停新的媒体下载。

镜像应用进程使用 UID/GID `10001`，入口程序初始化卷顶层权限后降权运行。使用自己准备的绑定目录时，已有内容也需要由 UID/GID `10001` 可读写。

## 更新下载工具

工具版本固定在 `requirements-tools.txt` 和 Dockerfile 构建参数。X 接口变化导致提取失败时，可以选择明确版本后重建：

```sh
docker compose -f compose.yaml -f compose.build.yaml build --build-arg GALLERY_DL_VERSION=1.32.19 --build-arg YT_DLP_VERSION=2025.10.14
docker compose -f compose.yaml -f compose.build.yaml up -d --no-build
```

本地环境可通过 `.tools/bin/pip install --upgrade gallery-dl yt-dlp` 更新，再检查设置页中的工具版本。更新后需要重新验证实际账号流程；固定版本不保证未来 X 接口兼容。

## 远程访问

Compose 默认发布到 `0.0.0.0:3000`，可用服务器 IP 访问。使用同机 HTTPS 反向代理时，将 `ports` 改为 `127.0.0.1:3000:3000`，保留原始 `Host`，让浏览器的 Origin 与 Host 一致。例如 Caddy：

```caddyfile
archive.example.com {
    reverse_proxy 127.0.0.1:3000
}
```

代理运行在容器中时应通过同一 Docker 网络连接 `x-archive:3000`。Compose 的访问密码直接填写在 `command` 的 `--password` 参数中，不写入 SQLite。Web 登录会话在应用重启后失效，X Cookie 和归档仍保留。

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

使用镜像部署时运行 `docker compose pull` 和 `docker compose up -d`；若固定了镜像版本，先修改 `compose.yaml` 的 `image`。二进制部署先停止旧进程，再替换可执行文件。迁移在启动时自动执行；降级前应恢复与旧版本对应的备份。`docker compose down` 保留数据卷，`docker compose down -v` 会删除数据卷。

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
