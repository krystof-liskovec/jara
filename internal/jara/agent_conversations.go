package jara

import "context"

// routedConversations lists conversations kept by the agent router: Claude
// Code conversations and ones that moved between agents. Each is labeled with
// the agent that ran its latest segment.
type routedConversations struct{}

func (routedConversations) Name() string { return "Claude Code" }

func (routedConversations) ResumeCommand(id, _ string, _ []string) []string {
	if thread := loadRoutedThread(id); thread != nil {
		return thread.resumeCommand()
	}
	return nil
}

func (routedConversations) List(context.Context) ([]Conversation, error) {
	var out []Conversation
	for _, thread := range listRoutedThreads() {
		agent := agentDisplayName(thread.current().Agent)
		if len(thread.Segments) > 1 {
			agent += " (switched)"
		}
		out = append(out, Conversation{ID: thread.ID, Preview: thread.preview(), CWD: thread.CWD,
			Updated: thread.Updated, Created: thread.Created, Agent: agent})
	}
	return out, nil
}

// resumeCommand reopens the latest segment in its agent's own terminal UI.
// Claude Code finds sessions by working directory, so callers start it in
// the conversation's directory.
func (t *routedThread) resumeCommand() []string {
	segment := t.current()
	if segment.Agent == agentClaude {
		return []string{claudeBinary, "--resume", segment.BackendID}
	}
	command := []string{codexBinary, "resume", segment.BackendID, "--include-non-interactive"}
	if t.CWD != "" {
		command = append(command, "-C", t.CWD)
	}
	return command
}

// hideRoutedSegments drops Codex threads that are now part of a routed
// conversation, so each conversation is listed once.
func hideRoutedSegments(all []Conversation) []Conversation {
	hidden := map[string]bool{}
	for _, thread := range listRoutedThreads() {
		for _, segment := range thread.Segments {
			hidden[segment.BackendID] = true
		}
		hidden[thread.ID] = true
	}
	out := all[:0]
	for _, conversation := range all {
		if conversation.Agent == "Codex" && hidden[conversation.ID] {
			continue
		}
		out = append(out, conversation)
	}
	return out
}

// conversationRuntime picks the agent that resumes a saved conversation.
func conversationRuntime(agentLabel, id string) string {
	if thread := loadRoutedThread(id); thread != nil {
		return thread.current().Agent
	}
	if agentLabel == "Claude Code" {
		return agentClaude
	}
	return agentCodex
}
