package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

const (
	maxTaskWait         = 3000
	defaultTaskWait     = 45
	defaultMaxDepth     = 2
	defaultMaxTasks     = 4
	maxTaskResultLength = 20000
)

func envLimit(name string, fallback, ceiling int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value >= 0 && value <= ceiling {
		return value
	}
	return fallback
}

type delegateArguments struct {
	Agent      string `json:"agent"`
	Prompt     string `json:"prompt"`
	Isolation  string `json:"isolation"`
	Permission string `json:"permission"`
	Model      string `json:"model"`
	CWD        string `json:"cwd"`
	TaskID     string `json:"task_id"`
	Wait       *int   `json:"wait_seconds"`
	Cleanup    *bool  `json:"cleanup"`
}

func callDelegationTool(name string, raw json.RawMessage) (string, error) {
	var args delegateArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	wait := defaultTaskWait
	if args.Wait != nil {
		wait = min(max(*args.Wait, 0), maxTaskWait)
	}
	switch name {
	case "delegate_task":
		task, err := startDelegatedTask(args)
		if err != nil {
			return "", err
		}
		return describeTask(waitForTask(task, wait), true), nil
	case "task_status":
		task, err := loadTask(args.TaskID)
		if err != nil {
			return "", err
		}
		return describeTask(waitForTask(reconcileTask(task), wait), true), nil
	case "continue_task":
		task, err := continueDelegatedTask(args.TaskID, args.Prompt)
		if err != nil {
			return "", err
		}
		return describeTask(waitForTask(task, wait), true), nil
	case "list_tasks":
		tasks, err := listTasks()
		if err != nil {
			return "", err
		}
		if len(tasks) == 0 {
			return "No delegated tasks yet.", nil
		}
		var lines []string
		for i, task := range tasks {
			if i == 20 {
				break
			}
			lines = append(lines, describeTask(task, false))
		}
		return strings.Join(lines, "\n"), nil
	case "merge_task":
		task, err := loadTask(args.TaskID)
		if err != nil {
			return "", err
		}
		cleanup := args.Cleanup == nil || *args.Cleanup
		report, err := mergeTask(reconcileTask(task), cleanup)
		if err != nil {
			return "", err
		}
		_, err = updateTask(task.ID, func(t *delegatedTask) error { t.Merged = true; return nil })
		return report, err
	case "cancel_task":
		task, err := updateTask(args.TaskID, func(t *delegatedTask) error {
			if t.Status != taskRunning {
				return fmt.Errorf("task %s is not running (%s)", t.ID, t.Status)
			}
			t.Status, t.ExitCode = taskCancelled, -1
			return nil
		})
		if err != nil {
			return "", err
		}
		_ = tmux("kill-session", "-t", task.TmuxSession).Run()
		return "Cancelled task " + task.ID + ".", nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func startDelegatedTask(args delegateArguments) (delegatedTask, error) {
	agent, err := protocol.NormalizeAgent(args.Agent)
	if err != nil || args.Agent == "" {
		return delegatedTask{}, fmt.Errorf("agent must be claude or codex")
	}
	if agent == protocol.AgentClaude {
		if _, err = os.Stat(protocol.ClaudeBinary); err != nil {
			return delegatedTask{}, fmt.Errorf("Claude Code is not installed on this host")
		}
	}
	prompt := strings.TrimSpace(args.Prompt)
	if prompt == "" || len(prompt) > maxBackgroundPrompt || strings.ContainsRune(prompt, 0) {
		return delegatedTask{}, fmt.Errorf("prompt must be nonempty text of at most 64 KiB")
	}
	if args.Permission == "" {
		args.Permission = permissionEdit
	}
	if !validPermission(args.Permission) {
		return delegatedTask{}, fmt.Errorf("permission must be read-only, edit or full")
	}
	parent, depth := delegationParent()
	if limit := envLimit("JARA_MAX_DELEGATION_DEPTH", defaultMaxDepth, 10); depth > limit {
		return delegatedTask{}, fmt.Errorf("delegation depth limit %d reached; finish this task yourself", limit)
	}
	if limit := envLimit("JARA_MAX_DELEGATED_TASKS", defaultMaxTasks, 50); runningTaskCount() >= limit {
		return delegatedTask{}, fmt.Errorf("%d delegated tasks are already running; wait for one with task_status", limit)
	}
	cwd := args.CWD
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return delegatedTask{}, err
		}
	}
	if resolved, e := filepath.EvalSymlinks(cwd); e == nil {
		cwd = resolved
	}
	if info, e := os.Stat(cwd); e != nil || !info.IsDir() || !filepath.IsAbs(cwd) {
		return delegatedTask{}, fmt.Errorf("cwd must be an existing absolute directory")
	}
	id, err := newTaskID()
	if err != nil {
		return delegatedTask{}, err
	}
	title := strings.SplitN(prompt, "\n", 2)[0]
	task := delegatedTask{ID: id, Agent: agent, Title: textutil.FitText(title, 100), OriginCWD: cwd, CWD: cwd, Permission: args.Permission,
		Model: args.Model, Status: taskRunning, Runs: 1, Depth: depth, Parent: parent, TmuxSession: "jara-task-" + strings.TrimPrefix(id, "t-"),
		Workspace: workspaceForPath(cwd), Created: time.Now().Unix()}
	if task.Workspace == "" && parent != "" {
		if parentTask, e := loadTask(parent); e == nil {
			task.Workspace = parentTask.Workspace
		}
	}
	isolation := args.Isolation
	if isolation == "" {
		isolation = "worktree"
		if _, e := gitIn(cwd, "rev-parse", "--show-toplevel"); e != nil {
			isolation = "shared"
		}
	}
	if isolation != "worktree" && isolation != "shared" {
		return delegatedTask{}, fmt.Errorf("isolation must be worktree or shared")
	}
	if isolation == "worktree" {
		if err = prepareTaskWorktree(&task); err != nil {
			return delegatedTask{}, err
		}
	}
	if agent == protocol.AgentClaude {
		if task.Session, err = protocol.NewUUID(); err != nil {
			return delegatedTask{}, err
		}
	}
	if err = os.MkdirAll(filepath.Join(tasksRoot(), id), 0700); err != nil {
		return delegatedTask{}, err
	}
	if err = os.WriteFile(taskFile(task, "prompt", 1), []byte(prompt), 0600); err != nil {
		return delegatedTask{}, err
	}
	if err = saveTask(task); err != nil {
		return delegatedTask{}, err
	}
	if err = launchTaskRun(task, 1); err != nil {
		_, _ = updateTask(id, func(t *delegatedTask) error { t.Status, t.ExitCode = taskFailed, -1; return nil })
		return delegatedTask{}, err
	}
	return task, nil
}

func continueDelegatedTask(id, prompt string) (delegatedTask, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || len(prompt) > maxBackgroundPrompt || strings.ContainsRune(prompt, 0) {
		return delegatedTask{}, fmt.Errorf("prompt must be nonempty text of at most 64 KiB")
	}
	if current, err := loadTask(id); err == nil {
		reconcileTask(current)
	}
	task, err := updateTask(id, func(t *delegatedTask) error {
		switch {
		case t.Status == taskRunning:
			return fmt.Errorf("task %s is still running; wait with task_status", t.ID)
		case t.Merged:
			return fmt.Errorf("task %s was merged; start a new task", t.ID)
		case t.Session == "":
			return fmt.Errorf("task %s has no agent conversation to continue", t.ID)
		}
		t.Runs++
		t.Status, t.ExitCode = taskRunning, 0
		return os.WriteFile(taskFile(*t, "prompt", t.Runs), []byte(prompt), 0600)
	})
	if err != nil {
		return task, err
	}
	if err = launchTaskRun(task, task.Runs); err != nil {
		_, _ = updateTask(id, func(t *delegatedTask) error { t.Status, t.ExitCode = taskFailed, -1; return nil })
		return task, err
	}
	return task, nil
}

func waitForTask(task delegatedTask, seconds int) delegatedTask {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for task.Status == taskRunning && time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if current, err := loadTask(task.ID); err == nil {
			task = reconcileTask(current)
		}
	}
	return task
}

func describeTask(task delegatedTask, detailed bool) string {
	line := fmt.Sprintf("%s · %s · %s · run %d", task.ID, protocol.AgentDisplayName(task.Agent), task.Status, task.Runs)
	if task.Branch != "" {
		line += " · branch " + task.Branch
		if task.Merged {
			line += " (merged)"
		}
	}
	if !detailed {
		return line + " · " + task.Title
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nTask: %s\nWorking directory: %s\nWatch it with: jara sessions (tmux session %s)\n", line, task.Title, task.CWD, task.TmuxSession)
	if task.Status == taskRunning {
		b.WriteString("Still running. Call task_status with this task ID to wait for the result.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Exit code: %d\n", task.ExitCode)
	if diff := taskDiff(task, 4000); diff != "" && !task.Merged {
		fmt.Fprintf(&b, "\nChanges in the task worktree:\n%s\nUse merge_task to bring them into your checkout.\n", diff)
	}
	fmt.Fprintf(&b, "\nResult:\n%s\n", taskResult(task, maxTaskResultLength))
	return b.String()
}

// workspaceForPath maps a directory inside a Jara workspace to its ID so a
// task's tmux session is listed with that workspace.
func workspaceForPath(path string) string {
	rel, err := filepath.Rel(filepath.Join(fsutil.DataRoot(), "workspaces"), path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	id := strings.Split(filepath.ToSlash(rel), "/")[0]
	if !nameRE.MatchString(id) {
		return ""
	}
	return id
}
