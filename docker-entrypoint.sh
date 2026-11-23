#!/bin/sh
set -eu

if [ "$#" -eq 0 ] || [ "${1#-}" != "$1" ]; then
  set -- open-xdownload "$@"
fi

if [ "$(id -u)" != "0" ]; then
  exec "$@"
fi

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"
OPEN_XDOWNLOAD_DATA_DIR="${OPEN_XDOWNLOAD_DATA_DIR:-/data}"
OPEN_XDOWNLOAD_DOWNLOAD_DIR="${OPEN_XDOWNLOAD_DOWNLOAD_DIR:-/downloads}"

case "$PUID" in
  '' | *[!0-9]*)
    echo "PUID must be numeric, got PUID=$PUID" >&2
    exit 1
    ;;
esac

case "$PGID" in
  '' | *[!0-9]*)
    echo "PGID must be numeric, got PGID=$PGID" >&2
    exit 1
    ;;
esac

strip_trailing_slashes() {
  p="$1"
  while [ "${#p}" -gt 1 ] && [ "${p%/}" != "$p" ]; do
    p="${p%/}"
  done
  printf '%s\n' "$p"
}

validate_dir() {
  dir="$(strip_trailing_slashes "$1")"
  if [ -z "$dir" ]; then
    echo "data and download directories must not be empty" >&2
    exit 1
  fi
  if [ -L "$dir" ]; then
    echo "data and download directories must not be symlinks: $dir" >&2
    exit 1
  fi
  ancestor="$dir"
  while [ ! -e "$ancestor" ] && [ ! -L "$ancestor" ]; do
    parent="$(dirname -- "$ancestor")"
    if [ "$parent" = "$ancestor" ]; then
      break
    fi
    ancestor="$parent"
  done
  if [ -L "$ancestor" ]; then
    echo "data and download directories must not resolve through symlinks: $dir" >&2
    exit 1
  fi
  if [ -d "$ancestor" ]; then
    anc_logical="$(CDPATH= cd -L -- "$ancestor" && pwd -L)"
    anc_physical="$(CDPATH= cd -P -- "$ancestor" && pwd -P)"
    if [ "$anc_logical" != "$anc_physical" ]; then
      echo "data and download directories must not resolve through symlinks: $dir" >&2
      exit 1
    fi
  fi
  mkdir -p -- "$dir"
  if [ -L "$dir" ]; then
    echo "data and download directories must not be symlinks: $dir" >&2
    exit 1
  fi
  logical="$(CDPATH= cd -L -- "$dir" && pwd -L)"
  physical="$(CDPATH= cd -P -- "$dir" && pwd -P)"
  for resolved in "$logical" "$physical"; do
    case "$resolved" in
      *[!/]*) ;;
      *)
        echo "data and download directories must not resolve to filesystem root: $dir" >&2
        exit 1
        ;;
    esac
  done
  if [ "$logical" != "$physical" ]; then
    echo "data and download directories must not resolve through symlinks: $dir" >&2
    exit 1
  fi
}

fix_owner() {
  dir="$(strip_trailing_slashes "$1")"
  [ -n "$dir" ] || return 0
  if [ -L "$dir" ]; then
    echo "data and download directories must not be symlinks: $dir" >&2
    exit 1
  fi

  current="$(stat -c '%u:%g' -- "$dir" 2>/dev/null || true)"
  if [ "$current" != "$PUID:$PGID" ] || [ "${OPEN_XDOWNLOAD_FORCE_CHOWN:-0}" = "1" ]; then
    # 默认只修正目录本身：下载目录可能有数万个文件，每次启动都 chown -R 会显著
    # 拖慢启动。升级/改 PUID 后可设 OPEN_XDOWNLOAD_FORCE_CHOWN=1 做一次递归修复。
    # NFS root_squash、只读卷等场景下 chown 会失败：不能让 set -e 直接把容器杀掉，
    # 留给程序自己在写入时报出权限错误。
    if [ "${OPEN_XDOWNLOAD_FORCE_CHOWN:-0}" = "1" ]; then
      if ! chown -R -h "$PUID:$PGID" -- "$dir"; then
        echo "warning: chown -R $PUID:$PGID $dir failed; continuing with existing ownership" >&2
      fi
    elif ! chown -h "$PUID:$PGID" -- "$dir"; then
      echo "warning: chown $PUID:$PGID $dir failed; continuing with existing ownership" >&2
    fi
  fi
}

if [ "$PUID" -ne 0 ] || [ "$PGID" -ne 0 ]; then
  validate_dir "$OPEN_XDOWNLOAD_DATA_DIR"
  validate_dir "$OPEN_XDOWNLOAD_DOWNLOAD_DIR"
  fix_owner "$OPEN_XDOWNLOAD_DATA_DIR"
  fix_owner "$OPEN_XDOWNLOAD_DOWNLOAD_DIR"
  exec su-exec "$PUID:$PGID" "$@"
fi

exec "$@"
