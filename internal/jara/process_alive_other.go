//go:build !unix

package jara

// processAlive cannot tell a dead process from a live one here, so it assumes
// the process may still run and leaves its lock alone.
func processAlive(pid int) bool { return true }
