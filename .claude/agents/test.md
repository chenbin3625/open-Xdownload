---
name: test
description: Run full test suite (Go + frontend + E2E)
---

Run the complete test suite for open-Xdownload:

```sh
cd "/Volumes/SSD 1/code/open-Xdownload"

echo "=== Go tests ==="
go test ./...

echo "=== Frontend unit tests ==="
npm --prefix apps/web run test

echo "=== E2E tests ==="
npm --prefix apps/web run test:e2e
```

Report results for each test suite separately.
