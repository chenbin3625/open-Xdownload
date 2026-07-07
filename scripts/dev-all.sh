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

backend_pid=""
frontend_pid=""

cleanup() {
  trap - INT TERM EXIT
  if [ -n "$frontend_pid" ] && kill -0 "$frontend_pid" 2>/dev/null; then
    kill "$frontend_pid" 2>/dev/null || true
  fi
  if [ -n "$backend_pid" ] && kill -0 "$backend_pid" 2>/dev/null; then
    kill "$backend_pid" 2>/dev/null || true
  fi
  wait "$frontend_pid" 2>/dev/null || true
  wait "$backend_pid" 2>/dev/null || true
}

trap cleanup INT TERM EXIT

echo "Starting backend:  http://0.0.0.0:8787"
go run ./cmd/server &
backend_pid=$!

echo "Starting frontend: http://0.0.0.0:5173"
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
