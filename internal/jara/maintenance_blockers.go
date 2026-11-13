package jara

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/textutil"
)

// updateBlockers collects everything that defers an update: each blocker is
// one line naming a session, workspace operation or SSH connection.
type updateBlockers struct {
	reasons  []string
	blockers []string
}

func (b *updateBlockers) add(reason, blocker string) {
	if !containsString(b.reasons, reason) {
		b.reasons = append(b.reasons, reason)
	}
	b.blockers = append(b.blockers, blocker)
}

func (b *updateBlockers) status() maintenanceStatus {
	if len(b.blockers) == 0 {
		return maintenanceStatus{}
	}
	return maintenanceStatus{Busy: true, Reason: strings.Join(b.reasons, "; "), Blockers: b.blockers}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func updateBusy() (maintenanceStatus, error) {
	var b updateBlockers
	if err := tmuxUpdateBlockers(&b); err != nil {
		return maintenanceStatus{}, err
	}
	if err := workspaceUpdateBlockers(&b); err != nil {
		return maintenanceStatus{}, err
	}
	if err := sshUpdateBlockers(&b); err != nil {
		return maintenanceStatus{}, err
	}
	return b.status(), nil
}

func tmuxUpdateBlockers(b *updateBlockers) error {
	// Count every session on Jara's socket, including sessions without metadata.
	out, err := tmux("list-sessions", "-F", "#{session_name}|#{@jara_background}|#{pane_dead}|#{session_attached}").Output()
	if err != nil {
		_, e := sessions()
		return e
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "|")
		if line == "" || len(fields) >= 3 && fields[1] == "1" && fields[2] == "1" {
			continue
		}
		state := "detached"
		if len(fields) >= 4 && fields[3] != "" && fields[3] != "0" {
			state = "attached"
		}
		b.add("tmux sessions exist (including detached or idle sessions)",
			fmt.Sprintf("terminal session %s (%s)", textutil.SanitizeText(fields[0]), state))
	}
	return nil
}

func workspaceUpdateBlockers(b *updateBlockers) error {
	locks, err := filepath.Glob(filepath.Join(fsutil.DataRoot(), "workspaces", "*.lock"))
	if err != nil {
		return err
	}
	for _, lock := range locks {
		if _, idle := idleChatDaemonWorkspace(lock, true); idle {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(lock), ".lock")
		label := workspaceBlockerLabel(id)
		if running, busy := chatDaemonIdleState(id, true); running && busy != "" {
			b.add("workspace operation in progress", fmt.Sprintf("Jara session in %s: %s", label, busy))
			continue
		}
		blocker := "workspace operation in " + label
		if pid := lockMarkerPID(filepath.Join(lock, workspaceLockOwnerFile)); pid != 0 {
			blocker += fmt.Sprintf(" by PID %d", pid)
			if command := processCommand(pid); command != "" {
				blocker += " (" + command + ")"
			}
		}
		b.add("workspace operation in progress", blocker)
	}
	return nil
}

func workspaceBlockerLabel(id string) string {
	if !nameRE.MatchString(id) {
		return textutil.SanitizeText(id)
	}
	if w, err := workspaceInfo(id); err == nil && w.Name != "" {
		return fmt.Sprintf("%s (%s)", textutil.SanitizeText(w.Name), id)
	}
	return id
}

type processInfo struct {
	parent  int
	sshd    bool
	command string
}

func sshUpdateBlockers(b *updateBlockers) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	processes := map[int]processInfo{}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil {
			continue
		}
		processes[pid] = processInfo{parent: parentPID(pid), sshd: sshdProcess(pid), command: processCommand(pid)}
	}
	// Connections that only carry reconnecting Jara clients do not count.
	for _, root := range sshConnectionRoots(processes, reconnectingRelayAncestors()) {
		b.add("SSH connections are open", describeSSHConnection(root, processes))
	}
	return nil
}

// sshConnectionRoots finds one process per SSH connection: the listener is an
// sshd without an sshd parent, and each of its sshd children is a connection.
func sshConnectionRoots(processes map[int]processInfo, skip map[int]bool) []int {
	var roots []int
	for pid, process := range processes {
		parent, ok := processes[process.parent]
		if process.sshd && ok && parent.sshd && !processes[parent.parent].sshd && !skip[pid] {
			roots = append(roots, pid)
		}
	}
	sort.Ints(roots)
	return roots
}

// describeSSHConnection names the connection's user and the programs it runs.
func describeSSHConnection(root int, processes map[int]processInfo) string {
	user := sshConnectionUser(processes[root].command)
	var programs []string
	for pid, process := range processes {
		if process.sshd || !processes[process.parent].sshd || !descendsFrom(pid, root, processes) {
			continue
		}
		if !containsString(programs, process.command) {
			programs = append(programs, process.command)
		}
	}
	sort.Strings(programs)
	description := "SSH connection"
	if user != "" && user != "unknown" {
		description += " by " + textutil.SanitizeText(user)
	}
	if len(programs) == 0 {
		return description + " (no program running yet)"
	}
	return description + " running " + textutil.SanitizeText(strings.Join(programs, ", "))
}

// sshConnectionUser reads the user from an sshd process title such as
// "sshd-session: alice [priv]" or "sshd: alice@pts/0".
func sshConnectionUser(title string) string {
	_, rest, found := strings.Cut(title, ": ")
	fields := strings.Fields(rest)
	if !found || len(fields) == 0 {
		return ""
	}
	return strings.SplitN(fields[0], "@", 2)[0]
}

func descendsFrom(pid, ancestor int, processes map[int]processInfo) bool {
	for steps := 0; pid > 1 && steps < 64; steps++ {
		if pid == ancestor {
			return true
		}
		pid = processes[pid].parent
	}
	return false
}

// processCommand summarizes a command line as its program and subcommand.
func processCommand(pid int) string {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(raw) == 0 {
		return ""
	}
	args := strings.Split(string(bytes.TrimRight(raw, "\x00")), "\x00")
	if len(args) == 1 {
		return args[0] // sshd rewrites its title as one string
	}
	command := filepath.Base(args[0])
	if second := args[1]; !strings.HasPrefix(second, "-") && len(second) <= 24 {
		command += " " + second
	}
	return command
}
