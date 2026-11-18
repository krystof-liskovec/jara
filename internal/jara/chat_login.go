package jara

import (
	"encoding/json"
	"krystof.liskovec/jara/internal/agentrouter"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// /login signs Codex or Claude Code in on the machine the agent runs on,
// from any device: Codex with a device code through its app-server, Claude
// Code through the router running `claude auth login`, whose code is pasted
// back into the composer. Jara's own commands work while signed out.

type chatLoginState struct {
	codexStatus    string                       // the menu's description of each sign-in; "" until known
	claudeStatus   string                       //
	codexLogin     string                       // a Codex sign-in waiting in the browser
	claudeLogin    string                       // a Claude Code sign-in waiting for its code
	secret         string                       // "codex key" or "claude token" while one is typed
	stopEverywhere func()                       // stops signing Codex in on every host
	accounts       []agentrouter.AccountSummary // the pool /accounts listed last
	checked        map[string]bool              // the places and agents already checked at open
	hinted         bool                         // the signed-out hint showed for this turn
}

const (
	chatCodexAccountMethod = "account/read"
	chatClaudeStatusMethod = "jara/login/status"
)

func isChatLoginCommand(line string) bool {
	return line == "/login" || strings.HasPrefix(line, "/login ")
}

func (u *chatUI) runChatLoginCommand(line string) error {
	arg := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "/login")))
	if u.chatOffline() {
		u.printRole("system", "Not connected to the agent; /login works once the connection is back.\n")
		return nil
	}
	agent, method, _ := strings.Cut(arg, " ")
	switch agent = chatLoginAgent(agent); {
	case arg == "":
		if err := u.requestSignInStatus(""); err != nil {
			return err
		}
		if chatUIIsInteractive(u) {
			u.pickerRequested = "login"
		} else {
			u.printRole("system", chatLoginUsage)
		}
		return nil
	case arg == "cancel":
		return u.cancelChatLogin()
	case agent != "":
		return u.startChatLogin(agent, strings.TrimSpace(method))
	}
	u.printRole("system", chatLoginUsage)
	return nil
}

func (u *chatUI) loginRequest(method string, params any) error {
	return u.pendingRequest(method, params, "", 15*time.Minute)
}

func (u *chatUI) pendingRequest(method string, params any, action string, timeout time.Duration) error {
	id, err := u.c.request(method, params)
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{method: method, action: action, deadline: time.Now().Add(timeout)}
	return nil
}

// requestSignInStatus asks both agents whether they are signed in; with
// action "hint", a signed-out answer prints how to sign in.
func (u *chatUI) requestSignInStatus(agent string) error {
	action := ""
	if agent != "" {
		action = "hint"
	}
	if agent == "" || agent == protocol.AgentCodex {
		if err := u.pendingRequest(chatCodexAccountMethod, map[string]any{}, action, time.Minute); err != nil {
			return err
		}
	}
	if agent == "" || agent == protocol.AgentClaude {
		return u.pendingRequest(chatClaudeStatusMethod, map[string]string{"agent": protocol.AgentClaude}, action, time.Minute)
	}
	return nil
}

// checkChatSignIn tells, once per place, when the conversation's agent is
// not signed in there.
func (u *chatUI) checkChatSignIn() {
	agent := u.chatAgent()
	key := chatPlace.remote + "\x00" + agent
	if u.login.checked[key] {
		return
	}
	if u.login.checked == nil {
		u.login.checked = map[string]bool{}
	}
	u.login.checked[key] = true
	_ = u.requestSignInStatus(agent)
}

func (u *chatUI) chatAgent() string {
	if protocol.AgentForModel(u.effectiveModel) == protocol.AgentClaude {
		return protocol.AgentClaude
	}
	return protocol.AgentCodex
}

func (u *chatUI) cancelChatLogin() error {
	if u.login.secret != "" {
		u.stopSecretEntry("Sign-in cancelled.")
		return nil
	}
	if stop := u.login.stopEverywhere; stop != nil {
		u.login.stopEverywhere = nil
		stop()
		u.printRole("system", "Stopping the sign-in on the remaining hosts…\n")
		return nil
	}
	if u.login.codexLogin == "" && u.login.claudeLogin == "" {
		u.printRole("system", "No sign-in is running.\n")
		return nil
	}
	if id := u.login.codexLogin; id != "" {
		if err := u.loginRequest("account/login/cancel", map[string]string{"loginId": id}); err != nil {
			return err
		}
	}
	if id := u.login.claudeLogin; id != "" {
		if err := u.loginRequest("jara/login/cancel", map[string]string{"loginId": id}); err != nil {
			return err
		}
	}
	u.login.codexLogin, u.login.claudeLogin = "", ""
	u.printRole("system", "Sign-in cancelled.\n")
	return nil
}

// takeChatLoginCode sends a line typed while a key or token is asked for,
// or while Claude Code waits for its code.
func (u *chatUI) takeChatLoginCode(line string) (bool, error) {
	if u.login.secret != "" && line != "" && !strings.HasPrefix(line, "/") {
		return true, u.sendChatSecret(line)
	}
	if u.login.claudeLogin == "" || line == "" || strings.HasPrefix(line, "/") {
		return false, nil
	}
	u.printRole("system", "Checking the code…\n")
	return true, u.loginRequest("jara/login/code", map[string]string{"loginId": u.login.claudeLogin, "code": line})
}

// acceptChatLoginResponse handles the answers to /login's requests.
func (u *chatUI) acceptChatLoginResponse(m appMessage, p chatPending) bool {
	failed := func(what string) {
		u.printRole("system", what+": "+chatText(m.Error.Message)+"\n")
	}
	switch p.method {
	case chatCodexAccountMethod:
		u.acceptCodexAccount(m, p.action == "hint")
	case chatClaudeStatusMethod:
		u.acceptClaudeStatus(m, p.action == "hint")
	case "account/login/start":
		u.acceptCodexLoginStart(m)
	case "jara/login/token":
		u.acceptClaudeToken(m)
	case "jara/login/start":
		var result struct{ LoginID, URL string }
		if m.Error != nil {
			failed("Could not start the Claude Code sign-in")
			return true
		}
		_ = json.Unmarshal(m.Result, &result)
		u.login.claudeLogin = result.LoginID
		u.printRole("system", "### Sign in to Claude Code "+chatPlaceName(chatPlace.remote)+"\n1. Open "+chatText(result.URL)+
			"\n2. Sign in, then paste the code the page shows here and press Enter\n\n/login cancel stops.\n")
	case "jara/login/code", "account/login/cancel", "jara/login/cancel":
		if m.Error != nil {
			failed("Sign-in")
		}
	default:
		return false
	}
	return true
}

func (u *chatUI) acceptCodexAccount(m appMessage, hint bool) {
	var result struct {
		Account *struct {
			Type     string  `json:"type"`
			Email    *string `json:"email"`
			PlanType string  `json:"planType"`
		} `json:"account"`
		RequiresOpenaiAuth bool `json:"requiresOpenaiAuth"`
	}
	if m.Error != nil {
		u.login.codexStatus = "unknown: " + chatText(m.Error.Message)
		return
	}
	_ = json.Unmarshal(m.Result, &result)
	switch account := result.Account; {
	case account == nil && result.RequiresOpenaiAuth:
		u.login.codexStatus = "not signed in"
		if hint {
			u.printSignInHint(protocol.AgentCodex)
		}
	case account == nil:
		u.login.codexStatus = "signed in"
	case account.Type == "apiKey":
		u.login.codexStatus = "signed in with an API key"
	default:
		u.login.codexStatus = chatSignedInAs(account.Email, account.PlanType)
	}
}

func (u *chatUI) acceptClaudeStatus(m appMessage, hint bool) {
	var status struct {
		LoggedIn         bool    `json:"loggedIn"`
		AuthMethod       string  `json:"authMethod"`
		Email            *string `json:"email"`
		SubscriptionType string  `json:"subscriptionType"`
		Source           string  `json:"source"`
	}
	if m.Error != nil {
		u.login.claudeStatus = "unknown: " + chatText(m.Error.Message)
		return
	}
	_ = json.Unmarshal(m.Result, &status)
	if !status.LoggedIn {
		u.login.claudeStatus = "not signed in"
		if hint {
			u.printSignInHint(protocol.AgentClaude)
		}
		return
	}
	u.login.claudeStatus = chatSignedInAs(status.Email, status.SubscriptionType)
	if status.Source != "" {
		u.login.claudeStatus += " · " + chatText(status.Source)
	}
}

func chatSignedInAs(email *string, plan string) string {
	text := "signed in"
	if email != nil && *email != "" {
		text += " as " + chatText(*email)
	}
	if plan != "" {
		text += " · " + strings.ToUpper(plan[:1]) + plan[1:]
	}
	return text
}

func (u *chatUI) printSignInHint(agent string) {
	name, command := "Codex", "/login codex"
	if agent == protocol.AgentClaude {
		name, command = "Claude Code", "/login claude"
	}
	u.printRole("system", name+" is not signed in "+chatPlaceName(chatPlace.remote)+". Sign in with "+command+"; Jara's commands work meanwhile.\n")
}

// acceptChatLoginEvent reports a sign-in that finished.
func (u *chatUI) acceptChatLoginEvent(m appMessage) bool {
	if m.Method != "account/login/completed" && m.Method != "jara/login/completed" {
		return false
	}
	var done struct {
		Agent   string  `json:"agent"`
		LoginID *string `json:"loginId"`
		Success bool    `json:"success"`
		Error   *string `json:"error"`
	}
	_ = json.Unmarshal(m.Params, &done)
	name := "Codex"
	if m.Method == "jara/login/completed" {
		name = "Claude Code"
		u.login.claudeLogin = ""
	} else {
		u.login.codexLogin = ""
	}
	if done.Success {
		u.printRole("system", "Signed in to "+name+" "+chatPlaceName(chatPlace.remote)+". Accounts signed in before stay in the pool (/accounts).\n")
	} else if done.Error != nil && *done.Error != "" {
		u.printRole("system", name+" sign-in did not go through: "+chatText(*done.Error)+"\n")
	}
	_ = u.requestSignInStatus("") // refresh the menu's descriptions
	return true
}

// chatSignedOut recognizes an agent failing because it is not signed in.
func chatSignedOut(message string) bool {
	lower := strings.ToLower(message)
	for _, sign := range []string{"401 unauthorized", "missing bearer", "not logged in", "please run /login", "invalid api key", "authentication required", "oauth token has expired"} {
		if strings.Contains(lower, sign) {
			return true
		}
	}
	return false
}

// signedOutTurnError replaces a signed-out failure with how to sign in, once
// per turn; it reports whether it did.
func (u *chatUI) signedOutTurnError(message string) bool {
	if !chatSignedOut(message) {
		return false
	}
	if !u.login.hinted {
		u.login.hinted = true
		u.printSignInHint(u.chatAgent())
	}
	return true
}

func chatLoginPickerChoices(u *chatUI, selected int) []chatCompletionChoice {
	status := func(text string) string { return textOr(text, "checking…") }
	return []chatCompletionChoice{
		{Value: "Sign in", Description: "where the agent runs: " + strings.TrimPrefix(chatPlaceName(chatPlace.remote), "on ")},
		{Value: "Codex", Description: status(u.login.codexStatus), Selected: selected == 0, Index: 0, Total: 2},
		{Value: "Claude Code", Description: status(u.login.claudeStatus), Selected: selected == 1, Index: 1, Total: 2},
	}
}
