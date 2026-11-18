//go:build linux || darwin

package jara

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChatAccountsListsPicksAndTellsOfSwitches(t *testing.T) {
	term, _ := newTestChatTerminal(t)
	sent := &bytes.Buffer{}
	term.u.c = offlineChatConnection()
	term.u.c.encoder = json.NewEncoder(sent)
	screen := func() string { return plainChatScreen(term.frame(160, 50, time.Now())) }

	if _, err := term.u.line("/accounts use 1"); err != nil || !strings.Contains(screen(), "List the accounts with /accounts first.") {
		t.Fatalf("before a list: %v\n%s", err, screen())
	}
	if _, err := term.u.line("/accounts"); err != nil {
		t.Fatal(err)
	}
	out := time.Now().Add(3 * time.Hour).Unix()
	answer(t, term, sentRequests(t, sent)["jara/accounts/list"], `{"accounts":[
		{"agent":"codex","id":"chatgpt:a","label":"a@example.com (pro)","active":true},
		{"agent":"codex","id":"chatgpt:b","label":"b@example.com (plus)","exhaustedUntil":`+strconv.FormatInt(out, 10)+`},
		{"agent":"claude","id":"token:1","label":"Claude token …abcd","active":true}]}`)
	got := screen()
	for _, want := range []string{"Accounts on this device", "Codex", "[1] a@example.com (pro) · in use", "[2] b@example.com (plus) · out of usage until", "Claude Code", "[3] Claude token …abcd · in use"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	sent.Reset()
	if _, err := term.u.line("/accounts use 2"); err != nil {
		t.Fatal(err)
	}
	use, _ := sentRequests(t, sent)["jara/accounts/use"]["params"].(map[string]any)
	if use["agent"] != "codex" || use["id"] != "chatgpt:b" {
		t.Fatalf("use %v", use)
	}
	_ = term.u.event(appMessage{Method: "jara/account/switched", Params: json.RawMessage(
		`{"agent":"claude","from":"Claude token …abcd","to":"Claude token …wxyz","resetsAt":` + strconv.FormatInt(out, 10) + `}`)})
	if !strings.Contains(screen(), "Claude Code account Claude token …abcd ran out of usage (back ") || !strings.Contains(screen(), "continuing on Claude token …wxyz.") {
		t.Fatalf("switch notice:\n%s", screen())
	}
}
