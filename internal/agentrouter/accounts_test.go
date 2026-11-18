package agentrouter

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// isolateAccounts keeps the pool, Codex's and Claude Code's sign-ins in
// temporary directories.
func isolateAccounts(t *testing.T) {
	t.Helper()
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
}

func signInCodex(t *testing.T, accountID, email string) {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"email": email, "https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": "pro"}})
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{
		"id_token": "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s", "account_id": accountID, "refresh_token": "r-" + accountID}})
	if err := os.WriteFile(codexAuthPath(), auth, 0600); err != nil {
		t.Fatal(err)
	}
}

func activeCodex(t *testing.T) string {
	t.Helper()
	account, _ := currentCodexAccount()
	return account.Label
}

func TestAccountPoolSwitchesToAnAccountWithUsageLeft(t *testing.T) {
	isolateAccounts(t)
	signInCodex(t, "a1", "a@example.com")
	RememberAccounts()
	signInCodex(t, "b2", "b@example.com") // a second sign-in replaces auth.json
	if got := Accounts(); len(got) != 2 || !got[1].Active || got[0].Label != "a@example.com (pro)" {
		t.Fatalf("pool %+v", got)
	}
	// Codex renewed b's login while it was in use; the pool keeps the renewal.
	data, _ := os.ReadFile(codexAuthPath())
	_ = os.WriteFile(codexAuthPath(), []byte(strings.Replace(string(data), "r-b2", "r-b2-renewed", 1)), 0600)

	reset := time.Now().Add(3 * time.Hour).Unix()
	from, to, ok := switchAccount(protocol.AgentCodex, reset)
	if !ok || from.Label != "b@example.com (pro)" || to.Label != "a@example.com (pro)" || activeCodex(t) != "a@example.com (pro)" {
		t.Fatalf("switch: %v %q → %q, active %q", ok, from.Label, to.Label, activeCodex(t))
	}
	for _, account := range loadAccountPool()[protocol.AgentCodex] {
		if account.ID == from.ID && (account.ExhaustedUntil != reset || !strings.Contains(string(account.Auth), "r-b2-renewed")) {
			t.Fatalf("b kept %+v", account)
		}
	}
	// a runs out too, while b is still out: nothing to switch to.
	if _, _, ok = switchAccount(protocol.AgentCodex, 0); ok || activeCodex(t) != "a@example.com (pro)" {
		t.Fatalf("switched with every account out; active %q", activeCodex(t))
	}
	if accountAvailable(protocol.AgentCodex) {
		t.Fatal("an exhausted account counted as available")
	}
	// Picking one by hand clears its mark; the one in use cannot be removed.
	if err := UseAccount(protocol.AgentCodex, from.ID); err != nil || activeCodex(t) != "b@example.com (pro)" {
		t.Fatalf("use: %v %q", err, activeCodex(t))
	}
	if RemoveAccount(protocol.AgentCodex, from.ID) == nil {
		t.Fatal("removed the account in use")
	}
	if err := RemoveAccount(protocol.AgentCodex, to.ID); err != nil || len(Accounts()) != 1 {
		t.Fatalf("remove: %v %+v", err, Accounts())
	}
	if info, _ := os.Stat(accountsPath()); info.Mode().Perm() != 0o600 {
		t.Fatalf("pool mode %v", info.Mode())
	}
}

func TestClaudeAccountsPoolTokensAndItsOwnLogin(t *testing.T) {
	isolateAccounts(t)
	if err := os.WriteFile(claudeCredentialsFile(), []byte(`{"claudeAiOauth":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"sk-ant-oat01-first", "sk-ant-oat01-second"} {
		if err := StoreClaudeToken(token); err != nil {
			t.Fatal(err)
		}
	}
	var labels []string
	for _, account := range Accounts() {
		if account.Agent == protocol.AgentClaude {
			labels = append(labels, account.Label)
		}
	}
	if strings.Join(labels, "|") != "Claude Code's own login|Claude token …irst|Claude token …cond" {
		t.Fatalf("pool %q", labels)
	}
	// From the second token the next with usage left is Claude Code's own
	// login: the stored token goes, so the login is used.
	if _, to, ok := switchAccount(protocol.AgentClaude, 0); !ok || !to.Login {
		t.Fatalf("switch to %+v", to)
	}
	if _, err := os.Stat(ClaudeTokenPath()); !os.IsNotExist(err) {
		t.Fatal("the stored token stayed")
	}
	if _, to, ok := switchAccount(protocol.AgentClaude, 0); !ok || to.Token != "sk-ant-oat01-first" {
		t.Fatalf("then %+v", to)
	}
	if data, _ := os.ReadFile(ClaudeTokenPath()); strings.TrimSpace(string(data)) != "sk-ant-oat01-first" {
		t.Fatalf("token %q", data)
	}
}
