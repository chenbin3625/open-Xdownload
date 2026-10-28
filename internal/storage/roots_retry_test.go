package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

// TestDownloadRootsSeededOnOpenAndUpdate 验证 download_roots 的生命周期：
// 保存配置时登记旧根+新根；启动时播种库内目录与 env 生效目录；历史根不会被丢弃。
func TestDownloadRootsSeededOnOpenAndUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root1 := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root1}); err != nil {
		t.Fatal(err)
	}
	root2 := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root2}); err != nil {
		t.Fatal(err)
	}
	roots, err := store.DownloadRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(roots, filepath.Clean(root1)) || !containsString(roots, filepath.Clean(root2)) {
		t.Fatalf("roots = %v, want old root %q and new root %q", roots, root1, root2)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启 + env 覆盖：生效目录（env）与库内目录都必须在启动时被播种。
	envRoot := t.TempDir()
	t.Setenv(config.EnvDownloadDir, envRoot)
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	roots, err = reopened.DownloadRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(roots, filepath.Clean(envRoot)) {
		t.Fatalf("env download root not seeded: %v", roots)
	}
	if !containsString(roots, filepath.Clean(root2)) {
		t.Fatalf("stored download root lost on restart: %v", roots)
	}
	cfg, err := reopened.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(cfg.DownloadDir) != filepath.Clean(envRoot) {
		t.Fatalf("effective download dir = %q, want env %q", cfg.DownloadDir, envRoot)
	}
}

// TestPruneDownloadRootsRemovesUnsafeAndCaps 验证回滚保护（VERIFY R-F）与上限：
// `/`、家目录这类历史遗留根必须被清理；数量按最近使用/最长前缀保留在 MaxDownloadRoots。
func TestPruneDownloadRootsRemovesUnsafeAndCaps(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	current := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: current}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	home, _ := os.UserHomeDir()
	unsafeRoots := []string{string(filepath.Separator)}
	if home != "" {
		unsafeRoots = append(unsafeRoots, filepath.Clean(home))
	}
	for _, root := range unsafeRoots {
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, root, now); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < MaxDownloadRoots*2; i++ {
		root := filepath.Join(os.TempDir(), fmt.Sprintf("xd-fake-root-%02d", i))
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, root, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PruneDownloadRoots(ctx, []string{current}); err != nil {
		t.Fatal(err)
	}
	roots, err := store.DownloadRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) > MaxDownloadRoots {
		t.Fatalf("roots = %d, want <= %d", len(roots), MaxDownloadRoots)
	}
	for _, root := range roots {
		if root == string(filepath.Separator) {
			t.Fatal("filesystem root survived pruning")
		}
		if home != "" && filepath.Clean(root) == filepath.Clean(home) {
			t.Fatal("home directory survived pruning")
		}
	}
	if !containsString(roots, filepath.Clean(current)) {
		t.Fatalf("keep root missing after prune: %v", roots)
	}
	// last_seen_at 最新的历史根优先保留。
	newest := filepath.Join(os.TempDir(), "xd-fake-root-00")
	if !containsString(roots, newest) {
		t.Fatalf("newest historical root pruned prematurely: %v", roots)
	}
	oldest := filepath.Join(os.TempDir(), fmt.Sprintf("xd-fake-root-%02d", MaxDownloadRoots*2-1))
	if containsString(roots, oldest) {
		t.Fatalf("oldest historical root should have been pruned: %v", roots)
	}
}

// TestReserveAutomaticRetryJobReusesRow 覆盖自动重试任务行复用：同一行在终态后可被重置，
// 活动期间重复调用返回 nil（不重复入队）；PruneAutomaticRetryJobs 只删旧终态行。
func TestReserveAutomaticRetryJobReusesRow(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	first, err := store.ReserveAutomaticRetryJob(ctx)
	if err != nil || first == nil {
		t.Fatalf("reserve: %+v %v", first, err)
	}
	if first.Status != JobPending || first.Input != AutomaticFailedRetryInput || first.Kind != JobKindFailedRetry {
		t.Fatalf("reserved job = %+v", first)
	}
	if second, err := store.ReserveAutomaticRetryJob(ctx); err != nil || second != nil {
		t.Fatalf("reserve while active = %+v %v, want nil", second, err)
	}
	// 活动行不能被清理。
	if pruned, err := store.PruneAutomaticRetryJobs(ctx, time.Now().UTC().Add(time.Hour)); err != nil || pruned != 0 {
		t.Fatalf("pruned active job: %d %v", pruned, err)
	}

	// 进入终态后可被复用（同一行，重置为 pending）。
	completed := *first
	completed.Status = JobCompleted
	completed.Progress = 1
	if _, err := store.UpdateJobAndGet(ctx, completed); err != nil {
		t.Fatal(err)
	}
	reused, err := store.ReserveAutomaticRetryJob(ctx)
	if err != nil || reused == nil {
		t.Fatalf("reuse: %+v %v", reused, err)
	}
	if reused.ID != first.ID || reused.Status != JobPending || reused.Progress != 0 || reused.Error != "" {
		t.Fatalf("reused job = %+v, want same id %d reset to pending", reused, first.ID)
	}
	var rowCount int
	if err := store.db.Get(&rowCount, `SELECT COUNT(*) FROM jobs WHERE kind = ? AND input = ?`, JobKindFailedRetry, AutomaticFailedRetryInput); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("automatic retry jobs rows = %d, want 1 (reuse, no growth)", rowCount)
	}

	// 终态 + 旧 updated_at → 清理。
	completed = *reused
	completed.Status = JobCompleted
	if _, err := store.UpdateJobAndGet(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET updated_at = ? WHERE id = ?`, time.Now().UTC().Add(-48*time.Hour), reused.ID); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneAutomaticRetryJobs(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil || pruned != 1 {
		t.Fatalf("pruned = %d %v, want 1", pruned, err)
	}
}

// TestDownloadRootsReturnsCleanAbsolutePaths 锁定 httpapi 依赖的契约：DownloadRoots 只返回
// 绝对、Clean 过、已去重、且通过安全校验的根（它直接把这些路径喂给多根沙箱解析）。
func TestDownloadRootsReturnsCleanAbsolutePaths(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	root := t.TempDir()
	if _, err := store.UpdateConfig(ctx, config.AppConfig{DownloadDir: root}); err != nil {
		t.Fatal(err)
	}
	// 构造脏数据：尾斜杠、`. ` 归一化变体、相对路径、非法根。
	now := time.Now().UTC()
	for _, dirty := range []string{root + "/", root + "/.", "some/relative/download-root"} {
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, dirty, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT OR REPLACE INTO download_roots(path, last_seen_at) VALUES (?, ?)`, string(filepath.Separator), now); err != nil {
		t.Fatal(err)
	}
	roots, err := store.DownloadRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) == 0 {
		t.Fatal("no roots returned")
	}
	seen := make(map[string]struct{}, len(roots))
	previousLen := 1 << 30
	for _, rootPath := range roots {
		if !filepath.IsAbs(rootPath) {
			t.Fatalf("root %q is not absolute", rootPath)
		}
		if filepath.Clean(rootPath) != rootPath {
			t.Fatalf("root %q is not cleaned", rootPath)
		}
		if rootPath == string(filepath.Separator) {
			t.Fatalf("unsafe root %q returned", rootPath)
		}
		if _, dup := seen[rootPath]; dup {
			t.Fatalf("root %q returned twice", rootPath)
		}
		seen[rootPath] = struct{}{}
		if len(rootPath) > previousLen {
			t.Fatalf("roots not sorted by length descending: %v", roots)
		}
		previousLen = len(rootPath)
	}
	cleanRoot := filepath.Clean(root)
	count := 0
	for _, rootPath := range roots {
		if rootPath == cleanRoot {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("current root %q returned %d times: %v", cleanRoot, count, roots)
	}
}

// TestDownloadRootSymlinkedHomeIsRejected 覆盖 config.ValidateDownloadRoot 的漏判场景：
// 家目录本身含符号链接时（macOS /tmp→/private/tmp、部分容器 /home→/var/home），候选路径
// 被 EvalSymlinks 而 home 没有，两者比较不相等 → `download_dir = 家目录` 会被漏判。
// storage 侧的 repair/seed/登记/读取统一走加固版校验，必须仍然识别为非法并修复，
// 且绝不把家目录（或其解析形态）登记进 download_roots。
func TestDownloadRootSymlinkedHomeIsRejected(t *testing.T) {
	base := t.TempDir()
	realHome := filepath.Join(base, "real-home")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	linkHome := filepath.Join(base, "link-home")
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Skipf("symlinks unsupported on this filesystem: %v", err)
	}

	cases := []struct {
		name     string
		home     string
		legacy   string
		forbid   []string
		repaired bool
	}{
		{"symlinked home as download dir", linkHome, linkHome, []string{linkHome, realHome}, true},
		{"resolved home as download dir", linkHome, realHome, []string{linkHome, realHome}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			dbPath := filepath.Join(dataDir, "test.db")
			t.Setenv("HOME", tc.home)
			store, err := Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE app_config SET download_dir = ? WHERE id = 1`, tc.legacy); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := Open(dbPath)
			if err != nil {
				t.Fatalf("unsafe symlinked-home download root must be repaired, not fatal: %v", err)
			}
			defer reopened.Close()
			ctx := context.Background()
			stored, err := reopened.GetStoredConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantFallback := filepath.Join(dataDir, "downloads")
			if tc.repaired && stored.DownloadDir != wantFallback {
				t.Fatalf("stored download dir = %q, want repaired fallback %q", stored.DownloadDir, wantFallback)
			}
			roots, err := reopened.DownloadRoots(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, root := range roots {
				for _, banned := range tc.forbid {
					if root == filepath.Clean(banned) {
						t.Fatalf("banned home path %q registered as trusted root: %v", root, roots)
					}
				}
			}
		})
	}

	// 反向保护：家目录"内部"的目录仍然合法（不能因为加固把正常用户目录误杀）。
	t.Run("directory inside symlinked home stays valid", func(t *testing.T) {
		t.Setenv("HOME", linkHome)
		validDir := filepath.Join(realHome, "downloads")
		if err := os.MkdirAll(validDir, 0o700); err != nil {
			t.Fatal(err)
		}
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "test.db")
		store, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE app_config SET download_dir = ? WHERE id = 1`, validDir); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		stored, err := reopened.GetStoredConfig(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if stored.DownloadDir != validDir {
			t.Fatalf("valid directory inside home was repaired away: %q, want %q", stored.DownloadDir, validDir)
		}
	})
}
