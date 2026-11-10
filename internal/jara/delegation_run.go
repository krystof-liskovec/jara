package jara

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Permission levels for delegated runs, mapped to each CLI's own controls.
// Delegated runs cannot answer prompts, so anything outside the level is
// refused rather than waiting for a person.
const (
	permissionReadOnly = "read-only"
	permissionEdit     = "edit"
	permissionFull     = "full"
)

func validPermission(p string) bool {
	return p == permissionReadOnly || p == permissionEdit || p == permissionFull
}

// delegatedCommand builds the non-interactive command for one run. The prompt
// is always written to standard input.
func delegatedCommand(task delegatedTask, run int) []string {
	if task.Agent == agentClaude {
		command := []string{claudeBinary, "--print", "--output-format", "text", "--permission-prompts", "none"}
		switch task.Permission {
		case permissionReadOnly:
			command = append(command, "--permission-mode", "plan")
		case permissionFull:
			command = append(command, "--permission-mode", "bypassPermissions")
		default:
			command = append(command, "--permission-mode", "acceptEdits")
		}
		if task.Model != "" {
			command = append(command, "--model", task.Model)
		}
		if run == 1 {
			return append(command, "--session-id", task.Session)
		}
		return append(command, "--resume", task.Session)
	}
	command := []string{codexBinary, "exec"}
	if run > 1 {
		command = append(command, "resume")
	}
	command = append(command, "--json", "--skip-git-repo-check", "-o", taskFile(task, "result", run))
	switch task.Permission {
	case permissionFull:
		command = append(command, "--dangerously-bypass-approvals-and-sandbox")
	case permissionReadOnly:
		command = append(command, "-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`)
	default:
		command = append(command, "-c", `sandbox_mode="workspace-write"`, "-c", `approval_policy="never"`)
	}
	if task.Model != "" {
		command = append(command, "-m", task.Model)
	}
	if run > 1 {
		command = append(command, task.Session)
	}
	return append(command, "-")
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// launchTaskRun starts a run in a fresh tmux session that stays visible after
// it exits. The previous run's finished session is replaced.
func launchTaskRun(task delegatedTask, run int) error {
	_ = tmux("kill-session", "-t", task.TmuxSession).Run()
	if err := checkSessionLimit(); err != nil {
		return err
	}
	if err := runTmux("new-session", "-d", "-s", task.TmuxSession, "-c", task.CWD, "/bin/sleep", "infinity"); err != nil {
		return fmt.Errorf("prepare task terminal: %w", err)
	}
	fail := func(err error) error { _ = tmux("kill-session", "-t", task.TmuxSession).Run(); return err }
	if err := initializeSessionLifecycle(task.TmuxSession); err != nil {
		return fail(err)
	}
	label := fmt.Sprintf("%s task: %s", agentDisplayName(task.Agent), fitMenu(task.Title, 60))
	options := [][2]string{{"@jara_background", "1"}, {"@jara_agent", task.Agent}, {"@jara_session_label", encodeSessionMenuLabel(label)}}
	if task.Workspace != "" {
		options = append(options, [2]string{"@jara_workspace", task.Workspace})
	}
	for _, option := range options {
		if err := runTmux("set-option", "-t", task.TmuxSession, option[0], option[1]); err != nil {
			return fail(err)
		}
	}
	if err := runTmux("set-window-option", "-t", task.TmuxSession, "remain-on-exit", "on"); err != nil {
		return fail(err)
	}
	if err := runTmux("respawn-pane", "-k", "-t", task.TmuxSession, "mise", "exec", "--", "jara", "_delegate-run", task.ID, strconv.Itoa(run)); err != nil {
		return fail(fmt.Errorf("start task: %w", err))
	}
	return nil
}

// runDelegatedTask is the tmux pane process for one run. It records the
// outcome even when the agent fails so callers never wait forever.
func runDelegatedTask(id, rawRun string) error {
	run, err := strconv.Atoi(rawRun)
	if err != nil || run < 1 {
		return fmt.Errorf("invalid task run")
	}
	task, err := loadTask(id)
	if err != nil {
		return err
	}
	prompt, err := os.ReadFile(taskFile(task, "prompt", run))
	if err != nil {
		return err
	}
	fmt.Printf("Jara task %s · %s · %s permissions\n%s\n\n", task.ID, agentDisplayName(task.Agent), task.Permission, strings.TrimSpace(string(prompt)))
	exitCode, session := executeTaskRun(task, run, prompt)
	status := taskSucceeded
	if exitCode != 0 {
		status = taskFailed
	}
	_, err = updateTask(id, func(t *delegatedTask) error {
		if t.Status == taskRunning && t.Runs == run {
			t.Status, t.ExitCode = status, exitCode
			if session != "" {
				t.Session = session
			}
		}
		return nil
	})
	fmt.Printf("\nJara task %s %s (exit %d).\n", task.ID, status, exitCode)
	return err
}

func executeTaskRun(task delegatedTask, run int, prompt []byte) (int, string) {
	logFile, err := os.OpenFile(taskFile(task, "log", run), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open task log:", err)
		return 1, ""
	}
	defer logFile.Close()
	command := delegatedCommand(task, run)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = task.CWD
	cmd.Env = append(os.Environ(), "JARA_TASK_ID="+task.ID, "JARA_DELEGATION_DEPTH="+strconv.Itoa(task.Depth))
	cmd.Stdin = strings.NewReader(string(prompt))
	cmd.Stderr = io.MultiWriter(os.Stderr, logFile)
	session := ""
	var copyDone chan struct{}
	if task.Agent == agentClaude {
		result, e := os.OpenFile(taskFile(task, "result", run), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if e != nil {
			fmt.Fprintln(os.Stderr, "open task result:", e)
			return 1, ""
		}
		defer result.Close()
		cmd.Stdout = io.MultiWriter(os.Stdout, logFile, result)
	} else {
		events, e := cmd.StdoutPipe()
		if e != nil {
			return 1, ""
		}
		copyDone = make(chan struct{})
		go func() {
			defer close(copyDone)
			session = renderCodexEvents(events, os.Stdout, logFile)
		}()
	}
	if err = cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start agent:", err)
		return 127, ""
	}
	if copyDone != nil {
		<-copyDone
	}
	err = cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), session
	}
	if err != nil {
		return 1, session
	}
	return 0, session
}

// renderCodexEvents shows Codex's JSONL events as readable progress and
// returns the thread ID needed to continue the conversation.
func renderCodexEvents(events io.Reader, terminal, log io.Writer) string {
	thread := ""
	scanner := bufio.NewScanner(events)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Command string `json:"command"`
			} `json:"item"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil {
			fmt.Fprintf(terminal, "%s\n", line)
			fmt.Fprintf(log, "%s\n", line)
			continue
		}
		text := ""
		switch {
		case event.Type == "thread.started":
			thread = event.ThreadID
		case event.Type == "item.completed" && event.Item.Type == "agent_message":
			text = event.Item.Text
		case event.Type == "item.completed" && event.Item.Type == "command_execution":
			text = "$ " + event.Item.Command
		case event.Type == "item.completed" && event.Item.Type == "file_change":
			text = "(edited files)"
		case event.Type == "turn.failed" || event.Type == "error":
			text = "error: " + event.Error.Message + event.Message
		}
		if text != "" {
			fmt.Fprintln(terminal, text)
			fmt.Fprintln(log, text)
		}
	}
	return thread
}
