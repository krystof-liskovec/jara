package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const chatScheduleUsage = "Usage: /schedule in DURATION PROMPT | /schedule at RFC3339 PROMPT | /schedule list | /schedule cancel ID\nExamples: /schedule in 30m summarize the open issues; /schedule at 2026-11-06T09:00:00Z check the deployment\n"

func isChatScheduleCommand(line string) bool {
	return line == "/schedule" || strings.HasPrefix(line, "/schedule ") || strings.HasPrefix(line, "/schedule\t")
}

func chatScheduleRequest(method string) bool {
	switch method {
	case "jara/schedule/create", "jara/schedule/list", "jara/schedule/cancel":
		return true
	default:
		return false
	}
}

func (u *chatUI) runChatScheduleCommand(line string) error {
	if !u.persistentSession {
		u.print("Scheduled prompts require a persistent Jara session.\n" + chatScheduleUsage + u.schedulePrompt())
		return nil
	}
	args := strings.TrimSpace(strings.TrimPrefix(line, "/schedule"))
	if args == "" || args == "list" {
		return u.requestWithAction("jara/schedule/list", map[string]any{}, "list")
	}
	if strings.HasPrefix(args, "cancel ") || strings.HasPrefix(args, "cancel\t") {
		id := strings.TrimSpace(args[len("cancel"):])
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			u.print(chatScheduleUsage + u.schedulePrompt())
			return nil
		}
		return u.requestWithAction("jara/schedule/cancel", map[string]string{"id": id}, "cancel")
	}
	mode, rest, ok := takeChatScheduleToken(args)
	if !ok {
		u.print(chatScheduleUsage + u.schedulePrompt())
		return nil
	}
	value, prompt, ok := takeChatScheduleToken(rest)
	if !ok || prompt == "" {
		u.print(chatScheduleUsage + u.schedulePrompt())
		return nil
	}
	if !u.threadReady || u.thread == "" {
		u.print("No conversation is open. Use /resume ID or /new first.\n" + u.schedulePrompt())
		return nil
	}
	var runAt time.Time
	switch mode {
	case "in":
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 || duration > 365*24*time.Hour {
			u.print("Schedule duration must be positive and no longer than one year.\n" + chatScheduleUsage + u.schedulePrompt())
			return nil
		}
		runAt = time.Now().Add(duration)
	case "at":
		var err error
		runAt, err = time.Parse(time.RFC3339, value)
		if err != nil {
			u.print("Schedule time must use RFC3339, for example 2026-11-06T09:00:00Z.\n" + u.schedulePrompt())
			return nil
		}
	default:
		u.print(chatScheduleUsage + u.schedulePrompt())
		return nil
	}
	if len(prompt) > maxBackgroundPrompt {
		u.print("Scheduled prompt exceeds 64 KiB.\n" + u.schedulePrompt())
		return nil
	}
	return u.requestWithAction("jara/schedule/create", map[string]any{
		"threadId": u.thread,
		"prompt":   prompt,
		"runAt":    runAt,
	}, "create")
}

func takeChatScheduleToken(value string) (string, string, bool) {
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	if value == "" {
		return "", "", false
	}
	for index, r := range value {
		if unicode.IsSpace(r) {
			return value[:index], strings.TrimSpace(value[index:]), true
		}
	}
	return value, "", false
}

func (u *chatUI) schedulePrompt() string {
	if u.composing {
		return "Compose> "
	}
	return "You> "
}

func (u *chatUI) acceptChatScheduleResponse(message appMessage, action string) error {
	var response struct {
		Schedule  chatSchedule   `json:"schedule"`
		Schedules []chatSchedule `json:"schedules"`
	}
	if err := json.Unmarshal(message.Result, &response); err != nil {
		return err
	}
	var output string
	switch action {
	case "create":
		output = fmt.Sprintf("Scheduled %s for %s.\nPrompt: %s\n", response.Schedule.ID, response.Schedule.RunAt.Local().Format("2006-01-02 15:04 MST"), compactScheduledPrompt(response.Schedule.Prompt))
	case "cancel":
		output = "Canceled scheduled prompt " + response.Schedule.ID + ".\n"
	case "list":
		if len(response.Schedules) == 0 {
			output = "No scheduled prompts.\n"
		} else {
			var b strings.Builder
			for _, job := range response.Schedules {
				fmt.Fprintf(&b, "%s  %s  %s  %s\n", job.ID, job.Status, job.RunAt.Local().Format("2006-01-02 15:04 MST"), compactScheduledPrompt(job.Prompt))
				if job.Error != "" {
					fmt.Fprintf(&b, "  Error: %s\n", job.Error)
				}
			}
			output = b.String()
		}
	default:
		return fmt.Errorf("unknown scheduled prompt action %q", action)
	}
	u.print("\n" + output + u.schedulePrompt())
	return nil
}

func compactScheduledPrompt(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	runes := []rune(prompt)
	if len(runes) > 96 {
		return string(runes[:93]) + "..."
	}
	return prompt
}
