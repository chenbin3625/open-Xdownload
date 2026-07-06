#!/usr/bin/env sh
set -eu

if [ ! -f apps/web/dist/index.html ]; then
  echo "apps/web/dist/index.html not found. Run npm run build first." >&2
  exit 1
fi

rm -rf internal/webui/dist
mkdir -p internal/webui/dist
cp -R apps/web/dist/. internal/webui/dist/
