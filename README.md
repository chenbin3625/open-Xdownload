# open-Xdownload

本地优先的 X 媒体下载工作台。当前第一版骨架包含 Go API、SQLite 配置/任务存储、SSE 事件流，以及 React/Vite 工作台前端。

## 开发运行

开发时分别启动后端和前端：

```bash
go run ./cmd/server
npm --prefix apps/web install
npm run dev
```

默认后端监听 `127.0.0.1:8787`，前端开发服务器会把 `/api` 代理到后端。

构建后也可以只启动 Go 服务，后端会自动托管 `apps/web/dist`：

```bash
npm run build
npm run server
```

## 发布

仓库包含三套 GitHub Actions：

- `CI`：推送到 `main` 或 PR 时运行前端构建、Go 测试和服务端构建。
- `Release Binaries`：推送 `v*` tag 时构建 Linux、macOS、Windows 的 `amd64/arm64` 二进制并创建 GitHub Release。
- `Docker Image`：推送到 `main` 或 `v*` tag 时构建并发布 `linux/amd64`、`linux/arm64` 镜像到 GHCR。

发布示例：

```bash
git tag v0.1.0
git push origin v0.1.0
```

Docker 镜像地址：

```text
ghcr.io/chenbin3625/open-xdownload
```
