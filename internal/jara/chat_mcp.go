package jara

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// /mcp shows the shared MCP servers, where each runs and, from the agent,
// which ones this conversation has loaded. /mcp add, where, remove, import
// and sync change the shared list like jara config mcp does; the change is
// sent to the remotes in the background, and agents load it when their
// session starts again.

func isChatMCPCommand(line string) bool {
	return line == "/mcp" || strings.HasPrefix(line, "/mcp ")
}

const chatMCPStatusMethod = "mcpServerStatus/list"

func (u *chatUI) runChatMCPCommand(line string) error {
	args, err := splitChatMCPArgs(strings.TrimSpace(strings.TrimPrefix(line, "/mcp")))
	if err != nil {
		u.printRole("system", err.Error()+"\n")
		return nil
	}
	if len(args) == 0 || len(args) == 1 && args[0] == "list" {
		return u.showChatMCP()
	}
	u.runInBackground(func(func(string)) string { return runChatMCPChange(args) })
	return nil
}

// runChatMCPChange edits the shared list off the interface's goroutine, as
// sending it to remotes can take a while.
func runChatMCPChange(args []string) string {
	var out strings.Builder
	c, err := readConfig()
	if err == nil {
		err = mcpConfigCommand(&c, args, &out)
	}
	text := out.String()
	if err != nil {
		text += "Error: " + err.Error() + "\n"
	} else if args[0] != "where" || len(args) > 1 {
		text += "Agents load the change when their session starts again; this conversation keeps its servers until then.\n"
	}
	return strings.ReplaceAll(text, "jara config mcp", "/mcp")
}

// showChatMCP lists the shared servers, then asks the agent which servers it
// has loaded; that answer follows as its own message.
func (u *chatUI) showChatMCP() error {
	c, err := readConfig()
	if err != nil {
		u.printRole("system", "Could not read Jara's configuration: "+err.Error()+"\n")
		return nil
	}
	place, placeName := chatPlace.remote, chatPlace.remote
	if place == "" {
		place, placeName = mcpLocal, "this device"
	}
	var b strings.Builder
	b.WriteString("### Shared MCP servers\n")
	if len(c.MCPServers) == 0 {
		b.WriteString("None yet. Add one with `/mcp add NAME --url URL` or `/mcp add NAME -- COMMAND ARGS`, or bring yours over with `/mcp import`.\n")
	}
	for _, name := range slices.Sorted(maps.Keys(c.MCPServers)) {
		server := c.MCPServers[name]
		line := "- `" + name + "` " + mcpServerSummary(server)
		if len(server.On) > 0 && !slices.Contains(server.On, place) {
			line += " · not on " + placeName
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nChange them with `/mcp add`, `/mcp where NAME everywhere|PLACE...`, `/mcp remove NAME` or `/mcp sync`; PLACE is `local` or a remote name.\n")
	u.printRole("system", b.String())
	if !u.threadReady || u.chatOffline() {
		return nil
	}
	id, err := u.c.request(chatMCPStatusMethod, map[string]any{"threadId": u.thread, "detail": "toolsAndAuthOnly", "limit": 100})
	if err != nil {
		return err
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.pending[id] = chatPending{method: chatMCPStatusMethod, deadline: time.Now().Add(time.Minute), threadID: u.thread}
	return nil
}

// acceptChatMCPStatus shows the servers the agent has loaded, its own
// configuration's included.
func (u *chatUI) acceptChatMCPStatus(m appMessage) {
	if m.Error != nil {
		u.printRole("system", "Could not ask the agent about its MCP servers: "+chatText(m.Error.Message)+"\n")
		return
	}
	var result struct {
		Data []struct {
			Name          string                     `json:"name"`
			RuntimeStatus string                     `json:"runtimeStatus"`
			AuthStatus    string                     `json:"authStatus"`
			Tools         map[string]json.RawMessage `json:"tools"`
			ToolsError    string                     `json:"toolsError"`
		} `json:"data"`
		Started *bool `json:"started"`
	}
	_ = json.Unmarshal(m.Result, &result)
	var b strings.Builder
	b.WriteString("### Loaded in this conversation\n")
	switch {
	case result.Started != nil && !*result.Started:
		b.WriteString("Claude Code reports its servers once the first turn starts it.\n")
	case len(result.Data) == 0:
		b.WriteString("No MCP servers.\n")
	}
	for _, server := range result.Data {
		notes := []string{chatMCPStatusName(server.RuntimeStatus)}
		if len(server.Tools) > 0 {
			notes = append(notes, chatCount(len(server.Tools), "tool"))
		}
		if server.AuthStatus == "notLoggedIn" {
			notes = append(notes, "not logged in")
		}
		if server.ToolsError != "" {
			notes = append(notes, chatText(server.ToolsError))
		}
		b.WriteString("- `" + chatText(server.Name) + "` " + strings.Join(slices.DeleteFunc(notes, func(s string) bool { return s == "" }), " · ") + "\n")
	}
	u.printRole("system", b.String())
}

func chatMCPStatusName(status string) string {
	switch status {
	case "notStarted":
		return "not started"
	case "authenticationRequired":
		return "needs sign-in"
	}
	return chatText(status)
}

func chatCount(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// splitChatMCPArgs splits a line into words; single or double quotes keep
// spaces inside one word.
func splitChatMCPArgs(line string) ([]string, error) {
	var args []string
	var word strings.Builder
	inWord, quote := false, rune(0)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				args, inWord = append(args, word.String()), false
				word.Reset()
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("A quote is not closed.")
	}
	if inWord {
		args = append(args, word.String())
	}
	return args, nil
}
