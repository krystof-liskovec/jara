package jara

import (
	"encoding/json"
	"strings"
	"time"
)

const chatActivitySummaryLimit = 2048

type chatReasoningSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (p *chatReasoningSummaryPart) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		p.Type = "summary_text"
		p.Text = text
		return nil
	}
	type summaryPart chatReasoningSummaryPart
	var decoded summaryPart
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = chatReasoningSummaryPart(decoded)
	return nil
}

func (u *chatUI) streamReasoningSummary(itemID string, index int, delta string) {
	if itemID == "" || delta == "" {
		return
	}
	if u.reasoningSummaryStreamed == nil {
		u.reasoningSummaryStreamed = map[string]bool{}
		u.reasoningSummaryIndexes = map[string]int{}
	}
	if previous, ok := u.reasoningSummaryIndexes[itemID]; ok && previous != index {
		u.printRole("progress", "\n")
	}
	u.reasoningSummaryIndexes[itemID] = index
	u.reasoningSummaryStreamed[itemID] = true
	u.appendChatActivitySummary(itemID, index, delta)
	if !chatUIIsInteractive(u) {
		u.printRole("progress", delta)
	}
}

func (u *chatUI) finishReasoningSummary(itemID string, parts []chatReasoningSummaryPart) {
	if u.reasoningSummaryStreamed[itemID] {
		if !chatUIIsInteractive(u) {
			u.printRole("progress", "\n")
		}
	} else {
		u.setChatActivitySummary(itemID, parts)
		if !chatUIIsInteractive(u) {
			u.renderReasoningSummaryAt(parts, time.Now())
		}
	}
	delete(u.reasoningSummaryStreamed, itemID)
	delete(u.reasoningSummaryIndexes, itemID)
}

func (u *chatUI) appendChatActivitySummary(itemID string, index int, delta string) {
	if u.activitySummaryItemID != itemID || u.activitySummaryIndex != index {
		u.activitySummaryItemID = itemID
		u.activitySummaryIndex = index
		u.activitySummaryText = ""
	}
	remaining := chatActivitySummaryLimit - len([]rune(u.activitySummaryText))
	if remaining <= 0 {
		return
	}
	deltaRunes := []rune(delta)
	if len(deltaRunes) > remaining {
		deltaRunes = deltaRunes[:remaining]
	}
	u.activitySummaryText += string(deltaRunes)
}

func (u *chatUI) setChatActivitySummary(itemID string, parts []chatReasoningSummaryPart) {
	u.clearChatActivitySummary()
	for index, part := range parts {
		if (part.Type == "" || part.Type == "summary_text") && strings.TrimSpace(part.Text) != "" {
			u.appendChatActivitySummary(itemID, index, part.Text)
		}
	}
}

func (u *chatUI) setActivityLabel(label string) {
	u.activityLabel = label
	u.clearChatActivitySummary()
}

func (u *chatUI) clearChatActivitySummary() {
	u.activitySummaryItemID = ""
	u.activitySummaryIndex = 0
	u.activitySummaryText = ""
}

func chatActivitySummaryLabel(text string) string {
	text = strings.Join(strings.Fields(chatText(text)), " ")
	text = strings.Trim(text, "*_` ")
	runes := []rune(text)
	if len(runes) > 240 {
		text = string(runes[:239]) + "…"
	}
	return text
}

func (u *chatUI) finishAllReasoningSummaries() {
	for itemID := range u.reasoningSummaryStreamed {
		u.finishReasoningSummary(itemID, nil)
	}
}

func (u *chatUI) renderReasoningSummaryAt(parts []chatReasoningSummaryPart, at time.Time) {
	for _, part := range parts {
		if part.Type != "" && part.Type != "summary_text" || part.Text == "" {
			continue
		}
		u.printRoleAt("progress", part.Text+"\n", at)
	}
}
