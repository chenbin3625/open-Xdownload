# syntax=docker/dockerfile:1.7

FROM node:22-alpine AS web
WORKDIR /src/apps/web
COPY apps/web/package*.json ./
RUN npm ci
COPY apps/web ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/apps/web/dist ./internal/webui/dist
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
  -o /out/open-xdownload ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
  && addgroup -S app \
  && adduser -S app -G app \
  && mkdir -p /data /downloads \
  && chown -R app:app /data /downloads
USER app
WORKDIR /app
COPY --from=build /out/open-xdownload /usr/local/bin/open-xdownload
ENV OPEN_XDOWNLOAD_ADDR=0.0.0.0:8787 \
  OPEN_XDOWNLOAD_DATA_DIR=/data
EXPOSE 8787
ENTRYPOINT ["open-xdownload"]
