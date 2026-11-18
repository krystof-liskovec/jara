//go:build unix

package agentrouter

import (
	"os"
	"syscall"
)

// lockAccounts serializes pool changes between the sessions on a machine.
func lockAccounts(path string) func() {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		f.Close()
		return func() {}
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
}
