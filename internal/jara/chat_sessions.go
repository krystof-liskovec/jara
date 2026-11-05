package jara

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	chatSessionPageSize   = 100
	chatSessionMaxPages   = 4
	chatSessionMaxShown   = 8
	chatSessionMaxQuery   = 128
	chatSessionTitleRunes = 72
)

// chatSessionBrowser holds the exact choice set shown to the user. Numeric
// resume selections always refer to this frozen set, never a refreshed list.
type chatSessionBrowser struct {
	query, cwd, provider string
	cursor               string
	pages                int
	matches              []Conversation
	seen                 map[string]struct{}
	complete             bool
	incomplete           bool
	more                 bool
	failure              string
}

func newChatSessionBrowser(query, cwd, provider string) (*chatSessionBrowser, error) {
	runtime, err := resolveChatRuntime(provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cwd) == "" {
		return nil, fmt.Errorf("cannot list saved sessions without a working directory")
	}
	query = strings.TrimSpace(chatText(query))
	if len(query) > chatSessionMaxQuery {
		return nil, fmt.Errorf("session search query must be %d bytes or fewer", chatSessionMaxQuery)
	}
	return &chatSessionBrowser{
		query:    query,
		cwd:      cwd,
		provider: runtime.name,
		seen:     map[string]struct{}{},
	}, nil
}

// requestParams builds the documented thread/list filter for this provider's
// active app-server connection. cwd is an exact path filter in that protocol.
func (b *chatSessionBrowser) requestParams() map[string]any {
	params := map[string]any{
		"cwd":         b.cwd,
		"limit":       chatSessionPageSize,
		"sortKey":     "updated_at",
		"sourceKinds": []string{"cli", "vscode", "exec", "appServer"},
	}
	if b.cursor != "" {
		params["cursor"] = b.cursor
	}
	return params
}

func (u *chatUI) startChatSessionSearch(query string) error {
	if !u.connected {
		return fmt.Errorf("wait for the Codex connection before listing sessions")
	}
	browser, err := newChatSessionBrowser(query, u.cwd, defaultChatRuntime)
	if err != nil {
		return err
	}
	for id, pending := range u.pending {
		if pending.method == "thread/list" {
			delete(u.pending, id)
		}
	}
	if u.pending == nil {
		u.pending = map[string]chatPending{}
	}
	u.sessions = browser
	if err := u.request("thread/list", browser.requestParams()); err != nil {
		u.sessions = nil
		return err
	}
	return nil
}

func (u *chatUI) acceptChatSessionPage(raw json.RawMessage) error {
	if u.sessions == nil {
		return nil
	}
	more, err := u.sessions.acceptPage(raw)
	if err != nil {
		u.finishChatSessionSearch(err)
		return nil
	}
	if more {
		if err := u.request("thread/list", u.sessions.requestParams()); err != nil {
			u.finishChatSessionSearch(err)
		}
		return nil
	}
	if u.resumeLatest {
		u.resumeLatest = false
		if len(u.sessions.matches) == 0 {
			u.sessionPickerRequested = false
			u.print("\nNo saved conversations found; use /new to start one.\n")
			if !u.busy {
				u.print("You> ")
			}
			return nil
		}
		return u.resumeChatSessionChoice("1")
	}
	u.printChatSessionResults()
	return nil
}

func (u *chatUI) failChatSessionRequest(err error) bool {
	if u.sessions == nil {
		return false
	}
	for id, pending := range u.pending {
		if pending.method == "thread/list" {
			delete(u.pending, id)
		}
	}
	u.finishChatSessionSearch(err)
	return true
}

func (u *chatUI) expireChatSessionRequest(id string) bool {
	pending, ok := u.pending[id]
	if !ok || pending.method != "thread/list" {
		return false
	}
	delete(u.pending, id)
	return u.failChatSessionRequest(fmt.Errorf("Codex app-server timed out during thread/list"))
}

func (u *chatUI) finishChatSessionSearch(err error) {
	if u.sessions == nil {
		u.sessionPickerRequested = false
		return
	}
	u.sessionPickerRequested = false
	u.print("\n" + u.sessions.fail(err))
	if !u.busy {
		u.print("You> ")
	}
}

func (u *chatUI) printChatSessionResults() {
	if u.sessions == nil {
		return
	}
	if u.sessionPickerRequested && chatUIIsInteractive(u) {
		u.sessionPickerRequested = false
		return
	}
	u.sessionPickerRequested = false
	u.print("\n" + u.sessions.render())
	if !u.busy {
		u.print("You> ")
	}
}

func (u *chatUI) resumeChatSessionChoice(number string) error {
	if u.sessions == nil {
		return fmt.Errorf("run /sessions first to choose a saved conversation")
	}
	id, err := u.sessions.resumeChoice(number)
	if err != nil {
		return err
	}
	return u.open(id)
}

func numericChatSessionChoice(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// acceptPage consumes one thread/list response and reports whether another
// request should be queued. Results are scanned in server recency order.
func (b *chatSessionBrowser) acceptPage(raw json.RawMessage) (bool, error) {
	if b.complete {
		return false, fmt.Errorf("session search is already complete")
	}
	var page struct {
		Data       []Conversation `json:"data"`
		NextCursor *string        `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return false, fmt.Errorf("decode saved sessions: %w", err)
	}
	if page.Data == nil {
		return false, fmt.Errorf("decode saved sessions: response is missing data")
	}
	b.pages++
	matchedBeyondLimit := false
	for _, conversation := range page.Data {
		if conversation.ID == "" || conversation.CWD != b.cwd {
			continue
		}
		if _, duplicate := b.seen[conversation.ID]; duplicate {
			continue
		}
		b.seen[conversation.ID] = struct{}{}
		conversation.Agent = providerLabel(b.provider)
		if !b.matchesQuery(conversation) {
			continue
		}
		if len(b.matches) < chatSessionMaxShown {
			b.matches = append(b.matches, conversation)
		} else {
			matchedBeyondLimit = true
		}
	}
	sort.SliceStable(b.matches, func(i, j int) bool {
		left, right := b.matches[i], b.matches[j]
		leftRecent, rightRecent := max(left.Updated, left.Created), max(right.Updated, right.Created)
		if leftRecent != rightRecent {
			return leftRecent > rightRecent
		}
		return left.ID < right.ID
	})
	if page.NextCursor == nil || *page.NextCursor == "" {
		b.complete = true
		b.more = matchedBeyondLimit
		return false, nil
	}
	if matchedBeyondLimit || len(b.matches) >= chatSessionMaxShown {
		b.more = true
		b.complete = true
		return false, nil
	}
	if *page.NextCursor == b.cursor {
		return false, fmt.Errorf("Codex returned a repeated history cursor")
	}
	if b.pages >= chatSessionMaxPages {
		b.incomplete = true
		b.complete = true
		return false, nil
	}
	b.cursor = *page.NextCursor
	return true, nil
}

func (b *chatSessionBrowser) matchesQuery(conversation Conversation) bool {
	if b.query == "" {
		return true
	}
	query := strings.ToLower(b.query)
	return strings.Contains(strings.ToLower(conversation.Name), query) ||
		strings.Contains(strings.ToLower(conversation.Preview), query) ||
		strings.Contains(strings.ToLower(conversation.ID), query)
}

// fail marks an app-server error or timeout as an incomplete search while
// preserving any matches already received.
func (b *chatSessionBrowser) fail(err error) string {
	b.complete = true
	b.incomplete = true
	if err != nil {
		b.failure = chatText(err.Error())
	}
	return b.render()
}

func (b *chatSessionBrowser) resumeChoice(value string) (string, error) {
	if !b.complete {
		return "", fmt.Errorf("session search is still loading; wait for results before choosing a number")
	}
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number < 1 || number > len(b.matches) {
		if len(b.matches) == 0 {
			return "", fmt.Errorf("no displayed sessions to resume; run /sessions first")
		}
		return "", fmt.Errorf("choose a displayed session number from 1 to %d", len(b.matches))
	}
	return b.matches[number-1].ID, nil
}

func (b *chatSessionBrowser) render() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Saved %s sessions in this folder", providerLabel(b.provider))
	if b.query != "" {
		fmt.Fprintf(&out, " matching %q", compactChatSessionText(b.query, chatSessionMaxQuery))
	}
	out.WriteString("\n")
	for i, conversation := range b.matches {
		title := conversation.Name
		if strings.TrimSpace(title) == "" {
			title = conversation.Preview
		}
		if strings.TrimSpace(title) == "" {
			title = conversation.ID
		}
		fmt.Fprintf(&out, "%d. %s\n", i+1, compactChatSessionText(title, chatSessionTitleRunes))
	}
	if len(b.matches) == 0 {
		if b.incomplete {
			out.WriteString("No matches in the sessions scanned so far.\n")
		} else if b.query == "" {
			out.WriteString("No saved sessions found.\n")
		} else {
			out.WriteString("No matching sessions found.\n")
		}
	}
	if b.more {
		out.WriteString("Showing the first 8 matches. Add a query to narrow results.\n")
	}
	if b.incomplete {
		out.WriteString("History search is incomplete; add a query and try again.\n")
	}
	if b.failure != "" {
		fmt.Fprintf(&out, "History lookup failed: %s\n", compactChatSessionText(b.failure, 160))
	}
	if len(b.matches) > 0 {
		out.WriteString("Use /resume N to open a listed session.\n")
	}
	return out.String()
}

func providerLabel(provider string) string {
	if provider == "codex" {
		return "Codex"
	}
	return provider
}

func compactChatSessionText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(chatText(value)), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}
