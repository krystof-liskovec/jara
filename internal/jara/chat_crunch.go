package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const chatCrunchUsage = "Usage: /crunch PROMPT | /crunch status | /crunch stop\n       /goalcrunch OBJECTIVE | /goalcrunch status | /goalcrunch stop\n"

func isChatCrunchCommand(line string) bool {
	return isChatCommand(line, "/crunch")
}

func isChatGoalCrunchCommand(line string) bool {
	return isChatCommand(line, "/goalcrunch")
}

func isChatCommand(line, command string) bool {
	return line == command || strings.HasPrefix(line, command+" ") || strings.HasPrefix(line, command+"\t")
}

func chatCrunchRequest(method string) bool {
	switch method {
	case "jara/crunch/create", "jara/crunch/status", "jara/crunch/stop":
		return true
	default:
		return false
	}
}

func (u *chatUI) runChatCrunchCommand(line string, goal bool) error {
	if !u.persistentSession {
		u.print("Crunch commands require a persistent Jara session.\n" + chatCrunchUsage + u.schedulePrompt())
		return nil
	}
	command := "/crunch"
	mode := "prompt"
	if goal {
		command = "/goalcrunch"
		mode = "goal"
	}
	args := strings.TrimSpace(strings.TrimPrefix(line, command))
	if args == "status" {
		return u.requestWithAction("jara/crunch/status", map[string]any{}, "status")
	}
	if args == "stop" {
		return u.requestWithAction("jara/crunch/stop", map[string]string{"threadId": u.thread}, "stop")
	}
	if args == "" {
		u.print(chatCrunchUsage + u.schedulePrompt())
		return nil
	}
	if !u.threadReady || u.thread == "" {
		u.print("No conversation is open. Use /resume ID or /new first.\n" + u.schedulePrompt())
		return nil
	}
	if goal {
		if utf8.RuneCountInString(args) > 4000 {
			u.print("Goal objectives must be 4,000 characters or fewer.\n" + chatCrunchUsage + u.schedulePrompt())
			return nil
		}
		return u.requestWithAction("jara/crunch/status", map[string]any{}, "goalcrunch-check:"+args)
	}
	if len(args) > maxBackgroundPrompt {
		u.print("Crunch prompt exceeds 64 KiB.\n" + u.schedulePrompt())
		return nil
	}
	return u.requestWithAction("jara/crunch/create", map[string]any{
		"threadId": u.thread,
		"mode":     mode,
		"prompt":   args,
	}, "create")
}

func (u *chatUI) acceptChatCrunchResponse(message appMessage, action string) error {
	var response struct {
		Job *chatCrunchJob `json:"job"`
	}
	if err := json.Unmarshal(message.Result, &response); err != nil {
		return err
	}
	var output string
	if strings.HasPrefix(action, "goalcrunch-check:") {
		if response.Job != nil && crunchJobActive(response.Job.Status) {
			u.print("\nA crunch task is already active. Use /crunch status or /crunch stop first.\n" + u.schedulePrompt())
			return nil
		}
		objective := strings.TrimPrefix(action, "goalcrunch-check:")
		return u.requestWithAction("thread/goal/set", map[string]any{
			"threadId":  u.thread,
			"origin":    "user",
			"objective": objective,
			"status":    "active",
		}, "goalcrunch:"+objective)
	}
	switch action {
	case "create":
		if response.Job == nil {
			return fmt.Errorf("crunch daemon returned no task")
		}
		kind := "Crunch started"
		if response.Job.Mode == "goal" {
			kind = "Goal crunch started"
		}
		output = fmt.Sprintf("%s (%s). Use /crunch status to check progress or /crunch stop to stop it.\n", kind, response.Job.ID)
	case "status":
		if response.Job == nil {
			output = "No crunch task is recorded.\n"
		} else {
			output = formatChatCrunchJob(*response.Job)
		}
	case "stop":
		if response.Job == nil {
			output = "No crunch task is running.\n"
		} else {
			output = "Stopping crunch task " + response.Job.ID + ".\n"
		}
	default:
		return fmt.Errorf("unknown crunch action %q", action)
	}
	u.print("\n" + output + u.schedulePrompt())
	return nil
}

func formatChatCrunchJob(job chatCrunchJob) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Crunch %s (%s)\nStatus: %s\n", job.ID, job.Mode, job.Status)
	fmt.Fprintf(&b, "Task: %s\n", compactScheduledPrompt(job.Prompt))
	if !job.ResetAt.IsZero() {
		fmt.Fprintf(&b, "Usage check: %s\n", job.ResetAt.Local().Format("2006-01-02 15:04 MST"))
	}
	if job.Error != "" {
		fmt.Fprintf(&b, "Details: %s\n", job.Error)
	}
	return b.String()
}
