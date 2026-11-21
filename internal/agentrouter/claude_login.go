package agentrouter

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// Claude Code signs in with `claude auth login`: it prints a URL, and the
// browser that opens it shows a code to paste back. The router runs that
// command on the host the agent runs on and relays the URL and the code, so
// the Jara interface signs in from any device:
//
//	jara/login/start  {agent: "claude", method}  → {loginId, url}; method
//	                                               "console" bills an
//	                                               Anthropic Console account
//	jara/login/code   {loginId, code}            → {}
//	jara/login/cancel {loginId}                  → {}
//	jara/login/token  {agent: "claude", token}   → status; stores a token
//	                                               from claude setup-token
//	                                               or an API key
//	jara/login/status {agent: "claude"}          → claude auth status --json
//	                                               with source
//	jara/login/completed {agent, loginId, success, error} once it finished
//
// Codex signs in through its own app-server (account/login/start).

const claudeLoginTimeout = 10 * time.Minute

type claudeLogins struct {
	mu     sync.Mutex
	seq    int
	id     string // the running login; "" when none runs
	stdin  io.WriteCloser
	cancel context.CancelFunc
}

var claudeLoginURL = regexp.MustCompile(`https://\S+`)

func (r *agentRouter) handleClaudeLogin(id json.RawMessage, method string, params json.RawMessage) {
	var request struct {
		LoginID string `json:"loginId"`
		Code    string `json:"code"`
		Method  string `json:"method"`
		Token   string `json:"token"`
	}
	_ = json.Unmarshal(params, &request)
	switch method {
	case "jara/login/start":
		r.startClaudeLogin(id, request.Method == "console")
	case "jara/login/token":
		token := strings.TrimSpace(request.Token)
		go func() {
			if err := StoreClaudeToken(token); err != nil {
				r.fail(id, err.Error())
				return
			}
			r.reply(id, claudeAuthStatus())
		}()
	case "jara/login/code":
		r.logins.mu.Lock()
		stdin := r.logins.stdin
		current := r.logins.id == request.LoginID && stdin != nil
		r.logins.mu.Unlock()
		code := strings.TrimSpace(request.Code)
		if !current || code == "" {
			r.fail(id, "no Claude Code sign-in is waiting for a code; start one with /login claude")
			return
		}
		if _, err := io.WriteString(stdin, code+"\n"); err != nil {
			r.fail(id, "could not pass the code to Claude Code: "+err.Error())
			return
		}
		r.reply(id, map[string]any{})
	case "jara/login/cancel":
		r.logins.mu.Lock()
		if r.logins.id == request.LoginID && r.logins.cancel != nil {
			r.logins.cancel()
		}
		r.logins.mu.Unlock()
		r.reply(id, map[string]any{})
	case "jara/login/status":
		go func() { r.reply(id, claudeAuthStatus()) }()
	}
}

func (r *agentRouter) startClaudeLogin(id json.RawMessage, console bool) {
	ctx, cancel := context.WithTimeout(context.Background(), claudeLoginTimeout)
	account := "--claudeai"
	if console {
		account = "--console"
	}
	cmd := exec.CommandContext(ctx, protocol.ClaudeExecutable(), "auth", "login", account)
	output, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	stdin, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		r.fail(id, "could not start Claude Code's sign-in: "+err.Error())
		return
	}
	r.logins.mu.Lock()
	if r.logins.cancel != nil {
		r.logins.cancel() // one sign-in at a time; a new one replaces it
	}
	r.logins.seq++
	loginID := "claude-login-" + strconv.Itoa(r.logins.seq)
	r.logins.id, r.logins.stdin, r.logins.cancel = loginID, stdin, cancel
	r.logins.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		writer.Close()
	}()
	go r.followClaudeLogin(id, loginID, cmd, output)
}

// followClaudeLogin sends the URL once Claude Code prints it, then reports
// how the sign-in ended.
func (r *agentRouter) followClaudeLogin(id json.RawMessage, loginID string, cmd *exec.Cmd, output io.Reader) {
	var transcript strings.Builder
	scanner := bufio.NewScanner(output)
	sent := false
	for scanner.Scan() {
		line := textutil.TerminalEscapes.ReplaceAllString(scanner.Text(), "")
		transcript.WriteString(line + "\n")
		if url := claudeLoginURL.FindString(line); url != "" && !sent {
			sent = true
			r.reply(id, map[string]any{"loginId": loginID, "url": url})
		}
	}
	_, _ = io.Copy(io.Discard, output) // past an overlong line, until it exits
	r.logins.mu.Lock()
	current := r.logins.id == loginID
	if current {
		r.logins.cancel()
		r.logins.id, r.logins.stdin, r.logins.cancel = "", nil, nil
	}
	r.logins.mu.Unlock()
	failure := claudeLoginFailure(cmd, transcript.String())
	if !sent {
		r.fail(id, "Claude Code's sign-in did not start: "+failure)
		return
	}
	success := cmd.ProcessState != nil && cmd.ProcessState.Success()
	if success {
		RememberAccounts()               // the stored token stays in the pool
		_ = os.Remove(ClaudeTokenPath()) // the newest sign-in wins over it
		RememberAccounts()
	}
	status := claudeAuthStatus()
	success = success && status["loggedIn"] == true
	completed := map[string]any{"agent": protocol.AgentClaude, "loginId": loginID, "success": success, "status": status}
	if !success {
		completed["error"] = failure
	}
	r.notify("jara/login/completed", completed)
}

func claudeLoginFailure(cmd *exec.Cmd, transcript string) string {
	var last string
	for _, line := range strings.Split(transcript, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Paste code here if prompted >"))
		if line != "" && !claudeLoginURL.MatchString(line) {
			last = line
		}
	}
	if last != "" {
		return last
	}
	if state := cmd.ProcessState; state != nil && !state.Success() {
		return "it exited with " + state.String()
	}
	return "it stopped before signing in"
}

// claudeAuthStatus reports `claude auth status --json`; loggedIn is false
// when Claude Code is missing or does not answer.
func claudeAuthStatus() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, protocol.ClaudeExecutable(), "auth", "status", "--json")
	cmd.Env = claudeEnv(os.Environ())
	out, err := cmd.Output()
	status := map[string]any{}
	if json.Unmarshal(out, &status) != nil || status["loggedIn"] == nil {
		message := "Claude Code did not report its sign-in"
		if err != nil {
			message += ": " + err.Error()
		}
		return map[string]any{"loggedIn": false, "error": message}
	}
	if source := claudeCredentialSource(); source != "" && status["loggedIn"] == true {
		status["source"] = source
	}
	return status
}

// claudeCredentialSource names a token Claude Code gets from its
// environment: one set where Jara runs, or the one Jara stored.
func claudeCredentialSource() string {
	for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if os.Getenv(name) != "" {
			return name + " from the environment"
		}
	}
	if data, err := os.ReadFile(ClaudeTokenPath()); err == nil && ValidateClaudeToken(strings.TrimSpace(string(data))) == nil {
		if claudeEnvToken(strings.TrimSpace(string(data))) == "ANTHROPIC_API_KEY" {
			return "API key stored by Jara"
		}
		return "token stored by Jara"
	}
	return ""
}

// ClaudeSignedIn reports whether Claude Code on this machine is signed in,
// by its own login or a token from the environment or Jara.
func ClaudeSignedIn() bool { return claudeAuthStatus()["loggedIn"] == true }
