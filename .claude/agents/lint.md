---
name: lint
description: Run code quality checks (gofmt + TypeScript)
---

Run lint and format checks:

```sh
cd "/Volumes/SSD 1/code/open-Xdownload"

echo "=== gofmt check ==="
UNFORMATTED=$(gofmt -l .)
if [ -n "$UNFORMATTED" ]; then
  echo "Unformatted files:"
  echo "$UNFORMATTED"
else
  echo "All Go files formatted"
fi

echo "=== TypeScript type check ==="
npx --prefix apps/web tsc --noEmit

echo "=== go mod tidy check ==="
go mod tidy -diff
```

Report any issues found.
