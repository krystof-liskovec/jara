//go:build linux || darwin

package jara

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteMovesTheConversation(t *testing.T) {
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	root := t.TempDir()
	if err := saveConfig(Config{
		Remotes:  map[string]Remote{"work": {Host: "work.example"}, "backup": {Host: "backup.example", Standby: true}},
		Order:    []string{"work", "backup"},
		Bindings: map[string]Binding{root: {Remote: "work", Workspace: "w1"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chatPlace, chatMove = chatPlacement{}, nil })
	move := func(place chatPlacement, line string) (*chatTerminal, string, bool) {
		t.Helper()
		term, wire := newTestChatTerminal(t)
		term.u.persistentSession = true
		chatPlace, chatMove = place, nil
		quit, err := term.u.line(line)
		if err != nil {
			t.Fatal(err)
		}
		return term, wire.String(), quit
	}
	here := chatPlacement{root: root, session: "s1"}

	// This device to the folder's host: hand over, then run there.
	if _, wire, quit := move(here, "/remote work"); !quit || chatMove == nil || strings.Join(chatMove.args, " ") != "--run-remote" ||
		chatMove.waitFor != "s1" || !strings.Contains(wire, `"jara/release"`) {
		t.Fatalf("to the host: quit %v, move %+v, wire %s", quit, chatMove, wire)
	}
	// Another host while running here: attach the folder there first.
	if _, wire, quit := move(here, "/remote backup"); !quit || strings.Join(chatMove.args, " ") != "--remote backup" || !strings.Contains(wire, `"jara/close"`) {
		t.Fatalf("attach: quit %v, move %+v, wire %s", quit, chatMove, wire)
	}
	// The host back to this device.
	onWork := chatPlacement{root: root, remote: "work"}
	if _, wire, quit := move(onWork, "/remote here"); !quit || strings.Join(chatMove.args, " ") != "--local" || !strings.Contains(wire, `"jara/detach"`) {
		t.Fatalf("back here: quit %v, move %+v, wire %s", quit, chatMove, wire)
	}
	// Refused: host to host, the same place, an unknown name, a running turn.
	for _, tc := range []struct {
		place chatPlacement
		line  string
		busy  bool
	}{{onWork, "/remote backup", false}, {onWork, "/remote work", false}, {here, "/remote nowhere", false}, {here, "/remote work", true}} {
		term, _ := newTestChatTerminal(t)
		term.u.busy, chatPlace, chatMove = tc.busy, tc.place, nil
		if tc.busy {
			term.u.turn = "turn-1"
		}
		if quit, _ := term.u.line(tc.line); quit || chatMove != nil {
			t.Fatalf("%s from %+v was not refused", tc.line, tc.place)
		}
	}

	// The menu lists the places; choosing one closes the window to move.
	term, _, _ := move(here, "/remote")
	term.picker.sync(term.u)
	if got := pickerRows(term); got != "Move this conversation|>This device|work|backup" {
		t.Fatalf("menu %s", got)
	}
	descriptions := term.suggestions()
	if descriptions[1].Description != "runs here now" || descriptions[2].Description != "this folder's host" || descriptions[3].Description != "standby" {
		t.Fatalf("descriptions %+v", descriptions)
	}
	chatMove = nil
	done := typeChatKeys(t, term, "\x1b[B\x1b[C") // work, then → chooses
	if !done || chatMove == nil || strings.Join(chatMove.args, " ") != "--run-remote" {
		t.Fatalf("menu choice: done %v, move %+v", done, chatMove)
	}
}
