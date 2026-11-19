package jara

import (
	"strconv"
	"strings"
)

type chatCompletionOption struct {
	Value       string
	Description string
}

type chatCompletionContext struct {
	Models    []chatCompletionOption
	Reasoning []chatCompletionOption
	Sessions  []chatCompletionOption
	Files     []chatCompletionOption  // @file matches
	Commands  []chatCommandCompletion // custom commands
}

type chatEditorCandidate struct {
	choice      chatCompletionChoice
	start, end  int
	replacement string
	append      string
}

type chatCommandCompletion struct {
	name        string
	description string
	arguments   bool
}

var chatCommandCompletions = []chatCommandCompletion{
	{name: "/new", description: "Start a new conversation"},
	{name: "/sessions", description: "Browse saved sessions in this folder", arguments: true},
	{name: "/resume", description: "Choose a recent saved session", arguments: true},
	{name: "/model", description: "Select a model for this conversation", arguments: true},
	{name: "/models", description: "List available models"},
	{name: "/reasoning", description: "Select a reasoning effort", arguments: true},
	{name: "/default", description: "Set the default model for new conversations", arguments: true},
	{name: "/fallback", description: "Set the model that takes over at a usage limit", arguments: true},
	{name: "/compose", description: "Compose a multi-line message"},
	{name: "/send", description: "Send the composed message"},
	{name: "/discard", description: "Discard the composed message"},
	{name: "/goal", description: "Set or manage a persistent goal", arguments: true},
	{name: "/plan", description: "Switch this conversation to Plan mode", arguments: true},
	{name: "/auto", description: "Turn auto-approval mode on or off", arguments: true},
	{name: "/vim", description: "Toggle Vim editing in the composer", arguments: true},
	{name: "/btw", description: "Ask a side question without adding it to the conversation", arguments: true},
	{name: "/agents", description: "List subagents, or view one by number", arguments: true},
	{name: "/compact", description: "Compact the context now, or set idle compaction", arguments: true},
	{name: "/clear", description: "Start a new conversation with an empty transcript"},
	{name: "/rewind", description: "Go back to before an earlier prompt", arguments: true},
	{name: "/diff", description: "Show or hide the changes pane, or list the changes"},
	{name: "/export", description: "Save the conversation as Markdown", arguments: true},
	{name: "/image", description: "Attach an image file to the next message", arguments: true},
	{name: "/alerts", description: "Turn bell and desktop alerts on or off", arguments: true},
	{name: "/notify", description: "Send push notifications to your phone", arguments: true},
	{name: "/schedule", description: "Schedule a prompt in this conversation", arguments: true},
	{name: "/crunch", description: "Run a prompt through usage-limit pauses", arguments: true},
	{name: "/goalcrunch", description: "Run a persistent goal through usage-limit pauses", arguments: true},
	{name: "/queue", description: "Inspect, run, clear, or edit queued prompts", arguments: true},
	{name: "/status", description: "Show connection and turn state"},
	{name: "/answer", description: "Answer a pending question", arguments: true},
	{name: "/cancel", description: "Stop the current turn"},
	{name: "/approve", description: "Approve the oldest pending request"},
	{name: "/deny", description: "Reject the oldest pending request"},
	{name: "/accounts", description: "List or pick the pooled agent accounts", arguments: true},
	{name: "/login", description: "Sign Codex or Claude Code in where the agent runs", arguments: true},
	{name: "/usage", description: "Show the usage limits of every Codex and Claude Code account", arguments: true},
	{name: "/mcp", description: "Show or change the shared MCP servers", arguments: true},
	{name: "/remote", description: "Move this conversation to this device or a remote host", arguments: true},
	{name: "/detach", description: "Disconnect and leave the remote session running"},
	{name: "/quit", description: "Stop an idle session; leave an active agent running"},
	{name: "/stop", description: "Stop the remote session and release the workspace"},
	{name: "/help", description: "Show available commands"},
}

func chatCompletionContextFromUI(u *chatUI) chatCompletionContext {
	var context chatCompletionContext
	if u == nil {
		return context
	}
	context.Files = chatMentionOptions(u.mentions)
	context.Commands = u.customCommandCompletions()
	if u.models != nil && u.models.complete {
		for _, model := range u.models.models {
			id := model.Model
			if id == "" {
				id = model.ID
			}
			if id == "" {
				continue
			}
			label := model.DisplayName
			if label == "" {
				label = id
			}
			context.Models = append(context.Models, chatCompletionOption{Value: id, Description: label})
		}
		if model := u.reasoningModel(); model != nil {
			for _, option := range model.SupportedReasoningEfforts {
				context.Reasoning = append(context.Reasoning, chatCompletionOption(option))
			}
		}
	}
	if u.sessions != nil && u.sessions.complete {
		for index, session := range u.sessions.matches {
			title := session.Name
			if strings.TrimSpace(title) == "" {
				title = session.Preview
			}
			if strings.TrimSpace(title) == "" {
				title = session.ID
			}
			context.Sessions = append(context.Sessions, chatCompletionOption{
				Value:       strconv.Itoa(index + 1),
				Description: compactChatSessionText(title, chatSessionTitleRunes),
			})
		}
	}
	return context
}

func chatCompletionCandidates(text string, cursor int, context chatCompletionContext) []chatEditorCandidate {
	if start, query, ok := chatMentionToken(text, cursor); ok {
		return chatMentionCandidates(text, start, cursor, query, context)
	}
	if cursor != len(text) || text == "" || !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") || strings.ContainsAny(text, "\r\n") {
		return nil
	}
	lower := strings.ToLower(text)
	if strings.ContainsRune(text, ' ') {
		return argumentCompletions(text, context)
	}
	var candidates []chatEditorCandidate
	for _, command := range append(chatCommandCompletions, context.Commands...) {
		if !strings.HasPrefix(strings.ToLower(command.name), lower) {
			continue
		}
		// An exact, unambiguous command is already complete; leaving it
		// submission-ready avoids forcing a redundant second Enter.
		if strings.EqualFold(command.name, text) && !hasLongerCommandPrefix(command.name) {
			continue
		}
		replacement, appendText := command.name, ""
		if command.arguments {
			appendText = " "
		}
		candidates = append(candidates, chatEditorCandidate{
			choice:      chatCompletionChoice{Value: command.name, Description: command.description},
			start:       0,
			end:         len(text),
			replacement: replacement,
			append:      appendText,
		})
	}
	return candidates
}

func hasLongerCommandPrefix(name string) bool {
	for _, command := range chatCommandCompletions {
		if len(command.name) > len(name) && strings.HasPrefix(command.name, name) {
			return true
		}
	}
	return false
}

func argumentCompletions(text string, context chatCompletionContext) []chatEditorCandidate {
	for _, command := range chatCommandCompletions {
		if !command.arguments {
			continue
		}
		space := len(command.name)
		if len(text) <= space || !strings.EqualFold(text[:space], command.name) || text[space] != ' ' {
			continue
		}
		start := space + 1
		prefix := text[start:]
		if strings.ContainsRune(prefix, ' ') {
			return nil
		}
		choices := argumentOptions(command.name, context)
		var candidates []chatEditorCandidate
		for _, option := range choices {
			if !strings.HasPrefix(strings.ToLower(option.Value), strings.ToLower(prefix)) &&
				!strings.HasPrefix(strings.ToLower(option.Description), strings.ToLower(prefix)) {
				continue
			}
			candidates = append(candidates, chatEditorCandidate{
				choice: chatCompletionChoice{
					Value:       strings.TrimSuffix(command.name, " ") + " " + option.Value,
					Description: option.Description,
				},
				start:       0,
				end:         len(text),
				replacement: command.name + " " + option.Value,
			})
		}
		return candidates
	}
	return nil
}

func argumentOptions(command string, context chatCompletionContext) []chatCompletionOption {
	switch command {
	case "/model":
		return context.Models
	case "/resume":
		return context.Sessions
	case "/reasoning":
		return context.Reasoning
	default:
		return nil
	}
}

func chatCompletionCatalog() []chatCompletionChoice {
	choices := make([]chatCompletionChoice, 0, len(chatCommandCompletions))
	for _, command := range chatCommandCompletions {
		choices = append(choices, chatCompletionChoice{Value: command.name, Description: command.description})
	}
	return choices
}
