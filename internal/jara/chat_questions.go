package jara

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

const (
	chatQuestionCancelledCode = -32000
	chatQuestionInvalidCode   = -32602
	chatQuestionLineWidth     = 40
)

type chatQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type chatQuestion struct {
	Header   string               `json:"header"`
	ID       string               `json:"id"`
	Question string               `json:"question"`
	IsOther  bool                 `json:"isOther"`
	IsSecret bool                 `json:"isSecret"`
	Options  []chatQuestionOption `json:"options"`
}

type chatQuestionParams struct {
	IsBlocking bool           `json:"isBlocking"`
	ItemID     string         `json:"itemId"`
	Questions  []chatQuestion `json:"questions"`
	ThreadID   string         `json:"threadId"`
	TurnID     string         `json:"turnId"`
}

type chatQuestionRequest struct {
	message appMessage
	params  chatQuestionParams
	answers map[string][]string
}

type chatQuestionAnswer struct {
	Answers []string `json:"answers"`
}

type chatQuestionResult struct {
	Answers map[string]chatQuestionAnswer `json:"answers"`
}

func (u *chatUI) handleQuestionRequest(m appMessage) error {
	var params chatQuestionParams
	if err := json.Unmarshal(m.Params, &params); err != nil || params.Questions == nil {
		message := "Malformed user-input request."
		if err != nil {
			message = "Malformed user-input request: " + err.Error()
		}
		u.print("\n" + message + " Request rejected.\n")
		return u.rejectQuestion(m, chatQuestionInvalidCode, message)
	}
	seen := map[string]bool{}
	for _, question := range params.Questions {
		if question.IsSecret {
			u.print("\nThis request includes a secret question. Jara cannot collect secret answers in an echoed terminal, so the request was rejected.\n")
			return u.rejectQuestion(m, chatQuestionInvalidCode, "Jara cannot collect secret answers in an echoed terminal")
		}
		if question.ID == "" || seen[question.ID] {
			message := "Question IDs must be present and unique."
			u.print("\n" + message + " Request rejected.\n")
			return u.rejectQuestion(m, chatQuestionInvalidCode, message)
		}
		seen[question.ID] = true
	}
	if len(params.Questions) == 0 {
		return u.c.reply(m.ID, chatQuestionResult{Answers: map[string]chatQuestionAnswer{}})
	}
	request := chatQuestionRequest{message: m, params: params, answers: map[string][]string{}}
	u.questionRequests = append(u.questionRequests, request)
	u.activityLabel = "Waiting for input"
	u.setAgentState("waiting")
	if len(u.questionRequests) > 1 {
		u.print("\n" + chatWrappedLines("", fmt.Sprintf("Another input request is queued (%d pending). Answer the current set first.", len(u.questionRequests)), chatQuestionLineWidth))
	} else {
		u.print("\n")
		renderChatQuestions(u)
	}
	return nil
}

func (u *chatUI) rejectQuestion(m appMessage, code int, message string) error {
	return u.c.encoder.Encode(map[string]any{
		"id":    m.ID,
		"error": map[string]any{"code": code, "message": message},
	})
}

func (u *chatUI) pendingQuestionCount() int {
	total := 0
	for _, request := range u.questionRequests {
		total += len(request.params.Questions) - len(request.answers)
	}
	return total
}

func renderChatQuestions(u *chatUI) {
	if len(u.questionRequests) == 0 {
		return
	}
	request := u.questionRequests[0]
	if len(u.questionRequests) > 1 {
		u.print(fmt.Sprintf("Input request 1/%d\n", len(u.questionRequests)))
	} else {
		u.print("Input requested\n")
	}
	for i, question := range request.params.Questions {
		if _, answered := request.answers[question.ID]; answered {
			u.print(fmt.Sprintf("Q%d answered\n", i+1))
			continue
		}
		if question.Header != "" {
			u.print(chatWrappedLines(fmt.Sprintf("Q%d: ", i+1), question.Header, chatQuestionLineWidth))
		} else {
			u.print(fmt.Sprintf("Q%d\n", i+1))
		}
		u.print(chatWrappedLines("  ", question.Question, chatQuestionLineWidth))
		if len(question.Options) == 0 {
			u.print(fmt.Sprintf("Free text: /answer %d <text>\n", i+1))
			continue
		}
		u.print("Options:\n")
		for optionIndex, option := range question.Options {
			label := option.Label
			if option.Description != "" {
				label += " — " + option.Description
			}
			prefix := fmt.Sprintf("  %d) ", optionIndex+1)
			u.print(chatWrappedLines(prefix, label, chatQuestionLineWidth))
		}
		u.print(fmt.Sprintf("Answer: /answer %d <number>\n", i+1))
		if question.IsOther {
			u.print(fmt.Sprintf("Other: /answer %d <text>\n", i+1))
		}
	}
	if len(u.questionRequests[0].params.Questions) > len(u.questionRequests[0].answers) {
		u.print(chatWrappedLines("", "/cancel rejects questions and stops the turn.", chatQuestionLineWidth))
	}
}

func chatWrappedLines(prefix, text string, width int) string {
	prefixWidth := uniseg.StringWidth(prefix)
	if width <= prefixWidth {
		width = prefixWidth + 1
	}
	line := prefix
	lineWidth := prefixWidth
	var lines []string
	for _, word := range strings.Fields(text) {
		spaceWidth := 0
		if lineWidth > prefixWidth {
			spaceWidth = 1
		}
		wordWidth := uniseg.StringWidth(word)
		if lineWidth+spaceWidth+wordWidth <= width {
			if spaceWidth > 0 {
				line += " "
				lineWidth++
			}
			line += word
			lineWidth += wordWidth
			continue
		}
		if lineWidth > prefixWidth {
			lines = append(lines, line)
			line, lineWidth = prefix, prefixWidth
		}
		if wordWidth <= width-prefixWidth {
			line += word
			lineWidth += wordWidth
			continue
		}
		graphemes := uniseg.NewGraphemes(word)
		for graphemes.Next() {
			part := graphemes.Str()
			partWidth := uniseg.StringWidth(part)
			if lineWidth+partWidth > width {
				lines = append(lines, line)
				line, lineWidth = prefix, prefixWidth
			}
			line += part
			lineWidth += partWidth
		}
	}
	if lineWidth > prefixWidth || len(lines) == 0 {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n"
}

func (u *chatUI) answerQuestion(line string) error {
	if len(u.questionRequests) == 0 {
		u.print("No questions are waiting for an answer.\n")
		return nil
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "/answer"))
	separator := strings.IndexFunc(rest, unicode.IsSpace)
	if separator <= 0 {
		u.print("Usage: /answer <question number> <option number or text>\n")
		return nil
	}
	questionNumber, err := strconv.Atoi(rest[:separator])
	if err != nil || questionNumber < 1 || questionNumber > len(u.questionRequests[0].params.Questions) {
		u.print("Question number is out of range. Use /answer <question number> <option number or text>.\n")
		return nil
	}
	answer := strings.TrimSpace(rest[separator:])
	if answer == "" {
		u.print("Add an option number or answer text after the question number.\n")
		return nil
	}
	request := &u.questionRequests[0]
	question := request.params.Questions[questionNumber-1]
	if _, answered := request.answers[question.ID]; answered {
		u.print(fmt.Sprintf("Question %d already has an answer.\n", questionNumber))
		return nil
	}
	value, err := resolveChatQuestionAnswer(question, answer)
	if err != nil {
		u.print(err.Error() + "\n")
		return nil
	}
	request.answers[question.ID] = []string{value}
	if len(request.answers) < len(request.params.Questions) {
		u.print(fmt.Sprintf("Saved answer for question %d of %d.\n", questionNumber, len(request.params.Questions)))
		renderChatQuestions(u)
		return nil
	}
	result := chatQuestionResult{Answers: make(map[string]chatQuestionAnswer, len(request.answers))}
	for id, values := range request.answers {
		result.Answers[id] = chatQuestionAnswer{Answers: values}
	}
	if err := u.c.reply(request.message.ID, result); err != nil {
		return err
	}
	u.questionRequests = u.questionRequests[1:]
	u.setAgentState(u.currentState())
	if len(u.questionRequests) == 0 && len(u.approvals) == 0 {
		u.activityLabel = "Thinking"
	}
	u.print("\nAnswers sent to the agent.\n")
	if len(u.questionRequests) > 0 {
		renderChatQuestions(u)
	} else if !u.busy {
		u.print("You> ")
	} else {
		u.print("Waiting for the agent to continue.\n")
	}
	return nil
}

func resolveChatQuestionAnswer(question chatQuestion, answer string) (string, error) {
	if len(question.Options) == 0 {
		return answer, nil
	}
	optionNumber, err := strconv.Atoi(answer)
	if err == nil && optionNumber >= 1 && optionNumber <= len(question.Options) {
		return question.Options[optionNumber-1].Label, nil
	}
	if question.IsOther {
		return answer, nil
	}
	return "", fmt.Errorf("Choose an option from 1 to %d", len(question.Options))
}

func (u *chatUI) cancelQuestionRequests(message string) error {
	for _, request := range u.questionRequests {
		if err := u.rejectQuestion(request.message, chatQuestionCancelledCode, message); err != nil {
			return err
		}
	}
	u.questionRequests = nil
	return nil
}
