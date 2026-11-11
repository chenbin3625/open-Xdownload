# 开发、测试与发布

[返回 README](../README.md) · [运维说明](OPERATIONS.md)

## 本地开发

需要稳定版 Rust、Node.js 24、Python 3.9+，以及 PATH 中的 FFmpeg / FFprobe。在仓库根目录运行：

```sh
python3 -m venv .tools
.tools/bin/pip install -r requirements-tools.txt
npm --prefix frontend ci
npm --prefix frontend run build
PATH="$PWD/.tools/bin:$PATH" cargo run --locked
```

默认监听 <http://127.0.0.1:3000>。本机模式可以不配置访问密码，且只接受回环 Host。前端热更新使用 `npm --prefix frontend run dev`，API 代理到端口 3000。发布编译前必须先构建前端，`frontend/dist` 会嵌入正式二进制。

## 自动化检查

```sh
npm --prefix frontend ci
npm --prefix frontend run build
cargo fmt --all --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked
cargo build --locked --release
node scripts/smoke-binary.mjs target/release/x-archive 1.2.0
cd frontend
npx playwright install --with-deps chromium
npm run test:e2e
```

浏览器测试启动独立临时服务，默认使用端口 3101；端口占用时可用 `X_ARCHIVE_TEST_PORT=13101 npm --prefix frontend run test:e2e` 指定其他端口。真实 Rust API、SQLite、任务执行、图片处理和 FFmpeg 参与测试，X 端提取/下载由明确标识的离线工具契约替代。测试生成实际 PNG / H.264 媒体；测试服务及专用接口不会编入正式二进制，不使用个人数据或真实 Cookie。报告位于 `frontend/test-results`。

当前工具契约依赖 Unix Python shebang，因此整套测试在 Linux 运行。各发布目标额外运行 `scripts/smoke-binary.mjs`：从独立临时目录启动实际生产二进制，检查版本、内嵌前端资源、访问密码鉴权和 SQLite 初始化。

## Docker 构建

本机平台：

```sh
docker build -t x-archive:local .
node scripts/verify-container.mjs x-archive:local amd64
```

ARM64 主机将最后一个参数改为 `arm64`。验证脚本使用可销毁卷和无效测试 Cookie，检查启动、内嵌资源、UID / 文件权限、鉴权、重启后数据持久化及旧 Web 会话失效。

同时构建并推送两个平台：

```sh
docker buildx bake -f docker-bake.hcl --set x-archive.tags=chenbin3625/x-archive:test --push
```

也可输出 OCI 存档：

```sh
docker buildx bake -f docker-bake.hcl --set x-archive.output=type=oci,dest=x-archive.oci.tar
```

运行镜像为 Debian Bookworm Slim；构建阶段在主机平台执行 Rust 交叉编译。导入多架构 OCI 索引需要支持该格式的镜像存储；普通本机测试优先使用单平台 `docker build`。

## GitHub Actions

| 工作流 | 触发 | 行为 |
| --- | --- | --- |
| `ci.yml` | `main`、PR、手动、发布流程调用 | 前端构建、Rust 格式与 Clippy、全部后端测试、发布编译、桌面/移动 Chromium |
| `image.yml` | `main`、PR、手动、发布流程调用 | 原生 AMD64 / ARM64 容器构建及持久化验证；PR 不推送，其余通过后推送 Docker Hub |
| `release.yml` | `v*` 标签、在标签上手动执行 | 校验版本，复用 CI，构建五个平台二进制及双架构镜像，全部成功后公开 GitHub Release |

下载包包含二进制、README、更新记录、MIT License、固定版本工具依赖、Compose 示例和运维文档。Linux 使用 musl 静态构建，Windows 静态链接 C 运行库；SQLite 已内置，不依赖外部 SQLite 或 OpenSSL。媒体下载仍依赖外部工具。

Docker Hub 镜像为 `chenbin3625/x-archive`。镜像 `edge` 随主分支更新；稳定版标签发布 `1.2.0`、`1.2`、`1` 和 `latest`。预发布标签不会更新 `latest`。Release 会先创建草稿，上传全部压缩包及 `SHA256SUMS` 后公开；失败可在同一标签重跑，不需移动标签。

手动发布已有源码标签时，在 `image.yml` 的 `source_ref` 输入中指定标签，例如 `v1.2.0`。工作流按指定源码构建、验证并生成对应版本标签，不会移动 Git 标签。

## 发布新版本

1. 同步 `Cargo.toml`、`Cargo.lock`、`frontend/package.json`、`frontend/package-lock.json` 中的版本，并更新 `CHANGELOG.md`。界面版本从前端包版本读取。
2. 运行自动化检查并提交。标签必须与 Cargo 版本完全一致，例如 `v1.2.0`。
3. 推送分支和标签：

```sh
git push origin main
git tag -a v1.2.0 -m "X Archive v1.2.0"
git push origin v1.2.0
```

GitHub Release 使用仓库自带的 `GITHUB_TOKEN`，仅发布 job 需要 `contents: write`。Docker Hub 登录使用仓库 Actions Secrets `DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN`，账号需有 `chenbin3625/x-archive` 的推送权限；发布流程通过 `secrets: inherit` 传给镜像工作流。PR 构建只有读取权限且不登录、不推送镜像。仓库需允许 GitHub Actions 运行，Docker Hub 仓库需设置为公开以允许匿名拉取。

发布后检查 [Actions](https://github.com/chenbin3625/x-archive/actions)、[Releases](https://github.com/chenbin3625/x-archive/releases) 和 [Docker Hub](https://hub.docker.com/r/chenbin3625/x-archive)，并在未登录 Docker Hub 的环境中验证镜像可拉取、两种架构 manifest 都存在。真实 X Cookie 流程需使用自己的有效会话另行验收，离线测试不构成真实 X 抓取成功的证明。
