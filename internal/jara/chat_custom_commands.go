package jara

import (
	"encoding/json"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// Custom slash commands come from the agent router, which reads the prompt
// files on the workspace host (see agent_router_commands.go). Jara expands
// them before sending, so a command works whichever provider runs the turn.

func (u *chatUI) requestCustomCommands() error {
	return u.requestWithAction("jara/commands/list", map[string]string{"cwd": u.cwd}, "")
}

func (u *chatUI) acceptCustomCommands(m appMessage) {
	var result struct {
		Commands []protocol.CustomCommand `json:"commands"`
	}
	if m.Error != nil || json.Unmarshal(m.Result, &result) != nil {
		return
	}
	builtin := map[string]bool{}
	for _, command := range chatCommandCompletions {
		builtin[command.name] = true
	}
	u.customCommands = nil
	for _, command := range result.Commands {
		if !builtin["/"+command.Name] && !isChatExtraCommand("/"+command.Name) && protocol.ValidCustomCommandName(command.Name) {
			command.Description = chatText(command.Description)
			u.customCommands = append(u.customCommands, command)
		}
	}
}

// expandCustomCommand turns "/name args" into the command's prompt.
func (u *chatUI) expandCustomCommand(line string) (string, bool) {
	name, args := splitChatCommand(line)
	for _, command := range u.customCommands {
		if "/"+command.Name == name {
			return expandCustomCommandBody(command.Body, args), true
		}
	}
	return "", false
}

// expandCustomCommandBody fills $ARGUMENTS and $1…$9 the way Claude Code and
// Codex do; arguments a prompt does not reference are appended.
func expandCustomCommandBody(body, args string) string {
	fields := strings.Fields(args)
	used := strings.Contains(body, "$ARGUMENTS")
	body = strings.ReplaceAll(body, "$ARGUMENTS", args)
	for index := 9; index >= 1; index-- {
		token := "$" + strconv.Itoa(index)
		if !strings.Contains(body, token) {
			continue
		}
		used = true
		value := ""
		if index <= len(fields) {
			value = fields[index-1]
		}
		body = strings.ReplaceAll(body, token, value)
	}
	if !used && args != "" {
		body += "\n\n" + args
	}
	return body
}

func (u *chatUI) customCommandCompletions() []chatCommandCompletion {
	completions := make([]chatCommandCompletion, 0, len(u.customCommands))
	for _, command := range u.customCommands {
		description := textutil.FirstNonEmpty(command.Description, "Custom command")
		if command.ArgumentHint != "" {
			description += " · " + chatText(command.ArgumentHint)
		}
		completions = append(completions, chatCommandCompletion{name: "/" + command.Name, description: description + " (" + command.Source + ")", arguments: true})
	}
	return completions
}
