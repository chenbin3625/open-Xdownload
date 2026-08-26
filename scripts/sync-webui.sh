#!/usr/bin/env sh
set -eu

if [ ! -f apps/web/dist/index.html ]; then
  echo "apps/web/dist/index.html not found. Run npm run build first." >&2
  exit 1
fi

rm -rf internal/webui/dist
mkdir -p internal/webui/dist
cp -R apps/web/dist/. internal/webui/dist/

# Go embed 只保留预压缩 sidecar：identity 的 js/css/svg 再占一份 ~1MB。
# 运行时无压缩客户端从 .br/.gz 解压；index.html 留下方便 Stat。
find internal/webui/dist -type f \( \
  -name '*.js' -o -name '*.css' -o -name '*.svg' -o -name '*.json' -o -name '*.map' \
\) ! -name '*.gz' ! -name '*.br' | while IFS= read -r file; do
  if [ -f "${file}.br" ] || [ -f "${file}.gz" ]; then
    rm -f "$file"
  fi
done
