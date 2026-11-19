package jara

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type hostSSHRunner struct {
	remote Remote
	// output receives streamed command output instead of the terminal; the
	// last lines are then kept for the error when a command fails.
	output io.Writer
}

func (r hostSSHRunner) run(timeout time.Duration, cwd string, args []string, input io.Reader, stream bool) ([]byte, error) {
	command := shellArgs(args...)
	if cwd != "" {
		command = "cd " + quote(cwd) + " && " + command
	}
	return r.shell(timeout, command, input, stream)
}

func (r hostSSHRunner) shell(timeout time.Duration, command string, input io.Reader, stream bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append(sshArgs(r.remote, false), "--", r.remote.Host, shellArgs("sh", "-c", command))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = input
	var stdout, stderr bytes.Buffer
	var tail *outputTail
	switch {
	case stream && r.output != nil:
		tail = &outputTail{out: r.output}
		cmd.Stdout, cmd.Stderr = tail, tail
	case stream:
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	default:
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("remote command timed out: %w", ctx.Err())
		}
		if tail != nil {
			if lines := tail.text(); lines != "" {
				return nil, fmt.Errorf("remote command failed: %w; last output:\n%s", err, lines)
			}
		}
		if stream {
			return nil, fmt.Errorf("remote command failed: %w", err)
		}
		return nil, fmt.Errorf("remote command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

const outputTailLines = 20

// outputTail forwards output and remembers its last lines.
type outputTail struct {
	mu    sync.Mutex
	out   io.Writer
	lines []string
	part  []byte
}

func (t *outputTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.part = append(t.part, p...)
	for {
		i := bytes.IndexByte(t.part, '\n')
		if i < 0 {
			break
		}
		t.lines = append(t.lines, strings.TrimRight(string(t.part[:i]), "\r"))
		if len(t.lines) > outputTailLines {
			t.lines = t.lines[1:]
		}
		t.part = t.part[i+1:]
	}
	return t.out.Write(p)
}

func (t *outputTail) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := t.lines
	if len(t.part) > 0 {
		lines = append(lines[:len(lines):len(lines)], string(t.part))
	}
	return strings.Join(lines, "\n")
}
