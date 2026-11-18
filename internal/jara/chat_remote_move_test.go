//go:build linux || darwin

package jara

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeChatPlaces stands in for this device's and the host's sessions.
type fakeChatPlaces struct {
	calls []string // "here explicit", "here", "host" with the thread
}

func (f *fakeChatPlaces) install() {
	chatMoves.hereSession, chatMoves.hostName = "s1", "work"
	chatMoves.here = func(thread string, explicit bool) (*appConnection, remoteChatServerReady, error) {
		call := "here " + thread
		if explicit {
			call = "here explicit " + thread
		}
		f.calls = append(f.calls, call)
		return offlineChatConnection(), remoteChatServerReady{Initialized: true, CWD: "/here"}, nil
	}
	chatMoves.host = func(thread string) (*appConnection, remoteChatServerReady, error) {
		f.calls = append(f.calls, "host "+thread)
		return offlineChatConnection(), remoteChatServerReady{Initialized: true, CWD: "/host"}, nil
	}
}

func TestRemoteMovesTheConversationInPlace(t *testing.T) {
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("JARA_DATA", t.TempDir())
	root := t.TempDir()
	if err := saveConfig(Config{
		Remotes:  map[string]Remote{"work": {Host: "work.example"}, "backup": {Host: "backup.example", Standby: true}},
		Order:    []string{"work", "backup"},
		Bindings: map[string]Binding{root: {Remote: "work", Workspace: "w1"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		chatPlace, chatMove = chatPlacement{}, nil
		chatMoves.here, chatMoves.host, chatMoves.hostName, chatMoves.hereSession = nil, nil, "", ""
	})
	places := &fakeChatPlaces{}
	places.install()
	open := func(place chatPlacement) (*chatTerminal, *bytes.Buffer) {
		t.Helper()
		term, _ := newTestChatTerminal(t)
		sent := &bytes.Buffer{}
		term.u.c = offlineChatConnection()
		term.u.c.encoder = json.NewEncoder(sent) // what the old session is told
		term.u.persistentSession, term.u.thread = true, "th"
		chatPlace, chatMove, places.calls = place, nil, nil
		return term, sent
	}
	// connected waits for the move's connection and applies it.
	connected := func(term *chatTerminal) {
		t.Helper()
		select {
		case result := <-term.u.reconnectRuns:
			if err := term.u.acceptReconnect(result); err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the move never connected")
		}
	}

	// This device to the folder's host: hand over, then join the host's
	// session in the same window.
	term, sent := open(chatPlacement{root: root, session: "s1"})
	quit, err := term.u.line("/remote work")
	if err != nil || quit || chatMove != nil || !strings.Contains(sent.String(), `"jara/release"`) {
		t.Fatalf("to the host: quit %v, err %v, restart %+v, sent %s", quit, err, chatMove, sent)
	}
	connected(term)
	if strings.Join(places.calls, ",") != "host th" || chatPlace.remote != "work" || term.u.cwd != "/host" ||
		!strings.Contains(term.u.afterHistory, "now runs on work") {
		t.Fatalf("to the host: calls %q, place %+v, cwd %q, note %q", places.calls, chatPlace, term.u.cwd, term.u.afterHistory)
	}

	// The host back to this device: an explicit claim now, and later
	// reconnects claim like a dropped connection does.
	term, sent = open(chatPlacement{root: root, remote: "work"})
	if quit, _ = term.u.line("/remote here"); quit || !strings.Contains(sent.String(), `"jara/detach"`) {
		t.Fatalf("moving here: quit %v, sent %s", quit, sent)
	}
	connected(term)
	if strings.Join(places.calls, ",") != "here explicit th" || chatPlace.remote != "" || chatPlace.session != "s1" {
		t.Fatalf("back here: calls %q, place %+v", places.calls, chatPlace)
	}
	if _, _, err = term.u.reconnect("th"); err != nil || places.calls[len(places.calls)-1] != "here th" {
		t.Fatalf("later reconnects: %q %v", places.calls, err)
	}

	// Another host while running here: Jara opens again to attach it.
	term, _ = open(chatPlacement{root: root, session: "s1"})
	if quit, _ = term.u.line("/remote backup"); !quit || chatMove == nil || strings.Join(chatMove.args, " ") != "--remote backup" {
		t.Fatalf("attach: quit %v, restart %+v", quit, chatMove)
	}

	// Refused: host to host, the same place, an unknown name, a running turn.
	for _, tc := range []struct {
		place chatPlacement
		line  string
		busy  bool
	}{{chatPlacement{root: root, remote: "work"}, "/remote backup", false}, {chatPlacement{root: root, remote: "work"}, "/remote work", false},
		{chatPlacement{root: root, session: "s1"}, "/remote nowhere", false}, {chatPlacement{root: root, session: "s1"}, "/remote work", true}} {
		term, _ = open(tc.place)
		if tc.busy {
			term.u.busy, term.u.turn = true, "turn-1"
		}
		if quit, _ = term.u.line(tc.line); quit || chatMove != nil || term.u.reconnectRuns != nil {
			t.Fatalf("%s from %+v was not refused", tc.line, tc.place)
		}
	}

	// The menu lists the places; choosing one moves without closing.
	term, _ = open(chatPlacement{root: root, session: "s1"})
	if _, err = term.u.line("/remote"); err != nil {
		t.Fatal(err)
	}
	term.picker.sync(term.u)
	if got := pickerRows(term); got != "Move this conversation|>This device|work|backup" {
		t.Fatalf("menu %s", got)
	}
	if choices := term.suggestions(); choices[1].Description != "runs here now" || choices[2].Description != "this folder's host" || choices[3].Description != "standby" {
		t.Fatalf("descriptions %+v", choices)
	}
	if done := typeChatKeys(t, term, "\x1b[B\x1b[C"); done || term.u.reconnectRuns == nil { // work, then → chooses
		t.Fatalf("menu choice: done %v, moving %v", done, term.u.reconnectRuns != nil)
	}
	connected(term)
}

func TestWatchingWindowsNameWhereTheAgentRuns(t *testing.T) {
	t.Cleanup(func() { chatPlace = chatPlacement{} })
	term, _ := newTestChatTerminal(t)
	term.u.c = offlineChatConnection()
	chatPlace = chatPlacement{root: "/folder", session: "s1"}
	// Opened here while the host still runs a turn: the window watches it.
	accept := func(runner string) {
		t.Helper()
		if err := term.u.acceptReconnect(chatReconnectResult{conn: offlineChatConnection(), ready: remoteChatServerReady{Initialized: true, CWD: "/w", Runner: runner}}); err != nil {
			t.Fatal(err)
		}
	}
	accept("work")
	if line := plainChatScreen(term.frame(120, 20, time.Now())); chatPlace.remote != "work" || !strings.Contains(line, "⇄ work") {
		t.Fatalf("watching: place %+v\n%s", chatPlace, line)
	}
	// The host hands back; the window's next connection is this device's.
	accept(chatHereRunner)
	if line := plainChatScreen(term.frame(120, 20, time.Now())); chatPlace.remote != "" || chatPlace.session != "s1" || strings.Contains(line, "⇄") {
		t.Fatalf("handed back: place %+v\n%s", chatPlace, line)
	}
	// A host's own session says nothing, and the place stays as it was.
	chatPlace.remote = "work"
	accept("")
	if chatPlace.remote != "work" {
		t.Fatalf("host session changed the place: %+v", chatPlace)
	}
}
