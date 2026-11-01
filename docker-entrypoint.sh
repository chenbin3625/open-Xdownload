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

validate_dir() {
  dir="$1"
  mkdir -p "$dir"
  logical="$(CDPATH= cd -L "$dir" && pwd -P)"
  physical="$(CDPATH= cd -P "$dir" && pwd -P)"
  for resolved in "$logical" "$physical"; do
    case "$resolved" in
      *[!/]*) ;;
      *)
        echo "data and download directories must not resolve to filesystem root: $dir" >&2
        exit 1
        ;;
    esac
  done
}

fix_owner() {
  dir="$1"
  mode="${2:-auto}"
  [ -n "$dir" ] || return 0

  current="$(stat -c '%u:%g' "$dir" 2>/dev/null || true)"
  if [ "$mode" = "always" ] || [ "$current" != "$PUID:$PGID" ] || [ "${OPEN_XDOWNLOAD_FORCE_CHOWN:-0}" = "1" ]; then
    # NFS root_squash、只读卷等场景下 chown 会失败：不能让 set -e 直接把容器杀掉，
    # 留给程序自己在写入时报出权限错误。
    if ! chown -R "$PUID:$PGID" "$dir"; then
      echo "warning: chown -R $PUID:$PGID $dir failed; continuing with existing ownership" >&2
    fi
  fi
}

if [ "$PUID" != "0" ]; then
  validate_dir "$OPEN_XDOWNLOAD_DATA_DIR"
  validate_dir "$OPEN_XDOWNLOAD_DOWNLOAD_DIR"
  fix_owner "$OPEN_XDOWNLOAD_DATA_DIR" always
  fix_owner "$OPEN_XDOWNLOAD_DOWNLOAD_DIR"
  exec su-exec "$PUID:$PGID" "$@"
fi

exec "$@"
