package jara

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func tmux(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", "jara"}, args...)...)
}

func sessions() ([]Session, error) {
	// has-session distinguishes an absent server from malformed output.
	probe := tmux("list-sessions", "-F", "#{session_name}|#{@jara_workspace}|#{session_attached}|#{@jara_background}|#{pane_dead}|#{@jara_created_at}|#{@jara_last_attached}|#{@jara_session_label}|#{@jara_codex_thread}|#{@jara_agent_state}|#{@jara_agent_pid}|#{@jara_agent}")
	b, err := probe.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (strings.Contains(string(ee.Stderr), "no server running") || strings.Contains(string(ee.Stderr), "No such file")) {
			b = nil
		} else {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
	}
	var ss []Session
	metadata := []agentMetadata{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		p := strings.Split(line, "|")
		if len(p) < 5 {
			continue
		}
		n, _ := strconv.Atoi(p[2])
		session := Session{Name: p[0], Workspace: p[1], Attached: n, Background: p[3] == "1", Dead: p[4] == "1"}
		if len(p) > 5 {
			session.CreatedAt, _ = strconv.ParseInt(p[5], 10, 64)
		}
		if len(p) > 6 {
			session.LastAttach, _ = strconv.ParseInt(p[6], 10, 64)
		}
		if len(p) > 7 {
			session.Label = decodeSessionMenuLabel(p[7])
		}
		if len(p) > 8 {
			session.Conversation = p[8]
		}
		if len(p) > 11 {
			session.Agent = p[11]
		}
		if len(p) > 10 {
			if agent, ok := parseAgentMetadata(session.Name, p[9], p[10]); ok {
				metadata = append(metadata, agent)
			}
		}
		ss = append(ss, session)
	}
	liveStates, _ := liveAgentStates(metadata)
	for i := range ss {
		ss[i].AgentState = liveStates[ss[i].Name]
	}
	ss = append(ss, chatDaemonSessions()...)
	return ss, nil
}

func activeSessions() ([]Session, error) {
	all, err := sessions()
	if err != nil {
		return nil, err
	}
	active := make([]Session, 0, len(all))
	for _, session := range all {
		if session.Background && session.Dead {
			continue
		}
		active = append(active, session)
	}
	return active, nil
}

const defaultMaxSessions = 500

func maxSessions() (int, error) {
	raw := strings.TrimSpace(os.Getenv("JARA_MAX_SESSIONS"))
	if raw == "" {
		return defaultMaxSessions, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > defaultMaxSessions {
		return 0, fmt.Errorf("JARA_MAX_SESSIONS must be an integer from 1 to %d", defaultMaxSessions)
	}
	return limit, nil
}

func checkSessionLimit() error {
	limit, err := maxSessions()
	if err != nil {
		return err
	}
	count, err := tmuxSessionCount()
	if err != nil {
		return err
	}
	if count >= limit {
		return fmt.Errorf("session limit reached (%d); list sessions with `tmux -L jara list-sessions` and close one with `tmux -L jara kill-session -t NAME`", limit)
	}
	return nil
}

func tmuxSessionCount() (int, error) {
	b, err := tmux("list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (strings.Contains(string(exitErr.Stderr), "no server running") || strings.Contains(string(exitErr.Stderr), "No such file")) {
			return 0, nil
		}
		return 0, fmt.Errorf("list sessions: %w", err)
	}
	output := strings.TrimSpace(string(b))
	if output == "" {
		return 0, nil
	}
	return strings.Count(output, "\n") + 1, nil
}

func sessionCountCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("invalid session count invocation")
	}
	count, err := tmuxSessionCount()
	if err != nil {
		fmt.Fprintln(os.Stdout, "Sessions ?")
		return nil
	}
	limit, err := maxSessions()
	if err != nil {
		fmt.Fprintln(os.Stdout, "Sessions ?")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Sessions %d/%d\n", count, limit)
	return nil
}

func sessionUtilityCommand(command string, args []string) error {
	if command == "_session-count" {
		return sessionCountCommand(args)
	}
	if len(args) != 1 {
		return fmt.Errorf("invalid session attachment invocation")
	}
	return recordSessionAttachment(args[0])
}

// connectCommand runs inside the environment, without client configuration or SSH.
func runTmux(args ...string) error { cmd := tmux(args...); cmd.Stderr = os.Stderr; return cmd.Run() }

func attach(s Session, takeover bool, remoteName string) error {
	if s.ChatDaemon {
		return runRemoteChatClient(Request{UserInterface: "jara", ID: s.Workspace, Conversation: s.Conversation})
	}
	if err := markSessionAttached(s.Name); err != nil {
		return err
	}
	if err := configureTmuxStatus(s.Name, remoteName); err != nil {
		return err
	}
	args := []string{"attach-session", "-t", s.Name}
	if takeover {
		args = append(args, "-d")
	}
	return interactive(tmux(args...))
}

// lastActiveWorkspace names the workspace of the most recently used running
// session, or "" when none runs.
func lastActiveWorkspace() string {
	running, err := activeSessions()
	if err != nil {
		return ""
	}
	best, bestAt := "", int64(-1)
	for _, session := range running {
		if session.Dead || session.Background || session.Workspace == "" {
			continue
		}
		if at := max(session.LastAttach, chatSessionSeconds(session.CreatedAt)); at > bestAt {
			best, bestAt = session.Workspace, at
		}
	}
	return best
}

// chatSessionSeconds reads creation times stored in seconds or nanoseconds.
func chatSessionSeconds(value int64) int64 {
	if value > 1e12 {
		return value / 1e9
	}
	return value
}
