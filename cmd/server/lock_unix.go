//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquireDataLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("data directory is already in use: %w", err)
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
}
