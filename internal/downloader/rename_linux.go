//go:build linux

package downloader

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func renameNoReplace(oldPath string, newPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
		return errNoReplaceUnsupported
	}
	return err
}
