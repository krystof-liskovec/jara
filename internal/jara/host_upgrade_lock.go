package jara

import (
	"fmt"
	"os"
	"path/filepath"
)

func hostUpgradeLockPath(root string) string {
	return filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".upgrade.lock")
}

func checkHostUpgradeLock(root string) error {
	_, err := os.Stat(hostUpgradeLockPath(root))
	if err == nil {
		return fmt.Errorf("Jara deployment upgrade is in progress; retry after it finishes")
	}
	if !os.IsNotExist(err) {
		return err
	}
	return nil
}
