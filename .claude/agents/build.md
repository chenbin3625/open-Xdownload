---
name: build
description: Build frontend and backend for open-Xdownload
---

Build the full project:

```sh
cd "/Volumes/SSD 1/code/open-Xdownload"

echo "=== Frontend build ==="
npm run build

echo "=== Sync embedded assets ==="
npm run build:embedded

echo "=== Backend build ==="
go build -o open-Xdownload ./cmd/server
```

Report build success or errors for each step.
