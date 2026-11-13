# open-Xdownload

推文/媒体下载工具，包含 Web UI 和 Go 后端服务。

## 技术栈

- 后端: Go 1.26+（`cmd/server` 入口，`internal/` 业务模块）
- 前端: React 19 + TypeScript + Vite + TailwindCSS + Semi UI
- 国际化: i18next
- 部署: Docker 多阶段构建

## 构建

```sh
# 前端开发
npm run dev

# 前端生产构建
npm run build

# 构建前端并同步到 Go embedded
npm run build:embedded

# 启动后端
go run ./cmd/server
```

## 测试

```sh
# Go 单元测试
go test ./...

# 前端单元测试 (Vitest)
npm --prefix apps/web run test

# 前端 E2E 测试 (Playwright)
npm --prefix apps/web run test:e2e

# 全量测试（Go + 前端 + 构建）
npm test
```

## 代码规范

```sh
gofmt -l .                              # Go 格式化检查
npx --prefix apps/web tsc --noEmit      # TypeScript 类型检查
go mod tidy                             # Go 模块整理
```

## CI/CD

- `.github/workflows/ci.yml` — PR/push 到 main：Go 测试 + 前端测试 + 构建 + gofmt 检查
- `.github/workflows/release.yml` — v* tag：多平台二进制发布
- `.github/workflows/dockerhub.yml` — Docker 镜像发布

## 发布 SOP

1. 确保所有测试通过：`npm test`
2. 前端构建并同步嵌入资源：`npm run build:embedded`
3. 提交嵌入资源变更（CI 会校验是否与源码一致）
4. 打 v* tag 触发 release workflow
5. Docker 镜像自动发布到 DockerHub

## 注意事项

- 前端构建产物需 `sync:web` 同步到 `internal/webui/dist` 并提交
- CI 校验嵌入资源是否最新，不同步会导致 CI 失败
- `.env` 文件不入库
