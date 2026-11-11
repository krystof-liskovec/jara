package jara

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
)

// A delegated task is one agent asking another (or itself) to work in a
// workspace. Each run executes in its own tmux session so people can watch
// it with `jara sessions`, and its state lives in tasks/<id>/task.json.
type delegatedTask struct {
	ID          string `json:"id"`
	Agent       string `json:"agent"`
	Title       string `json:"title"`
	Workspace   string `json:"workspace,omitempty"`
	OriginCWD   string `json:"origin_cwd"`
	CWD         string `json:"cwd"`
	Repository  string `json:"repository,omitempty"`
	Worktree    string `json:"worktree,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Permission  string `json:"permission"`
	Model       string `json:"model,omitempty"`
	Session     string `json:"session,omitempty"`
	Status      string `json:"status"`
	ExitCode    int    `json:"exit_code"`
	Runs        int    `json:"runs"`
	Depth       int    `json:"depth"`
	Parent      string `json:"parent,omitempty"`
	TmuxSession string `json:"tmux_session"`
	Merged      bool   `json:"merged,omitempty"`
	Created     int64  `json:"created"`
	Updated     int64  `json:"updated"`
}

const (
	taskRunning   = "running"
	taskSucceeded = "succeeded"
	taskFailed    = "failed"
	taskCancelled = "cancelled"
)

var taskIDRE = regexp.MustCompile(`^t-[0-9a-f]{12}$`)

func tasksRoot() string { return filepath.Join(fsutil.DataRoot(), "tasks") }

func taskDir(id string) (string, error) {
	if !taskIDRE.MatchString(id) {
		return "", fmt.Errorf("invalid task ID %q", id)
	}
	return filepath.Join(tasksRoot(), id), nil
}

func newTaskID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "t-" + hex.EncodeToString(b[:]), nil
}

// lockTasks serializes task state updates between the MCP server and task
// runners with a directory lock; a holder older than 30 seconds is stale.
func lockTasks() (func(), error) {
	if err := os.MkdirAll(tasksRoot(), 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(tasksRoot(), ".lock")
	for attempt := 0; attempt < 200; attempt++ {
		if err := os.Mkdir(lock, 0700); err == nil {
			return func() { _ = os.Remove(lock) }, nil
		} else if !os.IsExist(err) {
			return nil, err
		}
		if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(lock)
			continue
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, fmt.Errorf("task state is locked: %s", lock)
}

func loadTask(id string) (delegatedTask, error) {
	dir, err := taskDir(id)
	if err != nil {
		return delegatedTask{}, err
	}
	var task delegatedTask
	data, err := os.ReadFile(filepath.Join(dir, "task.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return task, fmt.Errorf("unknown task %s", id)
		}
		return task, err
	}
	if err = json.Unmarshal(data, &task); err != nil || task.ID != id {
		return task, fmt.Errorf("task %s has invalid state", id)
	}
	return task, nil
}

func saveTask(task delegatedTask) error {
	dir, err := taskDir(task.ID)
	if err != nil {
		return err
	}
	task.Updated = time.Now().Unix()
	return fsutil.WriteJSON(filepath.Join(dir, "task.json"), task)
}

// updateTask applies a change under the task lock.
func updateTask(id string, change func(*delegatedTask) error) (delegatedTask, error) {
	unlock, err := lockTasks()
	if err != nil {
		return delegatedTask{}, err
	}
	defer unlock()
	task, err := loadTask(id)
	if err != nil {
		return task, err
	}
	if err = change(&task); err != nil {
		return task, err
	}
	return task, saveTask(task)
}

func listTasks() ([]delegatedTask, error) {
	entries, err := os.ReadDir(tasksRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []delegatedTask
	for _, entry := range entries {
		if !entry.IsDir() || !taskIDRE.MatchString(entry.Name()) {
			continue
		}
		if task, e := loadTask(entry.Name()); e == nil {
			tasks = append(tasks, reconcileTask(task))
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Created > tasks[j].Created })
	return tasks, nil
}

// reconcileTask marks a running task failed when its tmux session vanished
// without the runner recording a result, for example after a restart.
func reconcileTask(task delegatedTask) delegatedTask {
	if task.Status != taskRunning || task.TmuxSession == "" {
		return task
	}
	if tmux("has-session", "-t", task.TmuxSession).Run() == nil {
		return task
	}
	updated, err := updateTask(task.ID, func(t *delegatedTask) error {
		if t.Status == taskRunning {
			t.Status, t.ExitCode = taskFailed, -1
		}
		return nil
	})
	if err != nil {
		return task
	}
	return updated
}

func runningTaskCount() int {
	tasks, _ := listTasks()
	count := 0
	for _, task := range tasks {
		if task.Status == taskRunning {
			count++
		}
	}
	return count
}

func taskFile(task delegatedTask, kind string, run int) string {
	return filepath.Join(tasksRoot(), task.ID, kind+"-"+strconv.Itoa(run)+".txt")
}

// taskResult returns the final answer of the latest run, or the tail of its
// log when the agent produced no final message.
func taskResult(task delegatedTask, limit int) string {
	if data, err := os.ReadFile(taskFile(task, "result", task.Runs)); err == nil && strings.TrimSpace(string(data)) != "" {
		return textutil.TailText(string(data), limit)
	}
	data, _ := os.ReadFile(taskFile(task, "log", task.Runs))
	return textutil.TailText(string(data), limit)
}

// handoffTaskSummary lists recent delegated tasks whose origin is path, so a
// receiving agent can find pending worktrees.
func handoffTaskSummary(path string) string {
	tasks, _ := listTasks()
	var lines []string
	for _, task := range tasks {
		if task.OriginCWD != path || len(lines) >= 10 {
			continue
		}
		line := fmt.Sprintf("- %s (%s, %s): %s", task.ID, protocol.AgentDisplayName(task.Agent), task.Status, task.Title)
		if task.Branch != "" && !task.Merged {
			line += " — unmerged branch " + task.Branch
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
