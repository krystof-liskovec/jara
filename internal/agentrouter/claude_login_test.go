package agentrouter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// waitFor returns the first message the router wrote that matches.
func (l *lockedBuffer) waitFor(t *testing.T, match func(map[string]any) bool) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		l.mu.Lock()
		scanner := bufio.NewScanner(bytes.NewReader(l.b.Bytes()))
		l.mu.Unlock()
		for scanner.Scan() {
			var message map[string]any
			if json.Unmarshal(scanner.Bytes(), &message) == nil && match(message) {
				return message
			}
		}
	}
	t.Fatalf("the router never wrote the message; it wrote %s", l.b.String())
	return nil
}

func TestClaudeLoginRelaysTheURLAndCode(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	// Signs in when given the code "good"; auth status reports it.
	if err := os.WriteFile(script, []byte(`#!/bin/sh
if [ "$2" = status ]; then
  if [ -f "`+dir+`/in" ]; then echo '{"loggedIn":true,"email":"me@example.com"}'; else echo '{"loggedIn":false}'; fi
  exit 0
fi
printf 'Opening browser to sign in…\nIf the browser did not open, visit: https://claude.com/cai/oauth/authorize?code=true\nPaste code here if prompted > '
read code
if [ "$code" = good ]; then touch "`+dir+`/in"; echo 'Login successful.'; exit 0; fi
echo 'OAuth error: Invalid code' >&2
exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JARA_CLAUDE_BIN", script)
	t.Setenv("JARA_DATA", t.TempDir()) // a successful sign-in removes the stored token
	out := &lockedBuffer{}
	r := &agentRouter{out: out}
	isID := func(id string) func(map[string]any) bool {
		return func(m map[string]any) bool { return m["id"] == id }
	}
	completed := func(m map[string]any) bool { return m["method"] == "jara/login/completed" }

	// A wrong code ends the sign-in with Claude Code's error.
	r.handleClaudeLogin(json.RawMessage(`"s1"`), "jara/login/start", nil)
	result, _ := out.waitFor(t, isID("s1"))["result"].(map[string]any)
	if !strings.HasPrefix(result["url"].(string), "https://claude.com/cai/oauth/authorize") {
		t.Fatalf("start %v", result)
	}
	code, _ := json.Marshal(map[string]any{"loginId": result["loginId"], "code": "bad"})
	r.handleClaudeLogin(json.RawMessage(`"c1"`), "jara/login/code", code)
	done, _ := out.waitFor(t, completed)["params"].(map[string]any)
	if done["success"] != false || done["error"] != "OAuth error: Invalid code" {
		t.Fatalf("bad code %v", done)
	}

	// The right one signs in.
	out.mu.Lock()
	out.b.Reset()
	out.mu.Unlock()
	r.handleClaudeLogin(json.RawMessage(`"s2"`), "jara/login/start", nil)
	result, _ = out.waitFor(t, isID("s2"))["result"].(map[string]any)
	code, _ = json.Marshal(map[string]any{"loginId": result["loginId"], "code": " good "})
	r.handleClaudeLogin(json.RawMessage(`"c2"`), "jara/login/code", code)
	done, _ = out.waitFor(t, completed)["params"].(map[string]any)
	if done["success"] != true || done["status"].(map[string]any)["email"] != "me@example.com" {
		t.Fatalf("good code %v", done)
	}
	r.handleClaudeLogin(json.RawMessage(`"c3"`), "jara/login/code", code)
	if failure := out.waitFor(t, isID("c3"))["error"]; failure == nil {
		t.Fatal("a code was taken with no sign-in running")
	}
}
