//go:build linux || darwin

package jara

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sentRequests decodes what the interface sent, by method.
func sentRequests(t *testing.T, sent *bytes.Buffer) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	scanner := bufio.NewScanner(bytes.NewReader(sent.Bytes()))
	for scanner.Scan() {
		var message map[string]any
		if json.Unmarshal(scanner.Bytes(), &message) == nil {
			if method, _ := message["method"].(string); method != "" {
				out[method] = message
			}
		}
	}
	return out
}

func answer(t *testing.T, term *chatTerminal, request map[string]any, result string) {
	t.Helper()
	id, _ := json.Marshal(request["id"])
	if err := term.u.response(appMessage{ID: id, Result: json.RawMessage(result)}); err != nil {
		t.Fatal(err)
	}
}

func TestChatLoginSignsBothAgentsIn(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	sent := &bytes.Buffer{}
	term.u.c = offlineChatConnection()
	term.u.c.encoder = json.NewEncoder(sent)
	screen := func() string { return plainChatScreen(term.frame(160, 60, time.Now())) }

	// Codex: a device code to enter in the browser.
	if _, err := term.u.line("/login codex"); err != nil {
		t.Fatal(err)
	}
	start := sentRequests(t, sent)["account/login/start"]
	if params, _ := start["params"].(map[string]any); params["type"] != "chatgptDeviceCode" {
		t.Fatalf("codex start %v", start)
	}
	answer(t, term, start, `{"type":"chatgptDeviceCode","loginId":"L1","verificationUrl":"https://auth.openai.com/codex/device","userCode":"PNS1-65JVV"}`)
	if got := screen(); !strings.Contains(got, "Sign in to Codex on this device") || !strings.Contains(got, "https://auth.openai.com/codex/device") || !strings.Contains(got, "PNS1-65JVV") {
		t.Fatalf("codex steps:\n%s", got)
	}
	_ = term.u.event(appMessage{Method: "account/login/completed", Params: json.RawMessage(`{"loginId":"L1","success":true}`)})
	if !strings.Contains(screen(), "Signed in to Codex on this device.") || term.u.login.codexLogin != "" {
		t.Fatalf("codex done:\n%s", screen())
	}

	// Claude Code: the code the page shows is typed into the composer.
	sent.Reset()
	if _, err := term.u.line("/login claude"); err != nil {
		t.Fatal(err)
	}
	answer(t, term, sentRequests(t, sent)["jara/login/start"], `{"loginId":"claude-login-1","url":"https://claude.com/cai/oauth/authorize?x=1"}`)
	if !strings.Contains(screen(), "paste the code the page shows here") {
		t.Fatalf("claude steps:\n%s", screen())
	}
	sent.Reset()
	if _, err := term.u.line("abc#def"); err != nil {
		t.Fatal(err)
	}
	requests := sentRequests(t, sent)
	code, _ := requests["jara/login/code"]["params"].(map[string]any)
	if code["code"] != "abc#def" || code["loginId"] != "claude-login-1" || requests["turn/start"] != nil {
		t.Fatalf("the code went %v", requests)
	}
	_ = term.u.event(appMessage{Method: "jara/login/completed", Params: json.RawMessage(`{"agent":"claude","success":false,"error":"Invalid code"}`)})
	if !strings.Contains(screen(), "Claude Code sign-in did not go through: Invalid code") || term.u.login.claudeLogin != "" {
		t.Fatalf("claude failed:\n%s", screen())
	}
}

func TestSignedOutFailuresSayHowToSignIn(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	term.u.c = offlineChatConnection()
	failure := `{"error":{"message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header"},"willRetry":false}`
	if err := term.u.event(appMessage{Method: "error", Params: json.RawMessage(failure)}); err != nil {
		t.Fatal(err)
	}
	completed := `{"turn":{"id":"t1","status":"failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer"}}}`
	if err := term.u.event(appMessage{Method: "turn/completed", Params: json.RawMessage(completed)}); err != nil {
		t.Fatal(err)
	}
	screen := plainChatScreen(term.frame(160, 40, time.Now()))
	if strings.Count(screen, "Codex is not signed in on this device. Sign in with /login codex") != 1 || strings.Contains(screen, "401") {
		t.Fatalf("%s", screen)
	}
	// The open-time check says the same for a signed-out Claude Code.
	sent := &bytes.Buffer{}
	term.u.c.encoder = json.NewEncoder(sent)
	term.u.effectiveModel = "claude/opus"
	term.u.checkChatSignIn()
	answer(t, term, sentRequests(t, sent)["jara/login/status"], `{"loggedIn":false}`)
	if screen = plainChatScreen(term.frame(160, 40, time.Now())); !strings.Contains(screen, "Claude Code is not signed in on this device") {
		t.Fatalf("%s", screen)
	}
}

func TestChatLoginTakesKeysAndTokensHidden(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	sent := &bytes.Buffer{}
	term.u.c = offlineChatConnection()
	term.u.c.encoder = json.NewEncoder(sent)
	term.u.thread, term.u.threadReady = "th", true
	screen := func() string { return plainChatScreen(term.frame(160, 50, time.Now())) }

	// The menu: the agent, then how it signs in.
	if _, err := term.u.line("/login"); err != nil {
		t.Fatal(err)
	}
	term.picker.sync(term.u)
	if done := typeChatKeys(t, term, "\x1b[B\r"); done || term.picker.provider != "claude" { // Claude Code
		t.Fatalf("agent step: %q", term.picker.provider)
	}
	if got := pickerRows(term); !strings.Contains(got, "Claude account|Anthropic Console|>Token or API key") && !strings.Contains(got, ">Claude account|Anthropic Console|Token or API key") {
		t.Fatalf("methods %s", got)
	}
	typeChatKeys(t, term, "\x1b[D") // ← back to the agents
	if term.picker.provider != "" || !term.picker.active() {
		t.Fatalf("back: %q", term.picker.provider)
	}
	typeChatKeys(t, term, "\r\x1b[B\x1b[B\r") // Claude Code, Token or API key
	if term.u.login.secret != "claude token" {
		t.Fatalf("secret %q", term.u.login.secret)
	}

	// Typed hidden, kept out of history and of the conversation.
	sent.Reset()
	typeChatKeys(t, term, "sk-ant-oat01-hush")
	if got := screen(); strings.Contains(got, "hush") || !strings.Contains(got, "•••••") || !strings.Contains(got, "Claude token · hidden") {
		t.Fatalf("composer:\n%s", got)
	}
	typeChatKeys(t, term, "\r")
	requests := sentRequests(t, sent)
	token, _ := requests["jara/login/token"]["params"].(map[string]any)
	if token["token"] != "sk-ant-oat01-hush" || requests["turn/start"] != nil || term.u.login.secret != "" || slicesContain(term.editor.history, "hush") {
		t.Fatalf("token went %v; history %q", requests, term.editor.history)
	}
	answer(t, term, requests["jara/login/token"], `{"loggedIn":true,"authMethod":"oauth_token","source":"CLAUDE_CODE_OAUTH_TOKEN from the environment"}`)
	if got := screen(); !strings.Contains(got, "Saved the token on this device") || !strings.Contains(got, "Until CLAUDE_CODE_OAUTH_TOKEN is unset there") || strings.Contains(got, "hush") {
		t.Fatalf("saved:\n%s", got)
	}

	// An OpenAI key the same way; Esc cancels one being typed.
	if _, err := term.u.line("/login codex key"); err != nil {
		t.Fatal(err)
	}
	typeChatKeys(t, term, "sk-proj-abc")
	term.parser.feed([]byte("\x1b")) // a lone Escape resolves on the next tick
	if err := term.tick(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if term.u.login.secret != "" || term.editor.Text() != "" || !strings.Contains(screen(), "Sign-in cancelled.") {
		t.Fatalf("esc: secret %q text %q", term.u.login.secret, term.editor.Text())
	}
	sent.Reset()
	_, _ = term.u.line("/login codex key")
	typeChatKeys(t, term, "sk-proj-abc\r")
	key, _ := sentRequests(t, sent)["account/login/start"]["params"].(map[string]any)
	if key["type"] != "apiKey" || key["apiKey"] != "sk-proj-abc" {
		t.Fatalf("codex key %v", key)
	}
}

func slicesContain(values []string, part string) bool {
	for _, value := range values {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func TestChatLoginSavesATokenEverywhere(t *testing.T) {
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if err := saveConfig(Config{}); err != nil {
		t.Fatal(err)
	}
	term, _ := newTestChatTerminal(t)
	term.u.c = offlineChatConnection()
	if _, err := term.u.line("/login claude token everywhere"); err != nil || term.u.login.secret != "claude token everywhere" {
		t.Fatalf("secret %q %v", term.u.login.secret, err)
	}
	typeChatKeys(t, term, "sk-ant-oat01-all\r")
	select {
	case text := <-term.u.backgroundRuns:
		term.u.acceptChatBackground(text)
	case <-time.After(5 * time.Second):
		t.Fatal("never saved")
	}
	if screen := plainChatScreen(term.frame(160, 40, time.Now())); !strings.Contains(screen, "This device: saved") || strings.Contains(screen, "sk-ant-oat01-all") {
		t.Fatalf("%s", screen)
	}
}
