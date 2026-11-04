package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func validateChatInterface(value string) error {
	if value != "" && value != "codex" && value != "jara" {
		return fmt.Errorf("--interface must be codex or jara")
	}
	return nil
}
func chatServerArgs(args []string) ([]string, error) {
	out := []string{"app-server", "--listen", "stdio://"}
	keys := map[string]string{"--model": "model", "-m": "model", "--sandbox": "sandbox_mode", "-s": "sandbox_mode", "--ask-for-approval": "approval_policy", "-a": "approval_policy"}
	for i := 0; i < len(args); i++ {
		flag, value, has := strings.Cut(args[i], "=")
		if !has && (flag == "--dangerously-bypass-approvals-and-sandbox" || flag == "--yolo") {
			out = append(out, "-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`)
			continue
		}
		if flag == "--search" {
			out = append(out, "-c", `web_search="live"`)
			continue
		}
		key, mapped := keys[flag]
		passthrough := flag == "-c" || flag == "--config" || flag == "--enable" || flag == "--disable"
		if !mapped && !passthrough {
			return nil, fmt.Errorf("Codex argument %q is unsupported by --interface jara; use --interface codex or a -c configuration override", flag)
		}
		if !has {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("missing value for %s", flag)
			}
			value = args[i]
		}
		if mapped {
			encoded, _ := json.Marshal(value)
			out = append(out, "-c", key+"="+string(encoded))
		} else {
			out = append(out, flag, value)
		}
	}
	return out, nil
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
	args, err := chatServerArgs(q.CodexArgs)
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
	connection, err := startChatServer("/usr/local/bin/codex", args, os.Stderr)
	if err != nil {
		return err
	}
	defer connection.close()
	return chatLoop(connection, os.Stdin, os.Stdout, cwd, id)
}
