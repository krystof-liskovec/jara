package jara

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/protocol"
)

// /accounts lists the accounts pooled where the agent runs: every account
// signed in there stays, and when the one in use runs out of usage Jara
// continues the turn on the next. /accounts use and remove pick or drop one
// by its number in the list.

func isChatAccountsCommand(line string) bool {
	return line == "/accounts" || strings.HasPrefix(line, "/accounts ")
}

const chatAccountsUsage = "Use /accounts, /accounts use NUMBER or /accounts remove NUMBER.\n"

func (u *chatUI) runChatAccountsCommand(line string) error {
	if u.chatOffline() {
		u.printRole("system", "Not connected to the agent; /accounts works once the connection is back.\n")
		return nil
	}
	fields := strings.Fields(strings.TrimPrefix(line, "/accounts"))
	if len(fields) == 0 {
		return u.pendingRequest("jara/accounts/list", map[string]any{}, "", time.Minute)
	}
	number, err := strconv.Atoi(textOr(strings.Join(fields[1:], ""), "x"))
	if len(fields) != 2 || (fields[0] != "use" && fields[0] != "remove") || err != nil || number < 1 || number > len(u.login.accounts) {
		if len(u.login.accounts) == 0 && err == nil {
			u.printRole("system", "List the accounts with /accounts first.\n")
			return nil
		}
		u.printRole("system", chatAccountsUsage)
		return nil
	}
	account := u.login.accounts[number-1]
	return u.pendingRequest("jara/accounts/"+fields[0], map[string]string{"agent": account.Agent, "id": account.ID}, fields[0], time.Minute)
}

func (u *chatUI) acceptChatAccountsResponse(m appMessage, p chatPending) bool {
	if !strings.HasPrefix(p.method, "jara/accounts/") {
		return false
	}
	if m.Error != nil {
		u.printRole("system", "Could not "+textOr(p.action, "list the accounts")+": "+chatText(m.Error.Message)+"\n")
		return true
	}
	var result struct {
		Accounts []agentrouter.AccountSummary `json:"accounts"`
	}
	_ = json.Unmarshal(m.Result, &result)
	u.login.accounts = result.Accounts
	u.printRole("system", chatAccountsText(result.Accounts, time.Now()))
	return true
}

// chatAccountsText lists the pool by agent, numbered for /accounts use.
func chatAccountsText(accounts []agentrouter.AccountSummary, now time.Time) string {
	var b strings.Builder
	b.WriteString("### Accounts " + chatPlaceName(chatPlace.remote) + "\n")
	if len(accounts) == 0 {
		b.WriteString("None signed in yet; /login adds one.\n")
	}
	agent := ""
	for index, account := range accounts {
		if account.Agent != agent {
			agent = account.Agent
			b.WriteString(map[string]string{protocol.AgentCodex: "Codex", protocol.AgentClaude: "Claude Code"}[agent] + "\n")
		}
		notes := []string{}
		if account.Active {
			notes = append(notes, "in use")
		}
		if account.ExhaustedUntil > 0 {
			notes = append(notes, "out of usage until "+usageWhen(time.Unix(account.ExhaustedUntil, 0), now))
		}
		line := "[" + strconv.Itoa(index+1) + "] " + chatText(account.Label)
		if len(notes) > 0 {
			line += " · " + strings.Join(notes, " · ")
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nEvery account signed in here is kept, and when the one in use runs out of usage the turn continues on the next. /login adds one; /accounts use N or remove N picks or drops one.\n")
	return b.String()
}

// acceptChatAccountSwitch tells that a turn continues on another account.
func (u *chatUI) acceptChatAccountSwitch(m appMessage) bool {
	if m.Method != "jara/account/switched" {
		return false
	}
	var switched struct {
		Agent, From, To string
		ResetsAt        int64 `json:"resetsAt"`
	}
	_ = json.Unmarshal(m.Params, &switched)
	name := "Codex"
	if switched.Agent == protocol.AgentClaude {
		name = "Claude Code"
	}
	back := ""
	if switched.ResetsAt > time.Now().Unix() {
		back = " (back " + usageWhen(time.Unix(switched.ResetsAt, 0), time.Now()) + ")"
	}
	u.printRole("system", name+" account "+chatText(switched.From)+" ran out of usage"+back+"; continuing on "+chatText(switched.To)+".\n")
	return true
}
