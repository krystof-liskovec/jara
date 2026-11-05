package jara

import "time"

type chatReasoningSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
	u.printRole("progress", delta)
}

func (u *chatUI) finishReasoningSummary(itemID string, parts []chatReasoningSummaryPart) {
	if u.reasoningSummaryStreamed[itemID] {
		u.printRole("progress", "\n")
	} else {
		u.renderReasoningSummaryAt(parts, time.Now())
	}
	delete(u.reasoningSummaryStreamed, itemID)
	delete(u.reasoningSummaryIndexes, itemID)
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
