package agentrouter

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestStoredClaudeTokenReachesClaudeCode(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	base := []string{"PATH=/bin"}
	if got := claudeEnv(base); !slices.Equal(got, base) {
		t.Fatalf("no token: %q", got)
	}
	before := claudeCredentialStamp()

	out := &lockedBuffer{}
	r := &agentRouter{out: out}
	r.handleClaudeLogin(json.RawMessage(`"t1"`), "jara/login/token", json.RawMessage(`{"agent":"claude","token":" sk-ant-oat01-abc "}`))
	out.waitFor(t, func(m map[string]any) bool { return m["id"] == "t1" })
	if info, err := os.Stat(ClaudeTokenPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored %v %v", info, err)
	}
	if got := claudeEnv(base); !slices.Contains(got, "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-abc") {
		t.Fatalf("oauth token: %q", got)
	}
	if claudeCredentialStamp() == before || claudeCredentialSource() != "token stored by Jara" {
		t.Fatalf("stamp or source unchanged: %q", claudeCredentialSource())
	}
	if err := StoreClaudeToken("sk-ant-api03-key"); err != nil {
		t.Fatal(err)
	}
	if got := claudeEnv(base); !slices.Contains(got, "ANTHROPIC_API_KEY=sk-ant-api03-key") || claudeCredentialSource() != "API key stored by Jara" {
		t.Fatalf("api key: %q", got)
	}
	// The environment's own token wins and is named.
	if got := claudeEnv([]string{"CLAUDE_CODE_OAUTH_TOKEN=mine"}); len(got) != 1 {
		t.Fatalf("environment token: %q", got)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "mine")
	if claudeCredentialSource() != "CLAUDE_CODE_OAUTH_TOKEN from the environment" {
		t.Fatal(claudeCredentialSource())
	}
	r.handleClaudeLogin(json.RawMessage(`"t2"`), "jara/login/token", json.RawMessage(`{"agent":"claude","token":"not-a-token"}`))
	if out.waitFor(t, func(m map[string]any) bool { return m["id"] == "t2" })["error"] == nil {
		t.Fatal("a malformed token was stored")
	}
}
