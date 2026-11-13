//go:build linux

package jara

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// chatPeerPID returns the process on the other end of a local socket.
func chatPeerPID(conn net.Conn) int {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		if cred, credErr := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED); credErr == nil {
			pid = int(cred.Pid)
		}
	})
	return pid
}

// reconnectingChatRelay reports whether pid is a `jara _chat-server` relay
// started by a client that reconnects on its own after the session stops.
func reconnectingChatRelay(pid int) bool {
	if pid < 2 {
		return false
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	return reconnectingRelayArgs(strings.Split(string(bytes.TrimRight(raw, "\x00")), "\x00"))
}

// reconnectingRelayAncestors lists every ancestor of reconnecting relays, so
// the SSH connections carrying them do not count as open interactive sessions.
func reconnectingRelayAncestors() map[int]bool {
	return processAncestors(reconnectingChatRelay)
}

func processAncestors(match func(pid int) bool) map[int]bool {
	ancestors := map[int]bool{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ancestors
	}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || !match(pid) {
			continue
		}
		for parent := parentPID(pid); parent > 1 && !ancestors[parent]; parent = parentPID(parent) {
			ancestors[parent] = true
		}
	}
	return ancestors
}
