package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The context meter shows how much of the model's context window the
// conversation fills; /compact shrinks it on demand and /clear starts over.

type chatContextUsage struct {
	used, window int64
}

func (u *chatUI) acceptChatTokenUsage(params json.RawMessage) {
	var update struct {
		ThreadID   string `json:"threadId"`
		TokenUsage struct {
			Last struct {
				TotalTokens int64 `json:"totalTokens"`
			} `json:"last"`
			ModelContextWindow *int64 `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(params, &update) != nil || update.ThreadID != "" && update.ThreadID != u.thread {
		return
	}
	u.contextUsage.used = update.TokenUsage.Last.TotalTokens
	if window := update.TokenUsage.ModelContextWindow; window != nil && *window > 0 {
		u.contextUsage.window = *window
	}
}

// contextControlLabel is the meter text, or "" before the first model call.
func (u *chatUI) contextControlLabel() string {
	usage := u.contextUsage
	if usage.used <= 0 {
		return ""
	}
	if usage.window <= 0 {
		return chatTokenCount(usage.used) + " tokens"
	}
	left := max(0, 100-int(usage.used*100/usage.window))
	return fmt.Sprintf("%d%% left", left)
}

func chatTokenCount(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1000:
		return fmt.Sprintf("%dk", tokens/1000)
	default:
		return fmt.Sprint(tokens)
	}
}

func (u *chatUI) contextStatusLine() string {
	usage := u.contextUsage
	if usage.used <= 0 {
		return "Context: not reported yet\n"
	}
	if usage.window <= 0 {
		return "Context: " + chatTokenCount(usage.used) + " tokens\n"
	}
	return fmt.Sprintf("Context: %s of %s tokens (%s)\n", chatTokenCount(usage.used), chatTokenCount(usage.window), u.contextControlLabel())
}

func (u *chatUI) runChatCompactCommand(args string) error {
	if fields := strings.Fields(strings.ToLower(args)); len(fields) > 0 {
		if fields[0] != "idle" {
			u.print("Use /compact, or /compact idle [on|off|DURATION|status].\n")
			return nil
		}
		return u.runChatIdleCompactCommand(fields[1:])
	}
	if !u.threadReady {
		u.print("No conversation is open. Use /resume ID or /new first.\n")
		return nil
	}
	if u.busy {
		u.print("A turn is running. Wait for it to finish before compacting.\n")
		return nil
	}
	u.busy, u.cancel = true, false
	u.turnStartedAt = time.Now()
	u.setActivityLabel("Compacting context")
	u.setAgentState("running")
	return u.requestWithAction("thread/compact/start", map[string]string{"threadId": u.thread}, "")
}

func (u *chatUI) acceptChatCompactResponse(m appMessage) {
	if m.Error == nil {
		return // the compaction turn reports its own progress
	}
	u.failChatCompact(m.Error.Message)
}

func (u *chatUI) failChatCompact(message string) {
	u.busy = false
	u.turnStartedAt = time.Time{}
	u.setActivityLabel("")
	u.setAgentState("idle")
	u.printRole("system", "Could not compact the conversation: "+chatText(message)+"\n")
}

// runChatClearCommand starts a new conversation with an empty transcript.
func (u *chatUI) runChatClearCommand() error {
	if u.busy {
		u.print("A turn is running. Use /cancel or wait for it to finish.\n")
		return nil
	}
	u.clearChatTranscript()
	return u.open("")
}

func (u *chatUI) clearChatTranscript() {
	if transcript, ok := u.out.(interface{ ClearChat() }); ok {
		transcript.ClearChat()
	}
	u.diff = chatDiffState{mode: u.diff.mode}
}

// Extra commands share one dispatcher so the main command switch stays short.
var chatExtraCommands = map[string]func(u *chatUI, args string) error{
	"/compact": func(u *chatUI, args string) error { return u.runChatCompactCommand(args) },
	"/clear":   func(u *chatUI, _ string) error { return u.runChatClearCommand() },
	"/rewind":  func(u *chatUI, args string) error { return u.runChatRewindCommand(args) },
	"/alerts":  func(u *chatUI, args string) error { u.runChatAlertsCommand(args); return nil },
	"/image":   func(u *chatUI, args string) error { u.runChatImageCommand(args); return nil },
	"/export":  func(u *chatUI, args string) error { u.runChatExportCommand(args); return nil },
	"/diff":    func(u *chatUI, _ string) error { u.runChatDiffCommand(); return nil },
	"/notify":  func(u *chatUI, args string) error { return u.runChatNotifyCommand(args) },
	"/vim":     (*chatUI).runChatVimCommand,
}

func splitChatCommand(line string) (name, args string) {
	line = strings.TrimSpace(line)
	if index := strings.IndexAny(line, " \t"); index >= 0 {
		return line[:index], strings.TrimSpace(line[index+1:])
	}
	return line, ""
}

func isChatExtraCommand(line string) bool {
	name, _ := splitChatCommand(line)
	_, ok := chatExtraCommands[name]
	return ok
}

func (u *chatUI) runChatExtraCommand(line string) error {
	name, args := splitChatCommand(line)
	return chatExtraCommands[name](u, args)
}

// acceptChatExtraResponse handles replies to the requests these features make.
func (u *chatUI) acceptChatExtraResponse(m appMessage, p chatPending) bool {
	switch p.method {
	case "thread/compact/start":
		u.acceptChatCompactResponse(m)
	case "jara/idleCompaction/get", "jara/idleCompaction/set":
		u.acceptChatIdleCompactResponse(m)
	case "jara/rewind":
		return u.acceptChatRewindResponse(m, p)
	case "fuzzyFileSearch":
		u.acceptChatMentionResponse(m, p)
	case "jara/files/list":
		u.acceptChatFolderResponse(m, p)
	case "jara/commands/list":
		u.acceptCustomCommands(m)
	case "jara/notify/get", "jara/notify/set", "jara/notify/test":
		u.acceptChatNotifyResponse(m, p)
	case "jara/git/diff":
		u.acceptGitDiffResponse(m)
	case chatProjectLocationMethod:
		u.acceptChatProjectLocation(m)
	default:
		return false
	}
	return true
}

// expireChatExtraRequest drops a timed-out request of these features
// without ending the session.
func (u *chatUI) expireChatExtraRequest(id string, pending chatPending) bool {
	switch pending.method {
	case "jara/git/diff":
		u.expireGitDiffRequest()
	case chatProjectLocationMethod:
		u.project.requesting = false
	case "fuzzyFileSearch", "jara/commands/list", "jara/files/list":
	case "jara/notify/get", "jara/notify/set", "jara/notify/test":
		u.printRole("system", "Notifications: no reply in time.\n")
	case "jara/idleCompaction/get", "jara/idleCompaction/set":
		u.printRole("system", "Idle compaction: no reply in time.\n")
	case "jara/rewind":
		u.printRole("system", "Rewinding timed out.\n")
	case "thread/compact/start":
		u.failChatCompact("no response")
	default:
		return false
	}
	delete(u.pending, id)
	return true
}
