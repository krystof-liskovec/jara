//go:build linux

package jara

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Kernel-managed locking releases the gate even if a process is killed.
func lockLifecycle() (func(), error) {
	if err := os.MkdirAll(dataRoot(), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataRoot(), ".lifecycle.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock lifecycle: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
