---
name: release
description: Run pre-release checks for open-Xdownload
---

Pre-release checklist:

1. Run full test suite:
```sh
cd "/Volumes/SSD 1/code/open-Xdownload"
npm test
```

2. Build and sync embedded assets:
```sh
npm run build:embedded
```

3. Check gofmt:
```sh
gofmt -l .
```

4. Check go mod tidy:
```sh
go mod tidy -diff
```

5. Verify git status is clean (no uncommitted embedded asset changes).

Report each step's result. If all pass, confirm ready for tagging.
