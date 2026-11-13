package jara

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
)

const (
	chatAgentStripRows     = 4 // rows for subagents, besides the main agent
	chatAgentCommandOutput = 1200
)

func isChatAgentsCommand(line string) bool {
	return line == "/agents" || strings.HasPrefix(line, "/agents ")
}

// runChatAgentsCommand lists subagents or, with a number, shows one.
func (u *chatUI) runChatAgentsCommand(line string) {
	argument := strings.TrimSpace(strings.TrimPrefix(line, "/agents"))
	if argument == "" {
		u.printRole("system", u.chatAgentList())
		return
	}
	if argument == "main" || argument == "0" {
		u.agentViewRequest = "main"
		return
	}
	number, err := strconv.Atoi(argument)
	if err != nil || number < 1 || number > len(u.agents) {
		u.printRole("system", "Use /agents to list subagents and /agents N to view one.\n")
		return
	}
	if !chatUIIsInteractive(u) {
		u.printRole("system", "Viewing a subagent needs the interactive terminal.\n")
		return
	}
	u.agentViewRequest = u.agents[number-1].id
}

func (u *chatUI) chatAgentList() string {
	if len(u.agents) == 0 {
		return "This conversation has no subagents yet.\n"
	}
	var list strings.Builder
	list.WriteString("Subagents (Ctrl+↓/↑ or /agents N to view one, Esc to return):\n")
	for index, agent := range u.agents {
		fmt.Fprintf(&list, "  %d. %s · %s\n", index+1, textutil.SanitizeText(agent.name), agent.status)
	}
	return list.String()
}

// cycleChatAgentView moves the shown transcript to the next or previous agent,
// the main agent coming first.
func (u *chatUI) cycleChatAgentView(step int) {
	ids := []string{""}
	for _, agent := range u.agents {
		ids = append(ids, agent.id)
	}
	current := 0
	for index, id := range ids {
		if id == u.viewing {
			current = index
		}
	}
	u.showChatAgent(ids[(current+step+len(ids))%len(ids)])
}

func (u *chatUI) showChatAgent(id string) {
	if id == "main" {
		id = ""
	}
	if id != "" && u.agentByID[id] == nil {
		return
	}
	u.viewing = id
	if agent := u.viewedAgent(); agent != nil {
		agent.stale = true
	}
}

// renderChatAgentTranscript writes a subagent's transcript, read from its
// thread, into out.
func renderChatAgentTranscript(out *chatTerminalOutput, agent *chatAgent) {
	out.ClearChat()
	out.SetChatAgent(textutil.SanitizeText(agent.name))
	if len(agent.turns) == 0 && agent.readError == "" {
		out.append("system", "Loading "+textutil.SanitizeText(agent.name)+"…\n")
	}
	for _, turn := range agent.turns {
		fallback := time.Time{}
		if turn.StartedAt != nil {
			fallback = time.Unix(*turn.StartedAt, 0)
		}
		for _, item := range turn.Items {
			at := chatHistoryItemTime(item, fallback)
			switch item.Type {
			case "userMessage":
				var text strings.Builder
				for _, content := range item.Content {
					if content.Type == "text" || content.Type == "input_text" {
						text.WriteString(content.Text)
					}
				}
				if text.Len() > 0 {
					out.appendAt("user", chatText(text.String())+"\n", at)
				}
			case "agentMessage":
				out.appendAt("assistant", chatText(item.Text)+"\n", at)
			case "plan":
				out.appendAt("assistant", "Plan:\n"+chatText(item.Text)+"\n", at)
			case "commandExecution":
				text := "$ " + chatText(item.Command) + "\n"
				if output := strings.TrimRight(item.AggregatedOutput, "\n"); output != "" {
					text += chatText(textutil.TailText(output, chatAgentCommandOutput)) + "\n"
				}
				out.appendAt("tool", text, at)
			case "fileChange":
				out.appendAt("tool", formatChatFileChangeList(item.Status, item.Changes), at)
			case "subAgentActivity", "collabAgentToolCall":
				if text := chatSubagentItemText(item, nil); text != "" {
					out.appendAt("subagent", text+"\n", at)
				}
			}
		}
	}
	if agent.readError != "" {
		out.append("system", "Could not load the transcript: "+agent.readError+"\n")
	}
	for _, note := range agent.notes {
		out.append("system", chatText(note)+"\n")
	}
}

// chatAgentStrip describes the subagent strip above the composer: the main
// agent first, then subagents with the viewed one kept visible. targets name
// the agent each line shows ("main" for the main agent; "" for none).
func chatAgentStrip(u *chatUI, now time.Time) (lines []chatViewLine, targets []string) {
	if len(u.agents) == 0 {
		return nil, nil
	}
	mainState := "idle"
	if u.agentTurnActive() {
		mainState = "running"
	}
	lines = []chatViewLine{chatAgentStripLine(u.viewing == "", "main", mainState, "")}
	targets = []string{"main"}
	first := max(0, len(u.agents)-chatAgentStripRows)
	for index, agent := range u.agents {
		if agent.id == u.viewing && index < first {
			first = index
		}
	}
	end := min(len(u.agents), first+chatAgentStripRows)
	for _, agent := range u.agents[first:end] {
		detail := ""
		if agent.running() && !agent.started.IsZero() {
			detail = chatAgentElapsed(now.Sub(agent.started))
		}
		lines = append(lines, chatAgentStripLine(agent.id == u.viewing, agent.name, agent.status, detail))
		targets = append(targets, agent.id)
	}
	if hidden := len(u.agents) - (end - first); hidden > 0 {
		lines = append(lines, chatViewLine{text: fmt.Sprintf("    … %d more · /agents", hidden), kind: "queue"})
		targets = append(targets, "")
	}
	return lines, targets
}

func chatAgentStripLine(viewing bool, name, status, detail string) chatViewLine {
	marker, kind := "  ", "queue"
	if viewing {
		marker, kind = "› ", "completion"
	}
	icon := map[string]string{"running": "●", "starting": "●", "completed": "✓", "failed": "✗", "interrupted": "■", "stopped": "■"}[status]
	if icon == "" {
		icon = "○"
	}
	text := marker + icon + " " + textutil.SanitizeText(name)
	if status != "" {
		text += "  · " + status
	}
	if detail != "" {
		text += " · " + detail
	}
	return chatViewLine{text: text, kind: kind}
}

func chatAgentElapsed(elapsed time.Duration) string {
	if elapsed < time.Minute {
		return strconv.Itoa(int(elapsed.Seconds())) + "s"
	}
	return strconv.Itoa(int(elapsed.Minutes())) + "m"
}

// chatAgentComposerLabel names how a message reaches the viewed agent.
func chatAgentComposerLabel(agent *chatAgent) string {
	if agent.claude() {
		return " Message " + textutil.SanitizeText(agent.name) + " via the main agent "
	}
	return " Message " + textutil.SanitizeText(agent.name) + " "
}
