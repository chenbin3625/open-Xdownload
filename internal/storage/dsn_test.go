package storage

import (
	"strings"
	"testing"
)

// TestSQLiteDSN 覆盖跨平台 DSN 构造：POSIX 必须走 file: URI 并百分号编码；Windows 盘符 /
// UNC 路径必须使用裸路径 + options（`file://C:/...` 会被 SQLite 判为非法 authority，
// Windows 发行版启动即失败）。这些用例不依赖 Windows，纯字符串断言。
func TestSQLiteDSN(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantPrefix  string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "posix absolute path uses file uri",
			path:        "/tmp/open-xdownload/data.db",
			wantPrefix:  "file:///tmp/open-xdownload/data.db?",
			wantContain: []string{"_txlock=immediate"},
			wantAbsent:  []string{"file://tmp"},
		},
		{
			name:        "posix path with uri characters is percent encoded",
			path:        "/tmp/archive?#.db",
			wantPrefix:  "file:///tmp/archive%3F%23.db?",
			wantContain: []string{"%3F", "%23", "_pragma=busy_timeout(5000)"},
		},
		{
			name:        "windows drive letter uses raw path",
			path:        "C:/Users/dev/AppData/Roaming/open-xdownload/data.db",
			wantPrefix:  "C:/Users/dev/AppData/Roaming/open-xdownload/data.db?",
			wantContain: []string{"_txlock=immediate"},
			wantAbsent:  []string{"file:"},
		},
		{
			name:        "windows backslash drive letter uses raw path",
			path:        `C:\Users\dev\AppData\Local\data.db`,
			wantPrefix:  `C:\Users\dev\AppData\Local\data.db?`,
			wantContain: []string{"_pragma=foreign_keys(1)"},
			wantAbsent:  []string{"file:"},
		},
		{
			name:        "unc forward slashes uses raw path",
			path:        "//server/share/open-xdownload/data.db",
			wantPrefix:  "//server/share/open-xdownload/data.db?",
			wantContain: []string{"_txlock=immediate"},
			wantAbsent:  []string{"file:"},
		},
		{
			name:        "unc backslashes uses raw path",
			path:        `\\server\share\open-xdownload\data.db`,
			wantPrefix:  `\\server\share\open-xdownload\data.db?`,
			wantContain: []string{"_txlock=immediate"},
			wantAbsent:  []string{"file:"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dsn, err := sqliteDSN(tc.path)
			if err != nil {
				t.Fatalf("sqliteDSN(%q): %v", tc.path, err)
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

// TestIsWindowsStylePath 确保盘符/UNC 判定不依赖运行平台（否则 Linux CI 无法覆盖）。
func TestIsWindowsStylePath(t *testing.T) {
	for _, path := range []string{`C:\data.db`, "C:/data.db", `\\server\share\data.db`, "//server/share/data.db"} {
		if !isWindowsStylePath(path) {
			t.Fatalf("isWindowsStylePath(%q) = false, want true", path)
		}
	}
	for _, path := range []string{"/tmp/data.db", "data.db", "./data.db", "/tmp/C:/data.db"} {
		if isWindowsStylePath(path) {
			t.Fatalf("isWindowsStylePath(%q) = true, want false", path)
		}
	}
}
