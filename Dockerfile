# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/apps/web
COPY apps/web/package*.json ./
RUN npm ci
COPY apps/web ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/apps/web/dist ./apps/web/dist
RUN sh scripts/sync-webui.sh
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
  -o /out/open-xdownload ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates su-exec tzdata \
  && mkdir -p /data /downloads
WORKDIR /app
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY --from=build /out/open-xdownload /usr/local/bin/open-xdownload
ENV OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787 \
  OPEN_XDOWNLOAD_DATA_DIR=/data \
  OPEN_XDOWNLOAD_DOWNLOAD_DIR=/downloads \
  PUID=1000 \
  PGID=1000
# 服务本身无鉴权。容器内需监听 0.0.0.0 才能被端口映射访问；
# 如只在本机使用，请用 `docker run -p 127.0.0.1:8787:8787` 限制仅本机可达，
# 暴露到网络时请前置带鉴权的反向代理。
#
# 关于 root：镜像故意不写 USER。entrypoint 需要 root 才能把挂载卷 chown 给 PUID/PGID
# （NFS/只读卷 chown 失败会告警但不退出），随后用 su-exec 降权执行本程序——即
# 实际服务进程始终以 PUID:PGID 运行，不是 root。若宿主已用 `--user` 指定非 root，
# entrypoint 会跳过 chown 直接 exec。
EXPOSE 8787

STOPSIGNAL SIGTERM

# 探针请求 /api/health（不依赖前端产物是否就绪），端口取自运行时 OPEN_XDOWNLOAD_ADDR，
# 因此用户覆盖端口后依然有效；TLS 模式先试 http 再试 https（busybox 的 TLS 不校验证书，
# 自签证书可直接探测）。start-period 放宽到 20s，给首次迁移留时间。
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD sh -c 'port="${OPEN_XDOWNLOAD_ADDR##*:}"; port="${port:-8787}"; \
    wget -q -O /dev/null "http://127.0.0.1:${port}/api/health" \
    || wget -q -O /dev/null "https://127.0.0.1:${port}/api/health" \
    || exit 1'

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["open-xdownload"]
