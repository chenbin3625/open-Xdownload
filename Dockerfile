# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS web
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM rust:1-slim-bookworm AS backend
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc-aarch64-linux-gnu gcc-x86-64-linux-gnu libc6-dev-arm64-cross libc6-dev-amd64-cross \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY migrations ./migrations
COPY src ./src
COPY --from=web /src/frontend/dist ./frontend/dist
ENV CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc \
    CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_LINKER=x86_64-linux-gnu-gcc \
    CC_aarch64_unknown_linux_gnu=aarch64-linux-gnu-gcc \
    CC_x86_64_unknown_linux_gnu=x86_64-linux-gnu-gcc
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target \
    case "$TARGETARCH" in arm64) target=aarch64-unknown-linux-gnu ;; amd64) target=x86_64-unknown-linux-gnu ;; *) exit 1 ;; esac \
    && rustup target add "$target" \
    && cargo build --locked --release --target "$target" \
    && install -Dm755 "target/$target/release/x-archive" /out/x-archive

FROM debian:bookworm-slim AS runtime
ARG GALLERY_DL_VERSION=1.32.19
ARG YT_DLP_VERSION=2025.10.14
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates python3 python3-venv ffmpeg tini gosu \
    && python3 -m venv /opt/download-tools \
    && /opt/download-tools/bin/pip install --no-cache-dir "gallery-dl==$GALLERY_DL_VERSION" "yt-dlp==$YT_DLP_VERSION" "PySocks==1.7.1" \
    && groupadd --gid 10001 archive && useradd --uid 10001 --gid archive --no-create-home --home-dir /config archive \
    && mkdir /data /config && chown archive:archive /data /config && chmod 700 /data /config \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/opt/download-tools/bin:$PATH" X_ARCHIVE_DATA=/data X_ARCHIVE_CONFIG=/config X_ARCHIVE_BIND=0.0.0.0:3000
COPY --from=backend /out/x-archive /usr/local/bin/x-archive
COPY --chmod=755 scripts/entrypoint.sh /usr/local/bin/x-archive-entrypoint
WORKDIR /app
VOLUME ["/data", "/config"]
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["python3", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:3000/api/health', timeout=3)"]
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/x-archive-entrypoint"]
CMD ["x-archive"]
