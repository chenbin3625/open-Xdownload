package storage

import (
	"runtime"
	"strings"
	"testing"
)

// TestSQLiteDSN 覆盖跨平台 DSN 构造：POSIX 必须走 file: URI 并百分号编码；Windows 盘符 /
// UNC 路径在无特殊字符时使用裸路径 + options（避免 `file://C:/...` 非法 authority），在包含
// `#` 等 URI 特殊字符时必须走空 authority 的 `file:` URI 并百分号编码，防止截断路径和丢弃 PRAGMA。
// 通过 sqliteDSNForOS 在 Linux CI 上覆盖两种平台。
func TestSQLiteDSN(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		path        string
		wantPrefix  string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "posix absolute path uses file uri",
			goos:        "linux",
			path:        "/tmp/open-xdownload/data.db",
			wantPrefix:  "file:///tmp/open-xdownload/data.db?",
			wantContain: []string{"_txlock=immediate"},
			wantAbsent:  []string{"file://tmp"},
		},
		{
			name:        "posix path with uri characters is percent encoded",
			goos:        "linux",
			path:        "/tmp/archive?#.db",
			wantPrefix:  "file:///tmp/archive%3F%23.db?",
			wantContain: []string{"%3F", "%23", "_pragma=busy_timeout(5000)"},
		},
		{
			name:        "posix path starting with drive-like prefix stays file uri",
			goos:        "linux",
			path:        "C:archive?#.db",
			wantPrefix:  "file:///",
			wantContain: []string{"C:archive%3F%23.db?", "_txlock=immediate"},
		},
		{
			name:        "windows drive letter uses raw path",
			goos:        "windows",
			path:        "C:/Users/dev/AppData/Roaming/open-xdownload/data.db",
			wantPrefix:  "C:/Users/dev/AppData/Roaming/open-xdownload/data.db?",
			wantContain: []string{"_txlock=immediate"},
			wantAbsent:  []string{"file:"},
		},
		{
			name:        "windows backslash drive letter uses raw path",
			goos:        "windows",
			path:        `C:\Users\dev\AppData\Local\data.db`,
			wantPrefix:  `C:\Users\dev\AppData\Local\data.db?`,
			wantContain: []string{"_pragma=foreign_keys(1)"},
			wantAbsent:  []string{"file:"},
		},
		{
			name:        "windows drive letter with hash is percent encoded as empty-authority file uri",
			goos:        "windows",
			path:        `C:\Users\dev#1\AppData\data#2.db`,
			wantPrefix:  "file:///C:/Users/dev%231/AppData/data%232.db?",
			wantContain: []string{"%231", "%232", "_txlock=immediate", "_pragma=foreign_keys(1)"},
			wantAbsent:  []string{"file://C:", "#"},
		},
		{
			name:        "windows unc path with hash is percent encoded",
			goos:        "windows",
			path:        `\\server\share\dir#1\data.db`,
			wantPrefix:  "file:////server/share/dir%231/data.db?",
			wantContain: []string{"%231", "_txlock=immediate"},
			wantAbsent:  []string{"#"},
		},
		{
			name:        "windows relative path with hash is percent encoded",
			goos:        "windows",
			path:        `dir#1\data.db`,
			wantPrefix:  "file:dir%231/data.db?",
			wantContain: []string{"%231", "_txlock=immediate"},
			wantAbsent:  []string{"#"},
		},
		{
			name:        "windows drive path with percent and hash is percent encoded",
			goos:        "windows",
			path:        `C:\Users\100%dev#1\data.db`,
			wantPrefix:  "file:///C:/Users/100%25dev%231/data.db?",
			wantContain: []string{"100%25dev%231", "_txlock=immediate", "_pragma=foreign_keys(1)"},
			wantAbsent:  []string{"file://C:", "#"},
		},
		{
			name:        "unc forward slashes uses raw path",
			goos:        "windows",
			path:        "//server/share/open-xdownload/data.db",
			wantPrefix:  "//server/share/open-xdownload/data.db?",
			wantContain: []string{"_txlock=immediate"},
			wantAbsent:  []string{"file:"},
		},
		{
			name:        "unc backslashes uses raw path",
			goos:        "windows",
			path:        `\\server\share\open-xdownload\data.db`,
			wantPrefix:  `\\server\share\open-xdownload\data.db?`,
			wantContain: []string{"_txlock=immediate"},
			wantAbsent:  []string{"file:"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.goos == "linux" && runtime.GOOS == "windows" {
				t.Skip("POSIX path prefix expectations apply on non-Windows hosts")
			}
			dsn, err := sqliteDSNForOS(tc.path, tc.goos)
			if err != nil {
				t.Fatalf("sqliteDSNForOS(%q, %q): %v", tc.path, tc.goos, err)
			}
			if !strings.HasPrefix(dsn, tc.wantPrefix) {
				t.Fatalf("dsn = %q, want prefix %q", dsn, tc.wantPrefix)
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(dsn, want) {
					t.Fatalf("dsn = %q, want to contain %q", dsn, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(dsn, absent) {
					t.Fatalf("dsn = %q must not contain %q", dsn, absent)
				}
			}
		})
	}
}

// TestIsWindowsStylePath 确保裸路径分支仅在 Windows 生效，POSIX 下任何路径都走 file: URI。
func TestIsWindowsStylePath(t *testing.T) {
	for _, path := range []string{`C:\data.db`, "C:/data.db", `\\server\share\data.db`, "//server/share/data.db", "data.db"} {
		if !isWindowsStylePathForOS(path, "windows") {
			t.Fatalf("isWindowsStylePathForOS(%q, windows) = false, want true", path)
		}
	}
	for _, path := range []string{`C:\data.db`, "C:/data.db", `\\server\share\data.db`, "//server/share/data.db", "/tmp/data.db", "data.db", "./data.db", "/tmp/C:/data.db"} {
		if isWindowsStylePathForOS(path, "linux") {
			t.Fatalf("isWindowsStylePathForOS(%q, linux) = true, want false", path)
		}
	}
	if runtime.GOOS != "windows" && isWindowsStylePath("C:/data.db") {
		t.Fatal("isWindowsStylePath must return false on non-Windows")
	}
}
