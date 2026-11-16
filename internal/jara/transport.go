package jara

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
	config := os.Getenv("JARA_SSH_CONFIG")
	if config != "" {
		a = append(a, "-F", config)
	}
	if tty {
		a = append(a, "-o", "LogLevel=ERROR", "-tt")
	} else {
		// Sync and checkpoints move text that compresses well; devices may be
		// on slow or metered connections.
		a = append(a, "-T", "-o", "Compression=yes")
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
	UserInterface      string   `json:"interface,omitempty"`
	Agent              string   `json:"agent,omitempty"`
	Runtime            string   `json:"runtime,omitempty"`
	AgentExplicit      bool     `json:"agent_explicit,omitempty"` // Runtime was chosen explicitly
	Handoff            bool     `json:"handoff,omitempty"`
	ConversationCWD    string   `json:"conversation_cwd,omitempty"`
	Conversation       string   `json:"conversation,omitempty"`
	Op                 string   `json:"op"`
	Provider           string   `json:"provider,omitempty"`
	OlderThan          string   `json:"older_than,omitempty"`
	Containers         []string `json:"containers,omitempty"`
	Name               string   `json:"name,omitempty"`
	ID                 string   `json:"id,omitempty"`
	URL                string   `json:"url,omitempty"`
	Branch             string   `json:"branch,omitempty"`
	SourceHost         string   `json:"source_host,omitempty"`
	SourcePath         string   `json:"source_path,omitempty"`
	Commit             string   `json:"commit,omitempty"`
	Base               string   `json:"base,omitempty"` // commit the endpoint is expected to have checked out
	Commits            []string `json:"commits,omitempty"`
	Compress           bool     `json:"compress,omitempty"` // answer zstd-compressed
	Devices            []string `json:"devices,omitempty"`
	Peer               string   `json:"-"` // the host forwarding this request, set by its forced command
	Subdir             string   `json:"subdir,omitempty"`
	Resume             bool     `json:"resume,omitempty"`
	ResumeLatest       bool     `json:"resume_latest,omitempty"`
	New                bool     `json:"new,omitempty"`
	Background         bool     `json:"background,omitempty"`
	Prompt             string   `json:"prompt,omitempty"`
	BackgroundPromptID string   `json:"background_prompt_id,omitempty"`
	Takeover           bool     `json:"takeover,omitempty"`
	Worktree           bool     `json:"worktree,omitempty"`
	Force              bool     `json:"force,omitempty"`
	IncludeIgnored     bool     `json:"include_ignored,omitempty"`
	CodexArgs          []string `json:"codex_args,omitempty"`
	AgentArgs          []string `json:"agent_args,omitempty"` // Claude Code arguments for the Jara UI
	Reconnect          bool     `json:"reconnect,omitempty"`  // the client reconnects if the session stops
	// Local runner fields: the device running the agent, its lease epoch and
	// link, the local checkout, and the remote that keeps the shared copy.
	Runner     string `json:"runner,omitempty"`
	Epoch      int64  `json:"epoch,omitempty"`
	Link       string `json:"link,omitempty"`
	Since      int64  `json:"since,omitempty"`   // export checkpoints after this one
	Wait       bool   `json:"wait,omitempty"`    // a busy runner may hand over after its turn
	Passive    bool   `json:"passive,omitempty"` // a reconnecting window: never displace another device
	Headless   bool   `json:"headless,omitempty"`
	Continue   string `json:"continue,omitempty"` // prompt that continues an interrupted turn
	Root       string `json:"root,omitempty"`
	RemoteName string `json:"remote_name,omitempty"`
	Workspace  string `json:"workspace,omitempty"`
}

// rpcToken is how rpc sends a request: asking for a compressed answer (see
// serveCompressedRPC).
func rpcToken(q Request) string {
	q.Compress = true
	return token(q)
}

func rpc(r Remote, q Request, input io.Reader, v any) error {
	cmd := sshCommand(r, false, shellArgs("jara", "_rpc", rpcToken(q)))
	cmd.Stdin = compressRequestInput(r, input)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remote %s: %w: %s", q.Op, err, strings.TrimSpace(stderr.String()))
	}
	answer, err := decompressAnswer(r, out.Bytes())
	if err != nil {
		return fmt.Errorf("remote %s: unreadable compressed answer: %w", q.Op, err)
	}
	if v != nil {
		if err := json.Unmarshal(answer, v); err != nil {
			return fmt.Errorf("invalid Jara response (check remote shell startup output): %w", err)
		}
	}
	return nil
}

// rpcStream copies the remote command's raw output to out.
func rpcStream(r Remote, q Request, input io.Reader, out io.Writer) error {
	cmd := sshCommand(r, false, shellArgs("jara", "_rpc", token(q)))
	cmd.Stdin = input
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remote %s: %w: %s", q.Op, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func openRemote(r Remote, q Request) error {
	if q.Background {
		wire := q
		wire.Prompt = ""
		cmd := sshCommand(r, false, shellArgs("jara", "_open", token(wire)))
		cmd.Stdin = strings.NewReader(q.Prompt)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	if q.UserInterface == "jara" && q.ID != "" {
		usedLocalClient, err := openRemoteJaraClient(r, q)
		if err != nil || usedLocalClient {
			return err
		}
	}
	return openRemoteLegacy(r, q)
}

func openRemoteLegacy(r Remote, q Request) error {
	if q.UserInterface == "jara" {
		return remoteMenuResult(interactive(sshCommand(r, true, shellArgs("jara", "_open", token(q)))))
	}
	return remoteMenuResult(clipboardTerminal(r, sshCommand(r, true, shellArgs("jara", "_open", token(q)))))
}
