//go:build linux || darwin

package jara

import (
	"strings"
	"time"
)

// chatTerminalAgents holds the terminal's view of a subagent: its rendered
// transcript and the main transcript's scroll position to return to.
type chatTerminalAgents struct {
	view        *chatTerminalOutput
	viewID      string
	viewVersion int
	mainScroll  int
	signature   int // changes when any agent's status or transcript does
}

// showAgent switches the transcript to an agent ("" for the main agent),
// keeping the main transcript's scroll position for the way back.
func (t *chatTerminal) showAgent(change func()) {
	before := t.u.viewing
	change()
	after := t.u.viewing
	if before == after {
		return
	}
	if before == "" {
		t.agents.mainScroll = t.scrollOffset
	}
	t.scrollOffset = 0
	if after == "" {
		t.scrollOffset = t.agents.mainScroll
	}
	t.agents.viewID, t.agents.viewVersion = "", -1
	t.dirty = true
}

// applyAgentViewRequest shows the agent /agents N asked for.
func (t *chatTerminal) applyAgentViewRequest() {
	request := t.u.agentViewRequest
	if request == "" {
		return
	}
	t.u.agentViewRequest = ""
	t.showAgent(func() { t.u.showChatAgent(request) })
}

// agentTranscriptLines renders the viewed agent's transcript, or returns nil
// while the main agent is shown.
func (t *chatTerminal) agentTranscriptLines(width int) []chatViewLine {
	agent := t.u.viewedAgent()
	if agent == nil {
		return nil
	}
	if t.agents.view == nil {
		t.agents.view = &chatTerminalOutput{}
	}
	if t.agents.viewID != agent.id || t.agents.viewVersion != agent.version {
		renderChatAgentTranscript(t.agents.view, agent)
		t.agents.viewID, t.agents.viewVersion = agent.id, agent.version
	}
	lines := t.agents.view.transcriptLines(width)
	if len(lines) == 0 {
		return []chatViewLine{{kind: "blank"}}
	}
	return lines
}

// tickAgents refreshes the viewed agent and redraws when any agent changed.
func (t *chatTerminal) tickAgents(now time.Time) error {
	u := t.u
	if err := u.pollChatAgent(now); err != nil {
		return err
	}
	signature := len(u.agents)
	for _, agent := range u.agents {
		signature = signature*31 + agent.version
	}
	if signature != t.agents.signature {
		t.agents.signature, t.dirty = signature, true
	}
	return nil
}

// escapeAgent returns from a subagent to the main agent when Escape meets an
// empty input box.
func (t *chatTerminal) escapeAgent() bool {
	if t.u.viewing == "" || strings.TrimSpace(t.editor.Text()) != "" || t.queueEditor.active() {
		return false
	}
	t.showAgent(func() { t.u.showChatAgent("") })
	return true
}

// submitToAgent sends a typed message to the viewed subagent. Slash commands
// still go to the conversation.
func (t *chatTerminal) submitToAgent(value string) (bool, error) {
	agent := t.u.viewedAgent()
	line := strings.TrimSpace(value)
	if agent == nil || line == "" || strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//") {
		return false, nil
	}
	if strings.HasPrefix(line, "//") {
		value = strings.TrimPrefix(strings.TrimSpace(value), "/")
	}
	t.dirty = true
	return true, t.u.messageChatAgent(agent, value)
}
