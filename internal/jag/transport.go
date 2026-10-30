package jag

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func shellArgs(args ...string) string {
	out := make([]string, len(args))
	for i, s := range args {
		out[i] = quote(s)
	}
	return strings.Join(out, " ")
}
func token(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
func decode(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func sshArgs(r Remote, tty bool) []string {
	a := []string{"-o", "StrictHostKeyChecking=ask", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}
	if config := os.Getenv("JAG_SSH_CONFIG"); config != "" {
		a = append(a, "-F", config)
	}
	if tty {
		a = append(a, "-tt")
	} else {
		a = append(a, "-T")
	}
	if r.Port != 0 {
		a = append(a, "-p", strconv.Itoa(r.Port))
	}
	if r.User != "" {
		a = append(a, "-l", r.User)
	}
	if r.Identity != "" {
		a = append(a, "-i", r.Identity)
	}
	return a
}
func sshCommand(r Remote, tty bool, command string) *exec.Cmd {
	return exec.Command("ssh", append(sshArgs(r, tty), "--", r.Host, command)...)
}
func interactive(cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type Request struct {
	Op             string `json:"op"`
	Name           string `json:"name,omitempty"`
	ID             string `json:"id,omitempty"`
	URL            string `json:"url,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Commit         string `json:"commit,omitempty"`
	Subdir         string `json:"subdir,omitempty"`
	Resume         bool   `json:"resume,omitempty"`
	New            bool   `json:"new,omitempty"`
	Takeover       bool   `json:"takeover,omitempty"`
	Worktree       bool   `json:"worktree,omitempty"`
	Force          bool   `json:"force,omitempty"`
	IncludeIgnored bool   `json:"include_ignored,omitempty"`
}

func rpc(r Remote, q Request, input io.Reader, v any) error {
	cmd := sshCommand(r, false, shellArgs("jag", "_rpc", token(q)))
	cmd.Stdin = input
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remote %s: %w: %s", q.Op, err, strings.TrimSpace(stderr.String()))
	}
	if v != nil {
		if err := json.Unmarshal(out.Bytes(), v); err != nil {
			return fmt.Errorf("invalid Jag response (check remote shell startup output): %w", err)
		}
	}
	return nil
}
func openRemote(r Remote, q Request) error {
	return interactive(sshCommand(r, true, shellArgs("jag", "_open", token(q))))
}
