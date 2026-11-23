package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEntrypointRejectsRootBeforeChown(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	entrypoint, err := filepath.Abs("../../docker-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"/", "//", "/tmp/..", "safe"} {
		for _, target := range []string{"data", "downloads"} {
			t.Run(target+root, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				data, downloads := t.TempDir(), t.TempDir()
				if root == "safe" {
					// Keep both temporary directories for the valid-path case.
				} else if target == "data" {
					data = root
				} else {
					downloads = root
				}
				// The script path is positional to avoid interpolating shell source.
				cmd := exec.CommandContext(ctx, bash, "-c", `
id() { echo 0; }
stat() { echo 0:0; }
chown() { echo CHOWN_CALLED; }
exec() { echo EXEC_CALLED; }
script=$1
set -- open-xdownload
source "$script"
`, "entrypoint-test", entrypoint)
				cmd.Env = append(os.Environ(), "PUID=1000", "PGID=1000", "OPEN_XDOWNLOAD_DATA_DIR="+data, "OPEN_XDOWNLOAD_DOWNLOAD_DIR="+downloads)
				output, err := cmd.CombinedOutput()
				if root == "safe" {
					if err != nil || strings.Count(string(output), "CHOWN_CALLED") != 2 || !strings.Contains(string(output), "EXEC_CALLED") {
						t.Fatalf("safe paths must launch: err=%v output=%s", err, output)
					}
					return
				}
				if err == nil || strings.Contains(string(output), "CHOWN_CALLED") || strings.Contains(string(output), "EXEC_CALLED") {
					t.Fatalf("root must fail before ownership or launch: err=%v output=%s", err, output)
				}
			})
		}
	}
}

func TestEntrypointRejectsSymlinksAndSkipsMatchingChown(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	entrypoint, err := filepath.Abs("../../docker-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("rejects symlinked dir", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		base := t.TempDir()
		realDir := filepath.Join(base, "real")
		if err := os.MkdirAll(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(base, "link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		cmd := exec.CommandContext(ctx, bash, "-c", `
id() { echo 0; }
stat() { echo 0:0; }
chown() { echo CHOWN_CALLED; }
exec() { echo EXEC_CALLED; }
script=$1
set -- open-xdownload
source "$script"
`, "entrypoint-test", entrypoint)
		cmd.Env = append(os.Environ(), "PUID=1000", "PGID=1000", "OPEN_XDOWNLOAD_DATA_DIR="+linkDir, "OPEN_XDOWNLOAD_DOWNLOAD_DIR="+t.TempDir())
		output, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(output), "CHOWN_CALLED") || strings.Contains(string(output), "EXEC_CALLED") {
			t.Fatalf("symlink dir must fail before chown/exec: err=%v output=%s", err, output)
		}
	})

	t.Run("rejects symlinked dir with trailing slashes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		base := t.TempDir()
		realDir := filepath.Join(base, "real")
		if err := os.MkdirAll(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(base, "link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		for _, suffix := range []string{"/", "///"} {
			cmd := exec.CommandContext(ctx, bash, "-c", `
id() { echo 0; }
stat() { echo 0:0; }
chown() { echo CHOWN_CALLED; }
exec() { echo EXEC_CALLED; }
script=$1
set -- open-xdownload
source "$script"
`, "entrypoint-test", entrypoint)
			cmd.Env = append(os.Environ(), "PUID=1000", "PGID=1000", "OPEN_XDOWNLOAD_DATA_DIR="+linkDir+suffix, "OPEN_XDOWNLOAD_DOWNLOAD_DIR="+t.TempDir())
			output, err := cmd.CombinedOutput()
			if err == nil || strings.Contains(string(output), "CHOWN_CALLED") || strings.Contains(string(output), "EXEC_CALLED") {
				t.Fatalf("trailing-slash symlink (%q) must fail before chown/exec: err=%v output=%s", suffix, err, output)
			}
		}
	})

	t.Run("rejects parent symlink existing and nonexistent subdirs without creating target", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		base := t.TempDir()
		realDir := filepath.Join(base, "real")
		existingSub := filepath.Join(realDir, "existing")
		if err := os.MkdirAll(existingSub, 0o700); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(base, "link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		for _, sub := range []string{"existing", "existing/", "newsub", "newsub/nested"} {
			cmd := exec.CommandContext(ctx, bash, "-c", `
id() { echo 0; }
stat() { echo 0:0; }
chown() { echo CHOWN_CALLED; }
exec() { echo EXEC_CALLED; }
script=$1
set -- open-xdownload
source "$script"
`, "entrypoint-test", entrypoint)
			cmd.Env = append(os.Environ(), "PUID=1000", "PGID=1000", "OPEN_XDOWNLOAD_DATA_DIR="+filepath.Join(linkDir, sub), "OPEN_XDOWNLOAD_DOWNLOAD_DIR="+t.TempDir())
			output, err := cmd.CombinedOutput()
			if err == nil || strings.Contains(string(output), "CHOWN_CALLED") || strings.Contains(string(output), "EXEC_CALLED") {
				t.Fatalf("parent symlink (%q) must fail before chown/exec: err=%v output=%s", sub, err, output)
			}
		}
		if _, err := os.Stat(filepath.Join(realDir, "newsub")); !os.IsNotExist(err) {
			t.Fatalf("mkdir must not create directories through symlinked parent: stat err=%v", err)
		}
	})

	t.Run("strips trailing slashes before chown -h", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		dataDir := t.TempDir()
		downloadDir := t.TempDir()
		cmd := exec.CommandContext(ctx, bash, "-c", `
id() { echo 0; }
stat() { echo 0:0; }
chown() { echo "CHOWN:$*"; }
exec() { echo EXEC_CALLED; }
script=$1
set -- open-xdownload
source "$script"
`, "entrypoint-test", entrypoint)
		cmd.Env = append(os.Environ(), "PUID=1000", "PGID=1000", "OPEN_XDOWNLOAD_DATA_DIR="+dataDir+"///", "OPEN_XDOWNLOAD_DOWNLOAD_DIR="+downloadDir+"/")
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "EXEC_CALLED") {
			t.Fatalf("valid trailing-slash dirs must launch: err=%v output=%s", err, output)
		}
		wantData := "CHOWN:-h 1000:1000 -- " + dataDir
		wantDL := "CHOWN:-h 1000:1000 -- " + downloadDir
		if !strings.Contains(string(output), wantData) || !strings.Contains(string(output), wantDL) {
			t.Fatalf("chown did not receive -h and stripped paths: output=%s", output)
		}
	})

	t.Run("skips chown when ownership matches", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bash, "-c", `
id() { echo 0; }
stat() { echo 1000:1000; }
chown() { echo CHOWN_CALLED; }
exec() { echo EXEC_CALLED:$*; }
script=$1
set -- open-xdownload
source "$script"
`, "entrypoint-test", entrypoint)
		cmd.Env = append(os.Environ(), "PUID=1000", "PGID=1000", "OPEN_XDOWNLOAD_DATA_DIR="+t.TempDir(), "OPEN_XDOWNLOAD_DOWNLOAD_DIR="+t.TempDir())
		output, err := cmd.CombinedOutput()
		if err != nil || strings.Contains(string(output), "CHOWN_CALLED") || !strings.Contains(string(output), "EXEC_CALLED:su-exec 1000:1000") {
			t.Fatalf("matching ownership should skip chown and su-exec: err=%v output=%s", err, output)
		}
	})

	t.Run("drops privileges when only PGID is non-zero", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bash, "-c", `
id() { echo 0; }
stat() { echo 0:0; }
chown() { echo CHOWN_CALLED; }
exec() { echo EXEC_CALLED:$*; }
script=$1
set -- open-xdownload
source "$script"
`, "entrypoint-test", entrypoint)
		cmd.Env = append(os.Environ(), "PUID=0", "PGID=1000", "OPEN_XDOWNLOAD_DATA_DIR="+t.TempDir(), "OPEN_XDOWNLOAD_DOWNLOAD_DIR="+t.TempDir())
		output, err := cmd.CombinedOutput()
		if err != nil || strings.Count(string(output), "CHOWN_CALLED") != 2 || !strings.Contains(string(output), "EXEC_CALLED:su-exec 0:1000") {
			t.Fatalf("PUID=0 PGID=1000 should chown and su-exec: err=%v output=%s", err, output)
		}
	})
}
