package jara

import "testing"

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
