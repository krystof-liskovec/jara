package jara

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
)

const maxBackgroundPrompt = 64 * 1024

func openRequest(encoded string) error {
	var q Request
	if err := decode(encoded, &q); err != nil {
		return err
	}
	if q.Background {
		prompt, err := io.ReadAll(io.LimitReader(os.Stdin, maxBackgroundPrompt+1))
		if err != nil {
			return err
		}
		if len(prompt) > maxBackgroundPrompt {
			return fmt.Errorf("prompt exceeds 64 KiB")
		}
		q.Prompt = string(prompt)
	}
	return remoteOpen(q)
}

func backgroundPromptPath(id string) (string, error) {
	if !nameRE.MatchString(id) {
		return "", fmt.Errorf("invalid background prompt ID")
	}
	return filepath.Join(os.TempDir(), "jara-background-"+id), nil
}

func writeBackgroundPrompt(id, prompt string) error {
	path, err := backgroundPromptPath(id)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("prepare background prompt: %w", err)
	}
	if _, err = file.WriteString(prompt); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func readBackgroundPrompt(id string) (string, error) {
	path, err := backgroundPromptPath(id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	_ = os.Remove(path)
	if len(data) > maxBackgroundPrompt {
		return "", fmt.Errorf("background prompt exceeds 64 KiB")
	}
	return string(data), nil
}

func backgroundCodexCommand(q Request) []string {
	command := []string{"/usr/local/bin/codex", "exec"}
	command = append(command, q.CodexArgs...)
	if q.ResumeLatest {
		command = append(command, "resume", "--last", q.Prompt)
	} else {
		command = append(command, q.Prompt)
	}
	return command
}

func runBackgroundCodex(q Request) error {
	if !q.Background {
		return fmt.Errorf("invalid background Codex run")
	}
	prompt, err := readBackgroundPrompt(q.BackgroundPromptID)
	if err != nil {
		return err
	}
	q.Prompt = prompt
	if strings.TrimSpace(q.Prompt) == "" {
		return fmt.Errorf("background Codex run requires a prompt")
	}
	if q.Runtime == protocol.AgentClaude {
		return runBackgroundClaude(q)
	}
	command := backgroundCodexCommand(q)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = strings.NewReader("")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
