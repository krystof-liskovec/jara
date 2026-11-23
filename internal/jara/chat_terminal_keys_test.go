package jara

import (
	"testing"
	"time"
)

func TestFunctionKeysAcrossTerminalEncodings(t *testing.T) {
	cases := map[string]chatEditorKey{
		"\x1bOQ": chatEditorModelPicker, "\x1b[12~": chatEditorModelPicker, // xterm/tmux, VT220
		"\x1b[Q": chatEditorModelPicker, "\x1b[1Q": chatEditorModelPicker, // kitty protocol
		"\x1b[1;2Q": chatEditorModelPicker, "\x1b[12;5~": chatEditorModelPicker, "\x1b[1;1:1Q": chatEditorModelPicker,
		"\x1bOR": chatEditorReasoningPicker, "\x1b[13~": chatEditorReasoningPicker,
		"\x1b[1;5R": chatEditorReasoningPicker, "\x1b[13;2~": chatEditorReasoningPicker,
	}
	for sequence, want := range cases {
		events := newChatTerminalKeyParser(1024).feed([]byte(sequence))
		if len(events) != 1 || events[0].input == nil || events[0].input.Key != want {
			t.Errorf("%q: got %+v, want %s", sequence, events, want)
		}
	}
	for _, sequence := range []string{"\x1bOS", "\x1b[S", "\x1b[14~", "\x1b[1;2S"} {
		if events := newChatTerminalKeyParser(1024).feed([]byte(sequence)); len(events) != 1 || !events[0].diffPane {
			t.Errorf("%q must toggle the changes pane: %+v", sequence, events)
		}
	}
	for sequence, want := range map[string]int{"\x1b[15~": -chatDiffPaneScrollStep, "\x1b[17;2~": chatDiffPaneScrollStep, "\x1b[5;3~": -chatDiffPaneScrollStep} {
		if events := newChatTerminalKeyParser(1024).feed([]byte(sequence)); len(events) != 1 || events[0].paneScroll != want {
			t.Errorf("%q pane scroll: %+v", sequence, events)
		}
	}
	for _, sequence := range []string{"\x1b[24;80R", "\x1b[R", "\x1b[1;5C", "\x1b[2Q", "\x1b[5~"} {
		if chatFunctionKey(sequence) != 0 {
			t.Errorf("%q is not a function key", sequence)
		}
	}
}

// Only an Escape press may stop a running turn; other keys must not read as one.
func TestOnlyEscapeReadsAsEscape(t *testing.T) {
	// escape reports an Escape that can stop a turn.
	escape := func(events []chatTerminalKeyEvent) bool {
		for _, event := range events {
			if event.input != nil && event.input.Key == chatEditorEscape && !event.withKey {
				return true
			}
		}
		return false
	}
	for _, sequence := range []string{
		"\x1b[97;5u", "\x1b[117;5u", "\x1b[119;5:1u", "\x1b[27;5;97~", // Ctrl+A, Ctrl+U, Ctrl+W
		"\x1b[108;5u", "\x1b[127;5u", "\x1b[127;3u", // Ctrl+L, Ctrl+/Alt+Backspace
		"\x1bx", "\x1b\x7f", "\x1b[120;3u", "\x1b\x1b[D", // Alt keys
		"\x1b[I", "\x1b[O", "\x1b[?62;22c", "\x1b]11;rgb:0000/0000/0000\x1b\\", "\x1b]10;?\x07", // reports
		"\x1b[57441u", "\x1b[99;9u", // modifier keys, Super+C
	} {
		if events := newChatTerminalKeyParser(1024).feed([]byte(sequence)); escape(events) {
			t.Errorf("%q reads as Escape: %+v", sequence, events)
		}
	}
	for _, sequence := range []string{"\x1b[27u", "\x1b[27;1u", "\x1b[27;1:1u", "\x1b[27;1;27~", "\x1b\x1b"} {
		if events := newChatTerminalKeyParser(1024).feed([]byte(sequence)); !escape(events) {
			t.Errorf("%q is Escape: %+v", sequence, events)
		}
	}
	parser := newChatTerminalKeyParser(1024)
	if events := parser.feed([]byte("\x1b")); len(events) != 0 || parser.flushEscape(time.Now().Add(time.Second)) == nil {
		t.Fatal("a lone Escape press is Escape")
	}

	events := newChatTerminalKeyParser(1024).feed([]byte("\x1bk\x1b[100;5u\x1b[99;5u"))
	if len(events) != 4 || events[0].input.Key != chatEditorEscape || events[1].input.Text != "k" ||
		!events[2].leave || !events[3].interrupt {
		t.Fatalf("Escape with k, Ctrl+D, Ctrl+C = %+v", events)
	}
	if events := newChatTerminalKeyParser(1024).feed([]byte("\x1b[99;9u\x1b[97;65u")); len(events) != 1 || events[0].input.Text != "a" {
		t.Fatalf("Super+C is not text; a with Caps Lock is: %+v", events)
	}
}
