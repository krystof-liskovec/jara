package jara

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type hostSSHRunner struct {
	remote Remote
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
	if stream {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	} else {
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("remote command timed out: %w", ctx.Err())
		}
		if stream {
			return nil, fmt.Errorf("remote command failed: %w", err)
		}
		return nil, fmt.Errorf("remote command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
