package agentrouter

import (
	"fmt"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// A Claude Code subagent started with run_in_background keeps working after
// the main agent's turn ends. When it finishes, the main agent should take
// up its result. Claude Code may start that turn by itself: the router
// follows main-agent output that arrives while no turn is open as a turn of
// its own, so the reply is shown instead of dropped. When nothing starts
// shortly after the subagent's task notification, the router tells the main
// agent itself, so the conversation continues without the user.

// SubagentWakeDelay is how long the router waits for the main agent to
// continue by itself after a background subagent finished.
var SubagentWakeDelay = 4 * time.Second

const claudeSelfStartedTurn = "(Claude Code continued on its own)"

// followSelfStartedTurn opens a turn for main-agent output that arrives
// while none is open.
func (s *claudeSession) followSelfStartedTurn(line claudeLine) {
	if s.turnID != "" || s.cmd == nil {
		return
	}
	if line.Type != "assistant" && (line.Type != "stream_event" || line.Event.Type != "message_start") {
		return
	}
	text := s.wakeText
	if text == "" {
		text = claudeSelfStartedTurn
	}
	s.wakeText, s.wakePrompt = "", ""
	s.thread.beginTurn(text, protocol.AgentClaude)
	s.openTurn(nil)
}

// noteBackgroundFinished arranges for the main agent to continue once one of
// its background subagents finished while it was idle.
func (s *claudeSession) noteBackgroundFinished(agent *claudeSubagent) {
	if s.turnID != "" || agent.parentThread != s.thread.ID || s.cmd == nil {
		return // a running turn gets the notification from Claude Code itself
	}
	state := map[string]string{"completed": "finished", "errored": "failed", "shutdown": "was stopped"}[agent.status]
	if state == "" {
		state = "finished"
	}
	s.wakeText = fmt.Sprintf("(background agent %q %s)", agent.name, state)
	s.wakePrompt = fmt.Sprintf("Your background agent %q %s. Read its result and continue the task; "+
		"if other background agents are still running, say so and wait for them.", agent.name, state)
	events, process := s.r.events, s.cmd
	if events == nil {
		return
	}
	time.AfterFunc(SubagentWakeDelay, func() { events <- routerEvent{source: "claude-wake", session: s, process: process} })
}

// wakeIfIdle tells the main agent about a finished background subagent when
// Claude Code did not continue by itself.
func (s *claudeSession) wakeIfIdle() {
	if s.turnID != "" || s.wakeText == "" || s.cmd == nil {
		return
	}
	text, prompt := s.wakeText, s.wakePrompt
	s.wakeText, s.wakePrompt = "", ""
	s.thread.beginTurn(text, protocol.AgentClaude)
	_ = s.startTurn(nil, prompt)
}
