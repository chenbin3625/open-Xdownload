package storage

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// sqliteDSN builds the driver DSN for a database path.
//
// 这里必须同时满足三件事：
//  1. POSIX 路径可能包含 `?`/`#`（用户可以把库放在任意文件名下），必须通过 `file:`
//     URI + 百分号编码传给 SQLite，否则会被当成 URI 的 query/fragment 截断，导致
//     "在错误的位置建库"甚至静默打开另一个文件；
//  2. Windows 绝对路径形如 `C:\data\x.db` / `C:/data/x.db`。`url.URL{Scheme:"file",
//     Path:"C:/..."}` 会生成 `file://C:/...`，SQLite 的 URI 解析把 `C:` 当成 authority，
//     modernc 未编译 SQLITE_ALLOW_URI_AUTHORITY，会直接报
//     `SQL logic error: invalid uri authority: C: (1)`，Windows 发行版因此无法启动；
//  3. Windows 路径同样允许 `#`/`%`（例如 `C:\Users\dev#1\data.db`）。modernc.org/sqlite
//     打开数据库时始终携带 SQLITE_OPEN_URI，若裸路径含 `#`/`%` 而不做百分号编码，SQLite 会把
//     `#` 之后全部当作 URI fragment 丢弃，既截断路径又丢弃所有 PRAGMA/query 选项。
//
// 因此按路径形态分流：
//   - Windows 路径且不含 `#`/`?`/`%`：使用裸路径 + query options。
//   - Windows 路径但包含 `#`/`?`/`%`：改用空 authority 的 `file:` URI（盘符路径加前导 `/`
//     生成 `file:///C:/...`，相对路径设置 `OmitHost: true` 生成 `file:...`），对 `#`/`?`/`%`
//     做百分号编码，同时避免 `file://C:/...` 触发非法 authority 错误。
//   - 其余（POSIX）：`file:` URI，保持原有百分号编码行为。
func sqliteDSN(path string) (string, error) {
	return sqliteDSNForOS(path, runtime.GOOS)
}

func sqliteDSNForOS(path, goos string) (string, error) {
	if isWindowsStylePathForOS(path, goos) {
		if !strings.ContainsAny(path, "#?%") {
			return path + sqliteOpenOptions, nil
		}
		return windowsURIDSN(path), nil
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

// windowsURIDSN encodes a Windows path containing '#', '?', or '%' as a SQLite file: URI
// without introducing a non-empty URI authority (which modernc.org/sqlite rejects).
func windowsURIDSN(path string) string {
	slashPath := strings.ReplaceAll(path, "\\", "/")
	switch {
	case len(slashPath) >= 2 && slashPath[1] == ':' && isASCIIAlpha(slashPath[0]):
		// Drive letter path: prefix '/' so url.URL renders file:///C:/... with empty authority.
		uri := (&url.URL{Scheme: "file", Path: "/" + slashPath}).String()
		return uri + sqliteOpenOptions
	case strings.HasPrefix(slashPath, "/"):
		// Rooted or UNC path (e.g. //server/share/... -> file:////server/share/...).
		uri := (&url.URL{Scheme: "file", Path: slashPath}).String()
		return uri + sqliteOpenOptions
	default:
		// Relative path: omit host to produce file:rel/path%231.db.
		uri := (&url.URL{Scheme: "file", OmitHost: true, Path: slashPath}).String()
		return uri + sqliteOpenOptions
	}
}

// isWindowsStylePath 判断路径是否走 Windows 分支。仅在 Windows 运行时生效，
// 避免 POSIX 下的相对路径（如 `C:foo?#.db` 或 `//foo?#.db`）跳过 POSIX file: URI 编码。
// 跨平台测试通过 isWindowsStylePathForOS / sqliteDSNForOS 传入目标 GOOS 覆盖。
func isWindowsStylePath(path string) bool {
	return isWindowsStylePathForOS(path, runtime.GOOS)
}

func isWindowsStylePathForOS(path, goos string) bool {
	if goos != "windows" {
		return false
	}
	if len(path) >= 2 && path[1] == ':' && isASCIIAlpha(path[0]) {
		return true
	}
	// UNC：`\\server\share` 或 `//server/share`。
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	return true
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
