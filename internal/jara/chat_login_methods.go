package jara

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"krystof.liskovec/jara/internal/protocol"
)

// Each agent signs in several ways; /login AGENT METHOD picks one, and the
// menu lists them after the agent. Keys and tokens are typed into the
// composer hidden, stay out of its history and go to the machine the agent
// runs on, never into the conversation.

type chatLoginMethod struct{ name, title, about string }

var chatLoginAgents = []string{protocol.AgentCodex, protocol.AgentClaude}

var chatLoginMethods = map[string][]chatLoginMethod{
	protocol.AgentCodex: {
		{"device", "Device code", "enter a code at auth.openai.com from any device"},
		{"browser", "Browser", "sign in with a browser on the machine the agent runs on"},
		{"key", "API key", "an OpenAI API key, billed per use"},
		{"device everywhere", "Device code on every host", "your ChatGPT account on this device and each remote, one code each"},
		{"add everywhere", "Add an account on every host", "one more ChatGPT account on each host; Jara switches when one runs out"},
		{"key everywhere", "API key for every host", "the same OpenAI key saved on this device and every remote"},
	},
	protocol.AgentClaude: {
		{"browser", "Claude account", "sign in with a browser, then paste the code it shows"},
		{"console", "Anthropic Console", "like a Claude account, billed per use"},
		{"token", "Token or API key", "paste a token from claude setup-token or an sk-ant-api key"},
		{"token everywhere", "Token for every host", "the same token saved on this device and every remote"},
	},
}

const chatLoginUsage = "Sign in with /login codex [device, browser, key, device everywhere, add everywhere or key everywhere] or /login claude [browser, console, token or token everywhere]; /login cancel stops.\n"

func chatLoginAgent(name string) string {
	switch name {
	case protocol.AgentCodex, "openai", "chatgpt":
		return protocol.AgentCodex
	case protocol.AgentClaude, "claude-code", "anthropic":
		return protocol.AgentClaude
	}
	return ""
}

// startChatLogin begins the sign-in method names; "" is the first one.
func (u *chatUI) startChatLogin(agent, method string) error {
	methods := chatLoginMethods[agent]
	if method == "" {
		method = methods[0].name
	}
	known := false
	for _, m := range methods {
		known = known || m.name == method
	}
	if !known {
		u.printRole("system", chatLoginUsage)
		return nil
	}
	switch agent + " " + method {
	case "codex device":
		return u.loginRequest("account/login/start", map[string]string{"type": "chatgptDeviceCode"})
	case "codex browser":
		return u.loginRequest("account/login/start", map[string]string{"type": "chatgpt"})
	case "claude browser", "claude console":
		return u.loginRequest("jara/login/start", map[string]string{"agent": protocol.AgentClaude, "method": method})
	case "codex device everywhere", "codex add everywhere":
		c, err := readConfig()
		if err != nil {
			u.printRole("system", "Could not read Jara's configuration: "+err.Error()+"\n")
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		if !u.runInBackground(func(say func(string)) string {
			return "### Codex sign-in\n" + chatMarkdownList(codexDeviceLoginEverywhere(ctx, c, say, method == "add everywhere"))
		}) {
			cancel()
			return nil
		}
		u.login.stopEverywhere = cancel
		note := "Signing Codex in on each host in turn; hosts already signed in are skipped. /login cancel stops.\n"
		if method == "add everywhere" {
			note = "Adding a Codex account on each host in turn; sign in with an account not used there yet. /login cancel stops.\n"
		}
		u.printRole("system", note)
		return nil
	}
	u.login.secret = agent + " " + method // codex key, claude token, claude token everywhere
	about, where := "Paste the OpenAI API key", chatPlaceName(chatPlace.remote)
	if agent == protocol.AgentClaude {
		about = "Paste a token from `claude setup-token` (run it on any machine with a browser) or an Anthropic API key"
	}
	if strings.HasSuffix(method, "everywhere") {
		where = "on this device and every remote"
	}
	u.printRole("system", about+" and press Enter. It stays hidden and is saved "+where+"; Esc cancels.\n")
	return nil
}

func (u *chatUI) sendChatSecret(secret string) error {
	kind := u.login.secret
	u.login.secret = ""
	switch kind {
	case "codex key everywhere":
		c, err := readConfig()
		if err != nil {
			u.printRole("system", "Could not read Jara's configuration: "+err.Error()+"\n")
			return nil
		}
		if u.runInBackground(func(func(string)) string {
			return "### OpenAI API key\n" + chatMarkdownList(shareCodexAPIKey(c, secret))
		}) {
			u.printRole("system", "Saving the key everywhere…\n")
		}
		return nil
	case "codex key":
		return u.loginRequest("account/login/start", map[string]string{"type": "apiKey", "apiKey": secret})
	case "claude token everywhere":
		c, err := readConfig()
		if err != nil {
			u.printRole("system", "Could not read Jara's configuration: "+err.Error()+"\n")
			return nil
		}
		if u.runInBackground(func(func(string)) string {
			return "### Claude token\n" + chatMarkdownList(shareClaudeToken(c, []byte(secret)))
		}) {
			u.printRole("system", "Saving the token everywhere…\n")
		}
		return nil
	}
	return u.loginRequest("jara/login/token", map[string]string{"agent": protocol.AgentClaude, "token": secret})
}

func (u *chatUI) stopSecretEntry(message string) {
	u.login.secret = ""
	u.printRole("system", message+"\n")
}

func chatSecretLabel(kind string) string {
	if strings.HasPrefix(kind, "codex") {
		return "OpenAI API key"
	}
	return "Claude token"
}

// chatMaskSecret shows each character as a dot, keeping the cursor on it.
func chatMaskSecret(text string, cursor int) (string, int) {
	cursor = min(max(cursor, 0), len(text))
	return strings.Repeat("•", utf8.RuneCountInString(text)), utf8.RuneCountInString(text[:cursor]) * len("•")
}

// acceptCodexLoginStart shows how to finish a Codex sign-in.
func (u *chatUI) acceptCodexLoginStart(m appMessage) {
	if m.Error != nil {
		u.printRole("system", "Could not start the Codex sign-in: "+chatText(m.Error.Message)+"\n")
		return
	}
	var result struct {
		Type, LoginID, UserCode, VerificationURL, AuthURL string
	}
	_ = json.Unmarshal(m.Result, &result)
	place := chatPlaceName(chatPlace.remote)
	switch result.Type {
	case "apiKey":
		u.printRole("system", "Codex now uses the API key "+place+".\n")
		_ = u.requestSignInStatus("")
	case "chatgpt":
		u.login.codexLogin = result.LoginID
		u.printRole("system", "### Sign in to Codex "+place+"\nOpen "+chatText(result.AuthURL)+
			"\n\nUse a browser on the machine the agent runs on: the sign-in returns to it. From another device, use /login codex device.\n")
	default:
		u.login.codexLogin = result.LoginID
		u.printRole("system", "### Sign in to Codex "+place+"\n1. Open "+chatText(result.VerificationURL)+
			"\n2. Enter the code `"+chatText(result.UserCode)+"`\n\nJara says when it went through; /login cancel stops.\n")
	}
}

// acceptClaudeToken reports a stored token and what Claude Code now uses.
func (u *chatUI) acceptClaudeToken(m appMessage) {
	if m.Error != nil {
		u.printRole("system", "The token was not saved: "+chatText(m.Error.Message)+"\n")
		return
	}
	u.acceptClaudeStatus(m, false)
	text := "Saved the token " + chatPlaceName(chatPlace.remote) + "; Claude Code uses it from the next turn."
	var status struct{ Source string }
	_ = json.Unmarshal(m.Result, &status)
	if strings.HasSuffix(status.Source, "from the environment") {
		text += " Until " + strings.TrimSuffix(status.Source, " from the environment") + " is unset there, that one is used instead."
	}
	u.printRole("system", text+"\n")
}

func chatLoginMethodChoices(u *chatUI, agent string, selected int) []chatCompletionChoice {
	name, status := "Codex", u.login.codexStatus
	if agent == protocol.AgentClaude {
		name, status = "Claude Code", u.login.claudeStatus
	}
	methods := chatLoginMethods[agent]
	choices := []chatCompletionChoice{{Value: "Sign in to " + name, Description: textOr(status, "checking…")}}
	for index, method := range methods {
		choices = append(choices, chatCompletionChoice{Value: method.title, Description: method.about,
			Selected: index == selected, Index: index, Total: len(methods)})
	}
	return choices
}

// chatMarkdownList shows report lines as a list.
func chatMarkdownList(report string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(report), "\n") {
		b.WriteString("- " + line + "\n")
	}
	return b.String()
}
