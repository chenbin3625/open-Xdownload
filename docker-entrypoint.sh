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

fix_owner() {
  dir="$1"
  [ -n "$dir" ] || return 0
  mkdir -p "$dir"

  current="$(stat -c '%u:%g' "$dir" 2>/dev/null || true)"
  if [ "$current" != "$PUID:$PGID" ] || [ "${OPEN_XDOWNLOAD_FORCE_CHOWN:-0}" = "1" ]; then
    chown -R "$PUID:$PGID" "$dir"
  fi
}

if [ "$PUID" != "0" ]; then
  fix_owner "$OPEN_XDOWNLOAD_DATA_DIR"
  fix_owner "$OPEN_XDOWNLOAD_DOWNLOAD_DIR"
  exec su-exec "$PUID:$PGID" "$@"
fi

exec "$@"
