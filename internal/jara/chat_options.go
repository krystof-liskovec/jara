package jara

import (
	"fmt"
	"os"
	"sort"
)

func validateChatInterface(value string) error {
	if value != "" && value != "codex" && value != "jara" {
		return fmt.Errorf("--interface must be codex or jara")
	}
	return nil
}
func chatServerArgs(args []string) ([]string, error) {
	provider, err := resolveChatRuntime(defaultChatRuntime)
	if err != nil {
		return nil, err
	}
	return provider.serverArgs(provider, args)
}
func chatTerminalCommand(q Request, args []string) []string {
	q.CodexArgs = args
	return []string{"/usr/local/bin/jara", "_chat", token(q)}
}
func chatResumeID(q Request, cwd string) (string, error) {
	if q.Conversation != "" {
		return q.Conversation, nil
	}
	if !q.Resume && !q.ResumeLatest {
		return "", nil
	}
	threads, err := loadConversations()
	if err != nil {
		return "", err
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].Updated > threads[j].Updated })
	var items []menuItem
	for _, thread := range threads {
		if thread.CWD == cwd {
			title := thread.Name
			if title == "" {
				title = thread.Preview
			}
			items = append(items, menuItem{Title: title, Detail: thread.ID, Action: thread.ID})
		}
	}
	if len(items) == 0 {
		return "", fmt.Errorf("no saved conversations in %s; use jara new --interface jara", cwd)
	}
	if q.ResumeLatest {
		return items[0].Action, nil
	}
	index, err := selectMenu("Resume conversation", items)
	if err != nil {
		return "", err
	}
	return items[index].Action, nil
}
func runJaraChat(q Request) error {
	provider, err := resolveChatRuntime(defaultChatRuntime)
	if err != nil {
		return err
	}
	binary, args, err := provider.command(q.CodexArgs)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	id, err := chatResumeID(q, cwd)
	if err != nil {
		return err
	}
	publishChatAgentState("starting")
	defer clearChatAgentState()
	connection, err := startChatServer(binary, args, os.Stderr)
	if err != nil {
		return err
	}
	defer connection.close()
	return chatLoop(connection, os.Stdin, os.Stdout, cwd, id)
}
