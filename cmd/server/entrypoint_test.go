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
