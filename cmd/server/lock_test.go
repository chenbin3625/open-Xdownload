package main

import (
	"path/filepath"
	"testing"
)

func TestDataLockRejectsSecondOwnerAndReleasesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.lock")
	first, err := acquireDataLock(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := acquireDataLock(path)
	if err == nil {
		_ = second.Close()
		t.Fatal("two servers acquired the same data directory")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireDataLock(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = third.Close()
}
