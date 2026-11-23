//go:build !linux

package filestore

func renameNoReplace(oldPath string, newPath string) error {
	return errNoReplaceUnsupported
}
