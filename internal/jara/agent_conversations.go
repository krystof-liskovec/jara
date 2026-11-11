package jara

import (
	"context"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentrouter"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// routedConversations lists conversations kept by the agent router: Claude
// Code conversations and ones that moved between agents. Each is labeled with
// the agent that ran its latest segment.
type routedConversations struct{}

func (routedConversations) Name() string { return "Claude Code" }

func (routedConversations) ResumeCommand(id, _ string, _ []string) []string {
	if thread := agentrouter.LoadThread(id); thread != nil {
		return thread.ResumeCommand()
	}
	return nil
}

func (routedConversations) List(context.Context) ([]Conversation, error) {
	var out []Conversation
	for _, thread := range agentrouter.ListThreads() {
		agent := protocol.AgentDisplayName(thread.Current().Agent)
		if len(thread.Segments) > 1 {
			agent += " (switched)"
		}
		out = append(out, Conversation{ID: thread.ID, Preview: thread.Preview(), CWD: thread.CWD,
			Updated: thread.Updated, Created: thread.Created, Agent: agent})
	}
	return out, nil
}

// hideRoutedSegments drops Codex threads that are now part of a routed
// conversation, so each conversation is listed once.
func hideRoutedSegments(all []Conversation) []Conversation {
	hidden := map[string]bool{}
	for _, thread := range agentrouter.ListThreads() {
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
	if thread := agentrouter.LoadThread(id); thread != nil {
		return thread.Current().Agent
	}
	if agentLabel == "Claude Code" {
		return protocol.AgentClaude
	}
	return protocol.AgentCodex
}
