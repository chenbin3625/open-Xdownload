package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

// validateTrustedDownloadRoot 是 config.ValidateDownloadRoot 的存储侧加固版本。
//
// 为什么需要加固：config.ValidateDownloadRoot 只对"候选路径"做 EvalSymlinks，不对
// $HOME 做。macOS 的 /tmp→/private/tmp、部分容器镜像的 /home→/var/home 等环境里，
// 当库内 download_dir 恰好是"家目录本身"时，候选被解析成 /private/tmp/home、
// home 仍是 /tmp/home，两者不相等 → 漏判。漏判的后果不是 UI 文案问题，而是：
//   - 旧库里的非法值不会被 repairDownloadDir 修复（升级后继续把整个家目录当媒体库根）；
//   - seedDownloadRoots / updateConfig 会把它登记进 download_roots；
//   - DownloadRoots 会把它返回给 httpapi 的路径沙箱。
//
// 因此在 storage 自己的四个环节（repair / seed / 登记 / 读取）统一使用本函数：
// 先复用 config 的判定（保持错误类型 ErrInvalidDownloadRoot 与文案一致），再把
// 候选与家目录都解析符号链接后逐一比较，并复查解析后的文件系统根。
func validateTrustedDownloadRoot(root string) error {
	if err := config.ValidateDownloadRoot(root); err != nil {
		return err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	resolved := evalSymlinksOrSelf(abs)
	if isFilesystemRootPath(abs) || isFilesystemRootPath(resolved) {
		return invalidDownloadRootError(abs)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	homeAbs := filepath.Clean(home)
	homeResolved := evalSymlinksOrSelf(homeAbs)
	for _, candidate := range []string{abs, resolved} {
		if sameFilesystemPath(candidate, homeAbs) || sameFilesystemPath(candidate, homeResolved) {
			return invalidDownloadRootError(abs)
		}
	}
	return nil
}

func evalSymlinksOrSelf(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func isFilesystemRootPath(path string) bool { return filepath.Dir(path) == path }

// sameFilesystemPath 比较两个已 Clean 的绝对路径；Windows 文件系统大小写不敏感。
func sameFilesystemPath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return false
}

func invalidDownloadRootError(abs string) error {
	return fmt.Errorf("%w（当前值：%s）", config.ErrInvalidDownloadRoot, abs)
}
