package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func chatSessionPageJSON(t *testing.T, items []Conversation, next string) json.RawMessage {
	t.Helper()
	if items == nil {
		items = []Conversation{}
	}
	page := struct {
		Data       []Conversation `json:"data"`
		NextCursor *string        `json:"nextCursor,omitempty"`
	}{Data: items}
	if next != "" {
		page.NextCursor = &next
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestChatSessionBrowserRequestIsScopedAndPaged(t *testing.T) {
	browser, err := newChatSessionBrowser("", "/work/project", "codex")
	if err != nil {
		t.Fatal(err)
	}
	params := browser.requestParams()
	if params["cwd"] != "/work/project" || params["limit"] != chatSessionPageSize || params["sortKey"] != "updated_at" {
		t.Fatalf("unexpected session request: %#v", params)
	}
	if _, ok := params["cursor"]; ok {
		t.Fatalf("initial page should not have cursor: %#v", params)
	}
	more, err := browser.acceptPage(chatSessionPageJSON(t, nil, "page-2"))
	if err != nil || !more {
		t.Fatalf("next page: more=%v err=%v", more, err)
	}
	if browser.requestParams()["cursor"] != "page-2" {
		t.Fatalf("next cursor was not retained: %#v", browser.requestParams())
	}
}

func TestChatSessionBrowserRejectsMissingData(t *testing.T) {
	for _, raw := range []string{`{}`, `{"data":null}`} {
		browser, err := newChatSessionBrowser("", "/work/project", "codex")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := browser.acceptPage(json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), "missing data") {
			t.Errorf("invalid session page %s: error=%v", raw, err)
		}
	}
}

func TestChatSessionBrowserFiltersAndFreezesNumberedChoices(t *testing.T) {
	browser, err := newChatSessionBrowser("build plan", "/work/project", "")
	if err != nil {
		t.Fatal(err)
	}
	items := []Conversation{
		{ID: "wrong-folder", Name: "Build plan", CWD: "/work/other"},
		{ID: "miss", Name: "Unrelated", Preview: "ordinary work", CWD: "/work/project"},
		{ID: "first", Name: "Weekly notes", Preview: "BUILD PLAN tasks", CWD: "/work/project"},
		{ID: "second", Name: "Build plan", CWD: "/work/project"},
	}
	if _, err = browser.resumeChoice("1"); err == nil || !strings.Contains(err.Error(), "still loading") {
		t.Fatalf("selection should wait for results: %v", err)
	}
	more, err := browser.acceptPage(chatSessionPageJSON(t, items, ""))
	if err != nil || more {
		t.Fatalf("final page: more=%v err=%v", more, err)
	}
	if len(browser.matches) != 2 || browser.matches[0].ID != "first" || browser.matches[1].ID != "second" {
		t.Fatalf("unexpected matches: %+v", browser.matches)
	}
	if id, err := browser.resumeChoice("2"); err != nil || id != "second" {
		t.Fatalf("resume choice: id=%q err=%v", id, err)
	}
	if _, err := browser.resumeChoice("3"); err == nil || !strings.Contains(err.Error(), "displayed session number") {
		t.Fatalf("out-of-range selection should be rejected: %v", err)
	}
	if _, err := browser.resumeChoice("12345678-1234-1234-1234-123456789abc"); err == nil {
		t.Fatal("ID must not be accepted as a numeric selection")
	}
}

func TestChatSessionBrowserBoundsDisplayAndScan(t *testing.T) {
	browser, err := newChatSessionBrowser("", "/work/project", "codex")
	if err != nil {
		t.Fatal(err)
	}
	var items []Conversation
	for i := 0; i < chatSessionMaxShown+2; i++ {
		items = append(items, Conversation{ID: fmt.Sprintf("id-%d", i+1), Name: strings.Repeat("a", 100), CWD: "/work/project"})
	}
	more, err := browser.acceptPage(chatSessionPageJSON(t, items, "next"))
	if err != nil || more {
		t.Fatalf("display cap should finish the search: more=%v err=%v", more, err)
	}
	if len(browser.matches) != chatSessionMaxShown || !browser.more {
		t.Fatalf("unexpected bounded result set: count=%d more=%v", len(browser.matches), browser.more)
	}
	if strings.Contains(browser.render(), strings.Repeat("a", chatSessionTitleRunes+1)) || !strings.Contains(browser.render(), "first 8 matches") {
		t.Fatalf("session list was not compact or did not mark truncation: %q", browser.render())
	}
	if id, err := browser.resumeChoice("1"); err != nil || id != "id-1" {
		t.Fatalf("displayed number did not select frozen result: id=%q err=%v", id, err)
	}
}

func TestChatSessionBrowserPageCapAndFailureRemainHonest(t *testing.T) {
	browser, err := newChatSessionBrowser("rare query", "/work/project", "codex")
	if err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= chatSessionMaxPages; page++ {
		next := fmt.Sprintf("cursor-%d", page)
		if page == chatSessionMaxPages {
			next = "more-history"
		}
		more, pageErr := browser.acceptPage(chatSessionPageJSON(t, nil, next))
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		if page < chatSessionMaxPages && !more {
			t.Fatalf("page %d should continue", page)
		}
		if page == chatSessionMaxPages && more {
			t.Fatal("search should stop at its page bound")
		}
	}
	if !strings.Contains(browser.render(), "No matches in the sessions scanned so far") || !strings.Contains(browser.render(), "incomplete") {
		t.Fatalf("bounded scan must not claim no history exists: %q", browser.render())
	}
	failed := browser.fail(fmt.Errorf("app-server timeout\x1b[2J"))
	if !strings.Contains(failed, "History lookup failed: app-server timeout") || strings.Contains(failed, "\x1b") {
		t.Fatalf("failure output should be safe and visible: %q", failed)
	}
}

func TestChatSessionBrowserRejectsUnsupportedScopeAndOversizedQuery(t *testing.T) {
	if _, err := newChatSessionBrowser("", "/work/project", "claude"); err == nil {
		t.Fatal("unsupported runtime provider should be rejected")
	}
	if _, err := newChatSessionBrowser(strings.Repeat("q", chatSessionMaxQuery+1), "/work/project", "codex"); err == nil {
		t.Fatal("oversized search query should be rejected")
	}
	if _, err := newChatSessionBrowser("", "", "codex"); err == nil {
		t.Fatal("empty working directory should be rejected")
	}
}
