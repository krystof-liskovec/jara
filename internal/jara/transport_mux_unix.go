//go:build unix

package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Opening a session makes several short remote calls in a row (claim, branch
// and commit checks, file lists, checkpoints). Each new SSH connection pays
// for TCP, key exchange and authentication, often most of a second, so short
// calls share one connection: the first starts an OpenSSH master that the
// next reuse, and it exits after sshMuxPersist without calls. Long-lived
// sessions (terminals, chat streams, tunnels) keep their own connections, so a
// dropped network never strands them behind a shared one.
// JARA_SSH_MULTIPLEX=0 turns sharing off.

// sshMuxPersist keeps an idle master briefly: long enough to cover a start
// or a sync, short enough that a master from before a sleep is gone after it.
const sshMuxPersist = "60s"

// sshSocketPathMax is the smallest Unix socket path limit (macOS: 104 bytes,
// including the terminator). OpenSSH binds a temporary name 17 bytes longer
// than ControlPath before renaming it.
const sshSocketPathMax = 104 - 1 - 17

// sshMuxArgs returns the options that share a connection to r, or nothing
// when no private directory gives a short enough socket path.
func sshMuxArgs(r Remote) []string {
	if os.Getenv("JARA_SSH_MULTIPLEX") == "0" {
		return nil
	}
	// Everything that changes how ssh authenticates or verifies the host
	// names its own master: remotes with different keys never share one.
	sum := sha256.Sum256([]byte(strings.Join([]string{r.Host, strconv.Itoa(r.Port), r.User, r.Identity, r.KnownHosts, os.Getenv("JARA_SSH_CONFIG")}, "\x00")))
	name := hex.EncodeToString(sum[:10])
	for _, dir := range sshMuxDirs() {
		path := filepath.Join(dir, name)
		if len(path) <= sshSocketPathMax && !strings.ContainsAny(path, " \t\"'%") && privateDir(dir) {
			return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + path, "-o", "ControlPersist=" + sshMuxPersist}
		}
	}
	return nil
}

// sshMuxDirs lists where sockets may go: the per-user runtime directory when
// there is one, then a directory in /tmp (not $TMPDIR, whose macOS path is
// too long for sockets).
func sshMuxDirs() []string {
	var dirs []string
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(runtime) {
		dirs = append(dirs, filepath.Join(runtime, "jara-ssh"))
	}
	return append(dirs, filepath.Join("/tmp", fmt.Sprintf("jara-ssh-%d", os.Getuid())))
}

// privateDir creates dir when missing and reports whether it is a real
// directory owned by this user that nobody else can enter. In a shared /tmp
// another user may have created the name first; then sharing stays off.
func privateDir(dir string) bool {
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return false
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
