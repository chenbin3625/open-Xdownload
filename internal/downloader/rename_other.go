//go:build !linux

package downloader

func renameNoReplace(oldPath string, newPath string) error {
	return errNoReplaceUnsupported
}
