package jara

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	chatShellTimeout     = 5 * time.Minute
	chatShellOutputLimit = 512 << 10
)

type chatShellResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

type chatShellCapture struct {
	mu        sync.Mutex
	text      strings.Builder
	truncated bool
}

func (c *chatShellCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := chatShellOutputLimit - c.text.Len()
	if remaining > 0 {
		c.text.Write(data[:min(len(data), remaining)])
	}
	if len(data) > remaining {
		c.truncated = true
	}
	return len(data), nil
}

func (u *chatUI) runChatShellCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		u.print("Start a shell command with !, for example: !git status\n")
		return nil
	}
	if len(command) > maxBackgroundPrompt {
		u.print("Shell command exceeds 64 KiB.\n")
		return nil
	}
	u.printRole("tool", "$ "+command+"\n")
	if u.persistentSession {
		id, err := u.c.request("jara/shell", map[string]string{"command": command})
		if err != nil {
			return err
		}
		u.pending[id] = chatPending{method: "jara/shell", deadline: time.Now().Add(chatShellTimeout + time.Minute)}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatShellTimeout)
	defer cancel()
	u.renderChatShellResult(runChatShell(ctx, u.cwd, command))
	return nil
}

func runChatShellRequest(params json.RawMessage, cwd string) chatShellResult {
	var request struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return chatShellResult{ExitCode: -1, Error: "invalid shell request: " + err.Error()}
	}
	command := strings.TrimSpace(request.Command)
	if command == "" {
		return chatShellResult{ExitCode: -1, Error: "shell command is empty"}
	}
	if len(command) > maxBackgroundPrompt {
		return chatShellResult{ExitCode: -1, Error: "shell command exceeds 64 KiB"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatShellTimeout)
	defer cancel()
	return runChatShell(ctx, cwd, command)
}

func runChatShell(ctx context.Context, cwd, command string) chatShellResult {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
	} else if shell == "" {
		shell = "/bin/sh"
	}
	args := []string{"-lc", command}
	if runtime.GOOS == "windows" {
		args = []string{"/C", command}
	}
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Dir = cwd
	capture := &chatShellCapture{}
	cmd.Stdout, cmd.Stderr = capture, capture
	err := cmd.Run()
	result := chatShellResult{Output: capture.text.String(), ExitCode: 0}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			result.ExitCode = exitError.ExitCode()
		} else {
			result.ExitCode = -1
			result.Error = err.Error()
		}
	}
	if ctx.Err() != nil {
		result.ExitCode = -1
		result.Error = "shell command timed out after 5 minutes"
	}
	if capture.truncated {
		result.Output += "\n[output truncated after 512 KiB]\n"
	}
	return result
}

func (u *chatUI) acceptChatShellResponse(message appMessage) error {
	var result chatShellResult
	if err := json.Unmarshal(message.Result, &result); err != nil {
		u.printRole("system", "Could not read shell command output: "+err.Error()+"\n")
		return nil
	}
	u.renderChatShellResult(result)
	u.refreshGitDiff() // the command may have changed or committed files
	return nil
}

func (u *chatUI) renderChatShellResult(result chatShellResult) {
	if result.Output != "" {
		u.printRole("tool", result.Output)
		if !strings.HasSuffix(result.Output, "\n") {
			u.printRole("tool", "\n")
		}
	}
	if result.Error != "" {
		u.printRole("system", "Shell command error: "+chatText(result.Error)+"\n")
	}
	if result.ExitCode != 0 {
		u.printRole("system", fmt.Sprintf("Shell exited with status %d.\n", result.ExitCode))
	}
}
