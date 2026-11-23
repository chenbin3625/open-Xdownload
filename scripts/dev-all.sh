#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WEB_DIR="$ROOT_DIR/apps/web"

cd "$ROOT_DIR"

needs_install=0
if [ ! -d "$WEB_DIR/node_modules" ]; then
  needs_install=1
elif [ -f "$WEB_DIR/package-lock.json" ] && [ "$WEB_DIR/package-lock.json" -nt "$WEB_DIR/node_modules/.package-lock.json" ]; then
  needs_install=1
elif [ ! -f "$WEB_DIR/package-lock.json" ] && [ "$WEB_DIR/package.json" -nt "$WEB_DIR/node_modules" ]; then
  needs_install=1
fi

if [ "$needs_install" -eq 1 ]; then
  echo "Installing frontend dependencies..."
  if [ -f "$WEB_DIR/package-lock.json" ]; then
    npm --prefix "$WEB_DIR" ci
  else
    npm --prefix "$WEB_DIR" install
  fi
fi

backend_bin=""
backend_pid=""
frontend_pid=""

kill_tree() {
  [ -n "${1:-}" ] || return 0
  for _child in $(ps -eo pid=,ppid= 2>/dev/null | awk -v p="$1" '$2 == p { print $1 }' || true); do
    kill_tree "$_child"
  done
  if kill -0 "$1" 2>/dev/null; then
    kill -TERM "$1" 2>/dev/null || true
  fi
}

cleanup() {
  trap - INT TERM EXIT
  if [ -n "$frontend_pid" ]; then
    kill_tree "$frontend_pid"
    wait "$frontend_pid" 2>/dev/null || true
  fi
  if [ -n "$backend_pid" ]; then
    kill_tree "$backend_pid"
    wait "$backend_pid" 2>/dev/null || true
  fi
  if [ -n "$backend_bin" ]; then
    rm -f "$backend_bin"
  fi
}

trap cleanup INT TERM EXIT

backend_bin=$(mktemp "${TMPDIR:-/tmp}/open-xdownload-dev.XXXXXX")
echo "Building backend..."
go build -o "$backend_bin" ./cmd/server

echo "Starting backend:  http://${OPEN_XDOWNLOAD_ADDR:-127.0.0.1:8787}"
"$backend_bin" &
backend_pid=$!

# 前端默认只监听回环（vite dev server 无鉴权，避免误暴露给局域网）。
# 需要局域网调试时单独运行 `npm --prefix apps/web run dev:lan`。
echo "Starting frontend: http://127.0.0.1:5173"
npm --prefix "$WEB_DIR" run dev &
frontend_pid=$!

while :; do
  if ! kill -0 "$backend_pid" 2>/dev/null; then
    wait "$backend_pid"
    exit $?
  fi
  if ! kill -0 "$frontend_pid" 2>/dev/null; then
    wait "$frontend_pid"
    exit $?
  fi
  sleep 1
done
