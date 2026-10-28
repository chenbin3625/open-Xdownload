package storage

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// sqliteDSN builds the driver DSN for a database path.
//
// 这里必须同时满足两件事：
//  1. POSIX 路径可能包含 `?`/`#`（用户可以把库放在任意文件名下），必须通过 `file:`
//     URI + 百分号编码传给 SQLite，否则会被当成 URI 的 query/fragment 截断，导致
//     "在错误的位置建库"甚至静默打开另一个文件；
//  2. Windows 绝对路径形如 `C:\data\x.db` / `C:/data/x.db`。`url.URL{Scheme:"file",
//     Path:"C:/..."}` 会生成 `file://C:/...`，SQLite 的 URI 解析把 `C:` 当成 authority，
//     modernc 未编译 SQLITE_ALLOW_URI_AUTHORITY，会直接报
//     `SQL logic error: invalid uri authority: C: (1)`，Windows 发行版因此无法启动。
//
// 因此按路径形态分流：
//   - Windows 盘符路径 / UNC 路径（以及运行在 Windows 上的任何路径）：使用裸路径 +
//     query options。modernc.org/sqlite 自己会拆分 `?` 之后的参数（不要求 file: 前缀），
//     而 Windows 文件名本身不允许 `?`/`#`，所以不存在编码问题。
//   - 其余（POSIX）：`file:` URI，保持原有百分号编码行为。
func sqliteDSN(path string) (string, error) {
	if isWindowsStylePath(path) {
		return path + sqliteOpenOptions, nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Path 必须以 "/" 开头，url.URL 才会输出 file:///... 而不是 file://host/...
	uriPath := filepath.ToSlash(absPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath}).String()
	return uri + sqliteOpenOptions, nil
}

// isWindowsStylePath 判断路径是否必须走"裸路径"分支。除了运行平台，还按路径字符串
// 识别盘符与 UNC，保证在非 Windows 上也能用 table-driven 测试覆盖这些形态。
func isWindowsStylePath(path string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	if len(path) >= 2 && path[1] == ':' && isASCIIAlpha(path[0]) {
		return true
	}
	// UNC：`\\server\share` 或 `//server/share`。
	return strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//")
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
