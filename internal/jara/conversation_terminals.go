package jara

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Recover explicit resume IDs from existing processes. Never infer identity
// from a shared working directory: several conversations can use the same files.
func linkConversationTerminals(threads []Conversation, terminals []Session) {
	raw, err := tmux("list-panes", "-a", "-F", "#{session_name}|#{pane_pid}").Output()
	if err != nil {
		return
	}
	processes, err := exec.Command("ps", "-eo", "pid=,ppid=,args=").Output()
	if err != nil {
		return
	}
	type process struct {
		parent int
		args   []string
	}
	tree := map[int]process{}
	for _, line := range strings.Split(string(processes), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		parent, _ := strconv.Atoi(fields[1])
		tree[pid] = process{parent, fields[2:]}
	}
	ids := map[string]bool{}
	for _, c := range threads {
		ids[c.ID] = true
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 2 {
			continue
		}
		root, _ := strconv.Atoi(fields[1])
		matches := map[string]bool{}
		for pid, p := range tree {
			ancestor := pid
			for steps := 0; steps < 100 && ancestor != 0; steps++ {
				if ancestor == root {
					env, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
					for _, value := range strings.Split(string(env), "\x00") {
						if id, ok := strings.CutPrefix(value, "CODEX_THREAD_ID="); ok && ids[id] {
							matches[id] = true
						}
					}

					for i, arg := range p.args {
						if arg == "resume" && i+1 < len(p.args) && ids[p.args[i+1]] {
							matches[p.args[i+1]] = true
						}
					}
					break
				}
				ancestor = tree[ancestor].parent
			}
		}
		if len(matches) == 1 {
			found := ""
			for id := range matches {
				found = id
			}
			for i := range terminals {
				if terminals[i].Name == fields[0] && terminals[i].Conversation == "" {
					terminals[i].Conversation = found
					_ = tmux("set-option", "-t", terminals[i].Name, "@jara_codex_thread", found).Run()
				}
			}
		}
	}
}
