//go:build windows

package storage

func diskUsageForPath(_ string) (totalBytes, freeBytes uint64, ok bool) {
	return 0, 0, false
}
