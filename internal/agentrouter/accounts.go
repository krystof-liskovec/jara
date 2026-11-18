package agentrouter

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

// The account pool lets several accounts of one agent act as one: every
// account signed in on this machine is kept, and when the one in use runs out
// of usage, Jara puts the next one with usage left in its place and the turn
// continues. Conversations live on this machine, not with an account, so any
// account continues any of them.
//
// Codex reads its account from CODEX_HOME/auth.json; the pool keeps a copy of
// each, refreshed from the live file before every switch, so logins Codex
// renewed are not lost. Claude Code uses a token Jara stores (claude
// setup-token or an API key) or, without one, its own login; the pool keeps
// the tokens and that login.

type poolAccount struct {
	ID             string          `json:"id"`
	Label          string          `json:"label"`
	Auth           json.RawMessage `json:"auth,omitempty"`  // Codex: auth.json
	Token          string          `json:"token,omitempty"` // Claude Code: a token
	Login          bool            `json:"login,omitempty"` // Claude Code: its own login
	ExhaustedUntil int64           `json:"exhaustedUntil,omitempty"`
}

type accountPool map[string][]poolAccount // by agent

func accountsPath() string { return filepath.Join(fsutil.DataRoot(), "accounts.json") }

func loadAccountPool() accountPool {
	pool := accountPool{}
	if data, err := os.ReadFile(accountsPath()); err == nil {
		_ = json.Unmarshal(data, &pool)
	}
	return pool
}

func (p accountPool) save() error {
	if err := os.MkdirAll(filepath.Dir(accountsPath()), 0700); err != nil {
		return err
	}
	return fsutil.WriteJSON(accountsPath(), p)
}

// withAccounts runs change on the pool under the machine's lock, after
// adding the account each agent uses now, and saves it.
func withAccounts[T any](change func(accountPool) T) T {
	if err := os.MkdirAll(fsutil.DataRoot(), 0700); err == nil {
		defer lockAccounts(accountsPath() + ".lock")()
	}
	pool := loadAccountPool()
	before, _ := json.Marshal(pool)
	for _, agent := range []string{protocol.AgentCodex, protocol.AgentClaude} {
		if current, ok := currentAccount(agent); ok {
			pool.upsert(agent, current)
		}
	}
	result := change(pool)
	if after, _ := json.Marshal(pool); !bytes.Equal(before, after) {
		_ = pool.save()
	}
	return result
}

func (p accountPool) upsert(agent string, account poolAccount) {
	for i, known := range p[agent] {
		if known.ID == account.ID {
			account.ExhaustedUntil = known.ExhaustedUntil
			if account.Label == "" {
				account.Label = known.Label
			}
			p[agent][i] = account
			return
		}
	}
	p[agent] = append(p[agent], account)
}

func currentAccount(agent string) (poolAccount, bool) {
	if agent == protocol.AgentCodex {
		return currentCodexAccount()
	}
	return currentClaudeAccount()
}

func codexHome() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func codexAuthPath() string { return filepath.Join(codexHome(), "auth.json") }

// currentCodexAccount identifies the account in Codex's auth.json.
func currentCodexAccount() (poolAccount, bool) {
	data, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return poolAccount{}, false
	}
	var auth struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			IDToken   string `json:"id_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) != nil {
		return poolAccount{}, false
	}
	account := poolAccount{Auth: json.RawMessage(bytes.TrimSpace(data))}
	switch {
	case auth.Tokens != nil && auth.Tokens.AccountID != "":
		email, plan := codexIDClaims(auth.Tokens.IDToken)
		account.ID, account.Label = "chatgpt:"+auth.Tokens.AccountID+":"+email, strings.TrimSpace(email+" "+plan)
	case auth.APIKey != "":
		account.ID, account.Label = "apikey:"+shortHash(auth.APIKey), "API key …"+lastFour(auth.APIKey)
	default:
		return poolAccount{}, false
	}
	return account, true
}

// codexIDClaims reads the email and plan from an ID token; it is only shown,
// so its signature is not checked.
func codexIDClaims(token string) (email, plan string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	_ = json.Unmarshal(payload, &claims)
	if claims.Auth.Plan != "" {
		plan = "(" + claims.Auth.Plan + ")"
	}
	return claims.Email, plan
}

// currentClaudeAccount identifies the token Jara stored, or Claude Code's
// own login when there is none.
func currentClaudeAccount() (poolAccount, bool) {
	data, err := os.ReadFile(ClaudeTokenPath())
	if token := strings.TrimSpace(string(data)); err == nil && ValidateClaudeToken(token) == nil {
		kind := "token"
		if claudeEnvToken(token) == "ANTHROPIC_API_KEY" {
			kind = "API key"
		}
		return poolAccount{ID: "token:" + shortHash(token), Label: "Claude " + kind + " …" + lastFour(token), Token: token}, true
	}
	if info, err := os.Stat(claudeCredentialsFile()); err == nil && info.Size() > 0 {
		return poolAccount{ID: "login", Label: "Claude Code's own login", Login: true}, true
	}
	return poolAccount{}, false
}

func shortHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:6])
}

func lastFour(secret string) string { return secret[max(0, len(secret)-4):] }

// activate makes account the one its agent uses on this machine.
func activate(agent string, account poolAccount) error {
	switch {
	case agent == protocol.AgentCodex:
		if len(account.Auth) == 0 {
			return errors.New("the account has no saved sign-in")
		}
		if err := os.MkdirAll(codexHome(), 0700); err != nil {
			return err
		}
		return fsutil.WritePrivateFile(codexAuthPath(), append(append([]byte(nil), account.Auth...), '\n'))
	case account.Login:
		if err := os.Remove(ClaudeTokenPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	default:
		return writeClaudeToken(account.Token)
	}
}

// switchAccount marks the account in use as out of usage until resetsAt and
// puts the next one with usage left in its place. A failure seen with an
// account another session already switched away from just continues.
func switchAccount(agent string, resetsAt int64) (poolAccount, poolAccount, bool) {
	type result struct {
		from, to poolAccount
		ok       bool
	}
	r := withAccounts(func(pool accountPool) result {
		current, ok := currentAccount(agent)
		accounts := pool[agent]
		if !ok || len(accounts) < 2 {
			return result{}
		}
		now := time.Now().Unix()
		if resetsAt <= now {
			resetsAt = now + int64(time.Hour/time.Second) // unknown: try it again in an hour
		}
		start := 0
		for i := range accounts {
			if accounts[i].ID == current.ID {
				accounts[i].ExhaustedUntil, start = resetsAt, i
			}
		}
		for step := 1; step < len(accounts); step++ {
			next := accounts[(start+step)%len(accounts)]
			if next.ExhaustedUntil > now || activate(agent, next) != nil {
				continue
			}
			return result{current, next, true}
		}
		return result{from: current}
	})
	return r.from, r.to, r.ok
}

// RememberAccounts adds the accounts in use now to the pool, as before a
// sign-in replaces one.
func RememberAccounts() { withAccounts(func(accountPool) struct{} { return struct{}{} }) }

// AccountSummary is one pooled account as the interface lists it.
type AccountSummary struct {
	Agent          string `json:"agent"`
	ID             string `json:"id"`
	Label          string `json:"label"`
	Active         bool   `json:"active"`
	ExhaustedUntil int64  `json:"exhaustedUntil,omitempty"`
}

// Accounts lists the pool on this machine, adding the accounts in use now.
func Accounts() []AccountSummary {
	return withAccounts(func(pool accountPool) []AccountSummary {
		var out []AccountSummary
		for _, agent := range []string{protocol.AgentCodex, protocol.AgentClaude} {
			current, _ := currentAccount(agent)
			for _, account := range pool[agent] {
				exhausted := account.ExhaustedUntil
				if exhausted <= time.Now().Unix() {
					exhausted = 0
				}
				out = append(out, AccountSummary{Agent: agent, ID: account.ID, Label: account.Label, Active: account.ID == current.ID, ExhaustedUntil: exhausted})
			}
		}
		return out
	})
}

// UseAccount switches agent to the pooled account id; RemoveAccount drops
// one that is not in use.
func UseAccount(agent, id string) error {
	return withAccounts(func(pool accountPool) error {
		for i, account := range pool[agent] {
			if account.ID == id {
				pool[agent][i].ExhaustedUntil = 0
				return activate(agent, account)
			}
		}
		return errors.New("no such account")
	})
}

func RemoveAccount(agent, id string) error {
	return withAccounts(func(pool accountPool) error {
		if current, ok := currentAccount(agent); ok && current.ID == id {
			return errors.New("that account is in use; switch to another first")
		}
		for i, account := range pool[agent] {
			if account.ID == id {
				pool[agent] = append(pool[agent][:i], pool[agent][i+1:]...)
				return nil
			}
		}
		return errors.New("no such account")
	})
}
