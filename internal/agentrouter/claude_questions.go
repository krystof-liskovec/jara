package agentrouter

import (
	"encoding/json"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/textutil"
)

// questionNotePrefix marks the user's note among a question's answers, as
// Codex's user-input answers carry it.
const questionNotePrefix = "user_note: "

// claudeQuestion is one entry of Claude Code's AskUserQuestion tool input.
type claudeQuestion struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
	MultiSelect bool `json:"multiSelect"`
}

// askUserQuestion forwards Claude Code's AskUserQuestion to the UI as a
// Codex-style user-input request. Each question can also take the user's own
// text, as Claude Code's own picker allows.
func (s *claudeSession) askUserQuestion(requestID, toolUseID string, input map[string]any) {
	raw, _ := json.Marshal(input["questions"])
	var questions []claudeQuestion
	if json.Unmarshal(raw, &questions) != nil || len(questions) == 0 {
		s.respondPermission(requestID, nil, false, "Ask the question in your reply instead; the user answers in their next message.")
		return
	}
	texts := make([]string, len(questions))
	uiQuestions := make([]map[string]any, len(questions))
	for i, question := range questions {
		texts[i] = question.Question
		options := make([]map[string]string, len(question.Options))
		for j, option := range question.Options {
			options[j] = map[string]string{"label": option.Label, "description": option.Description}
		}
		uiQuestions[i] = map[string]any{
			"id": strconv.Itoa(i), "header": question.Header, "question": question.Question,
			"options": options, "isOther": true, "isSecret": false, "multiSelect": question.MultiSelect,
		}
	}
	s.r.sequence++
	uiID := strconv.Quote("claude-question-" + strconv.Itoa(s.r.sequence))
	s.approvals[uiID] = claudeApproval{requestID: requestID, input: input, questions: texts}
	s.r.approvals[uiID] = s
	s.r.send(map[string]any{"id": json.RawMessage(uiID), "method": "item/tool/requestUserInput", "params": map[string]any{
		"threadId": s.thread.ID, "turnId": s.turnID, "itemId": textutil.FirstNonEmpty(toolUseID, s.nextItemID("tool")),
		"isBlocking": true, "questions": uiQuestions,
	}})
}

// answerQuestion returns the user's answers to Claude Code as the tool's
// updated input: answers keyed by question text, with several choices joined
// by ", ", and the user's notes as annotations keyed the same way. A rejected
// request (an error reply) tells Claude the user declined.
func (s *claudeSession) answerQuestion(approval claudeApproval, result json.RawMessage) {
	var reply struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if json.Unmarshal(result, &reply) != nil || len(reply.Answers) == 0 {
		s.respondPermission(approval.requestID, nil, false, "The user dismissed the question without answering.")
		return
	}
	answers := map[string]string{}
	annotations := map[string]map[string]string{}
	for i, text := range approval.questions {
		answer, ok := reply.Answers[strconv.Itoa(i)]
		if !ok {
			continue
		}
		var chosen, notes []string
		for _, value := range answer.Answers {
			if note, isNote := strings.CutPrefix(value, questionNotePrefix); isNote {
				notes = append(notes, note)
			} else {
				chosen = append(chosen, value)
			}
		}
		answers[text] = strings.Join(chosen, ", ")
		if len(notes) > 0 {
			annotations[text] = map[string]string{"notes": strings.Join(notes, "\n")}
		}
	}
	input := make(map[string]any, len(approval.input)+2)
	for key, value := range approval.input {
		input[key] = value
	}
	input["answers"] = answers
	if len(annotations) > 0 {
		input["annotations"] = annotations
	}
	s.respondPermission(approval.requestID, input, true, "")
}
