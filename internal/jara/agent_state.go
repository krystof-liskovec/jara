package jara

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type agentMetadata struct {
	session string
	state   string
	pid     int
}

type agentProcess struct {
	parent int
	args   []string
}

func parseAgentMetadata(session, state, rawPID string) (agentMetadata, bool) {
	state = strings.TrimSpace(state)
	if !validAgentState(state) {
		return agentMetadata{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil || pid <= 0 {
		return agentMetadata{}, false
	}
	return agentMetadata{session: session, state: state, pid: pid}, true
}

func validAgentState(state string) bool {
	switch state {
	case "starting", "running", "waiting", "idle":
		return true
	default:
		return false
	}
}

func agentStateProtectsFromCleanup(state string) bool {
	return state == "starting" || state == "running" || state == "waiting"
}

// liveAgentStates trusts metadata only while its recorded Jara UI process is
// still running beneath a pane in the same tmux session.
func liveAgentStates(metadata []agentMetadata) (map[string]string, error) {
	states := map[string]string{}
	valid := make([]agentMetadata, 0, len(metadata))
	for _, item := range metadata {
		if item.session != "" && item.pid > 0 && validAgentState(item.state) {
			valid = append(valid, item)
		}
	}
	if len(valid) == 0 {
		return states, nil
	}

	panesOutput, err := tmux("list-panes", "-a", "-F", "#{session_name}\t#{pane_pid}").Output()
	if err != nil {
		return nil, err
	}
	roots := map[string]map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(panesOutput)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			continue
		}
		if roots[fields[0]] == nil {
			roots[fields[0]] = map[int]bool{}
		}
		roots[fields[0]][pid] = true
	}

	output, err := exec.Command("ps", "-eo", "pid=,ppid=,args=").Output()
	if err != nil {
		return nil, err
	}
	processes := parseAgentProcessTable(string(output))
	for _, item := range valid {
		proc, ok := processes[item.pid]
		if !ok || !isJaraChatProcess(proc.args) || !descendsFromPane(item.pid, roots[item.session], processes) {
			continue
		}
		states[item.session] = item.state
	}
	return states, nil
}

func parseAgentProcessTable(output string) map[int]agentProcess {
	processes := map[int]agentProcess{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		parent, errParent := strconv.Atoi(fields[1])
		if errPID != nil || errParent != nil {
			continue
		}
		processes[pid] = agentProcess{parent: parent, args: fields[2:]}
	}
	return processes
}

func isJaraChatProcess(args []string) bool {
	return len(args) >= 2 && filepath.Base(args[0]) == "jara" && args[1] == "_chat"
}

func descendsFromPane(pid int, paneRoots map[int]bool, processes map[int]agentProcess) bool {
	for steps := 0; steps < 100 && pid > 0; steps++ {
		if paneRoots[pid] {
			return true
		}
		proc, ok := processes[pid]
		if !ok || proc.parent == pid {
			return false
		}
		pid = proc.parent
	}
	return false
}
